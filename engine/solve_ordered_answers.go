package engine

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

type orderedVerificationAnswer struct {
	values   []answerQuantity
	judgment bool
	subject  string
}

var orderedAnswerSeparator = regexp.MustCompile(`[,，、]`)
var namedJudgmentAnswer = regexp.MustCompile(`^([\p{Han}]{1,12})的说法(不正确|不对|错误|正确|对)(.*)$`)
var numericalClaimRe = regexp.MustCompile(`([\p{Han}]{1,12})算出`)
var namedResultJudgmentRe = regexp.MustCompile(`^([\p{Han}]{1,12})的结果(不正确|错误|正确)(.*)$`)

func asksSingleJudgment(problem string) bool {
	count := 0
	for _, marker := range []string{"说法对吗", "说得对吗", "说法对不对", "说法是否正确", "说法正确吗"} {
		count += strings.Count(problem, marker)
	}
	return count == 1
}

// orderedVerificationScope 只比较原题明确的多个填空，混合判断时必须一并保留。
func orderedVerificationScope(problem string) (int, bool) {
	count := len(verificationBlankSlotRe.FindAllStringIndex(problem, -1))
	return count, count >= 2 && strings.Contains(problem, "填")
}

func parseOrderedVerificationAnswer(problem, answer string, count int) (orderedVerificationAnswer, bool) {
	var out orderedVerificationAnswer
	answer = normalizeAnswer(answer)
	for _, prefix := range []string{"依次填：", "依次填:", "依次填", "答案：", "答案:"} {
		answer = strings.TrimSpace(strings.TrimPrefix(answer, prefix))
	}
	// 完整数值序列与判断之间必须有显式边界；不能从长讲解抽出局部数字。
	separator := strings.IndexAny(answer, "；;。")
	hasJudgment := asksSingleJudgment(problem)
	if separator < 0 && hasJudgment {
		return out, false
	}
	if !hasJudgment {
		answer = strings.TrimRight(answer, "。;； ")
		separator = len(answer)
	}
	values := orderedAnswerSeparator.Split(answer[:separator], -1)
	if len(values) != count {
		return out, false
	}
	for _, value := range values {
		quantity, ok := completeVerificationQuantity(strings.TrimSpace(value))
		if !ok {
			return out, false
		}
		out.values = append(out.values, quantity)
	}
	if !hasJudgment {
		return out, true
	}
	judgment := strings.TrimSpace(answer[separator:])
	judgment = strings.TrimLeft(judgment, "；;。 ")
	match := namedJudgmentAnswer.FindStringSubmatch(judgment)
	if len(match) != 4 {
		return out, false
	}
	out.subject = match[1]
	if !strings.Contains(problem, out.subject+"说") && !strings.Contains(problem, out.subject+"认为") && !strings.Contains(problem, out.subject+"的说法") {
		return out, false
	}
	// 后续只能是明确分隔的理由；理由仍由完整解法的过程审计负责。
	reason := strings.TrimSpace(match[3])
	if reason != "" {
		first, _ := utf8.DecodeRuneInString(reason)
		if !strings.ContainsRune(",，。;；:：", first) && !strings.HasPrefix(reason, "因为") {
			return out, false
		}
	}
	if strings.Contains(reason, "的说法") {
		return out, false
	}
	out.judgment = match[2] == "正确" || match[2] == "对"
	return out, true
}

func orderedVerificationAnswersEqual(problem, computed, candidate string) (equal, comparable, scoped bool) {
	count, scoped := orderedVerificationScope(problem)
	if !scoped {
		return false, false, false
	}
	left, leftOK := parseOrderedVerificationAnswer(problem, computed, count)
	right, rightOK := parseOrderedVerificationAnswer(problem, candidate, count)
	if !leftOK || !rightOK || left.subject != right.subject {
		return false, false, true
	}
	if left.judgment != right.judgment {
		return false, true, true
	}
	for i := range left.values {
		if !quantitiesEqual(left.values[i], right.values[i]) {
			return false, true, true
		}
	}
	return true, true, true
}

// resultJudgmentVerificationAnswersEqual 将明确的数值结果判定作为布尔答案，不能套用于求值题。
func resultJudgmentVerificationAnswersEqual(problem, computed, candidate string) (equal, comparable, scoped bool) {
	if !strings.Contains(problem, "判断") || !strings.Contains(problem, "结果是否正确") {
		return false, false, false
	}
	claims := numericalClaimRe.FindAllStringSubmatchIndex(problem, -1)
	if len(claims) != 1 {
		return false, false, false
	}
	claim := claims[0]
	subject := problem[claim[2]:claim[3]]
	text := problem[claim[1]:]
	end := strings.IndexAny(text, "。!?！？")
	if end < 0 {
		return false, false, false
	}
	quantities, ok := parseLabeledAnswerQuantities(text[:end])
	if !ok {
		return false, false, false
	}
	parse := func(answer string) (bool, bool) {
		answer = normalizeAnswer(answer)
		prefix := ""
		if at := strings.IndexAny(answer, ";；"); at >= 0 {
			_, width := utf8.DecodeRuneInString(answer[at:])
			prefix, answer = strings.TrimSpace(answer[:at]), strings.TrimSpace(answer[at+width:])
		}
		match := namedResultJudgmentRe.FindStringSubmatch(answer)
		if len(match) != 4 || match[1] != subject || strings.Trim(match[3], "。 ") != "" {
			return false, false
		}
		judgment := match[2] == "正确"
		if prefix != "" {
			values, ok := parseLabeledAnswerQuantities(prefix)
			if !ok || len(values) != len(quantities) {
				return false, false
			}
			for label, value := range values {
				expected, exists := quantities[label]
				if !exists || (judgment && !quantitiesEqual(value, expected)) {
					return false, false
				}
			}
		}
		return judgment, true
	}
	left, lok := parse(computed)
	right, rok := parse(candidate)
	return left == right, lok && rok, true
}
