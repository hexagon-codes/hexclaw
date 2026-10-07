package engineadapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// 模拟同一印刷题被拆成两个目标，独立裁决均返回完整的两问三行作答。
func TestRecognitionLayoutFinalizationProjectsCompletePrintedQuestion(t *testing.T) {
	plan, final := completePrintedQuestionFixture(t)
	before, _ := json.Marshal(final)
	questions, err := RecognizedQuestionsFromLayoutFinalizationV2(final, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 2 || !reflect.DeepEqual(questions[0].SourceNumberPath, []string{"15"}) ||
		!reflect.DeepEqual(questions[1].SourceNumberPath, []string{"16"}) ||
		questions[1].AnswerState != usecase.AnswerStateBlank || questions[1].StudentAnswer != "" {
		t.Fatalf("printed questions or blank answer changed: %#v", questions)
	}
	if questions[0].RawTranscription != simulatedOuterCompleteQuestion || questions[0].AnswerRawTranscription != simulatedOuterCompleteAnswer {
		t.Fatal("complete question or three answer lines were not preserved verbatim")
	}
	after, _ := json.Marshal(final)
	if !bytes.Equal(before, after) || len(final.CandidateResults) != 3 || len(plan.Targets) != 3 {
		t.Fatal("immutable finalization or plan was modified")
	}
	encoded, _ := json.Marshal(questions[0])
	var projection struct {
		Source struct {
			PageDigest         string    `json:"page_digest"`
			PlanDigest         string    `json:"plan_digest"`
			FinalizationDigest string    `json:"finalization_digest"`
			TargetIDs          [2]string `json:"target_ids"`
		} `json:"layout_source_projection"`
	}
	if err := json.Unmarshal(encoded, &projection); err != nil {
		t.Fatal(err)
	}
	if projection.Source.PageDigest != plan.PageDigest || projection.Source.PlanDigest != final.PlanDigest ||
		projection.Source.FinalizationDigest != final.FinalizationDigest ||
		projection.Source.TargetIDs != [2]string{plan.Targets[0].TargetID, plan.Targets[1].TargetID} {
		t.Fatal("derived result lost either immutable target lineage")
	}
	var roundTrip usecase.RecognizedQuestion
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.LayoutSourceProjection != questions[0].LayoutSourceProjection ||
		roundTrip.RawTranscription != questions[0].RawTranscription || roundTrip.AnswerRawTranscription != questions[0].AnswerRawTranscription {
		t.Fatal("source projection did not survive result JSON")
	}
	if _, err := usecase.NormalizeRecognizedProblemsForInitialReadMode("simulated-submission", questions, plan.InitialReadMode); err != nil {
		t.Fatal(err)
	}
}

func TestRecognitionLayoutFinalizationDoesNotMergeUnprovenPrintedTargets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*k12.RecognitionLayoutPlanV2, *k12.RecognitionLayoutPlanFinalizationResultV2)
	}{
		{"different page", func(p *k12.RecognitionLayoutPlanV2, _ *k12.RecognitionLayoutPlanFinalizationResultV2) {
			p.PageDigest = recognitionLayoutV2TestDigest("other-page")
		}},
		{"different section", func(p *k12.RecognitionLayoutPlanV2, _ *k12.RecognitionLayoutPlanFinalizationResultV2) {
			p.Targets[1].SourceSectionPath = []string{"五"}
		}},
		{"different section label", func(p *k12.RecognitionLayoutPlanV2, _ *k12.RecognitionLayoutPlanFinalizationResultV2) {
			p.Targets[1].SourceSectionLabel = "四、其他题"
		}},
		{"different number", func(p *k12.RecognitionLayoutPlanV2, _ *k12.RecognitionLayoutPlanFinalizationResultV2) {
			p.Targets[1].SourceNumberPath = []string{"14"}
		}},
		{"different label", func(p *k12.RecognitionLayoutPlanV2, _ *k12.RecognitionLayoutPlanFinalizationResultV2) {
			p.Targets[1].DisplayLabel = "15)"
		}},
		{"different original origin", func(p *k12.RecognitionLayoutPlanV2, _ *k12.RecognitionLayoutPlanFinalizationResultV2) {
			p.Targets[1].OriginalRegion.X++
		}},
		{"missing original region", func(p *k12.RecognitionLayoutPlanV2, _ *k12.RecognitionLayoutPlanFinalizationResultV2) {
			p.Targets[1].OriginalRegion = nil
		}},
		{"crossing original regions", func(p *k12.RecognitionLayoutPlanV2, _ *k12.RecognitionLayoutPlanFinalizationResultV2) {
			p.Targets[1].OriginalRegion.Width--
		}},
		{"missing adjudication", func(_ *k12.RecognitionLayoutPlanV2, f *k12.RecognitionLayoutPlanFinalizationResultV2) {
			f.CandidateResults[1].Adjudication = nil
		}},
		{"failed adjudication", func(_ *k12.RecognitionLayoutPlanV2, f *k12.RecognitionLayoutPlanFinalizationResultV2) {
			f.CandidateResults[1].Adjudication.MatchedAnswerPrior = ""
		}},
		{"missing adjudication source", func(_ *k12.RecognitionLayoutPlanV2, f *k12.RecognitionLayoutPlanFinalizationResultV2) {
			f.CandidateResults[1].OriginalCandidateJSON = nil
		}},
		{"partial answer", func(_ *k12.RecognitionLayoutPlanV2, f *k12.RecognitionLayoutPlanFinalizationResultV2) {
			changeProjectedReading(t, f, "student_answer", "女生：25（人）")
		}},
		{"partial question", func(_ *k12.RecognitionLayoutPlanV2, f *k12.RecognitionLayoutPlanFinalizationResultV2) {
			changeProjectedReading(t, f, "question", "女生各有多少人？")
		}},
		{"conflicting answer", func(_ *k12.RecognitionLayoutPlanV2, f *k12.RecognitionLayoutPlanFinalizationResultV2) {
			changeProjectedReading(t, f, "student_answer", "45÷(4+5)=5\n男生：5×4=21（人）\n女生：5×5=25（人）")
		}},
		{"conflicting question", func(_ *k12.RecognitionLayoutPlanV2, f *k12.RecognitionLayoutPlanFinalizationResultV2) {
			changeProjectedReading(t, f, "question", "六（2）班有44人，男生与女生人数的比是4：5。男生、女生各有多少人？")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, final := completePrintedQuestionFixture(t)
			test.mutate(&plan, &final)
			if test.name != "different page" {
				leftTarget, rightTarget := plan.Targets[0], plan.Targets[1]
				leftTarget.Region, rightTarget.Region = *leftTarget.AdjudicationRegion, *rightTarget.AdjudicationRegion
				left, leftErr := parseRecognitionLayoutQuestionV2(final.CandidateResults[0].ResultJSON, leftTarget, plan.RecognitionFormat)
				right, rightErr := parseRecognitionLayoutQuestionV2(final.CandidateResults[1].ResultJSON, rightTarget, plan.RecognitionFormat)
				if leftErr == nil && rightErr == nil && sameCompletePrintedQuestionV2(left, right, plan.Targets[0], plan.Targets[1], final.CandidateResults[0], final.CandidateResults[1]) {
					t.Fatal("unproven source pair qualified for projection")
				}
			}
			// 计划漂移仍由既有身份校验拒绝；候选内容变化重算集合以直接验证投影边界。
			final.CandidateResultsExactSetDigest, _ = k12.RecognitionLayoutCandidateResultsExactSetDigestV2(final.CandidateResults)
			questions, err := RecognizedQuestionsFromLayoutFinalizationV2(final, plan)
			if err == nil || len(questions) != 0 {
				t.Fatalf("unproven targets accepted: %#v", questions)
			}
		})
	}
}

const simulatedCompleteQuestion = "六（2）班有45人，男生与女生人数的比是4：5。男生、女生各有多少人？"
const simulatedCompleteAnswer = "\\(45\\div(4+5)=5\\)\n男生：\\(5\\times4=20\\)（人）\n女生：\\(5\\times5=25\\)（人）"
const simulatedOuterCompleteQuestion = "六（2）班有45人，男生与女生人数的比是4：5，男生、女生各有多少人？"
const simulatedOuterCompleteAnswer = "\\(45÷(4+5)=5\\)\n男生：\\(5×4=20\\)（人）\n女生：\\(5×5=25\\)（人）"

func completePrintedQuestionFixture(t *testing.T) (k12.RecognitionLayoutPlanV2, k12.RecognitionLayoutPlanFinalizationResultV2) {
	t.Helper()
	page := image.NewRGBA(image.Rect(0, 0, 600, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 600; x++ {
			page.Set(x, y, color.White)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, page); err != nil {
		t.Fatal(err)
	}
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{
		InitialReadMode: k12.RecognitionLayoutManifestWithContentV1, PagePNG: encoded.Bytes(),
		Manifest:          k12.RecognitionLayoutManifestSuccessV2{InvocationID: "simulated-manifest", ResultDigest: recognitionLayoutV2TestDigest("manifest")},
		RecognitionFormat: k12.RecognitionLayoutCompactV4, EnableSourceAdjudication: true,
		Targets: []k12.RecognitionLayoutManifestTargetV2{
			{ManifestRef: "manifest_0001", ManifestOrder: 1, SourceNumberPath: []string{"15"}, DisplayLabel: "15.", SourceSectionPath: []string{"四"}, SourceSectionLabel: "四、应用题", Region: k12.SourcePixelRegion{X: 20, Y: 20, Width: 220, Height: 100}},
			{ManifestRef: "manifest_0002", ManifestOrder: 2, SourceNumberPath: []string{"15"}, DisplayLabel: "15.", SourceSectionPath: []string{"四"}, SourceSectionLabel: "四、应用题", Region: k12.SourcePixelRegion{X: 20, Y: 20, Width: 220, Height: 140}},
			{ManifestRef: "manifest_0003", ManifestOrder: 3, SourceNumberPath: []string{"16"}, DisplayLabel: "16.", SourceSectionPath: []string{"四"}, SourceSectionLabel: "四、应用题", Region: k12.SourcePixelRegion{X: 300, Y: 20, Width: 220, Height: 140}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	final := k12.RecognitionLayoutPlanFinalizationResultV2{PlanID: "simulated-plan", PlanDigest: plan.AuthorizedPlanDigest, CandidateResultCount: len(plan.Targets)}
	final.PhysicalResults = append(final.PhysicalResults, k12.RecognitionLayoutPhysicalResultEvidenceV2{PhysicalInvocationID: plan.ManifestInvocationID, PhysicalUnit: k12.RecognitionPhysicalUnitWholePage, ResultDigest: plan.ManifestResultDigest, PlanDigest: recognitionLayoutV2TestDigest("header"), Attempt: 1})
	ids := make([]string, len(plan.Targets))
	for i, target := range plan.Targets {
		ids[i] = target.TargetID
		body := map[string]any{"question": simulatedCompleteQuestion, "subject": "数学", "answer_state": "present", "student_answer": simulatedCompleteAnswer, "recognition_confidence": 0.99, "ocr_signals": []string{}, "answer_bbox": map[string]int{"x": 60, "y": 60, "width": 100, "height": 60}}
		unit, _ := k12.RecognitionLayoutAdjudicationUnitV2(i + 1)
		sourceID, sourceDigest := string(unit), recognitionLayoutV2TestDigest(string(unit))
		candidate := k12.RecognitionLayoutCandidateFinalResultV2{CandidateID: target.TargetID, ResultKind: k12.RecognitionLayoutCandidateQuestionV2, ResultDigest: recognitionLayoutV2TestDigest("candidate" + string(unit)), SourcePhysicalInvocationID: sourceID, SourcePhysicalUnit: unit, SourcePhysicalResultDigest: sourceDigest, OriginalCandidateJSON: json.RawMessage(`{"original":true}`), Adjudication: &k12.RecognitionLayoutAdjudicationReceiptV2{AuthorizationID: "simulated-" + string(unit), SettlementDigest: recognitionLayoutV2TestDigest("settlement" + string(unit)), MatchedQuestionPrior: "repair", MatchedAnswerPrior: "repair"}}
		if i == 1 {
			body["question"] = simulatedOuterCompleteQuestion
			body["student_answer"] = simulatedOuterCompleteAnswer
		}
		if i == 2 {
			body["question"] = "另一道未作答应用题。"
			body["answer_state"] = "blank"
			body["student_answer"] = ""
			body["answer_bbox"] = nil
			candidate.SourcePhysicalInvocationID = plan.ManifestInvocationID
			candidate.SourcePhysicalUnit = k12.RecognitionPhysicalUnitWholePage
			candidate.SourcePhysicalResultDigest = plan.ManifestResultDigest
			candidate.OriginalCandidateJSON = nil
			candidate.Adjudication = nil
		} else {
			exact, _ := k12.RecognitionLayoutTargetExactSetDigestV2([]string{target.TargetID})
			final.PhysicalResults = append(final.PhysicalResults, k12.RecognitionLayoutPhysicalResultEvidenceV2{PhysicalInvocationID: sourceID, PhysicalUnit: unit, ResultDigest: sourceDigest, PlanDigest: plan.AuthorizedPlanDigest, CandidateExactSetDigest: exact, Attempt: 1})
		}
		candidate.ResultJSON, _ = json.Marshal(body)
		final.CandidateResults = append(final.CandidateResults, candidate)
	}
	final.CandidateExactSetDigest, _ = k12.RecognitionLayoutTargetExactSetDigestV2(ids)
	final.CandidateResultsExactSetDigest, _ = k12.RecognitionLayoutCandidateResultsExactSetDigestV2(final.CandidateResults)
	final.PhysicalResultCount = len(final.PhysicalResults)
	final.PhysicalResultsExactSetDigest, _ = k12.RecognitionLayoutPhysicalResultsExactSetDigestV2(final.PhysicalResults)
	_, final.FinalizationDigest, err = k12.CanonicalRecognitionLayoutPlanFinalizationV2("simulated-parent", final)
	if err != nil {
		t.Fatal(err)
	}
	return plan, final
}

func changeProjectedReading(t *testing.T, final *k12.RecognitionLayoutPlanFinalizationResultV2, key string, value string) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(final.CandidateResults[1].ResultJSON, &body); err != nil {
		t.Fatal(err)
	}
	body[key] = value
	final.CandidateResults[1].ResultJSON, _ = json.Marshal(body)
}

// 全局重复题号保护不因派生投影而放宽。
func TestRecognitionLayoutProjectionKeepsDuplicateValidator(t *testing.T) {
	plan, final := completePrintedQuestionFixture(t)
	final.CandidateResults[1].Adjudication.MatchedQuestionPrior = ""
	final.CandidateResultsExactSetDigest, _ = k12.RecognitionLayoutCandidateResultsExactSetDigestV2(final.CandidateResults)
	_, err := RecognizedQuestionsFromLayoutFinalizationV2(final, plan)
	if !errors.Is(err, k12.ErrRecognitionProtocolInvalid) {
		t.Fatalf("expected existing duplicate rejection, got %v", err)
	}
}
