package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	sqlitestore "github.com/hexagon-codes/hexclaw/storage/sqlite"
)

// 通过正式 HTTP 路由修改实例，并重开同一 SQLite 文件验证默认值和绑定的恢复。
func TestAgentDefaultLifecycleHTTPPreservesExplicitSelectionAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "agent-default-lifecycle.db")
	store, err := sqlitefixture.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	var agentStore *agentrouter.SQLiteStore
	t.Cleanup(func() {
		if server != nil {
			server.Close()
		}
		if store != nil {
			_ = store.Close()
		}
	})
	start := func() {
		t.Helper()
		if err := store.Init(ctx); err != nil {
			t.Fatal(err)
		}
		agentStore = agentrouter.NewSQLiteStore(store.DB())
		if err := agentStore.Init(ctx); err != nil {
			t.Fatal(err)
		}
		agents, defaultName, err := agentStore.LoadAgents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		rules, err := agentStore.LoadRules(ctx)
		if err != nil {
			t.Fatal(err)
		}
		dispatcher := agentrouter.New()
		dispatcher.LoadAll(agents, defaultName, rules)
		cfg := config.DefaultConfig()
		cfg.Server.APIToken = e2eAPIToken
		srv := NewServer(cfg, &mockEngine{}, nil, store)
		srv.SetAgentRouter(dispatcher)
		srv.SetAgentStore(agentStore)
		server = httptest.NewServer(srv.routes())
	}
	restart := func() {
		t.Helper()
		server.Close()
		server = nil
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		store, err = sqlitestore.New(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		start()
	}
	do := func(method, path, body string, target any) {
		t.Helper()
		if target == nil {
			var response map[string]any
			target = &response
		}
		apiCanonicalHistoryDoJSON(t, server.Client(), method, server.URL+path, []byte(body), target)
	}
	assertState := func(wantDefault string, wantAgents, wantRules int) {
		t.Helper()
		var listed struct {
			Default string                    `json:"default"`
			Agents  []agentrouter.AgentConfig `json:"agents"`
			Rules   []agentrouter.Rule        `json:"rules"`
		}
		do(http.MethodGet, "/api/v1/agents", "", &listed)
		if listed.Default != wantDefault || len(listed.Agents) != wantAgents || len(listed.Rules) != wantRules {
			t.Fatalf("HTTP state default=%q agents=%d rules=%d, want %q/%d/%d", listed.Default, len(listed.Agents), len(listed.Rules), wantDefault, wantAgents, wantRules)
		}
		stored, defaultName, err := agentStore.LoadAgents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		rules, err := agentStore.LoadRules(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if defaultName != wantDefault || len(stored) != wantAgents || len(rules) != wantRules {
			t.Fatalf("SQLite state default=%q agents=%d rules=%d, want %q/%d/%d", defaultName, len(stored), len(rules), wantDefault, wantAgents, wantRules)
		}
	}
	assertRoute := func(body, wantAgent string, wantSource agentrouter.RouteSource) {
		t.Helper()
		var result struct {
			Matched   bool                    `json:"matched"`
			AgentName string                  `json:"agent_name"`
			Source    agentrouter.RouteSource `json:"source"`
		}
		do(http.MethodPost, "/api/v1/agents/rules/test", body, &result)
		if result.AgentName != wantAgent || result.Source != wantSource || result.Matched != (wantAgent != "") {
			t.Fatalf("route=%+v, want agent=%q source=%q", result, wantAgent, wantSource)
		}
	}
	const boundRequest = `{"platform":"dingtalk","instance_id":"bound-instance","chat_id":"bound-chat"}`
	const unboundRequest = `{"platform":"dingtalk","instance_id":"unbound-instance","chat_id":"unbound-chat"}`
	const apiRequest = `{"platform":"api","chat_id":"ordinary-chat"}`
	start()

	// 名称更靠前的实例加入后，也不能自动成为默认。
	do(http.MethodPost, "/api/v1/agents", `{"name":"zeta","display_name":"Bound assistant"}`, nil)
	do(http.MethodPost, "/api/v1/agents", `{"name":"alpha","display_name":"Other assistant"}`, nil)
	do(http.MethodPost, "/api/v1/agents/rules", `{"platform":"dingtalk","instance_id":"bound-instance","chat_id":"bound-chat","agent_name":"zeta"}`, nil)
	assertState("", 2, 1)
	assertRoute(apiRequest, "", agentrouter.RouteSourceNone)
	assertRoute(boundRequest, "zeta", agentrouter.RouteSourceRule)
	assertRoute(unboundRequest, "", agentrouter.RouteSourceDefault)
	restart()
	assertState("", 2, 1)
	assertRoute(apiRequest, "", agentrouter.RouteSourceNone)
	assertRoute(boundRequest, "zeta", agentrouter.RouteSourceRule)

	// 显式默认跨新实例注册和重启保留，钉钉仍按自身绑定处理。
	do(http.MethodPost, "/api/v1/agents/default", `{"name":"alpha"}`, nil)
	do(http.MethodPost, "/api/v1/agents", `{"name":"beta"}`, nil)
	assertState("alpha", 3, 1)
	restart()
	assertState("alpha", 3, 1)
	assertRoute(apiRequest, "alpha", agentrouter.RouteSourceDefault)
	assertRoute(boundRequest, "zeta", agentrouter.RouteSourceRule)
	assertRoute(unboundRequest, "", agentrouter.RouteSourceDefault)

	// 清除默认后，重启、注册和删除普通实例均不能重新认领默认。
	do(http.MethodPost, "/api/v1/agents/default", `{"name":""}`, nil)
	assertState("", 3, 1)
	restart()
	assertState("", 3, 1)
	do(http.MethodPost, "/api/v1/agents", `{"name":"gamma"}`, nil)
	assertState("", 4, 1)
	do(http.MethodDelete, "/api/v1/agents/beta", "", nil)
	assertState("", 3, 1)
	restart()
	assertState("", 3, 1)
	assertRoute(apiRequest, "", agentrouter.RouteSourceNone)

	// 删除默认实例清除其规则，但保留其他实例及其钉钉绑定。
	do(http.MethodPost, "/api/v1/agents/default", `{"name":"alpha"}`, nil)
	do(http.MethodPost, "/api/v1/agents/rules", `{"platform":"dingtalk","instance_id":"deleted-instance","chat_id":"deleted-chat","agent_name":"alpha"}`, nil)
	assertState("alpha", 3, 2)
	do(http.MethodDelete, "/api/v1/agents/alpha", "", nil)
	assertState("", 2, 1)
	restart()
	assertState("", 2, 1)
	assertRoute(apiRequest, "", agentrouter.RouteSourceNone)
	assertRoute(boundRequest, "zeta", agentrouter.RouteSourceRule)
	assertRoute(`{"platform":"dingtalk","instance_id":"deleted-instance","chat_id":"deleted-chat"}`, "", agentrouter.RouteSourceDefault)
}
