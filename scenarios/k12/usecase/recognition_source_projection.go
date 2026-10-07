package usecase

import "strings"

// RecognitionLayoutSourceProjection 是最终识题 JSON 内的派生来源映射。
// 两个目标的物理回执与裁决仍由原计划终结回执保存，不改变 canonical 内容身份。
type RecognitionLayoutSourceProjection struct {
	PageDigest         string    `json:"page_digest"`
	PlanID             string    `json:"plan_id"`
	PlanDigest         string    `json:"plan_digest"`
	FinalizationDigest string    `json:"finalization_digest"`
	TargetIDs          [2]string `json:"target_ids"`
}

func normalizedRecognitionSourceReading(value string) string {
	// 仅改变比较视图，不改原始转写；分数边界、数值和运算分组仍参与比较。
	value = evidenceNewline.ReplaceAllString(value, "\n$1")
	value = evidenceNumericFraction.ReplaceAllString(value, `\frac{$1}{$2}`)
	value = strings.ReplaceAll(value, `\ `, " ")
	value = strings.NewReplacer("²", "^2", "³", "^3", "^{2}", "^2", "^{3}", "^3").Replace(value)
	value = strings.Join(strings.Fields(CanonicalPlainTextFallback(value)), "")
	value = evidenceNumericRatio.ReplaceAllString(value, "$1∶$2")
	value = evidenceNumericRatio.ReplaceAllString(value, "$1∶$2")
	return strings.NewReplacer(
		`\,`, "", "（", "(", "）", ")", "＝", "=", "＋", "+",
		"。", "", "；", "", "，", "", "：", "", "、", "", ";", "", ":", "",
	).Replace(value)
}

// CompleteRecognitionSourceReadingsEqual 只比较两份完整读数，不能用互补片段补全。
// 格式等价沿用来源证据比较；数值、运算符、作答状态与正文均须相同。
func CompleteRecognitionSourceReadingsEqual(left, right RecognizedQuestion) bool {
	left, right = normalizeRecognizedQuestionFacts(left), normalizeRecognizedQuestionFacts(right)
	if strings.TrimSpace(left.RawTranscription) == "" || strings.TrimSpace(right.RawTranscription) == "" ||
		normalizedRecognitionSourceReading(left.RawTranscription) != normalizedRecognitionSourceReading(right.RawTranscription) ||
		left.AnswerState != right.AnswerState {
		return false
	}
	if left.AnswerState == AnswerStateBlank {
		return left.StudentAnswer == "" && right.StudentAnswer == ""
	}
	return left.AnswerState == AnswerStatePresent && strings.TrimSpace(left.AnswerRawTranscription) != "" &&
		strings.TrimSpace(right.AnswerRawTranscription) != "" &&
		normalizedRecognitionSourceReading(left.AnswerRawTranscription) == normalizedRecognitionSourceReading(right.AnswerRawTranscription)
}
