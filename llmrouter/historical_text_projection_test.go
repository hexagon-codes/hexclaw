package llmrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/config"
)

type historicalTextCaptureProvider struct {
	requests []llm.CompletionRequest
}

func (*historicalTextCaptureProvider) Name() string { return "mixed" }

func (p *historicalTextCaptureProvider) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.requests = append(p.requests, req)
	return &llm.CompletionResponse{Content: "ok"}, nil
}

func (p *historicalTextCaptureProvider) Stream(_ context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	p.requests = append(p.requests, req)
	return llm.NewStream(strings.NewReader("data: [DONE]\n\n"), llm.StreamOpenAIFormat), nil
}

func (*historicalTextCaptureProvider) Models() []llm.ModelInfo { return nil }
func (*historicalTextCaptureProvider) CountTokens([]llm.Message) (int, error) {
	return 0, nil
}

type historicalTextFixture struct {
	request          llm.CompletionRequest
	messagesBacking  []llm.Message
	historyBacking   []llm.ContentPart
	toolBacking      []llm.ContentPart
	projectedHistory []llm.Message
}

func newHistoricalTextFixture() historicalTextFixture {
	history := []llm.ContentPart{
		llm.NewTextPart("same-message text"),
		llm.NewImageURLPart("data:image/png;base64,b2xk", "high"),
		{Type: " IMAGE_URL ", ImageURL: &llm.ImageURL{URL: "data:image/png;base64,b2xkMg==", Detail: "low"}},
		llm.NewTextPart("unused history capacity"),
	}
	tool := []llm.ContentPart{
		llm.NewTextPart("older tool text"),
		llm.NewImageURLPart("data:image/png;base64,dG9vbA==", "auto"),
		llm.NewTextPart("unused tool capacity"),
	}
	messages := make([]llm.Message, 10)
	messages[0] = llm.Message{Role: llm.RoleSystem, Content: "keep instructions"}
	messages[1] = llm.Message{Role: llm.RoleUser, Content: "older user content", Name: "learner", MultiContent: history[:3]}
	messages[2] = llm.Message{Role: llm.RoleAssistant, Content: "older assistant text", ToolCalls: []llm.ToolCallRef{{ID: "call-old", Name: "math", Arguments: `{"x":24}`}}}
	messages[3] = llm.Message{Role: llm.RoleTool, Name: "math", ToolCallID: "call-old", Content: "24", MultiContent: tool[:2]}
	messages[4] = llm.Message{Role: llm.RoleUser, Content: "current user content", MultiContent: []llm.ContentPart{llm.NewTextPart("current text")}}
	messages[5] = llm.Message{Role: llm.RoleAssistant, Content: "unused message capacity"}
	temperature, topP := 0.25, 0.8
	return historicalTextFixture{
		request: llm.CompletionRequest{
			Model: "text-model", Messages: messages[:5],
			Tools:      []llm.ToolDefinition{llm.NewToolDefinition("math", "local math", nil)},
			ToolChoice: "auto", MaxTokens: 128, Temperature: &temperature, TopP: &topP,
			Stop: []string{"END"}, User: "fixture-owner", Metadata: map[string]any{"tag": "keep"},
			ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		},
		messagesBacking: messages, historyBacking: history, toolBacking: tool,
		projectedHistory: []llm.Message{
			{Role: llm.RoleSystem, Content: "keep instructions"},
			{Role: llm.RoleUser, Content: "older user content", Name: "learner", MultiContent: []llm.ContentPart{
				llm.NewTextPart("same-message text"), llm.NewTextPart("[早前发送的图片]"), llm.NewTextPart("[早前发送的图片]"),
			}},
			{Role: llm.RoleAssistant, Content: "older assistant text", ToolCalls: []llm.ToolCallRef{{ID: "call-old", Name: "math", Arguments: `{"x":24}`}}},
			{Role: llm.RoleTool, Name: "math", ToolCallID: "call-old", Content: "24", MultiContent: []llm.ContentPart{
				llm.NewTextPart("older tool text"), llm.NewTextPart("[早前发送的图片]"),
			}},
			{Role: llm.RoleUser, Content: "current user content", MultiContent: []llm.ContentPart{llm.NewTextPart("current text")}},
		},
	}
}

func historicalTextProvider(t *testing.T, defaultModel string, capture *historicalTextCaptureProvider) hexagon.Provider {
	t.Helper()
	selector := NewWithProviders(config.LLMConfig{
		Default: "mixed",
		Providers: map[string]config.LLMProviderConfig{"mixed": {
			Model: defaultModel, Models: []string{"text-model", "vision-model", "vector-model", "unknown-model"},
			ModelSpecsMode: config.LLMModelSpecsModeExplicit,
			ModelSpecs: []config.LLMProviderModelSpec{
				{ID: "text-model", Capabilities: []string{config.LLMModelCapabilityText}},
				{ID: "vision-model", Capabilities: []string{config.LLMModelCapabilityText, config.LLMModelCapabilityVision}},
				{ID: "vector-model", Capabilities: []string{config.LLMModelCapabilityEmbedding}},
			},
		}},
	}, map[string]hexagon.Provider{"mixed": capture})
	provider, ok := selector.Get("mixed")
	if !ok {
		t.Fatal("configured provider missing")
	}
	return provider
}

func callHistoricalTextProvider(t *testing.T, provider hexagon.Provider, req llm.CompletionRequest, stream bool) error {
	t.Helper()
	if !stream {
		_, err := provider.Complete(context.Background(), req)
		return err
	}
	result, err := provider.Stream(context.Background(), req)
	if err != nil {
		return err
	}
	if result == nil {
		t.Fatal("successful stream is nil")
	}
	if err := result.Close(); err != nil {
		t.Fatalf("close fixture stream: %v", err)
	}
	return nil
}

func historicalTextJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(data)
}

func historicalTextSnapshot(t *testing.T, fixture historicalTextFixture) string {
	t.Helper()
	return historicalTextJSON(t, []any{fixture.request, fixture.messagesBacking, fixture.historyBacking, fixture.toolBacking})
}

func TestTextOnlyHistoryProjectionPreservesRequestAndBackingSlices(t *testing.T) {
	for _, mode := range []struct {
		name   string
		stream bool
	}{{name: "Complete"}, {name: "Stream", stream: true}} {
		t.Run(mode.name, func(t *testing.T) {
			fixture := newHistoricalTextFixture()
			before := historicalTextSnapshot(t, fixture)
			// 默认模型有视觉能力，显式选择的纯文本模型仍须采用自己的能力合同。
			capture := &historicalTextCaptureProvider{}
			provider := historicalTextProvider(t, "vision-model", capture)
			if err := callHistoricalTextProvider(t, provider, fixture.request, mode.stream); err != nil {
				t.Fatalf("text turn with historical images: %v", err)
			}
			if len(capture.requests) != 1 {
				t.Fatalf("transport requests = %d, want 1", len(capture.requests))
			}
			want := fixture.request
			want.Messages = fixture.projectedHistory
			got := capture.requests[0]
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("projected request changed text, roles, tool references, or model parameters:\ngot: %s\nwant: %s", historicalTextJSON(t, got), historicalTextJSON(t, want))
			}
			if after := historicalTextSnapshot(t, fixture); after != before {
				t.Fatal("projection mutated caller request or spare backing-slice capacity")
			}
			// 修改已投影的消息和图片位置，验证其不再与调用方的相应切片共用存储。
			got.Messages[1].Name = "transport-local"
			got.Messages[1].MultiContent[1] = llm.NewTextPart("transport-local placeholder")
			got.Messages[3].MultiContent[1] = llm.NewTextPart("transport-local tool placeholder")
			if after := historicalTextSnapshot(t, fixture); after != before {
				t.Fatal("projected message or content backing slices still alias caller storage")
			}
		})
	}
}

func TestTextOnlyHistoryProjectionKeepsImageAndNonTextGuards(t *testing.T) {
	for _, test := range []struct {
		name    string
		stream  bool
		mutate  func(*historicalTextFixture)
		wantErr bool
		project bool
	}{
		{name: "current_user_image", wantErr: true, mutate: func(f *historicalTextFixture) {
			f.request.Messages[4].MultiContent = append(f.request.Messages[4].MultiContent, llm.NewImageURLPart("data:image/png;base64,bmV3", "auto"))
		}},
		{name: "tool_image_after_latest_user", stream: true, wantErr: true, mutate: func(f *historicalTextFixture) {
			f.request.Messages = append(f.request.Messages,
				llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCallRef{{ID: "call-current", Name: "math", Arguments: `{}`}}},
				llm.Message{Role: llm.RoleTool, ToolCallID: "call-current", MultiContent: []llm.ContentPart{llm.NewImageURLPart("data:image/png;base64,bmV3LXRvb2w=", "auto")}},
			)
		}},
		{name: "no_user_message", wantErr: true, mutate: func(f *historicalTextFixture) {
			f.request.Messages = []llm.Message{{Role: llm.RoleAssistant, MultiContent: []llm.ContentPart{llm.NewImageURLPart("data:image/png;base64,bm8tdXNlcg==", "auto")}}}
		}},
		{name: "vision_model_retains_historical_images", mutate: func(f *historicalTextFixture) {
			f.request.Model = "vision-model"
		}},
		{name: "embedding_only_still_rejected", stream: true, wantErr: true, mutate: func(f *historicalTextFixture) {
			f.request.Model = "vector-model"
		}},
		{name: "unknown_capability_still_rejected", wantErr: true, mutate: func(f *historicalTextFixture) {
			f.request.Model = "unknown-model"
		}},
		{name: "empty_request_model_uses_configured_text_model", project: true, mutate: func(f *historicalTextFixture) {
			f.request.Model = ""
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHistoricalTextFixture()
			test.mutate(&fixture)
			before := historicalTextSnapshot(t, fixture)
			capture := &historicalTextCaptureProvider{}
			err := callHistoricalTextProvider(t, historicalTextProvider(t, "text-model", capture), fixture.request, test.stream)
			if test.wantErr {
				if !errors.Is(err, ErrModelCapabilityMismatch) {
					t.Fatalf("guard error = %v, want model capability mismatch", err)
				}
				if len(capture.requests) != 0 {
					t.Fatal("current image or non-text model reached transport")
				}
			} else {
				if err != nil {
					t.Fatalf("allowed route: %v", err)
				}
				if len(capture.requests) != 1 {
					t.Fatalf("transport requests = %d, want 1", len(capture.requests))
				}
				want := fixture.request
				if test.project {
					want.Messages = fixture.projectedHistory
				}
				if !reflect.DeepEqual(capture.requests[0], want) {
					t.Fatal("route did not preserve its selected model and image contract")
				}
			}
			if after := historicalTextSnapshot(t, fixture); after != before {
				t.Fatal("allowed or rejected route mutated the caller's history")
			}
		})
	}
}

func TestTextOnlyHistoryProjectionDoesNotAccumulateAcrossToolRounds(t *testing.T) {
	fixture := newHistoricalTextFixture()
	capture := &historicalTextCaptureProvider{}
	provider := historicalTextProvider(t, "text-model", capture)
	var previous []string
	for round := 0; round < 3; round++ {
		before := historicalTextSnapshot(t, fixture)
		if err := callHistoricalTextProvider(t, provider, fixture.request, false); err != nil {
			t.Fatalf("tool round %d: %v", round, err)
		}
		if len(capture.requests) != round+1 {
			t.Fatalf("tool round %d produced %d transport requests", round, len(capture.requests))
		}
		want := fixture.request
		want.Messages = append(append([]llm.Message(nil), fixture.projectedHistory...), fixture.request.Messages[5:]...)
		if !reflect.DeepEqual(capture.requests[round], want) {
			t.Fatalf("tool round %d accumulated placeholders or lost tool context", round)
		}
		if after := historicalTextSnapshot(t, fixture); after != before {
			t.Fatalf("tool round %d mutated caller history or backing capacity", round)
		}
		for i, snapshot := range previous {
			if historicalTextJSON(t, capture.requests[i]) != snapshot {
				t.Fatalf("tool round %d changed earlier request %d", round, i)
			}
		}
		previous = append(previous, historicalTextJSON(t, capture.requests[round]))
		if round < 2 {
			callID := fmt.Sprintf("call-round-%d", round+1)
			fixture.request.Messages = append(fixture.request.Messages,
				llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCallRef{{ID: callID, Name: "math", Arguments: `{"x":24}`}}},
				llm.Message{Role: llm.RoleTool, ToolCallID: callID, Content: "24"},
			)
		}
	}
}
