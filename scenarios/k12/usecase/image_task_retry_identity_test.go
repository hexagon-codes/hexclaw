package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func TestPreparedClassificationRetryRecoversCurrentInvocationAndPreservesOldFailure(t *testing.T) {
	classifier := &imageTaskClassifierStub{err: definitiveImageTaskTestError{message: "known unavailable"}}
	c, _ := newImageTaskCoordinatorForTest(t, classifier)
	v, _, err := c.Create(t.Context(), testCreateImageTaskInput())
	if err != nil {
		t.Fatal(err)
	}
	failed, runErr := c.Run(t.Context(), "mingming", v.Dispatch.DispatchID)
	if runErr == nil || !failed.Dispatch.RetrySafe {
		t.Fatalf("not a known retryable failure: %v", runErr)
	}
	originalID := failed.Dispatch.ClassificationInvocationID
	prepared, retry, err := c.Records.PrepareImageTaskRetry(t.Context(), "mingming", v.Dispatch.DispatchID, failed.Dispatch.Version, "prepared-classification-retry")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ClassificationInvocationID != originalID || retry.InvocationID == originalID || retry.Status != k12.ImageTaskInvocationPrepared {
		t.Fatalf("new retry changed the frozen identity or was not prepared: header=%s new=%s", prepared.ClassificationInvocationID, retry.InvocationID)
	}
	latest, err := c.Records.GetLatestImageTaskClassificationInvocation(t.Context(), " mingming ", " "+v.Dispatch.DispatchID+" ")
	if err != nil || latest.InvocationID != retry.InvocationID {
		t.Fatalf("current classification attempt is not the prepared retry: %+v err=%v", latest, err)
	}
	for _, identity := range [][2]string{{"", v.Dispatch.DispatchID}, {"mingming", ""}, {"mingming", "missing-dispatch"}, {"other-agent", v.Dispatch.DispatchID}} {
		if _, err := c.Records.GetLatestImageTaskClassificationInvocation(t.Context(), identity[0], identity[1]); !errors.Is(err, k12storage.ErrImageTaskNotFound) {
			t.Fatalf("classification lookup escaped its identity: agent=%q dispatch=%q err=%v", identity[0], identity[1], err)
		}
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.Shutdown(ctx)
	}()
	recovered, err := c.Recover(t.Context(), []string{"mingming"})
	if err != nil || recovered != 1 {
		t.Fatalf("prepared new classification retry was not recovered: recovered=%d err=%v", recovered, err)
	}
	waitCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := c.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
	original, err := c.Records.GetImageTaskInvocation(t.Context(), "mingming", originalID)
	if err != nil || original.Status != k12.ImageTaskInvocationFailed || original.Attempt != 1 {
		t.Fatalf("original failure was changed: status=%s attempt=%d err=%v", original.Status, original.Attempt, err)
	}
	if classifier.calls != 2 {
		t.Fatalf("recovery replayed the wrong request: calls=%d", classifier.calls)
	}
	result, err := c.Result(t.Context(), "mingming", v.Dispatch.DispatchID)
	if err != nil || len(result.OperationReceipts) != 1 || result.OperationReceipts[0].InvocationID != retry.InvocationID || result.OperationReceipts[0].Attempt != 2 {
		t.Fatalf("result exposed the old classification attempt: %+v err=%v", result.OperationReceipts, err)
	}
}

func TestCurrentClassificationUnknownRemainsParkedWithOriginalAnchor(t *testing.T) {
	classifier := &imageTaskClassifierStub{err: definitiveImageTaskTestError{message: "known unavailable"}}
	c, _ := newImageTaskCoordinatorForTest(t, classifier)
	v, _, err := c.Create(t.Context(), testCreateImageTaskInput())
	if err != nil {
		t.Fatal(err)
	}
	failed, runErr := c.Run(t.Context(), "mingming", v.Dispatch.DispatchID)
	if runErr == nil || !failed.Dispatch.RetrySafe {
		t.Fatalf("not a known retryable failure: %v", runErr)
	}
	prepared, retry, err := c.Records.PrepareImageTaskRetry(t.Context(), "mingming", v.Dispatch.DispatchID, failed.Dispatch.Version, "unknown-classification-retry")
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := c.Records.ClaimImageTaskInvocationSend(t.Context(), "mingming", retry.InvocationID, "controlled-sent-call", 1000); err != nil || !claimed {
		t.Fatalf("sent claim: claimed=%v err=%v", claimed, err)
	}
	if err := c.Records.FailImageTaskInvocation(t.Context(), "mingming", retry.InvocationID, "controlled_unknown", true, false); err != nil {
		t.Fatal(err)
	}
	recovered, err := c.Recover(t.Context(), []string{"mingming"})
	if err != nil || recovered != 0 || classifier.calls != 1 {
		t.Fatalf("unknown retry was replayed: recovered=%d calls=%d err=%v", recovered, classifier.calls, err)
	}
	view, err := c.Get(t.Context(), "mingming", v.Dispatch.DispatchID)
	if err != nil || view.ClassificationInvocationStatus != k12.ImageTaskInvocationOutcomeUnknown || view.Dispatch.ClassificationInvocationID != prepared.ClassificationInvocationID || view.Dispatch.RetrySafe {
		t.Fatalf("unknown attempt lost its frozen/current state: %+v err=%v", view, err)
	}
	result, err := c.Result(t.Context(), "mingming", v.Dispatch.DispatchID)
	if err != nil || len(result.OperationReceipts) != 1 || result.OperationReceipts[0].InvocationID != retry.InvocationID || result.OperationReceipts[0].Status != string(k12.ImageTaskInvocationOutcomeUnknown) {
		t.Fatalf("result concealed the current unknown attempt: %+v err=%v", result.OperationReceipts, err)
	}
}
