package k12

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestRecognitionPageOrientationV1(t *testing.T) {
	digest := "sha256:4e4ba413701220e22f371f0fb0c828d030001e1c5838227918d91bf67c4d45d9"
	for _, angle := range []int{0, 90, 180, 270} {
		t.Run(fmt.Sprint(angle), func(t *testing.T) {
			raw := fmt.Sprintf(`{"page_orientation":{"clockwise_degrees":%d,"confidence":0.98},"targets":[]}`, angle)
			got := RecognitionPageOrientationFromInitialReadV1(raw, digest)
			if got == nil || got.ClockwiseDegrees != angle || got.PageDigest != digest || got.Version != "page_orientation_v1" || got.Rotation() != angle {
				t.Fatalf("orientation receipt = %+v", got)
			}
		})
	}
	for _, raw := range []string{
		`{"targets":[]}`,
		`{"page_orientation":{"clockwise_degrees":45,"confidence":0.99}}`,
		`{"page_orientation":{"clockwise_degrees":90,"confidence":1.1}}`,
		`{"page_orientation":{"clockwise_degrees":90}}`,
	} {
		if got := RecognitionPageOrientationFromInitialReadV1(raw, digest); got != nil {
			t.Fatalf("invalid or absent orientation accepted: %+v", got)
		}
	}
	low := RecognitionPageOrientationFromInitialReadV1(`{"page_orientation":{"clockwise_degrees":90,"confidence":0.8}}`, digest)
	if low == nil || low.Rotation() != 0 {
		t.Fatal("low confidence changed image orientation")
	}
	var missing *RecognitionPageOrientationV1
	if missing.Rotation() != 0 {
		t.Fatal("legacy receipt changed image orientation")
	}
}

func TestRecognitionPageOrientationKeepsLegacySourceAndPlanReplay(t *testing.T) {
	raw, err := os.ReadFile("testdata/initial_read_compact_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := CanonicalRecognitionLayoutInitialReadEntriesV1(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["page_orientation"] = json.RawMessage(`{"clockwise_degrees":270,"confidence":0.97}`)
	withOrientation, _ := json.Marshal(envelope)
	current, err := CanonicalRecognitionLayoutInitialReadEntriesV1(string(withOrientation))
	if err != nil || !reflect.DeepEqual(legacy, current) {
		t.Fatalf("page metadata changed source observations: %v", err)
	}
	page := recognitionLayoutPlanTestPNG(t, 240, 220)
	input := RecognitionLayoutPlanInputV2{PagePNG: page, Manifest: RecognitionLayoutManifestSuccessV2{InvocationID: "legacy-whole-page", ResultDigest: "sha256:" + strings.Repeat("a", 64)}, Targets: recognitionLayoutPlanTestTargets(1)}
	plan, err := BuildRecognitionLayoutPlanV2(input)
	if err != nil {
		t.Fatal(err)
	}
	frozen, _ := json.Marshal(plan)
	if bytes.Contains(frozen, []byte("page_orientation")) {
		t.Fatal("legacy plan gained orientation metadata")
	}
	var replay RecognitionLayoutPlanV2
	if err := json.Unmarshal(frozen, &replay); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecognitionLayoutPlanV2(replay); err != nil || replay.AuthorizedPlanDigest != plan.AuthorizedPlanDigest {
		t.Fatalf("legacy plan no longer replays: %v", err)
	}
	input.PageOrientation = RecognitionPageOrientationFromInitialReadV1(string(withOrientation), plan.PageDigest)
	oriented, err := BuildRecognitionLayoutPlanV2(input)
	if err != nil || oriented.PageOrientation == nil || oriented.AuthorizedPlanDigest == plan.AuthorizedPlanDigest {
		t.Fatalf("new plan did not freeze orientation: %v", err)
	}
	changed := *oriented.PageOrientation
	changed.ClockwiseDegrees = 90
	oriented.PageOrientation = &changed
	if ValidateRecognitionLayoutPlanV2(oriented) == nil {
		t.Fatal("orientation drift preserved a frozen plan identity")
	}
}
