package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon/runtime"
	"github.com/hexagon-codes/hexclaw/storage"
)

type providerOutputMessagesContextKey struct{}

type providerOutputHistory struct {
	Version  int                     `json:"version"`
	Messages []providerOutputMessage `json:"messages"`
}

type providerOutputMessage struct {
	Message              llm.Message               `json:"message"`
	ProviderContinuation *llm.ProviderContinuation `json:"provider_continuation,omitempty"`
}

// WithProviderOutputMessages 绑定本次运行的完整输出组，保存时沿原顺序持久化。
// 只接纳引擎内部的运行结果；调用后修改原消息不会改变待保存的内容。
func WithProviderOutputMessages(ctx context.Context, messages []llm.Message) context.Context {
	return context.WithValue(ctx, providerOutputMessagesContextKey{}, runtime.CloneOutputMessages(messages))
}

func persistProviderOutputHistory(ctx context.Context, record *storage.MessageRecord) error {
	if ctx == nil {
		return nil
	}
	messages, _ := ctx.Value(providerOutputMessagesContextKey{}).([]llm.Message)
	if len(messages) == 0 {
		return nil
	}
	messages = alignProviderOutputHistory(messages, record.Content)
	history := providerOutputHistory{
		Version:  storage.ProviderOutputHistoryVersion,
		Messages: make([]providerOutputMessage, len(messages)),
	}
	for i, message := range messages {
		history.Messages[i] = providerOutputMessage{
			Message:              message,
			ProviderContinuation: message.ProviderContinuation,
		}
	}
	raw, err := json.Marshal(history)
	if err != nil {
		return fmt.Errorf("encode provider output history: %w", err)
	}
	return record.SetProviderOutputHistory(raw)
}

// alignProviderOutputHistory 保留完整工具组，只将最后的回复对齐实际保存的语义。
// 更正后的正文不能继续携带描述旧正文的协议条目，工具调用和已执行结果不改写。
func alignProviderOutputHistory(messages []llm.Message, content string) []llm.Message {
	messages = runtime.CloneOutputMessages(messages)
	if len(messages) > 0 {
		last := &messages[len(messages)-1]
		if last.Role == llm.RoleAssistant && len(last.ToolCalls) == 0 && len(last.MultiContent) == 0 {
			if last.Content != content {
				last.Content = content
				last.ProviderContinuation = nil
			}
			return messages
		}
	}
	return append(messages, llm.Message{Role: llm.RoleAssistant, Content: content})
}

func restoreProviderOutputHistory(metadata string) ([]llm.Message, bool, error) {
	var values map[string]json.RawMessage
	if json.Unmarshal([]byte(metadata), &values) != nil {
		return nil, false, nil
	}
	raw, exists := values[storage.ProviderOutputHistoryMetadataKey]
	if !exists {
		return nil, false, nil
	}
	var history providerOutputHistory
	if err := json.Unmarshal(raw, &history); err != nil {
		return nil, true, fmt.Errorf("decode provider output history: %w", err)
	}
	if history.Version != storage.ProviderOutputHistoryVersion {
		return nil, true, errors.New("unsupported provider output history version")
	}
	if len(history.Messages) == 0 {
		return nil, true, errors.New("provider output history has no messages")
	}
	messages := make([]llm.Message, len(history.Messages))
	for i, stored := range history.Messages {
		toolMedia := stored.Message.Role == llm.RoleUser &&
			stored.Message.Name == runtime.ToolMediaMessageName && len(stored.Message.MultiContent) > 0
		if stored.Message.Role != llm.RoleAssistant && stored.Message.Role != llm.RoleTool && !toolMedia {
			return nil, true, errors.New("invalid provider output history role")
		}
		messages[i] = stored.Message
		messages[i].ProviderContinuation = stored.ProviderContinuation
	}
	return runtime.CloneOutputMessages(messages), true, nil
}
