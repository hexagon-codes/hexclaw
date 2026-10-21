package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	hruntime "github.com/hexagon-codes/hexagon/runtime"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
)

func TestRuntimeToolHTTPFailurePreservesBodyAndDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<p>upstream diagnostic body</p>"))
	}))
	defer server.Close()
	registry := skill.NewRegistry()
	if err := registry.Register(builtin.NewBrowserSkill()); err != nil {
		t.Fatal(err)
	}
	for _, pipeline := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "pipeline"}[pipeline], func(t *testing.T) {
			ctx := t.Context()
			if pipeline {
				ctx = withFlagOn(ctx, skill.FlagSkillPipelineV1)
			}
			args, _ := json.Marshal(map[string]string{"url": server.URL, "action": "fetch"})
			result, err := newRuntimeToolExecutor(NewToolExecutor(registry, nil)).Execute(ctx, llm.ToolCall{ID: "call-1", Name: "browser", Arguments: string(args)})
			if err != nil {
				t.Fatal(err)
			}
			calls := runtimeToolCallsToAdapter([]hruntime.ToolCallRecord{{ID: "call-1", Name: "browser", Result: result}})
			if result.Status != hruntime.ToolStatusError || !strings.Contains(result.Content, "upstream diagnostic body") || !strings.Contains(result.Content, `"http_status":403`) || len(calls) != 1 || calls[0].Detail == nil || calls[0].Detail.HTTPStatus != 403 || calls[0].Detail.ApprovalStatus != "" {
				t.Fatalf("execution facts or model body lost: result=%+v calls=%+v", result, calls)
			}
		})
	}
}

func TestRuntimeToolUnknownPostDoesNotResend(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()
	registry := skill.NewRegistry()
	if err := registry.Register(builtin.NewBrowserSkill()); err != nil {
		t.Fatal(err)
	}
	executor := newRuntimeToolExecutor(NewToolExecutor(registry, nil))
	for _, args := range []map[string]any{
		{"url": server.URL, "action": "post", "data": map[string]any{"value": "1"}},
		{"action": "post", "data": map[string]any{"value": "1"}, "url": server.URL},
	} {
		encoded, _ := json.Marshal(args)
		result, err := executor.Execute(t.Context(), llm.ToolCall{ID: "post-1", Name: "browser", Arguments: string(encoded)})
		if err != nil || toolExecutionDetail(result.Raw) == nil || toolExecutionDetail(result.Raw).Outcome != "unknown" {
			t.Fatalf("unknown result lost: %+v %v", result, err)
		}
	}
	if received.Load() != 1 {
		t.Fatalf("unknown side effect resent: %d requests", received.Load())
	}
}
