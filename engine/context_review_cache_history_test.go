package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexagon"
	mockllm "github.com/hexagon-codes/hexagon/testing/mock"
	"github.com/hexagon-codes/hexclaw/adapter"
)

// 相同追问依赖各自会话中的最新事实，不能仅凭问题正文复用旧答案。
func TestContextReview_CacheUsesCurrentConversationHistory(t *testing.T) {
	for _, tc := range []struct {
		name       string
		newSession bool
	}{
		{name: "updated_fact_in_same_session"},
		{name: "different_history_in_another_session", newSession: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			provider := mockllm.NewLLMProvider("test").WithResponseFn(func(req hexagon.CompletionRequest) (*hexagon.CompletionResponse, error) {
				code := ""
				for _, message := range req.Messages {
					if message.Role != "user" {
						continue
					}
					switch {
					case strings.Contains(message.Content, "本次项目代号是 ORCHID"):
						code = "ORCHID"
					case strings.Contains(message.Content, "本次项目代号是 CEDAR"):
						code = "CEDAR"
					}
				}
				if code == "" {
					return nil, fmt.Errorf("current conversation contains no project code")
				}
				return &hexagon.CompletionResponse{
					Content: "当前项目代号是 " + code + "。",
				}, nil
			})
			eng := newEngineWithProvider(t, provider)

			send := func(id, chatID, sessionID, content string) (*adapter.Message, *adapter.Reply) {
				t.Helper()
				msg := &adapter.Message{
					ID:        id,
					Platform:  adapter.PlatformAPI,
					UserID:    "context-review-user",
					ChatID:    chatID,
					SessionID: sessionID,
					Content:   content,
					Metadata:  map[string]string{"memory": "off"},
				}
				reply, err := eng.Process(ctx, msg)
				if err != nil {
					t.Fatalf("Process(%s): %v", id, err)
				}
				if reply == nil || msg.SessionID == "" {
					t.Fatalf("Process(%s) did not return a reply and session", id)
				}
				return msg, reply
			}

			first, _ := send("seed-orchid", "chat-a", "", "本次项目代号是 ORCHID。")
			const question = "刚才确定的项目代号是什么？"
			_, firstReply := send("ask-orchid", "chat-a", first.SessionID, question)
			if firstReply.Content != "当前项目代号是 ORCHID。" {
				t.Fatalf("first history response = %q, want ORCHID", firstReply.Content)
			}

			chatID, sessionID := "chat-a", first.SessionID
			if tc.newSession {
				chatID, sessionID = "chat-b", ""
			}
			second, _ := send("seed-cedar", chatID, sessionID, "本次项目代号是 CEDAR。")
			if tc.newSession && second.SessionID == first.SessionID {
				t.Fatal("distinct chat fixture reused the first session")
			}

			// 独立读回已保存的事实，区分持久化失败与请求使用了过期回答。
			history, err := eng.sessions.BuildContext(ctx, second.SessionID)
			if err != nil {
				t.Fatalf("read current history: %v", err)
			}
			foundCurrentFact := false
			for _, message := range history {
				if message.Role == "user" && message.Content == "本次项目代号是 CEDAR。" {
					foundCurrentFact = true
				}
			}
			if !foundCurrentFact {
				t.Fatal("current fact was not persisted in the target session")
			}

			_, secondReply := send("ask-cedar", chatID, second.SessionID, question)
			if secondReply.Content != "当前项目代号是 CEDAR。" {
				t.Errorf("follow-up used stale history: got %q, want CEDAR; source=%q, provider_calls=%d",
					secondReply.Content, secondReply.Metadata["source"], provider.CallCount())
			}
		})
	}
}
