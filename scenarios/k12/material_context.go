package k12

import "context"

type materialPreparationContextKey struct{}

// WithMaterialPreparation 标记只使用冻结资料事实的独立答案准备。
func WithMaterialPreparation(ctx context.Context) context.Context {
	return context.WithValue(ctx, materialPreparationContextKey{}, true)
}

// IsMaterialPreparation 不改变普通聊天和学生作答的上下文规则。
func IsMaterialPreparation(ctx context.Context) bool {
	active, _ := ctx.Value(materialPreparationContextKey{}).(bool)
	return active
}
