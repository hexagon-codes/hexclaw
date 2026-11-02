package mcp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
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
	imageDigest := sha256.Sum256(imageData)
	wantDigest := "sha256:" + hex.EncodeToString(imageDigest[:])

	const summary = "The marked worksheet is ready."
	const artifactURI = "mcp://worksheet/result/question-3.png"
	const questionID = "worksheet-question-3"

	tests := []struct {
		name     string
		result   *sdkmcp.CallToolResult
		want     []string
		hasImage bool
	}{
		{
			name: "image_only",
			result: &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{
					&sdkmcp.ImageContent{Data: imageData, MIMEType: "image/png"},
				},
			},
			want:     []string{"image/png", wantDigest},
			hasImage: true,
		},
		{
			name: "text_and_image",
			result: &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{
					&sdkmcp.TextContent{Text: summary},
					&sdkmcp.ImageContent{Data: imageData, MIMEType: "image/png"},
				},
			},
			want:     []string{summary, "image/png", wantDigest},
			hasImage: true,
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

			// 文本保留摘要、媒体描述及资产引用；图片原字节走独立的内容合同。
			got := result.String()
			for _, required := range tc.want {
				if !strings.Contains(got, required) {
					t.Errorf("required MCP content missing from public result: want %q in %s", required, got)
				}
			}
			if strings.Contains(got, imageBase64) {
				t.Error("image encoding must not be copied into tool text")
			}
			content, ok := result.Output.(interface {
				ToolMessageContent() []llm.ContentPart
				ToolBinaryContents() []hexagonmcp.BinaryContent
			})
			if !ok {
				t.Fatal("MCP result does not expose typed content")
			}
			parts, binaries := content.ToolMessageContent(), content.ToolBinaryContents()
			if !tc.hasImage {
				if len(parts) != 0 || len(binaries) != 0 {
					t.Fatal("text-only result unexpectedly contains image data")
				}
				return
			}
			if len(parts) != 1 || parts[0].Type != "image_url" || parts[0].ImageURL == nil {
				t.Fatal("MCP image observation missing from typed content")
			}
			encoded, ok := strings.CutPrefix(parts[0].ImageURL.URL, "data:image/png;base64,")
			if !ok {
				t.Fatal("MCP image observation MIME changed")
			}
			observed, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || !bytes.Equal(observed, imageData) {
				t.Fatal("MCP image observation bytes changed")
			}
			if len(binaries) != 1 || binaries[0].Type != "image" || binaries[0].MIMEType != "image/png" || !bytes.Equal(binaries[0].Data, imageData) {
				t.Fatal("MCP binary image bytes or MIME changed")
			}
		})
	}
}
