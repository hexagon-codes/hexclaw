package mcp

import (
	"context"

	coretool "github.com/hexagon-codes/ai-core/tool"
)

// ToolDispatchHook 在唯一服务器和上游工具解析完成后接入其实际执行链。
// execute 绑定本次解析的原连接，包含该物理调用的错误翻译和断连观测。
// 应用拒绝或租约错误直接返回，不被误归类为 SDK 传输断连。
type ToolDispatchHook func(ctx context.Context, serverName, toolName string, args map[string]any, execute func(context.Context) (coretool.Result, error)) (coretool.Result, error)

// SetToolDispatchHook 仅协调指定服务器；nil 恢复原直接调用方式。
// 应用在发布其私有服务器前注册，普通外部服务无需添加此钩子。
func (m *Manager) SetToolDispatchHook(serverName string, hook ToolDispatchHook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if hook == nil {
		delete(m.toolDispatchHooks, serverName)
		return
	}
	if m.toolDispatchHooks == nil {
		m.toolDispatchHooks = make(map[string]ToolDispatchHook)
	}
	m.toolDispatchHooks[serverName] = hook
}
