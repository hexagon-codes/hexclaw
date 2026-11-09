package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	enginepkg "github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/session"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/streamstate"
)

// 错误终态必须保留已观察正文和工具回执，原HTTP错误及SSE错误语义均不变。
func TestChatFailureProjectsObservedResult(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "nonzero"
		cause := errors.New("exit status 7")
		terminal := adapter.RuntimeTerminalFailed
		if cancelled {
			name, cause, terminal = "cancelled", context.Canceled, adapter.RuntimeTerminalCancelled
		}
		t.Run(name, func(t *testing.T) {
			content := "partial-output; exit code: 7; artifact /api/v1/files/generated/stdout.txt"
			chunk := &adapter.ReplyChunk{Content: content, Done: true, Error: cause, AssistantMessageID: "observed-assistant", BackendMessageID: "observed-assistant", MessageID: "observed-assistant", MessageContent: canonicalChatContent(content, nil), Metadata: map[string]string{"producer_kind": "skill"}, ToolCalls: []adapter.ToolCall{{ID: "original-call", Name: "shell", Status: "error", Result: content}}, RuntimeEvent: &adapter.RuntimeEvent{Kind: adapter.RuntimeEventTerminal, TerminalStatus: terminal}}
			eng := &canonicalReceiptStreamEngine{mockEngine: &mockEngine{}, chunks: []*adapter.ReplyChunk{chunk}}
			srv := NewServer(config.DefaultConfig(), eng, nil, nil)
			w := httptest.NewRecorder()
			srv.handleChat(w, httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(`{"message":"/sh task","session_id":"session-original","request_id":"request-`+name+`"}`)))
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("failure status=%d, body=%s", w.Code, w.Body.String())
			}
			var body struct {
				Error string `json:"error"`
				ChatResponse
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error == "" || body.Reply != content || body.MessageContent == nil || body.MessageContent.Markdown != content || len(body.ToolCalls) != 1 || body.ToolCalls[0].Status != "error" || body.AssistantMessageID != "observed-assistant" {
				t.Fatalf("failed HTTP result lost: %s", w.Body.String())
			}
			w = httptest.NewRecorder()
			srv.handleChatSSE(context.Background(), w, &adapter.Message{}, &sseStreamEngine{chunks: []*adapter.ReplyChunk{chunk}}, time.Now())
			if strings.Contains(w.Body.String(), "[DONE]") {
				t.Fatal("failure became successful SSE completion")
			}
			var frame struct {
				Error        string                `json:"error"`
				Content      string                `json:"content"`
				ToolCalls    []adapter.ToolCall    `json:"tool_calls"`
				RuntimeEvent *adapter.RuntimeEvent `json:"runtime_event"`
			}
			if err := json.Unmarshal([]byte(sseFrames(w.Body.String())[0]), &frame); err != nil {
				t.Fatal(err)
			}
			if frame.Error == "" || frame.Content != content || len(frame.ToolCalls) != 1 || frame.ToolCalls[0].Status != "error" || frame.RuntimeEvent == nil || frame.RuntimeEvent.TerminalStatus != terminal {
				t.Fatalf("failed SSE result lost: %s", w.Body.String())
			}
		})
	}
}

type llmErrorWire struct {
	Error     string `json:"error"`
	Code      string `json:"code"`
	Retryable *bool  `json:"retryable"`
	Done      bool   `json:"done"`
}

func taxonomyErrorCases() []struct {
	name          string
	err           error
	wantStatus    int
	wantCode      string
	wantRetryable bool
} {
	return []struct {
		name          string
		err           error
		wantStatus    int
		wantCode      string
		wantRetryable bool
	}{
		{
			name:          "capability mismatch",
			err:           fmt.Errorf("engine: %w", llmrouter.ErrModelCapabilityMismatch),
			wantStatus:    http.StatusUnprocessableEntity,
			wantCode:      "MODEL_CAPABILITY_MISMATCH",
			wantRetryable: false,
		},
		{
			name: "provider rate limited",
			err: fmt.Errorf("engine: %w", &llm.ProviderError{
				Provider: "test", StatusCode: http.StatusTooManyRequests,
			}),
			wantStatus:    http.StatusTooManyRequests,
			wantCode:      "UPSTREAM_RATE_LIMITED",
			wantRetryable: true,
		},
		{
			name: "provider pool exhausted",
			err: fmt.Errorf("engine: %w", &llm.ProviderError{
				Provider: "test", StatusCode: http.StatusServiceUnavailable,
				Body: `{"error":{"type":"pool_exhausted"}}`,
			}),
			wantStatus:    http.StatusServiceUnavailable,
			wantCode:      "UPSTREAM_POOL_EXHAUSTED",
			wantRetryable: true,
		},
		{
			name: "provider generic service unavailable",
			err: fmt.Errorf("engine: %w", &llm.ProviderError{
				Provider: "test", StatusCode: http.StatusServiceUnavailable,
				Body: `{"error":{"type":"api_error","message":"No available accounts"}}`,
			}),
			wantStatus:    http.StatusServiceUnavailable,
			wantCode:      "UPSTREAM_UNAVAILABLE",
			wantRetryable: true,
		},
		{
			name: "provider unavailable",
			err: fmt.Errorf("engine: %w", &llm.ProviderError{
				Provider: "test", StatusCode: http.StatusBadGateway,
			}),
			wantStatus:    http.StatusServiceUnavailable,
			wantCode:      "UPSTREAM_UNAVAILABLE",
			wantRetryable: true,
		},
	}
}

func TestHandleChatLLMErrorsUseMachineCodesAndHTTPStatuses(t *testing.T) {
	for _, tt := range taxonomyErrorCases() {
		t.Run(tt.name, func(t *testing.T) {
			eng := &mockEngine{err: tt.err}
			srv := NewServer(config.DefaultConfig(), eng, nil, nil)
			w := httptest.NewRecorder()

			srv.handleChat(w, httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(`{"message":"hello"}`)))

			if w.Code != tt.wantStatus {
				t.Fatalf("status=%d, want %d; body=%s", w.Code, tt.wantStatus, w.Body.String())
			}
			var body llmErrorWire
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Code != tt.wantCode {
				t.Fatalf("code=%q, want %q; body=%s", body.Code, tt.wantCode, w.Body.String())
			}
			if body.Retryable == nil || *body.Retryable != tt.wantRetryable {
				t.Fatalf("retryable=%v, want %t; body=%s", body.Retryable, tt.wantRetryable, w.Body.String())
			}
		})
	}
}

func TestHandleChatSSELLMErrorFramesRetainMachineCodeAfterStreamOpens(t *testing.T) {
	for _, tt := range taxonomyErrorCases() {
		t.Run(tt.name, func(t *testing.T) {
			eng := &sseStreamEngine{chunks: []*adapter.ReplyChunk{
				{Content: "partial"},
				{Error: tt.err, Done: true},
			}}
			w := httptest.NewRecorder()

			(&Server{}).handleChatSSE(context.Background(), w, &adapter.Message{}, eng, time.Now())

			if w.Code != http.StatusOK {
				t.Fatalf("opened SSE status=%d, want 200", w.Code)
			}
			frames := sseFrames(w.Body.String())
			if len(frames) != 2 {
				t.Fatalf("frames=%d, want content plus error; body=%q", len(frames), w.Body.String())
			}
			var frame llmErrorWire
			if err := json.Unmarshal([]byte(frames[1]), &frame); err != nil {
				t.Fatalf("decode error frame: %v; frame=%s", err, frames[1])
			}
			if !frame.Done || frame.Code != tt.wantCode {
				t.Fatalf("frame=%+v, want done=true code=%q", frame, tt.wantCode)
			}
			if frame.Retryable == nil || *frame.Retryable != tt.wantRetryable {
				t.Fatalf("retryable=%v, want %t; frame=%s", frame.Retryable, tt.wantRetryable, frames[1])
			}
		})
	}
}

func TestLLMErrorPayloadDoesNotExposeRawProviderBody(t *testing.T) {
	err := fmt.Errorf("engine: %w", &llm.ProviderError{
		Provider:   "test",
		StatusCode: http.StatusServiceUnavailable,
		Status:     "503 Service Unavailable",
		Body:       `{"error":{"type":"pool_exhausted","message":"Temporarily unavailable","internal_marker":"must-not-leak"}}`,
	})

	payload, marshalErr := json.Marshal(llmErrorPayload(err))
	if marshalErr != nil {
		t.Fatalf("marshal payload: %v", marshalErr)
	}
	if strings.Contains(string(payload), "internal_marker") || strings.Contains(string(payload), "must-not-leak") {
		t.Fatalf("raw provider body leaked: %s", payload)
	}
}

// 同一权威终态的空正文与二进制附件在恢复和错误聚合中均不可被预览替换。
func failedCanonicalArtifactFixture(t *testing.T) (*messagecontent.MessageContent, *messagecontent.RenderManifest) {
	t.Helper()
	ref := messagecontent.AttachmentRef{AssetID: "review-artifact", Name: "stdout.bin", MIME: "application/octet-stream", Digest: "sha256:" + strings.Repeat("b", 64), AltText: "stdout.bin"}
	c, err := messagecontent.New(messagecontent.ProducerSkill, "en", "", []messagecontent.AttachmentRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	m, err := messagecontent.BuildManifest(c, messagecontent.RenderRequest{Surface: messagecontent.SurfaceDesktop, RendererVersion: "api-markdown-v1", Capabilities: messagecontent.CapabilitySnapshot{Markdown: true, TeXMath: true, Attachments: true}, Parts: []messagecontent.RenderPart{{Kind: messagecontent.PartArtifact, ArtifactRef: ref.AssetID, ArtifactDigest: ref.Digest, AltText: ref.AltText}}})
	if err != nil {
		t.Fatal(err)
	}
	return &c, &m
}
func TestStreamSnapshotKeepsCanonicalArtifact(t *testing.T) {
	c, m := failedCanonicalArtifactFixture(t)
	r := streamstate.NewRegistry(time.Minute)
	r.Start("review-user", "review-session", "review-request")
	r.Append("review-request", &adapter.ReplyChunk{Content: "", Done: true, Error: context.Canceled, MessageContent: c, RenderManifest: m, AssistantMessageID: "review-assistant", BackendMessageID: "review-assistant", MessageID: "review-assistant"})
	r.Cancel("review-request")
	s := &Server{streamStates: r}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/streams/{request_id}", s.handleGetStreamSnapshot)
	peer := httptest.NewServer(mux)
	defer peer.Close()
	resp, err := http.Get(peer.URL + "/api/v1/streams/review-request?source=websocket&user_id=review-user")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	t.Logf("HTTP=%d status=%s canonical=%t manifest=%t", resp.StatusCode, got["status"], len(got["message_content"]) > 0, len(got["render_manifest"]) > 0)
	if len(got["message_content"]) == 0 || len(got["render_manifest"]) == 0 {
		t.Fatal("real stream snapshot lost terminal canonical attachment and render manifest")
	}
}
func TestChatFailureEmptyCanonicalOverridesPreview(t *testing.T) {
	c, m := failedCanonicalArtifactFixture(t)
	eng := &canonicalReceiptStreamEngine{mockEngine: &mockEngine{}, chunks: []*adapter.ReplyChunk{
		{Content: "older partial preview"},
		{Content: "", Done: true, Error: errors.New("exit status 7"), MessageContent: c, RenderManifest: m, Metadata: map[string]string{"producer_kind": "skill"}},
	}}
	s := NewServer(config.DefaultConfig(), eng, nil, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/chat", s.handleChat)
	peer := httptest.NewServer(mux)
	defer peer.Close()
	resp, err := http.Post(peer.URL+"/api/v1/chat", "application/json", strings.NewReader(`{"message":"/sh controlled","session_id":"review-session","request_id":"review-canonical-empty"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Reply          string                         `json:"reply"`
		MessageContent *messagecontent.MessageContent `json:"message_content"`
		RenderManifest *messagecontent.RenderManifest `json:"render_manifest"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.MessageContent == nil {
		t.Fatal("canonical absent")
	}
	t.Logf("HTTP=%d reply=%q canonical_markdown=%q attachments=%d", resp.StatusCode, got.Reply, got.MessageContent.Markdown, len(got.MessageContent.Attachments))
	if resp.StatusCode != 500 || got.MessageContent.Markdown != "" || len(got.MessageContent.Attachments) != 1 {
		t.Fatal("HTTP failure rewrote authoritative empty canonical content from earlier preview")
	}
}

// 已完成工具后上游失败，只保存本次观察及真实failed历史，不重派发或生成成功终稿。
type observedFailureProvider struct{ calls atomic.Int32 }

func (*observedFailureProvider) Name() string { return "test" }
func (*observedFailureProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "mock-model", Name: "Mock Model"}}
}
func (*observedFailureProvider) CountTokens([]llm.Message) (int, error) { return 1, nil }
func (p *observedFailureProvider) Complete(context.Context, hexagon.CompletionRequest) (*hexagon.CompletionResponse, error) {
	if p.calls.Add(1) > 1 {
		return nil, &llm.ProviderError{Provider: "test", StatusCode: 500}
	}
	return &hexagon.CompletionResponse{Content: "Observed step. ", ToolCalls: []llm.ToolCall{{ID: "normal-observed-call", Name: "web_search", Arguments: `{"q":"synthetic evidence"}`}}}, nil
}
func (p *observedFailureProvider) Stream(context.Context, hexagon.CompletionRequest) (*hexagon.LLMStream, error) {
	if p.calls.Add(1) > 1 {
		return nil, &llm.ProviderError{Provider: "test", StatusCode: 500}
	}
	return llm.NewStream(strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Observed step. \"}}]}\n\n"+
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"normal-observed-call\",\"type\":\"function\",\"function\":{\"name\":\"web_search\",\"arguments\":\"{\\\"q\\\":\\\"synthetic evidence\\\"}\"}}]}}]}\n\n"+
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+"data: [DONE]\n\n"), llm.StreamOpenAIFormat), nil
}

type observedFailureSkill struct{ calls atomic.Int32 }

func (*observedFailureSkill) Name() string        { return "web_search" }
func (*observedFailureSkill) Description() string { return "Return an observed synthetic tool receipt" }
func (*observedFailureSkill) Match(string) bool   { return false }
func (*observedFailureSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("web_search", "Return an observed synthetic tool receipt", &llm.Schema{Type: "object", Properties: map[string]*llm.Schema{"q": {Type: "string"}}, Required: []string{"q"}})
}
func (s *observedFailureSkill) Execute(context.Context, map[string]any) (*skill.Result, error) {
	s.calls.Add(1)
	return &skill.Result{Content: "observed operation completed exactly once"}, nil
}
func TestNormalAgentFailurePersistsObservedHistory(t *testing.T) {
	st, err := sqlitefixture.New(filepath.Join(t.TempDir(), "normal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Compaction.Enabled = false
	cfg.Knowledge.Enabled = false
	cfg.FileMemory.Enabled = false
	cfg.FileMemory.AutoMemory = "off"
	cfg.LLM.Tools.Enabled = "on"
	cfg.LLM.Default = "test"
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"test": {Model: "mock-model", Models: []string{"mock-model"}}}
	provider := &observedFailureProvider{}
	registry := skill.NewRegistry()
	observed := &observedFailureSkill{}
	if err := registry.Register(observed); err != nil {
		t.Fatal(err)
	}
	router := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"test": provider})
	eng := enginepkg.NewReActEngine(cfg, router, st, registry)
	eng.SetToolExecutor(enginepkg.NewToolExecutor(registry, nil))
	eng.SetToolCollector(enginepkg.NewToolCollector(registry, nil, 20))
	eng.SetSessionLock(session.NewSessionLock())
	if err := eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop(context.Background())
	srv := NewServer(cfg, eng, nil, st)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/chat", srv.handleChat)
	peer := httptest.NewServer(mux)
	defer peer.Close()
	resp, err := http.Post(peer.URL+"/api/v1/chat", "application/json", strings.NewReader(`{"message":"Search then summarize with the provided tool","provider":"test","user_id":"normal-review-user","request_id":"normal-review-request","metadata":{"skills":"web_search","agent_mode":"agent"}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		ChatResponse
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	records, err := st.ListMessages(context.Background(), got.SessionID, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	assistantCount, historyTools := 0, 0
	for _, m := range records {
		if m.Role != "assistant" {
			continue
		}
		assistantCount++
		var meta struct {
			ToolCalls []json.RawMessage `json:"tool_calls"`
		}
		if err := json.Unmarshal([]byte(m.Metadata), &meta); err != nil {
			t.Fatal(err)
		}
		historyTools += len(meta.ToolCalls)
	}
	t.Logf("http=%d provider_calls=%d physical_tools=%d response_tools=%d response_blocks=%d error_code=%s snapshot_error=%q history_messages=%d assistant_messages=%d history_tools=%d", resp.StatusCode, provider.calls.Load(), observed.calls.Load(), len(got.ToolCalls), len(got.Blocks), got.Code, got.Metadata["runtime_snapshot_error"], len(records), assistantCount, historyTools)
	if resp.StatusCode < 400 || provider.calls.Load() != 2 || observed.calls.Load() != 1 || len(got.ToolCalls) != 1 {
		t.Fatal("fixture did not reach one observed tool followed by provider failure")
	}
	if assistantCount != 1 || historyTools != 1 {
		t.Fatal("normal provider failure lost already-completed tool facts from durable session history")
	}
	if resp.StatusCode != 503 || got.Code != "UPSTREAM_UNAVAILABLE" || got.Metadata["runtime_snapshot_error"] != "" || got.Metadata["backend_message_id"] == "" || got.Metadata["persist_error"] != "" {
		t.Fatal("provider failure classification or actual save marker changed")
	}
	for _, m := range records {
		if m.Role == "assistant" && !strings.Contains(m.Metadata, `"terminal_status":"failed"`) {
			t.Fatal("failed history became successful")
		}
	}
}

func TestNormalAgentSyncFailurePersistsObservedHistory(t *testing.T) {
	st, err := sqlitefixture.New(filepath.Join(t.TempDir(), "normal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Compaction.Enabled = false
	cfg.Knowledge.Enabled = false
	cfg.FileMemory.Enabled = false
	cfg.FileMemory.AutoMemory = "off"
	cfg.LLM.Tools.Enabled = "on"
	cfg.LLM.Default = "test"
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"test": {Model: "mock-model", Models: []string{"mock-model"}}}
	provider := &observedFailureProvider{}
	registry := skill.NewRegistry()
	observed := &observedFailureSkill{}
	if err := registry.Register(observed); err != nil {
		t.Fatal(err)
	}
	router := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"test": provider})
	eng := enginepkg.NewReActEngine(cfg, router, st, registry)
	eng.SetToolExecutor(enginepkg.NewToolExecutor(registry, nil))
	eng.SetToolCollector(enginepkg.NewToolCollector(registry, nil, 20))
	eng.SetSessionLock(session.NewSessionLock())
	if err := eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop(context.Background())

	msg := &adapter.Message{ID: "sync-observed-failure", Platform: adapter.PlatformAPI, UserID: "normal-sync-user", Content: "Search then summarize with the provided tool", Metadata: map[string]string{"provider": "test", "request_id": "normal-sync-request", "skills": "web_search", "agent_mode": "agent"}}
	reply, executionErr := eng.Process(context.Background(), msg)
	if executionErr == nil || reply == nil || len(reply.ToolCalls) != 1 || reply.ToolCalls[0].Status != "success" || provider.calls.Load() != 2 || observed.calls.Load() != 1 {
		t.Fatalf("sync failure facts lost or command repeated: reply=%#v err=%v provider=%d tool=%d", reply, executionErr, provider.calls.Load(), observed.calls.Load())
	}
	rows, err := st.ListMessages(context.Background(), msg.SessionID, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	assistants := 0
	for _, row := range rows {
		if row.Role == "assistant" {
			assistants++
			if !strings.Contains(row.Metadata, `"terminal_status":"failed"`) || !strings.Contains(row.Metadata, `"name":"web_search"`) {
				t.Fatal("sync failed history lost tool/terminal")
			}
		}
	}
	if assistants != 1 || reply.Metadata["backend_message_id"] == "" || reply.Metadata["persist_error"] != "" {
		t.Fatalf("sync failure history/save marker missing: assistants=%d metadata=%#v", assistants, reply.Metadata)
	}
}
