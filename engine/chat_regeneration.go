package engine

import (
	"context"
	"fmt"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/session"
)

// SupportsChatRegeneration 仅对具备原子消息替换能力的会话存储开放。
func (e *ReActEngine) SupportsChatRegeneration() bool {
	return e.sessions != nil && e.sessions.SupportsChatRegeneration()
}

func (e *ReActEngine) beginChatRegeneration(ctx context.Context, msg *adapter.Message) (context.Context, *session.ChatRegeneration, func(), error) {
	if msg == nil || msg.RegenerateFromMessageID == "" {
		return ctx, nil, func() {}, nil
	}
	if !e.SupportsChatRegeneration() {
		return ctx, nil, nil, fmt.Errorf("conversation regeneration is unavailable")
	}
	ctx, stage, err := e.sessions.BeginChatRegeneration(ctx, msg)
	if err != nil {
		return ctx, nil, nil, err
	}
	unlock, err := e.acquireSessionLane(ctx, msg.SessionID, messageRequestID(msg))
	if err != nil {
		return ctx, nil, nil, err
	}
	if unlock == nil {
		unlock = func() {}
	}
	return ctx, stage, unlock, nil
}

func (e *ReActEngine) commitRegeneratedStream(ctx context.Context, msg *adapter.Message, stage *session.ChatRegeneration, terminal *adapter.ReplyChunk, snapshot adapter.RuntimeSnapshot, content string) {
	if terminal.Metadata == nil {
		terminal.Metadata = make(map[string]string)
	}
	terminal.Metadata["regenerate_from_message_id"] = msg.RegenerateFromMessageID
	if terminal.Error == nil && !stage.HasDraft() {
		_, terminal.Error = e.sessions.SaveAssistantReply(ctx, msg.SessionID, content, session.AssistantMeta{
			MessageID:     canonicalAssistantMessageID(msg),
			Provider:      terminal.Metadata["provider"],
			Model:         terminal.Metadata["model"],
			AgentName:     assistantAgentName(msg.Metadata),
			RequestID:     messageRequestID(msg),
			ToolCalls:     terminal.ToolCalls,
			Blocks:        terminal.Blocks,
			ReplyMetadata: terminal.Metadata,
		})
	}
	if terminal.Error == nil {
		id, err := e.sessions.CommitChatRegeneration(ctx, stage, &snapshot)
		if err == nil {
			terminal.Metadata = withAssistantMessageID(terminal.Metadata, id)
			terminal.AssistantMessageID, terminal.BackendMessageID, terminal.MessageID = id, id, id
			return
		}
		terminal.Error = err
		terminal.RuntimeEvent = &adapter.RuntimeEvent{
			Version: 1, EventID: "terminal:" + string(adapter.RuntimeTerminalFailed),
			Kind: adapter.RuntimeEventTerminal, TerminalStatus: adapter.RuntimeTerminalFailed,
		}
	}
	terminal.BackendMessageID = ""
	delete(terminal.Metadata, "backend_message_id")
}
