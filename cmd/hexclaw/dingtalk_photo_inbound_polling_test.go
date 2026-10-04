package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/records"
	k12 "github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type pollingImageTaskFacade struct {
	*fakeK12ImageTaskFacade
	view        k12usecase.ImageTaskView
	getErrors   []error
	retryCalls  int
	resultCalls int
}

func (f *pollingImageTaskFacade) Get(context.Context, string, string) (k12usecase.ImageTaskView, error) {
	f.getCalls++
	if f.getCalls <= len(f.getErrors) {
		return f.view, f.getErrors[f.getCalls-1]
	}
	return f.view, nil
}

type pollingResumeSequence struct {
	*inboundPhotoCoordinatorFake
	errors  []error
	bundles map[int]k12usecase.InboundPhotoBundle
	cancel  context.CancelFunc
}

func (f *pollingResumeSequence) Resume(context.Context, string, string) (k12usecase.InboundPhotoBundle, error) {
	f.resumeCalls++
	if f.resumeCalls > len(f.errors) {
		// 错误序列耗尽时主动取消，错误实现也不会让测试无止境轮询。
		f.cancel()
		return k12usecase.InboundPhotoBundle{}, context.Canceled
	}
	if err := f.errors[f.resumeCalls-1]; err != nil {
		return k12usecase.InboundPhotoBundle{}, err
	}
	if bundle, ok := f.bundles[f.resumeCalls]; ok {
		return bundle, nil
	}
	return f.bundle, nil
}

func TestDingTalkPhotoWorkerRetryIntervalDoublesAndCaps(t *testing.T) {
	current := time.Second
	for i, want := range []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second,
	} {
		if current != want {
			t.Fatalf("retry interval at attempt %d: got=%v want=%v", i+1, current, want)
		}
		current = nextK12DingtalkPhotoRetryInterval(current)
	}
	if got := nextK12DingtalkPhotoRetryInterval(time.Duration(1<<63 - 1)); got != 30*time.Second {
		t.Fatalf("maximum duration exceeded retry cap or overflowed: got=%v", got)
	}
}

func TestDingTalkPhotoWorkerRetryBackoffResetsAfterSuccess(t *testing.T) {
	temporary := errors.New("temporary storage failure")
	for _, scenario := range []struct {
		name         string
		resumeErrors []error
	}{
		{name: "recoverable errors", resumeErrors: []error{temporary, temporary, nil, temporary, nil}},
		{name: "version conflict preserves backoff", resumeErrors: []error{temporary, records.ErrVersionConflict, temporary, nil, temporary, nil}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var logs bytes.Buffer
			originalLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
			t.Cleanup(func() { slog.SetDefault(originalLogger) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			bundle := terminalImageTaskBundle()
			delivered := bundle
			delivered.Dispatch.ReplyStatus = k12usecase.InboundPhotoReplyDelivered
			inbound := &pollingResumeSequence{
				inboundPhotoCoordinatorFake: &inboundPhotoCoordinatorFake{bundle: bundle},
				errors:                      scenario.resumeErrors,
				bundles: map[int]k12usecase.InboundPhotoBundle{
					len(scenario.resumeErrors): delivered,
				}, cancel: cancel,
			}
			images := &pollingImageTaskFacade{
				fakeK12ImageTaskFacade: &fakeK12ImageTaskFacade{},
				view: k12usecase.ImageTaskView{
					Dispatch:           k12.ImageTaskDispatch{Status: k12.ImageTaskStatusRouted, TaskIntent: k12.ImageTaskIntentCompletedHomework},
					HomeworkProjection: &k12usecase.ImageTaskHomeworkProjection{Stage: k12.GradingStageAssessing},
				},
			}
			runtime := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{
				BaseContext: ctx, Inbound: inbound, ImageTasks: images,
				RetryInterval: time.Millisecond, PollInterval: time.Nanosecond,
			})
			runtime.run("child-tutor", "receipt-1")
			if inbound.resumeCalls != len(scenario.resumeErrors) || images.getCalls != 1 || inbound.terminalCalls != 0 ||
				images.retryCalls != 0 || images.startCalls != 0 || images.resultCalls != 0 || len(images.events) != 0 {
				t.Fatalf("retry worker crossed task boundary or failed to stop: resume=%d get=%d terminal=%d retry=%d start=%d result=%d events=%v", inbound.resumeCalls, images.getCalls, inbound.terminalCalls, images.retryCalls, images.startCalls, images.resultCalls, images.events)
			}
			decoder := json.NewDecoder(&logs)
			var retryDelays []int64
			for {
				var entry struct {
					Level        string `json:"level"`
					Message      string `json:"msg"`
					RetryDelayMS int64  `json:"retry_delay_ms"`
					Error        string `json:"error"`
				}
				if err := decoder.Decode(&entry); errors.Is(err, io.EOF) {
					break
				} else if err != nil {
					t.Fatalf("decode retry log: %v", err)
				}
				if entry.Level != "WARN" || entry.Message != "K12 DingTalk inbound photo worker will retry" || entry.Error != temporary.Error() {
					t.Fatalf("unexpected retry warning: %+v", entry)
				}
				retryDelays = append(retryDelays, entry.RetryDelayMS)
			}
			want := []int64{1, 2, 1}
			if len(retryDelays) != len(want) {
				t.Fatalf("unexpected retry warnings: got=%v want=%v", retryDelays, want)
			}
			for i := range want {
				if retryDelays[i] != want[i] {
					t.Fatalf("retry delay did not grow or reset: got=%v want=%v", retryDelays, want)
				}
			}
		})
	}
}

func TestDingTalkPhotoWorkerStopsWhenInboundAggregateIsMissing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inbound := &pollingResumeSequence{
		inboundPhotoCoordinatorFake: &inboundPhotoCoordinatorFake{bundle: terminalImageTaskBundle()},
		errors:                      []error{records.ErrNotFound}, cancel: cancel,
	}
	images := &pollingImageTaskFacade{fakeK12ImageTaskFacade: &fakeK12ImageTaskFacade{}}
	runtime := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{
		BaseContext: ctx, Inbound: inbound, ImageTasks: images, RetryInterval: time.Nanosecond,
	})
	runtime.run("child-tutor", "receipt-1")
	if inbound.resumeCalls != 1 || inbound.terminalCalls != 0 || inbound.recordedImageTaskID != "" ||
		images.getCalls != 0 || images.retryCalls != 0 || images.startCalls != 0 || images.resultCalls != 0 || len(images.events) != 0 {
		t.Fatalf("missing aggregate worker retried or advanced: resume=%d terminal=%d task=%q get=%d retry=%d start=%d result=%d events=%v", inbound.resumeCalls, inbound.terminalCalls, inbound.recordedImageTaskID, images.getCalls, images.retryCalls, images.startCalls, images.resultCalls, images.events)
	}
}

func TestDingTalkPhotoWorkerKeepsRetryingRecoverableStorageAndAdvanceErrors(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		resumeErrors []error
		getErrors    []error
		wantGetCalls int
	}{
		{name: "temporary storage error", resumeErrors: []error{errors.New("temporary storage failure"), nil}, wantGetCalls: 1},
		{name: "advance record not found", resumeErrors: []error{nil, nil}, getErrors: []error{records.ErrNotFound, nil}, wantGetCalls: 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			inbound := &pollingResumeSequence{
				inboundPhotoCoordinatorFake: &inboundPhotoCoordinatorFake{bundle: terminalImageTaskBundle()},
				errors:                      scenario.resumeErrors, cancel: cancel,
			}
			images := &pollingImageTaskFacade{
				fakeK12ImageTaskFacade: &fakeK12ImageTaskFacade{}, getErrors: scenario.getErrors,
				view: k12usecase.ImageTaskView{Dispatch: k12.ImageTaskDispatch{Status: k12.ImageTaskStatusCancelled}},
			}
			runtime := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{
				BaseContext: ctx, Inbound: inbound, ImageTasks: images, RetryInterval: time.Nanosecond,
			})
			runtime.run("child-tutor", "receipt-1")
			if inbound.resumeCalls != 2 || images.getCalls != scenario.wantGetCalls || inbound.terminalCalls != 0 ||
				images.retryCalls != 0 || images.startCalls != 0 || images.resultCalls != 0 || len(images.events) != 0 {
				t.Fatalf("recoverable error did not retry safely: resume=%d get=%d terminal=%d retry=%d start=%d result=%d events=%v", inbound.resumeCalls, images.getCalls, inbound.terminalCalls, images.retryCalls, images.startCalls, images.resultCalls, images.events)
			}
		})
	}
}

func (f *pollingImageTaskFacade) Retry(context.Context, string, string, int) (k12usecase.ImageTaskView, error) {
	f.retryCalls++
	return k12usecase.ImageTaskView{}, nil
}

func (f *pollingImageTaskFacade) Result(ctx context.Context, agentName, dispatchID string) (k12usecase.ImageTaskResult, error) {
	f.resultCalls++
	return f.fakeK12ImageTaskFacade.Result(ctx, agentName, dispatchID)
}

func TestDingTalkPhotoWorkerTerminatesPendingRetrySafeFailureOnce(t *testing.T) {
	bundle := terminalImageTaskBundle()
	bundle.Dispatch.RoutingDecision = k12usecase.InboundPhotoRoutePending
	inbound := &inboundPhotoCoordinatorFake{bundle: bundle}
	images := &pollingImageTaskFacade{
		fakeK12ImageTaskFacade: &fakeK12ImageTaskFacade{},
		view: k12usecase.ImageTaskView{Dispatch: k12.ImageTaskDispatch{
			DispatchID: "dispatch-1", AgentName: "child-tutor", Status: k12.ImageTaskStatusFailed,
			RetrySafe: true, FailureKind: "interactive_deadline_exceeded",
		}, ClassificationInvocationStatus: k12.ImageTaskInvocationSucceeded},
	}
	runtime := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{
		BaseContext: context.Background(), Inbound: inbound, ImageTasks: images,
	})
	for range 2 {
		done, err := runtime.advance(context.Background(), inbound.bundle)
		if err != nil || !done {
			t.Fatalf("pending failure worker state: done=%v err=%v", done, err)
		}
	}
	if inbound.terminalCalls != 1 || inbound.bundle.Dispatch.TerminalStatus != k12usecase.InboundPhotoTerminalFailed ||
		inbound.bundle.Dispatch.TerminalStage != k12usecase.InboundPhotoTerminalStageImageTask ||
		inbound.bundle.Dispatch.FailureKind != "interactive_deadline_exceeded" {
		t.Fatalf("pending failure did not persist exactly one terminal receipt: %+v calls=%d", inbound.bundle.Dispatch, inbound.terminalCalls)
	}
	if images.getCalls != 1 || images.retryCalls != 0 || images.startCalls != 0 || images.resultCalls != 0 {
		t.Fatalf("terminal replay crossed image task boundary: get=%d retry=%d start=%d result=%d", images.getCalls, images.retryCalls, images.startCalls, images.resultCalls)
	}
}

func TestDingTalkPhotoWorkerPreservesNewSubmissionSafeRetry(t *testing.T) {
	bundle := terminalImageTaskBundle()
	inbound := &inboundPhotoCoordinatorFake{bundle: bundle}
	images := &pollingImageTaskFacade{
		fakeK12ImageTaskFacade: &fakeK12ImageTaskFacade{},
		view: k12usecase.ImageTaskView{Dispatch: k12.ImageTaskDispatch{
			DispatchID: "dispatch-1", AgentName: "child-tutor", Status: k12.ImageTaskStatusFailed,
			RetrySafe: true, FailureKind: "interactive_deadline_exceeded",
		}, ClassificationInvocationStatus: k12.ImageTaskInvocationSucceeded},
	}
	runtime := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{
		BaseContext: context.Background(), Inbound: inbound, ImageTasks: images,
	})
	done, err := runtime.advanceImageTask(context.Background(), bundle)
	if err != nil || done || images.retryCalls != 1 || images.resultCalls != 0 || images.startCalls != 0 || inbound.terminalCalls != 0 {
		t.Fatalf("safe new-submission retry changed: done=%v err=%v retry=%d result=%d start=%d terminal=%d", done, err, images.retryCalls, images.resultCalls, images.startCalls, inbound.terminalCalls)
	}
}

func TestDingTalkPhotoWorkerParksUnknownBeforeRetrySafeFlag(t *testing.T) {
	bundle := terminalImageTaskBundle()
	inbound := &inboundPhotoCoordinatorFake{bundle: bundle}
	images := &pollingImageTaskFacade{
		fakeK12ImageTaskFacade: &fakeK12ImageTaskFacade{},
		view: k12usecase.ImageTaskView{Dispatch: k12.ImageTaskDispatch{
			DispatchID: "dispatch-1", AgentName: "child-tutor", Status: k12.ImageTaskStatusFailed,
			RetrySafe: true, FailureKind: "interactive_deadline_outcome_unknown",
		}, ClassificationInvocationStatus: k12.ImageTaskInvocationOutcomeUnknown},
	}
	runtime := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{
		BaseContext: context.Background(), Inbound: inbound, ImageTasks: images,
	})
	done, err := runtime.advanceImageTask(context.Background(), bundle)
	if err != nil || !done || images.retryCalls != 0 || images.resultCalls != 0 || images.startCalls != 0 || inbound.terminalCalls != 0 {
		t.Fatalf("unknown invocation did not park before retry: done=%v err=%v retry=%d result=%d start=%d terminal=%d", done, err, images.retryCalls, images.resultCalls, images.startCalls, inbound.terminalCalls)
	}
}

func TestDingTalkPhotoWorkerPollsHomeworkStatusUntilFinalArtifactReady(t *testing.T) {
	for _, scenario := range []string{"routed", "completed_child_before_parent_failure"} {
		t.Run(scenario, func(t *testing.T) {
			source := []byte("source-photo")
			bundle := terminalImageTaskBundle()
			inbound := &inboundPhotoCoordinatorFake{bundle: bundle}
			artifact, annotated := finalArtifactFixture(source)
			artifact.AgentName = bundle.Receipt.AgentName
			artifact.AnnotatedAssetID = "asset://child-tutor/" + artifact.AnnotatedDigest + ".png"
			artifact.ArtifactDigest = k12.ComputeGradingFinalArtifactDigest(artifact)
			annotated.AssetID = artifact.AnnotatedAssetID
			images := &pollingImageTaskFacade{
				fakeK12ImageTaskFacade: &fakeK12ImageTaskFacade{result: k12usecase.ImageTaskResult{
					Kind: string(k12.ImageTaskIntentCompletedHomework), FinalArtifact: &artifact,
					Photo: &k12usecase.PhotoGradeResult{Markdown: artifact.CanonicalMarkdown,
						AnnotatedImage: &k12usecase.RenderedPhoto{Data: annotated.Data, MIME: annotated.MIME}},
				}},
				view: k12usecase.ImageTaskView{
					Dispatch: k12.ImageTaskDispatch{DispatchID: "dispatch-1", AgentName: "child-tutor",
						Status: k12.ImageTaskStatusRouted, TaskIntent: k12.ImageTaskIntentCompletedHomework},
					Homework:           &k12.HomeworkSubmission{GradingJobID: artifact.JobID},
					HomeworkProjection: &k12usecase.ImageTaskHomeworkProjection{Stage: k12.GradingStageAssessing},
				},
			}
			target := inboundPhotoFrozenTarget(bundle).Target
			batches := &numberedReplyBatchFake{preparedBatch: k12.DeliveryBatch{
				BatchID: "batch-1", AgentName: bundle.Receipt.AgentName, Status: k12.DeliveryBatchDelivered,
				Receipts: []k12.DeliveryReceipt{
					{DeliveryID: "markdown-1", BindingID: bundle.Receipt.BindingID, Target: target, PartKind: messagecontent.PartMarkdown, PartOrdinal: 1},
					{DeliveryID: "image-1", BindingID: bundle.Receipt.BindingID, Target: target, PartKind: messagecontent.PartArtifact, PartOrdinal: 2, PartMIME: "image/png"},
				},
			}}
			runtime := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{
				BaseContext: context.Background(), Inbound: inbound, ImageTasks: images,
				Artifacts: finalArtifactReaderFake{artifact: artifact, asset: annotated}, ReplyBatches: batches,
			})
			for range 2 {
				done, err := runtime.advanceImageTask(context.Background(), bundle)
				if err != nil || done {
					t.Fatalf("waiting homework state: done=%v err=%v", done, err)
				}
			}
			if images.getCalls != 2 || images.resultCalls != 0 || images.retryCalls != 0 || images.startCalls != 0 || inbound.recordedFinalArtifactID != "" {
				t.Fatalf("waiting homework assembled a complete result: get=%d result=%d retry=%d start=%d artifact=%q", images.getCalls, images.resultCalls, images.retryCalls, images.startCalls, inbound.recordedFinalArtifactID)
			}
			images.view.HomeworkProjection.Stage = k12.GradingStageCompleted
			if scenario == "completed_child_before_parent_failure" {
				images.view.Dispatch.Status = k12.ImageTaskStatusFailed
				images.view.Dispatch.RetrySafe = true
				images.view.Dispatch.FailureKind = "interactive_deadline_exceeded"
			}
			done, err := runtime.advanceImageTask(context.Background(), bundle)
			if err != nil || done || images.resultCalls != 1 || images.retryCalls != 0 || images.startCalls != 0 || inbound.terminalCalls != 0 || inbound.recordedFinalArtifactID != artifact.ArtifactID {
				t.Fatalf("completed homework failed to bind its existing artifact: done=%v err=%v result=%d retry=%d start=%d terminal=%d artifact=%q", done, err, images.resultCalls, images.retryCalls, images.startCalls, inbound.terminalCalls, inbound.recordedFinalArtifactID)
			}
			// 最终产物已绑定，后续投递复用冻结正文和图片，不重新读取完整任务结果。
			ready := inbound.bundle
			ready.Dispatch.ProcessingStatus = k12usecase.InboundPhotoFinalArtifactReady
			ready.Dispatch.FinalArtifactID = inbound.recordedFinalArtifactID
			ready.Dispatch.ReplyStatus = k12usecase.InboundPhotoReplyReady
			done, err = runtime.advanceFinalReply(context.Background(), ready)
			if err != nil || !done || !inbound.completed {
				t.Fatalf("final reply did not converge delivered: done=%v completed=%v err=%v", done, inbound.completed, err)
			}
			if batches.prepareCalls != 1 || len(batches.lastMessage.Attachments) != 1 ||
				!bytes.Equal(batches.lastMessage.Attachments[0].Data, annotated.Data) ||
				batches.lastMessage.Attachments[0].MIME != annotated.MIME ||
				!bytes.Contains([]byte(batches.lastMessage.Content), []byte(artifact.CanonicalMarkdown)) || images.resultCalls != 1 {
				t.Fatalf("completed reply lost frozen text/image or reassembled result: message=%+v result=%d", batches.lastMessage, images.resultCalls)
			}
		})
	}
}
