package engine

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
)

const unitSummaryToolName = "k12_unit_summary"

// 模板身份只要求进入语义解析，最终正文仍决定学科、动作、范围和是否生成。
func hasTypedUnitSummaryInvocation(msg *adapter.Message) bool {
	if msg == nil || isSystemDispatch(msg) || strings.TrimSpace(msg.Metadata["routed_agent"]) == "" {
		return false
	}
	var hint struct {
		Version  int    `json:"version"`
		Kind     string `json:"kind"`
		Scenario string `json:"scenario"`
		TaskKind string `json:"task_kind"`
	}
	if json.Unmarshal([]byte(msg.Metadata["k12_task_intent"]), &hint) != nil {
		return false
	}
	return hint.Version == 1 && hint.Kind == "unit_summary" && hint.Scenario == "k12" && hint.TaskKind == "unit_summary"
}

// 已打开资料仅使领域工具可选，不强制更新；真实归属和版本仍由领域用例核验。
func hasActiveUnitSummaryMaterial(msg *adapter.Message) bool {
	if msg == nil || isSystemDispatch(msg) || strings.TrimSpace(msg.Metadata["routed_agent"]) == "" {
		return false
	}
	var material struct {
		Version    int    `json:"version"`
		DocumentID string `json:"document_id"`
		RevisionID string `json:"revision_id"`
	}
	if json.Unmarshal([]byte(msg.Metadata["k12_active_material"]), &material) != nil {
		return false
	}
	return material.Version == 1 && strings.TrimSpace(material.DocumentID) != "" && strings.TrimSpace(material.RevisionID) != ""
}

func (e *ReActEngine) ensureUnitSummaryTool(tools []llm.ToolDefinition, msg *adapter.Message) []llm.ToolDefinition {
	if !hasTypedUnitSummaryInvocation(msg) && !hasActiveUnitSummaryMaterial(msg) {
		return tools
	}
	e.mu.RLock()
	registry := e.skills
	e.mu.RUnlock()
	if registry == nil {
		return tools
	}
	if enabled, found := registry.IsEnabled(unitSummaryToolName); !found || !enabled {
		return tools
	}
	s, found := registry.Get(unitSummaryToolName)
	if !found {
		return tools
	}
	def := s.ToolDefinition()
	if def.Type == "" {
		def.Type = "function"
	}
	// 资料调用复用当前工具策略，不能因模板身份绕过既有继承规则。
	front := applyInheritedToolPolicy(msg, []llm.ToolDefinition{def})
	if len(front) == 0 {
		return tools
	}
	for _, tool := range tools {
		if tool.Function.Name != unitSummaryToolName {
			front = append(front, tool)
		}
	}
	return front
}

type unitSummaryToolChoiceProvider struct{ provider hexagon.Provider }

func wrapUnitSummaryToolChoiceProvider(provider hexagon.Provider, msg *adapter.Message) hexagon.Provider {
	if provider == nil || !hasTypedUnitSummaryInvocation(msg) {
		return provider
	}
	return preserveContextTokenCounter(&unitSummaryToolChoiceProvider{provider: provider}, provider)
}

func (p *unitSummaryToolChoiceProvider) Name() string            { return p.provider.Name() }
func (p *unitSummaryToolChoiceProvider) Models() []llm.ModelInfo { return p.provider.Models() }
func (p *unitSummaryToolChoiceProvider) CountTokens(messages []llm.Message) (int, error) {
	return p.provider.CountTokens(messages)
}
func (p *unitSummaryToolChoiceProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return p.provider.Complete(ctx, applyUnitSummaryToolChoice(req))
}
func (p *unitSummaryToolChoiceProvider) Stream(ctx context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	return p.provider.Stream(ctx, applyUnitSummaryToolChoice(req))
}

func applyUnitSummaryToolChoice(req llm.CompletionRequest) llm.CompletionRequest {
	if !completionRequestHasTool(req.Tools, unitSummaryToolName) || len(req.Messages) == 0 {
		return req
	}
	// 工具回执之后不再强制调用；一次任务接纳和普通最终回复保持同一请求链。
	if req.Messages[len(req.Messages)-1].Role != llm.RoleUser || (req.ToolChoice != nil && req.ToolChoice != "auto") {
		return req
	}
	req.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": unitSummaryToolName}}
	return req
}
