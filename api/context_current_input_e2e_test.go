package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	mockllm "github.com/hexagon-codes/hexagon/testing/mock"
	"github.com/hexagon-codes/hexclaw/config"
	enginepkg "github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	hexmcp "github.com/hexagon-codes/hexclaw/mcp"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// 本轮原始输入必须在工具循环压缩后完整保留，且不进入历史摘要。
func TestContextCurrentInputHTTPAPI(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		historyRepeat int
		early         bool
	}{
		{"no_summary_tail", 1, false},
		{"summary_early", 600, true},
		{"summary_tail", 600, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {

			for _, sse := range []bool{false, true} {
				for _, surface := range []string{"api", "desktop", "k12"} {
					t.Run(fmt.Sprintf("%s/sse=%v", surface, sse), func(t *testing.T) {
						cfg := apiCanonicalHistoryConfig()
						cfg.Memory.Conversation.MaxTurns = 50
						cfg.LLM.Tools.Enabled = "on"
						cfg.LLM.Providers["test"] = config.LLMProviderConfig{Model: "mock-model", ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: []config.LLMProviderModelSpec{{ID: "mock-model", Capabilities: []string{"text", "vision"}}}}
						store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "mcp-context.db"))
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = store.Close() })
						if err := store.Init(t.Context()); err != nil {
							t.Fatal(err)
						}
						var pixels bytes.Buffer
						if err := png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
							t.Fatal(err)
						}
						var toolExecutions atomic.Int32
						upstream := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "context-fixture", Version: "1"}, nil)
						upstream.AddTool(&sdkmcp.Tool{Name: "context_media_page", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"page": map[string]any{"type": "integer"}}, "required": []string{"page"}}}, func(_ context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
							var args struct {
								Page int `json:"page"`
							}
							if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
								return nil, err
							}
							toolExecutions.Add(1)
							return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
								&sdkmcp.TextContent{Text: fmt.Sprintf("PAGE-%d-BEGIN\n", args.Page) + strings.Repeat("page data ", 900) + fmt.Sprintf("\nPAGE-%d-END", args.Page)},
								&sdkmcp.ImageContent{MIMEType: "image/png", Data: pixels.Bytes()},
							}}, nil
						})
						remote := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return upstream }, nil))
						t.Cleanup(remote.Close)
						mgr := hexmcp.NewManager()
						t.Cleanup(mgr.Close)
						if err := mgr.AddServer(t.Context(), hexmcp.ServerConfig{Name: "context-fixture", Transport: "streamable", Endpoint: remote.URL}); err != nil {
							t.Fatal(err)
						}
						phase, summaries := -1, 0
						const marker = "CURRENT_INSTRUCTION_MUST_ONLY_LIST_PAGE_NUMBERS"
						background := strings.Repeat("Background fixture. ", 80)
						currentQuestion := "Read all five current pages with context_media_page. " + background + marker
						if scenario.early {
							currentQuestion = "Read all five current pages with context_media_page. " + marker + " " + background
						}
						initialFull, initialMarker, summaryMarker, nextFull, nextMarker := false, false, false, false, false
						nextFullCount := 0
						finalBytes, finalResults := 0, 0
						provider := mockllm.NewLLMProvider("test").WithResponseFn(func(req hexagon.CompletionRequest) (*hexagon.CompletionResponse, error) {
							for _, message := range req.Messages {
								if strings.Contains(message.Content, "Summarize the following conversation concisely.") {
									summaries++
									summaryMarker = strings.Contains(message.Content, marker)
									summaryText := "Earlier fixture discussion."
									if summaryMarker {
										summaryText += " " + marker
									}
									return &hexagon.CompletionResponse{Content: summaryText}, nil
								}
							}
							if phase < 0 {
								return &hexagon.CompletionResponse{Content: "ready"}, nil
							}
							if phase == 0 {
								for _, message := range req.Messages {
									initialMarker = initialMarker || strings.Contains(message.Content, marker)
									initialFull = initialFull || (message.Role == llm.RoleUser && strings.Contains(message.Content, currentQuestion))
								}
								if !initialFull || !initialMarker {
									return nil, fmt.Errorf("initial current input is invalid")
								}

								phase++
								var calls []llm.ToolCall
								for i := 1; i <= 5; i++ {
									calls = append(calls, llm.ToolCall{ID: fmt.Sprintf("media-page-%d", i), Name: "context_media_page", Arguments: fmt.Sprintf(`{"page":%d}`, i)})
								}
								return &hexagon.CompletionResponse{ToolCalls: calls}, nil
							}
							calls, results := map[string]bool{}, map[string]string{}
							images, size := 0, 0
							for _, message := range req.Messages {
								size += len(message.Content)
								for _, call := range message.ToolCalls {
									calls[call.ID] = true
								}
								if message.Role == llm.RoleTool {
									if !calls[message.ToolCallID] {
										return nil, fmt.Errorf("orphan MCP result: %s", message.ToolCallID)
									}
									results[message.ToolCallID] = message.Content
								}
								for _, part := range message.MultiContent {
									if part.Type == "image_url" {
										images++
									}
								}
							}
							for i := 1; i <= 5; i++ {
								id := fmt.Sprintf("media-page-%d", i)
								if !calls[id] || !strings.Contains(results[id], fmt.Sprintf("PAGE-%d-BEGIN", i)) || !strings.Contains(results[id], fmt.Sprintf("PAGE-%d-END", i)) {
									return nil, fmt.Errorf("MCP media group was split or its result truncated: %s; calls=%d results=%d", id, len(calls), len(results))
								}
							}
							// 这五张是本轮工具实际新返回的图片；历史图片回放上限由既有用例独立验证。
							expectedSummaries := 1
							if scenario.historyRepeat == 1 {
								expectedSummaries = 0
							}
							if summaries != expectedSummaries || size >= 80000 || images != 5 {
								return nil, fmt.Errorf("existing compaction/image policy changed: summaries=%d text_bytes=%d images=%d", summaries, size, images)
							}
							phase++
							for _, message := range req.Messages {
								nextMarker = nextMarker || strings.Contains(message.Content, marker)
								if message.Role == llm.RoleUser && strings.Contains(message.Content, currentQuestion) {
									nextFull = true
									nextFullCount++
								}
							}
							finalBytes, finalResults = size, len(results)
							return &hexagon.CompletionResponse{Content: "Five current page results retained."}, nil
						})
						registry := skill.NewRegistry()
						router := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"test": contextAPIStreamProvider{provider}})
						eng := enginepkg.NewReActEngine(cfg, router, store, registry)
						eng.SetToolCollector(enginepkg.NewToolCollector(registry, mgr, 40))
						eng.SetToolExecutor(enginepkg.NewToolExecutor(registry, mgr))
						if err := eng.Start(t.Context()); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = eng.Stop(context.Background()) })
						srv := NewServer(cfg, eng, nil, store)
						metadata := map[string]string{"pinned_agent": "default"}
						if surface != "api" {
							srv.desktopAPIToken = e2eAPIToken
						}
						if surface == "k12" {
							d := agentrouter.New()
							if err := d.Register(agentrouter.AgentConfig{Name: "context-k12", Provider: "test", Model: "mock-model", SystemPrompt: "K12_CONTEXT_PERSONA", Metadata: map[string]string{"scenario": "k12-tutor"}}); err != nil {
								t.Fatal(err)
							}
							eng.SetAgentRouter(d)
							srv.SetAgentRouter(d)
							metadata["pinned_agent"] = "context-k12"
						}
						ts := httptest.NewServer(srv.routes())
						t.Cleanup(ts.Close)
						sessionID := fmt.Sprintf("sess-mcp-media-%s-%v", surface, sse)
						body, err := json.Marshal(ChatRequest{Message: "Prepare the isolated fixture.", SessionID: sessionID, Platform: surface, Metadata: metadata, RequestID: "media-prepare"})
						if err != nil {
							t.Fatal(err)
						}
						apiMultiTurnRequest(t, ts.Client(), ts.URL+"/api/v1/chat", body, sse)
						for i := 0; i < 12; i++ {
							role, content := "assistant", "earlier answer"
							if i%2 == 0 {
								role, content = "user", strings.Repeat("older context ", scenario.historyRepeat)
							}
							if err := store.SaveMessage(t.Context(), &storage.MessageRecord{ID: fmt.Sprintf("media-history-%d", i), SessionID: sessionID, Role: role, Content: content, CreatedAt: time.Now().Add(time.Duration(i-20) * time.Second)}); err != nil {
								t.Fatal(err)
							}
						}
						phase = 0
						body, err = json.Marshal(ChatRequest{Message: currentQuestion, SessionID: sessionID, Platform: surface, Metadata: metadata, RequestID: "media-read"})
						if err != nil {
							t.Fatal(err)
						}
						started := time.Now()
						reply := apiMultiTurnRequest(t, ts.Client(), ts.URL+"/api/v1/chat", body, sse)
						if phase != 2 || toolExecutions.Load() != 5 || len(reply.ToolCalls) != 5 || reply.Reply != "Five current page results retained." {
							t.Fatalf("MCP API did not complete exactly once: phase=%d executions=%d reply=%+v", phase, toolExecutions.Load(), reply)
						}
						var history struct {
							Messages []*storage.MessageRecord `json:"messages"`
						}
						apiCanonicalHistoryDoJSON(t, ts.Client(), http.MethodGet, ts.URL+"/api/v1/sessions/"+sessionID+"/messages?limit=50", nil, &history)
						found := false
						for _, row := range history.Messages {
							if row.ID == reply.AssistantMessageID && row.Content == reply.Reply {
								found = true
								for i := 1; i <= 5; i++ {
									if !strings.Contains(row.Metadata, fmt.Sprintf("media-page-%d", i)) {
										t.Fatalf("executed MCP result missing from public history: page %d", i)
									}
								}
							}
						}
						if !found {
							t.Fatal("current reply was not independently readable from public history")
						}
						storedFull := false
						for _, row := range history.Messages {
							if row.Role == "user" && row.Content == currentQuestion {
								storedFull = true
							}
						}
						receipt := map[string]any{
							"scenario": scenario.name, "surface": surface, "sse": sse,
							"initial_full_input": initialFull, "initial_marker": initialMarker,
							"summary_calls": summaries, "summary_received_marker": summaryMarker,
							"next_full_user_input": nextFull, "next_request_marker": nextMarker,
							"stored_full_input": storedFull, "tool_executions": toolExecutions.Load(),
							"final_tool_results": finalResults, "final_text_bytes": finalBytes,
							"current_input_chars": len([]rune(currentQuestion)), "elapsed_ms": float64(time.Since(started).Microseconds()) / 1000,
						}
						encoded, err := json.Marshal(receipt)
						if err != nil {
							t.Fatal(err)
						}
						t.Logf("CURRENT_INPUT_EVIDENCE %s", encoded)
						if !storedFull {
							t.Fatal("fixture input was not independently readable from history")
						}
						if !nextFull || !nextMarker || nextFullCount != 1 {
							t.Fatalf("current input must remain complete exactly once: full=%v tail=%v count=%d", nextFull, nextMarker, nextFullCount)
						}
						if summaries > 0 && summaryMarker {
							t.Fatal("protected current input was sent for historical summarization")
						}

					})
				}
			}

		})
	}
}
