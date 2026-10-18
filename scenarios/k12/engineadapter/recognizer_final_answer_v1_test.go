package engineadapter

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func finalAnswerAdjudicationResponseV1(t *testing.T, question, answer string, complete, questionComplete, finalConfirmed bool) string {
	t.Helper()
	var final any
	if finalConfirmed {
		final = map[string]any{"raw_transcription": "1/3", "bbox": map[string]int{"x": 20, "y": 20, "width": 12, "height": 12}, "is_final_answer": true, "target_ownership_confirmed": true, "not_cancelled": true, "complete_and_legible": true}
	}
	raw, err := json.Marshal(map[string]any{
		"verification": map[string]bool{"target_ownership_confirmed": true, "active_answer_complete": complete, "question_complete_and_legible": questionComplete},
		"final_answer": final,
		"items":        []any{map[string]any{"target_id": "t1", "kind": "question", "recognition": map[string]any{"question": question, "subject": "数学", "answer_state": "present", "student_answer": answer, "recognition_confidence": 0.99, "ocr_signals": []string{"中间手写分数的取消性质不明确"}, "answer_bbox": map[string]int{"x": 20, "y": 20, "width": 12, "height": 12}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRecognitionFinalAnswerV1KeepsWorkingRiskAndOldContract(t *testing.T) {
	contextRegion := k12.SourcePixelRegion{X: 400, Y: 100, Width: 500, Height: 400}
	original := k12.SourcePixelRegion{X: 560, Y: 160, Width: 250, Height: 200}
	target := k12.RecognitionLayoutTargetV2{TargetID: "target-1", Region: original, OriginalRegion: &original, AdjudicationRegion: &contextRegion}
	oldPrompt, err := buildRecognitionLayoutAdjudicationPrompt(target, k12.RecognitionLayoutCompactV4)
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{"", k12.RecognitionExecutionPolicyCompactDAGV1} {
		prompt, err := buildRecognitionLayoutAdjudicationPromptForPolicy(target, k12.RecognitionLayoutCompactV4, policy)
		if err != nil || prompt != oldPrompt {
			t.Fatalf("old prompt changed: %v", err)
		}
	}
	prompt, err := buildRecognitionLayoutAdjudicationPromptForPolicy(target, k12.RecognitionLayoutCompactV4, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1)
	if err != nil || !strings.Contains(prompt, `"question_complete_and_legible":true`) || !strings.Contains(prompt, `"final_answer":null`) || strings.Contains(prompt, "=2/4=1/3") || strings.Contains(prompt, "Return JSON with exactly verification and items.") {
		t.Fatalf("new prompt is inconsistent: %v", err)
	}
	raw := finalAnswerAdjudicationResponseV1(t, "7/9×3/14=", "=1/3", false, true, true)
	_, outcome, verified, evidence, err := parseRecognitionLayoutAdjudicationForPolicy(raw, target, k12.RecognitionLayoutCompactV4, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1)
	if err != nil || verified || evidence == nil || evidence.RawTranscription != "1/3" || evidence.BBox.X != 420 || evidence.BBox.Y != 120 || outcome == nil || outcome.question == nil || len(outcome.question.OCRSignals) != 1 || outcome.question.StudentAnswer != "=1/3" {
		t.Fatalf("final evidence erased working uncertainty: evidence=%+v outcome=%+v complete=%v err=%v", evidence, outcome, verified, err)
	}
	for _, data := range []string{
		finalAnswerAdjudicationResponseV1(t, "7/9×3/14=", "=1/3", false, true, false),
		finalAnswerAdjudicationResponseV1(t, "7/9×3/14=", "=1/3", false, false, true),
	} {
		_, _, full, final, err := parseRecognitionLayoutAdjudicationForPolicy(data, target, k12.RecognitionLayoutCompactV4, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1)
		if err != nil || full || final != nil {
			t.Fatalf("uncertain source acquired proof: %+v full=%v err=%v", final, full, err)
		}
	}
	incompleteQuestion := finalAnswerAdjudicationResponseV1(t, "7/9×3/14=", "=1/3", true, false, true)
	incompleteQuestion = strings.Replace(incompleteQuestion, `"ocr_signals":["中间手写分数的取消性质不明确"]`, `"ocr_signals":[]`, 1)
	if _, _, full, final, err := parseRecognitionLayoutAdjudicationForPolicy(incompleteQuestion, target, k12.RecognitionLayoutCompactV4, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1); err != nil || full || final != nil {
		t.Fatalf("incomplete question became full adoption: final=%+v full=%v err=%v", final, full, err)
	}
	for _, policy := range []string{"", k12.RecognitionExecutionPolicyCompactDAGV1} {
		_, _, full, final, err := parseRecognitionLayoutAdjudicationForPolicy(raw, target, k12.RecognitionLayoutCompactV4, policy)
		if err != nil || full || final != nil {
			t.Fatalf("old policy accepted new proof: %+v full=%v err=%v", final, full, err)
		}
	}
}

func TestRecognitionFinalAnswerV1SettlesSeparateProofWithoutAdoptingWholeWork(t *testing.T) {
	page, base := recognitionLayoutV2DispatchPlan(t, 1)
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{PagePNG: page, Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: base.ManifestInvocationID, ResultDigest: base.ManifestResultDigest}, Targets: []k12.RecognitionLayoutManifestTargetV2{{ManifestRef: "manifest_0001", ManifestOrder: 1, Region: base.Targets[0].Region}}, RecognitionFormat: k12.RecognitionLayoutCompactV4, EnableSourceAdjudication: true, InitialReadMode: k12.RecognitionLayoutManifestWithContentV1})
	if err != nil {
		t.Fatal(err)
	}
	target := plan.Targets[0]
	read := func(answer string) recognitionLayoutBatchOutcomeV2 {
		raw := finalAnswerAdjudicationResponseV1(t, "7/9×3/14=", answer, false, true, false)
		response, err := k12.ParseRecognitionFinalAnswerResponseV1(raw, *target.AdjudicationRegion)
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(response.LegacyEnvelope, &envelope); err != nil {
			t.Fatal(err)
		}
		delete(envelope, "verification")
		payload, _ := json.Marshal(envelope)
		_, result := classifyRecognitionLayoutRepairV2(string(payload), target, plan.RecognitionFormat)
		if result == nil || result.question == nil {
			t.Fatal("source fixture not parsed")
		}
		result.source = k12.RecognitionPhysicalCallResult{InvocationID: "source-" + answer, Payload: string(payload), ResultDigest: recognitionLayoutV2TestDigest(string(payload))}
		return *result
	}
	for _, tc := range []struct {
		name             string
		transport        error
		questionComplete bool
		changedQuestion  bool
		lowConfidence    bool
	}{
		{"independent final only", nil, true, false, false},
		{"printed question not complete", nil, false, false, false},
		{"third question differs", nil, true, true, false},
		{"third source confidence insufficient", nil, true, false, true},
		{"unknown request", errors.New("request outcome unknown"), true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary, review := read("=1/3"), read("=2/4=1/3")
			review.question.AnswerEvidenceTranscriptions = []string{"=1/3", "=2/4=1/3"}
			before, _ := json.Marshal(review.question)
			header := recognitionLayoutV2TestDigest("final-answer-header")
			runtime := recognitionLayoutV2RuntimeFixture(header, plan, 2, time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli())
			runtime.Header.InitialReadMode = plan.InitialReadMode
			runtime.Header.ExecutionPolicy = k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1
			unit := k12.RecognitionPhysicalUnit("layout_adjudicate_0001")
			executor := &sourceAdjudicationExecutor{recognitionLayoutRepairWaveExecutorV2: &recognitionLayoutRepairWaveExecutorV2{runtime: runtime, cached: map[k12.RecognitionPhysicalUnit]k12.RecognitionPhysicalCallResult{}, providerSends: map[k12.RecognitionPhysicalUnit]int{}, transportErrByUnit: map[k12.RecognitionPhysicalUnit]error{unit: tc.transport}}}
			ctx := k12.WithRecognitionPhysicalCallExecutor(k12.WithRecognitionLayoutPlanV2(context.Background(), header), executor)
			response := finalAnswerAdjudicationResponseV1(t, "7/9×3/14=", "=1/3", false, tc.questionComplete, true)
			if tc.changedQuestion {
				response = strings.ReplaceAll(response, "7/9×3/14=", "7/9×3/11=")
			}
			if tc.lowConfidence {
				response = strings.Replace(response, `"recognition_confidence":0.99`, `"recognition_confidence":0.5`, 1)
			}
			adapter := NewRecognizerAdapter(func(_ context.Context, _ []byte, prompt string) (string, error) {
				if strings.Contains(prompt, "=2/4=1/3") || strings.Contains(prompt, "中间手写分数的取消性质不明确") {
					t.Fatal("prior evidence leaked into independent request")
				}
				return response, nil
			})
			err := adapter.recognizeLayoutAdjudicationV2(ctx, page, plan, runtime, target, primary, review)
			if tc.transport != nil {
				if err == nil || len(executor.settlements) != 0 {
					t.Fatalf("unknown response created settlement: %v", err)
				}
				return
			}
			if err != nil || len(executor.settlements) != 1 {
				t.Fatalf("settlement chain: %v", err)
			}
			settlement := executor.settlements[0]
			wantEvidence := tc.questionComplete && !tc.changedQuestion && !tc.lowConfidence
			if settlement.Adopted || settlement.MatchedAnswerPrior != "" || len(settlement.ResultJSON) != 0 || (settlement.FinalAnswerEvidence != nil) != wantEvidence {
				t.Fatalf("partial final evidence changed full adoption: %+v", settlement)
			}
			after, _ := json.Marshal(review.question)
			if !reflect.DeepEqual(before, after) || settlement.SourcePhysicalResultDigest != recognitionLayoutV2TestDigest(response) {
				t.Fatal("source observation or raw digest changed")
			}
		})
	}
}
