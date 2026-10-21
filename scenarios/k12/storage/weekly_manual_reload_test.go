package k12storage_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestWeeklyManualTextbookSurvivesDueReviewReload(t *testing.T) {
	for _, variant := range []string{"ready", "legacy_empty", "changed_source"} {
		t.Run(variant, func(t *testing.T) {
			ctx := context.Background()
			d, plan := weeklyTextbookSourceFixture(t, weeklyLessonPage)
			generated, solved := 0, 0
			var question string
			d.PracticeVariant = usecase.PracticeVariantGeneratorFunc(func(context.Context, string, string, string) (usecase.SolveResult, error) {
				generated++
				n := generated + 4
				return usecase.SolveResult{Solution: fmt.Sprintf("## 问题\n计算：1/%d×%d\n## 解答\n约分后计算。\n## 答案\n4", n, n*4)}, nil
			})
			d.Solver = weeklySavedResponseSolver{&solved, &question}
			d.WeeklyCandidates = usecase.NewWeeklyPracticeCandidateSource(&d)
			ready, _, err := d.PrepareWeeklyTextbookTrack(ctx, "mingming", plan.PlanID, plan.Revision, 5, "manual-five")
			if err != nil || len(ready.Tracks[1].Items) != 5 {
				t.Fatalf("prepare: %+v %v", ready, err)
			}
			before := ready.Tracks[1]
			if variant == "legacy_empty" {
				ready.Tracks[1].Items = nil
				ready.Tracks[1].Status = k12.WeeklyTrackDisabled
				ready.AnswerKeys = map[string]string{}
				weeklyCrossRevisionStorePlan(t, &d, ready)
			}
			for i := range 10 {
				record, err := k12.NewMistakeRecord("mingming", "reload-session", k12.MistakeFields{Subject: "数学", GradeTerm: "五年级下", Question: fmt.Sprintf("到期错题%d+2=?", i+1), CanonicalAnswer: fmt.Sprint(i + 3), KnowledgePoint: "整数计算", EntrySource: k12.MistakeEntryPhoto})
				if err != nil {
					t.Fatal(err)
				}
				due := d.Now() - 1
				record.DueAt = &due
				if _, err := d.Records.Put(ctx, record); err != nil {
					t.Fatal(err)
				}
			}
			if variant == "changed_source" {
				if _, err := d.Records.DB().Exec(`UPDATE kb_semantic_document_bindings SET content_generation=2`); err != nil {
					t.Fatal(err)
				}
			}
			result, _, err := d.EnsureWeeklyPracticePlan(ctx, usecase.EnsureWeeklyPracticePlanRequest{AgentName: "mingming", IdempotencyKey: "reload-due-changed"})
			if err != nil || generated != 5 || solved != 5 || len(result.Tracks[0].Items) != 10 {
				t.Fatalf("reload called a model or lost due questions: generated=%d solved=%d err=%v", generated, solved, err)
			}
			if variant == "changed_source" {
				if len(result.Tracks[1].Items) != 0 {
					t.Fatal("changed textbook reused stale questions")
				}
				return
			}
			if !reflect.DeepEqual(result.Tracks[1], before) {
				t.Fatalf("completed manual track lost on reload: %+v", result.Tracks[1])
			}
			for _, item := range before.Items {
				if result.AnswerKeys[item.ItemID] != "4" {
					t.Fatal("manual answer keys lost")
				}
			}
			if _, _, err := d.EnsureWeeklyPracticePlan(ctx, usecase.EnsureWeeklyPracticePlanRequest{AgentName: "mingming", IdempotencyKey: "reload-again"}); err != nil || generated != 5 || solved != 5 {
				t.Fatalf("repeated reload regenerated questions: %v", err)
			}
		})
	}
}

func TestWeeklyReloadDoesNotReplacePendingTextbookBatch(t *testing.T) {
	ctx := context.Background()
	d, plan := weeklyTextbookSourceFixture(t, weeklyLessonPage)
	generated, calls := 0, 0
	d.PracticeVariant = usecase.PracticeVariantGeneratorFunc(func(context.Context, string, string, string) (usecase.SolveResult, error) {
		generated++
		return usecase.SolveResult{Solution: "## 问题\n计算：1/5×20\n## 解答\n约分计算。\n## 答案\n4"}, nil
	})
	d.Solver = weeklyReceiptSolver{calls: &calls, unknown: true}
	d.WeeklyCandidates = usecase.NewWeeklyPracticeCandidateSource(&d)
	if _, _, err := d.PrepareWeeklyTextbookTrack(ctx, "mingming", plan.PlanID, plan.Revision, 5, "original"); err == nil {
		t.Fatal("expected unknown verifier")
	}
	before, _ := weeklyRecoveryRead(t, &d, plan)
	result, _, err := d.EnsureWeeklyPracticePlan(ctx, usecase.EnsureWeeklyPracticePlanRequest{AgentName: "mingming", IdempotencyKey: "reload-pending"})
	if err != nil || result.Revision != plan.Revision || generated != 1 || calls != 2 {
		t.Fatalf("reload changed pending batch: revision=%d generated=%d calls=%d err=%v", result.Revision, generated, calls, err)
	}
	after, _ := weeklyRecoveryRead(t, &d, plan)
	if before != after {
		t.Fatal("pending receipts rewritten")
	}
}

func TestWeeklyReloadRetainedTrackCannotOverwriteNewerRevision(t *testing.T) {
	ctx := context.Background()
	d, plan := weeklyTextbookSourceFixture(t, weeklyLessonPage)
	plan.Revision = 3
	plan.Tracks[1] = k12.WeeklyPracticeTrack{PlanSection: k12.WeeklySectionTextbookConsolidation, Status: k12.WeeklyTrackReady, Items: []k12.WeeklyPracticeItem{{ItemID: "retained", PlanSection: k12.WeeklySectionTextbookConsolidation, PromptMarkdown: "1+1=?", Verification: k12.WeeklyPracticeVerification{Status: k12.WeeklyVerificationVerified}}}}
	newer := plan
	newer.Revision++
	weeklyCrossRevisionStorePlan(t, &d, newer)
	_, before := weeklyCrossRevisionRawPlan(t, &d, plan.PlanID)
	_, _, err := d.Records.UpsertWeeklyPracticePlan(ctx, plan, "reload-stale", "new-due-source")
	if !errors.Is(err, records.ErrVersionConflict) {
		t.Fatalf("stale retained track overwrote newer revision: %v", err)
	}
	_, after := weeklyCrossRevisionRawPlan(t, &d, plan.PlanID)
	if before != after {
		t.Fatal("rejected stale reload changed plan")
	}
}
