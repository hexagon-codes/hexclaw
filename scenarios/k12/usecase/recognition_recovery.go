package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/toolkit/util/idgen"
)

// RecognitionRecoveryInput 只授权原 unknown 批次或单项复核的新尝试，不接受替换模型或输入。
type RecognitionRecoveryInput struct {
	TimeoutOverrideMS        int64  `json:"timeout_override_ms,omitempty"`
	Agent                    string `json:"agent"`
	Version                  int    `json:"version"`
	JobVersion               int    `json:"job_version"`
	SourcePhysicalID         string `json:"source_physical_invocation_id"`
	IdempotencyKey           string `json:"idempotency_key"`
	AcceptDuplicateExecution bool   `json:"accept_duplicate_execution"`
}

type RecognitionRecoveryResult struct {
	Authorization k12.RecognitionRecoveryAuthorization `json:"authorization"`
	Status        string                               `json:"status"`
	Replayed      bool                                 `json:"replayed"`
}

func (o *GradingOrchestrator) recognitionRecoveryResult(ctx context.Context, a k12.RecognitionRecoveryAuthorization, replayed bool) (RecognitionRecoveryResult, error) {
	out := RecognitionRecoveryResult{Authorization: a, Status: "authorized", Replayed: replayed}
	child, err := o.deps.Records.GetModelPhysicalInvocation(ctx, a.AgentName, a.NewPhysicalID)
	if err == nil {
		out.Status = string(child.Status)
	} else if !errors.Is(err, records.ErrNotFound) {
		return out, err
	}
	return out, nil
}

// AuthorizeRecognitionRecovery 接纳明确的一次恢复；异步启动由公开命令负责，冷恢复沿原 Job。
func (o *GradingOrchestrator) AuthorizeRecognitionRecovery(ctx context.Context, dispatchID string, in RecognitionRecoveryInput) (RecognitionRecoveryResult, error) {
	var zero RecognitionRecoveryResult
	in.Agent = strings.TrimSpace(in.Agent)
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	in.SourcePhysicalID = strings.TrimSpace(in.SourcePhysicalID)
	if o == nil || o.deps.Records == nil || in.Agent == "" || in.SourcePhysicalID == "" || in.IdempotencyKey == "" || !in.AcceptDuplicateExecution {
		return zero, fmt.Errorf("%w: explicit recovery identity and duplicate-execution acknowledgement required", ErrInvalidInput)
	}
	if in.TimeoutOverrideMS != 0 && in.TimeoutOverrideMS != 180000 {
		return zero, fmt.Errorf("%w: recovery timeout must be 180000 milliseconds", ErrInvalidInput)
	}
	dispatch, err := o.deps.Records.GetImageTaskDispatch(ctx, in.Agent, dispatchID)
	if err != nil {
		return zero, err
	}
	request, _ := json.Marshal(in)
	requestDigest := modelInvocationDigest([]byte(dispatchID), request)
	if a, e := o.deps.Records.GetRecognitionRecoveryByKey(ctx, in.Agent, dispatchID, in.IdempotencyKey); e == nil {
		if a.RequestDigest != requestDigest {
			return zero, fmt.Errorf("%w: recovery idempotency identity changed", ErrInvalidInput)
		}
		return o.recognitionRecoveryResult(ctx, a, true)
	} else if !errors.Is(e, records.ErrNotFound) {
		return zero, e
	}
	source, err := o.deps.Records.GetModelPhysicalInvocation(ctx, in.Agent, in.SourcePhysicalID)
	if err != nil {
		return zero, err
	}
	lock := o.jobLock(source.JobID)
	lock.Lock()
	defer lock.Unlock()
	if a, e := o.deps.Records.GetRecognitionRecoveryByKey(ctx, in.Agent, dispatchID, in.IdempotencyKey); e == nil {
		if a.RequestDigest != requestDigest {
			return zero, fmt.Errorf("%w: recovery idempotency identity changed", ErrInvalidInput)
		}
		return o.recognitionRecoveryResult(ctx, a, true)
	} else if !errors.Is(e, records.ErrNotFound) {
		return zero, e
	}
	job, err := o.deps.GetGradingJob(ctx, in.Agent, source.JobID)
	if err != nil {
		return zero, err
	}
	if job.Record.Version != in.JobVersion || dispatch.Version != in.Version || job.Record.Status != k12.GradingStageOutcomeUnknown {
		return zero, records.ErrVersionConflict
	}
	prior, err := o.deps.Records.GetModelInvocation(ctx, in.Agent, source.ParentInvocationID)
	if err != nil {
		return zero, err
	}
	runtime, err := o.deps.Records.LoadRecognitionLayoutPlanRuntimeV2(ctx, in.Agent, prior.InvocationID)
	if err != nil {
		return zero, err
	}
	isPrimaryRecovery := strings.HasPrefix(string(source.PhysicalUnit), "layout_batch_")
	isRepairRecovery := strings.HasPrefix(string(source.PhysicalUnit), "layout_repair_") && source.PhysicalUnit.Valid()
	if runtime.AuthorizedPlan == nil || source.Status != k12.ModelInvocationOutcomeUnknown || (!isPrimaryRecovery && !isRepairRecovery) {
		return zero, fmt.Errorf("%w: one unknown primary batch or singleton repair with frozen plan required", ErrInvalidInput)
	}
	if isRepairRecovery && (in.TimeoutOverrideMS != 0 || source.PlanDigest != runtime.AuthorizedPlan.AuthorizedPlanDigest) {
		return zero, fmt.Errorf("%w: singleton repair requires the original plan and timeout", ErrInvalidInput)
	}
	run, err := o.ensureRun(ctx, source.JobID)
	if err != nil {
		return zero, err
	}
	page, err := k12.CanonicalizeRecognitionPageV2(run.req.Image)
	if err != nil {
		return zero, err
	}
	if page.Digest != runtime.Header.PageDigest || recognizingInvocationDigest(run.req.Image, job.Fields.ModelSnapshot, prior.RequestPolicySnapshot) != prior.RequestDigest {
		return zero, fmt.Errorf("%w: frozen recognition input changed", ErrInvalidInput)
	}
	sourceTimeout := source.EffectiveTimeoutMS
	if sourceTimeout == 0 {
		sourceTimeout = runtime.Header.PhysicalCallCapMillis
		if source.RouteSnapshot.TimeoutMS > 0 && int64(source.RouteSnapshot.TimeoutMS) < sourceTimeout {
			sourceTimeout = int64(source.RouteSnapshot.TimeoutMS)
		}
	}
	if in.TimeoutOverrideMS != 0 && sourceTimeout != 120000 {
		return zero, fmt.Errorf("%w: recovery timeout source must be 120000 milliseconds", ErrInvalidInput)
	}
	now := o.deps.now()
	parent := prior
	parent.InvocationID = "modelinv-" + idgen.ShortID()
	parent.Attempt++
	parent.CreatedAt = now
	parent.UpdatedAt = now
	parent.Status = k12.ModelInvocationPrepared
	parent.ResultDigest = ""
	parent.ResultJSON = ""
	parent.ExternalRequestID = ""
	parent.FailureKind = ""
	parent.ProviderIdempotencyKey = ""
	next := job.Fields
	next.AttemptCount = prior.Attempt
	next.Retryable = false
	next.FailureKind = ""
	next.FailedStage = ""
	next.ParentAutomaticAttemptID = parent.InvocationID
	window := dispatch.AutomaticBudgetSeconds
	if window <= 0 {
		window = 300
	}
	next.ParentAutomaticDeadlineAt = now + int64(window)
	next.ParentAutomaticRemainingSeconds = int64(window)
	if err = o.deps.setGradingDeadline(ctx, in.Agent, &next, k12.GradingStageRecognizing); err != nil {
		return zero, err
	}
	if in.TimeoutOverrideMS > 0 && (next.Deadline-now)*1000 < in.TimeoutOverrideMS {
		return zero, fmt.Errorf("%w: recovery stage window cannot contain authorized timeout", ErrInvalidInput)
	}
	next.ParentAutomaticDeadlineAt = next.Deadline
	next.ParentAutomaticRemainingSeconds = next.Deadline - now
	header, err := buildInitialRecognitionLayoutHeaderV2(parent, page.Digest, initialRecognitionLayoutContractV2{Budget: next.BudgetSnapshot, StageStartedAtUnixMillis: now * 1000})
	if err != nil {
		return zero, err
	}
	headerDigest, err := k12.RecognitionLayoutPlanHeaderDigestV2(header)
	if err != nil {
		return zero, err
	}
	manifestID, err := stableRecognitionPhysicalInvocationIDForCall(parent.InvocationID, k12.RecognitionPhysicalCall{PlanVersion: k12.RecognitionPlanVersionV2, PlanDigest: headerDigest, Unit: k12.RecognitionPhysicalUnitWholePage, Image: page.PNG})
	if err != nil {
		return zero, err
	}
	plan, err := k12.RebindRecognitionLayoutManifestV2(*runtime.AuthorizedPlan, manifestID)
	if err != nil {
		return zero, err
	}
	var call k12.RecognitionPhysicalCall
	if isRepairRecovery {
		// 单项候选与原图裁片保持不变，持久复核授权及成功来源由 Store 在事务内复验。
		for index, target := range plan.Targets {
			unit, unitErr := k12.RecognitionLayoutRepairUnitV2(index + 1)
			if unitErr != nil {
				return zero, unitErr
			}
			if unit != source.PhysicalUnit {
				continue
			}
			call = k12.RecognitionPhysicalCall{PlanVersion: k12.RecognitionPlanVersionV2, PlanDigest: plan.AuthorizedPlanDigest, Unit: unit, TargetIDs: []string{target.TargetID}}
			exactSetDigest, digestErr := k12.RecognitionLayoutTargetExactSetDigestV2(call.TargetIDs)
			if digestErr != nil || exactSetDigest != source.CandidateExactSetDigest {
				return zero, fmt.Errorf("%w: singleton repair candidate changed", ErrInvalidInput)
			}
			call.Image, err = k12.BuildRecognitionLayoutRepairImageV2(page.PNG, plan, target.TargetID)
			break
		}
	} else {
		for _, batch := range plan.Batches {
			if batch.Unit == source.PhysicalUnit {
				call = k12.RecognitionPhysicalCall{PlanVersion: k12.RecognitionPlanVersionV2, PlanDigest: plan.AuthorizedPlanDigest, Unit: batch.Unit, TargetIDs: batch.TargetIDs}
				call.Image, err = k12.BuildRecognitionLayoutBatchImageV2(page.PNG, plan, batch.Unit)
				break
			}
		}
	}
	if err != nil {
		return zero, err
	}
	if err = call.Validate(); err != nil {
		return zero, err
	}
	newID, err := stableRecognitionPhysicalInvocationIDForCall(parent.InvocationID, call)
	if err != nil {
		return zero, err
	}
	newDigest, err := recognizingPhysicalInvocationDigest(parent, call)
	if err != nil {
		return zero, err
	}
	oldCall := call
	oldCall.PlanDigest = runtime.AuthorizedPlan.AuthorizedPlanDigest
	if !recognitionPhysicalChildMatchesCall(prior, source, oldCall) {
		if isRepairRecovery {
			return zero, fmt.Errorf("%w: original repair request no longer matches frozen pixels", ErrInvalidInput)
		}
		return zero, fmt.Errorf("%w: original batch request no longer matches frozen pixels", ErrInvalidInput)
	}
	a := k12.RecognitionRecoveryAuthorization{SourceTimeoutMS: sourceTimeout, TimeoutOverrideMS: in.TimeoutOverrideMS, AuthorizationID: "recognition-recovery-" + idgen.ShortID(), AgentName: in.Agent, DispatchID: dispatchID, JobID: source.JobID, SourceParentID: prior.InvocationID, SourcePhysicalID: source.PhysicalInvocationID, NewParentID: parent.InvocationID, NewPhysicalID: newID, NewRequestDigest: newDigest, PageDigest: page.Digest, SourcePlanDigest: source.PlanDigest, SourceRequestDigest: source.RequestDigest, CandidateExactSetDigest: source.CandidateExactSetDigest, IdempotencyKey: in.IdempotencyKey, RequestDigest: requestDigest, CreatedAt: now}
	a, created, err := o.deps.Records.AuthorizeRecognitionRecovery(ctx, a, in.Version, in.JobVersion, parent, next)
	if err != nil {
		return zero, err
	}
	return o.recognitionRecoveryResult(ctx, a, !created)
}

// prepareRecognitionRecoveryPlan 只重绑定原计划，避免重新解析清单引入当前默认格式或裁决能力。
func (o *GradingOrchestrator) prepareRecognitionRecoveryPlan(ctx context.Context, parent k12.ModelInvocation) error {
	a, err := o.deps.Records.GetRecognitionRecoveryByParent(ctx, parent.AgentName, parent.InvocationID)
	if errors.Is(err, records.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	old, err := o.deps.Records.LoadRecognitionLayoutPlanRuntimeV2(ctx, parent.AgentName, a.SourceParentID)
	if err != nil {
		return err
	}
	current, err := o.deps.Records.LoadRecognitionLayoutPlanRuntimeV2(ctx, parent.AgentName, parent.InvocationID)
	if err != nil {
		return err
	}
	if old.AuthorizedPlan == nil || old.Header.PageDigest != a.PageDigest || old.AuthorizedPlan.AuthorizedPlanDigest != a.SourcePlanDigest {
		return fmt.Errorf("%w: recovery source plan changed", ErrModelInvocationRequiresReconciliation)
	}
	manifest, found, err := o.deps.Records.ReuseSucceededRecognitionPhysicalInvocation(ctx, parent.AgentName, current.ManifestPhysicalInvocationID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: recovery manifest could not be reused", ErrModelInvocationRequiresReconciliation)
	}
	plan, err := k12.RebindRecognitionLayoutManifestV2(*old.AuthorizedPlan, manifest.PhysicalInvocationID)
	if err != nil {
		return err
	}
	return o.deps.Records.AuthorizeRecognitionLayoutPlanV2(ctx, parent.AgentName, parent.InvocationID, k12.RecognitionLayoutManifestSuccessV2{InvocationID: manifest.PhysicalInvocationID, ResultDigest: manifest.ResultDigest}, plan)
}
