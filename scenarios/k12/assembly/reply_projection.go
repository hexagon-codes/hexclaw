package assembly

import (
	"context"
	"encoding/json"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// ReplyMetadataFooter 将已冻结的原回复元数据转换为渠道尾注，不读取当前模型配置。
func ReplyMetadataFooter(locale string, metadata map[string]json.RawMessage) string {
	reply := &adapter.Reply{Metadata: make(map[string]string)}
	for key, raw := range metadata {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			reply.Metadata[key] = value
		}
	}
	_ = json.Unmarshal(metadata["tool_calls"], &reply.ToolCalls)
	return adapter.DingTalkReplyFooter(reply, locale)
}

// CreativeWorkDeliveryDisclosure 只投影已通过正文与来源核对的执行摘要。
func CreativeWorkDeliveryDisclosure(ctx context.Context, canonical string) (string, string) {
	locale, facts, ok := usecase.CreativeWorkDeliveryExecutionSummary(ctx, canonical)
	if !ok {
		return "", ""
	}
	return locale, adapter.DingTalkExecutionSummaryFooter(locale, facts)
}
