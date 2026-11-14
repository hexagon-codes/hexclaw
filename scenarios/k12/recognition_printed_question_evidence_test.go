package k12

import (
	"strings"
	"testing"
)

func TestPrintedQuestionEvidenceForAutomaticGradingReference(t *testing.T) {
	primary := "内金水河像一条玉带。仿写一句：________"
	repair := "内金水河像一条玉带。仿写一句话：____"
	raw := `{"verification":{"question_complete_and_legible":true,"target_ownership_confirmed":true,"active_answer_complete":false},"final_answer":null,"items":[{"target_id":"t1","kind":"question","recognition":{"question":"内金水河像一条玉带。仿写一句话：________","subject":"语文","answer_state":"unclear","student_answer":"","recognition_confidence":0.99,"ocr_signals":["第二处手写字形不清，无法可靠转写。"],"answer_bbox":null}}]}`
	region := SourcePixelRegion{Width: 788, Height: 198}
	for _, action := range []string{"", "solve"} {
		t.Run("action="+action, func(t *testing.T) {
			plan := RecognitionLayoutPlanV2{InitialReadMode: RecognitionLayoutManifestWithContentV1, SourceAdjudication: true, ScopeInput: &RecognitionImageScopeInputV1{Version: RecognitionImageScopeVersionV1, Action: action, SourceReviewPolicy: RecognitionImageSourceUprightReviewPolicyV3, SourceComparisonPolicy: RecognitionImageSourceComparisonPolicyV1}}
			target := RecognitionLayoutTargetV2{AdjudicationRegion: &region}
			proof, err := RecognitionPrintedQuestionEvidenceFromResponseV1(raw, plan, target, []string{primary, repair})
			if err != nil || proof == nil || proof.MatchedQuestionPrior != "repair" || len(proof.OCRSignals) != 1 {
				t.Fatalf("independent printed proof missing: %#v, %v", proof, err)
			}
			for _, invalid := range []string{
				strings.Replace(raw, `"question_complete_and_legible":true`, `"question_complete_and_legible":false`, 1),
				strings.Replace(raw, "内金水河像一条玉带。仿写一句话", "内金水河不像一条玉带。仿写一句话", 1),
			} {
				got, err := RecognitionPrintedQuestionEvidenceFromResponseV1(invalid, plan, target, []string{primary, repair})
				if err != nil || got != nil {
					t.Fatalf("unverified printed source adopted: %#v, %v", got, err)
				}
			}
		})
	}
}
