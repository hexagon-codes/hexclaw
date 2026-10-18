package engine

import (
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
)

func TestMathSubproblemSolveVerifyGradeUsesCurrentTask(t *testing.T) {
	problem, ok := elementarymath.BuildMathSubproblem(strings.Repeat("公共材料与其他问项。", 30), "1/3小时走了？km", "1/3小时走了？km")
	if !ok || assessComplexity(problem) != complexityStandard {
		t.Fatal("unrelated shared questions changed the current task complexity")
	}
	exec := &solveExec{solverOuts: []string{"2/3小时走2千米，一份1/3小时走1千米。\n答案：1千米"},
		verifierOut: "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: 1千米", verifierStdout: "COMPUTED: 1千米\n",
		graderOuts: []string{"CORRECT: yes\nFINAL_ANSWER_CORRECT: yes\nWRONG_STEP:\nMISCONCEPTION:\nGUIDANCE: 正确。"}}
	solve := NewSolveSkill(exec.fn, nil)
	result, err := solve.Execute(t.Context(), map[string]any{"problem": problem, "subject": "数学", "grade": "六年级上"})
	if err != nil || result == nil || result.Metadata["solve_evidence"] != "numeric_exec" || exec.solverCalls() != 1 || exec.verifierCalls() != 1 {
		t.Fatalf("current quantity was not independently verified once: %+v %v", result, err)
	}
	if _, err := solve.GradeVerified(t.Context(), problem, result.Content, "1千米"); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{solverAgentName, verifierAgentName, graderAgentName} {
		spec, ok := exec.specFor(agent)
		if !ok || !strings.Contains(spec.Task, problem) {
			t.Fatalf("%s lost the exact current task contract", agent)
		}
	}
	hard, _ := elementarymath.BuildMathSubproblem("公共材料", "解释为什么这里需要乘法结合律？", "")
	if assessComplexity(hard) != complexityHard {
		t.Fatal("a genuinely hard current task was downgraded")
	}
}
