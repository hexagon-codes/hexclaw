package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/memory"
)

// 开启记忆时，本地与云端使用相同上下文；显式关闭时不注入。
func TestBuildTurnContext_ChatMemoryFollowsSetting(t *testing.T) {
	const marker = "MEMMARK_20260711_用户是杭州的产品经理"

	newEngWithMemory := func(t *testing.T) *ReActEngine {
		t.Helper()
		eng := newEngineForMemoryAudit(t)
		fm, err := memory.New(memory.Options{Enabled: true, Dir: t.TempDir(), MaxMemory: 200, DailyDays: 0})
		if err != nil {
			t.Fatalf("创建 FileMemory 失败: %v", err)
		}
		// identity=常驻层恒注入（BUG-20260712-L 起检索层零相关不注入；本测试意图是 locality
		// 门控，不依赖相关性召回）。
		if err := fm.SaveStructuredEntry("跨会话事实："+marker, "identity", "manual", "", memory.EntryMeta{}); err != nil {
			t.Fatalf("写入记忆失败: %v", err)
		}
		eng.SetFileMemory(fm)
		return eng
	}

	for _, tc := range []struct {
		name       string
		local, off bool
	}{
		{"cloud enabled", false, false}, {"cloud disabled", false, true}, {"local enabled", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := newEngWithMemory(t)
			ctx := egress.WithRequest(context.Background(), egress.PurposeGeneralChat, "chat-audit", egress.ClassGeneral)
			ctx = withProviderLocality(ctx, tc.local)
			metadata := map[string]string{}
			if tc.off {
				metadata["memory"] = "off"
			}
			msgs := eng.buildStreamMessages(ctx, "", nil, "", "介绍下我", metadata, nil)
			turn := msgs[len(msgs)-1].Content
			if strings.Contains(turn, marker) != !tc.off {
				t.Fatalf("memory inclusion does not match setting: %q", turn)
			}
			reqs, _ := egress.RequestsFromContext(ctx)
			if tc.off {
				requireNoEgressClass(t, reqs, egress.ClassMemory)
			} else {
				requireEgressRequest(t, reqs, egress.PurposeGeneralChat, egress.ClassMemory)
			}
			if err := (&egress.Policy{}).GuardContext(ctx); err != nil {
				t.Fatal(err)
			}
			child := egress.WithRequest(ctx, egress.PurposeGeneralChat, "independent-chat", egress.ClassMemory)
			if err := (&egress.Policy{}).GuardContext(child); err == nil {
				t.Fatal("new request inherited chat memory permission")
			}
		})
	}
}
