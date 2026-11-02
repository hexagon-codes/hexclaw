package builtin

import (
	"context"
	"errors"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/skill"
)

// ComputerUseFunc 由应用装配同一引擎、浏览器和回执依赖，避免 Skill 反向依赖引擎。
type ComputerUseFunc func(context.Context, map[string]any) (*skill.Result, error)

type ComputerUseSkill struct{ execute ComputerUseFunc }

func NewComputerUseSkill(execute ComputerUseFunc) *ComputerUseSkill {
	return &ComputerUseSkill{execute: execute}
}

func (*ComputerUseSkill) Name() string { return "computer_use" }
func (*ComputerUseSkill) Description() string {
	return "Use the connected browser visually and return actual task results"
}
func (*ComputerUseSkill) Match(string) bool { return false }

func (*ComputerUseSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("computer_use", "Complete a visual web task in the application's connected browser. Preserve the existing target and login when required. Return actual results; screenshots or proposed actions alone do not prove task completion. Provider/model are optional exact selections only when explicitly requested for this computer-use task.", &llm.Schema{
		Type: "object",
		Properties: map[string]*llm.Schema{
			"task":             {Type: "string", Description: "The complete user-authorized web task and desired result"},
			"server":           {Type: "string", Description: "Existing browser MCP service identity, when specified"},
			"profile":          {Type: "string", Description: "Existing browser profile identity, when specified"},
			"page":             {Type: "string", Description: "Stable page identity from the current browser session"},
			"existing_session": {Type: "boolean", Description: "True when the task requires an already logged-in browser session"},
			"provider":         {Type: "string", Description: "Exact configured provider only if explicitly selected for this task"},
			"model":            {Type: "string", Description: "Exact model only if explicitly selected for this task"},
		},
		Required: []string{"task"},
	})
}

func (s *ComputerUseSkill) Execute(ctx context.Context, args map[string]any) (*skill.Result, error) {
	if s == nil || s.execute == nil {
		return nil, errors.New("computer use runtime is unavailable")
	}
	return s.execute(ctx, args)
}
