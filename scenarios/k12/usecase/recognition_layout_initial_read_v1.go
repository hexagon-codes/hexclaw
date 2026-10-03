package usecase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image/png"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

var _ k12.RecognitionLayoutInitialReadSettlerV1 = (*durableRecognitionPhysicalCallExecutor)(nil)
var _ k12.RecognitionLayoutReviewBatchAuthorizerV1 = (*durableRecognitionPhysicalCallExecutor)(nil)
var _ k12.RecognitionLayoutReviewBatchSettlerV1 = (*durableRecognitionPhysicalCallExecutor)(nil)

// 新协议的桥接仍以存储中的父项、头部与实际子调用为权威，不从解析正文推定调用成功。
func (e *durableRecognitionPhysicalCallExecutor) initialReadRuntimeV1(ctx context.Context) (k12.RecognitionLayoutPlanRuntimeV2, error) {
	var zero k12.RecognitionLayoutPlanRuntimeV2
	if e == nil || e.o == nil || e.o.deps.Records == nil || e.parent.AgentName == "" || e.parent.InvocationID == "" || e.parent.JobID == "" || e.parent.Stage != k12.GradingStageRecognizing || e.parent.Status != k12.ModelInvocationSent {
		return zero, fmt.Errorf("%w: initial-read parent is unavailable", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	headerDigest, enabled := k12.RecognitionLayoutPlanV2HeaderDigestFromContext(ctx)
	if !enabled || k12.RecognitionLayoutInitialReadModeFromContext(ctx) != k12.RecognitionLayoutManifestWithContentV1 {
		return zero, fmt.Errorf("%w: initial-read header context is unavailable", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	inspectCtx, cancel := gradingDurableCommitContext(ctx)
	defer cancel()
	runtime, err := e.o.deps.Records.LoadRecognitionLayoutPlanRuntimeV2(inspectCtx, e.parent.AgentName, e.parent.InvocationID)
	if err != nil {
		return zero, err
	}
	h := runtime.Header
	if runtime.HeaderDigest != headerDigest || h.InitialReadMode != k12.RecognitionLayoutManifestWithContentV1 || h.ParentInvocationID != e.parent.InvocationID || h.AgentName != e.parent.AgentName || h.JobID != e.parent.JobID || h.ParentRequestDigest != e.parent.RequestDigest || h.RouteSnapshot != e.parent.RouteSnapshot || h.RequestPolicySnapshot != e.parent.RequestPolicySnapshot {
		return zero, fmt.Errorf("%w: initial-read runtime drifted from parent", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	parent, err := e.o.deps.Records.GetModelInvocation(inspectCtx, e.parent.AgentName, e.parent.InvocationID)
	if err != nil || parent.Status != k12.ModelInvocationSent || parent.RequestDigest != e.parent.RequestDigest || parent.JobID != e.parent.JobID || parent.RouteSnapshot != e.parent.RouteSnapshot || parent.RequestPolicySnapshot != e.parent.RequestPolicySnapshot {
		return zero, fmt.Errorf("%w: initial-read stored parent drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	return runtime, nil
}

func (e *durableRecognitionPhysicalCallExecutor) validateInitialReadSourceV1(ctx context.Context, source k12.RecognitionPhysicalCallResult, runtime k12.RecognitionLayoutPlanRuntimeV2, unit k12.RecognitionPhysicalUnit, targetIDs []string) error {
	if source.InvocationID == "" || !validModelInvocationDigest(source.ResultDigest) {
		return fmt.Errorf("%w: initial-read physical source is unavailable", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	inspectCtx, cancel := gradingDurableCommitContext(ctx)
	defer cancel()
	child, err := e.o.deps.Records.GetModelPhysicalInvocation(inspectCtx, e.parent.AgentName, source.InvocationID)
	if err != nil {
		return err
	}
	run, err := e.o.ensureRun(inspectCtx, e.parent.JobID)
	if err != nil {
		return err
	}
	page, err := k12.CanonicalizeRecognitionPageV2(run.req.Image)
	if err != nil || page.Digest != runtime.Header.PageDigest || run.req.InitialReadMode != runtime.Header.InitialReadMode {
		return fmt.Errorf("%w: initial-read frozen page drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	call := k12.RecognitionPhysicalCall{PlanVersion: k12.RecognitionPlanVersionV2, PlanDigest: runtime.HeaderDigest, Unit: unit, TargetIDs: targetIDs, Image: page.PNG}
	if unit != k12.RecognitionPhysicalUnitWholePage {
		if runtime.AuthorizedPlan == nil {
			return fmt.Errorf("%w: review source plan is unavailable", k12.ErrRecognitionLayoutPlanV2Unauthorized)
		}
		call.PlanDigest = runtime.AuthorizedPlan.AuthorizedPlanDigest
		call.Image, err = k12.BuildRecognitionLayoutReviewBatchImageV1(page.PNG, *runtime.AuthorizedPlan, targetIDs)
		if err != nil {
			return err
		}
	}
	wantID, err := stableRecognitionPhysicalInvocationIDForCall(e.parent.InvocationID, call)
	if err != nil || wantID != source.InvocationID || !recognitionPhysicalChildMatchesCall(e.parent, child, call) || child.Status != k12.ModelInvocationSucceeded || child.FailureKind != "" || child.ResultDigest != source.ResultDigest || child.Attempt != 1 {
		return fmt.Errorf("%w: initial-read physical source drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	payload, err := e.o.deps.Records.LoadSucceededModelPhysicalInvocationResultContent(inspectCtx, e.parent.AgentName, source.InvocationID, source.ResultDigest)
	if err != nil {
		return err
	}
	if source.Payload != payload {
		return fmt.Errorf("%w: initial-read private source content drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	return nil
}

func (e *durableRecognitionPhysicalCallExecutor) AuthorizeAndSettleRecognitionLayoutInitialReadV1(ctx context.Context, source k12.RecognitionPhysicalCallResult, plan k12.RecognitionLayoutPlanV2, settlement k12.RecognitionLayoutInitialReadSettlementV1) (k12.RecognitionLayoutInitialReadSettlementResultV1, bool, error) {
	var zero k12.RecognitionLayoutInitialReadSettlementResultV1
	runtime, err := e.initialReadRuntimeV1(ctx)
	if err != nil {
		return zero, false, err
	}
	if plan.InitialReadMode != runtime.Header.InitialReadMode || plan.PageDigest != runtime.Header.PageDigest || plan.ManifestInvocationID != source.InvocationID || plan.ManifestResultDigest != source.ResultDigest || settlement.SourcePhysicalInvocationID != source.InvocationID || settlement.SourcePhysicalResultDigest != source.ResultDigest || settlement.SourcePhysicalUnit != k12.RecognitionPhysicalUnitWholePage || settlement.PlanDigest != plan.AuthorizedPlanDigest {
		return zero, false, fmt.Errorf("%w: initial-read settlement source drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	if err = e.validateInitialReadSourceV1(ctx, source, runtime, k12.RecognitionPhysicalUnitWholePage, nil); err != nil {
		return zero, false, err
	}
	commitCtx, cancel := gradingDurableCommitContext(ctx)
	defer cancel()
	return e.o.deps.Records.AuthorizeAndSettleRecognitionLayoutInitialReadV1(commitCtx, e.parent.AgentName, e.parent.InvocationID, plan, settlement)
}

func (e *durableRecognitionPhysicalCallExecutor) AuthorizeRecognitionLayoutReviewBatchV1(ctx context.Context, request k12.RecognitionLayoutReviewBatchAuthorizationRequestV1) (k12.RecognitionLayoutReviewBatchAuthorizationV1, bool, error) {
	var zero k12.RecognitionLayoutReviewBatchAuthorizationV1
	runtime, err := e.initialReadRuntimeV1(ctx)
	if err != nil {
		return zero, false, err
	}
	if runtime.AuthorizedPlan == nil || runtime.AuthorizedPlan.AuthorizedPlanDigest != request.PlanDigest {
		return zero, false, fmt.Errorf("%w: review request plan drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	run, err := e.o.ensureRun(ctx, e.parent.JobID)
	if err != nil {
		return zero, false, err
	}
	page, err := k12.CanonicalizeRecognitionPageV2(run.req.Image)
	if err != nil || page.Digest != runtime.Header.PageDigest {
		return zero, false, fmt.Errorf("%w: review original page drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	targets := make([]string, len(request.Members))
	for index, member := range request.Members {
		targets[index] = member.CandidateID
	}
	image, err := k12.BuildRecognitionLayoutReviewBatchImageV1(page.PNG, *runtime.AuthorizedPlan, targets)
	if err != nil {
		return zero, false, err
	}
	sum := sha256.Sum256(image)
	geometry, err := png.DecodeConfig(bytes.NewReader(image))
	if err != nil || request.ImageDigest != "sha256:"+hex.EncodeToString(sum[:]) || request.ImageWidth != geometry.Width || request.ImageHeight != geometry.Height {
		return zero, false, fmt.Errorf("%w: review source pixels or geometry drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	commitCtx, cancel := gradingDurableCommitContext(ctx)
	defer cancel()
	return e.o.deps.Records.AuthorizeRecognitionLayoutReviewBatchV1(commitCtx, e.parent.AgentName, e.parent.InvocationID, request)
}

func (e *durableRecognitionPhysicalCallExecutor) SettleRecognitionLayoutReviewBatchV1(ctx context.Context, source k12.RecognitionPhysicalCallResult, settlement k12.RecognitionLayoutReviewBatchSettlementV1) (k12.RecognitionLayoutReviewBatchSettlementResultV1, bool, error) {
	var zero k12.RecognitionLayoutReviewBatchSettlementResultV1
	runtime, err := e.initialReadRuntimeV1(ctx)
	if err != nil {
		return zero, false, err
	}
	if runtime.AuthorizedPlan == nil || settlement.PlanDigest != runtime.AuthorizedPlan.AuthorizedPlanDigest || settlement.SourcePhysicalInvocationID != source.InvocationID || settlement.SourcePhysicalResultDigest != source.ResultDigest {
		return zero, false, fmt.Errorf("%w: review settlement source drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	var targets []string
	for _, batch := range runtime.ReviewBatches {
		if batch.AuthorizationID == settlement.AuthorizationID && batch.AuthorizationDigest == settlement.AuthorizationDigest && batch.PhysicalUnit == settlement.SourcePhysicalUnit {
			targets = batch.OrderedTargetIDs
			break
		}
	}
	if len(targets) == 0 {
		return zero, false, fmt.Errorf("%w: review member authorization is unavailable", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	if err = e.validateInitialReadSourceV1(ctx, source, runtime, settlement.SourcePhysicalUnit, targets); err != nil {
		return zero, false, err
	}
	commitCtx, cancel := gradingDurableCommitContext(ctx)
	defer cancel()
	return e.o.deps.Records.SettleRecognitionLayoutReviewBatchV1(commitCtx, e.parent.AgentName, e.parent.InvocationID, settlement)
}
