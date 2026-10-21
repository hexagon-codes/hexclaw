package k12

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"reflect"
	"strings"
	"testing"
)

func TestScopedUprightAdjudicationViewPreservesSourcePixels(t *testing.T) {
	page := recognitionLayoutPlanTestPNG(t, 180, 130)
	source, err := png.Decode(bytes.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	for _, angle := range []int{0, 90, 180, 270} {
		for _, mode := range []string{"new solve", "legacy", "grade", "uncertain"} {
			t.Run(fmt.Sprintf("%d/%s", angle, mode), func(t *testing.T) {
				scope := &RecognitionImageScopeInputV1{Version: RecognitionImageScopeVersionV1, Action: "solve", SourceReviewPolicy: "upright_context_v1"}
				confidence := .99
				if mode == "legacy" {
					scope.SourceReviewPolicy = ""
				}
				if mode == "grade" {
					scope.Action = "grade"
				}
				if mode == "uncertain" {
					confidence = .8
				}
				plan, err := BuildRecognitionLayoutPlanV2(RecognitionLayoutPlanInputV2{
					PagePNG: page, Manifest: RecognitionLayoutManifestSuccessV2{InvocationID: "first-read", ResultDigest: recognitionLayoutSHA256([]byte("manifest"))},
					PageOrientation: &RecognitionPageOrientationV1{Version: "page_orientation_v1", PageDigest: recognitionLayoutSHA256(page), ClockwiseDegrees: angle, Confidence: confidence},
					ScopeInput:      scope, Scope: &RecognitionImageScopeV1{Version: RecognitionImageScopeVersionV1, Source: "explicit", SelectedOrders: []int{1}, Basis: "current request"},
					Targets:         []RecognitionLayoutManifestTargetV2{{ManifestRef: "manifest_0001", ManifestOrder: 1, SourceNumberPath: []string{"1"}, DisplayLabel: "1.", Region: SourcePixelRegion{X: 40, Y: 30, Width: 60, Height: 50}}},
					InitialReadMode: RecognitionLayoutManifestWithContentV1, RecognitionFormat: RecognitionLayoutCompactV4, EnableSourceAdjudication: true,
				})
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := BuildRecognitionLayoutAdjudicationImageV2(page, plan, plan.Targets[0].TargetID)
				if err != nil {
					t.Fatal(err)
				}
				view, err := png.Decode(bytes.NewReader(encoded))
				if err != nil {
					t.Fatal(err)
				}
				r := *plan.Targets[0].AdjudicationRegion
				applied := 0
				if mode == "new solve" {
					applied = angle
				}
				wantW, wantH := r.Width, r.Height
				if applied == 90 || applied == 270 {
					wantW, wantH = wantH, wantW
				}
				if view.Bounds().Dx() != wantW || view.Bounds().Dy() != wantH {
					t.Fatalf("view=%v want=%dx%d", view.Bounds(), wantW, wantH)
				}
				for y := 0; y < r.Height; y++ {
					for x := 0; x < r.Width; x++ {
						dx, dy := x, y
						switch applied {
						case 90:
							dx, dy = r.Height-1-y, x
						case 180:
							dx, dy = r.Width-1-x, r.Height-1-y
						case 270:
							dx, dy = y, r.Width-1-x
						}
						ar, ag, ab, aa := source.At(r.X+x, r.Y+y).RGBA()
						br, bg, bb, ba := view.At(dx, dy).RGBA()
						if ar != br || ag != bg || ab != bb || aa != ba {
							t.Fatalf("source pixel changed at %d,%d", x, y)
						}
					}
				}
				if recognitionLayoutSHA256(encoded) != plan.Targets[0].AdjudicationDigest {
					t.Fatal("view digest not frozen in plan")
				}
			})
		}
	}
}

func TestAdjudicationViewMapsSourceGeometryWithoutRewritingWords(t *testing.T) {
	for _, tc := range []struct {
		angle      int
		focus, box SourcePixelRegion
	}{
		{90, SourcePixelRegion{X: 40, Y: 10, Width: 20, Height: 30}, SourcePixelRegion{X: 7, Y: 64, Width: 13, Height: 11}},
		{180, SourcePixelRegion{X: 80, Y: 40, Width: 30, Height: 20}, SourcePixelRegion{X: 104, Y: 60, Width: 11, Height: 13}},
		{270, SourcePixelRegion{X: 20, Y: 80, Width: 20, Height: 30}, SourcePixelRegion{X: 100, Y: 5, Width: 13, Height: 11}},
	} {
		t.Run(fmt.Sprint(tc.angle), func(t *testing.T) {
			digest := recognitionLayoutSHA256([]byte("page"))
			plan := RecognitionLayoutPlanV2{PageDigest: digest, ScopeInput: &RecognitionImageScopeInputV1{Version: RecognitionImageScopeVersionV1, Action: "solve", SourceReviewPolicy: RecognitionImageSourceUprightReviewPolicyV1}, PageOrientation: &RecognitionPageOrientationV1{Version: "page_orientation_v1", PageDigest: digest, ClockwiseDegrees: tc.angle, Confidence: .99}}
			target := RecognitionLayoutTargetV2{AdjudicationRegion: &SourcePixelRegion{X: 100, Y: 200, Width: 120, Height: 80}, OriginalRegion: &SourcePixelRegion{X: 110, Y: 220, Width: 30, Height: 20}}
			view, err := RecognitionLayoutAdjudicationViewTargetV1(plan, target)
			if err != nil || *view.OriginalRegion != tc.focus {
				t.Fatalf("view=%+v err=%v", view, err)
			}
			if target.OriginalRegion.X != 110 || target.AdjudicationRegion.X != 100 {
				t.Fatal("frozen geometry changed")
			}
			raw := `{"verification":{"target_ownership_confirmed":true,"active_answer_complete":true,"question_complete_and_legible":true},"items":[{"target_id":"t1","kind":"question","recognition":{"question":"内金水河，建筑物，不是12。","subject":"语文","answer_state":"present","student_answer":"答案原文","recognition_confidence":0.99,"ocr_signals":[],"answer_bbox":{"x":5,"y":7,"width":11,"height":13}}}],"final_answer":{"raw_transcription":"最终原文","bbox":{"x":5,"y":7,"width":11,"height":13},"is_final_answer":true,"target_ownership_confirmed":true,"not_cancelled":true,"complete_and_legible":true}}`
			mapped, err := RecognitionLayoutAdjudicationSourceResponseV1(raw, plan, target)
			if err != nil {
				t.Fatal(err)
			}
			var parsed struct {
				Items []struct {
					Recognition struct {
						Question string            `json:"question"`
						Answer   string            `json:"student_answer"`
						Box      SourcePixelRegion `json:"answer_bbox"`
					} `json:"recognition"`
				} `json:"items"`
			}
			if err := json.Unmarshal([]byte(mapped), &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed.Items[0].Recognition.Box != tc.box || parsed.Items[0].Recognition.Question != "内金水河，建筑物，不是12。" || parsed.Items[0].Recognition.Answer != "答案原文" {
				t.Fatalf("mapped=%s", mapped)
			}
			response, err := ParseRecognitionFinalAnswerResponseV1(mapped, *target.AdjudicationRegion)
			want := tc.box
			want.X += 100
			want.Y += 200
			if err != nil || response.Evidence == nil || response.Evidence.BBox != want {
				t.Fatalf("evidence=%+v err=%v", response.Evidence, err)
			}
			badFinalBox := raw[:strings.Index(raw, `,"final_answer":`)] + strings.Replace(raw[strings.Index(raw, `,"final_answer":`):], `"x":5`, `"x":500`, 1)
			for _, bad := range []string{badFinalBox, strings.Replace(raw, `"question":"内`, `"question":"duplicate","question":"内`, 1)} {
				if _, err := RecognitionLayoutAdjudicationSourceResponseV1(bad, plan, target); err == nil {
					t.Fatal("invalid source geometry or duplicate field accepted")
				}
			}
			nullRaw := strings.Replace(raw, `"answer_bbox":{"x":5,"y":7,"width":11,"height":13}`, `"answer_bbox":null`, 1)
			nullRaw = nullRaw[:strings.Index(nullRaw, `,"final_answer":`)] + `,"final_answer":null}`
			nullMapped, err := RecognitionLayoutAdjudicationSourceResponseV1(nullRaw, plan, target)
			if err != nil || !strings.Contains(nullMapped, `"answer_bbox":null`) || !strings.Contains(nullMapped, `"final_answer":null`) {
				t.Fatalf("null response changed: %s %v", nullMapped, err)
			}
			for _, invalidBox := range []string{`{"x":0,"y":0,"width":0,"height":0}`, `{"x":500,"y":7,"width":11,"height":13}`} {
				optionalRaw := strings.Replace(nullRaw, `"answer_bbox":null`, `"answer_bbox":`+invalidBox, 1)
				optionalMapped, err := RecognitionLayoutAdjudicationSourceResponseV1(optionalRaw, plan, target)
				if err != nil || !strings.Contains(optionalMapped, `"answer_bbox":null`) {
					t.Fatalf("optional box blocked printed question: %s %v", optionalMapped, err)
				}
				if _, err := ParseRecognitionFinalAnswerResponseV1(optionalMapped, *target.AdjudicationRegion); err != nil {
					t.Fatal(err)
				}
			}
			plan.ScopeInput = nil
			legacy, err := RecognitionLayoutAdjudicationSourceResponseV1(raw, plan, target)
			if err != nil || !reflect.DeepEqual(legacy, raw) {
				t.Fatal("legacy raw response changed")
			}
		})
	}
}
