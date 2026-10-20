package mcp_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	hexagonmcp "github.com/hexagon-codes/hexagon/mcp"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP 的图片与结构化结果是必要产物，不能因同时存在文字摘要而被丢弃。
func TestMCPProxyPreservesRequiredResultContent(t *testing.T) {
	const imageBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRz0AAAAASUVORK5CYII="
	imageData, err := base64.StdEncoding.DecodeString(imageBase64)
	if err != nil {
		t.Fatalf("decode image fixture: %v", err)
	}

	const summary = "The marked worksheet is ready."
	const artifactURI = "mcp://worksheet/result/question-3.png"
	const questionID = "worksheet-question-3"

	tests := []struct {
		name   string
		result *sdkmcp.CallToolResult
		want   []string
	}{
		{
			name: "image_only",
			result: &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{
					&sdkmcp.ImageContent{Data: imageData, MIMEType: "image/png"},
				},
			},
			want: []string{"image/png", imageBase64},
		},
		{
			name: "text_and_image",
			result: &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{
					&sdkmcp.TextContent{Text: summary},
					&sdkmcp.ImageContent{Data: imageData, MIMEType: "image/png"},
				},
			},
			want: []string{summary, "image/png", imageBase64},
		},
		{
			name: "text_and_structured_artifact",
			result: &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{
					&sdkmcp.TextContent{Text: summary},
				},
				StructuredContent: map[string]any{
					"question_id":  questionID,
					"artifact_uri": artifactURI,
				},
			},
			want: []string{summary, questionID, artifactURI},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)

			server := sdkmcp.NewServer(&sdkmcp.Implementation{
				Name:    "worksheet-content",
				Version: "1.0.0",
			}, nil)
			server.AddTool(&sdkmcp.Tool{
				Name:        "read_marked_worksheet",
				Description: "Return the marked worksheet and its associated content.",
				InputSchema: map[string]any{"type": "object"},
			}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
				return tc.result, nil
			})

			// 通过真实 SDK 协议与公共代理调用，内存传输不访问网络或启动子进程。
			serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
			serverSession, err := server.Connect(ctx, serverTransport, nil)
			if err != nil {
				t.Fatalf("connect in-memory server: %v", err)
			}
			t.Cleanup(func() { _ = serverSession.Close() })

			tools, closer, err := hexagonmcp.ConnectMCPServerV2(ctx, clientTransport)
			if err != nil {
				t.Fatalf("connect public MCP proxy: %v", err)
			}
			t.Cleanup(func() { _ = closer.Close() })
			if len(tools) != 1 || tools[0].Name() != "read_marked_worksheet" {
				t.Fatalf("expected the worksheet tool, got %d tools", len(tools))
			}

			result, err := tools[0].Execute(ctx, map[string]any{})
			if err != nil {
				t.Fatalf("execute worksheet tool: %v", err)
			}
			if !result.Success {
				t.Fatalf("worksheet tool reported failure: %s", result.Error)
			}

			// 应用通过 String 回填工具结果；同时检查摘要、图片内容和可引用资产。
			got := result.String()
			for _, required := range tc.want {
				if !strings.Contains(got, required) {
					t.Errorf("required MCP content missing from public result: want %q in %s", required, got)
				}
			}
		})
	}
}
