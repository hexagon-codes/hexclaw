package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// WeeklyCandidateCheckpointRef 绑定原周计划命令、刷新命令或口算批次，不创建平行任务。
type WeeklyCandidateCheckpointRef struct {
	AgentName      string `json:"agent"`
	Kind           string `json:"kind"`
	PlanID         string `json:"plan_id"`
	Revision       int    `json:"revision"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	BatchID        string `json:"batch_id,omitempty"`
}

func weeklyCandidateCommandQuery(ref WeeklyCandidateCheckpointRef) (string, []any, error) {
	switch ref.Kind {
	case "plan":
		return `k12_weekly_practice_plan_commands WHERE agent_name=? AND idempotency_key=?`, []any{ref.AgentName, ref.IdempotencyKey}, nil
	case "refresh":
		return `k12_weekly_track_refresh_commands WHERE agent_name=? AND plan_id=? AND idempotency_key=?`, []any{ref.AgentName, ref.PlanID, ref.IdempotencyKey}, nil
	default:
		return "", nil, fmt.Errorf("invalid weekly candidate checkpoint kind")
	}
}

func weeklyCandidateResponsePending(raw string) bool {
	var state struct {
		Pending bool `json:"_weekly_pending"`
	}
	return json.Unmarshal([]byte(raw), &state) == nil && state.Pending
}

func weeklyCandidateFinalResponse(previous string, plan k12.WeeklyPracticePlan) ([]byte, error) {
	raw, err := json.Marshal(plan)
	if err != nil || previous == "" {
		return raw, err
	}
	var old map[string]json.RawMessage
	if err := json.Unmarshal([]byte(previous), &old); err != nil {
		return nil, err
	}
	if request, ok := old["_weekly_generation"]; ok {
		var next map[string]json.RawMessage
		if err := json.Unmarshal(raw, &next); err != nil {
			return nil, err
		}
		next["_weekly_generation"] = request
		next["_weekly_pending"] = json.RawMessage("false")
		return json.Marshal(next)
	}
	return raw, nil
}

// PrepareWeeklyCandidateCommand 先保留请求与目标，再允许外发；同一命令恢复原冻结请求。
func (s *Store) PrepareWeeklyCandidateCommand(ctx context.Context, ref WeeklyCandidateCheckpointRef,
	digest string, plan k12.WeeklyPracticePlan, requestJSON string, at int64) (string, error) {
	query, args, err := weeklyCandidateCommandQuery(ref)
	if err != nil || !json.Valid([]byte(requestJSON)) || digest == "" || ref.AgentName != plan.AgentName || ref.PlanID != plan.PlanID {
		return "", fmt.Errorf("invalid weekly candidate command")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var storedDigest, response string
	err = tx.QueryRowContext(ctx, `SELECT request_digest,response_json FROM `+query, args...).Scan(&storedDigest, &response)
	if err == nil {
		if storedDigest != digest {
			return "", records.ErrVersionConflict
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(response), &envelope); err != nil {
			return "", err
		}
		request := envelope["_weekly_generation"]
		if len(request) == 0 {
			return "", records.ErrIllegalTransition
		}
		return string(request), tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	current, getErr := getWeeklyPlanVia(ctx, tx, ref.AgentName, `plan_id=?`, ref.PlanID)
	if errors.Is(getErr, records.ErrNotFound) && ref.Kind == "plan" {
		if err := insertWeeklyPlanTx(ctx, tx, plan, digest); err != nil {
			return "", err
		}
	} else if getErr != nil {
		return "", getErr
	} else if current.Status != k12.WeeklyPlanDraft || current.Revision != ref.Revision {
		return "", records.ErrVersionConflict
	}
	// 同一业务请求换幂等键也不能越过仍未知的物理回执。
	table := "k12_weekly_practice_plan_commands"
	if ref.Kind == "refresh" {
		table = "k12_weekly_track_refresh_commands"
	}
	var unresolved int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` c
  WHERE c.agent_name=? AND c.plan_id=? AND c.request_digest=? AND json_extract(c.response_json,'$._weekly_pending')=1
  AND EXISTS(SELECT 1 FROM json_tree(c.response_json,'$._weekly_generation.generation.items') j
   WHERE j.key='status' AND j.value IN ('sent','outcome_unknown'))`, ref.AgentName, ref.PlanID, digest).Scan(&unresolved); err != nil {
		return "", err
	}
	if unresolved > 0 {
		return "", fmt.Errorf("%w: weekly invocation reconciliation required", records.ErrIllegalTransition)
	}
	envelope, err := json.Marshal(map[string]any{"_weekly_pending": true, "_weekly_generation": json.RawMessage(requestJSON)})
	if err != nil {
		return "", err
	}
	if ref.Kind == "plan" {
		_, err = tx.ExecContext(ctx, `INSERT INTO k12_weekly_practice_plan_commands
			(agent_name,idempotency_key,request_digest,plan_id,plan_revision,response_json,created_at)
			VALUES(?,?,?,?,?,?,?)`, ref.AgentName, ref.IdempotencyKey, digest, ref.PlanID, ref.Revision, string(envelope), at)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO k12_weekly_track_refresh_commands
			(agent_name,plan_id,idempotency_key,request_digest,response_json,created_revision,created_at)
			VALUES(?,?,?,?,?,0,?)`, ref.AgentName, ref.PlanID, ref.IdempotencyKey, digest, string(envelope), at)
	}
	if err != nil {
		return "", err
	}
	return requestJSON, tx.Commit()
}

func (s *Store) GetWeeklyCandidateCheckpoint(ctx context.Context, ref WeeklyCandidateCheckpointRef) (string, error) {
	var request string
	var err error
	if ref.Kind == "arithmetic" {
		err = s.db.QueryRowContext(ctx, `SELECT generation_checkpoint_json FROM k12_weekly_arithmetic_batches
			WHERE agent_name=? AND batch_id=? AND plan_id=?`, ref.AgentName, ref.BatchID, ref.PlanID).Scan(&request)
	} else {
		query, args, queryErr := weeklyCandidateCommandQuery(ref)
		if queryErr != nil {
			return "", queryErr
		}
		err = s.db.QueryRowContext(ctx, `SELECT json_extract(response_json,'$._weekly_generation') FROM `+query, args...).Scan(&request)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", records.ErrNotFound
	}
	return request, err
}

// SaveWeeklyCandidateCheckpoint 用原字节 CAS，成功回执及未知状态不能被迟到执行覆盖。
func (s *Store) SaveWeeklyCandidateCheckpoint(ctx context.Context, ref WeeklyCandidateCheckpointRef, previous, next string) error {
	if !json.Valid([]byte(next)) {
		return records.ErrInvalidFields
	}
	var res sql.Result
	var err error
	if ref.Kind == "arithmetic" {
		res, err = s.db.ExecContext(ctx, `UPDATE k12_weekly_arithmetic_batches SET generation_checkpoint_json=?
			WHERE agent_name=? AND batch_id=? AND plan_id=? AND generation_checkpoint_json=?`,
			next, ref.AgentName, ref.BatchID, ref.PlanID, previous)
	} else {
		_, args, queryErr := weeklyCandidateCommandQuery(ref)
		if queryErr != nil {
			return queryErr
		}
		values := append([]any{next}, args...)
		values = append(values, previous)
		// 表名和条件只来自上方封闭枚举，值始终参数化。
		table, where := "k12_weekly_practice_plan_commands", "agent_name=? AND idempotency_key=?"
		if ref.Kind == "refresh" {
			table, where = "k12_weekly_track_refresh_commands", "agent_name=? AND plan_id=? AND idempotency_key=?"
		}
		res, err = s.db.ExecContext(ctx, `UPDATE `+table+` SET response_json=json_set(response_json,'$._weekly_generation',json(?))
			WHERE `+where+` AND json_extract(response_json,'$._weekly_generation')=? AND json_extract(response_json,'$._weekly_pending')=1`, values...)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return records.ErrVersionConflict
	}
	return nil
}

// validateWeeklyPracticeAssets 在周练自己的写事务中复核选题资格，历史打印重放不改写。
func validateWeeklyPracticeAssets(ctx context.Context, db dbHandle, agent string, tracks []k12.WeeklyPracticeTrack, keys map[string]string) error {
	for _, track := range tracks {
		for _, item := range track.Items {
			if item.AssetSource == nil {
				continue
			}
			if item.AssetSource.OriginalReview || item.AssetSource.KnowledgePoint != item.KnowledgePoint || item.SourceQuestion == "" {
				return ErrProblemAssetUnavailable
			}
			if err := validatePracticeAssetProblem(ctx, db, agent, item.SourceQuestion, k12.PracticeCandidateProblem{
				Subject: item.Subject, QuestionMarkdown: item.PromptMarkdown, ExpectedAnswerMarkdown: keys[item.ItemID], AssetSource: item.AssetSource,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
