package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/knowledge"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/skill"
)

// WorkflowStepOptions 仅由工作流执行入口携带，既有会话没有该上下文时行为保持。
// 请求快照保存配置与正文依据，检索命中和工具结果仍由真实执行另外记录。
type WorkflowStepOptions struct {
	Agent           *agentrouter.AgentConfig `json:"agent,omitempty"`
	SkillPrompt     *string                  `json:"skill_prompt,omitempty"`
	SkillReceipts   []skill.ContextReceipt   `json:"skill_receipts,omitempty"`
	ToolsMode       string                   `json:"tools_mode,omitempty"`
	ToolDefinitions []llm.ToolDefinition     `json:"tool_definitions,omitempty"`
	KnowledgeMode   string                   `json:"knowledge_mode,omitempty"`
	KnowledgeFilter knowledge.Filter         `json:"knowledge_filter"`
	MemoryRetrieval string                   `json:"memory_retrieval,omitempty"`
	MemoryQuery     string                   `json:"memory_query,omitempty"`
	MemoryWriteback string                   `json:"memory_writeback,omitempty"`
}

type workflowStepOptionsKey struct{}

// WithWorkflowStepOptions 给本次步骤增加执行配置，不修改共享引擎或注册智能体。
func WithWorkflowStepOptions(ctx context.Context, options *WorkflowStepOptions) context.Context {
	if options.KnowledgeMode == "override" {
		ctx = knowledge.WithDocumentScope(ctx, options.KnowledgeFilter.DocumentIDs)
	}
	return context.WithValue(ctx, workflowStepOptionsKey{}, options)
}

func workflowStepOptions(ctx context.Context) *WorkflowStepOptions {
	options, _ := ctx.Value(workflowStepOptionsKey{}).(*WorkflowStepOptions)
	return options
}

// FreezeWorkflowSkills 复用既有正文加载与回执格式；显式引用失效报错，继承沿既有跳过规则。
func (e *ReActEngine) FreezeWorkflowSkills(names []string, explicit bool) (string, []skill.ContextReceipt, error) {
	e.mu.RLock()
	registry := e.skills
	e.mu.RUnlock()
	if explicit {
		for _, name := range names {
			if registry == nil {
				return "", nil, fmt.Errorf("Skill is unavailable: %s", name)
			}
			enabled, exists := registry.IsEnabled(name)
			if !enabled || !exists {
				return "", nil, fmt.Errorf("Skill is unavailable: %s", name)
			}
		}
	}
	prompt, receipts := e.buildMountedSkillsContext(names, !explicit)
	return prompt, receipts, nil
}

func (e *ReActEngine) queryWorkflowKnowledge(ctx context.Context, query string, topK int) (string, []knowledge.SearchHit, error) {
	if options := workflowStepOptions(ctx); options != nil && options.KnowledgeMode == "override" {
		if len(options.KnowledgeFilter.DocumentIDs) == 0 {
			return "", nil, nil
		}
		// 显式选定资料沿用户检索语义读取，纯词法知识库仍可使用；自动注入保持自己的相关度判据。
		hits, err := e.kb.SearchWithFilter(ctx, query, topK, options.KnowledgeFilter)
		if err != nil {
			return "", nil, err
		}
		return encodeKnowledgeEvidence(hits), hits, nil
	}
	return e.kb.QueryHits(ctx, query, topK)
}

func workflowKnowledgeRequested(ctx context.Context) bool {
	options := workflowStepOptions(ctx)
	return options != nil && options.KnowledgeMode == "override"
}

// 工作流显式知识范围独立于正文技能；普通会话继续沿用人设独占的上下文规则。
func (e *ReActEngine) shouldUseKnowledgeContext(ctx context.Context, metadata map[string]string) bool {
	if options := workflowStepOptions(ctx); options != nil {
		switch options.KnowledgeMode {
		case "none":
			return false
		case "override":
			return true
		}
	}
	return !e.hasMountedPersonaSkill(metadata)
}

func (e *ReActEngine) workflowTools(ctx context.Context, tools []llm.ToolDefinition) []llm.ToolDefinition {
	options := workflowStepOptions(ctx)
	if options == nil {
		return tools
	}
	if options.ToolsMode == "none" {
		return nil
	}
	if options.ToolsMode == "override" {
		tools = append([]llm.ToolDefinition(nil), options.ToolDefinitions...)
	}
	filtered := make([]llm.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if workflowToolAllowed(options, tool.Function.Name, nil) == nil {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func workflowExplicitTools(ctx context.Context) bool {
	options := workflowStepOptions(ctx)
	return options != nil && options.ToolsMode == "override"
}

func workflowToolAllowed(options *WorkflowStepOptions, name string, args map[string]any) error {
	if options == nil {
		return nil
	}
	if options.ToolsMode == "none" {
		return fmt.Errorf("Tools are disabled for this workflow step")
	}
	if options.ToolsMode == "override" && !slices.ContainsFunc(options.ToolDefinitions, func(tool llm.ToolDefinition) bool { return tool.Function.Name == name }) {
		return fmt.Errorf("Tool is not selected for this workflow step: %s", name)
	}
	if options.KnowledgeMode == "none" && name == "knowledge_search" {
		return fmt.Errorf("Knowledge retrieval is disabled for this workflow step")
	}
	if options.MemoryRetrieval == "none" && name == "session_search" {
		return fmt.Errorf("Memory retrieval is disabled for this workflow step")
	}
	if name == "manage_memory" {
		action, _ := args["action"].(string)
		read := slices.Contains([]string{"search", "list", "get"}, strings.ToLower(action))
		if read && options.MemoryRetrieval == "none" || !read && (options.MemoryWriteback == "none" || options.MemoryWriteback == "suggest") {
			return fmt.Errorf("Memory operation is not enabled for this workflow step")
		}
	}
	return nil
}
