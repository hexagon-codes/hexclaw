package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type finalAnswerAssessmentGrader struct {
	calls   int
	outcome *GradeOutcome
}

func (*finalAnswerAssessmentGrader) VerifyIncorrectArithmeticFinalAnswer(problem, final string) (string, bool) {
	return "7/9×3/14=1/6\n答案：1/6", problem == "7/9×3/14=" && final == "1/3"
}
func (g *finalAnswerAssessmentGrader) Grade(ctx context.Context, problem, answer, solution string) (GradeOutcome, error) {
	return g.GradeVerified(ctx, "数学", problem, answer, solution)
}
func (g *finalAnswerAssessmentGrader) GradeVerified(_ context.Context, subject, problem, answer, solution string) (GradeOutcome, error) {
	g.calls++
	if subject != "数学" || problem != "7/9×3/14=" || answer != "1/3" || solution != "7/9×3/14=1/6\n答案：1/6" {
		return GradeOutcome{}, fmt.Errorf("unexpected final-answer execution input")
	}
	if g.outcome != nil {
		return *g.outcome, nil
	}
	wrong := false
	return GradeOutcome{Verdict: VerdictDisagree, FinalAnswerCorrect: &wrong, ErrorCause: "最终答案与精确计算结果不一致", KnowledgePoint: "分数乘法"}, nil
}
func (*finalAnswerAssessmentGrader) UsesGradingPhysicalCalls() bool { return true }

// 通过真实 SQLite 的首读、复核、裁决和最终化构造证据，后续用例只消费已提交的回执。
func finalAnswerAssessmentFixture(t *testing.T, finalAnswers ...string) (*GradingOrchestrator, GradingJobView, PhotoGradeRequest, RecognizedQuestion, *finalAnswerAssessmentGrader) {
	return finalAnswerAssessmentSourceFixture(t, false, finalAnswers...)
}

func finalAnswerAssessmentSourceFixture(t *testing.T, sameReadings bool, finalAnswers ...string) (*GradingOrchestrator, GradingJobView, PhotoGradeRequest, RecognizedQuestion, *finalAnswerAssessmentGrader) {
	t.Helper()
	finalAnswer := "1/3"
	if len(finalAnswers) != 0 {
		finalAnswer = finalAnswers[0]
	}
	ctx := t.Context()
	grader := &finalAnswerAssessmentGrader{}
	deps, _ := newPipeline(t, fakeSolver{}, grader, nil)
	deps.Now = time.Now().Unix
	deps.VerifiedGrader = grader
	deps.ParentTeachingGuide = &parentTeachingGuideSpy{}
	deps.GradingBudgetSnapshot = recognitionLayoutInitialV2Budget()
	route := k12.GradingModelSnapshot{Provider: "school-provider", Model: "school-reader", Route: "school-provider/school-reader", Capability: "vision"}
	o := trackGradingOrchestrator(t, NewGradingOrchestrator(deps, func(k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) { return route, nil }, WithGradingRunDir(t.TempDir())))
	canonicalPage, canonicalErr := k12.CanonicalizeRecognitionPageV2(recognitionLayoutInitialV2PagePNG(t))
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	page := canonicalPage.PNG
	req := PhotoGradeRequest{AgentName: "mingming", Grade: "六年级上", SourceSession: "final-answer-session", Image: page, TaskIntent: PhotoTaskCompletedHomework}
	job, created, err := o.StartPhotoGradingJob(ctx, StartPhotoGradingInput{Photo: req, SourceKind: "desktop", SourceKey: "final-answer-source"})
	if err != nil || !created {
		t.Fatalf("start: %v", err)
	}
	run := o.lookup(job.Record.RecordID)
	req = run.req
	req.SourceUncertaintyFinalized = true
	for _, in := range []AdvanceGradingInput{{Outcome: GradingOutcomeOK}, {Outcome: GradingOutcomeOK, ArtifactDigest: "normalized:fixture"}} {
		job, err = deps.AdvanceGradingStage(ctx, req.AgentName, job.Record.RecordID, in)
		if err != nil {
			t.Fatal(err)
		}
	}
	parent, err := o.beginRecognizingLayoutModelInvocationV2(ctx, job, page, recognizingInvocationDigest(page, route, k12.ModelRequestPolicySnapshot{}, req.InitialReadMode), k12.ModelRequestPolicySnapshot{}, initialRecognitionLayoutContractV2{InitialReadMode: req.InitialReadMode, ExecutionPolicy: req.RecognitionExecutionPolicy})
	if err != nil {
		t.Fatalf("begin recognition: %v deadline=%d parentCreated=%d budget=%+v", err, job.Fields.Deadline, parent.CreatedAt, job.Fields.BudgetSnapshot)
	}
	runtime, err := deps.Records.LoadRecognitionLayoutPlanRuntimeV2(ctx, req.AgentName, parent.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	region := k12.SourcePixelRegion{X: 20, Y: 20, Width: 70, Height: 70}
	observation := func(answer string) map[string]any {
		return map[string]any{"question": "7/9×3/14=", "subject": "数学", "answer_state": "present", "student_answer": answer, "recognition_confidence": 0.99, "ocr_signals": []string{}, "answer_bbox": map[string]int{"x": 30, "y": 30, "width": 10, "height": 10}}
	}
	primaryAnswer, reviewAnswer := "1/3", "2/4=1/3"
	signals := []string{"evidence_conflict"}
	if sameReadings {
		primaryAnswer, reviewAnswer = "=1/4=1/3", "=1/4=1/3"
		signals = []string{"手写的第一个分数上有斜划线，是否将该分数整体划去不明确；暂保留其可读内容。"}
	}
	entry := map[string]any{"manifest_ref": "manifest_0001", "manifest_order": 1, "source_number_path": []string{"3"}, "display_label": "3.", "source_section_path": []string{}, "source_section_label": "", "region": region, "initial_read": map[string]any{"kind": "question", "recognition": observation(primaryAnswer), "shared_conditions": "", "answer_ownership": "visible final answer"}}
	wholeRaw, _ := json.Marshal(map[string]any{"targets": []any{entry}})
	if _, claimed, err := deps.Records.ClaimModelPhysicalInvocationSent(ctx, req.AgentName, runtime.ManifestPhysicalInvocationID); err != nil || !claimed {
		t.Fatalf("whole claim: %v", err)
	}
	whole, err := deps.Records.MarkModelPhysicalInvocationSucceededWithContent(ctx, req.AgentName, runtime.ManifestPhysicalInvocationID, string(wholeRaw), "")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{PagePNG: page, Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: whole.PhysicalInvocationID, ResultDigest: whole.ResultDigest}, Targets: []k12.RecognitionLayoutManifestTargetV2{{ManifestRef: "manifest_0001", ManifestOrder: 1, SourceNumberPath: []string{"3"}, DisplayLabel: "3.", Region: region}}, RecognitionFormat: k12.RecognitionLayoutCompactV4, InitialReadMode: req.InitialReadMode, EnableSourceAdjudication: true})
	if err != nil {
		t.Fatal(err)
	}
	target := plan.Targets[0]
	entries, err := k12.CanonicalRecognitionLayoutInitialReadEntriesV1(string(wholeRaw))
	if err != nil {
		t.Fatal(err)
	}
	first := observation(primaryAnswer)
	first["answer_bbox"] = map[string]int{"x": 30 - target.Region.X, "y": 30 - target.Region.Y, "width": 10, "height": 10}
	firstRaw, _ := json.Marshal(first)
	initial, _, err := deps.Records.AuthorizeAndSettleRecognitionLayoutInitialReadV1(ctx, req.AgentName, parent.InvocationID, plan, k12.RecognitionLayoutInitialReadSettlementV1{PlanDigest: plan.AuthorizedPlanDigest, SourcePhysicalInvocationID: whole.PhysicalInvocationID, SourcePhysicalUnit: whole.PhysicalUnit, SourcePhysicalResultDigest: whole.ResultDigest, Classification: k12.RecognitionLayoutBatchClassifiedV2, Candidates: []k12.RecognitionLayoutInitialCandidateV1{{CandidateID: target.TargetID, Classification: k12.RecognitionLayoutCandidateReviewRequiredV2, ResultKind: k12.RecognitionLayoutCandidateQuestionV2, FirstReadJSON: entries[0], ResultJSON: firstRaw}}})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{target.TargetID}
	image, err := k12.BuildRecognitionLayoutReviewBatchImageV1(page, plan, ids)
	if err != nil {
		t.Fatal(err)
	}
	size, err := png.DecodeConfig(bytes.NewReader(image))
	if err != nil {
		t.Fatal(err)
	}
	reviewRequest := k12.RecognitionLayoutReviewBatchAuthorizationRequestV1{PlanDigest: plan.AuthorizedPlanDigest, PhysicalUnit: "layout_review_batch_0001", Members: initial.ReviewAuthorizations, ImageDigest: modelInvocationDigest(image), ImageWidth: size.Width, ImageHeight: size.Height, PromptDigest: modelInvocationDigest([]byte("independent source")), RecognitionFormat: plan.RecognitionFormat}
	reviewRequest.InputDigest, err = k12.RecognitionLayoutReviewBatchInputDigestV1(reviewRequest)
	if err != nil {
		t.Fatal(err)
	}
	auth, _, err := deps.Records.AuthorizeRecognitionLayoutReviewBatchV1(ctx, req.AgentName, parent.InvocationID, reviewRequest)
	if err != nil {
		t.Fatal(err)
	}
	physical := func(unit k12.RecognitionPhysicalUnit, image []byte, raw string) k12.ModelPhysicalInvocation {
		call := k12.RecognitionPhysicalCall{Unit: unit, Image: image, TargetIDs: ids, PlanVersion: k12.RecognitionPlanVersionV2, PlanDigest: plan.AuthorizedPlanDigest}
		id, e := stableRecognitionPhysicalInvocationIDForCall(parent.InvocationID, call)
		if e != nil {
			t.Fatal(e)
		}
		digest, e := recognizingPhysicalInvocationDigest(parent, call)
		if e != nil {
			t.Fatal(e)
		}
		exact, e := k12.RecognitionLayoutTargetExactSetDigestV2(ids)
		if e != nil {
			t.Fatal(e)
		}
		child, _, e := deps.Records.PrepareModelPhysicalInvocation(ctx, k12.ModelPhysicalInvocation{PhysicalInvocationID: id, ParentInvocationID: parent.InvocationID, AgentName: req.AgentName, JobID: job.Record.RecordID, Stage: k12.GradingStageRecognizing, PhysicalUnit: unit, RecognitionPlanVersion: k12.RecognitionPlanVersionV2, PlanDigest: plan.AuthorizedPlanDigest, CandidateExactSetDigest: exact, RequestDigest: digest, RouteSnapshot: parent.RouteSnapshot, RequestPolicySnapshot: parent.RequestPolicySnapshot, Attempt: 1, CreatedAt: deps.now(), UpdatedAt: deps.now()})
		if e != nil {
			t.Fatal(e)
		}
		if _, claimed, e := deps.Records.ClaimModelPhysicalInvocationSent(ctx, req.AgentName, child.PhysicalInvocationID); e != nil || !claimed {
			t.Fatalf("child claim: %v", e)
		}
		child, e = deps.Records.MarkModelPhysicalInvocationSucceededWithContent(ctx, req.AgentName, child.PhysicalInvocationID, raw, "")
		if e != nil {
			t.Fatal(e)
		}
		return child
	}
	second := observation(reviewAnswer)
	second["answer_bbox"] = map[string]int{"x": 10, "y": 10, "width": 10, "height": 10}
	secondRaw, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"target_id": "t1", "kind": "question", "recognition": second}}})
	review := physical(auth.PhysicalUnit, image, string(secondRaw))
	second["evidence_transcriptions"] = []string{"7/9×3/14=", "7/9×3/14="}
	second["answer_evidence_transcriptions"] = []string{primaryAnswer, reviewAnswer}
	second["ocr_signals"] = signals
	candidateRaw, _ := json.Marshal(second)
	_, _, err = deps.Records.SettleRecognitionLayoutReviewBatchV1(ctx, req.AgentName, parent.InvocationID, k12.RecognitionLayoutReviewBatchSettlementV1{PlanDigest: plan.AuthorizedPlanDigest, AuthorizationID: auth.AuthorizationID, AuthorizationDigest: auth.AuthorizationDigest, SourcePhysicalInvocationID: review.PhysicalInvocationID, SourcePhysicalUnit: review.PhysicalUnit, SourcePhysicalResultDigest: review.ResultDigest, Classification: k12.RecognitionLayoutBatchClassifiedV2, Candidates: []k12.RecognitionLayoutCandidateSettlementV2{{CandidateID: target.TargetID, Classification: k12.RecognitionLayoutCandidateValidV2, ResultKind: k12.RecognitionLayoutCandidateQuestionV2, ResultJSON: candidateRaw}}})
	if err != nil {
		t.Fatal(err)
	}
	conflictKind := "answer"
	if sameReadings {
		conflictKind = "observation_uncertainty"
	}
	adjudication, _, err := deps.Records.AuthorizeRecognitionLayoutAdjudicationV2(ctx, req.AgentName, parent.InvocationID, k12.RecognitionLayoutAdjudicationRequestV2{PlanDigest: plan.AuthorizedPlanDigest, CandidateID: target.TargetID, PrimaryPhysicalInvocationID: whole.PhysicalInvocationID, PrimaryPhysicalResultDigest: whole.ResultDigest, RepairPhysicalInvocationID: review.PhysicalInvocationID, RepairPhysicalResultDigest: review.ResultDigest, ConflictKind: conflictKind})
	if err != nil {
		t.Fatal(err)
	}
	adjudicationRaw, _ := json.Marshal(map[string]any{"verification": map[string]bool{"target_ownership_confirmed": true, "active_answer_complete": false, "question_complete_and_legible": true}, "items": []any{map[string]any{"target_id": "t1", "kind": "question", "recognition": observation(finalAnswer)}}, "final_answer": map[string]any{"raw_transcription": finalAnswer, "bbox": map[string]int{"x": 30, "y": 30, "width": 10, "height": 10}, "is_final_answer": true, "target_ownership_confirmed": true, "not_cancelled": true, "complete_and_legible": true}})
	adjudicationImage, err := k12.BuildRecognitionLayoutAdjudicationImageV2(page, plan, target.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	child := physical(adjudication.PhysicalUnit, adjudicationImage, string(adjudicationRaw))
	parsed, err := k12.ParseRecognitionFinalAnswerResponseV1(string(adjudicationRaw), *target.AdjudicationRegion)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = deps.Records.SettleRecognitionLayoutAdjudicationV2(ctx, req.AgentName, parent.InvocationID, k12.RecognitionLayoutAdjudicationSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, AuthorizationID: adjudication.AuthorizationID, AuthorizationDigest: adjudication.AuthorizationDigest, CandidateID: target.TargetID, SourcePhysicalInvocationID: child.PhysicalInvocationID, SourcePhysicalUnit: child.PhysicalUnit, SourcePhysicalResultDigest: child.ResultDigest, FinalAnswerEvidence: parsed.Evidence})
	if err != nil {
		t.Fatal(err)
	}
	final, _, err := deps.Records.FinalizeRecognitionLayoutPlanV2(ctx, req.AgentName, parent.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	q := RecognizedQuestion{Question: "7/9×3/14=", StudentAnswer: reviewAnswer, Subject: "数学", AnswerState: AnswerStatePresent, ProblemKind: ProblemKindStandalone, SourceNumberPath: []string{"3"}, DisplayLabel: "3.", SourceRegion: &target.Region, SourceWidth: 800, SourceHeight: 1200, RecognitionConfidence: float64Pointer(0.99), OCRSignals: signals, AnswerEvidenceTranscriptions: []string{primaryAnswer, reviewAnswer}, EvidenceTranscriptions: []string{"7/9×3/14=", "7/9×3/14="}, FinalAnswerReceipt: final.CandidateResults[0].FinalAnswerReceipt}
	questions, err := NormalizeRecognizedProblemsForInitialReadMode(job.Fields.SubmissionID, []RecognizedQuestion{q}, req.InitialReadMode)
	if err != nil {
		t.Fatal(err)
	}
	questions[0].ConfirmedVersion = 1
	questions = FreezeRecognizedQuestionInputDigests(questions, req.Grade)
	q = questions[0]
	run.req = req
	run.questions = questions
	if err := o.persistProblemAttemptFacts(ctx, req.AgentName, job.Fields.SubmissionID, questions, req.InitialReadMode); err != nil {
		t.Fatal(err)
	}
	if err := o.persistRecognitionReceipt(job.Record.RecordID, parent.InvocationID, run); err != nil {
		t.Fatal(err)
	}
	if err := o.persistRun(job.Record.RecordID, run); err != nil {
		t.Fatal(err)
	}
	job, err = deps.AdvanceGradingStage(ctx, req.AgentName, job.Record.RecordID, AdvanceGradingInput{Outcome: GradingOutcomeOK, ArtifactDigest: "canonical-recognition:" + CanonicalRecognizedQuestionsDigest(questions)})
	if err != nil {
		t.Fatal(err)
	}
	job, err = deps.ConfirmGradingJob(ctx, req.AgentName, job.Record.RecordID, []string{"canonical-recognition:" + CanonicalRecognizedQuestionsDigest(questions)})
	if err != nil {
		t.Fatal(err)
	}
	return o, job, req, q, grader
}

func TestFinalAnswerAssessmentUsesFrozenProofAndReplaysWithoutChangingSource(t *testing.T) {
	o, job, req, q, grader := finalAnswerAssessmentFixture(t)
	q.BBox = &BBox{X: 0.0375, Y: 0.025, W: 0.0125, H: 1.0 / 120}
	original, _ := json.Marshal(q)
	for _, tc := range []struct {
		name   string
		change func(*PhotoGradeRequest, *RecognizedQuestion)
	}{
		{"old policy", func(r *PhotoGradeRequest, q *RecognizedQuestion) {
			r.RecognitionExecutionPolicy = k12.RecognitionExecutionPolicyCompactDAGV1
		}},
		{"not finalized", func(r *PhotoGradeRequest, q *RecognizedQuestion) { r.SourceUncertaintyFinalized = false }},
		{"revision changed", func(r *PhotoGradeRequest, q *RecognizedQuestion) { q.ConfirmedVersion = 2 }},
		{"source changed", func(r *PhotoGradeRequest, q *RecognizedQuestion) { q.Question = "7/9×3/11=" }},
		{"working changed", func(r *PhotoGradeRequest, q *RecognizedQuestion) { q.StudentAnswer = "1/3" }},
		{"other risk", func(r *PhotoGradeRequest, q *RecognizedQuestion) {
			q.ConfirmationReasons = append(q.ConfirmationReasons, OCRRiskLowConfidence)
		}},
		{"forged final", func(r *PhotoGradeRequest, q *RecognizedQuestion) {
			copy := *q.FinalAnswerReceipt
			copy.Evidence.RawTranscription = "1/6"
			q.FinalAnswerReceipt = &copy
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, current := req, cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
			tc.change(&r, &current)
			if _, ok, err := o.verifiedFinalAnswerSolution(t.Context(), o.deps, job, r, current); err != nil || ok {
				t.Fatalf("untrusted source accepted: ok=%v err=%v", ok, err)
			}
		})
	}
	item, err := o.assessDurablePhotoItem(t.Context(), o.deps, job, req, PhotoModeGrade, q)
	if err != nil || item.Status != PhotoWrong || item.Warning != finalAnswerOnlyAssessmentWarning || item.ParentGuide == nil || grader.calls != 1 || item.Grade.Outcome.FinalAnswerCorrect == nil || *item.Grade.Outcome.FinalAnswerCorrect {
		t.Fatalf("final-only assessment=%+v calls=%d err=%v", item, grader.calls, err)
	}
	after, _ := json.Marshal(q)
	if !bytes.Equal(original, after) || item.Recognized.StudentAnswer != "2/4=1/3" || len(item.Recognized.ConfirmationReasons) != 1 {
		t.Fatal("original working or uncertainty changed")
	}
	marks := trustedPhotoMarks([]PhotoGradeItem{item})
	if len(marks) != 1 || marks[0].Status != PhotoWrong || marks[0].Correct || marks[0].BBox != *q.BBox {
		t.Fatalf("verified wrong final answer lost its image annotation: %+v", marks)
	}
	invocations, err := o.deps.Records.ListGradingItemInvocations(t.Context(), req.AgentName, job.Record.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	for _, inv := range invocations {
		if inv.Operation == k12.GradingItemOperationSolve && (inv.ExecutionKind != k12.GradingExecutionLocalDeterministic || inv.Status != k12.ModelInvocationSucceeded) {
			t.Fatalf("solve did not persist locally: %+v", inv)
		}
	}
	replayed, err := o.assessDurablePhotoItem(t.Context(), o.deps, job, req, PhotoModeGrade, q)
	if err != nil || grader.calls != 1 || !reflect.DeepEqual(item, replayed) {
		t.Fatalf("assessment replay changed result: %v calls=%d", err, grader.calls)
	}
	cold := trackGradingOrchestrator(t, NewGradingOrchestrator(o.deps, nil, WithGradingRunDir(o.runDir)))
	typed, ok := cold.typedRecognizedQuestions(t.Context(), job.Record.RecordID, req.AgentName)
	if !ok || len(typed) != 1 || typed[0].FinalAnswerReceipt == nil || !reflect.DeepEqual(*typed[0].FinalAnswerReceipt, *q.FinalAnswerReceipt) {
		t.Fatal("typed read lost validated independent receipt")
	}
	if _, eligible, err := cold.verifiedFinalAnswerSolution(t.Context(), cold.deps, job, req, typed[0]); err != nil || !eligible {
		t.Fatalf("cold typed projection lost final-answer eligibility: eligible=%v err=%v reasons=%v", eligible, err, typed[0].ConfirmationReasons)
	}
	// 本地文件中的扩展字段不能替代真实结算，正常档案字段及其摘要保持不变。
	receiptPath := o.recognitionReceiptPath(job.Record.RecordID)
	originalReceipt, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := o.readRecognitionReceipt(job.Record.RecordID)
	if !ok {
		t.Fatal("fixture receipt is unavailable")
	}
	receipt.Questions[0].FinalAnswerReceipt.Evidence.RawTranscription = "1/6"
	forged, _ := json.Marshal(receipt)
	if err := os.WriteFile(receiptPath, forged, 0o600); err != nil {
		t.Fatal(err)
	}
	typed, ok = cold.typedRecognizedQuestions(t.Context(), job.Record.RecordID, req.AgentName)
	if !ok || len(typed) != 1 || typed[0].FinalAnswerReceipt != nil {
		t.Fatal("typed restore trusted a modified local receipt")
	}
	if err := os.WriteFile(receiptPath, originalReceipt, 0o600); err != nil {
		t.Fatal(err)
	}
	run := o.lookup(job.Record.RecordID)
	if err := o.archiveRecognitionFacts(job.Record.RecordID, run); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(receiptPath); err != nil {
		t.Fatal(err)
	}
	typed, ok = cold.typedRecognizedQuestions(t.Context(), job.Record.RecordID, req.AgentName)
	if !ok || len(typed) != 1 || typed[0].FinalAnswerReceipt == nil || !reflect.DeepEqual(*typed[0].FinalAnswerReceipt, *q.FinalAnswerReceipt) {
		t.Fatal("typed archive restore lost the validated receipt")
	}
	if _, err := applyGradingCorrections(run, ConfirmPhotoGradingInput{Corrections: []GradingQuestionCorrection{{ProblemID: q.ProblemID, StudentAnswer: "1/6", Confirmed: true}}}); err != nil {
		t.Fatal(err)
	}
	if run.questions[0].FinalAnswerReceipt != nil {
		t.Fatal("manual correction inherited old final-answer proof")
	}
}

func TestFinalAnswerAssessmentRejectsInconsistentGradeWithoutContentFallback(t *testing.T) {
	o, job, req, q, grader := finalAnswerAssessmentFixture(t)
	grader.outcome = &GradeOutcome{Verdict: VerdictDisagree}
	_, err := o.assessDurablePhotoItem(t.Context(), o.deps, job, req, PhotoModeGrade, q)
	if !errors.Is(err, ErrGradingAssessmentExactSet) || grader.calls != 1 {
		t.Fatalf("technical verdict mismatch became content result: err=%v calls=%d", err, grader.calls)
	}
	if _, err := o.deps.Records.GetEffectiveGradingAssessment(t.Context(), req.AgentName, job.Record.RecordID, q.ProblemID); err == nil {
		t.Fatal("inconsistent verification was committed as an assessment")
	}
}

func TestFinalAnswerAssessmentKeepsCorrectOrUnsupportedFinalUnclear(t *testing.T) {
	for _, final := range []string{"1/6", "约1/3"} {
		t.Run(final, func(t *testing.T) {
			o, job, req, q, grader := finalAnswerAssessmentFixture(t, final)
			item, err := o.assessDurablePhotoItem(t.Context(), o.deps, job, req, PhotoModeGrade, q)
			if err != nil || item.Status != PhotoAnswerUnclear || item.ParentGuide != nil || grader.calls != 0 {
				t.Fatalf("unproven final was graded: status=%s calls=%d err=%v", item.Status, grader.calls, err)
			}
			invocations, err := o.deps.Records.ListGradingItemInvocations(t.Context(), req.AgentName, job.Record.RecordID)
			if err != nil || len(invocations) != 0 {
				t.Fatalf("unclear answer invoked grading: %+v err=%v", invocations, err)
			}
		})
	}
}
