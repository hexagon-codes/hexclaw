package usecase

import (
	"context"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type frozenSubjectWeeklyGrader struct{ subject string }

func (g *frozenSubjectWeeklyGrader) GradeVerified(_ context.Context, subject, _, _, _ string) (GradeOutcome, error) {
	g.subject = subject
	return GradeOutcome{Verdict: VerdictAgree}, nil
}

func TestWeeklyAssessorPreservesFrozenSubjectAndLegacyMath(t *testing.T) {
	for _, test := range []struct{ name, subject, expected string }{
		{"chinese", "语文", "语文"},
		{"english", "英语", "英语"},
		{"math", "数学", "数学"},
		{"legacy empty", "", "数学"},
	} {
		t.Run(test.name, func(t *testing.T) {
			grader := &frozenSubjectWeeklyGrader{}
			assessor := NewVerifiedSolutionWeeklyAssessor(grader)
			assessment, err := assessor.AssessWeeklyPracticeAnswer(t.Context(), WeeklyPracticeAnswerRequest{
				SnapshotID: "frozen-snapshot", StudentAnswer: "原作答", VerifiedSolution: "冻结参考解",
				Item: k12.WeeklyPracticeItem{ItemID: "frozen-item", Subject: test.subject, PromptMarkdown: "冻结题干"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if grader.subject != test.expected || assessment.Subject != test.expected {
				t.Fatalf("frozen subject lost: grader=%q assessment=%q expected=%q", grader.subject, assessment.Subject, test.expected)
			}
			if assessment.Result != k12.WeeklyAttemptCorrect {
				t.Fatalf("subject projection changed the verified assessment: %q", assessment.Result)
			}
		})
	}
}
