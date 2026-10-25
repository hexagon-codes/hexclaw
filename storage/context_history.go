package storage

import "context"

// ContextHistoryReader 将模型窗口与可回放的原始消息分离，不改变公开历史查询。
type ContextHistoryReader interface {
	ListContextMessages(context.Context, string, int, int) ([]*MessageRecord, error)
}

// ContextHistoryWriter 在消息事务中标记已摘要内容，原消息及附件仍可按身份读取。
type ContextHistoryWriter interface {
	MarkContextCompacted(context.Context, string, []string, string) error
}

// AssistantReplyReader 按原会话和请求读取至多两个终稿，保留重复完成检测。
type AssistantReplyReader interface {
	FindAssistantReplies(context.Context, string, string) ([]*MessageRecord, error)
}

// ListContextMessages 兼容未提供独立上下文索引的存储读取实现。
func ListContextMessages(ctx context.Context, store Store, sessionID string, limit, offset int) ([]*MessageRecord, error) {
	if reader, ok := store.(ContextHistoryReader); ok {
		return reader.ListContextMessages(ctx, sessionID, limit, offset)
	}
	return store.ListMessages(ctx, sessionID, limit, offset)
}
