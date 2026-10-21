package memory

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResidenceAdministrativeSuffixAndPrecision(t *testing.T) {
	for _, tc := range []struct {
		old, fresh, action string
		active             int
	}{
		{"北京市", "上海", "supersede", 1},
		{"杭州", "南京市", "supersede", 1},
		{"北京", "北京市", "discard", 1},
		{"北京市", "北京市朝阳区", "insert", 2},
		{"浙江省", "杭州市", "insert", 2},
		{"浙江省杭州市", "浙江省宁波市", "supersede", 1},
	} {
		t.Run(tc.old+"/"+tc.fresh, func(t *testing.T) {
			fm := newFM(t)
			if _, err := fm.UpsertStructuredFactForRole("小明住在"+tc.old, "fact", "manual", "", "人物:小明", "current_residence", tc.old); err != nil {
				t.Fatal(err)
			}
			action, err := fm.UpsertStructuredFactForRole("小明现在住在"+tc.fresh, "fact", "manual", "", "人物:小明", "current_residence", tc.fresh)
			if err != nil || action != tc.action {
				t.Fatalf("action=%s err=%v", action, err)
			}
			active := 0
			for _, entry := range fm.ParseEntries() {
				if entry.ValidTo == "" {
					active++
				}
			}
			if active != tc.active {
				t.Fatalf("active=%d want=%d", active, tc.active)
			}
		})
	}
}

func TestFailedResidenceReplacementPreservesOldFact(t *testing.T) {
	fm := newFM(t)
	if _, err := fm.UpsertStructuredFactForRole("小明住在北京", "fact", "manual", "", "人物:小明", "current_residence", "北京"); err != nil {
		t.Fatal(err)
	}
	before := fm.ParseEntriesForRole("tutor")
	if err := os.MkdirAll(filepath.Join(fm.roleDir("tutor"), memoryActiveFile), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := fm.UpsertStructuredFactForRole("小明现在住在上海", "fact", "manual", "tutor", "人物:小明", "current_residence", "上海"); err == nil {
		t.Fatal("unwritable target accepted")
	}
	if after := fm.ParseEntriesForRole("tutor"); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed replacement changed old facts: before=%+v after=%+v", before, after)
	}
}
