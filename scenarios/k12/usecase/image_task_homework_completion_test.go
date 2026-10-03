package usecase

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func TestImageTaskCompletedHomeworkSurvivesGapExpiry(t *testing.T) {
	for _, scenario := range []string{"completed", "historical_gap_failure", "incomplete", "missing_artifact", "stale_artifact", "invalid_artifact", "unknown", "other_failure"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			solver, grader := &feedbackReassessmentSolver{answer: "6"}, &assetReuseGrader{}
			o := newParallelAnchorOrchestrator(t, &countingRecognizer{questions: []RecognizedQuestion{{Question: "18÷3=", Subject: "数学", StudentAnswer: "6", AnswerState: AnswerStatePresent}}}, nil, WithGradingRunDir(t.TempDir()))
			o.deps.Solver, o.deps.Grader, o.deps.VerifiedGrader = solver, grader, nil
			jobID := runItemResumeJobToAssessing(t, o, "image-task-completed-homework")
			if _, err := o.ConfirmAndRun(ctx, jobID, nil); err != nil {
				t.Fatal(err)
			}
			artifact, err := o.deps.Records.GetCurrentGradingFinalArtifactByJob(ctx, "mingming", jobID)
			if err != nil || artifact.PublishedCount != 1 || artifact.Validate() != nil {
				t.Fatalf("complete final fixture: %+v %v", artifact, err)
			}
			classifier := &imageTaskClassifierStub{}
			c, _ := newImageTaskCoordinatorForTest(t, classifier)
			c.Records, c.Grading = o.deps.Records, o
			created, _, err := c.Create(ctx, testCreateImageTaskInput())
			if err != nil {
				t.Fatal(err)
			}
			dispatch, target, err := c.Records.CommitImageTaskRouting(ctx, "mingming", created.Dispatch.DispatchID, created.Dispatch.Version, k12storage.ImageTaskRoutingDecision{
				Intent: k12.ImageTaskIntentCompletedHomework, Confidence: 1, Evidence: []string{"worksheet"}, InvocationResultDigest: "sha256:classification-complete",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Records.BindHomeworkSubmissionGradingJob(ctx, "mingming", target.HomeworkSubmission.SubmissionID, jobID, target.HomeworkSubmission.Version); err != nil {
				t.Fatal(err)
			}
			db := c.Records.DB()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "historical_gap_failure":
				exec(`UPDATE k12_image_task_dispatches SET status='failed',failure_kind='interactive_deadline_exceeded',retry_safe=1,automatic_deadline_at=0 WHERE dispatch_id=?`, dispatch.DispatchID)
			case "incomplete":
				exec(`UPDATE k12_grading_jobs SET status='assessing' WHERE record_id=?`, jobID)
			case "missing_artifact":
				exec(`DELETE FROM k12_grading_final_artifacts WHERE job_id=?`, jobID)
			case "stale_artifact":
				exec(`UPDATE k12_grading_jobs SET finalization_generation=finalization_generation+1 WHERE record_id=?`, jobID)
			case "invalid_artifact":
				exec(`UPDATE k12_grading_final_artifacts SET canonical_markdown='altered final' WHERE job_id=?`, jobID)
			case "unknown":
				exec(`UPDATE k12_image_task_dispatches SET status='failed',failure_kind='interactive_deadline_outcome_unknown',retry_safe=0 WHERE dispatch_id=?`, dispatch.DispatchID)
			case "other_failure":
				exec(`UPDATE k12_image_task_dispatches SET status='failed',failure_kind='provider_failed',retry_safe=1 WHERE dispatch_id=?`, dispatch.DispatchID)
			}
			complete := scenario == "completed" || scenario == "historical_gap_failure"
			before, err := c.Records.GetImageTaskDispatch(ctx, "mingming", dispatch.DispatchID)
			if err != nil {
				t.Fatal(err)
			}
			invBefore, err := c.Records.GetImageTaskInvocation(ctx, "mingming", dispatch.ClassificationInvocationID)
			if err != nil {
				t.Fatal(err)
			}
			c.Now = func() int64 { return dispatch.AutomaticDeadlineAt + 1 }
			expired, _, changed, err := c.Records.ExpireImageTaskInvocation(ctx, "mingming", dispatch.DispatchID, "", c.now())
			if err != nil {
				t.Fatal(err)
			}
			if complete {
				if changed || !reflect.DeepEqual(expired, before) {
					t.Fatalf("completed task expired: changed=%v %+v", changed, expired)
				}
			} else if scenario != "unknown" && scenario != "other_failure" && (!changed || expired.Status != k12.ImageTaskStatusFailed) {
				t.Fatalf("incomplete gap was falsely complete: changed=%v %+v", changed, expired)
			}
			beforeCalls, beforeGrades := solver.calls, len(grader.answers)
			var firstReceipts []byte
			for range 2 {
				view, err := c.Get(ctx, "mingming", dispatch.DispatchID)
				if err != nil {
					t.Fatal(err)
				}
				if view.homeworkCompleted != complete {
					t.Fatalf("wrong completion projection: %+v", view)
				}
				if !complete {
					if view.Dispatch.Status != k12.ImageTaskStatusFailed || view.Dispatch.FailureKind != expired.FailureKind {
						t.Fatalf("failure was hidden: %+v", view.Dispatch)
					}
					continue
				}
				if view.Dispatch.Status != k12.ImageTaskStatusRouted || view.Dispatch.FailureKind != "" || view.Dispatch.RetrySafe || view.HomeworkProjection.Stage != k12.GradingStageCompleted {
					t.Fatalf("old failure leaked into completed projection: %+v", view)
				}
				result, err := c.Result(ctx, "mingming", dispatch.DispatchID)
				if err != nil || result.FinalArtifact == nil || result.Photo == nil || result.FinalArtifact.ArtifactDigest != artifact.ArtifactDigest || result.Photo.Markdown != artifact.CanonicalMarkdown || len(result.Photo.Items) != 1 {
					t.Fatalf("completed result drifted: %+v %v", result, err)
				}
				gotReceipts, err := json.Marshal(result.OperationReceipts)
				if err != nil {
					t.Fatal(err)
				}
				if firstReceipts != nil && string(firstReceipts) != string(gotReceipts) {
					t.Fatal("repeated result changed operation receipts")
				}
				firstReceipts = gotReceipts
				if run, err := c.Run(ctx, "mingming", dispatch.DispatchID); err != nil || !run.homeworkCompleted {
					t.Fatalf("completed Run did not converge read-only: %+v %v", run, err)
				}
			}
			stored, err := c.Records.GetImageTaskDispatch(ctx, "mingming", dispatch.DispatchID)
			if err != nil || !reflect.DeepEqual(stored, expired) {
				t.Fatalf("reads rewrote historical dispatch: %+v %v", stored, err)
			}
			invAfter, err := c.Records.GetImageTaskInvocation(ctx, "mingming", dispatch.ClassificationInvocationID)
			if err != nil || !reflect.DeepEqual(invAfter, invBefore) || classifier.calls != 0 || solver.calls != beforeCalls || len(grader.answers) != beforeGrades {
				t.Fatalf("read replay changed receipts or called provider: classify=%d solve=%d grade=%d err=%v", classifier.calls, solver.calls-beforeCalls, len(grader.answers)-beforeGrades, err)
			}
			if complete {
				after, err := c.Records.GetCurrentGradingFinalArtifactByJob(ctx, "mingming", jobID)
				if err != nil || !reflect.DeepEqual(after, artifact) {
					t.Fatalf("read replay changed frozen final artifact: %+v %v", after, err)
				}
			}
		})
	}
}
