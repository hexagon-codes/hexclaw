package api

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleExtractPandocDocumentsRealFormats(t *testing.T) {
	bin := findTool("pandoc", "/opt/homebrew/bin/pandoc", "/usr/local/bin/pandoc", "/usr/bin/pandoc")
	if bin == "" {
		t.Skip("Pandoc is unavailable; its real document extraction boundary not verified")
	}
	dir := t.TempDir()
	img, err := os.Create(filepath.Join(dir, "visual.png"))
	if err != nil {
		t.Fatal(err)
	}
	swatch := image.NewRGBA(image.Rect(0, 0, 2, 2))
	swatch.Set(0, 0, color.RGBA{B: 255, A: 255})
	if err := png.Encode(img, swatch); err != nil {
		_ = img.Close()
		t.Fatal(err)
	}
	if err := img.Close(); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "source.md")
	if err := os.WriteFile(input, []byte("# 文档参考\n\nPANDOC_DOCUMENT_REFERENCE\n\n中文正文：甲乙丙。\n\n![Visual example](visual.png)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ext    string
		writer string
	}{
		{"html", "html5"},
		{"htm", "html5"},
		{"odt", "odt"},
		{"rtf", "rtf"},
		{"epub", "epub"},
	} {
		t.Run(tc.ext, func(t *testing.T) {
			output := filepath.Join(dir, "document."+tc.ext)
			cmd := exec.CommandContext(t.Context(), bin, "--from", "markdown", "--to", tc.writer, "--standalone", "--metadata", "title=Document reference", "--output", output, input)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("generate real %s document: %v: %s", tc.ext, err, out)
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			rec := postExtract(t, filepath.Base(output), data)
			if rec.Code != http.StatusOK {
				t.Fatalf("extract actual %s document: HTTP %d: %s", tc.ext, rec.Code, rec.Body.String())
			}
			var result DocumentExtractResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Text, "PANDOC_DOCUMENT_REFERENCE") || !strings.Contains(result.Text, "甲乙丙") {
				t.Fatalf("actual %s document body was not preserved: %q", tc.ext, result.Text)
			}
			if result.FileName != filepath.Base(output) {
				t.Fatalf("unexpected document name: %q", result.FileName)
			}
			if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, " "), "not visually analyzed") {
				t.Fatalf("text-only extraction must preserve its visual limitation: %v", result.Warnings)
			}
		})
	}
}
