package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
)

func TestBUG20260726034A01GenericK12AgentPUTRejectsOwnedFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "display_name", body: `{"display_name":"新名称"}`},
		{name: "system_prompt", body: `{"system_prompt":"新人设"}`},
		{name: "provider", body: `{"provider":"new-provider"}`},
		{name: "model", body: `{"model":"new-model"}`},
		{name: "skills", body: `{"skills":["new-skill"]}`},
		{
			name: "profile_owned_metadata",
			body: `{"metadata":{"scenario":"k12-tutor","k12.child_name":"另一个孩子",
				"k12.grade_term":"五年级下","custom.note":"keep"}}`,
		},
	}
	for _, tt := range tests {
		for _, withDescription := range []bool{false, true} {
			name, body := tt.name, tt.body
			if withDescription {
				name += "_with_description"
				body = strings.TrimSuffix(body, "}") + `,"description":"新描述"}`
			}
			t.Run(name, func(t *testing.T) {
				dispatcher := agentrouter.New()
				original := agentrouter.AgentConfig{
					Name: "mingming", DisplayName: "明明的辅导助手",
					Description: "旧描述", SystemPrompt: "旧人设",
					Provider: "old-provider", Model: "old-model",
					Skills: []string{"solve"}, MaxTokens: 2048,
					ReasoningPolicy: &config.ReasoningPolicy{Mode: "off"},
					Metadata: map[string]string{
						"scenario":       "k12-tutor",
						"k12.child_name": "明明",
						"k12.grade_term": "五年级下",
						"custom.note":    "keep",
					},
				}
				if err := dispatcher.Register(original); err != nil {
					t.Fatal(err)
				}
				srv := NewServer(config.DefaultConfig(), &mockEngine{}, nil, nil)
				srv.SetAgentRouter(dispatcher)

				req := httptest.NewRequest(http.MethodPut,
					"/api/v1/agents/mingming", strings.NewReader(body))
				req.SetPathValue("name", "mingming")
				rec := httptest.NewRecorder()
				srv.handleUpdateAgent(rec, req)

				if rec.Code != http.StatusConflict {
					t.Errorf("status=%d want 409; body=%s", rec.Code, rec.Body.String())
				}
				if got := strings.TrimSpace(rec.Body.String()); got !=
					`{"error":"K12 profile fields require /api/k12/profile-bundle"}` {
					t.Errorf("body=%s", got)
				}
				stored, ok := dispatcher.GetAgent("mingming")
				if !ok {
					t.Fatal("K12 agent disappeared")
				}
				if !reflect.DeepEqual(*stored, original) {
					t.Errorf("rejected generic PUT mutated K12-owned state: %+v", stored)
				}
			})
		}
	}
}

func TestGenericK12AgentPUTDescriptionOnlyPersistsWithoutChangingOwnedFields(t *testing.T) {
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	agentStore := agentrouter.NewSQLiteStore(store.DB())
	if err := agentStore.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	original := agentrouter.AgentConfig{
		Name: "mingming", DisplayName: "明明的辅导助手",
		Description: "旧描述", SystemPrompt: "旧人设",
		Provider: "old-provider", Model: "old-model",
		Skills: []string{"solve"}, MaxTokens: 2048,
		ReasoningPolicy: &config.ReasoningPolicy{Mode: "off"},
		Metadata: map[string]string{
			"scenario": "k12-tutor", "avatar": "🎓",
			"k12.child_name": "明明", "k12.grade_term": "五年级下",
			"k12.textbook_edition": "人教版", "custom.note": "keep",
		},
	}
	dispatcher := agentrouter.New()
	if err := dispatcher.Register(original); err != nil {
		t.Fatal(err)
	}
	if err := agentStore.SaveAgent(context.Background(), &original); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(config.DefaultConfig(), &mockEngine{}, nil, store)
	srv.SetAgentRouter(dispatcher)
	srv.SetAgentStore(agentStore)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/agents/mingming",
		strings.NewReader(`{"description":"五年级下 · 数学教材与当前进度 · 按年级边界讲解"}`))
	req.SetPathValue("name", "mingming")
	rec := httptest.NewRecorder()
	srv.handleUpdateAgent(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("description-only status=%d want 200; body=%s", rec.Code, rec.Body.String())
	}
	want := original
	want.Description = "五年级下 · 数学教材与当前进度 · 按年级边界讲解"
	stored, ok := dispatcher.GetAgent(original.Name)
	if !ok || !reflect.DeepEqual(*stored, want) {
		t.Fatalf("description-only update changed unrelated in-memory fields: %+v", stored)
	}
	persisted, _, err := agentStore.LoadAgents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || !reflect.DeepEqual(persisted[0], want) {
		t.Fatalf("description-only update did not preserve persisted owned fields: %+v", persisted)
	}
}

func TestBUG20260726034A01GenericK12AgentPUTKeepsGenericFieldsPatchable(t *testing.T) {
	dispatcher := agentrouter.New()
	original := agentrouter.AgentConfig{
		Name: "mingming", DisplayName: "明明的辅导助手",
		Metadata: map[string]string{
			"scenario":       "k12-tutor",
			"k12.child_name": "明明",
			"k12.grade_term": "五年级下",
			"custom.note":    "old",
		},
	}
	if err := dispatcher.Register(original); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(config.DefaultConfig(), &mockEngine{}, nil, nil)
	srv.SetAgentRouter(dispatcher)
	body := `{"max_tokens":3072,
		"metadata":{"scenario":"k12-tutor","k12.child_name":"明明",
		"k12.grade_term":"五年级下","custom.note":"new"}}`
	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/agents/mingming", strings.NewReader(body))
	req.SetPathValue("name", "mingming")
	rec := httptest.NewRecorder()
	srv.handleUpdateAgent(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("generic-only patch status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, _ := dispatcher.GetAgent("mingming")
	if stored.MaxTokens != 3072 || len(stored.Skills) != 0 ||
		stored.Metadata["custom.note"] != "new" {
		t.Fatalf("generic-only fields were not patched: %+v", stored)
	}
}
