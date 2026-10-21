package usecase

import (
	"context"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestImageTaskAutomaticDeadlineExpiresPreparedInvocationBeforeProvider(t *testing.T) {
	now := int64(1000)
	classifier := &imageTaskClassifierStub{
		result: ImageTaskClassification{
			Intent:     k12.ImageTaskIntentArtwork,
			Confidence: 0.99,
		},
	}
	coordinator, _ := newImageTaskCoordinatorForTest(t, classifier)
	coordinator.Now = func() int64 { return now }

	created, fresh, err := coordinator.Create(
		context.Background(),
		testCreateImageTaskInput(),
	)
	if err != nil || !fresh {
		t.Fatalf("create: fresh=%v err=%v", fresh, err)
	}
	if created.Dispatch.AutomaticBudgetSeconds != k12.ImageTaskAutomaticBudgetSeconds ||
		created.Dispatch.AutomaticStartedAt != 1000 ||
		created.Dispatch.AutomaticDeadlineAt != 1300 ||
		created.Dispatch.AutomaticRemainingSeconds != 300 {
		t.Fatalf("wrong frozen automatic window: %+v", created.Dispatch)
	}
	invocation, err := coordinator.Records.GetImageTaskInvocation(
		context.Background(),
		"mingming",
		created.Dispatch.ClassificationInvocationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.DeadlineAt != created.Dispatch.AutomaticDeadlineAt {
		t.Fatalf(
			"invocation deadline=%d dispatch deadline=%d",
			invocation.DeadlineAt,
			created.Dispatch.AutomaticDeadlineAt,
		)
	}

	now = 1301
	view, err := coordinator.Run(
		context.Background(),
		"mingming",
		created.Dispatch.DispatchID,
	)
	if err != nil {
		t.Fatalf("expired prepared invocation must park durably: %v", err)
	}
	if classifier.calls != 0 {
		t.Fatalf("expired prepared invocation called provider %d times", classifier.calls)
	}
	if view.Dispatch.Status != k12.ImageTaskStatusFailed ||
		!view.Dispatch.RetrySafe ||
		view.Dispatch.FailureKind != "interactive_deadline_exceeded" {
		t.Fatalf("wrong expired dispatch: %+v", view.Dispatch)
	}
	invocation, err = coordinator.Records.GetImageTaskInvocation(
		context.Background(),
		"mingming",
		created.Dispatch.ClassificationInvocationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Status != k12.ImageTaskInvocationFailed ||
		!invocation.RetrySafe ||
		invocation.ErrorKind != "interactive_deadline_exceeded" {
		t.Fatalf("wrong expired invocation: %+v", invocation)
	}
}

func TestImageTaskRecoveryConvergesExpiredSentInvocationWithoutResend(t *testing.T) {
	now := int64(2000)
	classifier := &imageTaskClassifierStub{
		result: ImageTaskClassification{
			Intent:     k12.ImageTaskIntentArtwork,
			Confidence: 0.99,
		},
	}
	coordinator, _ := newImageTaskCoordinatorForTest(t, classifier)
	coordinator.Now = func() int64 { return now }
	created, fresh, err := coordinator.Create(
		context.Background(),
		testCreateImageTaskInput(),
	)
	if err != nil || !fresh {
		t.Fatalf("create: fresh=%v err=%v", fresh, err)
	}
	invocation, claimed, err := coordinator.Records.ClaimImageTaskInvocationSend(
		context.Background(),
		"mingming",
		created.Dispatch.ClassificationInvocationID,
		"provider-request-1",
		now,
	)
	if err != nil || !claimed {
		t.Fatalf("claim: claimed=%v invocation=%+v err=%v", claimed, invocation, err)
	}

	now = 2301
	recovered, err := coordinator.Recover(context.Background(), []string{"mingming"})
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 0 || classifier.calls != 0 {
		t.Fatalf("recovered=%d provider calls=%d", recovered, classifier.calls)
	}
	view, err := coordinator.Get(
		context.Background(),
		"mingming",
		created.Dispatch.DispatchID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if view.Dispatch.Status != k12.ImageTaskStatusFailed ||
		view.Dispatch.RetrySafe ||
		view.Dispatch.FailureKind != "interactive_deadline_outcome_unknown" {
		t.Fatalf("wrong recovered dispatch: %+v", view.Dispatch)
	}
	invocation, err = coordinator.Records.GetImageTaskInvocation(
		context.Background(),
		"mingming",
		created.Dispatch.ClassificationInvocationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Status != k12.ImageTaskInvocationOutcomeUnknown ||
		invocation.RetrySafe {
		t.Fatalf("wrong recovered invocation: %+v", invocation)
	}
}

func TestImageTaskPersistsAutomaticAttemptIdentityIntoNestedGrading(t *testing.T) {
	now := int64(3000)
	classifier := &imageTaskClassifierStub{
		result: ImageTaskClassification{
			Intent:     k12.ImageTaskIntentCompletedHomework,
			Confidence: 0.99,
		},
	}
	coordinator, grading := newImageTaskCoordinatorForTest(t, classifier)
	coordinator.Now = func() int64 { return now }
	created, fresh, err := coordinator.Create(
		context.Background(),
		testCreateImageTaskInput(),
	)
	if err != nil || !fresh {
		t.Fatalf("create: fresh=%v err=%v", fresh, err)
	}
	if _, err := coordinator.Run(
		context.Background(),
		"mingming",
		created.Dispatch.DispatchID,
	); err != nil {
		t.Fatal(err)
	}
	wantAttemptID := created.Dispatch.DispatchID + ":3000"
	if grading.input.ParentAutomaticAttemptID != wantAttemptID ||
		grading.input.ParentAutomaticDeadlineAt != 3300 {
		t.Fatalf(
			"nested grading parent window=%q/%d want %q/3300",
			grading.input.ParentAutomaticAttemptID,
			grading.input.ParentAutomaticDeadlineAt,
			wantAttemptID,
		)
	}
}

type imageTaskClampedRetryStub struct {
	*imageTaskGradingStub
}

func (s *imageTaskClampedRetryStub) ImageTaskHomeworkProjection(context.Context, string, string) (ImageTaskHomeworkProjection, error) {
	return ImageTaskHomeworkProjection{
		Stage: k12.GradingStageFailedRetryable, Retryable: true, retryFailureKind: "assess_item_failed",
	}, nil
}

func TestImageTaskHomeworkRetryUsesExpiredChildWindowBeforeParentDeadline(t *testing.T) {
	now := int64(1000)
	c, grading := newImageTaskCoordinatorForTest(t, &imageTaskClassifierStub{
		result: ImageTaskClassification{Intent: k12.ImageTaskIntentCompletedHomework, Confidence: 0.99},
	})
	c.Now = func() int64 { return now }
	c.Grading = &imageTaskClampedRetryStub{grading}
	created, _, err := c.Create(t.Context(), testCreateImageTaskInput())
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{Records: c.Records, Now: c.Now}
	job, _, err := d.CreateGradingJob(t.Context(), "mingming", "child-window-session", CreateGradingJobInput{
		SubmissionID: "child-window-submission", SourceKind: "image_task", SourceKey: created.Dispatch.DispatchID,
		ParentAutomaticAttemptID: created.Dispatch.DispatchID + ":1000", ParentAutomaticDeadlineAt: 1180,
		ModelSnapshot: k12.GradingModelSnapshot{Provider: "test-provider", Model: "test-vision"},
	})
	if err != nil {
		t.Fatal(err)
	}
	grading.jobID = job.Record.RecordID
	if _, err = d.AdvanceGradingStage(t.Context(), "mingming", grading.jobID, AdvanceGradingInput{Outcome: GradingOutcomeOK}); err != nil {
		t.Fatal(err)
	}
	if _, err = d.AdvanceGradingStage(t.Context(), "mingming", grading.jobID, AdvanceGradingInput{Outcome: GradingOutcomeFailed, FailureKind: "assess_item_failed", Retryable: true}); err != nil {
		t.Fatal(err)
	}
	routed, err := c.Run(t.Context(), "mingming", created.Dispatch.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Records.RestartImageTaskAutomaticWindow(t.Context(), "mingming", routed.Dispatch.DispatchID, routed.Dispatch.Version, 1179); err == nil {
		t.Fatal("unexpired child window allowed a parent restart")
	}
	// 实际子任务已过180秒窗口，父接纳窗口仍为300秒；资格由子编排器确认。
	now = 1181
	grading.parentRetryAllowed = true
	got, err := c.Retry(t.Context(), "mingming", routed.Dispatch.DispatchID, routed.Dispatch.Version)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dispatch.AutomaticStartedAt != 1181 || grading.parentRetryAttemptID != routed.Dispatch.DispatchID+":1181" || grading.parentRetryDeadlineAt != 1481 {
		t.Fatalf("expired child reused old parent window: dispatch=%+v grading=%+v", got.Dispatch, grading)
	}
}

func TestImageTaskExplicitHomeworkRetryStartsFreshParentAutomaticAttempt(t *testing.T) {
	now := int64(4000)
	classifier := &imageTaskClassifierStub{
		result: ImageTaskClassification{
			Intent:     k12.ImageTaskIntentCompletedHomework,
			Confidence: 0.99,
		},
	}
	coordinator, grading := newImageTaskCoordinatorForTest(t, classifier)
	coordinator.Now = func() int64 { return now }
	created, _, err := coordinator.Create(
		context.Background(),
		testCreateImageTaskInput(),
	)
	if err != nil {
		t.Fatal(err)
	}
	routed, err := coordinator.Run(
		context.Background(),
		"mingming",
		created.Dispatch.DispatchID,
	)
	if err != nil {
		t.Fatal(err)
	}
	now = 4301
	grading.parentRetryAllowed = true
	retried, err := coordinator.Retry(
		context.Background(),
		"mingming",
		routed.Dispatch.DispatchID,
		routed.Dispatch.Version,
	)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Dispatch.AutomaticStartedAt != 4301 ||
		retried.Dispatch.AutomaticDeadlineAt != 4601 ||
		grading.parentRetryAttemptID != routed.Dispatch.DispatchID+":4301" ||
		grading.parentRetryDeadlineAt != 4601 {
		t.Fatalf(
			"retry dispatch=%+v grading attempt=%q deadline=%d",
			retried.Dispatch,
			grading.parentRetryAttemptID,
			grading.parentRetryDeadlineAt,
		)
	}
}
