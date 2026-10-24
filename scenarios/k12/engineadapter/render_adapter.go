package engineadapter

import (
	"context"
	"fmt"
	"os"

	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// maxExportBytes 导出产物读取上限（错题本导出很小；防大文件撑爆 heap）。
const maxExportBytes = 30 << 20 // 30MB

// renderService 文档渲染（*render.Service 满足）。
type renderService interface {
	Render(ctx context.Context, content string, format render.Format, opts render.RenderOptions) (*render.RenderResult, error)
}

// RenderAdapter 把 Markdown 渲染成 PDF/Word 等（经平台 render 服务）。
type RenderAdapter struct{ svc renderService }

// NewRenderAdapter 创建 adapter。svc 可为 nil（render 服务未启用 → 调用方降级 markdown）。
func NewRenderAdapter(svc renderService) *RenderAdapter { return &RenderAdapter{svc: svc} }

var _ usecase.Renderer = (*RenderAdapter)(nil)

// RenderTeachingReport 使用同一宿主服务编译冻结资源，不进入通用Markdown重排。
func (a *RenderAdapter) RenderTeachingReport(ctx context.Context, bundle render.ReportBundle, input render.ReportRenderInput) ([]byte, string, error) {
	service, ok := a.svc.(interface {
		RenderReport(context.Context, render.ReportBundle, render.ReportRenderInput) (*render.ReportRenderResult, error)
	})
	if !ok {
		return nil, "", usecase.ErrRenderUnavailable
	}
	result, err := service.RenderReport(ctx, bundle, input)
	if err != nil {
		return nil, "", err
	}
	if result == nil || result.File == nil {
		return nil, "", fmt.Errorf("report renderer returned no file")
	}
	file := result.File
	if !file.CacheHit && !file.CacheOwned {
		defer os.Remove(file.Path)
	}
	if file.Size > maxExportBytes {
		return nil, "", fmt.Errorf("report output exceeds the export limit")
	}
	data, err := os.ReadFile(file.Path)
	return data, file.ContentType, err
}

// Render 把 markdown 渲染为指定格式，返回二进制 + Content-Type。
// svc 为 nil 或格式非法 → 返回 ErrRenderUnavailable，调用方回退 markdown。
func (a *RenderAdapter) Render(ctx context.Context, markdown, format string) ([]byte, string, error) {
	return a.RenderWithMetadata(ctx, markdown, format, usecase.DocumentRenderMetadata{})
}

// RenderWithMetadata 把可信展示身份传给共享PDF模板，不改变正文与原资产。
func (a *RenderAdapter) RenderWithMetadata(ctx context.Context, markdown, format string, metadata usecase.DocumentRenderMetadata) ([]byte, string, error) {
	if a.svc == nil {
		return nil, "", usecase.ErrRenderUnavailable
	}
	f := render.Format(format)
	if !f.IsValid() {
		return nil, "", fmt.Errorf("render: 不支持格式 %q", format)
	}
	res, err := a.svc.Render(ctx, markdown, f, render.RenderOptions{Locale: "zh-CN", PDFMetadata: render.PDFDocumentMetadata{
		AgentDisplayName: metadata.AgentDisplayName, Title: metadata.Title, Subtitle: metadata.Subtitle,
	}})
	if err != nil {
		return nil, "", err // RenderError（含 ENGINE_MISSING 等），调用方按需降级
	}
	if res.Size > maxExportBytes {
		if !res.CacheHit && !res.CacheOwned {
			_ = os.Remove(res.Path)
		}
		return nil, "", fmt.Errorf("render: 产物过大 %d 字节", res.Size)
	}
	data, err := os.ReadFile(res.Path)
	if !res.CacheHit && !res.CacheOwned { // 只清理临时文件，首次入缓存的文件仍由服务管理。
		_ = os.Remove(res.Path)
	}
	if err != nil {
		return nil, "", fmt.Errorf("render: 读产物: %w", err)
	}
	return data, res.ContentType, nil
}
