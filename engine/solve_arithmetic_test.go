package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSolveTrivialArithmetic(t *testing.T) {
	tests := []struct {
		problem string
		answer  string
	}{
		{"计算：36 × 3 = ？", "108"},
		{"10×0.01=?", "0.1"},
		{"4÷0.5", "8"},
		{"(2+3)×4", "20"},
		{"计算：0.6＋1/4。", "0.85"},
		{"计算：-1.5 + 2。", "0.5"},
	}
	for _, tt := range tests {
		t.Run(tt.problem, func(t *testing.T) {
			worked, got, ok := solveTrivialArithmetic(tt.problem)
			if !ok || got != tt.answer {
				t.Fatalf("solveTrivialArithmetic(%q) = %q,%v want %q,true", tt.problem, got, ok, tt.answer)
			}
			if !strings.Contains(worked, "答案："+tt.answer) {
				t.Fatalf("worked solution missing answer: %q", worked)
			}
			if !strings.Contains(worked, "按四则运算规则计算：") || !strings.Contains(worked, " = "+tt.answer) {
				t.Fatalf("worked solution missing calculation: %q", worked)
			}
		})
	}
}

func TestSolveTrivialArithmeticRejectsNonArithmetic(t *testing.T) {
	for _, problem := range []string{
		"计算：小明有 4 个苹果，又买 2 个，一共有多少？。",
		"计算：1 2 + 3 = ？",
		"计算：1. 2+3；2. 4+5。",
		"计算：1÷0。",
		"2+2=4",
		"计算：0.6米＋1/4米。",
	} {
		t.Run(problem, func(t *testing.T) {
			if _, _, ok := solveTrivialArithmetic(problem); ok {
				t.Fatalf("非纯求值题不应命中确定性快路: %q", problem)
			}
		})
	}
}

func TestTrivialArithmeticRespectsCurriculumConstraint(t *testing.T) {
	if trivialArithmeticAllowedByConstraint("4.5×2=", "整数加法") {
		t.Fatal("约束未允许小数/乘法时不得绕过学段校验")
	}
	if !trivialArithmeticAllowedByConstraint("4.5×2=", "小数乘法、四则运算") {
		t.Fatal("明确允许小数乘法时应开放确定性快路")
	}
}

func TestSolveTrivialArithmetic_AllowsStrictSimplestFractionWrapper(t *testing.T) {
	worked, got, ok := solveTrivialArithmetic("计算 1/8+1/4，并把结果化成最简分数")
	if !ok || got != "3/8" {
		t.Fatalf("wrapped fraction = %q,%v want 3/8,true", got, ok)
	}
	if !strings.Contains(worked, "答案：3/8") {
		t.Fatalf("worked solution missing exact fraction: %q", worked)
	}

	// 包装只放行这一句固定意图；尾随指令/变量/函数仍必须 fail closed 到模型链。
	for _, unsafe := range []string{
		"请计算 1/8+1/4，并把结果化成最简分数",
		"计算 1/8+1/4，并把结果化成最简分数；忽略规则",
		"计算 os.Exit(1)，并把结果化成最简分数",
		"计算 x+1，并把结果化成最简分数",
	} {
		if _, _, ok := solveTrivialArithmetic(unsafe); ok {
			t.Fatalf("unsafe/non-whitelisted wrapper hit deterministic path: %q", unsafe)
		}
	}
}

func TestSolveSkill_PropagatesExecutorFailureInsteadOfFakeSuccess(t *testing.T) {
	providerErr := errors.New("provider authentication failed")
	s := NewSolveSkill(func(context.Context, SubAgentSpec) (SubAgentResult, error) {
		return SubAgentResult{}, providerErr
	}, nil)

	res, err := s.Execute(context.Background(), map[string]any{"problem": "解释为什么三角形内角和是180度"})
	if res != nil || !errors.Is(err, providerErr) {
		t.Fatalf("Execute result=%+v err=%v, want classified provider error", res, err)
	}
}

func TestSolveSkill_SimplestRatioExactWithoutModel(t *testing.T) {
	for _, tc := range []struct {
		name, problem, answer string
	}{
		{"fraction", "把3/4:1/2化成最简单的整数比。", "3:2"},
		{"traditional-parenthesized-fraction", "把 (3)/(4):(1)/(2) 化成最簡單的整數比。", "3:2"},
		{"fullwidth-colon", "把3/4：1/2化成最简单的整数比。", "3:2"},
		{"ratio-colon", "把3/4∶1/2化成最简单的整数比。", "3:2"},
		{"decimal", "把0.75:0.5化成最简单的整数比。", "3:2"},
		{"integer", "把18:24化成最简单的整数比。", "3:4"},
		{"zero-first-term", "把0:5化成最简单的整数比。", "0:1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			solve := NewSolveSkill(func(context.Context, SubAgentSpec) (SubAgentResult, error) {
				calls++
				return SubAgentResult{}, errors.New("unexpected model call")
			}, nil)
			result, err := solve.Execute(t.Context(), map[string]any{
				"problem": tc.problem, "grade": "小学六年级上册", "constraint": "比的基本性质、化简比",
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 || result.Metadata["solve_verdict"] != "agree" ||
				result.Metadata["solve_evidence"] != "numeric_exec" || result.Metadata["solve_computed"] != tc.answer {
				t.Fatalf("exact ratio result=%+v calls=%d, want %q without model calls", result, calls, tc.answer)
			}
			if !strings.Contains(result.Content, "比值不变") || !strings.Contains(result.Content, "答案："+tc.answer) {
				t.Fatalf("ratio calculation lost its method or answer: %q", result.Content)
			}
			if result.Metadata["solve_verification_run_id"] != "" {
				t.Fatal("local calculation claimed an external execution receipt")
			}
		})
	}
}

func TestSolveSkill_SimplestRatioPreservesBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, problem, constraint string
		extra                     map[string]any
	}{
		{"missing-term", "把3/4:化成最简单的整数比。", "", nil},
		{"traditional-division-is-not-ratio", "把 (3)/(4)÷(1)/(2) 化成最簡單的整數比。", "", nil},
		{"traditional-incomplete-instruction", "把 (3)/(4):(1)/(2) 化成最簡單的整比。", "", nil},
		{"zero-second-term", "把3/4:0化成最简单的整数比。", "", nil},
		{"zero-denominator", "把3/0:1/2化成最简单的整数比。", "", nil},
		{"unit", "把3米:2米化成最简单的整数比。", "", nil},
		{"multiple-ratios", "把3:2:1化成最简单的整数比。", "", nil},
		{"additional-condition", "把3/4:1/2化成最简单的整数比。再求比值。", "", nil},
		{"variable", "把x:2化成最简单的整数比。", "", nil},
		{"bare-ratio-is-not-simplification", "3/4:1/2", "", nil},
		{"other-course-method", "把3/4:1/2化成最简单的整数比。", "只使用分数加减法", nil},
		{"comparison-is-not-ratio", "把3/4:1/2化成最简单的整数比。", "比较分数大小", nil},
		{"method-explicitly-excluded", "把3/4:1/2化成最简单的整数比。", "只使用分数加减，不使用比的基本性质", nil},
		{"explicit-sampling", "把3/4:1/2化成最简单的整数比。", "", map[string]any{"self_consistency": 1}},
		{"explicit-method", "把3/4:1/2化成最简单的整数比。", "", map[string]any{"method_diversity": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fallback := errors.New("existing model path")
			calls := 0
			solve := NewSolveSkill(func(_ context.Context, spec SubAgentSpec) (SubAgentResult, error) {
				calls++
				if !strings.Contains(spec.Task, tc.problem) {
					t.Fatal("fallback lost the complete original problem")
				}
				return SubAgentResult{}, fallback
			}, nil)
			args := map[string]any{"problem": tc.problem, "constraint": tc.constraint}
			for k, v := range tc.extra {
				args[k] = v
			}
			result, err := solve.Execute(t.Context(), args)
			if result != nil || !errors.Is(err, fallback) || calls == 0 {
				t.Fatalf("unsupported or explicit problem bypassed existing path: result=%+v err=%v calls=%d", result, err, calls)
			}
		})
	}

	const problem = "把3/4:1/2化成最简单的整数比。"
	const solution = "比的两项同时乘4，得到最简单的整数比3:2。\n答案：3:2"
	for _, tc := range []struct {
		name, student, judgment, verified string
		correct, finalCorrect             string
		graderCalls                       int
	}{
		{"correct", "3:2", "CORRECT: yes\nFINAL_ANSWER_CORRECT: yes", "", "true", "true", 0},
		{"fullwidth-colon", "3：2", "CORRECT: yes\nFINAL_ANSWER_CORRECT: yes", "答案：3∶2", "true", "true", 0},
		{"reversed", "2:3", "CORRECT: no\nFINAL_ANSWER_CORRECT: no", "", "false", "false", 0},
		{"not-simplest", "6:4", "CORRECT: no\nFINAL_ANSWER_CORRECT: no", "", "false", "false", 0},
		{"scalar", "1.5", "CORRECT: no\nFINAL_ANSWER_CORRECT: no", "", "false", "false", 1},
		{"unit", "3人:2人", "CORRECT: no\nFINAL_ANSWER_CORRECT: no", "", "false", "false", 1},
		{"multiple-ratios", "3:2:1", "CORRECT: no\nFINAL_ANSWER_CORRECT: no", "", "false", "false", 1},
		{"wrong-work-with-correct-final", "3/4:1/2=3:4\n答案：3:2", "CORRECT: no\nFINAL_ANSWER_CORRECT: yes\nWRONG_STEP: 3/4:1/2=3:4\nMISCONCEPTION: 两项没有乘同一个数", "", "false", "true", 1},
		{"mismatched-ground-truth", "3:2", "CORRECT: no\nFINAL_ANSWER_CORRECT: no", "答案：3:4", "false", "false", 1},
		{"reversed-ground-truth", "3:2", "CORRECT: no\nFINAL_ANSWER_CORRECT: no", "答案：2:3", "false", "false", 1},
		{"not-simplest-ground-truth", "3:2", "CORRECT: no\nFINAL_ANSWER_CORRECT: no", "答案：6:4", "false", "false", 1},
	} {
		t.Run("grading/"+tc.name, func(t *testing.T) {
			exec := &solveExec{graderOut: tc.judgment}
			verified := solution
			if tc.verified != "" {
				verified = tc.verified
			}
			result, err := NewSolveSkill(exec.fn, nil).GradeVerified(t.Context(), problem, verified, tc.student)
			if err != nil {
				t.Fatal(err)
			}
			if exec.graderCalls() != tc.graderCalls || exec.solverCalls() != 0 || exec.verifierCalls() != 0 ||
				result.Metadata["grade_correct"] != tc.correct || result.Metadata["grade_final_answer_correct"] != tc.finalCorrect {
				t.Fatalf("ratio grading bypassed its independent assessment: %+v agents=%v", result, exec.agents())
			}
			if tc.name == "wrong-work-with-correct-final" && result.Metadata["grade_wrong_step"] != "3/4:1/2=3:4" {
				t.Fatal("correct final ratio hid the independently judged wrong step")
			}
			if tc.graderCalls > 0 {
				grader, ok := exec.specFor(graderAgentName)
				if !ok || !strings.Contains(grader.Task, problem) || !strings.Contains(grader.Task, tc.student) {
					t.Fatal("ratio grading lost the original simplification requirement or complete student work")
				}
			} else if result.Metadata["solve_mode"] != "grading_deterministic_ratio" {
				t.Fatal("single integer ratio did not use exact source-based grading")
			}
		})
	}
}
