package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/hexclaw/engine"
	hexmcp "github.com/hexagon-codes/hexclaw/mcp"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpIdentityHook struct {
	calls []engine.ToolCallInfo
}

func (*mcpIdentityHook) Priority() int { return 1 }

func (h *mcpIdentityHook) AfterToolCall(_ context.Context, call *engine.ToolCallInfo, _ *engine.ToolCallResult) {
	h.calls = append(h.calls, *call)
}

// 两个独立协议服务使用不同必填参数与结果，错误路由不能伪装成成功。
func TestMCPToolIdentityAPIJourney(t *testing.T) {
	endpoints := map[string]string{}
	var calls atomic.Int32
	for _, owner := range []string{"alpha", "beta"} {
		upstream := sdkmcp.NewServer(&sdkmcp.Implementation{Name: owner, Version: "1"}, nil)
		upstream.AddTool(&sdkmcp.Tool{
			Name: "query", Description: owner,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{owner: map[string]any{"type": "string"}}, "required": []string{owner}},
		}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			calls.Add(1)
			var args map[string]string
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return nil, err
			}
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: owner + ":" + args[owner]}}}, nil
		})
		remote := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return upstream }, nil))
		t.Cleanup(remote.Close)
		endpoints[owner] = remote.URL
	}
	mgr := hexmcp.NewManager()
	request := newMCPJourneyAPI(t, mgr, nil)
	add := func(owner string) {
		t.Helper()
		status, body := request(http.MethodPost, "/api/v1/mcp/servers", addMCPServerRequest{Name: owner, Transport: "streamable", Endpoint: endpoints[owner]})
		if status != 200 || string(body["connected"]) != "true" {
			t.Fatalf("add %s: status=%d body=%s", owner, status, body)
		}
	}
	call := func(owner, expected string) {
		t.Helper()
		_, body := request(http.MethodPost, "/api/v1/mcp/tools/call", MCPToolCallRequest{ServerName: owner, Name: "query", Arguments: map[string]any{expected: "api"}})
		if len(body["error"]) > 0 || !strings.Contains(string(body["result"]), expected+":api") {
			t.Fatalf("call %s: %s", owner, body)
		}
	}
	collector := engine.NewToolCollector(nil, mgr, 20)
	add("alpha")
	call("", "alpha")
	if defs := collector.Collect(); len(defs) != 1 || defs[0].Function.Name != "query" {
		t.Fatalf("unique tool must retain its original callable name: %v", defs)
	}
	add("beta")
	_, body := request(http.MethodGet, "/api/v1/mcp/tools", nil)
	var infos []hexmcp.ToolInfo
	if err := json.Unmarshal(body["tools"], &infos); err != nil || len(infos) != 2 {
		t.Fatalf("both original tools must be visible: %s", body)
	}
	for _, info := range infos {
		schema, _ := info.InputSchema.(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		if info.Name != "query" || properties[info.ServerName] == nil {
			t.Fatalf("display name or schema lost its owner: %+v", info)
		}
	}
	before := calls.Load()
	_, ambiguous := request(http.MethodPost, "/api/v1/mcp/tools/call", MCPToolCallRequest{Name: "query"})
	_, missing := request(http.MethodPost, "/api/v1/mcp/tools/call", MCPToolCallRequest{ServerName: "missing", Name: "query"})
	if !strings.Contains(string(ambiguous["error"]), "ambiguous") || len(missing["error"]) == 0 || calls.Load() != before {
		t.Fatalf("ambiguous/unknown owner must not execute: %s / %s, calls=%d", ambiguous, missing, calls.Load()-before)
	}
	for _, owner := range []string{"alpha", "beta"} {
		call(owner, owner)
	}
	defs := collector.Collect()
	if len(defs) != 2 || defs[0].Function.Name == defs[1].Function.Name {
		t.Fatalf("Agent must receive two distinct callable tools: %v", defs)
	}
	executor := engine.NewToolExecutor(nil, mgr)
	hook := &mcpIdentityHook{}
	executor.AddHook(hook)
	validName := regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	for i, def := range defs {
		owner, name := "alpha", def.Function.Name
		if def.Function.Parameters.Properties["beta"] != nil {
			owner = "beta"
		}
		if !strings.Contains(def.Function.Description, "MCP server: "+owner+"; tool: query.") {
			t.Fatalf("Agent must be able to identify the tool owner: %s", def.Function.Description)
		}
		if !validName.MatchString(name) || !executor.HasTool(name) || collector.Collect()[i].Function.Name != name {
			t.Fatalf("Agent tool must be valid, discoverable and stable: %s", name)
		}
		result, err := executor.Execute(context.Background(), name, map[string]any{owner: "agent"})
		if err != nil || !strings.Contains(result, owner+":agent") {
			t.Fatalf("Agent call routed incorrectly: owner=%s result=%s err=%v", owner, result, err)
		}
		if len(hook.calls) != i+1 || hook.calls[i].Name != "query" || hook.calls[i].ServerName != owner {
			t.Fatalf("existing hooks must retain original tool semantics and exact owner: %+v", hook.calls)
		}
	}
	status, removed := request(http.MethodDelete, "/api/v1/mcp/servers/beta", nil)
	if status != 200 || len(removed["error"]) > 0 {
		t.Fatalf("remove: %s", removed)
	}
	call("", "alpha")
	if defs := collector.Collect(); len(defs) != 1 || defs[0].Function.Name != "query" {
		t.Fatalf("remaining unique tool must restore original name: %v", defs)
	}
	if got := calls.Load(); got != 6 {
		t.Fatalf("unexpected upstream calls: got %d, want 6", got)
	}
}
