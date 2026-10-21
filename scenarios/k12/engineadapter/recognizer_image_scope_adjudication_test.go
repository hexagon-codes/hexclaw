package engineadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestImageScopeAdjudicationUsesExistingOriginalPixelProof(t *testing.T) {
	for _, scenario := range []string{"scoped solve", "upright solve", "trailing writing area", "legacy", "old scoped task", "different printed words", "incomplete question", "equivalent readings", "identical unclear observations", "state-only uncertainty"} {
		t.Run(scenario, func(t *testing.T) {
			page, base := recognitionLayoutV2DispatchPlan(t, 1)
			input := k12.RecognitionLayoutPlanInputV2{PagePNG: page, Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: base.ManifestInvocationID, ResultDigest: base.ManifestResultDigest}, Targets: []k12.RecognitionLayoutManifestTargetV2{{ManifestRef: "manifest_0001", ManifestOrder: 1, Region: base.Targets[0].Region}}, RecognitionFormat: k12.RecognitionLayoutCompactV4, EnableSourceAdjudication: true, InitialReadMode: k12.RecognitionLayoutManifestWithContentV1}
			if scenario != "legacy" {
				input.ScopeInput = &k12.RecognitionImageScopeInputV1{Version: k12.RecognitionImageScopeVersionV1, Action: "solve"}
				if scenario != "old scoped task" {
					input.ScopeInput.SourceComparisonPolicy = k12.RecognitionImageSourceComparisonPolicyV1
				}
				input.Scope = &k12.RecognitionImageScopeV1{Version: k12.RecognitionImageScopeVersionV1, Source: "explicit", SelectedOrders: []int{1}, Basis: "current question"}
			}
			if scenario == "upright solve" {
				input.ScopeInput.SourceReviewPolicy = k12.RecognitionImageSourceUprightReviewPolicyV1
				input.PageOrientation = &k12.RecognitionPageOrientationV1{Version: "page_orientation_v1", PageDigest: base.PageDigest, ClockwiseDegrees: 90, Confidence: .99}
			}
			plan, err := k12.BuildRecognitionLayoutPlanV2(input)
			if err != nil {
				t.Fatal(err)
			}
			target := plan.Targets[0]
			payload := func(question, answer string, verified bool) string {
				data := map[string]any{"items": []any{map[string]any{"target_id": "t1", "kind": "question", "recognition": map[string]any{"question": question, "subject": "语文", "answer_state": "present", "student_answer": answer, "recognition_confidence": 0.99, "ocr_signals": []string{}, "answer_bbox": map[string]int{"x": 20, "y": 20, "width": 12, "height": 12}}}}}
				if verified {
					data["verification"] = map[string]bool{"target_ownership_confirmed": true, "active_answer_complete": true, "question_complete_and_legible": scenario != "incomplete question"}
					data["final_answer"] = nil
				}
				raw, _ := json.Marshal(data)
				return string(raw)
			}
			firstText := "弯弯的内金水河像一条玉带。这句话运用了________的修辞手法。"
			secondText := "弯弯的金水河像一条玉带。这句话运用了______的修辞手法。"
			thirdText := "弯弯的内金水河像一条玉带。这句话运用了______的修辞手法。"
			if scenario == "trailing writing area" {
				firstText += "仿写一句话：\n________________"
				secondText += "仿写一句话：________"
				thirdText += "仿写一句话："
			}
			if scenario == "different printed words" {
				thirdText = strings.ReplaceAll(thirdText, "玉带", "长绳")
			}
			firstRaw, secondRaw := payload(firstText, "比喻 协调", false), payload(secondText, "比喻 金水河", false)
			noTextConflict := scenario == "equivalent readings" || scenario == "identical unclear observations" || scenario == "state-only uncertainty"
			if noTextConflict {
				secondText = strings.Replace(firstText, "________", "______", 1)
				secondRaw = payload(secondText, "比喻 协调", false)
				if scenario != "equivalent readings" {
					secondRaw = strings.Replace(secondRaw, `"answer_state":"present"`, `"answer_state":"unclear"`, 1)
				}
				if scenario == "identical unclear observations" {
					firstRaw = strings.Replace(firstRaw, `"answer_state":"present"`, `"answer_state":"unclear"`, 1)
				}
			}
			_, first := classifyRecognitionLayoutRepairV2(firstRaw, target, plan.RecognitionFormat)
			_, second := classifyRecognitionLayoutRepairV2(secondRaw, target, plan.RecognitionFormat)
			if first == nil || second == nil {
				t.Fatal("source fixture invalid")
			}
			first.source = k12.RecognitionPhysicalCallResult{InvocationID: "first", ResultDigest: recognitionLayoutV2TestDigest(firstRaw), Payload: firstRaw}
			second.source = k12.RecognitionPhysicalCallResult{InvocationID: "second", ResultDigest: recognitionLayoutV2TestDigest(secondRaw), Payload: secondRaw}
			second.question.EvidenceTranscriptions = []string{firstText, secondText}
			header := recognitionLayoutV2TestDigest("scoped-source-header")
			runtime := recognitionLayoutV2RuntimeFixture(header, plan, 1, time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli())
			runtime.Header.ExecutionPolicy = k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV2
			executor := &sourceAdjudicationExecutor{recognitionLayoutRepairWaveExecutorV2: &recognitionLayoutRepairWaveExecutorV2{runtime: runtime, cached: map[k12.RecognitionPhysicalUnit]k12.RecognitionPhysicalCallResult{}, providerSends: map[k12.RecognitionPhysicalUnit]int{}, transportErrByUnit: map[k12.RecognitionPhysicalUnit]error{}}}
			ctx := k12.WithRecognitionPhysicalCallExecutor(k12.WithRecognitionLayoutPlanV2(t.Context(), header), executor)
			adapter := NewRecognizerAdapter(func(_ context.Context, image []byte, prompt string) (string, error) {
				if noTextConflict {
					t.Fatal("equivalent readings created an unnecessary third call")
				}
				if strings.Contains(prompt, "协调") {
					t.Fatal("prior reading leaked into original pixel verification")
				}
				if scenario == "upright solve" {
					view, err := png.Decode(bytes.NewReader(image))
					if err != nil || view.Bounds().Dx() != target.AdjudicationRegion.Height || view.Bounds().Dy() != target.AdjudicationRegion.Width {
						t.Fatalf("upright provider image differs: %v", err)
					}
					if !strings.Contains(prompt, "character by character") || strings.Contains(prompt, "内金水河") || strings.Contains(prompt, "金水河") {
						t.Fatal("verbatim contract missing or expected transcription leaked")
					}
				}
				return payload(thirdText, "比喻 金水河", true), nil
			})
			if err := adapter.recognizeLayoutAdjudicationV2(ctx, page, plan, runtime, target, *first, *second); err != nil {
				t.Fatal(err)
			}
			if noTextConflict {
				if len(executor.settlements) != 0 || len(executor.providerSends) != 0 {
					t.Fatal("equivalent readings requested adjudication")
				}
				return
			}
			want := scenario == "scoped solve" || scenario == "upright solve" || scenario == "trailing writing area"
			if len(executor.settlements) != 1 || executor.settlements[0].Adopted != want {
				t.Fatalf("settlement=%+v want_adopt=%v", executor.settlements, want)
			}
			if want && (executor.settlements[0].MatchedQuestionPrior != "primary" || executor.settlements[0].MatchedAnswerPrior != "repair") {
				t.Fatal("independent source ownership was lost")
			}
			if first.question.RawTranscription != firstText || second.question.RawTranscription != secondText {
				t.Fatal("original readings were rewritten")
			}
		})
	}
}
