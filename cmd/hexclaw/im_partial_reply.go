package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/documentflow"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/storage"
)

type imPartialDocumentStore interface {
	GetMessage(context.Context, string) (*storage.MessageRecord, error)
}

type imPartialDocumentDependencies struct {
	store             imPartialDocumentStore
	documents         *documentflow.Service
	resolveInstanceID func(string, string) (string, error)
	prepareDelivery   func(context.Context, *adapter.Message) (bool, error)
	send              func(context.Context, string, string, *adapter.Reply) error
	getPrintReply     func(context.Context, string, string) (k12.ConversationPrintReply, error)
}

// maybeDeliverIMPartialDocuments 只交付原助手消息已登记的普通文档，原处理错误保持不变。
// 媒体交付不拥有这些独立文档；打印回复已接管同一助手附件时沿原 Outbox 继续。
func maybeDeliverIMPartialDocuments(ctx context.Context, msg *adapter.Message, reply *adapter.Reply, handlerErr error, deps imPartialDocumentDependencies) (*adapter.Reply, error) {
	if handlerErr == nil || msg == nil || reply == nil || ctx.Err() != nil ||
		errors.Is(handlerErr, context.Canceled) ||
		msg.ID == "" || msg.InstanceID == "" || msg.UserID == "" || msg.ChatID == "" ||
		reply.AssistantMessageID == "" || reply.DeliveryAttempted(msg) ||
		deps.store == nil || deps.documents == nil || deps.send == nil || deps.resolveInstanceID == nil || deps.prepareDelivery == nil {
		return reply, handlerErr
	}
	attachments, instanceID, owned, err := prepareIMPartialDocuments(ctx, msg, reply, deps)
	if err != nil {
		slog.WarnContext(ctx, "IM partial document preparation failed", "error", err)
		return reply, handlerErr
	}
	if owned || len(attachments) == 0 || ctx.Err() != nil {
		return reply, handlerErr
	}
	prepared, err := deps.prepareDelivery(ctx, msg)
	if err != nil {
		slog.WarnContext(ctx, "IM partial document delivery preparation failed", "error_type", fmt.Sprintf("%T", err))
		return reply, handlerErr
	}
	if !prepared || ctx.Err() != nil {
		return reply, handlerErr
	}
	// 只发送已冻结文档，不重复原媒体、打印动作、模型或其它工具任务。
	outgoing := &adapter.Reply{Attachments: attachments, AssistantMessageID: reply.AssistantMessageID,
		BackendMessageID: reply.BackendMessageID, MessageID: reply.MessageID}
	// 入站事件在调用前已耐久接纳；一旦派发，发送结果未知也不能由原处理错误触发重投。
	reply.MarkDeliveryAttempted(msg)
	if err := deps.send(ctx, instanceID, msg.ChatID, outgoing); err != nil {
		slog.WarnContext(ctx, "IM partial document delivery remains unconfirmed", "error_type", fmt.Sprintf("%T", err))
	}
	return reply, handlerErr
}

func prepareIMPartialDocuments(ctx context.Context, msg *adapter.Message, reply *adapter.Reply, deps imPartialDocumentDependencies) ([]adapter.Attachment, string, bool, error) {
	saved, err := deps.store.GetMessage(ctx, reply.AssistantMessageID)
	if err != nil {
		return nil, "", false, err
	}
	requestID := msg.Metadata["request_id"]
	if requestID == "" {
		requestID = msg.ID
	}
	if saved == nil || saved.Role != "assistant" || saved.ID != reply.AssistantMessageID ||
		saved.SessionID != msg.SessionID || saved.RequestID != requestID {
		return nil, "", false, fmt.Errorf("partial document reply source mismatch")
	}
	attachments, err := deps.documents.ResolveMessageAttachments(ctx, saved)
	if err != nil || len(attachments) == 0 {
		return nil, "", false, err
	}
	instanceID, err := deps.resolveInstanceID(string(msg.Platform), msg.InstanceID)
	if err != nil {
		return nil, "", false, err
	}
	agentName := reply.Metadata["routed_agent"]
	if agentName == "" {
		agentName = msg.Metadata["routed_agent"]
	}
	for _, attachment := range attachments {
		artifact, err := deps.documents.Get(ctx, msg.UserID, attachment.ID)
		if err != nil {
			return nil, "", false, err
		}
		origin := artifact.Origin
		originInstance := origin.InstanceID
		if originInstance != instanceID {
			originInstance, err = deps.resolveInstanceID(origin.Platform, originInstance)
			if err != nil {
				return nil, "", false, err
			}
		}
		if origin.Owner != msg.UserID || origin.SessionID != msg.SessionID || origin.RequestID != requestID ||
			origin.Platform != string(msg.Platform) || originInstance != instanceID || origin.ChatID != msg.ChatID {
			return nil, "", false, fmt.Errorf("partial document delivery target mismatch")
		}
		if agentName == "" {
			agentName = origin.AgentName
		}
	}
	if deps.getPrintReply != nil && agentName != "" {
		source, err := deps.getPrintReply(ctx, agentName, saved.ID)
		if err == nil {
			originInstance := source.InstanceID
			if originInstance != instanceID {
				originInstance, err = deps.resolveInstanceID(source.Platform, originInstance)
			}
			if err != nil || source.Owner != msg.UserID || source.SessionID != msg.SessionID || source.RequestID != requestID ||
				source.AssistantMessageID != saved.ID || source.Platform != string(msg.Platform) || originInstance != instanceID || source.ChatID != msg.ChatID {
				return nil, "", false, fmt.Errorf("partial document print source mismatch")
			}
			return nil, instanceID, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, records.ErrNotFound) {
			return nil, "", false, err
		}
	}
	return attachments, instanceID, false, nil
}
