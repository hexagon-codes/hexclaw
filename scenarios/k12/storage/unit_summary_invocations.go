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

// 单元账本仅适配任务外键。调用身份、route/policy规范化、结果摘要及状态沿公共ModelInvocation合同。
func (s *Store) PrepareUnitSummaryModelInvocation(ctx context.Context, i k12.ModelInvocation) (k12.ModelInvocation, bool, error) {
	if err := validateModelInvocation(&i); err != nil {
		return i, false, err
	}
	i.Status = k12.ModelInvocationPrepared
	if i.CreatedAt <= 0 {
		i.CreatedAt = nowUnix()
	}
	i.UpdatedAt = i.CreatedAt
	route, err := json.Marshal(i.RouteSnapshot)
	if err != nil {
		return i, false, err
	}
	policy := ""
	if !i.RequestPolicySnapshot.IsZero() {
		raw, e := json.Marshal(i.RequestPolicySnapshot)
		if e != nil {
			return i, false, e
		}
		policy = string(raw)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO k12_unit_summary_model_invocations (`+modelInvocationColumns+`) SELECT ?,?,?,?,?,?,?,?,?,?,?,?,'','','','',?,? FROM k12_unit_summary_attempts a WHERE a.attempt_id=? AND a.agent_name=? ON CONFLICT(job_id,stage,attempt) DO NOTHING`, i.InvocationID, i.AgentName, i.JobID, i.Stage, i.RequestDigest, i.RouteSnapshot.Provider, i.RouteSnapshot.Model, string(route), policy, i.ProviderIdempotencyKey, i.Status, i.Attempt, i.CreatedAt, i.UpdatedAt, i.JobID, i.AgentName)
	if err != nil {
		return i, false, err
	}
	created, _ := res.RowsAffected()
	stored, err := scanModelInvocation(s.db.QueryRowContext(ctx, `SELECT `+modelInvocationColumns+` FROM k12_unit_summary_model_invocations WHERE agent_name=? AND job_id=? AND stage=? AND attempt=?`, i.AgentName, i.JobID, i.Stage, i.Attempt))
	if errors.Is(err, sql.ErrNoRows) {
		return i, false, records.ErrNotFound
	}
	if err != nil {
		return i, false, err
	}
	if stored.RequestDigest != i.RequestDigest || stored.RouteSnapshot != i.RouteSnapshot || stored.RequestPolicySnapshot != i.RequestPolicySnapshot {
		return i, false, ErrModelInvocationConflict
	}
	return stored, created == 0, nil
}
func (s *Store) ListUnitSummaryModelInvocations(ctx context.Context, agent, job string) ([]k12.ModelInvocation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+modelInvocationColumns+` FROM k12_unit_summary_model_invocations WHERE agent_name=? AND job_id=? ORDER BY stage,attempt`, agent, job)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []k12.ModelInvocation{}
	for rows.Next() {
		i, e := scanModelInvocation(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
func (s *Store) GetUnitSummaryModelInvocation(ctx context.Context, agent, id string) (k12.ModelInvocation, error) {
	i, err := scanModelInvocation(s.db.QueryRowContext(ctx, `SELECT `+modelInvocationColumns+` FROM k12_unit_summary_model_invocations WHERE agent_name=? AND invocation_id=?`, agent, id))
	if errors.Is(err, sql.ErrNoRows) {
		err = records.ErrNotFound
	}
	return i, err
}

// claim只有prepared的唯一写者能获得send权，重放sent不能再调用provider。
func (s *Store) MarkUnitSummaryModelInvocationSent(ctx context.Context, agent, id, key string) (k12.ModelInvocation, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE k12_unit_summary_model_invocations SET status='sent',provider_idempotency_key=?,updated_at=? WHERE agent_name=? AND invocation_id=? AND status='prepared'`, key, nowUnix(), agent, id)
	if err != nil {
		return k12.ModelInvocation{}, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return k12.ModelInvocation{}, ErrUnitSummaryCAS
	}
	return s.GetUnitSummaryModelInvocation(ctx, agent, id)
}
func (s *Store) unitSummaryInvocationFailure(ctx context.Context, agent, id, kind, status string) (k12.ModelInvocation, error) {
	if kind == "" {
		return k12.ModelInvocation{}, fmt.Errorf("model invocation failure kind is required")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE k12_unit_summary_model_invocations SET status=?,failure_kind=?,updated_at=? WHERE agent_name=? AND invocation_id=? AND status='sent'`, status, kind, nowUnix(), agent, id)
	if err != nil {
		return k12.ModelInvocation{}, err
	}
	n, _ := res.RowsAffected()
	i, err := s.GetUnitSummaryModelInvocation(ctx, agent, id)
	if err != nil {
		return i, err
	}
	if n != 1 && (string(i.Status) != status || i.FailureKind != kind) {
		return i, ErrUnitSummaryCAS
	}
	return i, nil
}
func (s *Store) MarkUnitSummaryModelInvocationFailed(ctx context.Context, agent, id, kind string) (k12.ModelInvocation, error) {
	return s.unitSummaryInvocationFailure(ctx, agent, id, kind, "failed")
}
func (s *Store) MarkUnitSummaryModelInvocationOutcomeUnknown(ctx context.Context, agent, id, kind string) (k12.ModelInvocation, error) {
	return s.unitSummaryInvocationFailure(ctx, agent, id, kind, "outcome_unknown")
}
func (s *Store) MarkUnitSummaryModelInvocationSucceededWithResult(ctx context.Context, agent, id, digest, raw, external string) (k12.ModelInvocation, error) {
	if !json.Valid([]byte(raw)) || digest == "" || modelInvocationResultPayloadDigest(raw) != digest {
		return k12.ModelInvocation{}, ErrModelInvocationConflict
	}
	res, err := s.db.ExecContext(ctx, `UPDATE k12_unit_summary_model_invocations SET status='succeeded',result_digest=?,result_json=?,external_request_id=?,failure_kind='',updated_at=? WHERE agent_name=? AND invocation_id=? AND status='sent'`, digest, raw, external, nowUnix(), agent, id)
	if err != nil {
		return k12.ModelInvocation{}, err
	}
	n, _ := res.RowsAffected()
	i, err := s.GetUnitSummaryModelInvocation(ctx, agent, id)
	if err != nil {
		return i, err
	}
	if n != 1 && (i.Status != k12.ModelInvocationSucceeded || i.ResultDigest != digest || i.ResultJSON != raw || i.ExternalRequestID != external) {
		return i, ErrModelInvocationConflict
	}
	return i, nil
}
