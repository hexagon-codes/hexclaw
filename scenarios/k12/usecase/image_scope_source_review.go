package usecase

import "github.com/hexagon-codes/hexclaw/scenarios/k12"

// 仅新策略中的清晰语言解题可复用首读；数学、批改和任何题干来源风险沿用独立复核。
func ImageScopeCanUseInitialLanguageRead(q RecognizedQuestion, input *k12.RecognitionImageScopeInputV1, scope *k12.RecognitionImageScopeV1, order int, mode string) bool {
	if input == nil || scope == nil || scope.Version != k12.RecognitionImageScopeVersionV1 || input.Version != k12.RecognitionImageScopeVersionV1 || input.SourceReviewPolicy != k12.RecognitionImageSourceReviewPolicyV1 || input.Action != "solve" || (q.Subject != "语文" && q.Subject != "英语") || q.RecognitionConfidence == nil {
		return false
	}
	selected := false
	for _, candidateOrder := range scope.SelectedOrders {
		selected = selected || candidateOrder == order
	}
	if !selected {
		return false
	}
	q = cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
	q.ImageSourceContext = &k12.RecognitionImageSourceContextV1{Version: input.Version, Action: input.Action}
	for _, gap := range scope.Gaps {
		if gap.Order == order {
			q.ImageSourceContext.Recoveries = append(q.ImageSourceContext.Recoveries, k12.RecognitionImageSourceRecoveryV1{Gap: gap, State: "unresolved"})
		}
	}
	return !recognizedQuestionRequiresGuardianConfirmationForInitialReadMode(q, PhotoTaskBlankWorksheet, mode)
}
