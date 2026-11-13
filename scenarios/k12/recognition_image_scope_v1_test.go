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

func TestRecognitionOwnAnswerContextPreservesObservedPixelsAndLegacyPlans(t *testing.T) {
	canvas := image.NewRGBA(image.Rect(0, 0, 1076, 1462))
	answerPixel := image.Pt(949, 650)
	canvas.Set(answerPixel.X, answerPixel.Y, color.Black)
	var page bytes.Buffer
	if err := png.Encode(&page, canvas); err != nil {
		t.Fatal(err)
	}
	own := SourcePixelRegion{X: 758, Y: 625, Width: 197, Height: 45}
	input := RecognitionLayoutPlanInputV2{PagePNG: page.Bytes(),
		Manifest: RecognitionLayoutManifestSuccessV2{InvocationID: "original-answer-page", ResultDigest: "sha256:" + strings.Repeat("a", 64)},
		Targets: []RecognitionLayoutManifestTargetV2{
			{ManifestRef: "manifest_0001", ManifestOrder: 1, SourceNumberPath: []string{"8"}, DisplayLabel: "8.", Region: SourcePixelRegion{X: 365, Y: 550, Width: 310, Height: 160}},
			{ManifestRef: "manifest_0002", ManifestOrder: 2, SourceNumberPath: []string{"9"}, DisplayLabel: "9.", Region: SourcePixelRegion{X: 690, Y: 556, Width: 190, Height: 126}, ObservedAnswerRegion: &own},
		}, RecognitionFormat: RecognitionLayoutCompactV4, InitialReadMode: RecognitionLayoutManifestWithContentV1,
		EnableSourceAdjudication: true,
		ScopeInput:               &RecognitionImageScopeInputV1{Version: RecognitionImageScopeVersionV1},
		Scope: &RecognitionImageScopeV1{Version: RecognitionImageScopeVersionV1, Source: "explicit", SelectedOrders: []int{2}, Basis: "Only question 9 was requested.",
			Excluded: []RecognitionImageScopeExclusionV1{{Order: 1, Reason: "Question 8 is outside the request."}}},
	}
	for _, policy := range []string{RecognitionImageSourceUprightReviewPolicyV1, RecognitionImageSourceUprightReviewPolicyV2, RecognitionImageSourceUprightReviewPolicyV3} {
		t.Run(policy, func(t *testing.T) {
			input.ScopeInput.SourceReviewPolicy = policy
			plan, err := BuildRecognitionLayoutPlanV2(input)
			if err != nil {
				t.Fatal(err)
			}
			target := plan.Targets[0]
			region := target.Region
			visible := answerPixel.In(image.Rect(region.X, region.Y, region.X+region.Width, region.Y+region.Height))
			if len(plan.Targets) != 1 || len(plan.ObservedTargets) != 2 || target.ManifestOrder != 2 || *target.OriginalRegion != input.Targets[1].Region || region.X < 675 || region.Width >= 1076 {
				t.Fatal("own answer expansion changed original identity or selected neighboring question")
			}
			if policy == RecognitionImageSourceUprightReviewPolicyV3 {
				if !visible || target.ObservedAnswerRegion == nil || *target.ObservedAnswerRegion != own {
					t.Fatal("review still clips this question's declared final answer")
				}
			} else {
				if visible || target.ObservedAnswerRegion != nil {
					t.Fatal("legacy review geometry changed")
				}
				legacyInput := input
				legacyInput.Targets = append([]RecognitionLayoutManifestTargetV2(nil), input.Targets...)
				legacyInput.Targets[1].ObservedAnswerRegion = nil
				legacy, err := BuildRecognitionLayoutPlanV2(legacyInput)
				if err != nil || !reflect.DeepEqual(plan, legacy) {
					t.Fatal("legacy serialized plan or digest changed")
				}
			}
			encoded, _ := json.Marshal(plan)
			var restored RecognitionLayoutPlanV2
			if json.Unmarshal(encoded, &restored) != nil || ValidateRecognitionLayoutPlanV2(restored) != nil || !reflect.DeepEqual(plan, restored) {
				t.Fatal("frozen own-answer geometry cannot be restored")
			}
			if policy == RecognitionImageSourceUprightReviewPolicyV3 {
				entries := make([]map[string]any, len(input.Targets))
				for index, source := range input.Targets {
					entry, _ := json.Marshal(source)
					_ = json.Unmarshal(entry, &entries[index])
					entries[index]["initial_read"] = map[string]any{"kind": "question", "recognition": map[string]any{"answer_state": "present", "student_answer": "=1", "answer_bbox": own}}
				}
				raw, _ := json.Marshal(map[string]any{"targets": entries})
				if err := ValidateRecognitionImageScopeSourceV1(plan, string(raw)); err != nil {
					t.Fatalf("original answer observation rejected: %v", err)
				}
				changed := own
				changed.X++
				restored.ObservedTargets[1].ObservedAnswerRegion = &changed
				if ValidateRecognitionImageScopeSourceV1(restored, string(raw)) == nil {
					t.Fatal("answer geometry detached from its original observation was accepted")
				}
			}
		})
	}
}

func TestRecognitionOwnAnswerRegionComesFromReadableOriginalObservation(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      *SourcePixelRegion
	}{
		{"present", `{"initial_read":{"kind":"question","recognition":{"answer_state":"present","student_answer":"=1","answer_bbox":{"x":758,"y":625,"width":197,"height":45}}}}`, &SourcePixelRegion{X: 758, Y: 625, Width: 197, Height: 45}},
		{"blank", `{"initial_read":{"kind":"question","recognition":{"answer_state":"blank","student_answer":"","answer_bbox":{"x":1,"y":1,"width":10,"height":10}}}}`, nil},
		{"non question", `{"initial_read":{"kind":"non_question","recognition":null}}`, nil},
		{"invalid box", `{"initial_read":{"kind":"question","recognition":{"answer_state":"present","student_answer":"1","answer_bbox":{"x":-1,"y":1,"width":10,"height":10}}}}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RecognitionInitialReadAnswerRegionV1(json.RawMessage(tc.raw)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("observed answer region=%+v, want=%+v", got, tc.want)
			}
		})
	}
}

func TestRecognitionOwnAnswerContextPreservesQuestionOrder(t *testing.T) {
	answer := SourcePixelRegion{X: 50, Y: 110, Width: 220, Height: 450}
	input := RecognitionLayoutPlanInputV2{PagePNG: recognitionLayoutPlanTestPNG(t, 800, 800),
		Manifest: RecognitionLayoutManifestSuccessV2{InvocationID: "answer-reading-order", ResultDigest: "sha256:" + strings.Repeat("a", 64)},
		Targets: []RecognitionLayoutManifestTargetV2{
			{ManifestRef: "manifest_0001", ManifestOrder: 1, SourceNumberPath: []string{"1"}, DisplayLabel: "1.", Region: SourcePixelRegion{X: 50, Y: 50, Width: 220, Height: 70}, ObservedAnswerRegion: &answer},
			{ManifestRef: "manifest_0002", ManifestOrder: 2, SourceNumberPath: []string{"2"}, DisplayLabel: "2.", Region: SourcePixelRegion{X: 400, Y: 50, Width: 220, Height: 70}},
			{ManifestRef: "manifest_0003", ManifestOrder: 3, SourceNumberPath: []string{"3"}, DisplayLabel: "3.", Region: SourcePixelRegion{X: 400, Y: 220, Width: 220, Height: 70}},
		}, RecognitionFormat: RecognitionLayoutCompactV4, InitialReadMode: RecognitionLayoutManifestWithContentV1,
		EnableSourceAdjudication: true,
		ScopeInput:               &RecognitionImageScopeInputV1{Version: RecognitionImageScopeVersionV1, SourceReviewPolicy: RecognitionImageSourceUprightReviewPolicyV3},
		Scope:                    &RecognitionImageScopeV1{Version: RecognitionImageScopeVersionV1, Source: "inferred", SelectedOrders: []int{1, 2, 3}, Basis: "All visible questions."}}
	plan, err := BuildRecognitionLayoutPlanV2(input)
	if err != nil {
		t.Fatal(err)
	}
	for index, target := range plan.Targets {
		if target.ManifestOrder != index+1 {
			t.Fatalf("answer extent changed printed reading order: position=%d order=%d", index, target.ManifestOrder)
		}
	}
	r := plan.Targets[0].Region
	if answer.Y+answer.Height > r.Y+r.Height || plan.Targets[0].ObservedAnswerRegion == nil || *plan.Targets[0].ObservedAnswerRegion != answer {
		t.Fatal("stable printed order clipped the observed answer")
	}
	if err := ValidateRecognitionLayoutPlanV2(plan); err != nil {
		t.Fatal(err)
	}
}

func TestRecognitionPartiallyVisibleRegionPreservesRawSource(t *testing.T) {
	original := SourcePixelRegion{X: 91, Y: 158, Width: 903, Height: 480}
	answer := SourcePixelRegion{X: 204, Y: 409, Width: 481, Height: 118}
	for _, tc := range []struct {
		name, policy string
		region       SourcePixelRegion
		wantValid    bool
	}{
		{"visible intersection", RecognitionImageSourceUprightReviewPolicyV3, original, true},
		{"legacy first", RecognitionImageSourceUprightReviewPolicyV1, original, false},
		{"legacy second", RecognitionImageSourceUprightReviewPolicyV2, original, false},
		{"entirely outside", RecognitionImageSourceUprightReviewPolicyV3, SourcePixelRegion{X: 960, Y: 158, Width: 34, Height: 480}, false},
		{"empty", RecognitionImageSourceUprightReviewPolicyV3, SourcePixelRegion{X: 91, Y: 158, Height: 480}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := RecognitionLayoutPlanInputV2{PagePNG: recognitionLayoutPlanTestPNG(t, 960, 642),
				Manifest:          RecognitionLayoutManifestSuccessV2{InvocationID: "partial-source-page", ResultDigest: "sha256:" + strings.Repeat("a", 64)},
				Targets:           []RecognitionLayoutManifestTargetV2{{ManifestRef: "manifest_0001", ManifestOrder: 1, SourceNumberPath: []string{"1"}, DisplayLabel: "1.", Region: tc.region, ObservedAnswerRegion: &answer}},
				RecognitionFormat: RecognitionLayoutCompactV4, InitialReadMode: RecognitionLayoutManifestWithContentV1, EnableSourceAdjudication: true,
				ScopeInput: &RecognitionImageScopeInputV1{Version: RecognitionImageScopeVersionV1, Action: "solve", SourceReviewPolicy: tc.policy},
				Scope:      &RecognitionImageScopeV1{Version: RecognitionImageScopeVersionV1, Source: "explicit", SelectedOrders: []int{1}, Basis: "Only question 1 was requested."}}
			plan, err := BuildRecognitionLayoutPlanV2(input)
			if !tc.wantValid {
				if err == nil {
					t.Fatal("invalid or legacy region contract changed")
				}
				return
			}
			if err != nil {
				t.Fatalf("visible question rejected instead of retaining its page intersection: %v", err)
			}
			target := plan.Targets[0]
			if *target.OriginalRegion != original || target.Region.X < 0 || target.Region.Y < 0 || target.Region.X+target.Region.Width != 960 || target.Region.Y+target.Region.Height > 642 || *target.ObservedAnswerRegion != answer || ValidateRecognitionLayoutPlanV2(plan) != nil {
				t.Fatal("raw observation changed or derived crop left the original page")
			}
			rawTarget, _ := json.Marshal(input.Targets[0])
			var entry map[string]any
			_ = json.Unmarshal(rawTarget, &entry)
			entry["initial_read"] = map[string]any{"kind": "question", "recognition": map[string]any{"answer_state": "present", "student_answer": "2 1/2 3 1/2", "answer_bbox": answer}}
			raw, _ := json.Marshal(map[string]any{"targets": []any{entry}})
			if ValidateRecognitionImageScopeSourceV1(plan, string(raw)) != nil {
				t.Fatal("original outlying observation lost its source binding")
			}
		})
	}
}

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
