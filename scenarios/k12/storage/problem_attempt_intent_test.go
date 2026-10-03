package k12storage_test

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func prepareRecognizedIntentFixture(t *testing.T, initialIntent k12.ImageTaskIntent) (*k12storage.Store, *sql.DB, k12.ImageTaskDispatch, k12.HomeworkSubmission, k12.ProblemAttemptSnapshot) {
	t.Helper()
	ctx := context.Background()
	store, db := setup(t)
	dispatch := testImageTaskDispatch()
	invocation := k12.ImageTaskInvocation{
		InvocationID: dispatch.ClassificationInvocationID, AgentName: dispatch.AgentName,
		DispatchID: dispatch.DispatchID, Operation: k12.ImageTaskOperationClassification,
		OperationKey: "dispatch:dispatch-1:classification", RequestDigest: "classification-request",
		RouteSnapshot: testImageRoute(), Status: k12.ImageTaskInvocationPrepared, Attempt: 1,
		CreatedAt: 100, UpdatedAt: 100,
	}
	if _, _, err := store.PrepareImageTaskDispatch(ctx, dispatch, invocation); err != nil {
		t.Fatal(err)
	}
	decision := k12storage.ImageTaskRoutingDecision{
		Intent: initialIntent, Evidence: []string{"initial model classification"},
		Confidence: .96, InvocationResultDigest: "sha256:original-classification",
	}
	if initialIntent == k12.ImageTaskIntentUnknown {
		decision.ConfirmationCandidates = []k12.ImageTaskIntent{k12.ImageTaskIntentCompletedHomework, k12.ImageTaskIntentBlankWorksheet}
	}
	routed, target, err := store.CommitImageTaskRouting(ctx, dispatch.AgentName, dispatch.DispatchID, 0, decision)
	if err == nil && initialIntent == k12.ImageTaskIntentUnknown {
		routed, target, err = store.ConfirmImageTaskIntent(ctx, dispatch.AgentName, dispatch.DispatchID, routed.Version, k12.ImageTaskIntentBlankWorksheet)
	}
	if err != nil || target.HomeworkSubmission == nil {
		t.Fatalf("route fixture: target=%+v err=%v", target, err)
	}
	job, err := k12.NewGradingJobRecord(dispatch.AgentName, "session", k12.GradingJobFields{
		SubmissionID: "photo-current", SourceKind: "image_task",
		IdempotencyKey:    k12.BuildGradingIdempotencyKey("image_task", dispatch.DispatchID, 0),
		ConfirmationState: k12.GradingConfirmationPending, AnchorState: k12.GradingAnchorPending,
		ModelSnapshot: k12.GradingModelSnapshot{Provider: "test", Model: "vision", Route: "test/vision"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE k12_grading_jobs SET status='recognizing' WHERE record_id=?`, job.RecordID); err != nil {
		t.Fatal(err)
	}
	homework, err := store.BindHomeworkSubmissionGradingJob(ctx, dispatch.AgentName, target.HomeworkSubmission.SubmissionID, job.RecordID, target.HomeworkSubmission.Version)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := problemAttemptFixture(dispatch.AgentName, "photo-current")
	for i := range snapshot.Problems {
		snapshot.Problems[i].PageAssetID = dispatch.SourceAssetRefs[0]
	}
	return store, db, routed, homework, snapshot
}

func TestProblemAttemptSnapshot_AutomaticAnsweredIntentIsAtomicAndReceiptImmutable(t *testing.T) {
	ctx := context.Background()
	store, _, dispatch, homework, snapshot := prepareRecognizedIntentFixture(t, k12.ImageTaskIntentBlankWorksheet)
	before, err := store.GetImageTaskInvocation(ctx, dispatch.AgentName, dispatch.ClassificationInvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutProblemAttemptSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetImageTaskDispatch(ctx, dispatch.AgentName, dispatch.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	gotHomework, err := store.GetHomeworkSubmission(ctx, dispatch.AgentName, homework.SubmissionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskIntent != k12.ImageTaskIntentCompletedHomework || gotHomework.TaskIntent != k12.ImageTaskIntentCompletedHomework {
		t.Fatalf("current handwritten answer must correct both intents: dispatch=%s homework=%s", got.TaskIntent, gotHomework.TaskIntent)
	}
	if got.Version != dispatch.Version+1 || gotHomework.Version != homework.Version+1 ||
		gotHomework.GradingJobID != homework.GradingJobID || got.TargetObjectID != dispatch.TargetObjectID ||
		got.ClassificationInvocationID != dispatch.ClassificationInvocationID ||
		got.ClassificationRouteSnapshot != dispatch.ClassificationRouteSnapshot ||
		got.IntentConfidence != dispatch.IntentConfidence || !reflect.DeepEqual(got.IntentEvidence, dispatch.IntentEvidence) {
		t.Fatalf("intent correction changed frozen identity or classification facts: dispatch=%+v homework=%+v", got, gotHomework)
	}
	after, err := store.GetImageTaskInvocation(ctx, dispatch.AgentName, dispatch.ClassificationInvocationID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("classification receipt changed: before=%+v after=%+v err=%v", before, after, err)
	}
	if err := store.PutProblemAttemptSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.GetImageTaskDispatch(ctx, dispatch.AgentName, dispatch.DispatchID)
	if err != nil || replayed.Version != got.Version {
		t.Fatalf("replay must not repeat correction: dispatch=%+v err=%v", replayed, err)
	}
}

func TestProblemAttemptSnapshot_IntentCorrectionStaysWithinCurrentAutomaticSource(t *testing.T) {
	for _, scenario := range []string{"blank", "unclear", "explicit blank choice", "different page", "different submission", "different owner", "completed history", "located", "assessment started"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			initialIntent := k12.ImageTaskIntentBlankWorksheet
			if scenario == "explicit blank choice" {
				initialIntent = k12.ImageTaskIntentUnknown
			}
			store, db, dispatch, homework, snapshot := prepareRecognizedIntentFixture(t, initialIntent)
			switch scenario {
			case "blank", "unclear":
				for i := range snapshot.Attempts {
					snapshot.Attempts[i].AnswerState = scenario
					snapshot.Attempts[i].AnswerRaw, snapshot.Attempts[i].AnswerMarkdown = "", ""
				}
			case "different page":
				for i := range snapshot.Problems {
					snapshot.Problems[i].PageAssetID = "page-other"
				}
			case "different submission", "different owner":
				for i := range snapshot.Problems {
					if scenario == "different submission" {
						snapshot.Problems[i].SubmissionID = "photo-other"
					} else {
						snapshot.Problems[i].AgentName = "lele"
					}
				}
				for i := range snapshot.Attempts {
					if scenario == "different submission" {
						snapshot.Attempts[i].SubmissionID = "photo-other"
					} else {
						snapshot.Attempts[i].AgentName = "lele"
					}
				}
			case "completed history", "located", "assessment started":
				query := `UPDATE k12_grading_jobs SET status='completed' WHERE record_id=?`
				if scenario == "located" {
					query = `UPDATE k12_grading_jobs SET anchor_state='located' WHERE record_id=?`
				} else if scenario == "assessment started" {
					query = `UPDATE k12_grading_jobs SET status='assessing' WHERE record_id=?`
				}
				if _, err := db.ExecContext(ctx, query, homework.GradingJobID); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.PutProblemAttemptSnapshot(ctx, snapshot); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetImageTaskDispatch(ctx, dispatch.AgentName, dispatch.DispatchID)
			if err != nil {
				t.Fatal(err)
			}
			gotHomework, err := store.GetHomeworkSubmission(ctx, dispatch.AgentName, homework.SubmissionID)
			if err != nil || got.TaskIntent != k12.ImageTaskIntentBlankWorksheet || gotHomework.TaskIntent != k12.ImageTaskIntentBlankWorksheet ||
				got.Version != dispatch.Version || gotHomework.Version != homework.Version {
				t.Fatalf("unrelated or frozen source changed: dispatch=%+v homework=%+v err=%v", got, gotHomework, err)
			}
		})
	}
}

func TestProblemAttemptSnapshot_IntentCorrectionFailureRollsBackFactsAndBothIntents(t *testing.T) {
	ctx := context.Background()
	store, db, dispatch, homework, snapshot := prepareRecognizedIntentFixture(t, k12.ImageTaskIntentBlankWorksheet)
	// 真实 SQLite 在第二次意图写入失败，不能留下半次纠正或已提交的识别事实。
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_homework_intent_correction
        BEFORE UPDATE OF task_intent ON k12_homework_submissions
        WHEN NEW.task_intent='completed_homework'
        BEGIN SELECT RAISE(ABORT,'injected homework correction failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProblemAttemptSnapshot(ctx, snapshot); err == nil {
		t.Fatal("second intent write failure must abort the facts transaction")
	}
	got, err := store.GetImageTaskDispatch(ctx, dispatch.AgentName, dispatch.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	gotHomework, err := store.GetHomeworkSubmission(ctx, dispatch.AgentName, homework.SubmissionID)
	if err != nil || got.TaskIntent != k12.ImageTaskIntentBlankWorksheet || gotHomework.TaskIntent != k12.ImageTaskIntentBlankWorksheet {
		t.Fatalf("partial intent transaction leaked: dispatch=%s homework=%s err=%v", got.TaskIntent, gotHomework.TaskIntent, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_problems WHERE agent_name=? AND submission_id=?`, dispatch.AgentName, "photo-current").Scan(&count); err != nil || count != 0 {
		t.Fatalf("recognition facts escaped rollback: count=%d err=%v", count, err)
	}
}
