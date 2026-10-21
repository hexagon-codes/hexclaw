package engine

import (
	"strings"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/adapter"
)

func TestSelectedDefaultTeachingBodyKeepsItsRequiredTail(t *testing.T) {
	eng, _, _, _, registry, _ := newInheritanceFixture(t, false)
	registerInheritanceMarkdown(t, registry, "complete-teaching-method", strings.Repeat("教学步骤。", 1000)+"REQUIRED_TEACHING_TAIL", "1.0.0")
	eng.SetAgentDefaultSkillSelector(func(string, []string) ([]string, bool) {
		return []string{"complete-teaching-method"}, true
	})
	msg := &adapter.Message{Content: "Explain this problem", Metadata: map[string]string{"role": "configured-agent"}}
	eng.prepareAgentDefaultSkillContext(msg)
	ctx := eng.prepareSkillPromptContext(t.Context(), msg)
	prepared, _ := ctx.Value(preparedSkillContextKey{}).(*preparedSkillContext)
	if prepared == nil || !strings.Contains(prepared.prompt, "REQUIRED_TEACHING_TAIL") || len(prepared.receipts) != 1 {
		t.Fatal("selected default teaching body lost its required tail or receipt")
	}
	msg.Metadata["skills"] = "other-method"
	eng.prepareAgentDefaultSkillContext(msg)
	if names := effectiveSkillContextNames(msg.Metadata); len(names) != 1 || names[0] != "other-method" {
		t.Fatal("default selector replaced explicit mounting")
	}
}

func TestResponseCacheReusesOnlyIdenticalEffectiveTeachingRequest(t *testing.T) {
	eng, provider, _, _, _, _ := newInheritanceFixture(t, true)
	msg := &adapter.Message{ID: "first", UserID: "owner", SessionID: "fixed-history", Platform: adapter.PlatformAPI,
		Metadata: map[string]string{"role": "configured-agent"}}
	request := llm.CompletionRequest{Messages: []llm.Message{{Role: llm.RoleSystem, Content: "Frozen teaching body"}, {Role: llm.RoleUser, Content: "Explain this concept"}}}
	ctx := eng.beginResponseCache(t.Context())
	reply, err := eng.wrapResponseCache(ctx, msg, provider, "test", "mock-model").Complete(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	eng.writeResponseCache(ctx, reply.Content, "test", "mock-model")
	msg.ID = "second"
	secondCtx := eng.beginResponseCache(t.Context())
	_, err = eng.wrapResponseCache(secondCtx, msg, provider, "test", "mock-model").Complete(secondCtx, request)
	if err != nil || provider.calls != 1 {
		t.Fatalf("identical effective request missed cache: calls=%d err=%v", provider.calls, err)
	}
	request.Messages[0].Content = "Updated teaching body"
	thirdCtx := eng.beginResponseCache(t.Context())
	_, err = eng.wrapResponseCache(thirdCtx, msg, provider, "test", "mock-model").Complete(thirdCtx, request)
	if err != nil || provider.calls != 2 {
		t.Fatalf("updated teaching body reused a stale response: calls=%d err=%v", provider.calls, err)
	}
}
