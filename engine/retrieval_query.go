package engine

import (
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
)

// knowledgeRetrievalQuery只投影本轮用户问题，附件与引用正文仍完整保留给模型。
func knowledgeRetrievalQuery(msg *adapter.Message) string {
	if msg == nil {
		return ""
	}
	if query, exists := msg.Metadata["retrieval_query"]; exists {
		return query
	}
	const documentPrefix = "[附件文档内容 - 请基于以下文档回答用户问题]\n"
	const referencePrefix = "[用户引用的参考上下文 — 请结合以下内容回答用户问题]\n"
	const questionMarker = "\n\n[用户问题]\n"
	if strings.HasPrefix(msg.Content, documentPrefix) || strings.HasPrefix(msg.Content, referencePrefix) {
		if index := strings.LastIndex(msg.Content, questionMarker); index >= 0 {
			return msg.Content[index+len(questionMarker):]
		}
	}
	return msg.Content
}
