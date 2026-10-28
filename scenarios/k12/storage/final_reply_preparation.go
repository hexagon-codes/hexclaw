package k12storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

const EventFinalReplyPreparation = "document.final_reply.prepare"

func FinalReplyPreparationID(agent, source, revision string) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + source + "\x00" + revision))
	return "document-prepare-" + hex.EncodeToString(sum[:])
}

// FreezeFinalReplyPreparation 在渲染前冻结真实来源与交付意图，不创建学生记录。
func (s *Store) FreezeFinalReplyPreparation(ctx context.Context, agent, source, revision, payload string, projection *k12.DeliveryProjectionSource) (OutboxEvent, error) {
	if projection == nil || projection.PrivateEvent == nil || projection.SourceRef != source || !json.Valid([]byte(payload)) {
		return OutboxEvent{}, ErrDeliveryBatchConflict
	}
	message, err := s.standalonePrintReplySource(ctx, k12.DeliveryBatch{AgentName: agent, ObjectKind: "im_final_reply"}, projection)
	if err != nil {
		return OutboxEvent{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OutboxEvent{}, err
	}
	defer tx.Rollback()
	locked, err := tx.ExecContext(ctx, `UPDATE messages SET id=id WHERE id=? AND session_id=? AND request_id=? AND metadata=? AND content=?`, message.ID, message.SessionID, message.RequestID, message.Metadata, message.Content)
	if err != nil {
		return OutboxEvent{}, err
	}
	if count, _ := locked.RowsAffected(); count != 1 {
		return OutboxEvent{}, records.ErrNotFound
	}
	var metadata map[string]any
	_ = json.Unmarshal([]byte(message.Metadata), &metadata)
	if metadata["print_reply_digest"] == nil {
		if _, err = acceptedConversationReplySourceVia(ctx, tx, agent, projection); err != nil {
			return OutboxEvent{}, err
		}
	}
	id := FinalReplyPreparationID(agent, source, revision)
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox_events(event_id,agent_name,aggregate_id,event_type,payload_version,payload_json,status,attempts,last_error,created_at,updated_at)
	 VALUES(?,?,?,?,1,?,'pending',0,'',?,?) ON CONFLICT(event_id) DO NOTHING`, id, agent, source, EventFinalReplyPreparation, payload, nowUnix(), nowUnix())
	if err != nil {
		return OutboxEvent{}, err
	}
	event, err := getFinalReplyPreparationVia(ctx, tx, agent, id)
	if err != nil {
		return event, err
	}
	if err = tx.Commit(); err != nil {
		return OutboxEvent{}, err
	}
	return event, nil
}

func getFinalReplyPreparationVia(ctx context.Context, q dbQueryer, agent, id string) (OutboxEvent, error) {
	var event OutboxEvent
	err := q.QueryRowContext(ctx, `SELECT event_id,agent_name,aggregate_id,event_type,payload_version,payload_json,status,attempts,last_error,created_at,updated_at FROM outbox_events WHERE event_id=? AND agent_name=? AND event_type=?`, id, agent, EventFinalReplyPreparation).
		Scan(&event.EventID, &event.AgentName, &event.AggregateID, &event.EventType, &event.PayloadVersion, &event.Payload, &event.Status, &event.Attempts, &event.LastError, &event.CreatedAt, &event.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return event, records.ErrNotFound
	}
	return event, err
}

func (s *Store) GetFinalReplyPreparation(ctx context.Context, agent, source, revision string) (OutboxEvent, error) {
	return getFinalReplyPreparationVia(ctx, s.db, agent, FinalReplyPreparationID(agent, source, revision))
}

func (s *Store) MarkFinalReplyPreparation(ctx context.Context, agent, id string, complete bool, detail string) error {
	status := "pending"
	if complete {
		status = "delivered"
		detail = ""
	}
	_, err := s.db.ExecContext(ctx, `UPDATE outbox_events SET status=?,last_error=?,updated_at=? WHERE agent_name=? AND event_id=? AND event_type=?`, status, detail, nowUnix(), agent, id, EventFinalReplyPreparation)
	return err
}

// RetryKnownFinalReplyPreparation 仅重置已证明零发送的明确上传失败，未知结果保持原态。
func (s *Store) RetryKnownFinalReplyPreparation(ctx context.Context, agent, batchID string) (k12.DeliveryBatch, error) {
	batch, err := s.GetDeliveryBatch(ctx, agent, batchID)
	if err != nil {
		return batch, err
	}
	if !PDFPreparationFallbackAllowed(batch) {
		return batch, ErrDeliveryBatchConflict
	}
	_, err = s.db.ExecContext(ctx, `UPDATE k12_delivery_receipts SET status='pending',last_error='',updated_at=? WHERE agent_name=? AND batch_id=? AND status='failed' AND last_error=? AND attempt=0 AND external_message_id=''
	 AND NOT EXISTS(SELECT 1 FROM k12_delivery_receipts r WHERE r.agent_name=? AND r.batch_id=? AND (r.attempt<>0 OR r.external_message_id<>'' OR r.status<>'failed' OR r.last_error<>?))`, nowUnix(), agent, batchID, PDFPreparationKnownFailure, agent, batchID, PDFPreparationKnownFailure)
	if err != nil {
		return batch, err
	}
	return s.GetDeliveryBatch(ctx, agent, batchID)
}
