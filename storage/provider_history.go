package storage

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hexagon-codes/hexagon/runtime"
)

// ProviderOutputHistoryMetadataKey 是内部输出组在既有消息元数据中的唯一载体。
const ProviderOutputHistoryMetadataKey = "_hexclaw_provider_output_v1"

// ProviderOutputHistoryVersion 是当前可无损恢复的内部输出组格式。
const ProviderOutputHistoryVersion = 1

// PublicMessageMetadata 只移除内部输出组，其余消息元数据保留原有值。
// 没有该内部键时直接返回原字节，供所有公开消息投影和输入适配复用。
func PublicMessageMetadata(raw string) string {
	var values map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &values) != nil {
		return raw
	}
	if _, exists := values[ProviderOutputHistoryMetadataKey]; !exists {
		return raw
	}
	delete(values, ProviderOutputHistoryMetadataKey)
	encoded, err := json.Marshal(values)
	if err != nil {
		return raw
	}
	return string(encoded)
}

// MarshalJSON 统一公开消息投影，不把协议续传内容混入历史或搜索响应。
func (m MessageRecord) MarshalJSON() ([]byte, error) {
	type publicRecord MessageRecord
	public := publicRecord(m)
	public.Metadata = PublicMessageMetadata(m.Metadata)
	public.Meta = PublicMessageMetadata(m.Meta)
	return json.Marshal(public)
}

// SetProviderOutputHistory 仅供内部保存完整运行输出，不接纳公开请求元数据。
// 完成格式校验后绑定本条记录的完整元数据，避免任意同名键扩大存储范围。
func (m *MessageRecord) SetProviderOutputHistory(raw json.RawMessage) error {
	if m == nil || m.Role != "assistant" {
		return errors.New("provider output history requires an assistant message")
	}
	var envelope struct {
		Version  int `json:"version"`
		Messages []struct {
			Message struct {
				Role         string            `json:"role"`
				Name         string            `json:"name"`
				MultiContent []json.RawMessage `json:"multi_content"`
			} `json:"message"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode provider output envelope: %w", err)
	}
	if envelope.Version != ProviderOutputHistoryVersion || len(envelope.Messages) == 0 {
		return errors.New("invalid provider output envelope")
	}
	for _, entry := range envelope.Messages {
		toolMedia := entry.Message.Role == "user" && entry.Message.Name == runtime.ToolMediaMessageName && len(entry.Message.MultiContent) > 0
		if entry.Message.Role != "assistant" && entry.Message.Role != "tool" && !toolMedia {
			return errors.New("invalid provider output envelope role")
		}
	}
	values := make(map[string]json.RawMessage)
	if m.Metadata != "" {
		if err := json.Unmarshal([]byte(m.Metadata), &values); err != nil {
			return fmt.Errorf("decode assistant message metadata: %w", err)
		}
	}
	if values == nil {
		values = make(map[string]json.RawMessage)
	}
	values[ProviderOutputHistoryMetadataKey] = append(json.RawMessage(nil), raw...)
	encoded, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("encode assistant provider output metadata: %w", err)
	}
	m.Metadata = string(encoded)
	m.providerOutputMetadata = m.Metadata
	return nil
}

// HasInternalProviderOutputHistory 表示本条元数据由内部输出组写入且随后未变化。
// 公开 JSON 或带同名键的普通记录不能产生此标记。
func (m *MessageRecord) HasInternalProviderOutputHistory() bool {
	return m != nil && m.providerOutputMetadata != "" && m.Metadata == m.providerOutputMetadata
}
