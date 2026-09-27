package engineadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// 独立核验只接收源像素和目标身份，不提供已有读数或计算答案。
const recognitionLayoutAdjudicationPrompt = `Independently verify one worksheet target using the original-image context. Do not solve, grade, correct, or infer any answer. The target region and printed section/number identify the requested problem; surrounding pixels are context, not another answer to include.
Read the ENTIRE printed question and ALL active handwritten working and the final answer. Follow any handwriting that belongs to this target across the original region edge. Exclude clearly crossed-out work and handwriting belonging to neighboring problems. For a genuinely empty target answer area, return blank even when neighboring handwriting is visible. For ambiguous ownership, cancellation, cropped active strokes, or unreadable text, preserve the uncertainty; never guess.
Return JSON with exactly verification and items. verification has exactly two booleans: target_ownership_confirmed (the printed target and its own writing/empty answer area are identified unambiguously), active_answer_complete (the entire active answer is visible and legible, or the target's entire answer area is visibly blank). False on any remaining uncertainty. items uses the following compact contract. Its answer_bbox coordinates are relative to the ENTIRE context image, not the target region.
`

func buildRecognitionLayoutAdjudicationPrompt(target k12.RecognitionLayoutTargetV2, format string) (string, error) {
	if target.AdjudicationRegion == nil || target.OriginalRegion == nil {
		return "", fmt.Errorf("adjudication target has no frozen context")
	}
	contextTarget := target
	contextTarget.Region = *target.AdjudicationRegion
	compact, err := buildRecognitionLayoutBatchPromptV2([]k12.RecognitionLayoutTargetV2{contextTarget}, format)
	if err != nil {
		return "", err
	}
	compact = strings.Replace(compact, `Return compact JSON only: {"items":`, `Return compact JSON only: {"verification":{"target_ownership_confirmed":true,"active_answer_complete":true},"items":`, 1)
	focus := *target.OriginalRegion
	focus.X -= contextTarget.Region.X
	focus.Y -= contextTarget.Region.Y
	descriptor, err := json.Marshal(struct {
		Target  k12.SourcePixelRegion `json:"target_region"`
		Section string                `json:"section"`
		Label   string                `json:"display_label"`
	}{focus, target.SourceSectionLabel, target.DisplayLabel})
	if err != nil {
		return "", err
	}
	return recognitionLayoutAdjudicationPrompt + "Target identity: " + string(descriptor) + "\n\n" + compact, nil
}

// 全部来源复读已经结束后，仅冲突目标进入一次核验；串行保留有界预算，未知立即停发。
func (a *RecognizerAdapter) recognizeLayoutAdjudicationsV2(ctx context.Context, pagePNG []byte, plan k12.RecognitionLayoutPlanV2, runtime k12.RecognitionLayoutPlanRuntimeV2, primary, repaired map[string]recognitionLayoutBatchOutcomeV2) error {
	for _, target := range plan.Targets {
		current, exists := repaired[target.TargetID]
		initial, hasInitial := primary[target.TargetID]
		if !exists || !hasInitial || current.question == nil || initial.question == nil {
			continue
		}
		risk := usecase.EvaluateOCRConfirmationRisk(*current.question)
		hasConflict := false
		for _, reason := range risk.ConfirmationReasons {
			hasConflict = hasConflict || reason == usecase.OCRRiskEvidenceConflict
		}
		if !hasConflict {
			continue
		}
		if initial.source.InvocationID == "" || current.source.InvocationID == "" || initial.source.InvocationID == current.source.InvocationID {
			return fmt.Errorf("%w: adjudication requires two distinct successful readings", k12.ErrRecognitionLayoutPlanV2Unauthorized)
		}
		_, reread := classifyRecognitionLayoutRepairV2(current.source.Payload, target, plan.RecognitionFormat)
		if reread == nil || reread.question == nil {
			return fmt.Errorf("%w: adjudication repair source cannot be parsed", k12.ErrRecognitionProtocolInvalid)
		}
		questionMatch, answerMatch := usecase.RecognitionSourceReadingsMatch(*initial.question, *reread.question)
		conflictKind := "both"
		switch {
		case questionMatch && !answerMatch:
			conflictKind = "answer"
		case !questionMatch && answerMatch:
			conflictKind = "question"
		}
		if initial.question.AnswerState != reread.question.AnswerState {
			// 空白与不清晰可能都没有作答文本，按已保留的题干冲突授权，不能伪造作答观察。
			if !questionMatch {
				conflictKind = "question"
			} else {
				conflictKind = "answer_ownership"
			}
		}
		authorization, _, err := k12.AuthorizeRecognitionLayoutAdjudicationV2(ctx, k12.RecognitionLayoutAdjudicationRequestV2{
			PlanDigest: plan.AuthorizedPlanDigest, CandidateID: target.TargetID,
			PrimaryPhysicalInvocationID: initial.source.InvocationID, PrimaryPhysicalResultDigest: initial.source.ResultDigest,
			RepairPhysicalInvocationID: current.source.InvocationID, RepairPhysicalResultDigest: current.source.ResultDigest,
			ConflictKind: conflictKind,
		})
		if err != nil {
			return err
		}
		contextImage, err := k12.BuildRecognitionLayoutAdjudicationImageV2(pagePNG, plan, target.TargetID)
		if err != nil {
			return err
		}
		prompt, err := buildRecognitionLayoutAdjudicationPrompt(target, plan.RecognitionFormat)
		if err != nil {
			return err
		}
		physicalCtx, cancel, err := recognitionLayoutPhysicalCallContextV2(ctx, time.UnixMilli(runtime.StageDeadlineAtUnixMillis), runtime.Header.PhysicalCallCapMillis)
		if err != nil {
			return err
		}
		physical, err := a.callRecognitionVisionPhysical(physicalCtx, k12.RecognitionPhysicalCall{PlanVersion: k12.RecognitionPlanVersionV2, PlanDigest: plan.AuthorizedPlanDigest, Unit: authorization.PhysicalUnit, TargetIDs: []string{target.TargetID}, Image: contextImage}, prompt)
		cancel()
		if err != nil {
			return fmt.Errorf("independent source adjudication failed: %w", err)
		}
		candidate, reviewed, verified := parseRecognitionLayoutAdjudication(physical.Payload, target, plan.RecognitionFormat)
		settlement := k12.RecognitionLayoutAdjudicationSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, AuthorizationID: authorization.AuthorizationID, AuthorizationDigest: authorization.AuthorizationDigest, CandidateID: target.TargetID, SourcePhysicalInvocationID: physical.InvocationID, SourcePhysicalUnit: authorization.PhysicalUnit, SourcePhysicalResultDigest: physical.ResultDigest}
		if verified && reviewed != nil && reviewed.question != nil {
			qp, ap := usecase.RecognitionSourceReadingsMatch(*initial.question, *reviewed.question)
			qr, ar := usecase.RecognitionSourceReadingsMatch(*reread.question, *reviewed.question)
			settlement.MatchedQuestionPrior = matchedRecognitionPrior(qp, qr)
			settlement.MatchedAnswerPrior = matchedRecognitionPrior(ap, ar)
			settlement.Adopted = settlement.MatchedQuestionPrior != "" && settlement.MatchedAnswerPrior != ""
			if settlement.Adopted {
				settlement.ResultKind, settlement.ResultJSON = candidate.ResultKind, candidate.ResultJSON
			}
		}
		if !settlement.Adopted {
			settlement.MatchedQuestionPrior, settlement.MatchedAnswerPrior = "", ""
		}
		if _, _, err := k12.SettleRecognitionLayoutAdjudicationV2(ctx, physical, settlement); err != nil {
			return err
		}
	}
	return nil
}

func matchedRecognitionPrior(primary, repair bool) string {
	switch {
	case primary && repair:
		return "both"
	case primary:
		return "primary"
	case repair:
		return "repair"
	default:
		return ""
	}
}

func parseRecognitionLayoutAdjudication(raw string, target k12.RecognitionLayoutTargetV2, format string) (k12.RecognitionLayoutCandidateSettlementV2, *recognitionLayoutBatchOutcomeV2, bool) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(sanitizeModelJSON(extractJSONObject(raw))), &envelope) != nil || !recognitionLayoutExactFieldsV2(envelope, map[string]struct{}{"verification": {}, "items": {}}) {
		return k12.RecognitionLayoutCandidateSettlementV2{}, nil, false
	}
	var checks map[string]json.RawMessage
	if json.Unmarshal(envelope["verification"], &checks) != nil || !recognitionLayoutExactFieldsV2(checks, map[string]struct{}{"target_ownership_confirmed": {}, "active_answer_complete": {}}) {
		return k12.RecognitionLayoutCandidateSettlementV2{}, nil, false
	}
	ownership, complete := false, false
	if json.Unmarshal(checks["target_ownership_confirmed"], &ownership) != nil || json.Unmarshal(checks["active_answer_complete"], &complete) != nil {
		return k12.RecognitionLayoutCandidateSettlementV2{}, nil, false
	}
	contextTarget := target
	if target.AdjudicationRegion == nil {
		return k12.RecognitionLayoutCandidateSettlementV2{}, nil, false
	}
	contextTarget.Region = *target.AdjudicationRegion
	delete(envelope, "verification")
	payload, _ := json.Marshal(envelope)
	candidate, outcome := classifyRecognitionLayoutRepairV2(string(payload), contextTarget, format)
	if !ownership || !complete || outcome == nil || outcome.question == nil {
		return candidate, outcome, false
	}
	q := usecase.EvaluateOCRConfirmationRisk(*outcome.question)
	if q.RecognitionConfidence == nil || q.ConfirmationRequired || len(q.OCRSignals) != 0 || q.AnswerState == usecase.AnswerStateUnclear {
		return candidate, outcome, false
	}
	if q.AnswerState == usecase.AnswerStatePresent && q.ObservedAnswerRegion == nil {
		return candidate, outcome, false
	}
	return candidate, outcome, true
}

// ReviewCompletedSource 沿用独立归属复核协议，不发送旧作答或已算出的答案。
func (a *RecognizerAdapter) ReviewCompletedSource(ctx context.Context, image []byte, q usecase.RecognizedQuestion) (usecase.CompletedSourceReview, error) {
	out := usecase.CompletedSourceReview{}
	if q.SourceRegion == nil || q.SourceWidth <= 0 || q.SourceHeight <= 0 {
		return out, fmt.Errorf("original source region unavailable")
	}
	whole := k12.SourcePixelRegion{X: 0, Y: 0, Width: q.SourceWidth, Height: q.SourceHeight}
	target := k12.RecognitionLayoutTargetV2{TargetID: "t1", SourceNumberPath: q.SourceNumberPath, DisplayLabel: q.DisplayLabel, SourceSectionPath: q.SourceSectionPath, SourceSectionLabel: q.SourceSectionLabel, Region: whole, OriginalRegion: q.SourceRegion, AdjudicationRegion: &whole}
	prompt, err := buildRecognitionLayoutAdjudicationPrompt(target, k12.RecognitionLayoutCompactV4)
	if err != nil {
		return out, err
	}
	raw, err := a.callVision(ctx, image, prompt)
	out.Raw = raw
	if err != nil {
		return out, err
	}
	_, review, verified := parseRecognitionLayoutAdjudication(raw, target, k12.RecognitionLayoutCompactV4)
	if review == nil || review.question == nil {
		return out, nil
	}
	out.Question = *review.question
	sameQuestion, _ := usecase.RecognitionSourceReadingsMatch(q, out.Question)
	out.Verified = verified && sameQuestion
	return out, nil
}
