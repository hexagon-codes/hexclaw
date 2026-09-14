package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	hexmcp "github.com/hexagon-codes/hexclaw/mcp"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
	"github.com/hexagon-codes/hexclaw/skill/hub"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// 保留真实路由、配置 Writer、Manager 和协议传输，仅把上游限制在本地。
func newMCPJourneyAPI(t *testing.T, mgr *hexmcp.Manager, writer *config.Writer, setup ...func(*Server)) func(string, string, any) (int, map[string]json.RawMessage) {
	t.Helper()
	t.Cleanup(mgr.Close)
	cfg := config.DefaultConfig()
	cfg.Server.APIToken = "fixture-api-token"
	srv := &Server{cfg: cfg, cfgWriter: writer, mcpMgr: mgr, credentialResolver: newInMemoryCredentialResolver()}
	for _, configure := range setup {
		configure(srv)
	}
	api := httptest.NewServer(srv.routes())
	t.Cleanup(api.Close)
	client := &http.Client{Timeout: 15 * time.Second}
	return func(method, path string, body any) (int, map[string]json.RawMessage) {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(method, api.URL+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer fixture-api-token")
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		payload, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal(payload, &result); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s %s: status=%d body=%s", method, path, res.StatusCode, payload)
		return res.StatusCode, result
	}
}

func TestMCPNetworkAPIJourney(t *testing.T) {
	for _, transport := range []string{"sse", "streamable", "http"} {
		t.Run(transport, func(t *testing.T) {
			upstream := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "fixture", Version: "1"}, &sdkmcp.ServerOptions{PageSize: 1})
			for _, name := range []string{"fixture_alpha", "fixture_beta"} {
				upstream.AddTool(&sdkmcp.Tool{Name: name, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"type": "string"}}}}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
					var args struct {
						Mode string `json:"mode"`
					}
					if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
						return nil, err
					}
					if args.Mode == "structured" {
						return &sdkmcp.CallToolResult{StructuredContent: map[string]any{"answer": "fixture-ok"}}, nil
					}
					if args.Mode == "wait" {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return &sdkmcp.CallToolResult{IsError: args.Mode == "business-error", Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "fixture-ok"}}}, nil
				})
			}
			getServer := func(*http.Request) *sdkmcp.Server { return upstream }
			var handler http.Handler = sdkmcp.NewStreamableHTTPHandler(getServer, nil)
			if transport == "sse" {
				handler = sdkmcp.NewSSEHandler(getServer, nil)
			}
			var rejectConnection atomic.Bool
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if rejectConnection.Load() && ((transport == "sse" && r.Method == http.MethodGet) || (transport != "sse" && r.Header.Get("Mcp-Session-Id") == "")) {
					http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(remote.Close)
			mgr := hexmcp.NewManager()
			request := newMCPJourneyAPI(t, mgr, nil)
			status, added := request(http.MethodPost, "/api/v1/mcp/servers", addMCPServerRequest{Name: "fixture", Transport: transport, Endpoint: remote.URL})
			if status != 200 || string(added["connected"]) != "true" {
				t.Fatalf("connection failed: %v", added)
			}
			_, listed := request(http.MethodGet, "/api/v1/mcp/tools", nil)
			if string(listed["total"]) != "2" {
				t.Errorf("paginated tools missing: %v", listed)
			}
			for _, name := range []string{"fixture_alpha", "fixture_beta"} {
				_, result := request(http.MethodPost, "/api/v1/mcp/tools/call", map[string]any{"name": name, "arguments": map[string]any{}})
				if len(result["error"]) > 0 || !strings.Contains(string(result["result"]), "fixture-ok") {
					t.Errorf("tool %s failed after add request ended: %v", name, result)
				}
			}
			_, structured := request(http.MethodPost, "/api/v1/mcp/tools/call", map[string]any{"name": "fixture_alpha", "arguments": map[string]any{"mode": "structured"}})
			if !strings.Contains(string(structured["result"]), "fixture-ok") {
				t.Errorf("structured result lost: %v", structured)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			_, cancelErr := mgr.CallTool(ctx, "fixture_alpha", map[string]any{"mode": "wait"})
			cancel()
			if !errors.Is(cancelErr, context.DeadlineExceeded) {
				t.Errorf("tool cancellation cause lost: %v", cancelErr)
			}
			_, failed := request(http.MethodPost, "/api/v1/mcp/tools/call", map[string]any{"name": "fixture_alpha", "arguments": map[string]any{"mode": "business-error"}})
			if len(failed["error"]) == 0 {
				t.Error("business error reported as success")
			}
			if statuses := mgr.ServerStatuses(); len(statuses) != 1 || !statuses[0].Connected {
				t.Errorf("business error or request cancellation disconnected server: %+v", statuses)
			}
			collector := engine.NewToolCollector(nil, mgr, 20)
			if defs := collector.Collect(); len(defs) != 2 {
				t.Errorf("Agent did not receive all tools: %d", len(defs))
			}
			result, err := engine.NewToolExecutor(nil, mgr).Execute(context.Background(), "fixture_beta", map[string]any{})
			if err != nil || !strings.Contains(result, "fixture-ok") {
				t.Errorf("Agent MCP execution failed: %q %v", result, err)
			}
			rejectConnection.Store(true)
			status, _ = request(http.MethodPost, "/api/v1/mcp/servers/fixture/restart", nil)
			if status != http.StatusBadGateway {
				t.Errorf("restart failure status=%d", status)
			}
			_, kept := request(http.MethodPost, "/api/v1/mcp/tools/call", map[string]any{"name": "fixture_alpha", "arguments": map[string]any{}})
			if !strings.Contains(string(kept["result"]), "fixture-ok") {
				t.Errorf("failed restart lost working connection: %v", kept)
			}
			request(http.MethodDelete, "/api/v1/mcp/servers/fixture", nil)
		})
	}
}

func TestMCPReconnectAPIJourney(t *testing.T) {
	upstream := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "fixture", Version: "1"}, nil)
	upstream.AddTool(&sdkmcp.Tool{Name: "fixture_echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "fixture-ok"}}}, nil
	})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return upstream }, nil)
	var ready atomic.Bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			http.Error(w, "fixture unavailable", 503)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)
	mgr := hexmcp.NewManager()
	if _, err := mgr.Connect(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	request := newMCPJourneyAPI(t, mgr, nil)
	_, added := request(http.MethodPost, "/api/v1/mcp/servers", addMCPServerRequest{Name: "fixture", Transport: "streamable", Endpoint: remote.URL})
	if string(added["connected"]) != "false" {
		t.Fatalf("unavailable service connected: %v", added)
	}
	ready.Store(true)
	deadline := time.NewTimer(40 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("background reconnect did not restore server")
		case <-ticker.C:
			statuses := mgr.ServerStatuses()
			if len(statuses) != 1 || !statuses[0].Connected {
				continue
			}
			_, result := request(http.MethodPost, "/api/v1/mcp/tools/call", map[string]any{"name": "fixture_echo", "arguments": map[string]any{}})
			if !strings.Contains(string(result["result"]), "fixture-ok") {
				t.Fatalf("reconnected tool failed: %v", result)
			}
			if statuses[0].LastError != "" || statuses[0].Retryable != nil {
				t.Fatalf("reconnect did not clear failure: %+v", statuses)
			}
			return
		}
	}
}

func TestMCPConnectionCancellation(t *testing.T) {
	for _, transport := range []string{"sse", "streamable"} {
		t.Run(transport, func(t *testing.T) {
			ended := make(chan struct{}, 1)
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				<-r.Context().Done()
				select {
				case ended <- struct{}{}:
				default:
				}
			}))
			t.Cleanup(remote.Close)
			mgr := hexmcp.NewManager()
			t.Cleanup(mgr.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			started := time.Now()
			if err := mgr.AddServer(ctx, hexmcp.ServerConfig{Name: "fixture", Transport: transport, Endpoint: remote.URL}); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("connection cancellation cause lost: %v", err)
			}
			if elapsed := time.Since(started); elapsed > 12*time.Second {
				t.Errorf("connection cleanup exceeded budget: %v", elapsed)
			}
			select {
			case <-ended:
			case <-time.After(time.Second):
				t.Fatal("request cancellation did not reach upstream")
			}
		})
	}
}

func TestMCPMarketplaceAPIJourney(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local package-manager fixture requires a POSIX shell")
	}
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "' -test.run='^TestMCPAPIProcessFixture$'\n"
	if err := os.WriteFile(filepath.Join(dir, "npx"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HEXCLAW_MCP_API_FIXTURE", "")
	repoDir := t.TempDir()
	mp := marketplace.NewMarketplace(t.TempDir())
	writeLocalHubCatalog(t, repoDir, `{"version":"1.0.0","updated_at":"2026-08-02T00:00:00Z","skills":[]}`, `{"version":"2.0.0","servers":[{"name":"fixture","type":"mcp","command":"npx","args":["-y","fixture-mcp@1.0.0"],"env":{"HEXCLAW_MCP_API_FIXTURE":"healthy"},"status":"pinned","artifact":{"ecosystem":"npm","package":"fixture-mcp","version":"1.0.0","integrity":"sha512-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==","source_registry":"https://registry.npmjs.org"}}]}`, nil)
	h := hub.New(hub.HubConfig{Enabled: true, RepoURL: "file://" + repoDir}, mp.Dir())
	if err := h.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hexclaw.yaml")
	mgr := hexmcp.NewManager()
	request := newMCPJourneyAPI(t, mgr, config.NewWriter(path), func(s *Server) { s.mp = mp; s.skillHub = h })
	status, listed := request(http.MethodGet, "/api/v1/clawhub/search?type=mcp&q=fixture", nil)
	if status != 200 || !strings.Contains(string(listed["skills"]), "fixture") {
		t.Fatalf("marketplace search failed: %v", listed)
	}
	status, added := request(http.MethodPost, "/api/v1/skills/install", map[string]any{"source": "clawhub://fixture"})
	if status != 200 || string(added["runtime_registered"]) != "true" {
		t.Fatalf("marketplace install failed: %v", added)
	}
	_, servers := request(http.MethodGet, "/api/v1/mcp/servers", nil)
	if !strings.Contains(string(servers["servers"]), `"transport":"stdio"`) {
		t.Errorf("marketplace config missing from server view: %v", servers)
	}
	_, result := request(http.MethodPost, "/api/v1/mcp/tools/call", map[string]any{"name": "fixture_echo", "arguments": map[string]any{}})
	if !strings.Contains(string(result["result"]), "fixture-ok") {
		t.Errorf("marketplace tool unusable: %v", result)
	}
	loaded, err := config.Load(path)
	if err != nil || len(loaded.MCP.Servers) != 1 {
		t.Fatalf("marketplace config not saved: %v", err)
	}
	request(http.MethodDelete, "/api/v1/mcp/servers/fixture", nil)
	status, _ = request(http.MethodDelete, "/api/v1/mcp/servers/fixture", nil)
	if status != http.StatusNotFound {
		t.Errorf("missing server removal status=%d", status)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	status, _ = request(http.MethodPost, "/api/v1/skills/install", map[string]any{"source": "clawhub://fixture"})
	if status != http.StatusServiceUnavailable || len(mgr.ConfiguredServerNames()) != 0 {
		t.Errorf("marketplace write failure changed runtime: status=%d", status)
	}
}

func TestMCPTransportValidationAPI(t *testing.T) {
	mgr := hexmcp.NewManager()
	request := newMCPJourneyAPI(t, mgr, nil)
	for _, transport := range []string{"unsupported", "streamable", "http"} {
		status, _ := request(http.MethodPost, "/api/v1/mcp/servers", addMCPServerRequest{Name: "fixture", Transport: transport})
		if status != http.StatusBadRequest {
			t.Errorf("invalid %s configuration accepted: status=%d", transport, status)
		}
	}
	if len(mgr.ConfiguredServerNames()) != 0 {
		t.Error("invalid transport configuration was registered")
	}
}

func TestMCPConfigReplacementAPIJourney(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hexclaw.yaml")
	writer := config.NewWriter(path)
	mgr := hexmcp.NewManager()
	request := newMCPJourneyAPI(t, mgr, writer)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	add := addMCPServerRequest{Name: "fixture", Transport: "stdio", Command: executable, Args: []string{"-test.run=^TestMCPAPIProcessFixture$"}, Env: map[string]string{"HEXCLAW_MCP_API_FIXTURE": "healthy"}}
	request(http.MethodPost, "/api/v1/mcp/servers", add)
	loaded, err := config.Load(path)
	if err != nil || len(loaded.MCP.Servers) != 1 {
		t.Fatalf("saved config unavailable: %v", err)
	}
	// 用户将配置改为不可用的服务；旧服务不能继续承接新配置的调用。
	add.Env["HEXCLAW_MCP_API_FIXTURE"] = "unsupported"
	_, added := request(http.MethodPost, "/api/v1/mcp/servers", add)
	if string(added["connected"]) != "false" {
		t.Fatalf("bad replacement unexpectedly connected: %v", added)
	}
	_, listed := request(http.MethodGet, "/api/v1/mcp/servers", nil)
	if strings.Contains(string(listed["servers"]), `"status":"connected"`) {
		t.Errorf("failed replacement still reports old connection as connected: %s", listed["servers"])
	}
	_, result := request(http.MethodPost, "/api/v1/mcp/tools/call", map[string]any{"name": "fixture_echo", "arguments": map[string]any{}})
	if len(result["error"]) == 0 {
		t.Errorf("old server still serves calls for replaced config: %v", result)
	}
	add.Env["HEXCLAW_MCP_API_FIXTURE"] = "healthy"
	request(http.MethodPost, "/api/v1/mcp/servers", add)
	mgr.Close()
	loaded, err = config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := hexmcp.NewManager()
	t.Cleanup(reloaded.Close)
	cfg := loaded.MCP.Servers[0]
	ctx, cancel := context.WithCancel(context.Background())
	_, err = reloaded.Connect(ctx, []hexmcp.ServerConfig{{Name: cfg.Name, Transport: cfg.Transport, Command: cfg.Command, Args: cfg.Args, Env: cfg.Env, Enabled: cfg.Enabled}})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	requestReloaded := newMCPJourneyAPI(t, reloaded, writer)
	_, result = requestReloaded(http.MethodPost, "/api/v1/mcp/tools/call", map[string]any{"name": "fixture_echo", "arguments": map[string]any{}})
	if len(result["error"]) > 0 || !strings.Contains(string(result["result"]), "fixture-ok") {
		t.Fatalf("config reload did not restore working server: %v", result)
	}
	requestReloaded(http.MethodDelete, "/api/v1/mcp/servers/fixture", nil)
	loaded, err = config.Load(path)
	if err != nil || len(loaded.MCP.Servers) != 0 {
		t.Errorf("removal not persisted: %v", err)
	}
}

func TestMCPPersistenceFailureAPIJourney(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hexclaw.yaml")
	writer := config.NewWriter(path)
	mgr := hexmcp.NewManager()
	request := newMCPJourneyAPI(t, mgr, writer)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	status, _ := request(http.MethodPost, "/api/v1/mcp/servers", addMCPServerRequest{Name: "fixture", Command: filepath.Join(t.TempDir(), "missing")})
	if status != http.StatusServiceUnavailable {
		t.Errorf("save failure reported as success: status=%d", status)
	}
	if len(mgr.ConfiguredServerNames()) != 0 {
		t.Error("failed save changed runtime config")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	request(http.MethodPost, "/api/v1/mcp/servers", addMCPServerRequest{Name: "fixture", Command: filepath.Join(t.TempDir(), "missing")})
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	status, _ = request(http.MethodDelete, "/api/v1/mcp/servers/fixture", nil)
	if status != http.StatusServiceUnavailable {
		t.Errorf("delete persistence failure reported as success: status=%d", status)
	}
	if names := mgr.ConfiguredServerNames(); len(names) != 1 {
		t.Errorf("failed delete lost runtime config: %v", names)
	}
	// Agent 管理工具与 HTTP 删除入口保留相同的配置一致性。
	installer := builtin.NewMcpInstallerSkill(nil, mgr, writer)
	if _, err := installer.Execute(context.Background(), map[string]any{"action": "remove", "keyword": "fixture"}); err == nil {
		t.Error("Agent removal hid persistence failure")
	}
	if len(mgr.ConfiguredServerNames()) != 1 {
		t.Error("failed Agent removal lost runtime config")
	}
}
