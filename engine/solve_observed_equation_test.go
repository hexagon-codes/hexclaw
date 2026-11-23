package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
)

func TestGradeVerifiedObservedMultiLineEquations(t *testing.T) {
	const curriculum = "简易方程、解方程、分数乘法、分数除法、小数乘法、小数除法"
	const problem = "x-1/4x=18"
	const correct = "3/4x=18\nx=24"
	const reference = "先合并同类的数量，再代回原式。\n答案：x=24"
	for _, tc := range []struct {
		name, problem, ref, work, course string
		local                            bool
	}{
		{"original observed work", problem, reference, correct, curriculum, true},
		{"different coefficients", "2x+4=18", "答案：x=7", "2x=14\nx=7", curriculum, true},
		{"fraction solution", "3x=2", "答案：x=2/3", "6x=4\nx=2/3", curriculum, true},
		{"real incorrect step", problem, reference, "3/4x=16\nx=24", curriculum, false},
		{"active crossed value", problem, reference, "3/4x=18\nx=18\nx=24", curriculum, false},
		{"other variable", problem, reference, "3/4y=18\nx=24", curriculum, false},
		{"unbound reference", problem, "答案：y=24", correct, curriculum, false},
		{"unlabelled last line", problem, reference, "3/4x=18\n24", curriculum, false},
		{"identity", problem, reference, "x=x\nx=24", curriculum, false},
		{"no solution", problem, reference, "x=x+1\nx=24", curriculum, false},
		{"nonlinear", problem, reference, "x*x=576\nx=24", curriculum, false},
		{"hidden domain", problem, reference, "x/x=1\nx=24", curriculum, false},
		{"mixed coefficient interpretation", problem, reference, "1 1/3x=9\nx=24", curriculum, false},
		{"unknown math command", problem, reference, `\rightx=24` + "\nx=24", curriculum, false},
		{"additional claim", problem, reference, "如果x≠0，则3/4x=18\nx=24", curriculum, false},
		{"topic excluded", problem, reference, correct, "四则运算，未学简易方程", false},
		{"old frozen input", problem, reference, correct, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			fallback := errors.New("existing grading operation")
			skill := NewSolveSkill(func(_ context.Context, spec SubAgentSpec) (SubAgentResult, error) {
				calls++
				if !strings.Contains(spec.Task, tc.work) {
					t.Fatalf("grader did not receive all active working: %s", tc.work)
				}
				if tc.name == "real incorrect step" {
					return SubAgentResult{Output: "CORRECT: no\nFINAL_ANSWER_CORRECT: yes\nWRONG_STEP: 3/4x=16\nWRONG_STEP_SOURCE: 3/4x=16\nMISCONCEPTION: 等式右边从18变成了16。\nGUIDANCE: 合并未知数后右边仍是18。"}, nil
				}
				return SubAgentResult{}, fallback
			}, nil)
			ctx := elementarymath.WithObservedEquationCurriculum(t.Context(), tc.course)
			result, err := skill.GradeVerifiedForSubject(ctx, "数学", tc.problem, tc.ref, tc.work)
			if tc.name == "real incorrect step" {
				if calls != 1 || err != nil || result == nil || result.Metadata["grade_correct"] != "false" || result.Metadata["grade_final_answer_correct"] != "true" {
					t.Fatalf("an incorrect active step was treated as fully correct: %+v %v", result, err)
				}
				prior, priorErr := skill.GradeVerifiedForSubject(t.Context(), "数学", tc.problem, tc.ref, tc.work)
				if priorErr != nil || prior == nil || prior.Metadata["grade_verdict"] != result.Metadata["grade_verdict"] || prior.Metadata["grade_wrong_step"] != result.Metadata["grade_wrong_step"] {
					t.Fatalf("existing grader judgment changed: %+v %v", prior, priorErr)
				}
				return
			}
			if !tc.local {
				if calls != 1 || !errors.Is(err, fallback) {
					t.Fatalf("grade fallback: calls=%d err=%v", calls, err)
				}
				return
			}
			if calls != 0 || err != nil || result == nil || result.Metadata["grade_correct"] != "true" || result.Metadata["grade_final_answer_correct"] != "true" || result.Metadata["solve_evidence"] != "numeric_exec" {
				t.Fatalf("grade=%+v calls=%d err=%v", result, calls, err)
			}
		})
	}
}
