package config

import (
	"strings"

	"github.com/hexagon-codes/ai-core/catalog"
)

// OfficialModelCapabilities 仅补充已核实的官方端点与精确模型声明，不将同名中转模型视为官方模型。
func OfficialModelCapabilities(providerKey string, pc LLMProviderConfig, modelID string) ([]string, bool) {
	compatible := strings.ToLower(strings.TrimSpace(pc.Compatible))
	if pc.HasOllamaTarget() || providerKey == "ollama" || compatible == "anthropic" || (compatible == "" && providerKey == "anthropic") {
		return nil, false
	}
	baseURL := strings.TrimSpace(pc.BaseURL)
	if baseURL == "" && providerKey == "openai" {
		baseURL = "https://api.openai.com/v1"
	}
	model, ok := catalog.LookupOfficialModel(baseURL, modelID)
	if !ok {
		return nil, false
	}
	capabilities := make([]string, 0, 3)
	if model.Modality == catalog.ModalityText {
		capabilities = append(capabilities, LLMModelCapabilityText)
	}
	if model.HasFeature(catalog.FeatureVision) || model.HasFeature(catalog.FeatureImageInput) {
		capabilities = append(capabilities, LLMModelCapabilityVision)
	}
	if model.HasFeature(catalog.FeatureComputerUse) {
		capabilities = append(capabilities, LLMModelCapabilityComputerUse)
	}
	return capabilities, true
}
