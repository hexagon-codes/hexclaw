package k12

import (
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

var (
	sourceLatexFractionV1              = regexp.MustCompile(`\\frac\s*\{([^{}]*)\}\s*\{([^{}]*)\}`)
	sourceLatexTextV1                  = regexp.MustCompile(`\\text\s*\{([^{}]*)\}`)
	sourceEvidenceNewlineV1            = regexp.MustCompile(`\\n(?:\b|([0-9]))`)
	sourceNumericFractionV1            = regexp.MustCompile(`\b([0-9]+(?:\.[0-9]+)?)\s*/\s*([0-9]+(?:\.[0-9]+)?)\b`)
	sourceMathRatioV1                  = regexp.MustCompile(`([0-9)π]|\b[A-Za-z]\b)[:：]([0-9A-Za-z(.+\\√π-])`)
	sourceNumberTokensV1               = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)
	sourceGroupedCoefficientFractionV1 = regexp.MustCompile(`(^|[=+\-−])\s*\(([0-9]+(?:\.[0-9]+)?)/([0-9]+(?:\.[0-9]+)?)\)(\s*[xX])`)
	sourceRepeatedMathEscapeV1         = regexp.MustCompile(`\\{2,}(frac|text|times|div|cdot|left|right|qquad|quad|\(|\)|\[|\]| )`)
)

// RecognitionQuestionSourceTextV1 只投影已知题号的印刷排版，不改写原始观察。
// 点号必须与冻结标签一致且后接排版空白，正文小数不能被当作题号删除。
func RecognitionQuestionSourceTextV1(value string, numberPath []string, label string) string {
	if len(numberPath) != 1 || numberPath[0] == "" || strings.Trim(numberPath[0], "0123456789") != "" {
		return value
	}
	text := sourceRepeatedMathEscapeV1.ReplaceAllString(value, `\${1}`)
	text = strings.NewReplacer(`\left`, "", `\right`, "").Replace(text)
	text = RecognitionSourcePlainTextV1(text)
	text = strings.NewReplacer(`\qquad`, " ", `\quad`, " ", `\ `, " ", "−", "-").Replace(text)
	text = strings.TrimSpace(text)
	if rest, ok := strings.CutPrefix(text, numberPath[0]+"、"); ok {
		return sourceGroupedCoefficientFractionV1.ReplaceAllString(strings.TrimSpace(rest), `$1($2)/($3)$4`)
	}
	if label == numberPath[0]+"." {
		if rest, ok := strings.CutPrefix(text, label); ok && rest != "" {
			for _, first := range rest {
				if unicode.IsSpace(first) {
					return sourceGroupedCoefficientFractionV1.ReplaceAllString(strings.TrimSpace(rest), `$1($2)/($3)$4`)
				}
				break
			}
		}
	}
	return sourceGroupedCoefficientFractionV1.ReplaceAllString(text, `$1($2)/($3)$4`)
}

// RecognitionSourcePlainTextV1 保留既有来源排版投影；分数显式加括号，不计算数值。
func RecognitionSourcePlainTextV1(markdown string) string {
	out := markdown
	for {
		next := sourceLatexFractionV1.ReplaceAllString(out, `($1)/($2)`)
		if next == out {
			break
		}
		out = next
	}
	out = sourceLatexTextV1.ReplaceAllString(out, `$1`)
	out = strings.NewReplacer(
		`\times`, "×", `\div`, "÷", `\cdot`, "·", `\leq`, "≤", `\geq`, "≥", `\neq`, "≠",
		`\(`, "", `\)`, "", `\（`, "（", `\）`, "）", `\[`, "", `\]`, "", "$", "",
	).Replace(out)
	return strings.TrimSpace(out)
}

// RecognitionSourceComparisonV1 是既有来源比较视图；原始题干与作答不被改写。
func RecognitionSourceComparisonV1(value string) string {
	value = sourceEvidenceNewlineV1.ReplaceAllString(value, "\n$1")
	value = sourceNumericFractionV1.ReplaceAllString(value, `\frac{$1}{$2}`)
	value = strings.ReplaceAll(value, `\ `, " ")
	value = strings.NewReplacer("²", "^2", "³", "^3", "^{2}", "^2", "^{3}", "^3").Replace(value)
	value = strings.Join(strings.Fields(RecognitionSourcePlainTextV1(value)), "")
	value = strings.NewReplacer("（", "(", "）", ")", "＝", "=", "＋", "+", "−", "-", "－", "-").Replace(value)
	// 已规范的比号不再占用相邻比项，第二遍补齐连续比中的交错边界。
	value = sourceMathRatioV1.ReplaceAllString(value, "$1∶$2")
	value = sourceMathRatioV1.ReplaceAllString(value, "$1∶$2")
	return strings.NewReplacer(`\,`, "", "。", "", "；", "", "，", "", "：", "", "、", "", ";", "", ":", "").Replace(value)
}

// RecognitionSourceQuestionsEqualV1 只接受完整来源的排版差异，数字 token 不能因去空格而合并。
func RecognitionSourceQuestionsEqualV1(left, right string) bool {
	leftView, rightView := RecognitionSourceComparisonV1(left), RecognitionSourceComparisonV1(right)
	if leftView == "" || leftView != rightView {
		return false
	}
	numbers := func(value string) []string {
		value = strings.NewReplacer("²", "2", "³", "3").Replace(value)
		return sourceNumberTokensV1.FindAllString(value, -1)
	}
	return slices.Equal(numbers(left), numbers(right))
}

// RecognitionFinalAnswerSourceV1 是三次原题观察共用的来源事实，不包含求解结果。
type RecognitionFinalAnswerSourceV1 struct {
	Question    string   `json:"question"`
	Subject     string   `json:"subject"`
	AnswerState string   `json:"answer_state"`
	Confidence  *float64 `json:"recognition_confidence"`
	OCRSignals  []string `json:"ocr_signals"`
}

// RecognitionSourceObservationUnresolvedV1 按信号合同判断是否存在未决观察，不解释自然语言关键词。
func RecognitionSourceObservationUnresolvedV1(signals []string) bool {
	for _, signal := range signals {
		if strings.TrimSpace(signal) != "" {
			return true
		}
	}
	return false
}

// RecognitionFinalAnswerSourcesTrustedV1 统一来源元数据与题干判据，原过程风险由调用方继续保留。
func RecognitionFinalAnswerSourcesTrustedV1(sources ...RecognitionFinalAnswerSourceV1) bool {
	if len(sources) == 0 {
		return false
	}
	for _, source := range sources {
		if source.Subject != "数学" || source.AnswerState != "present" || source.Confidence == nil ||
			math.IsNaN(*source.Confidence) || *source.Confidence < 0.90 || *source.Confidence > 1 ||
			!RecognitionSourceQuestionsEqualV1(sources[0].Question, source.Question) {
			return false
		}
	}
	return true
}

// RecognitionFinalAnswerSourcesTrustedForPolicyV1 仅新合同容许首读的整段置信度偏低。
// 复读及独立裁决仍须可信，所有完整题干和来源元数据保持一致。
func RecognitionFinalAnswerSourcesTrustedForPolicyV1(policy string, sources ...RecognitionFinalAnswerSourceV1) bool {
	if policy != RecognitionExecutionPolicyCompactDAGFinalAnswerV2 {
		return policy == RecognitionExecutionPolicyCompactDAGFinalAnswerV1 && RecognitionFinalAnswerSourcesTrustedV1(sources...)
	}
	if len(sources) < 2 || !RecognitionFinalAnswerSourcesTrustedV1(sources[1:]...) {
		return false
	}
	first := sources[0]
	return first.Subject == "数学" && first.AnswerState == "present" && first.Confidence != nil &&
		!math.IsNaN(*first.Confidence) && !math.IsInf(*first.Confidence, 0) && *first.Confidence >= 0 && *first.Confidence <= 1 &&
		RecognitionSourceQuestionsEqualV1(first.Question, sources[1].Question)
}
