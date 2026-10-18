package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/skill"
)

// 原成功程序的 stdout 固定为证据输入，不重新执行原程序或请求模型。
const verifiedDistanceStdout = "已知：2/3小时走2千米\n每份时间：2/3 ÷ 2 = 1/3小时\n每份路程：2 ÷ 2 = 1千米\n1小时包含的份数：1 ÷ 1/3 = 3\n每小时路程：1 × 3 = 3千米\n核验①：总路程÷时间 = 3，数量关系正确\n核验②：时间倒数=3/2；2×倒数=3；与独立结果相同=True\n核验③：3×2/3=2，等于已知路程=True\n范围核验：使用分数意义、分数除法和分数乘法，均在允许范围内\nCOMPUTED: 他每小时行走3千米\n"

func TestVerificationReceiptSeparatesExecutionFromComparability(t *testing.T) {
	const task = "verify the selected source question"
	for _, tc := range []struct {
		name, stdout, digest, computed string
		bytes                          int64
		numeric                        bool
	}{
		{"two parts", "COMPUTED: 2 份\n", "5ea779788dba3a187198a5bdfa1fa141646de3bce82845aab43e7bdcfd239a2e", "2 份", 16, true},
		{"one kilometer", "COMPUTED: 1 千米\n", "2920b3df20e920648b02f2b2601409d434924b1fb4e5c90853d921c5b7ddf5c6", "1 千米", 19, true},
		{"three parts", "COMPUTED: 3 份\n", "01866809a5cd229db5520db62bb32081e93b2d818ca4e2da382017cb816f8fdc", "3 份", 16, true},
		{"complete distance assertion", verifiedDistanceStdout, "180e76f9aa523adfed342b8018f4e4400983ec7a3a763b921583412f2e93fbd3", "他每小时行走3千米", 468, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest := sha256.Sum256([]byte(tc.stdout))
			if int64(len(tc.stdout)) != tc.bytes || tc.digest != "" && hex.EncodeToString(digest[:]) != tc.digest {
				t.Fatal("recorded stdout fixture changed")
			}
			receipt := standaloneVerifierTestReceipt(task, tc.stdout)
			output, complete := receipt.computedOutput(task)
			if !complete || output != tc.computed || receipt.Stdout != tc.stdout {
				t.Fatalf("successful complete stdout was rejected or rewritten: %q complete=%t", output, complete)
			}
			_, numeric := receipt.computed(task)
			if numeric != tc.numeric {
				t.Fatalf("numeric=%t want=%t; execution success must not infer a number from a sentence", numeric, tc.numeric)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*CodeExecutionReceipt)
	}{
		{"other input", func(r *CodeExecutionReceipt) { r.InputDigest = executionInputDigest("other question") }},
		{"missing run", func(r *CodeExecutionReceipt) { r.RunID = "" }},
		{"failed", func(r *CodeExecutionReceipt) { r.Status = "failed" }},
		{"nonzero exit", func(r *CodeExecutionReceipt) { r.ExitCode = 1 }},
		{"timeout", func(r *CodeExecutionReceipt) { r.Timeout = true }},
		{"missing runtime", func(r *CodeExecutionReceipt) { r.RuntimeMissing = true }},
		{"error report", func(r *CodeExecutionReceipt) { r.Error = "execution failed" }},
		{"truncated", func(r *CodeExecutionReceipt) { r.Truncated = true }},
		{"stdout truncated", func(r *CodeExecutionReceipt) { r.StdoutTruncated = true }},
		{"stderr truncated", func(r *CodeExecutionReceipt) { r.StderrTruncated = true }},
		{"incomplete bytes", func(r *CodeExecutionReceipt) { r.StdoutBytes++ }},
		{"two results", func(r *CodeExecutionReceipt) { r.Stdout += "COMPUTED: 3 份\n"; r.StdoutBytes = int64(len(r.Stdout)) }},
		{"empty result", func(r *CodeExecutionReceipt) { r.Stdout = "COMPUTED: \n"; r.StdoutBytes = int64(len(r.Stdout)) }},
		{"result on another line", func(r *CodeExecutionReceipt) { r.Stdout = "COMPUTED:\n2 份\n"; r.StdoutBytes = int64(len(r.Stdout)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt := standaloneVerifierTestReceipt(task, "COMPUTED: 2 份\n")
			tc.mutate(receipt)
			if output, ok := receipt.computedOutput(task); ok || output != "" {
				t.Fatalf("invalid execution acquired a complete output: %q", output)
			}
		})
	}
	var missing *CodeExecutionReceipt
	if _, ok := missing.computedOutput(task); ok {
		t.Fatal("missing receipt became a successful execution")
	}
}

func TestVerificationPrintedPartSlotAndLengthUnits(t *testing.T) {
	for _, tc := range []struct {
		name, problem, computed, candidate string
		equal, comparable                  bool
	}{
		{"known parts", "已知（　）份是2千米，可以先求出这样的1份是多少。", "2 份", "2", true, true},
		{"requested parts", "再求这样的（　）份是多少。", "3 份", "3", true, true},
		{"incorrect count", "再求这样的（　）份是多少。", "3 份", "2", false, true},
		{"ascii blank", "已知( )份是2千米。", "2份", "2", true, true},
		{"no explicit slot", "求需要的份数。", "2份", "2", false, false},
		{"two explicit slots", "已知（ ）份，再求（ ）份。", "2份", "2", false, false},
		{"another fraction slot", `已知（ ）份，分数为\square/2。`, "2份", "2", false, false},
		{"mismatched blank", "已知( ）份是2千米。", "2份", "2", false, false},
		{"different dimension", "已知（ ）份是2千米。", "2米", "2", false, false},
		{"candidate unit cannot be replaced", "已知（ ）份是2千米。", "2份", "2千米", false, false},
		{"computed unit cannot be guessed", "已知（ ）份是2千米。", "2", "2份", false, false},
		{"kilometers alias", "", "1千米", "1公里", true, true},
		{"latin kilometers", "", "1 km", "1千米", true, true},
		{"different kilometer value", "", "1公里", "2km", false, true},
		{"no implicit length conversion", "", "1千米", "1000米", false, false},
		{"no lost compound fields", "", "1千米", "一份（1/3 小时）走 1 千米；2÷2＝2×1/2，方框从上到下填 1、2", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			equal, comparable := verificationAnswersEqual(tc.problem, tc.computed, tc.candidate)
			if equal != tc.equal || comparable != tc.comparable {
				t.Fatalf("equal=%t comparable=%t want=%t/%t", equal, comparable, tc.equal, tc.comparable)
			}
			input := &verificationInput{Problem: tc.problem, Solution: "完整解法", Candidate: tc.candidate}
			stdout := "COMPUTED: " + tc.computed + "\n"
			judgment := verificationProgramJudgment(input, standaloneVerifierTestProgram("IN_SCOPE", true), standaloneVerifierTestReceipt("task", stdout), "task", "original provider response")
			verdict, computed := parseVerdict(judgment)
			want := verdictUnverifiable
			if tc.comparable {
				want = verdictDisagree
				if tc.equal {
					want = verdictAgree
				}
			}
			if verdict != want || computed != tc.computed || input.Candidate != tc.candidate {
				t.Fatalf("judgment lost original result or inferred unsupported comparison: %s", judgment)
			}
		})
	}
}

// Provider 与执行器仅回放已知边界；正式 Process/工具捕获/SQLite 回复保存保持真实。
type recordedVerificationCodeExec struct {
	*syncChildToolPolicySkill
	stdout  string
	program string
	calls   int
}

func (s *recordedVerificationCodeExec) Execute(_ context.Context, args map[string]any) (*skill.Result, error) {
	s.calls++
	if args["code"] != s.program {
		return nil, errors.New("recorded program identity changed")
	}
	return &skill.Result{Content: s.stdout, Data: &CodeExecutionReceipt{RunID: "recorded-execution", Status: "success", StdoutBytes: int64(len(s.stdout))}}, nil
}

func TestStandaloneVerificationPersistsUncomparableSuccessfulOutput(t *testing.T) {
	program := standaloneVerifierTestProgram("IN_SCOPE", true)
	program.Program = "distance_km = 1\nprint(f'COMPUTED: {distance_km} 千米')"
	program.Checks[0].Step, program.Checks[0].Reason = "求得1千米", "独立求得长度，候选答案没有给出相同单位。"
	raw, err := json.Marshal(program)
	if err != nil {
		t.Fatal(err)
	}
	provider := &standaloneVerifierTestProvider{response: string(raw)}
	eng := newEngineWithProvider(t, provider)
	eng.cfg.LLM.Tools.Enabled = "on"
	execution := &recordedVerificationCodeExec{syncChildToolPolicySkill: &syncChildToolPolicySkill{name: codeExecToolName}, stdout: "COMPUTED: 1 千米\n", program: program.Program}
	registry := skill.NewRegistry()
	if err := registry.Register(execution); err != nil {
		t.Fatal(err)
	}
	eng.SetToolCollector(NewToolCollector(registry, nil, 40))
	eng.SetToolExecutor(NewToolExecutor(registry, nil))
	var reply *adapter.Reply
	var calls int
	solve := NewSolveSkill(func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
		calls++
		if spec.Agent != verifierAgentName {
			return SubAgentResult{}, errors.New("successful solver must not be called again")
		}
		msg := &adapter.Message{ID: "uncomparable-verifier-output", Platform: adapter.PlatformAPI, UserID: "test-user", Content: spec.Task,
			Metadata: map[string]string{"memory": "off", "knowledge": "off"}}
		ApplySpecToMessage(msg, spec)
		var err error
		reply, err = eng.Process(ctx, msg)
		if err != nil {
			return SubAgentResult{}, err
		}
		return SubAgentResult{Output: reply.Content, SessionID: msg.SessionID}, nil
	}, nil)
	verdict, computed, grounded, receipt := solve.verifySolutionWithReceipt(t.Context(), "计算原题中的长度。", "原成功求解的完整解法", "2", "小学数学")
	if verdict != verdictUnverifiable || computed != "1 千米" || grounded || receipt == nil || receipt.Stdout != execution.stdout || calls != 1 || execution.calls != 1 || reply == nil {
		t.Fatalf("valid uncomparable output failed, retried or gained numeric proof: verdict=%v computed=%q grounded=%t calls=%d executions=%d receipt=%+v", verdict, computed, grounded, calls, execution.calls, receipt)
	}
	stored, err := eng.store.GetMessage(t.Context(), reply.Metadata["backend_message_id"])
	if err != nil {
		t.Fatal(err)
	}
	if stored.Content != reply.Content || !strings.Contains(stored.Content, "VERIFIER_RESPONSE_JSON:") {
		t.Fatal("original verification response was not persisted")
	}
	var metadata struct {
		ToolCalls []adapter.ToolCall `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(stored.Metadata), &metadata); err != nil || len(metadata.ToolCalls) != 1 || metadata.ToolCalls[0].Result != execution.stdout || metadata.ToolCalls[0].Status != "success" {
		t.Fatalf("successful actual output was not persisted: %+v err=%v", metadata, err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.requests) != 1 || provider.streamCalls != 0 {
		t.Fatal("semantic incomparability triggered an extra model request")
	}
}

func TestVerificationCompleteCandidateKeepsCompoundAuditAndRecovery(t *testing.T) {
	const candidate = "一份（1/3 小时）走 1 千米；2÷2＝2×1/2，方框从上到下填 1、2"
	const problem = "也就是2÷2＝2×□/□，求一份的路程和两个方框。"
	for _, answer := range []string{candidate, "他每小时行走3千米", "1千米；方框填1、2", "2=2×1/2"} {
		spec := verifierSpecWithSolution(problem, "已保存的完整求解", answer, "小学数学")
		ctx := context.WithValue(withSolveGrant(t.Context()), verificationInputKey{}, spec.verification)
		msg := &adapter.Message{Content: spec.Task, Metadata: map[string]string{"memory": "off", "knowledge": "off"}}
		ApplySpecToMessage(msg, spec)
		tools := []llm.ToolDefinition{llm.NewToolDefinition(codeExecToolName, "recorded code execution", &llm.Schema{Type: "object"})}
		if _, standalone := standaloneVerificationInput(ctx, msg, tools); standalone {
			t.Fatalf("partial numeric extraction routed complete candidate to standalone: %q", answer)
		}
	}
	var calls int
	solve := NewSolveSkill(func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
		calls++
		if spec.Agent != verifierAgentName || !strings.Contains(spec.Task, candidate) || !strings.Contains(spec.Task, "已保存的完整求解") {
			return SubAgentResult{}, errors.New("recovery regenerated or truncated the successful solution")
		}
		stdout := "COMPUTED: 1 千米\n"
		captureCodeExecutionReceipt(ctx, codeExecToolName, &skill.Result{Content: stdout, Data: standaloneVerifierTestReceipt(spec.Task, stdout)})
		return SubAgentResult{Output: "VERDICT: UNVERIFIABLE\nPROCESS: VALID\nCOMPUTED: 1 千米\n说明：此执行仅证明一份的路程，未证明两个方框的完整答案。"}, nil
	}, nil)
	verdict, computed, grounded, receipt := solve.verifySolutionWithReceipt(t.Context(), problem, "已保存的完整求解", candidate, "小学数学")
	if verdict != verdictUnverifiable || computed != "1 千米" || grounded || receipt == nil || calls != 1 {
		t.Fatalf("one quantity upgraded an entire compound answer or repeated recovery: verdict=%v computed=%q grounded=%t receipt=%+v calls=%d", verdict, computed, grounded, receipt, calls)
	}
	unknown := errors.New("existing request outcome is unknown")
	unknownCalls := 0
	unknownSolve := NewSolveSkill(func(context.Context, SubAgentSpec) (SubAgentResult, error) {
		unknownCalls++
		return SubAgentResult{}, unknown
	}, nil)
	_, _, _, _, _, err := unknownSolve.verifySolutionWithGuides(t.Context(), problem, "已保存的完整求解", candidate, "小学数学", nil)
	if !errors.Is(err, unknown) || unknownCalls != 1 {
		t.Fatalf("unknown failure was masked or repeated: err=%v calls=%d", err, unknownCalls)
	}
}
