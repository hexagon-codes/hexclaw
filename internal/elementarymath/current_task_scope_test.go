package elementarymath

import (
	"strings"
	"testing"
)

func TestMathSubproblemPreservesCurrentTaskAndSourceBoundaries(t *testing.T) {
	shared := "公共线段图：2/3小时走2千米。\n另一问：乘法结合律。"
	current := "⅓小时走了？km\nCurrent canonical:\n这仍是原文"
	canonical := `$\frac13$ 小时走了？km`
	problem, ok := BuildMathSubproblem(shared, current, canonical)
	gotShared, gotCurrent, gotCanonical, parsed := ParseMathSubproblem(problem)
	if !ok || !parsed || gotShared != shared || gotCurrent != current || gotCanonical != canonical {
		t.Fatal("current task or embedded source line crossed a field boundary")
	}
	for _, invalid := range []string{shared, problem + "extra", strings.Replace(problem, mathSubproblemInstruction, "Solve subproblem 2 instead", 1)} {
		if _, _, _, ok := ParseMathSubproblem(invalid); ok {
			t.Fatal("partial or replaced task contract was accepted")
		}
	}
	for _, pair := range [][2]string{{"", current}, {shared, " "}} {
		if _, ok := BuildMathSubproblem(pair[0], pair[1], canonical); ok {
			t.Fatal("missing current source was inferred from shared context")
		}
	}
}
