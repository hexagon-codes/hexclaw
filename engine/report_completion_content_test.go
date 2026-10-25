package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/storage"
)

func TestReportCompletionKeepsFullBodyAfterModelSummary(t *testing.T) {
	const report = "## 一、快速解答\n\n答案：12 TB。\n\n## 二、深度讲解\n\n已用的2份是4.8 TB，每份2.4 TB，总共5份。"
	for _, stream := range []bool{false, true} {
		name := "synchronous"
		if stream {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			provider := &teachingResultProvider{toolName: "k12_grade"}
			eng := newEgressGuardedCloudEngine(t, provider, nil)
			if err := eng.skills.Register(&testSkill{name: "k12_grade", result: "天气：晴。报告已准备。"}); err != nil {
				t.Fatal(err)
			}
			eng.SetToolCollector(NewToolCollector(eng.skills, nil, 40))
			eng.SetToolExecutor(NewToolExecutor(eng.skills, nil))
			eng.SetReplyArtifactResolver(func(ctx context.Context, _ *adapter.Message, _ map[string]string) (context.Context, error) {
				return storage.WithAssistantCompletion(ctx, storage.AssistantCompletion{RequiredContent: []string{report, report}}), nil
			})
			msg := &adapter.Message{ID: "complete-report", SessionID: "complete-report-session", UserID: "parent", Platform: adapter.PlatformAPI, Content: "请给出完整讲解，并说明天气。"}
			var body string
			if stream {
				chunks, err := eng.ProcessStream(t.Context(), msg)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range chunks {
					if chunk.Error != nil {
						t.Fatal(chunk.Error)
					}
					if chunk.MessageContent != nil {
						body = chunk.MessageContent.Markdown
					}
				}
			} else {
				reply, err := eng.Process(t.Context(), msg)
				if err != nil {
					t.Fatal(err)
				}
				body = reply.Content
			}
			if strings.Count(body, report) != 1 || !strings.Contains(body, "天气：晴") {
				t.Fatalf("original-turn content omitted or duplicated a result: %q", body)
			}
			messages, err := eng.store.ListMessages(t.Context(), msg.SessionID, 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, message := range messages {
				if message.Role == "assistant" && message.Content == body {
					return
				}
			}
			t.Fatal("the original assistant message did not persist the complete body")
		})
	}
}
