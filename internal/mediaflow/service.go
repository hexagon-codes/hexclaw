package mediaflow

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	imagegen "github.com/hexagon-codes/ai-core/media/image"
	videogen "github.com/hexagon-codes/ai-core/media/video"
	"github.com/hexagon-codes/ai-core/media/voicechat"
	"github.com/hexagon-codes/ai-core/transport"
	"github.com/hexagon-codes/toolkit/util/idgen"
	_ "golang.org/x/image/webp"
)

type BlobStore interface {
	SaveStream(context.Context, io.Reader, string) (string, error)
	OpenReader(string) (io.ReadCloser, error)
}

// Service 使用受宿主生命周期管理的有界推进器；HTTP断连只停止等待。
type Service struct {
	router     *Router
	repo       Repository
	blobs      BlobStore
	staging    string
	client     *http.Client
	ctx        context.Context
	cancel     context.CancelFunc
	wake       chan struct{}
	wg         sync.WaitGroup
	deliveries deliveryRuntime
	codec      RecoveryCodec
	replies    replyRecovery
	downloads  sync.Map // operation ID → 单次下载取消函数
}

func NewService(parent context.Context, router *Router, repo Repository, blobs BlobStore, staging string, options ...Option) (*Service, error) {
	if parent == nil || router == nil || repo == nil || blobs == nil {
		return nil, fmt.Errorf("media dependencies are required")
	}
	if err := os.MkdirAll(staging, 0700); err != nil {
		return nil, err
	}
	if err := repo.Interrupt(parent); err != nil {
		return nil, err
	}
	if deliveryRepo, ok := repo.(DeliveryRepository); ok {
		if err := deliveryRepo.InterruptDeliveries(parent); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Service{router: router, repo: repo, blobs: blobs, staging: staging, client: &http.Client{}, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
	for _, option := range options {
		option(s)
	}
	// 崩溃前已经原子落下的字节凭据可直接恢复，绝不重新调用生成器。
	entries, err := os.ReadDir(staging)
	if err != nil {
		cancel()
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "media-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(staging, entry.Name()))
		if err != nil {
			continue
		}
		var staged Operation
		if json.Unmarshal(data, &staged) != nil || staged.ID+".json" != entry.Name() {
			continue
		}
		op, err := repo.Get(parent, staged.Owner, staged.ID)
		if err == nil && op.SaveState != "ready" {
			s.restoreAssets(op)
		}
	}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.run() }()
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.runDeliveries() }()
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.runReplyRecovery() }()
	return s, nil
}
func (s *Service) Close()          { s.cancel(); s.wg.Wait() }
func (s *Service) Router() *Router { return s.router }
func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func normalizeRequest(req *Request) error {
	if req.Origin.Owner == "" || req.Origin.Kind == "" {
		return fmt.Errorf("trusted media origin is required")
	}
	if req.Origin.RequestID == "" {
		req.Origin.RequestID = "request-" + idgen.ShortID()
	}
	if req.Origin.Ordinal == "" {
		req.Origin.Ordinal = "0"
	}
	if req.Action == "" {
		if req.Kind == "audio" {
			return fmt.Errorf("audio action must be specified")
		} else {
			req.Action = "generate"
		}
	}
	count := 0
	for _, set := range []bool{req.Image != nil, req.Video != nil, req.Speech != nil, req.Chat != nil} {
		if set {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("exactly one typed media request is required")
	}
	switch {
	case req.Kind == "image" && req.Action == "generate" && req.Image != nil:
		if strings.TrimSpace(req.Image.Prompt) == "" {
			return fmt.Errorf("image prompt must not be empty")
		}
	case req.Kind == "video" && req.Action == "generate" && req.Video != nil:
		if strings.TrimSpace(req.Video.Prompt) == "" && strings.TrimSpace(req.Video.ImageURL) == "" {
			return fmt.Errorf("video prompt or image is required")
		}
	case req.Kind == "audio" && req.Action == "synthesize" && req.Speech != nil:
		if strings.TrimSpace(req.Speech.Text) == "" {
			return fmt.Errorf("speech text must not be empty")
		}
	case req.Kind == "audio" && req.Action == "chat" && req.Chat != nil:
		if strings.TrimSpace(req.Chat.Text) == "" && req.Chat.AudioB64 == "" {
			return fmt.Errorf("audio or text is required")
		}
	default:
		return fmt.Errorf("unsupported media operation")
	}
	return nil
}

// Begin 先查同一入口意图，旧操作不受当前默认路由或启停变化影响。
func (s *Service) Begin(ctx context.Context, req Request) (*Operation, error) {
	if err := normalizeRequest(&req); err != nil {
		return nil, err
	}
	if policy := req.Selection; policy != nil && policy.Mode != "auto" {
		if len(policy.Models) == 1 { req.Model = policy.Models[0] }
		if policy.Provider != "" { req.Provider = policy.Provider }
	}
	key := digest([]string{req.Origin.Kind, req.Origin.SessionID, req.Origin.RequestID, req.Origin.Ordinal})
	intentRequest := req
	intentRequest.Origin = Origin{}
	// 路由提示和解析快照不是新生成意图；用户原文由入口摘要独立校验。
	if req.Selection != nil && req.Selection.Mode != "pinned" {
		intentRequest.Provider, intentRequest.Model = "", ""
	}
	intentRequest.Selection = nil
	intent := digest(intentRequest)
	if op, err := s.repo.Find(ctx, req.Origin.Owner, key); err == nil {
		if op.Request.Origin.RequestDigest != "" && req.Origin.RequestDigest != "" && op.Request.Origin.RequestDigest != req.Origin.RequestDigest {
			return op, ErrConflict
		}
		if op.IntentDigest != intent {
			return op, ErrConflict
		}
		op, err = s.acceptRequestOperation(ctx, func() (*Operation, error) { return op, nil })
		if err != nil {
			return op, err
		}
		s.signal()
		return op, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if req.Origin.Kind != "direct_api" {
		previous, err := s.repo.ListForRequest(ctx, req.Origin.Owner, req.Origin)
		if err != nil {
			return nil, err
		}
		for _, op := range previous {
			if op.GenerationState == "unknown" && op.Request.Kind == req.Kind && op.Request.Action == req.Action {
				return op, &UnknownOperationError{OperationID: op.ID}
			}
			if op.IntentDigest == intent && op.GenerationState == "succeeded" && op.SaveState != "ready" {
				return op, &RecoveryRequiredError{OperationID: op.ID}
			}
			if op.IntentDigest == intent && op.GenerationState == "failed" {
				return op, fmt.Errorf("media operation %s exhausted its permitted attempts", op.ID)
			}
		}
	}
	routes, err := s.router.planRoutes(req)
	if err != nil {
		return nil, err
	}
	route := routes[0]
	now := time.Now().UTC()
	op := &Operation{ID: "media-" + idgen.ShortID(), Owner: req.Origin.Owner, IdempotencyKey: key, IntentDigest: intent, ExecutionDigest: digest(struct {
		Request Request
		Route   Route
	}{req, route}), Request: req, Route: route, GenerationState: "queued", SaveState: "not_started", CreatedAt: now, UpdatedAt: now, Timings: make(map[string]int64)}
	op.CandidateRoutes = routes
	if len(routes) > 1 {
		op.ExecutionDigest = digest(struct {
			Request Request
			Routes  []Route
		}{req, routes})
	}
	if req.Chat != nil && req.Chat.AudioB64 != "" {
		path, err := s.blobs.SaveStream(ctx, base64.NewDecoder(base64.StdEncoding, strings.NewReader(req.Chat.AudioB64)), "bin")
		if err != nil {
			return nil, fmt.Errorf("persist audio input: %w", err)
		}
		chat := *req.Chat
		chat.AudioB64 = ""
		op.Request.Chat = &chat
		op.InputBlob = path
	}
	op, err = s.acceptRequestOperation(ctx, func() (*Operation, error) {
		created, err := s.repo.Create(ctx, op)
		if err == nil && created.IntentDigest != intent {
			return nil, ErrConflict
		}
		return created, err
	})
	if err != nil {
		return op, err
	}
	if op.IntentDigest != intent {
		return nil, ErrConflict
	}
	s.signal()
	return op, nil
}

func (s *Service) Read(ctx context.Context, owner, id string) (*Operation, error) {
	return s.repo.Get(ctx, owner, id)
}

func (s *Service) RequestOperations(ctx context.Context, owner string, origin Origin) ([]*Operation, error) {
	operations, err := s.repo.ListForRequest(ctx, owner, origin)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(operations, func(i, j int) bool { return operations[i].CreatedAt.Before(operations[j].CreatedAt) })
	return operations, nil
}

// ResolveAssets 从可信操作登记查回实际资产，不采用工具返回的任意路径。
func (s *Service) ResolveAssets(ctx context.Context, owner string, refs []Asset) ([]Asset, error) {
	var assets []Asset
	seen := make(map[string]bool)
	for _, ref := range refs {
		if ref.OperationID == "" || ref.ID == "" {
			continue
		}
		op, err := s.repo.Get(ctx, owner, ref.OperationID)
		if err != nil {
			return nil, err
		}
		found := false
		for _, asset := range op.Assets {
			if asset.ID == ref.ID {
				if !seen[asset.ID] {
					assets = append(assets, asset)
					seen[asset.ID] = true
				}
				found = true
				break
			}
		}
		if !found {
			return nil, ErrNotFound
		}
	}
	return assets, nil
}

func (s *Service) OpenAsset(ctx context.Context, owner string, ref Asset) (io.ReadCloser, error) {
	assets, err := s.ResolveAssets(ctx, owner, []Asset{ref})
	if err != nil {
		return nil, err
	}
	if len(assets) != 1 {
		return nil, ErrNotFound
	}
	return s.blobs.OpenReader(assets[0].Path)
}

// AuthorizePath 只核对已登记媒体资产，历史文件继续遵守原有读取合同。
func (s *Service) AuthorizePath(ctx context.Context, owner, path string) (bool, error) {
	if repo, ok := s.repo.(AssetPathRepository); ok {
		return repo.AuthorizePath(ctx, owner, path)
	}
	return false, nil
}

// Wait 不改变副作用状态；视频API可仅等待提交引用，其他入口等待完整资产。
func (s *Service) Wait(ctx context.Context, owner, id string, submittedOnly bool) (*Operation, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		op, err := s.repo.Get(ctx, owner, id)
		if err != nil {
			return nil, err
		}
		if op.Settled() || op.CancelRequested || submittedOnly && op.UpstreamID != "" {
			return op, nil
		}
		select {
		case <-ctx.Done():
			return op, ctx.Err()
		case <-s.ctx.Done():
			return op, s.ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) Resume(ctx context.Context, owner, id string) (*Operation, error) {
	op, err := s.repo.Get(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	op, err = s.acceptRequestOperation(ctx, func() (*Operation, error) { return op, nil })
	if err != nil {
		return op, err
	}
	if op.GenerationState == "succeeded" && op.SaveState != "ready" && (op.RecoveryKind == "staged_blob" || op.RecoveryKind == "remote_url") {
		op.SaveState = "saving"
		op.Error = ""
		op.ErrorCode = ""
		if err := s.repo.Update(ctx, op, op.Revision); err != nil {
			return nil, err
		}
		s.signal()
		return op, nil
	}
	if op.SaveState != "ready" {
		s.signal()
		return op, nil
	}
	if err := s.resumeDeliveries(ctx, op); err != nil {
		return op, err
	}
	s.signal()
	return op, nil
}

func (s *Service) Cancel(ctx context.Context, owner, id string) (*Operation, error) {
	op, err := s.repo.Get(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	op.CancelRequested = true
	if op.GenerationState == "queued" {
		op.GenerationState = "cancelled"
	}
	if err := s.repo.Update(ctx, op, op.Revision); err != nil {
		return nil, err
	}
	if cancel, ok := s.downloads.Load(op.ID); ok {
		cancel.(context.CancelFunc)()
	}
	return op, nil
}

func (s *Service) run() {
	const executionSlots = 2
	type completion struct {
		id              string
		revision        int64
		pollImmediately bool
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	completed := make(chan completion, executionSlots)
	inFlight := make(map[string]struct{}, executionSlots)
	deferred := make(map[string]int64)
	cursor := ""
	var workers sync.WaitGroup
	defer workers.Wait()
	for {
		if s.ctx.Err() != nil {
			return
		}
		for len(inFlight) < executionSlots {
			pager, paged := s.repo.(interface {
				PendingAfter(context.Context, string, int) ([]*Operation, error)
			})
			var ops []*Operation
			var err error
			if paged {
				ops, err = pager.PendingAfter(s.ctx, cursor, 16)
			} else {
				ops, err = s.repo.Pending(s.ctx, 16)
			}
			if err != nil || len(ops) == 0 {
				cursor = ""
				break
			}
			if err == nil {
				for _, op := range ops {
					if s.ctx.Err() != nil {
						return
					}
					if len(inFlight) == executionSlots {
						break
					}
					cursor = op.ID
					if _, active := inFlight[op.ID]; active {
						continue
					}
					if revision, waiting := deferred[op.ID]; waiting && revision == op.Revision {
						continue
					}
					delete(deferred, op.ID)
					// 调度者独占操作集合，运行中的submitting不能被当作崩溃重领。
					inFlight[op.ID] = struct{}{}
					workers.Add(1)
					go func(op *Operation) {
						defer workers.Done()
						initial := op.GenerationState
						s.advance(op)
						completed <- completion{id: op.ID, revision: op.Revision, pollImmediately: initial == "queued" && (op.GenerationState == "running" || op.GenerationState == "queued")}
					}(op)
				}
			}
			if !paged || len(ops) < 16 {
				cursor = ""
				break
			}
		}
		select {
		case <-s.ctx.Done():
			return
		case done := <-completed:
			delete(inFlight, done.id)
			if !done.pollImmediately {
				// 完成通知仅释放槽位，不能把视频查询或失败恢复变成忙循环。
				deferred[done.id] = done.revision
			}
		case <-s.wake:
		case <-ticker.C:
			clear(deferred)
		}
	}
}

func (s *Service) advance(op *Operation) {
	if op.Timings == nil {
		op.Timings = make(map[string]int64)
	}
	if op.GenerationState == "succeeded" {
		s.recoverOperation(op)
		return
	}
	if op.GenerationState == "submitting" {
		if s.restoreAssets(op) {
			s.signal()
		} else {
			if _, err := s.readStaging(op); errors.Is(err, os.ErrNotExist) {
				s.fail(op, "unknown", "upstream_unknown", "generation interrupted without a durable upstream reference")
			}
		}
		return
	}
	if op.GenerationState == "running" {
		s.pollVideo(op)
		return
	}
	if op.GenerationState != "queued" || op.CancelRequested {
		return
	}
	op.GenerationState = "submitting"
	if s.repo.Update(s.ctx, op, op.Revision) != nil {
		return
	}
	ctx := transport.WithOperationSafety(s.ctx, transport.OperationSafetyNonIdempotent)
	started := time.Now()
	generationID := op.ID
	if op.AttemptIndex > 0 {
		generationID = fmt.Sprintf("%s:%d", op.ID, op.AttemptIndex)
	}
	snapshot := s.router.Current()
	pc, hasCredential := snapshot.providers[op.Route.ProviderID]
	if op.Route.Protocol != "tts" && (!hasCredential || pc.APIKey == "") {
		s.fail(op, "failed", "capability_unavailable", "original provider credentials are unavailable")
		return
	}
	var err error
	switch op.Route.Protocol {
	case "openai-image":
		req := *op.Request.Image
		req.Model = op.Route.Model
		req.IdempotencyKey = generationID
		var result *imagegen.Result
		result, err = imagegen.NewOpenAICompatible(op.Route.ProviderID, pc.APIKey, op.Route.Endpoint, []string{op.Route.Model}).Generate(ctx, req)
		op.Timings["generation"] = time.Since(started).Milliseconds()
		if err == nil {
			s.recordAttempt(op, started, "succeeded", "")
			s.saveImages(op, result)
			return
		}
	case "zhipu-video", "openai-video-compatible":
		req := *op.Request.Video
		req.Model = op.Route.Model
		req.IdempotencyKey = generationID
		var id string
		provider := videoProvider(op.Route, pc.APIKey)
		if provider == nil {
			s.fail(op, "failed", "resume_blocked", "original video protocol is unavailable")
			return
		}
		id, err = provider.Submit(ctx, req)
		if err == nil {
			s.recordAttempt(op, started, "submitted", "")
			op.UpstreamID = id
			op.GenerationState = "running"
			op.Timings["submit"] = time.Since(started).Milliseconds()
			// 数据库暂时不可写时，原任务引用仍可由同一推进器和重启恢复查回。
			_ = s.stageOperation(op)
			if s.repo.Update(s.ctx, op, op.Revision) == nil {
				s.signal()
			}
			return
		}
	case "tts":
		var result *SpeechOutcome
		result, err = s.synthesize(ctx, op, snapshot)
		if result != nil {
			op.Result = speechResultJSON(result)
			op.ActualProviderID = result.ProviderID
		}
		op.Timings["generation"] = time.Since(started).Milliseconds()
		if err == nil {
			s.saveSpeech(op, result)
			return
		}
	case "openai-voicechat":
		req := *op.Request.Chat
		req.Model = op.Route.Model
		if op.InputBlob != "" {
			reader, openErr := s.blobs.OpenReader(op.InputBlob)
			if openErr != nil {
				s.fail(op, "failed", "persist_failed", "original audio input is unavailable")
				return
			}
			data, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if errors.Join(readErr, closeErr) != nil {
				s.fail(op, "failed", "persist_failed", "original audio input could not be read")
				return
			}
			req.AudioB64 = base64.StdEncoding.EncodeToString(data)
		}
		var result *voicechat.Result
		result, err = voicechat.NewOpenAIVoiceChat(pc.APIKey, op.Route.Endpoint).Chat(ctx, req)
		op.Timings["generation"] = time.Since(started).Milliseconds()
		if err == nil {
			s.saveChat(op, result)
			return
		}
	default:
		err = fmt.Errorf("original media protocol is unavailable")
	}
	state, code := "unknown", "upstream_unknown"
	if definitiveMediaFailure(err) {
		state, code = "failed", "upstream_failed"
	}
	s.recordAttempt(op, started, state, code)
	if state == "failed" && s.failover(op, err) {
		return
	}
	// 只记录可公开的错误类别，供应商响应可能包含凭据或临时签名URL。
	detail := "media provider request did not complete"
	var providerError *transport.ProviderError
	if errors.As(err, &providerError) && providerError.StatusCode > 0 {
		detail = fmt.Sprintf("media provider request did not complete (HTTP %d)", providerError.StatusCode)
	}
	s.fail(op, state, code, detail)
}

func (s *Service) fail(op *Operation, state, code, detail string) {
	op.GenerationState = state
	op.ErrorCode = code
	op.Error = detail
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 5*time.Second)
	defer cancel()
	_ = s.repo.Update(ctx, op, op.Revision)
}

func (s *Service) pollVideo(op *Operation) {
	pc, ok := s.router.Current().providers[op.Route.ProviderID]
	if !ok || pc.APIKey == "" {
		return
	}
	provider := videoProvider(op.Route, pc.APIKey)
	if provider == nil {
		return
	}
	status, err := provider.Poll(s.ctx, op.UpstreamID)
	if err != nil {
		return
	}
	status.TaskID = op.UpstreamID
	status.Provider = op.Route.ProviderID
	status.Model = op.Route.Model
	op.Result, _ = json.Marshal(status)
	if !status.Done {
		_ = s.repo.Update(s.ctx, op, op.Revision)
		return
	}
	if status.Error != "" || status.State == "failed" || status.Status == "failed" {
		s.fail(op, "failed", "upstream_failed", "video generation failed")
		return
	}
	if status.State == "cancelled" || status.Status == "cancelled" {
		s.fail(op, "cancelled", "cancelled", "video generation was cancelled by the provider")
		return
	}
	s.saveVideo(op, status)
}

func (s *Service) saveVideo(op *Operation, status videogen.TaskStatus) {
	op.GenerationState = "succeeded"
	op.ActualProviderID = op.Route.ProviderID
	publicStatus := status
	publicStatus.VideoURL, publicStatus.CoverURL = "", ""
	op.Result, _ = json.Marshal(publicStatus)
	s.captureRecovery(op, recoveryResult{Video: &status})
	outputs := []stagedOutput{{Kind: "video", Role: "primary", Index: 0}}
	if status.CoverURL != "" {
		outputs = append(outputs, stagedOutput{Kind: "image", Role: "cover", Index: 1})
	}
	if err := s.stageOperation(op, outputs...); err != nil {
		s.saveFailure(op, err)
		return
	}
	previous := op.Assets
	op.Assets = nil
	asset, err := s.videoOutput(op, previous, outputs[0], status.VideoURL)
	if err != nil {
		s.saveFailure(op, err)
		return
	}
	op.Assets = append(op.Assets, asset)
	status.VideoFilePath = asset.Path
	status.VideoURL = ""
	if status.CoverURL != "" {
		if cover, err := s.videoOutput(op, previous, outputs[1], status.CoverURL); err == nil {
			op.Assets = append(op.Assets, cover)
			status.CoverFilePath = cover.Path
		} else {
			op.SaveState = "partial"
			op.ErrorCode, op.Error = "persist_failed", "generated video cover could not be saved"
		}
		status.CoverURL = ""
	}
	op.Result, _ = json.Marshal(status)
	s.finalizeAssets(op)
}

func (s *Service) videoOutput(op *Operation, previous []Asset, output stagedOutput, url string) (Asset, error) {
	for _, asset := range previous {
		if asset.Index == output.Index && asset.Role == output.Role && s.assetReadable(asset) {
			return asset, nil
		}
	}
	if asset, err := s.savedOutput(op, output); err == nil {
		return asset, nil
	}
	return s.saveURL(op, url, output.Kind, output.Role, output.Index)
}

func (s *Service) saveImages(op *Operation, result *imagegen.Result) {
	op.GenerationState = "succeeded"
	op.ActualProviderID = op.Route.ProviderID
	if result == nil || len(result.Images) == 0 {
		s.saveFailure(op, fmt.Errorf("provider returned no images"))
		return
	}
	copyResult := *result
	copyResult.Images = append([]imagegen.Image(nil), result.Images...)
	remote := false
	for i := range copyResult.Images {
		copyResult.Images[i].B64JSON = ""
		if copyResult.Images[i].URL != "" {
			remote = true
		}
	}
	if remote {
		s.captureRecovery(op, recoveryResult{Image: &copyResult})
	}
	publicResult := copyResult
	publicResult.Images = append([]imagegen.Image(nil), copyResult.Images...)
	for i := range publicResult.Images {
		publicResult.Images[i].URL = ""
	}
	publicResult.Provider, publicResult.Model = op.Route.ProviderID, op.Route.Model
	op.Result, _ = json.Marshal(publicResult)
	op.SaveState = "saving"
	if op.RecoveryKind != "remote_url" {
		op.RecoveryKind, op.RecoveryRef = "staged_blob", op.ID+".json"
	}
	outputs := make([]stagedOutput, len(result.Images))
	for i := range outputs {
		outputs[i] = stagedOutput{Kind: "image", Role: "primary", Index: i}
	}
	if err := s.stageOperation(op, outputs...); err != nil {
		s.saveFailure(op, err)
		return
	}
	var failures []error
	previous := op.Assets
	op.Assets = nil
	for i := range result.Images {
		img := &result.Images[i]
		var asset Asset
		var err error
		reused := false
		for _, saved := range previous {
			if saved.Index == i && saved.Role == "primary" && s.assetReadable(saved) {
				asset = saved
				reused = true
				break
			}
		}
		if reused {
			img.FilePath = asset.Path
		} else if recovered, recoverErr := s.savedOutput(op, outputs[i]); recoverErr == nil {
			asset = recovered
		} else if img.B64JSON != "" {
			asset, err = s.saveReader(op, base64.NewDecoder(base64.StdEncoding, strings.NewReader(img.B64JSON)), "image", "primary", i)
		} else if img.URL != "" {
			asset, err = s.saveURL(op, img.URL, "image", "primary", i)
		} else {
			err = fmt.Errorf("image output is empty")
		}
		img.B64JSON = ""
		img.URL = ""
		if err != nil {
			failures = append(failures, err)
			continue
		}
		op.Assets = append(op.Assets, asset)
		img.FilePath = asset.Path
	}
	result.Provider = op.Route.ProviderID
	result.Model = op.Route.Model
	op.Result, _ = json.Marshal(result)
	if len(failures) > 0 {
		op.SaveState = "partial"
		if len(op.Assets) == 0 {
			op.SaveState = "failed"
		}
		op.ErrorCode = "persist_failed"
		op.Error = "one or more generated images could not be saved"
	}
	if op.Request.Image != nil && op.Request.Image.N > len(result.Images) {
		op.SaveState = "partial"
		op.ErrorCode = "upstream_failed"
		op.Error = "provider returned fewer images than requested"
	}
	s.finalizeAssets(op)
}

func (s *Service) saveSpeech(op *Operation, outcome *SpeechOutcome) {
	op.GenerationState = "succeeded"
	if outcome != nil {
		op.ActualProviderID = outcome.ProviderID
	}
	if outcome == nil || outcome.Result == nil {
		s.saveFailure(op, fmt.Errorf("provider returned no audio"))
		return
	}
	result := outcome.Result
	asset, err := s.saveReader(op, bytes.NewReader(result.Audio), "audio", "primary", 0)
	if err != nil {
		s.saveFailure(op, err)
		return
	}
	op.Assets = []Asset{asset}
	op.Result = speechResultJSON(outcome)
	s.finalizeAssets(op)
}
func (s *Service) saveChat(op *Operation, result *voicechat.Result) {
	op.GenerationState = "succeeded"
	op.ActualProviderID = op.Route.ProviderID
	if result == nil {
		s.saveFailure(op, fmt.Errorf("provider returned no audio"))
		return
	}
	publicResult := *result
	publicResult.ResponseB64 = ""
	op.Result, _ = json.Marshal(publicResult)
	asset, err := s.saveReader(op, base64.NewDecoder(base64.StdEncoding, strings.NewReader(result.ResponseB64)), "audio", "primary", 0)
	if err != nil {
		s.saveFailure(op, err)
		return
	}
	result.ResponseB64 = ""
	op.Assets = []Asset{asset}
	op.Result, _ = json.Marshal(result)
	s.finalizeAssets(op)
}

func (s *Service) saveFailure(op *Operation, _ error) {
	op.SaveState = "failed"
	op.ErrorCode = "persist_failed"
	op.Error = "generated media could not be saved"
	if op.RecoveryKind != "remote_url" && op.RecoveryKind != "staged_blob" {
		op.RecoveryKind = "unrecoverable"
	}
	s.finalizeAssets(op)
}

func (s *Service) finalizeAssets(op *Operation) {
	if op.SaveState != "partial" && op.SaveState != "failed" {
		op.SaveState = "ready"
	}
	op.CompletionRevision = op.Revision + 1
	if len(op.Assets) > 0 {
		if op.RecoveryKind != "remote_url" {
			op.RecoveryKind = "staged_blob"
			op.RecoveryRef = op.ID + ".json"
		}
		err := s.stageOperation(op)
		if err != nil {
			op.SaveState = "failed"
			op.ErrorCode = "persist_failed"
			op.Error = "media staging receipt could not be saved"
		}
	}
	if err := s.repo.Update(s.ctx, op, op.Revision); errors.Is(err, ErrConflict) {
		if current, readErr := s.repo.Get(s.ctx, op.Owner, op.ID); readErr == nil {
			current.Assets = op.Assets
			current.ActualProviderID = op.ActualProviderID
			current.Result = op.Result
			current.GenerationState = op.GenerationState
			current.SaveState = op.SaveState
			current.RecoveryKind = op.RecoveryKind
			current.RecoveryRef = op.RecoveryRef
			current.ErrorCode = op.ErrorCode
			current.Error = op.Error
			current.CompletionRevision = current.Revision + 1
			_ = s.repo.Update(s.ctx, current, current.Revision)
		}
	}
}

func (s *Service) writeStaging(name string, data []byte) error {
	f, err := os.CreateTemp(s.staging, ".media-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.staging, name))
}
func (s *Service) saveURL(op *Operation, url, kind, role string, index int) (Asset, error) {
	if url == "" {
		return Asset{}, fmt.Errorf("media URL is empty")
	}
	// 下载不拥有生成副作用；超时或主动停止只释放本次读取，保留原恢复来源。
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Minute)
	s.downloads.Store(op.ID, context.CancelFunc(cancel))
	defer func() { s.downloads.Delete(op.ID); cancel() }()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Asset{}, err
	}
	response, err := s.client.Do(req)
	if err != nil {
		return Asset{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Asset{}, fmt.Errorf("media download returned %d", response.StatusCode)
	}
	return s.saveReader(op, response.Body, kind, role, index)
}

func (s *Service) saveReader(op *Operation, input io.Reader, kind, role string, index int) (Asset, error) {
	op.SaveState = "saving"
	if op.RecoveryKind != "remote_url" {
		op.RecoveryKind, op.RecoveryRef = "staged_blob", op.ID+".json"
	}
	output := stagedOutput{Kind: kind, Role: role, Index: index}
	if err := s.stageOutput(op, input, output); err != nil {
		return Asset{}, err
	}
	return s.savedOutput(op, output)
}

func (s *Service) saveBlobReader(op *Operation, input io.Reader, kind, role string, index int) (Asset, error) {
	started := time.Now()
	reader := bufio.NewReader(input)
	header, err := reader.Peek(512)
	if err != nil && err != io.EOF {
		return Asset{}, err
	}
	mime := http.DetectContentType(header)
	if len(header) > 1 && header[0] == 0xff && header[1]&0xe0 == 0xe0 && kind == "audio" {
		mime = "audio/mpeg"
	}
	if bytes.HasPrefix(header, []byte("fLaC")) {
		mime = "audio/flac"
	}
	ext := ""
	switch mime {
	case "image/png":
		ext = "png"
	case "image/jpeg":
		ext = "jpg"
	case "image/gif":
		ext = "gif"
	case "image/webp":
		ext = "webp"
	case "video/mp4":
		ext = "mp4"
	case "video/webm":
		ext = "webm"
	case "audio/mpeg":
		ext = "mp3"
	case "audio/wave", "audio/x-wav":
		ext = "wav"
		mime = "audio/wav"
	case "audio/ogg", "application/ogg":
		ext = "ogg"
		mime = "audio/ogg"
	case "audio/flac":
		ext = "flac"
	}
	if ext == "" || !strings.HasPrefix(mime, kind+"/") {
		return Asset{}, fmt.Errorf("generated %s has an unsupported or invalid media format", kind)
	}
	path, err := s.blobs.SaveStream(s.ctx, reader, ext)
	if err != nil {
		return Asset{}, err
	}
	r, err := s.blobs.OpenReader(path)
	if err != nil {
		return Asset{}, err
	}
	h := sha256.New()
	size, err := io.Copy(h, r)
	closeErr := r.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return Asset{}, err
	}
	asset := Asset{Index: index, OperationID: op.ID, Kind: kind, Role: role, Path: path, MIME: mime, Size: size, Digest: hex.EncodeToString(h.Sum(nil)), ParentAssetID: op.Request.ParentAssetID}
	asset.ID = "asset-" + digest([]string{op.ID, fmt.Sprint(index), asset.Digest})
	if kind == "image" {
		r, err := s.blobs.OpenReader(path)
		if err != nil {
			return Asset{}, err
		}
		decoded, _, decodeErr := image.Decode(r)
		closeErr := r.Close()
		if err = errors.Join(decodeErr, closeErr); err != nil {
			return Asset{}, err
		}
		asset.Width, asset.Height = decoded.Bounds().Dx(), decoded.Bounds().Dy()
	}
	op.Timings["save"] += time.Since(started).Milliseconds()
	return asset, nil
}
