package builtin

import (
	"testing"

	"github.com/hexagon-codes/hexclaw/memory"
)

func TestManageMemoryScopeBindsToCurrentContext(t *testing.T) {
	fm, err := memory.New(memory.Options{Enabled: true, Dir: t.TempDir(), MaxMemory: 200})
	if err != nil {
		t.Fatal(err)
	}
	s := NewManageMemorySkill(fm)
	ctx := memory.WithScope(t.Context(), memory.Scope{Agent: "tutor-a", Skills: []string{"girlfriend"}, SubjectID: "child-a", ProjectID: "project-a"})
	for _, test := range []struct {
		name string
		want memory.Scope
	}{
		{"global", memory.Scope{}},
		{"agent", memory.Scope{Agent: "tutor-a"}},
		{"persona", memory.Scope{Skills: []string{"girlfriend"}}},
		{"subject", memory.Scope{SubjectID: "child-a"}},
		{"project", memory.Scope{ProjectID: "project-a"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := s.Execute(ctx, map[string]any{"action": "add", "content": "持久约定 " + test.name, "type": "rule", "scope": test.name}); err != nil {
				t.Fatal(err)
			}
			entries := fm.ParseEntries()
			last := entries[len(entries)-1]
			if !last.Scope.Equal(test.want) {
				t.Fatalf("scope was invented or broadened: %+v", last)
			}
		})
	}
	before := len(fm.ParseEntries())
	if _, err := s.Execute(t.Context(), map[string]any{"action": "add", "content": "未绑定项目不能被猜成全局", "scope": "project"}); err == nil {
		t.Fatal("missing project identity silently broadened the scope")
	}
	if len(fm.ParseEntries()) != before {
		t.Fatal("missing-context operation persisted a memory")
	}
}

func TestManageMemoryScopeTargetOperationsStayInContext(t *testing.T) {
	fm, err := memory.New(memory.Options{Enabled: true, Dir: t.TempDir(), MaxMemory: 200})
	if err != nil {
		t.Fatal(err)
	}
	if err := fm.SaveStructuredEntry("孩子B的年级资料", "fact", "manual", "", memory.EntryMeta{Scope: memory.Scope{SubjectID: "child-b"}}); err != nil {
		t.Fatal(err)
	}
	entry := fm.ParseEntries()[0]
	s := NewManageMemorySkill(fm)
	wrong := memory.WithScope(t.Context(), memory.Scope{SubjectID: "child-a"})
	for _, action := range []string{"update", "remove", "pin"} {
		if _, err := s.Execute(wrong, map[string]any{"action": action, "target": entry.ID, "content": "不可覆盖其他孩子"}); err != nil {
			t.Fatal(err)
		}
	}
	entries := fm.ParseEntries()
	if len(entries) != 1 || entries[0].Content != entry.Content || entries[0].Pinned {
		t.Fatalf("another context changed the target: %+v", entries)
	}
	correct := memory.WithScope(t.Context(), memory.Scope{SubjectID: "child-b"})
	if _, err := s.Execute(correct, map[string]any{"action": "pin", "target": entry.ID}); err != nil {
		t.Fatal(err)
	}
	if entries := fm.ParseEntries(); !entries[0].Pinned || !entries[0].Scope.Equal(entry.Scope) {
		t.Fatalf("same-scope pin lost its applicability: %+v", entries)
	}
}
