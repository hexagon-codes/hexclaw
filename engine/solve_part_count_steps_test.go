package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/skill"
)

func TestPartCountExecutedStepRequiresCompleteSameTaskEvidence(t *testing.T) {
	const task = "frozen current part-count problem"
	const actualStdout = "parts = 2\ncheck: 2 * 1/3 = 2/3\n"
	for _, tc := range []struct {
		name, output, stdout, want string
		mutate                     func(*SubAgentResult)
	}{
		{name: "actual_solver_check", output: "答案：2", stdout: actualStdout, want: "2 * 1/3 = 2/3\n\n答案：2"},
		{name: "fraction_operand", output: "答案：1/2份", stdout: "check: (1/2) * (2/3) = 1/3\n", want: "(1/2) * (2/3) = 1/3\n\n答案：1/2份"},
		{name: "missing_receipt", output: "答案：2", stdout: actualStdout, mutate: func(r *SubAgentResult) { r.ExecutionReceipt = nil }},
		{name: "another_task", output: "答案：2", stdout: actualStdout, mutate: func(r *SubAgentResult) { r.ExecutionReceipt.InputDigest = executionInputDigest("other problem") }},
		{name: "truncated", output: "答案：2", stdout: actualStdout, mutate: func(r *SubAgentResult) { r.ExecutionReceipt.StdoutTruncated = true }},
		{name: "incomplete_bytes", output: "答案：2", stdout: actualStdout, mutate: func(r *SubAgentResult) { r.ExecutionReceipt.StdoutBytes++ }},
		{name: "incorrect_equation", output: "答案：2", stdout: "check: 2 * 1/3 = 1/3\n"},
		{name: "two_equations", output: "答案：2", stdout: actualStdout + "check: 2 * 1/4 = 1/2\n"},
		{name: "unrelated_result_numerator", output: "答案：2", stdout: "check: 1/3 + 1/3 = 2/3\n"},
		{name: "answer_only_identity", output: "答案：2", stdout: "check: 2 = 2\n"},
		{name: "incomplete_check", output: "答案：2", stdout: "check: 2 * 1/3 = 2/3 ...\n"},
		{name: "existing_method", output: "两段各表示一份。\n答案：2", stdout: actualStdout},
		{name: "joint_guide_source", output: "答案：2", stdout: actualStdout, mutate: func(r *SubAgentResult) {
			r.Generation = &SolveGenerationAttachment{OutputVersion: solveWithParentGuideV1, RawResponse: "frozen envelope"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := SubAgentResult{Output: tc.output, SessionID: "recorded-solver-session", ExecutionReceipt: &CodeExecutionReceipt{
				InputDigest: executionInputDigest(task), RunID: "recorded-solver-execution", Status: "success",
				Stdout: tc.stdout, StdoutBytes: int64(len(tc.stdout)),
			}}
			if tc.mutate != nil {
				tc.mutate(&original)
			}
			before, _ := json.Marshal(original)
			got := projectPartCountExecutedStep(task, original)
			want := tc.want
			if want == "" {
				want = tc.output
			}
			if got.Output != want || got.SessionID != original.SessionID || !reflect.DeepEqual(got.ExecutionReceipt, original.ExecutionReceipt) || !reflect.DeepEqual(got.Generation, original.Generation) {
				t.Fatalf("executed step/source mismatch: got=%+v want=%q", got, want)
			}
			after, _ := json.Marshal(original)
			if string(before) != string(after) {
				t.Fatal("projection rewrote the source result or execution receipt")
			}
		})
	}
}

func TestPartCountExecutedStepIsFrozenBeforeIndependentVerification(t *testing.T) {
	problem := partCountSlotEngineProblem(t, "已知（ ）份是2千米。")
	const equation = "2 * 1/3 = 2/3"
	var solverCalls, verifierCalls int
	var persistedSolver SubAgentResult
	var verifierTask string
	execute := func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
		var program, output string
		switch spec.Agent {
		case solverAgentName:
			solverCalls++
			program = "from fractions import Fraction as F\nparts = F(2, 3) / F(1, 3)\nassert parts * F(1, 3) == F(2, 3)\nprint('parts =', parts)\nprint('check: 2 * 1/3 = 2/3')\n"
			output = "答案：2"
		case verifierAgentName:
			verifierCalls++
			verifierTask = spec.Task
			program = "from fractions import Fraction as F\nvalue = F(2, 3) / F(1, 3)\nassert value == 2\nprint('COMPUTED: 2 份')\n"
			output = "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: 2 份"
		default:
			return SubAgentResult{}, fmt.Errorf("unexpected stage %s", spec.Agent)
		}
		// 真实 Python 只验证本地执行与回执传递；不冒称 OS 沙箱或真实模型验收。
		stdout, err := exec.CommandContext(ctx, "python3", "-c", program).Output()
		if err != nil {
			return SubAgentResult{}, err
		}
		captureCodeExecutionReceipt(ctx, codeExecToolName, &skill.Result{Content: string(stdout), Data: CodeExecutionReceipt{
			RunID: "local-python-" + spec.Agent, Status: "success", StdoutBytes: int64(len(stdout)),
		}})
		return SubAgentResult{Output: output, SessionID: "local-session-" + spec.Agent}, nil
	}
	ctx := WithSubAgentCallInterceptor(t.Context(), SubAgentCallInterceptorFunc(func(ctx context.Context, spec SubAgentSpec, next SubAgentExecFunc) (SubAgentResult, error) {
		result, err := next(ctx, spec)
		if spec.Agent == solverAgentName && err == nil {
			persistedSolver = result
		}
		return result, err
	}))
	result, err := NewSolveSkill(execute, nil).Execute(ctx, map[string]any{"problem": problem, "subject": "数学", "method_diversity": false, "self_consistency": 1})
	if err != nil || result == nil || solverCalls != 1 || verifierCalls != 1 || result.Metadata["solve_evidence"] != "numeric_exec" || result.Metadata["solve_verification_run_id"] == "" {
		t.Fatalf("scoped solve changed its calls or evidence: result=%+v err=%v calls=%d/%d", result, err, solverCalls, verifierCalls)
	}
	if persistedSolver.Output != equation+"\n\n答案：2" || persistedSolver.ExecutionReceipt == nil || persistedSolver.ExecutionReceipt.Stdout != "parts = 2\ncheck: "+equation+"\n" ||
		!strings.Contains(verifierTask, "待校验完整解法（必须逐步审计）：\n"+persistedSolver.Output) || !strings.Contains(result.Content, equation) {
		t.Fatalf("real equation did not reach persistence and independent audit: persisted=%+v verify=%s result=%+v", persistedSolver, verifierTask, result)
	}

	// 旧持久结果直接返回，不经过实际执行包装，因此不追溯补写方法。
	legacy := persistedSolver
	legacy.Output = "答案：2"
	legacyCtx := context.WithValue(t.Context(), partCountSlotSolveScopeKey{}, true)
	legacyCtx = WithSubAgentCallInterceptor(legacyCtx, SubAgentCallInterceptorFunc(func(context.Context, SubAgentSpec, SubAgentExecFunc) (SubAgentResult, error) { return legacy, nil }))
	replayed, err := NewSolveSkill(execute, nil).runSolveAgentResult(legacyCtx, solverSpec(problem, "数学", "", "", ""))
	if err != nil || !reflect.DeepEqual(replayed, legacy) || solverCalls != 1 || verifierCalls != 1 {
		t.Fatalf("legacy replay was reinterpreted or executed again: %+v %v", replayed, err)
	}
}
