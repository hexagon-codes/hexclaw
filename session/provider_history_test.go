package session

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon/runtime"

	"github.com/hexagon-codes/hexclaw/adapter"
)

func TestBuildContextProjectsCompletedChatToolPairsWithoutChangingObservations(t *testing.T) {
	first := llm.ToolCallRef{ID: "first", Name: "browser", Arguments: `{}`}
	second := llm.ToolCallRef{ID: "second", Name: "browser", Arguments: `{}`}
	for _, test := range []struct {
		name                          string
		complete, continuation, media bool
		wantCalls                     []llm.ToolCallRef
	}{
		{name: "canceled batch", wantCalls: []llm.ToolCallRef{first}},
		{name: "complete batch", complete: true, wantCalls: []llm.ToolCallRef{first, second}},
		{name: "native continuation unchanged", continuation: true, wantCalls: []llm.ToolCallRef{first, second}},
		{name: "completed tool media retained", media: true, wantCalls: []llm.ToolCallRef{first}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, store := newTestManager(t)
			ctx := context.Background()
			sess, err := manager.GetOrCreate(ctx, &adapter.Message{Platform: adapter.PlatformAPI, UserID: "owner", Content: "task"})
			if err != nil {
				t.Fatal(err)
			}
			outputs := []llm.Message{
				{Role: llm.RoleAssistant, Content: "observed prefix", ToolCalls: []llm.ToolCallRef{first, second}},
				{Role: llm.RoleTool, ToolCallID: "first", Content: "canceled observation"},
			}
			if test.continuation {
				outputs[0].ProviderContinuation = &llm.ProviderContinuation{Protocol: "openai.responses", Source: "original", Model: "model", Output: []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"original-call"}`)}}
			}
			if test.media {
				outputs = append(outputs, llm.Message{Role: llm.RoleUser, Name: runtime.ToolMediaMessageName, MultiContent: []llm.ContentPart{llm.NewImageURLPart("data:image/png;base64,c3ludGhldGlj", "auto")}})
			}
			if test.complete {
				outputs = append(outputs, llm.Message{Role: llm.RoleTool, ToolCallID: "second", Content: "second observed"})
			}
			record, err := manager.SaveAssistantReply(WithProviderOutputMessages(ctx, outputs), sess.ID, "observed prefix", AssistantMeta{MessageID: "original-assistant"})
			if err != nil {
				t.Fatal(err)
			}
			before, err := store.GetMessage(ctx, record.ID)
			if err != nil {
				t.Fatal(err)
			}
			messages, err := manager.BuildContext(ctx, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) == 0 || !reflect.DeepEqual(messages[0].ToolCalls, test.wantCalls) {
				t.Fatalf("next-turn calls = %#v, want %#v", messages, test.wantCalls)
			}
			if messages[0].Content != "observed prefix" {
				t.Fatal("observed text changed")
			}
			if test.continuation && messages[0].ProviderContinuation == nil {
				t.Fatal("native continuation was lost")
			}
			if test.continuation && !reflect.DeepEqual(messages[0].ProviderContinuation, outputs[0].ProviderContinuation) {
				t.Fatal("native continuation payload changed")
			}
			media := 0
			for _, message := range messages {
				if message.Name == runtime.ToolMediaMessageName {
					media++
				}
			}
			if test.media && media != 1 {
				t.Fatal("completed tool media was lost")
			}
			after, err := store.GetMessage(ctx, record.ID)
			if err != nil {
				t.Fatal(err)
			}
			if before.Metadata != after.Metadata {
				t.Fatal("next-turn projection rewrote original history")
			}
			original, found, err := restoreProviderOutputHistory(after.Metadata)
			if err != nil || !found || len(original[0].ToolCalls) != 2 {
				t.Fatalf("original observations lost: %#v, %v", original, err)
			}
		})
	}
}
