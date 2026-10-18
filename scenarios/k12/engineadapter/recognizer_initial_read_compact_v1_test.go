package engineadapter

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestRecognitionInitialReadCompactV1ProjectsCompleteTargets(t *testing.T) {
	raw, err := os.ReadFile("../testdata/initial_read_compact_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parseRecognitionInitialReadEnvelopeV1(string(raw))
	if err != nil || len(entries) != 2 {
		t.Fatalf("compact manifest rejected: %v", err)
	}
	for _, entry := range entries {
		projected, err := recognitionInitialReadProjectionV1(entry, k12.RecognitionLayoutTargetV2{TargetID: entry.manifest.ManifestRef, Region: entry.manifest.Region})
		if err != nil {
			t.Fatalf("complete compact observation rejected: %v", err)
		}
		var result struct {
			Recognition struct {
				AnswerBBox k12.SourcePixelRegion `json:"answer_bbox"`
			} `json:"recognition"`
		}
		if json.Unmarshal(projected, &result) != nil || result.Recognition.AnswerBBox != (k12.SourcePixelRegion{X: 10, Y: 2, Width: 8, Height: 5}) {
			t.Fatalf("absolute source rectangle was not projected relative to its target: %s", projected)
		}
	}
}

func TestRecognitionInitialReadCompactV1SelectsOnlyFrozenPolicy(t *testing.T) {
	legacy, err := buildRecognitionInitialReadPromptForPolicy("")
	if err != nil || legacy != recognitionLayoutInitialReadPromptV1 {
		t.Fatal("legacy frozen prompt changed")
	}
	compact, err := buildRecognitionInitialReadPromptForPolicy(k12.RecognitionExecutionPolicyCompactDAGV1)
	if err != nil || compact == legacy || compact == "" {
		t.Fatal("compact frozen policy did not select its transport")
	}
	if prompt, err := buildRecognitionInitialReadPromptForPolicy("unknown"); prompt != "" || !errors.Is(err, k12.ErrRecognitionProtocolInvalid) {
		t.Fatal("unknown frozen policy silently changed transport")
	}
}
