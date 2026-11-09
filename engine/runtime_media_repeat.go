package engine

import (
	"context"
	"fmt"

	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/skill"
)

type runtimeMediaIntent struct {
	origin mediaflow.Origin
}

func runtimeMediaOrigin(ctx context.Context) mediaflow.Origin {
	invocation := skill.CurrentInvocation(ctx)
	kind := "conversation"
	if dispatch := skill.SystemDispatchSource(ctx); dispatch != "" {
		kind = dispatch
	}
	return mediaflow.Origin{Owner: invocation.AuthenticatedUserID, Kind: kind, RequestID: invocation.RequestID, SessionID: invocation.SessionID, RequestDigest: invocation.RequestDigest}
}

// 只有当前运行已实际派发的入口身份可被复用，模型参数不能声明已有生成状态。
func (e *runtimeToolExecutor) mediaInvocationOrdinal(ctx context.Context, key, callID, repeated string) string {
	if repeated != "" {
		return repeated
	}
	origin := runtimeMediaOrigin(ctx)
	e.mu.Lock()
	defer e.mu.Unlock()
	origin.Ordinal = fmt.Sprintf("media:%d", e.mediaCalls)
	origin.ToolCallID = callID
	e.mediaCalls++
	if e.mediaFlow != nil {
		if e.mediaIntents == nil {
			e.mediaIntents = make(map[string]runtimeMediaIntent)
		}
		if _, exists := e.mediaIntents[key]; !exists {
			e.mediaIntents[key] = runtimeMediaIntent{origin: origin}
		}
	}
	return origin.Ordinal
}

// 第二次同参只查询原入口的已知部分保存失败；相同幂等键始终查回原任务。
func (e *runtimeToolExecutor) repeatedMediaOrdinal(ctx context.Context, key string, count int) string {
	if e.mediaFlow == nil || count != 2 {
		return ""
	}
	e.mu.Lock()
	intent, exists := e.mediaIntents[key]
	e.mu.Unlock()
	if !exists || intent.origin.Owner == "" || intent.origin.Ordinal == "" {
		return ""
	}
	current := runtimeMediaOrigin(ctx)
	origin := intent.origin
	if current.Owner != origin.Owner || current.Kind != origin.Kind || current.RequestID != origin.RequestID || current.SessionID != origin.SessionID || current.RequestDigest != origin.RequestDigest {
		return ""
	}
	operations, err := e.mediaFlow.RequestOperations(ctx, current.Owner, current)
	if err != nil {
		return ""
	}
	for _, operation := range operations {
		previous := operation.Request.Origin
		if operation.Owner == current.Owner && previous.Ordinal == origin.Ordinal && previous.ToolCallID == origin.ToolCallID && previous.RequestDigest == origin.RequestDigest && !operation.CancelRequested && operation.GenerationState == "succeeded" && operation.SaveState == "partial" && operation.ErrorCode == "persist_failed" && (operation.RecoveryKind == "staged_blob" || operation.RecoveryKind == "remote_url") {
			return origin.Ordinal
		}
	}
	return ""
}
