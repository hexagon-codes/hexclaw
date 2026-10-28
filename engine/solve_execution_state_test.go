package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/skill"
)

type knownCandidateFailure struct{}

func (knownCandidateFailure) Error() string           { return "known independent candidate failed" }
func (knownCandidateFailure) SubAgentRetryable() bool { return false }

func TestSolveKnownCandidateFailureKeepsIndependentCandidate(t *testing.T) {
	for _, cause := range []error{nil, errors.Join(egress.ErrProviderResponseProcessed, context.DeadlineExceeded)} {
		t.Run(map[bool]string{true: "processed_response_local_deadline", false: "known_failure"}[cause != nil], func(t *testing.T) {
			solverCalls := 0
			solver := NewSolveSkill(func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
				if spec.Agent == solverAgentName {
					solverCalls++
					if solverCalls == 1 {
						return SubAgentResult{}, errors.Join(knownCandidateFailure{}, cause)
					}
					return SubAgentResult{Output: "6×7=42。\n答案：42"}, nil
				}
				return SubAgentResult{Output: "VERDICT: UNVERIFIABLE\nCOMPUTED: N/A"}, nil
			}, NewSubAgentRegistry(""))
			result, err := solver.Execute(t.Context(), map[string]any{"problem": "六盒卡片每盒七张，共有多少张？", "self_consistency": 2})
			if err != nil || result == nil || solverCalls != 2 || !strings.Contains(result.Content, "42") {
				t.Fatalf("known failure blocked independent candidate: calls=%d result=%v err=%v", solverCalls, result, err)
			}
		})
	}
}

func TestSolveComputationEvidenceSurvivesDocumentCommandFailure(t *testing.T) {
	for _, reference := range []string{"", "\nEXECUTION_RUN_ID: math-computation"} {
		t.Run(map[bool]string{true: "explicit_execution", false: "unique_matching_computation"}[reference != ""], func(t *testing.T) {
			var observed SubAgentResult
			ctx := WithSubAgentCallInterceptor(t.Context(), SubAgentCallInterceptorFunc(func(ctx context.Context, spec SubAgentSpec, next SubAgentExecFunc) (SubAgentResult, error) {
				result, err := next(ctx, spec)
				if spec.Agent == verifierAgentName {
					observed = result
				}
				return result, err
			}))
			solver := NewSolveSkill(func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
				if spec.Agent == solverAgentName {
					return SubAgentResult{Output: "6×7=42。\n答案：42"}, nil
				}
				stdout := "COMPUTED: 7\n"
				captureCodeExecutionReceipt(ctx, codeExecToolName, &skill.Result{Content: stdout, Data: map[string]any{"run_id": "intermediate-computation", "status": "success", "exit_code": 0, "stdout_bytes": len(stdout)}})
				stdout = "COMPUTED: 42\n"
				captureCodeExecutionReceipt(ctx, codeExecToolName, &skill.Result{Content: stdout, Data: map[string]any{"run_id": "math-computation", "status": "success", "exit_code": 0, "stdout_bytes": len(stdout)}})
				captureCodeExecutionReceipt(ctx, codeExecToolName, &skill.Result{Data: map[string]any{"run_id": "document-formatting", "status": "failed", "exit_code": 1, "error": "document creation failed"}})
				return SubAgentResult{Output: "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: 42" + reference}, nil
			}, NewSubAgentRegistry(""))
			result, err := solver.Execute(ctx, map[string]any{"problem": "六盒卡片每盒七张，共有多少张？", "self_consistency": 1})
			if err != nil || result == nil || result.Metadata["solve_evidence"] != "numeric_exec" || result.Metadata["solve_verification_run_id"] != "math-computation" {
				t.Fatalf("mathematical evidence overwritten: result=%v err=%v", result, err)
			}
			if len(observed.ExecutionReceipts) != 3 || observed.ExecutionReceipts[2].Status != "failed" || observed.ExecutionReceipt == nil || observed.ExecutionReceipt.RunID != "math-computation" {
				t.Fatalf("stage receipts were discarded or selected incorrectly: %+v", observed)
			}
			if strings.Contains(result.Content, "EXECUTION_RUN_ID") {
				t.Fatal("internal execution reference leaked into final body")
			}
		})
	}
}

func TestSolveFailurePreservesObservedResponse(t *testing.T) {
	var observed SubAgentResult
	ctx := WithSubAgentCallInterceptor(t.Context(), SubAgentCallInterceptorFunc(func(ctx context.Context, spec SubAgentSpec, next SubAgentExecFunc) (SubAgentResult, error) {
		result, err := next(ctx, spec)
		observed = result
		return result, err
	}))
	solver := NewSolveSkill(func(ctx context.Context, _ SubAgentSpec) (SubAgentResult, error) {
		captureCodeExecutionReceipt(ctx, codeExecToolName, &skill.Result{Data: map[string]any{"run_id": "failed-computation", "status": "failed", "exit_code": 1, "error": "computation failed"}})
		return SubAgentResult{Output: "Observed but unverified solution.\n答案：42\nEXECUTION_RUN_ID: failed-computation"}, nil
	}, NewSubAgentRegistry(""))
	_, err := solver.Execute(ctx, map[string]any{"problem": "根据图形求面积并说明依据。", "self_consistency": 1})
	if !errors.Is(err, ErrVerificationExecutionFailed) || observed.Output == "" || observed.ExecutionReceipt == nil || len(observed.ExecutionReceipts) != 1 {
		t.Fatalf("failure response lost or promoted: observed=%+v err=%v", observed, err)
	}
}

func TestStructuredTeachingSolutionAssociatesActualComputationWithoutNewJSONFields(t *testing.T) {
	const solution = "7盒，每盒6张。6×7=42张。\n答案：42张"
	raw, _ := jointGuideJSON(t, solution, "用乘法计算七个六。")
	raw = strings.Replace(raw, solveWithParentGuideV1, solveWithTeachingReportV2, 1)
	solver := NewSolveSkill(func(ctx context.Context, _ SubAgentSpec) (SubAgentResult, error) {
		for _, entry := range []struct {
			id, stdout, status string
			exit               int
		}{
			{"intermediate", "COMPUTED: 7\n", "success", 0},
			{"final-math", "COMPUTED: 42张\n", "success", 0},
			{"document", "", "failed", 1},
		} {
			captureCodeExecutionReceipt(ctx, codeExecToolName, &skill.Result{Content: entry.stdout, Data: map[string]any{"run_id": entry.id, "status": entry.status, "exit_code": entry.exit, "stdout_bytes": len(entry.stdout)}})
		}
		return SubAgentResult{Output: raw}, nil
	}, NewSubAgentRegistry(""))
	spec := withVersionedParentGuideGeneration(solverSpec("六盒卡片每盒七张，共有多少张？", "数学", "", "五年级上", ""), solveWithTeachingReportV2, "保留完整解答。")
	result, err := solver.runSolveAgentResult(t.Context(), spec)
	if err != nil || result.Output != solution || result.Generation == nil || result.Generation.RawResponse != raw || len(result.ExecutionReceipts) != 3 || result.ExecutionReceipt == nil || result.ExecutionReceipt.RunID != "final-math" {
		t.Fatalf("structured solution lost its computation association: result=%+v err=%v", result, err)
	}
}

func TestUnassociatedDocumentFailureRemainsUnverified(t *testing.T) {
	solver := NewSolveSkill(func(ctx context.Context, _ SubAgentSpec) (SubAgentResult, error) {
		stdout := "COMPUTED: 7\n"
		captureCodeExecutionReceipt(ctx, codeExecToolName, &skill.Result{Content: stdout, Data: map[string]any{"run_id": "example-only", "status": "success", "exit_code": 0, "stdout_bytes": len(stdout)}})
		captureCodeExecutionReceipt(ctx, codeExecToolName, &skill.Result{Data: map[string]any{"run_id": "document-only", "status": "failed", "exit_code": 1}})
		return SubAgentResult{Output: "The available input does not establish a final numeric answer."}, nil
	}, NewSubAgentRegistry(""))
	result, err := solver.runSolveAgentResult(t.Context(), solverSpec("Explain the relation.", "数学", "", "", ""))
	if err != nil || result.ExecutionReceipt != nil || len(result.ExecutionReceipts) != 2 {
		t.Fatalf("unassociated document failure became whole mathematical failure or proof: result=%+v err=%v", result, err)
	}
}
