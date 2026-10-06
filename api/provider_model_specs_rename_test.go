package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/config"
)

// 稳定实例改名不改变模型声明；省略规格继承旧值，显式规格仍按提交内容更新。
func TestHandleUpdateLLMConfig_RenamePreservesModelSpecsByStableInstance(t *testing.T) {
	const providerID = "pvd_v1_0123456789abcdef0123456789abcdef"
	const fixtureKey = "fixture-provider-rename-key"
	for _, explicit := range []bool{false, true} {
		name := "omitted specs preserve declarations"
		if explicit {
			name = "explicit specs replace declarations"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			path := filepath.Join(t.TempDir(), "runtime.yaml")
			credentialRef, err := config.ProviderAPIKeyCredentialRef(providerID)
			if err != nil {
				t.Fatal(err)
			}
			specs := []config.LLMProviderModelSpec{
				{
					ID: "chat", DisplayName: "Multimodal chat",
					Capabilities:     []string{config.LLMModelCapabilityText, config.LLMModelCapabilityVision, config.LLMModelCapabilityCode},
					ReasoningSupport: config.LLMReasoningSupportSupported,
					ReasoningControl: &config.LLMReasoningControlSpec{
						Dialect: config.LLMReasoningDialectEffort, On: "medium", Off: "none",
						AllowedEfforts: []string{"low", "medium", "high"},
					},
				},
				{
					ID: "vector", DisplayName: "Vector model", Capabilities: []string{"embedding"},
					ReasoningSupport: config.LLMReasoningSupportUnknown,
					Embedding: &config.LLMEmbeddingModelSpec{
						Protocol: config.LLMEmbeddingProtocolOpenAI, Dimension: 3072, Normalization: "none",
					},
				},
				{
					ID: "unknown", DisplayName: "Unclassified model", IsCustom: true,
					Capabilities: []string{}, ReasoningSupport: config.LLMReasoningSupportUnknown,
				},
			}
			cfg := config.DefaultConfig()
			cfg.Server.APIToken = "fixture-provider-rename-token"
			cfg.LLM.Default = "source"
			cfg.LLM.ConfigRevision = 7
			toolsEnabled := true
			cfg.LLM.Providers = map[string]config.LLMProviderConfig{
				"source": {
					ProviderInstanceID: providerID, APIKey: fixtureKey, CredentialRef: credentialRef,
					BaseURL: "https://provider.example.test/v1", Compatible: "openai", Locality: "cloud",
					Model: "chat", Models: []string{"chat", "vector", "unknown"},
					ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: specs,
					ToolsEnabled: &toolsEnabled,
				},
			}
			if err := config.Save(cfg, path); err != nil {
				t.Fatal(err)
			}
			srv := NewServer(cfg, &mockEngine{activeLLM: cfg.LLM}, nil, nil)
			srv.SetRuntimeConfigPath(path)
			readConfig := func() LLMConfigResponse {
				t.Helper()
				req := httptest.NewRequest(http.MethodGet, "/api/v1/config/llm", nil)
				req.Header.Set("Authorization", "Bearer "+cfg.Server.APIToken)
				w := httptest.NewRecorder()
				srv.routes().ServeHTTP(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("GET status=%d body=%s", w.Code, w.Body.String())
				}
				if strings.Contains(w.Body.String(), fixtureKey) {
					t.Fatal("GET exposed the provider credential")
				}
				var response LLMConfigResponse
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				return response
			}
			before := readConfig()
			provider := map[string]any{
				"provider_instance_id": providerID, "api_key": config.MaskAPIKey(fixtureKey),
				"base_url": "https://provider.example.test/v1", "compatible": "openai", "locality": "cloud",
				"model": "chat", "models": []string{"chat", "vector", "unknown"},
				"tools_enabled": true,
			}
			want := specs
			if explicit {
				want = []config.LLMProviderModelSpec{
					{ID: "chat", DisplayName: "Text chat", Capabilities: []string{"text"}, ReasoningSupport: config.LLMReasoningSupportUnsupported},
					{
						ID: "vector", DisplayName: "Updated vector", Capabilities: []string{"embedding"},
						ReasoningSupport: config.LLMReasoningSupportUnknown,
						Embedding:        &config.LLMEmbeddingModelSpec{Protocol: config.LLMEmbeddingProtocolOpenAI, Dimension: 768, Normalization: "l2"},
					},
					specs[2],
				}
				provider["model_specs"] = want
			}
			body, err := json.Marshal(map[string]any{
				"default": "renamed", "providers": map[string]any{"renamed": provider},
				"expected_config_revision": before.ConfigRevision, "expected_config_digest": before.ConfigDigest,
			})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPut, "/api/v1/config/llm", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+cfg.Server.APIToken)
			w := httptest.NewRecorder()
			srv.routes().ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("rename status=%d body=%s", w.Code, w.Body.String())
			}
			after := readConfig()
			got, exists := after.Providers["renamed"]
			if !exists || len(after.Providers) != 1 || after.Default != "renamed" || after.ConfigRevision != 8 {
				t.Fatal("rename did not commit one provider under its new name and default")
			}
			if got.ProviderInstanceID != providerID || got.CredentialRef != credentialRef || !got.CredentialPresent {
				t.Fatal("rename changed the stable provider or credential identity")
			}
			if got.ToolsEnabled == nil || !*got.ToolsEnabled {
				t.Fatal("rename lost the submitted provider tool setting")
			}
			if got.ModelSpecsMode != config.LLMModelSpecsModeExplicit || !reflect.DeepEqual(got.ModelSpecs, want) {
				t.Errorf("GET model declarations changed: mode=%q specs=%+v; want explicit specs=%+v", got.ModelSpecsMode, got.ModelSpecs, want)
			}
			loaded, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			persisted := loaded.LLM.Providers["renamed"]
			if len(loaded.LLM.Providers) != 1 || loaded.LLM.Default != "renamed" || persisted.ProviderInstanceID != providerID || persisted.APIKey != fixtureKey || persisted.CredentialRef != credentialRef {
				t.Fatal("persisted rename lost provider identity, credential, or default")
			}
			if persisted.ToolsEnabled == nil || !*persisted.ToolsEnabled {
				t.Fatal("persisted rename lost the submitted provider tool setting")
			}
			if persisted.ModelSpecsMode != config.LLMModelSpecsModeExplicit || !reflect.DeepEqual(persisted.ModelSpecs, want) {
				t.Errorf("persisted model declarations changed: mode=%q specs=%+v; want explicit specs=%+v", persisted.ModelSpecsMode, persisted.ModelSpecs, want)
			}
		})
	}
}
