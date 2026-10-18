package engineadapter

import (
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestMathSubproblemAdapterKeepsExactCurrentTask(t *testing.T) {
	const shared = `小明 $\frac23$ 小时走了2千米。另一问的答案是乘法结合律。`
	const current = "⅓小时走了？km"
	const canonical = `$\frac13$ 小时走了？km`
	problem, ok := elementarymath.BuildMathSubproblem(shared, current, canonical)
	if !ok {
		t.Fatal("current task fixture was rejected")
	}
	got := gradingProblemWithGrounding(t.Context(), problem)
	gotShared, gotCurrent, gotCanonical, ok := elementarymath.ParseMathSubproblem(got)
	if !ok || gotShared != usecase.CanonicalPlainTextFallback(shared) || gotCurrent != current || gotCanonical != canonical {
		t.Fatal("provider plain-text conversion changed the selected task")
	}
}
