package storage

import (
	"context"
	"database/sql"

	"github.com/hexagon-codes/hexclaw/adapter"
)

// AssistantCompletion 在原消息事务内提交领域投递意图，附件字节不写入模型历史。
type AssistantCompletion struct {
	Attachments []adapter.Attachment
	// RequiredContent 来自已核对的冻结产物，原回合不能只保存模型对产物的摘要。
	RequiredContent []string
	// OwnsDelivery 表示本完成器在原消息事务中登记唯一渠道投递意图。
	OwnsDelivery bool
	// Commit 不提交或回滚外层事务；true 表示已存在同一终稿，可直接完成幂等读回。
	Commit func(context.Context, *sql.Tx, *MessageRecord) (bool, error)
}

type assistantCompletionKey struct{}

func WithAssistantCompletion(ctx context.Context, completion AssistantCompletion) context.Context {
	return context.WithValue(ctx, assistantCompletionKey{}, completion)
}

func AssistantCompletionFromContext(ctx context.Context) (AssistantCompletion, bool) {
	completion, ok := ctx.Value(assistantCompletionKey{}).(AssistantCompletion)
	return completion, ok
}
