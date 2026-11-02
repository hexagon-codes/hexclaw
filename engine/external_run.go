package engine

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/skill"
)

type toolBudgetKey struct{}

// CurrentToolBudget 让嵌套模型调用共享原回合的预算，不另建可绕过的计量范围。
func CurrentToolBudget(ctx context.Context) *BudgetController {
	budget, _ := ctx.Value(toolBudgetKey{}).(*BudgetController)
	return budget
}
func withToolBudget(ctx context.Context, budget *BudgetController) context.Context {
	return context.WithValue(ctx, toolBudgetKey{}, budget)
}

func (e *ReActEngine) finalizeExternalRun(ctx context.Context, msg *adapter.Message) {
	e.mu.RLock()
	executor := e.toolExecutor
	e.mu.RUnlock()
	if executor != nil && executor.runFinalizer != nil {
		executor.runFinalizer(ctx, msg)
	}
}

// ReplayCompletedStream 只读取已有完整助手结果，不重新进入工具、Provider或运行完成器。
func (e *ReActEngine) ReplayCompletedStream(ctx context.Context, msg *adapter.Message) (<-chan *adapter.ReplyChunk, bool, error) {
	replay, err := e.loadDurableAssistantReply(ctx, msg, true)
	if err != nil || replay == nil {
		return nil, false, err
	}
	chunk, err := replay.streamChunk(msg.Metadata)
	if err != nil {
		return nil, false, err
	}
	delete(chunk.Metadata, durableAssistantReplayMetaKey)
	return singleDurableAssistantChunk(chunk), true, nil
}

// 快路径每个请求只执行一次匹配工具，恢复沿相同可信调用身份查证。
func withFastPathInvocation(ctx context.Context, msg *adapter.Message, sessionID, tool string) (context.Context, string) {
	ctx = withSkillInvocationContext(ctx, msg, sessionID, "", "")
	inv := skill.CurrentInvocation(ctx)
	identity := sha256.Sum256([]byte(inv.AuthenticatedUserID + "\x00" + inv.RequestID + "\x00" + tool))
	inv.ToolCallID = fmt.Sprintf("tc-%x", identity[:12])
	return skill.WithInvocationContext(ctx, inv), inv.ToolCallID
}
