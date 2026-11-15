package engine

import (
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
)

func TestQuantityAnswerCompletenessKeepsCurrentNumericalTarget(t *testing.T) {
	const question = "将这两个圆按如图②所示放置，涂色部分的面积是（　　）cm²。"
	for _, tc := range []struct {
		name, problem, answer string
		required, complete    bool
	}{
		{"missing quantity", question, "缺少图②，无法确定涂色部分的面积，需补充完整图示。", true, false},
		{"empty", question, "", true, false},
		{"decimal area", question, "25.12平方厘米", true, true},
		{"complete named area", question, "涂色面积为25.12平方厘米", true, true},
		{"complete labeled pi area", question, "涂色面积：9π平方厘米", true, true},
		{"explicit answer label", question, "答案：25.12平方厘米", true, true},
		{"uncertain named area", question, "面积可能为25.12平方厘米", true, false},
		{"printed unit", question, "25.12", true, true},
		{"fraction area", question, "3/4 cm²", true, true},
		{"pi area", question, "9π cm²", true, true},
		{"fraction pi area", question, "9π/4 cm²", true, true},
		{"compound pi area", question, "(9π-12)cm²", true, true},
		{"pi prose is not quantity", question, "需要图②才能用π求面积", true, false},
		{"pi zero denominator", question, "9π/0 cm²", true, false},
		{"tex area", `涂色部分的面积是（ ）\(\mathrm{cm}^2\)。`, "9π平方厘米", true, true},
		{"escaped tex area", `涂色部分的面积是（ ）\\(\\mathrm{cm}^2\\)。`, "9π平方厘米", true, true},
		{"open explanation", "说明为什么两个图形的面积相等。", "因为可以通过平移完全重合。", false, false},
		{"unknown target", "图中表示的规律是（ ）。", "缺少图示", false, false},
		{"multiple targets", "面积是（ ）cm²，周长是（ ）cm。", "25.12平方厘米", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			required, complete := QuantityAnswerCompleteness(tc.problem, tc.answer)
			if required != tc.required || complete != tc.complete {
				t.Fatalf("required=%v complete=%v; want %v/%v", required, complete, tc.required, tc.complete)
			}
		})
	}
	wrapped, ok := elementarymath.BuildMathSubproblem("其他题的面积是（ ）cm²。", question, question)
	if !ok {
		t.Fatal("current source fixture is invalid")
	}
	if required, complete := QuantityAnswerCompleteness(wrapped, "25.12cm²"); !required || !complete {
		t.Fatal("shared material must not change the current answer target")
	}
}
