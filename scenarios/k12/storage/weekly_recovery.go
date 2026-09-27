package k12storage

import (
	"context"
	"encoding/json"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// GetWeeklyTextbookRecoveryPlan 只接纳原教材命令对当前空失败分区的限定恢复。
func (s *Store) GetWeeklyTextbookRecoveryPlan(ctx context.Context, ref WeeklyCandidateCheckpointRef, expectedRevision int, digest string) (k12.WeeklyPracticePlan, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	defer tx.Rollback()
	var storedDigest, response string
	if err := tx.QueryRowContext(ctx, `SELECT request_digest,response_json FROM k12_weekly_track_refresh_commands
		WHERE agent_name=? AND plan_id=? AND idempotency_key=?`, ref.AgentName, ref.PlanID, ref.IdempotencyKey).Scan(&storedDigest, &response); err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	if storedDigest != digest {
		return k12.WeeklyPracticePlan{}, false, records.ErrVersionConflict
	}
	if !weeklyCandidateResponsePending(response) {
		var plan k12.WeeklyPracticePlan
		if err := json.Unmarshal([]byte(response), &plan); err != nil {
			return k12.WeeklyPracticePlan{}, false, err
		}
		return plan, true, tx.Commit()
	}
	plan, err := weeklyTextbookRecoveryPlanVia(ctx, tx, ref.AgentName, ref.PlanID)
	if err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	if err := validateWeeklyTextbookRecoveryTarget(ctx, tx, plan, ref.Revision, expectedRevision, ref.IdempotencyKey, response); err != nil {
		return k12.WeeklyPracticePlan{}, false, err
	}
	return plan, false, tx.Commit()
}

func validateWeeklyTextbookRecoveryTarget(ctx context.Context, q weeklyPlanQuerier, current k12.WeeklyPracticePlan, sourceRevision, expectedRevision int, key, response string) error {
	if sourceRevision < 1 || expectedRevision <= sourceRevision || current.Status != k12.WeeklyPlanDraft || current.Revision != expectedRevision || !weeklyCandidateResponsePending(response) {
		return records.ErrVersionConflict
	}
	var envelope struct {
		Generation struct {
			Checkpoint WeeklyCandidateCheckpointRef `json:"checkpoint"`
		} `json:"_weekly_generation"`
	}
	if err := json.Unmarshal([]byte(response), &envelope); err != nil {
		return err
	}
	ref := envelope.Generation.Checkpoint
	if ref.Kind != "refresh" || ref.AgentName != current.AgentName || ref.PlanID != current.PlanID || ref.Revision != sourceRevision || ref.IdempotencyKey != key {
		return records.ErrVersionConflict
	}
	found := false
	otherItemIDs := make(map[string]bool)
	for _, track := range current.Tracks {
		if track.PlanSection == k12.WeeklySectionTextbookConsolidation {
			if found || track.Status != k12.WeeklyTrackFailed || len(track.Items) != 0 {
				return records.ErrVersionConflict
			}
			found = true
		} else {
			for _, item := range track.Items {
				otherItemIDs[item.ItemID] = true
			}
		}
	}
	if !found {
		return records.ErrVersionConflict
	}
	// 空教材分区不能遗留无法归属于其他分区的答案。
	for itemID := range current.AnswerKeys {
		if !otherItemIDs[itemID] {
			return records.ErrVersionConflict
		}
	}
	var pending int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM (
		SELECT response_json FROM k12_weekly_track_refresh_commands WHERE agent_name=? AND plan_id=? AND idempotency_key<>?
		UNION ALL
		SELECT response_json FROM k12_weekly_practice_plan_commands WHERE agent_name=? AND plan_id=?
	) WHERE json_extract(response_json,'$._weekly_pending')=1
	AND json_extract(response_json,'$._weekly_generation.checkpoint.revision')>=?`,
		current.AgentName, current.PlanID, key, current.AgentName, current.PlanID, expectedRevision).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return records.ErrVersionConflict
	}
	return nil
}

// 恢复时读取持久计划原文，不能把读取投影中隐藏的其他分区题目写回丢失。
func weeklyTextbookRecoveryPlanVia(ctx context.Context, q weeklyPlanQuerier, agentName, planID string) (k12.WeeklyPracticePlan, error) {
	var plan k12.WeeklyPracticePlan
	var raw, keys string
	if err := q.QueryRowContext(ctx, `SELECT plan_json,answer_keys_json,revision,status,source_digest,created_at,updated_at
		FROM k12_weekly_practice_plans WHERE agent_name=? AND plan_id=?`, agentName, planID).Scan(
		&raw, &keys, &plan.Revision, &plan.Status, &plan.SourceDigest, &plan.CreatedAt, &plan.UpdatedAt); err != nil {
		return plan, err
	}
	storedRevision, storedStatus, storedDigest, created, updated := plan.Revision, plan.Status, plan.SourceDigest, plan.CreatedAt, plan.UpdatedAt
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return plan, err
	}
	if err := json.Unmarshal([]byte(keys), &plan.AnswerKeys); err != nil {
		return plan, err
	}
	plan.Revision, plan.Status, plan.SourceDigest, plan.CreatedAt, plan.UpdatedAt = storedRevision, storedStatus, storedDigest, created, updated
	return plan, nil
}
