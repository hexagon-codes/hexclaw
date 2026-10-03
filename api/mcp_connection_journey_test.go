package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/config"
	hexmcp "github.com/hexagon-codes/hexclaw/mcp"
)

// 通过真实 HTTP 路由和 stdio 进程验证添加、查看失败、修正、调用、重启和移除。
func TestMCPConnectionAPIJourney(t *testing.T) {
	mgr := hexmcp.NewManager()
	t.Cleanup(mgr.Close)
	cfg := config.DefaultConfig()
	cfg.Server.APIToken = "fixture-api-token"
	srv := &Server{cfg: cfg, mcpMgr: mgr, credentialResolver: newInMemoryCredentialResolver()}
	api := httptest.NewServer(srv.routes())
	t.Cleanup(api.Close)
	client := &http.Client{Timeout: 15 * time.Second}
	request := func(method, path string, body any) map[string]json.RawMessage {
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
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status=%d body=%s", method, path, res.StatusCode, payload)
		}
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(payload, &decoded); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s %s: %s", method, path, payload)
		return decoded
	}
	serverState := func() mcpServerSummary {
		t.Helper()
		var servers []mcpServerSummary
		if err := json.Unmarshal(request(http.MethodGet, "/api/v1/mcp/servers", nil)["servers"], &servers); err != nil {
			t.Fatal(err)
		}
		if len(servers) != 1 {
			t.Fatalf("server count=%d, want 1", len(servers))
		}
		return servers[0]
	}
	add := addMCPServerRequest{Name: "fixture", Transport: "stdio", Command: filepath.Join(t.TempDir(), "missing-mcp-command")}
	request(http.MethodPost, "/api/v1/mcp/servers", add)
	state := serverState()
	if state.Status != "disconnected" || !strings.Contains(state.LastError, "stage=connect") || state.Retryable == nil || *state.Retryable {
		t.Fatalf("unexpected startup failure state: %+v", state)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	add.Command = executable
	add.Args = []string{"-test.run=^TestMCPAPIProcessFixture$"}
	add.Env = map[string]string{"HEXCLAW_MCP_API_FIXTURE": "unsupported"}
	request(http.MethodPost, "/api/v1/mcp/servers", add)
	state = serverState()
	if !strings.Contains(state.LastError, "stage=initialize") || !strings.Contains(state.LastError, "exit_code=0") {
		t.Fatalf("unexpected handshake failure state: %+v", state)
	}
	if !strings.Contains(state.LastError, "[REDACTED]") || strings.Contains(state.LastError, "fixture-auth-secret") || strings.Contains(state.LastError, "fixture-json-secret") {
		t.Fatalf("unexpected diagnostic output: %s", state.LastError)
	}
	add.Env["HEXCLAW_MCP_API_FIXTURE"] = "healthy"
	if result := request(http.MethodPost, "/api/v1/mcp/servers", add); string(result["connected"]) != "true" {
		t.Fatalf("corrected server did not connect: %s", result["connected"])
	}
	state = serverState()
	if state.Status != "connected" || state.LastError != "" || state.Retryable != nil {
		t.Fatalf("failure state was not cleared: %+v", state)
	}
	tools := request(http.MethodGet, "/api/v1/mcp/tools", nil)
	if !strings.Contains(string(tools["tools"]), "fixture_echo") {
		t.Fatalf("fixture tool was not discovered: %s", tools["tools"])
	}
	callTool := func() {
		t.Helper()
		result := request(http.MethodPost, "/api/v1/mcp/tools/call", map[string]any{"name": "fixture_echo", "arguments": map[string]any{}})
		var output string
		// API 返回工具结果序列化后的字符串。
		if err := json.Unmarshal(result["result"], &output); err != nil || output != `"fixture-ok"` || len(result["error"]) != 0 {
			t.Fatalf("connected MCP tool call failed: %v", result)
		}
	}
	callTool()
	request(http.MethodPost, "/api/v1/mcp/servers/fixture/restart", nil)
	callTool()
	request(http.MethodDelete, "/api/v1/mcp/servers/fixture", nil)
	if result := request(http.MethodGet, "/api/v1/mcp/servers", nil); string(result["total"]) != "0" {
		t.Fatalf("removed server is still listed: %v", result)
	}
}

// 本地 MCP 协议进程；输入关闭即退出，不访问模型、网络或用户配置。
func TestMCPAPIProcessFixture(t *testing.T) {
	mode := os.Getenv("HEXCLAW_MCP_API_FIXTURE")
	if mode == "" {
		return
	}
	if mode == "unsupported" {
		fmt.Fprintln(os.Stderr, `Authorization: Bearer fixture-auth-secret {"token":"fixture-json-secret"}`)
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if err := decoder.Decode(&request); err != nil {
			os.Exit(0)
		}
		if len(request.ID) == 0 {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			version := request.Params.ProtocolVersion
			if mode == "unsupported" {
				version = "unsupported-fixture-version"
			}
			result = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "fixture_echo", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "fixture-ok"}}}
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			os.Exit(1)
		}
	}
}
