package usecase

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestParentTeachingUnmatchedPracticeKeepsReferenceAndNoLearningProjection(t *testing.T) {
	solver := &itemResumeSolver{calls: map[string]int{}, solutions: map[string]string{"4.5×2=": "4.5×2=9\n答案：9"}}
	grader := &itemResumeGrader{calls: map[string]int{}}
	q := RecognizedQuestion{Question: "4.5×2=", RawTranscription: "4.5×2=", CanonicalMarkdown: "4.5×2=", Subject: "数学", AnswerState: AnswerStatePresent, StudentAnswer: "9", AnswerRawTranscription: "9", AnswerCanonicalMarkdown: "9", RecognitionConfidence: float64Ptr(.99)}
	o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{q}, solver, grader)
	jobID := runItemResumeJobToAssessing(t, o, t.Name())
	run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
	q = run.questions[0]
	before := cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
	req := run.req
	req.PracticeReferences = []PracticeGradingReference{{ItemID: "another-item", PracticeProblemID: "another-problem", QuestionMarkdown: "5×2="}}
	req.PracticePaperSize = 1
	ctx := k12.WithGradingModelSnapshot(t.Context(), job.Fields.ModelSnapshot)
	item, err := o.assessDurablePhotoItem(ctx, o.deps, job, req, PhotoModeGrade, q)
	if err != nil || item.Status != PhotoAnswerUnclear || item.ParentGuide == nil || !strings.Contains(item.ParentGuide.Answer, "9") || len(item.ParentGuide.FullSolutionSteps) == 0 || !photoPracticeUnmatched(item) {
		t.Fatalf("unmatched practice lost reliable reference and teaching: item=%+v err=%v", item, err)
	}
	if !reflect.DeepEqual(item.Recognized, before) || item.PracticeItemID != "" || grader.callCount("4.5×2=") != 0 {
		t.Fatal("independent reference changed the observed work or judged an unmatched practice item")
	}
	stored, err := o.deps.Records.GetEffectiveGradingAssessment(ctx, "mingming", jobID, q.ProblemID)
	if err != nil || stored.Current.ProjectionRecordID != "" || stored.Current.ProjectionCreated || stored.Current.GradeInvocationID != "" {
		t.Fatalf("unmatched reference wrote learning history: %+v err=%v", stored, err)
	}
	got, err := o.assessDurablePhotoItem(ctx, o.deps, job, req, PhotoModeGrade, q)
	if err != nil || got.ParentGuide == nil || solver.callCount("4.5×2=") != 1 || grader.callCount("4.5×2=") != 0 {
		t.Fatalf("reference replay resent or lost teaching: solves=%d item=%+v err=%v", solver.callCount("4.5×2="), got, err)
	}
	markdown := photoGradeMarkdown(PhotoGradeResult{Mode: PhotoModeGrade, Items: []PhotoGradeItem{item}})
	if !strings.Contains(markdown, item.ParentGuide.Answer) || !strings.Contains(markdown, item.ParentGuide.FullSolutionSteps[0]) {
		t.Fatal("shared result omitted the existing independent guide")
	}
}
