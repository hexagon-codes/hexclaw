package engine

import (
	"errors"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
)

type fractionSlotSolveScopeKey struct{}

var errFractionSlotAnswerTarget = errors.New("fraction slot answer does not match the current task")

// validateFractionSlotSolverResult 在标准答案进入持久回执前核对完整作答对象。
// 原响应保留；格式不符是已收到响应后的确定失败，不以猜测的片段继续批改。
func validateFractionSlotSolverResult(result SubAgentResult) error {
	if _, complete := elementarymath.ParseFractionSlotAnswer(extractFinalAnswer(result.Output)); !complete {
		return errors.Join(egress.ErrProviderResponseProcessed, errFractionSlotAnswerTarget)
	}
	return nil
}
