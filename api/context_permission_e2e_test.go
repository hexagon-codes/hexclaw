package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	mockllm "github.com/hexagon-codes/hexagon/testing/mock"
	enginepkg "github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/knowledge"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/skill"
)

type contextPermissionFileSkill struct{ path string }

func (*contextPermissionFileSkill) Name() string        { return "shell" }
func (*contextPermissionFileSkill) Description() string { return "Write the isolated acceptance file" }
func (*contextPermissionFileSkill) Match(content string) bool {
	return strings.HasPrefix(content, "/sh ")
}
func (*contextPermissionFileSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("shell", "Write the isolated acceptance file", &llm.Schema{Type: "object"})
}
func (s *contextPermissionFileSkill) Execute(context.Context, map[string]any) (*skill.Result, error) {
	if err := os.WriteFile(s.path, []byte("PERMISSION_ACCEPTED"), 0600); err != nil {
		return nil, err
	}
	return &skill.Result{Content: "PERMISSION_ACCEPTED"}, nil
}

type contextPermissionSender struct {
	hub   *enginepkg.PermissionHub
	calls atomic.Int32
}

func (s *contextPermissionSender) SendPermissionRequest(_ context.Context, sessionID string, req *enginepkg.PermissionRequest) error {
	s.calls.Add(1)
	s.hub.HandleResponse(enginepkg.PermissionResponse{RequestID: req.ID, OwnerID: req.OwnerID, SessionID: sessionID, InvocationID: req.InvocationID, ArgumentsDigest: req.ArgumentsDigest, SecurityScopeDigest: req.SecurityScopeDigest, Approved: true})
	return nil
}

func TestContextPermissionProfileHTTPAPI(t *testing.T) {
	cfg := apiCanonicalHistoryConfig()
	cfg.LLM.Tools.Enabled = "on"
	cfg.Knowledge.Enabled = true
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "permission.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	registry := skill.NewRegistry()
	artifact := filepath.Join(t.TempDir(), "accepted.txt")
	if err := registry.Register(&contextPermissionFileSkill{path: artifact}); err != nil {
		t.Fatal(err)
	}
	provider := mockllm.NewLLMProvider("test").WithResponseFn(func(req hexagon.CompletionRequest) (*hexagon.CompletionResponse, error) {
		for _, m := range req.Messages {
			if m.Role == llm.RoleTool {
				return &hexagon.CompletionResponse{Content: "PERMISSION_ACCEPTED"}, nil
			}
		}
		return &hexagon.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "permission-call", Name: "shell", Arguments: `{}`}}}, nil
	})
	router := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"test": contextAPIStreamProvider{provider}})
	eng := enginepkg.NewReActEngine(cfg, router, store, registry)
	collector := enginepkg.NewToolCollector(registry, nil, 40)
	executor := enginepkg.NewToolExecutor(registry, nil)
	hub := enginepkg.NewPermissionHub(0)
	sender := &contextPermissionSender{hub: hub}
	hub.SetSender(sender)
	hook := enginepkg.NewPermissionHook(hub, enginepkg.WithPolicy(enginepkg.DefaultBaselinePolicy()), enginepkg.WithSystemDispatchPolicy(enginepkg.DefaultSystemDispatchPolicy()))
	executor.AddHook(hook)
	eng.SetToolExecutor(executor)
	eng.SetToolCollector(collector)
	kbStore := knowledge.NewSQLiteStore(store.DB())
	if err := kbStore.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := kbStore.Add(t.Context(), &knowledge.Document{ID: "permission-doc", Title: "permission-fixture", Source: "manual", Status: "indexed", ChunkCount: 1}, []*knowledge.Chunk{{ID: "permission-chunk", DocID: "permission-doc", Content: "alpha reference material", Embedding: []float32{1, 0, 0}}}); err != nil {
		t.Fatal(err)
	}
	eng.SetKnowledgeBase(knowledge.NewManager(kbStore, kbStore, kwEmbedder{}))
	if err := eng.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	srv := NewServer(cfg, eng, nil, store)
	srv.SetDesktopAPIToken(e2eAPIToken)
	srv.SetAutonomy(hook, nil, nil, filepath.Join(t.TempDir(), "hexclaw.yaml"))
	ts := httptest.NewServer(srv.routes())
	t.Cleanup(ts.Close)
	for _, profile := range []string{"function_first", "balanced", "strict", "full_access"} {
		t.Run(profile, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"profile": profile})
			var state map[string]any
			apiCanonicalHistoryDoJSON(t, ts.Client(), http.MethodPut, ts.URL+"/api/v1/autonomy/profile", body, &state)
			if state["profile"] != profile {
				t.Fatalf("profile=%v", state)
			}
			apiCanonicalHistoryDoJSON(t, ts.Client(), http.MethodGet, ts.URL+"/api/v1/autonomy/profile", nil, &state)
			if state["profile"] != profile {
				t.Fatal("setting not effective")
			}
			for name, prompt := range map[string]string{"model": "请依据资料 alpha 执行本次临时文件验收", "fast": "/sh permission-fixture"} {
				_ = os.Remove(artifact)
				before := sender.calls.Load()
				body, _ = json.Marshal(ChatRequest{Message: prompt, SessionID: "sess-permission-" + profile + "-" + name, RequestID: "req-permission-" + profile + "-" + name, Metadata: map[string]string{"pinned_agent": "default", "memory": "off"}})
				response := apiMultiTurnRequest(t, ts.Client(), ts.URL+"/api/v1/chat", body, false)
				if response.Reply != "PERMISSION_ACCEPTED" {
					t.Fatalf("reply=%q", response.Reply)
				}
				if name == "model" && len(response.KnowledgeHits) == 0 {
					t.Fatal("knowledge evidence was not exercised")
				}
				data, err := os.ReadFile(artifact)
				if err != nil || string(data) != "PERMISSION_ACCEPTED" {
					t.Fatalf("file missing: %v", err)
				}
				approvals := sender.calls.Load() - before
				want := int32(1)
				if profile == "full_access" {
					want = 0
				}
				if approvals != want {
					t.Fatalf("approval count=%d want=%d", approvals, want)
				}
			}
		})
	}
}
