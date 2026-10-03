package k12

import (
	"errors"
	"fmt"
)

// 联合首读的最终化只核对实际发送的全页、独立复核与裁决，不要求不存在的旧首读批。
func validateRecognitionLayoutInitialReadFinalizationV1(runtime RecognitionLayoutPlanRuntimeV2, result RecognitionLayoutPlanFinalizationResultV2) error {
	plan := runtime.AuthorizedPlan
	if plan == nil || plan.InitialReadMode != RecognitionLayoutManifestWithContentV1 || len(plan.Batches) != 0 {
		return errors.New("initial-read finalization plan is invalid")
	}
	targetOrder := make(map[string]int, len(plan.Targets))
	for index, target := range plan.Targets {
		targetOrder[target.TargetID] = index
	}
	expected := []recognitionLayoutExpectedPhysicalResultV2{{unit: RecognitionPhysicalUnitWholePage, planDigest: runtime.HeaderDigest}}
	reviewSources := make(map[string]RecognitionPhysicalUnit)
	for index, batch := range runtime.ReviewBatches {
		unit, err := RecognitionLayoutReviewUnitV1(index + 1)
		digest, inputErr := RecognitionLayoutReviewBatchInputDigestV1(batch.RecognitionLayoutReviewBatchAuthorizationRequestV1)
		exact, exactErr := RecognitionLayoutTargetExactSetDigestV2(batch.OrderedTargetIDs)
		if err != nil || inputErr != nil || exactErr != nil || batch.PhysicalUnit != unit || batch.PlanDigest != plan.AuthorizedPlanDigest || batch.InputDigest != digest || batch.ExactSetDigest != exact || batch.ReviewRound != 1 || len(batch.Members) != len(batch.OrderedTargetIDs) || !validRecognitionLayoutSHA256(batch.AuthorizationDigest) {
			return errors.New("initial-read review authorization drifted")
		}
		lastOrder := -1
		for memberIndex, targetID := range batch.OrderedTargetIDs {
			order, exists := targetOrder[targetID]
			member := batch.Members[memberIndex]
			if !exists || order <= lastOrder || member.CandidateID != targetID || member.ReviewRound != 1 || !validRecognitionLayoutSHA256(member.AuthorizationDigest) {
				return errors.New("initial-read review member order drifted")
			}
			if _, duplicate := reviewSources[targetID]; duplicate {
				return errors.New("initial-read target belongs to multiple reviews")
			}
			lastOrder = order
			reviewSources[targetID] = unit
		}
		expected = append(expected, recognitionLayoutExpectedPhysicalResultV2{unit: unit, planDigest: plan.AuthorizedPlanDigest, exactSetDigest: exact})
	}
	actualByUnit := make(map[RecognitionPhysicalUnit]RecognitionLayoutPhysicalResultEvidenceV2)
	for _, evidence := range result.PhysicalResults {
		if _, duplicate := actualByUnit[evidence.PhysicalUnit]; duplicate {
			return errors.New("initial-read physical unit is duplicated")
		}
		actualByUnit[evidence.PhysicalUnit] = evidence
	}
	for index, target := range plan.Targets {
		unit, _ := RecognitionLayoutRepairUnitV2(index + 1)
		if _, exists := actualByUnit[unit]; exists {
			exact, err := RecognitionLayoutTargetExactSetDigestV2([]string{target.TargetID})
			if err != nil {
				return err
			}
			expected = append(expected, recognitionLayoutExpectedPhysicalResultV2{unit: unit, planDigest: plan.AuthorizedPlanDigest, exactSetDigest: exact})
		}
	}
	for index, target := range plan.Targets {
		unit, _ := RecognitionLayoutAdjudicationUnitV2(index + 1)
		if _, exists := actualByUnit[unit]; exists {
			repair, _ := RecognitionLayoutRepairUnitV2(index + 1)
			_, repaired := actualByUnit[repair]
			if !plan.SourceAdjudication || (reviewSources[target.TargetID] == "" && !repaired) {
				return errors.New("initial-read adjudication lacks independent review")
			}
			exact, err := RecognitionLayoutTargetExactSetDigestV2([]string{target.TargetID})
			if err != nil {
				return err
			}
			expected = append(expected, recognitionLayoutExpectedPhysicalResultV2{unit: unit, planDigest: plan.AuthorizedPlanDigest, exactSetDigest: exact})
		}
	}
	if len(result.PhysicalResults) != len(expected) || result.PhysicalResultCount != len(expected) {
		return errors.New("initial-read physical result cardinality drifted")
	}
	byID := make(map[string]RecognitionLayoutPhysicalResultEvidenceV2, len(expected))
	for index, evidence := range result.PhysicalResults {
		want := expected[index]
		if !validRecognitionPhysicalInvocationIDV2(evidence.PhysicalInvocationID) || evidence.PhysicalUnit != want.unit || evidence.PlanDigest != want.planDigest || evidence.CandidateExactSetDigest != want.exactSetDigest || !validRecognitionLayoutSHA256(evidence.ResultDigest) || evidence.Attempt != 1 {
			return fmt.Errorf("initial-read physical result %d drifted", index+1)
		}
		if index == 0 && (evidence.PhysicalInvocationID != runtime.ManifestPhysicalInvocationID || evidence.ResultDigest != runtime.ManifestResultDigest) {
			return errors.New("initial-read whole-page evidence drifted")
		}
		if _, duplicate := byID[evidence.PhysicalInvocationID]; duplicate {
			return errors.New("initial-read physical invocation is duplicated")
		}
		byID[evidence.PhysicalInvocationID] = evidence
	}
	for index, candidate := range result.CandidateResults {
		target := plan.Targets[index]
		repair, _ := RecognitionLayoutRepairUnitV2(index + 1)
		adjudication, _ := RecognitionLayoutAdjudicationUnitV2(index + 1)
		if candidate.CandidateID != target.TargetID || (candidate.ResultKind != RecognitionLayoutCandidateQuestionV2 && candidate.ResultKind != RecognitionLayoutCandidateNonQuestionV2) || !validRecognitionLayoutSHA256(candidate.ResultDigest) {
			return fmt.Errorf("initial-read candidate %d drifted", index+1)
		}
		source := candidate.SourcePhysicalUnit
		if source == RecognitionPhysicalUnitWholePage && reviewSources[target.TargetID] != "" {
			return errors.New("initial-read candidate skipped its independent review")
		}
		if source != RecognitionPhysicalUnitWholePage && source != reviewSources[target.TargetID] && source != repair && source != adjudication {
			return errors.New("initial-read candidate source is unauthorized")
		}
		if source == adjudication && (!plan.SourceAdjudication || candidate.Adjudication == nil || len(candidate.OriginalCandidateJSON) == 0) {
			return errors.New("initial-read candidate adjudication is incomplete")
		}
		evidence, exists := byID[candidate.SourcePhysicalInvocationID]
		if !exists || evidence.PhysicalUnit != source || evidence.ResultDigest != candidate.SourcePhysicalResultDigest {
			return errors.New("initial-read candidate physical source drifted")
		}
	}
	candidateDigest, err := RecognitionLayoutCandidateResultsExactSetDigestV2(result.CandidateResults)
	if err != nil || candidateDigest != result.CandidateResultsExactSetDigest {
		return errors.New("initial-read candidate aggregate digest drifted")
	}
	physicalDigest, err := RecognitionLayoutPhysicalResultsExactSetDigestV2(result.PhysicalResults)
	if err != nil || physicalDigest != result.PhysicalResultsExactSetDigest {
		return errors.New("initial-read physical aggregate digest drifted")
	}
	_, finalDigest, err := CanonicalRecognitionLayoutPlanFinalizationV2(runtime.Header.ParentInvocationID, result)
	if err != nil || finalDigest != result.FinalizationDigest {
		return errors.New("initial-read finalization digest drifted")
	}
	return nil
}
