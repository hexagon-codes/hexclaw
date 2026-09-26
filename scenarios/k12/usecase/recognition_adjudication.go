package usecase

import (
	"context"
	"fmt"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func (e *durableRecognitionPhysicalCallExecutor) AuthorizeRecognitionLayoutAdjudicationV2(ctx context.Context, request k12.RecognitionLayoutAdjudicationRequestV2) (k12.RecognitionLayoutAdjudicationAuthorizationV2, bool, error) {
	var zero k12.RecognitionLayoutAdjudicationAuthorizationV2
	if e == nil || e.o == nil || e.o.deps.Records == nil || e.parent.Stage != k12.GradingStageRecognizing || e.parent.Status != k12.ModelInvocationSent {
		return zero, false, fmt.Errorf("%w: adjudication parent is unavailable", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	runtime, err := e.LoadRecognitionLayoutPlanV2Runtime(ctx)
	header, enabled := k12.RecognitionLayoutPlanV2HeaderDigestFromContext(ctx)
	if err != nil || !enabled || runtime.HeaderDigest != header || runtime.AuthorizedPlan == nil || !runtime.AuthorizedPlan.SourceAdjudication || runtime.AuthorizedPlan.AuthorizedPlanDigest != request.PlanDigest {
		return zero, false, fmt.Errorf("%w: adjudication runtime is unavailable", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	commitCtx, cancel := gradingDurableCommitContext(ctx)
	defer cancel()
	return e.o.deps.Records.AuthorizeRecognitionLayoutAdjudicationV2(commitCtx, e.parent.AgentName, e.parent.InvocationID, request)
}

func (e *durableRecognitionPhysicalCallExecutor) SettleRecognitionLayoutAdjudicationV2(ctx context.Context, source k12.RecognitionPhysicalCallResult, settlement k12.RecognitionLayoutAdjudicationSettlementV2) (k12.RecognitionLayoutAdjudicationSettlementResultV2, bool, error) {
	var zero k12.RecognitionLayoutAdjudicationSettlementResultV2
	if e == nil || e.o == nil || e.o.deps.Records == nil || e.parent.Stage != k12.GradingStageRecognizing || e.parent.Status != k12.ModelInvocationSent || source.InvocationID == "" || source.InvocationID != settlement.SourcePhysicalInvocationID || !validModelInvocationDigest(source.ResultDigest) || source.ResultDigest != settlement.SourcePhysicalResultDigest {
		return zero, false, fmt.Errorf("%w: adjudication physical source is unavailable", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	runtime, err := e.LoadRecognitionLayoutPlanV2Runtime(ctx)
	header, enabled := k12.RecognitionLayoutPlanV2HeaderDigestFromContext(ctx)
	if err != nil || !enabled || runtime.HeaderDigest != header || runtime.AuthorizedPlan == nil || !runtime.AuthorizedPlan.SourceAdjudication || runtime.AuthorizedPlan.AuthorizedPlanDigest != settlement.PlanDigest {
		return zero, false, fmt.Errorf("%w: adjudication runtime is unavailable", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	commitCtx, cancel := gradingDurableCommitContext(ctx)
	defer cancel()
	// 存储层复核所属者、目标、两份原来源及成功核验；此处另外核对真实私有载荷仍可读。
	if _, err := e.o.deps.Records.LoadSucceededModelPhysicalInvocationResultContent(commitCtx, e.parent.AgentName, source.InvocationID, source.ResultDigest); err != nil {
		return zero, false, err
	}
	return e.o.deps.Records.SettleRecognitionLayoutAdjudicationV2(commitCtx, e.parent.AgentName, e.parent.InvocationID, settlement)
}
