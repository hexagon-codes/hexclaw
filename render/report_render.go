package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ReportRenderResult 将检查范围与文件本身分开，缺少可选检查工具不伪报通过。
type ReportRenderResult struct {
	File               *RenderResult
	Bundle             ReportBundleRef
	ProfileID          string
	LayoutCheck        string
	FontManifestDigest string
}

// RenderReport 复用同一Service的并发控制和缓存，不把JSON交给Markdown图片预处理。
// 所有资源已由调用方从原资产读取，只有本报告选择的模板参与编译。
func (s *Service) RenderReport(ctx context.Context, bundle ReportBundle, input ReportRenderInput) (*ReportRenderResult, error) {
	if int64(len(input.Document)) > MaxMarkdownBytes {
		return nil, &RenderError{Code: CodeInputTooLarge, Format: FormatPDF}
	}
	if err := validateReportRenderInput(bundle, input); err != nil {
		return nil, &RenderError{Code: CodeInvalidInput, Format: FormatPDF, Detail: err.Error()}
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		return nil, &RenderError{Code: CodeConcurrencyRejected, Format: FormatPDF}
	}
	renderer, ok := s.renderer.(*PandocRenderer)
	if !ok {
		return nil, &RenderError{Code: CodeEngineMissing, Format: FormatPDF, Engine: "report-profile"}
	}
	if input.Fonts.Sans == "" || input.Fonts.Serif == "" || input.Fonts.Math == "" {
		fonts, err := renderer.resolveReportFonts(ctx, bundle.profile)
		if err != nil {
			return nil, err
		}
		input.Fonts = fonts
	}
	engineID, err := renderer.reportEngineIdentity(ctx)
	if err != nil {
		return nil, err
	}
	resources := make(map[string]string, len(input.Resources))
	for path, data := range input.Resources {
		resources[path] = reportDigest(data)
	}
	identity, err := reportCanonicalJSON(struct {
		Bundle    ReportBundleRef   `json:"bundle"`
		Input     ReportRenderInput `json:"input"`
		Resources map[string]string `json:"resources"`
		Engine    string            `json:"engine"`
	}{bundle.ref, input, resources, engineID})
	if err != nil {
		return nil, err
	}
	key := ""
	// 未解析实际字体文件清单时仍允许生成，但不能把不同回退字体合并为同一缓存。
	if s.cache != nil && input.Fonts.ManifestDigest != "" {
		key = s.cache.Key("report-profile-v1\n"+reportDigest(identity), FormatPDF, RenderOptions{Locale: "zh-CN"})
		if hit, ok := s.cache.Lookup(key, FormatPDF); ok {
			check, err := renderer.validateReportProfileLayout(ctx, hit.Path, bundle.profile)
			if err != nil {
				return nil, err
			}
			return &ReportRenderResult{File: hit, Bundle: bundle.ref, ProfileID: bundle.profile.ID, LayoutCheck: check, FontManifestDigest: input.Fonts.ManifestDigest}, nil
		}
	}
	result, err := renderer.renderReportProfile(ctx, bundle, input)
	if err != nil {
		return nil, err
	}
	if key != "" {
		if cached, err := s.cache.Put(key, result.File.Path); err == nil {
			result.File.Path, result.File.CacheOwned = cached, true
		}
	}
	return result, nil
}

// resolveReportFonts 选择当前Typst实际可用的字体，保留模板声明的回退顺序。
// 未取得字体文件身份时保持缓存关闭，避免将不同机器的字体回退误作同一产物。
func (r *PandocRenderer) resolveReportFonts(ctx context.Context, profile reportProfile) (ReportFonts, error) {
	path, err := r.lookTypst()
	if err != nil {
		return ReportFonts{}, err
	}
	raw, err := exec.CommandContext(ctx, path, "fonts").Output()
	if err != nil {
		return ReportFonts{}, fmt.Errorf("resolve report fonts: %w", err)
	}
	available := make(map[string]bool)
	for _, line := range strings.Split(string(raw), "\n") {
		available[strings.TrimSpace(line)] = true
	}
	selected := make(map[string]string)
	for _, role := range []string{"sans", "serif", "math"} {
		for _, candidate := range profile.RequestedFonts[role] {
			if available[candidate] {
				selected[role] = candidate
				break
			}
		}
		if selected[role] == "" {
			return ReportFonts{}, fmt.Errorf("report font is unavailable: %s", role)
		}
	}
	return ReportFonts{Sans: selected["sans"], Serif: selected["serif"], Math: selected["math"]}, nil
}

func (r *PandocRenderer) reportEngineIdentity(ctx context.Context) (string, error) {
	pandoc, err := r.lookPandoc()
	if err != nil {
		return "", &RenderError{Code: CodeEngineMissing, Format: FormatPDF, Engine: "pandoc", Detail: err.Error()}
	}
	typst, err := r.lookTypst()
	if err != nil {
		return "", &RenderError{Code: CodeEngineMissing, Format: FormatPDF, Engine: "typst", Detail: err.Error()}
	}
	var identity strings.Builder
	for _, binary := range []string{pandoc, typst} {
		cmd := exec.CommandContext(ctx, binary, "--version")
		cmd.Env = r.subprocessEnv()
		out, err := cmd.Output()
		if err != nil {
			return "", reportCommandError(ctx, filepath.Base(binary), err, "read report engine version")
		}
		identity.Write(out)
	}
	return identity.String(), nil
}

func (r *PandocRenderer) renderReportProfile(ctx context.Context, bundle ReportBundle, input ReportRenderInput) (*ReportRenderResult, error) {
	work, err := os.MkdirTemp(r.SandboxDir, "report-profile-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	write := func(path string, data []byte) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		return os.WriteFile(path, data, 0o600)
	}
	for path, data := range bundle.files {
		if err := write(filepath.Join(work, filepath.FromSlash(path)), data); err != nil {
			return nil, err
		}
	}
	entry := filepath.Join(work, filepath.FromSlash(bundle.profile.Entrypoint))
	dir := filepath.Dir(entry)
	for path, data := range input.Resources {
		if err := write(filepath.Join(dir, filepath.FromSlash(path)), data); err != nil {
			return nil, err
		}
	}
	if err := write(filepath.Join(dir, "report.json"), input.Document); err != nil {
		return nil, err
	}
	subCtx, cancel := context.WithTimeout(ctx, timeoutFor(FormatPDF))
	defer cancel()
	if err := r.compileReportFormulas(subCtx, dir, input.Formulas); err != nil {
		return nil, err
	}
	output, err := r.createTempFile(FormatPDF)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = os.Remove(output)
		}
	}()
	typst, err := r.lookTypst()
	if err != nil {
		return nil, err
	}
	args := []string{"compile", "--root", work}
	for _, pair := range [][2]string{{"font-sans", input.Fonts.Sans}, {"font-serif", input.Fonts.Serif}, {"font-math", input.Fonts.Math}} {
		if pair[1] != "" {
			args = append(args, "--input", pair[0]+"="+pair[1])
		}
	}
	args = append(args, entry, output)
	cmd := exec.CommandContext(subCtx, typst, args...)
	cmd.Dir, cmd.Env = dir, r.subprocessEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, reportCommandError(subCtx, "typst", err, stderr.String())
	}
	stat, err := os.Stat(output)
	if err != nil {
		return nil, err
	}
	if stat.Size() == 0 {
		return nil, fmt.Errorf("report renderer returned an empty PDF")
	}
	if stat.Size() > MaxOutputBytes {
		return nil, &RenderError{Code: CodeOutputTooLarge, Format: FormatPDF}
	}
	check, err := r.validateReportProfileLayout(subCtx, output, bundle.profile)
	if err != nil {
		return nil, err
	}
	success = true
	return &ReportRenderResult{
		File:   &RenderResult{Path: output, Size: stat.Size(), ContentType: FormatPDF.MIME()},
		Bundle: bundle.ref, ProfileID: bundle.profile.ID, LayoutCheck: check, FontManifestDigest: input.Fonts.ManifestDigest,
	}, nil
}

func reportCommandError(ctx context.Context, engine string, err error, detail string) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	code := CodeRenderFailed
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		code = CodeTimeout
	}
	return &RenderError{Code: code, Format: FormatPDF, Engine: engine, Detail: err.Error() + ": " + truncateStderr(detail)}
}

func (r *PandocRenderer) compileReportFormulas(ctx context.Context, dir string, formulas map[string]string) error {
	keys := make([]string, 0, len(formulas))
	for key := range formulas {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		if key == "" || strings.ContainsAny(key, "\"\\\n\r") {
			return fmt.Errorf("invalid report formula identifier")
		}
		items = append(items, map[string]string{"id": key, "latex": formulas[key]})
	}
	data, err := json.Marshal(items)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "formulas.json"), data, 0o600); err != nil {
		return err
	}
	filter := filepath.Join(dir, "compile-formulas.lua")
	if err := os.WriteFile(filter, []byte(reportFormulaCompiler), 0o600); err != nil {
		return err
	}
	pandoc, err := r.lookPandoc()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, pandoc, "--from=markdown", "--to=typst", "--lua-filter="+filter, "--output="+filepath.Join(dir, "formulas.typ"))
	cmd.Dir, cmd.Env = dir, r.subprocessEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return reportCommandError(ctx, "pandoc", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "Could not convert TeX math") {
		return fmt.Errorf("report formula conversion failed: %s", truncateStderr(stderr.String()))
	}
	return nil
}

// 所有公式以Pandoc数学节点一次转换，正文及模型字符串不作为Typst代码求值。
const reportFormulaCompiler = `
function Pandoc(doc)
  local f = assert(io.open('formulas.json', 'r'))
  local items = pandoc.json.decode(f:read('*a'))
  f:close()
  if #items == 0 then
    return pandoc.Pandoc({pandoc.RawBlock('typst', '#let formulas = (:)\n')})
  end
  local output = {'#let formulas = (\n'}
  for _, item in ipairs(items) do
    local node = pandoc.Math('InlineMath', item.latex)
    local value = pandoc.write(pandoc.Pandoc({pandoc.Plain({node})}), 'typst'):gsub('%s+$', '')
    output[#output+1] = string.format('%q: %s,\n', item.id, value)
  end
  output[#output+1] = ')\n'
  return pandoc.Pandoc({pandoc.RawBlock('typst', table.concat(output))})
end
`
