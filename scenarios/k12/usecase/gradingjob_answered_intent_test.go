package usecase

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func prepareInitiallyBlankImageTaskJob(t *testing.T, d Deps, o *GradingOrchestrator, dispatchID string) (GradingJobView, k12.ImageTaskDispatch, k12.HomeworkSubmission) {
	t.Helper()
	ctx := context.Background()
	seedGradingImageTaskOwnerScopeForTest(t, d, dispatchID)
	dispatch, err := d.Records.GetImageTaskDispatch(ctx, "mingming", dispatchID)
	if err != nil {
		t.Fatal(err)
	}
	dispatch, target, err := d.Records.CommitImageTaskRouting(ctx, "mingming", dispatchID, dispatch.Version,
		k12storage.ImageTaskRoutingDecision{
			Intent: k12.ImageTaskIntentBlankWorksheet, Evidence: []string{"initial model classified blank"},
			Confidence: .96, InvocationResultDigest: "sha256:original-classification",
		})
	if err != nil || target.HomeworkSubmission == nil {
		t.Fatalf("prepare routed mathematics: target=%+v err=%v", target, err)
	}
	photo := orchestratorPhotoRequest()
	photo.Image = []byte("grading-image-task-owner-scope")
	photo.SourcePageAssetID, photo.TaskIntent = imageTaskAssetForTest, PhotoTaskBlankWorksheet
	job, _, err := o.StartPhotoGradingJob(ctx, StartPhotoGradingInput{
		Photo: photo, SourceKind: "image_task", SourceKey: dispatchID,
		BudgetSnapshot: frozenWiringBudget(), ParentAutomaticAttemptID: dispatchID + ":1",
		ParentAutomaticDeadlineAt: d.now() + 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(job.Fields.SubmissionID, photoSubmissionV2Prefix) ||
		job.Fields.IdempotencyKey != k12.BuildGradingIdempotencyKey("image_task", dispatchID, 0) ||
		job.Fields.ConfirmedVersion != 0 || job.Fields.AnchorState != k12.GradingAnchorPending ||
		job.Fields.ConfirmationState != k12.GradingConfirmationPending {
		t.Fatalf("actual ImageTask creation identity or pre-freeze state changed: %+v", job.Fields)
	}
	homework, err := d.Records.BindHomeworkSubmissionGradingJob(ctx, "mingming", target.HomeworkSubmission.SubmissionID,
		job.Record.RecordID, target.HomeworkSubmission.Version)
	if err != nil {
		t.Fatal(err)
	}
	return job, dispatch, homework
}

func assertCorrectedImageTaskPublicResult(t *testing.T, d Deps, o *GradingOrchestrator, dispatchID string, wantIntent k12.ImageTaskIntent, wantMode PhotoMode) {
	t.Helper()
	ctx := context.Background()
	classifier := &imageTaskClassifierStub{}
	c := &ImageTaskCoordinator{
		Records: d.Records, Grading: o, Classifier: classifier,
		ResolveRoute: imageTaskRouteForTest, Now: d.now,
		ReadAsset: func(agent, ref string) ([]byte, error) {
			if agent != "mingming" || ref != imageTaskAssetForTest {
				t.Fatalf("public result read wrong source: agent=%s ref=%s", agent, ref)
			}
			return []byte("grading-image-task-owner-scope"), nil
		},
	}
	view, err := c.Run(ctx, "mingming", dispatchID)
	if err != nil || view.Dispatch.TaskIntent != wantIntent || view.Homework == nil || view.Homework.TaskIntent != wantIntent {
		t.Fatalf("facade retained stale initial intent or CAS state: view=%+v err=%v", view, err)
	}
	result, err := c.Result(ctx, "mingming", dispatchID)
	if err != nil || result.Kind != string(wantIntent) || result.Photo == nil || result.Photo.Mode != wantMode {
		t.Fatalf("public result used wrong intent: result=%+v err=%v", result, err)
	}
	if wantMode == PhotoModeGrade {
		if result.Photo.ResultSurface != PhotoSurfaceAnnotatedHomework || result.Photo.TaskIntent != PhotoTaskCompletedHomework {
			t.Fatalf("public answered result changed to parent guide: photo=%+v", result.Photo)
		}
		hasGrade := false
		for _, receipt := range result.OperationReceipts {
			if receipt.Operation == "grade" && receipt.Status == string(k12.ModelInvocationSucceeded) {
				hasGrade = true
			}
		}
		if !hasGrade {
			t.Fatal("public answered result omitted the durable grade receipt")
		}
	} else if result.Photo.ResultSurface != PhotoSurfaceParentTeachingGuide {
		t.Fatalf("blank worksheet lost parent guide: photo=%+v", result.Photo)
	}
	if classifier.calls != 0 {
		t.Fatalf("completed facade replay resent classification: calls=%d", classifier.calls)
	}
}

func TestGradingImageTask_RecognizedAnswerCorrectsAutomaticBlankBeforeAssessment(t *testing.T) {
	for _, answer := range []string{"2", ""} {
		t.Run("answer="+answer, func(t *testing.T) {
			ctx := context.Background()
			rec := &countingRecognizer{questions: []RecognizedQuestion{{
				Question: "1+1=", Subject: "数学", StudentAnswer: answer, PageAssetID: imageTaskAssetForTest,
				RecognitionConfidence: float64Ptr(.99),
			}}}
			d := recoveryDeps(t, rec, nil, nil)
			o := newRecoverableOrchestrator(t, d, t.TempDir())
			job, dispatch, homework := prepareInitiallyBlankImageTaskJob(t, d, o, "automatic-blank")
			before, err := d.Records.GetImageTaskInvocation(ctx, "mingming", dispatch.ClassificationInvocationID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := o.RunGradingJob(ctx, job.Record.RecordID); err != nil {
				t.Fatal(err)
			}
			waitForStage(t, d, "mingming", job.Record.RecordID, k12.GradingStageCompleted)
			wantIntent, wantPhotoIntent, wantMode := k12.ImageTaskIntentBlankWorksheet, PhotoTaskBlankWorksheet, PhotoModeSolve
			if answer != "" {
				wantIntent, wantPhotoIntent, wantMode = k12.ImageTaskIntentCompletedHomework, PhotoTaskCompletedHomework, PhotoModeGrade
			}
			got, err := d.Records.GetImageTaskDispatch(ctx, "mingming", dispatch.DispatchID)
			if err != nil {
				t.Fatal(err)
			}
			gotHomework, err := d.Records.GetHomeworkSubmission(ctx, "mingming", homework.SubmissionID)
			if err != nil || got.TaskIntent != wantIntent || gotHomework.TaskIntent != wantIntent ||
				o.lookup(job.Record.RecordID).req.TaskIntent != wantPhotoIntent {
				t.Fatalf("persistent and runtime intents disagree: dispatch=%s homework=%s err=%v", got.TaskIntent, gotHomework.TaskIntent, err)
			}
			result, ok := o.PhotoResult(job.Record.RecordID)
			if !ok || result.Mode != wantMode || len(result.Items) != 1 ||
				(answer != "" && result.Items[0].Status == PhotoBlankSolved) {
				t.Fatalf("wrong assessment branch: result=%+v present=%v", result, ok)
			}
			after, err := d.Records.GetImageTaskInvocation(ctx, "mingming", dispatch.ClassificationInvocationID)
			if err != nil || !reflect.DeepEqual(before, after) || rec.calls != 1 {
				t.Fatalf("recognition repeated or original classification changed: before=%+v after=%+v calls=%d err=%v", before, after, rec.calls, err)
			}
			assertCorrectedImageTaskPublicResult(t, d, o, dispatch.DispatchID, wantIntent, wantMode)
		})
	}
}

func TestGradingRecovery_CorrectedAnsweredIntentOverridesStaleRunWithoutRecognitionResend(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	rec := &countingRecognizer{questions: []RecognizedQuestion{{
		Question: "1+1=", Subject: "数学", StudentAnswer: "2", AnswerState: AnswerStatePresent,
		PageAssetID: imageTaskAssetForTest, RecognitionConfidence: float64Ptr(.99),
	}}}
	d := recoveryDeps(t, rec, nil, nil)
	o1 := newRecoverableOrchestrator(t, d, dir)
	job, dispatch, homework := prepareInitiallyBlankImageTaskJob(t, d, o1, "corrected-recovery")
	before, err := d.Records.GetImageTaskInvocation(ctx, "mingming", dispatch.ClassificationInvocationID)
	if err != nil {
		t.Fatal(err)
	}
	jobID := job.Record.RecordID
	jobDir := o1.runPath(jobID, "")
	if err := os.Chmod(jobDir, 0o500); err != nil {
		t.Fatal(err)
	}
	_, runErr := o1.RunGradingJob(ctx, jobID)
	if err := os.Chmod(jobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if runErr == nil {
		t.Fatal("injected run file durability failure must be observable")
	}
	parked, err := d.GetGradingJob(ctx, "mingming", jobID)
	if err != nil || parked.Record.Status != k12.GradingStageOutcomeUnknown || parked.Fields.FailedStage != k12.GradingStageRecognizing {
		t.Fatalf("wrong recognition recovery checkpoint: job=%+v err=%v", parked, err)
	}
	got, err := d.Records.GetImageTaskDispatch(ctx, "mingming", dispatch.DispatchID)
	if err != nil || got.TaskIntent != k12.ImageTaskIntentCompletedHomework || rec.calls != 1 {
		t.Fatalf("recognition transaction was not durable before run failure: intent=%s calls=%d err=%v", got.TaskIntent, rec.calls, err)
	}
	o2 := newRecoverableOrchestrator(t, d, dir)
	restored, err := o2.ensureRun(ctx, jobID)
	if err != nil || restored.req.TaskIntent != PhotoTaskCompletedHomework {
		t.Fatalf("stale blank run file overrode effective intent: run=%+v err=%v", restored, err)
	}
	if _, err := o2.RecoverGradingJobs(ctx, []string{"mingming"}); err != nil {
		t.Fatal(err)
	}
	waitForStage(t, d, "mingming", jobID, k12.GradingStageCompleted)
	result, ok := o2.PhotoResult(jobID)
	if !ok || result.Mode != PhotoModeGrade || len(result.Items) != 1 || result.Items[0].Status == PhotoBlankSolved || rec.calls != 1 {
		t.Fatalf("recovery lost corrected grading intent or resent recognition: result=%+v calls=%d", result, rec.calls)
	}
	gotHomework, err := d.Records.GetHomeworkSubmission(ctx, "mingming", homework.SubmissionID)
	if err != nil || gotHomework.TaskIntent != k12.ImageTaskIntentCompletedHomework {
		t.Fatalf("recovery changed submission intent: homework=%+v err=%v", gotHomework, err)
	}
	after, err := d.Records.GetImageTaskInvocation(ctx, "mingming", dispatch.ClassificationInvocationID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("recovery rewrote original classification receipt: before=%+v after=%+v err=%v", before, after, err)
	}
	assertCorrectedImageTaskPublicResult(t, d, o2, dispatch.DispatchID, k12.ImageTaskIntentCompletedHomework, PhotoModeGrade)
}
