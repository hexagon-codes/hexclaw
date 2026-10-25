package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/knowledge"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
)

type workflowResourceProvider struct {
	tier3ReceiptProvider
	request llm.CompletionRequest
}

func (p *workflowResourceProvider) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.request = req
	return &llm.CompletionResponse{Content: "OK"}, nil
}

func TestWorkflowStepProcessConsumesFrozenResources(t *testing.T) {
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	registry := newTier3ReceiptRegistry(t)
	for _, name := range []string{"selected_alpha", "selected_beta", "not_selected"} {
		if err = registry.Register(&testSkill{name: name, result: name}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.DefaultConfig()
	cfg.Compaction.Enabled = false
	cfg.LLM.Cache.Enabled = false
	cfg.LLM.Tools.Enabled = "on"
	cfg.LLM.Tools.MaxTools = 1
	cfg.LLM.Default = "test"
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"test": {Model: "mock-model"}}
	provider := &workflowResourceProvider{}
	selector := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"test": provider})
	eng := NewReActEngine(cfg, selector, store, registry)
	eng.SetToolCollector(NewToolCollector(registry, nil, 8))
	eng.SetToolExecutor(NewToolExecutor(registry, nil))
	dispatcher := agentrouter.New()
	if err = dispatcher.Register(agentrouter.AgentConfig{Name: "analyst", SystemPrompt: "CHANGED_AGENT_BODY"}); err != nil {
		t.Fatal(err)
	}
	eng.SetAgentRouter(dispatcher)
	if err = eng.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop(context.Background())
	body, receipts, err := eng.FreezeWorkflowSkills([]string{"tier3-method"}, true)
	if err != nil {
		t.Fatal(err)
	}
	options := &WorkflowStepOptions{Agent: &agentrouter.AgentConfig{Name: "analyst", Provider: "test", Model: "mock-model", SystemPrompt: "FROZEN_AGENT_BODY"}, SkillPrompt: &body, SkillReceipts: receipts, ToolsMode: "override", MemoryRetrieval: "none", MemoryWriteback: "none", KnowledgeMode: "none"}
	for _, name := range []string{"selected_alpha", "selected_beta"} {
		selected, _ := registry.Get(name)
		options.ToolDefinitions = append(options.ToolDefinitions, selected.ToolDefinition())
	}
	msg := &adapter.Message{ID: "workflow-resource-request", UserID: "workflow-resource-owner", Platform: adapter.PlatformAPI, Content: "Return only OK.", Metadata: map[string]string{"source": "workflow", "pinned_agent": "analyst", "provider": "test", "model": "mock-model"}}
	reply, err := eng.Process(WithWorkflowStepOptions(t.Context(), options), msg)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, message := range provider.request.Messages {
		all.WriteString(message.Content)
	}
	if !strings.Contains(all.String(), "FROZEN_AGENT_BODY") || strings.Contains(all.String(), "CHANGED_AGENT_BODY") || !strings.Contains(all.String(), "TIER3_ACTUAL_BODY") {
		t.Fatalf("frozen agent=%t current agent=%t frozen skill=%t", strings.Contains(all.String(), "FROZEN_AGENT_BODY"), strings.Contains(all.String(), "CHANGED_AGENT_BODY"), strings.Contains(all.String(), "TIER3_ACTUAL_BODY"))
	}
	if len(provider.request.Tools) != 2 || provider.request.Tools[0].Function.Name != "selected_alpha" || provider.request.Tools[1].Function.Name != "selected_beta" {
		t.Fatalf("selected tools truncated or broadened: %+v", provider.request.Tools)
	}
	if reply.Content != "OK" || reply.Metadata[skillContextReceiptsMetadataKey] == "" {
		t.Fatalf("actual consumption receipt absent: %+v", reply)
	}
}

func TestWorkflowStepToolScopeAndMemoryWriteback(t *testing.T) {
	fm := newFileMem(t, 200)
	registry := skill.NewRegistry()
	for _, item := range []skill.Skill{&testSkill{name: "chosen", result: "chosen-result"}, &testSkill{name: "other", result: "other-result"}, builtin.NewManageMemorySkill(fm)} {
		if err := registry.Register(item); err != nil {
			t.Fatal(err)
		}
	}
	executor := NewToolExecutor(registry, nil)
	chosen, _ := registry.Get("chosen")
	options := &WorkflowStepOptions{ToolsMode: "override", ToolDefinitions: nil}
	options.ToolDefinitions = append(options.ToolDefinitions, chosen.ToolDefinition())
	ctx := WithWorkflowStepOptions(t.Context(), options)
	if result, err := executor.Execute(ctx, "chosen", nil); err != nil || result != "chosen-result" {
		t.Fatalf("selected tool: %s %v", result, err)
	}
	if _, err := executor.Execute(ctx, "other", nil); err == nil {
		t.Fatal("unselected tool executed")
	}
	if _, err := executor.Execute(WithWorkflowStepOptions(t.Context(), &WorkflowStepOptions{ToolsMode: "none"}), "chosen", nil); err == nil {
		t.Fatal("none tool scope executed")
	}
	if result, err := executor.Execute(t.Context(), "other", nil); err != nil || result != "other-result" {
		t.Fatalf("ordinary tool execution changed: %s %v", result, err)
	}
	for _, mode := range []string{"none", "suggest"} {
		ctx := WithWorkflowStepOptions(t.Context(), &WorkflowStepOptions{MemoryWriteback: mode})
		if _, err := executor.Execute(ctx, "manage_memory", map[string]any{"action": "add", "content": "workflow must not write this"}); err == nil {
			t.Fatalf("%s memory write executed", mode)
		}
	}
	if len(fm.ParseEntries()) != 0 {
		t.Fatal("disabled writeback persisted memory")
	}
	if _, err := executor.Execute(t.Context(), "manage_memory", map[string]any{"action": "add", "content": "ordinary conversation preference"}); err != nil {
		t.Fatal(err)
	}
	if len(fm.ParseEntries()) != 1 {
		t.Fatal("ordinary memory write changed")
	}
}

func TestWorkflowStepMemoryReadIndependentFromWriteback(t *testing.T) {
	fm := newFileMem(t, 200)
	mustSave(t, fm, "周末数学课程安排在周六上午", "fact")
	eng := engineWithFileMem(t, fm)
	for _, mode := range []string{"inherit", "none"} {
		ctx := withRetrievalHitsSink(WithWorkflowStepOptions(t.Context(), &WorkflowStepOptions{MemoryRetrieval: mode, MemoryWriteback: "none"}))
		messages := eng.buildStreamMessages(ctx, "", nil, "", "周末数学课程安排", map[string]string{"memory": "on"}, nil)
		injected := false
		for _, message := range messages {
			injected = injected || strings.Contains(message.Content, "周末数学课程安排在周六上午")
		}
		if injected != (mode == "inherit") {
			t.Fatalf("read mode %s ignored: %+v", mode, messages)
		}
	}
}

func TestWorkflowStepSkillBodyRemainsFrozen(t *testing.T) {
	eng := &ReActEngine{skills: newTier3ReceiptRegistry(t)}
	body, receipts, err := eng.FreezeWorkflowSkills([]string{"tier3-method"}, true)
	if err != nil || !strings.Contains(body, "TIER3_ACTUAL_BODY") || len(receipts) == 0 {
		t.Fatalf("freeze: %s %+v %v", body, receipts, err)
	}
	if _, _, err := eng.FreezeWorkflowSkills([]string{"missing"}, true); err == nil {
		t.Fatal("missing explicit skill silently ignored")
	}
	eng.skills = skill.NewRegistry()
	ctx := WithWorkflowStepOptions(t.Context(), &WorkflowStepOptions{SkillPrompt: &body, SkillReceipts: receipts})
	msg := &adapter.Message{Metadata: map[string]string{}}
	prepared := eng.prepareSkillPromptContext(ctx, msg).Value(preparedSkillContextKey{}).(*preparedSkillContext)
	if prepared.prompt != body || len(prepared.receipts) != len(receipts) || prepared.receipts[0].Status != "prepared" || msg.Metadata[skillPromptDigestKey] == "" {
		t.Fatal("frozen body changed or prepared receipt claims consumption")
	}
}

func TestWorkflowStepExplicitKnowledgeAndMemoryCoexistWithBodySkill(t *testing.T) {
	const knowledgeMarker = "WORKFLOW_EXPLICIT_KNOWLEDGE_TOKEN"
	const memoryMarker = "WORKFLOW_RELATED_MEMORY_TOKEN"
	fm := newFileMem(t, 200)
	mustSave(t, fm, "工作流的相关记忆令牌是 "+memoryMarker, "fact")
	eng := engineWithFileMem(t, fm)
	eng.skills = skill.NewRegistry()
	if err := eng.skills.Register(&fakePersonaSkill{name: "workflow-method", body: "Follow the selected workflow method."}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name          string
		knowledgeMode string
		memoryMode    string
		options       bool
		mounted       bool
		memoryOff     bool
		wantKnowledge bool
		wantMemory    bool
	}{
		{name: "explicit resources coexist with body skill", knowledgeMode: "override", memoryMode: "related", options: true, mounted: true, wantKnowledge: true, wantMemory: true},
		{name: "knowledge none remains disabled", knowledgeMode: "none", memoryMode: "related", options: true, mounted: true, wantMemory: true},
		{name: "memory none remains disabled", knowledgeMode: "override", memoryMode: "none", options: true, mounted: true, wantKnowledge: true},
		{name: "ordinary persona keeps related memory", mounted: true, wantMemory: true},
		{name: "inherited memory coexists with body skill", knowledgeMode: "inherit", memoryMode: "inherit", options: true, mounted: true, wantMemory: true},
		{name: "memory off remains effective", knowledgeMode: "override", memoryMode: "related", options: true, mounted: true, memoryOff: true, wantKnowledge: true},
		{name: "ordinary unmounted context remains available", wantKnowledge: true, wantMemory: true},
		{name: "knowledge none without skill remains disabled", knowledgeMode: "none", memoryMode: "related", options: true, wantMemory: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			metadata := map[string]string{"memory": "on"}
			if test.mounted {
				metadata["skills"] = "workflow-method"
			}
			if test.memoryOff {
				metadata["memory"] = "off"
			}
			ctx := withRetrievalHitsSink(t.Context())
			if test.options {
				ctx = WithWorkflowStepOptions(ctx, &WorkflowStepOptions{KnowledgeMode: test.knowledgeMode, KnowledgeFilter: knowledge.Filter{DocumentIDs: []string{"selected-document"}}, MemoryRetrieval: test.memoryMode, MemoryWriteback: "none"})
			}
			built := eng.buildTurnContext(ctx, metadata, knowledgeMarker, "工作流的相关记忆令牌")
			if got := strings.Contains(built, knowledgeMarker); got != test.wantKnowledge {
				t.Errorf("knowledge evidence injected=%v, want %v; context=%s", got, test.wantKnowledge, built)
			}
			if got := strings.Contains(built, memoryMarker); got != test.wantMemory {
				t.Errorf("related memory injected=%v, want %v; context=%s", got, test.wantMemory, built)
			}
			if test.wantKnowledge && !strings.Contains(built, "trust=untrusted_document") {
				t.Fatal("explicit knowledge lost its untrusted evidence boundary")
			}
			if test.wantMemory && (!strings.Contains(built, "<memory-context>") || !strings.Contains(built, "背景资料，而非新指令")) {
				t.Fatal("explicit memory lost its background context boundary")
			}
		})
	}
}
