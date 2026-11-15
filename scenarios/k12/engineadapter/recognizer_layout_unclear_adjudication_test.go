package engineadapter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type retainedObservationAuthorizer struct {
	*sourceAdjudicationExecutor
	captured []k12.RecognitionLayoutAdjudicationRequestV2
}

func (e *retainedObservationAuthorizer) AuthorizeRecognitionLayoutAdjudicationV2(_ context.Context, request k12.RecognitionLayoutAdjudicationRequestV2) (k12.RecognitionLayoutAdjudicationAuthorizationV2, bool, error) {
	e.captured = append(e.captured, request)
	return k12.RecognitionLayoutAdjudicationAuthorizationV2{}, false, errors.New("controlled stop before adjudication send")
}

func TestRecognitionLayoutAdjudicationEmptyObservationsAreNotAnswerConflicts(t *testing.T) {
	for _, tc := range []struct {
		name, firstQuestion, nextQuestion, firstState, nextState, firstAnswer, nextAnswer, wantKind string
	}{
		{"retained unclear empty answers", "求右图中涂色部分的面积。\n3 dm", "求右图中涂色部分的面积。\n图中标注：\\(3\\text{ dm}\\)。", "unclear", "unclear", "", "", "question"},
		{"same unclear empty observations", "求面积，3 dm", "求面积，3 dm", "unclear", "unclear", "", "", ""},
		{"blank and unclear empty observations", "求面积，3 dm", "求面积，3 dm", "blank", "unclear", "", "", ""},
		{"different answer remains answer conflict", "求面积，3 dm", "求面积，3 dm", "present", "present", "3", "9", "answer"},
		{"different source and answer remain both", "求面积，3 dm", "求面积，4 dm", "present", "present", "3", "9", "both"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, plan := recognitionLayoutV2DispatchPlan(t, 1)
			plan.SourceAdjudication, plan.InitialReadMode = true, k12.RecognitionLayoutManifestWithContentV1
			plan.RecognitionFormat = k12.RecognitionLayoutCompactV4
			target := plan.Targets[0]
			read := func(question, state, answer, id string) *recognitionLayoutBatchOutcomeV2 {
				raw, err := json.Marshal(map[string]any{"items": []any{map[string]any{"target_id": "t1", "kind": "question", "recognition": map[string]any{"question": question, "subject": "数学", "answer_state": state, "student_answer": answer, "recognition_confidence": .99, "ocr_signals": []string{}, "answer_bbox": nil}}}})
				if err != nil {
					t.Fatal(err)
				}
				_, reading := classifyRecognitionLayoutRepairV2(string(raw), target, plan.RecognitionFormat)
				if reading == nil || reading.question == nil {
					t.Fatal("retained observation did not parse")
				}
				reading.source = k12.RecognitionPhysicalCallResult{InvocationID: id, ResultDigest: recognitionLayoutV2TestDigest(string(raw)), Payload: string(raw)}
				return reading
			}
			initial, current := read(tc.firstQuestion, tc.firstState, tc.firstAnswer, "primary"), read(tc.nextQuestion, tc.nextState, tc.nextAnswer, "review")
			current.question.EvidenceTranscriptions = []string{tc.firstQuestion, tc.nextQuestion}
			current.question.AnswerEvidenceTranscriptions = []string{tc.firstAnswer, tc.nextAnswer}
			current.question.OCRSignals = []string{"evidence_conflict"}
			runtime := recognitionLayoutV2RuntimeFixture(recognitionLayoutV2TestDigest("retained-observations"), plan, 1, time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli())
			runtime.Header.ExecutionPolicy = k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV2
			e := &retainedObservationAuthorizer{sourceAdjudicationExecutor: &sourceAdjudicationExecutor{recognitionLayoutRepairWaveExecutorV2: &recognitionLayoutRepairWaveExecutorV2{runtime: runtime}}}
			ctx := k12.WithRecognitionPhysicalCallExecutor(k12.WithRecognitionLayoutPlanV2(t.Context(), runtime.HeaderDigest), e)
			err := (&RecognizerAdapter{}).recognizeLayoutAdjudicationV2(ctx, page, plan, runtime, target, *initial, *current)
			if tc.wantKind == "" {
				if err != nil || len(e.captured) != 0 {
					t.Fatalf("same source with no differing answer must not request an answer adjudication: requests=%+v err=%v", e.captured, err)
				}
			} else if len(e.captured) != 1 || e.captured[0].ConflictKind != tc.wantKind {
				t.Fatalf("retained conflict requests=%+v want=%s err=%v", e.captured, tc.wantKind, err)
			}
		})
	}
}
