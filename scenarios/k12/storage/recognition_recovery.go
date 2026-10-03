package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/sqliteutil"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

const recognitionRecoveryColumns = `authorization_id,agent_name,dispatch_id,job_id,source_parent_id,source_physical_id,new_parent_id,new_physical_id,new_request_digest,page_digest,source_plan_digest,source_request_digest,candidate_exact_set_digest,idempotency_key,request_digest,created_at,source_timeout_ms,timeout_override_ms`

func scanRecognitionRecovery(row rowScanner) (a k12.RecognitionRecoveryAuthorization, err error) {
	err = row.Scan(&a.AuthorizationID, &a.AgentName, &a.DispatchID, &a.JobID, &a.SourceParentID, &a.SourcePhysicalID, &a.NewParentID, &a.NewPhysicalID, &a.NewRequestDigest, &a.PageDigest, &a.SourcePlanDigest, &a.SourceRequestDigest, &a.CandidateExactSetDigest, &a.IdempotencyKey, &a.RequestDigest, &a.CreatedAt, &a.SourceTimeoutMS, &a.TimeoutOverrideMS)
	if errors.Is(err, sql.ErrNoRows) {
		err = records.ErrNotFound
	}
	return
}

func (s *Store) GetRecognitionRecoveryByKey(ctx context.Context, owner, dispatch, key string) (k12.RecognitionRecoveryAuthorization, error) {
	return scanRecognitionRecovery(s.db.QueryRowContext(ctx, `SELECT `+recognitionRecoveryColumns+` FROM k12_recognition_recovery_authorizations WHERE agent_name=? AND dispatch_id=? AND idempotency_key=?`, owner, dispatch, key))
}

func (s *Store) GetRecognitionRecoveryByParent(ctx context.Context, owner, parent string) (k12.RecognitionRecoveryAuthorization, error) {
	return getRecognitionRecoveryByParent(ctx, s.db, owner, parent)
}

func getRecognitionRecoveryByParent(ctx context.Context, q dbQueryer, owner, parent string) (k12.RecognitionRecoveryAuthorization, error) {
	return scanRecognitionRecovery(q.QueryRowContext(ctx, `SELECT `+recognitionRecoveryColumns+` FROM k12_recognition_recovery_authorizations WHERE agent_name=? AND new_parent_id=?`, owner, parent))
}

// AuthorizeRecognitionRecovery 原子保留旧回执，接纳唯一新父尝试并开启原任务的新执行窗口。
func (s *Store) AuthorizeRecognitionRecovery(ctx context.Context, a k12.RecognitionRecoveryAuthorization, dispatchVersion, jobVersion int, parent k12.ModelInvocation, next k12.GradingJobFields) (k12.RecognitionRecoveryAuthorization, bool, error) {
	var out k12.RecognitionRecoveryAuthorization
	var created bool
	err := sqliteutil.RetryOnBusy(ctx, func() error {
		var err error
		out, created, err = s.authorizeRecognitionRecoveryOnce(ctx, a, dispatchVersion, jobVersion, parent, next)
		return err
	})
	return out, created, err
}

func (s *Store) authorizeRecognitionRecoveryOnce(ctx context.Context, a k12.RecognitionRecoveryAuthorization, dispatchVersion, jobVersion int, parent k12.ModelInvocation, next k12.GradingJobFields) (k12.RecognitionRecoveryAuthorization, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return a, false, err
	}
	defer tx.Rollback()
	existing, err := scanRecognitionRecovery(tx.QueryRowContext(ctx, `SELECT `+recognitionRecoveryColumns+` FROM k12_recognition_recovery_authorizations WHERE agent_name=? AND dispatch_id=? AND idempotency_key=?`, a.AgentName, a.DispatchID, a.IdempotencyKey))
	if err == nil {
		if existing.RequestDigest != a.RequestDigest {
			return a, false, ErrModelInvocationConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, records.ErrNotFound) {
		return a, false, err
	}
	dispatch, err := getImageTaskDispatch(ctx, tx, a.AgentName, a.DispatchID, "")
	if err != nil {
		return a, false, err
	}
	if dispatch.Version != dispatchVersion || dispatch.TargetObjectType != k12.ImageTaskTargetHomeworkSubmission {
		return a, false, ErrImageTaskVersionConflict
	}
	homework, err := getHomeworkSubmission(ctx, tx, a.AgentName, dispatch.TargetObjectID)
	if err != nil {
		return a, false, err
	}
	if homework.GradingJobID != a.JobID {
		return a, false, ErrModelInvocationConflict
	}
	job, err := s.getVia(ctx, tx, a.JobID)
	if err != nil {
		return a, false, err
	}
	if job.AgentName != a.AgentName || job.Version != jobVersion || job.Status != k12.GradingStageOutcomeUnknown {
		return a, false, records.ErrVersionConflict
	}
	old, err := k12.ParseGradingJobFields(job.Fields)
	if err != nil {
		return a, false, err
	}
	source, err := getModelPhysicalInvocationByIDVia(ctx, tx, a.AgentName, a.SourcePhysicalID)
	if err != nil {
		return a, false, err
	}
	prior, err := getModelInvocationByIDVia(ctx, tx, a.SourceParentID)
	if err != nil {
		return a, false, err
	}
	isRepair := strings.HasPrefix(string(source.PhysicalUnit), "layout_repair_")
	if source.ParentInvocationID != prior.InvocationID || source.JobID != a.JobID || prior.AgentName != a.AgentName || prior.JobID != a.JobID || source.Status != k12.ModelInvocationOutcomeUnknown || prior.Status != k12.ModelInvocationOutcomeUnknown || source.RecognitionPlanVersion != k12.RecognitionPlanVersionV2 || (!strings.HasPrefix(string(source.PhysicalUnit), "layout_batch_") && !isRepair) || source.RequestDigest != a.SourceRequestDigest || source.PlanDigest != a.SourcePlanDigest || source.CandidateExactSetDigest != a.CandidateExactSetDigest {
		return a, false, ErrModelInvocationConflict
	}
	if isRepair && (source.Stage != k12.GradingStageRecognizing || source.Stage != prior.Stage || !reflect.DeepEqual(source.RouteSnapshot, prior.RouteSnapshot) || !reflect.DeepEqual(source.RequestPolicySnapshot, prior.RequestPolicySnapshot) || a.TimeoutOverrideMS != 0) {
		return a, false, ErrModelInvocationConflict
	}
	sourceTimeout := source.EffectiveTimeoutMS
	if sourceTimeout == 0 {
		sourceTimeout = 120000
		if source.RouteSnapshot.TimeoutMS > 0 && int64(source.RouteSnapshot.TimeoutMS) < sourceTimeout {
			sourceTimeout = int64(source.RouteSnapshot.TimeoutMS)
		}
	}
	if a.SourceTimeoutMS != sourceTimeout {
		return a, false, ErrModelInvocationConflict
	}
	if (a.TimeoutOverrideMS != 0 && a.TimeoutOverrideMS != 180000) || (a.TimeoutOverrideMS != 0 && a.SourceTimeoutMS != 120000) {
		return a, false, ErrModelInvocationConflict
	}
	if err = validateModelInvocation(&parent); err != nil {
		return a, false, err
	}
	if parent.InvocationID != a.NewParentID || parent.AgentName != a.AgentName || parent.JobID != a.JobID || parent.Stage != prior.Stage || parent.Attempt != prior.Attempt+1 || parent.RequestDigest != prior.RequestDigest || !reflect.DeepEqual(parent.RouteSnapshot, prior.RouteSnapshot) || !reflect.DeepEqual(parent.RequestPolicySnapshot, prior.RequestPolicySnapshot) || next.AttemptCount != prior.Attempt || !reflect.DeepEqual(old.ModelSnapshot, next.ModelSnapshot) || !reflect.DeepEqual(old.BudgetSnapshot, next.BudgetSnapshot) || old.SubmissionID != next.SubmissionID || next.Deadline <= a.CreatedAt || next.ParentAutomaticDeadlineAt != next.Deadline || old.FailedStage != k12.GradingStageRecognizing {
		return a, false, ErrModelInvocationConflict
	}
	var planID, planJSON, pageDigest string
	if err = tx.QueryRowContext(ctx, `SELECT plan_id,authorized_plan_json,page_digest FROM k12_recognition_layout_plans WHERE parent_invocation_id=? AND agent_name=? AND authorized_plan_digest=?`, a.SourceParentID, a.AgentName, a.SourcePlanDigest).Scan(&planID, &planJSON, &pageDigest); err != nil {
		return a, false, err
	}
	var plan k12.RecognitionLayoutPlanV2
	if json.Unmarshal([]byte(planJSON), &plan) != nil || k12.ValidateRecognitionLayoutPlanV2(plan) != nil || pageDigest != a.PageDigest {
		return a, false, ErrModelInvocationConflict
	}
	if isRepair {
		if plan.AuthorizedPlanDigest != a.SourcePlanDigest || plan.PageDigest != a.PageDigest {
			return a, false, ErrModelInvocationConflict
		}
		// 单题恢复沿用原轮次授权，事务内重新核对候选及成功主批次的完整证据。
		if err = validateRecognitionLayoutRepairAuthorizationEvidenceVia(ctx, tx, prior, source, planID, a.SourcePlanDigest, true); err != nil {
			return a, false, err
		}
	}
	// 成功来源可沿已持久授权链追溯；中间尝试未复制的批次不要求重新外发。
	sourceParents, err := recognitionRecoverySourceParents(ctx, tx, a.AgentName, prior.InvocationID)
	if err != nil {
		return a, false, err
	}
	units := []k12.RecognitionPhysicalUnit{k12.RecognitionPhysicalUnitWholePage}
	for _, b := range plan.Batches {
		units = append(units, b.Unit)
	}
	for _, unit := range units {
		if unit == source.PhysicalUnit {
			continue
		}
		found := false
		for _, parentID := range sourceParents {
			child, e := scanModelPhysicalInvocation(tx.QueryRowContext(ctx, `SELECT `+modelPhysicalInvocationColumns+` FROM k12_model_physical_invocations WHERE parent_invocation_id=? AND physical_unit=?`, parentID, unit))
			if errors.Is(e, sql.ErrNoRows) {
				continue
			}
			if e != nil {
				return a, false, e
			}
			if child.Status != k12.ModelInvocationSucceeded {
				continue
			}
			if child.AgentName != a.AgentName || child.JobID != a.JobID || child.RouteSnapshot != source.RouteSnapshot || child.RequestPolicySnapshot != source.RequestPolicySnapshot {
				return a, false, ErrModelPhysicalInvocationConflict
			}
			var content sql.NullString
			if e = tx.QueryRowContext(ctx, `SELECT result_content FROM k12_model_physical_invocations WHERE physical_invocation_id=?`, child.PhysicalInvocationID).Scan(&content); e != nil {
				return a, false, e
			}
			if !content.Valid || physicalInvocationResultDigest(content.String) != child.ResultDigest {
				return a, false, ErrModelPhysicalInvocationConflict
			}
			if unit != k12.RecognitionPhysicalUnitWholePage {
				var classified bool
				if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_recognition_layout_batch_settlements WHERE source_physical_invocation_id=? AND source_physical_result_digest=? AND classification='classified')`, child.PhysicalInvocationID, child.ResultDigest).Scan(&classified); e != nil {
					return a, false, e
				}
				if !classified {
					continue
				}
			}
			found = true
			break
		}
		if !found {
			return a, false, ErrModelPhysicalInvocationConflict
		}
	}
	var otherUnknown int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM k12_model_physical_invocations WHERE parent_invocation_id=? AND status IN ('sent','outcome_unknown') AND physical_invocation_id!=?`, prior.InvocationID, source.PhysicalInvocationID).Scan(&otherUnknown); err != nil {
		return a, false, err
	}
	if otherUnknown != 0 {
		return a, false, ErrModelPhysicalInvocationConflict
	}
	route, _ := json.Marshal(parent.RouteSnapshot)
	policy, _ := json.Marshal(parent.RequestPolicySnapshot)
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_model_invocations (`+modelInvocationColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,'prepared',?,'','','','',?,?)`, parent.InvocationID, parent.AgentName, parent.JobID, parent.Stage, parent.RequestDigest, parent.RouteSnapshot.Provider, parent.RouteSnapshot.Model, string(route), string(policy), "", parent.Attempt, parent.CreatedAt, parent.CreatedAt)
	if err != nil {
		return a, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_recognition_recovery_authorizations (`+recognitionRecoveryColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.AuthorizationID, a.AgentName, a.DispatchID, a.JobID, a.SourceParentID, a.SourcePhysicalID, a.NewParentID, a.NewPhysicalID, a.NewRequestDigest, a.PageDigest, a.SourcePlanDigest, a.SourceRequestDigest, a.CandidateExactSetDigest, a.IdempotencyKey, a.RequestDigest, a.CreatedAt, a.SourceTimeoutMS, a.TimeoutOverrideMS)
	if err != nil {
		return a, false, err
	}
	raw, _ := json.Marshal(next)
	mp := gradingJobMapper{}
	vals, err := mp.encode(string(raw))
	if err != nil {
		return a, false, err
	}
	assigns := []string{}
	for _, col := range mp.domainCols() {
		assigns = append(assigns, col+"=?")
	}
	vals = append(vals, a.CreatedAt, a.JobID, a.AgentName, jobVersion)
	res, err := tx.ExecContext(ctx, `UPDATE k12_grading_jobs SET `+strings.Join(assigns, ",")+`,status='recognizing',version=version+1,updated_at=? WHERE record_id=? AND agent_name=? AND version=? AND status='outcome_unknown'`, vals...)
	if err != nil {
		return a, false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return a, false, records.ErrVersionConflict
	}
	res, err = tx.ExecContext(ctx, `UPDATE k12_image_task_dispatches SET automatic_started_at=?,automatic_deadline_at=?,automatic_remaining_seconds=?,version=version+1,updated_at=? WHERE dispatch_id=? AND agent_name=? AND version=?`, a.CreatedAt, next.Deadline, next.Deadline-a.CreatedAt, a.CreatedAt, a.DispatchID, a.AgentName, dispatchVersion)
	if err != nil {
		return a, false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return a, false, records.ErrVersionConflict
	}
	if err = tx.Commit(); err != nil {
		return a, false, err
	}
	return a, true, nil
}

// validateRecognitionRecoverySend 限定明确恢复的外发单元，保留主批次恢复原有的后续复读。
func validateRecognitionRecoverySend(ctx context.Context, q dbQueryer, child k12.ModelPhysicalInvocation) error {
	a, err := getRecognitionRecoveryByParent(ctx, q, child.AgentName, child.ParentInvocationID)
	if errors.Is(err, records.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	source, err := getModelPhysicalInvocationByIDVia(ctx, q, child.AgentName, a.SourcePhysicalID)
	if err != nil {
		return err
	}
	if source.Status != k12.ModelInvocationOutcomeUnknown || source.RequestDigest != a.SourceRequestDigest || !reflect.DeepEqual(source.RouteSnapshot, child.RouteSnapshot) || !reflect.DeepEqual(source.RequestPolicySnapshot, child.RequestPolicySnapshot) {
		return ErrModelPhysicalInvocationConflict
	}
	if strings.HasPrefix(string(source.PhysicalUnit), "layout_repair_") {
		if source.ParentInvocationID != a.SourceParentID || source.JobID != a.JobID || source.RecognitionPlanVersion != k12.RecognitionPlanVersionV2 || source.PlanDigest != a.SourcePlanDigest || source.CandidateExactSetDigest != a.CandidateExactSetDigest || child.JobID != a.JobID || child.RecognitionPlanVersion != k12.RecognitionPlanVersionV2 || a.TimeoutOverrideMS != 0 {
			return ErrModelPhysicalInvocationConflict
		}
		if child.PhysicalUnit != source.PhysicalUnit {
			return fmt.Errorf("%w: singleton recovery requires reuse of every other recognition unit", ErrModelPhysicalInvocationConflict)
		}
	}
	if child.PhysicalUnit == source.PhysicalUnit {
		if child.EffectiveTimeoutMS != a.TimeoutOverrideMS || child.PhysicalInvocationID != a.NewPhysicalID || child.RequestDigest != a.NewRequestDigest || child.CandidateExactSetDigest != a.CandidateExactSetDigest {
			return ErrModelPhysicalInvocationConflict
		}
		return nil
	}
	if child.PhysicalUnit == k12.RecognitionPhysicalUnitWholePage || strings.HasPrefix(string(child.PhysicalUnit), "layout_batch_") {
		return fmt.Errorf("%w: succeeded recognition unit must be reused", ErrModelPhysicalInvocationConflict)
	}
	// 具体 repair/adjudication 的范围仍由原计划及既有 Store 授权检查；历史成功或未知不能重发。
	var alreadySent bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_model_physical_invocations WHERE parent_invocation_id=? AND physical_unit=? AND status IN ('sent','succeeded','outcome_unknown'))`, a.SourceParentID, child.PhysicalUnit).Scan(&alreadySent); err != nil {
		return err
	}
	if alreadySent {
		return fmt.Errorf("%w: historical recognition receipt requires reuse or reconciliation", ErrModelPhysicalInvocationConflict)
	}
	return nil
}

// recognitionRecoverySourceParents 只沿本任务已接纳的明确授权追溯，不采用其他 unknown 来源。
func recognitionRecoverySourceParents(ctx context.Context, q dbQueryer, owner, parent string) ([]string, error) {
	var parents []string
	seen := map[string]bool{}
	for parent != "" {
		if seen[parent] {
			return nil, ErrModelInvocationConflict
		}
		seen[parent] = true
		parents = append(parents, parent)
		a, err := getRecognitionRecoveryByParent(ctx, q, owner, parent)
		if errors.Is(err, records.ErrNotFound) {
			return parents, nil
		}
		if err != nil {
			return nil, err
		}
		parent = a.SourceParentID
	}
	return parents, nil
}

// RecognitionUnknownHasReplacement 只在明确授权链最终已有成功后继时跳过历史未知。
func (s *Store) RecognitionUnknownHasReplacement(ctx context.Context, owner, physicalID string) (bool, error) {
	seen := map[string]bool{}
	for !seen[physicalID] {
		seen[physicalID] = true
		var next, status string
		err := s.db.QueryRowContext(ctx, `SELECT a.new_physical_id,p.status FROM k12_recognition_recovery_authorizations a JOIN k12_model_physical_invocations p ON p.physical_invocation_id=a.new_physical_id AND p.agent_name=a.agent_name AND p.job_id=a.job_id WHERE a.agent_name=? AND a.source_physical_id=?`, owner, physicalID).Scan(&next, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if status == string(k12.ModelInvocationSucceeded) {
			return true, nil
		}
		if status != string(k12.ModelInvocationOutcomeUnknown) {
			return false, nil
		}
		physicalID = next
	}
	return false, ErrModelInvocationConflict
}

func recognitionRecoveryEffectiveTimeout(ctx context.Context, q dbQueryer, child k12.ModelPhysicalInvocation) (int64, error) {
	a, err := getRecognitionRecoveryByParent(ctx, q, child.AgentName, child.ParentInvocationID)
	if errors.Is(err, records.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if child.PhysicalInvocationID != a.NewPhysicalID {
		return 0, nil
	}
	if child.RequestDigest != a.NewRequestDigest || child.CandidateExactSetDigest != a.CandidateExactSetDigest {
		return 0, ErrModelPhysicalInvocationConflict
	}
	return a.TimeoutOverrideMS, nil
}
