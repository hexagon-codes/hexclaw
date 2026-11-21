package usecase

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 图示、逐空指令、演算和最后的提问分别比较，不能用主问的一个数值代替其余问项。
type travelWorksheetTail struct {
	diagram, blanks, derivation, reason             string
	diagramText, blanksText, workedText, reasonText string
	hasFinalValue                                   bool
}

// 标题只在比较副本中识别，仍用原始切片交给求解。中文排版换行不改变标题。
func travelHeading(words ...string) *regexp.Regexp {
	choices := make([]string, len(words))
	for i, word := range words {
		choices[i] = strings.Join(strings.Split(word, ""), `[\s\p{Zs}]*`)
	}
	return regexp.MustCompile(strings.Join(choices, "|"))
}

var travelBlanksHeading = travelHeading("从上图看出", "从上图得出", "由上图可知", "根据上图可知")
var travelDerivationHeading = travelHeading("由此推算出")
var travelReasonHeading = travelHeading("推算过程中", "计算过程中")
var travelQuotedReason = regexp.MustCompile(`^\s*[“"]\s*[0-9\s.+×÷*/()（）−－-]+\s*[=＝]\s*[0-9\s.+×÷*/()（）−－-]+\s*[”"]\s*的依据`)
var travelDerivationWithUnit = regexp.MustCompile(`^[:：\s]*([0-9\s.+×÷=*/()（）＝−－-]+)[（(]([\p{Han}A-Za-z]+)[）)][\s。.]*$`)

func readTravelWorksheetTail(tail string, travel elementarymath.UniformTravelProblem) travelWorksheetTail {
	var part travelWorksheetTail
	blank := travelBlanksHeading.FindStringIndex(tail)
	if blank == nil {
		return part
	}
	part.diagramText = strings.TrimSpace(tail[:blank[0]])
	part.diagram = part.diagramText
	for _, heading := range []string{"图示：", "图示:", "图中标注：", "图中标注:"} {
		if after, ok := strings.CutPrefix(part.diagram, heading); ok {
			part.diagram = strings.TrimSpace(after)
			break
		}
	}
	work := travelDerivationHeading.FindStringIndex(tail[blank[1]:])
	if work == nil {
		return part
	}
	workStart, workEnd := blank[1]+work[0], blank[1]+work[1]
	part.blanksText = strings.TrimSpace(tail[blank[0]:workStart])
	blanks := tail[blank[1]:workStart]
	if strings.Contains(blanks, "份") && strings.Contains(k12.RecognitionSourceComparisonV1(blanks), "也就是") {
		part.blanks = strings.ReplaceAll(blanks, "这样的一1份", "这样的1份")
	}
	end := len(tail)
	if reason := travelReasonHeading.FindStringIndex(tail[workEnd:]); reason != nil {
		end = workEnd + reason[0]
		if question := tail[workEnd+reason[1]:]; strings.Contains(k12.RecognitionSourceComparisonV1(question), "依据") {
			part.reason = question
			part.reasonText = strings.TrimSpace(tail[end:])
		}
	}
	match := travelDerivationWithUnit.FindStringSubmatch(strings.TrimSpace(tail[workEnd:end]))
	if match == nil || elementarymath.NormalizeUnit(match[2]) != elementarymath.NormalizeUnit(travel.Unit) {
		return part
	}
	part.derivation = strings.TrimSpace(strings.ReplaceAll(match[1], "＝", "="))
	if strings.Count(part.derivation, "=") < 2 {
		part.derivation = ""
		return part
	}
	// 有的读数写出等号链末尾的结果。只有它等于按题中路程和用时精算的值才视作冗余排版。
	equals := strings.LastIndexByte(part.derivation, '=')
	if value, numeric := new(big.Rat).SetString(strings.TrimSpace(part.derivation[equals+1:])); numeric {
		hours, _ := new(big.Rat).SetString(travel.Hours)
		distance, _ := new(big.Rat).SetString(travel.Distance)
		if value.Cmp(distance.Quo(distance, hours)) != 0 {
			part.derivation = ""
			return part
		}
		part.derivation = strings.TrimSpace(part.derivation[:equals])
		part.hasFinalValue = true
	}
	part.workedText = strings.TrimSpace(tail[workStart:end])
	return part
}

// 依赖前文的问项使用已核实的连续前缀；题目完整引述的独立等式可以单独核实算理。
func corroboratedTravelSource(primary string, originals, tails []string, travel elementarymath.UniformTravelProblem) string {
	first := readTravelWorksheetTail(tails[0], travel)
	diagram, blanks, worked, reason := first.diagram != "", first.blanks != "", first.derivation != "", first.reason != ""
	selected := originals[0]
	for i := 1; i < len(tails); i++ {
		other := readTravelWorksheetTail(tails[i], travel)
		diagram = diagram && k12.RecognitionSourceQuestionsEqualV1(first.diagram, other.diagram)
		blanks = blanks && k12.RecognitionSourceQuestionsEqualV1(first.blanks, other.blanks)
		worked = worked && k12.RecognitionSourceQuestionsEqualV1(first.derivation, other.derivation)
		reason = reason && k12.RecognitionSourceQuestionsEqualV1(first.reason, other.reason)
		if worked && other.hasFinalValue && !first.hasFinalValue {
			selected, first.workedText, first.hasFinalValue = originals[i], other.workedText, true
		}
	}
	parts := []string{primary}
	if diagram {
		parts = append(parts, first.diagramText)
	}
	if diagram && blanks {
		parts = append(parts, first.blanksText)
		if worked {
			if reason {
				return selected
			}
			parts = append(parts, first.workedText)
		}
	}
	if reason && travelQuotedReason.MatchString(first.reason) {
		parts = append(parts, first.reasonText)
	}
	return strings.Join(parts, "\n")
}

// 题干已有主问时，求解和讲解审计仍须逐项交付印刷的填空与依据。
// 只描述已通过来源核对的问项，不使用手写答案或从页尾猜测缺口。
func travelReportTargets(question string) string {
	if !elementarymath.IsTravelRateSource(question) {
		return ""
	}
	blank := travelBlanksHeading.FindStringIndex(question)
	if blank == nil {
		return ""
	}
	tail := question[blank[1]:]
	blanks, worked := tail, ""
	if heading := travelDerivationHeading.FindStringIndex(tail); heading != nil {
		blanks, worked = tail[:heading[0]], tail[heading[1]:]
	}
	blanks = strings.NewReplacer("（", "(", "）", ")").Replace(elementarymath.TravelFractionBlankLayout(blanks))
	slotCount := len(mathEmptyPrintedSlot.FindAllStringIndex(blanks, -1)) + strings.Count(blanks, "□")
	if slotCount == 0 {
		return ""
	}
	contract := fmt.Sprintf("\nThis printed worksheet asks for a rate AND an ordered sequence of %d fill-in values. Return the rate and every blank in printed order in the final answer, parent_guide.answer, and report_content.items[0].answers. Give each blank a separate labeled entry even when values repeat. Showing a value only in a worked step does not fill its blank.", slotCount)
	if heading := travelReasonHeading.FindStringIndex(worked); heading != nil && strings.Contains(k12.RecognitionSourceComparisonV1(worked[heading[1]:]), "依据") {
		contract += " The last printed prompt also asks for the justification of its equality. Include the named mathematical rule as a final labeled answer and explain it in the deep section."
	}
	return contract
}
