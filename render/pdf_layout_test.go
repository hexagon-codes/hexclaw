package render

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"runtime"
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
			// 保留失败证据及Windows默认样张，供实际字体和跨平台产物核对。
			t.Cleanup(func() {
				if !t.Failed() && (runtime.GOOS != "windows" || !tc.branded) {
					return
				}
				for _, binary := range []string{"pandoc", "typst", "pdftotext", "pdffonts"} {
					args := []string{"--version"}
					if binary == "pdftotext" {
						args = []string{"-v"}
					} else if binary == "pdffonts" {
						args = []string{result.Path}
					}
					output, err := exec.Command(binary, args...).CombinedOutput()
					t.Logf("PDF diagnostic %s: %s; err=%v", binary, output, err)
				}
				if data, err := os.ReadFile(result.Path); err == nil && len(data) <= 128<<10 {
					t.Logf("PDF fixture base64=%s", base64.StdEncoding.EncodeToString(data))
				}
			})
			// 不同发行版的默认编码不一致，提取结果固定为UTF-8。
			text, err := exec.Command("pdftotext", "-enc", "UTF-8", result.Path, "-").Output()
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
			compactText := strings.Join(strings.Fields(string(text)), "")
			// 每页页眉和页脚各保留一处品牌；关闭或自定义格式仍无共享品牌。
			if got := strings.Count(compactText, "HexClaw河蟹AI"); got != want*2 {
				t.Fatalf("brand count = %d, want %d; text=%q", got, want*2, text)
			}
			if got := strings.Count(compactText, "本地云端均可部署"); got != want {
				t.Fatalf("footer count = %d, want %d; text=%q", got, want, text)
			}
		})
	}
}
