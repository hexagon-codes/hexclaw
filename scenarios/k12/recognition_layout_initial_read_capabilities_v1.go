package k12

import (
	"context"
	"fmt"
)

// 新首读能力将真实全页回执与计划、正文在同一事务内提交。
type RecognitionLayoutInitialReadSettlerV1 interface {
	AuthorizeAndSettleRecognitionLayoutInitialReadV1(context.Context, RecognitionPhysicalCallResult, RecognitionLayoutPlanV2, RecognitionLayoutInitialReadSettlementV1) (RecognitionLayoutInitialReadSettlementResultV1, bool, error)
}

type RecognitionLayoutReviewBatchAuthorizerV1 interface {
	AuthorizeRecognitionLayoutReviewBatchV1(context.Context, RecognitionLayoutReviewBatchAuthorizationRequestV1) (RecognitionLayoutReviewBatchAuthorizationV1, bool, error)
}

type RecognitionLayoutReviewBatchSettlerV1 interface {
	SettleRecognitionLayoutReviewBatchV1(context.Context, RecognitionPhysicalCallResult, RecognitionLayoutReviewBatchSettlementV1) (RecognitionLayoutReviewBatchSettlementResultV1, bool, error)
}

func initialReadExecutorV1(ctx context.Context) (RecognitionPhysicalCallExecutor, error) {
	if ctx == nil || !RecognitionLayoutPlanV2Enabled(ctx) || RecognitionLayoutInitialReadModeFromContext(ctx) != RecognitionLayoutManifestWithContentV1 {
		return nil, fmt.Errorf("%w: frozen initial-read mode is unavailable", ErrRecognitionLayoutPlanV2Unauthorized)
	}
	executor, ok := ctx.Value(recognitionPhysicalCallContextKey{}).(RecognitionPhysicalCallExecutor)
	if !ok || executor == nil {
		return nil, fmt.Errorf("%w: durable initial-read executor is unavailable", ErrRecognitionLayoutPlanV2Unauthorized)
	}
	return executor, nil
}

func AuthorizeAndSettleRecognitionLayoutInitialReadV1(ctx context.Context, source RecognitionPhysicalCallResult, plan RecognitionLayoutPlanV2, settlement RecognitionLayoutInitialReadSettlementV1) (RecognitionLayoutInitialReadSettlementResultV1, bool, error) {
	var zero RecognitionLayoutInitialReadSettlementResultV1
	executor, err := initialReadExecutorV1(ctx)
	if err != nil {
		return zero, false, err
	}
	if plan.InitialReadMode != RecognitionLayoutManifestWithContentV1 || ValidateRecognitionLayoutPlanV2(plan) != nil || plan.ManifestInvocationID != source.InvocationID || plan.ManifestResultDigest != source.ResultDigest || settlement.PlanDigest != plan.AuthorizedPlanDigest || settlement.SourcePhysicalInvocationID != source.InvocationID || settlement.SourcePhysicalResultDigest != source.ResultDigest || settlement.SourcePhysicalUnit != RecognitionPhysicalUnitWholePage {
		return zero, false, fmt.Errorf("%w: initial-read plan or source drifted", ErrRecognitionLayoutPlanV2Unauthorized)
	}
	settler, ok := executor.(RecognitionLayoutInitialReadSettlerV1)
	if !ok {
		return zero, false, fmt.Errorf("%w: initial-read settlement capability is unavailable", ErrRecognitionLayoutPlanV2Unauthorized)
	}
	return settler.AuthorizeAndSettleRecognitionLayoutInitialReadV1(ctx, source, plan, settlement)
}

func AuthorizeRecognitionLayoutReviewBatchV1(ctx context.Context, request RecognitionLayoutReviewBatchAuthorizationRequestV1) (RecognitionLayoutReviewBatchAuthorizationV1, bool, error) {
	var zero RecognitionLayoutReviewBatchAuthorizationV1
	executor, err := initialReadExecutorV1(ctx)
	if err != nil {
		return zero, false, err
	}
	runtime, err := LoadRecognitionLayoutPlanV2Runtime(ctx)
	if err != nil {
		return zero, false, err
	}
	digest, err := RecognitionLayoutReviewBatchInputDigestV1(request)
	if err != nil || digest != request.InputDigest || runtime.Header.InitialReadMode != RecognitionLayoutManifestWithContentV1 || runtime.AuthorizedPlan.InitialReadMode != RecognitionLayoutManifestWithContentV1 || runtime.AuthorizedPlan.AuthorizedPlanDigest != request.PlanDigest || !request.PhysicalUnit.validLayoutOrdinal("layout_review_batch_") {
		return zero, false, fmt.Errorf("%w: review authorization input drifted", ErrRecognitionLayoutPlanV2Unauthorized)
	}
	authorizer, ok := executor.(RecognitionLayoutReviewBatchAuthorizerV1)
	if !ok {
		return zero, false, fmt.Errorf("%w: review authorization capability is unavailable", ErrRecognitionLayoutPlanV2Unauthorized)
	}
	return authorizer.AuthorizeRecognitionLayoutReviewBatchV1(ctx, request)
}

func SettleRecognitionLayoutReviewBatchV1(ctx context.Context, source RecognitionPhysicalCallResult, settlement RecognitionLayoutReviewBatchSettlementV1) (RecognitionLayoutReviewBatchSettlementResultV1, bool, error) {
	var zero RecognitionLayoutReviewBatchSettlementResultV1
	executor, err := initialReadExecutorV1(ctx)
	if err != nil {
		return zero, false, err
	}
	runtime, err := LoadRecognitionLayoutPlanV2Runtime(ctx)
	if err != nil {
		return zero, false, err
	}
	if settlement.SourcePhysicalInvocationID != source.InvocationID || !validRecognitionLayoutSHA256(source.ResultDigest) || settlement.SourcePhysicalResultDigest != source.ResultDigest || settlement.PlanDigest != runtime.AuthorizedPlan.AuthorizedPlanDigest || runtime.Header.InitialReadMode != RecognitionLayoutManifestWithContentV1 {
		return zero, false, fmt.Errorf("%w: review settlement source drifted", ErrRecognitionLayoutPlanV2Unauthorized)
	}
	authorized := false
	for _, batch := range runtime.ReviewBatches {
		if batch.AuthorizationID == settlement.AuthorizationID && batch.AuthorizationDigest == settlement.AuthorizationDigest && batch.PhysicalUnit == settlement.SourcePhysicalUnit {
			authorized = true
			break
		}
	}
	if !authorized {
		return zero, false, fmt.Errorf("%w: review settlement is not durably authorized", ErrRecognitionLayoutPlanV2Unauthorized)
	}
	settler, ok := executor.(RecognitionLayoutReviewBatchSettlerV1)
	if !ok {
		return zero, false, fmt.Errorf("%w: review settlement capability is unavailable", ErrRecognitionLayoutPlanV2Unauthorized)
	}
	return settler.SettleRecognitionLayoutReviewBatchV1(ctx, source, settlement)
}
