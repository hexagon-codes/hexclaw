package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/ai-core/llm/openai"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestK12NonIdempotentLLMContextUnexpectedEOFPhysicalPOSTOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		calls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[`))
	}))
	defer srv.Close()
	provider := openai.New("test", openai.WithBaseURL(srv.URL))

	_, err := provider.Complete(
		k12NonIdempotentLLMContext(context.Background()),
		llm.CompletionRequest{
			Messages: []llm.Message{llm.UserMessage("classify this worksheet")},
		},
	)
	if err == nil {
		t.Fatal("expected truncated-response error")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want unexpected EOF", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("physical POST attempts = %d, want 1", calls.Load())
	}
}

func TestK12UnitSummaryUnknownPhysicalResponseDoesNotResend(t *testing.T) {
	for _, stage := range []string{"resolve", "generate"} {
		t.Run(stage, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = w.Write([]byte(`{"choices":[`))
			}))
			defer server.Close()
			provider := openai.New("synthetic-key", openai.WithBaseURL(server.URL))
			generator, ctx := unitSummaryDeadlineFixture(t, func(ctx context.Context, request hexagon.CompletionRequest) (*hexagon.CompletionResponse, error) {
				return provider.Complete(ctx, request)
			})
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			ctx = egress.WithProviderClientRequestKey(ctx, "same-durable-unit-operation")
			var err error
			if stage == "resolve" {
				_, err = generator.ResolveUnitSummary(ctx, usecase.UnitSummaryResolveInput{})
			} else {
				_, err = generator.GenerateUnitSummary(ctx, usecase.UnitSummaryGenerationInput{})
			}
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("truncated response error=%v, want unexpected EOF", err)
			}
			if requests.Load() != 1 {
				t.Fatalf("one durable unit operation sent %d physical requests after ambiguous response", requests.Load())
			}
		})
	}
}
