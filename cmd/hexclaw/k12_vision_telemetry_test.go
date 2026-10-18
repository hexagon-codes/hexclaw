package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/toolkit/util/logger"
)

func TestK12VisionTelemetryPreservesEvidenceWithoutContent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		controlled bool
		status     int
		noUsage    bool
	}{
		{name: "configured reasoning receipt", controlled: true, status: http.StatusOK},
		{name: "missing receipt remains unknown", status: http.StatusOK},
		{name: "missing usage remains unavailable", status: http.StatusOK, noUsage: true},
		{name: "known provider failure is recorded once", status: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const prompt = "private-prompt-must-not-be-logged"
			const output = "private-answer-must-not-be-logged"
			const key = "private-api-key-must-not-be-logged"
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if tc.status != http.StatusOK {
					_, _ = w.Write([]byte(`{"error":{"message":"private-upstream-error-must-not-be-logged","type":"server_error"}}`))
					return
				}
				if tc.noUsage {
					_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + output + `"},"finish_reason":"stop"}]}`))
					return
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + output + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":19,"completion_tokens":31,"total_tokens":50,"prompt_tokens_details":{"cached_tokens":7},"completion_tokens_details":{"reasoning_tokens":12}}}`))
			}))
			defer server.Close()
			modelSpec := config.LLMProviderModelSpec{ID: "model-vision-telemetry", Capabilities: []string{"text", "vision"}}
			if tc.controlled {
				modelSpec.ReasoningSupport = config.LLMReasoningSupportSupported
				modelSpec.ReasoningControl = &config.LLMReasoningControlSpec{
					Dialect: config.LLMReasoningDialectEffort, On: "medium", Off: "none",
				}
			}
			providerConfig := config.LLMProviderConfig{
				APIKey: key, BaseURL: server.URL, Model: modelSpec.ID,
				ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: []config.LLMProviderModelSpec{modelSpec},
			}
			router := llmrouter.NewWithProviders(config.LLMConfig{
				Default: "provider-telemetry", Providers: map[string]config.LLMProviderConfig{"provider-telemetry": providerConfig},
			}, map[string]hexagon.Provider{"provider-telemetry": llmrouter.NewProviderFromConfig("provider-telemetry", providerConfig)})
			snapshot, err := resolveK12GradingModelSnapshot(router, k12.GradingModelSnapshot{})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "vision.jsonl")
			log, err := logger.New(&logger.Config{Format: "json", Output: path})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = log.Close() })
			ctx := logger.ContextWithLogger(context.Background(), log.With("task_id", "telemetry-task"))
			ctx = k12.WithGradingModelSnapshot(ctx, snapshot)
			ctx = egress.WithProviderClientRequestKey(ctx, "telemetry-physical-call")
			if !snapshot.RecognizingRequestPolicy.IsZero() {
				ctx = k12.WithGradingModelRequestPolicy(ctx, snapshot.RecognizingRequestPolicy)
			}
			provider, _ := router.Get("provider-telemetry")
			content, requestErr := completeK12VisionRequest(ctx, provider, snapshot.Model, []byte("private-image"), prompt)
			if tc.status == http.StatusOK && (requestErr != nil || content != output) {
				t.Fatalf("successful response changed: content=%q err=%v", content, requestErr)
			}
			if tc.status != http.StatusOK && requestErr == nil {
				t.Fatal("provider failure was hidden")
			}
			if calls != 1 {
				t.Fatalf("physical requests = %d, want exactly 1", calls)
			}
			if err := log.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, sensitive := range []string{prompt, output, key, server.URL, "private-image", "private-upstream-error"} {
				if strings.Contains(string(raw), sensitive) {
					t.Fatalf("vision telemetry includes sensitive value %q", sensitive)
				}
			}
			var record map[string]any
			if err := json.Unmarshal(raw, &record); err != nil {
				t.Fatalf("expected one completion record: %v", err)
			}
			if record["task_id"] != "telemetry-task" || record["provider_request_key"] != "telemetry-physical-call" || record["model"] != "model-vision-telemetry" {
				t.Fatalf("request identity was lost: %v", record)
			}
			if _, present := record["elapsed_ms"]; !present {
				t.Fatal("elapsed time is missing")
			}
			if tc.status != http.StatusOK {
				if record["outcome"] != "provider_error" || record["error_type"] == "" {
					t.Fatalf("missing typed failure: %v", record)
				}
				return
			}
			if record["output_bytes"] != float64(len(output)) || record["usage_available"] != !tc.noUsage {
				t.Fatalf("usage availability or output size changed: %v", record)
			}
			if tc.noUsage {
				for _, field := range []string{"prompt_tokens", "completion_tokens", "total_tokens", "cache_read_tokens", "cache_creation_tokens"} {
					if _, invented := record[field]; invented {
						t.Fatalf("missing usage was reported as a token measurement: %s", field)
					}
				}
			} else if record["prompt_tokens"] != float64(19) || record["completion_tokens"] != float64(31) || record["cache_read_tokens"] != float64(7) {
				t.Fatalf("upstream usage was not retained: %v", record)
			}
			if record["reasoning_receipt_available"] != tc.controlled {
				t.Fatalf("reasoning receipt availability changed: %v", record)
			}
			if tc.controlled {
				if record["reasoning_tokens"] != float64(12) || record["expected_reasoning_effort"] != "none" || record["reasoning_observed"] != true {
					t.Fatalf("actual reasoning evidence was not retained: %v", record)
				}
			} else if _, invented := record["reasoning_tokens"]; invented {
				t.Fatal("missing receipt was reported as a token measurement")
			}
		})
	}
}
