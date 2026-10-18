package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
)

func partCountSlotEngineProblem(t *testing.T, current string) string {
	t.Helper()
	problem, ok := elementarymath.BuildPartCountSlotProblemFromSources("小明2/3小时走了2千米；线段图每段代表1/3小时。", current, "")
	if !ok {
		t.Fatal("count-slot fixture did not project")
	}
	return problem
}

func TestPartCountSlotSolveVerifyGradeUsesIndependentQuantity(t *testing.T) {
	for _, tc := range []struct{ name, problem, standard, computed, student, correct string }{
		{"actual count not elapsed time", "从上图看出：已知（ ）份是2千米，可以先求出这样的1份是多少，也就是", "2", "2份", "2/3", "false"},
		{"correct count", "已知（ ）份是2千米。", "2份", "2", "2", "true"},
		{"half part remains legitimate", "半个完整单元相当于（ ）份。", "1/2", "0.5份", "1/2", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problem := partCountSlotEngineProblem(t, tc.problem)
			graderFlag := "no"
			if tc.correct == "true" {
				graderFlag = "yes"
			}
			exec := &solveExec{solverOuts: []string{"按当前空位独立列出份数关系。\n答案：" + tc.standard},
				verifierOut: "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: " + tc.computed, verifierStdout: "COMPUTED: " + tc.computed + "\n",
				graderOut: "CORRECT: " + graderFlag + "\nFINAL_ANSWER_CORRECT: " + graderFlag + "\nWRONG_STEP: \nMISCONCEPTION: \nGUIDANCE: 核对份数空位。"}
			solve := NewSolveSkill(exec.fn, nil)
			result, err := solve.Execute(t.Context(), map[string]any{"problem": problem, "subject": "数学"})
			if err != nil || result == nil || result.Metadata["solve_evidence"] != "numeric_exec" || result.Metadata["solve_verdict"] != "agree" {
				t.Fatalf("count target lost independent evidence: %+v %v", result, err)
			}
			graded, err := solve.GradeVerified(t.Context(), problem, result.Content, tc.student)
			if err != nil || graded.Metadata["grade_correct"] != tc.correct || graded.Metadata["grade_final_answer_correct"] != tc.correct {
				t.Fatalf("count answer compared against parent distance: %+v %v", graded, err)
			}
			for _, agent := range []string{solverAgentName, verifierAgentName, graderAgentName} {
				spec, ok := exec.specFor(agent)
				if !ok || !strings.Contains(spec.Task, problem) {
					t.Fatalf("%s received another answer target", agent)
				}
			}
			grader, _ := exec.specFor(graderAgentName)
			if !strings.Contains(grader.Task, "Student answer: "+tc.student+"\n") || exec.solverCalls() != 1 || exec.verifierCalls() != 1 || exec.graderCalls() != 1 {
				t.Fatal("student answer was changed or successful solver repeated")
			}
		})
	}
}

func TestPartCountSlotCompleteQuantityComparisonAndInvalidReference(t *testing.T) {
	problem := partCountSlotEngineProblem(t, "已知（ ）份是2千米。")
	for _, tc := range []struct {
		computed, candidate string
		equal, comparable   bool
	}{
		{"2份", "2", true, true}, {"2", "2份", true, true}, {"2份", "2/3", false, true},
		{"0.5份", "1/2", true, true}, {"0份", "0", true, true},
		{"1千米", "2", false, false}, {"2份", "2份；1千米", false, false},
		{"2份；1千米", "2", false, false}, {"2份", "2/3小时", false, false}, {"-1份", "-1", false, false},
	} {
		if equal, comparable := verificationAnswersEqual(problem, tc.computed, tc.candidate); equal != tc.equal || comparable != tc.comparable {
			t.Errorf("complete comparison %q / %q: %v,%v", tc.computed, tc.candidate, equal, comparable)
		}
	}
	for _, answer := range []string{"1千米", "2/3小时", "2份；1千米", "2份，时间2/3小时", "-1份"} {
		t.Run(answer, func(t *testing.T) {
			exec := &solveExec{solverOuts: []string{"答案：" + answer}}
			solve := NewSolveSkill(exec.fn, nil)
			published, retainedRaw := 0, ""
			ctx := WithSubAgentCallInterceptor(t.Context(), SubAgentCallInterceptorFunc(func(ctx context.Context, spec SubAgentSpec, next SubAgentExecFunc) (SubAgentResult, error) {
				result, err := next(ctx, spec)
				if err == nil {
					published++
				} else {
					retainedRaw = result.Output
				}
				return result, err
			}))
			result, err := solve.Execute(ctx, map[string]any{"problem": problem})
			if result != nil || !errors.Is(err, errPartCountSlotAnswerTarget) || !errors.Is(err, egress.ErrProviderResponseProcessed) || exec.solverCalls() != 1 || exec.verifierCalls() != 0 || published != 0 || retainedRaw != "答案："+answer {
				t.Fatalf("wrong-target standard was published or lost: %+v %v published=%d raw=%q", result, err, published, retainedRaw)
			}
			if result, err := solve.GradeVerified(t.Context(), problem, "答案："+answer, "2/3"); result != nil || !errors.Is(err, errPartCountSlotAnswerTarget) || exec.graderCalls() != 0 {
				t.Fatalf("wrong frozen standard reached grader: %+v %v", result, err)
			}
		})
	}
}

func TestPartCountSlotHardComplexityAndExplicitPolicyRemain(t *testing.T) {
	problem := partCountSlotEngineProblem(t, strings.Repeat("材料", 61)+"对应（ ）份。")
	if assessComplexity(problem) != complexityHard {
		t.Fatal("count fixture must be hard under the existing policy")
	}
	for _, tc := range []struct {
		name     string
		explicit bool
		calls    int
	}{{"auto", false, 2}, {"explicit", true, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			exec := &solveExec{solverOuts: []string{"答案：2", "答案：2"}, verifierOut: "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: 2份", verifierStdout: "COMPUTED: 2份\n"}
			args := map[string]any{"problem": problem}
			if tc.explicit {
				args["method_diversity"] = false
			}
			result, err := NewSolveSkill(exec.fn, nil).Execute(t.Context(), args)
			if err != nil || result == nil || exec.solverCalls() != tc.calls || exec.verifierCalls() != 1 {
				t.Fatalf("count target changed method policy: %+v %v solvers=%d", result, err, exec.solverCalls())
			}
		})
	}
}
