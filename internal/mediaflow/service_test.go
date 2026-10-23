package mediaflow_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	imagegen "github.com/hexagon-codes/ai-core/media/image"
	videogen "github.com/hexagon-codes/ai-core/media/video"
	"github.com/hexagon-codes/ai-core/media/voice"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/internal/requestcancel"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/secret"
	"github.com/hexagon-codes/hexclaw/session"
	"github.com/hexagon-codes/hexclaw/storage"
	"github.com/hexagon-codes/hexclaw/storage/sqlite"
	"github.com/hexagon-codes/toolkit/blobstore"
)

func pngData(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func mediaConfig(base string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Skill.Builtin.MediaGen = true
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"custom-gateway": {APIKey: "fixture-key", BaseURL: base, Compatible: "openai", ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: []config.LLMProviderModelSpec{{ID: "shared-image", Capabilities: []string{config.LLMModelCapabilityImageGeneration}}}}}
	return cfg
}

func newMedia(t *testing.T, cfg *config.Config) (*mediaflow.Service, *mediaflow.Router, *sqlite.Store, *blobstore.Store) {
	t.Helper()
	root := t.TempDir()
	store, err := sqlitefixture.New(filepath.Join(root, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	blobs, err := blobstore.NewStore(filepath.Join(root, "generated"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blobs.Close() })
	router := mediaflow.NewRouter()
	router.Reload(cfg, nil)
	codec, err := secret.LoadBox(root)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := mediaflow.NewService(t.Context(), router, store.MediaRepository(), blobs, filepath.Join(root, "staging"), mediaflow.WithRecoveryCodec(codec))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, router, store, blobs
}

func request(id string) mediaflow.Request {
	return mediaflow.Request{Origin: mediaflow.Origin{Owner: "owner", Kind: "direct_api", RequestID: id}, Kind: "image", Image: &imagegen.Request{Prompt: "a red square"}}
}

func TestImageOperationPersistsAndReplaysBeforeCurrentDefaults(t *testing.T) {
	data := pngData(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/images/generations" {
			t.Errorf("wrong protocol path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(data)}}})
	}))
	defer upstream.Close()
	cfg := mediaConfig(upstream.URL)
	svc, router, store, blobs := newMedia(t, cfg)
	op, err := svc.Begin(t.Context(), request("same-request"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	op, err = svc.Wait(ctx, "owner", op.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if op.SaveState != "ready" || len(op.Assets) != 1 || op.Assets[0].MIME != "image/png" || calls.Load() != 1 {
		t.Fatalf("media not delivered to durable asset: %+v calls=%d", op, calls.Load())
	}
	reader, err := blobs.OpenReader(op.Assets[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = png.Decode(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	var assets, messages int
	if err := store.DB().QueryRow(`SELECT count(*) FROM media_assets WHERE operation_id=?`, op.ID).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT count(*) FROM messages`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if assets != 1 || messages != 0 {
		t.Fatalf("standalone API invented chat: assets=%d messages=%d", assets, messages)
	}
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{}
	cfg.Skill.Builtin.MediaGen = false
	router.Reload(cfg, nil)
	again, err := svc.Begin(ctx, request("same-request"))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != op.ID || again.ExecutionDigest != op.ExecutionDigest || calls.Load() != 1 {
		t.Fatal("retry re-resolved disabled/default route or regenerated")
	}
	changed := request("same-request")
	changed.Image.Prompt = "different explicit intent"
	if _, err := svc.Begin(ctx, changed); !errors.Is(err, mediaflow.ErrConflict) {
		t.Fatalf("expected intent conflict, got %v", err)
	}
	if _, err := svc.Read(ctx, "another-owner", op.ID); !errors.Is(err, mediaflow.ErrNotFound) {
		t.Fatal("operation crossed owners")
	}
	if managed, err := svc.AuthorizePath(ctx, "owner", op.Assets[0].Path); !managed || err != nil {
		t.Fatalf("owner could not read its media path: managed=%t err=%v", managed, err)
	}
	if managed, err := svc.AuthorizePath(ctx, "another-owner", op.Assets[0].Path); !managed || !errors.Is(err, mediaflow.ErrNotFound) {
		t.Fatalf("media path crossed owners: managed=%t err=%v", managed, err)
	}
	if managed, err := svc.AuthorizePath(ctx, "owner", "legacy-k12-file.pdf"); managed || err != nil {
		t.Fatalf("unmanaged file changed its existing read contract: managed=%t err=%v", managed, err)
	}
}

func TestImageUnknownIsNotResubmitted(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	svc, _, _, _ := newMedia(t, mediaConfig(upstream.URL))
	op, err := svc.Begin(t.Context(), request("unknown"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	op, err = svc.Wait(ctx, "owner", op.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if op.GenerationState != "unknown" || calls.Load() != 1 {
		t.Fatalf("unknown retried: %+v calls=%d", op, calls.Load())
	}
	if _, err := svc.Begin(ctx, request("unknown")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resume(ctx, "owner", op.ID); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("unknown operation was resubmitted")
	}
}

func TestInvalidImageCannotBecomeReady(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString([]byte("not an image"))}}})
	}))
	defer upstream.Close()
	svc, _, _, _ := newMedia(t, mediaConfig(upstream.URL))
	op, err := svc.Begin(t.Context(), request("bad-image"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	op, err = svc.Wait(ctx, "owner", op.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if op.SaveState != "failed" || op.ErrorCode != "persist_failed" || len(op.Assets) != 0 {
		t.Fatalf("invalid output reported as ready: %+v", op)
	}
}

func TestRemoteImageRecoveryDoesNotRegenerateOrPersistSignedURLInClear(t *testing.T) {
	data := pngData(t)
	var generations, downloads atomic.Int32
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/images/generations" {
			generations.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"url": upstream.URL + "/image?token=private-recovery-token"}}})
			return
		}
		if downloads.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	}))
	defer upstream.Close()
	svc, _, store, _ := newMedia(t, mediaConfig(upstream.URL))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	op, err := svc.Begin(ctx, request("recover-url"))
	if err != nil {
		t.Fatal(err)
	}
	op, err = svc.Wait(ctx, "owner", op.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if op.SaveState != "failed" || op.RecoveryKind != "remote_url" || generations.Load() != 1 {
		t.Fatalf("wrong save-failure record: %+v", op)
	}
	var stored string
	if err := store.DB().QueryRowContext(ctx, `SELECT data FROM media_operations WHERE id=?`, op.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(stored), []byte("private-recovery-token")) {
		t.Fatal("signed recovery URL was persisted in clear")
	}
	if _, err := svc.Resume(ctx, "owner", op.ID); err != nil {
		t.Fatal(err)
	}
	op, err = svc.Wait(ctx, "owner", op.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if op.SaveState != "ready" || len(op.Assets) != 1 || generations.Load() != 1 || downloads.Load() != 2 {
		t.Fatalf("recovery repeated generation or lost asset: %+v gen=%d downloads=%d", op, generations.Load(), downloads.Load())
	}
}

func TestMediaRequestDisconnectKeepsOnlyAcceptedLifetime(t *testing.T) {
	svc, _, _, _ := newMedia(t, mediaConfig("http://unused.invalid"))
	signalParent, cancelSignal := requestcancel.WithSignal(t.Context())
	defer cancelSignal(nil)
	parent, cancel := context.WithCancel(signalParent)
	owned, cleanup := svc.RequestContext(parent)
	defer cleanup()
	if err := mediaflow.RetainRequest(owned); err != nil {
		t.Fatal(err)
	}
	cancel()
	if owned.Err() != nil {
		t.Fatal("accepted media task was cancelled by transport disconnect")
	}
	other, stop := context.WithCancel(t.Context())
	cancelled, release := svc.RequestContext(other)
	defer release()
	stop()
	select {
	case <-cancelled.Done():
	case <-time.After(time.Second):
		t.Fatal("unaccepted request ignored transport cancellation")
	}
	if err := mediaflow.RetainRequest(cancelled); err == nil {
		t.Fatal("cancelled request accepted a new media side effect")
	}
	svc.Close()
	select {
	case <-owned.Done():
	case <-time.After(time.Second):
		t.Fatal("host shutdown did not stop retained request")
	}
}

type cancelOnMediaAcceptanceRepository struct {
	*sqlite.MediaRepository
	cancel context.CancelCauseFunc
}

func (r *cancelOnMediaAcceptanceRepository) Create(ctx context.Context, op *mediaflow.Operation) (*mediaflow.Operation, error) {
	accepted, err := r.MediaRepository.Create(ctx, op)
	if err == nil {
		r.cancel(requestcancel.ErrUserCancelled)
	}
	return accepted, err
}

func TestMediaExplicitCancellationIsOrderedWithAcceptance(t *testing.T) {
	for _, during := range []bool{false, true} {
		name := "before_acceptance"
		if during {
			name = "during_acceptance"
		}
		t.Run(name, func(t *testing.T) {
			data := pngData(t)
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(data)}}})
			}))
			defer upstream.Close()
			svc, router, store, blobs := newMedia(t, mediaConfig(upstream.URL))
			parent, cancelUser := requestcancel.WithSignal(t.Context())
			defer cancelUser(nil)
			if during {
				svc.Close()
				repo := &cancelOnMediaAcceptanceRepository{MediaRepository: store.MediaRepository(), cancel: cancelUser}
				svc = restartMedia(t, router, repo, blobs, filepath.Dir(blobs.Root()))
			}
			ctx, cleanup := svc.RequestContext(parent)
			defer cleanup()
			if err := mediaflow.RetainRequest(ctx); err != nil {
				t.Fatal(err)
			}
			if !during {
				cancelUser(requestcancel.ErrUserCancelled)
			}
			op, err := svc.Begin(ctx, request(name))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("explicit cancellation did not stop acceptance: operation=%+v err=%v", op, err)
			}
			cleanup()
			var count int
			if err := store.DB().QueryRow(`SELECT count(*) FROM media_operations`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if !during {
				if count != 0 || calls.Load() != 0 {
					t.Fatalf("cancelled request created a side effect: operations=%d calls=%d", count, calls.Load())
				}
				return
			}
			if op == nil || count != 1 {
				t.Fatalf("accepted operation identity lost during cancellation: operation=%+v count=%d", op, count)
			}
			persisted, err := svc.Read(t.Context(), "owner", op.ID)
			if err != nil || !persisted.CancelRequested || calls.Load() > 1 {
				t.Fatalf("accepted cancellation was not durable: operation=%+v calls=%d err=%v", persisted, calls.Load(), err)
			}
		})
	}
}

func TestMediaCancellationAndHotReloadKeepOriginalSubmission(t *testing.T) {
	data := pngData(t)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var firstCalls, secondCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if firstCalls.Add(1) <= 2 {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(data)}}})
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(data)}}})
	}))
	defer second.Close()
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	cfg := mediaConfig(first.URL)
	svc, router, _, _ := newMedia(t, cfg)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	running, err := svc.Begin(ctx, request("running"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// 占满执行槽后再取消排队项，避免将已提交操作误当作未提交取消。
	otherRunning, err := svc.Begin(ctx, request("other-running"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	queued, err := svc.Begin(ctx, request("cancel-before-submit"))
	if err != nil {
		t.Fatal(err)
	}
	queued, err = svc.Cancel(ctx, "owner", queued.ID)
	if err != nil || queued.GenerationState != "cancelled" {
		t.Fatalf("queued cancel=%+v err=%v", queued, err)
	}
	if _, err := svc.Cancel(ctx, "owner", running.ID); err != nil {
		t.Fatal(err)
	}
	pc := cfg.LLM.Providers["custom-gateway"]
	pc.BaseURL = second.URL
	cfg.LLM.Providers["custom-gateway"] = pc
	router.Reload(cfg, nil)
	unblock()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		running, err = svc.Read(ctx, "owner", running.ID)
		if err != nil {
			t.Fatal(err)
		}
		if running.SaveState == "ready" {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if !running.CancelRequested || running.GenerationState != "succeeded" || len(running.Assets) != 1 || running.Route.Endpoint != first.URL || firstCalls.Load() != 2 {
		t.Fatalf("late result or original route lost: %+v calls=%d", running, firstCalls.Load())
	}
	otherRunning, err = svc.Wait(ctx, "owner", otherRunning.ID, false)
	if err != nil || otherRunning.SaveState != "ready" || otherRunning.Route.Endpoint != first.URL {
		t.Fatalf("second accepted operation lost its original route: %+v err=%v", otherRunning, err)
	}
	latest, err := svc.Begin(ctx, request("new-after-reload"))
	if err != nil {
		t.Fatal(err)
	}
	latest, err = svc.Wait(ctx, "owner", latest.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if latest.SaveState != "ready" || latest.Route.Endpoint != second.URL || firstCalls.Load() != 2 || secondCalls.Load() != 1 {
		t.Fatalf("reload crossed original route: %+v first=%d second=%d", latest, firstCalls.Load(), secondCalls.Load())
	}
}

func TestVideoOnlyOperationResumesOriginalTaskAfterRestart(t *testing.T) {
	videoBytes, err := os.ReadFile("testdata/short-video.mp4")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	var submits, polls atomic.Int32
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/videos/generations":
			submits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "original-task", "task_status": "PROCESSING"})
		case "/async-result/original-task":
			if polls.Add(1) == 1 {
				close(entered)
				<-r.Context().Done()
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"task_status": "SUCCESS", "video_result": []map[string]string{{"url": upstream.URL + "/clip.mp4"}}})
		case "/clip.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(videoBytes)
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	cfg := mediaConfig(upstream.URL)
	pc := cfg.LLM.Providers["custom-gateway"]
	pc.ModelSpecs = []config.LLMProviderModelSpec{{ID: "cogvideox-2", Capabilities: []string{config.LLMModelCapabilityVideoGeneration}}}
	cfg.LLM.Providers["custom-gateway"] = pc
	svc, router, store, blobs := newMedia(t, cfg)
	if router.Current().Images.HasProvider() || !router.Current().Videos.HasProvider() {
		t.Fatal("video was gated by image availability")
	}
	defer svc.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	op, err := svc.Begin(ctx, mediaflow.Request{Origin: mediaflow.Origin{Owner: "owner", Kind: "direct_api", RequestID: "one-video"}, Kind: "video", Video: &videogen.Request{Prompt: "a blue square"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	svc.Close()
	persisted, err := svc.Read(ctx, "owner", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.GenerationState != "running" || persisted.UpstreamID != "original-task" {
		t.Fatalf("task reference lost: %+v", persisted)
	}
	codec, err := secret.LoadBox(filepath.Dir(blobs.Root()))
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := mediaflow.NewService(t.Context(), router, store.MediaRepository(), blobs, filepath.Join(filepath.Dir(blobs.Root()), "staging"), mediaflow.WithRecoveryCodec(codec))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	op, err = restarted.Wait(ctx, "owner", op.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if op.SaveState != "ready" || len(op.Assets) != 1 || op.Assets[0].MIME != "video/mp4" || submits.Load() != 1 {
		t.Fatalf("video recovery regenerated or lost output: %+v submits=%d", op, submits.Load())
	}
}

type fixtureSpeech struct {
	audio []byte
	calls atomic.Int32
}

func (f *fixtureSpeech) Name() string { return "fixture-tts" }
func (f *fixtureSpeech) Synthesize(context.Context, string, voice.SynthesizeOptions) (*voice.SynthesizeResult, error) {
	f.calls.Add(1)
	return &voice.SynthesizeResult{Audio: f.audio, Format: voice.FormatWAV}, nil
}
func (*fixtureSpeech) Voices() []voice.VoiceInfo { return nil }
func (*fixtureSpeech) SupportedFormats() []voice.AudioFormat {
	return []voice.AudioFormat{voice.FormatWAV}
}

func TestMediaReplyAggregatesOperationsAndCommitsDeliveryAtomically(t *testing.T) {
	imageBytes := pngData(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(imageBytes)}}})
	}))
	defer upstream.Close()
	cfg := mediaConfig(upstream.URL)
	svc, router, store, blobs := newMedia(t, cfg)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var operations []string
	var assets []mediaflow.Asset
	for _, id := range []string{"first", "second"} {
		op, err := svc.Begin(ctx, request(id))
		if err != nil {
			t.Fatal(err)
		}
		op, err = svc.Wait(ctx, "owner", op.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		if op.SaveState != "ready" {
			t.Fatalf("operation failed: %+v", op)
		}
		operations = append(operations, op.ID)
		assets = append(assets, op.Assets...)
	}
	now := time.Now()
	if err := store.CreateSession(ctx, &storage.Session{ID: "media-session", UserID: "owner", Platform: "dingtalk", InstanceID: "fixture-account", ChatID: "fixture-chat", Status: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	completion := mediaflow.ReplyCompletion{Owner: "owner", SessionID: "media-session", RequestID: "one-turn", Operations: operations, Assets: assets, Platform: "dingtalk", InstanceID: "fixture-account", ChatID: "fixture-chat"}
	completionCtx := mediaflow.WithReplyCompletion(ctx, completion)
	manager := session.NewManager(store, cfg.Memory)
	meta := session.AssistantMeta{MessageID: "one-turn:assistant", RequestID: "one-turn"}
	if _, err := store.DB().ExecContext(ctx, `CREATE TRIGGER reject_media_delivery BEFORE INSERT ON media_deliveries BEGIN SELECT RAISE(ABORT,'fixture delivery failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SaveAssistantReply(completionCtx, "media-session", "both media assets", meta); err == nil {
		t.Fatal("delivery failure must fail the whole reply commit")
	}
	var messages, deliveries int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM messages`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if messages != 0 {
		t.Fatal("message escaped a failed delivery transaction")
	}
	if _, err := store.DB().ExecContext(ctx, `DROP TRIGGER reject_media_delivery`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := manager.SaveAssistantReply(completionCtx, "media-session", "both media assets", meta); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM messages`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM media_deliveries`).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || deliveries != 1 {
		t.Fatalf("duplicate reply or delivery: messages=%d deliveries=%d", messages, deliveries)
	}
	stored, err := store.GetMessage(ctx, meta.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	var attachmentMetadata struct {
		Attachments []adapter.Attachment `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(stored.Attachments), &attachmentMetadata); err != nil {
		t.Fatal(err)
	}
	attachments := attachmentMetadata.Attachments
	if len(attachments) != 2 || attachments[0].ID != assets[0].ID || attachments[1].ID != assets[1].ID {
		t.Fatalf("reply lost or reordered assets: %+v", attachments)
	}
	if _, err := manager.SaveAssistantReply(completionCtx, "media-session", "a different final reply", meta); !errors.Is(err, mediaflow.ErrConflict) {
		t.Fatalf("same reply overwritten: %v", err)
	}
	transport := &unknownDeliveryTransport{}
	resolver := func(context.Context, *mediaflow.Delivery) (mediaflow.DeliveryTransport, []json.RawMessage, error) {
		return transport, []json.RawMessage{json.RawMessage(`{"part":1}`), json.RawMessage(`{"part":2}`)}, nil
	}
	svc.SetDeliveryResolver(resolver)
	id := mediaflow.DeliveryID(completion)
	if err := svc.Deliver(ctx, "owner", id); err == nil {
		t.Fatal("unknown physical send must remain an error")
	}
	delivery, err := store.MediaRepository().Delivery(ctx, "owner", id)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.State != "unknown" || transport.sends.Load() != 2 {
		t.Fatalf("wrong physical send record: %+v sends=%d", delivery, transport.sends.Load())
	}
	svc.Close()
	restarted, err := mediaflow.NewService(t.Context(), router, store.MediaRepository(), blobs, filepath.Join(t.TempDir(), "restart-staging"))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restarted.SetDeliveryResolver(resolver)
	if err := restarted.Deliver(ctx, "owner", id); err != nil {
		t.Fatal(err)
	}
	if transport.sends.Load() != 2 {
		t.Fatal("unknown delivery replayed after restart")
	}
}

type unknownDeliveryTransport struct{ sends atomic.Int32 }

func (*unknownDeliveryTransport) Prepare(context.Context, json.RawMessage) (string, error) {
	return "prepared-resource", nil
}
func (t *unknownDeliveryTransport) Send(context.Context, json.RawMessage, string) (mediaflow.DeliveryOutcome, error) {
	if t.sends.Add(1) == 1 {
		return mediaflow.DeliveryOutcome{State: "accepted", ReceiptRef: "first-receipt"}, nil
	}
	return mediaflow.DeliveryOutcome{State: "unknown"}, errors.New("provider accepted but response was lost")
}
func (*unknownDeliveryTransport) Query(context.Context, string) (mediaflow.DeliveryOutcome, error) {
	return mediaflow.DeliveryOutcome{State: "delivered"}, nil
}
