// Package render 提供通用文档渲染能力
//
// 走 Markdown 单源 + Pandoc 确定性渲染范式：LLM 只产 markdown，pandoc 把
// markdown 渲染为目标格式（docx/pdf/epub/odt/rtf/txt/html/md），LLM 永不
// 直接产生二进制。
//
// 关键架构决策（详见 .claude/doc-generation-architecture.md）：
//   - Renderer.Render() 返回文件路径而非 []byte，避免 100 MB 输出在 Go heap
//     持有完整字节（3 并发 = 300 MB heap 击穿 sidecar RSS 预算）
//   - sidecar 内：完整生成 → 后处理 → 缓存 → 流式下发
//   - HTTP 层走 http.ServeContent 流式（OS sendfile / buffered read）
//   - raw HTML / TeX 默认禁用（reader 配置 markdown-raw_html-raw_tex），
//     防 LLM 产物中的 <script> / \write18 进入输出
//
// 不暴露为 LLM tool——确定性服务，不消耗推理预算。
// PDF默认使用内嵌A4排版、数学尺寸、品牌和页码；样式/Logo摘要仅隔离PDF缓存。
// 已冻结的领域产物继续复用原文件，DOCX/HTML等格式不继承PDF样式。
package render

import (
	"context"
	"fmt"
)

// Format 目标输出格式（稳定枚举，前后端契约的一部分）。
type Format string

const (
	FormatMarkdown Format = "md"
	FormatHTML     Format = "html"
	FormatDocx     Format = "docx"
	FormatPDF      Format = "pdf" // 默认 typst 引擎
	FormatEPUB     Format = "epub"
	FormatODT      Format = "odt"
	FormatRTF      Format = "rtf"
	FormatTXT      Format = "txt"
)

// AllFormats 枚举所有支持的格式（用于校验 + 文档生成 + 测试）。
var AllFormats = []Format{
	FormatMarkdown, FormatHTML, FormatDocx, FormatPDF,
	FormatEPUB, FormatODT, FormatRTF, FormatTXT,
}

// IsValid 判断给定字符串是否为合法格式。
func (f Format) IsValid() bool {
	for _, v := range AllFormats {
		if v == f {
			return true
		}
	}
	return false
}

// Extension 返回对应的文件扩展名（不含点）。
func (f Format) Extension() string { return string(f) }

// MIME 返回对应的 MIME 类型。MIME 是 Content-Type 头部的稳定承诺。
func (f Format) MIME() string {
	switch f {
	case FormatMarkdown:
		return "text/markdown; charset=utf-8"
	case FormatHTML:
		return "text/html; charset=utf-8"
	case FormatDocx:
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case FormatPDF:
		return "application/pdf"
	case FormatEPUB:
		return "application/epub+zip"
	case FormatODT:
		return "application/vnd.oasis.opendocument.text"
	case FormatRTF:
		return "application/rtf"
	case FormatTXT:
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// RenderOptions 渲染参数。本期不暴露给前端 UI 的字段保留接口位但默认为零值。
type RenderOptions struct {
	// Locale 输出文档语言（影响 docx/epub 元数据 + html lang 属性）。
	// 优先级：markdown frontmatter.lang > 此字段 > 默认 "zh-CN"。
	Locale string

	// AllowRawHTML 是否允许 markdown 中的原生 HTML 进入输出。
	// 默认 false（reader 配 markdown-raw_html）；本期前端不暴露 UI 开关，
	// 字段保留但接口位但不接 UI（缩小本期攻击面）。
	AllowRawHTML bool

	// AllowRawTeX 是否允许 markdown 中的原生 TeX 进入输出。同上默认 false。
	AllowRawTeX bool

	// PDFHeaderFooter 未指定时使用统一页眉页脚，仅显式用户要求可关闭本次输出。
	PDFHeaderFooter *bool `json:"pdf_header_footer,omitempty"`
	// PDFLayout 为 custom 时保留用户指定的特殊版式，不套共享PDF模板。
	PDFLayout string `json:"pdf_layout,omitempty"`
}

// Validate 校验 options 合法性。当前主要校验 Locale 形如 BCP 47（最低限度）。
func (o RenderOptions) Validate() error {
	if o.PDFLayout != "" && o.PDFLayout != "standard" && o.PDFLayout != "custom" {
		return fmt.Errorf("unsupported PDF layout %q", o.PDFLayout)
	}
	return nil
}

func (o RenderOptions) pdfHeaderFooterEnabled() bool {
	return o.PDFHeaderFooter == nil || *o.PDFHeaderFooter
}

// effectiveLocale 返回参与缓存键计算的 locale 串（含默认 fallback）。
func (o RenderOptions) effectiveLocale(defaultLocale string) string {
	if o.Locale != "" {
		return o.Locale
	}
	if defaultLocale != "" {
		return defaultLocale
	}
	return "zh-CN"
}

// RenderResult 渲染产物。返回文件路径而非 []byte，避免在 Go heap 持有完整字节。
type RenderResult struct {
	// Path sidecar 受控目录下的 temp / cache file 绝对路径。
	// HTTP handler 用 http.ServeContent 从此路径流式下发，OS 内核处理 sendfile。
	Path string

	// Size 字节数。
	Size int64

	// ContentType MIME 类型，对应 Format.MIME()。
	ContentType string

	// CacheHit 是否命中已有缓存；首次入缓存仍为false。
	// 文件清理须同时核对CacheOwned，不能把未命中等同于临时文件。
	CacheHit bool

	// CacheOwned 表示文件由缓存服务管理，首次生成入缓存也为true。
	// 调用者只在CacheHit和CacheOwned均为false时清理临时文件。
	CacheOwned bool
}

// Renderer 渲染器接口。实现负责：
//
//   - 把 markdown 渲染为目标格式
//   - 产物落到受控 temp file 或 cache file
//   - 返回 RenderResult{Path, Size, ContentType, CacheHit}
//
// 不应把完整产物 bytes 持有到 Go heap（影响 sidecar RSS 预算）。
//
// 错误返回 *RenderError。
type Renderer interface {
	Render(ctx context.Context, content string, format Format, opts RenderOptions) (*RenderResult, error)
}
