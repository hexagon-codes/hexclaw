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

const recognitionFinalAnswerPromptV1 = `
Independently inspect the printed question and final written answer separately from the completeness of the student's working. verification.question_complete_and_legible is true only when this target's ENTIRE original question, every condition, operand, sign and unit is visible and legible without inference; false on any missing or uncertain source. Keep verification.active_answer_complete and all existing ocr_signals truthful for the ENTIRE active working; do not clear a working ambiguity because a final value is readable.
final_answer is null unless the image independently proves one complete, legible, uncancelled final conclusion belonging to this target. When proved, return exactly {"raw_transcription":"verbatim complete final answer including units","bbox":{"x":0,"y":0,"width":1,"height":1},"is_final_answer":true,"target_ownership_confirmed":true,"not_cancelled":true,"complete_and_legible":true}. bbox uses pixels relative to this ENTIRE context image and tightly encloses that final conclusion. It is not the generic answer_bbox, which may describe working instead.
Do not extract a last number from a transcription, infer a final answer from arithmetic, use an abandoned/intermediate expression, or treat placement alone as proof. A readable intermediate value is not a final answer. Multiple competing final answers, unclear ownership, cancellation of the final conclusion, clipped symbols or unreadable final content require final_answer=null. Uncertainty solely in an earlier working step does not by itself negate independently established final-answer evidence; retain that uncertainty in ocr_signals and active_answer_complete. Do not solve or judge correctness.
`

func buildRecognitionLayoutAdjudicationPromptForPolicy(target k12.RecognitionLayoutTargetV2, format, policy string) (string, error) {
	prompt, err := buildRecognitionLayoutAdjudicationPrompt(target, format)
	if err != nil {
		return "", err
	}
	switch policy {
	case "", k12.RecognitionExecutionPolicyCompactDAGV1:
		return prompt, nil
	case k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV2:
		prompt = strings.Replace(prompt, "Return JSON with exactly verification and items.", "Return JSON with exactly verification, items and final_answer.", 1)
		prompt = strings.Replace(prompt, "verification has exactly two booleans:", "verification has exactly three booleans: question_complete_and_legible (the entire original question is visible and legible without inference),", 1)
		prompt = strings.Replace(prompt, `"verification":{"target_ownership_confirmed":`, `"verification":{"question_complete_and_legible":true,"target_ownership_confirmed":`, 1)
		prompt = strings.Replace(prompt, `"items":[`, `"final_answer":null,"items":[`, 1)
		return prompt + recognitionFinalAnswerPromptV1, nil
	default:
		return "", fmt.Errorf("%w: unsupported adjudication execution policy", k12.ErrRecognitionProtocolInvalid)
	}
}

// 历史计划仍按原顺序核验；新计划由共享调度器按目标依赖调用同一单题边界。
func (a *RecognizerAdapter) recognizeLayoutAdjudicationsV2(ctx context.Context, pagePNG []byte, plan k12.RecognitionLayoutPlanV2, runtime k12.RecognitionLayoutPlanRuntimeV2, primary, repaired map[string]recognitionLayoutBatchOutcomeV2) error {
	for _, target := range plan.Targets {
		if err := a.recognizeLayoutAdjudicationV2(ctx, pagePNG, plan, runtime, target, primary[target.TargetID], repaired[target.TargetID]); err != nil {
			return err
		}
	}
	return nil
}

func recognitionNeedsAdjudicationV2(initial, current recognitionLayoutBatchOutcomeV2, mode string, policies ...string) bool {
	if initial.question == nil || current.question == nil {
		return false
	}
	risk := usecase.EvaluateOCRConfirmationRiskForInitialReadMode(*current.question, mode)
	for _, reason := range risk.ConfirmationReasons {
		if reason == usecase.OCRRiskEvidenceConflict {
			return true
		}
	}
	if len(policies) != 0 && k12.RecognitionFinalAnswerExecutionPolicy(policies[0]) {
		return recognitionUnresolvedSourceObservationsV1(initial.question, current.question, policies[0])
	}
	return false
}

func recognitionFinalAnswerSourceV1(q *usecase.RecognizedQuestion) k12.RecognitionFinalAnswerSourceV1 {
	if q == nil {
		return k12.RecognitionFinalAnswerSourceV1{}
	}
	return k12.RecognitionFinalAnswerSourceV1{Question: q.RawTranscription, Subject: q.Subject, AnswerState: string(q.AnswerState), Confidence: q.RecognitionConfidence, OCRSignals: q.OCRSignals}
}

func recognitionUnresolvedSourceObservationsV1(initial, current *usecase.RecognizedQuestion, policies ...string) bool {
	policy := k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1
	if len(policies) != 0 {
		policy = policies[0]
	}
	return initial != nil && current != nil &&
		k12.RecognitionFinalAnswerSourcesTrustedForPolicyV1(policy, recognitionFinalAnswerSourceV1(initial), recognitionFinalAnswerSourceV1(current)) &&
		k12.RecognitionSourceObservationUnresolvedV1(current.OCRSignals)
}

func (a *RecognizerAdapter) recognizeLayoutAdjudicationV2(ctx context.Context, pagePNG []byte, plan k12.RecognitionLayoutPlanV2, runtime k12.RecognitionLayoutPlanRuntimeV2, target k12.RecognitionLayoutTargetV2, initial, current recognitionLayoutBatchOutcomeV2) error {
	if !recognitionNeedsAdjudicationV2(initial, current, plan.InitialReadMode, runtime.Header.ExecutionPolicy) {
		return nil
	}
	if initial.source.InvocationID == "" || current.source.InvocationID == "" || initial.source.InvocationID == current.source.InvocationID {
		return fmt.Errorf("%w: adjudication requires two distinct successful readings", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	_, reread := classifyRecognitionLayoutRepairV2(current.source.Payload, target, plan.RecognitionFormat)
	if reread == nil || reread.question == nil {
		return fmt.Errorf("%w: adjudication repair source cannot be parsed", k12.ErrRecognitionProtocolInvalid)
	}
	comparison := func(q usecase.RecognizedQuestion) usecase.RecognizedQuestion {
		if plan.ScopeInput != nil && plan.ScopeInput.Version == k12.RecognitionImageScopeVersionV1 && plan.ScopeInput.Action == "solve" && plan.ScopeInput.SourceComparisonPolicy == k12.RecognitionImageSourceComparisonPolicyV1 {
			q.ImageSourceContext = &k12.RecognitionImageSourceContextV1{Version: plan.ScopeInput.Version, Action: plan.ScopeInput.Action, SourceComparisonPolicy: plan.ScopeInput.SourceComparisonPolicy}
		}
		return q
	}
	questionMatch, answerMatch := usecase.RecognitionSourceReadingsMatchForInitialReadMode(comparison(*initial.question), comparison(*reread.question), plan.InitialReadMode)
	// 既定比较规则已消解排版差异时，不再把旧的泛化冲突原因当成第三读授权。
	sameAnswerText := strings.TrimSpace(initial.question.AnswerRawTranscription) == strings.TrimSpace(reread.question.AnswerRawTranscription)
	if questionMatch && (answerMatch || sameAnswerText) && !recognitionUnresolvedSourceObservationsV1(initial.question, reread.question, runtime.Header.ExecutionPolicy) {
		return nil
	}
	conflictKind := "both"
	switch {
	case questionMatch && answerMatch && k12.RecognitionFinalAnswerExecutionPolicy(runtime.Header.ExecutionPolicy) && recognitionUnresolvedSourceObservationsV1(initial.question, reread.question, runtime.Header.ExecutionPolicy):
		conflictKind = "observation_uncertainty"
		// 读数补全相容不等于完整作答相同；增加的歧义过程仍走作答冲突裁决。
		if !k12.RecognitionSourceQuestionsEqualV1(initial.question.AnswerRawTranscription, reread.question.AnswerRawTranscription) {
			conflictKind = "answer"
		}
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
	viewTarget, err := k12.RecognitionLayoutAdjudicationViewTargetV1(plan, target)
	if err != nil {
		return err
	}
	prompt, err := buildRecognitionLayoutAdjudicationPromptForPolicy(viewTarget, plan.RecognitionFormat, runtime.Header.ExecutionPolicy)
	if err != nil {
		return err
	}
	if plan.ScopeInput != nil && plan.ScopeInput.SourceReviewPolicy == k12.RecognitionImageSourceUprightReviewPolicyV1 {
		prompt += "\nTranscribe printed text verbatim, character by character, including short words, modifiers and full proper names. Do not abbreviate, paraphrase, substitute familiar textbook wording, or complete clipped text from memory. Preserve visible wording even when another edition differs. Report uncertain or missing characters through the existing uncertainty fields rather than silently omitting them.\n"
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
	sourcePayload, err := k12.RecognitionLayoutAdjudicationSourceResponseV1(physical.Payload, plan, target)
	if err != nil {
		return err
	}
	candidate, reviewed, verified, finalAnswer, err := parseRecognitionLayoutAdjudicationForPolicy(sourcePayload, target, plan.RecognitionFormat, runtime.Header.ExecutionPolicy)
	if err != nil {
		return err
	}
	settlement := k12.RecognitionLayoutAdjudicationSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, AuthorizationID: authorization.AuthorizationID, AuthorizationDigest: authorization.AuthorizationDigest, CandidateID: target.TargetID, SourcePhysicalInvocationID: physical.InvocationID, SourcePhysicalUnit: authorization.PhysicalUnit, SourcePhysicalResultDigest: physical.ResultDigest}
	if initial.question.Subject == "语文" && reread.question.Subject == "语文" && plan.ScopeInput != nil && plan.ScopeInput.SourceReviewPolicy == k12.RecognitionImageSourceUprightReviewPolicyV1 && k12.RecognitionFinalAnswerExecutionPolicy(runtime.Header.ExecutionPolicy) {
		settlement.PrintedQuestionEvidence, err = k12.RecognitionPrintedQuestionEvidenceFromResponseV1(physical.Payload, plan, target, []string{initial.question.RawTranscription, reread.question.RawTranscription})
		if err != nil {
			return err
		}
	}
	if finalAnswer != nil && (conflictKind == "answer" || conflictKind == "observation_uncertainty") && recognitionFinalAnswerSourcesEligibleV1(initial.question, reread.question, reviewed, plan.InitialReadMode, runtime.Header.ExecutionPolicy) {
		settlement.FinalAnswerEvidence = finalAnswer
	}
	if verified && reviewed != nil && reviewed.question != nil {
		qp, ap := usecase.RecognitionSourceReadingsMatchForInitialReadMode(comparison(*initial.question), comparison(*reviewed.question), plan.InitialReadMode)
		qr, ar := usecase.RecognitionSourceReadingsMatchForInitialReadMode(comparison(*reread.question), comparison(*reviewed.question), plan.InitialReadMode)
		settlement.MatchedQuestionPrior = matchedRecognitionPrior(qp, qr)
		settlement.MatchedAnswerPrior = matchedRecognitionPrior(ap, ar)
		settlement.Adopted = settlement.MatchedQuestionPrior != "" && settlement.MatchedAnswerPrior != ""
		if settlement.Adopted {
			settlement.ResultKind, settlement.ResultJSON = candidate.ResultKind, candidate.ResultJSON
		}
	}
	if !settlement.Adopted {
		settlement.MatchedQuestionPrior, settlement.MatchedAnswerPrior = "", ""
	} else {
		settlement.PrintedQuestionEvidence = nil
	}
	if _, _, err := k12.SettleRecognitionLayoutAdjudicationV2(ctx, physical, settlement); err != nil {
		return err
	}
	return nil
}

func parseRecognitionLayoutAdjudicationForPolicy(raw string, target k12.RecognitionLayoutTargetV2, format, policy string) (k12.RecognitionLayoutCandidateSettlementV2, *recognitionLayoutBatchOutcomeV2, bool, *k12.RecognitionFinalAnswerEvidenceV1, error) {
	if policy == "" || policy == k12.RecognitionExecutionPolicyCompactDAGV1 {
		candidate, reviewed, verified := parseRecognitionLayoutAdjudication(raw, target, format)
		return candidate, reviewed, verified, nil, nil
	}
	if !k12.RecognitionFinalAnswerExecutionPolicy(policy) || target.AdjudicationRegion == nil {
		return k12.RecognitionLayoutCandidateSettlementV2{}, nil, false, nil, fmt.Errorf("%w: unsupported adjudication execution policy", k12.ErrRecognitionProtocolInvalid)
	}
	response, err := k12.ParseRecognitionFinalAnswerResponseV1(raw, *target.AdjudicationRegion)
	if err != nil {
		return k12.RecognitionLayoutCandidateSettlementV2{}, nil, false, nil, err
	}
	candidate, reviewed, verified := parseRecognitionLayoutAdjudication(string(response.LegacyEnvelope), target, format)
	if !response.QuestionCompleteAndLegible || !response.TargetOwnershipConfirmed || reviewed == nil || reviewed.question == nil {
		return candidate, reviewed, false, nil, nil
	}
	return candidate, reviewed, verified, response.Evidence, nil
}

// 新增证据只处理两份题干已一致的作答冲突，不能替代原题来源、学科或清晰度证明。
func recognitionFinalAnswerSourcesEligibleV1(initial, reread *usecase.RecognizedQuestion, reviewed *recognitionLayoutBatchOutcomeV2, initialReadMode string, policies ...string) bool {
	policy := k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1
	if len(policies) != 0 {
		policy = policies[0]
	}
	if initial == nil || reread == nil || reviewed == nil || reviewed.question == nil ||
		!k12.RecognitionFinalAnswerSourcesTrustedForPolicyV1(policy, recognitionFinalAnswerSourceV1(initial), recognitionFinalAnswerSourceV1(reread), recognitionFinalAnswerSourceV1(reviewed.question)) {
		return false
	}
	for index, source := range []*usecase.RecognizedQuestion{initial, reread, reviewed.question} {
		if source.RecognitionConfidence == nil || source.Subject != "数学" || source.AnswerState != usecase.AnswerStatePresent {
			return false
		}
		question := usecase.EvaluateOCRConfirmationRiskForInitialReadMode(*source, initialReadMode)
		for _, reason := range question.ConfirmationReasons {
			if index == 0 && policy == k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV2 && reason == usecase.OCRRiskLowConfidence {
				continue
			}
			if reason != usecase.OCRRiskEvidenceConflict {
				return false
			}
		}
	}
	return true
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
