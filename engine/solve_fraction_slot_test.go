package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
)

func fractionSlotEngineProblem(t *testing.T, current string, canonical ...string) string {
	t.Helper()
	currentCanonical := ""
	if len(canonical) > 0 {
		currentCanonical = canonical[0]
	}
	problem, ok := elementarymath.BuildFractionSlotProblemFromSources("小明2/3小时走了2千米，照这样的速度，他每小时行走多少千米？\n线段图：1小时？km；1/3小时？km；2/3小时2km。", current, currentCanonical)
	if !ok {
		t.Fatal("printed fraction target fixture did not project")
	}
	return problem
}

// 正式 Solve/Verify/Grade 共用同一当前题面；仅模型与工具输出使用受控边界。
func TestFractionSlotSolveVerifyGradePreservesPrintedAnswerTarget(t *testing.T) {
	for _, source := range []struct{ raw, canonical string }{
		{"也就是2÷2＝2×□/□；", ""}, {"也就是2×□/□×3。", ""},
		{"2÷2＝2×　/　；", `$2\div 2=2\times\frac{\phantom{1}}{\phantom{2}}$；`},
		{"也就是2×　/　×3。", `也就是 $2\times\frac{\phantom{1}}{\phantom{2}}\times3$。`},
	} {
		t.Run(source.raw, func(t *testing.T) {
			problem := fractionSlotEngineProblem(t, source.raw, source.canonical)
			exec := &solveExec{solverOuts: []string{"除以2等于乘2的倒数；这里只填写分数空位。\n答案：1/2"},
				verifierOut: "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: 1/2\n说明：独立验证应填的分数。", verifierStdout: "COMPUTED: 1/2\n",
				graderOuts: []string{"CORRECT: yes\nFINAL_ANSWER_CORRECT: yes\nWRONG_STEP: \nMISCONCEPTION: \nGUIDANCE: 所填分数正确。", "CORRECT: no\nFINAL_ANSWER_CORRECT: no\nWRONG_STEP: 填成1/3\nMISCONCEPTION: 应填2的倒数\nGUIDANCE: 核对分数空位。"}}
			solve := NewSolveSkill(exec.fn, nil)
			result, err := solve.Execute(t.Context(), map[string]any{"problem": problem, "subject": "数学", "grade": "六年级上"})
			if err != nil || result == nil || result.Metadata["solve_evidence"] != "numeric_exec" || result.Metadata["solve_verdict"] != "agree" {
				t.Fatalf("scoped fraction lacked independent proof: result=%+v err=%v", result, err)
			}
			for _, agent := range []string{solverAgentName, verifierAgentName} {
				spec, ok := exec.specFor(agent)
				if !ok || !strings.Contains(spec.Task, problem) {
					t.Fatalf("%s received a different or partial answer scope", agent)
				}
			}
			for _, tc := range []struct{ student, correct string }{{"1/2", "true"}, {"1/3", "false"}} {
				graded, err := solve.GradeVerified(t.Context(), problem, result.Content, tc.student)
				if err != nil || graded.Metadata["grade_correct"] != tc.correct || graded.Metadata["grade_final_answer_correct"] != tc.correct {
					t.Fatalf("fraction slot grade requires an unrelated distance: result=%+v err=%v", graded, err)
				}
			}
			grader, ok := exec.specFor(graderAgentName)
			if !ok || !strings.Contains(grader.Task, problem) || !strings.Contains(grader.Task, "Correct answer: 1/2\n") || !strings.Contains(grader.Task, "Student answer: 1/2\n") {
				t.Fatal("grader did not receive the complete fraction target and matching reference")
			}
			if exec.solverCalls() != 1 || exec.verifierCalls() != 1 || exec.graderCalls() != 2 {
				t.Fatalf("grade repeated a successful solver: agents=%v", exec.agents())
			}
		})
	}
}

func TestFractionSlotRejectsWrongReferenceBeforeVerificationOrGrading(t *testing.T) {
	problem := fractionSlotEngineProblem(t, "也就是2÷2＝2×□/□；")
	for _, answer := range []string{"1 千米", "一份走1千米；方框从上到下填1、2", "1/2；1千米", "1、2", "1/2=0.5"} {
		t.Run(answer, func(t *testing.T) {
			exec := &solveExec{solverOuts: []string{"完整说明\n答案：" + answer}}
			solve := NewSolveSkill(exec.fn, nil)
			published := 0
			failedRaw := ""
			ctx := WithSubAgentCallInterceptor(t.Context(), SubAgentCallInterceptorFunc(func(ctx context.Context, spec SubAgentSpec, next SubAgentExecFunc) (SubAgentResult, error) {
				result, err := next(ctx, spec)
				if err == nil {
					published++
				} else {
					failedRaw = result.Output
				}
				return result, err
			}))
			result, err := solve.Execute(ctx, map[string]any{"problem": problem})
			if result != nil || !errors.Is(err, errFractionSlotAnswerTarget) || !errors.Is(err, egress.ErrProviderResponseProcessed) || exec.solverCalls() != 1 || exec.verifierCalls() != 0 {
				t.Fatalf("non-slot result became a standard answer: result=%+v err=%v agents=%v", result, err, exec.agents())
			}
			if published != 0 || failedRaw != "完整说明\n答案："+answer {
				t.Fatalf("wrong-target response was published or changed before persistence: published=%d raw=%q", published, failedRaw)
			}
			if result, err := solve.GradeVerified(t.Context(), problem, "旧路程解答\n答案："+answer, "1/2"); result != nil || !errors.Is(err, errFractionSlotAnswerTarget) || exec.graderCalls() != 0 {
				t.Fatalf("wrong frozen reference reached grader: result=%+v err=%v", result, err)
			}
		})
	}
	for _, computed := range []string{"1 千米", "0.5", "1/2；1千米"} {
		if _, comparable := verificationAnswersEqual(problem, computed, "1/2"); comparable {
			t.Fatalf("non-slot execution became fraction evidence: %q", computed)
		}
	}
	if equal, comparable := verificationAnswersEqual(problem, "1/3", "1/2"); !comparable || equal {
		t.Fatal("incorrect independently computed fraction was accepted")
	}
	if equal, comparable := verificationAnswersEqual(problem, `\(\frac{1}{2}\)`, "1/2"); !comparable || !equal {
		t.Fatal("complete canonical fraction did not preserve ordered fraction evidence")
	}
}

func TestFractionSlotHardComplexityKeepsExistingMethodPolicy(t *testing.T) {
	problem := fractionSlotEngineProblem(t, "2÷2="+strings.Repeat("1×", 61)+"2×□/□")
	if assessComplexity(problem) != complexityHard {
		t.Fatal("long fraction-slot fixture did not retain the existing hard classification")
	}
	for _, tc := range []struct {
		name           string
		explicitMethod bool
		solverCalls    int
	}{{"automatic hard", false, 2}, {"explicit single method", true, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			exec := &solveExec{solverOuts: []string{"答案：1/2", "答案：1/2"},
				verifierOut: "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: 1/2", verifierStdout: "COMPUTED: 1/2\n"}
			args := map[string]any{"problem": problem}
			if tc.explicitMethod {
				args["method_diversity"] = false
			}
			result, err := NewSolveSkill(exec.fn, nil).Execute(t.Context(), args)
			if err != nil || result == nil || result.Metadata["solve_evidence"] != "numeric_exec" {
				t.Fatalf("scoped hard question lost verification: result=%+v err=%v", result, err)
			}
			if exec.solverCalls() != tc.solverCalls || exec.verifierCalls() != 1 {
				t.Fatalf("fraction scope replaced the existing method policy: solvers=%d verifier=%d", exec.solverCalls(), exec.verifierCalls())
			}
		})
	}
}
