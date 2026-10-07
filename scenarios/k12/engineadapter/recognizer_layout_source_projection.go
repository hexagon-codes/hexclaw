package engineadapter

import (
	"slices"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// 仅在不可变终结证据已核验后派生题目；不修改计划、候选、裁决或物理回执。
func projectCompletePrintedQuestionsV2(questions []usecase.RecognizedQuestion, targetIndexes []int, plan k12.RecognitionLayoutPlanV2, finalization k12.RecognitionLayoutPlanFinalizationResultV2) []usecase.RecognizedQuestion {
	if plan.InitialReadMode != k12.RecognitionLayoutManifestWithContentV1 || !plan.SourceAdjudication ||
		!recognitionLayoutSHA256DigestV2.MatchString(plan.PageDigest) ||
		!recognitionLayoutSHA256DigestV2.MatchString(finalization.FinalizationDigest) {
		return questions
	}
	consumed := make([]bool, len(questions))
	out := make([]usecase.RecognizedQuestion, 0, len(questions))
	for i, question := range questions {
		if consumed[i] {
			continue
		}
		leftIndex := targetIndexes[i]
		for j := i + 1; j < len(questions); j++ {
			if consumed[j] {
				continue
			}
			rightIndex := targetIndexes[j]
			left, right := plan.Targets[leftIndex], plan.Targets[rightIndex]
			if !sameCompletePrintedQuestionV2(question, questions[j], left, right, finalization.CandidateResults[leftIndex], finalization.CandidateResults[rightIndex]) {
				continue
			}
			// 使用包含完整原题区域的现有完整读数，正文与作答逐字保留。
			if right.OriginalRegion.Width >= left.OriginalRegion.Width && right.OriginalRegion.Height >= left.OriginalRegion.Height {
				question = questions[j]
			}
			question.LayoutSourceProjection = usecase.RecognitionLayoutSourceProjection{PageDigest: plan.PageDigest, PlanID: finalization.PlanID, PlanDigest: finalization.PlanDigest, FinalizationDigest: finalization.FinalizationDigest, TargetIDs: [2]string{left.TargetID, right.TargetID}}
			consumed[j] = true
			// 两个来源之外的重复仍由原协议校验拒绝，不能按题号继续去重。
			break
		}
		out = append(out, question)
	}
	return out
}

func sameCompletePrintedQuestionV2(leftQuestion, rightQuestion usecase.RecognizedQuestion, left, right k12.RecognitionLayoutTargetV2, leftResult, rightResult k12.RecognitionLayoutCandidateFinalResultV2) bool {
	if len(left.SourceNumberPath) == 0 || strings.TrimSpace(left.DisplayLabel) == "" ||
		!slices.Equal(left.SourceNumberPath, right.SourceNumberPath) || left.DisplayLabel != right.DisplayLabel ||
		!slices.Equal(left.SourceSectionPath, right.SourceSectionPath) || left.SourceSectionLabel != right.SourceSectionLabel ||
		left.OriginalRegion == nil || right.OriginalRegion == nil ||
		left.OriginalRegion.X != right.OriginalRegion.X || left.OriginalRegion.Y != right.OriginalRegion.Y {
		return false
	}
	a, b := *left.OriginalRegion, *right.OriginalRegion
	if !((a.Width <= b.Width && a.Height <= b.Height) || (b.Width <= a.Width && b.Height <= a.Height)) {
		return false
	}
	if !completeAdjudicationProjectionV2(leftQuestion, leftResult) || !completeAdjudicationProjectionV2(rightQuestion, rightResult) ||
		leftQuestion.Subject != rightQuestion.Subject {
		return false
	}
	return usecase.CompleteRecognitionSourceReadingsEqual(leftQuestion, rightQuestion)
}

func completeAdjudicationProjectionV2(question usecase.RecognizedQuestion, result k12.RecognitionLayoutCandidateFinalResultV2) bool {
	// 采纳回执仅由 ownership/complete 均通过且匹配原有读数的裁决产生。
	priorValid := func(value string) bool { return value == "primary" || value == "repair" || value == "both" }
	return result.Adjudication != nil && len(result.OriginalCandidateJSON) > 0 &&
		result.Adjudication.AuthorizationID != "" && recognitionLayoutSHA256DigestV2.MatchString(result.Adjudication.SettlementDigest) &&
		priorValid(result.Adjudication.MatchedQuestionPrior) && priorValid(result.Adjudication.MatchedAnswerPrior) &&
		question.RecognitionConfidence != nil && !question.ConfirmationRequired && len(question.OCRSignals) == 0 &&
		(question.AnswerState == usecase.AnswerStateBlank || (question.AnswerState == usecase.AnswerStatePresent && question.ObservedAnswerRegion != nil))
}
