package engine

// BUG-20260613: a weak model (glm-4-flash) looped on an identical browser
// fetch 35 times until the token budget was exhausted. The runtime tool
// executor now short-circuits an identical (tool, args) call after a small
// number of repeats and nudges the model to answer from the result it has.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	coretool "github.com/hexagon-codes/ai-core/tool"
	"github.com/hexagon-codes/hexagon/browserruntime"
	hexagonmcp "github.com/hexagon-codes/hexagon/mcp"
	hruntime "github.com/hexagon-codes/hexagon/runtime"
	"github.com/hexagon-codes/hexagon/testing/mock"
	"github.com/hexagon-codes/hexclaw/adapter"
	hexmcp "github.com/hexagon-codes/hexclaw/mcp"
	"github.com/hexagon-codes/hexclaw/skill"
)

func TestBug20260613_ToolRepeatGuardShortCircuits(t *testing.T) {
	calls := 0
	exec := &ToolExecutor{}
	// Stub the real execution by routing through a fake skill registry would be
	// heavy; instead drive Execute directly and count how many reach the inner
	// executor via a sentinel: an empty executor returns an error path, so we
	// assert on the guard message instead.
	rte := newRuntimeToolExecutor(exec)
	_ = calls

	call := llm.ToolCall{Name: "browser", Arguments: `{"url":"http://x"}`}

	// First maxIdenticalToolCalls invocations pass through to the executor
	// (which here has no skills, returning a "not found" error — that's fine,
	// we only care that the guard does NOT short-circuit yet).
	for i := 0; i < maxIdenticalToolCalls; i++ {
		res, _ := rte.Execute(context.Background(), call)
		if strings.Contains(res.Content, "already called") {
			t.Fatalf("guard tripped too early on call %d", i+1)
		}
	}
	// The next identical call must be short-circuited by the guard.
	res, _ := rte.Execute(context.Background(), call)
	if !strings.Contains(res.Content, "already called") {
		t.Errorf("guard must short-circuit the repeated call, got %q", res.Content)
	}
	if res.Error != "" {
		t.Errorf("short-circuit must not be an error result, got %q", res.Error)
	}
}

func TestBug20260613_ToolRepeatGuardDistinctArgsNotBlocked(t *testing.T) {
	rte := newRuntimeToolExecutor(&ToolExecutor{})
	// Different arguments must each get their own budget.
	for i, url := range []string{`{"url":"a"}`, `{"url":"b"}`, `{"url":"c"}`, `{"url":"d"}`} {
		res, _ := rte.Execute(context.Background(), llm.ToolCall{Name: "browser", Arguments: url})
		if strings.Contains(res.Content, "already called") {
			t.Errorf("distinct args must not trip the guard (call %d, args %s)", i+1, url)
		}
	}
}

func TestToolRepeatGuard_UnsafeMutatingToolShortCircuitsAfterOne(t *testing.T) {
	rte := newRuntimeToolExecutor(&ToolExecutor{})
	call := llm.ToolCall{Name: "cron_task", Arguments: `{"action":"create","prompt":"每天早上9点采集百度热搜榜","schedule":"每天早上9点"}`}

	first, _ := rte.Execute(context.Background(), call)
	if strings.Contains(first.Content, "already called") {
		t.Fatalf("first mutating tool call must be allowed, got %q", first.Content)
	}
	second, _ := rte.Execute(context.Background(), call)
	if !strings.Contains(second.Content, "already called") {
		t.Fatalf("second identical mutating tool call must be blocked, got %q", second.Content)
	}
}

type repeatToolProvider struct {
	calls int
}

func (p *repeatToolProvider) Name() string { return "repeat-tool" }
func (p *repeatToolProvider) Complete(context.Context, llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.calls++
	return &llm.CompletionResponse{
		ToolCalls: []llm.ToolCall{{
			ID:        fmt.Sprintf("call-%d", p.calls),
			Name:      "cron_task",
			Arguments: `{"action":"create","prompt":"每天早上9点采集百度热搜榜","schedule":"每天早上9点"}`,
		}},
	}, nil
}
func (p *repeatToolProvider) Stream(context.Context, llm.CompletionRequest) (*llm.Stream, error) {
	return nil, errors.New("unused")
}

func TestToolRepeatGuard_RuntimeStopsAfterRepeatedMutatingTool(t *testing.T) {
	provider := &repeatToolProvider{}
	runner := hruntime.NewRunner(hruntime.Config{
		ProviderSelector: hruntime.StaticProviderSelector{Provider: provider, Name: "repeat", Model: "m"},
		ToolExecutor:     newRuntimeToolExecutor(&ToolExecutor{}),
		DefaultMaxTurns:  25,
	})

	result, err := runner.Run(context.Background(), hruntime.Request{
		ID:       "repeat-mutating-tool",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "create task"}},
		Tools:    []llm.ToolDefinition{{Type: "function", Function: llm.ToolFunctionDef{Name: "cron_task"}}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if provider.calls > 2 {
		t.Fatalf("repeat guard must stop the runtime after the blocked repeat, provider calls=%d", provider.calls)
	}
	if result.StopReason != hruntime.StopReasonEndTurn {
		t.Fatalf("StopReason=%q, want end_turn", result.StopReason)
	}
	if !strings.Contains(result.Content, "already called") {
		t.Fatalf("final content should explain the repeat guard, got %q", result.Content)
	}
}

func TestToolRepeatGuardEquivalentJSONArguments(t *testing.T) {
	rte := newRuntimeToolExecutor(&ToolExecutor{})
	first, err := rte.Execute(context.Background(), llm.ToolCall{Name: "cron_task", Arguments: `{"action":"create","prompt":"sample"}`})
	if err != nil || first.Stop {
		t.Fatalf("first call = %+v, %v", first, err)
	}
	second, err := rte.Execute(context.Background(), llm.ToolCall{Name: "cron_task", Arguments: ` { "prompt": "sample", "action": "create" } `})
	if err != nil || !second.Stop || second.Raw != repeatToolCallBlockedError {
		t.Fatalf("equivalent repeat = %+v, %v", second, err)
	}
}

type repeatStreamToolProvider struct{ repeatToolProvider }

func (p *repeatStreamToolProvider) Stream(ctx context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	resp, err := p.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(map[string]any{"choices": []any{map[string]any{
		"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": resp.ToolCalls[0].ID, "type": "function",
			"function": map[string]any{"name": resp.ToolCalls[0].Name, "arguments": resp.ToolCalls[0].Arguments},
		}}}, "finish_reason": "tool_calls",
	}}})
	if err != nil {
		return nil, err
	}
	return llm.NewStream(strings.NewReader("data: "+string(data)+"\n\ndata: [DONE]\n\n"), llm.StreamOpenAIFormat), nil
}

func (*repeatStreamToolProvider) Models() []llm.ModelInfo                { return []llm.ModelInfo{{ID: "mock-model"}} }
func (*repeatStreamToolProvider) CountTokens([]llm.Message) (int, error) { return 1, nil }

func TestToolRepeatGuardProcessStreamStopsAndPersists(t *testing.T) {
	provider := &repeatStreamToolProvider{}
	eng := newEngineWithProvider(t, provider)
	eng.SetToolExecutor(&ToolExecutor{})
	chunks, err := eng.ProcessStream(context.Background(), &adapter.Message{
		ID: "repeat-stream", Platform: adapter.PlatformAPI, UserID: "repeat-user",
		SessionID: "repeat-session", Content: "Create a sample task.",
	})
	if err != nil {
		t.Fatal(err)
	}
	var terminal *adapter.ReplyChunk
	for chunk := range chunks {
		if chunk.Done {
			terminal = chunk
		}
	}
	if provider.calls != 2 || terminal == nil || terminal.Error != nil {
		t.Fatalf("calls=%d, expected two calls and a successful terminal chunk", provider.calls)
	}
	record, err := eng.store.GetMessage(context.Background(), terminal.AssistantMessageID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(record.Content, "already called") {
		t.Fatalf("persisted terminal content = %q", record.Content)
	}
}

func TestToolRepeatGuardBrowserObservationsRemainLive(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("invoke_wrapper=%t", wrapped), func(t *testing.T) {
			state := "initial"
			observed := []string{}
			registry := skill.NewRegistry()
			if err := registry.Register(&externalAccessSkill{name: "browser_session", run: func(_ context.Context, args map[string]any) (*skill.Result, error) {
				action, _ := args["action"].(string)
				if action == "call" {
					if args["tool_name"] == "browser_snapshot" {
						observed = append(observed, state)
					} else {
						state = fmt.Sprint(args["tool_name"])
					}
				}
				return &skill.Result{Content: state}, nil
			}}); err != nil {
				t.Fatal(err)
			}
			executor := NewToolExecutor(registry, nil)
			RegisterExternalToolAccess(registry, nil, executor, nil)
			runtime := newRuntimeToolExecutor(executor)
			call := func(name string) hruntime.ToolResult {
				input := map[string]any{}
				if name == "browser_fill_form" {
					input["fields"] = []any{map[string]any{"target": "account", "name": "Account", "type": "textbox", "value": "synthetic"}}
				}
				if name == "browser_click" {
					input["target"], input["element"] = "login", "Login"
				}
				args := map[string]any{"action": "call", "page": "synthetic-page", "tool_name": name, "arguments": input}
				toolName := "browser_session"
				if wrapped {
					toolName, args = invokeToolName, map[string]any{"name": toolName, "arguments": args}
				}
				encoded, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				result, err := runtime.Execute(context.Background(), llm.ToolCall{Name: toolName, Arguments: string(encoded)})
				if err != nil || result.Stop || result.Status != hruntime.ToolStatusSuccess {
					t.Fatalf("%s observation/action = %+v, %v", name, result, err)
				}
				return result
			}
			call("browser_snapshot")
			call("browser_fill_form")
			call("browser_snapshot")
			call("browser_click")
			call("browser_snapshot")
			want := []string{"initial", "browser_fill_form", "browser_click"}
			if strings.Join(observed, ",") != strings.Join(want, ",") {
				t.Fatalf("actual page observations = %v, want %v", observed, want)
			}
		})
	}
}

func TestToolRepeatGuardUnknownWriteCannotChangeInvocationWrapper(t *testing.T) {
	for _, firstWrapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("first_wrapped=%t", firstWrapped), func(t *testing.T) {
			physicalCalls := 0
			registry := skill.NewRegistry()
			if err := registry.Register(&externalAccessSkill{name: "browser", run: func(context.Context, map[string]any) (*skill.Result, error) {
				physicalCalls++
				detail := skill.ToolExecutionDetail{Phase: "request", Outcome: "unknown", Reason: "response_unconfirmed"}
				return &skill.Result{Content: "The request was sent but no response was received.", Detail: &detail}, &skill.ExecutionError{Detail: detail, Err: errors.New("response unavailable")}
			}}); err != nil {
				t.Fatal(err)
			}
			executor := NewToolExecutor(registry, nil)
			RegisterExternalToolAccess(registry, nil, executor, nil)
			runtime := newRuntimeToolExecutor(executor)
			for index, wrapped := range []bool{firstWrapped, !firstWrapped} {
				name := "browser"
				arguments := `{"action":"request","method":"POST","url":"https://fixture.invalid/submit","body_kind":"json","json":{"marker":"single-write"}}`
				if wrapped {
					name = invokeToolName
					arguments = ` { "arguments": { "json": { "marker": "single-write" }, "body_kind": "json", "url": "https://fixture.invalid/submit", "method": "POST", "action": "request" }, "name": "browser" } `
				}
				result, err := runtime.Execute(context.Background(), llm.ToolCall{ID: fmt.Sprintf("call-%d", index), Name: name, Arguments: arguments})
				if err != nil || result.Status != hruntime.ToolStatusError {
					t.Fatalf("attempt %d = %+v, %v", index, result, err)
				}
				if detail := toolExecutionDetail(result.Raw); result.Stop || detail == nil || detail.Outcome != "unknown" || detail.Reason != "response_unconfirmed" {
					t.Fatalf("attempt %d did not preserve the original unresolved result: %+v", index, result)
				}
			}
			if physicalCalls != 1 {
				t.Fatalf("unknown write physically executed %d times, want one", physicalCalls)
			}
		})
	}
}

func TestToolRepeatGuardMutatingCallsShareWrapperIdentity(t *testing.T) {
	registry := skill.NewRegistry()
	physicalCalls := 0
	if err := registry.Register(&externalAccessSkill{name: "cron_task", run: func(context.Context, map[string]any) (*skill.Result, error) {
		physicalCalls++
		return &skill.Result{Content: "created"}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	executor := NewToolExecutor(registry, nil)
	RegisterExternalToolAccess(registry, nil, executor, nil)
	runtime := newRuntimeToolExecutor(executor)
	first, err := runtime.Execute(context.Background(), llm.ToolCall{Name: "cron_task", Arguments: `{"action":"create","prompt":"sample"}`})
	if err != nil || first.Stop || first.Status != hruntime.ToolStatusSuccess {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := runtime.Execute(context.Background(), llm.ToolCall{Name: invokeToolName, Arguments: `{"arguments":{"prompt":"sample","action":"create"},"name":"cron_task"}`})
	if err != nil || !second.Stop || physicalCalls != 1 {
		t.Fatalf("equivalent wrapped mutation = %+v, %v; physical calls=%d", second, err, physicalCalls)
	}
}

func TestToolRepeatGuardResolvedMCPBrowserObservationRemainsLive(t *testing.T) {
	var physicalCalls atomic.Int32
	server := hexagonmcp.NewMCPServerV2("browser-fixture", "1.0.0")
	server.RegisterTool(mock.NewTool("browser_snapshot", mock.WithToolExecuteFn(func(context.Context, map[string]any) (coretool.Result, error) {
		count := physicalCalls.Add(1)
		return coretool.NewResult(fmt.Sprintf("snapshot-%d", count)), nil
	})))
	httpServer := httptest.NewServer(server.HTTPHandler())
	t.Cleanup(httpServer.Close)
	replacement := hexagonmcp.NewMCPServerV2("ordinary-replacement", "1.0.0")
	replacement.RegisterTool(mock.NewTool("browser_snapshot"))
	replacementHTTP := httptest.NewServer(replacement.HTTPHandler())
	t.Cleanup(replacementHTTP.Close)
	manager := hexmcp.NewManager()
	t.Cleanup(manager.Close)
	if err := manager.AddServer(context.Background(), hexmcp.ServerConfig{Name: "browser-fixture", Transport: "streamable", Endpoint: httpServer.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	tools := manager.ListToolInfos()
	if len(tools) != 1 || tools[0].OriginalName != "browser_snapshot" {
		t.Fatalf("actual MCP tools = %+v", tools)
	}
	executor := NewToolExecutor(skill.NewRegistry(), manager)
	executor.SetMCPDispatch(func(ctx context.Context, server, name string, args map[string]any) (coretool.Result, error) {
		result, err := manager.CallServerToolResult(ctx, server, name, args)
		result.Output = repeatBrowserOutput{ref: browserruntime.SessionRef{ID: server, ServerName: server, ProfileID: "profile-one", PageID: "page-one", Generation: 1}, text: result.String()}
		return result, err
	})
	runtime := newRuntimeToolExecutor(executor)
	call := llm.ToolCall{Name: tools[0].Name, Arguments: `{}`}
	if runtime.SideEffectOf(call) != hruntime.SideEffectUnsafe {
		t.Fatal("an unproved MCP name must retain the conservative default")
	}
	for index := 0; index < 3; index++ {
		result, err := runtime.Execute(context.Background(), call)
		if err != nil || result.Stop || result.Status != hruntime.ToolStatusSuccess {
			t.Fatalf("MCP observation %d = %+v, %v", index, result, err)
		}
		if runtime.SideEffectOf(call) != hruntime.SideEffectReadOnly {
			t.Fatal("the actual browser adapter receipt must preserve read-only semantics")
		}
	}
	if count := physicalCalls.Load(); count != 3 {
		t.Fatalf("physical MCP observations = %d, want three", count)
	}
	if err := manager.AddServer(context.Background(), hexmcp.ServerConfig{Name: "browser-fixture", Transport: "streamable", Endpoint: replacementHTTP.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if runtime.SideEffectOf(call) != hruntime.SideEffectUnsafe {
		t.Fatal("old browser proof cannot be inherited by a new same-name source")
	}
}

type repeatBrowserOutput struct {
	ref  browserruntime.SessionRef
	text string
}

func (r repeatBrowserOutput) ToolBrowserSession() browserruntime.SessionRef { return r.ref }
func (r repeatBrowserOutput) ToolText() string                              { return r.text }

func TestToolRepeatGuardUnrelatedMCPSnapshotNameIsNotBrowserProof(t *testing.T) {
	server := hexagonmcp.NewMCPServerV2("ordinary-service", "1.0.0")
	server.RegisterTool(mock.NewTool("browser_snapshot", mock.WithToolExecuteFn(func(context.Context, map[string]any) (coretool.Result, error) {
		return coretool.NewResult(map[string]any{"session": map[string]any{"ServerName": "ordinary-service", "ProfileID": "pretended-browser", "PageID": "pretended-page", "Generation": 1}}), nil
	})))
	httpServer := httptest.NewServer(server.HTTPHandler())
	t.Cleanup(httpServer.Close)
	manager := hexmcp.NewManager()
	t.Cleanup(manager.Close)
	if err := manager.AddServer(context.Background(), hexmcp.ServerConfig{Name: "ordinary-service", Transport: "streamable", Endpoint: httpServer.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	tools := manager.ListToolInfos()
	if len(tools) != 1 {
		t.Fatalf("actual tools = %+v", tools)
	}
	runtime := newRuntimeToolExecutor(NewToolExecutor(skill.NewRegistry(), manager))
	call := llm.ToolCall{Name: tools[0].Name, Arguments: `{}`}
	if runtime.SideEffectOf(call) != hruntime.SideEffectUnsafe {
		t.Fatal("a matching third-party name is not a browser declaration")
	}
	first, err := runtime.Execute(context.Background(), call)
	if err != nil || first.Stop {
		t.Fatalf("first ordinary service call = %+v, %v", first, err)
	}
	second, err := runtime.Execute(context.Background(), call)
	if err != nil || !second.Stop || runtime.SideEffectOf(call) != hruntime.SideEffectUnsafe {
		t.Fatalf("unproved service must retain its original semantics: %+v, %v", second, err)
	}
}

func TestToolRepeatGuardBrowserObservationSideEffects(t *testing.T) {
	registry := skill.NewRegistry()
	if err := registry.Register(&testSkill{name: "browser_session"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(&testSkill{name: "browser_snapshot"}); err != nil {
		t.Fatal(err)
	}
	executor := NewToolExecutor(registry, nil)
	RegisterExternalToolAccess(registry, nil, executor, nil)
	runtime := newRuntimeToolExecutor(executor)
	cases := []struct {
		name, tool, arguments string
		want                  hruntime.ToolSideEffect
	}{
		{"tool schema", "browser_session", `{"action":"tools","tool_name":"browser_click"}`, hruntime.SideEffectReadOnly},
		{"status", "browser_session", `{"action":"status"}`, hruntime.SideEffectReadOnly},
		{"observe", "browser_session", `{"action":"observe","page":"page-one"}`, hruntime.SideEffectReadOnly},
		{"snapshot", "browser_session", `{"action":"call","tool_name":"browser_snapshot","arguments":{}}`, hruntime.SideEffectReadOnly},
		{"wait", "browser_session", `{"action":"call","tool_name":"browser_wait_for","arguments":{"time":1}}`, hruntime.SideEffectReadOnly},
		{"list tabs", "browser_session", `{"action":"call","tool_name":"browser_tabs","arguments":{"action":"list"}}`, hruntime.SideEffectReadOnly},
		{"wrapped snapshot", invokeToolName, `{"name":"browser_session","arguments":{"action":"call","tool_name":"browser_snapshot","arguments":{}}}`, hruntime.SideEffectReadOnly},
		{"click", "browser_session", `{"action":"call","tool_name":"browser_click","arguments":{"target":"submit"}}`, hruntime.SideEffectUnsafe},
		{"arbitrary evaluate", "browser_session", `{"action":"call","tool_name":"browser_evaluate","arguments":{"function":"() => 1"}}`, hruntime.SideEffectUnsafe},
		{"arbitrary code", "browser_session", `{"action":"call","tool_name":"browser_run_code_unsafe","arguments":{"code":"async page => 1"}}`, hruntime.SideEffectUnsafe},
		{"select tab", "browser_session", `{"action":"call","tool_name":"browser_tabs","arguments":{"action":"select","index":0}}`, hruntime.SideEffectUnsafe},
		{"wrapped write", invokeToolName, `{"name":"browser","arguments":{"action":"request","method":"POST","url":"https://fixture.invalid/submit"}}`, hruntime.SideEffectUnsafe},
		{"recursive wrapper", invokeToolName, `{"name":"invoke_tool","arguments":{"name":"browser_session","arguments":{"action":"observe"}}}`, hruntime.SideEffectUnsafe},
		{"same named unrelated skill", "browser_snapshot", `{}`, hruntime.SideEffectUnsafe},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := runtime.SideEffectOf(llm.ToolCall{Name: test.tool, Arguments: test.arguments}); got != test.want {
				t.Fatalf("side effect = %v, want %v", got, test.want)
			}
		})
	}
}

type repeatBrowserProbe struct {
	ref         browserruntime.SessionRef
	calls       int
	nextOutcome string
}

func newRepeatBrowserProbe(t *testing.T) (*runtimeToolExecutor, *repeatBrowserProbe) {
	t.Helper()
	probe := &repeatBrowserProbe{ref: browserruntime.SessionRef{ID: "browser-session", ServerName: "server-one", ProfileID: "profile-one", PageID: "page-one", Generation: 1}}
	registry := skill.NewRegistry()
	if err := registry.Register(&externalAccessSkill{name: "browser_session", run: func(_ context.Context, args map[string]any) (*skill.Result, error) {
		probe.calls++
		for key, field := range map[string]*string{"server": &probe.ref.ServerName, "profile": &probe.ref.ProfileID, "page": &probe.ref.PageID} {
			if value, _ := args[key].(string); value != "" {
				*field = value
			}
		}
		result := &skill.Result{Content: "interaction completed", Data: map[string]any{"session": probe.ref}}
		if outcome := probe.nextOutcome; outcome != "" {
			probe.nextOutcome = ""
			detail := skill.ToolExecutionDetail{Outcome: outcome, Phase: "browser", Reason: "fixture_outcome"}
			result.Detail = &detail
			return result, &skill.ExecutionError{Detail: detail, Err: errors.New("controlled browser failure")}
		}
		return result, nil
	}}); err != nil {
		t.Fatal(err)
	}
	executor := NewToolExecutor(registry, nil)
	RegisterExternalToolAccess(registry, nil, executor, nil)
	return newRuntimeToolExecutor(executor), probe
}

func runRepeatBrowserCall(t *testing.T, runtime *runtimeToolExecutor, args map[string]any, wrapped bool) hruntime.ToolResult {
	t.Helper()
	name := "browser_session"
	if wrapped {
		name, args = invokeToolName, map[string]any{"name": name, "arguments": args}
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Execute(context.Background(), llm.ToolCall{Name: name, Arguments: string(encoded)})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestToolRepeatGuardBrowserConfirmedProgress(t *testing.T) {
	fill := func(value string) map[string]any {
		return map[string]any{"action": "call", "tool_name": "browser_fill_form", "arguments": map[string]any{"fields": []any{map[string]any{"target": "title", "name": "Title", "type": "textbox", "value": value}}}}
	}
	click := map[string]any{"action": "call", "tool_name": "browser_click", "arguments": map[string]any{"target": "submit", "element": "Submit"}}
	cases := []struct {
		name    string
		calls   []map[string]any
		wrapped bool
	}{
		{"revisit URL", []map[string]any{{"action": "navigate", "url": "https://fixture.invalid/a"}, {"action": "navigate", "url": "https://fixture.invalid/b"}, {"action": "navigate", "url": "https://fixture.invalid/a"}}, false},
		{"select previous page", []map[string]any{{"action": "select_page", "page": "page-one"}, {"action": "select_page", "page": "page-two"}, {"action": "select_page", "page": "page-one"}}, false},
		{"submit different input", []map[string]any{fill("first"), click, fill("second"), click}, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			runtime, probe := newRepeatBrowserProbe(t)
			runRepeatBrowserCall(t, runtime, map[string]any{"action": "observe"}, false)
			for index, args := range test.calls {
				result := runRepeatBrowserCall(t, runtime, args, test.wrapped)
				if result.Stop || result.Status != hruntime.ToolStatusSuccess {
					t.Fatalf("confirmed flow blocked at step %d: %+v", index+1, result)
				}
			}
			if probe.calls != len(test.calls)+1 {
				t.Fatalf("physical calls = %d, want %d", probe.calls, len(test.calls)+1)
			}
			last := runRepeatBrowserCall(t, runtime, test.calls[len(test.calls)-1], !test.wrapped)
			if !last.Stop || probe.calls != len(test.calls)+1 {
				t.Fatalf("the last successful action must still consume its first count: %+v; calls=%d", last, probe.calls)
			}
		})
	}
}

func TestToolRepeatGuardBrowserUnrelatedProgressDoesNotUnlockWrite(t *testing.T) {
	for _, otherService := range []bool{false, true} {
		t.Run(fmt.Sprintf("other_service=%t", otherService), func(t *testing.T) {
			runtime, probe := newRepeatBrowserProbe(t)
			original := map[string]any{"action": "call", "server": "server-one", "profile": "profile-one", "page": "page-one", "tool_name": "browser_click", "arguments": map[string]any{"target": "submit", "element": "Submit"}}
			runRepeatBrowserCall(t, runtime, map[string]any{"action": "observe"}, false)
			if first := runRepeatBrowserCall(t, runtime, original, false); first.Status != hruntime.ToolStatusSuccess {
				t.Fatal(first)
			}
			server, profile := "server-one", "profile-one"
			if otherService {
				server, profile = "server-two", "profile-two"
			}
			other := map[string]any{"action": "call", "server": server, "profile": profile, "page": "page-two", "tool_name": "browser_fill_form", "arguments": map[string]any{"fields": []any{map[string]any{"target": "title", "name": "Title", "type": "textbox", "value": "other page"}}}}
			if result := runRepeatBrowserCall(t, runtime, other, false); result.Status != hruntime.ToolStatusSuccess {
				t.Fatal(result)
			}
			if result := runRepeatBrowserCall(t, runtime, map[string]any{"action": "select_page", "server": "server-one", "profile": "profile-one", "page": "page-one"}, false); result.Status != hruntime.ToolStatusSuccess {
				t.Fatal(result)
			}
			before := probe.calls
			last := runRepeatBrowserCall(t, runtime, original, true)
			if !last.Stop || probe.calls != before {
				t.Fatalf("unrelated page/profile progress unlocked the old write: %+v; calls=%d/%d", last, probe.calls, before)
			}
		})
	}
}

func TestToolRepeatGuardBrowserUnconfirmedProgressDoesNotUnlockWrite(t *testing.T) {
	for _, outcome := range []string{"failure", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			runtime, probe := newRepeatBrowserProbe(t)
			runRepeatBrowserCall(t, runtime, map[string]any{"action": "observe"}, false)
			click := map[string]any{"action": "call", "tool_name": "browser_click", "arguments": map[string]any{"target": "submit", "element": "Submit"}}
			runRepeatBrowserCall(t, runtime, click, false)
			probe.nextOutcome = outcome
			failed := runRepeatBrowserCall(t, runtime, map[string]any{"action": "call", "tool_name": "browser_fill_form", "arguments": map[string]any{"fields": []any{map[string]any{"target": "title", "name": "Title", "type": "textbox", "value": "unconfirmed"}}}}, false)
			if failed.Status != hruntime.ToolStatusError {
				t.Fatalf("expected failed interaction: %+v", failed)
			}
			before := probe.calls
			last := runRepeatBrowserCall(t, runtime, click, false)
			if !last.Stop || probe.calls != before {
				t.Fatalf("%s unlocked the previous write: %+v", outcome, last)
			}
		})
	}
}

func TestToolRepeatGuardBrowserReadDoesNotUnlockWrite(t *testing.T) {
	runtime, probe := newRepeatBrowserProbe(t)
	runRepeatBrowserCall(t, runtime, map[string]any{"action": "observe"}, false)
	click := map[string]any{"action": "call", "tool_name": "browser_click", "arguments": map[string]any{"target": "submit", "element": "Submit"}}
	runRepeatBrowserCall(t, runtime, click, false)
	for index := 0; index < 3; index++ {
		if read := runRepeatBrowserCall(t, runtime, map[string]any{"action": "observe"}, false); read.Stop || read.Status != hruntime.ToolStatusSuccess {
			t.Fatal(read)
		}
	}
	before := probe.calls
	last := runRepeatBrowserCall(t, runtime, click, true)
	if !last.Stop || probe.calls != before {
		t.Fatalf("read-only observations unlocked the previous write: %+v", last)
	}
}

func TestToolRepeatGuardBrowserUnknownWriteSurvivesConfirmedProgress(t *testing.T) {
	runtime, probe := newRepeatBrowserProbe(t)
	runRepeatBrowserCall(t, runtime, map[string]any{"action": "observe"}, false)
	click := map[string]any{"action": "call", "tool_name": "browser_click", "arguments": map[string]any{"target": "submit", "element": "Submit"}}
	probe.nextOutcome = "unknown"
	runRepeatBrowserCall(t, runtime, click, false)
	if progress := runRepeatBrowserCall(t, runtime, map[string]any{"action": "navigate", "url": "https://fixture.invalid/verified-state"}, false); progress.Status != hruntime.ToolStatusSuccess {
		t.Fatal(progress)
	}
	before := probe.calls
	last := runRepeatBrowserCall(t, runtime, click, true)
	if detail := toolExecutionDetail(last.Raw); last.Stop || detail == nil || detail.Outcome != "unknown" || probe.calls != before {
		t.Fatalf("confirmed progress bypassed unresolved original operation: %+v; calls=%d/%d", last, probe.calls, before)
	}
}

func TestToolRepeatGuardOldBrowserObservationCannotReplaceNewGeneration(t *testing.T) {
	runtime, probe := newRepeatBrowserProbe(t)
	runRepeatBrowserCall(t, runtime, map[string]any{"action": "observe"}, false)
	click := map[string]any{"action": "call", "tool_name": "browser_click", "arguments": map[string]any{"target": "submit", "element": "Submit"}}
	runRepeatBrowserCall(t, runtime, click, false)
	probe.ref.Generation = 2
	runRepeatBrowserCall(t, runtime, map[string]any{"action": "observe"}, false)
	runRepeatBrowserCall(t, runtime, map[string]any{"action": "call", "tool_name": "browser_fill_form", "arguments": map[string]any{"fields": []any{map[string]any{"target": "title", "name": "Title", "type": "textbox", "value": "new generation"}}}}, false)
	probe.ref.Generation = 1
	runRepeatBrowserCall(t, runtime, map[string]any{"action": "observe"}, false)
	probe.ref.Generation = 2
	last := runRepeatBrowserCall(t, runtime, click, false)
	if last.Stop || last.Status != hruntime.ToolStatusSuccess {
		t.Fatalf("a late old-generation observation replaced the current identity: %+v", last)
	}
	before := probe.calls
	if repeated := runRepeatBrowserCall(t, runtime, click, false); !repeated.Stop || probe.calls != before {
		t.Fatalf("the new generation's first completed action count was lost: %+v", repeated)
	}
}
