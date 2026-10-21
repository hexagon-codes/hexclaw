package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/mcp"
	"github.com/hexagon-codes/hexclaw/skill"
)

type snapshotCaptureProvider struct {
	hexagon.Provider
	requests []llm.CompletionRequest
	fail     error
}

func TestRequestRuntimeDistinguishesToolLimitAndDisconnectedMCP(t *testing.T) {
	registry := skill.NewRegistry()
	for _, name := range []string{"a", "b", "c"} {
		if err := registry.Register(&testSkill{name: name}); err != nil {
			t.Fatal(err)
		}
	}
	manager := mcp.NewManager()
	t.Cleanup(func() { manager.Close() })
	// 无效传输在本地直接失败，不连接任何外部服务。
	if _, err := manager.Connect(t.Context(), []mcp.ServerConfig{{Name: "unconnected-source", Enabled: true, Transport: "unsupported-test-transport"}}); err != nil {
		t.Fatal(err)
	}
	ctx := withRuntimeSnapshots(t.Context(), &adapter.Message{ID: "limited-turn"}, "limited-session")
	collector := NewToolCollector(registry, manager, 2)
	tools := collector.collectFilteredForRuntime(ctx, "", skill.Activation{})
	if len(tools) != 2 || tools[0].Function.Name != "a" || tools[1].Function.Name != "b" {
		t.Fatal("runtime projection changed tool selection")
	}
	recordRuntimeToolLimit(ctx, tools, 1)
	provider := (&ReActEngine{}).wrapRuntimeSnapshotProvider(&snapshotCaptureProvider{}, "provider", "model")
	if _, err := provider.Complete(ctx, llm.CompletionRequest{Tools: tools[:1]}); err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Selection runtimeToolSelection     `json:"tool_selection"`
		Attempts  []runtimeSnapshotAttempt `json:"attempts"`
	}
	if err := json.Unmarshal([]byte(runtimeSnapshotReceipt(ctx)), &receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Selection.CollectorLimitOmitted) != 1 || receipt.Selection.CollectorLimitOmitted[0] != "c" || len(receipt.Selection.RequestLimitOmitted) != 1 || receipt.Selection.RequestLimitOmitted[0] != "b" {
		t.Fatal("tool limits lost their actual omitted names")
	}
	if len(receipt.Selection.MCPConnections) != 1 || receipt.Selection.MCPConnections[0].Connected {
		t.Fatal("disconnected MCP was reported as connected")
	}
	reasons := strings.Join(receipt.Attempts[0].SelectionReasons, ",")
	if !strings.Contains(reasons, "mcp_disconnected") || !strings.Contains(reasons, "collector_tool_limit") || !strings.Contains(reasons, "request_tool_limit") || strings.Contains(reasons, "approval") {
		t.Fatalf("selection reasons are incorrect: %s", reasons)
	}
}

func (p *snapshotCaptureProvider) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.requests = append(p.requests, req)
	return &llm.CompletionResponse{Content: "answer"}, p.fail
}

func TestRequestRuntimeActualToolsAndDegrade(t *testing.T) {
	msg := &adapter.Message{ID: "inbound-1", Metadata: map[string]string{"request_id": "req-1", "routed_agent": "tutor"}}
	ctx := withRuntimeSnapshots(t.Context(), msg, "session-1")
	actual := &snapshotCaptureProvider{fail: errors.New("tool calling is not supported")}
	provider := (&ReActEngine{}).wrapRuntimeSnapshotProvider(actual, "selected-provider", "selected-model")
	original := []llm.Message{{Role: llm.RoleUser, Content: "what can you do"}}
	_, _ = provider.Complete(ctx, llm.CompletionRequest{Messages: original, Tools: []llm.ToolDefinition{llm.NewToolDefinition("app_query", "inspect", nil)}})
	actual.fail = nil
	_, err := provider.Complete(ctx, llm.CompletionRequest{Messages: actual.requests[0].Messages})
	if err != nil {
		t.Fatal(err)
	}
	if len(original) != 1 || original[0].Role != llm.RoleUser {
		t.Fatal("original history changed")
	}
	for i, request := range actual.requests {
		count := 0
		for _, message := range request.Messages {
			if strings.HasPrefix(message.Content, runtimeSnapshotMarker) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("request %d has %d current snapshots", i, count)
		}
	}
	current, err := CurrentRequestRuntimeJSON(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		EffectiveToolNames []string               `json:"effective_tool_names"`
		Current            runtimeSnapshotAttempt `json:"current"`
	}
	if err := json.Unmarshal([]byte(current), &state); err != nil {
		t.Fatal(err)
	}
	if state.Current.ActualProvider != "selected-provider" || state.Current.ActualModel != "selected-model" || state.Current.ToolsEnabledEffective || len(state.EffectiveToolNames) != 0 {
		t.Fatalf("degraded request reports old tools or model: %s", current)
	}
	var receipt struct {
		Attempts []runtimeSnapshotAttempt `json:"attempts"`
		ToolSets map[string][]string      `json:"tool_sets"`
	}
	if err := json.Unmarshal([]byte(runtimeSnapshotReceipt(ctx)), &receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Attempts) != 2 || receipt.Attempts[0].Outcome != "failed" || receipt.Attempts[1].Outcome != "completed" || len(receipt.ToolSets) != 2 {
		t.Fatalf("attempt history lost: %+v", receipt)
	}
}

func TestRequestRuntimeIsolationAndUnknown(t *testing.T) {
	ctxA := withRuntimeSnapshots(t.Context(), &adapter.Message{ID: "a"}, "session-a")
	ctxB := withRuntimeSnapshots(ctxA, &adapter.Message{ID: "b"}, "session-b")
	provider := (&ReActEngine{}).wrapRuntimeSnapshotProvider(&snapshotCaptureProvider{}, "provider-a", "model-a")
	_, _ = provider.Complete(ctxA, llm.CompletionRequest{})
	for _, ctx := range []context.Context{ctxB, t.Context()} {
		state, err := CurrentRequestRuntimeJSON(ctx)
		if err != nil || !strings.Contains(state, `"status":"unknown"`) || strings.Contains(state, "provider-a") {
			t.Fatalf("request leaked: %s %v", state, err)
		}
		if runtimeSnapshotReceipt(ctx) != "" {
			t.Fatal("unexecuted request has a receipt")
		}
	}
}
