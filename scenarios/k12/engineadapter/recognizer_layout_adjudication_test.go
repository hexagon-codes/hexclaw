package engineadapter

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"strings"
	"testing"
)

func TestRecognitionLayoutAdjudication_TargetOwnershipAndReadableEvidence(t *testing.T) {
	contextRegion := k12.SourcePixelRegion{X: 400, Y: 100, Width: 500, Height: 400}
	original := k12.SourcePixelRegion{X: 560, Y: 160, Width: 250, Height: 200}
	target := k12.RecognitionLayoutTargetV2{TargetID: "target-1", Region: original, OriginalRegion: &original, AdjudicationRegion: &contextRegion, SourceSectionPath: []string{"五"}, SourceSectionLabel: "五、思维题"}
	prompt, err := buildRecognitionLayoutAdjudicationPrompt(target, k12.RecognitionLayoutCompactV4)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, `"target_region":{"x":160,"y":60,"width":250,"height":200}`) || !strings.Contains(prompt, "五、思维题") {
		t.Fatalf("target context not provided: %s", prompt)
	}
	for _, forbidden := range []string{"225kg", "18/35", "11250"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatal("source prompt leaked an answer")
		}
	}
	tests := []struct {
		name                string
		ownership, complete bool
		answerState, answer string
		bbox                any
		signals             []string
		want                bool
	}{
		{"clear owned active answer", true, true, "present", "5", map[string]int{"x": 200, "y": 180, "width": 30, "height": 25}, []string{}, true},
		{"blank owned target ignores neighboring writing", true, true, "blank", "", nil, []string{}, true},
		{"ambiguous ownership remains unresolved", false, true, "blank", "", nil, []string{}, false},
		{"cropped active answer remains unresolved", true, false, "present", "5", map[string]int{"x": 200, "y": 180, "width": 30, "height": 25}, []string{}, false},
		{"missing location cannot qualify present", true, true, "present", "5", nil, []string{}, false},
		{"free text risk is not hidden by high confidence", true, true, "present", "5", map[string]int{"x": 200, "y": 180, "width": 30, "height": 25}, []string{"rightmost digit near edge"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"verification": map[string]bool{"target_ownership_confirmed": tt.ownership, "active_answer_complete": tt.complete}, "items": []any{map[string]any{"target_id": "t1", "kind": "question", "recognition": map[string]any{"question": "2+3=", "subject": "数学", "answer_state": tt.answerState, "student_answer": tt.answer, "recognition_confidence": 0.99, "ocr_signals": tt.signals, "answer_bbox": tt.bbox}}}})
			_, outcome, verified := parseRecognitionLayoutAdjudication(string(raw), target, k12.RecognitionLayoutCompactV4)
			if verified != tt.want {
				t.Fatalf("verified=%t want=%t", verified, tt.want)
			}
			if verified && tt.answerState == "present" && (outcome.question.ObservedAnswerRegion.X != 600 || outcome.question.ObservedAnswerRegion.Y != 280) {
				t.Fatal("answer location was not mapped to original page")
			}
		})
	}
}

type sourceAdjudicationExecutor struct {
	*recognitionLayoutRepairWaveExecutorV2
	requests    []k12.RecognitionLayoutAdjudicationRequestV2
	settlements []k12.RecognitionLayoutAdjudicationSettlementV2
}

func (e *sourceAdjudicationExecutor) AuthorizeRecognitionLayoutAdjudicationV2(_ context.Context, r k12.RecognitionLayoutAdjudicationRequestV2) (k12.RecognitionLayoutAdjudicationAuthorizationV2, bool, error) {
	e.requests = append(e.requests, r)
	return k12.RecognitionLayoutAdjudicationAuthorizationV2{AuthorizationID: "adjudication-one", AuthorizationDigest: recognitionLayoutV2TestDigest("authorization"), CandidateID: r.CandidateID, PhysicalUnit: "layout_adjudicate_0001"}, len(e.requests) == 1, nil
}
func (e *sourceAdjudicationExecutor) SettleRecognitionLayoutAdjudicationV2(_ context.Context, _ k12.RecognitionPhysicalCallResult, s k12.RecognitionLayoutAdjudicationSettlementV2) (k12.RecognitionLayoutAdjudicationSettlementResultV2, bool, error) {
	e.settlements = append(e.settlements, s)
	r := k12.RecognitionLayoutAdjudicationSettlementResultV2{SettlementDigest: recognitionLayoutV2TestDigest("settlement"), Adopted: s.Adopted}
	if s.Adopted {
		r.FrozenResult = &k12.RecognitionLayoutCandidateResultReceiptV2{CandidateID: s.CandidateID}
	}
	return r, true, nil
}

// 编排只在已保存的独立来源冲突后发送核验；回执归属由存储组另用真实 SQLite 验证。
func TestRecognitionLayoutAdjudication_AdoptsIndependentEvidenceAndStopsUnknown(t *testing.T) {
	page, base := recognitionLayoutV2DispatchPlan(t, 1)
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{PagePNG: page, Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: base.ManifestInvocationID, ResultDigest: base.ManifestResultDigest}, Targets: []k12.RecognitionLayoutManifestTargetV2{{ManifestRef: "manifest_0001", ManifestOrder: 1, Region: base.Targets[0].Region}}, RecognitionFormat: k12.RecognitionLayoutCompactV4, EnableSourceAdjudication: true})
	if err != nil {
		t.Fatal(err)
	}
	target := plan.Targets[0]
	payload := func(answer string, verification bool) string {
		v := map[string]any{"items": []any{map[string]any{"target_id": "t1", "kind": "question", "recognition": map[string]any{"question": "5/7-1/5=", "subject": "数学", "answer_state": "present", "student_answer": answer, "recognition_confidence": 0.99, "ocr_signals": []string{}, "answer_bbox": map[string]int{"x": 20, "y": 20, "width": 12, "height": 12}}}}}
		if verification {
			v["verification"] = map[string]bool{"target_ownership_confirmed": true, "active_answer_complete": true}
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	primaryPayload, repairPayload := payload("14/35", false), payload("18/35", false)
	_, primary := classifyRecognitionLayoutRepairV2(primaryPayload, target, plan.RecognitionFormat)
	_, repair := classifyRecognitionLayoutRepairV2(repairPayload, target, plan.RecognitionFormat)
	if primary == nil || repair == nil {
		t.Fatal("fixture did not parse")
	}
	primary.source = k12.RecognitionPhysicalCallResult{InvocationID: "primary", ResultDigest: recognitionLayoutV2TestDigest(primaryPayload), Payload: primaryPayload}
	repair.source = k12.RecognitionPhysicalCallResult{InvocationID: "repair", ResultDigest: recognitionLayoutV2TestDigest(repairPayload), Payload: repairPayload}
	repair.question.AnswerEvidenceTranscriptions = []string{"14/35", "18/35"}
	if !usecase.EvaluateOCRConfirmationRisk(*repair.question).ConfirmationRequired {
		t.Fatal("fixture must be genuine evidence conflict")
	}
	for _, tc := range []struct {
		name, answer   string
		transportErr   error
		adopt          bool
		blankOwnership bool
	}{{"agrees with repair", "18/35", nil, true, false}, {"neither prior agrees", "19/35", nil, false, false}, {"unknown stops without settlement", "", errors.New("request outcome unknown"), false, false}, {"blank ownership uses question evidence", "", nil, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			first, last := primary, repair
			independent := payload(tc.answer, true)
			if tc.blankOwnership {
				original := strings.ReplaceAll(strings.Replace(payload("", false), `"answer_state":"present"`, `"answer_state":"blank"`, 1), "5/7-1/5=", "另外两个数的两倍")
				reread := strings.ReplaceAll(strings.Replace(payload("", false), `"answer_state":"present"`, `"answer_state":"unclear"`, 1), "5/7-1/5=", "另外两个数和的两倍")
				independent = strings.ReplaceAll(strings.Replace(payload("", true), `"answer_state":"present"`, `"answer_state":"blank"`, 1), "5/7-1/5=", "另外两个数和的两倍")
				_, first = classifyRecognitionLayoutRepairV2(original, target, plan.RecognitionFormat)
				_, last = classifyRecognitionLayoutRepairV2(reread, target, plan.RecognitionFormat)
				first.source = k12.RecognitionPhysicalCallResult{InvocationID: "primary", ResultDigest: recognitionLayoutV2TestDigest(original), Payload: original}
				last.source = k12.RecognitionPhysicalCallResult{InvocationID: "repair", ResultDigest: recognitionLayoutV2TestDigest(reread), Payload: reread}
				last.question.EvidenceTranscriptions = []string{"另外两个数的两倍", "另外两个数和的两倍"}
			}
			header := recognitionLayoutV2TestDigest("adjudication-runtime")
			runtime := recognitionLayoutV2RuntimeFixture(header, plan, 1, time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli())
			unit := k12.RecognitionPhysicalUnit("layout_adjudicate_0001")
			e := &sourceAdjudicationExecutor{recognitionLayoutRepairWaveExecutorV2: &recognitionLayoutRepairWaveExecutorV2{runtime: runtime, cached: map[k12.RecognitionPhysicalUnit]k12.RecognitionPhysicalCallResult{}, providerSends: map[k12.RecognitionPhysicalUnit]int{}, transportErrByUnit: map[k12.RecognitionPhysicalUnit]error{unit: tc.transportErr}}}
			ctx := k12.WithRecognitionPhysicalCallExecutor(k12.WithRecognitionLayoutPlanV2(context.Background(), header), e)
			adapter := NewRecognizerAdapter(func(_ context.Context, _ []byte, prompt string) (string, error) {
				if strings.Contains(prompt, "14/35") || strings.Contains(prompt, "18/35") {
					t.Fatal("prior answers leaked into independent prompt")
				}
				if !strings.Contains(prompt, `Return compact JSON only: {"verification":`) {
					t.Fatal("prompt has contradictory top-level contract")
				}
				return independent, nil
			})
			err := adapter.recognizeLayoutAdjudicationsV2(ctx, page, plan, runtime, map[string]recognitionLayoutBatchOutcomeV2{target.TargetID: *first}, map[string]recognitionLayoutBatchOutcomeV2{target.TargetID: *last})
			if tc.transportErr != nil {
				if err == nil || len(e.settlements) != 0 {
					t.Fatalf("unknown must stop without settlement: %v", err)
				}
				return
			}
			if err != nil || len(e.requests) != 1 || len(e.settlements) != 1 {
				t.Fatalf("request/settlement chain: %v", err)
			}
			s := e.settlements[0]
			if s.Adopted != tc.adopt {
				t.Fatalf("adopted=%v want=%v", s.Adopted, tc.adopt)
			}
			if s.SourcePhysicalUnit != unit || e.requests[0].PrimaryPhysicalInvocationID != "primary" || e.requests[0].RepairPhysicalInvocationID != "repair" {
				t.Fatal("source identity lost")
			}
			if tc.adopt && !tc.blankOwnership && (s.MatchedAnswerPrior != "repair" || s.MatchedQuestionPrior != "both" || len(s.ResultJSON) == 0) {
				t.Fatalf("matched source not recorded: %+v", s)
			}
			if tc.blankOwnership && (s.MatchedQuestionPrior != "repair" || s.MatchedAnswerPrior != "primary" || e.requests[0].ConflictKind != "question") {
				t.Fatalf("blank ownership evidence mismatch: %+v", s)
			}
			if !tc.adopt && (len(s.ResultJSON) != 0 || s.MatchedAnswerPrior != "") {
				t.Fatal("unadopted result must preserve original candidate")
			}
		})
	}
}

func TestRecognitionLayoutAdjudication_AdoptsTraditionalRatioSource(t *testing.T) {
	page, base := recognitionLayoutV2DispatchPlan(t, 1)
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{PagePNG: page,
		Manifest:          k12.RecognitionLayoutManifestSuccessV2{InvocationID: base.ManifestInvocationID, ResultDigest: base.ManifestResultDigest},
		Targets:           []k12.RecognitionLayoutManifestTargetV2{{ManifestRef: "manifest_0001", ManifestOrder: 1, Region: base.Targets[0].Region}},
		RecognitionFormat: k12.RecognitionLayoutCompactV4, InitialReadMode: k12.RecognitionLayoutManifestWithContentV1, EnableSourceAdjudication: true})
	if err != nil {
		t.Fatal(err)
	}
	target := plan.Targets[0]
	const primaryQuestion = `把 \(\frac{3}{4}:\frac{1}{2}\) 化成最简单的整数比。`
	const repairQuestion = `把 \(\frac{3}{4}\div\frac{1}{2}\) 化成最简单的整数比。`
	payload := func(question string, verification bool) string {
		v := map[string]any{"items": []any{map[string]any{"target_id": "t1", "kind": "question", "recognition": map[string]any{
			"question": question, "subject": "数学", "answer_state": "present", "student_answer": `\(3:2\)`,
			"recognition_confidence": .99, "ocr_signals": []string{}, "answer_bbox": map[string]int{"x": 20, "y": 20, "width": 12, "height": 12}}}}}
		if verification {
			v["verification"] = map[string]bool{"target_ownership_confirmed": true, "active_answer_complete": true}
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	for _, tt := range []struct {
		name, independentQuestion string
		adopt                     bool
	}{
		{"full traditional ratio agrees with primary only", `把 \(\frac{3}{4}:\frac{1}{2}\) 化成最簡單的整數比。`, true},
		{"same answer cannot hide a changed fraction", `把 \(\frac{5}{4}:\frac{1}{2}\) 化成最簡單的整數比。`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			firstRaw, repairRaw := payload(primaryQuestion, false), payload(repairQuestion, false)
			_, first := classifyRecognitionLayoutRepairV2(firstRaw, target, plan.RecognitionFormat)
			_, repair := classifyRecognitionLayoutRepairV2(repairRaw, target, plan.RecognitionFormat)
			if first == nil || repair == nil {
				t.Fatal("fixture did not parse")
			}
			first.source = k12.RecognitionPhysicalCallResult{InvocationID: "primary", ResultDigest: recognitionLayoutV2TestDigest(firstRaw), Payload: firstRaw}
			repair.source = k12.RecognitionPhysicalCallResult{InvocationID: "repair", ResultDigest: recognitionLayoutV2TestDigest(repairRaw), Payload: repairRaw}
			repair.question.EvidenceTranscriptions = []string{primaryQuestion, repairQuestion}
			header := recognitionLayoutV2TestDigest("ratio-adjudication-runtime")
			runtime := recognitionLayoutV2RuntimeFixture(header, plan, 1, time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli())
			e := &sourceAdjudicationExecutor{recognitionLayoutRepairWaveExecutorV2: &recognitionLayoutRepairWaveExecutorV2{runtime: runtime,
				cached: map[k12.RecognitionPhysicalUnit]k12.RecognitionPhysicalCallResult{}, providerSends: map[k12.RecognitionPhysicalUnit]int{}}}
			ctx := k12.WithRecognitionPhysicalCallExecutor(k12.WithRecognitionLayoutPlanV2(context.Background(), header), e)
			adapter := NewRecognizerAdapter(func(_ context.Context, _ []byte, _ string) (string, error) {
				return payload(tt.independentQuestion, true), nil
			})
			err := adapter.recognizeLayoutAdjudicationsV2(ctx, page, plan, runtime,
				map[string]recognitionLayoutBatchOutcomeV2{target.TargetID: *first}, map[string]recognitionLayoutBatchOutcomeV2{target.TargetID: *repair})
			if err != nil || len(e.requests) != 1 || len(e.settlements) != 1 {
				t.Fatalf("request/settlement chain: %v", err)
			}
			s := e.settlements[0]
			if s.Adopted != tt.adopt {
				t.Fatalf("adopted=%v want=%v", s.Adopted, tt.adopt)
			}
			if tt.adopt && (s.MatchedQuestionPrior != "primary" || s.MatchedAnswerPrior != "both" || len(s.ResultJSON) == 0) {
				t.Fatalf("ratio source priors not preserved: %+v", s)
			}
			if !tt.adopt && (s.MatchedQuestionPrior != "" || s.MatchedAnswerPrior != "" || len(s.ResultJSON) != 0) {
				t.Fatal("unmatched question was adopted through its identical final answer")
			}
			if first.source.Payload != firstRaw || repair.source.Payload != repairRaw || first.question.RawTranscription != primaryQuestion || repair.question.RawTranscription != repairQuestion {
				t.Fatal("adjudication changed an original source reading")
			}
		})
	}
}

// 从真实复读合并入口验证空观察不会使作答归属冲突消失。
func TestRecognitionLayoutAdjudication_AnswerStateConflictSurvivesRepair(t *testing.T) {
	page, base := recognitionLayoutV2DispatchPlan(t, 1)
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{PagePNG: page, Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: base.ManifestInvocationID, ResultDigest: base.ManifestResultDigest}, Targets: []k12.RecognitionLayoutManifestTargetV2{{ManifestRef: "manifest_0001", ManifestOrder: 1, Region: base.Targets[0].Region}}, RecognitionFormat: k12.RecognitionLayoutCompactV4, EnableSourceAdjudication: true})
	if err != nil {
		t.Fatal(err)
	}
	target := plan.Targets[0]
	payload := func(state, answer string, verification bool) string {
		var box any
		if state == "present" {
			box = map[string]int{"x": 20, "y": 20, "width": 12, "height": 12}
		}
		v := map[string]any{"items": []any{map[string]any{"target_id": "t1", "kind": "question", "recognition": map[string]any{"question": "2+3=", "subject": "数学", "answer_state": state, "student_answer": answer, "recognition_confidence": 0.99, "ocr_signals": []string{}, "answer_bbox": box}}}}
		if verification {
			v["verification"] = map[string]bool{"target_ownership_confirmed": true, "active_answer_complete": true}
		}
		raw, _ := json.Marshal(v)
		return string(raw)
	}
	for _, tc := range []struct {
		name, firstState, firstAnswer, nextState, nextAnswer string
		wantConflict, unknown                                bool
	}{
		{"unclear to present", "unclear", "", "present", "5", true, false},
		{"blank to present", "blank", "", "present", "5", true, false},
		{"present to blank", "present", "5", "blank", "", true, false},
		{"same present", "present", "5", "present", "5", false, false},
		{"same blank", "blank", "", "blank", "", false, false},
		{"unclear remains unclear", "unclear", "", "unclear", "", false, false},
		{"unknown stops", "unclear", "", "present", "5", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primaryRaw := payload(tc.firstState, tc.firstAnswer, false)
			_, first := classifyRecognitionLayoutRepairV2(primaryRaw, target, plan.RecognitionFormat)
			if first == nil || first.question == nil {
				t.Fatal("primary did not parse")
			}
			first.source = k12.RecognitionPhysicalCallResult{InvocationID: "primary", ResultDigest: recognitionLayoutV2TestDigest(primaryRaw), Payload: primaryRaw}
			header := recognitionLayoutV2TestDigest("state-conflict-runtime")
			runtime := recognitionLayoutV2RuntimeFixture(header, plan, 1, time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli())
			repairUnit, adjudicateUnit := k12.RecognitionPhysicalUnit("layout_repair_0001"), k12.RecognitionPhysicalUnit("layout_adjudicate_0001")
			auth := k12.RecognitionLayoutRepairAuthorizationV2{AuthorizationID: "repair-auth", AuthorizationDigest: recognitionLayoutV2TestDigest("auth"), CandidateID: target.TargetID, PhysicalUnit: repairUnit, RepairRound: 1}
			e := &sourceAdjudicationExecutor{recognitionLayoutRepairWaveExecutorV2: &recognitionLayoutRepairWaveExecutorV2{runtime: runtime, cached: map[k12.RecognitionPhysicalUnit]k12.RecognitionPhysicalCallResult{}, providerSends: map[k12.RecognitionPhysicalUnit]int{}, transportErrByUnit: map[k12.RecognitionPhysicalUnit]error{}, authorizationByID: map[string]k12.RecognitionLayoutRepairAuthorizationV2{auth.AuthorizationID: auth}, settledRepairByAuth: map[string]k12.RecognitionLayoutRepairSettlementResultV2{}}}
			if tc.unknown {
				e.transportErrByUnit[adjudicateUnit] = errors.New("request outcome unknown")
			}
			ctx := k12.WithRecognitionPhysicalCallExecutor(k12.WithRecognitionLayoutPlanV2(t.Context(), header), e)
			adapter := NewRecognizerAdapter(func(_ context.Context, _ []byte, prompt string) (string, error) {
				return payload(tc.nextState, tc.nextAnswer, strings.Contains(prompt, `"verification":`)), nil
			})
			repaired := adapter.recognizeLayoutRepairV2(ctx, page, plan, runtime, auth, 0, first.question)
			if repaired.err != nil || repaired.outcome == nil {
				t.Fatalf("repair: %v", repaired.err)
			}
			risk := usecase.EvaluateOCRConfirmationRisk(*repaired.outcome.question)
			conflict := false
			for _, reason := range risk.ConfirmationReasons {
				conflict = conflict || reason == usecase.OCRRiskEvidenceConflict
			}
			if conflict != tc.wantConflict {
				t.Fatalf("conflict=%v want=%v signals=%v", conflict, tc.wantConflict, risk.OCRSignals)
			}
			err := adapter.recognizeLayoutAdjudicationsV2(ctx, page, plan, runtime, map[string]recognitionLayoutBatchOutcomeV2{target.TargetID: *first}, map[string]recognitionLayoutBatchOutcomeV2{target.TargetID: *repaired.outcome})
			if tc.unknown {
				if err == nil || len(e.settlements) != 0 {
					t.Fatalf("unknown settled: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.wantConflict {
				want = 1
			}
			if len(e.requests) != want || len(e.settlements) != want || e.providerSends[adjudicateUnit] != want {
				t.Fatalf("requests=%d settlements=%d calls=%d want=%d", len(e.requests), len(e.settlements), e.providerSends[adjudicateUnit], want)
			}
			if want == 1 && e.requests[0].ConflictKind != "answer_ownership" {
				t.Fatalf("kind=%s", e.requests[0].ConflictKind)
			}
		})
	}
}

func TestCompletedSourceReview_OriginalTargetAndFullReceipt(t *testing.T) {
	region := k12.SourcePixelRegion{X: 640, Y: 1248, Width: 574, Height: 244}
	q := usecase.RecognizedQuestion{Question: "5+6=", RawTranscription: "5+6=", CanonicalMarkdown: "5+6=", StudentAnswer: "neighbor answer 29", AnswerState: usecase.AnswerStatePresent, SourceWidth: 1280, SourceHeight: 1707, SourceRegion: &region, SourceSectionLabel: "五、思维题"}
	raw := `{"verification":{"target_ownership_confirmed":true,"active_answer_complete":true},"items":[{"target_id":"t1","kind":"question","recognition":{"question":"5+6=","subject":"数学","answer_state":"blank","student_answer":"","recognition_confidence":0.99,"ocr_signals":[],"answer_bbox":null}}]}`
	calls := 0
	adapter := NewRecognizerAdapter(func(_ context.Context, image []byte, prompt string) (string, error) {
		calls++
		if string(image) != "original pixels" || !strings.Contains(prompt, `"target_region":{"x":640,"y":1248,"width":574,"height":244}`) || strings.Contains(prompt, q.StudentAnswer) {
			t.Fatal("source or independent target contract changed")
		}
		return raw, nil
	})
	result, err := adapter.ReviewCompletedSource(context.Background(), []byte("original pixels"), q)
	if err != nil || !result.Verified || result.Question.AnswerState != usecase.AnswerStateBlank || result.Raw != raw || calls != 1 {
		t.Fatalf("review=%+v err=%v calls=%d", result, err, calls)
	}
}
