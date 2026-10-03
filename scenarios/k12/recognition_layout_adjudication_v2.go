package k12

import (
	"context"
	"encoding/json"
	"fmt"
)

// RecognitionLayoutAdjudicationRequestV2 绑定存在冲突的两份成功来源，不能替代物理请求身份。
type RecognitionLayoutAdjudicationRequestV2 struct {
	PlanDigest                  string `json:"plan_digest"`
	CandidateID                 string `json:"candidate_id"`
	PrimaryPhysicalInvocationID string `json:"primary_physical_invocation_id"`
	PrimaryPhysicalResultDigest string `json:"primary_physical_result_digest"`
	RepairPhysicalInvocationID  string `json:"repair_physical_invocation_id"`
	RepairPhysicalResultDigest  string `json:"repair_physical_result_digest"`
	ConflictKind                string `json:"conflict_kind"`
}

type RecognitionLayoutAdjudicationAuthorizationV2 struct {
	AuthorizationID     string                  `json:"authorization_id"`
	AuthorizationDigest string                  `json:"authorization_digest"`
	CandidateID         string                  `json:"candidate_id"`
	PhysicalUnit        RecognitionPhysicalUnit `json:"physical_unit"`
}

// RecognitionLayoutAdjudicationSettlementV2 记录独立读取后的采纳结论；不改首读或复核结果。
type RecognitionLayoutAdjudicationSettlementV2 struct {
	PlanDigest                 string                                 `json:"plan_digest"`
	AuthorizationID            string                                 `json:"authorization_id"`
	AuthorizationDigest        string                                 `json:"authorization_digest"`
	CandidateID                string                                 `json:"candidate_id"`
	SourcePhysicalInvocationID string                                 `json:"source_physical_invocation_id"`
	SourcePhysicalUnit         RecognitionPhysicalUnit                `json:"source_physical_unit"`
	SourcePhysicalResultDigest string                                 `json:"source_physical_result_digest"`
	Adopted                    bool                                   `json:"adopted"`
	MatchedQuestionPrior       string                                 `json:"matched_question_prior,omitempty"`
	MatchedAnswerPrior         string                                 `json:"matched_answer_prior,omitempty"`
	ResultKind                 RecognitionLayoutCandidateResultKindV2 `json:"result_kind,omitempty"`
	ResultJSON                 json.RawMessage                        `json:"result_json,omitempty"`
}

type RecognitionLayoutAdjudicationSettlementResultV2 struct {
	SettlementDigest string                                     `json:"settlement_digest"`
	Adopted          bool                                       `json:"adopted"`
	FrozenResult     *RecognitionLayoutCandidateResultReceiptV2 `json:"frozen_result,omitempty"`
}

type RecognitionLayoutAdjudicationReceiptV2 struct {
	AuthorizationID      string `json:"authorization_id"`
	SettlementDigest     string `json:"settlement_digest"`
	MatchedQuestionPrior string `json:"matched_question_prior"`
	MatchedAnswerPrior   string `json:"matched_answer_prior"`
}

type RecognitionLayoutAdjudicationAuthorizerV2 interface {
	AuthorizeRecognitionLayoutAdjudicationV2(context.Context, RecognitionLayoutAdjudicationRequestV2) (RecognitionLayoutAdjudicationAuthorizationV2, bool, error)
}

type RecognitionLayoutAdjudicationSettlerV2 interface {
	SettleRecognitionLayoutAdjudicationV2(context.Context, RecognitionPhysicalCallResult, RecognitionLayoutAdjudicationSettlementV2) (RecognitionLayoutAdjudicationSettlementResultV2, bool, error)
}

func AuthorizeRecognitionLayoutAdjudicationV2(ctx context.Context, request RecognitionLayoutAdjudicationRequestV2) (RecognitionLayoutAdjudicationAuthorizationV2, bool, error) {
	var zero RecognitionLayoutAdjudicationAuthorizationV2
	runtime, err := LoadRecognitionLayoutPlanV2Runtime(ctx)
	if err != nil {
		return zero, false, err
	}
	if ctx == nil || runtime.AuthorizedPlan == nil || !runtime.AuthorizedPlan.SourceAdjudication || runtime.AuthorizedPlan.AuthorizedPlanDigest != request.PlanDigest {
		return zero, false, ErrRecognitionLayoutPlanV2Unauthorized
	}
	authorizer, ok := ctx.Value(recognitionPhysicalCallContextKey{}).(RecognitionLayoutAdjudicationAuthorizerV2)
	if !ok {
		return zero, false, fmt.Errorf("%w: executor lacks adjudication authorization", ErrRecognitionLayoutPlanV2Unauthorized)
	}
	auth, created, err := authorizer.AuthorizeRecognitionLayoutAdjudicationV2(ctx, request)
	if err != nil {
		return zero, false, err
	}
	for i, target := range runtime.AuthorizedPlan.Targets {
		if target.TargetID == request.CandidateID {
			unit, _ := RecognitionLayoutAdjudicationUnitV2(i + 1)
			if auth.CandidateID == target.TargetID && auth.PhysicalUnit == unit && auth.AuthorizationID != "" && validRecognitionLayoutSHA256(auth.AuthorizationDigest) {
				return auth, created, nil
			}
		}
	}
	return zero, false, ErrRecognitionLayoutPlanV2Unauthorized
}

func SettleRecognitionLayoutAdjudicationV2(ctx context.Context, source RecognitionPhysicalCallResult, settlement RecognitionLayoutAdjudicationSettlementV2) (RecognitionLayoutAdjudicationSettlementResultV2, bool, error) {
	var zero RecognitionLayoutAdjudicationSettlementResultV2
	if ctx == nil || source.InvocationID == "" || source.InvocationID != settlement.SourcePhysicalInvocationID || source.ResultDigest != settlement.SourcePhysicalResultDigest || !settlement.SourcePhysicalUnit.validLayoutOrdinal("layout_adjudicate_") {
		return zero, false, ErrRecognitionLayoutPlanV2Unauthorized
	}
	settler, ok := ctx.Value(recognitionPhysicalCallContextKey{}).(RecognitionLayoutAdjudicationSettlerV2)
	if !ok {
		return zero, false, fmt.Errorf("%w: executor lacks adjudication settlement", ErrRecognitionLayoutPlanV2Unauthorized)
	}
	result, created, err := settler.SettleRecognitionLayoutAdjudicationV2(ctx, source, settlement)
	if err != nil {
		return zero, false, err
	}
	if !validRecognitionLayoutSHA256(result.SettlementDigest) || result.Adopted != settlement.Adopted || (result.Adopted && (result.FrozenResult == nil || result.FrozenResult.CandidateID != settlement.CandidateID)) {
		return zero, false, ErrRecognitionLayoutPlanV2Unauthorized
	}
	return result, created, nil
}
