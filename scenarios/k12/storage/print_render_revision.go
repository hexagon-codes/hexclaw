package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/hexagon-codes/hexclaw/records"
)

const printRenderRevisionEvent = "document.render_revision"

// GetPrintRenderRevision 读取首次冻结的重排资源，不读取当前模板。
func (s *Store) GetPrintRenderRevision(ctx context.Context, agent, artifact, key string) (string, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT payload_json FROM outbox_events WHERE event_id=? AND agent_name=? AND aggregate_id=? AND event_type=? AND payload_version=1`, FinalReplyPreparationID(agent, artifact, "render:"+key), agent, artifact, printRenderRevisionEvent).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", records.ErrNotFound
	}
	return raw, err
}

// FreezePrintRenderRevision 把显式重排请求作为不可变记录保存；不会改写原打印产物。
func (s *Store) FreezePrintRenderRevision(ctx context.Context, agent, artifact, key, raw string) (string, error) {
	if key == "" || len(key) > 256 || !json.Valid([]byte(raw)) {
		return "", ErrDeliveryBatchConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	id := FinalReplyPreparationID(agent, artifact, "render:"+key)
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox_events(event_id,agent_name,aggregate_id,event_type,payload_version,payload_json,status,attempts,last_error,created_at,updated_at)
	 SELECT ?,?,?,?,1,?,'delivered',0,'',?,? FROM k12_print_artifacts WHERE artifact_id=? AND agent_name=?
	 ON CONFLICT(event_id) DO NOTHING`, id, agent, artifact, printRenderRevisionEvent, raw, nowUnix(), nowUnix(), artifact, agent)
	if err != nil {
		return "", err
	}
	var frozen string
	if err = tx.QueryRowContext(ctx, `SELECT payload_json FROM outbox_events WHERE event_id=? AND agent_name=? AND aggregate_id=? AND event_type=? AND payload_version=1`, id, agent, artifact, printRenderRevisionEvent).Scan(&frozen); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return frozen, nil
}
