package engineadapter

import (
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// QuantityAnswerCompleteness 复用引擎已有数量解析，不调用模型或读取运行时配置。
func (*SolveAdapter) QuantityAnswerCompleteness(problem, answer string) (required, complete bool) {
	return engine.QuantityAnswerCompleteness(problem, answer)
}

var _ usecase.QuantityAnswerCompletenessChecker = (*SolveAdapter)(nil)
