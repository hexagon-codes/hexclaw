package assembly

import "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"

// WithTeachingReportResources 注入共享生命周期的只读资源端口，不加载模型Skill。
func WithTeachingReportResources(reader usecase.TeachingReportResourceReader) Option {
	return func(d *usecase.Deps) { d.TeachingReportResources = reader }
}
