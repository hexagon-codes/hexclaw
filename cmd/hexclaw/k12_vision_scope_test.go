package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/api"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/storage"
)

// 只替换外部模型协议端点，路由、配置指纹、回执及 SQLite 使用真实实现。
func k12VisionHTTPFixture(t *testing.T) (*llmrouter.Selector, config.LLMProviderConfig, storage.ModelCapabilityProbeReceiptStore, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "vision-v1" {
			http.Error(w, "unexpected fixture request", http.StatusBadRequest)
			return
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fixture accepted\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"fixture accepted"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(server.Close)
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "receipts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider := config.LLMProviderConfig{
		ProviderInstanceID: "pvd_v1_00112233445566778899aabbccddeeff",
		BaseURL:            server.URL + "/v1", APIKey: "fixture", Model: "vision-v1",
		Models: []string{"vision-v1", "other-model", "vector-model"}, ModelSpecsMode: config.LLMModelSpecsModeExplicit,
		ModelSpecs: []config.LLMProviderModelSpec{
			{ID: "vision-v1", Capabilities: []string{"text"}},
			{ID: "other-model", Capabilities: []string{"text"}},
			{ID: "vector-model", Capabilities: []string{"embedding"}},
		},
	}
	router := llmrouter.NewWithProviders(config.LLMConfig{Default: "hexclaw-gpt", Providers: map[string]config.LLMProviderConfig{"hexclaw-gpt": provider}},
		map[string]hexagon.Provider{"hexclaw-gpt": llmrouter.NewProviderFromConfig("hexclaw-gpt", provider)})
	return router, provider, store, calls
}

func saveK12VisionHTTPReceipt(t *testing.T, store storage.ModelCapabilityProbeReceiptStore, provider config.LLMProviderConfig, outcome, failureCode string) {
	t.Helper()
	saved, err := store.SaveModelCapabilityProbeReceipt(context.Background(), &storage.ModelCapabilityProbeReceipt{
		ProviderInstanceID: provider.ProviderInstanceID, ModelID: "vision-v1", ProbeKind: "vision",
		ConfigFingerprint:  api.ModelCapabilityProbeConfigFingerprint("hexclaw-gpt", provider, "vision-v1"),
		ProbePolicyVersion: api.ModelCapabilityProbePolicyVersion, Outcome: outcome, FailureCode: failureCode,
		ProbeStartedAt: 100, TestedAt: 101,
	})
	if err != nil || !saved {
		t.Fatalf("persist fixture receipt: saved=%t err=%v", saved, err)
	}
}

func k12VisionHTTPRequest(model string) llm.CompletionRequest {
	return llm.CompletionRequest{Model: model, Messages: []llm.Message{{Role: llm.RoleUser, MultiContent: []llm.ContentPart{
		llm.NewTextPart("Read this fixture image"), llm.NewImageURLPart("data:image/png;base64,iVBORw0KGgo=", "high"),
	}}}}
}

func TestK12VisionScopeVerifiedTextModelReachesCompleteAndStream(t *testing.T) {
	router, provider, store, calls := k12VisionHTTPFixture(t)
	saveK12VisionHTTPReceipt(t, store, provider, "passed", "")
	snapshot, err := resolveK12GradingModelSnapshotWithCapabilityReceipt(context.Background(), router, store, k12.GradingModelSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(k12.WithGradingModelSnapshot(context.Background(), snapshot), 5*time.Second)
	defer cancel()
	bound, model, err := resolveK12FrozenVisionCompletionRoute(ctx, router, store, "fixture")
	if err != nil || model != "vision-v1" {
		t.Fatalf("bind exact route: model=%q err=%v", model, err)
	}
	response, err := bound.Complete(ctx, k12VisionHTTPRequest(model))
	if err != nil || response == nil || response.Content != "fixture accepted" {
		t.Fatalf("completion: response=%v err=%v", response, err)
	}
	stream, err := bound.Stream(ctx, k12VisionHTTPRequest(model))
	if err != nil || stream == nil {
		t.Fatalf("stream: %v", err)
	}
	defer stream.Close()
	if _, err := stream.Collect(); err != nil {
		t.Fatalf("collect controlled stream: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("physical HTTP calls=%d, want Complete and Stream exactly once", calls.Load())
	}
	current, _ := router.ProviderConfig("hexclaw-gpt")
	if config.ModelHasCapability(current, model, "vision") || current.Model != "vision-v1" {
		t.Fatal("K12 facade changed global capability/default configuration")
	}
}

func TestK12VisionScopeRejectsReceiptFailuresBeforeBusinessHTTP(t *testing.T) {
	for _, tc := range []struct{ name, outcome, code string }{
		{"missing", "", ""}, {"failed", "failed", "PROBE_EXECUTION_FAILED"}, {"unknown_timeout", "failed", "UPSTREAM_TIMEOUT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, provider, store, calls := k12VisionHTTPFixture(t)
			if tc.outcome != "" {
				saveK12VisionHTTPReceipt(t, store, provider, tc.outcome, tc.code)
			}
			probes := 0
			_, err := resolveK12GradingModelSnapshotWithCapabilityReceipt(context.Background(), router, store, k12.GradingModelSnapshot{},
				func(context.Context, string, string, string) error {
					probes++
					return errors.New("fixture probe did not pass")
				})
			wantProbes := 0
			if tc.name == "missing" {
				wantProbes = 1
			}
			if !errors.Is(err, k12.ErrModelCapabilityUnverified) || probes != wantProbes || calls.Load() != 0 {
				t.Fatalf("unverified route escaped: err=%v probes=%d HTTP=%d", err, probes, calls.Load())
			}
		})
	}
}

func TestK12VisionScopeReloadUsesFrozenConfigWithoutSwitching(t *testing.T) {
	for _, change := range []string{"same_config", "changed_endpoint", "changed_instance", "changed_receipt"} {
		t.Run(change, func(t *testing.T) {
			router, provider, store, calls := k12VisionHTTPFixture(t)
			saveK12VisionHTTPReceipt(t, store, provider, "passed", "")
			snapshot, err := resolveK12GradingModelSnapshotWithCapabilityReceipt(context.Background(), router, store, k12.GradingModelSnapshot{})
			if err != nil {
				t.Fatal(err)
			}
			ctx := k12.WithGradingModelSnapshot(context.Background(), snapshot)
			bound, _, err := resolveK12FrozenVisionCompletionRoute(ctx, router, store, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			changed := provider
			switch change {
			case "changed_endpoint":
				changed.BaseURL += "/changed"
			case "changed_instance":
				changed.ProviderInstanceID = "pvd_v1_ffeeddccbbaa99887766554433221100"
			case "changed_receipt":
				_, err = store.SaveModelCapabilityProbeReceipt(context.Background(), &storage.ModelCapabilityProbeReceipt{
					ProviderInstanceID: provider.ProviderInstanceID, ModelID: "vision-v1", ProbeKind: "vision",
					ConfigFingerprint: snapshot.ConfigFingerprint, ProbePolicyVersion: api.ModelCapabilityProbePolicyVersion,
					Outcome: "passed", ProbeStartedAt: 200, TestedAt: 201,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := router.Reload(config.LLMConfig{Default: "hexclaw-gpt", Providers: map[string]config.LLMProviderConfig{"hexclaw-gpt": changed}}); err != nil {
				t.Fatal(err)
			}
			_, err = bound.Complete(ctx, k12VisionHTTPRequest("vision-v1"))
			if change == "same_config" {
				if err != nil || calls.Load() != 1 {
					t.Fatalf("same-config reload blocked frozen route: err=%v HTTP=%d", err, calls.Load())
				}
			} else if err == nil || calls.Load() != 0 {
				t.Fatalf("drift reached Provider: err=%v HTTP=%d", err, calls.Load())
			}
		})
	}
}

func TestK12VisionScopeRejectsWrongRouteAndUnscopedMetadata(t *testing.T) {
	router, provider, store, calls := k12VisionHTTPFixture(t)
	saveK12VisionHTTPReceipt(t, store, provider, "passed", "")
	snapshot, err := resolveK12GradingModelSnapshotWithCapabilityReceipt(context.Background(), router, store, k12.GradingModelSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{"wrong_instance", "wrong_model"} {
		changed := snapshot
		if identity == "wrong_instance" {
			changed.ProviderInstanceID = "pvd_v1_ffeeddccbbaa99887766554433221100"
		} else {
			changed.Model, changed.Route = "other-model", "hexclaw-gpt/other-model"
		}
		if _, _, err := resolveK12FrozenVisionCompletionRoute(k12.WithGradingModelSnapshot(context.Background(), changed), router, store, "fixture"); err == nil {
			t.Fatalf("%s route gained permission", identity)
		}
	}
	ctx := k12.WithGradingModelSnapshot(context.Background(), snapshot)
	bound, _, err := resolveK12FrozenVisionCompletionRoute(ctx, router, store, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bound.Complete(ctx, k12VisionHTTPRequest("other-model")); err == nil {
		t.Fatal("K12 facade accepted another model")
	}
	ordinary, _ := router.Get("hexclaw-gpt")
	request := k12VisionHTTPRequest("vision-v1")
	request.Metadata = map[string]any{"capabilities": []string{"vision"}, "purpose": "vision_ocr", "k12": true}
	if _, err := ordinary.Complete(ctx, request); !errors.Is(err, llmrouter.ErrModelCapabilityMismatch) {
		t.Fatalf("ordinary request gained scoped permission: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("rejected route performed %d physical HTTP calls", calls.Load())
	}
}

func TestK12VisionCandidateNeverReplacesEmbeddingDefault(t *testing.T) {
	router, provider, store, calls := k12VisionHTTPFixture(t)
	provider.Model = "vector-model"
	if err := router.Reload(config.LLMConfig{Default: "hexclaw-gpt", Providers: map[string]config.LLMProviderConfig{"hexclaw-gpt": provider}}); err != nil {
		t.Fatal(err)
	}
	probes := 0
	if _, err := resolveK12GradingModelSnapshotWithCapabilityReceipt(context.Background(), router, store, k12.GradingModelSnapshot{},
		func(context.Context, string, string, string) error { probes++; return nil }); err == nil {
		t.Fatal("automatic grading replaced the embedding default with another text model")
	}
	if _, err := resolveK12WorkFeedbackRouteWithCapabilityReceipt(context.Background(), router, store, k12.WorkTypeArt,
		func(context.Context, string, string, string) error { probes++; return nil }); err == nil {
		t.Fatal("automatic art feedback replaced the embedding default with another text model")
	}
	if probes != 0 || calls.Load() != 0 {
		t.Fatalf("invalid default invoked probe=%d HTTP=%d", probes, calls.Load())
	}
}

func TestK12WorkFeedbackFrozenReceiptCannotBeRebound(t *testing.T) {
	for _, replaceReceipt := range []bool{false, true} {
		t.Run(fmt.Sprintf("replace_receipt_%t", replaceReceipt), func(t *testing.T) {
			router, provider, store, calls := k12VisionHTTPFixture(t)
			saveK12VisionHTTPReceipt(t, store, provider, "passed", "")
			snapshot, err := resolveK12GradingModelSnapshotWithCapabilityReceipt(context.Background(), router, store, k12.GradingModelSnapshot{})
			if err != nil {
				t.Fatal(err)
			}
			requested := k12.ImageTaskRouteSnapshot{
				Provider: snapshot.Provider, Model: snapshot.Model, Route: snapshot.Route, Capability: snapshot.Capability,
				ProviderInstanceID: snapshot.ProviderInstanceID, ConfigFingerprint: snapshot.ConfigFingerprint,
				CapabilityReceiptDigest: snapshot.CapabilityReceiptDigest, ProbePolicyVersion: snapshot.ProbePolicyVersion,
				SelectionSource: "auto", PolicyVersion: "image-task-routing-v1", PromptVersion: "image-task-classifier-v1",
			}
			if replaceReceipt {
				requested.SelectionSource = "explicit"
				_, err := store.SaveModelCapabilityProbeReceipt(context.Background(), &storage.ModelCapabilityProbeReceipt{
					ProviderInstanceID: provider.ProviderInstanceID, ModelID: "vision-v1", ProbeKind: "vision",
					ConfigFingerprint: snapshot.ConfigFingerprint, ProbePolicyVersion: api.ModelCapabilityProbePolicyVersion,
					Outcome: "passed", ProbeStartedAt: 200, TestedAt: 201,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			probes := 0
			got, err := resolveK12RequestedWorkFeedbackRouteWithCapabilityReceipt(context.Background(), router, store, k12.WorkTypeArt, requested,
				func(context.Context, string, string, string) error { probes++; return nil })
			if replaceReceipt {
				if !errors.Is(err, k12.ErrModelCapabilityUnverified) {
					t.Fatalf("frozen feedback receipt was replaced: got=%+v err=%v", got, err)
				}
			} else if err != nil || got.Model != snapshot.Model || got.CapabilityReceiptDigest != snapshot.CapabilityReceiptDigest {
				t.Fatalf("frozen automatic classifier route was not preserved: got=%+v err=%v", got, err)
			}
			if !replaceReceipt {
				writingRequest := requested
				writingRequest.SelectionSource = "explicit"
				if _, err := resolveK12RequestedWorkFeedbackRouteWithCapabilityReceipt(context.Background(), router, store, k12.WorkTypeWriting, writingRequest); !errors.Is(err, k12.ErrModelCapabilityUnverified) {
					t.Fatalf("writing text stage reused the classifier vision receipt: %v", err)
				}
			}
			if probes != 0 || calls.Load() != 0 {
				t.Fatalf("frozen feedback invoked probe=%d HTTP=%d", probes, calls.Load())
			}
		})
	}
}
