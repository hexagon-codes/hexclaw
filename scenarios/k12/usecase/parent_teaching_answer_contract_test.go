package usecase

import (
	"context"
	"testing"
)

type parentGuideAnswerContractSolver struct{ solution string }

func (s parentGuideAnswerContractSolver) Solve(context.Context, string, string, string) (SolveResult, error) {
	return SolveResult{Solution: s.solution, Evidence: SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceHeuristic}}, nil
}

func TestParentTeachingGuideProjectsCompleteFrozenAnswer(t *testing.T) {
	for _, group := range []struct{ name, subject, problem, answer, partial string }{
		{"all_requested_quantities", "数学", "已用4.8 TB，占总容量的2/5。求每份容量和总容量。", "每份容量2.4 TB；总容量12 TB。", "12 TB"},
		{"preserve_negative_claim", "语文", "判断原句是否使用比喻或拟人，并说明结论。", "不是比喻，是拟人。", "比喻"},
	} {
		t.Run(group.name, func(t *testing.T) {
			solution := "## 完整方法\n依据题目给定条件逐项核对，保留所有所求量及结论。\n## 答案\n" + group.answer
			for _, candidate := range []struct{ name, answer string }{{"complete_control", group.answer}, {"incomplete_or_opposite", group.partial}} {
				t.Run(candidate.name, func(t *testing.T) {
					d, _ := newPipeline(t, parentGuideAnswerContractSolver{solution: solution}, fakeGrader{}, nil)
					d.ParentTeachingGuide = &parentTeachingGuideSpy{answer: candidate.answer}
					got, err := d.SolveBlankWorksheetProblem(t.Context(), GradeRequest{AgentName: "mingming", Subject: group.subject, Grade: "五年级上", Problem: group.problem})
					if err != nil || got.Guide.Answer != group.answer || got.Solved.Solution != solution {
						t.Fatalf("frozen answer was lost or rewritten: answer=%q solution=%q err=%v", got.Guide.Answer, got.Solved.Solution, err)
					}
				})
			}
		})
	}
}
