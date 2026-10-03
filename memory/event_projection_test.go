package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryProjectionRevisionAndRemoval(t *testing.T) {
	dir := t.TempDir()
	newMemory := func() *FileMemory {
		t.Helper()
		fm, err := New(Options{Dir: dir})
		if err != nil {
			t.Fatal(err)
		}
		return fm
	}
	fm := newMemory()
	meta := EntryMeta{Subject: "加法"}
	if err := fm.SaveStructuredEvent("attempt-a", "原错因", "fact", "学情", "child", meta); err != nil {
		t.Fatal(err)
	}
	if err := fm.SaveStructuredEvent("attempt-b", "独立作答", "fact", "学情", "child", meta); err != nil {
		t.Fatal(err)
	}
	id := "event-" + memoryEventHash([]byte("attempt-a"))
	if err := fm.ReviseStructuredEvent("attempt-a", 1, "新错因", "fact", "学情", "child", meta); err != nil {
		t.Fatal(err)
	}
	fm = newMemory()
	for _, revision := range []int{1, 2, 1} {
		content := "新错因"
		if revision == 2 {
			content = ""
		}
		if err := fm.ReviseStructuredEvent("attempt-a", revision, content, "fact", "学情", "child", meta); err != nil {
			t.Fatal(err)
		}
	}
	if err := fm.SaveStructuredEvent("attempt-a", "原错因", "fact", "学情", "child", meta); err != nil {
		t.Fatal(err)
	}
	entries := fm.ParseEntriesForRole("child")
	if len(entries) != 1 || entries[0].ID == id || entries[0].Content != "独立作答" {
		t.Fatalf("correction changed another attempt or restored old source: %+v", entries)
	}
	// 先收到撤回再收到原始事件，同样不能产生已经失效的正文。
	if err := fm.ReviseStructuredEvent("late", 1, "", "fact", "学情", "child", meta); err != nil {
		t.Fatal(err)
	}
	if err := fm.SaveStructuredEvent("late", "迟到错误", "fact", "学情", "child", meta); err != nil {
		t.Fatal(err)
	}
	if len(fm.ParseEntriesForRole("child")) != 1 {
		t.Fatal("late original event resurrected a retracted insight")
	}
}

func TestMemoryProjectionPreservesManualChanges(t *testing.T) {
	for _, action := range []string{"edit", "archive", "delete"} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			fm, err := New(Options{Dir: dir})
			if err != nil {
				t.Fatal(err)
			}
			meta := EntryMeta{Subject: "加法"}
			if err = fm.SaveStructuredEvent("source", "原错因", "fact", "学情", "child", meta); err != nil {
				t.Fatal(err)
			}
			id := fm.ParseEntriesForRole("child")[0].ID
			switch action {
			case "edit":
				err = fm.UpdateEntry(id, "家长自己的记录")
			case "archive":
				err = fm.ArchiveEntry(id)
			case "delete":
				err = fm.DeleteEntry(id)
			}
			if err != nil {
				t.Fatal(err)
			}
			fm, err = New(Options{Dir: dir})
			if err != nil {
				t.Fatal(err)
			}
			for revision, content := range []string{"", "更正错因"} {
				if err = fm.ReviseStructuredEvent("source", revision+1, content, "fact", "学情", "child", meta); err != nil {
					t.Fatal(err)
				}
			}
			got := fm.ParseEntriesForRole("child")
			if action == "edit" {
				if len(got) != 1 || got[0].Content != "家长自己的记录" {
					t.Fatalf("manual content overwritten: %+v", got)
				}
			} else if len(got) != 0 {
				t.Fatalf("removed entry restored: %+v", got)
			}
			if action == "archive" && fm.CapacityForRole("child").Archived != 1 {
				t.Fatal("archive changed")
			}
		})
	}
}

func TestMemoryProjectionRecoversAtomicWriteBoundary(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		applied  bool
		revision int
		content  string
	}{
		{"prepared", false, 1, "更正错因"},
		{"written", true, 1, "更正错因"},
		{"newer_after_prepared", false, 2, "最新错因"},
		{"newer_after_written", true, 2, "最新错因"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			dir := t.TempDir()
			fm, err := New(Options{Dir: dir})
			if err != nil {
				t.Fatal(err)
			}
			meta := EntryMeta{Subject: "加法"}
			if err = fm.SaveStructuredEvent("source", "原错因", "fact", "学情", "child", meta); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(fm.roleDir("child"), memoryActiveFile)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = fm.ReviseStructuredEvent("source", 1, "更正错因", "fact", "学情", "child", meta); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			rp := filepath.Join(dir, ".event-receipts", memoryEventHash([]byte("source"))+".json")
			raw, err := os.ReadFile(rp)
			if err != nil {
				t.Fatal(err)
			}
			var receipt eventEntryReceipt
			if err = json.Unmarshal(raw, &receipt); err != nil {
				t.Fatal(err)
			}
			rel, _ := filepath.Rel(dir, path)
			receipt.Projection.Pending = &eventProjectionWrite{Path: rel, BeforeHash: memoryEventHash(before), AfterHash: memoryEventHash(after)}
			if !scenario.applied {
				if err = os.WriteFile(path, before, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err = saveEventEntryReceipt(rp, receipt); err != nil {
				t.Fatal(err)
			}
			fm, err = New(Options{Dir: dir})
			if err != nil {
				t.Fatal(err)
			}
			if err = fm.ReviseStructuredEvent("source", scenario.revision, scenario.content, "fact", "学情", "child", meta); err != nil {
				t.Fatal(err)
			}
			got := fm.ParseEntriesForRole("child")
			if len(got) != 1 || got[0].Content != scenario.content {
				t.Fatalf("atomic replay: %+v", got)
			}
		})
	}
}
