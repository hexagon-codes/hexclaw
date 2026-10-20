package usecase_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type manualBudgetCandidates struct{ calls int }

func (s *manualBudgetCandidates) GenerateWeeklyPracticeCandidates(_ context.Context, req usecase.WeeklyPracticeCandidateRequest) ([]usecase.WeeklyPracticeCandidate, error) {
	s.calls++
	var items []usecase.WeeklyPracticeCandidate
	for i := range req.MaxItems {
		items = append(items, usecase.WeeklyPracticeCandidate{SourceKind: "curriculum_rule", GenerationMethod: k12.WeeklyGenerationMethodRuleGenerated,
			SourceRef: fmt.Sprintf("manual-budget:%d", i), PromptMarkdown: fmt.Sprintf("%d+1=?", i+1), ExpectedAnswer: fmt.Sprint(i + 2),
			EvidenceRefs: []string{"manual-budget:verified"}, EstimatedSeconds: 30})
	}
	return items, nil
}

func TestWeeklyManualBudget_SelectedCountIndependentOfDueReview(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual five with ten due", true: "automatic budget unchanged"}[automatic], func(t *testing.T) {
			ctx := context.Background()
			d := newDataDeps(t, "xiaoming")
			d.Now = func() int64 { return 1785081600 }
			configureWeeklyBundle(t, &d, automatic)
			for i := range 10 {
				record, err := k12.NewMistakeRecord("xiaoming", "budget-session", k12.MistakeFields{Subject: "数学", GradeTerm: "五年级下",
					Question: fmt.Sprintf("到期错题：%d+2=?", i+1), CanonicalAnswer: fmt.Sprint(i + 3), KnowledgePoint: "整数计算", EntrySource: k12.MistakeEntryPhoto})
				if err != nil {
					t.Fatal(err)
				}
				due := d.Now() - 1
				record.DueAt = &due
				if _, err := d.Records.Put(ctx, record); err != nil {
					t.Fatal(err)
				}
			}
			source := &manualBudgetCandidates{}
			d.WeeklyCandidates = source
			plan, _, err := d.EnsureWeeklyPracticePlan(ctx, usecase.EnsureWeeklyPracticePlanRequest{AgentName: "xiaoming", IdempotencyKey: "budget-plan"})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Tracks[0].Items) != 10 {
				t.Fatalf("due-review fixture lost original questions: %d", len(plan.Tracks[0].Items))
			}
			if automatic {
				if len(plan.Tracks[1].Items) != 0 {
					t.Fatal("automatic weekly budget expanded")
				}
				return
			}
			ready, replay, err := d.PrepareWeeklyTextbookTrack(ctx, "xiaoming", plan.PlanID, plan.Revision, 5, "manual-five")
			if err != nil || replay || len(ready.Tracks[0].Items) != 10 || len(ready.Tracks[1].Items) != 5 || ready.Tracks[1].Status != k12.WeeklyTrackReady {
				t.Fatalf("manual selected count was clipped: err=%v replay=%v plan=%+v", err, replay, ready)
			}
			if _, replay, err := d.PrepareWeeklyTextbookTrack(ctx, "xiaoming", plan.PlanID, plan.Revision, 5, "manual-five"); err != nil || !replay || source.calls != 1 {
				t.Fatalf("replay regenerated: err=%v replay=%v calls=%d", err, replay, source.calls)
			}
		})
	}
}
