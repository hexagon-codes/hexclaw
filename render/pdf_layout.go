package render

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type pdfLayoutBox struct {
	Top    float64 `xml:"top,attr"`
	Left   float64 `xml:"left,attr"`
	Width  float64 `xml:"width,attr"`
	Height float64 `xml:"height,attr"`
	Text   string  `xml:",innerxml"`
}

type pdfLayoutPage struct {
	Number int            `xml:"number,attr"`
	Width  float64        `xml:"width,attr"`
	Height float64        `xml:"height,attr"`
	Images []pdfLayoutBox `xml:"image"`
	Texts  []pdfLayoutBox `xml:"text"`
}

type pdfLayoutError struct{ detail string }

func (e *pdfLayoutError) Error() string { return e.detail }

var pdfLeadingImage = regexp.MustCompile(`(?s)^\s*#[^\n]*\n\s*!\[`)
var pdfLayoutTags = regexp.MustCompile(`<[^>]*>`)

// 只校验新标准PDF的实际产物，读取真实图片/文字几何而非源Markdown行数。
// 缺可选工具时保留已有生成能力，明确记录未校验；已具备工具的失败不能当作通过。
func (r *PandocRenderer) validatePDFLayout(ctx context.Context, pdf, markdown string, attempt int) error {
	tool, err := exec.LookPath("pdftohtml")
	if err != nil {
		slog.WarnContext(ctx, "PDF layout check unavailable", "reason", "pdftohtml not installed")
		return nil
	}
	dir, err := os.MkdirTemp(r.SandboxDir, "pdf-layout-*")
	if err != nil {
		return fmt.Errorf("prepare PDF layout check: %w", err)
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, tool, "-xml", "-hidden", "-noroundcoord", "-zoom", "1", "-stdout", "-q", pdf, filepath.Join(dir, "page"))
	cmd.Dir, cmd.Env = dir, r.subprocessEnv()
	var output, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("inspect PDF layout: %w: %s", err, truncateStderr(stderr.String()))
	}
	var document struct {
		Pages []pdfLayoutPage `xml:"page"`
	}
	if err := xml.Unmarshal(output.Bytes(), &document); err != nil || len(document.Pages) == 0 {
		return fmt.Errorf("PDF layout inspection returned no readable pages")
	}
	intentionalBreak := strings.Contains(markdown, "#pagebreak(") || strings.Contains(markdown, "\\newpage")
	imageCount := 0
	for i, page := range document.Pages {
		// 与共享A4模板相同的正文边界，保留字体字框的少量度量误差。
		const mm = 72.0 / 25.4
		const tolerance = 4.0
		left, right, top, bottom := 20*mm, page.Width-20*mm, 24*mm, page.Height-22*mm
		bodyImages, bodyText := 0, false
		for _, box := range page.Images {
			if box.Top+box.Height < top-tolerance || box.Top > bottom+tolerance {
				continue
			}
			if box.Width > 24 && box.Height > 24 {
				bodyImages++
				imageCount++
			}
			if box.Left < left-tolerance || box.Left+box.Width > right+tolerance || box.Top < top-tolerance || box.Top+box.Height > bottom+tolerance {
				return &pdfLayoutError{fmt.Sprintf("PDF page %d image exceeds body bounds", page.Number)}
			}
		}
		for _, box := range page.Texts {
			if box.Top+box.Height < top-tolerance || box.Top > bottom+tolerance {
				continue
			}
			text := strings.TrimSpace(pdfLayoutTags.ReplaceAllString(box.Text, ""))
			if text == "" {
				continue
			}
			bodyText = true
			leftTolerance, rightTolerance := tolerance, tolerance
			// 中文标点悬挂会伸出半个字宽，正文内容仍在同一排版区域内。
			runes := []rune(text)
			if strings.ContainsRune("（「『《“‘", runes[0]) {
				leftTolerance = max(tolerance, box.Height*0.6)
			}
			if strings.ContainsRune("），。！？；：」』》”’", runes[len(runes)-1]) {
				rightTolerance = max(tolerance, box.Height*0.6)
			}
			if box.Left < left-leftTolerance || box.Left+box.Width > right+rightTolerance || box.Top < top-tolerance || box.Top+box.Height > bottom+tolerance {
				return &pdfLayoutError{fmt.Sprintf("PDF page %d text exceeds body bounds", page.Number)}
			}
		}
		if !intentionalBreak && !bodyText && bodyImages == 0 {
			return &pdfLayoutError{fmt.Sprintf("PDF page %d has no body content", page.Number)}
		}
		if i == 0 && pdfLeadingImage.MatchString(markdown) && bodyImages == 0 {
			return &pdfLayoutError{"PDF leading image moved off the first page"}
		}
	}
	slog.InfoContext(ctx, "PDF layout validated", "pages", len(document.Pages), "body_images", imageCount, "reflow_attempt", attempt)
	return nil
}
