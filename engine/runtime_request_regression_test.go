package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/ai-core/streamx"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/storage"
)

type runtimeRegressionProvider struct {
	requests      chan llm.CompletionRequest
	waitForCancel bool
	streamContent string
	completeCalls atomic.Int32
	streamCalls   atomic.Int32
}

func (p *runtimeRegressionProvider) Name() string { return "test" }
func (p *runtimeRegressionProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock-model"}}
}
func (p *runtimeRegressionProvider) CountTokens([]llm.Message) (int, error) { return 1, nil }
func (p *runtimeRegressionProvider) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.completeCalls.Add(1)
	p.requests <- req
	return &llm.CompletionResponse{Content: "answer", FinishReason: "stop"}, nil
}
func (p *runtimeRegressionProvider) Stream(ctx context.Context, req llm.CompletionRequest) (*streamx.Stream, error) {
	p.streamCalls.Add(1)
	p.requests <- req
	content := p.streamContent
	if content == "" {
		content = "partial answer"
	}
	payload, err := json.Marshal(map[string]any{"id": "runtime-test", "model": "mock-model", "choices": []any{map[string]any{"delta": map[string]any{"content": content}}}})
	if err != nil {
		return nil, err
	}
	first := "data: " + string(payload) + "\n\n"
	if !p.waitForCancel {
		return streamx.NewStreamWithContext(ctx, strings.NewReader(first+"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), streamx.OpenAIFormat), nil
	}
	reader, writer := io.Pipe()
	go func() {
		if _, err := io.WriteString(writer, first); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		<-ctx.Done()
		_ = writer.CloseWithError(ctx.Err())
	}()
	return streamx.NewStreamWithContext(ctx, reader, streamx.OpenAIFormat), nil
}

func TestCancelledPartialKeepsCanonicalContent(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"paired tags", "visible prefix <think>private reasoning</think> visible suffix", "visible prefix  visible suffix"},
		{"unclosed tags", "visible prefix <thinking>private reasoning", "visible prefix"},
		{"only private text", "<reasoning>private reasoning</reasoning>", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &runtimeRegressionProvider{requests: make(chan llm.CompletionRequest, 4), waitForCancel: true, streamContent: tc.raw}
			eng := newEngineWithProvider(t, provider)
			eng.cfg.LLM.Tools.Enabled = "off"
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			msg := &adapter.Message{ID: "cancel-canonical", SessionID: "cancel-canonical-session", UserID: "cancel-user", Platform: adapter.PlatformAPI, Content: "Say hello.", Metadata: map[string]string{"provider": "test", "request_id": "cancel-canonical"}}
			chunks, err := eng.ProcessStream(ctx, msg)
			if err != nil {
				t.Fatal(err)
			}
			for chunk := range chunks {
				if chunk.Content != "" {
					cancel()
				}
			}
			if provider.streamCalls.Load() != 1 || provider.completeCalls.Load() != 0 {
				t.Fatal("cancelled canonicalization replayed the provider")
			}
			records, err := eng.store.ListMessages(t.Context(), msg.SessionID, 20, 0)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, record := range records {
				if record.Role != "assistant" {
					continue
				}
				count++
				if tc.want == "" || record.Content != tc.want || strings.Contains(record.Metadata, "private reasoning") {
					t.Fatalf("non-canonical cancelled record: %+v", record)
				}
				var metadata struct {
					Blocks []adapter.Block `json:"blocks"`
				}
				if err := json.Unmarshal([]byte(record.Metadata), &metadata); err != nil {
					t.Fatal(err)
				}
				if len(metadata.Blocks) != 1 || metadata.Blocks[0].Text != tc.want || metadata.Blocks[0].MessageContent == nil {
					t.Fatalf("non-canonical blocks: %+v", metadata.Blocks)
				}
				if metadata.Blocks[0].MessageContent.Markdown != tc.want {
					t.Fatal("canonical block markdown does not match persisted content")
				}
				if err := metadata.Blocks[0].MessageContent.Validate(); err != nil {
					t.Fatalf("canonical block identity was not rebuilt: %v", err)
				}
			}
			wantCount := 1
			if tc.want == "" {
				wantCount = 0
			}
			if count != wantCount {
				t.Fatalf("assistant count=%d, want %d", count, wantCount)
			}
		})
	}
}

func TestCanonicalizeCancelledPartialPreservesBlockFacts(t *testing.T) {
	const raw = "visible <think>private reasoning</think> tail"
	attachment := messagecontent.AttachmentRef{AssetID: "asset:test-document", MIME: "text/plain", Digest: "sha256:test-digest"}
	source, err := messagecontent.New(messagecontent.ProducerReport, "zh-CN", raw, []messagecontent.AttachmentRef{attachment})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := adapter.RuntimeSnapshot{
		Blocks: []adapter.Block{
			{Type: "text", Text: raw, MessageContent: &source},
			{Type: "tool_use", ID: "call-1", Name: "test_tool"},
			{Type: "retrieval", Retrieval: &adapter.RetrievalActivity{Kind: "knowledge", Status: "completed"}},
		},
		ToolCalls: []adapter.ToolCall{{ID: "call-1", Name: "test_tool", Result: "actual result"}},
	}
	content, normalized, err := canonicalizeCancelledPartial(raw, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if content != "visible  tail" || len(normalized.Blocks) != 3 {
		t.Fatalf("cancelled content or blocks changed unexpectedly: %q %+v", content, normalized.Blocks)
	}
	block := normalized.Blocks[0]
	if block.Text != "visible  tail" || block.MessageContent == nil || block.MessageContent.ProducerKind != messagecontent.ProducerReport || block.MessageContent.Locale != "zh-CN" {
		t.Fatalf("text block provenance changed: %+v", block)
	}
	if len(block.MessageContent.Attachments) != 1 || block.MessageContent.Attachments[0] != attachment {
		t.Fatal("cancelled content lost attachment facts")
	}
	if err := block.MessageContent.Validate(); err != nil {
		t.Fatal(err)
	}
	if source.Markdown != raw || block.MessageContent == &source {
		t.Fatal("canonicalization mutated the original streamed content")
	}
	if normalized.Blocks[1].ID != "call-1" || normalized.Blocks[2].Retrieval.Status != "completed" || len(normalized.ToolCalls) != 1 || normalized.ToolCalls[0].Result != "actual result" {
		t.Fatal("canonicalization changed non-text runtime facts")
	}
}

func TestRuntimeBridgePreservesRequestSampling(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "complete"
		limit := 128
		if streaming {
			name = "stream"
			limit = 32
		}
		t.Run(name, func(t *testing.T) {
			provider := &runtimeRegressionProvider{requests: make(chan llm.CompletionRequest, 4)}
			eng := newEngineWithProvider(t, provider)
			eng.cfg.LLM.Tools.Enabled = "off"
			msg := &adapter.Message{ID: "sampling-" + name, UserID: "sampling-user", Platform: adapter.PlatformAPI, Content: "Say hello.", Metadata: map[string]string{"provider": "test", "request_id": "sampling-" + name, "request_temperature": "0", "request_max_tokens": map[bool]string{false: "128", true: "32"}[streaming]}}
			if streaming {
				chunks, err := eng.ProcessStream(t.Context(), msg)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range chunks {
					if chunk.Error != nil {
						t.Fatal(chunk.Error)
					}
				}
			} else {
				if _, err := eng.Process(t.Context(), msg); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case req := <-provider.requests:
				if req.MaxTokens != limit {
					t.Fatalf("provider MaxTokens=%d, want %d", req.MaxTokens, limit)
				}
				if req.Temperature == nil || *req.Temperature != 0 {
					t.Fatalf("provider Temperature=%v, want explicit zero", req.Temperature)
				}
			default:
				t.Fatal("provider received no request")
			}
		})
	}
}

func TestProcessStreamPersistsCancelledPartialWithoutReplay(t *testing.T) {
	for _, deleteSession := range []bool{false, true} {
		name := "save partial"
		if deleteSession {
			name = "deleted session stays deleted"
		}
		t.Run(name, func(t *testing.T) {
			provider := &runtimeRegressionProvider{requests: make(chan llm.CompletionRequest, 4), waitForCancel: true}
			eng := newEngineWithProvider(t, provider)
			eng.cfg.LLM.Tools.Enabled = "off"
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			msg := &adapter.Message{ID: "runtime-cancel", SessionID: "runtime-cancel-session", UserID: "cancel-user", Platform: adapter.PlatformAPI, Content: "Say hello.", Metadata: map[string]string{"provider": "test", "request_id": "runtime-cancel"}}
			chunks, err := eng.ProcessStream(ctx, msg)
			if err != nil {
				t.Fatal(err)
			}
			received := false
			for chunk := range chunks {
				if chunk.Content != "" && !received {
					received = true
					if deleteSession {
						if err := eng.store.DeleteSession(t.Context(), msg.SessionID); err != nil {
							t.Fatal(err)
						}
					}
					cancel()
				}
			}
			if !received {
				t.Fatal("received no partial response")
			}
			if provider.streamCalls.Load() != 1 || provider.completeCalls.Load() != 0 {
				t.Fatalf("unexpected replay: streams=%d complete=%d", provider.streamCalls.Load(), provider.completeCalls.Load())
			}
			records, err := eng.store.ListMessages(t.Context(), msg.SessionID, 20, 0)
			if err != nil {
				t.Fatal(err)
			}
			if deleteSession {
				if _, err := eng.store.GetSession(t.Context(), msg.SessionID); !errors.Is(err, storage.ErrNotFound) {
					t.Fatalf("deleted session returned: %v", err)
				}
				for _, record := range records {
					if record.Role == "assistant" {
						t.Fatalf("deleted session gained assistant: %+v", record)
					}
				}
				return
			}
			assistantCount := 0
			for _, record := range records {
				if record.Role != "assistant" {
					continue
				}
				assistantCount++
				if record.ID != "runtime-cancel:assistant" || record.Content != "partial answer" {
					t.Fatalf("partial record=%+v", record)
				}
				var meta struct {
					Events []adapter.SequencedRuntimeEvent `json:"runtime_events"`
				}
				if err := json.Unmarshal([]byte(record.Metadata), &meta); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, event := range meta.Events {
					if event.Event.Kind == adapter.RuntimeEventTerminal && event.Event.TerminalStatus == adapter.RuntimeTerminalCancelled {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing cancelled terminal: %s", record.Metadata)
				}
			}
			if assistantCount != 1 {
				t.Fatalf("assistant count=%d, want 1", assistantCount)
			}
		})
	}
}
