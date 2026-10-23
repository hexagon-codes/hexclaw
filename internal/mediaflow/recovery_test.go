package mediaflow_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	imagegen "github.com/hexagon-codes/ai-core/media/image"
	videogen "github.com/hexagon-codes/ai-core/media/video"
	"github.com/hexagon-codes/ai-core/media/voice"
	"github.com/hexagon-codes/ai-core/transport"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/secret"
	"github.com/hexagon-codes/hexclaw/session"
	"github.com/hexagon-codes/hexclaw/storage"
	"github.com/hexagon-codes/hexclaw/storage/sqlite"
)

func restartMedia(t *testing.T, router *mediaflow.Router, repo mediaflow.Repository, blobs mediaflow.BlobStore, root string) *mediaflow.Service {
	t.Helper()
	codec, err := secret.LoadBox(root)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := mediaflow.NewService(t.Context(), router, repo, blobs, filepath.Join(root, "staging"), mediaflow.WithRecoveryCodec(codec))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func TestVideoSubmissionReferenceSurvivesDatabaseFailure(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%t", restart), func(t *testing.T) {
			video, err := os.ReadFile("testdata/short-video.mp4")
			if err != nil {
				t.Fatal(err)
			}
			var submits, polls atomic.Int32
			var upstream *httptest.Server
			upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/videos/generations":
					submits.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]string{"id": "durable-task", "task_status": "PROCESSING"})
				case "/async-result/durable-task":
					polls.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"task_status": "SUCCESS", "video_result": []map[string]string{{"url": upstream.URL + "/clip.mp4"}}})
				case "/clip.mp4":
					_, _ = w.Write(video)
				default:
					w.WriteHeader(404)
				}
			}))
			defer upstream.Close()
			cfg := mediaConfig(upstream.URL)
			provider := cfg.LLM.Providers["custom-gateway"]
			provider.ModelSpecs = []config.LLMProviderModelSpec{{ID: "cogvideox-2", Capabilities: []string{config.LLMModelCapabilityVideoGeneration}}}
			cfg.LLM.Providers["custom-gateway"] = provider
			svc, router, store, blobs := newMedia(t, cfg)
			if _, err := store.DB().Exec(`CREATE TRIGGER reject_submission_reference BEFORE UPDATE ON media_operations WHEN NEW.generation_state='running' BEGIN SELECT RAISE(ABORT,'fixture database failure'); END`); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			op, err := svc.Begin(ctx, mediaflow.Request{Origin: mediaflow.Origin{Owner: "owner", Kind: "direct_api", RequestID: "video-reference"}, Kind: "video", Video: &videogen.Request{Prompt: "a blue square"}})
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Dir(blobs.Root())
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				data, err := os.ReadFile(filepath.Join(root, "staging", op.ID+".json"))
				var staged mediaflow.Operation
				if err == nil && json.Unmarshal(data, &staged) == nil && staged.UpstreamID == "durable-task" {
					break
				}
				select {
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("submitted task reference was not durably retained")
				}
			}
			if restart {
				svc.Close()
			}
			if _, err := store.DB().Exec(`DROP TRIGGER reject_submission_reference`); err != nil {
				t.Fatal(err)
			}
			if restart {
				svc = restartMedia(t, router, store.MediaRepository(), blobs, root)
			} else if _, err := svc.Resume(ctx, "owner", op.ID); err != nil {
				t.Fatal(err)
			}
			op, err = svc.Wait(ctx, "owner", op.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			if op.SaveState != "ready" || op.UpstreamID != "durable-task" || len(op.Assets) != 1 || submits.Load() != 1 || polls.Load() != 1 {
				t.Fatalf("task reference recovery lost or resubmitted work: %+v submits=%d polls=%d", op, submits.Load(), polls.Load())
			}
		})
	}
}

type failFormalSave struct {
	mediaflow.BlobStore
	calls  atomic.Int32
	failAt int32
}

func (b *failFormalSave) SaveStream(ctx context.Context, r io.Reader, ext string) (string, error) {
	if b.calls.Add(1) == b.failAt {
		return "", errors.New("fixture formal asset store is unavailable")
	}
	return b.BlobStore.SaveStream(ctx, r, ext)
}

func TestRawMediaStagingRecoversOutputsBeforeFormalSave(t *testing.T) {
	for _, kind := range []string{"image", "audio"} {
		t.Run(kind, func(t *testing.T) {
			imageBytes := pngData(t)
			var generations atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				generations.Add(1)
				encoded := base64.StdEncoding.EncodeToString(imageBytes)
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": encoded}, {"b64_json": encoded}}})
			}))
			defer upstream.Close()
			cfg := mediaConfig(upstream.URL)
			svc, router, store, blobs := newMedia(t, cfg)
			svc.Close()
			failAt, count := int32(2), 2
			req := request("staged-output")
			req.Image.N = 2
			var speech *fixtureSpeech
			if kind == "audio" {
				data, err := os.ReadFile("testdata/short-audio.wav")
				if err != nil {
					t.Fatal(err)
				}
				speech = &fixtureSpeech{audio: data}
				cfg.Voice.Enabled, cfg.Voice.TTS.Provider = true, "fixture-tts"
				router.Reload(cfg, voice.NewService(nil, speech))
				req = mediaflow.Request{Origin: mediaflow.Origin{Owner: "owner", Kind: "direct_api", RequestID: "staged-audio"}, Kind: "audio", Action: "synthesize", Speech: &mediaflow.SpeechRequest{Text: "hello"}}
				failAt, count = 1, 1
			}
			root := filepath.Dir(blobs.Root())
			svc = restartMedia(t, router, store.MediaRepository(), &failFormalSave{BlobStore: blobs, failAt: failAt}, root)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			op, err := svc.Begin(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			op, err = svc.Wait(ctx, "owner", op.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			if op.RecoveryKind != "staged_blob" || op.ErrorCode != "persist_failed" || op.SaveState == "ready" {
				t.Fatalf("failed formal save lost its recovery source: %+v", op)
			}
			var firstAsset mediaflow.Asset
			if kind == "image" {
				if len(op.Assets) != 1 {
					t.Fatalf("partial output was lost: %+v", op)
				}
				firstAsset = op.Assets[0]
			}
			svc.Close()
			svc = restartMedia(t, router, store.MediaRepository(), blobs, root)
			op, err = svc.Wait(ctx, "owner", op.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			if op.SaveState != "ready" || len(op.Assets) != count {
				t.Fatalf("durable raw output was not recovered: %+v", op)
			}
			if kind == "image" {
				var result imagegen.Result
				if json.Unmarshal(op.Result, &result) != nil || len(result.Images) != 2 || result.Images[0].FilePath == "" || result.Images[1].FilePath == "" || firstAsset != op.Assets[0] || generations.Load() != 1 {
					t.Fatalf("recovery changed saved output or public result: %+v generations=%d", op, generations.Load())
				}
			} else if speech.calls.Load() != 1 || generations.Load() != 0 {
				t.Fatal("audio recovery repeated generation")
			}
			for _, asset := range op.Assets {
				reader, err := svc.OpenAsset(ctx, "owner", asset)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(reader)
				_ = reader.Close()
				if err != nil || len(data) == 0 {
					t.Fatal("recovered asset has no readable bytes")
				}
			}
		})
	}
}

// 仅关闭定时发现，投递读写仍全部使用同一个真实SQLite仓库。
type explicitDeliveryRepository struct{ *sqlite.MediaRepository }

func (*explicitDeliveryRepository) PendingDeliveries(context.Context, int) ([]*mediaflow.Delivery, error) {
	return nil, nil
}

type controlledDelivery struct {
	prepare func(context.Context, json.RawMessage) (string, error)
	send    func(context.Context, json.RawMessage, string) (mediaflow.DeliveryOutcome, error)
	query   func(context.Context, string) (mediaflow.DeliveryOutcome, error)
}

func (d controlledDelivery) Prepare(ctx context.Context, payload json.RawMessage) (string, error) {
	return d.prepare(ctx, payload)
}
func (d controlledDelivery) Send(ctx context.Context, payload json.RawMessage, prepared string) (mediaflow.DeliveryOutcome, error) {
	return d.send(ctx, payload, prepared)
}
func (d controlledDelivery) Query(ctx context.Context, receipt string) (mediaflow.DeliveryOutcome, error) {
	return d.query(ctx, receipt)
}

func deliveryFixture(t *testing.T, parts int, wrap ...func(*sqlite.MediaRepository) mediaflow.Repository) (*mediaflow.Service, *sqlite.Store, *mediaflow.Operation, string, *atomic.Int32) {
	t.Helper()
	data := pngData(t)
	calls := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(data)}}})
	}))
	t.Cleanup(upstream.Close)
	cfg := mediaConfig(upstream.URL)
	svc, router, store, blobs := newMedia(t, cfg)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	op, err := svc.Begin(ctx, request("delivery-source"))
	if err != nil {
		t.Fatal(err)
	}
	op, err = svc.Wait(ctx, "owner", op.ID, false)
	if err != nil || op.SaveState != "ready" {
		t.Fatalf("source asset unavailable: %+v %v", op, err)
	}
	svc.Close()
	var repo mediaflow.Repository = &explicitDeliveryRepository{store.MediaRepository()}
	if len(wrap) > 0 {
		repo = wrap[0](store.MediaRepository())
	}
	svc = restartMedia(t, router, repo, blobs, filepath.Dir(blobs.Root()))
	now := time.Now()
	if err := store.CreateSession(ctx, &storage.Session{ID: "recovery-session", UserID: "owner", Platform: "dingtalk", InstanceID: "fixture-account", ChatID: "fixture-chat", Status: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	completion := mediaflow.ReplyCompletion{Owner: "owner", SessionID: "recovery-session", RequestID: "delivery-recovery", Operations: []string{op.ID}, Assets: op.Assets, Platform: "dingtalk", InstanceID: "fixture-account", ChatID: "fixture-chat"}
	manager := session.NewManager(store, cfg.Memory)
	if _, err := manager.SaveAssistantReply(mediaflow.WithReplyCompletion(ctx, completion), completion.SessionID, "generated media", session.AssistantMeta{MessageID: "delivery-recovery:assistant", RequestID: completion.RequestID}); err != nil {
		t.Fatal(err)
	}
	id := mediaflow.DeliveryID(completion)
	delivery, err := store.MediaRepository().Delivery(ctx, "owner", id)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < parts; i++ {
		delivery.Parts = append(delivery.Parts, mediaflow.DeliveryPart{Payload: json.RawMessage(fmt.Sprintf(`{"part":%d}`, i)), State: "pending"})
	}
	if err := store.MediaRepository().SaveDelivery(ctx, delivery, delivery.Revision); err != nil {
		t.Fatal(err)
	}
	return svc, store, op, id, calls
}

func TestUnknownMediaDeliveryWithReceiptOnlyQueries(t *testing.T) {
	svc, store, op, id, generations := deliveryFixture(t, 1)
	var sends, queries atomic.Int32
	transport := controlledDelivery{
		prepare: func(context.Context, json.RawMessage) (string, error) { return "resource", nil },
		send: func(context.Context, json.RawMessage, string) (mediaflow.DeliveryOutcome, error) {
			sends.Add(1)
			return mediaflow.DeliveryOutcome{State: "unknown", ReceiptRef: "retained-receipt"}, errors.New("send acknowledgement interrupted")
		},
		query: func(_ context.Context, receipt string) (mediaflow.DeliveryOutcome, error) {
			queries.Add(1)
			if receipt != "retained-receipt" {
				t.Errorf("queried a different receipt: %s", receipt)
			}
			return mediaflow.DeliveryOutcome{State: "delivered"}, nil
		},
	}
	svc.SetDeliveryResolver(func(context.Context, *mediaflow.Delivery) (mediaflow.DeliveryTransport, []json.RawMessage, error) {
		return transport, nil, nil
	})
	if err := svc.Deliver(t.Context(), "owner", id); err == nil {
		t.Fatal("unknown send was reported as success")
	}
	pending, err := store.MediaRepository().PendingDeliveries(t.Context(), 16)
	if err != nil || len(pending) != 1 || pending[0].State != "unknown" {
		t.Fatalf("queryable unknown disappeared from recovery: %+v %v", pending, err)
	}
	if _, err := svc.Resume(t.Context(), "owner", op.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deliver(t.Context(), "owner", id); err != nil {
		t.Fatal(err)
	}
	delivery, err := store.MediaRepository().Delivery(t.Context(), "owner", id)
	if err != nil || delivery.State != "delivered" || sends.Load() != 1 || queries.Load() != 1 || generations.Load() != 1 {
		t.Fatalf("unknown recovery resent or lost receipt: %+v %v sends=%d queries=%d", delivery, err, sends.Load(), queries.Load())
	}
}

func TestFailedMediaReceiptAndResumePreserveSuccessfulParts(t *testing.T) {
	svc, store, op, id, generations := deliveryFixture(t, 2)
	var prepares, sends, queries atomic.Int32
	transport := controlledDelivery{
		prepare: func(context.Context, json.RawMessage) (string, error) { prepares.Add(1); return "resource", nil },
		send: func(context.Context, json.RawMessage, string) (mediaflow.DeliveryOutcome, error) {
			switch sends.Add(1) {
			case 1:
				return mediaflow.DeliveryOutcome{State: "delivered"}, nil
			case 2:
				return mediaflow.DeliveryOutcome{State: "accepted", ReceiptRef: "failed-receipt"}, nil
			default:
				return mediaflow.DeliveryOutcome{State: "delivered"}, nil
			}
		},
		query: func(context.Context, string) (mediaflow.DeliveryOutcome, error) {
			queries.Add(1)
			return mediaflow.DeliveryOutcome{State: "failed"}, nil
		},
	}
	svc.SetDeliveryResolver(func(context.Context, *mediaflow.Delivery) (mediaflow.DeliveryTransport, []json.RawMessage, error) {
		return transport, nil, nil
	})
	if err := svc.Deliver(t.Context(), "owner", id); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deliver(t.Context(), "owner", id); err != nil {
		t.Fatal(err)
	}
	delivery, err := store.MediaRepository().Delivery(t.Context(), "owner", id)
	if err != nil || delivery.State != "failed" || delivery.Parts[0].State != "delivered" || delivery.Parts[1].State != "failed" {
		t.Fatalf("failed receipt was reported as accepted: %+v %v", delivery, err)
	}
	if _, err := svc.Resume(t.Context(), "owner", op.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deliver(t.Context(), "owner", id); err != nil {
		t.Fatal(err)
	}
	delivery, err = store.MediaRepository().Delivery(t.Context(), "owner", id)
	if err != nil || delivery.State != "delivered" || sends.Load() != 3 || prepares.Load() != 2 || queries.Load() != 1 || generations.Load() != 1 {
		t.Fatalf("resume repeated a successful stage: %+v %v sends=%d prepares=%d", delivery, err, sends.Load(), prepares.Load())
	}
}

func TestMediaResumeRetriesOnlyKnownPreparationFailure(t *testing.T) {
	svc, store, op, id, generations := deliveryFixture(t, 1)
	var prepares, sends atomic.Int32
	transport := controlledDelivery{
		prepare: func(context.Context, json.RawMessage) (string, error) {
			if prepares.Add(1) == 1 {
				return "", &adapter.ResourcePreparationError{Known: true, Cause: errors.New("fixture resource rejected")}
			}
			return "prepared-resource", nil
		},
		send: func(context.Context, json.RawMessage, string) (mediaflow.DeliveryOutcome, error) {
			sends.Add(1)
			return mediaflow.DeliveryOutcome{State: "delivered"}, nil
		},
		query: func(context.Context, string) (mediaflow.DeliveryOutcome, error) {
			t.Error("unsubmitted part queried")
			return mediaflow.DeliveryOutcome{}, nil
		},
	}
	svc.SetDeliveryResolver(func(context.Context, *mediaflow.Delivery) (mediaflow.DeliveryTransport, []json.RawMessage, error) {
		return transport, nil, nil
	})
	if err := svc.Deliver(t.Context(), "owner", id); err == nil {
		t.Fatal("preparation failure was ignored")
	}
	if sends.Load() != 0 {
		t.Fatal("failed preparation submitted a visible message")
	}
	if _, err := svc.Resume(t.Context(), "owner", op.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deliver(t.Context(), "owner", id); err != nil {
		t.Fatal(err)
	}
	delivery, err := store.MediaRepository().Delivery(t.Context(), "owner", id)
	if err != nil || delivery.State != "delivered" || sends.Load() != 1 || prepares.Load() != 2 || generations.Load() != 1 {
		t.Fatalf("preparation resume repeated generation or send: %+v %v", delivery, err)
	}
}

func TestMediaReplyRecoveryPaginatesWithoutBlockingGeneration(t *testing.T) {
	data := pngData(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(data)}}})
	}))
	defer upstream.Close()
	svc, router, store, blobs := newMedia(t, mediaConfig(upstream.URL))
	svc.Close()
	var failedIDs []string
	for i := 0; i < 33; i++ {
		req := request(fmt.Sprintf("old-reply-%d", i))
		req.Origin.Kind, req.Origin.SessionID = "conversation", "recovery-session"
		op, err := svc.Begin(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		if i < 16 {
			if _, err := svc.Cancel(t.Context(), "owner", op.ID); err != nil {
				t.Fatal(err)
			}
		} else {
			op.GenerationState, op.ErrorCode = "failed", "upstream_failed"
			if err := store.MediaRepository().Update(t.Context(), op, op.Revision); err != nil {
				t.Fatal(err)
			}
			failedIDs = append(failedIDs, op.ID)
		}
	}
	sort.Strings(failedIDs)
	lastID := failedIDs[len(failedIDs)-1]
	svc = restartMedia(t, router, store.MediaRepository(), blobs, filepath.Dir(blobs.Root()))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var enteredOnce sync.Once
	svc.SetReplyResumer(func(ctx context.Context, op *mediaflow.Operation) error {
		if op.CancelRequested {
			t.Error("cancelled reply was included in recovery")
		}
		if op.ID == lastID {
			enteredOnce.Do(func() { close(entered) })
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return errors.New("fixture original reply cannot yet finish")
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("earlier failed replies starved later recovery")
	}
	op, err := svc.Begin(ctx, request("generation-during-reply-recovery"))
	if err != nil {
		t.Fatal(err)
	}
	op, err = svc.Wait(ctx, "owner", op.ID, false)
	if err != nil || op.SaveState != "ready" {
		t.Fatalf("reply recovery blocked generation: %+v %v", op, err)
	}
	unblock()
}

type classifiedSpeechFailure struct{ cause error }

func (e classifiedSpeechFailure) Error() string         { return e.cause.Error() }
func (e classifiedSpeechFailure) Unwrap() error         { return e.cause }
func (classifiedSpeechFailure) DefinitiveFailure() bool { return true }

type rejectedSpeech struct{ failure error }

func (rejectedSpeech) Name() string { return "fixture-tts" }
func (p rejectedSpeech) Synthesize(context.Context, string, voice.SynthesizeOptions) (*voice.SynthesizeResult, error) {
	return nil, p.failure
}
func (rejectedSpeech) Voices() []voice.VoiceInfo { return nil }
func (rejectedSpeech) SupportedFormats() []voice.AudioFormat {
	return []voice.AudioFormat{voice.FormatWAV}
}

func TestMediaSpeechFailureClassificationKeepsUnknownAttempts(t *testing.T) {
	known := classifiedSpeechFailure{cause: errors.New("fixture explicit rejection")}
	for _, tc := range []struct {
		name  string
		err   error
		state string
	}{
		{"wrapped_definitive", fmt.Errorf("speech: %w", known), "failed"},
		{"all_definitive", errors.Join(known, &transport.ProviderError{StatusCode: 429}), "failed"},
		{"mixed_unknown", errors.Join(known, errors.New("response lost")), "unknown"},
		{"cancelled", classifiedSpeechFailure{cause: context.Canceled}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mediaConfig("http://unused.invalid")
			cfg.Voice.Enabled, cfg.Voice.TTS.Provider = true, "fixture-tts"
			svc, router, _, _ := newMedia(t, cfg)
			router.Reload(cfg, voice.NewService(nil, rejectedSpeech{failure: tc.err}))
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			op, err := svc.Begin(ctx, mediaflow.Request{Origin: mediaflow.Origin{Owner: "owner", Kind: "direct_api", RequestID: tc.name}, Kind: "audio", Action: "synthesize", Speech: &mediaflow.SpeechRequest{Text: "hello"}})
			if err != nil {
				t.Fatal(err)
			}
			op, err = svc.Wait(ctx, "owner", op.ID, false)
			if err != nil || op.GenerationState != tc.state {
				t.Fatalf("wrong speech failure fact: %+v %v", op, err)
			}
		})
	}
}

type cancelAtSendRepository struct {
	*explicitDeliveryRepository
	beforeClaim func(context.Context, *mediaflow.Delivery)
}

func (r *cancelAtSendRepository) SaveDelivery(ctx context.Context, d *mediaflow.Delivery, revision int64) error {
	for _, part := range d.Parts {
		if part.State == "sending" && r.beforeClaim != nil {
			callback := r.beforeClaim
			r.beforeClaim = nil
			callback(ctx, d)
			break
		}
	}
	return r.MediaRepository.SaveDelivery(ctx, d, revision)
}

func TestMediaSendClaimChecksCancellationAndSpecificResend(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit_resend=%t", selected), func(t *testing.T) {
			var repo *cancelAtSendRepository
			svc, store, op, id, _ := deliveryFixture(t, 1, func(base *sqlite.MediaRepository) mediaflow.Repository {
				repo = &cancelAtSendRepository{explicitDeliveryRepository: &explicitDeliveryRepository{base}}
				return repo
			})
			delivery, err := store.MediaRepository().Delivery(t.Context(), "owner", id)
			if err != nil {
				t.Fatal(err)
			}
			delivery.Resend = true
			delivery.ResendOperations = []string{"another-original-operation"}
			if selected {
				delivery.ResendOperations = []string{op.ID}
			}
			if err := store.MediaRepository().SaveDelivery(t.Context(), delivery, delivery.Revision); err != nil {
				t.Fatal(err)
			}
			repo.beforeClaim = func(ctx context.Context, _ *mediaflow.Delivery) {
				if _, err := svc.Cancel(ctx, "owner", op.ID); err != nil {
					t.Fatal(err)
				}
			}
			var sends atomic.Int32
			transport := controlledDelivery{
				prepare: func(context.Context, json.RawMessage) (string, error) { return "resource", nil },
				send: func(context.Context, json.RawMessage, string) (mediaflow.DeliveryOutcome, error) {
					sends.Add(1)
					return mediaflow.DeliveryOutcome{State: "delivered"}, nil
				},
			}
			svc.SetDeliveryResolver(func(context.Context, *mediaflow.Delivery) (mediaflow.DeliveryTransport, []json.RawMessage, error) {
				return transport, nil, nil
			})
			if err := svc.Deliver(t.Context(), "owner", id); err != nil {
				t.Fatal(err)
			}
			delivery, err = store.MediaRepository().Delivery(t.Context(), "owner", id)
			if err != nil {
				t.Fatal(err)
			}
			if selected {
				if sends.Load() != 1 || delivery.State != "delivered" {
					t.Fatalf("explicit original-asset resend was blocked: %+v sends=%d", delivery, sends.Load())
				}
			} else if sends.Load() != 0 || delivery.State != "cancelled" {
				t.Fatalf("cancellation lost between check and send claim: %+v sends=%d", delivery, sends.Load())
			}
		})
	}
}

func TestVideoCoverRecoveryKeepsOriginalPrimaryAsset(t *testing.T) {
	video, err := os.ReadFile("testdata/short-video.mp4")
	if err != nil {
		t.Fatal(err)
	}
	cover := pngData(t)
	var submits, videoDownloads, coverDownloads atomic.Int32
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/videos/generations":
			submits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "cover-task", "task_status": "PROCESSING"})
		case "/async-result/cover-task":
			_ = json.NewEncoder(w).Encode(map[string]any{"task_status": "SUCCESS", "video_result": []map[string]string{{"url": upstream.URL + "/clip.mp4", "cover_image_url": upstream.URL + "/cover.png"}}})
		case "/clip.mp4":
			videoDownloads.Add(1)
			_, _ = w.Write(video)
		case "/cover.png":
			if coverDownloads.Add(1) == 1 {
				w.WriteHeader(503)
				return
			}
			_, _ = w.Write(cover)
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	cfg := mediaConfig(upstream.URL)
	provider := cfg.LLM.Providers["custom-gateway"]
	provider.ModelSpecs = []config.LLMProviderModelSpec{{ID: "cogvideox-2", Capabilities: []string{config.LLMModelCapabilityVideoGeneration}}}
	cfg.LLM.Providers["custom-gateway"] = provider
	svc, _, _, _ := newMedia(t, cfg)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	op, err := svc.Begin(ctx, mediaflow.Request{Origin: mediaflow.Origin{Owner: "owner", Kind: "direct_api", RequestID: "video-cover"}, Kind: "video", Video: &videogen.Request{Prompt: "a blue square"}})
	if err != nil {
		t.Fatal(err)
	}
	op, err = svc.Wait(ctx, "owner", op.ID, false)
	if err != nil || op.SaveState != "partial" || len(op.Assets) != 1 {
		t.Fatalf("missing cover was hidden or primary was lost: %+v %v", op, err)
	}
	primary := op.Assets[0]
	if _, err := svc.Resume(ctx, "owner", op.ID); err != nil {
		t.Fatal(err)
	}
	op, err = svc.Wait(ctx, "owner", op.ID, false)
	if err != nil || op.SaveState != "ready" || len(op.Assets) != 2 || op.Assets[0] != primary || op.Assets[1].Role != "cover" || submits.Load() != 1 || videoDownloads.Load() != 1 || coverDownloads.Load() != 2 {
		t.Fatalf("video recovery duplicated primary or generation: %+v %v submits=%d primary=%d cover=%d", op, err, submits.Load(), videoDownloads.Load(), coverDownloads.Load())
	}
}
