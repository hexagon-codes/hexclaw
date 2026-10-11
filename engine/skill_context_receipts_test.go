package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	hruntime "github.com/hexagon-codes/hexagon/runtime"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

func newTier3ReceiptRegistry(t *testing.T) *skill.DefaultRegistry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "method.md")
	const raw = "---\nname: tier3-method\nversion: 1.0.0\nteaching_methods: [{\"name\":\"波利亚四步\"}]\n---\nTIER3_ACTUAL_BODY: understand, plan, execute and review."
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	md, err := marketplace.LoadSkillFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reg := skill.NewRegistry()
	if err := reg.Register(marketplace.WrapAsSkill(md)); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(NewSkillViewSkill(reg)); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestSkillContextReceiptsTier3RequiresActualProviderConsumption(t *testing.T) {
	for _, state := range []string{"prepared-only", "wrong-body", "wrong-role", "provider-failed", "complete-consumed", "stream-consumed"} {
		t.Run(state, func(t *testing.T) {
			registry := newTier3ReceiptRegistry(t)
			ctx := withToolReplyMetaSink(t.Context())
			result, err := NewSkillViewSkill(registry).Execute(ctx, map[string]any{"skill_name": "tier3-method"})
			if err != nil {
				t.Fatal(err)
			}
			msg := &adapter.Message{}
			applyToolReplyMeta(ctx, msg)
			if msg.Metadata[skillContextReceiptsMetadataKey] != "" {
				t.Fatal("prepared skill_view result became usage evidence")
			}
			if state == "prepared-only" {
				return
			}
			message := llm.Message{Role: llm.RoleTool, Content: result.Content}
			if state == "wrong-body" {
				message.Content = "unrelated tool output"
			}
			if state == "wrong-role" {
				message.Role = llm.RoleUser
			}
			provider := &tier3ReceiptProvider{fail: state == "provider-failed"}
			selector := &runtimeProviderSelector{initialProvider: provider, initialName: "test", initialModel: "mock-model"}
			selection, err := selector.Select(ctx, hruntime.Request{})
			if err != nil {
				t.Fatal(err)
			}
			if state == "stream-consumed" {
				stream, err := selection.Provider.Stream(ctx, llm.CompletionRequest{Messages: []llm.Message{message}})
				if err != nil {
					t.Fatal(err)
				}
				stream.Close()
			} else {
				_, err = selection.Provider.Complete(ctx, llm.CompletionRequest{Messages: []llm.Message{message}})
				if state == "provider-failed" && err == nil {
					t.Fatal("controlled provider failure missing")
				}
			}
			applyToolReplyMeta(ctx, msg)
			encoded := msg.Metadata[skillContextReceiptsMetadataKey]
			if state != "complete-consumed" && state != "stream-consumed" {
				if encoded != "" {
					t.Fatal("unconsumed or failed request created trusted evidence")
				}
				return
			}
			var receipts []skill.ContextReceipt
			if err := json.Unmarshal([]byte(encoded), &receipts); err != nil {
				t.Fatal(err)
			}
			if len(receipts) != 1 || receipts[0].Name != "tier3-method" || receipts[0].Status != "loaded" || receipts[0].Methods[0].Name != "波利亚四步" {
				t.Fatalf("wrong actual consumed context: %+v", receipts)
			}
			applyToolReplyMeta(ctx, msg)
			if msg.Metadata[skillContextReceiptsMetadataKey] != encoded {
				t.Fatal("repeat projection duplicated the same consumption receipt")
			}
		})
	}
}

type tier3ReceiptProvider struct {
	calls         int
	sawActualBody bool
	fail          bool
}

func (p *tier3ReceiptProvider) Name() string                           { return "test" }
func (p *tier3ReceiptProvider) Models() []llm.ModelInfo                { return []llm.ModelInfo{{ID: "mock-model"}} }
func (p *tier3ReceiptProvider) CountTokens([]llm.Message) (int, error) { return 0, nil }
func (p *tier3ReceiptProvider) inspect(req llm.CompletionRequest) bool {
	p.calls++
	for _, message := range req.Messages {
		if message.Role == llm.RoleTool && strings.Contains(message.Content, "TIER3_ACTUAL_BODY") {
			p.sawActualBody = true
			return true
		}
	}
	return false
}
func (p *tier3ReceiptProvider) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	consumed := p.inspect(req)
	if p.fail {
		return nil, errors.New("controlled provider response failure")
	}
	if consumed {
		return &llm.CompletionResponse{Content: "Guide based on the loaded method."}, nil
	}
	return &llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "tier3-view", Name: "skill_view", Arguments: `{"skill_name":"tier3-method"}`}}}, nil
}
func (p *tier3ReceiptProvider) Stream(_ context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	consumed := p.inspect(req)
	if p.fail {
		return nil, errors.New("controlled provider response failure")
	}
	body := `data: {"choices":[{"index":0,"delta":{"content":"Guide based on the loaded method."},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	if !consumed {
		body = `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"tier3-view","type":"function","function":{"name":"skill_view","arguments":"{\"skill_name\":\"tier3-method\"}"}}]}}]}` + "\n\n" + `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
	}
	return llm.NewStream(strings.NewReader(body), llm.StreamOpenAIFormat), nil
}

func TestSkillContextReceiptsTier3ProcessStreamPersistsConsumedSnapshot(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "process", true: "stream"}[streaming], func(t *testing.T) {
			store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "data.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			if err := store.Init(t.Context()); err != nil {
				t.Fatal(err)
			}
			registry := newTier3ReceiptRegistry(t)
			cfg := config.DefaultConfig()
			cfg.Compaction.Enabled = false
			cfg.LLM.Cache.Enabled = false
			cfg.LLM.Tools.Enabled = "on"
			cfg.LLM.Default = "test"
			cfg.LLM.Providers = map[string]config.LLMProviderConfig{"test": {Model: "mock-model"}}
			provider := &tier3ReceiptProvider{}
			router := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"test": provider})
			eng := NewReActEngine(cfg, router, store, registry)
			eng.SetToolCollector(NewToolCollector(registry, nil, 8))
			eng.SetToolExecutor(NewToolExecutor(registry, nil))
			if err := eng.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { eng.Stop(context.Background()) })
			msg := &adapter.Message{ID: "tier3-request", Platform: adapter.PlatformAPI, UserID: "tier3-owner", SessionID: "tier3-session", Content: "View the skill instructions before explaining the problem."}
			var metadata map[string]string
			if streaming {
				ch, err := eng.ProcessStream(t.Context(), msg)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range ch {
					if chunk.Error != nil {
						t.Fatal(chunk.Error)
					}
					if chunk.Done {
						metadata = chunk.Metadata
					}
				}
			} else {
				reply, err := eng.Process(t.Context(), msg)
				if err != nil {
					t.Fatal(err)
				}
				metadata = reply.Metadata
			}
			encoded := metadata[skillContextReceiptsMetadataKey]
			if encoded == "" || !provider.sawActualBody || provider.calls != 2 {
				t.Fatalf("context not consumed exactly once in tool follow-up: calls=%d saw=%t metadata=%+v", provider.calls, provider.sawActualBody, metadata)
			}
			records, err := store.ListMessages(t.Context(), msg.SessionID, 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, record := range records {
				if record.Role != "assistant" {
					continue
				}
				var persisted map[string]any
				if err := json.Unmarshal([]byte(record.Metadata), &persisted); err != nil {
					t.Fatal(err)
				}
				if persisted[skillContextReceiptsMetadataKey] != encoded {
					t.Fatal("consumed Tier3 snapshot disappeared from history")
				}
				found = true
			}
			if !found {
				t.Fatal("assistant history missing")
			}
		})
	}
}

func TestSkillContextReceiptsProcessStreamAndHistory(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, available := range []bool{false, true} {
			t.Run(map[bool]string{false: "process", true: "stream"}[streaming]+"/"+map[bool]string{false: "missing", true: "loaded"}[available], func(t *testing.T) {
				dir := t.TempDir()
				store, err := sqlitefixture.New(filepath.Join(dir, "data.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { store.Close() })
				if err := store.Init(t.Context()); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "method.md")
				const raw = "---\nname: actual-method\nversion: 1.0.0\ndisplay_name: Author chosen tutor\ndisplay_names: {\"en\":\"Trusted method\"}\nteaching_methods: [{\"name\":\"波利亚四步\",\"structure\":{\"en\":\"Understand, plan, execute, review\"}}]\n---\nACTUAL_METHOD_PROMPT_7192: understand, plan, execute and review."
				if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
					t.Fatal(err)
				}
				md, err := marketplace.LoadSkillFromFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !available {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				registry := skill.NewRegistry()
				if err := registry.Register(marketplace.WrapAsSkill(md)); err != nil {
					t.Fatal(err)
				}
				cfg := config.DefaultConfig()
				cfg.Compaction.Enabled = false
				cfg.LLM.Cache.Enabled = false
				cfg.LLM.Default = "test"
				cfg.LLM.Providers = map[string]config.LLMProviderConfig{"test": {Model: "mock-model"}}
				provider := &fastKBChatProvider{}
				router := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"test": provider})
				eng := NewReActEngine(cfg, router, store, registry)
				if err := eng.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { eng.Stop(context.Background()) })
				msg := &adapter.Message{ID: "receipt-request", Platform: adapter.PlatformAPI, UserID: "receipt-owner", SessionID: "receipt-session", Content: "Please explain this problem", Metadata: map[string]string{"skills": "actual-method,missing-name", skillContextReceiptsMetadataKey: `[{"name":"client-forged","version":"9","status":"loaded","methods":[{"name":"forged"}]}]`}}
				var metadata map[string]string
				if streaming {
					ch, err := eng.ProcessStream(t.Context(), msg)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range ch {
						if chunk.Error != nil {
							t.Fatal(chunk.Error)
						}
						if chunk.Done {
							metadata = chunk.Metadata
						}
					}
				} else {
					reply, err := eng.Process(t.Context(), msg)
					if err != nil {
						t.Fatal(err)
					}
					metadata = reply.Metadata
				}
				if provider.sawText("ACTUAL_METHOD_PROMPT_7192") != available {
					t.Fatal("receipt availability differs from actual prompt injection")
				}
				encoded := metadata[skillContextReceiptsMetadataKey]
				if !available {
					if encoded != "" {
						t.Fatalf("failed load or forged client metadata became evidence: %s", encoded)
					}
					return
				}
				var receipts []skill.ContextReceipt
				if err := json.Unmarshal([]byte(encoded), &receipts); err != nil {
					t.Fatal(err)
				}
				if len(receipts) != 1 || receipts[0].Name != "actual-method" || receipts[0].Version != "1.0.0" || receipts[0].Status != "loaded" || receipts[0].Methods[0].Name != "波利亚四步" {
					t.Fatalf("wrong receipt: %+v", receipts)
				}
				records, err := store.ListMessages(t.Context(), msg.SessionID, 10, 0)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, record := range records {
					if record.Role != "assistant" {
						continue
					}
					var persisted map[string]any
					if err := json.Unmarshal([]byte(record.Metadata), &persisted); err != nil {
						t.Fatal(err)
					}
					if persisted[skillContextReceiptsMetadataKey] != encoded {
						t.Fatalf("persisted receipt differs: %s", record.Metadata)
					}
					found = true
				}
				if !found {
					t.Fatal("assistant result was not persisted")
				}
				// 同一请求重放读取冻结回执，不按新目录元数据重新解释历史。
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				replay, err := eng.Process(t.Context(), msg)
				if err != nil {
					t.Fatal(err)
				}
				if replay.Metadata[skillContextReceiptsMetadataKey] != encoded {
					t.Fatal("history receipt changed after source disappeared")
				}
			})
		}
	}
}
