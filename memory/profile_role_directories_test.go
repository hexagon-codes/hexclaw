package memory

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 真实临时文件同时保留回执和角色事实，验证内部目录不额外处理全局画像。
func TestProfileAllSkipsOnlyInternalReceiptDirectories(t *testing.T) {
	fm, err := New(Options{Dir: t.TempDir(), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = fm.SaveEntry("用户偏好清晰简短的说明", "preference", "manual"); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"learner", ".study-role"} {
		if err = fm.SaveStructuredEntry("当前角色为"+role, "fact", "manual", role, EntryMeta{}); err != nil {
			t.Fatal(err)
		}
	}
	const receipt = `{"state":"unknown","digest":"original-receipt"}`
	for _, directory := range []string{".event-receipts", ".profile-operations"} {
		path := filepath.Join(fm.dir, directory)
		if err = os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(path, "existing.json"), []byte(receipt), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if got := fm.listRoles(); !reflect.DeepEqual(got, []string{".study-role", "learner"}) {
		t.Fatalf("real role enumeration changed: %v", got)
	}
	var calls []int
	syn := consistencySynth(func(_ context.Context, facts []string, _ string) (string, error) {
		calls = append(calls, len(facts))
		return strings.Join(facts, "。"), nil
	})
	if err = fm.DistillProfileAll(context.Background(), syn, DistillProfileConfig{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []int{1, 2, 2}) {
		t.Fatalf("global or role processing duplicated: facts per call=%v", calls)
	}
	for _, directory := range []string{".event-receipts", ".profile-operations"} {
		data, readErr := os.ReadFile(filepath.Join(fm.dir, directory, "existing.json"))
		if readErr != nil || string(data) != receipt {
			t.Fatalf("existing receipt changed: directory=%s err=%v", directory, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(fm.dir, directory, memoryActiveFile)); !os.IsNotExist(statErr) {
			t.Fatalf("internal directory received a profile: directory=%s err=%v", directory, statErr)
		}
	}
	for _, role := range []string{"", ".study-role", "learner"} {
		entries := fm.parseEntriesFromDir(fm.roleDir(role))
		profiles := 0
		for _, entry := range entries {
			if isProfileEntry(entry) {
				profiles++
			}
		}
		if profiles != 1 {
			t.Fatalf("real scope lost its profile: role=%q profiles=%d", role, profiles)
		}
	}
}
