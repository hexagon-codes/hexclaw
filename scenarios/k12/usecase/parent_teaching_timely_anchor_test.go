package usecase

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type timelyAnchorTeachingSolver struct {
	reportResilienceSolver
	started, release chan struct{}
	once             sync.Once
}

func (s *timelyAnchorTeachingSolver) Solve(ctx context.Context, problem, grade, constraint string) (SolveResult, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return s.reportResilienceSolver.Solve(ctx, problem, grade, constraint)
	case <-ctx.Done():
		return SolveResult{}, ctx.Err()
	}
}

func TestParentTeachingUsesAnchorCompletedDuringSolveWithoutWaitingForIt(t *testing.T) {
	assertParentTeachingTimelyGeometry(t, false)
}

func TestParentTeachingUsesLocallyReusedGeometryWithoutProviderReceipt(t *testing.T) {
	assertParentTeachingTimelyGeometry(t, true)
}

type reusableParentTeachingGeometry struct {
	started, release, returned chan struct{}
	startedOnce, returnedOnce  sync.Once
}

func (a *reusableParentTeachingGeometry) AnchorAnswers(context.Context, []byte, []RecognizedQuestion) ([]RecognizedQuestion, error) {
	panic("locally verified answer geometry must not call a provider")
}

func (a *reusableParentTeachingGeometry) AnchorAnswerGeometry(context.Context, []byte, []RecognizedQuestion) ([]RecognizedQuestion, error) {
	panic("locally verified answer geometry must not call a provider")
}

func (a *reusableParentTeachingGeometry) ReuseRecognitionAnswerGeometry(ctx context.Context, _ []byte, questions []RecognizedQuestion) ([]RecognizedQuestion, bool) {
	a.startedOnce.Do(func() { close(a.started) })
	select {
	case <-a.release:
	case <-ctx.Done():
		return nil, false
	}
	result := cloneRecognizedQuestions(questions)
	result[0].BBox = &BBox{X: .2, Y: .3, W: .1, H: .05}
	a.returnedOnce.Do(func() { close(a.returned) })
	return result, true
}

func assertParentTeachingTimelyGeometry(t *testing.T, local bool) {
	t.Helper()
	anchorer := &blockingGradingAnchorer{started: make(chan struct{}), release: make(chan struct{})}
	var configuredAnchor AnswerAnchorer = anchorer
	var reused *reusableParentTeachingGeometry
	if local {
		reused = &reusableParentTeachingGeometry{started: anchorer.started, release: anchorer.release, returned: make(chan struct{})}
		configuredAnchor = reused
	}
	solver := &timelyAnchorTeachingSolver{reportResilienceSolver: reportResilienceSolver{solution: "1+1=2\n\n答案：2"}, started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-anchorer.release:
		default:
			close(anchorer.release)
		}
		select {
		case <-solver.release:
		default:
			close(solver.release)
		}
	})
	rec := &countingRecognizer{questions: []RecognizedQuestion{{Question: "1+1=", RawTranscription: "1+1=", CanonicalMarkdown: "1+1=", Subject: "数学", StudentAnswer: "3", AnswerState: AnswerStatePresent, RecognitionConfidence: float64Ptr(.99)}}}
	o := newParallelAnchorOrchestrator(t, rec, configuredAnchor, WithGradingRunDir(t.TempDir()))
	o.deps.TeachingReportResources, o.deps.GradingBudgetSnapshot = reportResilienceResources{}, orchestratorTestBudget()
	o.deps.TextbookOwnerID, o.deps.Renderer, o.deps.Solver = DefaultLocalOwnerScope, &reportResilienceRenderer{}, solver
	grader := &itemResumeGrader{calls: map[string]int{}, outcomes: map[string]GradeOutcome{"1+1=": {Verdict: VerdictDisagree, WrongStep: "1+1=3", ErrorCause: "计算失误"}}}
	o.deps.Grader = grader
	annotator := &sourceBoundaryPNGAnnotator{data: validPNGFixture(t, "timely-annotated")}
	o.deps.PhotoAnnotator = annotator
	data := validPNGFixture(t, "timely-original")
	dispatchID := "parent-teaching-timely-anchor"
	assetID := seedGradingImageTaskOwnerScopeForTest(t, o.deps, dispatchID, data)
	dispatch, err := o.deps.Records.GetImageTaskDispatch(t.Context(), "mingming", dispatchID)
	if err != nil {
		t.Fatal(err)
	}
	_, target, err := o.deps.Records.CommitImageTaskRouting(t.Context(), "mingming", dispatchID, dispatch.Version, k12storage.ImageTaskRoutingDecision{Intent: k12.ImageTaskIntentCompletedHomework, Evidence: []string{"observed student answer"}, Confidence: .99, InvocationResultDigest: "sha256:original-classification"})
	if err != nil || target.HomeworkSubmission == nil {
		t.Fatalf("route homework: %+v err=%v", target, err)
	}
	rec.questions[0].PageAssetID = assetID
	rec.questions[0].SourceWidth, rec.questions[0].SourceHeight = 2, 2
	rec.questions[0].SourceRegion = &k12.SourcePixelRegion{Width: 2, Height: 2}
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
	type completion struct {
		view GradingJobView
		err  error
	}
	finished := make(chan completion, 1)
	go func() { view, err := o.RunGradingJob(t.Context(), jobID); finished <- completion{view, err} }()
	select {
	case <-solver.started:
	case <-time.After(time.Second):
		t.Fatal("solving waited for optional geometry")
	}
	select {
	case <-anchorer.started:
	case <-time.After(time.Second):
		t.Fatal("optional geometry did not start")
	}
	close(anchorer.release)
	if local {
		select {
		case <-reused.returned:
		case <-time.After(time.Second):
			t.Fatal("local geometry was not ready during solving")
		}
	} else {
		deadline := time.NewTimer(time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			rows, err := o.deps.Records.ListModelInvocations(t.Context(), "mingming", jobID)
			if err != nil {
				t.Fatal(err)
			}
			ready := false
			for _, row := range rows {
				ready = ready || row.Stage == k12.GradingStageLocating && row.Status == k12.ModelInvocationSucceeded
			}
			if ready {
				break
			}
			select {
			case <-deadline.C:
				t.Fatal("geometry response was not durably received while solving")
			case <-ticker.C:
			}
		}
	}
	inputBeforeRendering := CanonicalRecognizedQuestionsDigest(o.lookup(jobID).questions)
	close(solver.release)
	select {
	case got := <-finished:
		if got.err != nil || got.view.Record.Status != k12.GradingStageCompleted {
			t.Fatalf("finish teaching: stage=%v err=%v", got.view.Record, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("report did not finish after solving")
	}
	artifact, err := o.deps.Records.GetGradingFinalArtifactByJob(t.Context(), "mingming", jobID)
	if err != nil || !artifact.HasAnnotatedAsset() || len(annotator.marks) != 1 || solver.calls != 1 || grader.callCount("1+1=") != 1 {
		t.Fatalf("timely reliable geometry was discarded: annotated=%v marks=%d solves=%d err=%v", artifact.HasAnnotatedAsset(), len(annotator.marks), solver.calls, err)
	}
	if CanonicalRecognizedQuestionsDigest(o.lookup(jobID).questions) != inputBeforeRendering {
		t.Fatal("geometry adoption changed the frozen problem or observed answer")
	}
	if local {
		rows, err := o.deps.Records.ListModelInvocations(t.Context(), "mingming", jobID)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Stage == k12.GradingStageLocating {
				t.Fatal("local geometry reuse fabricated a provider invocation")
			}
		}
	}
}
