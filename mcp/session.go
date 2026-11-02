package mcp

import (
	"context"

	"github.com/hexagon-codes/ai-core/tool"
	hexagonmcp "github.com/hexagon-codes/hexagon/mcp"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// sessionHandle 约束同一连接上的协议操作与关闭所有权。
type sessionHandle interface {
	Tools() []tool.Tool
	Resources(context.Context) ([]*sdkmcp.Resource, error)
	ResourceTemplates(context.Context) ([]*sdkmcp.ResourceTemplate, error)
	ReadResource(context.Context, string) (*sdkmcp.ReadResourceResult, error)
	Prompts(context.Context) ([]*sdkmcp.Prompt, error)
	GetPrompt(context.Context, string, map[string]string) (*sdkmcp.GetPromptResult, error)
	Close() error
}

var connectStdioSession = func(ctx context.Context, command string, env map[string]string, args ...string) (sessionHandle, error) {
	return hexagonmcp.ConnectStdioSession(ctx, command, env, args...)
}
