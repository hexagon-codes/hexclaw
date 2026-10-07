package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	mockllm "github.com/hexagon-codes/hexagon/testing/mock"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/skill"
)

type originalUserTextProbeSkill struct{ seen chan string }

func (*originalUserTextProbeSkill) Name() string { return "original_user_text_probe" }
func (*originalUserTextProbeSkill) Description() string {
	return "Return the current original user text"
}
func (*originalUserTextProbeSkill) Match(text string) bool {
	return strings.HasPrefix(text, "/original-user-text ")
}
func (*originalUserTextProbeSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("original_user_text_probe", "Return the current original user text", nil)
}
func (s *originalUserTextProbeSkill) Execute(ctx context.Context, _ map[string]any) (*skill.Result, error) {
	text := skill.OriginalUserText(ctx)
	s.seen <- text
	return &skill.Result{Content: text}, nil
}

func TestOriginalUserText_ProcessAndStreamPreserveCurrentInput(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "process"
		if streaming {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			eng := newEngineWithProvider(t, mockllm.NewLLMProvider("test"))
			probe := &originalUserTextProbeSkill{seen: make(chan string, 1)}
			if err := eng.skills.Register(probe); err != nil {
				t.Fatal(err)
			}
			input := "/original-user-text 老师讲到第三单元\n这是本轮原文。"
			msg := &adapter.Message{ID: "original-text-" + name, Platform: adapter.PlatformAPI,
				UserID: "source-test-user", Content: input}
			ctx := skill.WithOriginalUserText(context.Background(), "previous turn text")
			if streaming {
				chunks, err := eng.ProcessStream(ctx, msg)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range chunks {
					if chunk.Error != nil {
						t.Fatal(chunk.Error)
					}
				}
			} else if _, err := eng.Process(ctx, msg); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-probe.seen:
				if got != input {
					t.Fatalf("original text=%q, want current input=%q", got, input)
				}
			default:
				t.Fatal("the existing skill entry did not receive current user text")
			}
		})
	}
}
