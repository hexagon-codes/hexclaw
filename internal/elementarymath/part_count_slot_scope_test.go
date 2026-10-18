package elementarymath

import "testing"

func TestPartCountSlotScopePreservesOnlyCurrentPrintedBlank(t *testing.T) {
	const shared = "小明2/3小时走了2千米，照这样的速度，他每小时行走多少千米？"
	const current = "从上图看出：已知（ ）份是2千米，可以先求出这样的1份是多少，也就是"
	const canonical = "从上图看出：已知（　）份是 $2$ 千米，可以先求出这样的 $1$ 份是多少，也就是"
	const observedRaw = "从上图看出：已知（　）份是2千米，可以先求出这样的1份是多少，也就是"
	const observedCanonical = `从上图看出：已知（$\square$）份是 $2$ 千米，可以先求出这样的 $1$ 份是多少，也就是……`
	for _, source := range [][2]string{
		{"从上图看出：已知（　）份是2千米", "从上图看出：已知（　）份是 $2$ 千米。"},
		{"再求这样的（　）份是多少", "再求这样的（　）份是多少。"},
		{observedRaw, observedCanonical},
		{"再求这样的（　）份是多少，", `再求这样的（$\square$）份是多少，`},
		{current, canonical}, {"相当于( )份。", ""}, {"相当于（　）份。", "相当于（　）份。"},
	} {
		problem, ok := BuildPartCountSlotProblemFromSources(shared, source[0], source[1])
		if !ok {
			t.Fatalf("unique count blank was not projected: %q", source[0])
		}
		gotShared, gotCurrent, gotCanonical, ok := ParsePartCountSlotProblemSources(problem)
		if !ok || gotShared != shared || gotCurrent != source[0] || gotCanonical != source[1] {
			t.Fatal("scope changed original or shared source bytes")
		}
	}
	for _, source := range [][2]string{
		{"已知（ ）份和（ ）份。", ""}, {"已知（ ）份，走（ ）千米。", ""},
		{"已知（ ）份，填写□。", ""}, {"已知（2）份。", ""}, {"已知（ ）千米。", ""},
		{current, "从上图看出：已知（　）份是 $3$ 千米，可以先求出这样的 $1$ 份是多少，也就是"},
		{current, "已知（　）份和（　）份。"},
		{"已知（ ）份是2 3千米。", "已知（ ）份是23千米。"},
		{"已知（ ）份是23千米。", "已知（ ）份是2 3千米。"},
		{"已知（ ）份是23千米。", "已知（ ）份是 $2$ $3$ 千米。"},
		{observedRaw, `从上图看出：已知（$\square\square$）份是 $2$ 千米，可以先求出这样的 $1$ 份是多少，也就是……`},
		{observedRaw, `从上图看出：已知（$\square$）份是 $2$ 千米，可以先求出这样的（$\square$）份是多少，也就是……`},
		{observedRaw, `从上图看出：已知（$\square$）份是 $2$ 米，可以先求出这样的 $1$ 份是多少，也就是……`},
		{observedRaw, `从上图看出：已知（$\square$）份是 $3$ 千米，可以先求出这样的 $1$ 份是多少，也就是……`},
		{observedRaw, `从上图看出：已知 $2$ 千米，可以先求出这样的（$\square$）份是多少，也就是……`},
		{observedRaw, `从上图看出：已知（$\square$）份……`},
		{observedRaw, `从上图看出：已知（$\square$）份是 $2$ 千米……可以先求出这样的 $1$ 份是多少，也就是`},
	} {
		if _, ok := BuildPartCountSlotProblemFromSources(shared, source[0], source[1]); ok {
			t.Fatalf("ambiguous or conflicting source became a count target: %+v", source)
		}
	}
}
