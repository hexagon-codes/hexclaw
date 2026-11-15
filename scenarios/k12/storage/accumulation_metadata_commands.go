package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hexagon-codes/hexclaw/records"
)

// AccumulationMetadataCommand 保存一次创建意图的派生进度；最终对象仍由原创建回执拥有。
type AccumulationMetadataCommand struct {
	AgentName, CommandKey, RequestDigest, GradeTerm, PolicyVersion  string
	RouteSnapshotJSON, RequestPolicyJSON                            string
	State                                                           string
	Attempt                                                         int
	ExecutionID                                                     string
	LeaseExpiresAt                                                  int64
	RawResponse, RawDigest, MetadataJSON, FailureKind, ReceiptsJSON string
	CreatedAt, UpdatedAt                                            int64
}

const accumulationMetadataColumns = `agent_name,command_key,request_digest,grade_term,policy_version,
 route_snapshot_json,request_policy_json,state,attempt,execution_id,lease_expires_at,
 raw_response,raw_digest,metadata_json,failure_kind,attempt_receipts_json,created_at,updated_at`

func scanAccumulationMetadataCommand(row rowScanner) (AccumulationMetadataCommand, error) {
	var command AccumulationMetadataCommand
	err := row.Scan(&command.AgentName, &command.CommandKey, &command.RequestDigest, &command.GradeTerm, &command.PolicyVersion,
		&command.RouteSnapshotJSON, &command.RequestPolicyJSON, &command.State, &command.Attempt, &command.ExecutionID, &command.LeaseExpiresAt,
		&command.RawResponse, &command.RawDigest, &command.MetadataJSON, &command.FailureKind, &command.ReceiptsJSON, &command.CreatedAt, &command.UpdatedAt)
	return command, err
}

func (s *Store) GetAccumulationMetadataCommand(ctx context.Context, agent, key string) (AccumulationMetadataCommand, error) {
	command, err := scanAccumulationMetadataCommand(s.db.QueryRowContext(ctx, `SELECT `+accumulationMetadataColumns+`
 FROM k12_accumulation_metadata_commands WHERE agent_name=? AND command_key=?`, agent, key))
	if errors.Is(err, sql.ErrNoRows) {
		err = records.ErrNotFound
	}
	return command, err
}

func (s *Store) PrepareAccumulationMetadataCommand(ctx context.Context, command AccumulationMetadataCommand) (AccumulationMetadataCommand, error) {
	if command.AgentName == "" || command.CommandKey == "" || len(command.RequestDigest) != 71 || command.PolicyVersion == "" || command.CreatedAt <= 0 {
		return command, records.ErrInvalidFields
	}
	if err := ensureAgentRegistered(ctx, s.db, command.AgentName); err != nil {
		return command, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO k12_accumulation_metadata_commands
 (agent_name,command_key,request_digest,grade_term,policy_version,state,attempt,created_at,updated_at)
 VALUES(?,?,?,?,?,'prepared',1,?,?) ON CONFLICT(agent_name,command_key) DO NOTHING`, command.AgentName, command.CommandKey,
		command.RequestDigest, command.GradeTerm, command.PolicyVersion, command.CreatedAt, command.CreatedAt)
	if err != nil {
		return command, err
	}
	stored, err := s.GetAccumulationMetadataCommand(ctx, command.AgentName, command.CommandKey)
	if err == nil && stored.RequestDigest != command.RequestDigest {
		return stored, ErrCurrentCommandConflict
	}
	return stored, err
}

// ClaimAccumulationMetadataCommand 只回收明确未发的过期执行；已发送或未知不能按时间重发。
func (s *Store) ClaimAccumulationMetadataCommand(ctx context.Context, current AccumulationMetadataCommand, execution string, at, until int64) (AccumulationMetadataCommand, bool, error) {
	if execution == "" || at <= 0 || until <= at {
		return current, false, records.ErrInvalidFields
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return current, false, err
	}
	defer tx.Rollback()
	current, err = scanAccumulationMetadataCommand(tx.QueryRowContext(ctx, `SELECT `+accumulationMetadataColumns+`
 FROM k12_accumulation_metadata_commands WHERE agent_name=? AND command_key=?`, current.AgentName, current.CommandKey))
	if err != nil {
		return current, false, err
	}
	if current.State != "prepared" && current.State != "failed" || current.State == "prepared" && current.ExecutionID != "" && current.LeaseExpiresAt > at {
		return current, false, tx.Commit()
	}
	attempt := current.Attempt
	receipts := current.ReceiptsJSON
	if current.ExecutionID != "" {
		var history []json.RawMessage
		if err := json.Unmarshal([]byte(receipts), &history); err != nil {
			return current, false, err
		}
		previous, err := json.Marshal(struct {
			Attempt     int    `json:"attempt"`
			ExecutionID string `json:"execution_id"`
			State       string `json:"state"`
			FailureKind string `json:"failure_kind,omitempty"`
			RawResponse string `json:"raw_response,omitempty"`
			RawDigest   string `json:"raw_digest,omitempty"`
			UpdatedAt   int64  `json:"updated_at"`
		}{current.Attempt, current.ExecutionID, current.State, current.FailureKind, current.RawResponse, current.RawDigest, current.UpdatedAt})
		if err != nil {
			return current, false, err
		}
		history = append(history, previous)
		raw, err := json.Marshal(history)
		if err != nil {
			return current, false, err
		}
		receipts, attempt = string(raw), attempt+1
	}
	result, err := tx.ExecContext(ctx, `UPDATE k12_accumulation_metadata_commands
 SET state='prepared',attempt=?,execution_id=?,lease_expires_at=?,failure_kind='',raw_response='',raw_digest='',metadata_json='',attempt_receipts_json=?,updated_at=?
 WHERE agent_name=? AND command_key=? AND attempt=? AND execution_id=? AND state=?`, attempt, execution, until, receipts, at,
		current.AgentName, current.CommandKey, current.Attempt, current.ExecutionID, current.State)
	if err != nil {
		return current, false, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return current, false, errors.Join(records.ErrVersionConflict, err)
	}
	if err := tx.Commit(); err != nil {
		return current, false, err
	}
	current, err = s.GetAccumulationMetadataCommand(ctx, current.AgentName, current.CommandKey)
	return current, err == nil && current.ExecutionID == execution, err
}

func (s *Store) FreezeAccumulationMetadataRequest(ctx context.Context, command AccumulationMetadataCommand, routeJSON, requestJSON string, at int64) error {
	if !json.Valid([]byte(routeJSON)) || !json.Valid([]byte(requestJSON)) {
		return records.ErrInvalidFields
	}
	result, err := s.db.ExecContext(ctx, `UPDATE k12_accumulation_metadata_commands
 SET route_snapshot_json=?,request_policy_json=?,updated_at=?
 WHERE agent_name=? AND command_key=? AND execution_id=? AND attempt=? AND state='prepared' AND lease_expires_at>?
 AND (route_snapshot_json='' OR route_snapshot_json=?) AND (request_policy_json='' OR request_policy_json=?)`, routeJSON, requestJSON, at,
		command.AgentName, command.CommandKey, command.ExecutionID, command.Attempt, at, routeJSON, requestJSON)
	return accumulationMetadataCAS(result, err)
}

func (s *Store) MarkAccumulationMetadataSent(ctx context.Context, command AccumulationMetadataCommand, at int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE k12_accumulation_metadata_commands SET state='sent',updated_at=?
 WHERE agent_name=? AND command_key=? AND execution_id=? AND attempt=? AND state='prepared' AND lease_expires_at>?`, at,
		command.AgentName, command.CommandKey, command.ExecutionID, command.Attempt, at)
	return accumulationMetadataCAS(result, err)
}

func (s *Store) SaveAccumulationMetadataRaw(ctx context.Context, command AccumulationMetadataCommand, raw, digest string, at int64) error {
	if digest != sha256Hex([]byte(raw)) {
		return records.ErrInvalidFields
	}
	result, err := s.db.ExecContext(ctx, `UPDATE k12_accumulation_metadata_commands
 SET state='raw_received',raw_response=?,raw_digest=?,updated_at=?
 WHERE agent_name=? AND command_key=? AND execution_id=? AND attempt=?
 AND (state='sent' OR (state='raw_received' AND raw_response=? AND raw_digest=?))`, raw, digest, at,
		command.AgentName, command.CommandKey, command.ExecutionID, command.Attempt, raw, digest)
	return accumulationMetadataCAS(result, err)
}

func (s *Store) SaveAccumulationMetadataResult(ctx context.Context, command AccumulationMetadataCommand, raw string, at int64) error {
	if !json.Valid([]byte(raw)) {
		return records.ErrInvalidFields
	}
	result, err := s.db.ExecContext(ctx, `UPDATE k12_accumulation_metadata_commands
 SET state='metadata_ready',metadata_json=?,updated_at=?
 WHERE agent_name=? AND command_key=? AND execution_id=? AND attempt=?
 AND (state IN ('sent','raw_received') OR (state='metadata_ready' AND metadata_json=?))`, raw, at,
		command.AgentName, command.CommandKey, command.ExecutionID, command.Attempt, raw)
	return accumulationMetadataCAS(result, err)
}

func (s *Store) FailAccumulationMetadataCommand(ctx context.Context, command AccumulationMetadataCommand, unknown bool, kind string, at int64) error {
	state := "failed"
	states := "('prepared','sent','raw_received')"
	if unknown {
		state = "outcome_unknown"
		states = "('prepared','sent')"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE k12_accumulation_metadata_commands SET state=?,failure_kind=?,updated_at=?
 WHERE agent_name=? AND command_key=? AND execution_id=? AND attempt=? AND state IN `+states, state, kind, at,
		command.AgentName, command.CommandKey, command.ExecutionID, command.Attempt)
	return accumulationMetadataCAS(result, err)
}

func accumulationMetadataCAS(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("%w: accumulation metadata execution changed", records.ErrVersionConflict)
	}
	return nil
}
