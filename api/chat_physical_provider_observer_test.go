package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/ai-core/llm/openai"
	"github.com/hexagon-codes/hexclaw/adapter"
	webadapter "github.com/hexagon-codes/hexclaw/adapter/web"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

type physicalProviderObserverEngine struct {
	provider llm.Provider
}

func (e *physicalProviderObserverEngine) Start(context.Context) error  { return nil }
func (e *physicalProviderObserverEngine) Stop(context.Context) error   { return nil }
func (e *physicalProviderObserverEngine) Health(context.Context) error { return nil }

func (e *physicalProviderObserverEngine) Process(ctx context.Context, msg *adapter.Message) (*adapter.Reply, error) {
	chunks, err := e.ProcessStream(ctx, msg)
	if err != nil {
		return nil, err
	}
	var reply adapter.Reply
	for chunk := range chunks {
		reply.Content += chunk.Content
		reply.Metadata = chunk.Metadata
	}
	return &reply, nil
}

func (e *physicalProviderObserverEngine) ProcessStream(ctx context.Context, msg *adapter.Message) (<-chan *adapter.ReplyChunk, error) {
	stream, err := e.provider.Stream(ctx, llm.CompletionRequest{
		Messages: []llm.Message{llm.UserMessage(msg.Content)},
	})
	if err != nil {
		return nil, err
	}
	result, err := stream.Collect()
	if err != nil {
		return nil, err
	}
	chunks := make(chan *adapter.ReplyChunk, 1)
	chunks <- &adapter.ReplyChunk{
		Content:  result.Content,
		Done:     true,
		Metadata: map[string]string{"provider": "test", "model": "test"},
	}
	close(chunks)
	return chunks, nil
}

func TestHandleChatPhysicalProviderObserverPublishesOneStreamAttempt(t *testing.T) {
	t.Setenv("HEXCLAW_TEST_OBSERVE_CHAT_PHYSICAL_CALLS", "1")
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("provider path = %q, want /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"physical-1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()

	cfg := config.DefaultConfig()
	srv := NewServer(cfg, &physicalProviderObserverEngine{
		provider: openai.New("test-key", openai.WithBaseURL(upstream.URL)),
	}, nil, nil)
	t.Cleanup(func() { _ = srv.attachmentStaging.Close() })

	body, err := json.Marshal(ChatRequest{Message: "hello", RequestID: "request-observer-1"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	srv.handleChat(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
	}

	entries, total := srv.logCollector.Query("", "chat", "显式用户请求物理模型调用计数", 10, 0)
	if total != 1 || len(entries) != 1 {
		t.Fatalf("physical-call receipts = %d/%d, want 1/1", len(entries), total)
	}
	if got := entries[0].Fields["physical_provider_calls"]; got != int64(1) && got != int32(1) && got != 1 {
		t.Fatalf("physical_provider_calls = %#v, want 1", got)
	}
}

func TestWebSocketChatDoesNotReplayProviderError(t *testing.T) {
	var physicalCalls atomic.Int32
	const acceptedReply = "Second explicit request accepted."
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if physicalCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":{"message":"Synthetic upstream response"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"accepted\",\"choices\":[{\"delta\":{\"content\":\""+acceptedReply+"\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	cfg := apiCanonicalHistoryConfig()
	cfg.LLM.Cache.Enabled = false
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "chat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	eng := newAPICanonicalHistoryEngine(t, cfg, store, openai.New("synthetic-fixture", openai.WithBaseURL(upstream.URL)))
	defer eng.Stop(context.Background())
	srv := NewServer(cfg, eng, nil, store)
	defer srv.attachmentStaging.Close()
	wa := webadapter.New()
	if err = wa.Start(t.Context(), eng.Process); err != nil {
		t.Fatal(err)
	}
	defer wa.Stop(context.Background())
	wa.SetStreamHandler(eng.ProcessStream)
	srv.SetWebSocketHandler(wa.Handler())
	srv.SetStreamStateProvider(wa)
	app := httptest.NewServer(srv.routes())
	defer app.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(app.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{
		"Origin":        {"tauri://localhost"},
		"Authorization": {"Bearer " + cfg.Server.APIToken},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	for _, tc := range []struct {
		id, prompt, reply string
		wantError         bool
		wantCalls         int32
	}{
		{id: "ws-upstream-error", prompt: "First request", wantError: true, wantCalls: 1},
		{id: "ws-explicit-next", prompt: "Second request", reply: acceptedReply, wantCalls: 2},
	} {
		if err = wsjson.Write(ctx, conn, map[string]string{
			"type": "message", "request_id": tc.id, "content": tc.prompt, "provider": "test", "model": "mock-model",
		}); err != nil {
			t.Fatal(err)
		}
		var content strings.Builder
		for {
			var frame struct {
				Type, Content string
				Done          bool
				RequestID     string `json:"request_id"`
			}
			if err = wsjson.Read(ctx, conn, &frame); err != nil {
				t.Fatalf("%s: %v", tc.id, err)
			}
			if frame.RequestID != tc.id {
				continue
			}
			if frame.Type != "error" {
				content.WriteString(frame.Content)
			}
			if frame.Type != "error" && !frame.Done {
				continue
			}
			if got := physicalCalls.Load(); got != tc.wantCalls || (frame.Type == "error") != tc.wantError || content.String() != tc.reply {
				t.Fatalf("%s: terminal=%s physical calls=%d content=%q", tc.id, frame.Type, got, content.String())
			}
			break
		}
	}
}

func TestHandleChatDoesNotReplayNonIdempotentProviderStreamAfterRetryableStatus(t *testing.T) {
	t.Setenv("HEXCLAW_TEST_OBSERVE_CHAT_PHYSICAL_CALLS", "1")
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		if upstreamCalls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary upstream failure"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"must-not-be-consumed\",\"choices\":[{\"delta\":{\"content\":\"unexpected success\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()

	cfg := config.DefaultConfig()
	srv := NewServer(cfg, &physicalProviderObserverEngine{
		provider: openai.New("test-key", openai.WithBaseURL(upstream.URL)),
	}, nil, nil)
	t.Cleanup(func() { _ = srv.attachmentStaging.Close() })

	body, err := json.Marshal(ChatRequest{Message: "hello", RequestID: "request-no-replay-1"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	srv.handleChat(recorder, req)
	if recorder.Code == http.StatusOK {
		t.Fatalf("status = %d, body = %s; retryable first response must not be replayed", recorder.Code, recorder.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
	}

	entries, total := srv.logCollector.Query("", "chat", "显式用户请求物理模型调用计数", 10, 0)
	if total != 1 || len(entries) != 1 {
		t.Fatalf("physical-call receipts = %d/%d, want 1/1", len(entries), total)
	}
	if got := entries[0].Fields["physical_provider_calls"]; got != int64(1) && got != int32(1) && got != 1 {
		t.Fatalf("physical_provider_calls = %#v, want 1", got)
	}
}
