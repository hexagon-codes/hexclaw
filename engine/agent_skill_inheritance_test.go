package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
	sqlitestore "github.com/hexagon-codes/hexclaw/storage/sqlite"
)

type inheritanceCountingSkill struct {
	skill.Skill
	loader   skill.ContentLoader
	provider skill.ContextReceiptProvider
	loads    int
	path     string
}

func (s *inheritanceCountingSkill) LoadContent() (string, error) {
	s.loads++
	return s.loader.LoadContent()
}
func (s *inheritanceCountingSkill) LoadedContextReceipt(body string) (skill.ContextReceipt, bool) {
	return s.provider.LoadedContextReceipt(body)
}

func registerInheritanceMarkdown(t *testing.T, registry *skill.DefaultRegistry, name, marker, version string) *inheritanceCountingSkill {
	t.Helper()
	path := filepath.Join(t.TempDir(), "SKILL.md")
	md := fmt.Sprintf("---\nname: %s\nversion: %s\nteaching_methods: [{\"name\":\"Four-step teaching\"}]\n---\n%s", name, version, marker)
	if err := os.WriteFile(path, []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := marketplace.LoadSkillFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := marketplace.WrapAsSkill(source)
	counted := &inheritanceCountingSkill{Skill: wrapped, loader: wrapped.(skill.ContentLoader), provider: wrapped.(skill.ContextReceiptProvider), path: path}
	if err := registry.Register(counted); err != nil {
		t.Fatal(err)
	}
	return counted
}

type inheritanceCaptureProvider struct {
	fastKBChatProvider
	calls int
}

func (p *inheritanceCaptureProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.calls++
	return p.fastKBChatProvider.Complete(ctx, req)
}
func (p *inheritanceCaptureProvider) Stream(ctx context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	p.calls++
	return p.fastKBChatProvider.Stream(ctx, req)
}

func newInheritanceFixture(t *testing.T, cache bool) (*ReActEngine, *inheritanceCaptureProvider, *sqlitestore.Store, *agentrouter.Dispatcher, *skill.DefaultRegistry, *inheritanceCountingSkill) {
	t.Helper()
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	registry := skill.NewRegistry()
	active := registerInheritanceMarkdown(t, registry, "configured-method", "CONFIGURED_METHOD_BODY", "1.0.0")
	registerInheritanceMarkdown(t, registry, "other-method", "OTHER_METHOD_BODY", "1.0.0")
	registerInheritanceMarkdown(t, registry, "disabled-method", "DISABLED_METHOD_BODY", "1.0.0")
	if err := registry.SetEnabled("disabled-method", false); err != nil {
		t.Fatal(err)
	}
	failed := registerInheritanceMarkdown(t, registry, "failed-method", "FAILED_METHOD_BODY", "1.0.0")
	if err := os.Remove(failed.path); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(&fakeToolSkill{name: "configured-tool"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(&inheritanceFastPathSkill{}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Compaction.Enabled = false
	cfg.LLM.Cache.Enabled = cache
	cfg.LLM.Default = "test"
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"test": {Model: "mock-model"}}
	provider := &inheritanceCaptureProvider{}
	router := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"test": provider})
	eng := NewReActEngine(cfg, router, store, registry)
	agents := agentrouter.New()
	if err := agents.Register(agentrouter.AgentConfig{Name: "configured-agent", SystemPrompt: "REGISTERED_AGENT_PERSONA", Skills: []string{"configured-method", "disabled-method", "configured-tool", "not-registered", "failed-method"}}); err != nil {
		t.Fatal(err)
	}
	if err := agents.Register(agentrouter.AgentConfig{Name: "other-agent", SystemPrompt: "OTHER_AGENT_PERSONA", Skills: []string{"other-method"}}); err != nil {
		t.Fatal(err)
	}
	if err := agents.Register(agentrouter.AgentConfig{Name: "empty-agent", SystemPrompt: "EMPTY_AGENT_PERSONA"}); err != nil {
		t.Fatal(err)
	}
	if err := agents.AddRule(agentrouter.Rule{ID: 1, Platform: "dingtalk", ChatID: "bound-chat", AgentName: "configured-agent"}); err != nil {
		t.Fatal(err)
	}
	eng.SetAgentRouter(agents)
	if err := eng.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Stop(context.Background()) })
	return eng, provider, store, agents, registry, active
}

func TestAgentSkillInheritanceOrdinaryProviderStreamAndIM(t *testing.T) {
	for _, mode := range []string{"process-role", "stream-role", "process-provider", "stream-provider", "process-pinned-provider", "stream-pinned-provider", "process-im", "stream-im"} {
		t.Run(mode, func(t *testing.T) {
			eng, provider, store, _, registry, active := newInheritanceFixture(t, false)
			msg := &adapter.Message{ID: "inherit-request", Platform: adapter.PlatformAPI, UserID: "inherit-owner", SessionID: "inherit-session", Content: "diagnostic", Metadata: map[string]string{"role": "configured-agent", "request_id": "inherit-request"}}
			if strings.HasSuffix(mode, "provider") {
				msg.Metadata["provider"] = "test"
				msg.Metadata["model"] = "mock-model"
			}
			if strings.Contains(mode, "-pinned-") {
				delete(msg.Metadata, "role")
				msg.Metadata["pinned_agent"] = "configured-agent"
			}
			if strings.HasSuffix(mode, "im") {
				delete(msg.Metadata, "role")
				msg.Platform = adapter.PlatformDingtalk
				msg.ChatID = "bound-chat"
			}
			var metadata map[string]string
			if strings.HasPrefix(mode, "stream") {
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
			if !provider.sawText("CONFIGURED_METHOD_BODY") || provider.sawText("DISABLED_METHOD_BODY") || provider.sawText("OTHER_METHOD_BODY") || active.loads != 1 || provider.calls != 1 {
				t.Fatalf("default source not consumed once: loads=%d calls=%d", active.loads, provider.calls)
			}
			fast, _ := registry.Get("diagnostic")
			if fast.(*inheritanceFastPathSkill).calls != 0 {
				t.Fatal("keyword fast path bypassed configured teaching body")
			}
			if enabled, _ := registry.IsEnabled("disabled-method"); enabled {
				t.Fatal("default inheritance enabled disabled skill")
			}
			if _, explicit := msg.Metadata["skills"]; explicit || eng.hasMountedPersonaSkill(msg.Metadata) {
				t.Fatal("defaults changed explicit mount/memory semantics")
			}
			tools := []llm.ToolDefinition{llm.NewToolDefinition("existing-tool", "existing", nil)}
			if len(eng.ensureMountedSkillTools(tools, msg.Metadata)) != 1 {
				t.Fatal("defaults expanded tool floor")
			}
			var receipts []skill.ContextReceipt
			if err := json.Unmarshal([]byte(metadata[skillContextReceiptsMetadataKey]), &receipts); err != nil {
				t.Fatal(err)
			}
			if len(receipts) != 1 || receipts[0].Name != "configured-method" || receipts[0].Status != "loaded" {
				t.Fatalf("wrong default context receipt: %+v", receipts)
			}
			rows, err := store.ListMessages(t.Context(), msg.SessionID, 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.Role == "user" && (strings.Contains(row.Metadata, `"skills"`) || strings.Contains(row.Attachments, `"skills"`)) {
					t.Fatal("server defaults became user history chips")
				}
			}
		})
	}
}

type inheritanceFastPathSkill struct{ calls int }

func (s *inheritanceFastPathSkill) Name() string              { return "diagnostic" }
func (s *inheritanceFastPathSkill) Description() string       { return "local diagnostic" }
func (s *inheritanceFastPathSkill) Match(content string) bool { return content == "diagnostic" }
func (s *inheritanceFastPathSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition(s.Name(), s.Description(), nil)
}
func (s *inheritanceFastPathSkill) Execute(context.Context, map[string]any) (*skill.Result, error) {
	s.calls++
	return &skill.Result{Content: "local diagnostic"}, nil
}

func TestAgentSkillInheritanceExplicitSelectionAndEmptyOverride(t *testing.T) {
	for _, selection := range []string{"other-method", ""} {
		t.Run(map[bool]string{true: "explicit", false: "explicit-empty"}[selection != ""], func(t *testing.T) {
			eng, provider, _, _, _, active := newInheritanceFixture(t, false)
			msg := &adapter.Message{ID: "explicit-request", Platform: adapter.PlatformAPI, UserID: "owner", SessionID: "explicit-session", Content: "Explain this concept.", Metadata: map[string]string{"role": "configured-agent", "skills": selection}}
			reply, err := eng.Process(t.Context(), msg)
			if err != nil {
				t.Fatal(err)
			}
			if active.loads != 0 || provider.sawText("CONFIGURED_METHOD_BODY") || provider.sawText("OTHER_METHOD_BODY") != (selection != "") {
				t.Fatal("explicit request was overridden by agent defaults")
			}
			if selection == "" && reply.Metadata[skillContextReceiptsMetadataKey] != "" {
				t.Fatal("explicit empty selection fabricated a default receipt")
			}
		})
	}
}

func TestAgentSkillInheritanceReplaySwitchAndActualBodyCacheIdentity(t *testing.T) {
	eng, provider, _, agents, registry, active := newInheritanceFixture(t, true)
	message := func(id string) *adapter.Message {
		return &adapter.Message{ID: id, Platform: adapter.PlatformAPI, UserID: "owner", SessionID: "cache-session", Content: "Explain this concept.", Metadata: map[string]string{"role": "configured-agent", "request_id": id}}
	}
	first := message("first")
	reply, err := eng.Process(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	original := reply.Metadata[skillContextReceiptsMetadataKey]
	if original == "" || active.loads != 1 || provider.calls != 1 {
		t.Fatal("initial request did not consume configured body")
	}
	if err := registry.Unregister("configured-method"); err != nil {
		t.Fatal(err)
	}
	updated := registerInheritanceMarkdown(t, registry, "configured-method", "UPDATED_METHOD_BODY", "2.0.0")
	replay, err := eng.Process(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Metadata[skillContextReceiptsMetadataKey] != original || provider.calls != 1 || updated.loads != 0 {
		t.Fatal("durable replay reloaded current defaults or changed original receipt")
	}
	second := message("second")
	reply, err = eng.Process(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	if !provider.sawText("UPDATED_METHOD_BODY") || provider.calls != 2 || reply.Metadata["source"] == "cache" {
		t.Fatal("new source body reused stale cached answer")
	}
	cfg, _ := agents.GetAgent("configured-agent")
	cfg.Skills = []string{"other-method"}
	if err := agents.UpdateAgent(*cfg); err != nil {
		t.Fatal(err)
	}
	third := message("third")
	reply, err = eng.Process(t.Context(), third)
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 3 || reply.Metadata["source"] == "cache" || msgDefaultOwner(third) != "configured-agent" {
		t.Fatal("changed configured defaults reused old cache partition")
	}
	var receipts []skill.ContextReceipt
	if err := json.Unmarshal([]byte(reply.Metadata[skillContextReceiptsMetadataKey]), &receipts); err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].Name != "other-method" {
		t.Fatal("prior default leaked after config changed")
	}
	fourth := message("fourth")
	cached, err := eng.Process(t.Context(), fourth)
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 3 || cached.Metadata["source"] != "cache" {
		t.Fatal("unchanged effective body no longer reused its existing cache partition")
	}
	if cached.Metadata[skillContextReceiptsMetadataKey] != "" {
		t.Fatal("cache preparation falsely claimed a new model context consumption")
	}
}

func msgDefaultOwner(msg *adapter.Message) string { return msg.Metadata[agentDefaultSkillOwnerKey] }

func TestAgentSkillInheritanceClearsOtherAgentAndSystemScope(t *testing.T) {
	eng, _, _, _, _, _ := newInheritanceFixture(t, false)
	msg := &adapter.Message{Platform: adapter.PlatformAPI, Metadata: map[string]string{"role": "configured-agent"}}
	eng.prepareAgentDefaultSkillContext(msg)
	ctx := eng.prepareSkillPromptContext(t.Context(), msg)
	if msg.Metadata[skillContextReceiptsMetadataKey] != "" {
		t.Fatal("preparation created usage receipt")
	}
	msg.Metadata["role"] = "empty-agent"
	eng.prepareAgentDefaultSkillContext(msg)
	ctx = eng.prepareSkillPromptContext(ctx, msg)
	if len(agentDefaultSkillContextNames(msg.Metadata)) != 0 || msg.Metadata[skillPromptDigestKey] != "" {
		t.Fatal("empty new agent retained previous defaults")
	}
	messages := eng.buildStreamMessages(ctx, "empty-agent", nil, "", "Explain.", msg.Metadata, nil)
	if strings.Contains(messages[0].Content, "CONFIGURED_METHOD_BODY") {
		t.Fatal("previous prepared body leaked to new agent")
	}
	msg.Metadata["role"] = "configured-agent"
	msg.Metadata["source"] = solveDispatchSource
	eng.prepareAgentDefaultSkillContext(msg)
	if len(agentDefaultSkillContextNames(msg.Metadata)) != 0 {
		t.Fatal("system solve inherited interactive defaults")
	}
	msg.Metadata[agentDefaultSkillNamesKey] = `["configured-method"]`
	msg.Metadata[skillPromptDigestKey] = "forged"
	ctx = clearPreparedSkillContext(ctx, msg)
	if len(agentDefaultSkillContextNames(msg.Metadata)) != 0 || msg.Metadata[skillPromptDigestKey] != "" {
		t.Fatal("client or parent request supplied private defaults")
	}
}
