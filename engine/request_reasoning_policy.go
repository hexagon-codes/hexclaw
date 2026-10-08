package engine

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
)

const agentRequestReasoningPolicyKey = "agent_request_reasoning_policy"

type requestReasoningPolicyContextKey struct{}

type requestReasoningPolicySnapshot struct {
	global    config.ReasoningPolicy
	frozen    bool
	inherited bool
	thinking  string
	effort    string
}

// 全局策略在请求接纳时复制；热更新只影响之后接纳的新请求。
func (e *ReActEngine) beginRequestReasoningPolicy(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestReasoningPolicyContextKey{}, &requestReasoningPolicySnapshot{
		global: e.ActiveLLMConfig().DefaultReasoningPolicy,
	})
}

func recordAgentRequestReasoningPolicy(metadata map[string]string, policy *config.ReasoningPolicy) {
	delete(metadata, agentRequestReasoningPolicyKey)
	if policy == nil || policy.Mode == config.ReasoningPolicyModeInherit {
		return
	}
	encoded, err := json.Marshal(policy)
	if err == nil {
		metadata[agentRequestReasoningPolicyKey] = string(encoded)
	}
}

// 显式请求优先；Agent 的 auto 与 inherit 不同，不能继续继承全局开关。
func freezeRequestReasoningPolicy(ctx context.Context, msg *adapter.Message) {
	state, ok := ctx.Value(requestReasoningPolicyContextKey{}).(*requestReasoningPolicySnapshot)
	if !ok || state.frozen || msg == nil {
		return
	}
	state.frozen = true
	ensureMessageMetadata(msg)
	defer func() {
		state.thinking = strings.TrimSpace(msg.Metadata["thinking"])
		state.effort = strings.TrimSpace(msg.Metadata["thinking_effort"])
	}()
	defer delete(msg.Metadata, agentRequestReasoningPolicyKey)
	if strings.TrimSpace(msg.Metadata["thinking"]) != "" {
		return
	}
	if strings.TrimSpace(msg.Metadata["thinking_effort"]) != "" {
		msg.Metadata["thinking"] = "on"
		return
	}
	policy := state.global
	state.inherited = true
	if raw := msg.Metadata[agentRequestReasoningPolicyKey]; raw != "" {
		var agentPolicy config.ReasoningPolicy
		if json.Unmarshal([]byte(raw), &agentPolicy) == nil && agentPolicy.Mode != config.ReasoningPolicyModeInherit {
			policy = agentPolicy
		}
	}
	delete(msg.Metadata, "thinking_effort")
	switch policy.Mode {
	case config.ReasoningPolicyModeOn:
		msg.Metadata["thinking"] = "on"
	case config.ReasoningPolicyModeOff:
		msg.Metadata["thinking"] = "off"
	case config.ReasoningPolicyModeEffort:
		msg.Metadata["thinking"] = "on"
		msg.Metadata["thinking_effort"] = string(policy.Effort)
	default:
		msg.Metadata["thinking"] = "auto"
	}
}

// 保存的继承偏好不等于当前模型可执行的档位；不能因此拒绝正常文字请求。
func (e *ReActEngine) freezeSelectedRequestReasoningPolicy(ctx context.Context, msg *adapter.Message, selection llmSelection) {
	state, ok := ctx.Value(requestReasoningPolicyContextKey{}).(*requestReasoningPolicySnapshot)
	if !ok || state.frozen {
		return
	}
	freezeRequestReasoningPolicy(ctx, msg)
	if !state.inherited || msg.Metadata["thinking_effort"] == "" {
		return
	}
	e.mu.RLock()
	router := e.router
	e.mu.RUnlock()
	if router == nil {
		return
	}
	provider, exists := router.ProviderConfig(selection.providerName)
	if !exists {
		return
	}
	support, control := config.ModelReasoningControl(provider, selection.modelName)
	if support != config.LLMReasoningSupportSupported || control == nil {
		return
	}
	if control.Dialect == config.LLMReasoningDialectEffort {
		for _, effort := range control.AllowedEfforts {
			if effort == msg.Metadata["thinking_effort"] {
				return
			}
		}
	}
	msg.Metadata["thinking"] = "auto"
	delete(msg.Metadata, "thinking_effort")
	state.thinking = "auto"
	state.effort = ""
}

// 消息层保留 auto 的来源与缓存身份，实际请求不携带自动模式控制参数。
// K12 已冻结的阶段级策略具有更高优先级，不受普通请求继承影响。
func applyFrozenRequestReasoningPolicy(ctx context.Context, req *hexagon.CompletionRequest, msg *adapter.Message) {
	if req == nil || msg == nil || req.ReasoningPolicyScope != "" {
		return
	}
	state, ok := ctx.Value(requestReasoningPolicyContextKey{}).(*requestReasoningPolicySnapshot)
	if !ok || !state.frozen {
		return
	}
	thinking := state.thinking
	if thinking == "auto" {
		delete(req.Metadata, "thinking")
		delete(req.Metadata, "thinking_effort")
		delete(req.Metadata, llm.ReasoningCapabilityMetadataKey)
		return
	}
	if thinking == "" {
		return
	}
	if req.Metadata == nil {
		req.Metadata = make(map[string]any, 2)
	}
	req.Metadata["thinking"] = thinking
	delete(req.Metadata, "thinking_effort")
	if effort := state.effort; effort != "" {
		req.Metadata["thinking_effort"] = effort
	}
}
