package usecase

import (
	"bytes"
	"context"
	"image"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func TestGradingFinalizerFreezesAnnotatedPageAssetBeforeFinalArtifactCommit(t *testing.T) {
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	ctx := context.Background()
	fixture := prepareFinalSummaryCrashFixture(t)
	repository := &PageAssetRepository{Records: fixture.orchestrator.deps.Records}
	original := validPNGFixture(t, "final-artifact-original")
	annotated := validPNGFixture(t, "final-artifact-annotated")
	originalReady, err := repository.Persist(
		ctx, "guardian-annotated", "mingming", original,
	)
	if err != nil {
		t.Fatalf("persist original PageAsset: %v", err)
	}

	classifier := &imageTaskClassifierStub{result: ImageTaskClassification{
		Intent:         k12.ImageTaskIntentCompletedHomework,
		IntentEvidence: []string{"visible answers"},
		Confidence:     1,
	}}
	coordinator := &ImageTaskCoordinator{
		Records: fixture.orchestrator.deps.Records, PageAssets: repository,
		Classifier: classifier, ResolveRoute: imageTaskRouteForTest,
		Now: func() int64 { return 1_000 },
		NewID: func(kind string) string {
			if kind == "dispatch" {
				return "dispatch-final-annotated"
			}
			return "classification-final-annotated"
		},
	}
	input := testCreateImageTaskInput()
	input.OwnerScope = "guardian-annotated"
	input.SourceRef = "message-final-annotated"
	input.SourceAssetRefs = []string{originalReady.Metadata.PageAssetID}
	prepared, created, err := coordinator.Create(ctx, input)
	if err != nil || !created {
		t.Fatalf("prepare source dispatch: created=%v err=%v", created, err)
	}

	fixture.job.Fields.SourceKind = "image_task"
	fixture.job.Fields.IdempotencyKey = k12.BuildGradingIdempotencyKey(
		"image_task", prepared.Dispatch.DispatchID, fixture.job.Fields.ConfirmedVersion,
	)
	fixture.run.result = &PhotoGradeResult{
		TaskIntent:     PhotoTaskCompletedHomework,
		ResultSurface:  PhotoSurfaceAnnotatedHomework,
		AnnotatedImage: &RenderedPhoto{Data: annotated, MIME: "image/png"},
	}
	pendingJob := fixture.job
	pendingJob.Fields.ConfirmationState = k12.GradingConfirmationPending
	if dispatchID, err := gradingFinalImageTaskDispatchID(pendingJob); err != nil ||
		dispatchID != prepared.Dispatch.DispatchID {
		t.Fatalf("pending confirmation must retain immutable source identity: dispatch=%q err=%v", dispatchID, err)
	}
	var pendingArtifact k12.GradingFinalArtifact
	if err := fixture.orchestrator.freezeGradingFinalAnnotatedAsset(
		ctx, fixture.run, pendingJob, &pendingArtifact,
	); err == nil || pendingArtifact.HasAnnotatedAsset() {
		t.Fatalf("pending page must not freeze a final annotated asset: artifact=%+v err=%v", pendingArtifact, err)
	}
	if _, err := fixture.orchestrator.deps.Records.MarkModelInvocationSucceededWithResult(
		ctx,
		fixture.invocation.AgentName,
		fixture.invocation.InvocationID,
		modelInvocationResultDigest(fixture.tips),
		fixture.resultJSON,
		"",
	); err != nil {
		t.Fatalf("persist summary result: %v", err)
	}

	artifact, err := fixture.orchestrator.finalizeGradingPage(
		ctx, fixture.run, fixture.job,
	)
	if err != nil {
		t.Fatalf("finalize annotated grading artifact: %v", err)
	}
	if !artifact.HasAnnotatedAsset() ||
		artifact.AnnotatedAssetOwnerScope != input.OwnerScope ||
		artifact.OriginalSourceDigest != originalReady.Metadata.ContentDigest ||
		artifact.ArtifactDigest != k12.ComputeGradingFinalArtifactDigest(artifact) {
		t.Fatalf("final artifact did not freeze annotated identity: %+v", artifact)
	}
	opened, err := fixture.orchestrator.deps.Records.OpenGradingFinalAnnotatedAsset(
		ctx, artifact.AgentName, artifact.ArtifactID,
	)
	if err != nil || opened.MIME != "image/png" || !bytes.Equal(opened.Data, annotated) {
		t.Fatalf("open frozen annotated bytes: opened=%+v err=%v", opened, err)
	}

	fixture.run.result = nil
	replayed, err := fixture.restartedFinalizer().finalizeGradingPage(
		ctx, fixture.run, fixture.job,
	)
	if err != nil || replayed.ArtifactID != artifact.ArtifactID ||
		replayed.AnnotatedAssetID != artifact.AnnotatedAssetID {
		t.Fatalf("restart replay must not depend on PhotoResult: replayed=%+v err=%v", replayed, err)
	}
}

type coveragePNGAnnotator struct{ data []byte }

func (a coveragePNGAnnotator) Annotate(context.Context, []byte, []PhotoAnnotation) (RenderedPhoto, error) {
	return RenderedPhoto{Data: append([]byte(nil), a.data...), MIME: "image/png"}, nil
}

func TestImageTaskFrozenAnnotationCoverageSurvivesRuntimeRelease(t *testing.T) {
	for _, scenario := range []string{"partial", "unlocated", "blank", "partial_reconciled_unknown", "partial_reconciled_sent"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
			ctx := context.Background()
			confidence := 1.0
			questions := []RecognizedQuestion{
				{Question: "1+1=", RawTranscription: "1+1=", CanonicalMarkdown: "1+1=", Subject: "数学", StudentAnswer: "3", AnswerRawTranscription: "3", AnswerCanonicalMarkdown: "3", AnswerState: AnswerStatePresent, RecognitionConfidence: &confidence, SourceNumberPath: []string{"1"}, DisplayLabel: "1."},
				{Question: "1+2=", RawTranscription: "1+2=", CanonicalMarkdown: "1+2=", Subject: "数学", StudentAnswer: "4", AnswerRawTranscription: "4", AnswerCanonicalMarkdown: "4", AnswerState: AnswerStatePresent, RecognitionConfidence: &confidence, SourceNumberPath: []string{"2"}, DisplayLabel: "2."},
			}
			var anchorer AnswerAnchorer = &partialDegradedAnchorer{err: context.DeadlineExceeded}
			intent := k12.ImageTaskIntentCompletedHomework
			if scenario == "unlocated" {
				anchorer = &photoAnchorerFake{err: context.DeadlineExceeded}
			}
			if scenario == "blank" {
				intent, anchorer = k12.ImageTaskIntentBlankWorksheet, nil
				for i := range questions {
					questions[i].AnswerState, questions[i].StudentAnswer = AnswerStateBlank, ""
					questions[i].AnswerRawTranscription, questions[i].AnswerCanonicalMarkdown = "", ""
				}
			}
			o := newParallelAnchorOrchestrator(t, &countingRecognizer{questions: questions}, anchorer, WithGradingRunDir(t.TempDir()))
			o.deps.GradingBudgetSnapshot = k12.GradingBudgetSnapshot{PolicyVersion: 1, RecognitionPlanVersion: k12.RecognitionPlanVersionV1, ItemConcurrency: 2,
				StageSeconds:     k12.GradingStageBudgets{Queued: 60, Normalizing: 60, Recognizing: 120, Locating: 60, Rendering: 60, Projecting: 60},
				AssessingBuckets: []k12.GradingAssessingBudgetBucket{{MaxProblems: 1, Seconds: 90}, {MaxProblems: 8, Seconds: 180}, {MaxProblems: 16, Seconds: 300}, {MaxProblems: 32, Seconds: 540}}}
			o.deps.PhotoAnnotator = coveragePNGAnnotator{data: validPNGFixture(t, "coverage-annotated")}
			repository := &PageAssetRepository{Records: o.deps.Records}
			original := validPNGFixture(t, "coverage-original")
			dimensions, _, err := image.DecodeConfig(bytes.NewReader(original))
			if err != nil {
				t.Fatal(err)
			}
			for i := range questions {
				questions[i].SourceWidth, questions[i].SourceHeight = dimensions.Width, dimensions.Height
			}
			ready, err := repository.Persist(ctx, "coverage-owner", "mingming", original)
			if err != nil {
				t.Fatal(err)
			}
			classifier := &imageTaskClassifierStub{}
			c, _ := newImageTaskCoordinatorForTest(t, classifier)
			c.Records, c.Grading, c.PageAssets = o.deps.Records, o, repository
			c.Now = func() int64 { return time.Now().Unix() }
			input := testCreateImageTaskInput()
			input.OwnerScope, input.SourceAssetRefs = "coverage-owner", []string{ready.Metadata.PageAssetID}
			created, _, err := c.Create(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			dispatch, target, err := c.Records.CommitImageTaskRouting(ctx, "mingming", created.Dispatch.DispatchID, created.Dispatch.Version,
				k12storage.ImageTaskRoutingDecision{Intent: intent, Confidence: 1, Evidence: []string{"frozen worksheet"}, InvocationResultDigest: "sha256:coverage-classification"})
			if err != nil {
				t.Fatal(err)
			}
			job, _, err := o.StartPhotoGradingJob(ctx, StartPhotoGradingInput{
				Photo:      PhotoGradeRequest{AgentName: "mingming", SourceSession: input.SourceSessionID, Image: original, SourcePageAssetID: ready.Metadata.PageAssetID, TaskIntent: photoTaskIntentFromDispatch(intent)},
				SourceKind: "image_task", SourceKey: dispatch.DispatchID,
				ParentAutomaticAttemptID: dispatch.DispatchID + ":coverage", ParentAutomaticDeadlineAt: time.Now().Unix() + 300,
				BudgetSnapshot: o.deps.GradingBudgetSnapshot,
			})
			if err != nil {
				t.Fatal(err)
			}
			jobID := job.Record.RecordID
			if _, err := c.Records.BindHomeworkSubmissionGradingJob(ctx, "mingming", target.HomeworkSubmission.SubmissionID, jobID, target.HomeworkSubmission.Version); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(scenario, "partial_reconciled_") {
				// 在逐题结果已经提交、aggregate 确认中断处恢复，不重跑模型边界。
				run := o.lookup(jobID)
				if _, err := o.advanceOK(ctx, run, jobID, ""); err != nil {
					t.Fatal(err)
				}
				if _, err := o.advanceOK(ctx, run, jobID, "image:"+shortSHA1(run.req.Image)); err != nil {
					t.Fatal(err)
				}
				job, err = o.runRecognize(ctx, run, jobID)
				if err != nil || job.Record.Status != k12.GradingStageAwaitingConfirmation {
					t.Fatalf("prepare recognition checkpoint: stage=%s err=%v", job.Record.Status, err)
				}
				o.startAnchorAsync(jobID, run, job.Fields.ModelSnapshot)
				job = waitGradingView(t, o, jobID, func(v GradingJobView) bool { return v.Fields.AnchorState != k12.GradingAnchorPending })
				job, err = o.autoFreezeRecognition(ctx, run, job)
				if err != nil || job.Record.Status != k12.GradingStageAssessing {
					t.Fatalf("freeze assessment checkpoint: stage=%s err=%v", job.Record.Status, err)
				}
				invocation, err := o.beginFrozenAssessInvocation(ctx, run, job)
				if err != nil {
					t.Fatal(err)
				}
				for _, question := range RecognizedQuestionsForAssessment(run.questions) {
					if _, err := o.assessDurablePhotoItem(ctx, o.deps, job, run.req, PhotoModeGrade, question); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "partial_reconciled_unknown" {
					if _, err := o.deps.Records.MarkModelInvocationOutcomeUnknown(ctx, "mingming", invocation.InvocationID, "ack_lost"); err != nil {
						t.Fatal(err)
					}
				}
				job, err = o.markGradingOutcomeUnknown(ctx, run, jobID, "ack_lost")
				if err != nil {
					t.Fatal(err)
				}
				itemReceipts, err := o.deps.Records.ListGradingAssessmentItems(ctx, "mingming", jobID)
				if err != nil {
					t.Fatal(err)
				}
				reconciled, _, err := o.reconcileDurableGradingOutcome(ctx, run, job)
				if err != nil || !reconciled {
					t.Fatalf("reconcile durable assessment: reconciled=%v err=%v", reconciled, err)
				}
				stored, err := o.deps.Records.GetModelInvocation(ctx, "mingming", invocation.InvocationID)
				wantStatus := k12.ModelInvocationSucceeded
				if scenario == "partial_reconciled_unknown" {
					wantStatus = k12.ModelInvocationReconciled
				}
				if err != nil || stored.Status != wantStatus || stored.ResultJSON == "" || stored.ExternalRequestID != "" {
					t.Fatalf("same assessment result was not frozen independently of request id: %+v err=%v", stored, err)
				}
				replayedReceipts, err := o.deps.Records.ListGradingAssessmentItems(ctx, "mingming", jobID)
				if err != nil || !reflect.DeepEqual(itemReceipts, replayedReceipts) {
					t.Fatal("durable success reconciliation rewrote item receipts")
				}
			}
			if _, err := o.RunGradingJob(ctx, jobID); err != nil {
				t.Fatal(err)
			}
			waitGradingView(t, o, jobID, func(v GradingJobView) bool { return v.Fields.AnchorState != k12.GradingAnchorPending })
			waitGradingView(t, o, jobID, func(v GradingJobView) bool { return v.Record.Status == k12.GradingStageCompleted })
			before, err := c.Result(ctx, "mingming", dispatch.DispatchID)
			if err != nil || before.Photo == nil || before.FinalArtifact == nil {
				t.Fatalf("public result: %+v %v", before, err)
			}
			want := ""
			switch scenario {
			case "partial", "partial_reconciled_unknown", "partial_reconciled_sent":
				want = "本次 2 题已判定，其中 1 题在原作答位置标注；其余 1 题仅作文字汇总，未在图上猜测位置。"
			case "unlocated":
				want = "本次 2 题已判定，0 题已在图上标注；本次未生成批改图，判定结果仅作文字汇总，以避免标记错位。"
			}
			if before.Photo.ImageWarning != want || (want != "" && !strings.Contains(before.FinalArtifact.CanonicalMarkdown, want)) {
				t.Fatalf("coverage lost before release: got=%q canonical=%q", before.Photo.ImageWarning, before.FinalArtifact.CanonicalMarkdown)
			}
			if (before.Photo.AnnotatedImage != nil) != strings.HasPrefix(scenario, "partial") {
				t.Fatalf("wrong actual annotation presence: %+v", before.Photo)
			}
			if strings.HasPrefix(scenario, "partial") && (before.Photo.Items[0].Recognized.BBox == nil || before.Photo.Items[1].Recognized.BBox != nil) {
				t.Fatal("frozen public geometry differs from rendered result")
			}
			receipts, _ := o.deps.Records.ListGradingAssessmentItems(ctx, "mingming", jobID)
			invocations, _ := o.deps.Records.ListModelInvocations(ctx, "mingming", jobID)
			o.ReleaseGradingRun(jobID)
			if _, ok := o.PhotoResult(jobID); ok {
				t.Fatal("runtime result was not released")
			}
			c.Grading = trackGradingOrchestrator(t, NewGradingOrchestrator(o.deps, orchestratorSnapshotResolver, WithGradingRunDir(o.runDir)))
			after, err := c.Result(ctx, "mingming", dispatch.DispatchID)
			if err != nil || after.Photo == nil || after.Photo.ImageWarning != want || !reflect.DeepEqual(after.FinalArtifact, before.FinalArtifact) || !reflect.DeepEqual(after.Photo, before.Photo) {
				t.Fatalf("release changed frozen result: before=%+v after=%+v err=%v", before.Photo, after.Photo, err)
			}
			afterReceipts, _ := o.deps.Records.ListGradingAssessmentItems(ctx, "mingming", jobID)
			afterInvocations, _ := o.deps.Records.ListModelInvocations(ctx, "mingming", jobID)
			if !reflect.DeepEqual(receipts, afterReceipts) || !reflect.DeepEqual(invocations, afterInvocations) || classifier.calls != 0 {
				t.Fatal("read/release rewrote receipts or called classifier")
			}
		})
	}
}
