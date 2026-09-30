package engineadapter

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 原始成功正文与冻结分组来自同一次整页识别，仅将持久身份替换为夹具身份。
func TestRecognitionBatchClosureRestoresSixteenFrozenTargets(t *testing.T) {
	data, err := os.ReadFile("../testdata/recognition-batch-closure.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Targets   []k12.RecognitionLayoutTargetV2 `json:"targets"`
		Batches   []k12.RecognitionLayoutBatchV2  `json:"batches"`
		Responses map[string]string               `json:"responses"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	plan := k12.RecognitionLayoutPlanV2{Targets: fixture.Targets, Batches: fixture.Batches}
	seen := make(map[string]bool)
	for _, batch := range fixture.Batches {
		targets, err := recognitionLayoutBatchTargetsV2(plan, batch)
		if err != nil {
			t.Fatal(err)
		}
		raw := fixture.Responses[string(batch.Unit)]
		completed, repaired := k12.CompleteRecognitionLayoutBatchJSON(raw)
		if repaired != (batch.Unit == "layout_batch_0004") {
			t.Fatalf("unexpected completion for %s: %v", batch.Unit, repaired)
		}
		var original struct {
			Items []struct {
				Recognition json.RawMessage `json:"recognition"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(completed), &original); err != nil {
			t.Fatal(err)
		}
		decision := classifyRecognitionLayoutBatchV2(raw, targets, k12.RecognitionLayoutCompactV4)
		if decision.classification != k12.RecognitionLayoutBatchClassifiedV2 || len(decision.outcomes) != len(targets) {
			t.Fatalf("batch %s did not preserve its candidates: %+v", batch.Unit, decision)
		}
		for i, candidate := range decision.candidates {
			if candidate.Classification != k12.RecognitionLayoutCandidateValidV2 || candidate.CandidateID != targets[i].TargetID || seen[candidate.CandidateID] {
				t.Fatalf("lost, duplicated or reassigned candidate: %+v", candidate)
			}
			seen[candidate.CandidateID] = true
			var want, got map[string]any
			if err := json.Unmarshal(original.Items[i].Recognition, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(candidate.ResultJSON, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("completion changed source fields for %s", candidate.CandidateID)
			}
		}
	}
	if len(seen) != 16 {
		t.Fatalf("restored %d frozen targets, want 16", len(seen))
	}
}

func TestRecognitionBatchClosureKeepsAmbiguityAndDamageInvalid(t *testing.T) {
	targets := []k12.RecognitionLayoutTargetV2{{TargetID: "target-1", Region: k12.SourcePixelRegion{Width: 300, Height: 300}}}
	valid := `{"items":[{"target_id":"t1","kind":"question","recognition":{"question":"2+2=","subject":"数学","answer_state":"blank","student_answer":"","recognition_confidence":0.99,"ocr_signals":[]}}]}`
	missing := strings.TrimSuffix(valid, "}]}") + "]}"
	for _, tc := range []struct {
		name, raw string
		terminal  bool
	}{
		{"extra target", strings.Replace(missing, `"t1"`, `"t2"`, 1), true},
		{"duplicate target", `{"items":[{"target_id":"t1","kind":"non_question","recognition":null},{"target_id":"t1","kind":"non_question","recognition":null]}`, true},
		{"missing question value", strings.Replace(missing, `"2+2="`, ``, 1), true},
		{"two missing closures", strings.TrimSuffix(missing, "}]}") + "]}", true},
		{"missing question field", strings.Replace(missing, `"question":"2+2=",`, ``, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyRecognitionLayoutBatchV2(tc.raw, targets, k12.RecognitionLayoutCompactV4)
			if len(got.outcomes) != 0 {
				t.Fatal("ambiguous or incomplete source produced a valid question")
			}
			if tc.terminal && got.classification != k12.RecognitionLayoutBatchTerminalAmbiguousV2 {
				t.Fatalf("true ambiguity was accepted: %+v", got)
			}
			if !tc.terminal && (len(got.candidates) != 1 || got.candidates[0].Classification != k12.RecognitionLayoutCandidateInvalidV2) {
				t.Fatalf("missing field bypassed the question contract: %+v", got)
			}
		})
	}
	for _, text := range []string{`literal }] and { [`, `quoted " } ]`, `trailing slash \`, `\frac{3}{4} and \(x\)`} {
		t.Run(fmt.Sprintf("string_%q", text), func(t *testing.T) {
			encoded, _ := json.Marshal(text)
			raw := `{"items":[{"question":` + string(encoded) + `]}`
			want := `{"items":[{"question":` + string(encoded) + `}]}`
			got, ok := k12.CompleteRecognitionLayoutBatchJSON(raw)
			if !ok || got != want {
				t.Fatalf("string or escape changed: %q", got)
			}
			if unchanged, ok := k12.CompleteRecognitionLayoutBatchJSON(want); ok || unchanged != want {
				t.Fatal("valid JSON was rewritten")
			}
		})
	}
}
