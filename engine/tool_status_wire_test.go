package engine

import (
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	hruntime "github.com/hexagon-codes/hexagon/runtime"
	"github.com/hexagon-codes/hexclaw/skill"
)

// TestRuntimeToolCallsToAdapter_StatusDuration 钉死下沉透传契约：
// hexagon 在执行点产出的 Status/DurationMs 必须经 wire 透到客户端，不得在转换时丢弃。
func TestRuntimeToolCallsToAdapter_StatusDuration(t *testing.T) {
	calls := []hruntime.ToolCallRecord{
		{
			ID:        "tc1",
			Name:      "weather",
			Arguments: `{"location":"杭州"}`,
			Result: hruntime.ToolResult{
				Content:    "🌍 杭州 27°C",
				Status:     hruntime.ToolStatusSuccess,
				DurationMs: 1234,
			},
		},
		{
			ID:   "tc2",
			Name: "glob",
			Result: hruntime.ToolResult{
				Content:    "Error: path rejected",
				Error:      "path rejected",
				Status:     hruntime.ToolStatusError,
				DurationMs: 7,
			},
		},
	}

	out := runtimeToolCallsToAdapter(calls)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}

	if out[0].Status != "success" {
		t.Errorf("out[0].Status = %q, want success", out[0].Status)
	}
	if out[0].DurationMs != 1234 {
		t.Errorf("out[0].DurationMs = %d, want 1234", out[0].DurationMs)
	}
	if out[0].MessageContent == nil || out[0].MessageContent.ProducerKind != "tool" || out[0].MessageContent.Markdown != out[0].Result {
		t.Fatalf("out[0] omitted canonical tool output: %#v", out[0])
	}
	if out[1].Status != "error" {
		t.Errorf("out[1].Status = %q, want error", out[1].Status)
	}
	if out[1].DurationMs != 7 {
		t.Errorf("out[1].DurationMs = %d, want 7", out[1].DurationMs)
	}
}

func TestRuntimeToolExecutorProducesTerminalStatus(t *testing.T) {
	registry := skill.NewRegistry()
	if err := registry.Register(&syncChildToolPolicySkill{name: "weather"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, tool, arguments string
		available             bool
		want                  string
	}{
		{"completed", "weather", `{}`, true, "success"},
		{"not registered", "missing-tool", `{}`, true, "error"},
		{"invalid arguments", "weather", `{`, true, "error"},
		{"unavailable executor", "weather", `{}`, false, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var executor *ToolExecutor
			if tc.available {
				executor = NewToolExecutor(registry, nil)
			}
			result, err := newRuntimeToolExecutor(executor).Execute(t.Context(), llm.ToolCall{ID: tc.name, Name: tc.tool, Arguments: tc.arguments})
			if err != nil {
				t.Fatal(err)
			}
			calls := runtimeToolCallsToAdapter([]hruntime.ToolCallRecord{{ID: tc.name, Name: tc.tool, Result: result}})
			if len(calls) != 1 || calls[0].Status != tc.want {
				t.Fatalf("actual terminal status lost: calls=%+v want=%s", calls, tc.want)
			}
		})
	}
}
