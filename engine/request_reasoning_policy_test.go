package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
)

func requestReasoningHTTPEngine(t *testing.T, handler http.HandlerFunc, policy config.ReasoningPolicy, controlKnown bool) (*ReActEngine, *config.LLMReasoningControlSpec) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	control := &config.LLMReasoningControlSpec{Dialect: "reasoning_effort", On: "high", Off: "none", AllowedEfforts: []string{"low", "medium", "high"}}
	provider := config.LLMProviderConfig{ProviderInstanceID: "pvd_v1_00112233445566778899aabbccddeeff", BaseURL: server.URL, APIKey: "test-only", Model: "exact-model", Compatible: "openai", ModelSpecsMode: "explicit", Models: []string{"exact-model"}, ModelSpecs: []config.LLMProviderModelSpec{{ID: "exact-model", Capabilities: []string{"text"}, NativeReasoningSupport: "supported"}}}
	if controlKnown {
		provider.ModelSpecs[0].ReasoningSupport = "supported"
		provider.ModelSpecs[0].ReasoningControl = control
	}
	eng := newEngineWithProviders(t, map[string]hexagon.Provider{"custom": llmrouter.NewProviderFromConfig("custom", provider)}, map[string]config.LLMProviderConfig{"custom": provider}, "custom")
	next := eng.ActiveLLMConfig()
	next.DefaultReasoningPolicy = policy
	next.Cache.Enabled = false
	if err := eng.ReloadLLMConfig(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	return eng, control
}

func TestRequestReasoningPolicyFreezeUsesAcceptanceSnapshotAndNextRequestReload(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]any
	eng, _ := requestReasoningHTTPEngine(t, func(w http.ResponseWriter, r *http.Request) {
		body := requestReasoningServeResponse(t, w, r)
		mu.Lock()
		requests = append(requests, body)
		mu.Unlock()
	}, config.ReasoningPolicy{Mode: "on"}, true)
	ctx := eng.beginRequestReasoningPolicy(context.Background())
	next := eng.ActiveLLMConfig()
	next.DefaultReasoningPolicy = config.ReasoningPolicy{Mode: "off"}
	if err := eng.ReloadLLMConfig(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	msg := &adapter.Message{ID: "accepted-old", Platform: adapter.PlatformAPI, UserID: "policy-owner", Content: "请解释整数的意义。", Metadata: map[string]string{}}
	selection, err := eng.resolveLLMSelection(ctx, msg)
	if err != nil {
		t.Fatal(err)
	}
	eng.freezeSelectedRequestReasoningPolicy(ctx, msg, selection)
	cacheBefore := buildLLMCacheInput(msg)
	req := eng.buildCompletionRequest(ctx, msg, nil, "")
	applyPerTurnRequestPolicy(ctx, &req, selection.modelName, eng.visionRoutingStrategy(), msg, nil)
	if _, err := selection.provider.Complete(ctx, req); err != nil {
		t.Fatal(err)
	}
	freezeRequestReasoningPolicy(ctx, msg)
	if msg.Metadata["thinking"] != "on" || cacheBefore != buildLLMCacheInput(msg) {
		t.Fatal("inflight policy or cache identity changed after reload")
	}
	requestReasoningSend(t, eng, &adapter.Message{ID: "next-request", Platform: adapter.PlatformAPI, UserID: "policy-owner", Content: "请解释整数的意义。", Metadata: map[string]string{}}, false)
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0]["reasoning_effort"] != "high" || requests[1]["reasoning_effort"] != "none" {
		t.Fatalf("freeze/reload wire=%v", requests)
	}
	if eng.ActiveLLMConfig().DefaultReasoningPolicy.Mode != "off" {
		t.Fatal("request rewrote saved policy")
	}
}

func TestRequestReasoningPolicyAutoOverridesLegacyDefaultsAndFrozenStageWins(t *testing.T) {
	for _, stage := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("stage_%t/explicit_%t", stage, explicit), func(t *testing.T) {
				var payload map[string]any
				eng, control := requestReasoningHTTPEngine(t, func(w http.ResponseWriter, r *http.Request) { payload = requestReasoningServeResponse(t, w, r) }, config.ReasoningPolicy{Mode: "off"}, true)
				cfg := eng.ActiveLLMConfig()
				if !explicit {
					cfg.DefaultReasoningPolicy = config.ReasoningPolicy{}
				}
				p := cfg.Providers["custom"]
				p.Model = "qwen3-policy-fixture"
				p.Models = []string{p.Model}
				p.ModelSpecs[0].ID = p.Model
				cfg.Providers["custom"] = p
				if err := eng.ReloadLLMConfig(context.Background(), cfg); err != nil {
					t.Fatal(err)
				}
				metadata := map[string]string{}
				if explicit {
					metadata["thinking"] = "auto"
				}
				msg := &adapter.Message{Metadata: metadata, Content: "test"}
				ctx := eng.beginRequestReasoningPolicy(context.Background())
				freezeRequestReasoningPolicy(ctx, msg)
				req := hexagon.CompletionRequest{Model: p.Model, Messages: []hexagon.Message{{Role: "user", Content: "test"}}, Metadata: map[string]any{llm.ReasoningCapabilityMetadataKey: llm.ReasoningCapability{Support: llm.ReasoningSupported, Dialect: llm.ReasoningDialectEffort, OnValue: control.On, OffValue: control.Off}}}
				if stage {
					req.ReasoningPolicyScope = llm.ReasoningPolicyScopeStructuredVisionRecognition
					req.Metadata["thinking"] = "off"
					req.Metadata["expected_reasoning_effort"] = "none"
				}
				before := buildLLMCacheInput(msg)
				applyPerTurnRequestPolicy(ctx, &req, p.Model, eng.visionRoutingStrategy(), msg, nil)
				if !stage && shouldInjectNoThink(true, false, msg.Metadata["thinking"], p.Model) {
					t.Fatal("auto was suppressed by legacy prompt policy")
				}
				provider, ok := eng.router.Get("custom")
				if !ok {
					t.Fatal("provider unavailable")
				}
				if _, err := provider.Complete(ctx, req); err != nil {
					t.Fatal(err)
				}
				want := any(nil)
				if stage {
					want = "none"
				}
				if payload["reasoning_effort"] != want || msg.Metadata["thinking"] != "auto" || before != buildLLMCacheInput(msg) {
					t.Fatalf("auto/stage wire=%v msg=%v", payload, msg.Metadata)
				}
				if !stage {
					metadata = map[string]string{}
					if explicit {
						metadata["thinking"] = "auto"
					}
					requestReasoningSend(t, eng, &adapter.Message{ID: "auto-public", Platform: adapter.PlatformAPI, UserID: "policy-owner", Content: "请解释整数的意义。", Metadata: metadata}, false)
					if _, exists := payload["reasoning_effort"]; exists {
						t.Fatalf("auto public request sent control: %v", payload)
					}
					messages, _ := json.Marshal(payload["messages"])
					if strings.Contains(string(messages), "/no_think") {
						t.Fatal("auto public request suppressed native reasoning with prompt")
					}
				}
			})
		}
	}
}

func TestRequestReasoningPolicyInheritedUnavailableEffortIsEffectiveAutoButExplicitRemainsStrict(t *testing.T) {
	for _, dialect := range []string{"reasoning_effort", "think"} {
		for _, agentPolicy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/agent_%t", dialect, agentPolicy), func(t *testing.T) {
				var payload map[string]any
				eng, _ := requestReasoningHTTPEngine(t, func(w http.ResponseWriter, r *http.Request) { payload = requestReasoningServeResponse(t, w, r) }, config.ReasoningPolicy{Mode: "effort", Effort: "high"}, true)
				cfg := eng.ActiveLLMConfig()
				p := cfg.Providers["custom"]
				if dialect == "think" {
					p.ModelSpecs[0].ReasoningControl = &config.LLMReasoningControlSpec{Dialect: "think", On: true, Off: false}
				} else {
					p.ModelSpecs[0].ReasoningControl = &config.LLMReasoningControlSpec{Dialect: "reasoning_effort", On: "low", Off: "none", AllowedEfforts: []string{"low"}}
				}
				cfg.Providers["custom"] = p
				if err := eng.ReloadLLMConfig(context.Background(), cfg); err != nil {
					t.Fatal(err)
				}
				metadata := map[string]string{}
				if agentPolicy {
					router := agentrouter.New()
					if err := router.Register(agentrouter.AgentConfig{Name: "policy-agent", DisplayName: "Policy agent", Provider: "custom", Model: p.Model, ReasoningPolicy: &config.ReasoningPolicy{Mode: "effort", Effort: "high"}}); err != nil {
						t.Fatal(err)
					}
					eng.SetAgentRouter(router)
					metadata["pinned_agent"] = "policy-agent"
				}
				requestReasoningSend(t, eng, &adapter.Message{ID: "inherited-high", Platform: adapter.PlatformDingtalk, UserID: "policy-owner", Content: "请解释整数的意义。", Metadata: metadata}, false)
				for _, key := range []string{"reasoning_effort", "think", "thinking", "enable_thinking"} {
					if _, exists := payload[key]; exists {
						t.Fatalf("unavailable inherited effort sent %s: %v", key, payload)
					}
				}
				if eng.ActiveLLMConfig().DefaultReasoningPolicy.Effort != "high" {
					t.Fatal("effective projection rewrote global preference")
				}
				if agentPolicy {
					stored, _ := eng.getAgentRouter().GetAgent("policy-agent")
					if stored.ReasoningPolicy.Effort != "high" {
						t.Fatal("effective projection rewrote agent preference")
					}
				}
				if dialect == "reasoning_effort" {
					payload = nil
					_, err := eng.Process(context.Background(), &adapter.Message{ID: "explicit-high", Platform: adapter.PlatformAPI, UserID: "policy-owner", Content: "请解释整数的意义。", Metadata: map[string]string{"thinking": "on", "thinking_effort": "high"}})
					if err == nil || payload != nil {
						t.Fatalf("explicit illegal effort no longer strict: err=%v payload=%v", err, payload)
					}
					provider, found := eng.router.Get("custom")
					if !found {
						t.Fatal("provider unavailable")
					}
					_, err = provider.Complete(context.Background(), hexagon.CompletionRequest{Model: p.Model, Metadata: map[string]any{"thinking": "on", "thinking_effort": "high"}, Messages: []hexagon.Message{{Role: "user", Content: "test"}}})
					if err == nil || !strings.Contains(err.Error(), "not allowed") || payload != nil {
						t.Fatalf("provider explicit effort contract changed: err=%v payload=%v", err, payload)
					}
				} else {
					payload = nil
					requestReasoningSend(t, eng, &adapter.Message{ID: "explicit-boolean", Platform: adapter.PlatformAPI, UserID: "policy-owner", Content: "请解释整数的意义。", Metadata: map[string]string{"thinking": "on", "thinking_effort": "high"}}, false)
					if payload["think"] != true {
						t.Fatalf("existing boolean explicit-on contract changed: %v", payload)
					}
				}
			})
		}
	}
}

func requestReasoningServeResponse(t *testing.T, w http.ResponseWriter, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Error(err)
		return nil
	}
	if streamed, _ := body["stream"].(bool); streamed {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	} else {
		_, _ = io.WriteString(w, `{"model":"exact-model","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
	}
	return body
}

func requestReasoningSend(t *testing.T, eng *ReActEngine, msg *adapter.Message, streaming bool) {
	t.Helper()
	if streaming {
		chunks, err := eng.ProcessStream(context.Background(), msg)
		if err != nil {
			t.Fatal(err)
		}
		var content strings.Builder
		for chunk := range chunks {
			if chunk.Error != nil {
				t.Fatal(chunk.Error)
			}
			content.WriteString(chunk.Content)
		}
		if content.String() != "OK" {
			t.Fatalf("stream content=%q", content.String())
		}
	} else {
		reply, err := eng.Process(context.Background(), msg)
		if err != nil {
			t.Fatal(err)
		}
		if reply == nil || reply.Content != "OK" {
			t.Fatalf("reply=%+v", reply)
		}
	}
}

func TestRequestReasoningPolicyPriorityReachesProcessAndStreamWire(t *testing.T) {
	for _, tc := range []struct {
		name     string
		global   config.ReasoningPolicy
		agent    *config.ReasoningPolicy
		metadata map[string]string
		want     any
		known    bool
	}{
		{"global_on", config.ReasoningPolicy{Mode: "on"}, nil, nil, "high", true},
		{"agent_off", config.ReasoningPolicy{Mode: "on"}, &config.ReasoningPolicy{Mode: "off"}, nil, "none", true},
		{"agent_auto", config.ReasoningPolicy{Mode: "off"}, &config.ReasoningPolicy{Mode: ""}, nil, nil, true},
		{"agent_inherit", config.ReasoningPolicy{Mode: "on"}, &config.ReasoningPolicy{Mode: "inherit"}, nil, "high", true},
		{"explicit_off", config.ReasoningPolicy{Mode: "on"}, &config.ReasoningPolicy{Mode: "on"}, map[string]string{"thinking": "off"}, "none", true},
		{"explicit_auto", config.ReasoningPolicy{Mode: "off"}, &config.ReasoningPolicy{Mode: "on"}, map[string]string{"thinking": "auto"}, nil, true},
		{"explicit_effort", config.ReasoningPolicy{Mode: "off"}, &config.ReasoningPolicy{Mode: "off"}, map[string]string{"thinking": "on", "thinking_effort": "low"}, "low", true},
		{"unknown_control", config.ReasoningPolicy{Mode: "on"}, nil, nil, nil, false},
		{"unknown_control_effort", config.ReasoningPolicy{Mode: "effort", Effort: "high"}, nil, nil, nil, false},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream_%t", tc.name, streaming), func(t *testing.T) {
				var mu sync.Mutex
				var requests []map[string]any
				eng, _ := requestReasoningHTTPEngine(t, func(w http.ResponseWriter, r *http.Request) {
					body := requestReasoningServeResponse(t, w, r)
					mu.Lock()
					requests = append(requests, body)
					mu.Unlock()
				}, tc.global, tc.known)
				metadata := map[string]string{}
				for key, value := range tc.metadata {
					metadata[key] = value
				}
				if tc.agent != nil {
					router := agentrouter.New()
					if err := router.Register(agentrouter.AgentConfig{Name: "policy-agent", DisplayName: "Policy agent", Provider: "custom", Model: "exact-model", ReasoningPolicy: tc.agent}); err != nil {
						t.Fatal(err)
					}
					eng.SetAgentRouter(router)
					if streaming {
						metadata["pinned_agent"] = "policy-agent"
					} else {
						if err := router.AddRule(agentrouter.Rule{Platform: string(adapter.PlatformDingtalk), ChatID: "policy-chat", AgentName: "policy-agent"}); err != nil {
							t.Fatal(err)
						}
					}
				}
				platform := adapter.PlatformAPI
				if !streaming {
					platform = adapter.PlatformDingtalk
				}
				msg := &adapter.Message{ID: "policy-message", Platform: platform, ChatID: "policy-chat", UserID: "policy-owner", Content: "请解释整数的意义。", Metadata: metadata}
				requestReasoningSend(t, eng, msg, streaming)
				mu.Lock()
				defer mu.Unlock()
				if len(requests) != 1 || requests[0]["reasoning_effort"] != tc.want {
					t.Fatalf("wire requests=%v want effort=%v", requests, tc.want)
				}
				for _, key := range []string{"thinking", "thinking_effort", "reasoning_policy", "enable_thinking", "think"} {
					if _, exists := requests[0][key]; exists {
						t.Fatalf("internal key leaked wire: %s", key)
					}
				}
			})
		}
	}
}
