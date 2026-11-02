// Package externaltools 将真实外部派发、结果文件与原任务归属接入同一持久账本。
package externaltools

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
	"github.com/hexagon-codes/toolkit/blobstore"
	"github.com/hexagon-codes/toolkit/util/idgen"
)

type Service struct {
	ctx   context.Context
	repo  storage.ExternalToolExecutionRepository
	blobs *blobstore.Store
	mu    sync.Mutex
}

// New 的生命周期必须属于应用；HTTP连接取消不取消最终回执保存。
func New(ctx context.Context, repo storage.ExternalToolExecutionRepository, blobs *blobstore.Store) *Service {
	return &Service{ctx: ctx, repo: repo, blobs: blobs}
}

type Intent struct {
	Kind, ToolName, ServerName, TargetID, LogicalSuffix string
	Input                                               any
	ModelExchangeID                                     string
	ModelExchangeOrdinal                                uint64
	UpstreamCallID                                      string
	ActionIndex                                         int
	BrowserGeneration                                   uint64
	SafeRetry                                           bool
}

type Attempt struct{ Record storage.ExternalToolExecution }

var ErrPolicyChanged = errors.New("execution policy changed before dispatch")

type Artifact struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	MIME    string `json:"mime"`
	Path    string `json:"path"`
	Digest  string `json:"digest"`
	Size    int64  `json:"size"`
	Deliver bool   `json:"deliver"`
	// 源文件身份只在应用私有账本内保存，不进入公共文件引用。
	SourcePath        string `json:"source_path,omitempty"`
	SourceExecutionID string `json:"source_execution_id,omitempty"`
}

type manifest struct {
	Artifacts []Artifact     `json:"artifacts,omitempty"`
	Result    map[string]any `json:"result,omitempty"`
}

// ArtifactRegistrationError 保留已保存字节的待登记身份；不能当作已交付附件。
type ArtifactRegistrationError struct {
	Artifact Artifact
	Err      error
}

func (e *ArtifactRegistrationError) Error() string {
	return "external artifact bytes were saved but ownership registration failed"
}
func (e *ArtifactRegistrationError) Unwrap() error { return e.Err }

func artifactReference(a Artifact) map[string]any {
	reference := map[string]any{"id": a.ID, "artifact_id": a.ID, "name": a.Name, "display_name": a.Name, "mime": a.MIME, "path": a.Path, "digest": a.Digest, "size": a.Size, "url": "/api/v1/files/generated/" + a.Path}
	if a.SourceExecutionID != "" {
		reference["source_execution_id"] = a.SourceExecutionID
	}
	return reference
}

func (s *Service) cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(s.ctx), 10*time.Second)
}

// Begin 对同一可信逻辑调用读回原尝试；只有显式安全重试才允许增加attempt。
func (s *Service) Begin(ctx context.Context, intent Intent, retry bool) (context.Context, *Attempt, error) {
	inv := skill.CurrentInvocation(ctx)
	if inv.AuthenticatedUserID == "" || (inv.RequestID == "" && skill.SystemDispatchTaskRef(ctx) == "") || inv.ToolCallID == "" {
		return ctx, nil, errors.New("external execution requires trusted owner, request and tool call identity")
	}
	input, err := json.Marshal(intent.Input)
	if err != nil {
		return ctx, nil, fmt.Errorf("encode external execution input: %w", err)
	}
	digest := sha256.Sum256(input)
	keyInput, _ := json.Marshal([]string{inv.AuthenticatedUserID, inv.RequestID, skill.SystemDispatchTaskRef(ctx), inv.ToolCallID, intent.Kind, intent.LogicalSuffix})
	key := sha256.Sum256(keyInput)
	policy := skill.CurrentExecutionPolicy(ctx)
	record := storage.ExternalToolExecution{
		ID: idgen.ShortID(), OwnerID: inv.AuthenticatedUserID, SessionID: inv.SessionID, RequestID: inv.RequestID,
		ToolCallID: inv.ToolCallID, AgentName: inv.RoutedAgentName, DispatchTaskRef: skill.SystemDispatchTaskRef(ctx),
		Kind: intent.Kind, LogicalCallKey: hex.EncodeToString(key[:]), Attempt: 1,
		ParentExecutionID: skill.ExternalExecutionID(ctx), ModelExchangeID: intent.ModelExchangeID,
		ModelExchangeOrdinal: intent.ModelExchangeOrdinal, UpstreamCallID: intent.UpstreamCallID, ActionIndex: intent.ActionIndex,
		ToolName: intent.ToolName, ServerName: intent.ServerName, TargetID: intent.TargetID, InputDigest: hex.EncodeToString(digest[:]),
		BrowserGeneration: intent.BrowserGeneration, PolicyProfile: policy.Profile, PolicyRevision: policy.Revision,
	}
	previous, err := s.repo.Latest(ctx, record.OwnerID, record.Kind, record.LogicalCallKey)
	if err == nil {
		if previous.InputDigest != record.InputDigest {
			return ctx, nil, storage.ErrExternalExecutionConflict
		}
		if !retry && !(previous.Phase == storage.ExternalExecutionObserved && previous.Reason == "policy_changed_before_dispatch") {
			return skill.WithExternalExecution(ctx, previous.ID), &Attempt{Record: previous}, nil
		}
		if (previous.Phase == storage.ExternalExecutionDispatched || previous.Phase == storage.ExternalExecutionUnknown) && !intent.SafeRetry {
			return ctx, nil, errors.New("original external execution is unresolved; reconcile it before retrying")
		}
		record.Attempt = previous.Attempt + 1
	} else if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, storage.ErrNotFound) {
		return ctx, nil, err
	}
	// 同一已接纳请求的未决动作不能仅换模型tool-call身份再次派发。
	// 已知完成的两次合法相同输入、以及独立新请求均不在此合并。
	if !intent.SafeRetry {
		match := &storage.ExternalToolExecutionIntentFilter{
			RequestID: record.RequestID, DispatchTaskRef: record.DispatchTaskRef,
			ToolName: record.ToolName, ServerName: record.ServerName, TargetID: record.TargetID, InputDigest: record.InputDigest,
		}
		for _, phase := range []string{storage.ExternalExecutionDispatched, storage.ExternalExecutionUnknown} {
			rows, listErr := s.repo.List(ctx, storage.ExternalToolExecutionFilter{OwnerID: record.OwnerID, Kind: record.Kind, Phase: phase, Intent: match, Limit: 1})
			if listErr != nil {
				return ctx, nil, listErr
			}
			if len(rows) > 0 {
				pending := rows[0]
				return skill.WithExternalExecution(ctx, pending.ID), &Attempt{Record: pending}, nil
			}
		}
	}
	record, err = s.repo.Prepare(ctx, record)
	if err != nil {
		return ctx, nil, err
	}
	return skill.WithExternalExecution(ctx, record.ID), &Attempt{Record: record}, nil
}

// Dispatch 在任何外部动作之前提交；旧已派发身份只能查证，不能再次执行。
func (s *Service) Dispatch(ctx context.Context, a *Attempt) error {
	if a.Record.Phase != storage.ExternalExecutionPrepared {
		return errors.New("external execution already dispatched; use its original receipt")
	}
	policy := skill.CurrentExecutionPolicy(ctx)
	if policy.Profile != a.Record.PolicyProfile || policy.Revision != a.Record.PolicyRevision {
		return errors.Join(ErrPolicyChanged, s.Finish(a, storage.ExternalExecutionObserved, "failed", "policy_changed_before_dispatch", nil, false))
	}
	r, err := s.repo.Update(ctx, a.Record.OwnerID, a.Record.ID, a.Record.Revision, storage.ExternalToolExecutionUpdate{Phase: storage.ExternalExecutionDispatched})
	if err == nil {
		a.Record = r
	}
	return err
}

// Finish 只投影真实结果元数据；原始参数、认证和文件字节均不进入清单。
func (s *Service) Finish(a *Attempt, phase, outcome, reason string, result map[string]any, accepted bool) error {
	ctx, cancel := s.cleanupContext()
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.repo.Get(ctx, a.Record.OwnerID, a.Record.ID)
	if err != nil {
		return err
	}
	var m manifest
	if len(r.ResultManifestJSON) > 0 {
		if err = json.Unmarshal(r.ResultManifestJSON, &m); err != nil {
			return err
		}
	}
	m.Result = result
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	r, err = s.repo.Update(ctx, r.OwnerID, r.ID, r.Revision, storage.ExternalToolExecutionUpdate{Phase: phase, Outcome: outcome, Reason: reason, ResultManifestJSON: data, AcceptedResponse: accepted})
	if err == nil {
		a.Record = r
	}
	return err
}

type countingWriter struct {
	io.Writer
	n int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.n += int64(n)
	return n, err
}

// SaveArtifact 先保存字节，再将引用提交到本次执行；未登记不能声称文件已经交付。
func (s *Service) SaveArtifact(ctx context.Context, name, contentType string, input io.Reader) (map[string]any, error) {
	return s.saveArtifact(ctx, name, contentType, input, true, "")
}

// SaveArtifactFromPath 记录应用已授权打开的实际源文件，供同任务读取时引用已有制品。
// sourcePath 不接受 MCP 响应中的自报身份；调用方先完成路径授权与文件打开。
func (s *Service) SaveArtifactFromPath(ctx context.Context, sourcePath, name, contentType string, input io.Reader) (map[string]any, error) {
	canonical, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return nil, err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return nil, err
	}
	return s.saveArtifact(ctx, name, contentType, input, true, canonical)
}

// SaveObservation 保留模型实际观察的身份，不把过程截图作为用户结果附件。
func (s *Service) SaveObservation(ctx context.Context, name, contentType string, input io.Reader) (map[string]any, error) {
	return s.saveArtifact(ctx, name, contentType, input, false, "")
}

func (s *Service) saveArtifact(ctx context.Context, name, contentType string, input io.Reader, deliver bool, sourcePath string) (map[string]any, error) {
	inv := skill.CurrentInvocation(ctx)
	executionID := skill.ExternalExecutionID(ctx)
	if executionID == "" || inv.AuthenticatedUserID == "" || s.blobs == nil {
		return nil, errors.New("artifact execution identity or storage is unavailable")
	}
	hash := sha256.New()
	count := &countingWriter{Writer: hash}
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	if ext == "" {
		ext = "bin"
	}
	saveCtx, saveCancel := s.cleanupContext()
	defer saveCancel()
	var path string
	var err error
	if deliver {
		path, err = s.blobs.SaveStreamAllowEmpty(saveCtx, io.TeeReader(input, count), ext)
	} else {
		path, err = s.blobs.SaveStream(saveCtx, io.TeeReader(input, count), ext)
	}
	if err != nil {
		return nil, err
	}
	if contentType == "" {
		contentType = mime.TypeByExtension("." + ext)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	artifact := Artifact{ID: idgen.ShortID(), Name: filepath.Base(name), MIME: contentType, Path: path, Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Size: count.n, Deliver: deliver}
	if sourcePath != "" {
		artifact.SourcePath, artifact.SourceExecutionID = sourcePath, executionID
	}
	finalCtx, cancel := s.cleanupContext()
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.repo.Get(finalCtx, inv.AuthenticatedUserID, executionID)
	if err != nil {
		return artifactReference(artifact), &ArtifactRegistrationError{Artifact: artifact, Err: err}
	}
	var m manifest
	if len(r.ResultManifestJSON) > 0 {
		if err = json.Unmarshal(r.ResultManifestJSON, &m); err != nil {
			return artifactReference(artifact), &ArtifactRegistrationError{Artifact: artifact, Err: err}
		}
	}
	m.Artifacts = append(m.Artifacts, artifact)
	data, _ := json.Marshal(m)
	_, err = s.repo.Update(finalCtx, r.OwnerID, r.ID, r.Revision, storage.ExternalToolExecutionUpdate{Phase: r.Phase, ResultManifestJSON: data})
	if err != nil {
		return artifactReference(artifact), &ArtifactRegistrationError{Artifact: artifact, Err: err}
	}
	return artifactReference(artifact), nil
}

// ReferenceArtifactFromPath 将同 owner、同请求中已交付且源路径和实际字节都相符的文件
// 引用到当前执行。未命中时不写入，input 恢复到开头供调用方按原流程保存新文件。
func (s *Service) ReferenceArtifactFromPath(ctx context.Context, sourcePath, name, contentType string, input io.ReadSeeker) (map[string]any, bool, error) {
	inv := skill.CurrentInvocation(ctx)
	executionID := skill.ExternalExecutionID(ctx)
	if executionID == "" || inv.AuthenticatedUserID == "" || s.blobs == nil {
		return nil, false, errors.New("artifact execution identity or storage is unavailable")
	}
	if inv.RequestID == "" {
		return nil, false, nil
	}
	canonical, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return nil, false, err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return nil, false, err
	}
	hash := sha256.New()
	size, readErr := io.Copy(hash, input)
	_, seekErr := input.Seek(0, io.SeekStart)
	if err = errors.Join(readErr, seekErr); err != nil {
		return nil, false, err
	}
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(name))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	finalCtx, cancel := s.cleanupContext()
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	filter := storage.ExternalToolExecutionFilter{OwnerID: inv.AuthenticatedUserID, RequestID: inv.RequestID, Limit: 1000}
	for {
		rows, err := s.repo.List(finalCtx, filter)
		if err != nil {
			return nil, false, err
		}
		for _, source := range rows {
			var sourceManifest manifest
			if len(source.ResultManifestJSON) == 0 {
				continue
			}
			if err := json.Unmarshal(source.ResultManifestJSON, &sourceManifest); err != nil {
				return nil, false, err
			}
			for _, artifact := range sourceManifest.Artifacts {
				if !artifact.Deliver || artifact.SourcePath != canonical || artifact.Name != filepath.Base(name) || artifact.MIME != contentType || artifact.Size != size || artifact.Digest != digest {
					continue
				}
				if artifact.SourceExecutionID == "" {
					artifact.SourceExecutionID = source.ID
				}
				current, err := s.repo.Get(finalCtx, inv.AuthenticatedUserID, executionID)
				if err != nil {
					return artifactReference(artifact), false, &ArtifactRegistrationError{Artifact: artifact, Err: err}
				}
				if current.RequestID != inv.RequestID {
					return nil, false, errors.New("artifact reference does not belong to the current request")
				}
				var currentManifest manifest
				if len(current.ResultManifestJSON) > 0 {
					if err := json.Unmarshal(current.ResultManifestJSON, &currentManifest); err != nil {
						return artifactReference(artifact), false, &ArtifactRegistrationError{Artifact: artifact, Err: err}
					}
				}
				for _, existing := range currentManifest.Artifacts {
					if existing.ID == artifact.ID {
						return artifactReference(artifact), true, nil
					}
				}
				currentManifest.Artifacts = append(currentManifest.Artifacts, artifact)
				data, err := json.Marshal(currentManifest)
				if err == nil {
					_, err = s.repo.Update(finalCtx, current.OwnerID, current.ID, current.Revision, storage.ExternalToolExecutionUpdate{Phase: current.Phase, ResultManifestJSON: data})
				}
				if err != nil {
					return artifactReference(artifact), false, &ArtifactRegistrationError{Artifact: artifact, Err: err}
				}
				return artifactReference(artifact), true, nil
			}
		}
		if len(rows) < filter.Limit {
			return nil, false, nil
		}
		filter.Offset += len(rows)
	}
}

func (s *Service) AuthorizePath(ctx context.Context, owner, path string) (bool, error) {
	r, err := s.repo.FindArtifact(ctx, owner, path)
	if err == nil {
		return r != nil, nil
	}
	if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, storage.ErrNotFound) {
		return false, err
	}
	managed, err := s.repo.HasArtifact(ctx, path)
	if err != nil {
		return false, err
	}
	if managed {
		return false, errors.New("external artifact does not belong to this owner")
	}
	return false, nil
}

// Receipt 不重新派发，供原调用恢复与未知结果查证使用。
func (s *Service) Receipt(ctx context.Context, owner, id string) (storage.ExternalToolExecution, error) {
	return s.repo.Get(ctx, owner, id)
}

// ResolveReply 与现有完成器合并，消息持久化使用同一事务，不另建投递状态机。
func (s *Service) ResolveReply(ctx context.Context, msg *adapter.Message, metadata map[string]string) (context.Context, error) {
	inv := skill.CurrentInvocation(ctx)
	owner := inv.AuthenticatedUserID
	if owner == "" {
		owner = msg.UserID
	}
	requestID := inv.RequestID
	if requestID == "" {
		requestID = msg.Metadata["request_id"]
	}
	dispatchTaskRef := skill.SystemDispatchTaskRef(ctx)
	if requestID == "" && dispatchTaskRef == "" {
		return ctx, nil
	}
	completion, _ := storage.AssistantCompletionFromContext(ctx)
	seen := map[string]bool{}
	for _, a := range completion.Attachments {
		seen[a.ID] = true
	}
	filter := storage.ExternalToolExecutionFilter{OwnerID: owner, RequestID: requestID, DispatchTaskRef: dispatchTaskRef, Limit: 1000}
	for {
		rows, err := s.repo.List(ctx, filter)
		if err != nil {
			return ctx, err
		}
		for _, r := range rows {
			var m manifest
			if len(r.ResultManifestJSON) == 0 {
				continue
			}
			if err = json.Unmarshal(r.ResultManifestJSON, &m); err != nil {
				return ctx, err
			}
			for _, a := range m.Artifacts {
				if !a.Deliver {
					continue
				}
				if seen[a.ID] {
					continue
				}
				seen[a.ID] = true
				path := a.Path
				completion.Attachments = append(completion.Attachments, adapter.Attachment{ID: a.ID, Type: "file", Name: a.Name, Mime: a.MIME, Size: a.Size, ContentDigest: a.Digest, URL: "/api/v1/files/generated/" + path, Source: &adapter.AttachmentSource{OpenReader: func(context.Context) (io.ReadCloser, error) { return s.blobs.Open(path) }}})
			}
		}
		if len(rows) < filter.Limit {
			break
		}
		filter.Offset += len(rows)
	}
	return storage.WithAssistantCompletion(ctx, completion), nil
}
