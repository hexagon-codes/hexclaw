package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// ArchivedReviewSchedule 随所属错题归档。所有者和记录 ID 由外层归档与键确定。
type ArchivedReviewSchedule struct {
	State         string `json:"state"`
	DeferredYear  int    `json:"deferred_iso_year,omitempty"`
	DeferredWeek  int    `json:"deferred_iso_week,omitempty"`
	PriorSchedule string `json:"prior_schedule_json"`
	Revision      int    `json:"revision"`
	UpdatedAt     int64  `json:"updated_at"`
}

func (r ArchivedReviewSchedule) Valid() bool {
	switch r.State {
	case k12.MistakeReviewDeferredThisWeek:
		if r.DeferredYear < 1 || r.DeferredWeek < 1 || r.DeferredWeek > 53 {
			return false
		}
	case k12.MistakeReviewScheduled, k12.MistakeReviewSuppressed, k12.MistakeReviewMastered:
		if r.DeferredYear != 0 || r.DeferredWeek != 0 {
			return false
		}
	default:
		return false
	}
	var prior *priorMistakeSchedule
	return r.Revision > 0 && r.UpdatedAt > 0 && json.Unmarshal([]byte(r.PriorSchedule), &prior) == nil && prior != nil
}

func (s *Store) ExportReviewSchedulesTx(ctx context.Context, tx *sql.Tx, agent string) (map[string]ArchivedReviewSchedule, error) {
	// 初始安排由错题根状态唯一决定。只归档根记录无法还原的选择和修订。
	rows, err := tx.QueryContext(ctx, `SELECT s.mistake_record_id,s.state,s.deferred_iso_year,s.deferred_iso_week,s.prior_schedule_json,s.revision,s.updated_at
		FROM k12_mistake_review_states s JOIN k12_mistakes m ON m.agent_name=s.agent_name AND m.record_id=s.mistake_record_id
		WHERE s.agent_name=? AND (s.revision<>1 OR s.deferred_iso_year<>0 OR s.deferred_iso_week<>0 OR s.prior_schedule_json<>'{}'
		OR s.state<>CASE m.status WHEN 'mastered' THEN 'mastered' WHEN 'archived' THEN 'suppressed' ELSE 'scheduled' END)`, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]ArchivedReviewSchedule)
	for rows.Next() {
		var id string
		var r ArchivedReviewSchedule
		if err := rows.Scan(&id, &r.State, &r.DeferredYear, &r.DeferredWeek, &r.PriorSchedule, &r.Revision, &r.UpdatedAt); err != nil {
			return nil, err
		}
		result[id] = r
	}
	return result, rows.Err()
}

// 原记录先在同一事务内写入。按双重归属覆盖触发器产生的初始计划。
func (s *Store) RestoreReviewSchedulesTx(ctx context.Context, tx *sql.Tx, agent string, roots []*records.AgentRecord, items map[string]ArchivedReviewSchedule) error {
	for _, root := range roots {
		if root.Collection != k12.CollectionMistakes {
			continue
		}
		id := root.RecordID
		r, saved := items[id]
		if !saved {
			r = ArchivedReviewSchedule{State: initialReviewState(root.Status), Revision: 1, UpdatedAt: max(1, root.UpdatedAt), PriorSchedule: "{}"}
		}
		if !r.Valid() {
			return fmt.Errorf("invalid archived review schedule for %q", id)
		}
		res, err := tx.ExecContext(ctx, `UPDATE k12_mistake_review_states SET state=?,deferred_iso_year=?,deferred_iso_week=?,
			prior_schedule_json=?,revision=?,updated_at=? WHERE agent_name=? AND mistake_record_id=?`,
			r.State, r.DeferredYear, r.DeferredWeek, r.PriorSchedule, r.Revision, r.UpdatedAt, agent, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("archived review schedule has no matching mistake %q", id)
		}
	}
	return nil
}
