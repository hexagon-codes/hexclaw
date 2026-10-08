package config

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNativeReasoningDeclarationRoundTripRemainsIndependent(t *testing.T) {
	for _, native := range []string{"supported", "unsupported", "unknown", ""} {
		t.Run("native_"+native, func(t *testing.T) {
			provider := LLMProviderConfig{
				Model: "exact-model", Models: []string{"exact-model"}, ModelSpecsMode: LLMModelSpecsModeExplicit,
				ModelSpecs: []LLMProviderModelSpec{{ID: "exact-model", Capabilities: []string{"text"}, NativeReasoningSupport: native, NativeReasoningSourceFingerprint: "source-fixture"}},
			}
			for _, format := range []string{"json", "yaml"} {
				var decoded LLMProviderConfig
				var body []byte
				var err error
				if format == "json" {
					body, err = json.Marshal(provider)
					if err == nil {
						err = json.Unmarshal(body, &decoded)
					}
				} else {
					body, err = yaml.Marshal(provider)
					if err == nil {
						err = yaml.Unmarshal(body, &decoded)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				_, specs := NormalizeProviderModelSpecs(decoded)
				if specs[0].NativeReasoningSupport != native || specs[0].NativeReasoningSourceFingerprint != "source-fixture" {
					t.Fatalf("%s lost native declaration: %+v", format, specs[0])
				}
				if support, control := ModelReasoningControl(decoded, "exact-model"); support != "unknown" || control != nil {
					t.Fatalf("native declaration changed control: %q %+v", support, control)
				}
				if !ModelHasCapability(decoded, "exact-model", "text") {
					t.Fatal("native declaration removed text")
				}
			}
		})
	}
	provider := LLMProviderConfig{ModelSpecsMode: LLMModelSpecsModeExplicit, ModelSpecs: []LLMProviderModelSpec{{ID: "model", Capabilities: []string{"text"}, NativeReasoningSupport: "invalid"}}}
	_, specs := NormalizeProviderModelSpecs(provider)
	if specs[0].NativeReasoningSupport != "unknown" {
		t.Fatalf("invalid native declaration=%q", specs[0].NativeReasoningSupport)
	}
	for _, modelID := range []string{OpenRouterNemotronEmbedFreeModelID, OpenRouterVLLEmbedFreeModelID} {
		for _, native := range []string{"supported", "unsupported", "unknown", "invalid"} {
			t.Run(modelID+"_"+native, func(t *testing.T) {
				provider := LLMProviderConfig{ModelSpecsMode: LLMModelSpecsModeExplicit, ModelSpecs: []LLMProviderModelSpec{{ID: modelID, Capabilities: []string{"text"}, NativeReasoningSupport: native, NativeReasoningSourceFingerprint: "source-fixture"}}}
				body, err := yaml.Marshal(provider)
				if err != nil {
					t.Fatal(err)
				}
				var decoded LLMProviderConfig
				if err := yaml.Unmarshal(body, &decoded); err != nil {
					t.Fatal(err)
				}
				_, specs := NormalizeProviderModelSpecs(decoded)
				want := native
				if want == "invalid" {
					want = "unknown"
				}
				if specs[0].NativeReasoningSupport != want || specs[0].NativeReasoningSourceFingerprint != "source-fixture" {
					t.Fatalf("intrinsic embedding lost native: %+v", specs[0])
				}
				if ModelHasCapability(decoded, modelID, "text") || !ModelHasCapability(decoded, modelID, "embedding") || specs[0].ReasoningControl != nil || specs[0].ReasoningSupport != "unknown" {
					t.Fatalf("native changed intrinsic routing/control: %+v", specs[0])
				}
			})
		}
	}
}
