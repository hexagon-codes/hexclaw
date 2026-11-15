package k12storage_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func blankRecognizedIntentScope() *k12.RecognitionImageScopeInputV1 {
	return &k12.RecognitionImageScopeInputV1{
		Version:            k12.RecognitionImageScopeVersionV1,
		SourceReviewPolicy: k12.RecognitionImageSourceUprightReviewPolicyV3,
	}
}

func setClearBlankIntentFacts(snapshot *k12.ProblemAttemptSnapshot) {
	for i := range snapshot.Problems {
		snapshot.Problems[i].Subject = "数学"
		snapshot.Problems[i].ConfirmationRequired = false
		snapshot.Problems[i].ConfirmationReasons = nil
	}
	for i := range snapshot.Attempts {
		snapshot.Attempts[i].AnswerState = "blank"
		snapshot.Attempts[i].AnswerRaw, snapshot.Attempts[i].AnswerMarkdown = "", ""
	}
}

func TestProblemAttemptSnapshot_AutomaticClearBlankIntentIsAtomicAndReceiptImmutable(t *testing.T) {
	ctx := context.Background()
	store, _, dispatch, homework, snapshot := prepareRecognizedIntentFixture(t, k12.ImageTaskIntentCompletedHomework, blankRecognizedIntentScope())
	setClearBlankIntentFacts(&snapshot)
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
	child, err := store.GetHomeworkSubmission(ctx, dispatch.AgentName, homework.SubmissionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskIntent != k12.ImageTaskIntentBlankWorksheet || child.TaskIntent != k12.ImageTaskIntentBlankWorksheet {
		t.Fatalf("verified blank source requires solve flow: dispatch=%s homework=%s", got.TaskIntent, child.TaskIntent)
	}
	if got.Version != dispatch.Version+1 || child.Version != homework.Version+1 || child.GradingJobID != homework.GradingJobID ||
		got.ClassificationInvocationID != dispatch.ClassificationInvocationID || got.TargetObjectID != dispatch.TargetObjectID ||
		!reflect.DeepEqual(got.ClassificationRouteSnapshot, dispatch.ClassificationRouteSnapshot) || !reflect.DeepEqual(got.IntentEvidence, dispatch.IntentEvidence) {
		t.Fatal("derived intent correction changed frozen source identity")
	}
	after, err := store.GetImageTaskInvocation(ctx, dispatch.AgentName, dispatch.ClassificationInvocationID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("classification receipt changed: err=%v", err)
	}
	if err := store.PutProblemAttemptSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	replay, err := store.GetImageTaskDispatch(ctx, dispatch.AgentName, dispatch.DispatchID)
	if err != nil || replay.Version != got.Version {
		t.Fatalf("correction replay advanced again: %+v err=%v", replay, err)
	}
}

func TestProblemAttemptSnapshot_ClearBlankIntentDoesNotReinterpretOtherEvidence(t *testing.T) {
	for _, scenario := range []string{"present", "unclear", "source risk", "explicit grade", "explicit solve", "selected scope", "different page", "confirmed", "locating", "unknown", "legacy policy", "other subject"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			scope := blankRecognizedIntentScope()
			switch scenario {
			case "explicit grade":
				scope.Action = "grade"
			case "explicit solve":
				scope.Action = "solve"
			case "selected scope":
				scope.MessageIntent = "只处理第4题"
			case "legacy policy":
				scope.SourceReviewPolicy = k12.RecognitionImageSourceUprightReviewPolicyV2
			}
			store, db, dispatch, homework, snapshot := prepareRecognizedIntentFixture(t, k12.ImageTaskIntentCompletedHomework, scope)
			setClearBlankIntentFacts(&snapshot)
			switch scenario {
			case "present":
				snapshot.Attempts[0].AnswerState, snapshot.Attempts[0].AnswerRaw, snapshot.Attempts[0].AnswerMarkdown = "present", "3", "3"
			case "unclear":
				snapshot.Attempts[0].AnswerState = "unclear"
			case "source risk":
				snapshot.Problems[0].ConfirmationRequired, snapshot.Problems[0].ConfirmationReasons = true, []string{"evidence_conflict"}
			case "different page":
				for i := range snapshot.Problems {
					snapshot.Problems[i].PageAssetID = "other-source-page"
				}
			case "other subject":
				for i := range snapshot.Problems {
					snapshot.Problems[i].Subject = "语文"
				}
			}
			var query string
			switch scenario {
			case "confirmed":
				query = `UPDATE k12_grading_jobs SET confirmed_version=1,confirmation_state='confirmed' WHERE record_id=?`
			case "locating":
				query = `UPDATE k12_grading_jobs SET status='locating' WHERE record_id=?`
			case "unknown":
				query = `UPDATE k12_grading_jobs SET status='outcome_unknown',failed_stage='recognizing' WHERE record_id=?`
			}
			if query != "" {
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
			child, err := store.GetHomeworkSubmission(ctx, dispatch.AgentName, homework.SubmissionID)
			if err != nil || got.TaskIntent != dispatch.TaskIntent || child.TaskIntent != homework.TaskIntent ||
				got.Version != dispatch.Version || child.Version != homework.Version {
				t.Fatalf("unrelated or frozen evidence was reinterpreted: dispatch=%+v homework=%+v err=%v", got, child, err)
			}
		})
	}
}
