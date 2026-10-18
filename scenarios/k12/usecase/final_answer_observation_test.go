package usecase

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestFinalAnswerObservationCannotGradeUncertainWorkingWithoutReceipt(t *testing.T) {
	for _, proof := range []bool{false, true} {
		t.Run(map[bool]string{false: "no independent proof", true: "proven final answer only"}[proof], func(t *testing.T) {
			o, job, req, q, grader := finalAnswerAssessmentSourceFixture(t, true)
			if q.ConfirmationRequired {
				t.Fatal("same-read fixture did not reproduce missing risk reason")
			}
			if !proof {
				q.FinalAnswerReceipt = nil
			}
			before, _ := json.Marshal(q)
			item, err := o.assessDurablePhotoItem(t.Context(), o.deps, job, req, PhotoModeGrade, q)
			if err != nil {
				t.Fatal(err)
			}
			if !proof {
				if item.Status != PhotoAnswerUnclear || grader.calls != 0 || item.Grade.Outcome.WrongStep != "" || item.ParentGuide != nil {
					t.Fatalf("uncertain intermediate step was graded: %+v", item)
				}
				calls, err := o.deps.Records.ListGradingItemInvocations(t.Context(), req.AgentName, job.Record.RecordID)
				if err != nil || len(calls) != 0 {
					t.Fatalf("unproven working invoked grading: %v", err)
				}
			} else if item.Status != PhotoWrong || item.Warning != finalAnswerOnlyAssessmentWarning || grader.calls != 1 || item.Grade.Outcome.WrongStep != "" {
				t.Fatalf("final-only grading fabricated an intermediate error: %+v", item)
			}
			after, _ := json.Marshal(q)
			if string(before) != string(after) || item.Recognized.StudentAnswer != "=1/4=1/3" || !reflect.DeepEqual(item.Recognized.OCRSignals, q.OCRSignals) {
				t.Fatal("original uncertainty or working was overwritten")
			}
			if proof {
				cold := trackGradingOrchestrator(t, NewGradingOrchestrator(o.deps, nil, WithGradingRunDir(o.runDir)))
				typed, ok := cold.typedRecognizedQuestions(t.Context(), job.Record.RecordID, req.AgentName)
				if !ok || len(typed) != 1 || !reflect.DeepEqual(typed[0].OCRSignals, q.OCRSignals) || !finalAnswerAssessmentEligible(req, typed[0]) {
					t.Fatal("typed restore lost unresolved same-read observations")
				}
			}
		})
	}
}

func TestFinalAnswerObservationGateKeepsOldPolicy(t *testing.T) {
	q := RecognizedQuestion{OCRSignals: []string{"手写步骤是否划去不明确"}}
	for _, policy := range []string{"", k12.RecognitionExecutionPolicyCompactDAGV1} {
		if finalAnswerObservationUnresolved(PhotoGradeRequest{RecognitionExecutionPolicy: policy}, q) {
			t.Fatal("old frozen policy acquired the new gate")
		}
	}
}
