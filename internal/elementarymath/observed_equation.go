package elementarymath

import "context"

type observedEquationCurriculumKey struct{}

// WithObservedEquationCurriculum 由已核对当前图片来源及评分账本的宿主绑定，只影响本次逐式批改。
func WithObservedEquationCurriculum(ctx context.Context, constraint string) context.Context {
	return context.WithValue(ctx, observedEquationCurriculumKey{}, constraint)
}

func ObservedEquationCurriculum(ctx context.Context) string {
	constraint, _ := ctx.Value(observedEquationCurriculumKey{}).(string)
	return constraint
}
