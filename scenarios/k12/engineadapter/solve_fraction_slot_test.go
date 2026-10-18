package engineadapter

import (
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestFractionSlotAdapterPreservesScopeThroughPlainTextProjection(t *testing.T) {
	const shared = `小明 $\frac{2}{3}$ 小时走了 $2$ 千米，照这样的速度，他每小时行走多少千米？`
	const current = `也就是 $2\div2=2\times\frac{\square}{\square}$。`
	problem, ok := elementarymath.BuildFractionSlotProblem(shared, current)
	if !ok {
		t.Fatal("fraction slot input fixture did not project")
	}
	got := gradingProblemWithGrounding(t.Context(), problem)
	gotShared, gotCurrent, ok := elementarymath.ParseFractionSlotProblem(got)
	if !ok || gotShared != usecase.CanonicalPlainTextFallback(shared) || gotCurrent != usecase.CanonicalPlainTextFallback(current) {
		t.Fatalf("adapter lost answer scope while normalizing math: shared=%q current=%q ok=%t", gotShared, gotCurrent, ok)
	}
	if !strings.Contains(gotShared, "千米") || !strings.Contains(gotCurrent, "square") && !strings.Contains(gotCurrent, "□") {
		t.Fatal("parent units or current printed boxes were discarded")
	}
}

func TestFractionSlotAdapterPreservesPhantomSourceProof(t *testing.T) {
	const shared = `小明 $\frac{2}{3}$ 小时走了 $2$ 千米。`
	const raw = "2÷2＝2×　/　；"
	const canonical = `$2\div 2=2\times\frac{\phantom{1}}{\phantom{2}}$；`
	problem, ok := elementarymath.BuildFractionSlotProblemFromSources(shared, raw, canonical)
	if !ok {
		t.Fatal("same-source fixture did not project")
	}
	got := gradingProblemWithGrounding(t.Context(), problem)
	gotShared, gotRaw, gotCanonical, ok := elementarymath.ParseFractionSlotProblemSources(got)
	if !ok || gotShared != usecase.CanonicalPlainTextFallback(shared) || gotRaw != raw || gotCanonical != canonical {
		t.Fatalf("plain-text projection removed raw blanks or canonical proof: shared=%q raw=%q canonical=%q", gotShared, gotRaw, gotCanonical)
	}
}
