package engine

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/hexagon-codes/hexclaw/adapter"
)

var gradingSourceEquation = regexp.MustCompile(`[0-9A-Za-z().+*/-]+=[0-9A-Za-z().+*/-]+`)

func (a gradeAssessment) verdict() string {
	if a.unverifiable {
		return "unverifiable"
	}
	if a.correct {
		return "agree"
	}
	return "disagree"
}

// 首错解释可以换措辞，但必须有原作答来源；独立参考答案不证明孩子写过某一步。
func groundGradingAssessment(a gradeAssessment, problem, studentAnswer, groundTruth string) gradeAssessment {
	if a.correct {
		return a
	}
	student := normalizeGradingSource(studentAnswer)
	if !gradingOnlyFinalValue(student) {
		// 可精确复算时直接定位原等式，不采用模型虚构或换题的步骤。
		if valid, conclusive, firstWrong := validateStudentArithmeticWork(problem, normalizeArithmeticAnswerMarkup(adapter.NormalizeMathText(studentAnswer))); conclusive && !valid && firstWrong != "" {
			firstSource := normalizeGradingSource(firstWrong)
			identifiesFirst := gradingSourceContains(normalizeGradingSource(a.wrongStepSource), firstSource) || gradingSourceContains(normalizeGradingSource(a.wrongStep), firstSource)
			if !identifiesFirst || !gradingExplanationUsesSource(a.wrongStep, student) {
				a.wrongStep = strings.TrimSpace(firstWrong)
				a.misconception = "The original equality does not hold when recalculated."
				a.guidance = "Recalculate the original equality step by step."
			}
			return a
		}
		source := normalizeGradingSource(a.wrongStepSource)
		if source == "" {
			// 旧响应没有独立引文时，只认可本身可在原作答定位的步骤，不强求解释逐字相同。
			source = normalizeGradingSource(a.wrongStep)
		}
		if gradingSourceContains(student, source) && gradingExplanationUsesSource(a.wrongStep, student) {
			return a
		}
	}
	// 没有过程来源时不发布首错/错因，也不能据此推断整份作答正确。
	a.wrongStep, a.wrongStepSource, a.misconception, a.guidance = "", "", "", ""
	a.unverifiable = true
	if !a.finalAnswerCorrect {
		// 完整最终值已被程序证明不同，保留答错事实；首错仍保持缺失。
		if equal, comparable := verificationAnswersEqual(problem, studentAnswer, groundTruth); comparable && !equal {
			a.unverifiable = false
		}
	}
	return a
}

func normalizeGradingSource(value string) string {
	value = normalizeArithmeticAnswerMarkup(adapter.NormalizeMathText(value))
	value = strings.NewReplacer("**", "", "`", "", "×", "*", "÷", "/", "＝", "=", "−", "-", "＋", "+", "（", "(", "）", ")").Replace(value)
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

func gradingOnlyFinalValue(value string) bool {
	value = normalizeAnswer(value)
	for _, label := range []string{"最终答案：", "最终答案:", "答案：", "答案:", "答：", "答:"} {
		value = strings.TrimPrefix(value, label)
	}
	if strings.Contains(value, "=") {
		return false
	}
	return completeVerificationAnswer(value)
}

func gradingSourceContains(student, source string) bool {
	if len([]rune(source)) < 2 {
		return false
	}
	for start := 0; start < len(student); {
		index := strings.Index(student[start:], source)
		if index < 0 {
			return false
		}
		index += start
		end := index + len(source)
		// 数字/变量边界不能把15的来源截成5，中文解释不参与这个限制。
		leftOK := index == 0 || !gradingSourceToken(student[index-1]) || !gradingSourceToken(source[0])
		rightOK := end == len(student) || !gradingSourceToken(student[end]) || !gradingSourceToken(source[len(source)-1])
		if leftOK && rightOK {
			return true
		}
		start = index + 1
	}
	return false
}

func gradingSourceToken(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value == '.'
}

func gradingExplanationUsesSource(explanation, student string) bool {
	for _, equation := range gradingSourceEquation.FindAllString(normalizeGradingSource(explanation), -1) {
		if !gradingSourceContains(student, equation) {
			return false
		}
	}
	return true
}
