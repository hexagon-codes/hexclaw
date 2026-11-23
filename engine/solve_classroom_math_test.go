package engine

import (
	"strings"
	"testing"
)

func TestVerifiedClassroomMathKeepsAnswerAndNamedTargets(t *testing.T) {
	const grade = "四则运算、分数乘法、分数除法、乘法分配律、简易方程、比的应用"
	const named = "甲队和乙队共有45人，甲队与乙队人数的比是2∶3。甲队、乙队各有多少人？"
	for _, tc := range []struct{ problem, answer, method, check string }{
		{"(3/4+1/6)×12", "11", "分配律", "11"},
		{"5/9×7/8+5/9×1/8", "5/9", "共同的因数 5/9", "5/72"},
		{"把3/4:1/2化成最简单的整数比。", "3∶2", "同乘", "互质"},
		{"x-1/4x=18", "x=24", "0.75x", "左边"},
		{named, "甲队18人，乙队27人。", "2∶3", "18 + 27 = 45"},
		{"2+5", "7", "数位", "7"},
	} {
		m, ok := VerifiedClassroomMath(tc.problem, grade, tc.answer)
		if !ok || len(m.Steps) == 0 || !strings.Contains(m.Method, tc.method) || !strings.Contains(m.Check, tc.check) || m.Mistake == "" || m.Prompt == "" || m.Question == "" {
			t.Fatalf("%s: %+v ok=%v", tc.problem, m, ok)
		}
	}
	for _, tc := range []struct{ problem, answer, allowed string }{
		{named, "甲队27人，乙队18人", grade}, {named, "18人", grade},
		{named, "甲队18本，乙队27本", grade},
		{"广场有45人，甲队与乙队人数的比是2∶3。甲队、乙队各有多少人？", "甲队18人，乙队27人", grade},
		{"甲队和乙队共有44人，甲队与乙队人数的比是2∶3。甲队、乙队各有多少人？", "甲队18人，乙队26人", grade},
		{"把3/4:1/2化成最简单的整数比。", "6∶4", grade},
		{"x-1/4x=18", "24", grade},
		{"12+3", "推测 12+3=15，或者18", grade},
		{"求阴影面积，图中右侧长度无法确认。", "12平方厘米", grade},
		{named, "甲队18人，乙队27人", "整数加减法"},
		{"图书角有240本书，故事书占总数的3/8。故事书有多少本？", "科技书有90本", grade},
		{"x-1/4x=18", "x=24", "四则运算，尚未学习简易方程"},
		{named, "甲队18人，乙队27人", "四则运算，未学习比的应用"},
	} {
		if _, ok := VerifiedClassroomMath(tc.problem, tc.allowed, tc.answer); ok {
			t.Fatalf("unexpected local guide for %s / %s", tc.problem, tc.answer)
		}
	}
	if _, old := compileCurrentSolveProgram(WithClassroomMath(t.Context(), false), named, grade); old {
		t.Fatal("old contract acquired a new solve")
	}
	if _, current := compileCurrentSolveProgram(WithClassroomMath(t.Context(), true), named, grade); !current {
		t.Fatal("current contract did not compile the complete share problem")
	}
	for _, restriction := range []string{"四则运算、分数乘法，未学乘法分配律", "四则运算；禁止使用乘法分配律"} {
		m, ok := VerifiedClassroomMath("(3/4+1/6)×12", restriction, "11")
		if !ok || strings.Contains(m.Method, "分配律") || strings.Contains(m.Method, "分别乘") {
			t.Fatalf("excluded method used: %+v", m)
		}
	}
}
