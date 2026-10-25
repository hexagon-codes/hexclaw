package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestScopePersistenceAndLegacyCompatibility(t *testing.T) {
	dir := t.TempDir()
	fm, err := New(Options{Enabled: true, Dir: dir, MaxMemory: 200})
	if err != nil {
		t.Fatal(err)
	}
	want := Scope{Agent: "assistant-a", Skills: []string{"girlfriend"}, SubjectID: "child-a", ProjectID: "project-a"}
	const text = "知道现在几几年吗？\n几几黏你去洗啊，麻利滴儿"
	if err := fm.SaveStructuredEntry(text, "rule", "manual", "", EntryMeta{Scope: want, Pinned: true}); err != nil {
		t.Fatal(err)
	}
	if err := fm.SaveStructuredEntry("用户喜欢简洁的回答", "preference", "manual", "", EntryMeta{}); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Options{Enabled: true, Dir: dir, MaxMemory: 200})
	if err != nil {
		t.Fatal(err)
	}
	entries := reopened.ParseEntries()
	if len(entries) != 2 || entries[0].Content != text || !entries[0].Pinned || !entries[0].Scope.Equal(want) {
		t.Fatalf("scope or original multiline content was lost: %+v", entries)
	}
	if !entries[1].Scope.IsZero() || len(EntriesForScope(entries, Scope{})) != 1 {
		t.Fatal("unscoped preference compatibility or scoped exclusion changed")
	}
	encoded, err := json.Marshal(entries[1])
	if err != nil || strings.Contains(string(encoded), `"scope"`) {
		t.Fatalf("legacy JSON unexpectedly gained an empty scope: %s %v", encoded, err)
	}
}

func TestScopeDoesNotLeakIntoGlobalProfile(t *testing.T) {
	fm := newFM(t)
	if err := fm.SaveStructuredEntry("用户偏好简洁回答", "preference", "manual", "", EntryMeta{}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		text  string
		scope Scope
	}{
		{"女友专属互动约定", Scope{Skills: []string{"girlfriend"}}},
		{"孩子正在读六年级", Scope{SubjectID: "child-a"}},
		{"项目采用特定交付方式", Scope{ProjectID: "project-a"}},
		{"当前助教的讲解约定", Scope{Agent: "tutor-a"}},
	} {
		if err := fm.SaveStructuredEntry(item.text, "fact", "manual", "", EntryMeta{Scope: item.scope}); err != nil {
			t.Fatal(err)
		}
	}
	facts, _ := fm.collectProfileInputs("", time.Now())
	if len(facts) != 1 || facts[0] != "用户偏好简洁回答" {
		t.Fatalf("global profile consumed private facts: %v", facts)
	}
	facts, _ = fm.collectProfileInputs("tutor-a", time.Now())
	if len(facts) != 2 || !strings.Contains(strings.Join(facts, "\n"), "当前助教的讲解约定") {
		t.Fatalf("agent profile lost its own fact or consumed another scope: %v", facts)
	}
}

func TestScopeReflectionKeepsDifferentObjects(t *testing.T) {
	fm := newFM(t)
	for _, subject := range []string{"child-a", "child-b"} {
		if err := fm.SaveStructuredEntry("喜欢阅读科普读物", "fact", "chat_extract", "", EntryMeta{Scope: Scope{SubjectID: subject}}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := fm.ReflectRole("", time.Now())
	if err != nil || report.Deduped != 0 || report.Superseded != 0 || len(fm.ParseEntries()) != 2 {
		t.Fatalf("reflection merged different children: %+v %v", report, err)
	}
}

func TestScopeDeepConsolidationInheritsSourceScope(t *testing.T) {
	fm := newFM(t)
	fake := &fakeConsolidator{reply: "这个孩子喜欢阅读科普读物，也喜欢画水彩画。"}
	fm.WithConsolidator(fake).WithDreamOptions(foldableOpts())
	for _, item := range []struct{ text, child string }{
		{"喜欢阅读科普读物", "child-a"},
		{"喜欢画水彩画", "child-a"},
		{"喜欢阅读科普读物", "child-b"},
	} {
		if err := fm.SaveStructuredEntry(item.text, "fact", "chat_extract", "", EntryMeta{Subject: "兴趣", Scope: Scope{SubjectID: item.child}}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := fm.DeepReflectRole(t.Context(), "", time.Now())
	if err != nil || report.Folded != 2 || report.Synthesized != 1 || fake.calls != 1 {
		t.Fatalf("different scopes entered one synthesis: %+v %v", report, err)
	}
	var found bool
	for _, entry := range fm.ParseEntries() {
		if entry.Content == fake.reply {
			found = true
			if entry.Scope.SubjectID != "child-a" {
				t.Fatalf("synthesis lost its scope: %+v", entry)
			}
		}
	}
	if !found {
		t.Fatal("synthesized memory was not persisted")
	}
}

func TestScopeDedupDoesNotMergeDifferentObjects(t *testing.T) {
	fm, err := New(Options{Enabled: true, Dir: t.TempDir(), MaxMemory: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"child-a", "child-b"} {
		action, err := fm.UpsertScopedFactForRole("目前就读六年级", "fact", "manual", "", "年级", "", "", Scope{SubjectID: subject})
		if err != nil || action != "insert" {
			t.Fatalf("different child was deduplicated: %s %v", action, err)
		}
	}
	if action, err := fm.UpsertScopedFactForRole("目前就读六年级", "fact", "manual", "", "年级", "", "", Scope{SubjectID: "child-a"}); err != nil || action != "discard" {
		t.Fatalf("same-scope replay did not remain idempotent: %s %v", action, err)
	}
	if entries := fm.ParseEntries(); len(entries) != 2 || entries[0].ID == entries[1].ID {
		t.Fatalf("object identity changed: %+v", entries)
	}
}
