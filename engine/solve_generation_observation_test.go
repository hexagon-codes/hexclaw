package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
)

func TestSolveGenerationExecutionObservationPreservesActualResponseAndTrustedReceipts(t *testing.T) {
	for i := 1; i <= 2; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", fmt.Sprintf("solve-generation-observation-%d.json", i)))
			if err != nil {
				t.Fatal(err)
			}
			var original SubAgentResult
			if err := json.Unmarshal(raw, &original); err != nil {
				t.Fatal(err)
			}
			parsed, err := decodeSolveGeneration(original)
			if err != nil || parsed.Generation == nil || parsed.Generation.OutputVersion != solveWithTeachingReportV2 || parsed.Generation.RawResponse != original.Output || parsed.Output == "" {
				t.Fatalf("complete observed generation was rejected: err=%v", err)
			}
			if !reflect.DeepEqual(parsed.ExecutionReceipt, original.ExecutionReceipt) || !reflect.DeepEqual(parsed.ExecutionReceipts, original.ExecutionReceipts) || parsed.SessionID != original.SessionID {
				t.Fatal("wire observation altered the trusted execution receipts")
			}
			original.ExecutionReceipt, original.ExecutionReceipts = nil, nil
			untrusted, err := decodeSolveGeneration(original)
			if err != nil || untrusted.ExecutionReceipt != nil || len(untrusted.ExecutionReceipts) != 0 {
				t.Fatal("model execution observation was promoted to trusted evidence")
			}
		})
	}
}

func TestSolveGenerationExecutionObservationKeepsStrictWireFields(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		want        bool
	}{
		{"absent", "", true},
		{"string", `,"execution_evidence":"EXECUTION_RUN_ID: observation-only"`, true},
		{"null is absent", `,"execution_evidence":null`, true},
		{"object", `,"execution_evidence":{}`, false},
		{"array", `,"execution_evidence":[]`, false},
		{"boolean", `,"execution_evidence":true`, false},
		{"number", `,"execution_evidence":1`, false},
		{"other unknown field", `,"unexpected":"metadata"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"schema":"solve_with_teaching_report_v2","solution":"1+1=2。\n答案：2","parent_guide":{"answer":"2"}` + tc.extra + `}`
			got, err := decodeSolveGeneration(SubAgentResult{Output: raw})
			if (err == nil) != tc.want || tc.want && (got.Generation == nil || got.Generation.RawResponse != raw) || !tc.want && !errors.Is(err, egress.ErrProviderResponseProcessed) {
				t.Fatalf("accepted=%t want=%t err=%v", err == nil, tc.want, err)
			}
		})
	}
	if _, err := decodeSolveGeneration(SubAgentResult{Output: `{"schema":"solve_with_teaching_report_v2","parent_guide":{"answer":"2"},"execution_evidence":"observation"}`}); !errors.Is(err, egress.ErrProviderResponseProcessed) {
		t.Fatalf("missing solution was accepted: %v", err)
	}
}
