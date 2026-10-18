package elementarymath

import (
	"encoding/json"
	"math/big"
	"regexp"
	"strings"
	"unicode"
)

const fractionSlotProblemPrefix = "k12-fraction-slot-v1\nShared context:\n"
const fractionSlotCurrentSeparator = "\nCurrent original:\n"
const fractionSlotCanonicalSeparator = "\nCurrent canonical:\n"
const fractionSlotTargetSeparator = "\nAnswer target:\n"
const fractionSlotAnswerTarget = "Fill only the printed numerator and denominator boxes in the current original expression. The shared context supplies facts, not another question to answer. Treat the two boxes as one ordered fraction. The final answer must be only that fraction, with no distance, unit, or answer to the parent problem. Explain the calculation if needed, but do not require the student to rewrite printed expression parts or units outside the boxes. Independently verify the fraction that fills these boxes, not merely the final distance of the parent problem."
const fractionSlotPhantomTarget = " The current canonical expression records the same printed blanks: each \\phantom{...} reserves empty layout space. Its contents are not visible numbers, conditions, or answers; compute the fraction from the actual problem facts."

var fractionSlotTeXBoxes = regexp.MustCompile(`\\frac\s*\{\s*\\square\s*\}\s*\{\s*\\square\s*\}`)
var fractionSlotTeXNumbers = regexp.MustCompile(`\\frac\s*\{([0-9]+)\}\s*\{([0-9]+)\}`)
var fractionSlotPlainBoxes = regexp.MustCompile(`(?:\(\s*□\s*\)|□)\s*/\s*(?:\(\s*□\s*\)|□)`)
var fractionSlotTeXPhantoms = regexp.MustCompile(`\\frac\s*\{\s*\\phantom\s*\{[0-9]+\}\s*\}\s*\{\s*\\phantom\s*\{[0-9]+\}\s*\}`)
var fractionSlotRawSpaces = regexp.MustCompile(`[ \t\p{Zs}]+/[ \t\p{Zs}]+`)
var fractionSlotSeparatedNumbers = regexp.MustCompile(`[0-9.][\s\p{Zs}]+[0-9.]`)
var fractionSlotFinalValue = regexp.MustCompile(`^([+\-]?[0-9]+)[ \t]*/[ \t]*([+\-]?[0-9]+)$`)
var fractionSlotFinalTeX = regexp.MustCompile(`^\\frac[ \t]*\{([+\-]?[0-9]+)\}[ \t]*\{([+\-]?[0-9]+)\}$`)

// BuildFractionSlotProblem 把当前纯数值式中的唯一上下分数空位与共享材料分开。
// 版本和完整材料直接进入已有 Problem 字段，不改原始识读或增加持久字段。
func BuildFractionSlotProblem(shared, current string) (string, bool) {
	return BuildFractionSlotProblemFromSources(shared, current, "")
}

// BuildFractionSlotProblemFromSources 仅在同代次当前题面的两种表示完整一致时补充 phantom 空位。
// 原始空白斜杠本身不构成空位证明，排版占位数字不进入算式或答案。
func BuildFractionSlotProblemFromSources(shared, current, canonical string) (string, bool) {
	if strings.TrimSpace(shared) == "" {
		return "", false
	}
	if isFractionSlotExpression(current) {
		canonical = ""
	} else if !matchingPhantomFractionSlot(current, canonical) {
		return "", false
	}
	sharedJSON, _ := json.Marshal(shared)
	currentJSON, _ := json.Marshal(current)
	projection := fractionSlotProblemPrefix + string(sharedJSON) + fractionSlotCurrentSeparator + string(currentJSON)
	target := fractionSlotAnswerTarget
	if canonical != "" {
		canonicalJSON, _ := json.Marshal(canonical)
		projection += fractionSlotCanonicalSeparator + string(canonicalJSON)
		target += fractionSlotPhantomTarget
	}
	return projection + fractionSlotTargetSeparator + target, true
}

// ParseFractionSlotProblem 只读取本版完整投影，JSON 字符串保证原始换行和公式不会串入段边界。
func ParseFractionSlotProblem(problem string) (shared, current string, ok bool) {
	shared, current, _, ok = ParseFractionSlotProblemSources(problem)
	return
}

// ParseFractionSlotProblemSources 连同可选当前 canonical 返回同源证明，适配层不得丢弃它。
func ParseFractionSlotProblemSources(problem string) (shared, current, canonical string, ok bool) {
	if !strings.HasPrefix(problem, fractionSlotProblemPrefix) {
		return "", "", "", false
	}
	sharedJSON, rest, found := strings.Cut(strings.TrimPrefix(problem, fractionSlotProblemPrefix), fractionSlotCurrentSeparator)
	if !found {
		return "", "", "", false
	}
	currentJSON, target, found := strings.Cut(rest, fractionSlotTargetSeparator)
	if !found || json.Unmarshal([]byte(sharedJSON), &shared) != nil || strings.TrimSpace(shared) == "" {
		return "", "", "", false
	}
	if rawJSON, canonicalJSON, hasCanonical := strings.Cut(currentJSON, fractionSlotCanonicalSeparator); hasCanonical {
		if target != fractionSlotAnswerTarget+fractionSlotPhantomTarget || json.Unmarshal([]byte(rawJSON), &current) != nil || json.Unmarshal([]byte(canonicalJSON), &canonical) != nil || !matchingPhantomFractionSlot(current, canonical) {
			return "", "", "", false
		}
	} else if target != fractionSlotAnswerTarget || json.Unmarshal([]byte(currentJSON), &current) != nil || !isFractionSlotExpression(current) {
		return "", "", "", false
	}
	return shared, current, canonical, true
}

func isFractionSlotExpression(current string) bool {
	_, ok := fractionSlotExpressionSignature(current)
	return ok
}

func matchingPhantomFractionSlot(raw, canonical string) bool {
	if len(fractionSlotRawSpaces.FindAllStringIndex(raw, -1)) != 1 || len(fractionSlotTeXPhantoms.FindAllStringIndex(canonical, -1)) != 1 || strings.Count(canonical, `\phantom`) != 2 {
		return false
	}
	rawSignature, rawOK := fractionSlotExpressionSignature(fractionSlotRawSpaces.ReplaceAllString(raw, "□/□"))
	canonicalSignature, canonicalOK := fractionSlotExpressionSignature(fractionSlotTeXPhantoms.ReplaceAllString(canonical, "□/□"))
	return rawOK && canonicalOK && rawSignature == canonicalSignature
}

func fractionSlotExpressionSignature(current string) (string, bool) {
	s := strings.TrimSpace(current)
	s = fractionSlotTeXBoxes.ReplaceAllString(s, "□/□")
	s = strings.ReplaceAll(s, `\square`, "□")
	if len(fractionSlotPlainBoxes.FindAllStringIndex(s, -1)) != 1 || strings.Count(s, "□") != 2 {
		return "", false
	}
	s = fractionSlotPlainBoxes.ReplaceAllString(s, "□/□")
	s = fractionSlotTeXNumbers.ReplaceAllString(s, "($1/$2)")
	s = strings.NewReplacer(`\times`, "×", `\div`, "÷", `\cdot`, "×", `\left(`, "(", `\right)`, ")", `\(`, "", `\)`, "", `\[`, "", `\]`, "", "$", "").Replace(s)
	// 印刷引导保留在原题中，仅在唯一数值式的比较副本中去掉这一完整前缀。
	s = strings.TrimSpace(strings.TrimPrefix(s, "可以先求出这样的1份是多少，也就是"))
	s = strings.TrimSpace(strings.TrimPrefix(s, "也就是"))
	s = strings.TrimRight(s, "。；; \t")
	if strings.Count(s, "=")+strings.Count(s, "＝") > 1 || fractionSlotSeparatedNumbers.MatchString(s) {
		return "", false
	}
	for _, r := range s {
		if !unicode.IsSpace(r) && !strings.ContainsRune("0123456789.+-*/×÷＋－−()（）=＝□", r) {
			return "", false
		}
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	s = strings.NewReplacer("×", "*", "÷", "/", "＋", "+", "－", "-", "−", "-", "（", "(", "）", ")", "＝", "=").Replace(s)
	return s, s != ""
}

// ParseFractionSlotAnswer 要求最终答案完整给出有序分子和分母，不从复合答句截取数字。
func ParseFractionSlotAnswer(answer string) (string, bool) {
	s := strings.TrimSpace(answer)
	if strings.ContainsAny(s, "\r\n") {
		return "", false
	}
	for _, delimiters := range [][2]string{{`\(`, `\)`}, {`\[`, `\]`}, {"$$", "$$"}, {"$", "$"}} {
		if strings.HasPrefix(s, delimiters[0]) {
			if len(s) < len(delimiters[0])+len(delimiters[1]) || !strings.HasSuffix(s, delimiters[1]) {
				return "", false
			}
			s = strings.TrimSpace(s[len(delimiters[0]) : len(s)-len(delimiters[1])])
			break
		}
	}
	match := fractionSlotFinalValue.FindStringSubmatch(s)
	if len(match) != 3 {
		match = fractionSlotFinalTeX.FindStringSubmatch(s)
	}
	if len(match) != 3 {
		return "", false
	}
	numerator, numeratorOK := new(big.Int).SetString(match[1], 10)
	denominator, denominatorOK := new(big.Int).SetString(match[2], 10)
	if !numeratorOK || !denominatorOK || denominator.Sign() == 0 {
		return "", false
	}
	return numerator.String() + "/" + denominator.String(), true
}
