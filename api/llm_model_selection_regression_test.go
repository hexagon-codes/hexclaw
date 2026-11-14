package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/featureflag"
	"github.com/hexagon-codes/hexclaw/storage"
)

func TestLLMModelSelection_ConfigSaveUsesCurrentEffectiveCapability(t *testing.T) {
	for _, tc := range []struct {
		name        string
		change      string
		withProof   bool
		transaction bool
		wantStatus  int
	}{
		{name: "current proof", withProof: true, wantStatus: http.StatusOK},
		{name: "current proof with transaction", withProof: true, transaction: true, wantStatus: http.StatusOK},
		{name: "missing proof", wantStatus: http.StatusBadRequest},
		{name: "changed endpoint", withProof: true, change: "endpoint", wantStatus: http.StatusBadRequest},
		{name: "changed protocol", withProof: true, change: "protocol", wantStatus: http.StatusBadRequest},
		{name: "changed credential", withProof: true, change: "credential", wantStatus: http.StatusBadRequest},
		{name: "manual empty", withProof: true, change: "manual", wantStatus: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			for _, name := range []string{"DEEPSEEK_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "QWEN_API_KEY", "GEMINI_API_KEY"} {
				t.Setenv(name, "")
			}
			store := bug20260728OpenStore(t)
			cfg := bug20260728ProviderConfig()
			provider := cfg.LLM.Providers["custom"]
			provider.Model = "baseline"
			provider.Models = []string{"baseline", "pending"}
			provider.ModelSpecs = []config.LLMProviderModelSpec{
				{ID: "baseline", Capabilities: []string{"text"}, CapabilitiesSource: "manual"},
				{ID: "pending", Capabilities: []string{}, CapabilitiesSource: "catalog"},
			}
			cfg.LLM.Providers["custom"] = provider
			if tc.withProof {
				if _, err := store.SaveModelCapabilityProbeReceipt(context.Background(), &storage.ModelCapabilityProbeReceipt{
					ProviderInstanceID: provider.ProviderInstanceID, ModelID: "pending", ProbeKind: "text",
					ConfigFingerprint:  ModelCapabilityProbeConfigFingerprint("custom", provider, "pending"),
					ProbePolicyVersion: ModelCapabilityProbePolicyVersion, Outcome: "passed", TestedAt: 1, ProbeStartedAt: 1,
				}); err != nil {
					t.Fatal(err)
				}
			}
			srv := NewServer(cfg, &mockEngine{activeLLM: cfg.LLM}, nil, store)
			path := filepath.Join(t.TempDir(), "runtime.yaml")
			srv.SetRuntimeConfigPath(path)
			if tc.transaction {
				srv.SetConfigTxManager(config.NewTransactionManager(cfg, []config.Validator{config.BuiltinValidator{}}, nil))
			}
			provider.Model = "pending"
			switch tc.change {
			case "endpoint":
				provider.BaseURL = "https://changed.example.test/v1"
			case "protocol":
				provider.APIProtocol = "responses"
			case "credential":
				provider.APIKey = "replacement-fixture-key"
			case "manual":
				provider.ModelSpecs[1].CapabilitiesSource = "manual"
			}
			body, err := json.Marshal(map[string]any{"default": "custom", "providers": map[string]any{"custom": map[string]any{
				"provider_instance_id": provider.ProviderInstanceID, "api_key": provider.APIKey,
				"base_url": provider.BaseURL, "compatible": provider.Compatible, "api_protocol": provider.APIProtocol,
				"locality": provider.Locality, "model": provider.Model, "models": provider.Models, "model_specs": provider.ModelSpecs,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPut, "/api/v1/config/llm", bytes.NewReader(body))
			if tc.transaction {
				flags := featureflag.NewStatic(featureflag.Registered(), map[string]bool{config.FlagConfigTxHotloadV1: true})
				req = req.WithContext(featureflag.WithContext(req.Context(), flags))
			}
			recorder := httptest.NewRecorder()
			srv.handleUpdateLLMConfig(recorder, req)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", recorder.Code, recorder.Body.String(), tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK {
				if got := srv.cfg.LLM.Providers["custom"].Model; got != "baseline" {
					t.Fatalf("rejected selection changed current model to %q", got)
				}
				return
			}
			loaded, err := config.Load(path)
			if err != nil {
				t.Fatalf("saved effective selection cannot restart: %v", err)
			}
			persisted := loaded.LLM.Providers["custom"]
			if persisted.Model != "pending" || len(persisted.ModelSpecs[1].Capabilities) != 0 {
				t.Fatalf("selection or declared capability changed: %+v", persisted)
			}
			if !config.ModelHasCapability(srv.ResolveEffectiveProviderModelSpecs(context.Background(), "custom", persisted), "pending", "text") {
				t.Fatal("saved selection lost its independently persisted current proof")
			}
		})
	}
}

func TestLLMModelSelection_JointTargetCannotReusePreviousTargetProof(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := bug20260728OpenStore(t)
	cfg := bug20260728ProviderConfig()
	provider := cfg.LLM.Providers["custom"]
	provider.BaseURL, provider.OllamaTargetBaseURL = "http://127.0.0.1:11300/v1", "http://127.0.0.1:11300"
	provider.Model, provider.Models = "pending", []string{"pending"}
	provider.ModelSpecs = []config.LLMProviderModelSpec{{ID: "pending", Capabilities: []string{}, CapabilitiesSource: "catalog"}}
	provider.ModelSpecs[0].CapabilitiesSourceFingerprint = ModelCapabilityProbeConfigFingerprint("custom", provider, "pending")
	cfg.LLM.Providers["custom"] = provider
	cfg.Ollama = config.OllamaTargetConfig{Mode: "custom", CustomBaseURL: provider.OllamaTargetBaseURL,
		AssociatedProviderInstanceIDs: []string{provider.ProviderInstanceID}}
	if _, err := store.SaveModelCapabilityProbeReceipt(context.Background(), &storage.ModelCapabilityProbeReceipt{
		ProviderInstanceID: provider.ProviderInstanceID, ModelID: "pending", ProbeKind: "text",
		ConfigFingerprint:  provider.ModelSpecs[0].CapabilitiesSourceFingerprint,
		ProbePolicyVersion: ModelCapabilityProbePolicyVersion, Outcome: "passed", TestedAt: 1, ProbeStartedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(cfg, &mockEngine{activeLLM: cfg.LLM}, nil, store)
	if !config.ModelHasCapability(srv.ResolveEffectiveProviderModelSpecs(context.Background(), "custom", provider), "pending", "text") {
		t.Fatal("previous target fixture must have a matching positive receipt")
	}
	revision := cfg.Ollama.TargetRevision
	digest := cfg.Ollama.Digest(provider.OllamaTargetBaseURL)
	configRevision := cfg.LLM.ConfigRevision
	configDigest, err := digestLLMConfig(cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	target := cfg.Ollama
	target.CustomBaseURL = "http://127.0.0.1:11301"
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/config", nil)
	req.Header.Set("Idempotency-Key", "selected-model-target-update")
	srv.updateLLMConfig(recorder, req, LLMConfigUpdateRequest{}, &ollamaTargetUpdateRequest{
		Ollama: &target, ExpectedTargetRevision: &revision, ExpectedTargetDigest: &digest,
		ExpectedConfigRevision: &configRevision, ExpectedConfigDigest: &configDigest,
	})
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "text capability") {
		t.Fatalf("changed target accepted previous target proof: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if cfg.Ollama.CustomBaseURL != provider.OllamaTargetBaseURL || cfg.LLM.Providers["custom"].BaseURL != provider.BaseURL {
		t.Fatal("rejected joint target changed the active configuration")
	}
}

func TestLLMModelSelection_ExplicitCapabilitySourceReplacesWithoutLosingOmittedSource(t *testing.T) {
	for _, tc := range []struct {
		name, oldSource, source, wantSource string
		oldCaps, caps, wantCaps             []string
	}{
		{"restore preset", "manual", "preset", "preset", []string{"text"}, []string{"text", "vision"}, []string{"text", "vision"}},
		{"accept catalog", "legacy_unknown", "catalog", "catalog", []string{}, []string{"text", "vision"}, []string{"text", "vision"}},
		{"omitted source keeps manual", "manual", "", "manual", []string{}, []string{}, []string{}},
		{"ordinary explicit manual", "manual", "manual", "manual", []string{"text"}, []string{"text"}, []string{"text"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := config.LLMProviderConfig{ProviderInstanceID: bug20260728ProviderInstanceID,
				ModelSpecsMode: "explicit", Models: []string{"m"},
				ModelSpecs: []config.LLMProviderModelSpec{{ID: "m", Capabilities: tc.oldCaps, CapabilitiesSource: tc.oldSource}},
			}
			candidate := old
			candidate.ModelSpecs = []config.LLMProviderModelSpec{{ID: "m", Capabilities: tc.caps, CapabilitiesSource: tc.source}}
			resolveProviderCapabilitySources("custom", &candidate, old, true)
			got := candidate.ModelSpecs[0]
			if got.CapabilitiesSource != tc.wantSource || !reflect.DeepEqual(got.Capabilities, tc.wantCaps) {
				t.Fatalf("source=%q caps=%v, want source=%q caps=%v", got.CapabilitiesSource, got.Capabilities, tc.wantSource, tc.wantCaps)
			}
		})
	}
}
