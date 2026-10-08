package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/storage"
)

func nativeReasoningReadConfig(t *testing.T, srv *Server) LLMConfigResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.handleGetLLMConfig(rec, httptest.NewRequest(http.MethodGet, "/api/v1/config/llm", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result LLMConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func nativeReasoningWriteConfig(t *testing.T, srv *Server, key string, provider config.LLMProviderConfig) {
	t.Helper()
	specs := provider.ModelSpecs
	body, err := json.Marshal(LLMConfigUpdateRequest{Default: key, Providers: map[string]LLMProviderConfigUpdateItem{key: {
		ProviderInstanceID: provider.ProviderInstanceID, APIKey: provider.APIKey, BaseURL: provider.BaseURL,
		Model: provider.Model, Models: provider.Models, Compatible: provider.Compatible, Locality: provider.Locality,
		ModelSpecs: &specs,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.handleUpdateLLMConfig(rec, httptest.NewRequest(http.MethodPut, "/api/v1/config/llm", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNativeReasoningCatalogExplicitValuesAndSourceBinding(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	entries := []string{
		`{"id":"true","supports_reasoning":true}`, `{"id":"false","supports_reasoning":false}`,
		`{"id":"supported","native_reasoning_support":"supported"}`, `{"id":"unsupported","native_reasoning_support":"unsupported"}`,
		`{"id":"unknown","native_reasoning_support":"unknown","supports_reasoning":true}`,
		`{"id":"invalid","native_reasoning_support":12,"supports_reasoning":true}`,
		`{"id":"absent"}`, `{"id":"invalid_bool","supports_reasoning":"true"}`,
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[`+strings.Join(entries, ",")+`]}`)
	}))
	defer upstream.Close()
	cfg := bug20260728ProviderConfig()
	provider := cfg.LLM.Providers["custom"]
	provider.BaseURL = upstream.URL + "/v1"
	cfg.LLM.Providers["custom"] = provider
	srv := NewServer(cfg, &mockEngine{activeLLM: cfg.LLM}, nil, nil)
	rec := httptest.NewRecorder()
	srv.handleFetchProviderModels(rec, httptest.NewRequest(http.MethodPost, "/api/v1/config/llm/models", strings.NewReader(`{"provider_instance_id":"`+provider.ProviderInstanceID+`"}`)))
	var response struct {
		Models []providerModelInfo `json:"models"`
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	wants := []string{"supported", "unsupported", "supported", "unsupported", "unknown", "unknown", "", "unknown"}
	if len(response.Models) != len(wants) {
		t.Fatalf("model count=%d", len(response.Models))
	}
	for i, model := range response.Models {
		if model.NativeReasoningSupport != wants[i] || len(model.NativeReasoningSourceFingerprint) != 64 || model.ReasoningSupport != "" || model.ReasoningControl != nil {
			t.Fatalf("catalog model=%+v want native=%q independent control", model, wants[i])
		}
	}
	if strings.Contains(rec.Body.String(), provider.APIKey) {
		t.Fatal("catalog leaked key")
	}
}

func TestNativeReasoningSavedDeclarationInvalidatesOnlyChangedPhysicalSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*config.LLMProviderConfig)
		key    string
		want   string
	}{
		{"same", func(*config.LLMProviderConfig) {}, "custom", "supported"},
		{"rename", func(*config.LLMProviderConfig) {}, "renamed", "supported"},
		{"rename_custom_untyped", func(p *config.LLMProviderConfig) { p.Compatible = "" }, "bar", "supported"},
		{"normalized_url", func(p *config.LLMProviderConfig) { p.BaseURL += "/" }, "custom", "supported"},
		{"changed_default_model", func(p *config.LLMProviderConfig) {
			p.Model = "another-model"
			p.Models = append(p.Models, p.Model)
			p.ModelSpecs = append(p.ModelSpecs, config.LLMProviderModelSpec{ID: p.Model, Capabilities: []string{"text"}})
		}, "custom", "supported"},
		{"changed_address", func(p *config.LLMProviderConfig) { p.BaseURL = "https://different.example.test/v1" }, "custom", "unknown"},
		{"changed_key", func(p *config.LLMProviderConfig) { p.APIKey = "test-only-new-key" }, "custom", "unknown"},
		{"changed_instance", func(p *config.LLMProviderConfig) { p.ProviderInstanceID = "pvd_v1_ffeeddccbbaa99887766554433221100" }, "replacement", "unknown"},
		{"changed_model", func(p *config.LLMProviderConfig) {
			p.Model = "another-model"
			p.Models = []string{p.Model}
			p.ModelSpecs[0].ID = p.Model
		}, "custom", "unknown"},
		{"missing_source", func(p *config.LLMProviderConfig) { p.ModelSpecs[0].NativeReasoningSourceFingerprint = "" }, "custom", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			cfg := bug20260728ProviderConfig()
			provider := cfg.LLM.Providers["custom"]
			if tc.name == "rename_custom_untyped" {
				provider.Compatible = ""
			}
			provider.ModelSpecs[0].NativeReasoningSupport = "supported"
			provider.ModelSpecs[0].NativeReasoningSourceFingerprint = nativeReasoningSourceFingerprint("custom", provider, provider.Model)
			cfg.LLM.Providers["custom"] = provider
			srv := NewServer(cfg, &mockEngine{activeLLM: cfg.LLM}, nil, nil)
			before := nativeReasoningReadConfig(t, srv).Providers["custom"]
			provider.ModelSpecs = before.ModelSpecs
			tc.change(&provider)
			nativeReasoningWriteConfig(t, srv, tc.key, provider)
			after := nativeReasoningReadConfig(t, srv).Providers[tc.key]
			if after.ModelSpecs[0].NativeReasoningSupport != tc.want || after.EffectiveModels[0].EffectiveNativeReasoningSupport != tc.want {
				t.Fatalf("native after source change=%+v", after)
			}
			if after.ModelSpecs[0].ReasoningSupport != "unknown" || after.ModelSpecs[0].ReasoningControl != nil || !after.EffectiveModels[0].RouteEligible {
				t.Fatalf("native source invalidation changed text/control: %+v", after)
			}
			if after.EffectiveModels[0].Availability != "unknown" {
				t.Fatal("native declaration manufactured readiness")
			}
			if tc.want == "supported" && before.NativeReasoningSourceFingerprint != after.NativeReasoningSourceFingerprint {
				t.Fatal("display-only change invalidated provider source")
			}
			if tc.want == "supported" {
				persisted := srv.persistedLLMConfig().Providers[tc.key]
				probeFingerprint := ModelCapabilityProbeConfigFingerprint(tc.key, persisted, persisted.ModelSpecs[0].ID)
				persisted.ModelSpecs[0].NativeReasoningSupport = "unsupported"
				if got := ModelCapabilityProbeConfigFingerprint(tc.key, persisted, persisted.ModelSpecs[0].ID); got != probeFingerprint {
					t.Fatal("native declaration invalidated v4 probe")
				}
				provider = srv.persistedLLMConfig().Providers[tc.key]
				provider.ModelSpecs[0].NativeReasoningSupport = ""
				provider.ModelSpecs[0].NativeReasoningSourceFingerprint = ""
				nativeReasoningWriteConfig(t, srv, tc.key, provider)
				if got := nativeReasoningReadConfig(t, srv).Providers[tc.key].ModelSpecs[0].NativeReasoningSupport; got != "supported" {
					t.Fatalf("omission erased same-source declaration: %q", got)
				}
			}
		})
	}
}

func TestNativeReasoningProbePositiveUsesCompleteStructuredResponseOnce(t *testing.T) {
	for _, tc := range []struct{ name, content, finish string }{
		{"normal", "x=11,y=6", "stop"}, {"length_empty_content", "", "length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if r.URL.Path != "/v1/chat/completions" || body["model"] != "gpt-5.6-terra" || body["max_tokens"] != float64(1024) || body["stream"] != false {
					t.Errorf("unexpected wire: %v path=%s", body, r.URL.Path)
				}
				messages, ok := body["messages"].([]any)
				if !ok || len(messages) != 1 {
					t.Errorf("native probe messages=%v", body["messages"])
				} else {
					message, ok := messages[0].(map[string]any)
					if !ok || message["role"] != "user" || message["content"] != "请求出满足 x+y=17、2x−3y=4 的 x 和 y，只输出最终答案。" {
						t.Errorf("native probe message=%v", messages[0])
					}
				}
				for _, key := range []string{"reasoning_effort", "enable_thinking", "think", "thinking", "tools", "response_format"} {
					if _, exists := body[key]; exists {
						t.Errorf("native probe injected %s", key)
					}
				}
				_, _ = fmt.Fprintf(w, `{"id":"response","model":"canonical-provider-model","choices":[{"message":{"role":"assistant","content":%q},"finish_reason":%q}],"usage":{"completion_tokens_details":{"reasoning_tokens":37}}}`, tc.content, tc.finish)
			}))
			defer upstream.Close()
			store := bug20260728OpenStore(t)
			cfg := bug20260728ProviderConfig()
			p := cfg.LLM.Providers["custom"]
			p.BaseURL = upstream.URL + "/v1"
			p.Compatible = ""
			p.Model = "gpt-5.6-terra"
			p.Models = []string{p.Model}
			p.ModelSpecs = []config.LLMProviderModelSpec{{ID: p.Model, Capabilities: []string{"text"}}}
			cfg.LLM.Providers["custom"] = p
			srv := NewServer(cfg, &mockEngine{activeLLM: cfg.LLM}, nil, store)
			srv.SetSidecarCapabilityToken("model-probe-capability-token")
			rec := runModelCapabilityProbe(t, srv, "reasoning")
			if rec.Code != http.StatusOK {
				t.Fatalf("probe status=%d body=%s", rec.Code, rec.Body.String())
			}
			var result LLMModelCapabilityProbeResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || len(result.Results) != 1 || result.Results[0].Outcome != "passed" || !result.Results[0].Persisted || result.Results[0].ReasoningTokens != 37 || result.Results[0].ReportedModel != "canonical-provider-model" || result.Results[0].ProbePolicyVersion != "native-reasoning-v1" {
				t.Fatalf("native positive result=%+v calls=%d", result, calls.Load())
			}
			stored, err := store.GetModelCapabilityProbeReceipt(context.Background(), p.ProviderInstanceID, p.Model, "reasoning")
			if err != nil || stored == nil || stored.ModelID != p.Model || stored.ConfigFingerprint != nativeReasoningSourceFingerprint("custom", p, p.Model) {
				t.Fatalf("wrong requested model identity: %+v err=%v", stored, err)
			}
			read := nativeReasoningReadConfig(t, srv).Providers["custom"]
			if read.EffectiveModels[0].EffectiveNativeReasoningSupport != "supported" || read.EffectiveModels[0].Availability != "unknown" || read.ModelSpecs[0].NativeReasoningSupport != "unknown" || read.ModelSpecs[0].ReasoningSupport != "unknown" || read.ModelSpecs[0].ReasoningControl != nil {
				t.Fatalf("positive polluted static/readiness/control: %+v", read)
			}
			nativeReasoningWriteConfig(t, srv, "renamed-untyped", p)
			if after := nativeReasoningReadConfig(t, srv).Providers["renamed-untyped"]; after.EffectiveModels[0].EffectiveNativeReasoningSupport != "supported" || after.ModelSpecs[0].NativeReasoningSupport != "unknown" || calls.Load() != 1 {
				t.Fatalf("rename lost native receipt or repeated probe: %+v calls=%d", after, calls.Load())
			}
			p.APIKey = "changed-test-key"
			if model := srv.effectiveModelsForProvider(context.Background(), "custom", p)[0]; model.EffectiveNativeReasoningSupport != "unknown" {
				t.Fatalf("old positive crossed key scope: %+v", model)
			}
		})
	}
}

func TestNativeReasoningProbeNonPositiveAndErrorsNeverDisableTextOrRepeat(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		disconnect bool
	}{
		{"missing", 200, `{"choices":[{"message":{"content":"I am reasoning"}}]}`, false},
		{"zero", 200, `{"choices":[{"message":{"content":"OK"}}],"usage":{"completion_tokens_details":{"reasoning_tokens":0}}}`, false},
		{"negative", 200, `{"choices":[{"message":{"content":"OK"}}],"usage":{"completion_tokens_details":{"reasoning_tokens":-2}}}`, false},
		{"empty_body", 200, "", false}, {"invalid_json", 200, "{invalid", false},
		{"rate_limited", 429, `{"error":{"message":"limited"}}`, false}, {"unavailable", 503, `{"error":{"message":"unavailable"}}`, false},
		{"interrupted", 200, `{"usage":{"completion_tokens_details":{"reasoning_tokens":37}}`, true},
		{"connection_closed", 200, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if tc.disconnect {
					conn, buf, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					if tc.body != "" {
						_, _ = fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\n%s", tc.body)
					}
					_ = buf.Flush()
					_ = conn.Close()
					return
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			store := bug20260728OpenStore(t)
			cfg := bug20260728ProviderConfig()
			p := cfg.LLM.Providers["custom"]
			p.BaseURL = upstream.URL + "/v1"
			p.Model = "gpt-5.6-terra"
			p.Models = []string{p.Model}
			p.ModelSpecs = []config.LLMProviderModelSpec{{ID: p.Model, Capabilities: []string{"text"}}}
			cfg.LLM.Providers["custom"] = p
			_, err := store.SaveModelCapabilityProbeReceipt(context.Background(), &storage.ModelCapabilityProbeReceipt{ProviderInstanceID: p.ProviderInstanceID, ModelID: p.Model, ProbeKind: "text", ConfigFingerprint: ModelCapabilityProbeConfigFingerprint("custom", p, p.Model), ProbePolicyVersion: "v4", Outcome: "passed", TestedAt: 1, ProbeStartedAt: 1})
			if err != nil {
				t.Fatal(err)
			}
			srv := NewServer(cfg, &mockEngine{activeLLM: cfg.LLM}, nil, store)
			srv.SetSidecarCapabilityToken("model-probe-capability-token")
			rec := runModelCapabilityProbe(t, srv, "reasoning")
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var result LLMModelCapabilityProbeResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || len(result.Results) != 1 || result.Results[0].Outcome != "failed" || result.Results[0].ReasoningTokens != 0 {
				t.Fatalf("failure repeated or claimed positive: %+v calls=%d", result, calls.Load())
			}
			read := nativeReasoningReadConfig(t, srv).Providers["custom"]
			if read.EffectiveModels[0].EffectiveNativeReasoningSupport != "unknown" || read.EffectiveModels[0].Availability != "ready" || !read.EffectiveModels[0].RouteEligible || read.ModelSpecs[0].ReasoningControl != nil {
				t.Fatalf("reasoning failure disabled text/control: %+v", read)
			}
		})
	}
}

func TestNativeReasoningProbeCanceledOrChangedSourceCannotPublishPositive(t *testing.T) {
	for _, mode := range []string{"canceled", "changed_key"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int64
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				close(started)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				_, _ = io.WriteString(w, `{"model":"canonical","choices":[{"message":{"content":"OK"}}],"usage":{"completion_tokens_details":{"reasoning_tokens":37}}}`)
			}))
			defer upstream.Close()
			store := bug20260728OpenStore(t)
			cfg := bug20260728ProviderConfig()
			p := cfg.LLM.Providers["custom"]
			p.BaseURL = upstream.URL + "/v1"
			cfg.LLM.Providers["custom"] = p
			srv := NewServer(cfg, &mockEngine{activeLLM: cfg.LLM}, nil, store)
			candidate, err := srv.modelCapabilityProbeCandidate(p.ProviderInstanceID, p.Model)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				result LLMModelCapabilityProbeResult
				err    error
			}
			done := make(chan completion, 1)
			go func() {
				result, err := srv.executeAndPersistModelCapabilityProbe(ctx, candidate, "reasoning")
				done <- completion{result, err}
			}()
			<-started
			if mode == "canceled" {
				cancel()
			} else {
				srv.cfgMu.Lock()
				changed := srv.cfg.LLM.Providers["custom"]
				changed.APIKey = "test-only-changed"
				srv.cfg.LLM.Providers["custom"] = changed
				srv.cfgMu.Unlock()
			}
			once.Do(func() { close(release) })
			completed := <-done
			if calls.Load() != 1 || completed.result.Persisted || completed.err == nil {
				t.Fatalf("late result persisted/repeated: %+v calls=%d", completed, calls.Load())
			}
			receipt, err := store.GetModelCapabilityProbeReceipt(context.Background(), p.ProviderInstanceID, p.Model, "reasoning")
			if err != nil || receipt != nil {
				t.Fatalf("rejected completion wrote receipt: %+v err=%v", receipt, err)
			}
		})
	}
}

func TestNativeReasoningDoesNotChangeOrdinaryOrFrozenReasoningWire(t *testing.T) {
	for _, tc := range []struct {
		name       string
		metadata   map[string]any
		scope      llm.ReasoningPolicyScope
		wantEffort any
	}{
		{"ordinary", nil, "", nil},
		{"native_unknown_control", map[string]any{llm.ReasoningCapabilityMetadataKey: llm.ReasoningCapability{Support: llm.ReasoningUnknown}, "thinking": false}, "", nil},
		{"explicit_control_off", map[string]any{llm.ReasoningCapabilityMetadataKey: llm.ReasoningCapability{Support: llm.ReasoningSupported, Dialect: llm.ReasoningDialectEffort, OnValue: "high", OffValue: "none"}, "thinking": false}, "", "none"},
		{"explicit_control_on", map[string]any{llm.ReasoningCapabilityMetadataKey: llm.ReasoningCapability{Support: llm.ReasoningSupported, Dialect: llm.ReasoningDialectEffort, OnValue: "high", OffValue: "none"}, "thinking": true}, "", "high"},
		{"k12_frozen_off", map[string]any{"thinking": "off"}, llm.ReasoningPolicyScopeStructuredVisionRecognition, "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`)
			}))
			defer upstream.Close()
			p := llmrouter.NewProviderFromConfig("custom", config.LLMProviderConfig{BaseURL: upstream.URL, APIKey: "test-only", Model: "gpt-5.6-sol", ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: []config.LLMProviderModelSpec{{ID: "gpt-5.6-sol", Capabilities: []string{"text"}, NativeReasoningSupport: "supported"}}})
			_, err := p.Complete(context.Background(), hexagon.CompletionRequest{Model: "gpt-5.6-sol", Messages: []hexagon.Message{{Role: "user", Content: "test"}}, Metadata: tc.metadata, ReasoningPolicyScope: tc.scope})
			if err != nil {
				t.Fatal(err)
			}
			if payload["reasoning_effort"] != tc.wantEffort {
				t.Fatalf("wire effort=%v want=%v body=%v", payload["reasoning_effort"], tc.wantEffort, payload)
			}
			for _, key := range []string{"native_reasoning_support", "native_reasoning_source_fingerprint", "enable_thinking", "thinking", "think"} {
				if _, exists := payload[key]; exists {
					t.Fatalf("native declaration leaked wire %s", key)
				}
			}
		})
	}
}
