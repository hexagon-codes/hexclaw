package k12

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"strings"
	"testing"
)

func TestRecognitionSingleTargetContextPreservesPrintedEdgesAndSelection(t *testing.T) {
	canvas := image.NewRGBA(image.Rect(0, 0, 960, 642))
	printedEdge := image.Pt(900, 570)
	canvas.Set(printedEdge.X, printedEdge.Y, color.Black)
	var page bytes.Buffer
	if err := png.Encode(&page, canvas); err != nil {
		t.Fatal(err)
	}
	first := RecognitionLayoutManifestTargetV2{ManifestRef: "manifest_0001", ManifestOrder: 1,
		SourceNumberPath: []string{"1"}, DisplayLabel: "1.", Region: SourcePixelRegion{X: 120, Y: 165, Width: 650, Height: 445}}
	for _, selectedFromMany := range []bool{false, true} {
		name := "single original question"
		if selectedFromMany {
			name = "one selected from multiple questions"
		}
		t.Run(name, func(t *testing.T) {
			input := RecognitionLayoutPlanInputV2{PagePNG: page.Bytes(),
				Manifest: RecognitionLayoutManifestSuccessV2{InvocationID: "original-whole-page", ResultDigest: "sha256:" + strings.Repeat("a", 64)},
				Targets:  []RecognitionLayoutManifestTargetV2{first}, RecognitionFormat: RecognitionLayoutCompactV4,
				InitialReadMode: RecognitionLayoutManifestWithContentV1, EnableSourceAdjudication: true,
				ScopeInput: &RecognitionImageScopeInputV1{Version: RecognitionImageScopeVersionV1, SourceReviewPolicy: RecognitionImageSourceUprightReviewPolicyV1},
				Scope:      &RecognitionImageScopeV1{Version: RecognitionImageScopeVersionV1, Source: "inferred", SelectedOrders: []int{1}, Basis: "One complete numbered question is visible."}}
			if selectedFromMany {
				input.Targets = append(input.Targets, RecognitionLayoutManifestTargetV2{ManifestRef: "manifest_0002", ManifestOrder: 2,
					SourceNumberPath: []string{"2"}, DisplayLabel: "2.", Region: SourcePixelRegion{X: 800, Y: 165, Width: 140, Height: 445}})
				input.Scope.Source = "explicit"
				input.Scope.Excluded = []RecognitionImageScopeExclusionV1{{Order: 2, Reason: "Only question 1 was requested."}}
			}
			legacy, err := BuildRecognitionLayoutPlanV2(input)
			if err != nil {
				t.Fatal(err)
			}
			policy := *input.ScopeInput
			policy.SourceReviewPolicy = RecognitionImageSourceUprightReviewPolicyV2
			input.ScopeInput = &policy
			current, err := BuildRecognitionLayoutPlanV2(input)
			if err != nil {
				t.Fatal(err)
			}
			region := current.Targets[0].Region
			visible := printedEdge.In(image.Rect(region.X, region.Y, region.X+region.Width, region.Y+region.Height))
			if selectedFromMany {
				if visible || region != legacy.Targets[0].Region || len(current.ObservedTargets) != 2 {
					t.Fatal("selecting one question exposed excluded neighboring pixels")
				}
			} else if !visible {
				t.Fatal("single-question review still clips the printed equation at the right edge")
			}
			if current.Targets[0].OriginalRegion == nil || *current.Targets[0].OriginalRegion != first.Region {
				t.Fatal("review context replaced the original manifest region")
			}
			if ValidateRecognitionLayoutPlanV2(legacy) != nil || ValidateRecognitionLayoutPlanV2(current) != nil {
				t.Fatal("old or current frozen geometry cannot be replayed")
			}
		})
	}
}

func TestRecognitionImageScopeV1PreservesGeometryAndExactSet(t *testing.T) {
	input := RecognitionLayoutPlanInputV2{InitialReadMode: RecognitionLayoutManifestWithContentV1, RecognitionFormat: RecognitionLayoutCompactV4, PagePNG: recognitionLayoutPlanTestPNG(t, 240, 220), Manifest: RecognitionLayoutManifestSuccessV2{InvocationID: "whole-scope", ResultDigest: "sha256:" + strings.Repeat("a", 64)}, Targets: recognitionLayoutPlanTestTargets(3), EnableSourceAdjudication: true}
	legacy, err := BuildRecognitionLayoutPlanV2(input)
	if err != nil {
		t.Fatal(err)
	}
	input.ScopeInput = &RecognitionImageScopeInputV1{Version: RecognitionImageScopeVersionV1, MessageIntent: "只解第1题"}
	input.Scope = &RecognitionImageScopeV1{Version: RecognitionImageScopeVersionV1, Source: "explicit", SelectedOrders: []int{1}, Basis: "Current instruction specifies question 1", Excluded: []RecognitionImageScopeExclusionV1{{Order: 2, Reason: "outside explicit scope"}, {Order: 3, Reason: "outside explicit scope"}}, Dependencies: []RecognitionImageScopeDependencyV1{{ForOrders: []int{1}, Region: SourcePixelRegion{X: 0, Y: 0, Width: 10, Height: 10}, Text: "shared visible material"}}}
	plan, err := BuildRecognitionLayoutPlanV2(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Targets) != 1 || len(plan.ObservedTargets) != 3 || len(plan.Batches) != 0 {
		t.Fatalf("execution/observation sets: %+v", plan)
	}
	if plan.Targets[0].TargetID != legacy.Targets[0].TargetID || plan.Targets[0].Region != legacy.Targets[0].Region || plan.Targets[0].CropDigest != legacy.Targets[0].CropDigest {
		t.Fatal("filtering changed source identity or crop geometry")
	}
	if plan.AuthorizedPlanDigest == legacy.AuthorizedPlanDigest {
		t.Fatal("scope is absent from input identity")
	}
	raw, _ := json.Marshal(plan)
	var restored RecognitionLayoutPlanV2
	if json.Unmarshal(raw, &restored) != nil || ValidateRecognitionLayoutPlanV2(restored) != nil || !reflect.DeepEqual(plan, restored) {
		t.Fatal("persisted scope cannot be restored")
	}
	restored.Scope.SelectedOrders = []int{2}
	if ValidateRecognitionLayoutPlanV2(restored) == nil {
		t.Fatal("scope drift was accepted")
	}
	input.Scope.Excluded = input.Scope.Excluded[:1]
	if _, err := BuildRecognitionLayoutPlanV2(input); err == nil {
		t.Fatal("unaccounted candidate was accepted")
	}
	old, _ := json.Marshal(legacy)
	var oldFields map[string]json.RawMessage
	_ = json.Unmarshal(old, &oldFields)
	if oldFields["scope"] != nil || oldFields["observed_targets"] != nil || oldFields["scope_input"] != nil {
		t.Fatal("legacy projection contains new zero fields")
	}
}
