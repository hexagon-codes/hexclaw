package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/storage"
)

// MediaRepository 复用应用数据库，媒体操作不会伪造会话记录。
type MediaRepository struct{ db *sql.DB }

// SuccessfulModelKinds 为配置目录返回精确实例上已验证的媒体种类，不返回内容或凭据。
func (r *MediaRepository) SuccessfulModelKinds(ctx context.Context, providerID string) (map[string][]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT json_extract(data,'$.route.model'),json_extract(data,'$.request.kind') FROM media_operations WHERE COALESCE(NULLIF(json_extract(data,'$.actual_provider_instance_id'),''),json_extract(data,'$.route.provider_instance_id'))=? AND json_extract(data,'$.generation_state')='succeeded' AND json_extract(data,'$.save_state')='ready' AND COALESCE(json_extract(data,'$.route.model'),'')<>''`, providerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make(map[string][]string)
	for rows.Next() {
		var model, kind string
		if err := rows.Scan(&model, &kind); err != nil {
			return nil, err
		}
		result[model] = append(result[model], kind)
	}
	return result, rows.Err()
}

// commitMediaReply 与原消息写入共用事务，完整资产关联和投递意图一起提交。
func commitMediaReply(ctx context.Context, tx *sql.Tx, msg *storage.MessageRecord) (bool, error) {
	c, ok := mediaflow.ReplyCompletionFromContext(ctx)
	if !ok {
		return false, nil
	}
	if c.Owner == "" || c.SessionID != msg.SessionID || c.RequestID != msg.RequestID || msg.Role != "assistant" {
		return false, fmt.Errorf("media reply origin mismatch")
	}
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM sessions WHERE id=?`, msg.SessionID).Scan(&owner); err != nil {
		return false, err
	}
	if owner != c.Owner {
		return false, fmt.Errorf("media reply owner mismatch")
	}
	for _, asset := range c.Assets {
		var data string
		if err := tx.QueryRowContext(ctx, `SELECT data FROM media_assets WHERE id=? AND owner=? AND operation_id=?`, asset.ID, c.Owner, asset.OperationID).Scan(&data); err != nil {
			return false, err
		}
		var stored mediaflow.Asset
		if json.Unmarshal([]byte(data), &stored) != nil || stored != asset {
			return false, fmt.Errorf("media reply asset mismatch")
		}
	}
	encoded, err := json.Marshal(struct {
		Content string
		Assets  []mediaflow.Asset
	}{msg.Content, c.Assets})
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(encoded)
	completionDigest := hex.EncodeToString(sum[:])
	var previousMeta string
	err = tx.QueryRowContext(ctx, `SELECT metadata FROM messages WHERE id=? AND session_id=? AND role='assistant'`, msg.ID, msg.SessionID).Scan(&previousMeta)
	if err == nil {
		var metadata map[string]any
		if json.Unmarshal([]byte(previousMeta), &metadata) != nil || metadata["media_completion_digest"] != completionDigest {
			return false, mediaflow.ErrConflict
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	// 与助手消息使用同一事务裁决停止意图，已完成回放和显式重发不重新结算。
	for _, id := range c.Operations {
		if slices.Contains(c.ResendOperations, id) {
			continue
		}
		var cancelled int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(data,'$.cancel_requested'),0) FROM media_operations WHERE id=? AND owner=?`, id, c.Owner).Scan(&cancelled); err != nil {
			return false, err
		}
		if cancelled != 0 {
			return false, context.Canceled
		}
	}
	metadata := make(map[string]any)
	if msg.Metadata != "" && json.Unmarshal([]byte(msg.Metadata), &metadata) != nil {
		return false, fmt.Errorf("invalid media reply metadata")
	}
	metadata["media_completion_digest"] = completionDigest
	metadata["media_assets"] = c.Assets
	if c.Platform != "" && c.InstanceID != "" && c.ChatID != "" {
		id := mediaflow.DeliveryID(c)
		metadata["media_delivery_id"] = id
		payload, err := json.Marshal(c)
		if err != nil {
			return false, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO media_deliveries(id,owner,message_id,completion_key,content_digest,platform,instance_id,chat_id,state,data,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(owner,completion_key,platform,instance_id,chat_id) DO NOTHING`, id, c.Owner, msg.ID, c.RequestID, completionDigest, c.Platform, c.InstanceID, c.ChatID, "pending", string(payload), time.Now().UTC())
		if err != nil {
			return false, err
		}
		var storedDigest string
		if err := tx.QueryRowContext(ctx, `SELECT content_digest FROM media_deliveries WHERE id=?`, id).Scan(&storedDigest); err != nil {
			return false, err
		}
		if storedDigest != completionDigest {
			return false, mediaflow.ErrConflict
		}
	}
	encoded, err = json.Marshal(metadata)
	if err != nil {
		return false, err
	}
	msg.Metadata = string(encoded)
	return false, nil
}

func (s *Store) MediaRepository() *MediaRepository { return &MediaRepository{db: s.db} }

func (r *MediaRepository) AuthorizePath(ctx context.Context, owner, path string) (bool, error) {
	var count, owned int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(MAX(owner=?),0) FROM media_assets WHERE json_extract(data,'$.file_path')=?`, owner, path).Scan(&count, &owned); err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	if owned == 0 {
		return true, mediaflow.ErrNotFound
	}
	return true, nil
}

func scanMediaDelivery(scanner interface{ Scan(...any) error }) (*mediaflow.Delivery, error) {
	var id, owner, messageID, state, data string
	var revision int64
	if err := scanner.Scan(&id, &owner, &messageID, &state, &revision, &data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, mediaflow.ErrNotFound
		}
		return nil, err
	}
	var delivery mediaflow.Delivery
	if err := json.Unmarshal([]byte(data), &delivery); err != nil {
		return nil, err
	}
	delivery.ID, delivery.Owner, delivery.MessageID, delivery.State, delivery.Revision = id, owner, messageID, state, revision
	return &delivery, nil
}

func (r *MediaRepository) Delivery(ctx context.Context, owner, id string) (*mediaflow.Delivery, error) {
	return scanMediaDelivery(r.db.QueryRowContext(ctx, `SELECT id,owner,message_id,state,revision,data FROM media_deliveries WHERE id=? AND owner=?`, id, owner))
}

func (r *MediaRepository) SaveDelivery(ctx context.Context, d *mediaflow.Delivery, expected int64) error {
	copyDelivery := *d
	copyDelivery.Parts = append([]mediaflow.DeliveryPart(nil), d.Parts...)
	copyDelivery.Revision = expected + 1
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, part := range copyDelivery.Parts {
		if part.State != "sending" {
			continue
		}
		for _, id := range copyDelivery.Operations {
			if slices.Contains(copyDelivery.ResendOperations, id) {
				continue
			}
			var cancelled int
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(data,'$.cancel_requested'),0) FROM media_operations WHERE id=? AND owner=?`, id, d.Owner).Scan(&cancelled); err != nil {
				return err
			}
			if cancelled != 0 {
				copyDelivery.State = "cancelled"
				for i := range copyDelivery.Parts {
					if copyDelivery.Parts[i].State == "sending" {
						copyDelivery.Parts[i].State = "prepared"
					}
				}
				break
			}
		}
		break
	}
	data, err := json.Marshal(copyDelivery)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE media_deliveries SET state=?,revision=?,data=?,updated_at=? WHERE id=? AND owner=? AND revision=?`, copyDelivery.State, copyDelivery.Revision, string(data), time.Now().UTC(), d.ID, d.Owner, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return mediaflow.ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*d = copyDelivery
	return nil
}

func (r *MediaRepository) PendingDeliveries(ctx context.Context, limit int) ([]*mediaflow.Delivery, error) {
	return r.PendingDeliveriesAfter(ctx, "", limit)
}

// PendingDeliveriesAfter 按稳定身份分页，回执查询失败不会永久遮住后续投递。
func (r *MediaRepository) PendingDeliveriesAfter(ctx context.Context, afterID string, limit int) ([]*mediaflow.Delivery, error) {
	if limit <= 0 {
		limit = -1
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,owner,message_id,state,revision,data FROM media_deliveries WHERE id>? AND state NOT IN ('delivered','cancelled') AND (state IN ('pending','accepted') OR EXISTS(SELECT 1 FROM json_each(media_deliveries.data,'$.parts') p WHERE json_extract(p.value,'$.state') IN ('accepted','unknown') AND COALESCE(json_extract(p.value,'$.receipt_ref'),'')<>'')) ORDER BY id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []*mediaflow.Delivery
	for rows.Next() {
		record, err := scanMediaDelivery(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (r *MediaRepository) DeliveriesForOperation(ctx context.Context, owner, operationID string) ([]*mediaflow.Delivery, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,owner,message_id,state,revision,data FROM media_deliveries WHERE owner=? AND EXISTS(SELECT 1 FROM json_each(media_deliveries.data,'$.Operations') WHERE value=?) ORDER BY updated_at`, owner, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []*mediaflow.Delivery
	for rows.Next() {
		record, err := scanMediaDelivery(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (r *MediaRepository) InterruptDeliveries(ctx context.Context) error {
	records, err := r.PendingDeliveries(ctx, 0)
	if err != nil {
		return err
	}
	for _, record := range records {
		changed := false
		for i := range record.Parts {
			part := &record.Parts[i]
			if part.State == "preparing" || part.State == "sending" {
				part.State = "unknown"
				record.State = "unknown"
				changed = true
			}
		}
		if changed {
			if err := r.SaveDelivery(ctx, record, record.Revision); err != nil {
				return err
			}
		}
	}
	return nil
}

func readMedia(row *sql.Row) (*mediaflow.Operation, error) {
	var data string
	if err := row.Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, mediaflow.ErrNotFound
		}
		return nil, err
	}
	var op mediaflow.Operation
	if err := json.Unmarshal([]byte(data), &op); err != nil {
		return nil, err
	}
	return &op, nil
}
func (r *MediaRepository) Find(ctx context.Context, owner, key string) (*mediaflow.Operation, error) {
	return readMedia(r.db.QueryRowContext(ctx, `SELECT data FROM media_operations WHERE owner=? AND idempotency_key=?`, owner, key))
}

func (r *MediaRepository) ListForRequest(ctx context.Context, owner string, origin mediaflow.Origin) ([]*mediaflow.Operation, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT data FROM media_operations WHERE owner=? AND json_extract(data,'$.request.origin.kind')=? AND json_extract(data,'$.request.origin.session_id')=? AND json_extract(data,'$.request.origin.request_id')=? ORDER BY updated_at,id`, owner, origin.Kind, origin.SessionID, origin.RequestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []*mediaflow.Operation
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var op mediaflow.Operation
		if err := json.Unmarshal([]byte(data), &op); err != nil {
			return nil, err
		}
		records = append(records, &op)
	}
	return records, rows.Err()
}

func (r *MediaRepository) UnfinishedReplies(ctx context.Context, limit int) ([]*mediaflow.Operation, error) {
	return r.UnfinishedRepliesAfter(ctx, "", limit)
}

func (r *MediaRepository) UnfinishedRepliesAfter(ctx context.Context, afterID string, limit int) ([]*mediaflow.Operation, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT o.data FROM media_operations o WHERE o.id>? AND json_extract(o.data,'$.request.origin.kind')='conversation' AND COALESCE(json_extract(o.data,'$.cancel_requested'),0)=0 AND o.generation_state IN ('succeeded','failed','unknown') AND NOT EXISTS(SELECT 1 FROM messages m WHERE m.role='assistant' AND m.session_id=json_extract(o.data,'$.request.origin.session_id') AND m.request_id=json_extract(o.data,'$.request.origin.request_id')) ORDER BY o.id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ops []*mediaflow.Operation
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var op mediaflow.Operation
		if err := json.Unmarshal([]byte(data), &op); err != nil {
			return nil, err
		}
		ops = append(ops, &op)
	}
	return ops, rows.Err()
}
func (r *MediaRepository) Get(ctx context.Context, owner, id string) (*mediaflow.Operation, error) {
	return readMedia(r.db.QueryRowContext(ctx, `SELECT data FROM media_operations WHERE owner=? AND id=?`, owner, id))
}
func (r *MediaRepository) Create(ctx context.Context, op *mediaflow.Operation) (*mediaflow.Operation, error) {
	op.Revision = 1
	data, err := json.Marshal(op)
	if err != nil {
		return nil, err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO media_operations(id,owner,idempotency_key,generation_state,save_state,revision,data,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(owner,idempotency_key) DO NOTHING`, op.ID, op.Owner, op.IdempotencyKey, op.GenerationState, op.SaveState, op.Revision, string(data), op.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return r.Find(ctx, op.Owner, op.IdempotencyKey)
}
func (r *MediaRepository) Update(ctx context.Context, op *mediaflow.Operation, expected int64) error {
	copyOp := *op
	copyOp.Revision = expected + 1
	copyOp.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(&copyOp)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE media_operations SET generation_state=?,save_state=?,revision=?,data=?,updated_at=? WHERE id=? AND owner=? AND revision=?`, copyOp.GenerationState, copyOp.SaveState, copyOp.Revision, string(data), copyOp.UpdatedAt, op.ID, op.Owner, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return mediaflow.ErrConflict
	}
	for _, asset := range copyOp.Assets {
		encoded, err := json.Marshal(asset)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO media_assets(id,operation_id,owner,data) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING`, asset.ID, op.ID, op.Owner, string(encoded))
		if err != nil {
			return err
		}
		var previous string
		if err := tx.QueryRowContext(ctx, `SELECT data FROM media_assets WHERE id=? AND operation_id=? AND owner=?`, asset.ID, op.ID, op.Owner).Scan(&previous); err != nil {
			return err
		}
		if previous != string(encoded) {
			return fmt.Errorf("immutable media asset conflict")
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*op = copyOp
	return nil
}
func (r *MediaRepository) Pending(ctx context.Context, limit int) ([]*mediaflow.Operation, error) {
	return r.PendingAfter(ctx, "", limit)
}

// PendingAfter 用稳定身份分页，暂时无法推进的旧操作不会遮住后续任务。
func (r *MediaRepository) PendingAfter(ctx context.Context, after string, limit int) ([]*mediaflow.Operation, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT data FROM media_operations WHERE id>? AND (generation_state IN ('queued','submitting','running') OR (generation_state='succeeded' AND save_state IN ('saving','not_started'))) ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var operations []*mediaflow.Operation
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var op mediaflow.Operation
		if err := json.Unmarshal([]byte(data), &op); err != nil {
			return nil, err
		}
		operations = append(operations, &op)
	}
	return operations, rows.Err()
}
func (r *MediaRepository) Interrupt(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `SELECT data FROM media_operations WHERE generation_state='submitting'`)
	if err != nil {
		return err
	}
	var operations []*mediaflow.Operation
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			_ = rows.Close()
			return err
		}
		var op mediaflow.Operation
		if err := json.Unmarshal([]byte(data), &op); err != nil {
			_ = rows.Close()
			return err
		}
		operations = append(operations, &op)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, op := range operations {
		op.GenerationState = "unknown"
		op.ErrorCode = "upstream_unknown"
		op.Error = "generation interrupted without a durable upstream reference"
		if err := r.Update(ctx, op, op.Revision); err != nil {
			return err
		}
	}
	return nil
}
