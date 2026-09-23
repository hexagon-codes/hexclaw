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
