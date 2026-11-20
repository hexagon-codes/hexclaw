package usecase

import (
	"math/big"
	"regexp"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 图示、逐空指令、演算和最后的提问分别比较，不能用主问的一个数值代替其余问项。
type travelWorksheetTail struct {
	diagram, blanks, derivation, reason string
	hasFinalValue                       bool
}

func (a travelWorksheetTail) sameQuestions(b travelWorksheetTail) bool {
	return k12.RecognitionSourceQuestionsEqualV1(a.diagram, b.diagram) &&
		k12.RecognitionSourceQuestionsEqualV1(a.blanks, b.blanks) &&
		k12.RecognitionSourceQuestionsEqualV1(a.derivation, b.derivation) &&
		k12.RecognitionSourceQuestionsEqualV1(a.reason, b.reason)
}

var travelDerivationWithUnit = regexp.MustCompile(`^[:：\s]*([0-9\s.+×÷=*/()（）＝−－-]+)[（(]([\p{Han}A-Za-z]+)[）)][\s。.]*$`)

func readTravelWorksheetTail(tail string, travel elementarymath.UniformTravelProblem) (travelWorksheetTail, bool) {
	var part travelWorksheetTail
	diagram, rest, found := strings.Cut(tail, "从上图看出")
	if !found {
		return part, false
	}
	blanks, rest, found := strings.Cut(rest, "由此推算出")
	if !found || !strings.Contains(blanks, "份") || !strings.Contains(blanks, "也就是") {
		return part, false
	}
	derivation, reason, found := strings.Cut(rest, "推算过程中")
	if !found {
		derivation, reason, found = strings.Cut(rest, "计算过程中")
	}
	if !found || !strings.Contains(reason, "依据") {
		return part, false
	}
	match := travelDerivationWithUnit.FindStringSubmatch(strings.TrimSpace(derivation))
	if match == nil || elementarymath.NormalizeUnit(match[2]) != elementarymath.NormalizeUnit(travel.Unit) {
		return part, false
	}
	part.derivation = strings.TrimSpace(strings.ReplaceAll(match[1], "＝", "="))
	if strings.Count(part.derivation, "=") < 2 {
		return part, false
	}
	// 有的读数写出等号链末尾的结果。只有它等于按题中路程和用时精算的值才视作冗余排版。
	equals := strings.LastIndexByte(part.derivation, '=')
	if value, numeric := new(big.Rat).SetString(strings.TrimSpace(part.derivation[equals+1:])); numeric {
		hours, _ := new(big.Rat).SetString(travel.Hours)
		distance, _ := new(big.Rat).SetString(travel.Distance)
		if value.Cmp(distance.Quo(distance, hours)) != 0 {
			return part, false
		}
		part.derivation = strings.TrimSpace(part.derivation[:equals])
		part.hasFinalValue = true
	}
	part.diagram = strings.TrimSpace(diagram)
	for _, heading := range []string{"图示：", "图示:", "图中标注：", "图中标注:"} {
		if after, ok := strings.CutPrefix(part.diagram, heading); ok {
			part.diagram = strings.TrimSpace(after)
			break
		}
	}
	// 对照副本中只消除「这样的1份」的重复计数词；原图转写和交给求解的原始读数保留。
	part.blanks = strings.ReplaceAll(blanks, "这样的一1份", "这样的1份")
	part.reason = reason
	return part, true
}
