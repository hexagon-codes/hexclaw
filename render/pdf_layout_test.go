package render

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestPDFDefaultAndExplicitLayoutOverridesDoNotShareCache(t *testing.T) {
	cache := newTestCache(t, 0, 0)
	no, yes := false, true
	standard := cache.Key("# PDF", FormatPDF, RenderOptions{})
	if standard != cache.Key("# PDF", FormatPDF, RenderOptions{PDFHeaderFooter: &yes, PDFLayout: "standard"}) {
		t.Fatal("explicit standard layout changed the default cache identity")
	}
	for _, opts := range []RenderOptions{{PDFHeaderFooter: &no}, {PDFLayout: "custom"}} {
		if standard == cache.Key("# PDF", FormatPDF, opts) {
			t.Fatal("PDF layout exception reused the branded default cache")
		}
		if cache.Key("# PDF", FormatDocx, RenderOptions{}) != cache.Key("# PDF", FormatDocx, opts) {
			t.Fatal("PDF-only layout flags changed another format's cache")
		}
	}
}

func TestPDFDefaultBrandingAndExplicitExceptionsRenderRealFiles(t *testing.T) {
	for _, binary := range []string{"pandoc", "typst", "pdftotext"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s not installed; PDF rendering integration unavailable", binary)
		}
	}
	no := false
	for _, tc := range []struct {
		name    string
		opts    RenderOptions
		branded bool
	}{
		{"default", RenderOptions{}, true},
		{"no header footer", RenderOptions{PDFHeaderFooter: &no}, false},
		{"custom", RenderOptions{PDFLayout: "custom"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			renderer := newTestRenderer(t)
			content := "First page content.\n\n```{=typst}\n#pagebreak()\n```\n\nSecond page content."
			result, err := renderer.Render(context.Background(), content, FormatPDF, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			text, err := exec.Command("pdftotext", result.Path, "-").Output()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(text), "First page content.") || !strings.Contains(string(text), "Second page content.") {
				t.Fatal("PDF layout lost document content")
			}
			want := 0
			if tc.branded {
				want = 2
			}
			if got := strings.Count(strings.Join(strings.Fields(string(text)), ""), "HexClaw河蟹AI"); got != want {
				t.Fatalf("header count = %d, want %d", got, want)
			}
			if got := strings.Count(string(text), "本地云端均可部署"); got != want {
				t.Fatalf("footer count = %d, want %d", got, want)
			}
		})
	}
}
