package api

import (
	"net/http"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/upstreamerr"
	"github.com/hexagon-codes/hexclaw/llmrouter"
)

// llmErrorPayload 生成聊天传输共用的错误体，保持机器码与用户可读信息同步。
func llmErrorPayload(err error) map[string]any {
	payload := map[string]any{
		"error": upstreamerr.PublicMessage(err, "error"),
		"done":  true,
	}
	if classification, ok := llmrouter.ClassifyLLMError(err); ok {
		payload["code"] = string(classification.Code)
		payload["retryable"] = classification.Retryable
	}
	return payload
}

func writeChatLLMError(w http.ResponseWriter, status int, err error) {
	payload := llmErrorPayload(err)
	delete(payload, "done")
	writeJSON(w, status, payload)
}

// appendObservedChatError 保留同次失败已收到的结果，错误机器码和终态仍由原错误拥有。
func appendObservedChatError(payload map[string]any, chunk *adapter.ReplyChunk) {
	if chunk == nil {
		return
	}
	if chunk.Content != "" {
		payload["content"] = chunk.Content
	}
	if chunk.MessageContent != nil {
		payload["message_content"] = chunk.MessageContent
	}
	if chunk.RenderManifest != nil {
		payload["render_manifest"] = chunk.RenderManifest
	}
	if len(chunk.ToolCalls) > 0 {
		payload["tool_calls"] = chunk.ToolCalls
	}
	if len(chunk.Metadata) > 0 {
		payload["metadata"] = chunk.Metadata
	}
	if len(chunk.Blocks) > 0 {
		payload["blocks"] = chunk.Blocks
	}
	if chunk.Usage != nil {
		payload["usage"] = chunk.Usage
	}
	payload["assistant_message_id"] = chunk.AssistantMessageID
	payload["backend_message_id"] = chunk.BackendMessageID
	payload["message_id"] = chunk.MessageID
	payload["sequence"] = chunk.Sequence
	payload["reasoning_disclosure"] = chunk.ReasoningDisclosure
	payload["runtime_event"] = chunk.RuntimeEvent
}

func writeChatLLMErrorResult(w http.ResponseWriter, status int, err error, reply *adapter.Reply, sessionID string) {
	if reply == nil {
		writeChatLLMError(w, status, err)
		return
	}
	payload := llmErrorPayload(err)
	delete(payload, "done")
	appendObservedChatError(payload, &adapter.ReplyChunk{Content: reply.Content, MessageContent: reply.MessageContent, RenderManifest: reply.RenderManifest, Metadata: reply.Metadata, ToolCalls: reply.ToolCalls, Blocks: reply.Blocks, Usage: reply.Usage, AssistantMessageID: reply.AssistantMessageID, BackendMessageID: reply.BackendMessageID, MessageID: reply.MessageID, Sequence: reply.LastSequence, ReasoningDisclosure: reply.ReasoningDisclosure})
	delete(payload, "content")
	delete(payload, "sequence")
	delete(payload, "runtime_event")
	payload["reply"], payload["session_id"] = reply.Content, sessionID
	payload["last_sequence"] = reply.LastSequence
	if len(reply.RuntimeEvents) > 0 {
		payload["runtime_events"] = reply.RuntimeEvents
	}
	writeJSON(w, status, payload)
}
