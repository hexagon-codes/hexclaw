package skilladapter

import (
	"context"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
)

func TestProgressSkill_UsesOnlyExplicitCurrentParentText(t *testing.T) {
	for _, tc := range []struct {
		text string
		unit int
	}{
		{"请把进度改为第三单元。", 3},
		{"老师正在讲到第3单元", 3},
		{"学校正在学第六单元", 6},
		{"把进度调整到第二单元", 2},
		{"老师讲到了第十一单元", 11},
		{"这道题在第三单元", 0},
		{"如果老师讲到第三单元应该怎么办", 0},
		{"老师讲到第三单元了吗？", 0},
		{"复习：老师讲到第三单元", 0},
		{"题干：\n老师讲到第三单元\n求学生人数", 0},
		{"题目说“老师讲到第三单元”，帮我解题", 0},
		{"引用：\"老师讲到第三单元\"", 0},
		{"```\n老师讲到第三单元\n```", 0},
		{"老师讲到第二单元，正在学第三单元", 0},
	} {
		t.Run(tc.text, func(t *testing.T) {
			unit, ok := explicitProgressUnit(tc.text)
			if unit != tc.unit || ok != (tc.unit > 0) {
				t.Fatalf("unit=%d ok=%v, want unit=%d", unit, ok, tc.unit)
			}
		})
	}
}

func TestProgressSkill_CatalogUnitMustBeUnique(t *testing.T) {
	for _, tc := range []struct {
		name  string
		units []k12.CurriculumCatalogUnit
		unit  int
		id    string
	}{
		{"printed ordinal", []k12.CurriculumCatalogUnit{{UnitID: "u3", Title: "第三单元 小数除法"}}, 3, "u3"},
		{"verified directory order", []k12.CurriculumCatalogUnit{{UnitID: "u1", Title: "观察物体"}, {UnitID: "u2", Title: "因数与倍数"}}, 2, "u2"},
		{"duplicate ordinal", []k12.CurriculumCatalogUnit{{UnitID: "u3a", Title: "第三单元"}, {UnitID: "u3b", Title: "第三单元"}}, 3, ""},
		{"absent unit", []k12.CurriculumCatalogUnit{{UnitID: "u1", Title: "第一单元"}}, 3, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unit, ok := uniqueProgressUnit(tc.units, tc.unit)
			if ok != (tc.id != "") || (ok && unit.UnitID != tc.id) {
				t.Fatalf("unit=%+v ok=%v, want id=%q", unit, ok, tc.id)
			}
		})
	}
}

func TestProgressSkill_ModelFlagsAndSystemTasksCannotConfirm(t *testing.T) {
	s := NewProgressSkill(usecase.Deps{})
	base := skill.WithRoutedAgent(context.Background(), "child")
	for _, ctx := range []context.Context{
		base,
		skill.WithOriginalUserText(base, "这是一道第三单元的复习题"),
		skill.WithOriginalUserText(skill.WithSystemDispatchSource(base, "cron"), "老师讲到第三单元"),
	} {
		if _, err := s.Execute(ctx, map[string]any{"confirmed": true, "unit_id": "u3", "evidence_source": "parent_confirmed"}); err == nil {
			t.Fatal("non-parent source incorrectly saved progress")
		}
	}
	ctx := skill.WithOriginalUserText(base, "老师讲到第三单元")
	ctx = skill.WithOriginalUserText(ctx, "")
	if skill.OriginalUserText(ctx) != "" {
		t.Fatal("a new empty source inherited the previous turn's parent text")
	}
}
