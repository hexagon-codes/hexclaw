package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
)

func TestPDFRenderAPIUsesSharedDefaultAndExplicitExceptions(t *testing.T) {
	for _, binary := range []string{"pandoc", "typst", "pdftotext"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s not installed; real PDF API unavailable", binary)
		}
	}
	content := "# Layout verification\n\nFirst page.\n\n```{=typst}\n#pagebreak()\n```\n\nSecond page."
	if path := os.Getenv("HEXCLAW_PDF_REVIEW_MARKDOWN"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		content = string(data)
	}
	renderer, err := render.NewPandocRenderer("", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cache, err := render.NewCache(render.CacheConfig{Dir: t.TempDir(), EngineVersion: "real-render-api-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := render.NewService(render.ServiceConfig{Renderer: renderer, Cache: cache})
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestRenderServer(t, false)
	srv.SetRenderService(svc)
	server := httptest.NewServer(srv.routes())
	defer server.Close()
	output := os.Getenv("HEXCLAW_PDF_EVIDENCE_DIR")
	if output != "" {
		if err := os.MkdirAll(output, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	request := func(name, markdown string, options map[string]any) ([]byte, http.Header) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"content": markdown, "format": "pdf", "options": options})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/v1/render", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+srv.cfg.Server.APIToken)
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK || !bytes.HasPrefix(data, []byte("%PDF-")) {
			t.Fatalf("PDF API %s: status=%d body=%s", name, response.StatusCode, data)
		}
		if output != "" {
			if err := os.WriteFile(filepath.Join(output, name+".pdf"), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return data, response.Header
	}
	for _, tc := range []struct {
		name    string
		options map[string]any
		branded bool
	}{
		{"api-default", nil, true},
		{"api-no-header-footer", map[string]any{"pdf_header_footer": false}, false},
		{"api-custom", map[string]any{"pdf_layout": "custom"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := request(tc.name, content, tc.options)
			path := filepath.Join(t.TempDir(), "render.pdf")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			plain, err := exec.Command("pdftotext", path, "-").Output()
			if err != nil {
				t.Fatal(err)
			}
			for _, page := range strings.Split(string(plain), "\f") {
				if strings.TrimSpace(page) == "" {
					continue
				}
				text := strings.Join(strings.Fields(page), "")
				if strings.Contains(text, "HexClaw河蟹AI") != tc.branded || strings.Contains(text, "本地云端均可部署") != tc.branded {
					t.Fatalf("%s did not apply its own header/footer preference", tc.name)
				}
			}
		})
	}
	first, headers := request("api-default", content, nil)
	if headers.Get("X-Render-Cache-Hit") != "true" {
		t.Fatal("default PDF cache was not reusable after exceptions")
	}
	result, err := builtin.NewExportDocumentSkill(svc).Execute(context.Background(), map[string]any{"content": content, "format": "pdf"})
	if err != nil {
		t.Fatal(err)
	}
	file := result.Data.(*render.RenderResult)
	exported, err := os.ReadFile(file.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, exported) {
		t.Fatal("agent export bypassed the shared PDF renderer/cache")
	}
	if output != "" {
		if err := os.WriteFile(filepath.Join(output, "agent-export-default.pdf"), exported, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if path := os.Getenv("HEXCLAW_PDF_LEGACY_MARKDOWN"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		request("api-legacy-numeric-fractions", string(data), nil)
	}
}
