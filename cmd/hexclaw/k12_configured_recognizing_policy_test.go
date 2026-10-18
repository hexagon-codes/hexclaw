package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestConfiguredRecognizingPolicyWireAndStageIsolation(t *testing.T) {
	for _, route := range []struct{ provider, model string }{
		{provider: "provider-a", model: "model-vision-a"},
		{provider: "provider-b", model: "model-vision-b"},
	} {
		t.Run(route.provider, func(t *testing.T) {
			testConfiguredRecognizingPolicyWireAndStageIsolation(t, route.provider, route.model)
		})
	}
	t.Run("empty configuration cannot invent a route", func(t *testing.T) {
		router := llmrouter.NewWithProviders(config.LLMConfig{}, map[string]hexagon.Provider{})
		got, err := resolveK12GradingModelSnapshot(router, k12.GradingModelSnapshot{})
		if err == nil || got.Provider != "" || got.Model != "" || !got.RecognizingRequestPolicy.IsZero() {
			t.Fatalf("empty configuration acquired a route or policy: %+v err=%v", got, err)
		}
	})
}

func testConfiguredRecognizingPolicyWireAndStageIsolation(t *testing.T, providerName, model string) {
	t.Helper()
	var payloads []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			http.Error(w, "invalid request", 400)
			return
		}
		payloads = append(payloads, payload)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	providerConfig := config.LLMProviderConfig{
		ProviderInstanceID: "instance-" + providerName,
		APIKey:             "test-only", BaseURL: server.URL, Model: model,
		ModelSpecsMode: config.LLMModelSpecsModeExplicit,
		ModelSpecs: []config.LLMProviderModelSpec{{
			ID: model, Capabilities: []string{"text", "vision"},
			ReasoningSupport: config.LLMReasoningSupportSupported,
			ReasoningControl: &config.LLMReasoningControlSpec{
				Dialect: config.LLMReasoningDialectEffort, On: "medium", Off: "none",
			},
		}},
	}
	makeRouter := func(name string, cfg config.LLMProviderConfig) *llmrouter.Selector {
		return llmrouter.NewWithProviders(config.LLMConfig{
			Default: name, Providers: map[string]config.LLMProviderConfig{name: cfg},
		}, map[string]hexagon.Provider{name: llmrouter.NewProviderFromConfig(name, cfg)})
	}
	router := makeRouter(providerName, providerConfig)
	snapshot, err := resolveK12GradingModelSnapshot(router, k12.GradingModelSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RecognizingRequestPolicy != k12.ConfiguredRecognizingRequestPolicy() {
		t.Fatalf("new route did not freeze verified policy: %+v", snapshot)
	}
	if snapshot.Provider != providerName || snapshot.Model != model || snapshot.ProviderInstanceID != "instance-"+providerName {
		t.Fatalf("configured route identity changed: %+v", snapshot)
	}
	provider, _ := router.Get(providerName)
	for _, stage := range []string{k12.GradingStageRecognizing, k12.GradingStageLocating, "solve_verify", "ordinary"} {
		t.Run(stage, func(t *testing.T) {
			ctx := k12.WithGradingModelSnapshot(context.Background(), snapshot)
			if stage == k12.GradingStageRecognizing {
				ctx = k12.WithGradingModelRequestPolicy(ctx, snapshot.RecognizingRequestPolicy)
			} else if stage == k12.GradingStageLocating {
				ctx = k12.WithGradingModelRequestPolicy(ctx, k12.ApprovedLocatingRequestPolicy())
			}
			if _, err := completeK12VisionRequest(ctx, provider, snapshot.Model, []byte("image"), "recognize"); err != nil {
				t.Fatal(err)
			}
			wire := payloads[len(payloads)-1]
			if stage == k12.GradingStageRecognizing || stage == k12.GradingStageLocating {
				if wire["reasoning_effort"] != "none" {
					t.Fatalf("wire policy missing: %v", wire)
				}
			} else if _, exists := wire["reasoning_effort"]; exists {
				t.Fatalf("unmarked stage inherited recognition policy: %v", wire)
			}
			if _, exists := wire["expected_reasoning_effort"]; exists {
				t.Fatal("audit metadata leaked onto wire")
			}
		})
	}
	t.Run("mapping drift stops before sending", func(t *testing.T) {
		changed := providerConfig
		changed.ModelSpecs = append([]config.LLMProviderModelSpec(nil), providerConfig.ModelSpecs...)
		changed.ModelSpecs[0].ReasoningControl = &config.LLMReasoningControlSpec{
			Dialect: config.LLMReasoningDialectEffort, On: "medium", Off: "low",
		}
		changedProvider, _ := makeRouter(providerName, changed).Get(providerName)
		ctx := k12.WithGradingModelRequestPolicy(k12.WithGradingModelSnapshot(context.Background(), snapshot), snapshot.RecognizingRequestPolicy)
		before := len(payloads)
		if _, err := completeK12VisionRequest(ctx, changedProvider, snapshot.Model, []byte("image"), "recognize"); err == nil {
			t.Fatal("changed mapping silently replaced frozen policy")
		}
		if len(payloads) != before {
			t.Fatal("drifted request was sent")
		}
	})
	for _, name := range []string{"unknown capability", "native reasoning without control", "off value differs", "historical model name without control"} {
		t.Run(name, func(t *testing.T) {
			cfg := providerConfig
			cfg.ModelSpecs = append([]config.LLMProviderModelSpec(nil), providerConfig.ModelSpecs...)
			switch name {
			case "unknown capability":
				cfg.ModelSpecs[0].ReasoningSupport = config.LLMReasoningSupportUnknown
				cfg.ModelSpecs[0].ReasoningControl = nil
			case "native reasoning without control":
				cfg.ModelSpecs[0].ReasoningControl = nil
			case "off value differs":
				cfg.ModelSpecs[0].ReasoningControl = &config.LLMReasoningControlSpec{
					Dialect: config.LLMReasoningDialectEffort, On: "medium", Off: "low",
				}
			case "historical model name without control":
				cfg.Model = k12.RecognizingPolicyModel
				cfg.ModelSpecs[0].ID = cfg.Model
				cfg.ModelSpecs[0].ReasoningSupport = config.LLMReasoningSupportUnknown
				cfg.ModelSpecs[0].ReasoningControl = nil
			}
			got, err := resolveK12GradingModelSnapshot(makeRouter(providerName, cfg), k12.GradingModelSnapshot{})
			if err != nil {
				t.Fatal(err)
			}
			if !got.RecognizingRequestPolicy.IsZero() {
				t.Fatal("unverified route acquired new policy")
			}
		})
	}
}
