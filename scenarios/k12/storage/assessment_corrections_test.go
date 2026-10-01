package k12storage_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/memory"
	"github.com/hexagon-codes/hexclaw/records"
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
	afterFields, err := k12.ParseMistakeFields(view.Fields)
	if err != nil || afterFields.ReviewStage != fields.ReviewStage || afterFields.LastRetriedAt != fields.LastRetriedAt || afterFields.ParentConfirmedAt != fields.ParentConfirmedAt {
		t.Fatalf("correction added learning evidence: before=%+v after=%+v err=%v", fields, afterFields, err)
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

type mixedNumberCorrectionFixture struct {
	store    *k12storage.Store
	snapshot k12.ProblemAttemptSnapshot
	original k12.GradingAssessmentItem
	artifact k12.GradingFinalArtifact
	request  k12.GradingAssessmentCorrection
	effects  k12storage.GradingAssessmentEffects
}

func mixedNumberCorrectionJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func mixedNumberCorrectionDigest(raw string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw)))
}

func mixedNumberLocalProof(t *testing.T, s *k12storage.Store, jobID string, attempt k12.Attempt, operation k12.GradingItemOperation, n int, request, result string) string {
	t.Helper()
	inv := itemInvocation(jobID, attempt, operation, n)
	inv.InvocationID = fmt.Sprintf("mixed-local-%s-%s-%d", jobID, operation, n)
	inv.ExecutionKind = k12.GradingExecutionLocalDeterministic
	inv.RequestDigest = mixedNumberCorrectionDigest(request)
	if _, _, err := s.PrepareGradingItemInvocation(t.Context(), inv); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkGradingItemInvocationSent(t.Context(), "mingming", inv.InvocationID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkGradingItemInvocationSucceeded(t.Context(), "mingming", inv.InvocationID, mixedNumberCorrectionDigest(result), result); err != nil {
		t.Fatal(err)
	}
	return inv.InvocationID
}

func seedMixedNumberCorrection(t *testing.T) mixedNumberCorrectionFixture {
	t.Helper()
	s, _ := problemAssetStore(t)
	ctx := t.Context()
	const rawAnswer = "8×\\(\\frac{1}{4}\\)×\\(\\frac{4}{5}\\)\n=2×\\(\\frac{4}{5}\\)\n=\\(\\frac{8}{5}\\)=1\\(\\frac{3}{5}\\)\n答：是1\\(\\frac{3}{5}\\)。"
	const oldAnswer = "8×1/4×4/5\n=2×4/5\n=8/5=13/5\n答：是13/5。"
	q := usecase.RecognizedQuestion{ProblemID: "q14", ProblemKind: usecase.ProblemKindStandalone,
		AttemptID: "q14-attempt", Question: "8的1/4的4/5是多少？", RawTranscription: "8的1/4的4/5是多少？",
		CanonicalMarkdown: "8的1/4的4/5是多少？", CanonicalVersion: 1, ConfirmedVersion: 1,
		Subject: "数学", AnswerState: usecase.AnswerStatePresent,
		StudentAnswer: oldAnswer, AnswerRawTranscription: rawAnswer, AnswerCanonicalMarkdown: oldAnswer}
	q = usecase.FreezeRecognizedQuestionInputDigests([]usecase.RecognizedQuestion{q})[0]
	snapshot := k12.ProblemAttemptSnapshot{Problems: []k12.Problem{{
		ProblemID: q.ProblemID, AgentName: "mingming", SubmissionID: "submission-1", PageAssetID: "mixed-page",
		Ordinal: 0, ProblemKind: k12.ProblemKindStandalone, Subject: q.Subject,
		StemRaw: q.RawTranscription, StemMarkdown: q.CanonicalMarkdown, CanonicalVersion: 1, CreatedAt: 100, UpdatedAt: 100,
	}}, Attempts: []k12.Attempt{{AttemptID: q.AttemptID, AgentName: "mingming", SubmissionID: "submission-1",
		ProblemID: q.ProblemID, AnswerState: "present", AnswerRaw: rawAnswer, AnswerMarkdown: oldAnswer,
		ConfirmedVersion: 1, InputDigest: q.InputDigest, CreatedAt: 100, UpdatedAt: 100}}}
	if err := s.PutProblemAttemptSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	job := newGradingJobRecord(t, "mingming", "mixed-number-correction")
	if _, err := s.Put(ctx, job); err != nil {
		t.Fatal(err)
	}
	attempt := snapshot.Attempts[0]
	solve := mixedNumberLocalProof(t, s, job.RecordID, attempt, k12.GradingItemOperationSolve, 1,
		q.Question, `{"solution":"8×1/4×4/5=8/5=1.6","evidence":"numeric_exec"}`)
	grade := mixedNumberLocalProof(t, s, job.RecordID, attempt, k12.GradingItemOperationGrade, 1,
		oldAnswer, `{"verdict":"disagree","final_answer_correct":false}`)
	guide := mixedNumberLocalProof(t, s, job.RecordID, attempt, k12.GradingItemOperationParentGuide, 1,
		oldAnswer, `{"answer":"1.6","likely_mistakes":["混数投影错误"]}`)
	oldItem := usecase.PhotoGradeItem{Recognized: q, Status: usecase.PhotoWrong,
		Grade: usecase.GradeResult{Solution: "8×1/4×4/5=8/5=1.6", Outcome: usecase.GradeOutcome{
			Verdict: usecase.VerdictDisagree, KnowledgePoint: "分数连乘", ErrorCause: "混数投影错误"}},
		ParentGuide: &usecase.ParentTeachingGuide{Answer: "1.6", LikelyMistakes: []string{"混数投影错误"}}}
	original := assessmentReceipt(job.RecordID, attempt, solve, grade)
	original.ParentGuideInvocationID = guide
	original.ResultJSON = mixedNumberCorrectionJSON(t, oldItem)
	original.ResultDigest = mixedNumberCorrectionDigest(original.ResultJSON)
	due := int64(86400)
	original, _, err := s.CommitGradingAssessmentItem(ctx, original, k12storage.GradingAssessmentEffects{
		Mistake: &k12storage.GradingMistakeEffect{SourceSession: "mixed-session", DueAt: &due,
			Fields: k12.MistakeFields{Subject: "数学", Question: q.Question, KnowledgePoint: "分数连乘",
				ErrorCause: "混数投影错误", CanonicalAnswer: "1.6", EntrySource: k12.MistakeEntryPhoto}}})
	if err != nil {
		t.Fatal(err)
	}
	mistake, err := s.Get(ctx, original.ProjectionRecordID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateStatusScoped(ctx, "mingming", mistake.RecordID, k12.StatusRetried, &due, mistake.Version); err != nil {
		t.Fatal(err)
	}
	mistake, err = s.Get(ctx, mistake.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := k12.ParseMistakeFields(mistake.Fields)
	if err != nil {
		t.Fatal(err)
	}
	fields.ArchivedReason, fields.ArchivedAt, fields.ArchiveCommandID = k12.MistakeArchivedReasonSourceCorrection, 300, "mixed-correction"
	fields.ArchivedFromStatus, fields.ArchivedFromDueAt, fields.ArchivedFromSpotCheckState = mistake.Status, mistake.DueAt, fields.SpotCheckState
	fields.LastArchive = &k12.MistakeArchiveSnapshot{Reason: fields.ArchivedReason, ArchivedAt: 300,
		ArchiveCommandID: "mixed-correction", FromStatus: mistake.Status, FromDueAt: mistake.DueAt, FromSpotCheckState: fields.SpotCheckState}
	fields.SpotCheckState = k12.SpotCheckNone
	effects := k12storage.GradingAssessmentEffects{Review: &k12storage.GradingReviewEffect{
		RecordID: mistake.RecordID, ExpectedVersion: mistake.Version, NewStatus: k12.StatusArchived, Fields: fields}}
	artifact := k12.GradingFinalArtifact{AgentName: "mingming", JobID: job.RecordID,
		StructureVersion: k12.GradingFinalArtifactStructureVersion, CoverageStatus: k12.GradingFinalArtifactCoverageComplete,
		TotalCount: 1, PublishedCount: 1, OrderedCurrentDigestsJSON: mixedNumberCorrectionJSON(t, []string{original.ResultDigest}),
		CanonicalMarkdown: "# 原批改结果\nQ14：13/5，错误。", SummaryInvocationID: "mixed-original-summary"}
	artifact.ArtifactDigest = k12.ComputeGradingFinalArtifactDigest(artifact)
	artifact, _, err = s.CommitGradingFinalArtifact(ctx, artifact, 0)
	if err != nil {
		t.Fatal(err)
	}
	correctItem := oldItem
	correctItem.Status, correctItem.ParentGuide = usecase.PhotoCorrect, nil
	finalAnswerCorrect := true
	correctItem.Grade.Outcome = usecase.GradeOutcome{Verdict: usecase.VerdictAgree, FinalAnswerCorrect: &finalAnswerCorrect}
	corrected := original
	corrected.Status, corrected.ParentGuideInvocationID = k12.GradingAssessmentCorrect, ""
	corrected.GradeInvocationID = mixedNumberLocalProof(t, s, job.RecordID, attempt, k12.GradingItemOperationGrade, 2,
		mixedNumberCorrectionJSON(t, map[string]string{"input_digest": q.InputDigest, "raw_answer": rawAnswer,
			"comparison_answer": "8×1/4×4/5\n=2×4/5\n=8/5=1 3/5\n答：是1 3/5。"}),
		`{"verdict":"agree","final_answer_correct":true,"solution":"8×1/4×4/5=8/5=1.6"}`)
	corrected.ResultJSON = mixedNumberCorrectionJSON(t, correctItem)
	corrected.ResultDigest = mixedNumberCorrectionDigest(corrected.ResultJSON)
	return mixedNumberCorrectionFixture{store: s, snapshot: snapshot, original: original, artifact: artifact, effects: effects,
		request: k12.GradingAssessmentCorrection{CorrectionID: "mixed-correction", OriginalResultDigest: original.ResultDigest,
			Reason: k12.AssessmentCorrectionGrading, Assessment: corrected}}
}

func TestAssessmentCorrectionMixedNumberPreservesFrozenHistoryAndWithdrawsWeakness(t *testing.T) {
	f := seedMixedNumberCorrection(t)
	ctx := t.Context()
	beforeInput, err := f.store.GetProblemAttemptSnapshot(ctx, "mingming", "submission-1")
	if err != nil {
		t.Fatal(err)
	}
	beforeCalls, err := f.store.ListGradingItemInvocations(ctx, "mingming", f.original.JobID)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := memory.New(memory.Options{Dir: filepath.Join(t.TempDir(), "memory")})
	if err != nil {
		t.Fatal(err)
	}
	consumer := usecase.InsightsConsumer{Insights: engineadapter.NewInsightsAdapter(fm), Records: f.store}
	dispatcher := k12storage.NewDispatcher(f.store, consumer)
	if err = dispatcher.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fm.ParseEntriesForRole("mingming"); len(got) != 1 {
		t.Fatalf("fixture must contain the original weakness, got %+v", got)
	}
	corrected, created, err := f.store.AppendGradingAssessmentCorrection(ctx, f.request, f.effects)
	if err != nil || !created || corrected.Assessment.Status != k12.GradingAssessmentCorrect {
		t.Fatalf("append correction: created=%v result=%+v err=%v", created, corrected, err)
	}
	if err = dispatcher.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fm.ParseEntriesForRole("mingming"); len(got) != 0 {
		t.Fatalf("archiving alone must not leave the original weakness: %+v", got)
	}
	afterInput, err := f.store.GetProblemAttemptSnapshot(ctx, "mingming", "submission-1")
	if err != nil || !reflect.DeepEqual(beforeInput, afterInput) {
		t.Fatalf("correction rewrote frozen raw/canonical/input identity: %+v %v", afterInput, err)
	}
	afterCalls, err := f.store.ListGradingItemInvocations(ctx, "mingming", f.original.JobID)
	if err != nil || !reflect.DeepEqual(beforeCalls, afterCalls) {
		t.Fatalf("correction rewrote successful local receipts: %+v %v", afterCalls, err)
	}
	for _, call := range afterCalls {
		if call.ExecutionKind != k12.GradingExecutionLocalDeterministic || call.CostReceiptID != "" || call.Status != k12.ModelInvocationSucceeded {
			t.Fatalf("local fixture must not claim a provider call: %+v", call)
		}
	}
	history, err := f.store.GetGradingAssessmentItem(ctx, "mingming", f.original.JobID, f.original.ProblemID)
	if err != nil || !reflect.DeepEqual(history, f.original) {
		t.Fatalf("original assessment was overwritten: %+v %v", history, err)
	}
	artifact, err := f.store.GetGradingFinalArtifactByJob(ctx, "mingming", f.original.JobID)
	if err != nil || !reflect.DeepEqual(artifact, f.artifact) {
		t.Fatalf("delivered final artifact changed: %+v %v", artifact, err)
	}
	effective, err := f.store.GetEffectiveGradingAssessment(ctx, "mingming", f.original.JobID, f.original.ProblemID)
	if err != nil || effective.Current.Status != k12.GradingAssessmentCorrect || effective.Current.InputDigest != f.original.InputDigest || effective.Current.InputRevision != f.original.InputRevision || effective.Current.GradeInvocationID != f.request.Assessment.GradeInvocationID || effective.Current.GradeInvocationID == f.original.GradeInvocationID || effective.Current.ParentGuideInvocationID != "" {
		t.Fatalf("effective correction lost original input binding: %+v %v", effective, err)
	}
	var result usecase.PhotoGradeItem
	if err = json.Unmarshal([]byte(effective.Current.ResultJSON), &result); err != nil || result.ParentGuide != nil || result.Recognized.AnswerRawTranscription != beforeInput.Attempts[0].AnswerRaw || result.Recognized.AnswerCanonicalMarkdown != beforeInput.Attempts[0].AnswerMarkdown || result.Grade.Solution != "8×1/4×4/5=8/5=1.6" {
		t.Fatalf("correct result must retain frozen recognition and the complete solution without stale wrong guide: %+v %v", result, err)
	}
	mistake, err := f.store.Get(ctx, f.original.ProjectionRecordID)
	if err != nil || mistake.Status != k12.StatusArchived || mistake.DueAt != nil {
		t.Fatalf("incorrect retried review was not archived: %+v %v", mistake, err)
	}
	fields, err := k12.ParseMistakeFields(mistake.Fields)
	if err != nil || fields.LastArchive == nil || fields.LastArchive.FromStatus != k12.StatusRetried || fields.ReviewStage != f.effects.Review.Fields.ReviewStage || fields.LastRetriedAt != f.effects.Review.Fields.LastRetriedAt || fields.ParentConfirmedAt != f.effects.Review.Fields.ParentConfirmedAt {
		t.Fatalf("archive fabricated learning evidence or lost the prior status: %+v %v", fields, err)
	}
}

func TestAssessmentCorrectionMixedNumberReplayAndConflicts(t *testing.T) {
	t.Run("correct_rejects_stale_parent_guide", func(t *testing.T) {
		f := seedMixedNumberCorrection(t)
		withGuide := f.request
		withGuide.Assessment.ParentGuideInvocationID = f.original.ParentGuideInvocationID
		if _, _, err := f.store.AppendGradingAssessmentCorrection(t.Context(), withGuide, f.effects); !errors.Is(err, k12.ErrGradingAssessmentTerminalInvariant) {
			t.Fatalf("correct assessment accepted a stale wrong parent guide: %v", err)
		}
		view, err := f.store.GetEffectiveGradingAssessment(t.Context(), "mingming", f.original.JobID, f.original.ProblemID)
		if err != nil || view.Correction != nil {
			t.Fatalf("rejected guide reference left a correction: %+v %v", view, err)
		}
	})
	t.Run("same_command_and_stale_predecessor", func(t *testing.T) {
		f := seedMixedNumberCorrection(t)
		first, created, err := f.store.AppendGradingAssessmentCorrection(t.Context(), f.request, f.effects)
		if err != nil || !created {
			t.Fatalf("first correction: created=%v err=%v", created, err)
		}
		before, err := f.store.Get(t.Context(), f.original.ProjectionRecordID)
		if err != nil {
			t.Fatal(err)
		}
		replay, created, err := f.store.AppendGradingAssessmentCorrection(t.Context(), f.request, f.effects)
		if err != nil || created || !reflect.DeepEqual(first, replay) {
			t.Fatalf("same command changed correction: %+v created=%v err=%v", replay, created, err)
		}
		stale := f.request
		stale.CorrectionID = "mixed-stale-predecessor"
		if _, _, err = f.store.AppendGradingAssessmentCorrection(t.Context(), stale, f.effects); !errors.Is(err, k12storage.ErrAssessmentCorrectionConflict) {
			t.Fatalf("stale predecessor was accepted: %v", err)
		}
		changed := f.request
		changed.Assessment.ResultJSON = `{"Status":"wrong"}`
		if _, _, err = f.store.AppendGradingAssessmentCorrection(t.Context(), changed, f.effects); !errors.Is(err, k12storage.ErrAssessmentCorrectionConflict) {
			t.Fatalf("same command with changed payload was accepted: %v", err)
		}
		after, err := f.store.Get(t.Context(), f.original.ProjectionRecordID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("replay/conflict changed review projection: %+v %v", after, err)
		}
		var corrections, events int
		if err = f.store.DB().QueryRow(`SELECT count(*) FROM k12_assessment_corrections WHERE job_id=?`, f.original.JobID).Scan(&corrections); err != nil {
			t.Fatal(err)
		}
		if err = f.store.DB().QueryRow(`SELECT count(*) FROM outbox_events WHERE event_type=?`, k12storage.EventAssessmentCorrected).Scan(&events); err != nil || corrections != 1 || events != 1 {
			t.Fatalf("replay duplicated correction or event: corrections=%d events=%d err=%v", corrections, events, err)
		}
	})
	t.Run("changed_mistake_version", func(t *testing.T) {
		f := seedMixedNumberCorrection(t)
		if err := f.store.UpdateStatusScoped(t.Context(), "mingming", f.original.ProjectionRecordID, k12.StatusMastered, nil, f.effects.Review.ExpectedVersion); err != nil {
			t.Fatal(err)
		}
		before, err := f.store.Get(t.Context(), f.original.ProjectionRecordID)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = f.store.AppendGradingAssessmentCorrection(t.Context(), f.request, f.effects); !errors.Is(err, records.ErrVersionConflict) {
			t.Fatalf("stale review version was accepted: %v", err)
		}
		after, err := f.store.Get(t.Context(), f.original.ProjectionRecordID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("correction overwrote a later review state: %+v %v", after, err)
		}
		view, err := f.store.GetEffectiveGradingAssessment(t.Context(), "mingming", f.original.JobID, f.original.ProblemID)
		if err != nil || view.Correction != nil || !reflect.DeepEqual(view.Original, view.Current) {
			t.Fatalf("failed CAS left a partial correction: %+v %v", view, err)
		}
	})
}
