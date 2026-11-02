package mcp

import (
	"context"
	"fmt"
	"sync"
)

type backgroundLifecycleKey struct{}

// IsBackgroundLifecycle 仅识别 Manager 内部发起的后台生命周期操作。
// 该来源标记不承载调用者权限，不能由 HTTP 参数或模型参数设置。
func IsBackgroundLifecycle(ctx context.Context) bool {
	background, _ := ctx.Value(backgroundLifecycleKey{}).(bool)
	return background
}

// BeforeReplaceHook 为有状态服务收敛动作，并在连接前确认旧实例关闭。
// hook 成功后必须返回 finish；该回调在候选发布或关闭后恰好执行一次。
// connected=true 表示候选已发布，false 表示连接失败或候选已被取代。
// 回调不得依赖可关闭的观测钩子维持会话正确性。
type BeforeReplaceHook func(ctx context.Context, serverName string, closeOld func() error) (finish func(connected bool) error, err error)

// SetBeforeReplace 仅为指定服务器启用替换协调，nil 恢复普通连接替换方式。
func (m *Manager) SetBeforeReplace(serverName string, hook BeforeReplaceHook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if hook == nil {
		delete(m.beforeReplace, serverName)
		return
	}
	if m.beforeReplace == nil {
		m.beforeReplace = make(map[string]BeforeReplaceHook)
	}
	m.beforeReplace[serverName] = hook
}

func (m *Manager) prepareReplacement(ctx context.Context, name string, revisions []uint64) (func(bool) error, error) {
	m.mu.RLock()
	hook := m.beforeReplace[name]
	revision := m.revisions[name]
	m.mu.RUnlock()
	if hook == nil {
		return nil, nil
	}
	if len(revisions) != 0 {
		revision = revisions[0]
	}
	finish, err := hook(ctx, name, func() error {
		m.mu.Lock()
		if m.closedLocked() || m.revisions[name] != revision {
			m.mu.Unlock()
			return fmt.Errorf("MCP replacement %q was superseded or manager closed", name)
		}
		old := m.servers[name]
		wasConnected := old != nil && old.connected
		if old != nil {
			old.connected = false
		}
		m.mu.Unlock()
		err := closeServer(old)
		if err != nil && canRetireClosedRPC(old) {
			// 仅移除已关闭的本地客户端；原清理错误仍返回，配置和未知动作归属不变。
			m.mu.Lock()
			if m.servers[name] == old && m.revisions[name] == revision {
				delete(m.servers, name)
			}
			m.mu.Unlock()
		}
		if wasConnected {
			m.hooks.fireDisconnected(ctx, name, "stateful replacement")
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if finish == nil {
		return nil, fmt.Errorf("MCP replacement coordinator %q returned no completion callback", name)
	}
	var once sync.Once
	var finishErr error
	return func(connected bool) error {
		once.Do(func() { finishErr = finish(connected) })
		return finishErr
	}, nil
}

func canRetireClosedRPC(server *connectedServer) bool {
	if server == nil || server.transportCloseErr == nil || server.replacementCloseErr != nil {
		return false
	}
	switch server.transport {
	case "sse", "streamable", "http":
	default:
		return false
	}
	closed, ok := server.session.(interface{ LocalRPCClosed() bool })
	return ok && closed.LocalRPCClosed()
}

// finishPublishedServer 只在候选发布后登记新会话，登记失败立即停止该候选。
func (m *Manager) finishPublishedServer(ctx context.Context, server *connectedServer) error {
	if server.replacementFinish == nil {
		return nil
	}
	m.mu.RLock()
	current := m.servers[server.name] == server && !m.closedLocked()
	m.mu.RUnlock()
	if !current {
		_ = closeServer(server)
		return fmt.Errorf("MCP replacement %q was superseded before activation", server.name)
	}
	if err := server.replacementFinish(true); err != nil {
		_ = closeServer(server)
		return fmt.Errorf("MCP replacement %q activation failed: %w", server.name, err)
	}
	m.mu.Lock()
	current = m.servers[server.name] == server && !m.closedLocked()
	if current {
		server.connected = true
	}
	m.mu.Unlock()
	if !current {
		_ = closeServer(server)
		return fmt.Errorf("MCP replacement %q was superseded during activation", server.name)
	}
	return nil
}
