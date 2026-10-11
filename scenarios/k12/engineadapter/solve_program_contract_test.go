package engineadapter

import (
	"context"
	"errors"
	"testing"

	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 教材等非图片路径也消费同一个冻结模型快照，不能在没有物理拦截器时升级旧计算合同。
func TestSolveAdapterFrozenProgramContractWithoutPhysicalInterceptor(t *testing.T) {
	for _, contract := range []string{"", "legacy", "program-first-v1"} {
		t.Run(contract, func(t *testing.T) {
			calls := 0
			stopped := errors.New("controlled program boundary")
			execute := func(context.Context, engine.SubAgentSpec) (engine.SubAgentResult, error) {
				calls++
				return engine.SubAgentResult{}, stopped
			}
			solver := NewSolveAdapter(engine.NewSolveSkill(nil, nil, engine.WithSolveProgramExecutor(execute)))
			ctx := k12.WithGradingModelSnapshot(t.Context(), k12.GradingModelSnapshot{Provider: "fixture", Model: "fixture", Route: "fixture/fixture", MathExecutionContract: contract})
			result, err := solver.Solve(ctx, "6*7", "", "")
			if contract == "program-first-v1" {
				if calls != 1 || !errors.Is(err, stopped) {
					t.Fatalf("new contract calls=%d err=%v", calls, err)
				}
				return
			}
			if err != nil || calls != 0 || result.Solution == "" || result.Evidence.VerificationRunID != "" {
				t.Fatalf("legacy contract executed new program: calls=%d result=%+v err=%v", calls, result, err)
			}
		})
	}
}
