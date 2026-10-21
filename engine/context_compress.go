package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	hexagon "github.com/hexagon-codes/hexagon"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon/observe/trace"
	hruntime "github.com/hexagon-codes/hexagon/runtime"
	"github.com/hexagon-codes/hexagon/runtime/middleware"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/toolkit/lang/stringx"
)

const (
	// contextCompressCharThreshold 触发压缩的字符数阈值。
	//
	// Chinese text averages ~1.5 chars/token, so 80K chars ≈ 53K tokens —
	// overflowing 32K context models. Use 40K chars as a safer threshold:
	// ~20K tokens for Chinese-heavy content, ~10K for English.
	contextCompressCharThreshold = 40000

	// contextHeuristicMaxChars 启发式路径上限。超过则降级到 LLM 摘要。
	// 设为 threshold 的 2 倍：小幅超限走启发式（快、便宜、稳定），
	// 大幅超限走 LLM（保留语义）。
	contextHeuristicMaxChars = contextCompressCharThreshold * 2

	// contextKeepRecent 保留最近 N 条消息不压缩。
	// 这些是当前工作的"短期记忆"，LLM 需要完整上下文。
	contextKeepRecent = 8

	// contextMinMessagesToCompress 需要压缩的最少消息数。
	// 太少不值得压缩。
	contextMinMessagesToCompress = 6
)

// compressContextIfNeeded 在 tool loop 内检查并压缩上下文。
//
// 当 messages 的总字符数超过阈值时：
//  1. 保留 system 消息 + 最近 N 条消息
//  2. 将中间的工具交互消息用 LLM 生成摘要
//  3. 摘要失败时回退到启发式摘要
//
// 返回压缩后的 messages（可能和原始相同）。
func compressContextIfNeeded(
	ctx context.Context,
	messages []llm.Message,
	provider hexagon.Provider,
	sessionID string,
) []llm.Message {
	compressed, _ := compressContextWithInput(ctx, messages, provider, sessionID, -1)
	return compressed
}

// compressContextWithInput 同时维护当前输入位置，只压缩其余上下文。
func compressContextWithInput(ctx context.Context, messages []llm.Message, provider hexagon.Provider, sessionID string, inputIndex int) ([]llm.Message, int) {
	if inputIndex < 0 || inputIndex >= len(messages) || messages[inputIndex].Role != llm.RoleUser {
		inputIndex = -1
	}
	totalChars := contextMessageBytes(messages)

	if totalChars < contextCompressCharThreshold {
		if middleware.HasCompactableToolMedia(messages, 3) {
			compressed := HeuristicCompress(messages, HeuristicCompressOptions{})
			if inputIndex >= 0 {
				inputIndex = contextUserMessageIndex(compressed, countContextUserMessages(messages[:inputIndex+1]))
			}
			return compressed, inputIndex
		}
		return messages, inputIndex
	}

	// 双路径路由（v0.4.0 F4）：
	//   - 小幅超限（< 2x 阈值）→ HeuristicCompress 全量启发式（无 LLM 调用，快/便宜/稳定）
	//   - 大幅超限（>= 2x 阈值）→ LLM 摘要（保留语义）
	if totalChars < contextHeuristicMaxChars {
		compressed := HeuristicCompress(messages, HeuristicCompressOptions{})
		if inputIndex >= 0 {
			// 共享启发式保留所有用户消息及顺序，按序号跟踪身份，不比较正文。
			inputIndex = contextUserMessageIndex(compressed, countContextUserMessages(messages[:inputIndex+1]))
		}
		trace.L(ctx).Info("工具循环上下文压缩: 启发式路径",
			"before", len(messages), "after", len(compressed),
			"chars_before", totalChars, "session", sessionID)
		return compressed, inputIndex
	}

	// 分割消息: system | old (可压缩) | recent (保留)
	systemEnd, keepStart := splitIndexes(messages)
	oldMsgs := messages[systemEnd:keepStart]
	inputInOld := inputIndex >= systemEnd && inputIndex < keepStart
	if inputInOld {
		// 当前请求不能进入历史摘要；复制旧消息区，避免改动原始消息数组。
		oldMsgs = make([]llm.Message, 0, keepStart-systemEnd-1)
		oldMsgs = append(oldMsgs, messages[systemEnd:inputIndex]...)
		oldMsgs = append(oldMsgs, messages[inputIndex+1:keepStart]...)
	}
	if len(oldMsgs) < contextMinMessagesToCompress {
		return messages, inputIndex
	}

	// 生成摘要。本地 provider 慢且工具摘要经 LLM 价值有限——直接走启发式：
	// 确定性、即时、不挤占整条 chat 的时间预算（避免慢本地模型在 30s 摘要超时上白白
	// 烧掉时间预算后整条 /api/v1/chat 仍超时）；启发式已保留 user 约束 + 工具名 + 结果。
	var summary string
	localByConfig, localityStamped := ctx.Value(providerLocalityKey{}).(bool)
	if provider != nil && ((localityStamped && localByConfig) || (!localityStamped && isLocalProvider(provider.Name()))) {
		summary = heuristicToolSummary(oldMsgs)
		trace.L(ctx).Info("工具循环上下文压缩: 本地模型直接启发式（跳过 LLM 摘要）",
			"provider", provider.Name(), "session", sessionID)
	} else {
		s, err := llmToolSummary(ctx, oldMsgs, provider)
		if err != nil {
			trace.L(ctx).Warn("工具循环上下文压缩: LLM 摘要失败，使用启发式", "err", err, "session", sessionID)
			s = heuristicToolSummary(oldMsgs)
		}
		summary = s
	}

	// 重建 messages
	compressed := make([]llm.Message, 0, systemEnd+2+len(messages)-keepStart)
	compressed = append(compressed, messages[:systemEnd]...)
	compressed = append(compressed, llm.SystemMessage("[Prior tool interactions summary]\n"+summary))
	if inputInOld {
		current := messages[inputIndex]
		inputIndex = len(compressed)
		compressed = append(compressed, current)
	} else if inputIndex >= keepStart {
		inputIndex = len(compressed) + inputIndex - keepStart
	}
	compressed = append(compressed, messages[keepStart:]...)

	trace.L(ctx).Info("工具循环上下文压缩",
		"before", len(messages), "after", len(compressed),
		"chars_before", totalChars, "session", sessionID)

	return compressed, inputIndex
}

// countContextUserMessages 只统计结构位置，用于跟踪原始请求中的用户输入。
func countContextUserMessages(messages []llm.Message) int {
	n := 0
	for _, message := range messages {
		if message.Role == llm.RoleUser && message.Name != hruntime.ToolMediaMessageName {
			n++
		}
	}
	return n
}

func contextUserMessageIndex(messages []llm.Message, ordinal int) int {
	if ordinal <= 0 {
		return -1
	}
	for i, message := range messages {
		if message.Role == llm.RoleUser && message.Name != hruntime.ToolMediaMessageName {
			ordinal--
			if ordinal == 0 {
				return i
			}
		}
	}
	return -1
}

// contextMessageBytes 保持既有的正文及工具参数字节统计口径。
func contextMessageBytes(messages []llm.Message) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content)
		for _, call := range message.ToolCalls {
			total += len(call.Arguments)
		}
	}
	return total
}

// splitIndexes 使用共享边界计算，应用仅提供既有保留条数。
func splitIndexes(messages []llm.Message) (systemEnd, keepStart int) {
	return middleware.RecentMessageBounds(messages, contextKeepRecent)
}

// llmToolSummary 用 LLM 生成工具交互的摘要。
func llmToolSummary(ctx context.Context, msgs []llm.Message, provider hexagon.Provider) (string, error) {
	if provider == nil {
		return "", fmt.Errorf("no provider available")
	}

	var sb strings.Builder
	sb.WriteString("Summarize the following conversation concisely. ")
	sb.WriteString("IMPORTANT: Preserve any user instructions, constraints, or requirements (e.g. 'do not modify', 'read-only', 'only use X'). ")
	sb.WriteString("Also keep: tool names, key results, errors, and decisions made. ")
	sb.WriteString("Output a brief summary (under 300 words).\n\n")

	for _, m := range msgs {
		switch m.Role {
		case "user":
			sb.WriteString(fmt.Sprintf("User: %s\n", stringx.TruncateWithSuffix(m.Content, 500, "...")))
		case "assistant":
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					sb.WriteString(fmt.Sprintf("Called: %s(%s)\n", tc.Name, stringx.TruncateWithSuffix(tc.Arguments, 200, "...")))
				}
			} else if m.Content != "" {
				sb.WriteString(fmt.Sprintf("Assistant: %s\n", stringx.TruncateWithSuffix(m.Content, 300, "...")))
			}
		case "tool":
			sb.WriteString(fmt.Sprintf("Result: %s\n", stringx.TruncateWithSuffix(m.Content, 300, "...")))
		}
	}

	summaryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// 工具对话压缩不是长期记忆写入；独立副本沿用真实分类，不能污染主请求。
	requests, _ := egress.RequestsFromContext(ctx)
	purpose, auditID := egress.Purpose(""), ""
	classes := make([]egress.DataClass, 0, len(requests))
	for _, request := range requests {
		purpose, auditID = request.Purpose, request.AuditID
		classes = append(classes, request.DataClass)
	}
	summaryCtx = egress.WithRequest(summaryCtx, purpose, auditID, classes...)
	if len(requests) > 0 && requests[0].ChatMemoryEnabled {
		egress.EnableChatMemory(summaryCtx)
	}

	var temp float64 = 0
	// v0.4.0 E4：用 CompleteWithFailover 做 ClassifyError-aware 重试（同 Provider，
	// 无 Fallback —— 这函数本身是压缩，再压缩没意义；429/未知会自动退避重试）。
	fc := &LLMCallContext{
		Provider:     provider,
		ProviderName: provider.Name(),
	}
	resp, err := CompleteWithFailover(summaryCtx, fc, hexagon.CompletionRequest{
		Messages: []hexagon.Message{
			{Role: "user", Content: sb.String()},
		},
		MaxTokens:   500,
		Temperature: &temp,
	})
	if err != nil {
		return "", err
	}
	if resp.Content == "" {
		return "", fmt.Errorf("empty summary response")
	}
	return resp.Content, nil
}

// heuristicToolSummary 委托不依赖业务与模型的共享摘要策略。
func heuristicToolSummary(messages []llm.Message) string {
	return middleware.HeuristicToolSummary(messages)
}
