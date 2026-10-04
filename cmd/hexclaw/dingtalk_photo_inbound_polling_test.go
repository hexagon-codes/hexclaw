package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	k12 "github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type pollingImageTaskFacade struct {
	*fakeK12ImageTaskFacade
	view        k12usecase.ImageTaskView
	retryCalls  int
	resultCalls int
}

func (f *pollingImageTaskFacade) Get(context.Context, string, string) (k12usecase.ImageTaskView, error) {
	f.getCalls++
	return f.view, nil
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
