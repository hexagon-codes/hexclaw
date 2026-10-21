package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
	"github.com/hexagon-codes/toolkit/os/sandbox"
)

func TestStandaloneVerificationSyntaxRepairUsesRealExecution(t *testing.T) {
	for _, variant := range []string{"repaired", "still_invalid", "preserve_invalid_audit"} {
		t.Run(variant, func(t *testing.T) {
			first := standaloneVerifierTestProgram("IN_SCOPE", variant != "preserve_invalid_audit")
			first.Program = "from fractions import Fraction\n\n value = Fraction(6) * 7\n print(f'COMPUTED: {value}张')\n"
			second := standaloneVerifierTestProgram("IN_SCOPE", true)
			second.Program = "from fractions import Fraction\nvalue = Fraction(6) * 7\nprint(f'COMPUTED: {value}张')\n"
			if variant == "still_invalid" {
				second.Program = first.Program
			}
			firstRaw, _ := json.Marshal(first)
			secondRaw, _ := json.Marshal(second)
			provider := &standaloneVerifierTestProvider{responses: []string{string(firstRaw), string(secondRaw)}}
			eng := newEngineWithProvider(t, provider)
			eng.cfg.LLM.Tools.Enabled = "on"
			cfg := sandbox.Config{Workspace: t.TempDir(), Timeout: 30, RequiredCapabilities: sandbox.UntrustedCodeIsolationCapabilities}
			sb, err := sandbox.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			tool := &standaloneVerifierCountingCodeExec{CodeExecSkill: builtin.NewCodeExecSkill(sb, cfg)}
			registry := skill.NewRegistry()
			if err := registry.Register(tool); err != nil {
				t.Fatal(err)
			}
			eng.SetToolCollector(NewToolCollector(registry, nil, 40))
			eng.SetToolExecutor(NewToolExecutor(registry, nil))
			spec := verifierSpecWithSolution("每盒6张卡片，7盒共有多少张？", "6×7=42张。", "42张", "小学乘法")
			ctx := context.WithValue(withSolveGrant(t.Context()), verificationInputKey{}, spec.verification)
			ctx = context.WithValue(ctx, codeExecutionReceiptKey{}, &codeExecutionReceiptSink{inputDigest: executionInputDigest(spec.Task)})
			msg := &adapter.Message{ID: variant, Platform: adapter.PlatformAPI, UserID: "test-user", Content: spec.Task, Metadata: map[string]string{"memory": "off", "knowledge": "off"}}
			ApplySpecToMessage(msg, spec)
			reply, err := eng.Process(ctx, msg)
			provider.mu.Lock()
			count := len(provider.requests)
			provider.mu.Unlock()
			if count != 2 || tool.calls.Load() != 2 {
				t.Fatalf("repair bound changed: model=%d executions=%d err=%v", count, tool.calls.Load(), err)
			}
			if variant == "still_invalid" {
				if err == nil || reply != nil {
					t.Fatal("second syntax failure became successful")
				}
				return
			}
			if err != nil || reply == nil || len(reply.ToolCalls) != 2 || reply.ToolCalls[0].Status != "error" || reply.ToolCalls[1].Status != "success" || !strings.Contains(reply.Content, "VERIFIER_REPAIR_SOURCE_JSON:") {
				t.Fatalf("real repair evidence lost: reply=%+v err=%v", reply, err)
			}
			verdict, _ := parseVerdict(reply.Content)
			want := verdictAgree
			if variant == "preserve_invalid_audit" {
				want = verdictDisagree
			}
			if verdict != want {
				t.Fatalf("audit changed after syntax repair: verdict=%s", verdictString(verdict))
			}
		})
	}
}

func TestStandaloneVerificationSyntaxRepairRequiresKnownCompilerFailure(t *testing.T) {
	const task = "current input"
	stderr := "  File \"snippet.py\", line 2\n    value = 1\nIndentationError: unexpected indent\n"
	for _, variant := range []string{"known", "missing", "unknown", "timeout", "runtime_missing", "truncated", "wrong_input", "partial_stderr", "runtime_error", "stdout_present"} {
		t.Run(variant, func(t *testing.T) {
			r := &CodeExecutionReceipt{InputDigest: executionInputDigest(task), RunID: "real-run", Status: "failed", ExitCode: 1, StderrBytes: int64(len(stderr))}
			output := "[stderr]\n" + stderr + "[exit code 1]\n"
			switch variant {
			case "missing":
				r = nil
			case "unknown":
				r.Status = "outcome_unknown"
			case "timeout":
				r.Timeout = true
			case "runtime_missing":
				r.RuntimeMissing = true
			case "truncated":
				r.StderrTruncated = true
			case "wrong_input":
				r.InputDigest = executionInputDigest("different input")
			case "partial_stderr":
				r.StderrBytes = 9999
			case "runtime_error":
				output = strings.ReplaceAll(output, "IndentationError", "RuntimeError")
			case "stdout_present":
				r.StdoutBytes = 10
			}
			if _, ok := verificationSyntaxDiagnostic(r, task, output); ok != (variant == "known") {
				t.Fatalf("repairable=%v for %s", ok, variant)
			}
		})
	}
}
