package engine

import (
	"context"
	"encoding/json"
	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/ai-core/streamx"
	"github.com/hexagon-codes/hexclaw/adapter"
	"io"
	"strings"
	"sync/atomic"
	"testing"
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
