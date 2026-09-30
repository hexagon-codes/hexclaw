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
		APIKey: "test-only", BaseURL: server.URL, Model: "gpt-6-sol",
		ModelSpecsMode: config.LLMModelSpecsModeExplicit,
		ModelSpecs: []config.LLMProviderModelSpec{{
			ID: "gpt-6-sol", Capabilities: []string{"text", "vision"},
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
	router := makeRouter("cloud-gpt", providerConfig)
	snapshot, err := resolveK12GradingModelSnapshot(router, k12.GradingModelSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RecognizingRequestPolicy != k12.ConfiguredRecognizingRequestPolicy() {
		t.Fatalf("new route did not freeze verified policy: %+v", snapshot)
	}
	provider, _ := router.Get("cloud-gpt")
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
		changedProvider, _ := makeRouter("cloud-gpt", changed).Get("cloud-gpt")
		ctx := k12.WithGradingModelRequestPolicy(k12.WithGradingModelSnapshot(context.Background(), snapshot), snapshot.RecognizingRequestPolicy)
		before := len(payloads)
		if _, err := completeK12VisionRequest(ctx, changedProvider, snapshot.Model, []byte("image"), "recognize"); err == nil {
			t.Fatal("changed mapping silently replaced frozen policy")
		}
		if len(payloads) != before {
			t.Fatal("drifted request was sent")
		}
	})
	for _, name := range []string{"unknown capability", "other provider"} {
		t.Run(name, func(t *testing.T) {
			cfg := providerConfig
			cfg.ModelSpecs = append([]config.LLMProviderModelSpec(nil), providerConfig.ModelSpecs...)
			providerName := "cloud-gpt"
			if name == "unknown capability" {
				cfg.ModelSpecs[0].ReasoningSupport = config.LLMReasoningSupportUnknown
				cfg.ModelSpecs[0].ReasoningControl = nil
			} else {
				providerName = "another-route"
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
