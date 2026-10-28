package builtin

// 文档导出 Skill —— Markdown → md/html/docx/pdf/epub/odt/rtf/txt。
//
// 行为：
//   - 包一层 render.Service（pandoc/typst sidecar），不经过 LLM。
//   - 应用注入持久化后返回归属当前会话的产物引用；独立渲染调用保留路径返回合同。
//   - raw_html / raw_tex 默认禁（沙箱攻击面收敛，不暴露 UI 开关）。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/internal/documentflow"
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/skill"
)

// DocumentExporter 是 *render.Service 的窄注入面（便于测试 + 复用 Server 已持有的 renderSvc）。
type DocumentExporter interface {
	Render(ctx context.Context, content string, format render.Format, opts render.RenderOptions) (*render.RenderResult, error)
}

// ExportDocumentSkill 把 Markdown 渲染成可下载文档并返回原会话的持久产物引用。
type ExportDocumentSkill struct {
	render    DocumentExporter
	publisher DocumentArtifactPublisher
}

// DocumentArtifactPublisher 由应用注入持久产物能力，工具本身不管理归属数据库。
type DocumentArtifactPublisher interface {
	ExportDocument(context.Context, string, render.Format, render.RenderOptions, string) (documentflow.Artifact, error)
}

func (s *ExportDocumentSkill) SetArtifactPublisher(publisher DocumentArtifactPublisher) {
	s.publisher = publisher
}

func NewExportDocumentSkill(r DocumentExporter) *ExportDocumentSkill {
	return &ExportDocumentSkill{render: r}
}

func (s *ExportDocumentSkill) Name() string { return "export_document" }
func (s *ExportDocumentSkill) Description() string {
	return "Render Markdown into a downloadable document; PDF uses the shared HexClaw layout by default"
}

// Match 只走 LLM tool-call 主路径。
func (s *ExportDocumentSkill) Match(string) bool { return false }

func (s *ExportDocumentSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("export_document",
		"Render Markdown into a downloadable document (md/html/docx/pdf/epub/odt/rtf/txt) and return "+
			"a persistent attachment reference when the application document service is available. Use this tool for generated PDFs so the shared layout is applied. Write formulas as Markdown/LaTeX math. "+
			"PDF defaults to the standard HexClaw layout with a header and footer on every page. "+
			"Only set PDF exceptions when the user explicitly requests no header/footer or a custom format. "+
			"The application attaches a registered document to the current reply; do not claim delivery from a local file path alone.",
		&llm.Schema{Type: "object", Properties: map[string]*llm.Schema{
			"content":           {Type: "string", Description: "Markdown content to render (required)"},
			"format":            {Type: "string", Description: "md|html|docx|pdf|epub|odt|rtf|txt (required)"},
			"filename":          {Type: "string", Description: "suggested file name (optional)"},
			"locale":            {Type: "string", Description: `output language, default "zh-CN"`},
			"pdf_header_footer": {Type: "boolean", Description: "PDF only: default true; set false only when the user explicitly asks to omit headers and footers"},
			"pdf_layout":        {Type: "string", Description: "PDF only: standard (default) or custom; custom skips the shared layout and uses Markdown/frontmatter formatting only when explicitly requested by the user"},
		}, Required: []string{"content", "format"}})
}

func (s *ExportDocumentSkill) Execute(ctx context.Context, args map[string]any) (*skill.Result, error) {
	if s.render == nil {
		return nil, fmt.Errorf("document export is not enabled")
	}
	content := firstStringArg(args, "content")
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("content must not be empty")
	}

	format := render.Format(strings.ToLower(firstStringArg(args, "format")))
	if !format.IsValid() {
		return nil, fmt.Errorf("unsupported format %q (want one of %v)", format, render.AllFormats)
	}

	locale := firstStringArg(args, "locale")
	if locale == "" {
		locale = "zh-CN"
	}
	// AllowRawHTML / AllowRawTeX 保持默认 false —— 不暴露 UI，沙箱攻击面收敛。
	opts := render.RenderOptions{Locale: locale}
	if value, exists := args["pdf_header_footer"]; exists {
		enabled, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("pdf_header_footer must be a boolean")
		}
		opts.PDFHeaderFooter = &enabled
	}
	opts.PDFLayout = firstStringArg(args, "pdf_layout")
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if s.publisher != nil {
		artifact, err := s.publisher.ExportDocument(ctx, content, format, opts, firstStringArg(args, "filename"))
		if err != nil {
			return nil, fmt.Errorf("export document failed: %w", err)
		}
		ref := documentflow.ReferenceFor(artifact)
		raw, _ := json.Marshal([]documentflow.Reference{ref})
		return &skill.Result{Content: fmt.Sprintf("Exported %s (%d bytes): %s", format, artifact.Size, ref.URL), Data: ref, Metadata: map[string]string{"artifacts": string(raw)}}, nil
	}

	res, err := s.render.Render(ctx, content, format, opts)
	if err != nil {
		return nil, fmt.Errorf("render to %s failed: %w", format, err)
	}

	return &skill.Result{
		Content: fmt.Sprintf("Exported %s (%d bytes): %s", format, res.Size, res.Path),
		Data:    res, // Path/Size/ContentType/CacheHit → 下游（送达附件 / 入库 / 下载）
		Metadata: map[string]string{
			"format":       string(format),
			"path":         res.Path,
			"content_type": res.ContentType,
		},
	}, nil
}
