package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/secret"
)

// 自定义启动路径上的模型设置和 MCP 交替保存，重载后必须仍是一份完整配置。
func TestRuntimeConfigPathPreservesWriterFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	cfg := config.DefaultConfig()
	cfg.Server.APIToken = "fixture-business-token"
	if err := config.Save(cfg, path); err != nil {
		t.Fatal(err)
	}
	box, err := secret.LoadBox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writer := config.NewWriter(path)
	writer.SetSecretBox(box)
	srv := NewServer(cfg, &mockEngine{activeLLM: cfg.LLM}, nil, nil)
	srv.SetRuntimeConfigPath(path)
	srv.SetCfgWriter(writer)
	if err := writer.AppendMCPServer("fixture", "stdio", "fixture-command", nil, map[string]string{"TOKEN": "fixture-mcp-value"}, ""); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/config/llm", strings.NewReader(`{"default":"fixture","providers":{"fixture":{"api_key":"fixture-provider-key","base_url":"https://example.invalid/v1","model":"fixture-model","compatible":"openai","locality":"cloud"}}}`))
	req.Header.Set("Authorization", "Bearer fixture-business-token")
	w := httptest.NewRecorder()
	srv.routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("save status=%d: %s", w.Code, w.Body.String())
	}
	if err := writer.AppendMCPServer("second", "sse", "", nil, nil, "https://example.invalid/mcp"); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LLM.Providers["fixture"].APIKey != "fixture-provider-key" || len(loaded.MCP.Servers) != 2 {
		t.Fatal("interleaved save lost a configuration section")
	}
	if !secret.IsEncrypted(loaded.MCP.Servers[0].Env["TOKEN"]) {
		t.Fatal("MCP credential lost at-rest encoding")
	}
	decoded, err := writer.GetMCPServer("fixture")
	if err != nil || decoded.Env["TOKEN"] != "fixture-mcp-value" {
		t.Fatal("MCP credential cannot be recovered")
	}
	if srv.cfg.MCP.Servers[0].Env["TOKEN"] != "fixture-mcp-value" {
		t.Fatal("runtime received ciphertext")
	}
	if _, err := os.Stat(filepath.Join(home, ".hexclaw", "hexclaw.yaml")); !os.IsNotExist(err) {
		t.Fatal("custom config save wrote the default path")
	}
}
