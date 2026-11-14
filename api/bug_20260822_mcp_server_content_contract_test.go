package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	hexmcp "github.com/hexagon-codes/hexclaw/mcp"
)

// BUG-20260723-009/010：MCP 服务器列表必须返回可直接投影到 UI 的结构化内容，
// 但不能把 stdio command、args、env 或凭据带回 Desktop。
func TestHandleListMCPServers_ReturnsRedactedStructuredProjection(t *testing.T) {
	command, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestMCPAPIProcessFixture$", "--", "-y", "@modelcontextprotocol/server-filesystem", "--token", "ARG_SECRET"}
	env := map[string]string{"HEXCLAW_MCP_API_FIXTURE": "healthy", "FILESYSTEM_PASSWORD": "ENV_SECRET"}

	mgr := hexmcp.NewManager()
	t.Cleanup(mgr.Close)
	if _, err := mgr.AddServerBestEffort(context.Background(), hexmcp.ServerConfig{
		Name:      "filesystem",
		Transport: "stdio",
		Command:   command,
		Args:      args,
		Env:       env,
	}); err != nil {
		t.Fatalf("AddServerBestEffort: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.MCP.Servers = []config.MCPServerConfig{{
		Name:      "filesystem",
		Transport: "stdio",
		Command:   command,
		Args:      args,
		Env:       env,
		Enabled:   true,
	}}
	srv := NewServer(cfg, &mockEngine{reply: &adapter.Reply{Content: "ok"}}, nil, nil)
	srv.SetMCPManager(mgr)

	w := httptest.NewRecorder()
	srv.handleListMCPServers(w, httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var response struct {
		Servers []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Status      string `json:"status"`
			Transport   string `json:"transport"`
			ToolCount   int    `json:"tool_count"`
		} `json:"servers"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode structured server response: %v; body=%s", err, w.Body.String())
	}
	if response.Total != 1 || len(response.Servers) != 1 {
		t.Fatalf("unexpected response cardinality: total=%d servers=%d", response.Total, len(response.Servers))
	}
	server := response.Servers[0]
	if server.Name != "filesystem" || server.Status != "connected" || server.Transport != "stdio" || server.ToolCount != 1 {
		t.Fatalf("unexpected server projection: %+v", server)
	}
	if strings.TrimSpace(server.Description) == "" {
		t.Fatal("server description must be non-empty")
	}
	for _, secret := range []string{command, "npx", "@modelcontextprotocol/server-filesystem", "--token", "ARG_SECRET", "FILESYSTEM_PASSWORD", "ENV_SECRET"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("server projection leaked %q: %s", secret, w.Body.String())
		}
	}
}

func TestHandleAddMCPServer_UpdatesStructuredProjectionTransport(t *testing.T) {
	command, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfig()
	mgr := hexmcp.NewManager()
	t.Cleanup(mgr.Close)
	srv := NewServer(cfg, &mockEngine{reply: &adapter.Reply{Content: "ok"}}, nil, nil)
	srv.SetMCPManager(mgr)

	body, err := json.Marshal(addMCPServerRequest{
		Name:      "redis",
		Transport: "stdio",
		Command:   command,
		Args:      []string{"-test.run=^TestMCPAPIProcessFixture$"},
		Env:       map[string]string{"HEXCLAW_MCP_API_FIXTURE": "healthy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	add := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers", strings.NewReader(string(body)))
	addResponse := httptest.NewRecorder()
	srv.handleAddMCPServer(addResponse, add)
	if addResponse.Code != http.StatusOK {
		t.Fatalf("add status=%d body=%s", addResponse.Code, addResponse.Body.String())
	}

	listResponse := httptest.NewRecorder()
	srv.handleListMCPServers(listResponse, httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers", nil))
	var response struct {
		Servers []mcpServerSummary `json:"servers"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode list: %v; body=%s", err, listResponse.Body.String())
	}
	if len(response.Servers) != 1 || response.Servers[0].Transport != "stdio" {
		t.Fatalf("dynamic server transport not projected: %+v", response.Servers)
	}
}
