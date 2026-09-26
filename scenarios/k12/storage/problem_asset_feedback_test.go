package k12storage_test

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenario"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// 已被替代的旧答案反馈必须保留治理任务，但不能停用独立验证后的当前版本。
func TestProblemAssetFeedbackOldVersionKeepsReplacementAndRestarts(t *testing.T) {
	ctx := context.Background()
	store, path := problemAssetStore(t)
	p := problemAssetPublication(t, store)
	v1, _, err := store.PublishProblemAsset(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	job, attempt := seedItemLedgerFacts(t, store, "feedback-old-version")
	adoption, _, err := store.AdoptProblemAsset(ctx, k12.ProblemAssetAdoption{OwnerID: v1.OwnerID, JobID: job.RecordID, ProblemID: attempt.ProblemID, InputRevision: attempt.ConfirmedVersion, InputDigest: attempt.InputDigest, AssetID: v1.AssetID, AssetVersion: v1.Version, AssetRevision: v1.Revision, FactsDigest: v1.FactsDigest})
	if err != nil {
		t.Fatal(err)
	}
	_, grade := successfulAssessmentInvocations(t, store, job.RecordID, attempt)
	receipt := assessmentReceipt(job.RecordID, attempt, "", grade)
	receipt.Status = k12.GradingAssessmentCorrect
	receipt.AnswerSource = &k12.ProblemAnswerSource{Kind: k12.ProblemAnswerAsset, AssetID: v1.AssetID, AssetVersion: v1.Version, AssetRevision: v1.Revision, AdoptionID: adoption.AdoptionID, FactsDigest: v1.FactsDigest}
	original, _, err := store.CommitGradingAssessmentItem(ctx, receipt, k12storage.GradingAssessmentEffects{})
	if err != nil {
		t.Fatal(err)
	}
	oldProof, err := store.GetGradingItemInvocation(ctx, "mingming", p.Verification.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	newProof := oldProof
	newProof.InvocationID = "feedback-replacement-proof"
	newProof.OperationAttempt++
	newProof.RequestDigest = "sha256:feedback-replacement-proof"
	if _, _, err = store.PrepareGradingItemInvocation(ctx, newProof); err != nil {
		t.Fatal(err)
	}
	if _, err = store.MarkGradingItemInvocationSent(ctx, "mingming", newProof.InvocationID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.MarkGradingItemInvocationSucceeded(ctx, "mingming", newProof.InvocationID, oldProof.ResultDigest, oldProof.ResultJSON); err != nil {
		t.Fatal(err)
	}
	p.PublicationID = "feedback-replacement"
	p.ReplacesVersion = v1.Version
	p.ExpectedRevision = v1.Revision
	p.Verification.InvocationID = newProof.InvocationID
	v2, created, err := store.PublishProblemAsset(ctx, p)
	if err != nil || !created || v2.Version != 2 {
		t.Fatalf("replacement=%+v %v %v", v2, created, err)
	}
	command := k12storage.ProblemAssetFeedbackCommand{RequestID: "old-version-feedback", OwnerID: v1.OwnerID, AgentName: "mingming", DispatchID: "old-dispatch", JobID: job.RecordID, ProblemID: attempt.ProblemID, InputRevision: original.InputRevision, ResultDigest: original.ResultDigest, Kind: "answer_error", Reason: "The earlier version had an incorrect answer."}
	feedback, created, err := store.AcceptProblemAssetFeedback(ctx, command)
	if err != nil || !created {
		t.Fatalf("feedback=%+v %v %v", feedback, created, err)
	}
	active, err := store.FindExactProblemAsset(ctx, v1.OwnerID, v1.Facts)
	if err != nil || !reflect.DeepEqual(active, v2) {
		t.Fatalf("old feedback disabled replacement: %+v %v", active, err)
	}
	if err = store.DB().Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	registry := scenario.NewRegistry()
	if err = registry.Assemble(k12.Pack(k12.NewCurriculumStub())); err != nil {
		t.Fatal(err)
	}
	restarted := k12storage.NewStore(db, registry.Records)
	stored, err := restarted.GetProblemAssetFeedback(ctx, v1.OwnerID, feedback.FeedbackID)
	if err != nil || !reflect.DeepEqual(stored, feedback) {
		t.Fatalf("feedback did not survive restart: %+v %v", stored, err)
	}
	events, err := k12storage.PendingEvents(ctx, db, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.EventID == feedback.FeedbackID && event.EventType == k12storage.EventProblemAssetFeedback {
			found = true
		}
	}
	if !found {
		t.Fatal("durable feedback has no pending recovery event")
	}
	if _, created, err = restarted.AcceptProblemAssetFeedback(ctx, command); err != nil || created {
		t.Fatalf("restart replay=%v %v", created, err)
	}
	if active, err = restarted.FindExactProblemAsset(ctx, v1.OwnerID, v1.Facts); err != nil || active.Version != 2 {
		t.Fatalf("replacement after restart=%+v %v", active, err)
	}
}
