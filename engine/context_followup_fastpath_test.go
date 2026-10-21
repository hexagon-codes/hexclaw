package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	mockllm "github.com/hexagon-codes/hexagon/testing/mock"
	"github.com/hexagon-codes/hexclaw/adapter"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
)

type conversationStreamProvider struct{ hexagon.Provider }

func (p conversationStreamProvider) Stream(ctx context.Context, req hexagon.CompletionRequest) (*hexagon.LLMStream, error) {
	response, err := p.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	frame, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": response.Content}, "finish_reason": "stop"}}})
	if err != nil {
		return nil, err
	}
	return llm.NewStream(strings.NewReader("data: "+string(frame)+"\n\ndata: [DONE]\n\n"), llm.StreamOpenAIFormat), nil
}

func TestConversationFollowupUsesHistoryBeforeKeywordSkill(t *testing.T) {
	for _, mode := range []string{"sync", "stream"} {
		for _, channel := range []string{"ordinary", "named", "k12", "dingtalk-k12"} {
			t.Run(mode+"/"+channel, func(t *testing.T) {
				provider := mockllm.NewLLMProvider("test").WithResponseFn(func(req hexagon.CompletionRequest) (*hexagon.CompletionResponse, error) {
					last := req.Messages[len(req.Messages)-1].Content
					if strings.Contains(last, "今天什么天气") {
						return &hexagon.CompletionResponse{Content: "杭州：小雨。"}, nil
					}
					city := ""
					for _, msg := range req.Messages[:len(req.Messages)-1] {
						if msg.Role == "user" && strings.Contains(msg.Content, "杭州") {
							city = "杭州"
						}
						if msg.Role == "user" && strings.Contains(msg.Content, "改查上海") {
							city = "上海"
						}
					}
					if city == "" {
						return nil, fmt.Errorf("follow-up request lost its city history")
					}
					return &hexagon.CompletionResponse{Content: city + "上下文已保留"}, nil
				})
				reg := skill.NewRegistry()
				for _, s := range []skill.Skill{builtin.NewWeatherSkill(), builtin.NewSearchSkill(), builtin.NewTranslateSkill()} {
					if err := reg.Register(s); err != nil {
						t.Fatal(err)
					}
				}
				eng := newEngineWithProviderAndSkills(t, conversationStreamProvider{provider}, reg)
				name := "default"
				if channel != "ordinary" {
					name = "context-tutor"
					d := agentrouter.New()
					metadata := map[string]string{}
					if strings.Contains(channel, "k12") {
						metadata["scenario"] = "k12-tutor"
					}
					if err := d.Register(agentrouter.AgentConfig{Name: name, Provider: "test", Model: "mock-model", SystemPrompt: "按本会话当前事实回答。", Metadata: metadata}); err != nil {
						t.Fatal(err)
					}
					eng.SetAgentRouter(d)
				}
				platform := adapter.PlatformAPI
				if channel == "dingtalk-k12" {
					platform = adapter.PlatformDingtalk
				}
				sessionID := ""
				send := func(index int, text string) string {
					t.Helper()
					msg := &adapter.Message{ID: fmt.Sprintf("followup-%d", index), UserID: "context-parent", Platform: platform, InstanceID: "context-instance", ChatID: "context-chat", SessionID: sessionID, Content: text, Metadata: map[string]string{"pinned_agent": name, "memory": "off", "knowledge": "off"}}
					var content string
					if mode == "sync" {
						reply, err := eng.Process(context.Background(), msg)
						if err != nil {
							t.Fatal(err)
						}
						content = reply.Content
					} else {
						chunks, err := eng.ProcessStream(context.Background(), msg)
						if err != nil {
							t.Fatal(err)
						}
						for chunk := range chunks {
							if chunk.Error != nil {
								t.Fatal(chunk.Error)
							}
							content += chunk.Content
						}
					}
					sessionID = msg.SessionID
					return content
				}
				// 首轮用真实会话持久接口播种，避免天气公网依赖掩盖追问路由。
				seed := &adapter.Message{UserID: "context-parent", Platform: platform, InstanceID: "context-instance", ChatID: "context-chat"}
				sess, err := eng.sessions.GetOrCreate(t.Context(), seed)
				if err != nil {
					t.Fatal(err)
				}
				sessionID = sess.ID
				if err := eng.sessions.SaveUserMessage(t.Context(), sessionID, &adapter.Message{ID: "weather-seed", Content: "杭州今天什么天气？"}); err != nil {
					t.Fatal(err)
				}
				if err := eng.sessions.SaveAssistantMessage(t.Context(), sessionID, "杭州：小雨。"); err != nil {
					t.Fatal(err)
				}
				for i, text := range []string{"下雨吗？", "搜索这个城市明天的天气", "翻译刚才的回答"} {
					if got := send(i, text); got != "杭州上下文已保留" {
						t.Fatalf("%q: %q", text, got)
					}
				}
				send(3, "改查上海，接下来都指上海。")
				if got := send(4, "下雨吗？"); got != "上海上下文已保留" {
					t.Fatalf("city switch: %q", got)
				}
			})
		}
	}
}

func TestConversationFastPathRespectsToolsOff(t *testing.T) {
	provider := mockllm.NewLLMProvider("test").AddResponse("tools remain disabled")
	reg := skill.NewRegistry()
	if err := reg.Register(builtin.NewWeatherSkill()); err != nil {
		t.Fatal(err)
	}
	eng := newEngineWithProviderAndSkills(t, provider, reg)
	reply, err := eng.Process(t.Context(), &adapter.Message{ID: "tools-off-weather", UserID: "settings-parent", Platform: adapter.PlatformAPI, Content: "杭州天气", Metadata: map[string]string{"tools_enabled": "off"}})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Content != "tools remain disabled" || len(reply.ToolCalls) != 0 {
		t.Fatalf("disabled tools bypassed: %+v", reply)
	}
}
