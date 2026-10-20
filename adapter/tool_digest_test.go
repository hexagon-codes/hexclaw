package adapter

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hexagon-codes/hexclaw/internal/mathtext"
)

func TestToolCallDigest_IMFriendly(t *testing.T) {
	out := ToolCallDigest([]ToolCall{
		{Name: "weather", Result: "🌍 **杭州 天气**\n🌡 温度: 27°C", Status: "success"},
		{Name: "mcp__svc__search", Result: "找到 3 条结果", Status: "success"},
		{Name: "glob", Result: "Error: path rejected", Status: "error"},
	})
	// 本地化名（裸 id 不泄露；MCP 前缀剥离）
	if !strings.Contains(out, "天气查询") || !strings.Contains(out, "网络搜索") {
		t.Fatalf("工具名未本地化: %q", out)
	}
	if strings.Contains(out, "weather") || strings.Contains(out, "mcp__") {
		t.Fatalf("泄露裸 id: %q", out)
	}
	// 状态字形
	if !strings.Contains(out, "✓") || !strings.Contains(out, "✗") {
		t.Fatalf("缺状态字形: %q", out)
	}
	// 摘要：markdown 星号被剥离、单行
	if strings.Contains(out, "**") {
		t.Fatalf("摘要未去 markdown: %q", out)
	}
	if !strings.Contains(out, "温度: 27°C") {
		t.Fatalf("摘要缺关键信息: %q", out)
	}
}

func TestToolCallDigest_Empty(t *testing.T) {
	if ToolCallDigest(nil) != "" {
		t.Fatal("空调用应返回空串")
	}
	if ToolCallDigest([]ToolCall{}) != "" {
		t.Fatal("空切片应返回空串")
	}
}

func TestToolResultSummary_RuneSafe(t *testing.T) {
	// 恰在 emoji 处溢出，不得切裂出 U+FFFD
	s := toolResultSummary(strings.Repeat("天", 46)+"🌍🌡 尾", 48)
	if strings.ContainsRune(s, '�') {
		t.Fatalf("摘要切裂 emoji: %q", s)
	}
	if !strings.HasSuffix(s, "…") {
		t.Fatalf("超长摘要应带省略号: %q", s)
	}
}

func TestToolCallDigestProjectsSolveMarkdownBeforeTruncation(t *testing.T) {
	got := ToolCallDigest([]ToolCall{{Name: "solve", Result: "### 第一步\n\n**找单位一：** 原长40米"}})
	want := "\n\n———\n🔧 solve · 第一步 找单位一： 原长40米"
	if got != want {
		t.Fatalf("solve digest=%q, want %q", got, want)
	}
	for _, tc := range []struct {
		text  string
		limit int
		want  string
	}{
		{"### 中文🌍维吾尔语", 4, "中文🌍…"},
		{"### سوئال中文", 4, "سوئ…"},
		{"文字", 1, "…"},
		{"文字", 0, ""},
	} {
		if got := toolResultSummary(tc.text, tc.limit); got != tc.want || !utf8.ValidString(got) {
			t.Fatalf("summary(%q,%d)=%q, want %q", tc.text, tc.limit, got, tc.want)
		}
	}
}

func TestToolCallDigestPreservesCodeLiteralSymbols(t *testing.T) {
	got := ToolCallDigest([]ToolCall{{Name: "code_exec", Status: "success", Result: "`2 ** 3`、`_x_` 与 [教材](https://example.invalid)"}})
	if !strings.Contains(got, `2 \*\* 3`) || !strings.Contains(got, `\_x\_`) || strings.Contains(got, "https://") {
		t.Fatalf("digest did not protect literal inline characters: %q", got)
	}
	want := "——— 🔧 代码执行 ✓ · 2 ** 3、_x_ 与 教材"
	if visible := mathtext.PlainMarkdownText(got); visible != want {
		t.Fatalf("visible digest=%q, want %q", visible, want)
	}
}
