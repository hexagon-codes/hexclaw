package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hexagon-codes/hexclaw/mcp"
	"github.com/hexagon-codes/hexclaw/skill"
)

// 增量6 闭环回归：cron / webhook / workflow / chat 四条触发链都调同一个 engine.Process →
// 同一个 ToolCollector.CollectFiltered。CollectFiltered(query, activation) **不接触发源参数**，
// 且 MCP 工具是「不参与 activation 过滤」地无条件并入（engine/tools.go）。
// 因此只要 mcpMgr 有工具，无论触发源是谁、query 是否为空（cron 派发无用户 query），
// MCP 工具都会进入 LLM 工具列表 —— 这正是「云端已证 chat→mysql_query」对 cron/webhook/workflow
// 同样成立的结构性根据。本测试把该不变量钉死：未来若有人按触发源/平台门控工具收集，立即 FAIL。
func TestMCPToolsCollectedRegardlessOfTriggerSource_20260623(t *testing.T) {
	// 使用官方 SDK 的内存/HTTP 传输，避免真实子进程，同时经过当前生产连接路径。
	upstream := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "mysql", Version: "1"}, nil)
	upstream.AddTool(&sdkmcp.Tool{
		Name:        "mysql_query",
		Description: "run sql",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}}}, nil
	})
	remote := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return upstream
	}, nil))
	t.Cleanup(remote.Close)

	mgr := mcp.NewManager()
	t.Cleanup(mgr.Close)
	if connected, err := mgr.AddServerBestEffort(context.Background(), mcp.ServerConfig{
		Name: "mysql", Transport: "streamable", Endpoint: remote.URL,
	}); err != nil || !connected {
		t.Fatalf("准备 MCP server 失败: connected=%v err=%v", connected, err)
	}

	tc := NewToolCollector(skill.NewRegistry(), mgr, 40)

	// 代表性 query：空（cron 派发/定时无用户输入）、中文意图、任意串——都应收集到 MCP 工具。
	for _, q := range []string{"", "查询数据库里的用户", "do something unrelated"} {
		tools := tc.CollectFiltered(q, skill.Activation{})
		found := false
		for _, td := range tools {
			if td.Function.Name == "mysql_query" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("query=%q：MCP 工具 mysql_query 未被收集——触发源/空 query 不应门控 MCP 工具", q)
		}
	}
}
