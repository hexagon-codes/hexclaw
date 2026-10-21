package engine

import (
	"regexp"
	"strings"
)

const verificationRationalLiteral = `[+-]?[0-9]+(?:\.[0-9]+)?(?:/[0-9]+(?:\.[0-9]+)?)?`

var verificationNumberedGroupRe = regexp.MustCompile(`[（(]([0-9]+)[）)]`)
var verificationDivisionEquationRe = regexp.MustCompile(`(` + verificationRationalLiteral + `)\s*÷\s*(` + verificationRationalLiteral + `)\s*[=＝]\s*(` + verificationRationalLiteral + `)`)
var verificationMultiplicationEquationRe = regexp.MustCompile(`^(` + verificationRationalLiteral + `)\s*×\s*(` + verificationRationalLiteral + `)\s*[=＝]\s*(` + verificationRationalLiteral + `)\s*$`)

type verificationDivisionFamily struct {
	label         string
	a, b, product float64
}

// verificationDivisionFamilyScope 只识别原题明确要求由每道乘法写出两道除法的完整题组。
func verificationDivisionFamilyScope(problem string) ([]verificationDivisionFamily, bool) {
	if !strings.Contains(problem, "乘法算式") || !strings.Contains(problem, "两道除法算式") {
		return nil, false
	}
	var groups []verificationDivisionFamily
	for _, line := range strings.Split(problem, "\n") {
		line = strings.TrimSpace(normalizeAnswer(line))
		label := verificationNumberedGroupRe.FindStringSubmatchIndex(line)
		if label == nil || label[0] != 0 {
			continue
		}
		m := verificationMultiplicationEquationRe.FindStringSubmatch(strings.TrimSpace(line[label[1]:]))
		if m == nil {
			return nil, false
		}
		a, aok := numericValue(m[1])
		b, bok := numericValue(m[2])
		product, pok := numericValue(m[3])
		if !aok || !bok || !pok || a == 0 || b == 0 || floatsClose(a, b) || !floatsClose(a*b, product) {
			return nil, false
		}
		name := line[label[2]:label[3]]
		for _, group := range groups {
			if group.label == name {
				return nil, false
			}
		}
		groups = append(groups, verificationDivisionFamily{label: name, a: a, b: b, product: product})
	}
	return groups, len(groups) > 0
}

// divisionFamilyAnswerValid 接受两式换序及明确展示两种顺序，不能遗漏、跨组或夹入其他等式。
func divisionFamilyAnswerValid(answer string, groups []verificationDivisionFamily) (valid, complete bool) {
	answer = strings.TrimSpace(normalizeAnswer(answer))
	for _, prefix := range []string{"答案：", "答案:"} {
		answer = strings.TrimSpace(strings.TrimPrefix(answer, prefix))
	}
	indices := verificationNumberedGroupRe.FindAllStringSubmatchIndex(answer, -1)
	if len(indices) != len(groups) || indices[0][0] != 0 {
		return false, false
	}
	valid = true
	seen := make(map[string]bool)
	for i, index := range indices {
		name := answer[index[2]:index[3]]
		var group *verificationDivisionFamily
		for j := range groups {
			if groups[j].label == name {
				group = &groups[j]
			}
		}
		if group == nil || seen[name] {
			return false, false
		}
		seen[name] = true
		end := len(answer)
		if i+1 < len(indices) {
			end = indices[i+1][0]
		}
		content := answer[index[1]:end]
		equations := verificationDivisionEquationRe.FindAllStringSubmatch(content, -1)
		if len(equations) != 2 && len(equations) != 4 {
			return false, false
		}
		residual := verificationDivisionEquationRe.ReplaceAllString(content, "")
		if strings.ContainsAny(residual, "=＝÷×+−^<>≤≥") {
			return false, false
		}
		aSeen, bSeen := false, false
		for _, equation := range equations {
			dividend, dok := numericValue(equation[1])
			divisor, vok := numericValue(equation[2])
			quotient, qok := numericValue(equation[3])
			if !dok || !vok || !qok {
				return false, false
			}
			if floatsClose(dividend, group.product) && floatsClose(divisor, group.a) && floatsClose(quotient, group.b) {
				aSeen = true
			} else if floatsClose(dividend, group.product) && floatsClose(divisor, group.b) && floatsClose(quotient, group.a) {
				bSeen = true
			} else {
				valid = false
			}
		}
		valid = valid && aSeen && bSeen
	}
	return valid, true
}

func divisionFamilyVerificationAnswersEqual(problem, computed, candidate string) (equal, comparable, scoped bool) {
	groups, scoped := verificationDivisionFamilyScope(problem)
	if !scoped {
		return false, false, false
	}
	lv, lc := divisionFamilyAnswerValid(computed, groups)
	rv, rc := divisionFamilyAnswerValid(candidate, groups)
	return lv && rv, lc && rc, true
}
