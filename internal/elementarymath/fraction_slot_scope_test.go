package elementarymath

import (
	"strings"
	"testing"
)

func TestFractionSlotScopePreservesCurrentSourceAndAnswerShape(t *testing.T) {
	const shared = "小明2/3小时走了2千米，照这样的速度，他每小时行走多少千米？\n线段图：1小时？km；1/3小时？km；2/3小时2km。"
	for _, current := range []string{"也就是2÷2＝2×□/□；", "也就是2×□/□×3。", `也就是 $2\div2=2\times\frac{\square}{\square}$。`, `也就是2×(\square)/(\square)×3。`} {
		projected, ok := BuildFractionSlotProblem(shared, current)
		if !ok {
			t.Fatalf("fraction slot source rejected: %q", current)
		}
		gotShared, gotCurrent, ok := ParseFractionSlotProblem(projected)
		if !ok || gotShared != shared || gotCurrent != current {
			t.Fatalf("scope projection changed original source: shared=%q current=%q ok=%t", gotShared, gotCurrent, ok)
		}
	}
	current := "可以先求出这样的1份是多少，也就是2÷2＝2×□/□"
	projection, ok := BuildFractionSlotProblem(shared, current)
	if _, gotCurrent, parsed := ParseFractionSlotProblem(projection); !ok || !parsed || gotCurrent != current {
		t.Fatal("printed lead-in lost the unique current fraction target")
	}
	for _, current := range []string{"求一份走多少千米？", "已知（ ）份是2千米。", "2×□/□+□", "2×□/□，再求路程是多少？", "2×1/2=1千米", "2×□/□千米"} {
		if _, ok := BuildFractionSlotProblem(shared, current); ok {
			t.Fatalf("other answer target was treated as a single fraction slot: %q", current)
		}
	}
	for _, tc := range []struct {
		input, want string
		valid       bool
	}{
		{"1/2", "1/2", true},
		{"2/4", "2/4", true},
		{`$\frac{1}{2}$`, "1/2", true},
		{`\(\frac{1}{2}\)`, "1/2", true},
		{"1 千米", "", false},
		{"1千米；方框1、2", "", false},
		{"1/2千米", "", false},
		{"1、2", "", false},
		{"0.5", "", false},
		{"1/0", "", false},
		{"1/2\n3/4", "", false},
		{"1/2=0.5", "", false},
	} {
		got, valid := ParseFractionSlotAnswer(tc.input)
		if valid != tc.valid || got != tc.want {
			t.Fatalf("complete answer %q parsed as %q/%t, want %q/%t", tc.input, got, valid, tc.want, tc.valid)
		}
	}
}

func TestFractionSlotPhantomScopeRequiresSameCompleteSources(t *testing.T) {
	const raw = "2÷2＝2×　/　；"
	const canonical = `$2\div 2=2\times\frac{\phantom{1}}{\phantom{2}}$；`
	for _, canonicalSource := range []string{canonical, strings.NewReplacer(`\phantom{1}`, `\phantom{91}`, `\phantom{2}`, `\phantom{82}`).Replace(canonical)} {
		projection, ok := BuildFractionSlotProblemFromSources("共享题干：2/3小时走2千米。", raw, canonicalSource)
		if !ok {
			t.Fatal("same-source empty fraction was rejected")
		}
		_, gotRaw, gotCanonical, ok := ParseFractionSlotProblemSources(projection)
		if !ok || gotRaw != raw || gotCanonical != canonicalSource {
			t.Fatal("source proof rewrote original raw or canonical")
		}
		if _, answer := ParseFractionSlotAnswer(canonicalSource); answer {
			t.Fatal("phantom layout contents became a numeric answer")
		}
	}
	for _, tc := range []struct{ name, raw, canonical string }{
		{"no canonical proof", raw, ""},
		{"actual fraction is not a blank", raw, `$2\div2=2\times\frac{1}{2}$；`},
		{"only numerator empty", raw, `$2\div2=2\times\frac{\phantom{1}}{2}$；`},
		{"different operand", raw, `$3\div2=2\times\frac{\phantom{1}}{\phantom{2}}$；`},
		{"different operation", raw, `$2\div2=2+\frac{\phantom{1}}{\phantom{2}}$；`},
		{"filled raw values", "2÷2＝2× 1 / 2；", canonical},
		{"missing raw operand without blanks", "2÷2＝2×/；", canonical},
		{"parent material is not current canonical", raw, "父题每小时走多少千米？\n" + canonical},
		{"two fraction slots", "2×　/　+　/　", `$2\times\frac{\phantom{1}}{\phantom{2}}+\frac{\phantom{1}}{\phantom{2}}$`},
		{"slot order differs", "2×　/　×1/1", `$2\times1/1\times\frac{\phantom{1}}{\phantom{2}}$`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := BuildFractionSlotProblemFromSources("shared", tc.raw, tc.canonical); ok {
				t.Fatal("unproved or mismatched fraction source gained an answer target")
			}
		})
	}
}
