package k12storage_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func tutorRefFixture(t *testing.T, s *k12storage.Store, key, message string) (k12storage.TutorContextRef, k12.GradingAssessmentItem, k12.Attempt) {
	t.Helper()
	job, attempt := seedItemLedgerFacts(t, s, key)
	solve, grade := successfulAssessmentInvocations(t, s, job.RecordID, attempt)
	item := assessmentReceipt(job.RecordID, attempt, solve, grade)
	item.Status = k12.GradingAssessmentCorrect
	result, _ := json.Marshal(usecase.PhotoGradeItem{Status: usecase.PhotoCorrect})
	item.ResultJSON = string(result)
	stored, _, err := s.CommitGradingAssessmentItem(context.Background(), item, k12storage.GradingAssessmentEffects{})
	if err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(usecase.RecognizedQuestion{ProblemID: attempt.ProblemID, ConfirmedVersion: 1, SourceNumberPath: []string{"3"}, Question: "2+2=?", StudentAnswer: "4"})
	return k12storage.TutorContextRef{OwnerScope: "guardian", AgentName: "mingming", ConversationKey: k12storage.TutorConversationKey("desktop", "", "session"), MessageID: message, Kind: "source", JobID: job.RecordID, ProblemID: attempt.ProblemID, InputRevision: 1, ResultDigest: stored.ResultDigest, PrintedNumber: "3", QuestionJSON: string(q)}, stored, attempt
}

func tutorHomeworkFixture(t *testing.T, s *k12storage.Store, ref k12storage.TutorContextRef, dispatchID string) {
	t.Helper()
	ctx := context.Background()
	dispatch := testImageTaskDispatch()
	dispatch.DispatchID, dispatch.SourceRef = dispatchID, ref.MessageID
	dispatch.IdempotencyKey, dispatch.ClassificationInvocationID = dispatchID, "classify-"+dispatchID
	invocation := k12.ImageTaskInvocation{
		InvocationID: dispatch.ClassificationInvocationID, AgentName: ref.AgentName, DispatchID: dispatchID,
		Operation: k12.ImageTaskOperationClassification, OperationKey: "dispatch:" + dispatchID + ":classification",
		RequestDigest: "sha256:classify-request", RouteSnapshot: testImageRoute(),
		Status: k12.ImageTaskInvocationPrepared, Attempt: 1, CreatedAt: 100, UpdatedAt: 100,
	}
	if _, _, err := s.PrepareImageTaskDispatch(ctx, dispatch, invocation); err != nil {
		t.Fatal(err)
	}
	_, err := s.DB().Exec(`INSERT INTO k12_homework_submissions
		(submission_id,dispatch_id,agent_name,learner_id,source_kind,source_ref,source_asset_refs_json,task_intent,status,grading_job_id,idempotency_key,created_at,updated_at)
		VALUES(?,?,'mingming','learner-1','desktop',?,'[]','completed_homework','completed',?,?,100,100)`,
		"submission-"+dispatchID, dispatchID, ref.MessageID, ref.JobID, dispatchID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestTutorHomeworkNumberSummaryUsesExactStoredTaskAndScope(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	first, _, _ := tutorRefFixture(t, s, "number-first", "photo-first")
	second, _, _ := tutorRefFixture(t, s, "number-second", "photo-second")
	first.ConversationKey = k12storage.TutorConversationKey("dingtalk", "family-bot", "parent")
	second.ConversationKey = first.ConversationKey
	if err := s.SaveTutorSourceRefs(ctx, []k12storage.TutorContextRef{first, second}); err != nil {
		t.Fatal(err)
	}
	tutorHomeworkFixture(t, s, first, "task-first")
	tutorHomeworkFixture(t, s, second, "task-second")
	deps := &usecase.Deps{Records: s}
	input := usecase.TutorFollowupInput{OwnerScope: first.OwnerScope, AgentName: first.AgentName,
		ConversationKey: first.ConversationKey, MessageID: "summary", ReplyTo: "native-result-id",
		Query: "这条批改对应哪次作业？作业编号：HW-task-first"}
	for range 2 {
		text, err := deps.TutorFollowupDirective(ctx, input)
		if err != nil || !strings.Contains(text, `"homework_id":"HW-task-first"`) || !strings.Contains(text, first.JobID) || strings.Contains(text, second.JobID) || !strings.Contains(text, `"status":"correct"`) {
			t.Fatalf("exact summary: %q %v", text, err)
		}
	}
	for _, change := range []func(*usecase.TutorFollowupInput){
		func(in *usecase.TutorFollowupInput) { in.OwnerScope = "another-owner" },
		func(in *usecase.TutorFollowupInput) { in.AgentName = "another-child" },
		func(in *usecase.TutorFollowupInput) {
			in.ConversationKey = k12storage.TutorConversationKey("dingtalk", "another-bot", "parent")
		},
		func(in *usecase.TutorFollowupInput) {
			in.ConversationKey = k12storage.TutorConversationKey("dingtalk", "family-bot", "another-parent")
		},
		func(in *usecase.TutorFollowupInput) { in.Query = "作业编号：HW-unknown" },
	} {
		other := input
		change(&other)
		text, err := deps.TutorFollowupDirective(ctx, other)
		if err != nil || !strings.Contains(text, "No stored homework") || strings.Contains(text, first.JobID) {
			t.Fatalf("scope or absent reference guessed: %q %v", text, err)
		}
	}
	input.Query = "HW-task-first 和 HW-task-second 对应哪次作业"
	if text, err := deps.TutorFollowupDirective(ctx, input); err != nil || !strings.Contains(text, "multiple homework references") {
		t.Fatalf("multiple IDs guessed: %q %v", text, err)
	}
	input.Query = "这条批改对应哪次作业"
	if text, err := deps.TutorFollowupDirective(ctx, input); err != nil || !strings.Contains(text, "No stored homework reference") {
		t.Fatalf("legacy native ID guessed: %q %v", text, err)
	}
	var refs, assessments, invocations int
	err := s.DB().QueryRow(`SELECT (SELECT count(*) FROM k12_tutor_context_refs),
		(SELECT count(*) FROM k12_grading_assessment_items), (SELECT count(*) FROM k12_grading_item_invocations)`).Scan(&refs, &assessments, &invocations)
	if err != nil || refs != 2 || assessments != 2 || invocations != 4 {
		t.Fatalf("summary mutated homework: refs=%d assessments=%d invocations=%d err=%v", refs, assessments, invocations, err)
	}
	input.MessageID, input.Query = "question", "HW-task-first 第三题再讲一下"
	for range 2 {
		text, err := deps.TutorFollowupDirective(ctx, input)
		if err != nil || !strings.Contains(text, first.JobID) || strings.Contains(text, second.JobID) || !strings.Contains(text, "Homework follow-up context") {
			t.Fatalf("numbered question: %q %v", text, err)
		}
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM k12_tutor_context_refs WHERE kind='followup'`).Scan(&refs); err != nil || refs != 1 {
		t.Fatalf("replay created duplicate association: %d %v", refs, err)
	}
}

func TestTutorHomeworkNumberSummaryDoesNotRequireSubquestion(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	ref, original, attempt := tutorRefFixture(t, s, "worksheet-summary", "photo")
	second := problemAttemptFixture("mingming", "submission-1").Attempts[1]
	second.ConfirmedVersion, second.InputDigest = 1, "sha256:input-child-2-v1"
	solve := correctionProof(t, s, ref.JobID, second, k12.GradingItemOperationSolve, 2, true)
	grade := correctionProof(t, s, ref.JobID, second, k12.GradingItemOperationGrade, 2, true)
	item := assessmentReceipt(ref.JobID, second, solve, grade)
	item.ResultDigest = "sha256:summary-second"
	stored, _, err := s.CommitGradingAssessmentItem(ctx, item, k12storage.GradingAssessmentEffects{})
	if err != nil {
		t.Fatal(err)
	}
	other := ref
	other.ProblemID, other.ResultDigest = stored.ProblemID, stored.ResultDigest
	if err := s.SaveTutorSourceRefs(ctx, []k12storage.TutorContextRef{ref, other}); err != nil {
		t.Fatal(err)
	}
	tutorHomeworkFixture(t, s, ref, "worksheet")
	deps := &usecase.Deps{Records: s}
	input := usecase.TutorFollowupInput{OwnerScope: ref.OwnerScope, AgentName: ref.AgentName, ConversationKey: ref.ConversationKey, MessageID: "summary", Query: "HW-worksheet 对应哪次作业？"}
	text, err := deps.TutorFollowupDirective(ctx, input)
	if err != nil || !strings.Contains(text, `"problem_id":"`+ref.ProblemID+`"`) || !strings.Contains(text, `"problem_id":"`+other.ProblemID+`"`) || strings.Contains(text, "reference is ambiguous") {
		t.Fatalf("worksheet incorrectly requires one question: %q %v", text, err)
	}
	corrected := original
	corrected.GradeInvocationID = correctionProof(t, s, original.JobID, attempt, k12.GradingItemOperationGrade, 3, true)
	corrected.Status, corrected.ResultJSON, corrected.ResultDigest = k12.GradingAssessmentUntrusted, `{"Status":"untrusted"}`, "sha256:withdrawn"
	if _, _, err := s.AppendGradingAssessmentCorrection(ctx, k12.GradingAssessmentCorrection{CorrectionID: "summary-correction", OriginalResultDigest: original.ResultDigest, Reason: k12.AssessmentCorrectionGrading, Assessment: corrected}, k12storage.GradingAssessmentEffects{}); err != nil {
		t.Fatal(err)
	}
	text, err = deps.TutorFollowupDirective(ctx, input)
	if err != nil || strings.Contains(text, original.ResultDigest) || !strings.Contains(text, `"status":"untrusted"`) {
		t.Fatalf("summary ignored current correction: %q %v", text, err)
	}
	input.MessageID, input.Query = "duplicate-question", "HW-worksheet 第三题再讲"
	if text, err = deps.TutorFollowupDirective(ctx, input); err != nil || !strings.Contains(text, "reference is ambiguous") {
		t.Fatalf("duplicate printed question guessed: %q %v", text, err)
	}
}

func TestTutorContextReplayRestartAndScope(t *testing.T) {
	s, path := problemAssetStore(t)
	ctx := context.Background()
	ref, _, _ := tutorRefFixture(t, s, "first", "photo")
	for range 2 {
		if err := s.SaveTutorSourceRefs(ctx, []k12storage.TutorContextRef{ref}); err != nil {
			t.Fatal(err)
		}
	}
	scope := ref
	scope.MessageID = "followup"
	first, ambiguous, err := s.ResolveTutorContext(ctx, scope, "", "3", "第3题再简单点")
	if err != nil || ambiguous || first.JobID != ref.JobID || first.Kind != "followup" {
		t.Fatalf("resolve: %+v %v %v", first, ambiguous, err)
	}
	second, _, _ := tutorRefFixture(t, s, "second", "photo-2")
	if err := s.SaveTutorSourceRefs(ctx, []k12storage.TutorContextRef{second}); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = k12storage.NewStore(db, nil)
	got, ambiguous, err := s.ResolveTutorContext(ctx, scope, "", "3", "第3题再简单点")
	if err != nil || ambiguous || got.JobID != first.JobID {
		t.Fatalf("replay rebound to new homework: %+v %v %v", got, ambiguous, err)
	}
	if _, _, err = s.ResolveTutorContext(ctx, scope, "", "3", "changed request"); !errors.Is(err, k12storage.ErrTutorContextConflict) {
		t.Fatalf("message changed: %v", err)
	}
	scope.MessageID = "next"
	if _, ambiguous, err = s.ResolveTutorContext(ctx, scope, "", "3", "第3题"); err != nil || !ambiguous {
		t.Fatalf("multiple jobs guessed: %v %v", ambiguous, err)
	}
	got, ambiguous, err = s.ResolveTutorContext(ctx, scope, "photo", "3", "第3题")
	if err != nil || ambiguous || got.JobID != first.JobID {
		t.Fatalf("explicit reference: %+v %v %v", got, ambiguous, err)
	}
	for _, change := range []func(*k12storage.TutorContextRef){
		func(r *k12storage.TutorContextRef) { r.OwnerScope = "other" },
		func(r *k12storage.TutorContextRef) { r.AgentName = "other-child" },
		func(r *k12storage.TutorContextRef) {
			r.ConversationKey = k12storage.TutorConversationKey("dingtalk", "another-bot", "session")
		},
	} {
		other := scope
		change(&other)
		if _, _, err := s.ResolveTutorContext(ctx, other, "photo", "3", "第3题"); !errors.Is(err, records.ErrNotFound) {
			t.Fatalf("cross scope reference: %v", err)
		}
	}
}

func TestTutorContextUsesCurrentCorrectionWithoutNewAssessment(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	ref, original, attempt := tutorRefFixture(t, s, "corrected", "photo")
	if err := s.SaveTutorSourceRefs(ctx, []k12storage.TutorContextRef{ref}); err != nil {
		t.Fatal(err)
	}
	d := &usecase.Deps{Records: s}
	input := usecase.TutorFollowupInput{OwnerScope: ref.OwnerScope, AgentName: ref.AgentName, ConversationKey: ref.ConversationKey, MessageID: "followup", Query: "第三题再简单点"}
	before, err := d.TutorFollowupDirective(ctx, input)
	if err != nil || !strings.Contains(before, "2+2=?") || !strings.Contains(before, original.ResultDigest) {
		t.Fatalf("original context: %q %v", before, err)
	}
	corrected := original
	corrected.GradeInvocationID = correctionProof(t, s, original.JobID, attempt, k12.GradingItemOperationGrade, 2, true)
	corrected.Status = k12.GradingAssessmentUntrusted
	corrected.ResultJSON, corrected.ResultDigest = `{"Status":"untrusted"}`, "sha256:retracted"
	if _, _, err := s.AppendGradingAssessmentCorrection(ctx, k12.GradingAssessmentCorrection{CorrectionID: "retracted", OriginalResultDigest: original.ResultDigest, Reason: k12.AssessmentCorrectionGrading, Assessment: corrected}, k12storage.GradingAssessmentEffects{}); err != nil {
		t.Fatal(err)
	}
	after, err := d.TutorFollowupDirective(ctx, input)
	if err != nil || strings.Contains(after, original.ResultDigest) || !strings.Contains(after, "no reliable current assessment") {
		t.Fatalf("stale context: %q %v", after, err)
	}
	input.MessageID, input.Query = "continued-after-correction", "再简单点"
	continued, err := d.TutorFollowupDirective(ctx, input)
	if err != nil || strings.Contains(continued, original.ResultDigest) || !strings.Contains(continued, "no reliable current assessment") {
		t.Fatalf("continued followup reused the withdrawn assessment: %q %v", continued, err)
	}
	var assessments int
	if err := s.DB().QueryRow(`SELECT count(*) FROM k12_grading_assessment_items`).Scan(&assessments); err != nil || assessments != 1 {
		t.Fatalf("followup created assessment: %d %v", assessments, err)
	}
	input.MessageID, input.Query, input.HasAttachments = "new-answer", "第三题", true
	if text, err := d.TutorFollowupDirective(ctx, input); err != nil || text != "" {
		t.Fatalf("new photo intercepted: %q %v", text, err)
	}
}

func TestTutorContextRepeatedPrintedNumberDoesNotGuess(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	ref, _, _ := tutorRefFixture(t, s, "duplicate-number", "photo")
	second := problemAttemptFixture("mingming", "submission-1").Attempts[1]
	second.ConfirmedVersion, second.InputDigest = 1, "sha256:input-child-2-v1"
	solve := correctionProof(t, s, ref.JobID, second, k12.GradingItemOperationSolve, 2, true)
	grade := correctionProof(t, s, ref.JobID, second, k12.GradingItemOperationGrade, 2, true)
	item := assessmentReceipt(ref.JobID, second, solve, grade)
	stored, _, err := s.CommitGradingAssessmentItem(ctx, item, k12storage.GradingAssessmentEffects{})
	if err != nil {
		t.Fatal(err)
	}
	other := ref
	other.ProblemID, other.ResultDigest = stored.ProblemID, stored.ResultDigest
	if err := s.SaveTutorSourceRefs(ctx, []k12storage.TutorContextRef{ref, other}); err != nil {
		t.Fatal(err)
	}
	scope := ref
	scope.MessageID = "followup"
	if _, ambiguous, err := s.ResolveTutorContext(ctx, scope, "photo", "3", "第3题"); err != nil || !ambiguous {
		t.Fatalf("duplicate printed number guessed: %v %v", ambiguous, err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM k12_tutor_context_refs WHERE kind='followup'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("ambiguous reference persisted: %d %v", count, err)
	}
}

func TestTutorContextExplicitResolutionContinuesAfterRestart(t *testing.T) {
	s, path := problemAssetStore(t)
	ctx := context.Background()
	first, _, _ := tutorRefFixture(t, s, "im-first", "photo-first")
	second, _, _ := tutorRefFixture(t, s, "im-second", "photo-second")
	third, _, _ := tutorRefFixture(t, s, "im-third", "photo-third")
	conversation := k12storage.TutorConversationKey("dingtalk", "family-bot", "parent")
	first.ConversationKey, second.ConversationKey = conversation, conversation
	third.ConversationKey = conversation
	if err := s.SaveTutorSourceRefs(ctx, []k12storage.TutorContextRef{first, second}); err != nil {
		t.Fatal(err)
	}
	scope := first
	scope.MessageID = "ambiguous-followup"
	if _, ambiguous, err := s.ResolveTutorContext(ctx, scope, "", "3", "第3题再简单点"); err != nil || !ambiguous {
		t.Fatalf("multiple worksheets were not clarified: ambiguous=%v err=%v", ambiguous, err)
	}
	// 外部回执查询键不能冒充被引用消息；未知引用也不能回退到最近作业。
	scope.MessageID = "unknown-reference"
	if _, _, err := s.ResolveTutorContext(ctx, scope, "process-query-key", "3", "第3题"); !errors.Is(err, records.ErrNotFound) {
		t.Fatalf("unknown reference was guessed: %v", err)
	}
	scope.MessageID = "explicit-selection"
	selected, ambiguous, err := s.ResolveTutorContext(ctx, scope, "photo-first", "3", "这份的第3题")
	if err != nil || ambiguous || selected.JobID != first.JobID {
		t.Fatalf("explicit selection: %+v ambiguous=%v err=%v", selected, ambiguous, err)
	}
	if err := s.DB().Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = k12storage.NewStore(db, nil)
	scope.MessageID = "continued-followup"
	continued, ambiguous, err := s.ResolveTutorContext(ctx, scope, "", "", "再简单点")
	if err != nil || ambiguous || continued.JobID != first.JobID || continued.ProblemID != first.ProblemID {
		t.Fatalf("resolved worksheet was lost after restart: %+v ambiguous=%v err=%v", continued, ambiguous, err)
	}
	// 重放已登记的原图不改变当前题；新的作业到达才重新进入歧义判断。
	if err := s.SaveTutorSourceRefs(ctx, []k12storage.TutorContextRef{second}); err != nil {
		t.Fatal(err)
	}
	scope.MessageID = "after-source-replay"
	continued, ambiguous, err = s.ResolveTutorContext(ctx, scope, "", "3", "第3题再讲一下")
	if err != nil || ambiguous || continued.JobID != first.JobID {
		t.Fatalf("source replay changed the active worksheet: %+v ambiguous=%v err=%v", continued, ambiguous, err)
	}
	if err := s.SaveTutorSourceRefs(ctx, []k12storage.TutorContextRef{third}); err != nil {
		t.Fatal(err)
	}
	scope.MessageID = "after-new-worksheet"
	if _, ambiguous, err := s.ResolveTutorContext(ctx, scope, "", "3", "第3题再简单点"); err != nil || !ambiguous {
		t.Fatalf("new worksheet inherited the previous selection: ambiguous=%v err=%v", ambiguous, err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM k12_grading_assessment_items`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("followup changed assessments: count=%d err=%v", count, err)
	}
}

func TestTutorContextDoesNotUseArchivedAsset(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	p := problemAssetPublication(t, s)
	v, _, err := s.PublishProblemAsset(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	job, attempt := seedItemLedgerFacts(t, s, "followup-adoption")
	adoption, _, err := s.AdoptProblemAsset(ctx, k12.ProblemAssetAdoption{OwnerID: v.OwnerID, JobID: job.RecordID, ProblemID: attempt.ProblemID, InputRevision: 1, InputDigest: attempt.InputDigest, AssetID: v.AssetID, AssetVersion: v.Version, AssetRevision: v.Revision, FactsDigest: v.FactsDigest})
	if err != nil {
		t.Fatal(err)
	}
	grade := correctionProof(t, s, job.RecordID, attempt, k12.GradingItemOperationGrade, 1, true)
	item := assessmentReceipt(job.RecordID, attempt, "", grade)
	item.Status, item.ResultJSON = k12.GradingAssessmentCorrect, `{"Status":"correct"}`
	item.AnswerSource = &k12.ProblemAnswerSource{Kind: k12.ProblemAnswerAsset, FactsDigest: v.FactsDigest, AssetID: v.AssetID, AssetVersion: v.Version, AssetRevision: v.Revision, AdoptionID: adoption.AdoptionID}
	stored, _, err := s.CommitGradingAssessmentItem(ctx, item, k12storage.GradingAssessmentEffects{})
	if err != nil {
		t.Fatal(err)
	}
	ref := k12storage.TutorContextRef{OwnerScope: v.OwnerID, AgentName: "mingming", ConversationKey: k12storage.TutorConversationKey("desktop", "", "session"), MessageID: "photo", Kind: "source", JobID: job.RecordID, ProblemID: attempt.ProblemID, InputRevision: 1, ResultDigest: stored.ResultDigest, PrintedNumber: "3", QuestionJSON: `{"Question":"2+2=?"}`}
	if err := s.SaveTutorSourceRefs(ctx, []k12storage.TutorContextRef{ref}); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
		t.Fatal(err)
	}
	d := &usecase.Deps{Records: s}
	text, err := d.TutorFollowupDirective(ctx, usecase.TutorFollowupInput{OwnerScope: v.OwnerID, AgentName: "mingming", ConversationKey: ref.ConversationKey, MessageID: "followup", Query: "第3题再讲一下"})
	if err != nil || !strings.Contains(text, "no longer verified") || strings.Contains(text, item.ResultDigest) {
		t.Fatalf("withdrawn answer reused: %q %v", text, err)
	}
}
