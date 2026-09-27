package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// WeeklyTextbookReinterpretationRequest 只授权读取既有回执，不允许新的物理调用。
type WeeklyTextbookReinterpretationRequest struct {
	SourceCommandKey     string `json:"source_command_key"`
	PlanRevision         int    `json:"plan_revision"`
	CheckpointSHA256     string `json:"checkpoint_sha256"`
	ItemIndex            int    `json:"item_index"`
	IdempotencyKey       string `json:"idempotency_key"`
	SourcePlanRevision   int    `json:"source_plan_revision,omitempty"`
	ExpectedPlanRevision int    `json:"expected_plan_revision,omitempty"`
}

// ReinterpretWeeklyTextbookTrack 追加当前解析器对成功回执的结论，保留原汇总及物理回执。
func (d Deps) ReinterpretWeeklyTextbookTrack(ctx context.Context, agentName, planID string, input WeeklyTextbookReinterpretationRequest) (k12.WeeklyPracticePlan, bool, error) {
	input.SourceCommandKey = strings.TrimSpace(input.SourceCommandKey)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if agentName == "" || planID == "" || input.SourceCommandKey == "" || input.IdempotencyKey == "" || input.ItemIndex < 0 {
		return k12.WeeklyPracticePlan{}, false, fmt.Errorf("%w: invalid weekly reinterpretation request", ErrInvalidInput)
	}
	if decoded, err := hex.DecodeString(input.CheckpointSHA256); err != nil || len(decoded) != sha256.Size {
		return k12.WeeklyPracticePlan{}, false, fmt.Errorf("%w: invalid weekly checkpoint digest", ErrInvalidInput)
	}
	sourceRevision, expectedRevision, err := weeklyRecoveryRevisions(input.PlanRevision, input.SourcePlanRevision, input.ExpectedPlanRevision)
	if err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	ref := WeeklyCandidateCheckpointRef{AgentName: agentName, PlanID: planID, Kind: "refresh", Revision: sourceRevision, IdempotencyKey: input.SourceCommandKey}
	raw, err := d.Records.GetWeeklyCandidateCheckpoint(ctx, ref)
	if err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	var request WeeklyPracticeCandidateRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	if request.AgentName != agentName || request.PlanSection != k12.WeeklySectionTextbookConsolidation || request.Checkpoint != ref || request.Generation == nil || input.ItemIndex >= len(request.Generation.Items) {
		return k12.WeeklyPracticePlan{}, false, records.ErrVersionConflict
	}
	digest := digestValue(input)
	for i := range request.Generation.Items {
		item := &request.Generation.Items[i]
		calls := []*weeklyCandidateCall{&item.Solve}
		for j := range item.SolveRecoveryAttempts {
			calls = append(calls, &item.SolveRecoveryAttempts[j].Call)
		}
		for _, call := range calls {
			for _, interpretation := range call.Interpretations {
				if interpretation.IdempotencyKey != input.IdempotencyKey {
					continue
				}
				if interpretation.RequestDigest != digest {
					return k12.WeeklyPracticePlan{}, false, records.ErrVersionConflict
				}
				plan, _, err := d.resumeWeeklyTextbookRecovery(ctx, request, expectedRevision)
				return plan, true, err
			}
		}
	}
	sum := sha256.Sum256([]byte(raw))
	if hex.EncodeToString(sum[:]) != input.CheckpointSHA256 {
		return k12.WeeklyPracticePlan{}, false, records.ErrVersionConflict
	}
	plan, completed, err := d.weeklyRecoveryPlan(ctx, request, expectedRevision)
	if err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	if completed || plan.Status != k12.WeeklyPlanDraft || plan.Revision != expectedRevision {
		return k12.WeeklyPracticePlan{}, false, records.ErrVersionConflict
	}
	if err := d.validateWeeklyRecoverySource(ctx, request); err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	step := &request.Generation.Items[input.ItemIndex]
	call := weeklyLatestSolve(step)
	if step.Candidate != nil || step.Generate.Status != "succeeded" || step.Generate.Result == nil || step.Target < 0 || step.Target >= len(request.Targets) || !weeklyPhysicalReplayReady(call) {
		return k12.WeeklyPracticePlan{}, false, records.ErrIllegalTransition
	}
	if len(request.Generation.Items) != request.MaxItems {
		return k12.WeeklyPracticePlan{}, false, records.ErrIllegalTransition
	}
	for i, item := range request.Generation.Items {
		if i != input.ItemIndex && item.Candidate == nil {
			return k12.WeeklyPracticePlan{}, false, records.ErrIllegalTransition
		}
	}
	capability, ok := d.Solver.(interface{ UsesGradingPhysicalCalls() bool })
	if !ok || !capability.UsesGradingPhysicalCalls() {
		return k12.WeeklyPracticePlan{}, false, fmt.Errorf("weekly solver receipt replay unavailable")
	}
	question, _, _ := SplitRetryPresentation(step.Generate.Result.Solution)
	callCtx := k12.WithGradingModelSnapshot(ctx, request.Generation.Route)
	inputDigest := d.weeklySolveInputDigest(callCtx, request, input.ItemIndex, question)
	if inputDigest != call.InputDigest {
		return k12.WeeklyPracticePlan{}, false, fmt.Errorf("%w: weekly solve input changed", records.ErrVersionConflict)
	}
	executor := &weeklyPhysicalExecutor{call: call, replayOnly: true, used: make(map[string]bool)}
	replayCtx := WithGradingPhysicalCallExecutor(k12.WithMaterialPreparation(callCtx), executor)
	result, err := d.solveProblem(replayCtx, request.Targets[step.Target].Subject, question, request.GradeTerm)
	if executor.err != nil {
		return k12.WeeklyPracticePlan{}, false, executor.err
	}
	if err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	// 必须实际消费 solver 和 verifier 回执，不接受绕过拦截器的汇总成功。
	generated, verified := false, false
	for key := range executor.used {
		generated = generated || strings.HasPrefix(key, string(k12.GradingItemOperationSolveGenerate)+":")
		verified = verified || strings.HasPrefix(key, string(k12.GradingItemOperationSolveVerify)+":")
	}
	if !generated || !verified {
		return k12.WeeklyPracticePlan{}, false, fmt.Errorf("weekly physical replay incomplete")
	}
	if err := d.validateWeeklyRecoverySource(ctx, request); err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	call.Interpretations = append(call.Interpretations, weeklySolveInterpretation{IdempotencyKey: input.IdempotencyKey, RequestDigest: digest, InputDigest: inputDigest, ReceiptsDigest: digestValue(call.PhysicalCalls), CreatedAt: d.now(), Result: result, SourcePlanRevision: input.SourcePlanRevision, ExpectedPlanRevision: input.ExpectedPlanRevision})
	next, err := json.Marshal(request)
	if err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	if err := d.Records.SaveWeeklyCandidateCheckpoint(context.WithoutCancel(ctx), ref, raw, string(next)); err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	resultPlan, _, err := d.resumeWeeklyTextbookRecovery(ctx, request, expectedRevision)
	return resultPlan, false, err
}
