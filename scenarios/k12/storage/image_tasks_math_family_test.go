package k12storage_test

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func prepareMathFamilyRouting(t *testing.T, scope *k12.RecognitionImageScopeInputV1, candidates []k12.ImageTaskIntent) (*k12storage.Store, *sql.DB, k12.ImageTaskDispatch, k12storage.ImageTaskRoutingDecision, k12storage.ImageTaskClassificationResponse) {
	t.Helper()
	store, db := setup(t)
	dispatch := testImageTaskDispatch()
	dispatch.ClassificationRouteSnapshot.ImageScope, dispatch.RoutePolicySnapshot.ImageScope = scope, scope
	invocation := k12.ImageTaskInvocation{
		InvocationID: dispatch.ClassificationInvocationID, AgentName: dispatch.AgentName, DispatchID: dispatch.DispatchID,
		Operation: k12.ImageTaskOperationClassification, OperationKey: "dispatch:" + dispatch.DispatchID + ":classification",
		RequestDigest: "classification-request", RouteSnapshot: dispatch.ClassificationRouteSnapshot,
		Status: k12.ImageTaskInvocationPrepared, Attempt: 1, CreatedAt: 100, UpdatedAt: 100,
	}
	if _, _, err := store.PrepareImageTaskDispatch(t.Context(), dispatch, invocation); err != nil {
		t.Fatal(err)
	}
	if _, won, err := store.ClaimImageTaskInvocationSend(t.Context(), dispatch.AgentName, invocation.InvocationID, "same-classification-request", 100); err != nil || !won {
		t.Fatalf("claim: won=%v err=%v", won, err)
	}
	raw, err := json.Marshal(map[string]any{"intent": "unknown", "confirmation_candidates": candidates, "confidence": .95})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveImageTaskClassificationResponse(t.Context(), dispatch.AgentName, invocation.InvocationID, string(raw)); err != nil {
		t.Fatal(err)
	}
	prior, err := store.GetImageTaskInvocation(t.Context(), dispatch.AgentName, invocation.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	response, present, err := k12storage.ReadImageTaskClassificationResponse(prior)
	if err != nil || !present {
		t.Fatalf("missing original response: present=%v err=%v", present, err)
	}
	decision := k12storage.ImageTaskRoutingDecision{Intent: k12.ImageTaskIntentUnknown, Evidence: []string{"worksheet; student-answer state deferred to question reading"}, Confidence: .95, ConfirmationCandidates: candidates, InvocationResultDigest: "sha256:original-math-family-classification"}
	return store, db, dispatch, decision, response
}

func TestImageTaskMathFamilyCreatesReadingTargetAndKeepsClassification(t *testing.T) {
	scope := blankRecognizedIntentScope()
	scope.MessageIntent = "帮我给孩子讲这道数学题"
	store, db, dispatch, decision, raw := prepareMathFamilyRouting(t, scope, []k12.ImageTaskIntent{k12.ImageTaskIntentCompletedHomework, k12.ImageTaskIntentBlankWorksheet})
	routed, target, err := store.CommitImageTaskRouting(t.Context(), dispatch.AgentName, dispatch.DispatchID, dispatch.Version, decision)
	if err != nil || routed.Status != k12.ImageTaskStatusRouted || routed.TaskIntent != k12.ImageTaskIntentCompletedHomework || target.HomeworkSubmission == nil || len(routed.ConfirmationCandidates) != 0 {
		t.Fatalf("math family must enter existing reading pipeline: dispatch=%+v target=%+v err=%v", routed, target, err)
	}
	stored, err := store.GetImageTaskInvocation(t.Context(), dispatch.AgentName, dispatch.ClassificationInvocationID)
	if err != nil {
		t.Fatal(err)
	}
	var original k12storage.ImageTaskRoutingDecision
	if err := json.Unmarshal([]byte(stored.ResultJSON), &original); err != nil {
		t.Fatal(err)
	}
	if stored.Status != k12.ImageTaskInvocationSucceeded || stored.ResultDigest != decision.InvocationResultDigest || original.Intent != k12.ImageTaskIntentUnknown || !reflect.DeepEqual(original.ConfirmationCandidates, decision.ConfirmationCandidates) || original.ImageTaskClassificationResponse != raw {
		t.Fatalf("execution routing rewrote original classification: %+v", original)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM k12_homework_submissions WHERE dispatch_id=?`, dispatch.DispatchID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("homework count=%d err=%v", count, err)
	}
}

func TestImageTaskMathFamilyKeepsExplicitAndCrossTypeDecisions(t *testing.T) {
	for _, scenario := range []string{"explicit grade", "explicit solve", "cross type", "duplicate candidate", "legacy policy"} {
		t.Run(scenario, func(t *testing.T) {
			scope := blankRecognizedIntentScope()
			candidates := []k12.ImageTaskIntent{k12.ImageTaskIntentCompletedHomework, k12.ImageTaskIntentBlankWorksheet}
			switch scenario {
			case "explicit grade":
				scope.Action = "grade"
			case "explicit solve":
				scope.Action = "solve"
			case "cross type":
				candidates[1] = k12.ImageTaskIntentWriting
			case "duplicate candidate":
				candidates[1] = k12.ImageTaskIntentCompletedHomework
			case "legacy policy":
				scope.SourceReviewPolicy = k12.RecognitionImageSourceUprightReviewPolicyV2
			}
			store, _, dispatch, decision, _ := prepareMathFamilyRouting(t, scope, candidates)
			routed, target, err := store.CommitImageTaskRouting(t.Context(), dispatch.AgentName, dispatch.DispatchID, dispatch.Version, decision)
			if err != nil || routed.Status != k12.ImageTaskStatusAwaitingConfirmation || target.HomeworkSubmission != nil || routed.TaskIntent != k12.ImageTaskIntentUnknown {
				t.Fatalf("unrelated classification was reinterpreted: dispatch=%+v target=%+v err=%v", routed, target, err)
			}
		})
	}
}

func TestImageTaskMathFamilyDoesNotActivateHistoricalAwaiting(t *testing.T) {
	store, db, dispatch, decision, response := prepareMathFamilyRouting(t, blankRecognizedIntentScope(), []k12.ImageTaskIntent{k12.ImageTaskIntentCompletedHomework, k12.ImageTaskIntentBlankWorksheet})
	// 保留旧版本已完成分类、尚未由家长选择的持久状态。
	decision.ImageTaskClassificationResponse = response
	encoded, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE k12_image_task_invocations SET status='succeeded',result_digest=?,result_json=? WHERE invocation_id=?`, decision.InvocationResultDigest, string(encoded), dispatch.ClassificationInvocationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE k12_image_task_dispatches SET status='awaiting_confirmation',confirmation_candidates_json='["completed_homework","blank_worksheet"]',version=1 WHERE dispatch_id=?`, dispatch.DispatchID); err != nil {
		t.Fatal(err)
	}
	routed, target, err := store.CommitImageTaskRouting(t.Context(), dispatch.AgentName, dispatch.DispatchID, 1, decision)
	if err != nil || routed.Status != k12.ImageTaskStatusAwaitingConfirmation || target.HomeworkSubmission != nil {
		t.Fatalf("historical awaiting task activated: %+v err=%v", routed, err)
	}
	selected, selectedTarget, err := store.ConfirmImageTaskIntent(t.Context(), dispatch.AgentName, dispatch.DispatchID, routed.Version, k12.ImageTaskIntentCompletedHomework)
	if err != nil || selectedTarget.HomeworkSubmission == nil || len(selected.ConfirmationCandidates) != 2 {
		t.Fatalf("historical explicit choice changed: %+v err=%v", selected, err)
	}
	job, err := k12.NewGradingJobRecord(dispatch.AgentName, "session", k12.GradingJobFields{SubmissionID: "photo-selected", SourceKind: "image_task", IdempotencyKey: k12.BuildGradingIdempotencyKey("image_task", dispatch.DispatchID, 0), ConfirmationState: k12.GradingConfirmationPending, AnchorState: k12.GradingAnchorPending, ModelSnapshot: k12.GradingModelSnapshot{Provider: "test", Model: "vision", Route: "test/vision"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE k12_grading_jobs SET status='recognizing' WHERE record_id=?`, job.RecordID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindHomeworkSubmissionGradingJob(t.Context(), dispatch.AgentName, selectedTarget.HomeworkSubmission.SubmissionID, job.RecordID, selectedTarget.HomeworkSubmission.Version); err != nil {
		t.Fatal(err)
	}
	snapshot := problemAttemptFixture(dispatch.AgentName, "photo-selected")
	for i := range snapshot.Problems {
		snapshot.Problems[i].PageAssetID, snapshot.Problems[i].Subject = dispatch.SourceAssetRefs[0], "数学"
	}
	setClearBlankIntentFacts(&snapshot)
	if err := store.PutProblemAttemptSnapshot(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetImageTaskDispatch(t.Context(), dispatch.AgentName, dispatch.DispatchID)
	if err != nil || after.TaskIntent != selected.TaskIntent || after.Version != selected.Version {
		t.Fatalf("same reading reinterpreted a historical parent choice: %+v err=%v", after, err)
	}
}

func TestProblemAttemptMathFamilyDerivesIntentFromSameReading(t *testing.T) {
	for _, state := range []string{"blank", "present", "unclear"} {
		t.Run(state, func(t *testing.T) {
			scope := blankRecognizedIntentScope()
			scope.MessageIntent = "帮我给孩子讲这道数学题"
			store, db, dispatch, decision, _ := prepareMathFamilyRouting(t, scope, []k12.ImageTaskIntent{k12.ImageTaskIntentCompletedHomework, k12.ImageTaskIntentBlankWorksheet})
			routed, target, err := store.CommitImageTaskRouting(t.Context(), dispatch.AgentName, dispatch.DispatchID, dispatch.Version, decision)
			if err != nil || target.HomeworkSubmission == nil {
				t.Fatalf("missing reading target: %v", err)
			}
			job, err := k12.NewGradingJobRecord(dispatch.AgentName, "session", k12.GradingJobFields{SubmissionID: "photo-current", SourceKind: "image_task", IdempotencyKey: k12.BuildGradingIdempotencyKey("image_task", dispatch.DispatchID, 0), ConfirmationState: k12.GradingConfirmationPending, AnchorState: k12.GradingAnchorPending, ModelSnapshot: k12.GradingModelSnapshot{Provider: "test", Model: "vision", Route: "test/vision"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Put(t.Context(), job); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE k12_grading_jobs SET status='recognizing' WHERE record_id=?`, job.RecordID); err != nil {
				t.Fatal(err)
			}
			homework, err := store.BindHomeworkSubmissionGradingJob(t.Context(), dispatch.AgentName, target.HomeworkSubmission.SubmissionID, job.RecordID, target.HomeworkSubmission.Version)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := problemAttemptFixture(dispatch.AgentName, "photo-current")
			for i := range snapshot.Problems {
				snapshot.Problems[i].PageAssetID = dispatch.SourceAssetRefs[0]
				snapshot.Problems[i].Subject = "数学"
			}
			if state == "blank" {
				setClearBlankIntentFacts(&snapshot)
			} else if state == "unclear" {
				for i := range snapshot.Attempts {
					snapshot.Attempts[i].AnswerState = "unclear"
					snapshot.Attempts[i].AnswerMarkdown = ""
				}
			}
			before, err := store.GetImageTaskInvocation(t.Context(), dispatch.AgentName, dispatch.ClassificationInvocationID)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.PutProblemAttemptSnapshot(t.Context(), snapshot); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetImageTaskDispatch(t.Context(), dispatch.AgentName, dispatch.DispatchID)
			if err != nil {
				t.Fatal(err)
			}
			child, err := store.GetHomeworkSubmission(t.Context(), dispatch.AgentName, homework.SubmissionID)
			want := k12.ImageTaskIntentCompletedHomework
			if state == "blank" {
				want = k12.ImageTaskIntentBlankWorksheet
			}
			if err != nil || got.TaskIntent != want || child.TaskIntent != want {
				t.Fatalf("same reading intent=%s/%s want=%s err=%v", got.TaskIntent, child.TaskIntent, want, err)
			}
			after, err := store.GetImageTaskInvocation(t.Context(), dispatch.AgentName, dispatch.ClassificationInvocationID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("reading changed original classification: %v", err)
			}
			if state == "blank" && got.Version != routed.Version+1 {
				t.Fatalf("intent correction version=%d want=%d", got.Version, routed.Version+1)
			}
		})
	}
}
