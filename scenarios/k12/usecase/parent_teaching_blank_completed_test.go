package usecase

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestParentTeachingCompletedBlankKeepsFrozenIntentAndReference(t *testing.T) {
	solver := &itemResumeSolver{calls: map[string]int{}, solutions: map[string]string{"1+1=": "1+1=2\n答案：2"}}
	grader := &itemResumeGrader{calls: map[string]int{}}
	o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{{Question: "1+1=", RawTranscription: "1+1=", CanonicalMarkdown: "1+1=", Subject: "数学", AnswerState: AnswerStateBlank, RecognitionConfidence: float64Ptr(.99)}}, solver, grader)
	jobID := runItemResumeJobToAssessing(t, o, t.Name())
	run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
	req := run.req
	req.TaskIntent, req.SolveOutputVersion = PhotoTaskCompletedHomework, SolveOutputWithTeachingReportV2
	req.ImageScope = &k12.RecognitionImageScopeInputV1{Version: k12.RecognitionImageScopeVersionV1, SourceReviewPolicy: k12.RecognitionImageSourceUprightReviewPolicyV3}
	o.deps.TeachingReportResources = reportResilienceResources{}
	var err error
	req.TeachingReport, err = o.deps.freezeTeachingReportTask(t.Context(), req.AgentName, "数学", req.Grade, "作业批改结果")
	if err != nil || req.TeachingReport == nil {
		t.Fatalf("freeze original report context: report=%v err=%v", req.TeachingReport, err)
	}
	before, err := o.deps.Records.GetProblemAttemptSnapshot(t.Context(), req.AgentName, job.Fields.SubmissionID)
	if err != nil {
		t.Fatal(err)
	}
	deps := o.deps
	deps.Recognizer = presetRecognizer{questions: cloneRecognizedQuestions(run.questions)}
	assess := func(ctx context.Context, request PhotoGradeRequest, mode PhotoMode, question RecognizedQuestion) (PhotoGradeItem, error) {
		return o.assessDurablePhotoItem(k12.WithGradingModelSnapshot(ctx, job.Fields.ModelSnapshot), o.deps, job, request, mode, question)
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := deps.gradeFrozenHomeworkPhotoWithAssessor(t.Context(), req, 1, assess)
		if err != nil {
			t.Fatalf("reliable blank source must retain the completed-homework report contract: %v", err)
		}
		if result.Mode != PhotoModeGrade || result.TaskIntent != PhotoTaskCompletedHomework || len(result.Items) != 1 {
			t.Fatalf("frozen page semantics changed: mode=%s intent=%s items=%d", result.Mode, result.TaskIntent, len(result.Items))
		}
		item := result.Items[0]
		if item.Status != PhotoBlankSolved || item.ParentGuide == nil || !strings.Contains(item.Solve.Solution, "2") || len(item.ParentGuide.FullSolutionSteps) == 0 || item.Grade.Outcome.Verdict != "" || item.Recognized.ProblemID != run.questions[0].ProblemID {
			t.Fatalf("reference teaching or original identity was lost, or a student verdict was fabricated: %+v", item)
		}
	}
	assessment, err := o.deps.Records.GetEffectiveGradingAssessment(t.Context(), req.AgentName, jobID, run.questions[0].ProblemID)
	if err != nil || assessment.Current.Status != k12.GradingAssessmentBlankSolved || assessment.Current.GradeInvocationID != "" || assessment.Current.ProjectionRecordID != "" || assessment.Current.ProjectionCreated {
		t.Fatalf("blank reference wrote a student assessment or learning projection: %+v err=%v", assessment, err)
	}
	after, err := o.deps.Records.GetProblemAttemptSnapshot(t.Context(), req.AgentName, job.Fields.SubmissionID)
	if err != nil || !reflect.DeepEqual(before, after) || solver.callCount("1+1=") != 1 || grader.callCount("1+1=") != 0 {
		t.Fatalf("reference replay changed source facts or resent evaluation: solves=%d grades=%d err=%v", solver.callCount("1+1="), grader.callCount("1+1="), err)
	}
}
