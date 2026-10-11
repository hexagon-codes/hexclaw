package usecase

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/skill"
)

func TestSkillContextReceiptsPhotoUsesFrozenSuccessfulTeachingOnly(t *testing.T) {
	receipt := skill.ContextReceipt{Name: "math-tutor", Version: "1.0.0", Status: "loaded", ContentDigest: "sha256:f6caef336258acf16fe84b114a4361ee26269c9402f2a5d1e4391f697ee4878d", Methods: []skill.TeachingMethod{{Name: "波利亚四步"}}}
	guide := solveTeachingTestGuide()
	item := PhotoGradeItem{Status: PhotoBlankSolved, ParentGuide: &guide, Solve: SolveHomeworkResult{Generation: &SolveGeneration{SkillContextReceipts: []skill.ContextReceipt{receipt}}}}
	got := photoSkillContextReceipts(PhotoGradeResult{Items: []PhotoGradeItem{item, item}})
	if len(got) != 1 || got[0].Name != "math-tutor" || got[0].Methods[0].Name != "波利亚四步" {
		t.Fatalf("successful frozen result lost or duplicated basis: %+v", got)
	}
	for _, state := range []string{"prepared", "failed", "no-guide", "historical"} {
		t.Run(state, func(t *testing.T) {
			candidate := item
			switch state {
			case "prepared":
				r := receipt
				r.Status = "prepared"
				candidate.Solve.Generation = &SolveGeneration{SkillContextReceipts: []skill.ContextReceipt{r}}
			case "failed":
				candidate.Status = PhotoFailed
			case "no-guide":
				candidate.ParentGuide = nil
			case "historical":
				candidate.Solve.Generation = nil
			}
			if len(photoSkillContextReceipts(PhotoGradeResult{Items: []PhotoGradeItem{candidate}})) != 0 {
				t.Fatal("non-executed or historical context fabricated teaching evidence")
			}
		})
	}
	// 新可选字段为空时不改变旧 JSON；恢复旧任务不会出现当前 Skill 元数据。
	old := PhotoGradeItem{Status: PhotoBlankSolved, ParentGuide: &guide}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "skill_context_receipts") || strings.Contains(string(raw), "parent_teaching_skill_snapshot") {
		t.Fatal("old frozen payload shape changed")
	}
	var restored PhotoGradeItem
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if len(photoSkillContextReceipts(PhotoGradeResult{Items: []PhotoGradeItem{restored}})) != 0 {
		t.Fatal("old restored payload acquired current basis")
	}
	parent := withSolveTeachingRequest(t.Context(), GradeRequest{SolveOutputVersion: SolveOutputWithParentGuideV1, ParentTeachingContract: "original inherited contract", ParentTeachingSkillSnapshot: []skill.ContextReceipt{receipt}})
	child := withSolveTeachingRequest(parent, GradeRequest{})
	if len(SolveTeachingSkillSnapshot(child)) != 0 || len(SolveTeachingSkillSnapshot(parent)) != 1 {
		t.Fatal("legacy request inherited another task's display evidence")
	}
	version, contract := SolveTeachingRequest(child)
	if version != SolveOutputWithParentGuideV1 || contract != "original inherited contract" {
		t.Fatal("display isolation changed existing contract inheritance")
	}
}
