package instances

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

func photoIntakeLane(lane string) (string, error) {
	switch lane {
	case "notice":
		return "notice_json", nil
	case "work":
		return "work_json", nil
	case "final":
		return "final_json", nil
	}
	return "", fmt.Errorf("invalid IM photo operation")
}

func validPhotoTransition(lane, from, to string) bool {
	if lane == "work" {
		switch from {
		case "pending":
			return to == "running" || to == "failed"
		case "running":
			return to == "completed" || to == "unknown" || to == "failed"
		case "unknown":
			return to == "completed" || to == "failed"
		}
		return false
	}
	switch from {
	case "pending":
		return to == "sending" || to == "skipped" || (lane == "final" && to == "delegated")
	case "sending":
		return to == "accepted" || to == "delivered" || to == "unknown" || to == "failed"
	case "accepted", "unknown":
		return to == "delivered" || to == "failed" || (from == "accepted" && to == "unknown")
	case "delivered":
		return lane == "notice" && to == "recalling"
	case "recalling":
		return to == "recalled" || to == "recall_unknown" || to == "recall_failed"
	case "recall_unknown":
		return to == "recalled" || to == "recall_failed"
	case "delegated":
		return lane == "final" && (to == "delivered" || to == "failed")
	}
	return false
}

// Transition is a compare-and-set on one lane. Other operations can progress independently.
// Once an external request is sent, recovery can query its receipt but cannot claim a new send.
func (s *PhotoIntakeStore) Transition(ctx context.Context, key PhotoIntakeKey, lane, from string, next PhotoIntakeState, atMS int64) (bool, error) {
	column, err := photoIntakeLane(lane)
	if err != nil {
		return false, err
	}
	if atMS <= 0 || !validPhotoTransition(lane, from, next.Status) ||
		(next.FailureKind != "" && !validPhotoFailureKind(next.FailureKind)) {
		return false, fmt.Errorf("invalid IM photo %s transition from %q to %q", lane, from, next.Status)
	}
	var raw string
	var ready int64
	var workStatus, finalStatus string
	if err = s.db.QueryRowContext(ctx, `SELECT `+column+`,assets_ready_at_ms,json_extract(work_json,'$.status'),json_extract(final_json,'$.status') FROM im_photo_intakes WHERE `+photoKeyWhere, key.args()...).Scan(&raw, &ready, &workStatus, &finalStatus); err != nil {
		return false, err
	}
	var state PhotoIntakeState
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		return false, err
	}
	if state.Status != from || (lane == "work" && next.Status == "running" && ready == 0) {
		return false, nil
	}
	if lane == "final" && from == "pending" && workStatus != "completed" ||
		lane == "notice" && next.Status == "recalling" && finalStatus != "delivered" && finalStatus != "failed" {
		return false, nil
	}
	if state.ExternalID != "" && next.ExternalID != "" && state.ExternalID != next.ExternalID {
		return false, ErrPhotoIntakeConflict
	}
	if next.ExternalID != "" {
		state.ExternalID = next.ExternalID
	}
	if next.ResultRef != "" {
		if state.ResultRef != "" && state.ResultRef != next.ResultRef {
			return false, ErrPhotoIntakeConflict
		}
		state.ResultRef = next.ResultRef
	}
	if lane == "work" && next.Status == "completed" && state.ResultRef == "" {
		return false, fmt.Errorf("%w: completed handoff requires a durable result reference", ErrPhotoIntakeConflict)
	}
	if next.FailureKind != "" {
		state.FailureKind = next.FailureKind
	}
	// A query result never proves which message was delivered without its original handle.
	if lane != "work" && (from == "accepted" || from == "unknown") && next.Status != "unknown" && state.ExternalID == "" {
		return false, fmt.Errorf("%w: original delivery handle is unavailable", ErrPhotoIntakeConflict)
	}
	state.Status, state.UpdatedAtMS = next.Status, atMS
	switch next.Status {
	case "running", "sending":
		state.StartedAtMS = atMS
	case "accepted":
		state.AcceptedAtMS = atMS
	case "completed", "delivered":
		state.DeliveredAtMS = atMS
	case "recalling":
		state.RecallStartedAtMS = atMS
	case "recalled":
		state.RecalledAtMS = atMS
	}
	out, err := json.Marshal(state)
	if err != nil {
		return false, err
	}
	args := append([]any{string(out)}, key.args()...)
	res, err := s.db.ExecContext(ctx, `UPDATE im_photo_intakes SET `+column+`=? WHERE `+photoKeyWhere+` AND `+column+`=?`, append(args, raw)...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// Recoverable pages the indexed set of outstanding operations for one stable instance.
// A receipt with only missing media and no pending notice must await a new authenticated callback.
func (s *PhotoIntakeStore) Recoverable(ctx context.Context, instanceID, afterID string, limit int) ([]PhotoIntakeReceipt, error) {
	if instanceID == "" || limit <= 0 {
		return nil, fmt.Errorf("IM photo recovery requires a stable instance and positive limit")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT platform,instance_id,chat_id,provider_message_id FROM im_photo_intakes
		WHERE instance_id=? AND recoverable=1 AND id>? ORDER BY id LIMIT ?`, instanceID, afterID, limit)
	if err != nil {
		return nil, err
	}
	var keys []PhotoIntakeKey
	for rows.Next() {
		var k PhotoIntakeKey
		if err = rows.Scan(&k.Platform, &k.InstanceID, &k.ChatID, &k.ProviderMessageID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	res := make([]PhotoIntakeReceipt, 0, len(keys))
	for _, k := range keys {
		row, readErr := readPhotoIntake(ctx, tx, k)
		if readErr != nil {
			return nil, readErr
		}
		res = append(res, row)
	}
	return res, tx.Commit()
}
