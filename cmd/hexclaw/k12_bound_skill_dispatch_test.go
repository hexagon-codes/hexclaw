package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/skilladapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
)

type boundToolObservation struct {
	names  []string
	system string
	choice any
}

type boundToolContractModel struct {
	mu           sync.Mutex
	observations []boundToolObservation
}

func (*boundToolContractModel) Name() string { return "bound-tool-contract" }
func (*boundToolContractModel) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "bound-tool-contract"}}
}
func (*boundToolContractModel) CountTokens([]llm.Message) (int, error) { return 0, nil }
func (p *boundToolContractModel) observe(request llm.CompletionRequest) {
	observation := boundToolObservation{choice: request.ToolChoice}
	for _, tool := range request.Tools {
		observation.names = append(observation.names, tool.Function.Name)
	}
	for _, message := range request.Messages {
		if message.Role == llm.RoleSystem {
			observation.system += message.Content + "\n"
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observations = append(p.observations, observation)
}
func (p *boundToolContractModel) Complete(_ context.Context, request llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.observe(request)
	return &llm.CompletionResponse{Content: "Received the current task."}, nil
}
func (p *boundToolContractModel) Stream(_ context.Context, request llm.CompletionRequest) (*llm.Stream, error) {
	p.observe(request)
	return llm.NewStream(strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Received the current task.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), llm.StreamOpenAIFormat), nil
}

type boundToolFixtureSkill struct{ name string }

func (s boundToolFixtureSkill) Name() string        { return s.name }
func (s boundToolFixtureSkill) Description() string { return s.name }
func (boundToolFixtureSkill) Match(string) bool     { return false }
func (s boundToolFixtureSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition(s.name, s.Description(), &llm.Schema{Type: "object"})
}
func (boundToolFixtureSkill) Execute(context.Context, map[string]any) (*skill.Result, error) {
	return nil, fmt.Errorf("capture-only tool must not execute")
}

// 验证真实注册绑定在普通/流式请求中形成的模型契约；真实模型自然选工具另由渠道验收证明。
func TestK12BoundHomeworkSkillProjectsIntoNaturalConversation(t *testing.T) {
	const customPrompt = "保留我的自定义语气与教学要求。"
	for _, mode := range []string{"sync", "stream"} {
		for _, test := range []struct {
			name           string
			generic        bool
			explicit       bool
			disabled       bool
			deny           bool
			internal       bool
			identity       bool
			collectorLimit int
		}{
			{name: "natural bound homework"},
			{name: "explicit skill matches binding", explicit: true},
			{name: "bound tool survives collector limit", collectorLimit: 1},
			{name: "generic adviser keeps both capabilities", generic: true},
			{name: "disabled bound capability retains general solver", disabled: true},
			{name: "inherited policy retains authority", deny: true},
			{name: "internal solve dispatch retains execution contract", internal: true},
			{name: "identity does not force a homework call", identity: true},
		} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "bound-tools.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				registry := skill.NewRegistry()
				for _, tool := range []skill.Skill{boundToolFixtureSkill{name: "a_collected_first"}, engine.NewSolveSkill(nil, nil), skilladapter.NewGradeSkill(usecase.Deps{})} {
					if err := registry.Register(tool); err != nil {
						t.Fatal(err)
					}
				}
				if test.disabled {
					if err := registry.SetEnabled("k12_grade", false); err != nil {
						t.Fatal(err)
					}
				}
				dispatcher := agentrouter.New()
				if err := dispatcher.Register(agentrouter.AgentConfig{Name: "bound-tutor", SystemPrompt: customPrompt, Skills: []string{"a_collected_first", "k12_grade"}, Metadata: map[string]string{"scenario": "k12-tutor", "k12.child_name": "当前学生"}}); err != nil {
					t.Fatal(err)
				}
				model := &boundToolContractModel{}
				cfg := config.DefaultConfig()
				cfg.Compaction.Enabled, cfg.FileMemory.Enabled, cfg.LLM.Cache.Enabled = false, false, false
				cfg.LLM.Default, cfg.LLM.Tools.Enabled = model.Name(), "on"
				if test.collectorLimit > 0 {
					cfg.LLM.Tools.MaxTools = test.collectorLimit
				}
				cfg.LLM.Providers = map[string]config.LLMProviderConfig{model.Name(): {Model: model.Name()}}
				providers := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{model.Name(): model})
				eng := engine.NewReActEngine(cfg, providers, store, registry)
				eng.SetAgentRouter(dispatcher)
				eng.SetAgentSystemPromptPolicy(newK12TutorIdentityPolicy(dispatcher, nil))
				eng.SetToolCollector(engine.NewToolCollector(registry, nil, test.collectorLimit))
				eng.SetToolExecutor(engine.NewToolExecutor(registry, nil))
				if err := eng.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = eng.Stop(context.Background()) })
				metadata := map[string]string{"pinned_agent": "bound-tutor", "knowledge": "off", "memory": "off"}
				if test.generic {
					metadata["pinned_agent"] = "default"
					metadata["scenario"] = "k12-tutor"
				}
				if test.explicit {
					metadata["skills"] = "k12_grade"
				}
				if test.deny {
					metadata["tool_deny"] = "k12_grade"
				}
				if test.internal {
					metadata["source"] = "solve"
				}
				text := standaloneIMProblem
				if test.identity {
					text = "你是谁？"
				}
				message := &adapter.Message{ID: "natural-request", UserID: "parent", Platform: adapter.PlatformAPI, ChatID: "current-chat", Content: text, Metadata: metadata}
				if mode == "sync" {
					if _, err = eng.Process(t.Context(), message); err != nil {
						t.Fatal(err)
					}
				} else {
					stream, streamErr := eng.ProcessStream(t.Context(), message)
					if streamErr != nil {
						t.Fatal(streamErr)
					}
					for chunk := range stream {
						if chunk.Error != nil {
							t.Fatal(chunk.Error)
						}
					}
				}
				model.mu.Lock()
				defer model.mu.Unlock()
				if len(model.observations) != 1 {
					t.Fatalf("unexpected model calls: %d", len(model.observations))
				}
				observed := model.observations[0]
				names := make(map[string]bool)
				for _, name := range observed.names {
					names[name] = true
				}
				wantGrade := !test.disabled && !test.deny
				wantSolve := test.generic || test.disabled || test.deny || test.internal
				if names["k12_grade"] != wantGrade || names["solve"] != wantSolve {
					t.Fatalf("bound tool contract lost or duplicated capabilities: got=%v grade=%t solve=%t", observed.names, wantGrade, wantSolve)
				}
				if !test.generic && !test.internal && wantGrade && (len(observed.names) == 0 || observed.names[0] != "k12_grade") {
					t.Fatalf("bound homework tool was not prioritized: %v", observed.names)
				}
				if !test.generic && !test.internal && (!strings.Contains(observed.system, customPrompt) || !strings.Contains(observed.system, "k12_grade")) {
					t.Fatalf("binding directive or original persona did not reach model: %s", observed.system)
				}
				if test.internal && strings.Contains(observed.system, "[Bound homework capability]") {
					t.Fatal("internal solve inherited a top-level homework routing directive")
				}
				if observed.choice != nil && observed.choice != "auto" {
					t.Fatalf("ordinary conversation was forced to execute homework: %v", observed.choice)
				}
				configured, _ := dispatcher.GetAgent("bound-tutor")
				if configured.SystemPrompt != customPrompt {
					t.Fatal("runtime binding projection rewrote the user's persona")
				}
			})
		}
	}
}
