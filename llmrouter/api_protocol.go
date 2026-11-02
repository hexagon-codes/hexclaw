package llmrouter

import (
	"context"
	"strings"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/ai-core/llm/openai"

	"github.com/hexagon-codes/hexclaw/config"
)

const (
	APIProtocolAuto            = "auto"
	APIProtocolResponses       = "responses"
	APIProtocolChatCompletions = "chat_completions"
)

// EffectiveModelAPIProtocol 与工厂共享实际适配器及精确官方模型判断；名称相同的中转模型不继承官方协议。
func EffectiveModelAPIProtocol(name string, provider config.LLMProviderConfig, model string) string {
	if isOllamaProviderConfig(name, provider) {
		return "ollama"
	}
	if usesAnthropicNativeAdapter(name, provider) {
		return "anthropic"
	}
	switch strings.ToLower(strings.TrimSpace(provider.APIProtocol)) {
	case APIProtocolResponses:
		return APIProtocolResponses
	case APIProtocolChatCompletions:
		return APIProtocolChatCompletions
	}
	// 使用工厂实际解析后的地址，避免别名在探测与运行时被解析成不同默认协议。
	actual := provider
	actual.BaseURL = providerEndpointBaseURL(name, provider)
	if capabilities, official := config.OfficialModelCapabilities("openai", actual, strings.TrimSpace(model)); official {
		for _, capability := range capabilities {
			if capability == config.LLMModelCapabilityComputerUse {
				return APIProtocolResponses
			}
		}
	}
	return APIProtocolChatCompletions
}

func usesAnthropicNativeAdapter(name string, provider config.LLMProviderConfig) bool {
	compatible := strings.ToLower(strings.TrimSpace(provider.Compatible))
	if compatible != "" {
		return compatible == "anthropic"
	}
	return strings.EqualFold(strings.TrimSpace(name), "anthropic")
}

// WithModelAPIProtocol 仅设置当前物理请求的协议，不保存 response ID 或修改全局模型。
func WithModelAPIProtocol(ctx context.Context, name string, provider config.LLMProviderConfig, model, scope string) context.Context {
	if EffectiveModelAPIProtocol(name, provider, model) == APIProtocolResponses {
		return openai.WithResponsesProtocol(ctx, scope)
	}
	return openai.WithChatCompletionsProtocol(ctx)
}

// OpenAI 的可选接口通过嵌入原适配器保留，避免普通协议包装隐藏向量和 Computer Use 能力。
type openAIProtocolProvider struct {
	*openai.Provider
	name   string
	config config.LLMProviderConfig
}

func (p *openAIProtocolProvider) Complete(ctx context.Context, request llm.CompletionRequest) (*llm.CompletionResponse, error) {
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = p.config.Model
	}
	return p.Provider.Complete(WithModelAPIProtocol(ctx, p.name, p.config, model, ""), request)
}

func (p *openAIProtocolProvider) Stream(ctx context.Context, request llm.CompletionRequest) (*llm.Stream, error) {
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = p.config.Model
	}
	return p.Provider.Stream(WithModelAPIProtocol(ctx, p.name, p.config, model, ""), request)
}
