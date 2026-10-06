package llmrouter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/config"
)

func TestProviderFactoryBaseURLUsesSinglePathSeparator(t *testing.T) {
	for _, prefix := range []string{"/v1beta/openai", "/gateway/custom/v1"} {
		for _, suffix := range []string{"", "/", "//"} {
			t.Run(prefix+suffix, func(t *testing.T) {
				paths := make(chan string, 2)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					paths <- r.URL.Path
					w.Header().Set("Content-Type", "application/json")
					if strings.HasSuffix(r.URL.Path, "/embeddings") {
						_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.25,0.75]}]}`))
						return
					}
					_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`))
				}))
				defer server.Close()
				provider := NewProviderFromConfig("openai-compatible", config.LLMProviderConfig{
					BaseURL: server.URL + prefix + suffix,
					APIKey:  "test-only", Model: "test-model",
				})
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				response, err := provider.Complete(ctx, hexagon.CompletionRequest{
					Messages: []hexagon.Message{{Role: hexagon.RoleUser, Content: "hello"}},
				})
				if err != nil || response == nil || response.Content != "OK" {
					t.Fatalf("completion response=%v error=%v", response, err)
				}
				if got := <-paths; got != prefix+"/chat/completions" {
					t.Errorf("completion path=%q, want %q", got, prefix+"/chat/completions")
				}
				embeddingProvider, ok := provider.(llm.EmbeddingProvider)
				if !ok {
					t.Fatal("provider does not implement embedding")
				}
				vectors, err := embeddingProvider.EmbedWithModel(ctx, "test-embedding", []string{"hello"})
				if err != nil || len(vectors) != 1 || len(vectors[0]) != 2 {
					t.Fatalf("embedding vectors=%v error=%v", vectors, err)
				}
				if got := <-paths; got != prefix+"/embeddings" {
					t.Errorf("embedding path=%q, want %q", got, prefix+"/embeddings")
				}
			})
		}
	}
}
