package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// completedWeeklyTextbookProjection 由完整回执恢复投影，不进入模型或改写旧批次。
func (d Deps) completedWeeklyTextbookProjection(ctx context.Context, plan k12.WeeklyPracticePlan) (k12.WeeklyPracticeTrack, map[string]string, string, error) {
	var empty k12.WeeklyPracticeTrack
	if plan.Status != k12.WeeklyPlanDraft {
		return empty, nil, "", nil
	}
	raw, err := d.Records.GetCompletedWeeklyTextbookCheckpoint(ctx, plan.AgentName, plan.PlanID, plan.Revision)
	if errors.Is(err, records.ErrNotFound) {
		return empty, nil, "", nil
	}
	if err != nil {
		return empty, nil, "", err
	}
	var request WeeklyPracticeCandidateRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		return empty, nil, "", err
	}
	if request.AgentName != plan.AgentName || request.Checkpoint.PlanID != plan.PlanID || request.Checkpoint.Kind != "refresh" ||
		request.PlanSection != k12.WeeklySectionTextbookConsolidation || request.Generation == nil || request.MaxItems < 1 || len(request.Generation.Items) != request.MaxItems {
		return empty, nil, "", nil
	}
	if err := d.validateWeeklyRecoverySource(ctx, request); err != nil {
		if errors.Is(err, records.ErrVersionConflict) || errors.Is(err, records.ErrNotFound) {
			return empty, nil, "", nil
		}
		return empty, nil, "", err
	}
	candidates := make([]WeeklyPracticeCandidate, 0, request.MaxItems)
	for _, step := range request.Generation.Items {
		if step.Candidate == nil || strings.TrimSpace(step.Candidate.ExpectedAnswer) == "" || len(step.Candidate.EvidenceRefs) == 0 {
			return empty, nil, "", nil
		}
		candidates = append(candidates, *step.Candidate)
	}
	progress, err := d.GetCurriculumProgress(ctx, plan.AgentName, "math")
	if err != nil || progress == nil {
		return empty, nil, "", err
	}
	// 旧错误验证页码仅在回执中保留，展示采用当前真实进度的验证事实。
	request.Progress = *progress
	track, keys, _ := projectWeeklySupplementCandidates(request, candidates, 900)
	if track.Status != k12.WeeklyTrackReady || len(track.Items) != request.MaxItems {
		return empty, nil, "", nil
	}
	return track, keys, raw, nil
}

func (d Deps) projectWeeklyManualRecommendations(
	ctx context.Context,
	plan k12.WeeklyPracticePlan,
) (k12.WeeklyPracticePlan, error) {
	progress, err := d.GetCurriculumProgress(ctx, plan.AgentName, "math")
	_, production := d.WeeklyCandidates.(weeklyCandidateRequestFreezer)
	if production && plan.Status == k12.WeeklyPlanDraft {
		progress, err = d.PreviewCurriculumProgress(ctx, d.TextbookOwnerID, plan.AgentName)
	}
	if err != nil {
		return k12.WeeklyPracticePlan{}, err
	}
	textbookCount, err := d.Records.GetWeeklyManualPracticePreference(
		ctx, plan.AgentName, k12.WeeklySectionTextbookConsolidation)
	if errors.Is(err, records.ErrNotFound) {
		textbookCount, err = 5, nil
	}
	if err != nil {
		return k12.WeeklyPracticePlan{}, err
	}
	if textbookCount < 1 || textbookCount > 10 {
		textbookCount = 5
	}
	arithmeticCount, err := d.Records.GetWeeklyManualPracticePreference(
		ctx, plan.AgentName, k12.WeeklySectionArithmeticWarmup)
	if errors.Is(err, records.ErrNotFound) {
		arithmeticCount, err = 10, nil
	}
	if err != nil {
		return k12.WeeklyPracticePlan{}, err
	}
	if arithmeticCount < 1 || arithmeticCount > 20 {
		arithmeticCount = 10
	}
	syncAvailability := k12.WeeklyManualTrackAvailable
	arithmeticAvailability := k12.WeeklyManualTrackAvailable
	arithmeticSetupMessage := "curriculum progress setup required"
	if plan.Status != k12.WeeklyPlanDraft {
		syncAvailability = k12.WeeklyManualTrackFailedTerminal
		arithmeticAvailability = k12.WeeklyManualTrackFailedTerminal
	} else if !k12.CurriculumProgressSuggested(progress) {
		syncAvailability = k12.WeeklyManualTrackSetupRequired
		arithmeticAvailability = k12.WeeklyManualTrackSetupRequired
	}
	if freezer, production := d.WeeklyCandidates.(weeklyCandidateRequestFreezer); production && plan.Status == k12.WeeklyPlanDraft &&
		!(k12.CurriculumProgressSuggested(progress) && !k12.CurriculumProgressUsable(progress)) {
		request := WeeklyPracticeCandidateRequest{
			AgentName: plan.AgentName, PlanSection: k12.WeeklySectionArithmeticWarmup, MaxItems: 1,
		}
		if progress != nil {
			request.Progress = *progress
		}
		_, targetErr := freezer.FreezeWeeklyPracticeCandidateRequest(ctx, request)
		switch {
		case targetErr == nil:
			arithmeticAvailability = k12.WeeklyManualTrackAvailable
		case errors.Is(targetErr, errWeeklyLearningTargetUnavailable), errors.Is(targetErr, records.ErrNotFound):
			arithmeticAvailability = k12.WeeklyManualTrackSetupRequired
			arithmeticSetupMessage = "weekly learning target evidence unavailable"
		default:
			return k12.WeeklyPracticePlan{}, targetErr
		}
	}
	for index := range plan.Tracks {
		track := &plan.Tracks[index]
		switch track.PlanSection {
		case k12.WeeklySectionTextbookConsolidation:
			if syncAvailability == k12.WeeklyManualTrackSetupRequired {
				track.FailureMessage = "curriculum progress setup required"
			} else if track.Status == k12.WeeklyTrackFailed {
				syncAvailability = k12.WeeklyManualTrackFailedRetryable
			}
		case k12.WeeklySectionArithmeticWarmup:
			if arithmeticAvailability == k12.WeeklyManualTrackSetupRequired {
				track.FailureMessage = arithmeticSetupMessage
			}
			if track.ArithmeticBatch == nil {
				continue
			}
			// 未完成批次恢复原冻结请求，数量不由上次成功偏好或失败后的零产出推断。
			if track.ArithmeticBatch.State == k12.WeeklyArithmeticPreparing ||
				track.ArithmeticBatch.State == k12.WeeklyArithmeticFailedRetryable ||
				track.ArithmeticBatch.State == k12.WeeklyArithmeticFailedTerminal {
				var frozen struct{ MaxItems int }
				if json.Unmarshal([]byte(track.ArithmeticBatch.GenerationCheckpoint), &frozen) == nil &&
					frozen.MaxItems >= 1 && frozen.MaxItems <= 20 {
					arithmeticCount = frozen.MaxItems
				}
			}
			switch track.ArithmeticBatch.State {
			case k12.WeeklyArithmeticPreparing:
				arithmeticAvailability = k12.WeeklyManualTrackProcessing
			case k12.WeeklyArithmeticFailedRetryable:
				arithmeticAvailability = k12.WeeklyManualTrackFailedRetryable
			case k12.WeeklyArithmeticFailedTerminal:
				arithmeticAvailability = k12.WeeklyManualTrackFailedTerminal
			}
		}
	}
	plan.ManualTrackRecommendations = k12.WeeklyManualTrackRecommendations{
		TextbookConsolidation: k12.WeeklyManualTrackRecommendation{
			Availability: syncAvailability, SelectedItemCount: textbookCount,
			RecommendedItemCount: 5, MinItemCount: 1, MaxItemCount: 10,
		},
		ArithmeticWarmup: k12.WeeklyManualTrackRecommendation{
			Availability: arithmeticAvailability, SelectedItemCount: arithmeticCount,
			RecommendedItemCount: 10, MinItemCount: 1, MaxItemCount: 20,
		},
	}
	return plan, nil
}

func (d Deps) PrepareWeeklyTextbookTrack(
	ctx context.Context,
	agentName, planID string,
	expectedRevision, itemCount int,
	key string,
) (k12.WeeklyPracticePlan, bool, error) {
	agentName, planID, key = strings.TrimSpace(agentName), strings.TrimSpace(planID),
		strings.TrimSpace(key)
	if agentName == "" || planID == "" || key == "" || expectedRevision < 1 ||
		itemCount < 1 || itemCount > 10 {
		return k12.WeeklyPracticePlan{}, false,
			fmt.Errorf("%w: invalid textbook prepare", ErrInvalidInput)
	}
	requestDigest := digestValue(struct {
		Agent, Plan         string
		Revision, ItemCount int
	}{agentName, planID, expectedRevision, itemCount})
	plan, err := d.Records.GetWeeklyPracticePlan(ctx, agentName, planID)
	if err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	if plan.Status != k12.WeeklyPlanDraft {
		return k12.WeeklyPracticePlan{}, false, records.ErrVersionConflict
	}
	if plan.Revision != expectedRevision {
		stored, replay, _, commitErr := d.Records.CommitWeeklyTextbookRefresh(
			ctx, agentName, planID, expectedRevision, key, requestDigest,
			plan, false, 0, "", d.now())
		if commitErr != nil {
			return k12.WeeklyPracticePlan{}, false, commitErr
		}
		projected, projectErr := d.projectWeeklyArithmetic(ctx, stored)
		return projected, replay, projectErr
	}
	progress, progressRevision, err := d.GetCurriculumProgressState(ctx, agentName, "math")
	if _, production := d.WeeklyCandidates.(weeklyCandidateRequestFreezer); production {
		ref := WeeklyCandidateCheckpointRef{AgentName: agentName, Kind: "refresh", PlanID: planID, Revision: plan.Revision, IdempotencyKey: key}
		if _, checkpointErr := d.Records.GetWeeklyCandidateCheckpoint(ctx, ref); errors.Is(checkpointErr, records.ErrNotFound) {
			pending, pendingErr := d.Records.WeeklyPlanHasPendingGeneration(ctx, agentName, planID)
			if pendingErr != nil {
				return k12.WeeklyPracticePlan{}, false, pendingErr
			}
			// 原未决来源与已清除草稿保持不变；只有新命令才可采用建议。
			if !pending && !(progress == nil && progressRevision > 0) {
				progress, err = d.EnsureCurriculumProgress(ctx, d.TextbookOwnerID, agentName)
			}
		} else if checkpointErr != nil {
			return k12.WeeklyPracticePlan{}, false, checkpointErr
		}
	}
	if err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	if !k12.CurriculumProgressUsable(progress) {
		return k12.WeeklyPracticePlan{}, false, records.ErrIllegalTransition
	}
	found := false
	for _, track := range plan.Tracks {
		found = found || track.PlanSection == k12.WeeklySectionTextbookConsolidation
	}
	if !found {
		return k12.WeeklyPracticePlan{}, false, records.ErrIllegalTransition
	}
	request := WeeklyPracticeCandidateRequest{AgentName: agentName, PlanSection: k12.WeeklySectionTextbookConsolidation,
		MaxItems: itemCount, Progress: *progress}
	request, err = d.prepareWeeklyCandidateCommand(ctx, request, plan, "refresh", key, requestDigest)
	if err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	return d.finishWeeklyTextbookTrack(ctx, plan, request, expectedRevision, key, requestDigest, 0)
}

// finishWeeklyTextbookTrack 消费已有冻结请求，仅替换当前计划的教材分区。
func (d Deps) finishWeeklyTextbookTrack(ctx context.Context, plan k12.WeeklyPracticePlan,
	request WeeklyPracticeCandidateRequest, expectedRevision int, key, requestDigest string, recoverySourceRevision int,
) (k12.WeeklyPracticePlan, bool, error) {
	index := -1
	for i := range plan.Tracks {
		if plan.Tracks[i].PlanSection == k12.WeeklySectionTextbookConsolidation {
			index = i
			break
		}
	}
	if index < 0 {
		return k12.WeeklyPracticePlan{}, false, records.ErrIllegalTransition
	}
	// 显式选择题数的手动分区不消耗自动周练剩余预算，仍遵守分区原有上限。
	budget := 900
	nextTrack, nextKeys, _ := d.weeklySupplementRequest(ctx, request, true, budget)
	if nextTrack.Status != k12.WeeklyTrackReady {
		return k12.WeeklyPracticePlan{}, false,
			fmt.Errorf("%w: %s", ErrSolveFailed, nextTrack.FailureMessage)
	}
	if recoverySourceRevision > 0 {
		if err := d.validateWeeklyRecoverySource(ctx, request); err != nil {
			return k12.WeeklyPracticePlan{}, false, err
		}
	}
	next := plan
	next.Tracks = append([]k12.WeeklyPracticeTrack(nil), plan.Tracks...)
	next.Tracks[index] = nextTrack
	next.AnswerKeys = make(map[string]string, len(plan.AnswerKeys)+len(nextKeys))
	for itemID, answer := range plan.AnswerKeys {
		next.AnswerKeys[itemID] = answer
	}
	for _, old := range plan.Tracks[index].Items {
		delete(next.AnswerKeys, old.ItemID)
	}
	for itemID, answer := range nextKeys {
		next.AnswerKeys[itemID] = answer
	}
	next.Revision++
	revision := request.Progress.Revision
	next.CurriculumProgressRevision = &revision
	next.UpdatedAt = d.now()
	next.SourceDigest = digestValue(struct {
		PlanID    string
		ItemCount int
		Track     k12.WeeklyPracticeTrack
	}{next.PlanID, request.MaxItems, nextTrack})
	checkpointJSON := d.weeklyCandidateCheckpointJSON(ctx, request)
	stored, replay, _, err := d.Records.CommitWeeklyTextbookRefresh(
		ctx, plan.AgentName, plan.PlanID, expectedRevision, key, requestDigest,
		next, true, request.MaxItems, string(checkpointJSON), d.now(), recoverySourceRevision)
	if err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	projected, projectErr := d.projectWeeklyArithmetic(ctx, stored)
	return projected, replay, projectErr
}
