package usecase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type sourceBoundaryPNGAnnotator struct {
	data  []byte
	marks []PhotoAnnotation
}

func (a *sourceBoundaryPNGAnnotator) Annotate(_ context.Context, _ []byte, marks []PhotoAnnotation) (RenderedPhoto, error) {
	a.marks = append([]PhotoAnnotation(nil), marks...)
	return RenderedPhoto{Data: append([]byte(nil), a.data...), MIME: "image/png"}, nil
}

func TestProblemSourceRequiredImageCompletesCurrentAssessmentAndSkipExactSet(t *testing.T) {
	for _, skipIndex := range []int{0, 1} {
		t.Run(fmt.Sprintf("skip_at_%d", skipIndex+1), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			ctx := context.Background()
			fixture := newSourceReprocessIntegrationFixture(t, nil)
			targetIndex := 1 - skipIndex
			originalIDs := []string{fixture.run.questions[0].ProblemID, fixture.run.questions[1].ProblemID}
			skippedQuestion := fixture.run.questions[skipIndex]
			targetQuestion := fixture.run.anchored[targetIndex]
			annotator := &sourceBoundaryPNGAnnotator{data: validPNGFixture(t, "source-boundary-annotated")}
			fixture.orchestrator.deps.PhotoAnnotator = annotator
			seedBUG20260726031SkipReceipt(t, fixture.orchestrator, fixture.job.Record.RecordID, skippedQuestion, "source-boundary-unrelated")
			skipsBefore, err := fixture.coordinator.Records.ListCurrentProblemSkipReceipts(ctx, "mingming", fixture.job.Record.RecordID)
			if err != nil || len(skipsBefore) != 1 {
				t.Fatalf("current skip fixture: %+v err=%v", skipsBefore, err)
			}
			work := fixture.claimCorrectText(t, "corrected affected with skipped sibling", "source-boundary-skip", targetQuestion.ProblemID)
			if err := fixture.coordinator.ProcessProblemSourceReprocess(ctx, work); err != nil {
				t.Fatal(err)
			}
			job, err := fixture.orchestrator.deps.GetGradingJob(ctx, "mingming", work.JobID)
			if err != nil || job.Record.Status != k12.GradingStageCompleted {
				t.Fatalf("complete assessment/skip exact-set did not finalize: stage=%s err=%v", job.Record.Status, err)
			}
			artifact, err := fixture.coordinator.Records.GetCurrentGradingFinalArtifactByJob(ctx, "mingming", work.JobID)
			if err != nil || !artifact.HasAnnotatedAsset() || artifact.TotalCount != 2 || artifact.PublishedCount != 1 || artifact.SkippedCount != 1 || artifact.CoverageStatus != k12.GradingFinalArtifactCoverageWithSkips {
				t.Fatalf("mixed receipt page lost its image or coverage: %+v err=%v", artifact, err)
			}
			asset, err := fixture.coordinator.Records.OpenGradingFinalAnnotatedAsset(ctx, "mingming", artifact.ArtifactID)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := image.DecodeConfig(bytes.NewReader(asset.Data)); err != nil {
				t.Fatalf("frozen annotated image is not decodable: %v", err)
			}
			if len(annotator.marks) != 1 || annotator.marks[0].QuestionNumber != targetIndex+1 || targetQuestion.BBox == nil || annotator.marks[0].BBox != *targetQuestion.BBox {
				t.Fatalf("mixed receipt image changed the original ordinal or coordinates: marks=%+v original=%+v", annotator.marks, targetQuestion)
			}
			if len(fixture.run.questions) != 2 || fixture.run.questions[0].ProblemID != originalIDs[0] || fixture.run.questions[1].ProblemID != originalIDs[1] {
				t.Fatal("render filtering changed the full source-run order")
			}
			assessments, err := fixture.coordinator.Records.ListGradingAssessmentItems(ctx, "mingming", work.JobID)
			if err != nil || len(assessments) != 1 || assessments[0].ProblemID != work.ProblemID || assessments[0].ProblemID == skippedQuestion.ProblemID {
				t.Fatalf("skip was fabricated as an assessment: %+v err=%v", assessments, err)
			}
			skipsAfter, err := fixture.coordinator.Records.ListCurrentProblemSkipReceipts(ctx, "mingming", work.JobID)
			if err != nil || !reflect.DeepEqual(skipsBefore, skipsAfter) || fixture.solver.callCount("unrelated") != 0 || fixture.grader.callCount("unrelated") != 0 {
				t.Fatalf("source finalization changed skipped facts or assessed the sibling: skips=%+v err=%v", skipsAfter, err)
			}
			beforeCalls, beforeSends := fixture.recognizer.counts()
			beforeSolver, beforeGrader := fixture.solver.callCount("corrected affected with skipped sibling"), fixture.grader.callCount("corrected affected with skipped sibling")
			restarted := trackGradingOrchestrator(t, NewGradingOrchestrator(fixture.orchestrator.deps, nil, WithGradingRunDir(fixture.orchestrator.runDir)))
			if err := restarted.ProcessProblemSourceReprocess(ctx, work); err != nil {
				t.Fatal(err)
			}
			current, err := fixture.coordinator.Records.GetCurrentGradingFinalArtifactByJob(ctx, "mingming", work.JobID)
			afterCalls, afterSends := fixture.recognizer.counts()
			if err != nil || !reflect.DeepEqual(artifact, current) || beforeCalls != afterCalls || beforeSends != afterSends || fixture.solver.callCount("corrected affected with skipped sibling") != beforeSolver || fixture.grader.callCount("corrected affected with skipped sibling") != beforeGrader {
				t.Fatalf("mixed receipt restart replay changed artifact or resent calls: err=%v", err)
			}
		})
	}
}

func TestProblemSourceFinalizationDoesNotReassessUnrelatedInvalidAsset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	fixture := newSourceReprocessIntegrationFixture(t, nil)
	const owner = "guardian-1"
	fixture.orchestrator.deps.TextbookOwnerID = owner
	seedDeps := fixture.orchestrator.deps
	seedDeps.Solver, seedDeps.Grader, seedDeps.VerifiedGrader = &assetReuseSolver{}, &assetReuseGrader{}, nil
	seedDeps.Recognizer = &countingRecognizer{questions: []RecognizedQuestion{{Question: "unrelated qualified", Subject: "数学", StudentAnswer: "4", AnswerState: AnswerStatePresent, RecognitionConfidence: float64Pointer(0.99)}}}
	publication := k12storage.NewDispatcher(seedDeps.Records, ProblemAssetConsumer{Records: seedDeps.Records})
	seed := trackGradingOrchestrator(t, NewGradingOrchestrator(seedDeps, orchestratorSnapshotResolver, WithGradingRunDir(t.TempDir())))
	seedPhoto := orchestratorPhotoRequest()
	seedPhoto.Image = validPNGFixture(t, "source-boundary-asset-seed")
	started, _, err := seed.StartPhotoGradingJob(ctx, StartPhotoGradingInput{Photo: seedPhoto, SourceKind: "im", SourceKey: "source-boundary-asset-seed"})
	if err != nil {
		t.Fatal(err)
	}
	seedJob := started.Record.RecordID
	freezeItemResumeBudget(t, seed, seedJob)
	if view, err := seed.RunGradingJob(ctx, seedJob); err != nil || view.Record.Status != k12.GradingStageAwaitingConfirmation {
		t.Fatalf("prepare real PNG answer publication: stage=%s err=%v", view.Record.Status, err)
	}
	waitGradingView(t, seed, seedJob, func(v GradingJobView) bool { return v.Fields.AnchorState != k12.GradingAnchorPending })
	if _, err := seed.ConfirmAndRun(ctx, seedJob, nil); err != nil {
		t.Fatal(err)
	}
	if err := publication.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	if asset, err := fixture.coordinator.Records.FindExactProblemAsset(ctx, owner, k12.ProblemAssetFacts{Subject: "数学", Stem: "unrelated qualified", AnswerContext: map[string]string{"grade_term": "五年级上"}}); err != nil {
		seedRun := seed.lookup(seedJob)
		facts, eligible := gradingProblemAssetFacts(seedRun.questions[0], photoItemGradeRequest(seedRun.req, seedRun.questions[0]))
		t.Fatalf("answer publication fixture missing: facts=%+v eligible=%v asset=%+v error=%v", facts, eligible, asset, err)
	}
	unrelated := fixture.run.questions[1]
	unrelatedWork := fixture.claimCorrectText(t, "unrelated qualified", "source-boundary-qualified-sibling", unrelated.ProblemID)
	if err := fixture.coordinator.ProcessProblemSourceReprocess(ctx, unrelatedWork); err != nil {
		t.Fatalf("prepare the qualified sibling through its actual source action: %v", err)
	}
	before, err := fixture.coordinator.Records.GetEffectiveGradingAssessment(ctx, "mingming", fixture.job.Record.RecordID, unrelated.ProblemID)
	if err != nil || before.Current.AnswerSource == nil || before.Correction != nil {
		t.Fatalf("unrelated assessment did not adopt the published answer: %+v err=%v", before, err)
	}
	source := before.Current.AnswerSource
	if err := fixture.coordinator.Records.ArchiveProblemAsset(ctx, owner, source.AssetID, source.AssetRevision); err != nil {
		t.Fatal(err)
	}
	if err := fixture.coordinator.Records.ValidateGradingAssessmentAnswer(ctx, before.Current); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		t.Fatalf("fixture must expose the actual archived-source cause: %v", err)
	}
	beforeSolver, beforeGrader := fixture.solver.callCount("unrelated qualified"), fixture.grader.callCount("unrelated qualified")
	work := fixture.claimCorrectText(t, "corrected affected with invalid sibling source", "source-boundary-invalid-asset")
	err = fixture.coordinator.ProcessProblemSourceReprocess(ctx, work)
	if fixture.solver.callCount("unrelated qualified") != beforeSolver || fixture.grader.callCount("unrelated qualified") != beforeGrader {
		t.Fatalf("source finalization reassessed an unrelated question: solve=%d->%d grade=%d->%d error=%v", beforeSolver, fixture.solver.callCount("unrelated qualified"), beforeGrader, fixture.grader.callCount("unrelated qualified"), err)
	}
	if !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		t.Fatalf("source finalization must retain the invalid sibling source: %v", err)
	}
	after, err := fixture.coordinator.Records.GetEffectiveGradingAssessment(ctx, "mingming", work.JobID, unrelated.ProblemID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("source finalization changed the unrelated immutable assessment: %+v err=%v", after, err)
	}
	if _, err := fixture.coordinator.Records.GetCurrentGradingFinalArtifactByJob(ctx, "mingming", work.JobID); !errors.Is(err, records.ErrNotFound) {
		t.Fatalf("invalid source published a final artifact: %v", err)
	}
}
