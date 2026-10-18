package knowledge

import (
	"context"
	"slices"
)

type documentScopeKey struct{}

// WithDocumentScope 约束当前步骤的自动及工具检索，不修改知识库全局配置。
// 空列表表示本步骤没有可检索文档；无此上下文时保留原查询行为。
func WithDocumentScope(ctx context.Context, ids []string) context.Context {
	return context.WithValue(ctx, documentScopeKey{}, append([]string{}, ids...))
}

func scopedDocumentFilter(ctx context.Context, filter Filter) (Filter, bool) {
	ids, scoped := ctx.Value(documentScopeKey{}).([]string)
	if !scoped {
		return filter, true
	}
	selected := append([]string(nil), ids...)
	if len(filter.DocumentIDs) > 0 {
		selected = slices.DeleteFunc(selected, func(id string) bool { return !slices.Contains(filter.DocumentIDs, id) })
	}
	filter.DocumentIDs = selected
	return filter, len(selected) > 0
}
