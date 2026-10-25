package engine

import (
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/memory"
	"github.com/hexagon-codes/hexclaw/skill"
)

func TestMemoryPersonaKeepsRelevantManualMemory(t *testing.T) {
	fm := newFileMem(t, 200)
	const memoryText = "知道现在几几年吗？几几黏你去洗啊，麻利滴儿"
	mustSave(t, fm, memoryText, "fact")
	eng := engineWithFileMem(t, fm)
	eng.skills = skill.NewRegistry()
	if err := eng.skills.Register(&fakePersonaSkill{name: "girlfriend", body: "保持亲切的对话风格。"}); err != nil {
		t.Fatal(err)
	}
	ctx := withRetrievalHitsSink(t.Context())
	turn := eng.buildTurnContext(ctx, map[string]string{"skills": "girlfriend"}, "", "你的设定是2012年，你现在穿越了，你知道现在几几年吗？")
	if !strings.Contains(turn, memoryText) {
		t.Fatalf("mounted skill discarded the related manual memory: %s", turn)
	}
}

func TestMemoryExtractionKeepsBusinessObjectScope(t *testing.T) {
	fm := newFileMem(t, 200)
	eng := engineWithFileMem(t, fm)
	facts := []extractedFact{{Content: "目前就读六年级", Subject: "年级"}}
	for _, subject := range []string{"child-a", "child-b"} {
		ctx := memory.WithScope(t.Context(), memory.Scope{Agent: "tutor-a", SubjectID: subject})
		if saved := eng.ingestExtractedFacts(ctx, facts, ""); saved != 1 {
			t.Fatalf("another object's fact suppressed this write: %s saved=%d", subject, saved)
		}
		if saved := eng.ingestExtractedFacts(ctx, facts, ""); saved != 0 {
			t.Fatalf("same-object replay was not idempotent: %s saved=%d", subject, saved)
		}
	}
	entries := fm.ParseEntriesForRole("tutor-a")
	if len(entries) != 2 {
		t.Fatalf("business objects were merged: %+v", entries)
	}
	for _, subject := range []string{"child-a", "child-b"} {
		matched := memory.EntriesForScope(entries, memory.Scope{Agent: "tutor-a", SubjectID: subject})
		if len(matched) != 1 || matched[0].Scope.SubjectID != subject {
			t.Fatalf("extraction broadened the object's scope: %+v", matched)
		}
	}
}

func TestMemoryExtractionContextExcludesOtherObjects(t *testing.T) {
	fm := newFileMem(t, 200)
	items := []struct {
		text  string
		scope memory.Scope
	}{
		{"用户偏好简洁回答", memory.Scope{}},
		{"孩子A目前六年级", memory.Scope{Agent: "tutor-a", SubjectID: "child-a"}},
		{"孩子B目前三年级", memory.Scope{Agent: "tutor-a", SubjectID: "child-b"}},
		{"其他人设的专属暗号", memory.Scope{Skills: []string{"girlfriend"}}},
	}
	for _, item := range items {
		if err := fm.SaveStructuredEntry(item.text, "fact", "manual", "", memory.EntryMeta{Scope: item.scope}); err != nil {
			t.Fatal(err)
		}
	}
	eng := engineWithFileMem(t, fm)
	ctx := memory.WithScope(t.Context(), memory.Scope{Agent: "tutor-a", SubjectID: "child-a"})
	text := eng.memoryExtractionContext(ctx, "")
	if !strings.Contains(text, items[0].text) || !strings.Contains(text, items[1].text) || strings.Contains(text, items[2].text) || strings.Contains(text, items[3].text) {
		t.Fatalf("extraction context mixed business objects: %s", text)
	}
}

func TestMemoryScopeUsesPinnedAndRoutedAgentIdentity(t *testing.T) {
	fm := newFileMem(t, 200)
	const text = "LEGACY_ROLE_FACT 现有角色私有资料"
	if err := fm.SaveEntryForRole(text, "fact", "manual", "tutor-a"); err != nil {
		t.Fatal(err)
	}
	eng := engineWithFileMem(t, fm)
	for _, key := range []string{"pinned_agent", "routed_agent"} {
		t.Run(key, func(t *testing.T) {
			turn := eng.buildTurnContext(withRetrievalHitsSink(t.Context()), map[string]string{key: "tutor-a"}, "", "LEGACY_ROLE_FACT")
			if !strings.Contains(turn, text) {
				t.Fatalf("effective agent lost its existing role memory: %s", turn)
			}
		})
	}
}

func TestMemoryScopeFiltersResidentEntriesBeforeInjection(t *testing.T) {
	fm := newFileMem(t, 200)
	items := []struct {
		text  string
		scope memory.Scope
	}{
		{"COMMON_PREFERENCE 简洁回答", memory.Scope{}},
		{"AGENT_RULE 逐步讲解", memory.Scope{Agent: "tutor-a"}},
		{"PERSONA_RULE 私人暗号", memory.Scope{Skills: []string{"girlfriend"}}},
		{"SUBJECT_FACT 六年级", memory.Scope{SubjectID: "child-a"}},
		{"PROJECT_FACT 方案已确认", memory.Scope{ProjectID: "project-a"}},
	}
	for _, item := range items {
		if err := fm.SaveStructuredEntry(item.text, "rule", "manual", "", memory.EntryMeta{Scope: item.scope, Pinned: true}); err != nil {
			t.Fatal(err)
		}
	}
	eng := engineWithFileMem(t, fm)
	eng.skills = skill.NewRegistry()
	for _, name := range []string{"girlfriend", "lawyer"} {
		if err := eng.skills.Register(&fakePersonaSkill{name: name, body: "按当前职责回答。"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name, key, value, excluded string
	}{
		{name: "all matching"},
		{name: "other agent", key: "role", value: "tutor-b", excluded: "AGENT_RULE"},
		{name: "other persona", key: "skills", value: "lawyer", excluded: "PERSONA_RULE"},
		{name: "other child", key: "memory_subject_id", value: "child-b", excluded: "SUBJECT_FACT"},
		{name: "other project", key: "memory_project_id", value: "project-b", excluded: "PROJECT_FACT"},
		{name: "explicit off", key: "memory", value: "off", excluded: "all"},
	} {
		t.Run(test.name, func(t *testing.T) {
			metadata := map[string]string{"role": "tutor-a", "skills": "girlfriend", "memory_subject_id": "child-a", "memory_project_id": "project-a"}
			if test.key != "" {
				metadata[test.key] = test.value
			}
			turn := eng.buildTurnContext(withRetrievalHitsSink(t.Context()), metadata, "", "请按已有约定介绍当前情况")
			for _, item := range items {
				want := test.excluded != "all" && !strings.HasPrefix(item.text, test.excluded+" ")
				if got := strings.Contains(turn, item.text); got != want {
					t.Fatalf("scope=%s injected=%t want=%t: %s", item.text, got, want, turn)
				}
			}
			if test.excluded == "all" && strings.Contains(turn, "<memory-context>") {
				t.Fatal("memory off still created a memory envelope")
			}
		})
	}
}
