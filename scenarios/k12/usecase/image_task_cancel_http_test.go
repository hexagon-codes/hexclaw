package usecase

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type cancellableImageTaskHTTP struct{ url string }

func (s cancellableImageTaskHTTP) ClassifyImageTask(ctx context.Context, _ ImageTaskClassificationInput) (ImageTaskClassification, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, nil)
	if err != nil {
		return ImageTaskClassification{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ImageTaskClassification{}, err
	}
	_ = resp.Body.Close()
	return ImageTaskClassification{Intent: k12.ImageTaskIntentCompletedHomework, IntentEvidence: []string{"visible completed homework"}, Confidence: .99}, nil
}

func (s cancellableImageTaskHTTP) RecognizeImageTaskWriting(ctx context.Context, _ []byte) (ImageTaskWritingOCRResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, nil)
	if err != nil {
		return ImageTaskWritingOCRResult{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ImageTaskWritingOCRResult{}, err
	}
	_ = resp.Body.Close()
	return ImageTaskWritingOCRResult{Raw: "synthetic writing", CanonicalContent: "synthetic writing", Confidence: .99}, nil
}

func TestImageTaskCancelClassificationStopsOriginalHTTP(t *testing.T) {
	entered, disconnected, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
			close(disconnected)
		case <-release:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	c, _ := newImageTaskCoordinatorForTest(t, nil)
	c.Classifier = cancellableImageTaskHTTP{url: server.URL}
	defer func() {
		close(release)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.Shutdown(ctx)
	}()
	v, _, err := c.Create(t.Context(), testCreateImageTaskInput())
	if err != nil {
		t.Fatal(err)
	}
	if !c.StartAsync("mingming", v.Dispatch.DispatchID) {
		t.Fatal("worker was not scheduled")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("original HTTP did not arrive")
	}
	cancelled, err := c.Cancel(t.Context(), "mingming", v.Dispatch.DispatchID, v.Dispatch.Version)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Dispatch.Status != k12.ImageTaskStatusCancelled {
		t.Fatalf("cancel state=%s", cancelled.Dispatch.Status)
	}
	select {
	case <-disconnected:
		t.Log("original HTTP was cancelled")
	case <-time.After(250 * time.Millisecond):
		inv, readErr := c.Records.GetImageTaskInvocation(t.Context(), "mingming", v.Dispatch.ClassificationInvocationID)
		t.Fatalf("cancel returned terminal but original HTTP remains active: invocation=%s readErr=%v", inv.Status, readErr)
	}
	assertCancelledImageTaskHTTPReceipt(t, c, v.Dispatch.DispatchID, v.Dispatch.ClassificationInvocationID)
}

func TestImageTaskCancelManualWritingStopsOriginalHTTP(t *testing.T) {
	entered, disconnected, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
			close(disconnected)
		case <-release:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	c, _ := newImageTaskCoordinatorForTest(t, nil)
	c.WritingOCR = cancellableImageTaskHTTP{url: server.URL}
	defer func() {
		close(release)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.Shutdown(ctx)
	}()
	in := testCreateImageTaskInput()
	in.CreativeEntry = &k12.ImageTaskCreativeEntry{Kind: k12.CreativeWorkEntryNewWork, TaskIntent: k12.ImageTaskIntentWriting}
	v, _, err := c.Create(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !c.StartAsync("mingming", v.Dispatch.DispatchID) {
		t.Fatal("worker was not scheduled")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("original HTTP did not arrive")
	}
	current, err := c.Get(t.Context(), "mingming", v.Dispatch.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := c.Cancel(t.Context(), "mingming", v.Dispatch.DispatchID, current.Dispatch.Version)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Dispatch.Status != k12.ImageTaskStatusCancelled {
		t.Fatalf("cancel state=%s", cancelled.Dispatch.Status)
	}
	select {
	case <-disconnected:
		t.Log("original writing HTTP was cancelled")
	case <-time.After(250 * time.Millisecond):
		inv, readErr := c.Records.GetLatestWritingOCRInvocation(t.Context(), "mingming", v.Creative.IntakeID)
		t.Fatalf("cancel returned terminal but original writing HTTP remains active: invocation=%s readErr=%v", inv.Status, readErr)
	}
	inv, err := c.Records.GetLatestWritingOCRInvocation(t.Context(), "mingming", v.Creative.IntakeID)
	if err != nil {
		t.Fatal(err)
	}
	assertCancelledImageTaskHTTPReceipt(t, c, v.Dispatch.DispatchID, inv.InvocationID)
}

func TestImageTaskCancelSynchronousClassificationRetryStopsOriginalHTTP(t *testing.T) {
	entered, disconnected, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
			close(disconnected)
		case <-release:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	c, _ := newImageTaskCoordinatorForTest(t, &imageTaskClassifierStub{
		err: definitiveImageTaskTestError{message: "known unavailable"},
	})
	defer func() {
		close(release)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.Shutdown(ctx)
	}()
	v, _, err := c.Create(t.Context(), testCreateImageTaskInput())
	if err != nil {
		t.Fatal(err)
	}
	failed, runErr := c.Run(t.Context(), "mingming", v.Dispatch.DispatchID)
	if runErr == nil || !failed.Dispatch.RetrySafe {
		t.Fatalf("initial call is not a known retryable failure: %v", runErr)
	}
	c.Classifier = cancellableImageTaskHTTP{url: server.URL}
	done := make(chan error, 1)
	go func() {
		_, err := c.Retry(t.Context(), "mingming", v.Dispatch.DispatchID, failed.Dispatch.Version)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("retry HTTP did not arrive")
	}
	current, err := c.Get(t.Context(), "mingming", v.Dispatch.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := c.Cancel(t.Context(), "mingming", v.Dispatch.DispatchID, current.Dispatch.Version)
	if err != nil || cancelled.Dispatch.Status != k12.ImageTaskStatusCancelled {
		t.Fatalf("cancel: status=%s err=%v", cancelled.Dispatch.Status, err)
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("synchronous retry remained active after cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("synchronous retry did not drain")
	}
	var invocationID string
	if err := c.Records.DB().QueryRowContext(t.Context(), `SELECT invocation_id FROM k12_image_task_invocations WHERE agent_name=? AND dispatch_id=? AND operation='classification' ORDER BY attempt DESC LIMIT 1`, "mingming", current.Dispatch.DispatchID).Scan(&invocationID); err != nil {
		t.Fatal(err)
	}
	assertCancelledImageTaskHTTPReceipt(t, c, current.Dispatch.DispatchID, invocationID)
}

func assertCancelledImageTaskHTTPReceipt(t *testing.T, c *ImageTaskCoordinator, dispatchID, invocationID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := c.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	inv, err := c.Records.GetImageTaskInvocation(ctx, "mingming", invocationID)
	if err != nil || inv.Status != k12.ImageTaskInvocationOutcomeUnknown || inv.RetrySafe {
		t.Fatalf("sent cancellation lost unknown receipt: status=%s retry_safe=%v err=%v", inv.Status, inv.RetrySafe, err)
	}
	dispatch, err := c.Records.GetImageTaskDispatch(ctx, "mingming", dispatchID)
	if err != nil || dispatch.Status != k12.ImageTaskStatusCancelled {
		t.Fatalf("unknown receipt changed cancellation: status=%s err=%v", dispatch.Status, err)
	}
}
