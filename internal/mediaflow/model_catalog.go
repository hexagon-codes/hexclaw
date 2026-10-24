package mediaflow

import (
	"context"
	"net/url"
	"sort"
	"strings"

	imagegen "github.com/hexagon-codes/ai-core/media/image"
	videogen "github.com/hexagon-codes/ai-core/media/video"
	"github.com/hexagon-codes/ai-core/media/voicechat"

	"github.com/hexagon-codes/hexclaw/config"
)

// NativeProviderModelSpecs 将已实现原生协议的 SDK 目录投影给管理与执行入口。
func NativeProviderModelSpecs(baseURL string) []config.LLMProviderModelSpec {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" {
		return nil
	}
	var result []config.LLMProviderModelSpec
	appendModels := func(models []string, capability string) {
		for _, model := range models {
			result = append(result, config.LLMProviderModelSpec{ID: model, Capabilities: []string{capability}})
		}
	}
	if u.Host == "open.bigmodel.cn" && strings.TrimRight(u.Path, "/") == "/api/paas/v4" {
		appendModels(imagegen.NewZhipuCogView("", "").SupportedModels(), config.LLMModelCapabilityImageGeneration)
		appendModels(videogen.NewZhipuCogVideoX("", "").SupportedModels(), config.LLMModelCapabilityVideoGeneration)
	}
	if u.Host == "api.openai.com" && strings.TrimRight(u.Path, "/") == "/v1" {
		appendModels(imagegen.NewOpenAIDallE("", "").SupportedModels(), config.LLMModelCapabilityImageGeneration)
		appendModels(voicechat.NewOpenAIVoiceChat("", "").SupportedModels(), config.LLMModelCapabilityAudio)
	}
	return result
}

// ObservedModelSpecs 只以同一供应商已成功保存的媒体结果补充精确模型能力，不推断可用权限。
func (s *Service) ObservedModelSpecs(ctx context.Context, providerID string) ([]config.LLMProviderModelSpec, error) {
	reader, ok := s.repo.(interface {
		SuccessfulModelKinds(context.Context, string) (map[string][]string, error)
	})
	if !ok {
		return nil, nil
	}
	models, err := reader.SuccessfulModelKinds(ctx, providerID)
	if err != nil {
		return nil, err
	}
	var result []config.LLMProviderModelSpec
	for model, kinds := range models {
		sort.Strings(kinds)
		var capabilities []string
		for _, kind := range kinds {
			switch kind {
			case "image":
				capabilities = append(capabilities, config.LLMModelCapabilityImageGeneration)
			case "video":
				capabilities = append(capabilities, config.LLMModelCapabilityVideoGeneration)
			case "audio":
				capabilities = append(capabilities, config.LLMModelCapabilityAudio)
			}
		}
		if model != "" && len(capabilities) > 0 {
			result = append(result, config.LLMProviderModelSpec{ID: model, Capabilities: capabilities})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
