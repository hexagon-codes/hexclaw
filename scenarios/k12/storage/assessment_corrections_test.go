package k12storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/memory"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func correctionProof(t *testing.T, s *k12storage.Store, jobID string, attempt k12.Attempt, operation k12.GradingItemOperation, n int, succeeded bool) string {
	t.Helper()
	ctx := context.Background()
	inv := itemInvocation(jobID, attempt, operation, n)
	inv.InvocationID = fmt.Sprintf("correction-%s-%s-%d", jobID, operation, n)
	if _, _, err := s.PrepareGradingItemInvocation(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkGradingItemInvocationSent(ctx, "mingming", inv.InvocationID); err != nil {
		t.Fatal(err)
	}
	if succeeded {
		if _, err := s.MarkGradingItemInvocationSucceeded(ctx, "mingming", inv.InvocationID, "sha256:correction-proof", `{"ok":true}`); err != nil {
			t.Fatal(err)
		}
	}
	return inv.InvocationID
}

func TestAssessmentCorrectionPreservesHistoryAndReplaysInsights(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	job, attempt := seedItemLedgerFacts(t, s, "correction-flow")
	solve, grade := successfulAssessmentInvocations(t, s, job.RecordID, attempt)
	original := assessmentReceipt(job.RecordID, attempt, solve, grade)
	original.Status = k12.GradingAssessmentCorrect
	original.ResultJSON = `{"Status":"correct"}`
	stored, _, err := s.CommitGradingAssessmentItem(ctx, original, k12storage.GradingAssessmentEffects{})
	if err != nil {
		t.Fatal(err)
	}
	corrected := stored
	corrected.Status = k12.GradingAssessmentWrong
	corrected.GradeInvocationID = correctionProof(t, s, job.RecordID, attempt, k12.GradingItemOperationGrade, 2, true)
	corrected.ParentGuideInvocationID = correctionProof(t, s, job.RecordID, attempt, k12.GradingItemOperationParentGuide, 1, true)
	raw, _ := json.Marshal(usecase.PhotoGradeItem{Status: usecase.PhotoWrong, Grade: usecase.GradeResult{Outcome: usecase.GradeOutcome{KnowledgePoint: "加法", ErrorCause: "漏算个位"}}})
	corrected.ResultJSON = string(raw)
	corrected.ResultDigest = "sha256:corrected-wrong"
	request := k12.GradingAssessmentCorrection{CorrectionID: "correction-1", OriginalResultDigest: stored.ResultDigest, Reason: k12.AssessmentCorrectionGrading, Assessment: corrected}
	due := int64(86400)
	effects := k12storage.GradingAssessmentEffects{Mistake: &k12storage.GradingMistakeEffect{SourceSession: "session", DueAt: &due, Fields: k12.MistakeFields{Subject: "数学", Question: "1+1=?", KnowledgePoint: "加法", ErrorCause: "漏算个位", CanonicalAnswer: "2", EntrySource: k12.MistakeEntryPhoto}}}
	first, created, err := s.AppendGradingAssessmentCorrection(ctx, request, effects)
	if err != nil || !created {
		t.Fatalf("append: %v %v", created, err)
	}
	replay, created, err := s.AppendGradingAssessmentCorrection(ctx, request, effects)
	if err != nil || created || !reflect.DeepEqual(first, replay) {
		t.Fatalf("replay: %+v %v %v", replay, created, err)
	}
	history, err := s.GetGradingAssessmentItem(ctx, "mingming", job.RecordID, attempt.ProblemID)
	if err != nil || !reflect.DeepEqual(stored, history) {
		t.Fatalf("historical assessment changed: %+v %v", history, err)
	}
	effective, err := s.GetEffectiveGradingAssessment(ctx, "mingming", job.RecordID, attempt.ProblemID)
	if err != nil || effective.Current.Status != k12.GradingAssessmentWrong {
		t.Fatalf("effective: %+v %v", effective, err)
	}

	dir := filepath.Join(t.TempDir(), "memory")
	fm, err := memory.New(memory.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	consumer := usecase.InsightsConsumer{Insights: engineadapter.NewInsightsAdapter(fm), Records: s}
	events, err := k12storage.PendingEvents(ctx, s.DB(), 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if err = consumer.Handle(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	entries := fm.ParseEntriesForRole("mingming")
	if len(entries) != 1 || entries[0].Content != "[学情] 在「加法」出错：漏算个位" {
		t.Fatalf("duplicate or missing effective insight: %+v", entries)
	}

	second := first.Assessment
	second.Status = k12.GradingAssessmentCorrect
	second.ParentGuideInvocationID = ""
	second.GradeInvocationID = correctionProof(t, s, job.RecordID, attempt, k12.GradingItemOperationGrade, 3, true)
	second.ResultJSON = `{"Status":"correct"}`
	second.ResultDigest = "sha256:corrected-right"
	newRequest := k12.GradingAssessmentCorrection{CorrectionID: "correction-2", PreviousCorrectionID: first.CorrectionID, OriginalResultDigest: stored.ResultDigest, Reason: k12.AssessmentCorrectionGrading, Assessment: second}
	mistake, err := s.Get(ctx, first.Assessment.ProjectionRecordID)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := k12.ParseMistakeFields(mistake.Fields)
	if err != nil {
		t.Fatal(err)
	}
	review := k12storage.GradingAssessmentEffects{Review: &k12storage.GradingReviewEffect{RecordID: mistake.RecordID, ExpectedVersion: mistake.Version, NewStatus: k12.StatusArchived, Fields: fields}}
	if _, _, err = s.AppendGradingAssessmentCorrection(ctx, newRequest, review); err != nil {
		t.Fatal(err)
	}
	view, err := s.Get(ctx, mistake.RecordID)
	if err != nil || view.Status != k12.StatusArchived || view.DueAt != nil {
		t.Fatalf("future review not withdrawn: %+v %v", view, err)
	}
	// 在文件写入和消费标记之间重新装配，再逆序重放早期事件。
	fm, err = memory.New(memory.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	consumer = usecase.InsightsConsumer{Insights: engineadapter.NewInsightsAdapter(fm), Records: s}
	events, err = k12storage.PendingEvents(ctx, s.DB(), 20)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if err = consumer.Handle(ctx, events[i]); err != nil {
			t.Fatal(err)
		}
	}
	if got := fm.ParseEntriesForRole("mingming"); len(got) != 0 {
		t.Fatalf("late events restored withdrawn weakness: %+v", got)
	}
	stale := request
	stale.CorrectionID = "stale-correction"
	if _, _, err = s.AppendGradingAssessmentCorrection(ctx, stale, effects); !errors.Is(err, k12storage.ErrAssessmentCorrectionConflict) {
		t.Fatalf("stale predecessor accepted: %v", err)
	}
	if err = k12storage.NewDispatcher(s, consumer).ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fm.ParseEntriesForRole("mingming"); len(got) != 0 {
		t.Fatal("dispatcher replay restored old insight")
	}
}

func TestAssessmentCorrectionRequiresNewSuccessfulProof(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	job, attempt := seedItemLedgerFacts(t, s, "correction-proof")
	solve, grade := successfulAssessmentInvocations(t, s, job.RecordID, attempt)
	item := assessmentReceipt(job.RecordID, attempt, solve, grade)
	item.Status = k12.GradingAssessmentCorrect
	original, _, err := s.CommitGradingAssessmentItem(ctx, item, k12storage.GradingAssessmentEffects{})
	if err != nil {
		t.Fatal(err)
	}
	req := k12.GradingAssessmentCorrection{CorrectionID: "new-proof", OriginalResultDigest: original.ResultDigest, Reason: k12.AssessmentCorrectionGrading, Assessment: original}
	if _, _, err = s.AppendGradingAssessmentCorrection(ctx, req, k12storage.GradingAssessmentEffects{}); err == nil {
		t.Fatal("reused old grading proof")
	}
	req.Assessment.GradeInvocationID = correctionProof(t, s, job.RecordID, attempt, k12.GradingItemOperationGrade, 2, false)
	if _, _, err = s.AppendGradingAssessmentCorrection(ctx, req, k12storage.GradingAssessmentEffects{}); !errors.Is(err, k12storage.ErrAssessmentCorrectionConflict) {
		t.Fatalf("in-flight proof accepted: %v", err)
	}
	var count int
	if err = s.DB().QueryRow(`SELECT count(*) FROM k12_assessment_corrections`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed correction persisted: %d %v", count, err)
	}
	history, err := s.GetGradingAssessmentItem(ctx, "mingming", job.RecordID, attempt.ProblemID)
	if err != nil || !reflect.DeepEqual(original, history) {
		t.Fatal("failed correction changed original")
	}
}

func TestAssessmentCorrectionKeepsOtherAttemptReview(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	var items []k12.GradingAssessmentItem
	var attempts []k12.Attempt
	due := int64(86400)
	for i := 0; i < 2; i++ {
		job, attempt := seedItemLedgerFacts(t, s, fmt.Sprintf("independent-%d", i))
		solve, grade := successfulAssessmentInvocations(t, s, job.RecordID, attempt)
		item := assessmentReceipt(job.RecordID, attempt, solve, grade)
		item.ParentGuideInvocationID = correctionProof(t, s, job.RecordID, attempt, k12.GradingItemOperationParentGuide, 1, true)
		stored, _, err := s.CommitGradingAssessmentItem(ctx, item, k12storage.GradingAssessmentEffects{Mistake: &k12storage.GradingMistakeEffect{
			SourceSession: "same-homework", DueAt: &due, Fields: k12.MistakeFields{Subject: "数学", Question: "2+2=?", KnowledgePoint: "加法", ErrorCause: "计算失误", EntrySource: k12.MistakeEntryPhoto},
		}})
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, stored)
		attempts = append(attempts, attempt)
	}
	if items[0].ProjectionRecordID != items[1].ProjectionRecordID {
		t.Fatal("fixture did not share the deduplicated mistake")
	}
	before, err := s.Get(ctx, items[0].ProjectionRecordID)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := k12.ParseMistakeFields(before.Fields)
	if err != nil {
		t.Fatal(err)
	}
	corrected := items[0]
	corrected.Status = k12.GradingAssessmentCorrect
	corrected.ParentGuideInvocationID = ""
	corrected.GradeInvocationID = correctionProof(t, s, corrected.JobID, attempts[0], k12.GradingItemOperationGrade, 2, true)
	corrected.ResultJSON = `{"Status":"correct"}`
	corrected.ResultDigest = "sha256:independent-correction"
	_, _, err = s.AppendGradingAssessmentCorrection(ctx, k12.GradingAssessmentCorrection{CorrectionID: "withdraw-one-attempt", OriginalResultDigest: items[0].ResultDigest, Reason: k12.AssessmentCorrectionGrading, Assessment: corrected},
		k12storage.GradingAssessmentEffects{Review: &k12storage.GradingReviewEffect{RecordID: before.RecordID, ExpectedVersion: before.Version, NewStatus: k12.StatusArchived, Fields: fields}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(ctx, before.RecordID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("independent review removed or mutated: %+v %v", after, err)
	}
}
