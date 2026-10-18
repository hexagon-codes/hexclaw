package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/library"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
	"gopkg.in/yaml.v3"
)

// 真实模型核对仅显式开启；用户配置只读，运行与数据库均写隔离临时目录。
func TestWorkflowResourcesLiveModel(t *testing.T) {
	if os.Getenv("HEXCLAW_WORKFLOW_REAL_MODEL") != "1" {
		t.Skip("Live model verification is opt-in")
	}
	configPath := os.Getenv("HEXCLAW_WORKFLOW_REAL_CONFIG")
	if configPath == "" {
		t.Fatal("Explicit live model config path is required")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal("Configured model could not be read")
	}
	var loaded struct {
		LLM config.LLMConfig `yaml:"llm"`
	}
	if err = yaml.Unmarshal(raw, &loaded); err != nil {
		t.Fatal("Configured model could not be decoded")
	}
	provider, ok := loaded.LLM.Providers[loaded.LLM.Default]
	if !ok {
		t.Fatal("Configured default provider is unavailable")
	}
	cfg := config.DefaultConfig()
	cfg.LLM = loaded.LLM
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{cfg.LLM.Default: provider}
	cfg.LLM.Cache.Enabled = false
	cfg.Compaction.Enabled = false
	selector, err := llmrouter.New(cfg.LLM)
	if err != nil {
		t.Fatal("Configured provider could not be initialized")
	}
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	registry := skill.NewRegistry()
	eng := engine.NewReActEngine(cfg, selector, store, registry)
	eng.SetToolCollector(engine.NewToolCollector(registry, nil, 8))
	eng.SetToolExecutor(engine.NewToolExecutor(registry, nil))
	if err = eng.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop(context.Background())
	s := newWorkflowTestServer()
	s.engine = eng
	s.cfg = cfg
	s.agentRouter = agentrouter.New()
	if err = s.agentRouter.Register(agentrouter.AgentConfig{Name: "workflow-resource-probe", Provider: cfg.LLM.Default, Model: provider.Model, SystemPrompt: "Return only the exact JSON requested by the task."}); err != nil {
		t.Fatal(err)
	}
	eng.SetAgentRouter(s.agentRouter)
	db, err := sqlitefixture.Memory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = migrate.Run(t.Context(), db, migrate.All); err != nil {
		t.Fatal(err)
	}
	s.promptStore = library.NewPromptStore(db)
	id, err := s.promptStore.Upsert(t.Context(), &library.Prompt{Title: "Resource execution proof", BodyMD: `Return exactly {"summary":"WF_RESOURCE_OK"}. Do not use Markdown fences. $ARGUMENTS`, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	wf := contractWorkflow(contractNode("agent", "agent", map[string]any{"agent": "workflow-resource-probe", "prompt": "Follow the attached template.", "resources": map[string]any{"prompts": map[string]any{"mode": "override", "value": []string{id}}, "tools": map[string]any{"mode": "none"}, "knowledge": map[string]any{"mode": "none"}, "skills": map[string]any{"mode": "none"}}, "memory": map[string]any{"retrieval": map[string]any{"mode": "none"}, "writeback": map[string]any{"mode": "none"}}, "output": map[string]any{"kind": "structured", "fields": "summary: text"}}), contractNode("out", "output", nil))
	wf.Data = map[string]any{"run_settings": map[string]any{"timeout": map[string]any{"mode": "custom", "minutes": 2}}}
	run, err := s.launchWorkflow(wf, RunWorkflowRequest{UserID: "workflow-resource-probe", Metadata: map[string]string{"locale": "en"}}, nil)
	if err != nil {
		t.Fatal("Live workflow could not be started")
	}
	deadline := time.NewTimer(130 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		snapshot := getRunSnapshot(t, s, run.ID)
		if snapshot.Status != "running" && snapshot.Status != "pending" {
			if snapshot.Status != "completed" {
				t.Fatalf("Live workflow status: %s; invocation was not replayed", snapshot.Status)
			}
			var result map[string]string
			if json.Unmarshal([]byte(snapshot.Output), &result) != nil || result["summary"] != "WF_RESOURCE_OK" {
				t.Fatal("Live template result did not match the requested contract")
			}
			receipt := nodeStatusOf(snapshot, "agent").ResourceReceipts["main"]
			if receipt == nil || len(receipt.Effective.Prompts) != 1 || receipt.Effective.Digest == "" || len(receipt.ToolCalls) != 0 || len(receipt.MemoryHits) != 0 || len(receipt.KnowledgeHits) != 0 {
				t.Fatal("Live resource receipt did not match execution")
			}
			t.Logf("Live structured output verified; provider=%s model=%s run=%s", cfg.LLM.Default, provider.Model, run.ID)
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("Live workflow did not reach a confirmed terminal state; do not replay")
		}
	}
}
