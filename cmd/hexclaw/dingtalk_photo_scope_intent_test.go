package main

import (
	"context"
	"fmt"
	"testing"

	k12 "github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type scopedSolveImageTasks struct {
	*fakeK12ImageTaskFacade
	action string
}

func (f *scopedSolveImageTasks) Get(ctx context.Context, agent, id string) (k12usecase.ImageTaskView, error) {
	view, err := f.fakeK12ImageTaskFacade.Get(ctx, agent, id)
	view.Dispatch.TaskIntent = k12.ImageTaskIntentCompletedHomework
	view.HomeworkProjection = &k12usecase.ImageTaskHomeworkProjection{Stage: k12.GradingStageCompleted}
	if f.action != "" {
		view.Dispatch.ClassificationRouteSnapshot.ImageScope = &k12.RecognitionImageScopeInputV1{Version: k12.RecognitionImageScopeVersionV1, Action: f.action}
	}
	return view, err
}

type solveFinalArtifactReader struct {
	finalArtifactReaderFake
	annotatedReads int
}

func (f *solveFinalArtifactReader) OpenGradingFinalAnnotatedAsset(context.Context, string, string) (k12.GradingFinalAnnotatedAsset, error) {
	f.annotatedReads++
	return k12.GradingFinalAnnotatedAsset{}, fmt.Errorf("No annotated image exists for a solved worksheet")
}

func TestDingTalkPhotoExplicitSolveUsesFrozenGuideWithoutAnnotatedImage(t *testing.T) {
	for _, action := range []string{"solve", "grade", ""} {
		t.Run("action="+action, func(t *testing.T) {
			bundle := inboundPhotoBundleFixture([]byte("original-photo"))
			bundle.Receipt.AgentName = "child-tutor"
			bundle.Dispatch.ProcessingStatus = k12usecase.InboundPhotoImageTaskSubmitted
			bundle.Dispatch.RoutingDecision = k12usecase.InboundPhotoRouteNewSubmission
			bundle.Dispatch.ImageTaskID = "dispatch-1"
			artifact, _ := finalArtifactFixture(nil)
			artifact.AgentName = "child-tutor"
			artifact.AnnotatedAssetOwnerScope, artifact.AnnotatedAssetID, artifact.AnnotatedMIME, artifact.AnnotatedDigest, artifact.OriginalSourceDigest = "", "", "", "", ""
			artifact.CanonicalMarkdown = "# 家长讲题指南"
			artifact.ArtifactDigest = k12.ComputeGradingFinalArtifactDigest(artifact)
			reader := &solveFinalArtifactReader{finalArtifactReaderFake: finalArtifactReaderFake{artifact: artifact}}
			coordinator := &inboundPhotoCoordinatorFake{bundle: bundle}
			images := &scopedSolveImageTasks{fakeK12ImageTaskFacade: &fakeK12ImageTaskFacade{dispatchStatus: k12.ImageTaskStatusRouted, result: k12usecase.ImageTaskResult{FinalArtifact: &artifact}}, action: action}
			runtime := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{BaseContext: context.Background(), Inbound: coordinator, ImageTasks: images, Artifacts: reader, ReplyBatches: &finalReplyBatchFake{}})
			_, err := runtime.advanceImageTask(t.Context(), bundle)
			if action == "solve" {
				if err != nil || reader.annotatedReads != 0 || coordinator.recordedFinalArtifactID != "final-1" {
					t.Fatalf("solve still required grading media: reads=%d artifact=%q err=%v", reader.annotatedReads, coordinator.recordedFinalArtifactID, err)
				}
			} else if err == nil || reader.annotatedReads != 1 || coordinator.recordedFinalArtifactID != "" {
				t.Fatal("legacy/grading source validation changed")
			}
		})
	}
}
