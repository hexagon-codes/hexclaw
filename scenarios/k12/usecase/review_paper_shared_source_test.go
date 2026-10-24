package usecase

import (
	"reflect"
	"strings"
	"testing"
)

func TestReviewPaperSharedMaterialPreservesAllChildTargetsAndStandalonePrompts(t *testing.T) {
	const material = "已用空间占总容量的2/5，容量为4.8 TB。"
	first, second := "（1）先求一份，再求总容量。", "（2）根据数量关系填空。"
	items := []reviewPaperItem{
		{Statement: first + "\n\n" + material, SharedSource: &reviewPaperSharedSource{Key: "same-published-parent", Current: first, Material: material}},
		{Statement: second + "\n\n" + material, SharedSource: &reviewPaperSharedSource{Key: "same-published-parent", Current: second, Material: material}},
	}
	before := []string{items[0].Statement, items[1].Statement}
	display := reviewPaperDisplayStatements(items)
	if !reflect.DeepEqual(display, []string{material + "\n\n" + first, second}) || strings.Count(strings.Join(display, "\n"), material) != 1 {
		t.Fatalf("shared conditions or child targets were duplicated or lost: %v", display)
	}
	if !reflect.DeepEqual(before, []string{items[0].Statement, items[1].Statement}) {
		t.Fatal("paper presentation changed standalone weekly-practice prompts")
	}
	items[1].SharedSource.Key = "different-input-or-page-or-parent"
	if got := reviewPaperDisplayStatements(items); !reflect.DeepEqual(got, before) {
		t.Fatal("different source conditions were hidden under another parent")
	}
	items[1].SharedSource = nil
	if got := reviewPaperDisplayStatements(items); !reflect.DeepEqual(got, before) {
		t.Fatal("missing shared-source evidence removed independent conditions")
	}
}
