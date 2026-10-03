package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/skill"
)

// 单量等式必须自身成立，再与独立执行结果比较，不能仅截取最后一个数字。
func TestSolveSingleQuantityEquationEvidence(t *testing.T) {
	const problem = "一袋面包重3/10千克，3袋面包共重多少千克？"
	const stdout = "9/10 kg\n"
	for _, tc := range []struct {
		name, candidate string
		grounded        bool
		verdict         string
	}{
		{"saved-equation", "3/10×3＝9/10（kg）", true, "agree"},
		{"same-unit-name", "3/10×3=0.9 千克", true, "agree"},
		{"ascii-unit-parentheses", "(3/10)*3=9/10(kg)", true, "agree"},
		{"correct-equation-wrong-answer", "3/10×4=6/5 kg", true, "disagree"},
		{"wrong-equation", "3/10×4＝9/10（kg）", false, "agree"},
		{"wrong-value", "3/10×3＝8/10（kg）", false, "agree"},
		{"wrong-unit", "3/10×3＝9/10（g）", false, "agree"},
		{"missing-unit", "3/10×3＝9/10", false, "agree"},
		{"multiple-equations", "3/10×3=0.9=9/10 kg", false, "agree"},
		{"multiple-conclusions", "3/10×3=9/10 kg；1 kg", false, "agree"},
		{"ambiguous-conclusion", "3/10×3=大约9/10 kg", false, "agree"},
		{"unmatched-unit-parentheses", "3/10×3=9/10(kg）", false, "agree"},
		{"incomplete-left-expression", "3/10×3?=9/10 kg", false, "agree"},
		{"separated-left-number", "3 0/10×3=9 kg", false, "agree"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := &solveExec{
				solverOuts:     []string{"每袋重量乘袋数。\n答案：" + tc.candidate},
				verifierOut:    "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: 9/10 kg",
				verifierStdout: stdout,
			}
			result, err := NewSolveSkill(exec.fn, nil).Execute(t.Context(), map[string]any{"problem": problem, "self_consistency": 1})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Metadata["solve_evidence"] == "numeric_exec"; got != tc.grounded {
				t.Fatalf("numeric evidence=%v, want %v: %+v", got, tc.grounded, result.Metadata)
			}
			if result.Metadata["solve_verdict"] != tc.verdict {
				t.Fatalf("verdict=%q, want %q", result.Metadata["solve_verdict"], tc.verdict)
			}
			if tc.grounded && tc.verdict == "agree" && (result.Metadata["solve_verification_run_id"] == "" || result.Metadata["solve_verification_input_digest"] == "") {
				t.Fatalf("valid equation lost execution identity: %+v", result.Metadata)
			}
			if !strings.Contains(result.Content, tc.candidate) {
				t.Fatal("original answer was changed")
			}
			if len(exec.specs) != 2 {
				t.Fatalf("quantity comparison must not add model calls: %d", len(exec.specs))
			}
		})
	}
}

// 模型正文和正确等式不能代替成功、完整且与当前输入绑定的执行回执。
func TestSolveSingleQuantityEquationReceiptGuards(t *testing.T) {
	const stdout = "9/10 kg\n"
	for _, variant := range []string{"valid", "missing", "missing-run", "failed-status", "failed-exit", "timeout", "truncated", "stdout-truncated", "stderr-truncated", "incomplete-stdout"} {
		t.Run(variant, func(t *testing.T) {
			var calls int
			execute := func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
				calls++
				if spec.Agent == solverAgentName {
					return SubAgentResult{Output: "每袋重量乘袋数。\n答案：3/10×3＝9/10（kg）"}, nil
				}
				data := map[string]any{"run_id": "quantity-execution", "status": "success", "exit_code": 0, "stdout_bytes": len(stdout)}
				switch variant {
				case "missing":
					data = nil
				case "missing-run":
					data["run_id"] = ""
				case "failed-status":
					data["status"] = "failed"
				case "failed-exit":
					data["exit_code"] = 1
				case "timeout":
					data["timeout"] = true
				case "truncated":
					data["truncated"] = true
				case "stdout-truncated":
					data["stdout_truncated"] = true
				case "stderr-truncated":
					data["stderr_truncated"] = true
				case "incomplete-stdout":
					data["stdout_bytes"] = len(stdout) + 1
				}
				captureCodeExecutionReceipt(ctx, codeExecToolName, &skill.Result{Content: stdout, Data: data})
				return SubAgentResult{Output: "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: 9/10 kg"}, nil
			}
			result, err := NewSolveSkill(execute, nil).Execute(t.Context(), map[string]any{"problem": "一袋面包重3/10千克，3袋面包共重多少千克？", "self_consistency": 1})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Metadata["solve_evidence"] == "numeric_exec"; got != (variant == "valid") {
				t.Fatalf("%s receipt became numeric evidence: %+v", variant, result.Metadata)
			}
			if variant == "valid" && calls != 2 {
				t.Fatalf("valid receipt should not trigger another verifier: calls=%d", calls)
			}
		})
	}
	wrongInput := &CodeExecutionReceipt{InputDigest: executionInputDigest("another question"), RunID: "run", Status: "success", Stdout: stdout, StdoutBytes: int64(len(stdout))}
	if _, ok := wrongInput.computed("current question"); ok {
		t.Fatal("an unrelated execution receipt became numeric evidence")
	}
}
