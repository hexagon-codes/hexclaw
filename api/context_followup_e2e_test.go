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
	"github.com/hexagon-codes/hexclaw/skill/builtin"
	"github.com/hexagon-codes/hexclaw/storage"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// 外部模型以协议替身确定性核对请求；路由、引擎、技能匹配、SQLite和公开历史均为正式实现。
type contextAPIStreamProvider struct{ hexagon.Provider }

type contextLargeOutputSkill struct{}

func (*contextLargeOutputSkill) Name() string        { return "context_large_output" }
func (*contextLargeOutputSkill) Description() string { return "Read an isolated large context fixture" }
func (*contextLargeOutputSkill) Match(string) bool   { return false }
func (*contextLargeOutputSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("context_large_output", "Read an isolated large context fixture", &llm.Schema{Type: "object"})
}
func (*contextLargeOutputSkill) Execute(_ context.Context, args map[string]any) (*skill.Result, error) {
	repeat := 1000
	if compact, _ := args["compact"].(bool); compact {
		repeat = 600
	}
	return &skill.Result{Content: strings.Repeat("context data ", repeat)}, nil
}

func (p contextAPIStreamProvider) Stream(ctx context.Context, req hexagon.CompletionRequest) (*hexagon.LLMStream, error) {
	r, err := p.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	delta := map[string]any{"content": r.Content}
	finish := "stop"
	if len(r.ToolCalls) > 0 {
		var calls []map[string]any
		for i, call := range r.ToolCalls {
			calls = append(calls, map[string]any{"index": i, "id": call.ID, "type": "function", "function": map[string]string{"name": call.Name, "arguments": call.Arguments}})
		}
		delta["tool_calls"], finish = calls, "tool_calls"
	}
	frame, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	if err != nil {
		return nil, err
	}
	return llm.NewStream(strings.NewReader("data: "+string(frame)+"\n\ndata: [DONE]\n\n"), llm.StreamOpenAIFormat), nil
}

func TestContextFollowupHTTPAPI(t *testing.T) {
	for _, sse := range []bool{false, true} {
		for _, surface := range []string{"api", "desktop", "k12"} {
			t.Run(fmt.Sprintf("%s/sse=%v", surface, sse), func(t *testing.T) {
				cfg := apiCanonicalHistoryConfig()
				cfg.Memory.Conversation.MaxTurns = 50
				cfg.LLM.Tools.Enabled = "on"
				cfg.LLM.Providers["test"] = config.LLMProviderConfig{Model: "mock-model", ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: []config.LLMProviderModelSpec{{ID: "mock-model", Capabilities: []string{"text", "vision"}}}}
				store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "context.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				if err := store.Init(t.Context()); err != nil {
					t.Fatal(err)
				}
				var required, forbidden []string
				want := "收到"
				maxMessages := 0
				maxImages := 0
				compactionStep := -1
				parallelGroupStep := -1
				provider := mockllm.NewLLMProvider("test").WithResponseFn(func(req hexagon.CompletionRequest) (*hexagon.CompletionResponse, error) {
					var text strings.Builder
					images := 0
					transcriptMessages := 0
					for _, message := range req.Messages {
						if message.Role != llm.RoleSystem {
							transcriptMessages++
						}
						text.WriteString(message.Content)
						text.WriteByte('\n')
						for _, part := range message.MultiContent {
							if part.Type == "image_url" {
								images++
							}
						}
					}
					if images > maxImages {
						return nil, fmt.Errorf("historical images exceeded the existing policy: %d > %d", images, maxImages)
					}
					for _, needle := range required {
						if !strings.Contains(text.String(), needle) {
							return nil, fmt.Errorf("required context missing: %s", needle)
						}
					}
					for _, needle := range forbidden {
						if strings.Contains(text.String(), needle) {
							return nil, fmt.Errorf("unrelated context leaked: %s", needle)
						}
					}
					if maxMessages > 0 && transcriptMessages > maxMessages {
						return nil, fmt.Errorf("history grew without a window: total=%d transcript=%d", len(req.Messages), transcriptMessages)
					}
					if want == "已记录当前题目" || want == "工具保持关闭" {
						question := "下雨吗？"
						if want == "工具保持关闭" {
							question = "为什么这样算？"
						}
						repeated := 0
						for _, message := range req.Messages {
							if message.Role == llm.RoleUser && strings.HasSuffix(strings.TrimSpace(message.Content), question) {
								repeated++
							}
						}
						if repeated != 2 {
							return nil, fmt.Errorf("distinct historical questions lost after compaction: question=%q before=2 after=%d", question, repeated)
						}
					}
					if parallelGroupStep == 0 {
						parallelGroupStep = 1
						var calls []llm.ToolCall
						for i := 1; i <= 7; i++ {
							calls = append(calls, llm.ToolCall{ID: fmt.Sprintf("parallel-output-%d", i), Name: "context_large_output", Arguments: fmt.Sprintf(`{"page":%d,"compact":true}`, i)})
						}
						return &hexagon.CompletionResponse{ToolCalls: calls}, nil
					}
					if parallelGroupStep == 1 {
						calls, results := map[string]bool{}, map[string]bool{}
						for _, message := range req.Messages {
							for _, call := range message.ToolCalls {
								calls[call.ID] = true
							}
							if message.Role == llm.RoleTool {
								if !calls[message.ToolCallID] {
									return nil, fmt.Errorf("orphan tool result after compaction: %s", message.ToolCallID)
								}
								results[message.ToolCallID] = true
							}
						}
						for i := 1; i <= 7; i++ {
							id := fmt.Sprintf("parallel-output-%d", i)
							if !calls[id] || !results[id] {
								return nil, fmt.Errorf("parallel tool group was split: %s", id)
							}
						}
						if images != 2 {
							return nil, fmt.Errorf("distinct image requests lost after compaction: %d", images)
						}
						parallelGroupStep = 2
					}
					if want == "本轮压缩后仍有完整新回答" && len(req.Tools) == 0 {
						return nil, fmt.Errorf("compaction regression did not reach the tool runtime")
					}
					if compactionStep >= 0 && compactionStep < 4 {
						if compactionStep == 0 && strings.Count(text.String(), "重复的历史请求") < 10 {
							return nil, fmt.Errorf("long-history fixture was not present in the runtime input")
						}
						compactionStep++
						return &hexagon.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("large-output-%d", compactionStep), Name: "context_large_output", Arguments: fmt.Sprintf(`{"page":%d}`, compactionStep)}}}, nil
					}
					if compactionStep == 4 {
						toolMessages := 0
						for _, m := range req.Messages {
							if m.Role == llm.RoleTool {
								toolMessages++
							}
						}
						if toolMessages >= 4 {
							return nil, fmt.Errorf("large tool results did not reach input compaction")
						}
					}
					return &hexagon.CompletionResponse{Content: want}, nil
				})
				registry := skill.NewRegistry()
				for _, s := range []skill.Skill{builtin.NewWeatherSkill(), builtin.NewSearchSkill(), builtin.NewTranslateSkill(), builtin.NewBrowserSkill(), builtin.NewSummarySkill(), &contextLargeOutputSkill{}} {
					if err := registry.Register(s); err != nil {
						t.Fatal(err)
					}
				}
				router := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"test": contextAPIStreamProvider{provider}})
				eng := enginepkg.NewReActEngine(cfg, router, store, registry)
				eng.SetToolCollector(enginepkg.NewToolCollector(registry, nil, 40))
				eng.SetToolExecutor(enginepkg.NewToolExecutor(registry, nil))
				if err := eng.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = eng.Stop(context.Background()) })
				srv := NewServer(cfg, eng, nil, store)
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
				}
				ts := httptest.NewServer(srv.routes())
				t.Cleanup(ts.Close)
				platform := surface
				if surface == "k12" {
					platform = "desktop"
				}
				sessionID := fmt.Sprintf("sess-context-%s-%v", surface, sse)
				index := 0
				send := func(text string, metadata map[string]string) ChatResponse {
					t.Helper()
					index++
					if metadata == nil {
						metadata = map[string]string{}
					}
					metadata["pinned_agent"] = "default"
					if surface == "k12" {
						metadata["pinned_agent"] = "context-k12"
					}
					if surface == "k12" {
						required = append(required, "K12_CONTEXT_PERSONA")
					}
					body, err := json.Marshal(ChatRequest{Message: text, SessionID: sessionID, Platform: platform, Metadata: metadata, RequestID: fmt.Sprintf("context-%s-%v-%d", surface, sse, index)})
					if err != nil {
						t.Fatal(err)
					}
					r := apiMultiTurnRequest(t, ts.Client(), ts.URL+"/api/v1/chat", body, sse)
					if r.Reply != want {
						t.Fatalf("%q reply=%q want=%q", text, r.Reply, want)
					}
					var history struct {
						Messages []*storage.MessageRecord `json:"messages"`
					}
					apiCanonicalHistoryDoJSON(t, ts.Client(), http.MethodGet, ts.URL+"/api/v1/sessions/"+sessionID+"/messages?limit=50", nil, &history)
					found := false
					for _, row := range history.Messages {
						if row.ID == r.AssistantMessageID && row.Content == want {
							found = true
						}
					}
					if !found {
						t.Fatal("API reply was not independently readable in public history")
					}
					return r
				}
				send("本次地点是杭州，只回答收到。", nil)
				required = []string{"本次地点是杭州"}
				want = "杭州：小雨。"
				for _, question := range []string{"下雨吗？", "搜索这个城市明天的天气", "翻译刚才的回答", "打开网页，刚才的那个链接", "总结刚才的内容"} {
					send(question, nil)
				}
				want = "已切换上海"
				send("现在改查上海，杭州不再是当前地点。", nil)
				required = []string{"现在改查上海"}
				want = "上海：晴。"
				send("下雨吗？", nil)
				var beforeCompression struct {
					Messages []*storage.MessageRecord `json:"messages"`
				}
				apiCanonicalHistoryDoJSON(t, ts.Client(), http.MethodGet, ts.URL+"/api/v1/sessions/"+sessionID+"/messages?limit=50", nil, &beforeCompression)
				repeatedBefore := 0
				for _, message := range beforeCompression.Messages {
					if message.Role == "user" && message.Content == "下雨吗？" {
						repeatedBefore++
					}
				}
				if repeatedBefore != 2 {
					t.Fatalf("invalid fixture: repeated questions before compression=%d", repeatedBefore)
				}
				if err := store.SaveMessage(t.Context(), &storage.MessageRecord{ID: "context-large-history", SessionID: sessionID, Role: "assistant", Content: strings.Repeat("历史资料", 5000), CreatedAt: time.Now().Add(-time.Minute)}); err != nil {
					t.Fatal(err)
				}
				want = "已记录当前题目"
				send("当前作业题是12除以3，请先记住题目。", nil)
				required = []string{"当前作业题是12除以3"}
				want = "12÷3=4"
				send("这道题怎么做？", nil)
				send("为什么这样算？", nil)
				want = "已更新当前题目"
				send("换一道题：20除以5，接下来只讨论这道。", nil)
				required = []string{"换一道题：20除以5"}
				want = "20÷5=4"
				send("为什么这样算？", nil)
				want = "工具保持关闭"
				send("杭州天气", map[string]string{"tools_enabled": "off"})
				oldSession := sessionID
				sessionID += "-compacted-tools"
				required = nil
				want = "这是旧回答，不能混入下一轮"
				send("先建立本会话的历史。", nil)
				for i := 0; i < 20; i++ {
					row := &storage.MessageRecord{ID: fmt.Sprintf("repeat-%s-%v-%d", surface, sse, i), SessionID: sessionID, Role: "user", Content: "重复的历史请求", CreatedAt: time.Now().Add(time.Duration(i-30) * time.Second)}
					if err := store.SaveMessage(t.Context(), row); err != nil {
						t.Fatal(err)
					}
				}
				compactionStep = 0
				want = "本轮压缩后仍有完整新回答"
				// 四次顺序工具结果足以触发现有压缩；模型输入可缩减，真实执行产出不能丢失。
				compressedReply := send("请分步读取 context_large_output 并核对当前工具结果。", nil)
				var blockText strings.Builder
				uses, results := map[string]bool{}, map[string]bool{}
				for _, block := range compressedReply.Blocks {
					if block.Type == "text" {
						blockText.WriteString(block.Text)
					}
					if block.Type == "tool_use" {
						uses[block.ID] = true
					}
				}
				// 公开协议通过扁平 ToolCalls 承载结果，块流仅保留调用的渲染位置。
				for _, call := range compressedReply.ToolCalls {
					results[call.ID] = call.Status == "success" && strings.Contains(call.Result, "context data")
				}
				if blockText.String() != want {
					t.Fatalf("compaction mixed or lost the current output blocks: %q", blockText.String())
				}
				for i := 1; i <= 4; i++ {
					id := fmt.Sprintf("large-output-%d", i)
					if !uses[id] || !results[id] {
						t.Fatalf("compaction lost an executed tool block: %s; uses=%v results=%v", id, uses, results)
					}
				}
				compactionStep = -1
				sessionID += "-parallel"
				want = "准备核对两张图"
				send("建立本次独立验收会话。", nil)
				// 两张同文图片与一组七个顺序执行的大工具返回共同触发压缩，不能丢图或拆组。
				for i, data := range []string{"YWJj", "ZGVm"} {
					row := &storage.MessageRecord{ID: fmt.Sprintf("parallel-image-%d", i), SessionID: sessionID, Role: "user", Content: "解题", Metadata: fmt.Sprintf(`{"attachments":[{"type":"image","mime":"image/png","data":%q}]}`, data), CreatedAt: time.Now().Add(time.Duration(i-2) * time.Second)}
					if err := store.SaveMessage(t.Context(), row); err != nil {
						t.Fatal(err)
					}
				}
				maxImages = 2
				parallelGroupStep = 0
				want = "两张图和七个工具结果均保留"
				groupReply := send("参考刚才两张图，调用 context_large_output 的七页，保留当前所有结果。", nil)
				if parallelGroupStep != 2 || len(groupReply.ToolCalls) != 7 {
					t.Fatalf("parallel tool loop did not finish: step=%d calls=%d", parallelGroupStep, len(groupReply.ToolCalls))
				}
				parallelGroupStep = -1
				maxImages = 0
				sessionID = "sess-context-isolated"
				required = nil
				forbidden = []string{"当前作业题是12除以3", "换一道题：20除以5", "本次地点是杭州"}
				want = "请提供城市"
				// 明确创建第二会话，验证同一用户同一智能体也不借用旧会话。
				body, _ := json.Marshal(ChatRequest{Message: "下雨吗？", SessionID: sessionID, Platform: platform, Metadata: map[string]string{"pinned_agent": map[bool]string{true: "context-k12", false: "default"}[surface == "k12"], "tools_enabled": "off"}, RequestID: "context-isolated"})
				r := apiMultiTurnRequest(t, ts.Client(), ts.URL+"/api/v1/chat", body, sse)
				if r.Reply != want {
					t.Fatalf("isolation reply=%q", r.Reply)
				}
				// 百张旧图只作为持久来源引用，模型请求仍受现有消息窗口控制。
				for i := 0; i < 100; i++ {
					row := &storage.MessageRecord{ID: fmt.Sprintf("old-image-%d", i), SessionID: oldSession, Role: "user", Content: fmt.Sprintf("旧题%d", i), Metadata: fmt.Sprintf(`{"attachments":[{"type":"image","url":"asset://old-page-%d"}]}`, i), CreatedAt: time.Now().Add(time.Duration(i-100) * time.Millisecond)}
					if err := store.SaveMessage(t.Context(), row); err != nil {
						t.Fatal(err)
					}
				}
				sessionID = oldSession
				required = nil
				forbidden = []string{"旧题0\n"}
				// 历史窗口加当前用户消息；系统策略提示不属于用户会话历史。
				maxMessages = cfg.Memory.Conversation.MaxTurns + 1
				want = "请说明当前新题"
				send("请处理这次新题，不重发旧图。", nil)
				// 旧版内联图仍遵循现有默认最多两图的规则，不因恢复多轮而无限回放。
				for i := 0; i < 100; i++ {
					row := &storage.MessageRecord{ID: fmt.Sprintf("legacy-image-%d", i), SessionID: oldSession, Role: "user", Content: fmt.Sprintf("旧版图片题%d", i), Metadata: `{"attachments":[{"type":"image","mime":"image/png","data":"YWJj"}]}`, CreatedAt: time.Now().Add(time.Duration(i-100) * time.Millisecond)}
					if err := store.SaveMessage(t.Context(), row); err != nil {
						t.Fatal(err)
					}
				}
				maxImages = 2
				want = "仅承接当前文本"
				send("继续解释，不要重新处理旧作业。", nil)
			})
		}
	}
}

// 真实 MCP 传输生成图文交错消息，压缩后检查最终模型请求及公开会话产物。
func TestContextMCPMediaHTTPAPI(t *testing.T) {
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
				provider := mockllm.NewLLMProvider("test").WithResponseFn(func(req hexagon.CompletionRequest) (*hexagon.CompletionResponse, error) {
					for _, message := range req.Messages {
						if strings.Contains(message.Content, "Summarize the following conversation concisely.") {
							summaries++
							return &hexagon.CompletionResponse{Content: "Earlier fixture discussion."}, nil
						}
					}
					if phase < 0 {
						return &hexagon.CompletionResponse{Content: "ready"}, nil
					}
					if phase == 0 {
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
					if summaries != 1 || size >= 80000 || images != 5 {
						return nil, fmt.Errorf("existing compaction/image policy changed: summaries=%d text_bytes=%d images=%d", summaries, size, images)
					}
					phase++
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
						role, content = "user", strings.Repeat("older context ", 600)
					}
					if err := store.SaveMessage(t.Context(), &storage.MessageRecord{ID: fmt.Sprintf("media-history-%d", i), SessionID: sessionID, Role: role, Content: content, CreatedAt: time.Now().Add(time.Duration(i-20) * time.Second)}); err != nil {
						t.Fatal(err)
					}
				}
				phase = 0
				body, err = json.Marshal(ChatRequest{Message: "Read all five current pages with context_media_page.", SessionID: sessionID, Platform: surface, Metadata: metadata, RequestID: "media-read"})
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
				t.Logf("MCP media API completed with five retained results in %s", time.Since(started))
			})
		}
	}
}
