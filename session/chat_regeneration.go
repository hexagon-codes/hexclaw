package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/internal/requestcancel"
	"github.com/hexagon-codes/hexclaw/storage"
)

// ChatRegeneration 为一次运行暂存构建完成的助手，不在真实终帧收口前改历史。
type ChatRegeneration struct {
	snapshot *storage.ChatRegenerationSnapshot
	request  string
	draft    *storage.MessageRecord
	values   context.Context
}

type chatRegenerationKey struct{}

func ChatRegenerationFromContext(ctx context.Context) *ChatRegeneration {
	stage, _ := ctx.Value(chatRegenerationKey{}).(*ChatRegeneration)
	return stage
}

func (m *Manager) SupportsChatRegeneration() bool {
	_, freeze := m.store.(storage.ChatRegenerationReader)
	_, write := m.store.(storage.ActiveSessionMessageWriter)
	return freeze && write
}

func (m *Manager) BeginChatRegeneration(ctx context.Context, msg *adapter.Message) (context.Context, *ChatRegeneration, error) {
	if !m.SupportsChatRegeneration() {
		return ctx, nil, errors.New("conversation regeneration is unavailable")
	}
	request := strings.TrimSpace(msg.Metadata["request_id"])
	if request == "" || strings.TrimSpace(msg.SessionID) == "" || strings.TrimSpace(msg.UserID) == "" {
		return ctx, nil, errors.New("conversation regeneration requires a session and request identity")
	}
	snapshot, err := m.store.(storage.ChatRegenerationReader).FreezeChatRegeneration(ctx, msg.UserID, msg.SessionID, msg.RegenerateFromMessageID, m.contextMessageLimit())
	if err != nil {
		return ctx, nil, err
	}
	meta := map[string]string{"request_id": request, "source_message_id": snapshot.Source.ID}
	for _, key := range []string{adapter.MetadataRequestTemperature, adapter.MetadataRequestMaxTokens, "thinking", "memory", "locale"} {
		if value := msg.Metadata[key]; value != "" {
			meta[key] = value
		}
	}
	var source messageMetadata
	if raw := snapshot.Source.Metadata; raw != "" {
		if err = json.Unmarshal([]byte(raw), &source); err != nil {
			return ctx, nil, fmt.Errorf("decode original user message: %w", err)
		}
	}
	if len(source.Documents) > 0 {
		meta["documents"] = string(source.Documents)
	}
	if len(source.Skills) > 0 {
		meta["skills"] = strings.Join(source.Skills, ",")
	}
	if len(source.PromptInvocation) > 0 {
		meta["prompt_invocation"] = string(source.PromptInvocation)
	}
	if previous := snapshot.Previous; previous != nil {
		var route struct {
			Provider, Model string
			AgentName       string `json:"agent_name"`
		}
		if raw := previous.Metadata; raw != "" {
			if err = json.Unmarshal([]byte(raw), &route); err != nil {
				return ctx, nil, fmt.Errorf("decode original assistant route: %w", err)
			}
		}
		meta["provider"], meta["model"], meta["role"] = route.Provider, route.Model, route.AgentName
	} else {
		// 已发送但尚无助手的原轮沿正常新回复的路由选择。
		for _, key := range []string{"provider", "model", "role"} {
			meta[key] = msg.Metadata[key]
		}
	}
	msg.Content, msg.Attachments, msg.Metadata = snapshot.Source.Content, source.Attachments, meta
	stage := &ChatRegeneration{snapshot: snapshot, request: request}
	return context.WithValue(ctx, chatRegenerationKey{}, stage), stage, nil
}

func (r *ChatRegeneration) HasDraft() bool { return r.draft != nil }

func (r *ChatRegeneration) matches(sessionID, requestID string) bool {
	return r != nil && sessionID == r.snapshot.Source.SessionID && requestID == r.request
}

func (m *Manager) CommitChatRegeneration(ctx context.Context, stage *ChatRegeneration, snapshot *adapter.RuntimeSnapshot) (string, error) {
	if stage == nil || !stage.HasDraft() {
		return "", errors.New("regeneration has no complete assistant reply")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if requestcancel.IsExplicit(ctx) {
		return "", context.Canceled
	}
	record := stage.draft
	if snapshot != nil {
		metadata, err := encodeAssistantRuntimeSnapshot(record, *snapshot)
		if err != nil {
			return "", err
		}
		record.Metadata = metadata
	}
	// 从暂存时保留完成器和领域产物，提交期限绑定仍可取消的原运行。
	saveCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	completion, _ := storage.AssistantCompletionFromContext(stage.values)
	completion.BeforeInsert = stage.snapshot.Replace
	saveCtx = storage.WithAssistantCompletion(saveCtx, completion)
	if media, found := mediaflow.ReplyCompletionFromContext(stage.values); found {
		saveCtx = mediaflow.WithReplyCompletion(saveCtx, media)
	}
	if err := m.store.(storage.ActiveSessionMessageWriter).SaveMessageIfSessionActive(saveCtx, record); err != nil {
		return "", err
	}
	if stage.snapshot.Invalidate != nil {
		stage.snapshot.Invalidate()
	}
	return record.ID, nil
}
