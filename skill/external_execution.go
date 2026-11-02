package skill

import (
	"context"
	"sync"
)

type externalExecutionKey struct{}

// WithExternalExecution 仅由派发层注入已经持久准备的调用身份。
func WithExternalExecution(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, externalExecutionKey{}, id)
}

func ExternalExecutionID(ctx context.Context) string {
	id, _ := ctx.Value(externalExecutionKey{}).(string)
	return id
}

type toolDispatchGrantKey struct{}
type toolDispatchGrant struct {
	mu     sync.RWMutex
	Tool   string
	Policy ExecutionPolicy
	Parent *toolDispatchGrant
}

// WithToolDispatchGrant 复用已经过完整钩子链的本次调用授权；档位切换后自动失效。
func WithToolDispatchGrant(ctx context.Context, name string, policy ExecutionPolicy) context.Context {
	previous, _ := ctx.Value(toolDispatchGrantKey{}).(*toolDispatchGrant)
	for grant := previous; grant != nil; grant = grant.Parent {
		if grant.Tool == name {
			grant.mu.Lock()
			grant.Policy = policy
			grant.mu.Unlock()
			return ctx
		}
	}
	return context.WithValue(ctx, toolDispatchGrantKey{}, &toolDispatchGrant{Tool: name, Policy: policy, Parent: previous})
}

func ToolDispatchAuthorized(ctx context.Context, name string) bool {
	policy := CurrentExecutionPolicy(ctx)
	granted, ok := ToolDispatchPolicy(ctx, name)
	return ok && granted == policy
}

// ToolDispatchPolicy 返回实际通过钩子时的版本，不把当前配置当作已有授权。
func ToolDispatchPolicy(ctx context.Context, name string) (ExecutionPolicy, bool) {
	for grant, _ := ctx.Value(toolDispatchGrantKey{}).(*toolDispatchGrant); grant != nil; grant = grant.Parent {
		if grant.Tool == name {
			grant.mu.RLock()
			policy := grant.Policy
			grant.mu.RUnlock()
			return policy, true
		}
	}
	return ExecutionPolicy{}, false
}
