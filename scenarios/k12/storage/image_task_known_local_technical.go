package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// ImageTaskKnownLocalTechnicalRecovery 只恢复原任务已落库的确定本地失败，不携带替换领域字段。
type ImageTaskKnownLocalTechnicalRecovery struct {
	AgentName, OwnerScope, DispatchID, JobID    string
	ExpectedDispatchVersion, ExpectedJobVersion int
	Now, QueuedDeadline                         int64
}

// RestoreImageTaskKnownLocalTechnical 在同一短事务中核对原归属、当前失败回执并恢复父子窗口。
// 通用 GradingJob 状态机保持终态闭合；调用者必须在提交成功后启动原运行对象。
func (s *Store) RestoreImageTaskKnownLocalTechnical(
	ctx context.Context, command ImageTaskKnownLocalTechnicalRecovery,
) (k12.ImageTaskDispatch, *records.AgentRecord, error) {
	if strings.TrimSpace(command.AgentName) == "" || strings.TrimSpace(command.OwnerScope) == "" ||
		command.DispatchID == "" || command.JobID == "" || command.Now <= 0 || command.QueuedDeadline <= command.Now ||
		command.QueuedDeadline > command.Now+k12.ImageTaskAutomaticBudgetSeconds {
		return k12.ImageTaskDispatch{}, nil, ErrImageTaskInvalidState
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return k12.ImageTaskDispatch{}, nil, fmt.Errorf("begin known local technical recovery: %w", err)
	}
	defer tx.Rollback()
	// 首语句取得写锁；任何资格或双版本校验失败均回滚，不改变任务时间或历史。
	locked, err := tx.ExecContext(ctx, `UPDATE k12_image_task_dispatches SET version=version
		WHERE agent_name=? AND dispatch_id=? AND version=?`, command.AgentName, command.DispatchID, command.ExpectedDispatchVersion)
	if err != nil {
		return k12.ImageTaskDispatch{}, nil, err
	}
	if rows, err := locked.RowsAffected(); err != nil {
		return k12.ImageTaskDispatch{}, nil, err
	} else if rows != 1 {
		return k12.ImageTaskDispatch{}, nil, ErrImageTaskVersionConflict
	}
	dispatch, err := getImageTaskDispatch(ctx, tx, command.AgentName, command.DispatchID, "")
	if err != nil {
		return k12.ImageTaskDispatch{}, nil, err
	}
	if dispatch.TargetObjectType != k12.ImageTaskTargetHomeworkSubmission ||
		(dispatch.Status != k12.ImageTaskStatusRouted && dispatch.Status != k12.ImageTaskStatusFailed) {
		return k12.ImageTaskDispatch{}, nil, ErrImageTaskInvalidState
	}
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT owner_scope FROM k12_image_task_owner_scopes WHERE agent_name=? AND dispatch_id=?`,
		command.AgentName, command.DispatchID).Scan(&owner); errors.Is(err, sql.ErrNoRows) {
		return k12.ImageTaskDispatch{}, nil, ErrImageTaskNotFound
	} else if err != nil {
		return k12.ImageTaskDispatch{}, nil, err
	} else if owner != command.OwnerScope {
		return k12.ImageTaskDispatch{}, nil, ErrImageTaskNotFound
	}
	homework, err := getHomeworkSubmission(ctx, tx, command.AgentName, dispatch.TargetObjectID)
	if err != nil {
		return k12.ImageTaskDispatch{}, nil, err
	}
	if homework.DispatchID != dispatch.DispatchID || homework.GradingJobID != command.JobID {
		return k12.ImageTaskDispatch{}, nil, ErrImageTaskInvalidState
	}
	job, err := s.getVia(ctx, tx, command.JobID)
	if err != nil {
		return k12.ImageTaskDispatch{}, nil, err
	}
	if job.AgentName != command.AgentName || job.Collection != k12.CollectionGradingJob {
		return k12.ImageTaskDispatch{}, nil, ErrImageTaskNotFound
	}
	if job.Version != command.ExpectedJobVersion {
		return k12.ImageTaskDispatch{}, nil, records.ErrVersionConflict
	}
	fields, err := k12.ParseGradingJobFields(job.Fields)
	if err != nil {
		return k12.ImageTaskDispatch{}, nil, err
	}
	if job.Status != k12.GradingStageFailedTerminal || fields.FailedStage != k12.GradingStageAssessing ||
		fields.AttemptCount != k12.GradingMaxStageAttempts || fields.SourceKind != "image_task" ||
		fields.IdempotencyKey != k12.BuildGradingIdempotencyKey("image_task", dispatch.DispatchID, fields.ConfirmedVersion) ||
		fields.ConfirmationState != k12.GradingConfirmationConfirmed || k12.GradingResumeStage(fields.StageCheckpoints) != k12.GradingStageAssessing {
		return k12.ImageTaskDispatch{}, nil, ErrImageTaskInvalidState
	}
	for _, checkpoint := range fields.StageCheckpoints {
		if checkpoint.Stage == k12.GradingStageAssessing || checkpoint.Stage == k12.GradingStageRendering || checkpoint.Stage == k12.GradingStageProjecting {
			return k12.ImageTaskDispatch{}, nil, ErrImageTaskInvalidState
		}
	}
	var blocked int
	err = tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM k12_grading_final_artifacts WHERE agent_name=? AND job_id=?)
		OR EXISTS(SELECT 1 FROM k12_model_invocations WHERE agent_name=? AND job_id=? AND status IN ('sent','outcome_unknown','reconciled'))
		OR EXISTS(SELECT 1 FROM k12_model_physical_invocations WHERE agent_name=? AND job_id=? AND status IN ('sent','outcome_unknown','reconciled'))
		OR EXISTS(SELECT 1 FROM k12_grading_item_invocations WHERE agent_name=? AND job_id=? AND status IN ('sent','outcome_unknown','reconciled'))`,
		command.AgentName, command.JobID, command.AgentName, command.JobID,
		command.AgentName, command.JobID, command.AgentName, command.JobID).Scan(&blocked)
	if err != nil {
		return k12.ImageTaskDispatch{}, nil, err
	}
	if blocked != 0 {
		return k12.ImageTaskDispatch{}, nil, ErrImageTaskInvalidState
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+gradingItemInvocationColumns+` FROM k12_grading_item_invocations AS invocation
		WHERE agent_name=? AND job_id=? AND operation_attempt/1000=?
		  AND status='failed' AND execution_kind='provider' AND failure_class='local' AND failure_code='provider_response_processed'
		  AND result_json='' AND result_digest=''
		  AND EXISTS(SELECT 1 FROM k12_attempts AS attempt WHERE attempt.agent_name=invocation.agent_name
		    AND attempt.submission_id=? AND attempt.problem_id=invocation.problem_id AND attempt.attempt_id=invocation.attempt_id
		    AND attempt.confirmed_version=invocation.input_revision AND attempt.input_digest=invocation.input_digest)
		  AND NOT EXISTS(SELECT 1 FROM k12_grading_item_invocations AS later WHERE later.agent_name=invocation.agent_name
		    AND later.job_id=invocation.job_id AND later.problem_id=invocation.problem_id AND later.operation=invocation.operation
		    AND later.operation_attempt>invocation.operation_attempt)`, command.AgentName, command.JobID, fields.AttemptCount, fields.SubmissionID)
	if err != nil {
		return k12.ImageTaskDispatch{}, nil, err
	}
	eligible := false
	for rows.Next() {
		invocation, scanErr := scanGradingItemInvocation(rows)
		if scanErr != nil {
			rows.Close()
			return k12.ImageTaskDispatch{}, nil, scanErr
		}
		if invocation.RouteSnapshot == fields.ModelSnapshot {
			eligible = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return k12.ImageTaskDispatch{}, nil, err
	}
	if !eligible {
		return k12.ImageTaskDispatch{}, nil, ErrImageTaskInvalidState
	}
	dispatch, err = restartImageTaskAutomaticWindowVia(ctx, tx, command.AgentName, command.DispatchID, command.ExpectedDispatchVersion, command.Now, true)
	if err != nil {
		return k12.ImageTaskDispatch{}, nil, err
	}
	fields.Deadline = command.QueuedDeadline
	fields.ParentAutomaticAttemptID = fmt.Sprintf("%s:%d", dispatch.DispatchID, dispatch.AutomaticStartedAt)
	fields.ParentAutomaticDeadlineAt = dispatch.AutomaticDeadlineAt
	fields.ParentAutomaticRemainingSeconds = int64(dispatch.AutomaticRemainingSeconds)
	raw, err := json.Marshal(fields)
	if err != nil {
		return k12.ImageTaskDispatch{}, nil, err
	}
	values, err := (gradingJobMapper{}).encode(string(raw))
	if err != nil {
		return k12.ImageTaskDispatch{}, nil, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE k12_grading_jobs SET status=?,deadline=?,budget_snapshot_json=?,version=version+1,updated_at=?
		WHERE agent_name=? AND record_id=? AND version=? AND status=? AND failed_stage=? AND attempt_count=?`,
		k12.GradingStageQueued, fields.Deadline, values[8], command.Now, command.AgentName, command.JobID,
		command.ExpectedJobVersion, k12.GradingStageFailedTerminal, k12.GradingStageAssessing, fields.AttemptCount)
	if err != nil {
		return k12.ImageTaskDispatch{}, nil, err
	}
	if count, err := updated.RowsAffected(); err != nil {
		return k12.ImageTaskDispatch{}, nil, err
	} else if count != 1 {
		return k12.ImageTaskDispatch{}, nil, records.ErrVersionConflict
	}
	job.Status, job.Fields, job.Version, job.UpdatedAt = k12.GradingStageQueued, string(raw), job.Version+1, command.Now
	if err := tx.Commit(); err != nil {
		return k12.ImageTaskDispatch{}, nil, fmt.Errorf("commit known local technical recovery: %w", err)
	}
	return dispatch, job, nil
}
