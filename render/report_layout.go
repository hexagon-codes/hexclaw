package render

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// validateReportProfileLayout 使用本报告profile的几何，避免套用通用模板的固定边距。
// XML只证明对象边界与空页，复杂图文关系仍由实际文件视觉验收。
func (r *PandocRenderer) validateReportProfileLayout(ctx context.Context, pdf string, profile reportProfile) (string, error) {
	tool, err := exec.LookPath("pdftohtml")
	if err != nil {
		return "not_run", nil
	}
	dir, err := os.MkdirTemp(r.SandboxDir, "report-layout-*")
	if err != nil {
		return "failed", err
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, tool, "-xml", "-hidden", "-noroundcoord", "-zoom", "1", "-stdout", "-q", pdf, filepath.Join(dir, "page"))
	cmd.Dir, cmd.Env = dir, r.subprocessEnv()
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "failed", reportCommandError(ctx, "pdftohtml", err, stderr.String())
	}
	var document struct {
		Pages []pdfLayoutPage `xml:"page"`
	}
	if err := xml.Unmarshal(out.Bytes(), &document); err != nil || len(document.Pages) == 0 {
		return "failed", fmt.Errorf("report PDF has no inspectable pages")
	}
	const mm = 72.0 / 25.4
	const tolerance = 4.0
	for _, page := range document.Pages {
		left, right := profile.Margins["left"]*mm, page.Width-profile.Margins["right"]*mm
		top, bottom := profile.Margins["top"]*mm, page.Height-profile.Margins["bottom"]*mm
		body := false
		for _, box := range page.Images {
			if box.Top+box.Height < top-tolerance || box.Top > bottom+tolerance {
				continue
			}
			body = true
			if box.Left < left-tolerance || box.Left+box.Width > right+tolerance || box.Top < top-tolerance || box.Top+box.Height > bottom+tolerance {
				return "failed", fmt.Errorf("report page %d image exceeds body bounds", page.Number)
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
			body = true
			lt, rt := tolerance, tolerance
			runes := []rune(text)
			if strings.ContainsRune("（「『《“‘", runes[0]) {
				lt = max(lt, box.Height*0.6)
			}
			if strings.ContainsRune("），。、！？；：」』》”’", runes[len(runes)-1]) {
				rt = max(rt, box.Height*0.6)
			}
			if box.Left < left-lt || box.Left+box.Width > right+rt || box.Top < top-tolerance || box.Top+box.Height > bottom+tolerance {
				return "failed", fmt.Errorf("report page %d text exceeds body bounds", page.Number)
			}
		}
		if !body {
			return "failed", fmt.Errorf("report page %d has no body content", page.Number)
		}
	}
	return "passed", nil
}
