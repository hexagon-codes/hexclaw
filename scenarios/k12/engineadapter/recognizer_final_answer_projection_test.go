package engineadapter

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestFinalAnswerProjectionBindsAdjudicationAndReusesInkGeometry(t *testing.T) {
	page := anchorTestImage(t)
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{
		PagePNG:                  page,
		Manifest:                 k12.RecognitionLayoutManifestSuccessV2{InvocationID: "modelphysical-11111111111111111111111111111111", ResultDigest: recognitionLayoutV2TestDigest("final-manifest")},
		Targets:                  []k12.RecognitionLayoutManifestTargetV2{{ManifestRef: "manifest_0001", ManifestOrder: 1, SourceNumberPath: []string{"3"}, DisplayLabel: "3.", Region: k12.SourcePixelRegion{X: 80, Y: 20, Width: 160, Height: 70}}},
		EnableSourceAdjudication: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := plan.Targets[0]
	ids, err := k12.RecognitionLayoutTargetExactSetDigestV2([]string{target.TargetID})
	if err != nil {
		t.Fatal(err)
	}
	answerBox := k12.SourcePixelRegion{X: 100, Y: 40, Width: 100, Height: 8}
	result, _ := json.Marshal(map[string]any{
		"problem_kind": "standalone", "parent_problem_id": "", "subproblem_no": "", "source_number_path": target.SourceNumberPath, "display_label": target.DisplayLabel,
		"question": "7/9×3/14=", "subject": "数学", "answer_state": "present", "student_answer": "2/4=1/3", "recognition_confidence": 0.99,
		"ocr_signals": []string{"evidence_conflict"}, "evidence_transcriptions": []string{"7/9×3/14=", "7/9×3/14="}, "answer_evidence_transcriptions": []string{"1/3", "2/4=1/3"},
	})
	adjudicationUnit, _ := k12.RecognitionLayoutAdjudicationUnitV2(1)
	receipt := k12.RecognitionFinalAnswerReceiptV1{
		Evidence:   k12.RecognitionFinalAnswerEvidenceV1{RawTranscription: "1/3", BBox: answerBox, IsFinalAnswer: true, TargetOwnershipConfirmed: true, NotCancelled: true, CompleteAndLegible: true},
		PlanDigest: plan.AuthorizedPlanDigest, CandidateID: target.TargetID,
		SourcePhysicalInvocationID: "modelphysical-33333333333333333333333333333333", SourcePhysicalResultDigest: recognitionLayoutV2TestDigest("final-adjudication"),
		AuthorizationID: "adjudication-1", SettlementDigest: recognitionLayoutV2TestDigest("final-settlement"),
	}
	final := k12.RecognitionLayoutPlanFinalizationResultV2{
		PlanID: "layout-final", PlanDigest: plan.AuthorizedPlanDigest, CandidateExactSetDigest: ids, CandidateResultCount: 1, PhysicalResultCount: 3,
		CandidateResults: []k12.RecognitionLayoutCandidateFinalResultV2{{CandidateID: target.TargetID, ResultKind: k12.RecognitionLayoutCandidateQuestionV2, ResultDigest: recognitionLayoutV2TestDigest("final-result"), ResultJSON: result, SourcePhysicalInvocationID: "modelphysical-22222222222222222222222222222222", SourcePhysicalUnit: plan.Batches[0].Unit, SourcePhysicalResultDigest: recognitionLayoutV2TestDigest("final-review"), FinalAnswerReceipt: &receipt}},
		PhysicalResults: []k12.RecognitionLayoutPhysicalResultEvidenceV2{
			{PhysicalInvocationID: "modelphysical-11111111111111111111111111111111", PhysicalUnit: k12.RecognitionPhysicalUnitWholePage, ResultDigest: recognitionLayoutV2TestDigest("final-manifest"), PlanDigest: recognitionLayoutV2TestDigest("header"), Attempt: 1},
			{PhysicalInvocationID: "modelphysical-22222222222222222222222222222222", PhysicalUnit: plan.Batches[0].Unit, PlanDigest: plan.AuthorizedPlanDigest, ResultDigest: recognitionLayoutV2TestDigest("final-review"), CandidateExactSetDigest: ids, Attempt: 1},
			{PhysicalInvocationID: receipt.SourcePhysicalInvocationID, PhysicalUnit: adjudicationUnit, PlanDigest: plan.AuthorizedPlanDigest, ResultDigest: receipt.SourcePhysicalResultDigest, CandidateExactSetDigest: ids, Attempt: 1},
		},
	}
	freeze := func(value *k12.RecognitionLayoutPlanFinalizationResultV2) {
		t.Helper()
		var err error
		value.CandidateResultsExactSetDigest, err = k12.RecognitionLayoutCandidateResultsExactSetDigestV2(value.CandidateResults)
		if err != nil {
			t.Fatal(err)
		}
		value.PhysicalResultsExactSetDigest, err = k12.RecognitionLayoutPhysicalResultsExactSetDigestV2(value.PhysicalResults)
		if err != nil {
			t.Fatal(err)
		}
	}
	freeze(&final)
	questions, err := RecognizedQuestionsFromLayoutFinalizationV2(final, plan)
	if err != nil || len(questions) != 1 || questions[0].FinalAnswerReceipt == nil || !reflect.DeepEqual(*questions[0].FinalAnswerReceipt, receipt) || !reflect.DeepEqual(questions[0].ObservedAnswerRegion, &answerBox) {
		t.Fatalf("projection lost independent evidence: %+v err=%v", questions, err)
	}
	if !questions[0].ConfirmationRequired || questions[0].StudentAnswer != "2/4=1/3" {
		t.Fatal("final evidence replaced the uncertain complete working")
	}
	questions[0].SourceWidth, questions[0].SourceHeight = 1000, 1600
	adapter := NewRecognizerAdapter(func(context.Context, []byte, string) (string, error) {
		t.Fatal("final-answer geometry must not invoke a model")
		return "", nil
	})
	anchored, ok := adapter.ReuseRecognitionAnswerGeometry(t.Context(), page, questions)
	wantBox := usecase.BBox{X: 0.1, Y: 0.025, W: 0.1, H: 0.005}
	if !ok || anchored[0].BBox == nil || *anchored[0].BBox != wantBox || !anchored[0].ConfirmationRequired || anchored[0].FinalAnswerReceipt == nil {
		t.Fatalf("validated final ink did not pass the existing geometry gate: %+v ok=%v", anchored, ok)
	}
	for _, tc := range []struct {
		name   string
		change func(*k12.RecognitionFinalAnswerReceiptV1)
	}{
		{"wrong source", func(r *k12.RecognitionFinalAnswerReceiptV1) { r.SourcePhysicalInvocationID = "unknown" }},
		{"wrong digest", func(r *k12.RecognitionFinalAnswerReceiptV1) {
			r.SourcePhysicalResultDigest = recognitionLayoutV2TestDigest("other")
		}},
		{"wrong target", func(r *k12.RecognitionFinalAnswerReceiptV1) { r.CandidateID = "other" }},
		{"outside crop", func(r *k12.RecognitionFinalAnswerReceiptV1) { r.Evidence.BBox.Y = 1500 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := final
			value.CandidateResults = append([]k12.RecognitionLayoutCandidateFinalResultV2(nil), final.CandidateResults...)
			copy := receipt
			tc.change(&copy)
			value.CandidateResults[0].FinalAnswerReceipt = &copy
			freeze(&value)
			if _, err := RecognizedQuestionsFromLayoutFinalizationV2(value, plan); !errors.Is(err, k12.ErrRecognitionLayoutPlanV2Unauthorized) {
				t.Fatalf("detached evidence accepted: %v", err)
			}
		})
	}
}
