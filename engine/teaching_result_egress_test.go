package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	hruntime "github.com/hexagon-codes/hexagon/runtime"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/skill"
)

// 模型边界只负责确定性工具选择与结果回传，出口策略、工具循环和持久化使用真实实现。
type teachingResultProvider struct {
	egressCaptureProvider
	toolName      string
	interruptOnce bool
}

func (p *teachingResultProvider) Complete(ctx context.Context, req hexagon.CompletionRequest) (*hexagon.CompletionResponse, error) {
	p.capture(ctx, req)
	for _, msg := range req.Messages {
		if msg.Role != llm.RoleTool {
			continue
		}
		if p.interruptOnce {
			p.interruptOnce = false
			return nil, errors.New("fixture model connection interrupted")
		}
		return &hexagon.CompletionResponse{Content: msg.Content}, nil
	}
	return &hexagon.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "teaching-call", Name: p.toolName, Arguments: `{}`}}}, nil
}

func (p *teachingResultProvider) Stream(ctx context.Context, req hexagon.CompletionRequest) (*hexagon.LLMStream, error) {
	resp, err := p.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	delta := map[string]any{"content": resp.Content}
	reason := "stop"
	if len(resp.ToolCalls) != 0 {
		call := resp.ToolCalls[0]
		delta = map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": call.ID, "type": "function",
			"function": map[string]any{"name": call.Name, "arguments": call.Arguments},
		}}}
		reason = "tool_calls"
	}
	chunk, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}})
	if err != nil {
		return nil, err
	}
	return llm.NewStream(strings.NewReader("data: "+string(chunk)+"\n\ndata: [DONE]\n\n"), llm.StreamOpenAIFormat), nil
}

func TestCloudTeachingResultCompletesReply(t *testing.T) {
	const report = "答案：10天。\n甲乙合作3天完成一半，乙再做7.5天完成另一半。甲每天完成1/10，单独需要10天。"
	for _, streaming := range []bool{false, true} {
		name := "synchronous"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			provider := &teachingResultProvider{toolName: "k12_grade"}
			eng := newEgressGuardedCloudEngine(t, provider, nil)
			if err := eng.skills.Register(&testSkill{name: "k12_grade", result: report}); err != nil {
				t.Fatal(err)
			}
			eng.SetToolCollector(NewToolCollector(eng.skills, nil, 40))
			eng.SetToolExecutor(NewToolExecutor(eng.skills, nil))
			msg := &adapter.Message{ID: "teaching-result", SessionID: "teaching-session", UserID: "parent", Platform: adapter.PlatformAPI, Content: "请讲解这道公路工程题。"}
			var output string
			if streaming {
				ch, err := eng.ProcessStream(t.Context(), msg)
				if err != nil {
					t.Fatal(err)
				}
				output, err = drainStream(t, ch)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				reply, err := eng.Process(t.Context(), msg)
				if err != nil {
					t.Fatal(err)
				}
				output = reply.Content
			}
			if !strings.Contains(output, report) {
				t.Fatalf("complete teaching result was lost: %q", output)
			}
			provider.mu.Lock()
			calls := len(provider.messages)
			provider.mu.Unlock()
			if calls != 2 {
				t.Fatalf("model calls=%d, want tool selection and final answer", calls)
			}
			requireNoEgressClass(t, provider.last(t), egress.ClassRecord)
		})
	}
}

func TestCloudToolFailureContinuesWithoutInventingRecordData(t *testing.T) {
	provider := &teachingResultProvider{toolName: "k12_review", interruptOnce: true}
	eng := newEgressGuardedCloudEngine(t, provider, nil)
	registry := skill.NewRegistry()
	if err := registry.Register(&testSkill{name: "k12_review", err: errors.New("record scope unavailable")}); err != nil {
		t.Fatal(err)
	}
	runner := hruntime.NewRunner(hruntime.Config{
		ProviderSelector: hruntime.StaticProviderSelector{Provider: eng.router.Default(), Name: "cloud-openai", Model: "gpt-x"},
		ToolExecutor:     newRuntimeToolExecutor(NewToolExecutor(registry, nil)),
	})
	req := hruntime.Request{ID: "failed-tool-continuation", Messages: []llm.Message{llm.UserMessage("请检查复习记录。")}, Tools: []llm.ToolDefinition{(&testSkill{name: "k12_review"}).ToolDefinition()}}
	ctx := egress.WithRequest(t.Context(), egress.PurposeGeneralChat, req.ID, egress.ClassGeneral)
	_, err := runRuntimeAttempt(ctx, runner, req, nil, nil)
	var interrupted *hruntime.ProviderCallError
	if !errors.As(err, &interrupted) || len(interrupted.State.ToolCalls) != 1 || !strings.Contains(err.Error(), "fixture model connection interrupted") {
		t.Fatalf("expected model interruption after actual tool error: %v", err)
	}
	requireNoEgressClass(t, provider.last(t), egress.ClassRecord)
	// 同次调用切换模型时会重建出口信封；恢复工具记录也不能把错误提示升级为业务数据。
	ctx = egress.WithRequest(t.Context(), egress.PurposeGeneralChat, req.ID, egress.ClassGeneral)
	result, err := runRuntimeAttempt(ctx, runner, req, interrupted, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "record scope unavailable") || len(result.ToolCalls) != 1 {
		t.Fatalf("error explanation or original tool receipt was lost: %+v", result)
	}
	requireNoEgressClass(t, provider.last(t), egress.ClassRecord)
}

func TestActualHistoricalRecordRetainsClassification(t *testing.T) {
	registry := skill.NewRegistry()
	if err := registry.Register(&testSkill{name: "k12_review", result: "Existing student record"}); err != nil {
		t.Fatal(err)
	}
	ctx := egress.WithRequest(t.Context(), egress.PurposeGeneralChat, "historical-record", egress.ClassGeneral)
	if _, err := NewToolExecutor(registry, nil).Execute(ctx, "k12_review", nil); err != nil {
		t.Fatal(err)
	}
	if err := (&egress.Policy{}).GuardContext(ctx); err == nil {
		t.Fatal("teaching-result correction changed the existing historical-record boundary")
	}
}
