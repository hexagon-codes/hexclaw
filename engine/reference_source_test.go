package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
)

type referenceSearchFixture struct {
	url   string
	calls int
}

func (s *referenceSearchFixture) Name() string        { return "search" }
func (s *referenceSearchFixture) Description() string { return "Source fixture" }
func (s *referenceSearchFixture) Match(string) bool   { return false }
func (s *referenceSearchFixture) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("search", "Source fixture", &llm.Schema{Type: "object"})
}
func (s *referenceSearchFixture) Execute(context.Context, map[string]any) (*skill.Result, error) {
	s.calls++
	return &skill.Result{Content: "A search snippet is not the original page.", Data: []map[string]string{{"url": s.url}}}, nil
}

func TestReferenceSourcePagesReadOriginalAndHonorDisabledTools(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><p>Actual original source</p></html>"))
	}))
	defer server.Close()
	registry := skill.NewRegistry()
	search := &referenceSearchFixture{url: server.URL}
	if err := registry.Register(search); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(builtin.NewBrowserSkill()); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.LLM.Tools.Enabled = "on"
	eng := NewReActEngine(cfg, nil, nil, registry)
	eng.SetToolCollector(NewToolCollector(registry, nil, 8))
	eng.SetToolExecutor(NewToolExecutor(registry, nil))
	pages, err := eng.ReadReferenceSourcePages(t.Context(), "test-agent", "visible printed source")
	if err != nil || len(pages) != 1 || !strings.Contains(pages[0].Content, "Actual original source") || strings.Contains(pages[0].Content, "search snippet") || pages[0].URL != server.URL || pages[0].Digest == "" || search.calls != 1 || requests != 1 {
		t.Fatalf("pages=%+v err=%v search=%d requests=%d", pages, err, search.calls, requests)
	}
	cfg.LLM.Tools.Enabled = "off"
	pages, err = eng.ReadReferenceSourcePages(t.Context(), "test-agent", "visible printed source")
	if err != nil || len(pages) != 0 || search.calls != 1 || requests != 1 {
		t.Fatal("disabled tools still performed network lookup")
	}
	cfg.LLM.Tools.Enabled = "on"
	eng.toolExecutor.AddHook(NewToolPermissionHook(NewToolPermissions(nil, []string{"browser"})))
	pages, err = eng.ReadReferenceSourcePages(t.Context(), "test-agent", "visible printed source")
	if err != nil || len(pages) != 0 || requests != 1 {
		t.Fatal("source reading bypassed an existing tool permission")
	}
}
