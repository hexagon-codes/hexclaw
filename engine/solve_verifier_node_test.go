package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
	"github.com/hexagon-codes/toolkit/os/sandbox"
)

func TestStandaloneVerificationRequiresTrustedInputAndCodeExec(t *testing.T) {
	for _, tc := range []struct {
		name      string
		grant     bool
		withInput bool
		candidate string
		toolName  string
		want      bool
	}{
		{"trusted-verifier", true, true, "42张", codeExecToolName, true},
		{"metadata-without-grant", false, true, "42张", codeExecToolName, false},
		{"missing-internal-input", true, false, "42张", codeExecToolName, false},
		{"missing-code-exec", true, true, "42张", "ordinary_tool", false},
		{"non-numeric-answer", true, true, "无法确定", codeExecToolName, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := &verificationInput{Problem: "每盒6张卡片，7盒共有多少张？", Candidate: tc.candidate}
			ctx := context.Background()
			if tc.grant {
				ctx = withSolveGrant(ctx)
			}
			if tc.withInput {
				ctx = context.WithValue(ctx, verificationInputKey{}, input)
			}
			msg := &adapter.Message{Metadata: map[string]string{"memory": "off", "knowledge": "off"}}
			ApplySpecToMessage(msg, verifierSpec(input.Problem, input.Candidate, ""))
			tools := []llm.ToolDefinition{llm.NewToolDefinition(tc.toolName, "fixture tool", &llm.Schema{Type: "object"})}
			got, ok := standaloneVerificationInput(ctx, msg, tools)
			if ok != tc.want || (ok && got != input) || (!ok && got != nil) {
				t.Fatalf("eligible=%v input=%p, want eligible=%v", ok, got, tc.want)
			}
		})
	}
}

// 过程和课程否决经最终 solve 判定后仍须保留，不能被相同数值纠偏。
func TestStandaloneVerificationPreservesProcessAndScope(t *testing.T) {
	for _, tc := range []struct {
		name, scope, solution string
		valid                 bool
		want                  verifyVerdict
	}{
		{"invalid-process-with-equal-answer", "IN_SCOPE", "6+7=42张。", false, verdictDisagree},
		{"out-of-scope-with-equal-answer", "OUT_OF_SCOPE", "设共有x张，x/7=6，所以x=42。", true, verdictOutOfScope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			var judgment string
			execute := func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
				calls++
				program := standaloneVerifierTestProgram(tc.scope, tc.valid)
				program.Checks[0].Step = tc.solution
				receipt := standaloneVerifierTestReceipt(spec.Task, "COMPUTED: 42张\n")
				captureCodeExecutionReceipt(ctx, codeExecToolName, &skill.Result{Content: receipt.Stdout, Data: receipt})
				judgment = verificationProgramJudgment(spec.verification, program, receipt, spec.Task, "original response")
				return SubAgentResult{Output: judgment}, nil
			}
			verdict, computed, _, receipt := NewSolveSkill(execute, nil).verifySolutionWithReceipt(
				t.Context(), "每盒6张卡片，7盒共有多少张？", tc.solution, "42张", "小学乘法",
			)
			if verdict != tc.want || computed != "42张" || receipt == nil || calls != 1 {
				t.Fatalf("verdict=%s computed=%q receipt=%+v calls=%d, want verdict=%s and one call",
					verdictString(verdict), computed, receipt, calls, verdictString(tc.want))
			}
			if tc.valid && !strings.Contains(judgment, "PROCESS: VALID") || !tc.valid && !strings.Contains(judgment, "PROCESS: INVALID") {
				t.Fatalf("process audit was lost: %s", judgment)
			}
		})
	}
}

// 原始程序文字中的正确答案不能补足缺失或截断的工具回执。
func TestStandaloneVerificationRejectsUnusableEvidence(t *testing.T) {
	const task = "verify cards"
	input := &verificationInput{Problem: "每盒6张卡片，7盒共有多少张？", Solution: "6×7=42张。", Candidate: "42张"}
	program := standaloneVerifierTestProgram("IN_SCOPE", true)
	for _, tc := range []struct {
		name    string
		stdout  string
		missing bool
		mutate  func(*CodeExecutionReceipt)
		want    verifyVerdict
		value   string
	}{
		{name: "wrong-value", stdout: "COMPUTED: 43张\n", want: verdictDisagree, value: "43张"},
		{name: "wrong-unit", stdout: "COMPUTED: 42米\n", want: verdictUnverifiable, value: "42米"},
		{name: "missing-receipt", missing: true, want: verdictUnverifiable, value: ""},
		{name: "truncated-receipt", stdout: "COMPUTED: 42张\n", mutate: func(r *CodeExecutionReceipt) { r.StdoutTruncated = true }, want: verdictUnverifiable, value: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var receipt *CodeExecutionReceipt
			if !tc.missing {
				receipt = standaloneVerifierTestReceipt(task, tc.stdout)
				if tc.mutate != nil {
					tc.mutate(receipt)
				}
			}
			judgment := verificationProgramJudgment(input, program, receipt, task, `{"program":"print('COMPUTED: 42张')"}`)
			verdict, computed := parseVerdict(judgment)
			if verdict != tc.want || computed != tc.value {
				t.Fatalf("verdict=%s computed=%q, want %s %q: %s", verdictString(verdict), computed, verdictString(tc.want), tc.value, judgment)
			}
		})
	}
}

func TestStandaloneVerificationIdentifiesProcessedResponseFailures(t *testing.T) {
	raw, err := json.Marshal(standaloneVerifierTestProgram("IN_SCOPE", true))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, response, reason string
	}{
		{"program-parse-failure", "not JSON", "verification program is invalid"},
		{"missing-execution-receipt", string(raw), "verification execution receipt is invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &standaloneVerifierTestProvider{response: tc.response}
			eng := newEngineWithProvider(t, provider)
			eng.cfg.LLM.Tools.Enabled = "on"
			registry := skill.NewRegistry()
			// 无结构化执行报告的既有技能替身只用于回执失败边界。
			if err := registry.Register(&syncChildToolPolicySkill{name: codeExecToolName}); err != nil {
				t.Fatal(err)
			}
			eng.SetToolCollector(NewToolCollector(registry, nil, 40))
			eng.SetToolExecutor(NewToolExecutor(registry, nil))
			spec := verifierSpecWithSolution("每盒6张卡片，7盒共有多少张？", "6×7=42张。", "42张", "小学乘法")
			ctx := context.WithValue(withSolveGrant(t.Context()), verificationInputKey{}, spec.verification)
			ctx = context.WithValue(ctx, codeExecutionReceiptKey{}, &codeExecutionReceiptSink{inputDigest: executionInputDigest(spec.Task)})
			msg := &adapter.Message{
				ID: tc.name, Platform: adapter.PlatformAPI, UserID: "test-user", Content: spec.Task,
				Metadata: map[string]string{"memory": "off", "knowledge": "off"},
			}
			ApplySpecToMessage(msg, spec)
			reply, err := eng.Process(ctx, msg)
			if reply != nil || !errors.Is(err, egress.ErrProviderResponseProcessed) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("local failure lost processed-response identity: reply=%+v err=%v", reply, err)
			}
			provider.mu.Lock()
			calls := len(provider.requests)
			provider.mu.Unlock()
			if calls != 1 {
				t.Fatalf("complete calls=%d, want one responded request", calls)
			}
		})
	}
}

func TestStandaloneVerificationExecutesOnceAndPersistsEvidence(t *testing.T) {
	program := standaloneVerifierTestProgram("IN_SCOPE", true)
	raw, err := json.Marshal(program)
	if err != nil {
		t.Fatal(err)
	}
	provider := &standaloneVerifierTestProvider{response: string(raw)}
	eng := newEngineWithProvider(t, provider)
	eng.cfg.LLM.Tools.Enabled = "on"
	registry := skill.NewRegistry()
	sbCfg := sandbox.Config{
		Workspace:            t.TempDir(),
		Timeout:              30,
		RequiredCapabilities: sandbox.UntrustedCodeIsolationCapabilities,
	}
	sb, err := sandbox.New(sbCfg)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	codeExec := &standaloneVerifierCountingCodeExec{CodeExecSkill: builtin.NewCodeExecSkill(sb, sbCfg)}
	if err := registry.Register(codeExec); err != nil {
		t.Fatalf("register code_exec: %v", err)
	}
	eng.SetToolCollector(NewToolCollector(registry, nil, 40))
	eng.SetToolExecutor(NewToolExecutor(registry, nil))

	var reply *adapter.Reply
	var task string
	execute := func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
		task = spec.Task
		msg := &adapter.Message{
			ID: "standalone-verifier-component", Platform: adapter.PlatformAPI, UserID: "test-user",
			Content: spec.Task, Metadata: map[string]string{"memory": "off", "knowledge": "off"},
		}
		ApplySpecToMessage(msg, spec)
		var callErr error
		reply, callErr = eng.Process(ctx, msg)
		if callErr != nil {
			return SubAgentResult{}, callErr
		}
		return SubAgentResult{Output: reply.Content, SessionID: msg.SessionID}, nil
	}
	verdict, computed, grounded, receipt := NewSolveSkill(execute, nil).verifySolutionWithReceipt(
		t.Context(), "每盒6张卡片，7盒共有多少张？", "6×7=42张。", "42张", "小学乘法",
	)
	if verdict != verdictAgree || computed != "42张" || !grounded || reply == nil {
		t.Fatalf("verdict=%s computed=%q grounded=%v reply=%+v", verdictString(verdict), computed, grounded, reply)
	}
	if receipt == nil || receipt.RunID == "" || receipt.InputDigest != executionInputDigest(task) || receipt.Stdout != "COMPUTED: 42张\n" {
		t.Fatalf("actual execution receipt was lost or mismatched: %+v", receipt)
	}
	if codeExec.calls.Load() != 1 {
		t.Fatalf("code_exec calls=%d, want one actual execution", codeExec.calls.Load())
	}
	provider.mu.Lock()
	requests := append([]hexagon.CompletionRequest(nil), provider.requests...)
	streamCalls := provider.streamCalls
	provider.mu.Unlock()
	if len(requests) != 1 || streamCalls != 0 {
		t.Fatalf("complete calls=%d stream calls=%d, want one request", len(requests), streamCalls)
	}
	req := requests[0]
	if len(req.Tools) != 0 || req.ToolChoice != nil || req.ResponseFormat == nil || req.ResponseFormat.Type != "json_object" {
		t.Fatalf("standalone request retained tool round-trip or lost JSON mode: %+v", req)
	}
	last := req.Messages[len(req.Messages)-1]
	for _, field := range []string{`"problem":"每盒6张卡片，7盒共有多少张？"`, `"solution":"6×7=42张。"`, `"candidate":"42张"`, `"curriculum":"小学乘法"`} {
		if !strings.Contains(last.Content, field) {
			t.Fatalf("structured verification input missing %s: %s", field, last.Content)
		}
	}
	var original string
	_, originalJSON, found := strings.Cut(reply.Content, "VERIFIER_RESPONSE_JSON: ")
	if !found {
		t.Fatalf("original model response missing: %s", reply.Content)
	}
	if err := json.Unmarshal([]byte(originalJSON), &original); err != nil || original != string(raw) {
		t.Fatalf("original model response was not retained: original=%q err=%v", original, err)
	}
	stored, err := eng.store.GetMessage(t.Context(), reply.Metadata["backend_message_id"])
	if err != nil {
		t.Fatalf("read SQLite assistant message: %v", err)
	}
	if stored.Content != reply.Content {
		t.Fatalf("SQLite lost verification response: %s", stored.Content)
	}
	var meta struct {
		ToolCalls []adapter.ToolCall `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(stored.Metadata), &meta); err != nil {
		t.Fatalf("decode SQLite tool evidence: %v", err)
	}
	if len(meta.ToolCalls) != 1 || meta.ToolCalls[0].Name != codeExecToolName || meta.ToolCalls[0].Status != "success" ||
		!strings.HasPrefix(meta.ToolCalls[0].Result, "COMPUTED: 42张\n") {
		t.Fatalf("SQLite lost actual tool output: %+v", meta.ToolCalls)
	}
	var args struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(meta.ToolCalls[0].Arguments), &args); err != nil || args.Code != program.Program {
		t.Fatalf("persisted program does not match executed response: code=%q err=%v", args.Code, err)
	}
}

func standaloneVerifierTestProgram(scope string, valid bool) verificationProgram {
	return verificationProgram{
		Program: "boxes = 7\ncards_per_box = 6\nprint(f'COMPUTED: {boxes * cards_per_box}张')",
		Scope:   scope,
		Checks:  []verificationStep{{Step: "6×7=42张", Valid: &valid, Reason: "每盒数量应乘盒数。"}},
		Note:    "检查完整解法和课程范围。",
	}
}

func standaloneVerifierTestReceipt(task, stdout string) *CodeExecutionReceipt {
	return &CodeExecutionReceipt{
		InputDigest: executionInputDigest(task), RunID: "verification-fixture", Status: "success",
		StdoutBytes: int64(len(stdout)), Stdout: stdout,
	}
}

type standaloneVerifierCountingCodeExec struct {
	*builtin.CodeExecSkill
	calls atomic.Int32
}

func (s *standaloneVerifierCountingCodeExec) Execute(ctx context.Context, args map[string]any) (*skill.Result, error) {
	s.calls.Add(1)
	return s.CodeExecSkill.Execute(ctx, args)
}

type standaloneVerifierTestProvider struct {
	mu          sync.Mutex
	response    string
	requests    []hexagon.CompletionRequest
	streamCalls int
}

func (*standaloneVerifierTestProvider) Name() string { return "standalone-verifier-fixture" }

func (p *standaloneVerifierTestProvider) Complete(_ context.Context, req hexagon.CompletionRequest) (*hexagon.CompletionResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	return &hexagon.CompletionResponse{Content: p.response, FinishReason: "stop"}, nil
}

func (p *standaloneVerifierTestProvider) Stream(context.Context, hexagon.CompletionRequest) (*llm.Stream, error) {
	p.mu.Lock()
	p.streamCalls++
	p.mu.Unlock()
	return nil, fmt.Errorf("unexpected streaming request")
}

func (*standaloneVerifierTestProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock-model"}}
}

func (*standaloneVerifierTestProvider) CountTokens([]hexagon.Message) (int, error) { return 0, nil }
