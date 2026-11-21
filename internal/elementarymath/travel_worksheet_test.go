package elementarymath

import (
	"strings"
	"testing"
)

func TestTravelWorksheetSeparatesPartCountFromElapsedTime(t *testing.T) {
	const src = "1. 小宁3/5小时步行了6千米，按相同的速度，她每小时步行多少千米？图示：1小时走了？km；1/5小时走了？km；3/5小时走了6km。" +
		"从上图看出：已知（ ）份是6千米，可以先求出这样的1份是多少，也就是6÷3＝6×（ ）；再求这样的（ ）份是多少，也就是6×（ ）×5。" +
		"由此推\n算出：6÷3/5＝6÷3×5＝6×1/3×5＝6×（1/3×5）＝6×5/3＝10（km）。推算过程中“6×1/3×5＝6×（1/3×5）”的依据是（ ）。"
	sheet, ok := ParseTravelWorksheet(src)
	if !ok || sheet.Parts != "3" || sheet.HourParts != "5" || sheet.Reciprocal != "1/3" || sheet.PartDistance != "2" || sheet.Rate != "10" || !strings.Contains(sheet.Worked(), sheet.Answer()) {
		t.Fatalf("wrong worksheet targets: %+v", sheet)
	}
	fractions := strings.ReplaceAll(src, "×（ ）", `×\frac{\square}{\square}`)
	for _, text := range []string{
		fractions,
		strings.ReplaceAll(fractions, `\frac{\square}{\square}`, "□/□"),
		strings.Replace(fractions, "图示：", "图：", 1),
		strings.Replace(fractions, "从上图看出：", "从上图得出：", 1),
		strings.Replace(fractions, "从上图看出：", "", 1),
	} {
		if withBoxes, ok := ParseTravelWorksheet(text); !ok || withBoxes != sheet {
			t.Fatal("printed diagram or fraction boxes lost their target")
		}
	}
	for _, tc := range []struct{printed, written string}{
		{`\frac{\square}{3}`, "1"}, {`\frac{1}{\square}`, "3"},
	} {
		variant, ok := ParseTravelWorksheet(strings.Replace(fractions, `\frac{\square}{\square}`, tc.printed, 1))
		if !ok || variant.WrittenValues() != [4]string{"3",tc.written,"5","1/3"} { t.Fatalf("wrong written value for %s: %+v",tc.printed,variant) }
		merged, agrees := sheet.Corroborate(variant)
		if !agrees || merged.FactorSlots != [2]FractionSlot{UncertainFractionSlot,WholeFraction} || !strings.Contains(merged.Answer(),"只空分母填3") { t.Fatalf("unresolved slot treated as certain: %+v",merged) }
	}
	for _, other := range []string{
		strings.Replace(fractions, `\frac{\square}{\square}`, `\frac{2}{\square}`, 1),
		strings.Replace(fractions, `\frac{\square}{\square}`, `\frac{\square}{4}`, 1),
		strings.Replace(src, "已知（ ）份是6", "已知（ ）份是9", 1),
		strings.Replace(src, "6÷3＝6", "6×3＝6", 1),
		strings.Replace(src, "3/5小时走了6km", "3/5小时走了6米", 1),
		strings.Replace(src, "从上图看出：", "余下路程速度加倍：", 1),
		strings.Replace(src, "6×（1/3×5）”", "6×（1/3+5）”", 1),
		src + "如果走得更快呢？",
	} {
		if _, ok := ParseTravelWorksheet(other); ok {
			t.Fatalf("conflicting or incomplete source entered exact calculation: %s", other)
		}
	}
}
