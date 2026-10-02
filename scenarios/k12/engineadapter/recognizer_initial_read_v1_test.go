package engineadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestRecognitionInitialReadV1RedundantObservationsPreserveActualManifest(t *testing.T) {
	raw, err := os.ReadFile("testdata/initial_read_redundant_observations.json")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parseRecognitionInitialReadEnvelopeV1(string(raw))
	if err != nil || len(entries) != 16 {
		t.Fatalf("complete successful manifest rejected: entries=%d err=%v", len(entries), err)
	}
	for index, entry := range entries {
		var observation map[string]json.RawMessage
		if json.Unmarshal(entry.raw, &observation) != nil || len(observation["shared_conditions"]) == 0 || len(observation["answer_ownership"]) == 0 || entry.manifest.ManifestOrder != index+1 {
			t.Fatalf("raw observation or manifest identity lost: %s", entry.raw)
		}
	}
	for _, failure := range []string{"conflicting shared conditions", "conflicting ownership", "unknown field", "duplicate reference"} {
		t.Run(failure, func(t *testing.T) {
			var envelope struct {
				Targets []map[string]json.RawMessage `json:"targets"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "conflicting shared conditions":
				envelope.Targets[0]["shared_conditions"] = json.RawMessage(`"each bag contains 2"`)
			case "conflicting ownership":
				envelope.Targets[0]["answer_ownership"] = json.RawMessage(`"neighboring answer"`)
			case "unknown field":
				envelope.Targets[0]["invented_answer"] = json.RawMessage(`"8"`)
			case "duplicate reference":
				envelope.Targets[1]["manifest_ref"] = envelope.Targets[0]["manifest_ref"]
			}
			mutated, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseRecognitionInitialReadEnvelopeV1(string(mutated)); !errors.Is(err, k12.ErrRecognitionProtocolInvalid) {
				t.Fatalf("invalid manifest accepted: %v", err)
			}
		})
	}
}

func TestRecognitionInitialReadV1FrozenSectionPreservesActualSourceFacts(t *testing.T) {
	raw, err := os.ReadFile("testdata/initial_read_section_observations.json")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parseRecognitionInitialReadEnvelopeV1(string(raw))
	if err != nil || len(entries) != 17 {
		t.Fatalf("successful source manifest rejected: %v", err)
	}
	entry := entries[7]
	target := k12.RecognitionLayoutTargetV2{TargetID: "target", Region: entry.manifest.Region}
	projection, err := recognitionInitialReadProjectionV1(entry, target)
	if err != nil {
		t.Fatalf("frozen section metadata discarded source observation: %v", err)
	}
	var projected struct {
		Recognition struct {
			Question      string `json:"question"`
			StudentAnswer string `json:"student_answer"`
		} `json:"recognition"`
	}
	if err := json.Unmarshal(projection, &projected); err != nil || projected.Recognition.Question != `7-\(\frac{5}{7}\)=` || projected.Recognition.StudentAnswer != `6\frac{2}{7}` {
		t.Fatalf("printed expression or complete mixed number was lost: %s err=%v", projection, err)
	}
	var read map[string]json.RawMessage
	if err := json.Unmarshal(entry.read, &read); err != nil {
		t.Fatal(err)
	}
	read["shared_conditions"] = json.RawMessage(`"一、直接写得数。每包2个。"`)
	entry.read, _ = json.Marshal(read)
	if _, err := recognitionInitialReadProjectionV1(entry, target); err == nil {
		t.Fatal("numeric shared conditions absent from the question were accepted")
	}
}

func TestRecognitionInitialReadV1SectionMetadataStillAdjudicatesClippedSource(t *testing.T) {
	for _, scenario := range []struct{ name, firstQuestion, firstAnswer, rereadQuestion, rereadAnswer string }{
		{"missing left operand", `0.5+\(\frac{1}{3}\)=`, `\(\frac{2}{3}\)`, `\(\frac{1}{3}\)=`, `\(\frac{2}{3}\)`},
		{"missing mixed-number integer", `7-\(\frac{5}{7}\)=`, `6\frac{2}{7}`, `7-\(\frac{5}{7}\)=`, `\(\frac{2}{7}\)`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			entry := initialReadAdapterEntryV1("manifest_0001", 1, 40, scenario.firstQuestion, scenario.firstAnswer, "一、直接写得数", 0.80)
			entry["source_section_path"] = []string{"一"}
			entry["source_section_label"] = "一、直接写得数"
			whole, _ := json.Marshal(map[string]any{"targets": []any{entry}})
			review, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"target_id": "t1", "kind": "question", "recognition": map[string]any{"question": scenario.rereadQuestion, "subject": "数学", "answer_state": "present", "student_answer": scenario.rereadAnswer, "recognition_confidence": 0.99, "ocr_signals": []string{}, "answer_bbox": k12.SourcePixelRegion{X: 130, Y: 35, Width: 20, Height: 20}}}}})
			executor := &jsonOutputInitialReadExecutorV1{initialReadAdapterExecutorV1: &initialReadAdapterExecutorV1{}}
			ctx := k12.WithRecognitionPhysicalCallExecutor(k12.WithRecognitionLayoutInitialReadMode(k12.WithRecognitionLayoutPlanV2(t.Context(), recognitionLayoutV2TestDigest("initial-read-header")), k12.RecognitionLayoutManifestWithContentV1), executor)
			stop := errors.New("source adjudication reached")
			_, err := NewRecognizerAdapter(func(_ context.Context, _ []byte, prompt string) (string, error) {
				switch {
				case strings.Contains(prompt, "SAME JSON response"):
					return string(whole), nil
				case strings.Contains(prompt, "Independently verify one worksheet target"):
					return "", stop
				default:
					return string(review), nil
				}
			}).Recognize(ctx, denseWorksheetTestImage(t, 320, 320))
			if !errors.Is(err, stop) || len(executor.initial.FirstReads) != 1 || len(executor.initial.FirstReads[0].ResultJSON) == 0 {
				t.Fatalf("source conflict bypassed independent adjudication: %v", err)
			}
		})
	}
}

// 替身仅覆盖获授权的 Provider/结算边界；真实 SQLite 事务与 unknown 回放另由存储组验证。
type initialReadAdapterExecutorV1 struct {
	mu       sync.Mutex
	runtime  k12.RecognitionLayoutPlanRuntimeV2
	initial  k12.RecognitionLayoutInitialReadSettlementResultV1
	whole    k12.RecognitionPhysicalCallResult
	calls    []k12.RecognitionPhysicalCall
	physical []k12.RecognitionLayoutPhysicalResultEvidenceV2
	results  map[string]k12.RecognitionLayoutCandidateFinalResultV2
}

func (e *initialReadAdapterExecutorV1) ExecuteRecognitionPhysicalCall(ctx context.Context, call k12.RecognitionPhysicalCall, send func(context.Context) (string, error)) (k12.RecognitionPhysicalCallResult, error) {
	raw, err := send(ctx)
	if err != nil {
		return k12.RecognitionPhysicalCallResult{}, err
	}
	physical := k12.RecognitionPhysicalCallResult{Payload: raw, InvocationID: "modelphysical-" + recognitionLayoutV2TestDigest(string(call.Unit))[7:39], ResultDigest: recognitionLayoutV2TestDigest(raw)}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, call)
	evidence := k12.RecognitionLayoutPhysicalResultEvidenceV2{PhysicalInvocationID: physical.InvocationID, PhysicalUnit: call.Unit, ResultDigest: physical.ResultDigest, PlanDigest: call.PlanDigest, Attempt: 1}
	if len(call.TargetIDs) > 0 {
		evidence.CandidateExactSetDigest, _ = k12.RecognitionLayoutTargetExactSetDigestV2(call.TargetIDs)
	}
	e.physical = append(e.physical, evidence)
	if call.Unit == k12.RecognitionPhysicalUnitWholePage {
		e.whole = physical
	}
	return physical, nil
}

func (e *initialReadAdapterExecutorV1) LookupRecognitionLayoutPlanV2(context.Context) (k12.RecognitionLayoutPlanV2, bool, error) {
	return k12.RecognitionLayoutPlanV2{}, false, nil
}
func (e *initialReadAdapterExecutorV1) LoadRecognitionLayoutPlanV2Runtime(context.Context) (k12.RecognitionLayoutPlanRuntimeV2, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runtime, nil
}

func (e *initialReadAdapterExecutorV1) AuthorizeAndSettleRecognitionLayoutInitialReadV1(_ context.Context, _ k12.RecognitionPhysicalCallResult, plan k12.RecognitionLayoutPlanV2, settlement k12.RecognitionLayoutInitialReadSettlementV1) (k12.RecognitionLayoutInitialReadSettlementResultV1, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runtime = recognitionLayoutV2RuntimeFixture(recognitionLayoutV2TestDigest("initial-read-header"), plan, 2, time.Now().Add(-time.Second).UnixMilli(), time.Now().Add(time.Minute).UnixMilli())
	e.runtime.Header.InitialReadMode = k12.RecognitionLayoutManifestWithContentV1
	e.runtime.Header.PlanID = "initial-read-plan"
	e.runtime.Header.ParentInvocationID = "initial-read-parent"
	e.runtime.Header.PageDigest = plan.PageDigest
	e.runtime.ManifestPhysicalInvocationID = plan.ManifestInvocationID
	e.runtime.ManifestResultDigest = plan.ManifestResultDigest
	ids := make([]string, len(plan.Targets))
	for i, target := range plan.Targets {
		ids[i] = target.TargetID
	}
	e.runtime.CandidateExactSetDigest, _ = k12.RecognitionLayoutTargetExactSetDigestV2(ids)
	e.results = make(map[string]k12.RecognitionLayoutCandidateFinalResultV2)
	e.initial = k12.RecognitionLayoutInitialReadSettlementResultV1{Classification: settlement.Classification, SettlementDigest: recognitionLayoutV2TestDigest("initial-settlement")}
	for _, candidate := range settlement.Candidates {
		firstReadDigest, err := k12.RecognitionLayoutFirstReadDigestV1(e.runtime.Header.ParentInvocationID, settlement, candidate)
		if err != nil {
			return k12.RecognitionLayoutInitialReadSettlementResultV1{}, false, err
		}
		resultDigest := ""
		if len(candidate.ResultJSON) > 0 {
			identity := k12.RecognitionLayoutPrimaryBatchSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, SourcePhysicalInvocationID: e.whole.InvocationID, SourcePhysicalUnit: k12.RecognitionPhysicalUnitWholePage, SourcePhysicalResultDigest: e.whole.ResultDigest}
			resultDigest, err = k12.RecognitionLayoutCandidateResultDigestV2(e.runtime.Header.ParentInvocationID, identity, k12.RecognitionLayoutCandidateSettlementV2{CandidateID: candidate.CandidateID, Classification: candidate.Classification, ResultKind: candidate.ResultKind, ResultJSON: candidate.ResultJSON})
			if err != nil {
				return k12.RecognitionLayoutInitialReadSettlementResultV1{}, false, err
			}
		}
		e.initial.FirstReads = append(e.initial.FirstReads, k12.RecognitionLayoutInitialReadReceiptV1{RecognitionLayoutInitialCandidateV1: candidate, FirstReadDigest: firstReadDigest, ResultDigest: resultDigest})
		if candidate.Classification == k12.RecognitionLayoutCandidateValidV2 {
			frozen, err := e.freeze(candidate.CandidateID, candidate.ResultKind, candidate.ResultJSON, e.whole, k12.RecognitionPhysicalUnitWholePage)
			if err != nil {
				return k12.RecognitionLayoutInitialReadSettlementResultV1{}, false, err
			}
			e.initial.FrozenResults = append(e.initial.FrozenResults, frozen)
		} else {
			e.initial.ReviewAuthorizations = append(e.initial.ReviewAuthorizations, k12.RecognitionLayoutReviewMemberAuthorizationV1{AuthorizationID: "member-" + candidate.CandidateID, AuthorizationDigest: recognitionLayoutV2TestDigest(candidate.CandidateID), CandidateID: candidate.CandidateID, ReviewRound: 1})
		}
	}
	return e.initial, true, nil
}

func (e *initialReadAdapterExecutorV1) freeze(id string, kind k12.RecognitionLayoutCandidateResultKindV2, raw json.RawMessage, source k12.RecognitionPhysicalCallResult, unit k12.RecognitionPhysicalUnit) (k12.RecognitionLayoutCandidateResultReceiptV2, error) {
	identity := k12.RecognitionLayoutPrimaryBatchSettlementV2{PlanDigest: e.runtime.AuthorizedPlan.AuthorizedPlanDigest, SourcePhysicalInvocationID: source.InvocationID, SourcePhysicalUnit: unit, SourcePhysicalResultDigest: source.ResultDigest}
	digest, err := k12.RecognitionLayoutCandidateResultDigestV2(e.runtime.Header.ParentInvocationID, identity, k12.RecognitionLayoutCandidateSettlementV2{CandidateID: id, ResultKind: kind, ResultJSON: raw})
	if err != nil {
		return k12.RecognitionLayoutCandidateResultReceiptV2{}, err
	}
	e.results[id] = k12.RecognitionLayoutCandidateFinalResultV2{CandidateID: id, ResultKind: kind, ResultDigest: digest, ResultJSON: raw, SourcePhysicalInvocationID: source.InvocationID, SourcePhysicalUnit: unit, SourcePhysicalResultDigest: source.ResultDigest}
	return k12.RecognitionLayoutCandidateResultReceiptV2{CandidateID: id, ResultKind: kind, ResultDigest: digest}, nil
}

func (e *initialReadAdapterExecutorV1) AuthorizeRecognitionLayoutReviewBatchV1(_ context.Context, request k12.RecognitionLayoutReviewBatchAuthorizationRequestV1) (k12.RecognitionLayoutReviewBatchAuthorizationV1, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ids := make([]string, len(request.Members))
	for i, member := range request.Members {
		ids[i] = member.CandidateID
	}
	digest, _ := k12.RecognitionLayoutTargetExactSetDigestV2(ids)
	auth := k12.RecognitionLayoutReviewBatchAuthorizationV1{RecognitionLayoutReviewBatchAuthorizationRequestV1: request, AuthorizationID: "batch-" + string(request.PhysicalUnit), AuthorizationDigest: recognitionLayoutV2TestDigest(string(request.PhysicalUnit)), OrderedTargetIDs: ids, ExactSetDigest: digest, ReviewRound: 1}
	e.runtime.ReviewBatches = append(e.runtime.ReviewBatches, auth)
	return auth, true, nil
}

func (e *initialReadAdapterExecutorV1) SettleRecognitionLayoutReviewBatchV1(_ context.Context, source k12.RecognitionPhysicalCallResult, settlement k12.RecognitionLayoutReviewBatchSettlementV1) (k12.RecognitionLayoutReviewBatchSettlementResultV1, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := k12.RecognitionLayoutReviewBatchSettlementResultV1{Classification: settlement.Classification, SettlementDigest: recognitionLayoutV2TestDigest("review-settlement")}
	for _, candidate := range settlement.Candidates {
		if candidate.Classification == k12.RecognitionLayoutCandidateValidV2 {
			frozen, err := e.freeze(candidate.CandidateID, candidate.ResultKind, candidate.ResultJSON, source, settlement.SourcePhysicalUnit)
			if err != nil {
				return result, false, err
			}
			result.FrozenResults = append(result.FrozenResults, frozen)
		} else {
			result.UnresolvedCandidateIDs = append(result.UnresolvedCandidateIDs, candidate.CandidateID)
		}
	}
	return result, true, nil
}

func (e *initialReadAdapterExecutorV1) FinalizeRecognitionLayoutPlanV2(context.Context) (k12.RecognitionLayoutPlanFinalizationResultV2, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	plan := e.runtime.AuthorizedPlan
	result := k12.RecognitionLayoutPlanFinalizationResultV2{PlanID: e.runtime.Header.PlanID, PlanDigest: plan.AuthorizedPlanDigest, CandidateExactSetDigest: e.runtime.CandidateExactSetDigest, PhysicalResults: e.physical, PhysicalResultCount: len(e.physical), CandidateResultCount: len(plan.Targets)}
	for _, target := range plan.Targets {
		candidate, exists := e.results[target.TargetID]
		if !exists {
			return result, false, fmt.Errorf("candidate is not settled")
		}
		result.CandidateResults = append(result.CandidateResults, candidate)
	}
	var err error
	result.CandidateResultsExactSetDigest, err = k12.RecognitionLayoutCandidateResultsExactSetDigestV2(result.CandidateResults)
	if err != nil {
		return result, false, err
	}
	result.PhysicalResultsExactSetDigest, err = k12.RecognitionLayoutPhysicalResultsExactSetDigestV2(result.PhysicalResults)
	if err != nil {
		return result, false, err
	}
	_, result.FinalizationDigest, err = k12.CanonicalRecognitionLayoutPlanFinalizationV2(e.runtime.Header.ParentInvocationID, result)
	if err != nil {
		return result, false, err
	}
	return result, true, nil
}

func initialReadAdapterEntryV1(ref string, order, y int, question, answer, shared string, confidence float64) map[string]any {
	return map[string]any{"manifest_ref": ref, "manifest_order": order, "source_number_path": []string{fmt.Sprint(order)}, "display_label": fmt.Sprintf("%d.", order), "source_section_path": []string{}, "source_section_label": "", "region": k12.SourcePixelRegion{X: 30, Y: y, Width: 180, Height: 70}, "initial_read": map[string]any{"kind": "question", "recognition": map[string]any{"question": question, "subject": "数学", "answer_state": "present", "student_answer": answer, "recognition_confidence": confidence, "ocr_signals": []string{}, "answer_bbox": k12.SourcePixelRegion{X: 160, Y: y + 35, Width: 20, Height: 20}}, "shared_conditions": shared, "answer_ownership": "visible active handwriting in this target's answer area"}}
}

type jsonOutputInitialReadExecutorV1 struct{ *initialReadAdapterExecutorV1 }

func (e *jsonOutputInitialReadExecutorV1) AuthorizeRecognitionLayoutAdjudicationV2(_ context.Context, request k12.RecognitionLayoutAdjudicationRequestV2) (k12.RecognitionLayoutAdjudicationAuthorizationV2, bool, error) {
	return k12.RecognitionLayoutAdjudicationAuthorizationV2{AuthorizationID: "json-output-adjudication", AuthorizationDigest: recognitionLayoutV2TestDigest("json-output-adjudication"), CandidateID: request.CandidateID, PhysicalUnit: "layout_adjudicate_0001"}, true, nil
}

// 从公开首读入口观察实际 sender 上下文；独立复读冲突继续触发裁决，均不继承首读输出选择。
func TestRecognitionInitialReadV1JSONOutputScopeSurvivesPhysicalSender(t *testing.T) {
	entry := initialReadAdapterEntryV1("manifest_0001", 1, 40, "5+5=", "10", "", 0.80)
	raw, err := json.Marshal(map[string]any{"targets": []any{entry}})
	if err != nil {
		t.Fatal(err)
	}
	executor := &jsonOutputInitialReadExecutorV1{initialReadAdapterExecutorV1: &initialReadAdapterExecutorV1{}}
	ctx := k12.WithRecognitionLayoutPlanV2(t.Context(), recognitionLayoutV2TestDigest("initial-read-header"))
	ctx = k12.WithRecognitionLayoutInitialReadMode(ctx, k12.RecognitionLayoutManifestWithContentV1)
	ctx = k12.WithRecognitionPhysicalCallExecutor(ctx, executor)
	stop := errors.New("stop after observing adjudication request")
	var mu sync.Mutex
	var phases []string
	var jsonOutput []bool
	recognizer := NewRecognizerAdapter(func(visionCtx context.Context, _ []byte, prompt string) (string, error) {
		phase := "review_batch"
		if strings.Contains(prompt, "SAME JSON response") {
			phase = "whole_page_initial_read"
		} else if strings.Contains(prompt, "Independently verify one worksheet target") {
			phase = "adjudication"
		}
		mu.Lock()
		phases = append(phases, phase)
		jsonOutput = append(jsonOutput, k12.RecognitionLayoutInitialReadJSONOutputFromContext(visionCtx))
		mu.Unlock()
		switch phase {
		case "whole_page_initial_read":
			return string(raw), nil
		case "adjudication":
			return "", stop
		default:
			return `{"items":[{"target_id":"t1","kind":"question","recognition":{"question":"5+5=","subject":"数学","answer_state":"present","student_answer":"12","recognition_confidence":0.99,"ocr_signals":[],"answer_bbox":{"x":130,"y":35,"width":20,"height":20}}}]}`, nil
		}
	}, WithRecognizerProviderTransportSendBoundary())
	_, err = recognizer.Recognize(ctx, denseWorksheetTestImage(t, 320, 320))
	if !errors.Is(err, stop) {
		t.Fatalf("adjudication sender was not reached: %v", err)
	}
	if !reflect.DeepEqual(phases, []string{"whole_page_initial_read", "review_batch", "adjudication"}) {
		t.Fatalf("actual provider request phases=%v", phases)
	}
	if !reflect.DeepEqual(jsonOutput, []bool{true, false, false}) {
		t.Fatalf("actual sender JSON output scopes=%v, want first read only", jsonOutput)
	}
}

func TestRecognitionInitialReadV1PublicAdapterCoverageAndIndependentReview(t *testing.T) {
	entries := []map[string]any{initialReadAdapterEntryV1("manifest_0002", 2, 160, "5+5=", "5+5=10\n10", "", 0.80), initialReadAdapterEntryV1("manifest_0001", 1, 40, "每包2个。3包共有多少个？", "2×3=6\n6个", "每包2个。", 0.80)}
	raw, _ := json.Marshal(map[string]any{"targets": entries})
	executor := &initialReadAdapterExecutorV1{}
	ctx := k12.WithRecognitionLayoutPlanV2(context.Background(), recognitionLayoutV2TestDigest("initial-read-header"))
	ctx = k12.WithRecognitionLayoutInitialReadMode(ctx, k12.RecognitionLayoutManifestWithContentV1)
	ctx = k12.WithRecognitionPhysicalCallExecutor(ctx, executor)
	var reviewPrompt string
	recognizer := NewRecognizerAdapter(func(_ context.Context, _ []byte, prompt string) (string, error) {
		if strings.Contains(prompt, "SAME JSON response") {
			return string(raw), nil
		}
		reviewPrompt = prompt
		return `{"items":[{"target_id":"t1","kind":"question","recognition":{"question":"每包2个。3包共有多少个？","subject":"数学","answer_state":"present","student_answer":"2×3=6\n6个","recognition_confidence":0.99,"ocr_signals":[],"answer_bbox":{"x":130,"y":35,"width":20,"height":20}}},{"target_id":"t2","kind":"question","recognition":{"question":"5+5=","subject":"数学","answer_state":"present","student_answer":"5+5=10\n10","recognition_confidence":0.99,"ocr_signals":[],"answer_bbox":{"x":130,"y":35,"width":20,"height":20}}}]}`, nil
	})
	questions, err := recognizer.Recognize(ctx, denseWorksheetTestImage(t, 320, 320))
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 2 || questions[0].Question != "每包2个。3包共有多少个？" || questions[0].StudentAnswer != "2×3=6\n6个" || questions[1].StudentAnswer != "5+5=10\n10" {
		t.Fatalf("shared conditions or student ownership drifted: %+v", questions)
	}
	if len(executor.calls) != 2 || executor.calls[0].Unit != k12.RecognitionPhysicalUnitWholePage || string(executor.calls[1].Unit) != "layout_review_batch_0001" || len(executor.calls[1].TargetIDs) != 2 || len(executor.runtime.AuthorizedPlan.Batches) != 0 {
		t.Fatalf("expected one whole call and one real shared review receipt: %+v", executor.calls)
	}
	for _, secret := range []string{"每包2个。3包共有多少个？", "2×3=6", "5+5=10"} {
		if strings.Contains(reviewPrompt, secret) {
			t.Fatalf("independent reread received initial content %q", secret)
		}
	}
	for _, read := range executor.initial.FirstReads {
		if !bytes.Contains(read.FirstReadJSON, []byte(`"answer_ownership"`)) || !bytes.Contains(read.FirstReadJSON, []byte(`"shared_conditions"`)) {
			t.Fatal("private raw observation was lost")
		}
	}
	if questions[0].ObservedAnswerRegion == nil || questions[0].ObservedAnswerRegion.X != executor.runtime.AuthorizedPlan.Targets[0].Region.X+130 {
		t.Fatalf("crop relative bbox did not map to original page: %+v", questions[0].ObservedAnswerRegion)
	}
	// 保存实际首读回执的原始 JSON 与身份摘要，完整观察的绝对坐标和投影坐标各自绑定。
	const storedReceiptJSON = `{"candidate_id":"layout_target_v2_a46fc683461a5fea3f07a54e4a67c8fbb957da9f63268e432a9daeaa03efa0c0","classification":"valid","result_kind":"question","first_read_json":{"display_label":"","initial_read":{"answer_ownership":"Handwritten 8 appears in the answer space after the printed equals sign.","kind":"question","recognition":{"answer_bbox":{"height":32,"width":17,"x":212,"y":246},"answer_state":"present","ocr_signals":[],"question":"4 ÷ 0.5 =","recognition_confidence":0.99,"student_answer":"8","subject":"数学"},"shared_conditions":""},"manifest_order":1,"manifest_ref":"manifest_0001","region":{"height":49,"width":143,"x":105,"y":236},"source_number_path":[],"source_section_label":"一、直接写得数","source_section_path":["一"]},"result_json":{"answer_bbox":{"height":32,"width":17,"x":156,"y":34},"answer_state":"present","ocr_signals":[],"question":"4 ÷ 0.5 =","recognition_confidence":0.99,"student_answer":"8","subject":"数学"},"first_read_digest":"sha256:439ff6c9468e3cca43bf9b1ffa9cfc6188f08efe11e412b92ae8e73459ecdb4e","result_digest":"sha256:2e5cd51a985e13c94a8bfde15133f8ca6fa9c7dc7d3f330edc818b0fd6e86db2"}`
	const storedParent = "modelinv-LxHMchnx"
	const storedPlan = "sha256:4d37ccfe0f711e61cc7360db643bcf8a1aada1a51101c1f1b5e50a27a55faf6a"
	storedSource := k12.RecognitionPhysicalCallResult{InvocationID: "modelphysical-8b7f920289b429b6a7224f191d4d59c6", ResultDigest: "sha256:38ffb0f3a4be84c46d3f4f6b0e2b10b51e71d5c9f0a7ad47356f7a500618c61e"}
	var storedRead k12.RecognitionLayoutInitialReadReceiptV1
	if err := json.Unmarshal([]byte(storedReceiptJSON), &storedRead); err != nil {
		t.Fatal(err)
	}
	if err := validateRecognitionInitialReadObservationV1(storedParent, storedPlan, storedSource, storedRead.CandidateID, storedRead); err != nil {
		t.Fatalf("actual identity-bound receipt was rejected: %v", err)
	}
	for _, name := range []string{"first read content", "strict result content", "parent", "plan", "source", "source result", "candidate"} {
		t.Run(name, func(t *testing.T) {
			read, parent, plan, source := storedRead, storedParent, storedPlan, storedSource
			switch name {
			case "first read content":
				read.FirstReadJSON = bytes.Replace(read.FirstReadJSON, []byte(`"student_answer":"8"`), []byte(`"student_answer":"7"`), 1)
			case "strict result content":
				read.ResultJSON = bytes.Replace(read.ResultJSON, []byte(`"student_answer":"8"`), []byte(`"student_answer":"7"`), 1)
			case "parent":
				parent += "-other"
			case "plan":
				plan = recognitionLayoutV2TestDigest("other plan")
			case "source":
				source.InvocationID += "-other"
			case "source result":
				source.ResultDigest = recognitionLayoutV2TestDigest("other source result")
			case "candidate":
				read.CandidateID += "-other"
			}
			if err := validateRecognitionInitialReadObservationV1(parent, plan, source, storedRead.CandidateID, read); !errors.Is(err, k12.ErrRecognitionLayoutPlanV2Unauthorized) {
				t.Fatalf("changed %s was accepted: %v", name, err)
			}
		})
	}
	for _, scenario := range []struct {
		name, question, answer, shared string
		confidence                     float64
		needsReview                    bool
		reviewQuestion                 string
	}{
		{"printed direct instruction", "直接写得数：4÷0.5=", "8", "直接写得数：", 0.99, false, ""},
		{"printed direct instruction without shared colon", "直接写得数：4÷0.5=", "8", "直接写得数", 0.99, false, ""},
		{"printed direct instruction halfwidth colon", "直接写得数:4÷0.5=", "8", "直接写得数:", 0.99, false, ""},
		{"printed fraction instruction", `把下面每题的得数化简：\(\frac{5}{7}-\frac{1}{5}=\)`, `\(\frac{18}{35}\)`, "把下面每题的得数化简：", 0.98, false, ""},
		{"printed fraction instruction without shared colon", `把下面每题的得数化简：\(\frac{5}{7}-\frac{1}{5}=\)`, `\(\frac{18}{35}\)`, "把下面每题的得数化简", 0.98, false, ""},
		{"printed fraction instruction paired colon width", `把下面每题的得数化简:\(\frac{5}{7}-\frac{1}{5}=\)`, `\(\frac{18}{35}\)`, "把下面每题的得数化简：", 0.98, false, ""},
		{"printed calculation instruction", `计算下面各题，能简算的要简算：\(8.7\times17.4-8.7\times7.4\)`, "= 8.7 × (17.4 − 7.4)\n= 8.7 × 10\n= 87", "计算下面各题，能简算的要简算：", 0.99, false, ""},
		{"printed calculation instruction without shared colon", `计算下面各题，能简算的要简算：\(8.7\times17.4-8.7\times7.4\)`, "= 8.7 × (17.4 − 7.4)\n= 8.7 × 10\n= 87", "计算下面各题，能简算的要简算", 0.99, false, ""},
		{"printed calculation instruction paired colon width", "计算下面各题，能简算的要简算：2+2=", "4", "计算下面各题，能简算的要简算:", 0.99, false, ""},
		{"direct instruction without frozen shared fact", "直接写得数：2+2=", "4", "", 0.99, true, ""},
		{"different frozen heading", "直接写得数：2+2=", "4", "把下面每题的得数化简", 0.99, true, ""},
		{"heading with added numeric condition", "直接写得数：每包2个。2+2=", "4", "直接写得数：每包2个。", 0.99, true, ""},
		{"heading with added unit condition", "直接写得数：单位为米。2+2=", "4", "直接写得数：单位为米。", 0.99, true, ""},
		{"heading with added method condition", "直接写得数：先乘后加。2+2=", "4", "直接写得数：先乘后加。", 0.99, true, ""},
		{"heading with repeated shared colon", "直接写得数：：2+2=", "4", "直接写得数：：", 0.99, true, ""},
		{"heading without question colon", "直接写得数2+2=", "4", "直接写得数", 0.99, true, ""},
		{"paired direct heading with wrong answer", "直接写得数：2+2=", "5", "直接写得数", 0.99, true, ""},
		{"wrong final value", `把下面每题的得数化简：\(\frac{5}{7}-\frac{1}{5}=\)`, `\(\frac{19}{35}\)`, "把下面每题的得数化简：", 0.99, true, ""},
		{"wrong intermediate step", `计算下面各题，能简算的要简算：\(8.7\times17.4-8.7\times7.4\)`, "= 8.7 × (17.4 − 7.4)\n= 8.7 × 11\n= 87", "计算下面各题，能简算的要简算：", 0.99, true, ""},
		{"numeric shared condition", "每包2个。3包共有多少个？", "2×3=6\n6个", "每包2个。", 0.99, true, ""},
		{"instruction without frozen shared fact", "把下面每题的得数化简：2+2=", "4", "", 0.99, true, ""},
		{"independent OCR uncertainty", "把下面每题的得数化简：2+2=", "4", "把下面每题的得数化简：", 0.60, true, ""},
		{"six-number complete source equivalence", "在下列六个数：5、6、12、14、23、29中划去一个数（ ），后，能使其中3个数的和为另外2个数和的2倍。", "划去：29\n因为：5+23+14=42\n6+12=18\n42=18×2", "", 0.99, true, "在下列六个数：5，6，12，14，23，29中划去数（ ）后，能使其中3个数的和为另外2个数和的2倍。"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			entry := initialReadAdapterEntryV1("manifest_0001", 1, 40, scenario.question, scenario.answer, scenario.shared, scenario.confidence)
			entryJSON, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			wholeJSON, err := json.Marshal(map[string]any{"targets": []any{entry}})
			if err != nil {
				t.Fatal(err)
			}
			reviewQuestion := scenario.question
			if scenario.reviewQuestion != "" {
				reviewQuestion = scenario.reviewQuestion
			}
			independentJSON, err := json.Marshal(map[string]any{"items": []any{map[string]any{"target_id": "t1", "kind": "question", "recognition": map[string]any{"question": reviewQuestion, "subject": "数学", "answer_state": "present", "student_answer": scenario.answer, "recognition_confidence": 0.99, "ocr_signals": []string{}, "answer_bbox": k12.SourcePixelRegion{X: 130, Y: 35, Width: 20, Height: 20}}}}})
			if err != nil {
				t.Fatal(err)
			}
			executor := &initialReadAdapterExecutorV1{}
			ctx := k12.WithRecognitionLayoutInitialReadMode(k12.WithRecognitionLayoutPlanV2(context.Background(), recognitionLayoutV2TestDigest("initial-read-header")), k12.RecognitionLayoutManifestWithContentV1)
			ctx = k12.WithRecognitionPhysicalCallExecutor(ctx, executor)
			questions, err := NewRecognizerAdapter(func(_ context.Context, _ []byte, prompt string) (string, error) {
				if strings.Contains(prompt, "SAME JSON response") {
					return string(wholeJSON), nil
				}
				return string(independentJSON), nil
			}).Recognize(ctx, denseWorksheetTestImage(t, 320, 320))
			if err != nil {
				t.Fatal(err)
			}
			expectedCalls := 1
			if scenario.needsReview {
				expectedCalls = 2
			}
			if len(executor.calls) != expectedCalls || len(executor.initial.ReviewAuthorizations) != expectedCalls-1 {
				t.Fatalf("printed instruction changed review routing: calls=%d reviews=%d", len(executor.calls), len(executor.initial.ReviewAuthorizations))
			}
			if len(questions) != 1 || questions[0].RawTranscription != reviewQuestion || questions[0].AnswerRawTranscription != scenario.answer || (scenario.shared != "" && strings.Contains(scenario.question, scenario.shared) && !strings.HasPrefix(questions[0].Question, scenario.shared)) {
				t.Fatalf("comparison view changed source facts: %+v", questions)
			}
			if scenario.reviewQuestion != "" {
				if questions[0].ConfirmationRequired || !reflect.DeepEqual(questions[0].EvidenceTranscriptions, []string{scenario.question, reviewQuestion}) {
					t.Fatalf("final projection reintroduced a question-only conflict: %+v", questions[0])
				}
			}
			readsBefore, _ := json.Marshal(executor.initial.FirstReads)
			final, _, err := executor.FinalizeRecognitionLayoutPlanV2(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(final)
			replayed, err := RecognizedQuestionsFromLayoutFinalizationV2(final, *executor.runtime.AuthorizedPlan)
			after, _ := json.Marshal(final)
			readsAfter, _ := json.Marshal(executor.initial.FirstReads)
			if err != nil || !reflect.DeepEqual(replayed, questions) || !bytes.Equal(before, after) || !bytes.Equal(readsBefore, readsAfter) || len(executor.calls) != expectedCalls {
				t.Fatalf("frozen final replay changed facts, digests or calls: %v", err)
			}
			var observation struct {
				InitialRead struct {
					SharedConditions string `json:"shared_conditions"`
					Recognition      struct {
						Question      string `json:"question"`
						StudentAnswer string `json:"student_answer"`
					} `json:"recognition"`
				} `json:"initial_read"`
			}
			var original, frozen map[string]any
			if err := json.Unmarshal(entryJSON, &original); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(executor.initial.FirstReads[0].FirstReadJSON, &frozen); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(executor.initial.FirstReads[0].FirstReadJSON, &observation); err != nil || observation.InitialRead.SharedConditions != scenario.shared || observation.InitialRead.Recognition.Question != scenario.question || observation.InitialRead.Recognition.StudentAnswer != scenario.answer || !reflect.DeepEqual(original, frozen) {
				t.Fatal("comparison view overwrote the private initial observation")
			}
		})
	}
}

func TestRecognitionInitialReadV1MissingOrDuplicateCoverageStopsBeforeSettlement(t *testing.T) {
	entry := initialReadAdapterEntryV1("manifest_0001", 1, 40, "2+2=", "4", "", 0.99)
	for _, name := range []string{"missing first read", "duplicate manifest ref"} {
		t.Run(name, func(t *testing.T) {
			copyEntry := map[string]any{}
			for key, value := range entry {
				copyEntry[key] = value
			}
			entries := []map[string]any{copyEntry}
			if name == "missing first read" {
				delete(copyEntry, "initial_read")
			} else {
				entries = append(entries, entry)
			}
			raw, _ := json.Marshal(map[string]any{"targets": entries})
			executor := &initialReadAdapterExecutorV1{}
			ctx := k12.WithRecognitionLayoutInitialReadMode(k12.WithRecognitionLayoutPlanV2(context.Background(), recognitionLayoutV2TestDigest("initial-read-header")), k12.RecognitionLayoutManifestWithContentV1)
			ctx = k12.WithRecognitionPhysicalCallExecutor(ctx, executor)
			_, err := NewRecognizerAdapter(func(context.Context, []byte, string) (string, error) { return string(raw), nil }).Recognize(ctx, denseWorksheetTestImage(t, 320, 320))
			if !errors.Is(err, k12.ErrRecognitionProtocolInvalid) || len(executor.calls) != 1 || executor.runtime.AuthorizedPlan != nil {
				t.Fatalf("incomplete coverage was committed or called again: err=%v calls=%d", err, len(executor.calls))
			}
		})
	}
}

func TestRecognitionInitialReadV1ReviewOriginalPixelsAndOversizeSingleton(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 320, 1200))
	for y := 0; y < 1200; y++ {
		for x := 0; x < 320; x++ {
			source.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), uint8(x + y), 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	manifest := []k12.RecognitionLayoutManifestTargetV2{{ManifestRef: "manifest_0001", ManifestOrder: 1, Region: k12.SourcePixelRegion{X: 30, Y: 20, Width: 180, Height: 70}}, {ManifestRef: "manifest_0002", ManifestOrder: 2, Region: k12.SourcePixelRegion{X: 30, Y: 200, Width: 180, Height: 70}}, {ManifestRef: "manifest_0003", ManifestOrder: 3, Region: k12.SourcePixelRegion{X: 30, Y: 380, Width: 180, Height: 790}}}
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{PagePNG: encoded.Bytes(), Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: "modelphysical-initial-pixels", ResultDigest: recognitionLayoutV2TestDigest("pixels")}, Targets: manifest, RecognitionFormat: k12.RecognitionLayoutCompactV4, InitialReadMode: k12.RecognitionLayoutManifestWithContentV1})
	if err != nil {
		t.Fatal(err)
	}
	sheetPNG, _, height, err := buildRecognitionReviewImageV1(encoded.Bytes(), plan, plan.Targets[:2])
	if err != nil || height > 768 {
		t.Fatalf("contact geometry: height=%d err=%v", height, err)
	}
	sheet, err := png.Decode(bytes.NewReader(sheetPNG))
	if err != nil {
		t.Fatal(err)
	}
	top := 8
	for _, target := range plan.Targets[:2] {
		for y := 0; y < target.Region.Height; y++ {
			for x := 0; x < target.Region.Width; x++ {
				if !reflect.DeepEqual(sheet.At(8+x, top+y), source.At(target.Region.X+x, target.Region.Y+y)) {
					t.Fatal("review contact sheet changed or misplaced original pixels")
				}
			}
		}
		top += target.Region.Height + 8
	}
	oversize, _, oversizeHeight, err := buildRecognitionReviewImageV1(encoded.Bytes(), plan, plan.Targets[2:])
	if err != nil || oversizeHeight <= 768 {
		t.Fatalf("oversize original crop was resized: height=%d err=%v", oversizeHeight, err)
	}
	original, err := k12.BuildRecognitionLayoutRepairImageV2(encoded.Bytes(), plan, plan.Targets[2].TargetID)
	if err != nil || !bytes.Equal(oversize, original) {
		t.Fatal("oversize singleton differs from its frozen original crop")
	}
}
