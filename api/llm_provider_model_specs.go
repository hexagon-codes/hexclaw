package api

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/llmrouter"
)

// 原生能力来源绑定物理实例和执行目标；展示名与默认模型不参与来源身份。
func nativeReasoningSourceFingerprint(providerKey string, provider config.LLMProviderConfig, modelID string) string {
	domain := "native_reasoning/model/v1"
	probeModel := modelID
	if modelID == "" {
		domain = "native_reasoning/provider/v1"
		probeModel = "native_reasoning_source"
	}
	// 自定义别名最终使用同一 OpenAI 适配器，不让展示名称制造新的来源。
	providerType := canonicalProviderProbeType(providerKey, provider)
	if llmrouter.UsesOllamaNativeAdapter(providerType, provider) {
		providerType = "ollama"
	} else if providerType != "anthropic" {
		providerType = "openai"
	}
	payload := domain + "\x00" + config.EffectiveProviderInstanceID(providerKey, provider) + "\x00" + modelID + "\x00" +
		modelCapabilityProbeConfigFingerprint(providerType, provider, probeModel)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
}

func nativeReasoningModelSpecs(providerKey string, provider config.LLMProviderConfig) (string, []config.LLMProviderModelSpec) {
	mode, specs := config.NormalizeProviderModelSpecs(provider)
	for i := range specs {
		fingerprint := nativeReasoningSourceFingerprint(providerKey, provider, specs[i].ID)
		if specs[i].NativeReasoningSourceFingerprint != fingerprint {
			specs[i].NativeReasoningSupport = config.LLMReasoningSupportUnknown
		}
		if specs[i].NativeReasoningSupport == "" {
			specs[i].NativeReasoningSupport = config.LLMReasoningSupportUnknown
		}
		specs[i].NativeReasoningSourceFingerprint = fingerprint
	}
	return mode, specs
}

// 写入只接纳当前来源；同来源旧客户端省略字段仍保留已保存声明。
func resolveProviderNativeReasoning(providerKey string, candidate *config.LLMProviderConfig, old config.LLMProviderConfig, oldExists bool, requested LLMProviderConfigUpdateItem) {
	oldSpecs := make(map[string]config.LLMProviderModelSpec)
	if oldExists && config.EffectiveProviderInstanceID(providerKey, *candidate) == config.EffectiveProviderInstanceID(providerKey, old) {
		_, normalized := config.NormalizeProviderModelSpecs(old)
		for _, spec := range normalized {
			oldSpecs[spec.ID] = spec
		}
	}
	requestedSpecs := make(map[string]config.LLMProviderModelSpec)
	if requested.ModelSpecs != nil {
		for _, spec := range *requested.ModelSpecs {
			requestedSpecs[spec.ID] = spec
		}
	}
	for i := range candidate.ModelSpecs {
		spec := &candidate.ModelSpecs[i]
		fingerprint := nativeReasoningSourceFingerprint(providerKey, *candidate, spec.ID)
		incoming, supplied := requestedSpecs[spec.ID]
		if !supplied || incoming.NativeReasoningSupport == "" {
			previous := oldSpecs[spec.ID]
			if previous.NativeReasoningSourceFingerprint == fingerprint {
				spec.NativeReasoningSupport = previous.NativeReasoningSupport
				spec.NativeReasoningSourceFingerprint = fingerprint
				continue
			}
		}
		if spec.NativeReasoningSourceFingerprint != fingerprint {
			spec.NativeReasoningSupport = config.LLMReasoningSupportUnknown
		}
		spec.NativeReasoningSourceFingerprint = fingerprint
	}
}

func resolveProviderInstanceID(
	providerKey string,
	old config.LLMProviderConfig,
	oldExists bool,
	requested string,
) (string, error) {
	if oldExists {
		effectiveOld := config.EffectiveProviderInstanceID(providerKey, old)
		if requested == "" {
			return effectiveOld, nil
		}
		if err := config.ValidateProviderInstanceID(requested); err != nil {
			return "", err
		}
		if requested != effectiveOld {
			return "", fmt.Errorf("provider_instance_id 不可变：已有 %q，收到 %q", effectiveOld, requested)
		}
		return requested, nil
	}

	if requested != "" {
		if err := config.ValidateProviderInstanceID(requested); err != nil {
			return "", err
		}
		return requested, nil
	}
	return config.NewProviderInstanceID()
}

func resolveProviderModelSpecs(
	old config.LLMProviderConfig,
	oldExists bool,
	requested LLMProviderConfigUpdateItem,
) (string, []config.LLMProviderModelSpec) {
	if requested.ModelSpecs != nil {
		explicit := make([]config.LLMProviderModelSpec, len(*requested.ModelSpecs))
		copy(explicit, *requested.ModelSpecs)
		candidate := config.LLMProviderConfig{
			Models:         requested.Models,
			ModelSpecsMode: config.LLMModelSpecsModeExplicit,
			ModelSpecs:     explicit,
		}
		_, normalized := config.NormalizeProviderModelSpecs(candidate)
		return config.LLMModelSpecsModeExplicit, normalized
	}

	if !oldExists {
		return config.LLMModelSpecsModeLegacy, nil
	}
	oldMode, oldSpecs := config.NormalizeProviderModelSpecs(old)
	if oldMode != config.LLMModelSpecsModeExplicit {
		return config.LLMModelSpecsModeLegacy, nil
	}

	allowed := make(map[string]struct{}, len(requested.Models))
	for _, modelID := range requested.Models {
		allowed[modelID] = struct{}{}
	}
	filtered := make([]config.LLMProviderModelSpec, 0, len(oldSpecs))
	for _, spec := range oldSpecs {
		if _, keep := allowed[spec.ID]; keep {
			filtered = append(filtered, spec)
		}
	}
	oldModelIDs := make(map[string]struct{}, len(old.Models))
	for _, modelID := range old.Models {
		oldModelIDs[modelID] = struct{}{}
	}
	for _, modelID := range requested.Models {
		if _, existed := oldModelIDs[modelID]; existed {
			continue
		}
		filtered = append(filtered, config.LLMProviderModelSpec{
			ID:           modelID,
			DisplayName:  modelID,
			Capabilities: []string{config.LLMModelCapabilityText},
		})
	}
	return config.LLMModelSpecsModeExplicit, filtered
}

func isEmbeddingOnlyCompletionModel(llmCfg config.LLMConfig, providerInstanceID, providerType, baseURL, modelID string) bool {
	if providerInstanceID != "" {
		for providerKey, provider := range llmCfg.Providers {
			if config.EffectiveProviderInstanceID(providerKey, provider) == providerInstanceID {
				return config.ModelHasCapability(provider, modelID, config.LLMModelCapabilityEmbedding) &&
					!config.ModelHasCapability(provider, modelID, config.LLMModelCapabilityText)
			}
		}
	}
	if _, ok := config.MigrateOpenRouterEmbeddingModelSpec(modelID); ok {
		return true
	}
	if providerInstanceID != "" {
		return false
	}

	normalizedBaseURL := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	embeddingOnly := false
	for providerKey, provider := range llmCfg.Providers {
		if normalizedBaseURL != "" {
			if strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/") != normalizedBaseURL {
				continue
			}
		} else if !strings.EqualFold(strings.TrimSpace(providerKey), strings.TrimSpace(providerType)) {
			continue
		}
		// 临时旧请求缺少稳定身份时，不能借同地址其它实例的向量声明误拦文字协议。
		if config.ModelHasCapability(provider, modelID, config.LLMModelCapabilityText) {
			return false
		}
		if config.ModelHasCapability(provider, modelID, config.LLMModelCapabilityEmbedding) {
			embeddingOnly = true
		}
	}
	return embeddingOnly
}

// validateConfiguredTextModel is the shared API-boundary guard for operations
// that necessarily invoke a completion model (chat, agent binding and tool
// capability probes). A configured provider/model must be explicitly eligible
// for text routing; unknown and embedding-only rows fail closed.
func validateConfiguredTextModel(llmCfg config.LLMConfig, providerName, modelID string) error {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return fmt.Errorf("model 不能为空")
	}
	if _, intrinsicEmbeddingOnly := config.MigrateOpenRouterEmbeddingModelSpec(modelID); intrinsicEmbeddingOnly {
		return fmt.Errorf("model %q 是 embedding-only，不能执行 completion 或工具探测", modelID)
	}
	providerKey, ok := findLLMProviderKey(llmCfg, providerName)
	if !ok {
		return fmt.Errorf("指定的 provider %q 不存在", strings.TrimSpace(providerName))
	}
	provider := llmCfg.Providers[providerKey]
	if !config.ModelHasCapability(provider, modelID, config.LLMModelCapabilityText) {
		return fmt.Errorf("provider %q 的 model %q 不具备 text capability", providerKey, modelID)
	}
	return nil
}

func validateRequestedCompletionModel(llmCfg config.LLMConfig, providerName, modelID string) error {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" || strings.EqualFold(modelID, "auto") {
		return nil
	}
	if _, embeddingOnly := config.MigrateOpenRouterEmbeddingModelSpec(modelID); embeddingOnly {
		return fmt.Errorf("embedding-only 模型不能执行 completion")
	}
	providerName = strings.TrimSpace(providerName)
	if providerName == "" || strings.EqualFold(providerName, "auto") {
		// Dynamic/provider-less routing is checked again by the selector's
		// data-plane capability facade once the actual provider is known.
		return nil
	}
	return validateConfiguredTextModel(llmCfg, providerName, modelID)
}

func validateUniqueProviderInstanceIDs(providers map[string]config.LLMProviderConfig) error {
	seen := make(map[string]string, len(providers))
	for name, provider := range providers {
		id := provider.ProviderInstanceID
		if previous, exists := seen[id]; exists {
			return fmt.Errorf("provider %q 与 %q 复用了 provider_instance_id %q", name, previous, id)
		}
		seen[id] = name
	}
	return nil
}
