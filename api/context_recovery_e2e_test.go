package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/ai-core/llm/openai"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	enginepkg "github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	hexmcp "github.com/hexagon-codes/hexclaw/mcp"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/skill"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type recoveryWireRequest struct {
	Stream   bool `json:"stream"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

func writeRecoveryCompletion(w http.ResponseWriter, streaming bool, text, tool string, callData ...string) {
	message := map[string]any{"role": "assistant", "content": text}
	finish := "stop"
	if tool != "" {
		arguments, id := "{}", "recovery-call"
		if len(callData) > 0 {
			arguments = callData[0]
		}
		if len(callData) > 1 {
			id = callData[1]
		}
		message["tool_calls"] = []any{map[string]any{"id": id, "index": 0, "type": "function", "function": map[string]string{"name": tool, "arguments": arguments}}}
		finish = "tool_calls"
	}
	choice := map[string]any{"index": 0, "message": message, "finish_reason": finish}
	if streaming {
		choice["delta"] = message
		delete(choice, "message")
	}
	frame := map[string]any{"id": "isolated-recovery", "model": "gpt-4o", "choices": []any{choice}, "usage": map[string]int{"prompt_tokens": 11, "completion_tokens": 3, "total_tokens": 14}}
	if streaming {
		w.Header().Set("Content-Type", "text/event-stream")
		encoded, _ := json.Marshal(frame)
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
	} else {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(frame)
	}
}

type recoveryOnceSkill struct {
	path  string
	calls atomic.Int32
}

func (*recoveryOnceSkill) Name() string        { return "recovery_once" }
func (*recoveryOnceSkill) Description() string { return "Persist the isolated operation once" }
func (*recoveryOnceSkill) Match(string) bool   { return false }
func (*recoveryOnceSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("recovery_once", "Persist the isolated operation once", &llm.Schema{Type: "object"})
}
func (s *recoveryOnceSkill) Execute(context.Context, map[string]any) (*skill.Result, error) {
	s.calls.Add(1)
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	_, err = f.WriteString("RECOVERY_TOOL_OK\n")
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return &skill.Result{Content: "RECOVERY_TOOL_OK"}, err
}

// 模型故障通过真实 OpenAI HTTP/SSE 协议触发，应用路由、工具副作用与历史持久化均不替换。
func TestContextProviderRecoveryHTTPAPI(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, role := range []string{"", "recovery-k12"} {
			t.Run(fmt.Sprintf("role=%s/stream=%v", role, streaming), func(t *testing.T) {
				var backupHadResult, partialShown atomic.Bool
				primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req recoveryWireRequest
					if json.NewDecoder(r.Body).Decode(&req) != nil {
						http.Error(w, "invalid request", 400)
						return
					}
					for _, m := range req.Messages {
						if m.Role != "tool" {
							continue
						}
						if req.Stream {
							partialShown.Store(true)
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"PARTIAL_\"}}]}\n\n")
							w.(http.Flusher).Flush()
							<-r.Context().Done()
						} else {
							http.Error(w, "503 Service Unavailable", http.StatusServiceUnavailable)
						}
						return
					}
					writeRecoveryCompletion(w, req.Stream, "", "recovery_once")
				}))
				t.Cleanup(primary.Close)
				backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req recoveryWireRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					for _, m := range req.Messages {
						if m.Role == "tool" && strings.Contains(string(m.Content), "RECOVERY_TOOL_OK") {
							backupHadResult.Store(true)
						}
					}
					answer := "MISSING_TOOL_RESULT"
					if backupHadResult.Load() {
						answer = "RECOVERY_OK"
						if req.Stream {
							answer = "PARTIAL_RECOVERY_OK"
						}
					}
					writeRecoveryCompletion(w, req.Stream, answer, "")
				}))
				t.Cleanup(backup.Close)
				cfg := apiCanonicalHistoryConfig()
				cfg.LLM.Tools.Enabled, cfg.LLM.Default, cfg.LLM.Routing.Strategy = "on", "primary", "default"
				cfg.LLM.Providers = map[string]config.LLMProviderConfig{"primary": {Model: "gpt-4o"}, "backup": {Model: "gpt-4o"}}
				providers := map[string]hexagon.Provider{
					"primary": openai.New("isolated-test-token", openai.WithBaseURL(primary.URL), openai.WithModel("gpt-4o"), openai.WithStreamIdleTimeout(150*time.Millisecond)),
					"backup":  openai.New("isolated-test-token", openai.WithBaseURL(backup.URL), openai.WithModel("gpt-4o")),
				}
				store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "recovery.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				if err := store.Init(t.Context()); err != nil {
					t.Fatal(err)
				}
				registry := skill.NewRegistry()
				once := &recoveryOnceSkill{path: filepath.Join(t.TempDir(), "operation.txt")}
				if err := registry.Register(once); err != nil {
					t.Fatal(err)
				}
				eng := enginepkg.NewReActEngine(cfg, llmrouter.NewWithProviders(cfg.LLM, providers), store, registry)
				eng.SetToolExecutor(enginepkg.NewToolExecutor(registry, nil))
				eng.SetToolCollector(enginepkg.NewToolCollector(registry, nil, 40))
				dispatcher := agentrouter.New()
				if err := dispatcher.Register(agentrouter.AgentConfig{Name: "recovery-k12", SystemPrompt: "Follow the current K12 task.", Metadata: map[string]string{"scenario": "k12-tutor"}}); err != nil {
					t.Fatal(err)
				}
				if role != "" {
					eng.SetAgentRouter(dispatcher)
				}
				if err := eng.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = eng.Stop(context.Background()) })
				srv := NewServer(cfg, eng, nil, store)
				if role != "" {
					srv.SetAgentRouter(dispatcher)
				}
				srv.SetDesktopAPIToken(e2eAPIToken)
				server := httptest.NewServer(srv.routes())
				t.Cleanup(server.Close)
				body, _ := json.Marshal(ChatRequest{Message: "Execute recovery_once and report the observed result.", SessionID: "sess-recovery", Role: role, RequestID: "recover-after-tool", Metadata: map[string]string{"memory": "off"}})
				response := apiMultiTurnRequest(t, server.Client(), server.URL+"/api/v1/chat", body, streaming)
				want := "RECOVERY_OK"
				if partialShown.Load() {
					want = "PARTIAL_RECOVERY_OK"
				}
				if !backupHadResult.Load() || response.Reply != want || len(response.ToolCalls) != 1 {
					t.Fatalf("lost completed progress: backup=%v response=%+v", backupHadResult.Load(), response)
				}
				data, err := os.ReadFile(once.path)
				if err != nil || once.calls.Load() != 1 || string(data) != "RECOVERY_TOOL_OK\n" {
					t.Fatalf("operation repeated: calls=%d data=%q err=%v", once.calls.Load(), data, err)
				}
				var history struct {
					Messages []*struct {
						Role     string `json:"role"`
						Content  string `json:"content"`
						Metadata string `json:"metadata"`
					} `json:"messages"`
				}
				apiCanonicalHistoryDoJSON(t, server.Client(), http.MethodGet, server.URL+"/api/v1/sessions/sess-recovery/messages", nil, &history)
				found := false
				for _, row := range history.Messages {
					if row.Role == "assistant" && row.Content == want && strings.Contains(row.Metadata, "RECOVERY_TOOL_OK") {
						found = true
					}
				}
				if !found {
					t.Fatal("final reply and completed tool result missing from independent history readback")
				}
				if !streaming && (response.Usage == nil || response.Usage.TotalTokens != 28) {
					t.Fatalf("completed usage lost: %+v", response.Usage)
				}
			})
		}
	}
}

func TestContextOriginalImageSurvivesToolMediaHTTPAPI(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, scenario := range []struct {
			rounds   int
			fallback bool
		}{{1, false}, {4, false}, {1, true}} {
			t.Run(fmt.Sprintf("stream=%v/rounds=%d/fallback=%v", streaming, scenario.rounds, scenario.fallback), func(t *testing.T) {
				rounds := scenario.rounds
				var original, media bytes.Buffer
				_ = png.Encode(&original, image.NewRGBA(image.Rect(0, 0, 2, 2)))
				_ = png.Encode(&media, image.NewRGBA(image.Rect(0, 0, 1, 1)))
				originalURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(original.Bytes())
				upstream := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "recovery-media", Version: "1"}, nil)
				var toolExecutions atomic.Int32
				upstream.AddTool(&sdkmcp.Tool{Name: "recovery_pages", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
					toolExecutions.Add(1)
					result := &sdkmcp.CallToolResult{}
					for range 5 {
						result.Content = append(result.Content, &sdkmcp.ImageContent{MIMEType: "image/png", Data: media.Bytes()})
					}
					return result, nil
				})
				mcpServer := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return upstream }, nil))
				t.Cleanup(mcpServer.Close)
				manager := hexmcp.NewManager()
				t.Cleanup(manager.Close)
				if err := manager.AddServer(t.Context(), hexmcp.ServerConfig{Name: "recovery-media", Transport: "streamable", Endpoint: mcpServer.URL}); err != nil {
					t.Fatal(err)
				}
				var phase atomic.Int32
				model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req recoveryWireRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					if n := phase.Add(1); int(n) <= rounds {
						writeRecoveryCompletion(w, req.Stream, "", "recovery_pages", fmt.Sprintf("{\"round\":%d}", n), fmt.Sprintf("recovery-%d", n))
						return
					}
					if scenario.fallback && r.Header.Get("X-Context-Recovery") != "backup" {
						http.Error(w, "503 Service Unavailable", 503)
						return
					}
					images, toolResults, bytes, hasOriginal := 0, 0, 0, false
					for _, m := range req.Messages {
						bytes += len(m.Content)
						if m.Role == "tool" {
							toolResults++
						}
						var parts []struct {
							Type     string `json:"type"`
							ImageURL struct {
								URL string `json:"url"`
							} `json:"image_url"`
						}
						_ = json.Unmarshal(m.Content, &parts)
						for _, p := range parts {
							if p.Type == "image_url" {
								images++
								hasOriginal = hasOriginal || p.ImageURL.URL == originalURL
							}
						}
					}
					if images > 5 {
						http.Error(w, "input image count limit: too many images", 400)
						return
					}
					answer := "ORIGINAL_LOST"
					if hasOriginal && images == 5 {
						answer = "ORIGINAL_IMAGE_OK"
					}
					if toolResults != min(rounds, 3) || bytes >= 40000 {
						answer = "OLD_TOOL_CONTEXT_NOT_COMPACTED"
					}
					writeRecoveryCompletion(w, req.Stream, answer, "")
				}))
				t.Cleanup(model.Close)
				cfg := apiCanonicalHistoryConfig()
				cfg.LLM.Tools.Enabled = "on"
				cfg.LLM.Routing.Strategy = "default"
				cfg.LLM.Providers["test"] = config.LLMProviderConfig{Model: "glm-4v-flash", ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: []config.LLMProviderModelSpec{{ID: "glm-4v-flash", Capabilities: []string{"text", "vision", "tool_use"}}}}
				store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "media.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				if err := store.Init(t.Context()); err != nil {
					t.Fatal(err)
				}
				registry := skill.NewRegistry()
				provider := openai.New("isolated-test-token", openai.WithBaseURL(model.URL), openai.WithModel("glm-4v-flash"))
				providers := map[string]hexagon.Provider{"test": provider}
				if scenario.fallback {
					cfg.LLM.Providers["backup"] = cfg.LLM.Providers["test"]
					providers["backup"] = openai.New("isolated-test-token", openai.WithBaseURL(model.URL), openai.WithModel("glm-4v-flash"), openai.WithHeaders(map[string]string{"X-Context-Recovery": "backup"}))
				}
				eng := enginepkg.NewReActEngine(cfg, llmrouter.NewWithProviders(cfg.LLM, providers), store, registry)
				eng.SetToolExecutor(enginepkg.NewToolExecutor(registry, manager))
				eng.SetToolCollector(enginepkg.NewToolCollector(registry, manager, 40))
				if err := eng.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = eng.Stop(context.Background()) })
				srv := NewServer(cfg, eng, nil, store)
				srv.SetDesktopAPIToken(e2eAPIToken)
				server := httptest.NewServer(srv.routes())
				t.Cleanup(server.Close)
				body, _ := json.Marshal(ChatRequest{Message: "Read recovery_pages and keep this original worksheet.", SessionID: "sess-image-recovery", RequestID: "image-recovery", Metadata: map[string]string{"pinned_agent": "default"}, Attachments: []adapter.Attachment{{Type: "image", Mime: "image/png", Data: base64.StdEncoding.EncodeToString(original.Bytes())}}})
				response := apiMultiTurnRequest(t, server.Client(), server.URL+"/api/v1/chat", body, streaming)
				expectedRequests := rounds + 1
				if scenario.fallback {
					expectedRequests++
				}
				if response.Reply != "ORIGINAL_IMAGE_OK" || len(response.ToolCalls) != rounds || int(phase.Load()) != expectedRequests || int(toolExecutions.Load()) != rounds {
					t.Fatalf("original input lost after tool media: %+v", response)
				}
			})
		}
	}
}
