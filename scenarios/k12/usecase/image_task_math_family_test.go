package usecase

import (
	"context"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type heldMathFamilyGrading struct{ *GradingOrchestrator }

func (*heldMathFamilyGrading) StartAsync(string) bool { return true }

type mathFamilyReadingRecognizer struct {
	countingRecognizer
	before func()
}

func (r *mathFamilyReadingRecognizer) Recognize(ctx context.Context, image []byte) ([]RecognizedQuestion, error) {
	r.before()
	return r.countingRecognizer.Recognize(ctx, image)
}

func TestImageTaskMathFamilyPublicReadingSequence(t *testing.T) {
	for _, scenario := range []string{"known completed control", "unknown blank", "unknown present"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			classification := ImageTaskClassification{Intent: k12.ImageTaskIntentCompletedHomework, Confidence: .99, IntentEvidence: []string{"mathematics worksheet"}}
			if scenario != "known completed control" {
				classification.Intent = k12.ImageTaskIntentUnknown
				classification.ConfirmationCandidates = []k12.ImageTaskIntent{k12.ImageTaskIntentCompletedHomework, k12.ImageTaskIntentBlankWorksheet}
			}
			classifier := &imageTaskClassifierStub{result: classification}
			c, _ := newImageTaskCoordinatorForTest(t, classifier)
			question := RecognizedQuestion{Question: "1+1=", Subject: "数学", PageAssetID: imageTaskAssetForTest, RecognitionConfidence: float64Ptr(.99)}
			if scenario == "unknown blank" {
				question.AnswerState = AnswerStateBlank
			} else {
				question.AnswerState, question.StudentAnswer = AnswerStatePresent, "2"
			}
			rec := &mathFamilyReadingRecognizer{countingRecognizer: countingRecognizer{questions: []RecognizedQuestion{question}}}
			d := recoveryDeps(t, rec, nil, nil)
			c.Records, c.Now, c.GradingBudgetSnapshot = d.Records, d.now, frozenWiringBudget()
			c.ReadAsset = func(string, string) ([]byte, error) { return []byte("math-family-source"), nil }
			o := newRecoverableOrchestrator(t, d, t.TempDir())
			// 仅控制异步排程时间，创建、路由、任务绑定与识题落盘均走真实入口。
			c.Grading = &heldMathFamilyGrading{o}
			input := testCreateImageTaskInput()
			input.MessageIntent = "帮我给孩子讲这道数学题"
			created, _, err := c.Create(ctx, input)
			if err != nil || created.Dispatch.Version != 0 {
				t.Fatalf("create version=%d err=%v", created.Dispatch.Version, err)
			}
			view, err := c.Run(ctx, input.AgentName, created.Dispatch.DispatchID)
			if err != nil || view.Homework == nil || view.Homework.GradingJobID == "" {
				t.Fatalf("public routing did not bind reading job: %+v err=%v", view, err)
			}
			if view.Dispatch.Version != 1 || view.Homework.Version != 1 {
				t.Fatalf("route/bind versions=%d/%d", view.Dispatch.Version, view.Homework.Version)
			}
			rec.before = func() {
				dispatch, err := d.Records.GetImageTaskDispatch(ctx, input.AgentName, view.Dispatch.DispatchID)
				if err != nil || dispatch.Version != 1 || len(dispatch.ConfirmationCandidates) != 0 {
					t.Fatalf("reading dispatch changed before recognition: %+v err=%v", dispatch, err)
				}
				t.Logf("public sequence: Create dispatch v0 -> routed dispatch v%d -> bound homework v%d -> recognizing", dispatch.Version, view.Homework.Version)
			}
			if _, err := o.RunGradingJob(ctx, view.Homework.GradingJobID); err != nil {
				t.Fatal(err)
			}
			got, err := d.Records.GetImageTaskDispatch(ctx, input.AgentName, view.Dispatch.DispatchID)
			want := k12.ImageTaskIntentCompletedHomework
			if scenario == "unknown blank" {
				want = k12.ImageTaskIntentBlankWorksheet
			}
			if err != nil || got.TaskIntent != want || rec.calls != 1 || classifier.calls != 1 {
				t.Fatalf("same reading result intent=%s want=%s reads=%d classifications=%d err=%v", got.TaskIntent, want, rec.calls, classifier.calls, err)
			}
		})
	}
}
