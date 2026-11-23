package k12storage

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"time"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// WeeklyHistoryArchive 只携带往期已冻结的卷面、原 PDF 和已发生的作答。活跃任务和投递命令不随档案启动。
type WeeklyHistoryArchive struct {
	Plan       k12.WeeklyPracticePlan     `json:"plan"`
	PlanDigest string                     `json:"plan_source_digest"`
	PlanKeys   map[string]string          `json:"plan_answer_keys,omitempty"`
	Snapshot   k12.WeeklyPracticeSnapshot `json:"snapshot"`
	AnswerKeys map[string]string          `json:"snapshot_answer_keys,omitempty"`
	Artifact   k12.PrintArtifact          `json:"artifact"`
	Render     k12.PrintArtifactRender    `json:"render"`
	PDF        []byte                     `json:"pdf"`
	Attempts   []WeeklyHistoryAttempt     `json:"attempts,omitempty"`
}

type WeeklyHistoryAttempt struct {
	Attempt           k12.WeeklyPracticeAttempt `json:"attempt"`
	RequestSnapshotID string                    `json:"request_snapshot_id,omitempty"`
	OrderID           string                    `json:"order_id,omitempty"`
	Key               string                    `json:"idempotency_key"`
	Digest            string                    `json:"request_digest"`
	Updated           int64                     `json:"updated_at"`

	inferredOrderID string // 仅供本次迁移核对，归档和数据库不保存。
}

// FreezeMigrationOrder must run before rewriting the source attempt's identity.
func (a *WeeklyHistoryAttempt) FreezeMigrationOrder() {
	if a.OrderID != "" {
		return
	}
	a.OrderID = a.Attempt.AttemptID
	if a.OrderID == k12.WeeklyPracticeAttemptID(a.Attempt.SnapshotID, a.Attempt.ItemID, a.Key) {
		a.inferredOrderID = a.OrderID
	}
}

type storedWeeklyHistoryAttempt struct {
	k12.WeeklyPracticeAttempt
	RequestSnapshotID string `json:"archive_request_snapshot_id,omitempty"`
	OrderID           string `json:"archive_order_id,omitempty"`
}

func weeklyArchivePeriod(ctx context.Context, tx *sql.Tx, agent string, at int64) (year, week int, zone string, err error) {
	settings, err := weeklyPracticeSettingsVia(ctx, tx, agent)
	if err != nil {
		return 0, 0, "", err
	}
	loc, err := time.LoadLocation(settings.Timezone)
	if err != nil {
		return 0, 0, "", err
	}
	year, week = time.Unix(at, 0).In(loc).ISOWeek()
	return year, week, settings.Timezone, nil
}

// 在备份事务内以同一时区与周界选取卷面。即使家长从未打开历史页，过周的已发布卷也会进入归档。
func (s *Store) ExportWeeklyHistoryTx(ctx context.Context, tx *sql.Tx, agent string, at int64) ([]WeeklyHistoryArchive, error) {
	year, week, zone, err := weeklyArchivePeriod(ctx, tx, agent, at)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT s.snapshot_id`+weeklyHistorySources+`
		WHERE s.agent_name=? AND (p.status='archived' OR (p.status IN ('draft','frozen') AND `+otherWeeklyPeriodSQL+`))
		ORDER BY s.snapshot_id`, agent, year, week, zone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WeeklyHistoryArchive
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		item, err := readArchivedWeek(ctx, tx, agent, id)
		if err != nil {
			return nil, err
		}
		if item.Plan.Status != k12.WeeklyPlanArchived {
			item.Plan.Status, item.Plan.UpdatedAt = k12.WeeklyPlanArchived, at
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func readArchivedWeek(ctx context.Context, tx *sql.Tx, agent, id string) (WeeklyHistoryArchive, error) {
	var out WeeklyHistoryArchive
	snap, err := getWeeklySnapshotVia(ctx, tx, agent, "snapshot_id=?", id)
	if err != nil {
		return out, err
	}
	out.AnswerKeys, snap.AnswerKeys = snap.AnswerKeys, nil
	out.Snapshot = snap
	// 按归档行读取原计划；当前错题状态不能过滤历史题目。
	var planJSON, keysJSON, status string
	var created, archived int64
	err = tx.QueryRowContext(ctx, `SELECT plan_json,answer_keys_json,source_digest,status,created_at,updated_at
		FROM k12_weekly_practice_plans WHERE plan_id=? AND agent_name=?`,
		snap.PlanID, agent).Scan(&planJSON, &keysJSON, &out.PlanDigest, &status, &created, &archived)
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal([]byte(planJSON), &out.Plan); err != nil {
		return out, err
	}
	if err = json.Unmarshal([]byte(keysJSON), &out.PlanKeys); err != nil {
		return out, err
	}
	out.Plan.Status, out.Plan.CreatedAt, out.Plan.UpdatedAt = status, created, archived
	if out.Artifact, err = getPrintArtifactVia(ctx, tx, agent, snap.ArtifactID); err != nil {
		return out, fmt.Errorf("read archived weekly artifact: %w", err)
	}
	if out.Render, err = getPrintArtifactRenderVia(ctx, tx, agent, snap.ArtifactID); err != nil {
		return out, fmt.Errorf("read archived weekly PDF: %w", err)
	}
	if !validPrintArtifactRender(out.Artifact, out.Render) {
		return out, fmt.Errorf("archived weekly PDF failed integrity check")
	}
	out.PDF, out.Render.Payload = out.Render.Payload, nil
	rows, err := tx.QueryContext(ctx, `SELECT attempt_json,idempotency_key,request_digest,updated_at
		FROM k12_weekly_practice_attempts WHERE snapshot_id=? AND agent_name=? ORDER BY attempt_id`, id, agent)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var attempt WeeklyHistoryAttempt
		var raw string
		if err = rows.Scan(&raw, &attempt.Key, &attempt.Digest, &attempt.Updated); err != nil {
			return out, err
		}
		var stored storedWeeklyHistoryAttempt
		if err = json.Unmarshal([]byte(raw), &stored); err != nil {
			return out, err
		}
		attempt.Attempt, attempt.RequestSnapshotID = stored.WeeklyPracticeAttempt, stored.RequestSnapshotID
		attempt.OrderID = stored.OrderID
		out.Attempts = append(out.Attempts, attempt)
	}
	return out, rows.Err()
}

func ValidateWeeklyHistoryArchive(agent string, weeks []WeeklyHistoryArchive) error {
	seen := make(map[string]bool)
	for _, item := range weeks {
		p, s, a := item.Plan, item.Snapshot, item.Artifact
		if s.SnapshotID == "" || seen[s.SnapshotID] || s.AgentName != agent || p.AgentName != agent ||
			p.Status != k12.WeeklyPlanArchived || p.PlanID == "" || s.PlanID != p.PlanID ||
			p.Revision < 1 || s.PlanRevision != p.Revision || p.ISOWeekNumber != s.ISOWeekNumber ||
			p.ISOWeekYear != s.ISOWeekYear || p.Timezone != s.Timezone ||
			a.ArtifactID == "" || s.ArtifactID != a.ArtifactID || a.AgentName != agent ||
			a.SourceKind != k12.PrintSourceWeeklyPracticeSnapshot || a.SourceRef != s.SnapshotID {
			return fmt.Errorf("%w: inconsistent archived weekly snapshot", records.ErrInvalidFields)
		}
		seen[s.SnapshotID] = true
		render := item.Render
		render.Payload = item.PDF
		if !validPrintArtifactRender(a, render) {
			return fmt.Errorf("%w: invalid archived weekly PDF", records.ErrInvalidFields)
		}
		ids := make(map[string]bool)
		for _, track := range s.Tracks {
			for _, question := range track.Items {
				ids[question.ItemID] = true
			}
		}
		for _, r := range item.Attempts {
			if r.Attempt.SnapshotID != s.SnapshotID || !ids[r.Attempt.ItemID] || r.Attempt.AttemptID == "" ||
				r.Key == "" || r.Updated < r.Attempt.CreatedAt {
				return fmt.Errorf("%w: invalid archived weekly attempt", records.ErrInvalidFields)
			}
		}
	}
	return nil
}

// ImportWeeklyHistoryTx 对相同冻结事实幂等。冲突时让外层回滚整次恢复，保留两端原件。
func (s *Store) ImportWeeklyHistoryTx(ctx context.Context, tx *sql.Tx, agent string, weeks []WeeklyHistoryArchive, at int64) error {
	for _, item := range weeks {
		snap := item.Snapshot
		prior, err := readArchivedWeek(ctx, tx, agent, snap.SnapshotID)
		if err == nil {
			if prior.Plan.Status != k12.WeeklyPlanArchived {
				// 已发布但源端还未结转的同一旧卷可以重放；目标的当周计划仍受归属保护。
				y, w, zone, err := weeklyArchivePeriod(ctx, tx, agent, at)
				if err != nil {
					return err
				}
				if (prior.Plan.Status != k12.WeeklyPlanFrozen && prior.Plan.Status != k12.WeeklyPlanDraft) ||
					(prior.Plan.ISOWeekYear == y && prior.Plan.ISOWeekNumber == w && prior.Plan.Timezone == zone) {
					return fmt.Errorf("%w: target week is active", records.ErrVersionConflict)
				}
				prior.Plan.Status = k12.WeeklyPlanArchived
			}
			if !sameArchivedWeek(prior, item) {
				return fmt.Errorf("%w: weekly history differs for %s", records.ErrVersionConflict, snap.SnapshotID)
			}
			continue
		}
		if !errors.Is(err, records.ErrNotFound) {
			return err
		}
		p := item.Plan
		var collision bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_weekly_practice_plans
			WHERE plan_id=? OR (agent_name=? AND iso_week_year=? AND iso_week_number=? AND timezone=?))`,
			p.PlanID, agent, p.ISOWeekYear, p.ISOWeekNumber, p.Timezone).Scan(&collision)
		if err != nil {
			return err
		}
		if collision {
			return fmt.Errorf("%w: target week already has a different plan", records.ErrVersionConflict)
		}
		p.AnswerKeys = item.PlanKeys
		if err := insertWeeklyPlanTx(ctx, tx, p, item.PlanDigest); err != nil {
			return err
		}
		raw, err := json.Marshal(snap)
		if err != nil {
			return err
		}
		keys, err := json.Marshal(item.AnswerKeys)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO k12_weekly_practice_snapshots
			(snapshot_id,plan_id,plan_revision,agent_name,snapshot_digest,snapshot_json,answer_keys_json,created_at)
			VALUES(?,?,?,?,?,?,?,?)`, snap.SnapshotID, snap.PlanID, snap.PlanRevision, agent,
			snap.SnapshotDigest, string(raw), string(keys), snap.CreatedAt); err != nil {
			return err
		}
		render := item.Render
		render.Payload = item.PDF
		_, frozen, _, err := freezePrintArtifactVia(ctx, tx, item.Artifact, render)
		if err != nil {
			return err
		}
		if frozen.ByteDigest != render.ByteDigest {
			return fmt.Errorf("%w: target has a different frozen weekly PDF", records.ErrVersionConflict)
		}
		for _, r := range item.Attempts {
			encoded, err := json.Marshal(storedWeeklyHistoryAttempt{
				WeeklyPracticeAttempt: r.Attempt, RequestSnapshotID: r.RequestSnapshotID, OrderID: r.OrderID,
			})
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO k12_weekly_practice_attempts
				(attempt_id,agent_name,snapshot_id,item_id,idempotency_key,request_digest,attempt_json,created_at,updated_at)
				VALUES(?,?,?,?,?,?,?,?,?)`, r.Attempt.AttemptID, agent, snap.SnapshotID, r.Attempt.ItemID,
				r.Key, r.Digest, string(encoded), r.Attempt.CreatedAt, r.Updated); err != nil {
				return err
			}
		}
	}
	return nil
}

func sameArchivedWeek(a, b WeeklyHistoryArchive) bool {
	// 读回已核实摘要，比较时不再复制两份 PDF。空的可选 JSON 与省略等价。
	a.PDF, b.PDF = nil, nil
	// 周界结转时刻取决于首次读取，与不可变题面及真实作答无关。原目标保留自己的归档时刻。
	a.Plan.UpdatedAt, b.Plan.UpdatedAt = 0, 0
	if !maps.Equal(a.PlanKeys, b.PlanKeys) || !maps.Equal(a.AnswerKeys, b.AnswerKeys) {
		return false
	}
	a.PlanKeys, b.PlanKeys, a.AnswerKeys, b.AnswerKeys = nil, nil, nil, nil
	if len(a.Attempts) != len(b.Attempts) {
		return false
	}
	aAttempts, bAttempts := a.Attempts, b.Attempts
	a.Attempts, b.Attempts = nil, nil
	if !reflect.DeepEqual(a, b) {
		return false
	}
	checkOrder := false
	for i, prior := range aAttempts {
		incoming := bAttempts[i]
		// 旧目标没有排序键时，兼容原请求 ID 和本次迁移从源快照推导的中转 ID。
		if prior.OrderID == "" && prior.RequestSnapshotID != "" &&
			(incoming.OrderID == incoming.inferredOrderID ||
				incoming.OrderID == k12.WeeklyPracticeAttemptID(prior.RequestSnapshotID, prior.Attempt.ItemID, prior.Key)) {
			checkOrder = checkOrder || incoming.OrderID != ""
			incoming.OrderID = ""
		}
		incoming.inferredOrderID, prior.inferredOrderID = "", ""
		if !reflect.DeepEqual(prior, incoming) {
			return false
		}
	}
	return !checkOrder || sameArchivedAttemptOrder(aAttempts, bAttempts)
}

func sameArchivedAttemptOrder(prior, incoming []WeeklyHistoryAttempt) bool {
	indices := make([]int, len(prior))
	for i := range indices {
		indices[i] = i
	}
	slices.SortFunc(indices, func(i, j int) int {
		return compareArchivedAttemptOrder(&prior[i], &prior[j])
	})
	return slices.IsSortedFunc(indices, func(i, j int) int {
		return compareArchivedAttemptOrder(&incoming[i], &incoming[j])
	})
}

// 与周练历史查询使用相同的分组、作答时刻、冻结排序键与 ID 兜底。
func compareArchivedAttemptOrder(a, b *WeeklyHistoryAttempt) int {
	return cmp.Or(
		cmp.Compare(a.Attempt.ItemID, b.Attempt.ItemID),
		cmp.Compare(a.Updated, b.Updated),
		cmp.Compare(cmp.Or(a.OrderID, a.Attempt.AttemptID), cmp.Or(b.OrderID, b.Attempt.AttemptID)),
		cmp.Compare(a.Attempt.AttemptID, b.Attempt.AttemptID),
	)
}

// RollbackWeeklyHistoryTx 仅撤回原迁移新增的周；迁移后产生的作答、打印和投递记录必须保留。
func (s *Store) RollbackWeeklyHistoryTx(ctx context.Context, tx *sql.Tx, agent string, previous, imported []WeeklyHistoryArchive) error {
	existed := make(map[string]bool, len(previous))
	for _, item := range previous {
		existed[item.Snapshot.SnapshotID] = true
	}
	for _, item := range imported {
		snap := item.Snapshot
		if existed[snap.SnapshotID] {
			continue
		}
		current, err := readArchivedWeek(ctx, tx, agent, snap.SnapshotID)
		if err != nil {
			return err
		}
		if !sameArchivedWeek(current, item) {
			return fmt.Errorf("%w: migrated weekly history has later work", records.ErrVersionConflict)
		}
		var used bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM k12_weekly_practice_sends WHERE snapshot_id=?1
			UNION ALL SELECT 1 FROM k12_weekly_practice_saves WHERE snapshot_id=?1
			UNION ALL SELECT 1 FROM k12_weekly_assessment_commands WHERE snapshot_id=?1
			UNION ALL SELECT 1 FROM k12_weekly_practice_plan_commands WHERE plan_id=?4
			UNION ALL SELECT 1 FROM k12_delivery_batches WHERE agent_name=?2 AND object_kind='weekly_practice_snapshot' AND object_id=?1
			UNION ALL SELECT 1 FROM k12_generic_print_jobs WHERE agent_name=?2 AND artifact_id=?3)`,
			snap.SnapshotID, agent, snap.ArtifactID, snap.PlanID).Scan(&used)
		if err != nil {
			return err
		}
		if used {
			return fmt.Errorf("%w: migrated weekly output has later commands", records.ErrVersionConflict)
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM k12_print_artifacts WHERE artifact_id=? AND agent_name=?", snap.ArtifactID, agent); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM k12_weekly_practice_plans WHERE plan_id=? AND agent_name=? AND status='archived'", snap.PlanID, agent); err != nil {
			return err
		}
	}
	return nil
}
