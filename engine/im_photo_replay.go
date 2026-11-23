package engine

import (
	"context"
	"errors"
	"maps"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/storage"
)

// SetIMPhotoDocumentReader 接入按原消息核对所有权、文件摘要和大小的只读文档查询。
func (e *ReActEngine) SetIMPhotoDocumentReader(read func(context.Context, *storage.MessageRecord) ([]adapter.Attachment, error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.imPhotoDocumentReader = read
}

// ReplayCompletedIMPhotoReply 按原始私聊事件恢复已持久化的普通回复；没有终稿时只返回空结果。
// 投递状态归原入站和投递回执所有，助手消息本身不能证明已经发送。
func (e *ReActEngine) ReplayCompletedIMPhotoReply(ctx context.Context, msg *adapter.Message) (*adapter.Reply, error) {
	identity, authenticated := adapter.PlatformInstanceIdentityFromContext(ctx)
	if !authenticated || msg == nil || msg.Platform != adapter.PlatformDingtalk || identity.Platform != string(msg.Platform) ||
		msg.ID == "" || msg.UserID == "" || msg.UserID != msg.ChatID || msg.InstanceID == "" {
		return nil, errors.New("invalid IM photo replay origin")
	}
	requestID := storage.IMRequestIDForEvent(identity.Platform, identity.ID, msg.ID)
	if messageRequestID(msg) != requestID {
		return nil, errors.New("IM photo replay request identity mismatch")
	}
	reader, ok := e.store.(storage.IMPhotoReplyReader)
	if !ok {
		return nil, errors.New("scoped IM photo reply reader unavailable")
	}
	// 会话沿用原入站的实例名或 ID，请求身份始终绑定接收事件的物理实例。
	scope := storage.Session{ID: msg.SessionID, UserID: msg.UserID, Platform: identity.Platform, InstanceID: msg.InstanceID, ChatID: msg.ChatID}
	records, err := reader.FindIMPhotoReplies(ctx, scope, requestID)
	if err != nil || len(records) == 0 {
		return nil, err
	}
	if len(records) != 1 {
		return nil, errors.New("ambiguous IM photo assistant reply identity")
	}
	record := records[0]
	if record == nil || record.Role != "assistant" || record.RequestID != requestID || record.ID != canonicalAssistantMessageID(msg) ||
		record.SessionID == "" || (scope.ID != "" && record.SessionID != scope.ID) {
		return nil, errors.New("invalid IM photo assistant reply identity")
	}
	original := *msg
	original.SessionID = record.SessionID
	original.Metadata = maps.Clone(msg.Metadata)
	replay, err := e.restoreDurableAssistantRecord(ctx, &original, record, false, true)
	if err != nil {
		return nil, err
	}
	if replay.reply.MessageContent == nil {
		if err := finalizeProducerReply(replay.reply, original.Metadata); err != nil {
			return nil, err
		}
	}
	msg.SessionID = original.SessionID
	if agent := replay.reply.Metadata["routed_agent"]; agent != "" {
		msg.Metadata["routed_agent"] = agent
	}
	return replay.reply, nil
}
