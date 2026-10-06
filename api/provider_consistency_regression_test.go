package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/storage"
)

func providerConsistencyServer(t *testing.T, upstreamURL string, capabilities ...string) *Server {
	t.Helper()
	cfg := bug20260728ProviderConfig()
	p := cfg.LLM.Providers["custom"]
	p.BaseURL = upstreamURL + "/v1"
	p.Model = "shared-model"
	p.Models = []string{p.Model}
	p.ModelSpecs = []config.LLMProviderModelSpec{{ID: p.Model, Capabilities: capabilities}}
	cfg.LLM.Providers["custom"] = p
	return NewServer(cfg, &mockEngine{}, nil, bug20260728OpenStore(t))
}

func providerConsistencyConnection(t *testing.T, srv *Server) LLMConnectionTestResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.handleTestLLMConfig(rec, httptest.NewRequest(http.MethodPost, "/api/v1/config/llm/test", strings.NewReader(
		`{"provider":{"provider_instance_id":"`+bug20260728ProviderInstanceID+`"}}`,
	)))
	if rec.Code != http.StatusOK {
		t.Fatalf("connection status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result LLMConnectionTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestProviderConsistency_ConnectionValidatesTextResult(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		wantOK         bool
		wantFailure    string
	}{
		{"empty", `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`, false, "PROBE_EMPTY_RESPONSE"},
		{"truncated", `{"choices":[{"message":{"content":""},"finish_reason":"length"}]}`, false, "PROBE_OUTPUT_TRUNCATED"},
		{"visible response", `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			var request struct {
				Model     string `json:"model"`
				MaxTokens int    `json:"max_tokens"`
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer upstream.Close()
			srv := providerConsistencyServer(t, upstream.URL, config.LLMModelCapabilityText)
			result := providerConsistencyConnection(t, srv)
			if result.OK != tc.wantOK {
				t.Errorf("connection ok=%t, want %t for actual response", result.OK, tc.wantOK)
			}
			if request.Model != "shared-model" || request.MaxTokens != 128 {
				t.Errorf("request model=%q max_tokens=%d, want selected model and 128", request.Model, request.MaxTokens)
			}
			if calls.Load() != 1 {
				t.Errorf("physical calls=%d, want one", calls.Load())
			}
			receipt, err := srv.store.(storage.ModelCapabilityProbeReceiptStore).GetModelCapabilityProbeReceipt(
				context.Background(), bug20260728ProviderInstanceID, "shared-model", modelCapabilityProbeKindText,
			)
			if err != nil || receipt == nil {
				t.Fatalf("same-call model receipt=%#v err=%v", receipt, err)
			}
			if !result.Persisted || receipt.ProbeStartedAt != result.ProbeStartedAt || receipt.TestedAt != result.TestedAt || receipt.FailureCode != tc.wantFailure {
				t.Errorf("provider/model facts differ: result=%+v receipt=%+v", result, receipt)
			}
			wantOutcome := "failed"
			if tc.wantOK {
				wantOutcome = "passed"
			}
			if receipt.Outcome != wantOutcome {
				t.Errorf("model outcome=%q, want %q", receipt.Outcome, wantOutcome)
			}
			provider := bug20260728GetProvider(t, srv)
			model := provider["effective_models"].([]any)[0].(map[string]any)
			if !model["route_eligible"].(bool) || len(model["capabilities"].([]any)) != 1 {
				t.Fatal("probe changed declared text routing")
			}
		})
	}
}

func TestProviderConsistency_ConnectionIsolatesSameEndpointInstances(t *testing.T) {
	var chatCalls, embeddingCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/chat/completions":
			chatCalls.Add(1)
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"OK"}}]}`)
		case "/v1/embeddings":
			embeddingCalls.Add(1)
			_, _ = io.WriteString(w, `{"data":[{"embedding":[0.1,0.2]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	srv := providerConsistencyServer(t, upstream.URL, config.LLMModelCapabilityText)
	embed := srv.cfg.LLM.Providers["custom"]
	embed.ProviderInstanceID = "pvd_v1_ffeeddccbbaa99887766554433221100"
	embed.ModelSpecs = []config.LLMProviderModelSpec{{ID: "shared-model", Capabilities: []string{config.LLMModelCapabilityEmbedding}}}
	srv.cfg.LLM.Providers["vector"] = embed
	result := providerConsistencyConnection(t, srv)
	if !result.OK || chatCalls.Load() != 1 || embeddingCalls.Load() != 0 {
		t.Fatalf("text instance result=%+v chat=%d embedding=%d", result, chatCalls.Load(), embeddingCalls.Load())
	}
}

func TestProviderConsistency_UnknownInstanceDoesNotBorrowEmbeddingDeclaration(t *testing.T) {
	var chatCalls, embeddingCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			chatCalls.Add(1)
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"OK"}}]}`)
		} else {
			embeddingCalls.Add(1)
			_, _ = io.WriteString(w, `{"data":[{"embedding":[0.1,0.2]}]}`)
		}
	}))
	defer upstream.Close()
	srv := providerConsistencyServer(t, upstream.URL, config.LLMModelCapabilityEmbedding)
	body, _ := json.Marshal(LLMConnectionTestRequest{Provider: llmConnectionTestProvider{
		ProviderInstanceID: "pvd_v1_ffeeddccbbaa99887766554433221100",
		Type:               "openai", BaseURL: upstream.URL + "/v1", APIKey: "sk-temporary-fixture", Model: "shared-model",
	}})
	rec := httptest.NewRecorder()
	srv.handleTestLLMConfig(rec, httptest.NewRequest(http.MethodPost, "/api/v1/config/llm/test", strings.NewReader(string(body))))
	var result LLMConnectionTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || !result.OK || result.Persisted || chatCalls.Load() != 1 || embeddingCalls.Load() != 0 {
		t.Fatalf("unknown instance result=%+v chat=%d embedding=%d", result, chatCalls.Load(), embeddingCalls.Load())
	}
}

func TestProviderConsistency_OllamaTargetInvalidatesBothReceipts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"OK"},"done":true,"done_reason":"stop"}`)
	}))
	defer upstream.Close()
	srv := providerConsistencyServer(t, upstream.URL, config.LLMModelCapabilityText)
	p := srv.cfg.LLM.Providers["custom"]
	p.Compatible = "ollama"
	p.BaseURL = upstream.URL
	p.OllamaTargetBaseURL = upstream.URL
	srv.cfg.LLM.Providers["custom"] = p
	result := providerConsistencyConnection(t, srv)
	if !result.OK || !result.Persisted {
		t.Fatalf("original target connection=%+v", result)
	}
	provider := bug20260728GetProvider(t, srv)
	if provider["probe_receipt"] == nil {
		t.Fatal("original target receipt missing")
	}
	model := provider["effective_models"].([]any)[0].(map[string]any)
	if len(model["probe_receipts"].([]any)) != 1 {
		t.Fatal("original target model receipt missing")
	}
	// 保持显示地址不变，仅切换真正执行的 Ollama 宿主。
	p.OllamaTargetBaseURL = "http://localhost:11435"
	srv.cfg.LLM.Providers["custom"] = p
	provider = bug20260728GetProvider(t, srv)
	if provider["probe_receipt"] != nil {
		t.Error("old target provider receipt remained visible")
	}
	model = provider["effective_models"].([]any)[0].(map[string]any)
	if len(model["probe_receipts"].([]any)) != 0 {
		t.Error("old target model receipt remained visible")
	}
}

func TestProviderConsistency_ProbeDoesNotReplayPhysicalRequests(t *testing.T) {
	for _, tc := range []struct {
		name, entry, kind string
		status            int
		body              string
	}{
		{"connection text 429", "connection", "text", http.StatusTooManyRequests, `{"error":{"message":"limited","code":429}}`},
		{"connection embedding 429", "connection", "embedding", http.StatusTooManyRequests, `{"error":{"message":"limited","code":429}}`},
		{"vision incomplete JSON", "capability", "vision", http.StatusOK, `{"choices":`},
		{"tools provider unavailable", "capability", "tools", http.StatusServiceUnavailable, `{"error":{"message":"unavailable","code":503}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			capability := config.LLMModelCapabilityText
			if tc.kind == modelCapabilityProbeKindEmbedding {
				capability = config.LLMModelCapabilityEmbedding
			}
			srv := providerConsistencyServer(t, upstream.URL, capability)
			if tc.entry == "connection" {
				result := providerConsistencyConnection(t, srv)
				if result.OK {
					t.Fatal("upstream failure claimed success")
				}
			} else {
				body, _ := json.Marshal(LLMModelCapabilityProbeRequest{
					ProviderInstanceID: bug20260728ProviderInstanceID,
					Model:              "shared-model", Kinds: []string{tc.kind},
				})
				rec := httptest.NewRecorder()
				srv.handleProbeModelCapability(rec, httptest.NewRequest(http.MethodPost, "/api/v1/config/llm/probe", strings.NewReader(string(body))))
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"outcome":"failed"`) {
					t.Fatalf("capability status=%d body=%s", rec.Code, rec.Body.String())
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("physical model requests=%d, want exactly one", calls.Load())
			}
		})
	}
}

func TestProviderConsistency_ConnectionFailureSavesRateLimitedModelFact(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"Provider returned error","code":429}}`)
	}))
	defer upstream.Close()
	srv := providerConsistencyServer(t, upstream.URL, config.LLMModelCapabilityText)
	result := providerConsistencyConnection(t, srv)
	receipt, err := srv.store.(storage.ModelCapabilityProbeReceiptStore).GetModelCapabilityProbeReceipt(
		context.Background(), bug20260728ProviderInstanceID, "shared-model", modelCapabilityProbeKindText,
	)
	if result.OK || !result.Persisted || err != nil || receipt == nil {
		t.Fatalf("failed result=%+v model receipt=%+v err=%v", result, receipt, err)
	}
	if calls.Load() != 1 || receipt.FailureCode != string(llmrouter.LLMErrorCodeUpstreamRateLimited) || receipt.ProbeStartedAt != result.ProbeStartedAt {
		t.Fatalf("rate limited fact=%+v calls=%d", receipt, calls.Load())
	}
}

func TestProviderConsistency_ConnectionStaleConfigDoesNotSaveEitherFact(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer upstream.Close()
	srv := providerConsistencyServer(t, upstream.URL, config.LLMModelCapabilityText)
	resultCh := make(chan LLMConnectionTestResponse, 1)
	go func() { resultCh <- providerConsistencyConnection(t, srv) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("connection probe did not reach local provider")
	}
	srv.cfgMu.Lock()
	p := srv.cfg.LLM.Providers["custom"]
	p.APIKey = "sk-new-local-fixture"
	srv.cfg.LLM.Providers["custom"] = p
	srv.cfgMu.Unlock()
	close(release)
	var result LLMConnectionTestResponse
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("stale connection probe did not finish")
	}
	if !result.OK || result.Persisted {
		t.Fatalf("stale execution result=%+v, want non-persisted success", result)
	}
	providerReceipt, err := srv.store.(storage.ProviderProbeReceiptStore).GetProviderProbeReceipt(context.Background(), bug20260728ProviderInstanceID)
	if err != nil || providerReceipt != nil {
		t.Fatalf("stale provider receipt=%+v err=%v", providerReceipt, err)
	}
	modelReceipt, err := srv.store.(storage.ModelCapabilityProbeReceiptStore).GetModelCapabilityProbeReceipt(context.Background(), bug20260728ProviderInstanceID, "shared-model", modelCapabilityProbeKindText)
	if err != nil || modelReceipt != nil {
		t.Fatalf("stale model receipt=%+v err=%v", modelReceipt, err)
	}
}
