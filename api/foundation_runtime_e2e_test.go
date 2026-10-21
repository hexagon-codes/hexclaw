package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
	sqlitestore "github.com/hexagon-codes/hexclaw/storage/sqlite"
)

// 模型边界仅提供协议响应；HTTP 路由、引擎、网页工具和持久化均使用真实实现。
type foundationProtocolProvider struct {
	hexagon.Provider
	url   string
	calls atomic.Int32
}

func (p *foundationProtocolProvider) Name() string { return "test" }
func (p *foundationProtocolProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "fixture-model"}}
}
func (p *foundationProtocolProvider) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.calls.Add(1)
	for _, message := range req.Messages {
		if message.Role == llm.RoleTool && strings.Contains(message.Content, "upstream diagnostic") {
			if !strings.Contains(message.Content, `"http_status":403`) {
				return nil, fmt.Errorf("model did not receive execution facts")
			}
			return &llm.CompletionResponse{Content: "The upstream returned HTTP 403. This is not an application approval denial."}, nil
		}
	}
	var facts, snapshot bool
	for _, message := range req.Messages {
		facts = facts || strings.Contains(message.Content, "https://github.com/hexagon-codes/hexclaw")
		snapshot = snapshot || strings.Contains(message.Content, `"actual_model":"fixture-model"`)
	}
	if !facts || !snapshot {
		return nil, fmt.Errorf("model request is missing facts or actual runtime")
	}
	args, _ := json.Marshal(map[string]string{"url": p.url, "action": "fetch"})
	return &llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "browser-call", Name: "browser", Arguments: string(args)}}}, nil
}
func (p *foundationProtocolProvider) Stream(ctx context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	response, err := p.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	delta := map[string]any{"content": response.Content}
	finish := "stop"
	if len(response.ToolCalls) > 0 {
		call := response.ToolCalls[0]
		delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": call.ID, "type": "function", "function": map[string]string{"name": call.Name, "arguments": call.Arguments}}}}
		finish = "tool_calls"
	}
	frame, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	return llm.NewStream(strings.NewReader("data: "+string(frame)+"\n\ndata: [DONE]\n\n"), llm.StreamOpenAIFormat), nil
}

func TestFoundationRuntimeHTTPPersistenceAndRestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<p>upstream diagnostic</p>"))
	}))
	defer upstream.Close()
	dbPath := filepath.Join(t.TempDir(), "data.db")
	store, err := sqlitefixture.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Server.APIToken = "isolated-fixture-token"
	cfg.Compaction.Enabled = false
	cfg.LLM.Default = "test"
	cfg.LLM.Tools.Enabled = "on"
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"test": {Model: "fixture-model"}}
	registry := skill.NewRegistry()
	if err := registry.Register(builtin.NewBrowserSkill()); err != nil {
		t.Fatal(err)
	}
	router := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"test": &foundationProtocolProvider{url: upstream.URL}})
	eng := engine.NewReActEngine(cfg, router, store, registry)
	eng.SetToolCollector(engine.NewToolCollector(registry, nil, 10))
	eng.SetToolExecutor(engine.NewToolExecutor(registry, nil))
	if err := eng.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop(context.Background())
	server := httptest.NewServer(NewServer(cfg, eng, nil, store).routes())
	defer server.Close()
	request := func(url, method, body string, sse bool) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Server.APIToken)
		req.Header.Set("Content-Type", "application/json")
		if sse {
			req.Header.Set("Accept", "text/event-stream")
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("HTTP %d: %s", response.StatusCode, data)
		}
		return response
	}
	response := request(server.URL+"/api/v1/chat", "POST", `{"message":"Explain the observed upstream response","request_id":"foundation-json-1"}`, false)
	var reply ChatResponse
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	receipt := reply.Metadata["self_context_receipt"]
	if receipt == "" || !json.Valid([]byte(receipt)) || len(reply.ToolCalls) != 1 || reply.ToolCalls[0].Status != "error" || reply.ToolCalls[0].Detail == nil || reply.ToolCalls[0].Detail.HTTPStatus != 403 {
		t.Fatalf("HTTP response lost runtime or tool facts: %+v", reply)
	}
	record, err := store.GetMessage(t.Context(), reply.AssistantMessageID)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]json.RawMessage
	if err := json.Unmarshal([]byte(record.Metadata), &persisted); err != nil {
		if err := json.Unmarshal([]byte(record.Meta), &persisted); err != nil {
			t.Fatal(err)
		}
	}
	var saved string
	if err := json.Unmarshal(persisted["self_context_receipt"], &saved); err != nil || saved != receipt {
		t.Fatalf("database receipt differs: %s %v", persisted["self_context_receipt"], err)
	}
	if !strings.Contains(string(persisted["tool_calls"]), `"http_status":403`) {
		t.Fatal("database lost tool detail")
	}
	streamResponse := request(server.URL+"/api/v1/chat", "POST", `{"message":"Explain the observed upstream response","request_id":"foundation-sse-1"}`, true)
	streamBody, err := io.ReadAll(streamResponse.Body)
	streamResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(streamBody), "self_context_receipt") || !strings.Contains(string(streamBody), "http_status") {
		t.Fatalf("SSE lost execution facts: %s", streamBody)
	}
	server.Close()
	if err := eng.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restartedURL := startFoundationRestartProcess(t, dbPath, upstream.URL)
	history := request(restartedURL+"/api/v1/sessions/"+reply.SessionID+"/messages", "GET", "", false)
	historyBody, err := io.ReadAll(history.Body)
	history.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(historyBody), "foundation-json-1") || !strings.Contains(string(historyBody), "self_context_receipt") || !strings.Contains(string(historyBody), "http_status") {
		t.Fatalf("restart lost history: %s", historyBody)
	}
}

func startFoundationRestartProcess(t *testing.T, dbPath, upstream string) string {
	t.Helper()
	dir := t.TempDir()
	ready, stop := filepath.Join(dir, "ready.json"), filepath.Join(dir, "stop")
	log, err := os.Create(filepath.Join(dir, "process.log"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestFoundationRuntimeProcessHelper$", "-test.v")
	command.Env = append(os.Environ(), "HEXCLAW_FOUNDATION_PROCESS_FIXTURE=1", "HEXCLAW_FOUNDATION_DB="+dbPath,
		"HEXCLAW_FOUNDATION_READY="+ready, "HEXCLAW_FOUNDATION_STOP="+stop, "HEXCLAW_FOUNDATION_UPSTREAM="+upstream)
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait(); close(done) }()
	t.Cleanup(func() {
		if err := os.WriteFile(stop, []byte("stop"), 0o600); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil {
				data, _ := os.ReadFile(log.Name())
				t.Errorf("restarted API process failed: %v\n%s", err, data)
			}
		case <-time.After(30 * time.Second):
			_ = command.Process.Kill()
			<-done
			t.Error("restarted API process did not stop")
		}
		_ = log.Close()
	})
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		data, _ := os.ReadFile(ready)
		var result struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(data, &result) == nil && result.URL != "" {
			return result.URL
		}
		select {
		case err := <-done:
			data, _ := os.ReadFile(log.Name())
			t.Fatalf("restarted API exited before readiness: %v\n%s", err, data)
		case <-deadline.C:
			t.Fatal("restarted API did not become ready")
		case <-tick.C:
		}
	}
}

// 仅由上面的真实进程恢复案例启动，不作为独立验收通过项。
func TestFoundationRuntimeProcessHelper(t *testing.T) {
	if os.Getenv("HEXCLAW_FOUNDATION_PROCESS_FIXTURE") != "1" {
		t.Skip("subprocess fixture")
	}
	store, err := sqlitestore.New(os.Getenv("HEXCLAW_FOUNDATION_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Server.APIToken = "isolated-fixture-token"
	cfg.Compaction.Enabled = false
	cfg.LLM.Default = "test"
	cfg.LLM.Tools.Enabled = "on"
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"test": {Model: "fixture-model"}}
	provider := &foundationProtocolProvider{url: os.Getenv("HEXCLAW_FOUNDATION_UPSTREAM")}
	registry := skill.NewRegistry()
	if err := registry.Register(builtin.NewBrowserSkill()); err != nil {
		t.Fatal(err)
	}
	router := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"test": provider})
	eng := engine.NewReActEngine(cfg, router, store, registry)
	if err := eng.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop(context.Background())
	server := httptest.NewServer(NewServer(cfg, eng, nil, store).routes())
	defer server.Close()
	ready, _ := json.Marshal(map[string]string{"url": server.URL})
	if err := os.WriteFile(os.Getenv("HEXCLAW_FOUNDATION_READY"), ready, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(os.Getenv("HEXCLAW_FOUNDATION_STOP")); err == nil {
			if provider.calls.Load() != 0 {
				t.Fatalf("history read invoked the model %d times", provider.calls.Load())
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("subprocess fixture was not stopped")
		case <-tick.C:
		}
	}
}
