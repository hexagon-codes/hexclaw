package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/skill"
	sqlitestore "github.com/hexagon-codes/hexclaw/storage/sqlite"
)

// 自定义 SOUL 只替换人设，独立冻结的公共工作规则仍需装配到请求。
func TestCustomSoul_StillGetsOperatingManual(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := config.WriteSoul("你是定制小蟹。只说人话，别啰嗦。"); err != nil {
		t.Fatalf("WriteSoul: %v", err)
	}
	t.Cleanup(func() { _ = config.WriteSoul("") })

	cfg := config.DefaultConfig()
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"test": {APIKey: "sk-test", Model: "test-model"}}
	router, _ := llmrouter.New(cfg.LLM)
	dir := t.TempDir()
	store, _ := sqlitestore.New(filepath.Join(dir, "test.db"))
	t.Cleanup(func() { store.Close() })
	eng := NewReActEngine(cfg, router, store, skill.NewRegistry())

	msgs := eng.buildStreamMessages(context.Background(), "", nil, "", "你好", nil, nil)
	sys := msgs[0].Content

	if !strings.Contains(sys, "你是定制小蟹") {
		t.Fatalf("自定义 SOUL 未被应用")
	}
	// 自定义人设不能覆盖真实产物与文件写入规则。
	if !strings.Contains(sys, "没有工具结果不得声称已执行成功") || !strings.Contains(sys, "write_file 仅用于纯文本") {
		t.Errorf("自定义 SOUL 丢了公共工作规则")
	}
	// 人设打底，公共规则独立附加一次。
	if i, j := strings.Index(sys, "你是定制小蟹"), strings.Index(sys, "# HexClaw 公共工作规则"); i < 0 || j < 0 || i > j {
		t.Errorf("装配顺序应为「人设 → 公共工作规则」")
	}
	if strings.Count(sys, "# HexClaw 公共工作规则") != 1 {
		t.Errorf("公共工作规则必须恰好装配一次")
	}
}

// 最终默认请求包含人设与公共工作规则，不把规则混入可编辑 SOUL。
func TestDefaultSystemPrompt_RetainsOperatingManual(t *testing.T) {
	eng := newEngineForMemoryAudit(t)
	msgs := eng.buildStreamMessages(context.Background(), "", nil, "", "你好", nil, nil)
	full := msgs[0].Content
	for _, must := range []string{
		"hexclaw.net",
		"没有工具结果不得声称已执行成功",
		"write_file 仅用于纯文本",
		"code_exec 是否联网",
		"调用实际可用的文档导出能力并检查返回产物",
		"修改文件前先读取现有内容，优先精确修改",
	} {
		if !strings.Contains(full, must) {
			t.Errorf("最终 system prompt 丢了公共工作规则关键项: %q", must)
		}
	}
}

// 不变量②：给用户编辑的「人设(DefaultSoul)」是角色，不是工具手册——
// 含角色要素，但不混入 write_file/code_exec 等工具操作细节；且显著短于完整 prompt。
func TestDefaultSoul_IsPersonaNotManual(t *testing.T) {
	soul := DefaultSoul()
	for _, must := range []string{"小蟹", "河蟹", "hexclaw.net", "官网", "信条"} {
		if !strings.Contains(soul, must) {
			t.Errorf("默认人设缺角色要素: %q", must)
		}
	}
	for _, mustNot := range []string{"write_file", "code_exec", "file_edit", "严禁"} {
		if strings.Contains(soul, mustNot) {
			t.Errorf("默认人设(给用户编辑的)不应混入工具手册项: %q", mustNot)
		}
	}
	eng := newEngineForMemoryAudit(t)
	msgs := eng.buildStreamMessages(context.Background(), "", nil, "", "你好", nil, nil)
	if len(soul) >= len(msgs[0].Content) {
		t.Errorf("人设应短于实际请求 system prompt，soul=%d full=%d", len(soul), len(msgs[0].Content))
	}
}

// 不变量④（本地化 A · 先 en）：user_locale=en 时下发**原生英文人设**，不是中文人设机翻；
// 且仍带运行手册纪律 + EN locale 指令；中文人设的签名不出现（证明确实换成了 EN 原生）。
func TestEnLocale_GetsNativeEnglishPersona(t *testing.T) {
	eng := newEngineForMemoryAudit(t)
	msgs := eng.buildStreamMessages(context.Background(), "", nil, "", "Hello", map[string]string{"user_locale": "en"}, nil)
	got := msgs[0].Content

	for _, must := range []string{
		"Little Crab",                    // EN 角色名
		"Hard claws",                     // EN 声音：钳
		"your device or your own server", // EN 运行位置
		"Creed:",                         // EN 信条
		"hexclaw.net",                    // 官网锚点
	} {
		if !strings.Contains(got, must) {
			t.Errorf("en 人设缺原生英文要素: %q", must)
		}
	}
	// 公共工作规则对所有 locale 一致。
	if !strings.Contains(got, "没有工具结果不得声称已执行成功") || !strings.Contains(got, "调用实际可用的文档导出能力并检查返回产物") {
		t.Errorf("en 下发的 system prompt 丢了公共工作规则")
	}
	// EN locale 指令在位。
	if !strings.Contains(got, "respond in English") {
		t.Errorf("en locale 指令缺失")
	}
	// 不应再是中文人设机翻：中文人设独有签名不出现。
	if strings.Contains(got, "私人 AI 搭子") || strings.Contains(got, "钳得住活，锁得住数据") {
		t.Errorf("en 仍混入中文人设签名（应为原生 EN 人设）")
	}
}
