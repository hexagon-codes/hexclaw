package config

import (
	"context"
	"path/filepath"
	"testing"
)

func TestProviderModelSelection_DeferredValidationPreservesUnknownCapability(t *testing.T) {
	for _, tc := range []struct {
		name, source, fingerprint string
		capabilities              []string
		wantValid                 bool
	}{
		{"catalog with source", LLMModelCapabilitiesSourceCatalog, "current-source", []string{}, true},
		{"preset with source", LLMModelCapabilitiesSourcePreset, "current-source", []string{}, true},
		{"catalog without source", LLMModelCapabilitiesSourceCatalog, "", []string{}, false},
		{"manual empty", LLMModelCapabilitiesSourceManual, "current-source", []string{}, false},
		{"legacy explicit unknown", LLMModelCapabilitiesSourceLegacyUnknown, "current-source", []string{}, false},
		{"known non-text", LLMModelCapabilitiesSourceCatalog, "current-source", []string{LLMModelCapabilityVision}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := LLMProviderConfig{
				Model: "pending", Models: []string{"pending"}, ModelSpecsMode: LLMModelSpecsModeExplicit,
				ModelSpecs: []LLMProviderModelSpec{{ID: "pending", Capabilities: tc.capabilities,
					CapabilitiesSource: tc.source, CapabilitiesSourceFingerprint: tc.fingerprint}},
			}
			if err := ValidateProviderModelSpecs(provider); (err == nil) != tc.wantValid {
				t.Fatalf("structural validation=%v, want valid=%v", err, tc.wantValid)
			}
			cfg := DefaultConfig()
			cfg.LLM.Default, cfg.LLM.Providers = "custom", map[string]LLMProviderConfig{"custom": provider}
			if err := cfg.Validate(); (err == nil) != tc.wantValid {
				t.Fatalf("config validation=%v, want valid=%v", err, tc.wantValid)
			}
			if ModelHasCapability(provider, "pending", LLMModelCapabilityText) {
				t.Fatal("structural deferral must not grant text capability")
			}
		})
	}
}

func TestProviderModelSelection_DeferredSelectionLoadsAndStagesWithoutChangingDeclaration(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LLM.Default = "custom"
	cfg.LLM.Providers = map[string]LLMProviderConfig{"custom": {
		Model: "pending", Models: []string{"pending"}, ModelSpecsMode: LLMModelSpecsModeExplicit,
		ModelSpecs: []LLMProviderModelSpec{{ID: "pending", Capabilities: []string{},
			CapabilitiesSource: LLMModelCapabilitiesSourceCatalog, CapabilitiesSourceFingerprint: "saved-source"}},
	}}
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	if err := Save(cfg, path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("saved automatic selection cannot restart: %v", err)
	}
	manager := NewTransactionManager(loaded, []Validator{BuiltinValidator{}}, nil)
	tx, err := manager.Begin(withTxFlag(context.Background(), true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.Stage(context.Background(), loaded); err != nil {
		t.Fatalf("transaction rejected a persisted automatic selection: %v", err)
	}
	provider := loaded.LLM.Providers["custom"]
	if provider.Model != "pending" || len(provider.ModelSpecs[0].Capabilities) != 0 ||
		ModelHasCapability(provider, "pending", LLMModelCapabilityText) {
		t.Fatalf("load or Stage changed the unknown declaration: %+v", provider.ModelSpecs)
	}
}
