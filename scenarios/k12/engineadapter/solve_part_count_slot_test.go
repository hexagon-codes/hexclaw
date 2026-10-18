package engineadapter

import (
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestPartCountSlotAdapterPreservesCurrentOriginal(t *testing.T) {
	const shared = `小明 $\frac{2}{3}$ 小时走了 $2$ 千米。`
	const raw = "从上图看出：已知（ ）份是2千米，可以先求出这样的1份是多少，也就是"
	const canonical = "从上图看出：已知（　）份是 $2$ 千米，可以先求出这样的 $1$ 份是多少，也就是"
	for _, currentCanonical := range []string{canonical, `从上图看出：已知（$\square$）份是 $2$ 千米，可以先求出这样的 $1$ 份是多少，也就是……`} {
		problem, ok := elementarymath.BuildPartCountSlotProblemFromSources(shared, raw, currentCanonical)
		if !ok {
			t.Fatal("count source fixture did not project")
		}
		gotShared, gotRaw, gotCanonical, ok := elementarymath.ParsePartCountSlotProblemSources(gradingProblemWithGrounding(t.Context(), problem))
		if !ok || gotShared != usecase.CanonicalPlainTextFallback(shared) || gotRaw != raw || gotCanonical != currentCanonical {
			t.Fatal("adapter discarded the count target or original source")
		}
	}
}
