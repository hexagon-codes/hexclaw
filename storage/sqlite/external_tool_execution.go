package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/storage"
)

type ExternalToolExecutionRepository struct{ db *sql.DB }

// ExternalToolExecutionRepository 复用应用数据库，不在工具内部维护第二套回执。
func (s *Store) ExternalToolExecutionRepository() storage.ExternalToolExecutionRepository {
	return &ExternalToolExecutionRepository{db: s.db}
}

var _ storage.ExternalToolExecutionRepository = (*ExternalToolExecutionRepository)(nil)

const externalExecutionColumns = `execution_id,owner_id,session_id,request_id,run_id,tool_call_id,
agent_name,dispatch_task_ref,kind,logical_call_key,attempt,parent_execution_id,model_exchange_id,
model_exchange_ordinal,upstream_call_id,action_index,tool_name,server_name,target_id,input_digest,
browser_generation,policy_profile,policy_revision,phase,outcome,reason,result_manifest_json,
accepted_response,revision,created_at,updated_at`

func scanExternalExecution(row rowScanner) (storage.ExternalToolExecution, error) {
	var record storage.ExternalToolExecution
	var manifest string
	err := row.Scan(&record.ID, &record.OwnerID, &record.SessionID, &record.RequestID, &record.RunID,
		&record.ToolCallID, &record.AgentName, &record.DispatchTaskRef, &record.Kind, &record.LogicalCallKey,
		&record.Attempt, &record.ParentExecutionID, &record.ModelExchangeID, &record.ModelExchangeOrdinal,
		&record.UpstreamCallID, &record.ActionIndex, &record.ToolName, &record.ServerName, &record.TargetID,
		&record.InputDigest, &record.BrowserGeneration, &record.PolicyProfile, &record.PolicyRevision,
		&record.Phase, &record.Outcome, &record.Reason, &manifest, &record.AcceptedResponse,
		&record.Revision, &record.CreatedAt, &record.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return record, storage.ErrNotFound
	}
	if err == nil {
		record.ResultManifestJSON = json.RawMessage(manifest)
	}
	return record, err
}

func validateExternalManifest(manifest json.RawMessage) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(manifest, &object); err != nil || object == nil {
		return fmt.Errorf("external execution manifest must be a JSON object")
	}
	return nil
}

// Prepare 保存派发前意图；同逻辑键的不同尝试允许采用更新后的权限快照。
func (r *ExternalToolExecutionRepository) Prepare(ctx context.Context, record storage.ExternalToolExecution) (storage.ExternalToolExecution, error) {
	if record.ID == "" || record.OwnerID == "" || record.Kind == "" || record.LogicalCallKey == "" || record.InputDigest == "" {
		return record, fmt.Errorf("external execution identity and input digest are required")
	}
	if record.ModelExchangeOrdinal > math.MaxInt64 || record.BrowserGeneration > math.MaxInt64 || record.PolicyRevision > math.MaxInt64 {
		return record, fmt.Errorf("external execution numeric identity is out of range")
	}
	if record.Attempt == 0 {
		record.Attempt = 1
	}
	if record.Phase != "" && record.Phase != storage.ExternalExecutionPrepared || record.AcceptedResponse || record.Outcome != "" {
		return record, fmt.Errorf("external execution must start in prepared phase")
	}
	if record.Kind == "model_request" && (record.ModelExchangeID == "" || record.ModelExchangeOrdinal == 0) {
		return record, fmt.Errorf("model request exchange identity is required before dispatch")
	}
	if len(record.ResultManifestJSON) == 0 {
		record.ResultManifestJSON = json.RawMessage(`{}`)
	}
	if err := validateExternalManifest(record.ResultManifestJSON); err != nil {
		return record, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return record, err
	}
	defer tx.Rollback()
	latest, err := scanExternalExecution(tx.QueryRowContext(ctx, `SELECT `+externalExecutionColumns+`
FROM external_tool_executions WHERE owner_id=? AND kind=? AND logical_call_key=? ORDER BY attempt DESC LIMIT 1`,
		record.OwnerID, record.Kind, record.LogicalCallKey))
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return record, err
	}
	if err == nil {
		if !sameExternalExecutionIntent(latest, record) {
			return record, storage.ErrExternalExecutionConflict
		}
		if record.Attempt <= latest.Attempt {
			existing, err := scanExternalExecution(tx.QueryRowContext(ctx, `SELECT `+externalExecutionColumns+`
FROM external_tool_executions WHERE owner_id=? AND kind=? AND logical_call_key=? AND attempt=?`,
				record.OwnerID, record.Kind, record.LogicalCallKey, record.Attempt))
			if err != nil {
				return record, err
			}
			if err := tx.Commit(); err != nil {
				return existing, err
			}
			return existing, nil
		}
		if uint64(record.Attempt) != uint64(latest.Attempt)+1 {
			return record, storage.ErrExternalExecutionConflict
		}
	} else if record.Attempt != 1 {
		return record, storage.ErrExternalExecutionConflict
	}
	// 标识来自调用者可信运行时；时间和初始revision由存储统一生成。
	now := time.Now().UnixMilli()
	record.Phase = storage.ExternalExecutionPrepared
	record.Revision = 1
	record.CreatedAt = now
	record.UpdatedAt = now
	_, err = tx.ExecContext(ctx, `INSERT INTO external_tool_executions (`+externalExecutionColumns+`)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		record.ID, record.OwnerID, record.SessionID, record.RequestID, record.RunID, record.ToolCallID,
		record.AgentName, record.DispatchTaskRef, record.Kind, record.LogicalCallKey, record.Attempt,
		record.ParentExecutionID, record.ModelExchangeID, record.ModelExchangeOrdinal, record.UpstreamCallID,
		record.ActionIndex, record.ToolName, record.ServerName, record.TargetID, record.InputDigest,
		record.BrowserGeneration, record.PolicyProfile, record.PolicyRevision, record.Phase, record.Outcome,
		record.Reason, string(record.ResultManifestJSON), record.AcceptedResponse, record.Revision, record.CreatedAt, record.UpdatedAt)
	if err != nil {
		return record, fmt.Errorf("prepare external execution: %w", err)
	}
	return record, tx.Commit()
}

func sameExternalExecutionIntent(a, b storage.ExternalToolExecution) bool {
	return a.InputDigest == b.InputDigest && a.SessionID == b.SessionID && a.RequestID == b.RequestID &&
		a.RunID == b.RunID && a.ToolCallID == b.ToolCallID && a.AgentName == b.AgentName &&
		a.DispatchTaskRef == b.DispatchTaskRef && a.ParentExecutionID == b.ParentExecutionID &&
		a.ModelExchangeID == b.ModelExchangeID && a.ModelExchangeOrdinal == b.ModelExchangeOrdinal &&
		a.ActionIndex == b.ActionIndex && a.ToolName == b.ToolName && a.ServerName == b.ServerName && a.TargetID == b.TargetID
}

func (r *ExternalToolExecutionRepository) Get(ctx context.Context, owner, id string) (storage.ExternalToolExecution, error) {
	if owner == "" || id == "" {
		return storage.ExternalToolExecution{}, storage.ErrNotFound
	}
	return scanExternalExecution(r.db.QueryRowContext(ctx, `SELECT `+externalExecutionColumns+`
FROM external_tool_executions WHERE owner_id=? AND execution_id=?`, owner, id))
}

func (r *ExternalToolExecutionRepository) Latest(ctx context.Context, owner, kind, key string) (storage.ExternalToolExecution, error) {
	if owner == "" || kind == "" || key == "" {
		return storage.ExternalToolExecution{}, storage.ErrNotFound
	}
	return scanExternalExecution(r.db.QueryRowContext(ctx, `SELECT `+externalExecutionColumns+`
FROM external_tool_executions WHERE owner_id=? AND kind=? AND logical_call_key=? ORDER BY attempt DESC LIMIT 1`, owner, kind, key))
}

func (r *ExternalToolExecutionRepository) List(ctx context.Context, filter storage.ExternalToolExecutionFilter) ([]storage.ExternalToolExecution, error) {
	if filter.OwnerID == "" {
		return nil, fmt.Errorf("external execution owner is required")
	}
	query := `SELECT ` + externalExecutionColumns + ` FROM external_tool_executions WHERE owner_id=?`
	args := []any{filter.OwnerID}
	for _, part := range []struct{ column, value string }{
		{"session_id", filter.SessionID}, {"request_id", filter.RequestID}, {"parent_execution_id", filter.ParentExecutionID},
		{"model_exchange_id", filter.ModelExchangeID}, {"kind", filter.Kind}, {"phase", filter.Phase},
		{"dispatch_task_ref", filter.DispatchTaskRef},
	} {
		if part.value != "" {
			query += " AND " + part.column + "=?"
			args = append(args, part.value)
		}
	}
	if filter.ArtifactID != "" {
		query += ` AND EXISTS (
SELECT 1 FROM json_each(CASE WHEN json_valid(result_manifest_json) THEN result_manifest_json ELSE '{}' END,'$.artifacts') artifact
WHERE artifact.type='object' AND json_extract(artifact.value,'$.id')=?)`
		args = append(args, filter.ArtifactID)
	}
	if intent := filter.Intent; intent != nil {
		for _, part := range []struct{ column, value string }{
			{"request_id", intent.RequestID}, {"dispatch_task_ref", intent.DispatchTaskRef},
			{"tool_name", intent.ToolName}, {"server_name", intent.ServerName},
			{"target_id", intent.TargetID}, {"input_digest", intent.InputDigest},
		} {
			query += " AND " + part.column + "=?"
			args = append(args, part.value)
		}
	}
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	offset := max(filter.Offset, 0)
	query += ` ORDER BY created_at,execution_id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []storage.ExternalToolExecution
	for rows.Next() {
		record, err := scanExternalExecution(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func validExternalExecutionTransition(from, to string) bool {
	if from == to {
		return from == storage.ExternalExecutionPrepared || from == storage.ExternalExecutionDispatched || from == storage.ExternalExecutionUnknown
	}
	switch from {
	case storage.ExternalExecutionPrepared:
		return to == storage.ExternalExecutionDispatched || to == storage.ExternalExecutionObserved
	case storage.ExternalExecutionDispatched:
		return to == storage.ExternalExecutionUnknown || to == storage.ExternalExecutionObserved
	case storage.ExternalExecutionUnknown:
		return to == storage.ExternalExecutionObserved
	default:
		return false
	}
}

func (r *ExternalToolExecutionRepository) Update(ctx context.Context, owner, id string, expectedRevision uint64, update storage.ExternalToolExecutionUpdate) (storage.ExternalToolExecution, error) {
	if expectedRevision == 0 || expectedRevision >= math.MaxInt64 {
		return storage.ExternalToolExecution{}, storage.ErrExternalExecutionConflict
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.ExternalToolExecution{}, err
	}
	defer tx.Rollback()
	record, err := scanExternalExecution(tx.QueryRowContext(ctx, `SELECT `+externalExecutionColumns+`
FROM external_tool_executions WHERE owner_id=? AND execution_id=?`, owner, id))
	if err != nil {
		return record, err
	}
	if record.Revision != expectedRevision || !validExternalExecutionTransition(record.Phase, update.Phase) {
		return record, storage.ErrExternalExecutionConflict
	}
	// 文件保存后立即登记清单，不等待外部调用终态；同阶段补写不能改写执行事实。
	if record.Phase == update.Phase {
		if update.ResultManifestJSON == nil || update.AcceptedResponse ||
			update.Outcome != "" && update.Outcome != record.Outcome ||
			update.Reason != "" && update.Reason != record.Reason ||
			update.UpstreamCallID != "" && update.UpstreamCallID != record.UpstreamCallID {
			return record, storage.ErrExternalExecutionConflict
		}
		update.Outcome = record.Outcome
		update.Reason = record.Reason
		update.UpstreamCallID = record.UpstreamCallID
	}
	if update.Phase == storage.ExternalExecutionObserved && update.Outcome == "" {
		return record, fmt.Errorf("observed external execution outcome is required")
	}
	if update.AcceptedResponse && (record.Kind != "model_request" || record.ModelExchangeID == "" || update.Phase != storage.ExternalExecutionObserved) {
		return record, storage.ErrExternalExecutionConflict
	}
	if update.UpstreamCallID != "" {
		if record.UpstreamCallID != "" && record.UpstreamCallID != update.UpstreamCallID {
			return record, storage.ErrExternalExecutionConflict
		}
		record.UpstreamCallID = update.UpstreamCallID
	}
	if update.ResultManifestJSON != nil {
		if err := validateExternalManifest(update.ResultManifestJSON); err != nil {
			return record, err
		}
		record.ResultManifestJSON = update.ResultManifestJSON
	}
	if update.AcceptedResponse {
		var other string
		err := tx.QueryRowContext(ctx, `SELECT execution_id FROM external_tool_executions
WHERE owner_id=? AND model_exchange_id=? AND kind='model_request' AND accepted_response=1 AND execution_id<>?`,
			owner, record.ModelExchangeID, id).Scan(&other)
		if err == nil {
			return record, storage.ErrExternalExecutionConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return record, err
		}
	}
	record.Phase = update.Phase
	record.Outcome = update.Outcome
	record.Reason = update.Reason
	record.AcceptedResponse = update.AcceptedResponse
	record.Revision++
	record.UpdatedAt = time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE external_tool_executions SET
phase=?,outcome=?,reason=?,upstream_call_id=?,result_manifest_json=?,accepted_response=?,revision=?,updated_at=?
WHERE owner_id=? AND execution_id=? AND revision=?`, record.Phase, record.Outcome, record.Reason,
		record.UpstreamCallID, string(record.ResultManifestJSON), record.AcceptedResponse, record.Revision,
		record.UpdatedAt, owner, id, expectedRevision)
	if err != nil {
		return record, fmt.Errorf("update external execution: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return record, err
	}
	if changed != 1 {
		return record, storage.ErrExternalExecutionConflict
	}
	return record, tx.Commit()
}

const externalArtifactPathExists = `EXISTS (
SELECT 1 FROM json_each(external_tool_executions.result_manifest_json,'$.artifacts') artifact
WHERE artifact.type='object' AND json_extract(artifact.value,'$.path')=?
UNION ALL
SELECT 1 FROM json_each(external_tool_executions.result_manifest_json,'$.result.pending_artifacts') artifact
WHERE artifact.type='object' AND json_extract(artifact.value,'$.path')=?
UNION ALL
SELECT 1 WHERE json_type(external_tool_executions.result_manifest_json,'$.result.pending_artifact')='object'
AND json_extract(external_tool_executions.result_manifest_json,'$.result.pending_artifact.path')=?
UNION ALL
SELECT 1 WHERE json_type(external_tool_executions.result_manifest_json,'$.result.execution.pending_artifact')='object'
AND json_extract(external_tool_executions.result_manifest_json,'$.result.execution.pending_artifact.path')=?
)`

// FindArtifact 按已登记或待登记清单的确切存储路径查归属，不解析任意宿主文件路径。
func (r *ExternalToolExecutionRepository) FindArtifact(ctx context.Context, owner, path string) (*storage.ExternalToolExecution, error) {
	if owner == "" || strings.TrimSpace(path) == "" {
		return nil, storage.ErrNotFound
	}
	record, err := scanExternalExecution(r.db.QueryRowContext(ctx, `SELECT `+externalExecutionColumns+`
FROM external_tool_executions WHERE owner_id=? AND `+externalArtifactPathExists+`
ORDER BY updated_at DESC,execution_id LIMIT 1`, owner, path, path, path, path))
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// HasArtifact 仅返回路径是否登记或待登记，供旧文件路由区分外部产物与原有业务文件。
func (r *ExternalToolExecutionRepository) HasArtifact(ctx context.Context, path string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, nil
	}
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1 FROM external_tool_executions WHERE `+externalArtifactPathExists+`
)`, path, path, path, path).Scan(&exists)
	return exists, err
}
