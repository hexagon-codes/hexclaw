package engineadapter

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestRecognitionExtendedReadUncertaintyUsesAnswerConflict(t *testing.T) {
	raw, err := os.ReadFile("../testdata/recognition_extended_read_uncertainty.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ Initial, Review map[string]json.RawMessage }
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	page := denseWorksheetTestImage(t, 1076, 1462)
	canonical, err := k12.CanonicalizeRecognitionPageV2(page)
	if err != nil {
		t.Fatal(err)
	}
	page = canonical.PNG
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{
		PagePNG: page, Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: "extended-read-whole", ResultDigest: recognitionLayoutV2TestDigest("extended-read")},
		Targets:           []k12.RecognitionLayoutManifestTargetV2{{ManifestRef: "manifest_0001", ManifestOrder: 1, SourceNumberPath: []string{"3"}, DisplayLabel: "3.", Region: k12.SourcePixelRegion{X: 683, Y: 280, Width: 320, Height: 108}}},
		RecognitionFormat: k12.RecognitionLayoutCompactV4, InitialReadMode: k12.RecognitionLayoutManifestWithContentV1, EnableSourceAdjudication: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := plan.Targets[0]
	read := func(fields map[string]json.RawMessage, id string) recognitionLayoutBatchOutcomeV2 {
		t.Helper()
		payload := func(value map[string]json.RawMessage) string {
			b, err := json.Marshal(map[string]any{"items": []any{map[string]any{"target_id": "t1", "kind": "question", "recognition": value}}})
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		_, outcome := classifyRecognitionLayoutRepairV2(payload(fields), target, plan.RecognitionFormat)
		if outcome == nil || outcome.question == nil {
			t.Fatal("captured source did not parse")
		}
		source := make(map[string]json.RawMessage, len(fields))
		for key, value := range fields {
			source[key] = value
		}
		delete(source, "evidence_transcriptions")
		delete(source, "answer_evidence_transcriptions")
		body := payload(source)
		outcome.source = k12.RecognitionPhysicalCallResult{InvocationID: id, Payload: body, ResultDigest: recognitionLayoutV2TestDigest(body)}
		return *outcome
	}
	primary, review := read(fixture.Initial, "initial-source"), read(fixture.Review, "review-source")
	questionMatch, answerMatch := usecase.RecognitionSourceReadingsMatchForInitialReadMode(*primary.question, *review.question, plan.InitialReadMode)
	if !questionMatch || !answerMatch || k12.RecognitionSourceQuestionsEqualV1(primary.question.AnswerRawTranscription, review.question.AnswerRawTranscription) {
		t.Fatal("captured sources no longer reproduce compatible but different complete working")
	}
	before, _ := json.Marshal(review.question)
	header := recognitionLayoutV2TestDigest("extended-read-header")
	runtime := recognitionLayoutV2RuntimeFixture(header, plan, 2, time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli())
	runtime.Header.InitialReadMode, runtime.Header.ExecutionPolicy = plan.InitialReadMode, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1
	executor := &sourceAdjudicationExecutor{recognitionLayoutRepairWaveExecutorV2: &recognitionLayoutRepairWaveExecutorV2{runtime: runtime, cached: map[k12.RecognitionPhysicalUnit]k12.RecognitionPhysicalCallResult{}, providerSends: map[k12.RecognitionPhysicalUnit]int{}, transportErrByUnit: map[k12.RecognitionPhysicalUnit]error{}}}
	ctx := k12.WithRecognitionPhysicalCallExecutor(k12.WithRecognitionLayoutPlanV2(context.Background(), header), executor)
	var question string
	if err := json.Unmarshal(fixture.Review["question"], &question); err != nil {
		t.Fatal(err)
	}
	response := finalAnswerAdjudicationResponseV1(t, question, "=1/3", false, true, true)
	calls := 0
	adapter := NewRecognizerAdapter(func(context.Context, []byte, string) (string, error) { calls++; return response, nil })
	if err := adapter.recognizeLayoutAdjudicationV2(ctx, page, plan, runtime, target, primary, review); err != nil {
		t.Fatal(err)
	}
	if len(executor.requests) != 1 || executor.requests[0].ConflictKind != "answer" || calls != 1 || len(executor.settlements) != 1 || executor.settlements[0].FinalAnswerEvidence == nil || executor.settlements[0].Adopted {
		t.Fatalf("extended reading used an invalid authorization or erased working risk: requests=%+v calls=%d settlements=%+v", executor.requests, calls, executor.settlements)
	}
	after, _ := json.Marshal(review.question)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("source working or observation changed")
	}
}

func TestRecognitionSameReadUncertaintyUsesOneIndependentFinalAdjudication(t *testing.T) {
	runRecognitionSameReadUncertainty(t, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1, nil)
}

func TestRecognitionLowInitialConfidenceUsesIndependentFinalAdjudication(t *testing.T) {
	confidence := 0.76
	runRecognitionSameReadUncertainty(t, "compact_dag_final_answer_v2", &confidence)
}

func runRecognitionSameReadUncertainty(t *testing.T, policy string, initialConfidence *float64) {
	t.Helper()
	raw, err := os.ReadFile("../testdata/recognition_same_read_uncertainty.json")
	if err != nil {
		t.Fatal(err)
	}
	page := denseWorksheetTestImage(t, 1076, 1462)
	canonical, err := k12.CanonicalizeRecognitionPageV2(page)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{
		PagePNG: canonical.PNG, Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: "same-read-whole", ResultDigest: recognitionLayoutV2TestDigest("same-read")},
		Targets:           []k12.RecognitionLayoutManifestTargetV2{{ManifestRef: "manifest_0001", ManifestOrder: 1, SourceNumberPath: []string{"3"}, DisplayLabel: "3.", Region: k12.SourcePixelRegion{X: 621, Y: 248, Width: 412, Height: 138}}},
		RecognitionFormat: k12.RecognitionLayoutCompactV4, InitialReadMode: k12.RecognitionLayoutManifestWithContentV1, EnableSourceAdjudication: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := plan.Targets[0]
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		t.Fatal("invalid observation fixture")
	}
	var questions []string
	if json.Unmarshal(fields["evidence_transcriptions"], &questions) != nil {
		t.Fatal("missing independent questions")
	}
	payload := func(fields map[string]json.RawMessage) string {
		p, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"target_id": "t1", "kind": "question", "recognition": fields}}})
		return string(p)
	}
	reviewRaw := payload(fields)
	_, review := classifyRecognitionLayoutRepairV2(reviewRaw, target, plan.RecognitionFormat)
	fields["question"], _ = json.Marshal(questions[0])
	fields["ocr_signals"] = json.RawMessage(`[]`)
	if initialConfidence != nil {
		fields["recognition_confidence"], _ = json.Marshal(*initialConfidence)
	}
	delete(fields, "evidence_transcriptions")
	delete(fields, "answer_evidence_transcriptions")
	primaryRaw := payload(fields)
	_, primary := classifyRecognitionLayoutRepairV2(primaryRaw, target, plan.RecognitionFormat)
	if primary == nil || review == nil || primary.question == nil || review.question == nil {
		t.Fatal("actual observation did not parse")
	}
	primary.source = k12.RecognitionPhysicalCallResult{InvocationID: "whole-source", ResultDigest: recognitionLayoutV2TestDigest(primaryRaw), Payload: primaryRaw}
	review.source = k12.RecognitionPhysicalCallResult{InvocationID: "review-source", ResultDigest: recognitionLayoutV2TestDigest(reviewRaw), Payload: reviewRaw}
	if review.question.ConfirmationRequired {
		t.Fatal("fixture does not reproduce the same-reading risk gap")
	}
	for _, policy := range []string{"", k12.RecognitionExecutionPolicyCompactDAGV1} {
		if recognitionNeedsAdjudicationV2(*primary, *review, plan.InitialReadMode, policy) {
			t.Fatal("old policy acquired a new invocation")
		}
	}
	if initialConfidence != nil && recognitionNeedsAdjudicationV2(*primary, *review, plan.InitialReadMode, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1) {
		t.Fatal("legacy final-answer policy acquired a new invocation")
	}
	if !recognitionNeedsAdjudicationV2(*primary, *review, plan.InitialReadMode, policy) {
		t.Fatal("unresolved observation did not enter independent adjudication")
	}
	cleared := *review.question
	cleared.OCRSignals = nil
	clearedReview := *review
	clearedReview.question = &cleared
	initialRisk := *primary.question
	initialRisk.OCRSignals = append([]string(nil), review.question.OCRSignals...)
	initialObservation := *primary
	initialObservation.question = &initialRisk
	if recognitionNeedsAdjudicationV2(initialObservation, clearedReview, plan.InitialReadMode, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1) {
		t.Fatal("already resolved first-read observation caused another adjudication")
	}
	before, _ := json.Marshal(review.question)
	header := recognitionLayoutV2TestDigest("same-read-header")
	runtime := recognitionLayoutV2RuntimeFixture(header, plan, 2, time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli())
	runtime.Header.InitialReadMode, runtime.Header.ExecutionPolicy = plan.InitialReadMode, policy
	executor := &sourceAdjudicationExecutor{recognitionLayoutRepairWaveExecutorV2: &recognitionLayoutRepairWaveExecutorV2{runtime: runtime, cached: map[k12.RecognitionPhysicalUnit]k12.RecognitionPhysicalCallResult{}, providerSends: map[k12.RecognitionPhysicalUnit]int{}, transportErrByUnit: map[k12.RecognitionPhysicalUnit]error{}}}
	ctx := k12.WithRecognitionPhysicalCallExecutor(k12.WithRecognitionLayoutPlanV2(context.Background(), header), executor)
	response := finalAnswerAdjudicationResponseV1(t, questions[1], "=1/3", false, true, true)
	adapter := NewRecognizerAdapter(func(context.Context, []byte, string) (string, error) { return response, nil })
	if err := adapter.recognizeLayoutAdjudicationV2(ctx, canonical.PNG, plan, runtime, target, *primary, *review); err != nil {
		t.Fatal(err)
	}
	if len(executor.requests) != 1 || executor.requests[0].ConflictKind != "observation_uncertainty" || len(executor.settlements) != 1 || executor.settlements[0].FinalAnswerEvidence == nil || executor.settlements[0].Adopted || len(executor.settlements[0].ResultJSON) != 0 {
		t.Fatal("same-reading final evidence did not retain its independent bounded contract")
	}
	after, _ := json.Marshal(review.question)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("source working or unresolved observation changed")
	}
}
