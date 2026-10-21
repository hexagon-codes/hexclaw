package adapter

import (
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/mathtext"
)

// IM 端没有桌面那样的结构化工具卡，本文件把工具调用渲染成**紧凑文本尾注**，
// 让 Telegram / 企业微信 / 飞书等 IM 用户也能看到「调用了什么工具、成没成、结果摘要」。
//
// 这是「工具调用展示」的 Go 侧共享实现（对应桌面前端 src/utils/tool-call.ts），
// 供所有 IM 适配器经统一出站点复用，避免各适配器各写一套或干脆没有。

// toolDisplayNames 工具名 → 中文显示名（镜像前端 i18n chat.toolName）。
// 未命中回退裸名。河蟹/小蟹为中文优先产品，IM 默认中文。
var toolDisplayNames = map[string]string{
	"code_exec":         "代码执行",
	"code":              "代码运行",
	"shell":             "终端",
	"file_ops":          "文件管理",
	"file_edit":         "文件编辑",
	"grep":              "内容搜索",
	"glob":              "文件查找",
	"search":            "网络搜索",
	"weather":           "天气查询",
	"translate":         "翻译",
	"summary":           "摘要提取",
	"browser":           "网页浏览",
	"create_skill":      "新建 Skill",
	"manage_skill":      "Skill 管理",
	"manage_mcp_server": "MCP 管理",
	"orchestrate":       "多 Agent 协作",
	"spawn_agent":       "子 Agent",
	"transfer_to_agent": "Agent 交接",
	"cron_task":         "定时任务",
}

// toolBaseName 剥离 MCP/命名空间前缀取末段（mcp__server__tool → tool）。
func toolBaseName(name string) string {
	if name == "" {
		return name
	}
	s := strings.TrimPrefix(name, "mcp__")
	s = strings.ReplaceAll(s, "__", "\x00")
	s = strings.ReplaceAll(s, ":", "\x00")
	s = strings.ReplaceAll(s, ".", "\x00")
	parts := strings.Split(s, "\x00")
	last := parts[len(parts)-1]
	if last == "" {
		return name
	}
	return last
}

// toolDisplayName 工具显示名 —— IM 永不泄露裸 id。
func toolDisplayName(name string) string {
	base := toolBaseName(name)
	if zh, ok := toolDisplayNames[base]; ok {
		return zh
	}
	return base
}

// toolResultSummary 先提取真实可读文本，再按字符预算截断，避免截断 Markdown 语法。
func toolResultSummary(result string, maxRunes int) string {
	if strings.TrimSpace(result) == "" || maxRunes <= 0 {
		return ""
	}
	cleaned := mathtext.PlainMarkdownText(result)
	r := []rune(cleaned)
	if len(r) <= maxRunes {
		return cleaned
	}
	return string(r[:maxRunes-1]) + "…"
}

// toolStatusGlyph 状态字形：success→✓ / error→✗ / 其它（未知/运行）→·
func toolStatusGlyph(status string) string {
	switch status {
	case "error":
		return "✗"
	case "success":
		return "✓"
	default:
		return "·"
	}
}

// ToolCallDigest 把工具调用渲染成 IM 友好的紧凑尾注（带前导分隔）。
// 空调用返回空串（调用方据此决定是否追加）。
func ToolCallDigest(calls []ToolCall) string {
	return renderToolCallDigest(calls, true)
}

// ToolCallStatusDigest 只展示工具名与文字状态，正文不混入截断的工具响应。
func ToolCallStatusDigest(calls []ToolCall) string {
	return renderToolCallDigest(calls, false)
}

func renderToolCallDigest(calls []ToolCall, includeResult bool) string {
	if len(calls) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n")
	if includeResult {
		b.WriteString("———")
	}
	for i, c := range calls {
		if includeResult {
			b.WriteString("\n🔧 ")
		} else if i > 0 {
			b.WriteString(" · ")
		}
		name := toolDisplayName(c.Name)
		if !includeResult {
			switch toolBaseName(c.Name) {
			case "solve":
				name = "解题"
			case "knowledge_search":
				name = "知识库检索"
			}
		}
		statusLabel := "未知"
		if !includeResult {
			statusIcon := "⚪"
			switch c.Status {
			case "success":
				statusIcon, statusLabel = "🟢", "成功"
			case "error":
				statusIcon, statusLabel = "🔴", "失败"
			}
			b.WriteString(statusIcon)
			b.WriteByte(' ')
		}
		b.WriteString(name)
		if includeResult {
			b.WriteString(" ")
			b.WriteString(toolStatusGlyph(c.Status))
		} else {
			b.WriteString("：")
			b.WriteString(statusLabel)
		}
		if sum := toolResultSummary(c.Result, 48); includeResult && sum != "" {
			if toolStatusGlyph(c.Status) == "·" {
				b.WriteByte(' ')
			} else {
				b.WriteString(" · ")
			}
			b.WriteString(mathtext.EscapeMarkdownInline(sum))
		}
	}
	return b.String()
}
