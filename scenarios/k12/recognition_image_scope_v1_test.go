package k12

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

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
