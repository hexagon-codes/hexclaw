package k12storage_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func weeklyAdoptedAIProgressFixture(t *testing.T) (usecase.Deps, k12.WeeklyPracticePlan) {
	t.Helper()
	d, _ := weeklyTextbookSourceFixture(t, weeklyLessonPage)
	ctx := context.Background()
	p, err := d.GetCurriculumProgress(ctx, "mingming", "math")
	if err != nil {
		t.Fatal(err)
	}
	state, err := d.GetProfileWithRevision(ctx, "mingming")
	if err != nil {
		t.Fatal(err)
	}
	profile := k12.ChildProfile{ChildName: state.ChildName, GradeTerm: state.GradeTerm, SubjectTextbooks: state.SubjectTextbooks, TextbookEdition: state.TextbookEdition}
	// 基础夹具带人工进度；先按原清除命令结束它，再由新请求采用建议，不越过人工优先规则。
	if _, err := d.Records.SaveCurriculumProgress(ctx, k12storage.TextbookScope{OwnerID: d.TextbookOwnerID, AgentName: "mingming", Subject: "math"}, profile, nil, p.Revision, d.Now()); err != nil {
		t.Fatal(err)
	}
	p, err = d.EnsureCurriculumProgress(ctx, d.TextbookOwnerID, "mingming")
	if err != nil || p == nil || p.EvidenceSource != "ai_estimated" || p.ConfirmedAt != 0 {
		t.Fatalf("new request estimate unavailable: progress=%+v err=%v", p, err)
	}
	d.WeeklyCandidates = usecase.NewWeeklyPracticeCandidateSource(&d)
	plan, _, err := d.EnsureWeeklyPracticePlan(ctx, usecase.EnsureWeeklyPracticePlanRequest{AgentName: "mingming", IdempotencyKey: "ai-source-plan"})
	if err != nil {
		t.Fatal(err)
	}
	return d, plan
}

func TestWeeklyAIProgress_ArithmeticUsesRealLessonAndPreservesReplay(t *testing.T) {
	d, plan := weeklyAdoptedAIProgressFixture(t)
	d.WeeklyCandidates = usecase.NewWeeklyPracticeCandidateSource(&d)
	ctx := context.Background()
	generated, solved := 0, 0
	d.PracticeVariant = usecase.PracticeVariantGeneratorFunc(func(_ context.Context, _, prompt, grade string) (usecase.SolveResult, error) {
		generated++
		if grade != "五年级下" || !strings.Contains(prompt, "分数乘整数") || !strings.Contains(prompt, "3/8×2=3/4") || !strings.Contains(prompt, "numeric arithmetic expression") {
			t.Fatalf("arithmetic source or grade missing: grade=%s prompt=%s", grade, prompt)
		}
		return usecase.SolveResult{Solution: "## 问题\n计算：1/5×20\n## 解答\n分子乘20，再约分。\n## 答案\n4"}, nil
	})
	var question string
	d.Solver = weeklySavedResponseSolver{&solved, &question}
	batch, replay, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "mingming", plan.PlanID, plan.Revision, 1, "ai-arithmetic")
	if err != nil || replay {
		t.Fatalf("AI arithmetic: batch=%+v replay=%v calls=%d/%d err=%v", batch, replay, generated, solved, err)
	}
	batch, err = d.Records.GetWeeklyArithmeticBatch(ctx, "mingming", batch.BatchID)
	if err != nil || batch.State != k12.WeeklyArithmeticReady || generated != 1 || solved != 1 || len(batch.Items) != 1 {
		t.Fatalf("stored AI arithmetic: batch=%+v calls=%d/%d err=%v", batch, generated, solved, err)
	}
	item := batch.Items[0]
	if item.SourceKind != "textbook_variant" || !strings.HasPrefix(item.SourceRef, "textbook:") || item.KnowledgePoint != "分数乘整数" {
		t.Fatalf("estimated source fabricated a target: %+v", item)
	}
	var frozen usecase.WeeklyPracticeCandidateRequest
	if err := json.Unmarshal([]byte(batch.GenerationCheckpoint), &frozen); err != nil || frozen.Progress.EvidenceSource != "ai_estimated" || frozen.Progress.ConfirmedAt != 0 || frozen.Progress.EstimateBasis == nil {
		t.Fatalf("estimated provenance not frozen: %+v err=%v", frozen.Progress, err)
	}
	before, err := d.GetCurriculumProgress(ctx, "mingming", "math")
	if err != nil {
		t.Fatal(err)
	}
	originalClock := d.Now()
	d.Now = func() int64 { return originalClock + 86400 }
	again, replay, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "mingming", plan.PlanID, plan.Revision, 1, "ai-arithmetic")
	if err != nil || !replay || generated != 1 || solved != 1 {
		t.Fatalf("frozen batch replay changed: replay=%v calls=%d/%d err=%v", replay, generated, solved, err)
	}
	again, err = d.Records.GetWeeklyArithmeticBatch(ctx, "mingming", again.BatchID)
	if err != nil || again.GenerationCheckpoint != batch.GenerationCheckpoint {
		t.Fatalf("stored frozen batch changed on replay: err=%v", err)
	}
	after, err := d.GetCurriculumProgress(ctx, "mingming", "math")
	if err != nil || after.Revision != before.Revision || after.EstimateBasis.AsOfDate != before.EstimateBasis.AsOfDate {
		t.Fatalf("replay re-estimated progress: before=%+v after=%+v err=%v", before, after, err)
	}
	var learning int
	if err := d.Records.DB().QueryRow(`SELECT (SELECT count(*) FROM k12_mistakes)+(SELECT count(*) FROM k12_attempts)+(SELECT count(*) FROM k12_mistake_review_states)+(SELECT count(*) FROM k12_weekly_arithmetic_attempts)+(SELECT count(*) FROM k12_grading_assessment_items)`).Scan(&learning); err != nil || learning != 0 {
		t.Fatalf("generation fabricated learning: count=%d err=%v", learning, err)
	}
}

func TestWeeklyAIProgress_UnknownGenerationDoesNotReadoptOrResend(t *testing.T) {
	d, plan := weeklyAdoptedAIProgressFixture(t)
	d.WeeklyCandidates = usecase.NewWeeklyPracticeCandidateSource(&d)
	ctx := context.Background()
	calls := 0
	d.PracticeVariant = usecase.PracticeVariantGeneratorFunc(func(context.Context, string, string, string) (usecase.SolveResult, error) {
		calls++
		return usecase.SolveResult{}, usecase.ErrModelInvocationRequiresReconciliation
	})
	var question string
	solves := 0
	d.Solver = weeklySavedResponseSolver{&solves, &question}
	batch, _, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "mingming", plan.PlanID, plan.Revision, 1, "unknown-ai-arithmetic")
	if err != nil {
		t.Fatal(err)
	}
	batch, err = d.Records.GetWeeklyArithmeticBatch(ctx, "mingming", batch.BatchID)
	if err != nil || batch.State != k12.WeeklyArithmeticFailedTerminal || calls != 1 || solves != 0 || !strings.Contains(batch.GenerationCheckpoint, "outcome_unknown") {
		t.Fatalf("unknown generation: batch=%+v calls=%d/%d err=%v", batch, calls, solves, err)
	}
	before, err := d.GetCurriculumProgress(ctx, "mingming", "math")
	if err != nil {
		t.Fatal(err)
	}
	originalClock := d.Now()
	d.Now = func() int64 { return originalClock + 86400 }
	_, replay, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "mingming", plan.PlanID, plan.Revision, 1, "unknown-ai-arithmetic")
	if err != nil || !replay || calls != 1 || solves != 0 {
		t.Fatalf("unknown replay resent: replay=%v calls=%d/%d err=%v", replay, calls, solves, err)
	}
	after, err := d.GetCurriculumProgress(ctx, "mingming", "math")
	stored, getErr := d.Records.GetWeeklyArithmeticBatch(ctx, "mingming", batch.BatchID)
	if err != nil || getErr != nil || after.Revision != before.Revision || after.EstimateBasis.AsOfDate != before.EstimateBasis.AsOfDate || stored.GenerationCheckpoint != batch.GenerationCheckpoint {
		t.Fatalf("unknown replay changed frozen source: before=%+v after=%+v err=%v/%v", before, after, err, getErr)
	}
}
