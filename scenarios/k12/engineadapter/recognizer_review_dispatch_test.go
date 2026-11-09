package engineadapter

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestRecognitionArithmeticFastPathRetainsUnresolvedSourceReview(t *testing.T) {
	for _, risky := range []bool{true, false} {
		name := "no unresolved source observation"
		q := usecase.RecognizedQuestion{Subject: "数学", Question: "5/9×7/8+5/9×1/8",
			StudentAnswer: "=5/9×(7/8+1/8)=5/9", AnswerState: usecase.AnswerStatePresent}
		confidence := .95
		q.RecognitionConfidence = &confidence
		if risky {
			name = "captured unresolved source observation"
			q.OCRSignals = []string{"There is crossed-out handwriting above the active line; it is not transcribed because it appears to be abandoned work."}
		}
		t.Run(name, func(t *testing.T) {
			before, _ := json.Marshal(q)
			decision := recognitionLayoutBatchClassificationDecisionV2{
				classification: k12.RecognitionLayoutBatchClassifiedV2,
				candidates:     []k12.RecognitionLayoutCandidateSettlementV2{{CandidateID: "question-8", Classification: k12.RecognitionLayoutCandidateValidV2}},
				outcomes:       []recognitionLayoutBatchOutcomeV2{{targetID: "question-8", question: &q}},
			}
			selectRecognitionSourceReviewV3(&decision)
			if risky {
				if len(decision.outcomes) != 0 || decision.candidates[0].Classification != k12.RecognitionLayoutCandidateReviewRequiredV2 {
					t.Fatal("arithmetic agreement bypassed unresolved source review")
				}
			} else if len(decision.outcomes) != 1 || decision.candidates[0].Classification != k12.RecognitionLayoutCandidateValidV2 {
				t.Fatal("risk-free arithmetic stopped using the existing fast path")
			}
			after, _ := json.Marshal(q)
			if string(before) != string(after) {
				t.Fatal("review selection rewrote the source observation")
			}
		})
	}
}

func TestRecognitionReviewUnresolvedObservationsReachIndependentRead(t *testing.T) {
	for _, scenario := range []struct {
		name, question, initialAnswer, reviewAnswer, signal string
		initialConfidence                                   float64
	}{
		{"editorial answer conflict", `\(\frac{3}{5}x=\frac{9}{10}\)`, `x=\(\frac{9}{10}\times\frac{3}{5}=\frac{27}{50}\)`, "（上方有划掉的草稿）\nx=" + `\(\frac{9}{10}\times\frac{3}{5}=\frac{27}{50}\)`, "", 0.89},
		{"excluded neighboring context", `\(\frac{7}{8}\div\frac{7}{12}-\frac{1}{2}\)`, `=\(\frac{3}{2}-\frac{1}{2}=1\)`, `=\(\frac{3}{2}-\frac{1}{2}=1\)`, "答案左侧露出邻题的分数尾部，不属于本题。", 0.99},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			page := denseWorksheetTestImage(t, 1076, 1462)
			canonical, err := k12.CanonicalizeRecognitionPageV2(page)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{
				PagePNG: canonical.PNG, Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: "source-whole", ResultDigest: recognitionLayoutV2TestDigest("source-whole")},
				Targets:           []k12.RecognitionLayoutManifestTargetV2{{ManifestRef: "manifest_0001", ManifestOrder: 1, SourceNumberPath: []string{"10"}, DisplayLabel: "10.", Region: k12.SourcePixelRegion{X: 0, Y: 747, Width: 391, Height: 238}}},
				RecognitionFormat: k12.RecognitionLayoutCompactV4, InitialReadMode: k12.RecognitionLayoutManifestWithContentV1, EnableSourceAdjudication: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			target := plan.Targets[0]
			read := func(answer, signal, id string, confidence float64) recognitionLayoutBatchOutcomeV2 {
				t.Helper()
				signals := []string{}
				if signal != "" {
					signals = append(signals, signal)
				}
				fields := map[string]any{"question": scenario.question, "subject": "数学", "answer_state": "present", "student_answer": answer, "recognition_confidence": confidence, "ocr_signals": signals, "answer_bbox": nil}
				payload, err := json.Marshal(map[string]any{"items": []any{map[string]any{"target_id": "t1", "kind": "question", "recognition": fields}}})
				if err != nil {
					t.Fatal(err)
				}
				_, outcome := classifyRecognitionLayoutRepairV2(string(payload), target, plan.RecognitionFormat)
				if outcome == nil || outcome.question == nil {
					t.Fatal("captured observation did not parse")
				}
				outcome.source = k12.RecognitionPhysicalCallResult{InvocationID: id, ResultDigest: recognitionLayoutV2TestDigest(string(payload)), Payload: string(payload)}
				return *outcome
			}
			initial := read(scenario.initialAnswer, "", "initial-source", scenario.initialConfidence)
			review := read(scenario.reviewAnswer, scenario.signal, "review-source", 0.99)
			review.question.EvidenceTranscriptions = []string{initial.question.RawTranscription, review.question.RawTranscription}
			review.question.AnswerEvidenceTranscriptions = []string{initial.question.AnswerRawTranscription, review.question.AnswerRawTranscription}
			checked := usecase.EvaluateOCRConfirmationRiskForInitialReadMode(*review.question, plan.InitialReadMode)
			review.question = &checked
			before, _ := json.Marshal(review.question)
			header := recognitionLayoutV2TestDigest("review-dispatch-header")
			runtime := recognitionLayoutV2RuntimeFixture(header, plan, 2, time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli())
			runtime.Header.InitialReadMode, runtime.Header.ExecutionPolicy = plan.InitialReadMode, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV2
			executor := &sourceAdjudicationExecutor{recognitionLayoutRepairWaveExecutorV2: &recognitionLayoutRepairWaveExecutorV2{runtime: runtime, cached: map[k12.RecognitionPhysicalUnit]k12.RecognitionPhysicalCallResult{}, providerSends: map[k12.RecognitionPhysicalUnit]int{}, transportErrByUnit: map[k12.RecognitionPhysicalUnit]error{}}}
			ctx := k12.WithRecognitionPhysicalCallExecutor(k12.WithRecognitionLayoutPlanV2(context.Background(), header), executor)
			calls := 0
			response := finalAnswerAdjudicationResponseV1(t, scenario.question, scenario.initialAnswer, false, true, true)
			adapter := NewRecognizerAdapter(func(context.Context, []byte, string) (string, error) { calls++; return response, nil })
			if err := adapter.recognizeLayoutAdjudicationV2(ctx, canonical.PNG, plan, runtime, target, initial, review); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || len(executor.requests) != 1 {
				t.Fatalf("independent source read was skipped: calls=%d requests=%d", calls, len(executor.requests))
			}
			after, _ := json.Marshal(review.question)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("source observation was rewritten before independent verification")
			}
		})
	}
}
