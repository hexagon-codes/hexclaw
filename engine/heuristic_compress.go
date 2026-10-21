package engine

import (
	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/ai-core/tokenizer"
	"github.com/hexagon-codes/hexagon/runtime/middleware"
)

// tokenEstimator 沿用 ai-core 的中文感知估算。
var tokenEstimator = tokenizer.New(tokenizer.GPT4)

// HeuristicCompressOptions 复用共享压缩策略参数，保留应用包的既有类型名。
type HeuristicCompressOptions = middleware.HeuristicCompressOptions

// HeuristicCompress 委托共享消息压缩策略，返回压缩后的副本。
func HeuristicCompress(history []llm.Message, cfg HeuristicCompressOptions) []llm.Message {
	return middleware.HeuristicCompress(history, cfg)
}

// EstimateMessagesTokens 估算 messages 总 token 数，用于 Compactor 双路径路由阈值判断。
//
// 改用 ai-core/tokenizer 的 CJK 感知估算替代裸 len/4：英文仍约 4 字符/token，
// 中文按约 1.5 字符/token，消除与同仓 token_estimate.go 的口径不一致。
// 逐条 content + 每个 tool_call.Arguments 累加，保持与原循环相同的统计范围。
func EstimateMessagesTokens(history []llm.Message) int {
	total := 0
	for _, m := range history {
		total += tokenEstimator.Count(m.Content)
		for _, tc := range m.ToolCalls {
			total += tokenEstimator.Count(tc.Arguments)
		}
	}
	return total
}
