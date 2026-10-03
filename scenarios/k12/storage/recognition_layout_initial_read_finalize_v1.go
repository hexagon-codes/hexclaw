package k12storage

import (
	"context"
	"fmt"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 合并首读仅清点实际发生的整页、独立复核及裁决，不要求未发送的主批次。
func reconstructRecognitionLayoutInitialReadFinalizationV1(ctx context.Context, q dbQueryer, authority recognitionLayoutFinalizationAuthorityV2) (k12.RecognitionLayoutPlanFinalizationResultV2, []byte, error) {
	empty := k12.RecognitionLayoutPlanFinalizationResultV2{}
	plan, parent := authority.Plan, authority.Parent
	initial, found, err := loadInitialReadV1(ctx, q, parent.AgentName, parent.InvocationID)
	if err != nil {
		return empty, nil, err
	}
	if !found || initial.Result.Classification != k12.RecognitionLayoutBatchClassifiedV2 {
		return empty, nil, fmt.Errorf("%w: initial read is not fully classified", records.ErrIllegalTransition)
	}
	if err = validateInitialReadSourceV1(ctx, q, parent.AgentName, parent.InvocationID, plan, initial.Settlement); err != nil {
		return empty, nil, err
	}
	manifest, err := loadRecognitionLayoutFinalPhysicalEvidenceV2(ctx, q, parent, plan.ManifestInvocationID, k12.RecognitionPhysicalUnitWholePage, authority.HeaderDigest, "", plan.ManifestResultDigest)
	if err != nil {
		return empty, nil, err
	}
	physical := []k12.RecognitionLayoutPhysicalResultEvidenceV2{manifest}
	physicalByID := map[string]k12.RecognitionLayoutPhysicalResultEvidenceV2{manifest.PhysicalInvocationID: manifest}
	expected := map[string]string{}
	for _, receipt := range initial.Result.FrozenResults {
		expected[receipt.CandidateID] = manifest.PhysicalInvocationID
	}
	reviews, err := loadReviewBatchesV1(ctx, q, authority.PlanID, parent.InvocationID)
	if err != nil {
		return empty, nil, err
	}
	for _, review := range reviews {
		settlement, exists, e := loadReviewSettlementV1(ctx, q, authority.PlanID, parent.InvocationID, review)
		if e != nil {
			return empty, nil, e
		}
		if !exists || settlement.Result.Classification != k12.RecognitionLayoutBatchClassifiedV2 || len(settlement.Result.UnresolvedCandidateIDs) != 0 {
			return empty, nil, fmt.Errorf("%w: review batch is not fully settled", records.ErrIllegalTransition)
		}
		source, e := loadRecognitionLayoutFinalPhysicalEvidenceV2(ctx, q, parent, settlement.Settlement.SourcePhysicalInvocationID, review.PhysicalUnit, review.PlanDigest, review.ExactSetDigest, settlement.Settlement.SourcePhysicalResultDigest)
		if e != nil {
			return empty, nil, e
		}
		if _, duplicate := physicalByID[source.PhysicalInvocationID]; duplicate {
			return empty, nil, ErrModelPhysicalInvocationConflict
		}
		physical = append(physical, source)
		physicalByID[source.PhysicalInvocationID] = source
		for _, receipt := range settlement.Result.FrozenResults {
			if expected[receipt.CandidateID] != "" {
				return empty, nil, ErrModelPhysicalInvocationConflict
			}
			expected[receipt.CandidateID] = source.PhysicalInvocationID
		}
	}
	overlays, adjudications, err := loadRecognitionAdjudicationOverlays(ctx, q, authority, true)
	if err != nil {
		return empty, nil, err
	}
	for _, source := range adjudications {
		if _, duplicate := physicalByID[source.PhysicalInvocationID]; duplicate {
			return empty, nil, ErrModelPhysicalInvocationConflict
		}
		physical = append(physical, source)
		physicalByID[source.PhysicalInvocationID] = source
	}
	var physicalCount, settlementCount, candidateCount int
	if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_model_physical_invocations WHERE parent_invocation_id=? AND recognition_plan_version='v2'`, parent.InvocationID).Scan(&physicalCount); err != nil {
		return empty, nil, err
	}
	if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_recognition_layout_batch_settlements WHERE plan_id=?`, authority.PlanID).Scan(&settlementCount); err != nil {
		return empty, nil, err
	}
	if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_recognition_layout_candidate_results WHERE plan_id=?`, authority.PlanID).Scan(&candidateCount); err != nil {
		return empty, nil, err
	}
	if physicalCount != len(physical) || settlementCount != len(reviews) || candidateCount != len(plan.Targets) || len(expected) != len(plan.Targets) {
		return empty, nil, fmt.Errorf("%w: initial read final exact-set is incomplete", records.ErrIllegalTransition)
	}
	candidates := make([]k12.RecognitionLayoutCandidateFinalResultV2, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		result, exists, e := loadRecognitionLayoutFinalCandidateResultV2(ctx, q, authority.PlanID, target.TargetID)
		if e != nil {
			return empty, nil, e
		}
		source, sourceExists := physicalByID[result.SourcePhysicalInvocationID]
		if !exists || !sourceExists || expected[target.TargetID] != result.SourcePhysicalInvocationID || source.PhysicalUnit != result.SourcePhysicalUnit || source.ResultDigest != result.SourcePhysicalResultDigest {
			return empty, nil, ErrModelPhysicalInvocationConflict
		}
		digest, e := recognitionLayoutCandidateResultDigestV2(parent.InvocationID, k12.RecognitionLayoutPrimaryBatchSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, SourcePhysicalInvocationID: source.PhysicalInvocationID, SourcePhysicalUnit: source.PhysicalUnit, SourcePhysicalResultDigest: source.ResultDigest}, k12.RecognitionLayoutCandidateSettlementV2{CandidateID: target.TargetID, ResultKind: result.ResultKind, ResultJSON: result.ResultJSON})
		if e != nil || digest != result.ResultDigest {
			return empty, nil, ErrModelPhysicalInvocationConflict
		}
		candidates = append(candidates, applyRecognitionAdjudicationOverlay(result, overlays[target.TargetID]))
	}
	candidateDigest, err := k12.RecognitionLayoutCandidateResultsExactSetDigestV2(candidates)
	if err != nil {
		return empty, nil, err
	}
	physicalDigest, err := k12.RecognitionLayoutPhysicalResultsExactSetDigestV2(physical)
	if err != nil {
		return empty, nil, err
	}
	out := k12.RecognitionLayoutPlanFinalizationResultV2{PlanID: authority.PlanID, PlanDigest: plan.AuthorizedPlanDigest, CandidateExactSetDigest: authority.CandidateExactSetDigest, CandidateResultsExactSetDigest: candidateDigest, PhysicalResultsExactSetDigest: physicalDigest, CandidateResultCount: len(candidates), PhysicalResultCount: len(physical), CandidateResults: candidates, PhysicalResults: physical}
	raw, digest, err := k12.CanonicalRecognitionLayoutPlanFinalizationV2(parent.InvocationID, out)
	out.FinalizationDigest = digest
	return out, raw, err
}

// 裁决继续沿既有授权表；首读来源是整页，独立来源是该题所属的真实复核批。
func loadInitialReadAdjudicationSourcesV1(ctx context.Context, q dbQueryer, owner, parent, planID, candidate string) (primaryID, primaryDigest, reviewID, reviewDigest, originalJSON, originalDigest string, err error) {
	initial, found, e := loadInitialReadV1(ctx, q, owner, parent)
	if e != nil {
		err = e
		return
	}
	if !found {
		err = ErrModelPhysicalInvocationConflict
		return
	}
	eligible := false
	for _, member := range initial.Result.ReviewAuthorizations {
		if member.CandidateID == candidate {
			eligible = true
			break
		}
	}
	if !eligible {
		err = ErrModelPhysicalInvocationConflict
		return
	}
	primaryID, primaryDigest = initial.Settlement.SourcePhysicalInvocationID, initial.Settlement.SourcePhysicalResultDigest
	reviews, e := loadReviewBatchesV1(ctx, q, planID, parent)
	if e != nil {
		err = e
		return
	}
	for _, review := range reviews {
		for _, id := range review.OrderedTargetIDs {
			if id != candidate {
				continue
			}
			settlement, exists, e := loadReviewSettlementV1(ctx, q, planID, parent, review)
			if e != nil {
				err = e
				return
			}
			if !exists {
				err = ErrModelPhysicalInvocationConflict
				return
			}
			receipt := nextRecognitionLayoutCandidateReceiptV2(settlement.Result.FrozenResults, candidate)
			if receipt.CandidateID == "" {
				err = ErrModelPhysicalInvocationConflict
				return
			}
			reviewID, reviewDigest = settlement.Settlement.SourcePhysicalInvocationID, settlement.Settlement.SourcePhysicalResultDigest
			result, exists, e := loadRecognitionLayoutFinalCandidateResultV2(ctx, q, planID, candidate)
			if e != nil {
				err = e
				return
			}
			if !exists || result.SourcePhysicalInvocationID != reviewID || result.ResultDigest != receipt.ResultDigest {
				err = ErrModelPhysicalInvocationConflict
				return
			}
			originalJSON, originalDigest = string(result.ResultJSON), result.ResultDigest
			return
		}
	}
	err = ErrModelPhysicalInvocationConflict
	return
}
