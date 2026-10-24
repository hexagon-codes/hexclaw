package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/session"
)

// recoverMediaRequest 查回已接纳的媒体步骤，不能用重新规划代替原操作恢复。
func (e *ReActEngine) recoverMediaRequest(ctx context.Context, msg *adapter.Message) (*adapter.Reply, bool, error) {
	if e.mediaFlow == nil || messageRequestID(msg) == "" {
		return nil, false, nil
	}
	ops, err := e.mediaFlow.RequestOperations(ctx, msg.UserID, mediaflow.Origin{Kind: "conversation", SessionID: msg.SessionID, RequestID: messageRequestID(msg)})
	if err != nil {
		return nil, false, err
	}
	if len(ops) == 0 {
		return nil, false, nil
	}
	sourceDigest := mediaflow.SourceRequestDigest(msg.Content, msg.Attachments)
	for _, op := range ops {
		if op.Request.Origin.RequestDigest != "" && op.Request.Origin.RequestDigest != sourceDigest {
			return nil, true, mediaflow.ErrConflict
		}
		if _, err := e.mediaFlow.Begin(ctx, op.Request); err != nil {
			return nil, true, err
		}
	}
	if err := mediaflow.RetainRequest(ctx); err != nil {
		return nil, true, err
	}
	return e.finishMediaRecovery(ctx, msg, ops)
}

func (e *ReActEngine) finishMediaRecovery(ctx context.Context, msg *adapter.Message, ops []*mediaflow.Operation) (*adapter.Reply, bool, error) {
	var err error
	var assets []mediaflow.Asset
	var statuses []string
	for _, op := range ops {
		if op.GenerationState != "unknown" && !op.Settled() {
			op, err = e.mediaFlow.Wait(ctx, msg.UserID, op.ID, false)
			if err != nil {
				return nil, true, err
			}
		}
		if op.CancelRequested {
			return nil, true, fmt.Errorf("media operation %s: cancel requested", op.ID)
		}
		assets = append(assets, op.Assets...)
		statuses = append(statuses, fmt.Sprintf("%s: generation=%s, persistence=%s", op.ID, op.GenerationState, op.SaveState))
	}
	content := "Recovered media operation status. Other interrupted conversation steps are unconfirmed.\n\n" + strings.Join(statuses, "\n")
	metadata := map[string]string{"producer_kind": "chat", "locale": msg.Metadata["user_locale"]}
	var operationIDs []string
	for _, op := range ops {
		operationIDs = append(operationIDs, op.ID)
	}
	encodedIDs, _ := json.Marshal(operationIDs)
	metadata["media_operations"] = string(encodedIDs)
	if len(assets) > 0 {
		encoded, err := json.Marshal(assets)
		if err != nil {
			return nil, true, err
		}
		metadata["artifacts"] = string(encoded)
	}
	completionCtx, content, err := e.prepareMediaReply(ctx, msg, metadata, content)
	if err != nil {
		return nil, true, err
	}
	record, err := e.sessions.SaveAssistantReply(completionCtx, msg.SessionID, content, session.AssistantMeta{AgentName: assistantAgentName(msg.Metadata), RequestID: messageRequestID(msg), ReplyMetadata: metadata})
	if err != nil {
		return nil, true, err
	}
	metadata = withAssistantMessageID(metadata, record.ID)
	return &adapter.Reply{Content: content, Metadata: metadata, AssistantMessageID: record.ID}, true, nil
}

// resumeInterruptedMediaTurn 仅接续已接纳的媒体事实，不重新运行模型或原工具循环。
func (e *ReActEngine) resumeInterruptedMediaTurn(ctx context.Context, op *mediaflow.Operation) error {
	origin := op.Request.Origin
	if origin.SessionID == "" || origin.RequestID == "" || op.CancelRequested {
		return nil
	}
	key := op.Owner + "\x00" + origin.RequestID
	e.mu.RLock()
	active := e.mediaRequests[key] > 0
	e.mu.RUnlock()
	if active {
		return nil
	}
	unlock, err := e.acquireSessionLane(ctx, origin.SessionID, origin.RequestID)
	if err != nil {
		return err
	}
	if unlock != nil {
		defer unlock()
	}
	e.mu.RLock()
	active = e.mediaRequests[key] > 0
	e.mu.RUnlock()
	if active {
		return nil
	}
	messageID := origin.RequestID + ":assistant"
	if previous, err := e.store.GetMessage(ctx, messageID); err == nil {
		if previous.SessionID != origin.SessionID {
			return mediaflow.ErrConflict
		}
		return nil
	}
	sessionRow, err := e.store.GetSession(ctx, origin.SessionID)
	if err != nil {
		return err
	}
	if sessionRow.UserID != op.Owner {
		return fmt.Errorf("media recovery owner mismatch")
	}
	ops, err := e.mediaFlow.RequestOperations(ctx, op.Owner, origin)
	if err != nil {
		return err
	}
	for _, pending := range ops {
		if !pending.Settled() || pending.CancelRequested {
			return nil
		}
	}
	msg := &adapter.Message{ID: origin.RequestID, UserID: op.Owner, SessionID: origin.SessionID, Platform: adapter.Platform(origin.Platform), InstanceID: origin.InstanceID, ChatID: origin.ChatID, Metadata: map[string]string{"request_id": origin.RequestID, "routed_agent": origin.AgentName}}
	if origin.InstanceID != "" {
		ctx = adapter.WithPlatformInstanceIdentity(ctx, origin.InstanceID, origin.Platform)
	}
	ctx = session.WithAssistantMessageID(ctx, messageID)
	_, _, err = e.finishMediaRecovery(ctx, msg, ops)
	return err
}
