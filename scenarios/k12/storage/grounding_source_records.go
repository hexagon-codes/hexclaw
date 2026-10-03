package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/internal/sqliteutil"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

const (
	GroundingSourceSnapshotOperation = "k12_grounding_source_snapshot"
	GroundingSourceRecoveryOperation = "k12_grounding_source_recovery"
)

// ListGroundingRetrievalInvocations 返回任务全部来源与召回事实，不隐藏历史未知结果。
func (s *Store) ListGroundingRetrievalInvocations(ctx context.Context, owner, agent, job string) ([]GroundingRetrievalInvocation, error) {
	return listGroundingRetrievalInvocations(ctx, s.db, owner, agent, job)
}

func listGroundingRetrievalInvocations(ctx context.Context, q dbQueryer, owner, agent, job string) ([]GroundingRetrievalInvocation, error) {
	rows, err := q.QueryContext(ctx, groundingRetrievalInvocationSelect+` WHERE owner_id=? AND agent_name=? AND job_id=? ORDER BY created_at,invocation_id`, owner, agent, job)
	if err != nil {
		return nil, groundingRetrievalLedgerError(err)
	}
	defer rows.Close()
	var out []GroundingRetrievalInvocation
	for rows.Next() {
		invocation, err := scanGroundingRetrievalInvocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, invocation)
	}
	return out, rows.Err()
}

func validateGroundingSourceClaim(claim GroundingRetrievalInvocationClaim, resultJSON, operation string) error {
	if err := validateGroundingRetrievalClaim(claim); err != nil {
		return err
	}
	if claim.Operation != operation || claim.Provider != "" || claim.Model != "" ||
		claim.RevisionID != "" || claim.ProfileConfigHash != "" || !json.Valid([]byte(resultJSON)) {
		return fmt.Errorf("invalid local grounding source record")
	}
	return nil
}

func getGroundingSourceRecord(ctx context.Context, q dbQueryer, claim GroundingRetrievalInvocationClaim) (GroundingRetrievalInvocation, error) {
	return scanGroundingRetrievalInvocation(q.QueryRowContext(ctx, groundingRetrievalInvocationSelect+` WHERE owner_id=? AND agent_name=? AND job_id=? AND operation=?`, claim.OwnerID, claim.AgentName, claim.JobID, claim.Operation))
}

func insertGroundingSourceRecord(ctx context.Context, tx *sql.Tx, claim GroundingRetrievalInvocationClaim, resultJSON string) (GroundingRetrievalInvocation, error) {
	key := groundingRetrievalInvocationKey(claim)
	id := "grounding_retrieval_" + key[:32]
	now := time.Now().UTC().UnixMilli()
	_, err := tx.ExecContext(ctx, `INSERT INTO k12_grounding_retrieval_invocations
		(invocation_id,invocation_key,owner_id,agent_name,job_id,problem_id,operation,
		 grounding_snapshot_digest,query_digest,document_id,document_generation,revision_id,
		 profile_config_hash,scope_digest,provider,model,status,result_json,query_receipt_digest,
		 hit_set_digest,citation_set_digest,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,'','',?,'','','succeeded',?,?,'','',?,?)`,
		id, key, claim.OwnerID, claim.AgentName, claim.JobID, claim.ProblemID, claim.Operation,
		claim.GroundingSnapshotDigest, claim.QueryDigest, claim.DocumentID, claim.DocumentGeneration,
		claim.ScopeDigest, resultJSON, sha256Hex([]byte(resultJSON)), now, now)
	if err != nil {
		return GroundingRetrievalInvocation{}, err
	}
	return scanGroundingRetrievalInvocation(tx.QueryRowContext(ctx, groundingRetrievalInvocationSelect+` WHERE invocation_id=?`, id))
}

// FreezeGroundingSource 在首次采用时保存来源快照；同任务随后始终使用原冻结结果。
func (s *Store) FreezeGroundingSource(ctx context.Context, claim GroundingRetrievalInvocationClaim, resultJSON string) (out GroundingRetrievalInvocation, created bool, err error) {
	if err = validateGroundingSourceClaim(claim, resultJSON, GroundingSourceSnapshotOperation); err != nil {
		return
	}
	err = sqliteutil.RetryOnBusy(ctx, func() error {
		created = false
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		out, err = getGroundingSourceRecord(ctx, tx, claim)
		if err == nil {
			if out.Status != GroundingRetrievalInvocationStatusSucceeded {
				return fmt.Errorf("grounding source snapshot is incomplete")
			}
			return tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return groundingRetrievalLedgerError(err)
		}
		out, err = insertGroundingSourceRecord(ctx, tx, claim, resultJSON)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		created = true
		return nil
	})
	return
}

// AuthorizeGroundingSourceRecovery 保留旧回执，原子追加来源变更并开启原任务执行窗口。
func (s *Store) AuthorizeGroundingSourceRecovery(ctx context.Context, claim GroundingRetrievalInvocationClaim, resultJSON string, prior []GroundingRetrievalInvocation, dispatchID string, dispatchVersion, jobVersion int, next k12.GradingJobFields) (out GroundingRetrievalInvocation, created bool, err error) {
	if err = validateGroundingSourceClaim(claim, resultJSON, GroundingSourceRecoveryOperation); err != nil {
		return
	}
	err = sqliteutil.RetryOnBusy(ctx, func() error {
		created = false
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		out, err = getGroundingSourceRecord(ctx, tx, claim)
		if err == nil {
			if out.InvocationKey != groundingRetrievalInvocationKey(claim) || out.ResultJSON != resultJSON || out.Status != GroundingRetrievalInvocationStatusSucceeded {
				return ErrModelInvocationConflict
			}
			return tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return groundingRetrievalLedgerError(err)
		}
		storedPrior, err := listGroundingRetrievalInvocations(ctx, tx, claim.OwnerID, claim.AgentName, claim.JobID)
		if err != nil {
			return err
		}
		if !sameGroundingSourcePrior(prior, storedPrior) {
			return ErrModelInvocationConflict
		}
		dispatch, err := getImageTaskDispatch(ctx, tx, claim.AgentName, dispatchID, "")
		if err != nil {
			return err
		}
		if dispatch.Version != dispatchVersion || dispatch.TargetObjectType != k12.ImageTaskTargetHomeworkSubmission {
			return ErrImageTaskVersionConflict
		}
		if err := validateImageTaskOwnerScopeReplay(ctx, tx, dispatch, claim.OwnerID); err != nil {
			return err
		}
		homework, err := getHomeworkSubmission(ctx, tx, claim.AgentName, dispatch.TargetObjectID)
		if err != nil {
			return err
		}
		if homework.GradingJobID != claim.JobID || homework.DispatchID != dispatchID {
			return ErrModelInvocationConflict
		}
		job, err := s.getVia(ctx, tx, claim.JobID)
		if err != nil {
			return err
		}
		if job.AgentName != claim.AgentName || job.Version != jobVersion || job.Status != k12.GradingStageFailedRetryable {
			return records.ErrVersionConflict
		}
		old, err := k12.ParseGradingJobFields(job.Fields)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Unix()
		startedAt := next.Deadline - next.ParentAutomaticRemainingSeconds
		var problemCount int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM k12_attempts WHERE agent_name=? AND submission_id=?`, claim.AgentName, old.SubmissionID).Scan(&problemCount); err != nil {
			return err
		}
		budget := k12.GradingStageBudgetSeconds(k12.GradingStageAssessing)
		if old.BudgetSnapshot.IsFrozen() {
			var valid bool
			budget, valid = old.BudgetSnapshot.StageBudgetSeconds(k12.GradingStageAssessing, problemCount)
			if !valid {
				return ErrModelInvocationConflict
			}
		} else if old.BudgetSnapshot.Validate() != nil {
			return ErrModelInvocationConflict
		}
		assessmentReady := old.FailedStage == k12.GradingStageAssessing ||
			old.FailedStage == k12.GradingStageQueued && old.FailureKind == "interactive_deadline_exceeded" &&
				k12.GradingResumeStage(old.StageCheckpoints) == k12.GradingStageAssessing
		if !assessmentReady || next.FailureKind != "" || next.FailedStage != "" || next.Retryable ||
			next.Deadline <= now || next.ParentAutomaticDeadlineAt != next.Deadline || startedAt > now ||
			next.ParentAutomaticRemainingSeconds <= 0 || next.ParentAutomaticRemainingSeconds > budget {
			return ErrModelInvocationConflict
		}
		unchanged := next
		unchanged.Deadline, unchanged.ParentAutomaticDeadlineAt = old.Deadline, old.ParentAutomaticDeadlineAt
		unchanged.ParentAutomaticRemainingSeconds = old.ParentAutomaticRemainingSeconds
		unchanged.FailureKind, unchanged.FailedStage, unchanged.Retryable = old.FailureKind, old.FailedStage, old.Retryable
		if !reflect.DeepEqual(old, unchanged) {
			return ErrModelInvocationConflict
		}
		out, err = insertGroundingSourceRecord(ctx, tx, claim, resultJSON)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		mp := gradingJobMapper{}
		vals, err := mp.encode(string(raw))
		if err != nil {
			return err
		}
		assigns := make([]string, 0, len(mp.domainCols()))
		for _, column := range mp.domainCols() {
			assigns = append(assigns, column+"=?")
		}
		vals = append(vals, now, claim.JobID, claim.AgentName, jobVersion)
		res, err := tx.ExecContext(ctx, `UPDATE k12_grading_jobs SET `+strings.Join(assigns, ",")+`,status='queued',version=version+1,updated_at=? WHERE record_id=? AND agent_name=? AND version=? AND status='failed_retryable'`, vals...)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return records.ErrVersionConflict
		}
		res, err = tx.ExecContext(ctx, `UPDATE k12_image_task_dispatches SET automatic_budget_seconds=?,automatic_started_at=?,automatic_deadline_at=?,automatic_remaining_seconds=?,version=version+1,updated_at=? WHERE dispatch_id=? AND agent_name=? AND version=?`, budget, startedAt, next.Deadline, next.ParentAutomaticRemainingSeconds, now, dispatchID, claim.AgentName, dispatchVersion)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return ErrImageTaskVersionConflict
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		created = true
		return nil
	})
	return
}

func sameGroundingSourcePrior(prior, stored []GroundingRetrievalInvocation) bool {
	if len(prior) != len(stored) {
		return false
	}
	byID := make(map[string]GroundingRetrievalInvocation, len(prior))
	for _, invocation := range prior {
		invocation.Fresh = false
		byID[invocation.InvocationID] = invocation
	}
	if len(byID) != len(prior) {
		return false
	}
	for _, invocation := range stored {
		invocation.Fresh = false
		if expected, ok := byID[invocation.InvocationID]; !ok || !reflect.DeepEqual(expected, invocation) {
			return false
		}
	}
	return true
}

// ReconcileGroundingSourceNotStarted 仅纠正来源读取失败且尚无新调用的本地未知状态。
func (s *Store) ReconcileGroundingSourceNotStarted(ctx context.Context, owner, agent, jobID, recoveryID string, jobVersion int) (changed bool, err error) {
	if owner == "" || agent == "" || jobID == "" || recoveryID == "" {
		return false, nil
	}
	err = sqliteutil.RetryOnBusy(ctx, func() error {
		changed = false
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		recovery, err := scanGroundingRetrievalInvocation(tx.QueryRowContext(ctx, groundingRetrievalInvocationSelect+` WHERE invocation_id=? AND owner_id=? AND agent_name=? AND job_id=?`, recoveryID, owner, agent, jobID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if recovery.Operation != GroundingSourceRecoveryOperation || recovery.Status != GroundingRetrievalInvocationStatusSucceeded ||
			sha256Hex([]byte(recovery.ResultJSON)) != recovery.QueryReceiptDigest {
			return nil
		}
		var source struct {
			InvocationIDs []string `json:"source_invocation_ids"`
		}
		if json.Unmarshal([]byte(recovery.ResultJSON), &source) != nil || len(source.InvocationIDs) == 0 {
			return nil
		}
		originalIDs := make(map[string]bool, len(source.InvocationIDs))
		for _, id := range source.InvocationIDs {
			if id == "" || originalIDs[id] || id == recoveryID {
				return nil
			}
			originalIDs[id] = true
		}
		var itemCount, queryCount int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM k12_grading_item_invocations WHERE agent_name=? AND job_id=?`, agent, jobID).Scan(&itemCount); err != nil {
			return err
		}
		if itemCount != 0 {
			return nil
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM k12_grounding_retrieval_invocations WHERE agent_name=? AND job_id=?`, agent, jobID).Scan(&queryCount); err != nil {
			return err
		}
		if queryCount != len(originalIDs)+1 {
			return nil
		}
		rows, err := listGroundingRetrievalInvocations(ctx, tx, owner, agent, jobID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.InvocationID == recoveryID {
				continue
			}
			if !originalIDs[row.InvocationID] || row.Operation != "k12_grounding_retrieval" ||
				row.Status != GroundingRetrievalInvocationStatusOutcomeUnknown || row.UpdatedAt.After(recovery.CreatedAt) {
				return nil
			}
			delete(originalIDs, row.InvocationID)
		}
		if len(originalIDs) != 0 {
			return nil
		}
		job, err := s.getVia(ctx, tx, jobID)
		if errors.Is(err, records.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if job.AgentName != agent || job.Version != jobVersion || job.Status != k12.GradingStageOutcomeUnknown {
			return nil
		}
		fields, err := k12.ParseGradingJobFields(job.Fields)
		if err != nil {
			return err
		}
		if (fields.FailureKind != "item_invocation_outcome_unknown" && fields.FailureKind != "invocation_reconciliation_required") || fields.FailedStage != k12.GradingStageAssessing ||
			k12.GradingResumeStage(fields.StageCheckpoints) != k12.GradingStageAssessing {
			return nil
		}
		now := time.Now().UTC().Unix()
		attemptCount := fields.AttemptCount
		prior, err := scanModelInvocation(tx.QueryRowContext(ctx, `SELECT `+modelInvocationColumns+` FROM k12_model_invocations WHERE agent_name=? AND job_id=? AND stage='assessing' AND attempt=?`, agent, jobID, fields.AttemptCount+1))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if errors.Is(err, sql.ErrNoRows) {
			if fields.FailureKind == "invocation_reconciliation_required" {
				return nil
			}
		} else {
			if prior.Status != k12.ModelInvocationOutcomeUnknown || prior.FailureKind != "item_invocation_outcome_unknown" ||
				prior.RouteSnapshot != k12.NormalizeGradingModelSnapshot(fields.ModelSnapshot) || prior.RequestDigest == "" ||
				prior.ResultJSON != "" || prior.ResultDigest != "" || prior.ExternalRequestID != "" || prior.CreatedAt < recovery.CreatedAt.Unix() {
				return nil
			}
			// 每份来源恢复最多追加一个聚合尝试；旧未知记录始终保留原值。
			nextID := "modelinv-source-" + sha256Hex([]byte(recoveryID))[:32]
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM k12_model_invocations WHERE invocation_id=?`, nextID).Scan(&exists); err != nil {
				return err
			}
			if exists != 0 {
				return nil
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO k12_model_invocations (`+modelInvocationColumns+`)
				SELECT ?,agent_name,job_id,stage,request_digest,provider,model,route_snapshot_json,request_policy_snapshot_json,
				'','prepared',attempt+1,'','','','',?,? FROM k12_model_invocations WHERE invocation_id=?`, nextID, now, now, prior.InvocationID); err != nil {
				return err
			}
			attemptCount = prior.Attempt
		}
		result, err := tx.ExecContext(ctx, `UPDATE k12_grading_jobs SET status='failed_retryable',retryable=1,failure_kind='interactive_deadline_exceeded',attempt_count=?,version=version+1,updated_at=? WHERE record_id=? AND agent_name=? AND version=? AND status='outcome_unknown'`, attemptCount, now, jobID, agent, jobVersion)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return nil
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return
}
