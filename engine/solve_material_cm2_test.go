package engine

import (
	"encoding/json"
	"os"
	"testing"
)

// 保留实际验证正文及 stdout，验证程序回执到强证据的完整转换。
func TestSolveMaterialSquareCentimetresFromSavedExecution(t *testing.T) {
	raw, err := os.ReadFile("testdata/material_verification_cm2.json")
	if err != nil {
		t.Fatal(err)
	}
	var saved SubAgentResult
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, candidate string
		verdict         verifyVerdict
		grounded        bool
	}{
		{"valid", "42 cm²", verdictAgree, true},
		{"wrong-number", "48 cm²", verdictDisagree, true},
		{"wrong-unit", "42 m²", verdictAgree, false},
		{"missing-unit", "42", verdictAgree, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := &solveExec{verifierOut: saved.Output, verifierStdout: saved.ExecutionReceipt.Stdout}
			verdict, computed, grounded := NewSolveSkill(exec.fn, nil).verifySolution(t.Context(), "图中长方形的面积", "长×宽=7×6，答案："+tc.candidate, tc.candidate, "")
			if verdict != tc.verdict || computed != "42 cm²" || grounded != tc.grounded {
				t.Fatalf("verdict=%v computed=%q grounded=%v", verdict, computed, grounded)
			}
			if len(exec.specs) != 1 {
				t.Fatalf("valid execution should not request another model call: %d", len(exec.specs))
			}
		})
	}
	exec := &solveExec{solverOuts: []string{"长×宽=7×6。\n答案：42 cm²"}, verifierOut: saved.Output, verifierStdout: saved.ExecutionReceipt.Stdout}
	result, err := NewSolveSkill(exec.fn, nil).Execute(t.Context(), map[string]any{"problem": "图中长方形长7厘米、宽6厘米，面积是多少？", "self_consistency": 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata["solve_evidence"] != "numeric_exec" || result.Metadata["solve_primary_digest"] == "" || result.Metadata["solve_verification_run_id"] == "" {
		t.Fatalf("actual execution lost at solve boundary: %+v", result.Metadata)
	}
	for _, variant := range []string{"missing-receipt", "truncated", "failed"} {
		t.Run(variant, func(t *testing.T) {
			r := *saved.ExecutionReceipt
			r.InputDigest = executionInputDigest("same frozen verification")
			switch variant {
			case "missing-receipt":
				r.RunID = ""
			case "truncated":
				r.StdoutTruncated = true
			case "failed":
				r.ExitCode = 1
			}
			if _, ok := r.computed("same frozen verification"); ok {
				t.Fatal("invalid execution became numeric evidence")
			}
		})
	}
}
