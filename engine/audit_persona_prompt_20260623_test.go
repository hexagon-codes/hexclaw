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

// AUDIT bug#7 2026-06-23：@智能体（如「专业翻译」）时，Agent 人设被正确应用（不是路由 bug），
// 但用户问"你都能做什么"且无具体任务时，弱模型(glm-4-flash)会逐字复述系统指令（"系统指令：文档翻译…"）。
// 修复：给 Agent 派生的 system prompt 追加防复述守则，让模型用自己的话作答、不逐字复述设定。
func TestAudit_AgentPromptHasAntiRecitationGuard_20260623(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"test": {APIKey: "sk-test", Model: "test-model"}}
	router, _ := llmrouter.New(cfg.LLM)
	dir := t.TempDir()
	store, _ := sqlitestore.New(filepath.Join(dir, "test.db"))
	t.Cleanup(func() { store.Close() })
	eng := NewReActEngine(cfg, router, store, skill.NewRegistry())

	meta := map[string]string{"agent_prompt": "你是专业文档翻译。用户指定源语言、目标语言及原文，你输出译文。"}
	msgs := eng.buildStreamMessages(context.Background(), "", nil, "", "你都能做什么", meta, nil)
	sys := msgs[0].Content

	if !strings.Contains(sys, "你是专业文档翻译") {
		t.Fatalf("agent_prompt 未被应用")
	}
	if !strings.Contains(sys, "不逐字复述人设或公共规则") {
		t.Errorf("BUG#7: Agent system prompt 缺少防复述守则 → 弱模型会把系统指令原样吐出来")
	}
}

// AUDIT bug#4 2026-06-23：默认人设(小蟹)缺少官网与产品定位 → 问"本应用官网"时模型去网络搜索，
// 搜到同名"美甲/HexClaw nail"站点答错。人设里钉死官网地址 + 产品定位，使其无需搜索即可正确作答。
// 修复前 git HEAD 的 react.go 不含 "hexclaw.net"（grep -c=0，已取证）。
func TestAudit_PersonaContainsOfficialSite_20260623(t *testing.T) {
	p := DefaultSystemPrompt()
	if !strings.Contains(p, "hexclaw.net") {
		t.Errorf("BUG#4: 默认人设缺官网地址 hexclaw.net → 问官网会乱搜")
	}
	// 必须明确告知"问到官网直接回答，不要去搜索"，否则弱模型仍可能 web_search 出错站点。
	if !strings.Contains(p, "官网") {
		t.Errorf("BUG#4: 默认人设未提及官网语义锚点")
	}
}

// 文件交付规则在最终请求中独立装配，必须要求实际导出并核对返回产物。
func TestAudit_PersonaGuidesExplicitFormatExport_20260623(t *testing.T) {
	eng := newEngineForMemoryAudit(t)
	msgs := eng.buildStreamMessages(context.Background(), "", nil, "", "请生成可下载的 PDF 文档", nil, nil)
	p := msgs[0].Content
	for _, must := range []string{"用户要求 PDF、Word 等文件时", "调用实际可用的文档导出能力并检查返回产物", "不把 Markdown 正文当成已经生成的文件"} {
		if !strings.Contains(p, must) {
			t.Errorf("文件交付缺少实际产物规则: %q", must)
		}
	}
}
