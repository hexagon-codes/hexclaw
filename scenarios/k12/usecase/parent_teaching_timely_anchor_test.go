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
	assertParentTeachingTimelyGeometry(t, false, false)
}

func TestParentTeachingUsesLocallyReusedGeometryWithoutProviderReceipt(t *testing.T) {
	assertParentTeachingTimelyGeometry(t, true, false)
}

func TestParentTeachingCompletesAnswerMarksOnAnAlreadyAnnotatedPage(t *testing.T) {
	assertParentTeachingTimelyGeometry(t, false, true)
}

func TestLateAnswerGeometryPreservesCurrentMarksAndSkipsStaleObservations(t *testing.T) {
	questions := []RecognizedQuestion{
		{ProblemID: "first", AttemptID: "a", PageAssetID: "page", Question: "1+1", StudentAnswer: "2", AnswerState: AnswerStatePresent},
		{ProblemID: "second", AttemptID: "b", PageAssetID: "page", Question: "1+2", StudentAnswer: "2", AnswerState: AnswerStatePresent},
		{ProblemID: "third", AttemptID: "c", PageAssetID: "page", Question: "1+3", StudentAnswer: "4", AnswerState: AnswerStatePresent},
	}
	first := BBox{X: .1, Y: .2, W: .1, H: .1}
	later := BBox{X: .4, Y: .5, W: .1, H: .1}
	previous := map[renderedAnswerIdentity]BBox{answerBoxIdentity(questions[0]): first}
	observed := cloneRecognizedQuestions(questions)
	for i := range observed {
		observed[i].BBox = &later
	}
	observed[2].StudentAnswer = "3"
	merged := extendRenderedAnswerGeometry(questions, observed, previous)
	if len(merged) != len(questions) || merged[0].BBox == nil || *merged[0].BBox != first ||
		merged[1].BBox == nil || *merged[1].BBox != later || merged[2].BBox != nil {
		t.Fatalf("expected the original mark, a new mark, and no stale mark: %+v", merged)
	}
	previous[answerBoxIdentity(questions[1])] = later
	if extendRenderedAnswerGeometry(questions, observed, previous) != nil {
		t.Fatal("redrew a page without new trusted answer geometry")
	}
	observed[0].PageAssetID = "different-page"
	if extendRenderedAnswerGeometry(questions, observed, nil) != nil {
		t.Fatal("adopted a different page's locating receipt")
	}
}

type timelyPhotoAnnotator struct {
	sourceBoundaryPNGAnnotator
	frames [][]PhotoAnnotation
}

func (a *timelyPhotoAnnotator) Annotate(ctx context.Context, image []byte, marks []PhotoAnnotation) (RenderedPhoto, error) {
	a.frames = append(a.frames, append([]PhotoAnnotation(nil), marks...))
	return a.sourceBoundaryPNGAnnotator.Annotate(ctx, image, marks)
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

func assertParentTeachingTimelyGeometry(t *testing.T, local, previousQuestionMark bool) {
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
	if previousQuestionMark {
		rec.questions = append(rec.questions, RecognizedQuestion{Question: "2+2=", RawTranscription: "2+2=", CanonicalMarkdown: "2+2=", Subject: "数学",
			AnswerState: AnswerStateUnclear, RecognitionConfidence: float64Ptr(.99)})
	}
	o := newParallelAnchorOrchestrator(t, rec, configuredAnchor, WithGradingRunDir(t.TempDir()))
	o.deps.TeachingReportResources, o.deps.GradingBudgetSnapshot = reportResilienceResources{}, orchestratorTestBudget()
	o.deps.TextbookOwnerID, o.deps.Renderer, o.deps.Solver = DefaultLocalOwnerScope, &reportResilienceRenderer{}, solver
	grader := &itemResumeGrader{calls: map[string]int{}, outcomes: map[string]GradeOutcome{"1+1=": {Verdict: VerdictDisagree, WrongStep: "1+1=3", ErrorCause: "计算失误"}}}
	o.deps.Grader = grader
	annotator := &timelyPhotoAnnotator{sourceBoundaryPNGAnnotator: sourceBoundaryPNGAnnotator{data: validPNGFixture(t, "timely-annotated")}}
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
	for i := range rec.questions {
		rec.questions[i].PageAssetID = assetID
		rec.questions[i].SourceWidth, rec.questions[i].SourceHeight = 2, 2
		rec.questions[i].SourceRegion = &k12.SourcePixelRegion{Width: 2, Height: 2}
	}
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
	if err != nil || !artifact.HasAnnotatedAsset() || len(annotator.marks) != len(rec.questions) || grader.callCount("1+1=") != 1 {
		t.Fatalf("timely reliable geometry was discarded: annotated=%v marks=%d solves=%d err=%v", artifact.HasAnnotatedAsset(), len(annotator.marks), solver.calls, err)
	}
	if previousQuestionMark {
		if len(annotator.frames) != 2 || len(annotator.frames[0]) != 1 || annotator.frames[0][0].Status != PhotoAnswerUnclear ||
			annotator.frames[1][0].Status != PhotoWrong || annotator.frames[1][0].QuestionNumber != 1 {
			t.Fatalf("expected a provisional question mark, then the first answer's correction: %+v", annotator.frames)
		}
	} else if solver.calls != 1 {
		t.Fatalf("existing assessment was solved again: %d", solver.calls)
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
