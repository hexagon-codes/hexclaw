package engine

import (
	"context"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
)

type agentDeliveryChannelKey struct{}

// freezeAgentRequestInstructions 在缓存与首个请求前固定规则，不信任请求自填摘要。
func freezeAgentRequestInstructions(ctx context.Context, msg *adapter.Message) context.Context {
	ctx, snapshot := config.FreezeAgentInstructions(ctx)
	ensureMessageMetadata(msg)
	msg.Metadata["agent_instructions_digest"] = snapshot.Digest
	return context.WithValue(ctx, agentDeliveryChannelKey{}, msg.Platform)
}

func agentDeliveryInstructions(platform adapter.Platform) string {
	if platform == adapter.PlatformDesktop {
		return "\n\n当前交付渠道是桌面应用。已有 Markdown 产物可在产物面板查看或选择导出格式；用户明确要求文件时优先完成实际导出，并以真实返回产物说明结果。后端运行位置不等于用户桌面文件位置。"
	}
	if platform == adapter.PlatformAPI {
		return "\n\n当前交付渠道是 API；只引用本次实际生成且可访问的产物，不指引桌面专有菜单。"
	}
	return "\n\n当前交付渠道是即时消息。用户要求文件或图片时使用当前渠道实际可用的附件投递能力，按原任务和发送回执说明结果；不要指引桌面产物面板，也不要把服务器本地路径当成手机可下载链接。"
}
