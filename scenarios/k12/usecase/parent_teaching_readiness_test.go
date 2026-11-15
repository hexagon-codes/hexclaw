package usecase

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type parentTeachingPendingSolver struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func TestParentTeachingAutomaticRunFinishesBeforeLateAnchorWithoutChangingFrozenReport(t *testing.T) {
	anchorer := &blockingGradingAnchorer{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-anchorer.release:
		default:
			close(anchorer.release)
		}
	})
	rec := &countingRecognizer{questions: []RecognizedQuestion{{Question: "1+1=", RawTranscription: "1+1=", CanonicalMarkdown: "1+1=", Subject: "数学", StudentAnswer: "3", AnswerState: AnswerStatePresent, RecognitionConfidence: float64Ptr(.99)}}}
	o := newParallelAnchorOrchestrator(t, rec, anchorer, WithGradingRunDir(t.TempDir()))
	o.deps.TeachingReportResources = reportResilienceResources{}
	o.deps.GradingBudgetSnapshot = orchestratorTestBudget()
	o.deps.TextbookOwnerID = DefaultLocalOwnerScope
	o.deps.Renderer = &reportResilienceRenderer{}
	solver := &reportResilienceSolver{solution: "1+1=2\n\n答案：2"}
	o.deps.Solver = solver
	data, _ := problemSourceOrientationFixture(t)
	dispatchID := "parent-teaching-late-anchor"
	assetID := seedGradingImageTaskOwnerScopeForTest(t, o.deps, dispatchID, data)
	dispatch, err := o.deps.Records.GetImageTaskDispatch(t.Context(), "mingming", dispatchID)
	if err != nil {
		t.Fatal(err)
	}
	_, target, err := o.deps.Records.CommitImageTaskRouting(t.Context(), "mingming", dispatchID, dispatch.Version, k12storage.ImageTaskRoutingDecision{Intent: k12.ImageTaskIntentCompletedHomework, Evidence: []string{"observed student answer"}, Confidence: .99, InvocationResultDigest: "sha256:original-classification"})
	if err != nil || target.HomeworkSubmission == nil {
		t.Fatalf("create typed homework source: %+v err=%v", target, err)
	}
	rec.questions[0].PageAssetID = assetID
	req := orchestratorPhotoRequest()
	req.TaskIntent, req.Image, req.SourcePageAssetID = PhotoTaskCompletedHomework, data, assetID
	job, _, err := o.StartPhotoGradingJob(t.Context(), StartPhotoGradingInput{Photo: req, SourceKind: "image_task", SourceKey: dispatchID, BudgetSnapshot: o.deps.GradingBudgetSnapshot, ParentAutomaticAttemptID: dispatchID + ":1", ParentAutomaticDeadlineAt: o.deps.now() + 300})
	if err != nil {
		t.Fatal(err)
	}
	jobID := job.Record.RecordID
	if _, err := o.deps.Records.BindHomeworkSubmissionGradingJob(t.Context(), "mingming", target.HomeworkSubmission.SubmissionID, jobID, target.HomeworkSubmission.Version); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	completed, err := o.RunGradingJob(ctx, jobID)
	if err != nil || completed.Record.Status != k12.GradingStageCompleted || completed.Fields.AnchorState != k12.GradingAnchorPending {
		t.Fatalf("parent teaching waited for optional geometry: stage=%v err=%v", completed.Record, err)
	}
	before, err := o.deps.Records.GetGradingFinalArtifactByJob(t.Context(), "mingming", jobID)
	if err != nil || before.TeachingReport == nil || len(before.TeachingReport.SourcePages) == 0 {
		t.Fatalf("frozen report omitted the real source: %+v err=%v", before, err)
	}
	select {
	case <-anchorer.started:
	case <-time.After(time.Second):
		t.Fatal("optional anchor did not start")
	}
	invocations, err := o.deps.Records.ListModelInvocations(t.Context(), "mingming", jobID)
	if err != nil {
		t.Fatal(err)
	}
	done := o.anchorDoneChannel(jobID)
	close(anchorer.release)
	if done != nil {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("late anchor did not settle")
		}
	}
	after, err := o.deps.Records.GetGradingFinalArtifactByJob(t.Context(), "mingming", jobID)
	if err != nil || !reflect.DeepEqual(after, before) || solver.calls != 1 || o.lookup(jobID).anchored != nil {
		t.Fatalf("late anchor changed frozen report or reran solving: solves=%d err=%v", solver.calls, err)
	}
	settled, err := o.deps.Records.ListModelInvocations(t.Context(), "mingming", jobID)
	if err != nil || len(settled) != len(invocations) {
		t.Fatalf("late anchor started new calls: before=%d after=%d err=%v", len(invocations), len(settled), err)
	}
	found := false
	for _, row := range settled {
		if row.Stage == k12.GradingStageLocating {
			found = row.Status == k12.ModelInvocationSucceeded && row.ResultDigest != ""
		}
	}
	if !found {
		t.Fatal("late optional anchor lost its actual physical receipt")
	}
}

func (s *parentTeachingPendingSolver) Solve(ctx context.Context, _, _, _ string) (SolveResult, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return SolveResult{}, ctx.Err()
	case <-s.release:
		return SolveResult{Solution: "2", Evidence: SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec}}, nil
	}
}

func TestParentTeachingConfirmationProceedsWithoutGeometryAndPreservesCancellation(t *testing.T) {
	for _, name := range []string{"in_flight", "unavailable", "recovered_confirmation"} {
		hasAnchor := name != "unavailable"
		t.Run(name, func(t *testing.T) {
			anchorer := &blockingGradingAnchorer{started: make(chan struct{}), release: make(chan struct{})}
			t.Cleanup(func() {
				select {
				case <-anchorer.release:
				default:
					close(anchorer.release)
				}
			})
			rec := &countingRecognizer{questions: []RecognizedQuestion{{Question: "1+1=", RawTranscription: "1+1=", CanonicalMarkdown: "1+1=", Subject: "数学", StudentAnswer: "3", AnswerState: AnswerStatePresent, RecognitionConfidence: float64Ptr(.99)}}}
			var configuredAnchor AnswerAnchorer
			if hasAnchor {
				configuredAnchor = anchorer
			}
			o := newParallelAnchorOrchestrator(t, rec, configuredAnchor, WithGradingRunDir(t.TempDir()))
			o.deps.TeachingReportResources = reportResilienceResources{}
			solver := &parentTeachingPendingSolver{started: make(chan struct{}), release: make(chan struct{})}
			t.Cleanup(func() {
				select {
				case <-solver.release:
				default:
					close(solver.release)
				}
			})
			o.deps.Solver = solver
			req := orchestratorPhotoRequest()
			req.TaskIntent = PhotoTaskCompletedHomework
			job, _, err := o.StartPhotoGradingJob(t.Context(), StartPhotoGradingInput{Photo: req, SourceKind: "im", SourceKey: t.Name()})
			if err != nil {
				t.Fatal(err)
			}
			jobID := job.Record.RecordID
			freezeItemResumeBudget(t, o, jobID)
			if _, err := o.RunGradingJob(t.Context(), jobID); err != nil {
				t.Fatal(err)
			}
			if hasAnchor {
				select {
				case <-anchorer.started:
				case <-time.After(time.Second):
					t.Fatal("optional anchor did not start")
				}
			} else if done := o.anchorDoneChannel(jobID); done != nil {
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("absent optional anchor did not settle")
				}
			}
			run := o.lookup(jobID)
			if run.req.TeachingReport == nil {
				t.Fatal("current report snapshot was not frozen at creation")
			}
			var confirmed GradingJobView
			if name == "recovered_confirmation" {
				candidate := *run
				candidate.questions = cloneRecognizedQuestions(run.questions)
				if err := applyAndValidateGradingConfirmation(&candidate, ConfirmPhotoGradingInput{}); err != nil {
					t.Fatal(err)
				}
				current, err := o.deps.GetGradingJob(t.Context(), "mingming", jobID)
				if err != nil {
					t.Fatal(err)
				}
				if err := o.persistProblemAttemptFacts(t.Context(), "mingming", current.Fields.SubmissionID, candidate.questions); err != nil {
					t.Fatal(err)
				}
				if err := o.persistRun(jobID, &candidate); err != nil {
					t.Fatal(err)
				}
				if _, err := o.deps.ConfirmGradingJob(t.Context(), "mingming", jobID, []string{"frozen canonical"}); err != nil {
					t.Fatal(err)
				}
				o.mu.Lock()
				delete(o.runs, jobID)
				o.mu.Unlock()
				if n, err := o.RecoverGradingJobs(t.Context(), []string{"mingming"}); err != nil || n != 1 {
					t.Fatalf("recover frozen confirmation: count=%d err=%v", n, err)
				}
				select {
				case <-solver.started:
				case <-time.After(time.Second):
					t.Fatal("recovered confirmed source still waited for optional anchor")
				}
				run = o.lookup(jobID)
				confirmed, err = o.deps.GetGradingJob(t.Context(), "mingming", jobID)
			} else {
				confirmed, _, err = o.ConfirmPhotoGradingJob(t.Context(), jobID, ConfirmPhotoGradingInput{})
			}
			if err != nil || confirmed.Record.Status != k12.GradingStageAssessing || hasAnchor && confirmed.Fields.AnchorState != k12.GradingAnchorPending {
				t.Fatalf("reliable frozen problem waited for answer geometry: stage=%v anchor=%q err=%v", confirmed.Record.Status, confirmed.Fields.AnchorState, err)
			}
			select {
			case <-solver.started:
			case <-time.After(time.Second):
				t.Fatal("confirmation did not start solving before anchor completed")
			}
			before := CanonicalRecognizedQuestionsDigest(run.questions)
			cancelled, _, err := o.CancelPhotoGradingJob(t.Context(), "mingming", jobID)
			if err != nil || cancelled.Record.Status != k12.GradingStageCancelled {
				t.Fatalf("cancel after independent solving: %+v err=%v", cancelled, err)
			}
			done := o.anchorDoneChannel(jobID)
			close(solver.release)
			close(anchorer.release)
			if done != nil {
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("optional anchor failed to settle")
				}
			}
			after, err := o.deps.GetGradingJob(t.Context(), "mingming", jobID)
			if err != nil || after.Record.Status != k12.GradingStageCancelled || CanonicalRecognizedQuestionsDigest(run.questions) != before || run.anchored != nil {
				t.Fatalf("late anchor changed frozen input or cancellation: %+v err=%v", after, err)
			}
		})
	}
}
