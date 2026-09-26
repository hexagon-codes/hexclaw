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
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// WeeklyTextbookRecoveryRequest 将一次恢复绑定到原命令和原 checkpoint。
type WeeklyTextbookRecoveryRequest struct {
	SourceCommandKey         string `json:"source_command_key"`
	PlanRevision             int    `json:"plan_revision"`
	CheckpointSHA256         string `json:"checkpoint_sha256"`
	ItemIndex                int    `json:"item_index"`
	IdempotencyKey           string `json:"idempotency_key"`
	AcceptDuplicateExecution bool   `json:"accept_duplicate_execution"`
}

type weeklySolveRecoveryAttempt struct {
	Authorization WeeklyTextbookRecoveryRequest `json:"authorization"`
	RequestDigest string                        `json:"request_digest"`
	CreatedAt     int64                         `json:"created_at"`
	Call          weeklyCandidateCall           `json:"call"`
}

// validateWeeklyRecoverySource 阻止在课程或档案变化后继续旧题验算。
func (d Deps) validateWeeklyRecoverySource(ctx context.Context, request WeeklyPracticeCandidateRequest) error {
	profile, err := d.GetProfileWithRevision(ctx, request.AgentName)
	if err != nil {
		return err
	}
	progress, err := d.GetCurriculumProgress(ctx, request.AgentName, "math")
	if err != nil {
		return err
	}
	if profile.Revision != request.ProfileRevision || progress == nil || digestValue(*progress) != digestValue(request.Progress) {
		return fmt.Errorf("%w: weekly recovery source changed", records.ErrVersionConflict)
	}
	pages, err := d.Records.ListWeeklyTextbookPages(ctx, k12storage.TextbookScope{OwnerID: d.TextbookOwnerID, AgentName: request.AgentName, Subject: "math"}, *progress)
	if err != nil {
		return err
	}
	for _, target := range request.Targets {
		matched := false
		for _, page := range pages {
			matched = matched || page.SourceRef == target.SourceRef
		}
		if !matched {
			return fmt.Errorf("%w: weekly recovery textbook changed", records.ErrVersionConflict)
		}
	}
	return nil
}

// RecoverWeeklyTextbookTrack 仅追加明确授权的原题验算，成功生成及未知回执不重置。
func (d Deps) RecoverWeeklyTextbookTrack(ctx context.Context, agentName, planID string, input WeeklyTextbookRecoveryRequest) (k12.WeeklyPracticePlan, bool, error) {
	input.SourceCommandKey = strings.TrimSpace(input.SourceCommandKey)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if agentName == "" || planID == "" || input.SourceCommandKey == "" || input.IdempotencyKey == "" || input.PlanRevision < 1 || input.ItemIndex < 0 || !input.AcceptDuplicateExecution {
		return k12.WeeklyPracticePlan{}, false, fmt.Errorf("%w: invalid weekly recovery authorization", ErrInvalidInput)
	}
	if decoded, err := hex.DecodeString(input.CheckpointSHA256); err != nil || len(decoded) != sha256.Size {
		return k12.WeeklyPracticePlan{}, false, fmt.Errorf("%w: invalid weekly checkpoint digest", ErrInvalidInput)
	}
	ref := WeeklyCandidateCheckpointRef{AgentName: agentName, PlanID: planID, Kind: "refresh", Revision: input.PlanRevision, IdempotencyKey: input.SourceCommandKey}
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
	// 已接纳的授权先按原内容重放；完成后计划修订变化不触发第二次调用。
	for _, item := range request.Generation.Items {
		for _, attempt := range item.SolveRecoveryAttempts {
			if attempt.Authorization.IdempotencyKey == input.IdempotencyKey {
				if attempt.RequestDigest != digest {
					return k12.WeeklyPracticePlan{}, false, records.ErrVersionConflict
				}
				plan, _, err := d.PrepareWeeklyTextbookTrack(ctx, agentName, planID, input.PlanRevision, request.MaxItems, input.SourceCommandKey)
				return plan, true, err
			}
		}
	}
	sum := sha256.Sum256([]byte(raw))
	if hex.EncodeToString(sum[:]) != input.CheckpointSHA256 {
		return k12.WeeklyPracticePlan{}, false, records.ErrVersionConflict
	}
	plan, err := d.Records.GetWeeklyPracticePlan(ctx, agentName, planID)
	if err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	if plan.Status != k12.WeeklyPlanDraft || plan.Revision != input.PlanRevision {
		return k12.WeeklyPracticePlan{}, false, records.ErrVersionConflict
	}
	if err := d.validateWeeklyRecoverySource(ctx, request); err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	step := &request.Generation.Items[input.ItemIndex]
	prior := &step.Solve
	if n := len(step.SolveRecoveryAttempts); n > 0 {
		prior = &step.SolveRecoveryAttempts[n-1].Call
	}
	eligible := prior.Status == "outcome_unknown"
	if prior.Status == "succeeded" && prior.Result != nil && strings.TrimSpace(prior.Result.Solution) != "" && step.Target >= 0 && step.Target < len(request.Targets) {
		_, _, answer := SplitRetryPresentation(prior.Result.Solution)
		status, _, _ := classifyGeneratedPracticeItem(request.Targets[step.Target].Subject, *prior.Result, strings.TrimSpace(answer) != "")
		// 成功响应仍缺独立验证证据时，保留原响应并允许明确授权的新尝试。
		eligible = status == k12.PracticeItemNeedsReview && !prior.Result.Evidence.StrongTrust()
	}
	if step.Candidate != nil || step.Generate.Status != "succeeded" || step.Generate.Result == nil || !eligible {
		return k12.WeeklyPracticePlan{}, false, records.ErrIllegalTransition
	}
	// 本授权只恢复指定题；不借恢复入口生成其他尚未准备的题目。
	if len(request.Generation.Items) != request.MaxItems {
		return k12.WeeklyPracticePlan{}, false, records.ErrIllegalTransition
	}
	for i, item := range request.Generation.Items {
		if i != input.ItemIndex && item.Candidate == nil {
			return k12.WeeklyPracticePlan{}, false, records.ErrIllegalTransition
		}
	}
	step.SolveRecoveryAttempts = append(step.SolveRecoveryAttempts, weeklySolveRecoveryAttempt{Authorization: input, RequestDigest: digest, CreatedAt: d.now(), Call: weeklyCandidateCall{Status: "prepared"}})
	next, err := json.Marshal(request)
	if err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	if err := d.Records.SaveWeeklyCandidateCheckpoint(ctx, ref, raw, string(next)); err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	result, _, err := d.PrepareWeeklyTextbookTrack(ctx, agentName, planID, input.PlanRevision, request.MaxItems, input.SourceCommandKey)
	return result, false, err
}
