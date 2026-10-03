package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
	"github.com/hexagon-codes/toolkit/os/sandbox"
)

const jointGuideVersion = "solve_with_parent_guide_v1"

var jointGuideTestFields = []string{
	"answer", "full_solution_steps", "grade_level_method", "likely_mistakes",
	"parent_teaching_sequence", "follow_up_questions", "checking_method",
}

func jointGuideArgs() map[string]any {
	return map[string]any{
		"problem":                  "每盒有6张卡片，7盒一共有多少张？",
		"self_consistency":         1,
		"solve_output_version":     jointGuideVersion,
		"parent_teaching_contract": "使用小学乘法解释，每一项都说明理由，不引入方程。",
	}
}

func jointGuideJSON(t *testing.T, solution, method string) (string, map[string]any) {
	t.Helper()
	guide := map[string]any{
		"answer":                   "42张",
		"full_solution_steps":      []string{"每盒6张，7盒就是7个6相加。", "6×7=42张。"},
		"grade_level_method":       method,
		"likely_mistakes":          []string{"不能把每盒张数和盒数相加，6+7=13并不是总张数。"},
		"parent_teaching_sequence": []string{"先让孩子指出一盒有几张，再指出一共有几盒。", "把7个6相加写成6×7。"},
		"follow_up_questions":      []string{"如果有8盒，需要怎样计算？6×8=48张。"},
		"checking_method":          "用6+6+6+6+6+6+6=42张核对。",
	}
	raw, err := json.Marshal(map[string]any{"schema": jointGuideVersion, "solution": solution, "parent_guide": guide})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), guide
}

func jointGuideDigest(solution string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(solution)))
}

func jointGuideAudit(solution, scope string) map[string]any {
	fields := make([]map[string]any, 0, len(jointGuideTestFields))
	for _, field := range jointGuideTestFields {
		fields = append(fields, map[string]any{"field": field, "valid": true, "reason": "与7个6相加及小学乘法范围一致。"})
	}
	return map[string]any{"source_digest": jointGuideDigest(solution), "scope": scope, "fields": fields}
}

func jointGuideJudgment(t *testing.T, verdict, process string, audits ...map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(audits)
	if err != nil {
		t.Fatal(err)
	}
	return "VERDICT: " + verdict + "\nPROCESS: " + process + "\nCOMPUTED: 42张\nSCOPE: IN_SCOPE\nPARENT_GUIDE_AUDITS: " + string(raw)
}

func jointGuideExecution(ctx context.Context, task string) {
	receipt := standaloneVerifierTestReceipt(task, "COMPUTED: 42张\n")
	captureCodeExecutionReceipt(ctx, codeExecToolName, &skill.Result{Content: receipt.Stdout, Data: receipt})
}

func jointGuideMetadata(t *testing.T, result *skill.Result, method, digest, audit string) {
	t.Helper()
	if result == nil {
		t.Fatal("missing solve result")
	}
	if result.Metadata["solve_parent_guide_source_digest"] != digest || result.Metadata["solve_parent_guide_audit"] != audit {
		t.Fatalf("guide source or audit was lost: %v", result.Metadata)
	}
	var guide map[string]any
	if err := json.Unmarshal([]byte(result.Metadata["solve_parent_guide_json"]), &guide); err != nil {
		t.Fatalf("missing guide JSON: %v", err)
	}
	if guide["grade_level_method"] != method {
		t.Fatalf("selected solution received another candidate's guide: %#v", guide)
	}
}

// 联合生成的讲解由同一个独立验算审核，数值证明仍绑定纯解法而非 JSON 外壳。
func TestSolveJointGuideUsesTwoCallsAndPureSolutionProof(t *testing.T) {
	const solution = "7盒是7个6相加。6×7=42张。\n答案：42张"
	const method = "七个相同加数用乘法表示。"
	raw, _ := jointGuideJSON(t, solution, method)
	var agents []string
	execute := func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
		agents = append(agents, spec.Agent)
		switch spec.Agent {
		case solverAgentName:
			if !strings.Contains(spec.Task, jointGuideVersion) || !strings.Contains(spec.Task, "使用小学乘法解释") {
				t.Fatal("solver lost the frozen output version or teaching contract")
			}
			return SubAgentResult{Output: raw}, nil
		case verifierAgentName:
			for _, field := range jointGuideTestFields {
				if !strings.Contains(spec.Task, field) {
					t.Fatalf("independent verifier omitted guide field %q", field)
				}
			}
			jointGuideExecution(ctx, spec.Task)
			return SubAgentResult{Output: jointGuideJudgment(t, "AGREE", "VALID", jointGuideAudit(solution, "IN_SCOPE"))}, nil
		default:
			t.Fatalf("unexpected separate guide call: %s", spec.Agent)
			return SubAgentResult{}, nil
		}
	}
	result, err := NewSolveSkill(execute, nil).Execute(t.Context(), jointGuideArgs())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 || agents[0] != solverAgentName || agents[1] != verifierAgentName {
		t.Fatalf("expected one generation and one independent verification, got %v", agents)
	}
	jointGuideMetadata(t, result, method, jointGuideDigest(solution), "VALID")
	if result.Metadata["solve_evidence"] != "numeric_exec" || result.Metadata["solve_primary_digest"] != jointGuideDigest(solution) {
		t.Fatalf("pure solution execution proof changed: %v", result.Metadata)
	}
	if result.Metadata["solve_primary_digest"] == jointGuideDigest(raw) || strings.Contains(result.Content, `"parent_guide"`) {
		t.Fatal("raw generation JSON leaked into the solution or numeric proof")
	}
}

// 数值节点的真实程序执行与讲解 JSON 审计必须沿同一响应保留，不能多调模型总结。
func TestSolveJointGuideNumericNodePreservesAudit(t *testing.T) {
	const solution = "6×7=42张。\n答案：42张"
	const method = "用乘法表示7个6相加，提醒孩子写单位。"
	generation, _ := jointGuideJSON(t, solution, method)
	response, err := json.Marshal(map[string]any{
		"program": "print(f'COMPUTED: {6 * 7}张')",
		"scope":   "IN_SCOPE",
		"checks": []map[string]any{{"step": "6×7=42张", "valid": true,
			"reason": "七个六相加等于四十二，单位仍为张。"}},
		"note":                "原题计算及讲解均正确。",
		"parent_guide_audits": []map[string]any{jointGuideAudit(solution, "IN_SCOPE")},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &standaloneVerifierTestProvider{response: string(response)}
	eng := newEngineWithProvider(t, provider)
	eng.cfg.LLM.Tools.Enabled = "on"
	registry := skill.NewRegistry()
	sbCfg := sandbox.Config{Workspace: t.TempDir(), Timeout: 30, RequiredCapabilities: sandbox.UntrustedCodeIsolationCapabilities}
	sb, err := sandbox.New(sbCfg)
	if err != nil {
		t.Fatal(err)
	}
	codeExec := &standaloneVerifierCountingCodeExec{CodeExecSkill: builtin.NewCodeExecSkill(sb, sbCfg)}
	if err := registry.Register(codeExec); err != nil {
		t.Fatal(err)
	}
	eng.SetToolCollector(NewToolCollector(registry, nil, 40))
	eng.SetToolExecutor(NewToolExecutor(registry, nil))
	var calls int
	execute := func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
		calls++
		if spec.Agent == solverAgentName {
			return SubAgentResult{Output: generation}, nil
		}
		if spec.Agent != verifierAgentName {
			t.Fatalf("unexpected agent %s", spec.Agent)
		}
		if !strings.Contains(spec.Task, "提醒孩子写单位") {
			t.Fatal("verifier lost the guide's unit reminder")
		}
		msg := &adapter.Message{ID: "joint-guide-verifier-component", Platform: adapter.PlatformAPI, UserID: "test-user",
			Content: spec.Task, Metadata: map[string]string{"memory": "off", "knowledge": "off"}}
		ApplySpecToMessage(msg, spec)
		reply, err := eng.Process(ctx, msg)
		if err != nil {
			return SubAgentResult{}, err
		}
		return SubAgentResult{Output: reply.Content, SessionID: msg.SessionID}, nil
	}
	result, err := NewSolveSkill(execute, nil).Execute(t.Context(), jointGuideArgs())
	if err != nil {
		t.Fatal(err)
	}
	jointGuideMetadata(t, result, method, jointGuideDigest(solution), "VALID")
	if calls != 2 || codeExec.calls.Load() != 1 || result.Metadata["solve_evidence"] != "numeric_exec" {
		t.Fatalf("joint audit needs two boundary calls and one actual execution: calls=%d code_exec=%d metadata=%v", calls, codeExec.calls.Load(), result.Metadata)
	}
	provider.mu.Lock()
	providerCalls, streamCalls := len(provider.requests), provider.streamCalls
	requests, marshalErr := json.Marshal(provider.requests)
	provider.mu.Unlock()
	if providerCalls != 1 || streamCalls != 0 {
		t.Fatalf("numeric verifier added a model summary: calls=%d streams=%d", providerCalls, streamCalls)
	}
	if marshalErr != nil || !strings.Contains(string(requests), "提醒孩子写单位") {
		t.Fatalf("unit reminder did not reach the verification request: %v", marshalErr)
	}
}

// 无论同值投票还是执行纠偏，都必须带回最终展示解法自己的讲解。
func TestSolveJointGuideKeepsSelectedCandidatePair(t *testing.T) {
	const firstMethod = "第一候选的重复相加法。"
	const secondMethod = "第二候选的分组乘法。"
	for _, tc := range []struct {
		name, firstSolution, secondSolution, verdict, process, wantMethod, wantSolution string
	}{
		{"equal-answer-first-candidate", "6+6+6+6+6+6+6=42张。\n答案：42张", "6×7=42张。\n答案：42张", "AGREE", "VALID", firstMethod, "6+6+6+6+6+6+6=42张。\n答案：42张"},
		{"executed-answer-selects-other-group", "6×7=41张。\n答案：41张", "6×7=42张。\n答案：42张", "DISAGREE", "INVALID", secondMethod, "6×7=42张。\n答案：42张"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			firstRaw, _ := jointGuideJSON(t, tc.firstSolution, firstMethod)
			secondRaw, _ := jointGuideJSON(t, tc.secondSolution, secondMethod)
			var solverCalls, verifierCalls int
			execute := func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
				if spec.Agent == solverAgentName {
					solverCalls++
					if solverCalls == 1 {
						return SubAgentResult{Output: firstRaw}, nil
					}
					return SubAgentResult{Output: secondRaw}, nil
				}
				if spec.Agent != verifierAgentName {
					t.Fatalf("unexpected agent %s", spec.Agent)
				}
				verifierCalls++
				if !strings.Contains(spec.Task, jointGuideDigest(tc.firstSolution)) || !strings.Contains(spec.Task, jointGuideDigest(tc.secondSolution)) {
					t.Fatal("verifier did not receive each solution's own guide identity")
				}
				jointGuideExecution(ctx, spec.Task)
				firstAudit := jointGuideAudit(tc.firstSolution, "IN_SCOPE")
				if tc.verdict == "DISAGREE" {
					fields := firstAudit["fields"].([]map[string]any)
					fields[0]["valid"] = false
					fields[0]["reason"] = "讲解的42张与该候选解法的41张相矛盾。"
				}
				return SubAgentResult{Output: jointGuideJudgment(t, tc.verdict, tc.process,
					firstAudit, jointGuideAudit(tc.secondSolution, "IN_SCOPE"))}, nil
			}
			args := jointGuideArgs()
			args["self_consistency"] = 2
			result, err := NewSolveSkill(execute, nil).Execute(t.Context(), args)
			if err != nil {
				t.Fatal(err)
			}
			if solverCalls != 2 || verifierCalls != 1 {
				t.Fatalf("unexpected generation/verification calls: %d/%d", solverCalls, verifierCalls)
			}
			jointGuideMetadata(t, result, tc.wantMethod, jointGuideDigest(tc.wantSolution), "VALID")
			if !strings.Contains(result.Content, tc.wantSolution) {
				t.Fatalf("guide and displayed solution diverged: %s", result.Content)
			}
		})
	}
}

func TestSolveJointGuideFollowsInScopeReplacement(t *testing.T) {
	const initial = "设总数为x，x/7=6，所以x=42。\n答案：42张"
	const replacement = "7盒是7个6相加，6×7=42张。\n答案：42张"
	initialRaw, _ := jointGuideJSON(t, initial, "使用方程解题。")
	replacementRaw, _ := jointGuideJSON(t, replacement, "使用小学乘法。")
	var solverCalls, verifierCalls int
	execute := func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
		if spec.Agent == solverAgentName {
			solverCalls++
			if solverCalls == 1 {
				return SubAgentResult{Output: initialRaw}, nil
			}
			return SubAgentResult{Output: replacementRaw}, nil
		}
		if spec.Agent != verifierAgentName {
			t.Fatalf("unexpected agent %s", spec.Agent)
		}
		verifierCalls++
		if verifierCalls == 1 {
			return SubAgentResult{Output: jointGuideJudgment(t, "OUT_OF_SCOPE", "VALID", jointGuideAudit(initial, "OUT_OF_SCOPE"))}, nil
		}
		if strings.Contains(spec.Task, jointGuideDigest(initial)) || !strings.Contains(spec.Task, jointGuideDigest(replacement)) {
			t.Fatal("replacement verification reused an out-of-scope guide")
		}
		jointGuideExecution(ctx, spec.Task)
		return SubAgentResult{Output: jointGuideJudgment(t, "AGREE", "VALID", jointGuideAudit(replacement, "IN_SCOPE"))}, nil
	}
	args := jointGuideArgs()
	args["constraint"] = "小学乘法，不使用方程"
	result, err := NewSolveSkill(execute, nil).Execute(t.Context(), args)
	if err != nil {
		t.Fatal(err)
	}
	if solverCalls != 2 || verifierCalls != 2 {
		t.Fatalf("scope recovery must keep its generation/verification pairs: %d/%d", solverCalls, verifierCalls)
	}
	jointGuideMetadata(t, result, "使用小学乘法。", jointGuideDigest(replacement), "VALID")
	if strings.Contains(result.Content, "x/7") || result.Metadata["solve_primary_digest"] != jointGuideDigest(replacement) {
		t.Fatalf("out-of-scope solution survived replacement: %v %s", result.Metadata, result.Content)
	}
}

// 答案正确与讲解合格是独立事实，缺失或歧义的审计不得被数值回执补齐。
func TestSolveJointGuideRejectsWrongOrIncompleteAudit(t *testing.T) {
	t.Run("single-concept-audit", func(t *testing.T) {
		for _, tc := range []struct{ name, want string }{
			{"missing-explanation", "INVALID"},
			{"missing-audit", "NOT_PROVIDED"},
			{"wrong-source", "NOT_PROVIDED"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				raw, guide := fractionRuleGuideJSON(t)
				audit := fractionRuleAudit()
				if tc.name == "missing-explanation" {
					guide["full_solution_steps"] = []string{}
					envelope, err := json.Marshal(map[string]any{"schema": jointGuideVersion, "solution": fractionRuleSolution, "parent_guide": guide})
					if err != nil {
						t.Fatal(err)
					}
					raw = string(envelope)
					fields := audit["fields"].([]map[string]any)
					fields[1]["valid"] = false
					fields[1]["reason"] = "讲法没有提供同分母相加和约分的必要解释，不能只给规律。"
				}
				if tc.name == "wrong-source" {
					audit["source_digest"] = jointGuideDigest("另一道题的解法")
				}
				judgment := fractionRuleJudgment(t, audit)
				if tc.name == "missing-audit" {
					judgment = "VERDICT: UNVERIFIABLE\nPROCESS: VALID\nCOMPUTED: N/A\nSCOPE: IN_SCOPE"
				}
				exec := &solveExec{solverOuts: []string{raw, raw}, verifierOut: judgment}
				result, err := NewSolveSkill(exec.fn, nil).Execute(t.Context(), fractionRuleArgs(fractionRuleQuestion))
				if err != nil {
					t.Fatal(err)
				}
				if exec.solverCalls() != 1 || exec.verifierCalls() != 1 || result.Metadata["solve_parent_guide_audit"] != tc.want || result.Metadata["solve_evidence"] != "model" {
					t.Fatalf("single candidate bypassed independent audit or added numeric proof/calls: %v %v", exec.agents(), result.Metadata)
				}
				var actual map[string]any
				if err := json.Unmarshal([]byte(result.Metadata["solve_parent_guide_json"]), &actual); err != nil {
					t.Fatal(err)
				}
				if tc.name == "missing-explanation" && len(actual["full_solution_steps"].([]any)) != 0 {
					t.Fatal("rejected missing explanation was silently replaced with a generic guide")
				}
			})
		}
	})
	const solution = "6×7=42张。\n答案：42张"
	for _, tc := range []struct {
		name, want string
		alter      func([]map[string]any) []map[string]any
	}{
		{"wrong-guide-example", "INVALID", func(a []map[string]any) []map[string]any {
			fields := a[0]["fields"].([]map[string]any)
			fields[3]["valid"] = false
			fields[3]["reason"] = "讲解把6+7错误地算成42，与乘法问题不一致。"
			return a
		}},
		{"out-of-scope-guide", "INVALID", func(a []map[string]any) []map[string]any { a[0]["scope"] = "OUT_OF_SCOPE"; return a }},
		{"missing-field", "NOT_PROVIDED", func(a []map[string]any) []map[string]any {
			a[0]["fields"] = a[0]["fields"].([]map[string]any)[:6]
			return a
		}},
		{"wrong-source", "NOT_PROVIDED", func(a []map[string]any) []map[string]any {
			a[0]["source_digest"] = jointGuideDigest("另一道题")
			return a
		}},
		{"duplicate-source", "NOT_PROVIDED", func(a []map[string]any) []map[string]any { return append(a, jointGuideAudit(solution, "IN_SCOPE")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, guide := jointGuideJSON(t, solution, "小学乘法。")
			if tc.name == "wrong-guide-example" {
				guide["likely_mistakes"] = []string{"正确方法是6+7=42张。"}
				envelope, err := json.Marshal(map[string]any{"schema": jointGuideVersion, "solution": solution, "parent_guide": guide})
				if err != nil {
					t.Fatal(err)
				}
				raw = string(envelope)
			}
			var calls int
			execute := func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
				calls++
				if spec.Agent == solverAgentName {
					return SubAgentResult{Output: raw}, nil
				}
				if spec.Agent != verifierAgentName {
					t.Fatalf("unexpected agent %s", spec.Agent)
				}
				jointGuideExecution(ctx, spec.Task)
				return SubAgentResult{Output: jointGuideJudgment(t, "AGREE", "VALID", tc.alter([]map[string]any{jointGuideAudit(solution, "IN_SCOPE")})...)}, nil
			}
			result, err := NewSolveSkill(execute, nil).Execute(t.Context(), jointGuideArgs())
			if err != nil {
				t.Fatal(err)
			}
			jointGuideMetadata(t, result, "小学乘法。", jointGuideDigest(solution), tc.want)
			if result.Metadata["solve_evidence"] != "numeric_exec" || calls != 2 {
				t.Fatalf("guide rejection changed numeric proof or repeated model calls: calls=%d metadata=%v", calls, result.Metadata)
			}
		})
	}
}

func TestSolveJointGuideUnknownVerificationDoesNotInventAudit(t *testing.T) {
	for _, stage := range []string{"solver", "verifier"} {
		t.Run("single-concept-"+stage+"-unknown", func(t *testing.T) {
			raw, _ := fractionRuleGuideJSON(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var solverCalls, verifierCalls int
			execute := func(_ context.Context, spec SubAgentSpec) (SubAgentResult, error) {
				switch spec.Agent {
				case solverAgentName:
					solverCalls++
					if stage == "solver" {
						cancel()
						return SubAgentResult{}, context.DeadlineExceeded
					}
					return SubAgentResult{Output: raw}, nil
				case verifierAgentName:
					verifierCalls++
					cancel()
					return SubAgentResult{}, context.DeadlineExceeded
				default:
					t.Fatalf("unexpected agent %s", spec.Agent)
					return SubAgentResult{}, nil
				}
			}
			result, err := NewSolveSkill(execute, nil).Execute(ctx, fractionRuleArgs(fractionRuleQuestion))
			wantVerifierCalls := 1
			if stage == "solver" {
				wantVerifierCalls = 0
			}
			if result != nil || !errors.Is(err, context.Canceled) || solverCalls != 1 || verifierCalls != wantVerifierCalls {
				t.Fatalf("unknown %s continued a second method or invented success: solver=%d verifier=%d result=%+v err=%v", stage, solverCalls, verifierCalls, result, err)
			}
		})
	}
	const solution = "6×7=42张。\n答案：42张"
	raw, _ := jointGuideJSON(t, solution, "小学乘法。")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls int
	execute := func(_ context.Context, spec SubAgentSpec) (SubAgentResult, error) {
		calls++
		if spec.Agent == solverAgentName {
			return SubAgentResult{Output: raw}, nil
		}
		if spec.Agent != verifierAgentName {
			t.Fatalf("unexpected agent %s", spec.Agent)
		}
		cancel()
		return SubAgentResult{}, context.DeadlineExceeded
	}
	result, err := NewSolveSkill(execute, nil).Execute(ctx, jointGuideArgs())
	if result != nil || !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("unknown verification lost its error or created a result or extra call: calls=%d result=%+v err=%v", calls, result, err)
	}
}

func TestSolveLegacyDoesNotAcquireJointGuideContract(t *testing.T) {
	const solution = "6×7=42张。\n答案：42张"
	var calls int
	execute := func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
		calls++
		if strings.Contains(spec.Task, jointGuideVersion) || strings.Contains(spec.Task, "PARENT_GUIDE_AUDITS") || strings.Contains(spec.Task, "parent_guide") {
			t.Fatal("legacy task acquired a new generation or verification contract")
		}
		if spec.Agent == solverAgentName {
			return SubAgentResult{Output: solution}, nil
		}
		if spec.Agent != verifierAgentName {
			t.Fatalf("unexpected agent %s", spec.Agent)
		}
		jointGuideExecution(ctx, spec.Task)
		return SubAgentResult{Output: "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: 42张"}, nil
	}
	args := jointGuideArgs()
	delete(args, "solve_output_version")
	delete(args, "parent_teaching_contract")
	result, err := NewSolveSkill(execute, nil).Execute(t.Context(), args)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"solve_parent_guide_json", "solve_parent_guide_audit", "solve_parent_guide_source_digest"} {
		if _, exists := result.Metadata[key]; exists {
			t.Fatalf("legacy result acquired joint guide field %q", key)
		}
	}
	if calls != 2 || result.Metadata["solve_evidence"] != "numeric_exec" || result.Metadata["solve_primary_digest"] != jointGuideDigest(solution) {
		t.Fatalf("legacy execution proof or calls changed: %d %v", calls, result.Metadata)
	}
}
