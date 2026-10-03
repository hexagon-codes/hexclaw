package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

var ErrMaterialRecoveryConflict = errors.New("material recovery plan is no longer current")
var ErrMaterialRecoveryInvalid = errors.New("material recovery requires a plan, idempotency key and duplicate-charge acknowledgement")

// 被消费的决定只替代原调用；新调用未知时仍受同一栅栏约束。
const materialUnresolvedPredicate = `i.status IN ('sent','outcome_unknown') AND NOT EXISTS(SELECT 1 FROM k12_material_recovery_decisions r WHERE r.original_invocation_id=i.invocation_id AND r.replacement_invocation_id IS NOT NULL)`

type MaterialRecoveryReceipt struct {
	InvocationID  string `json:"invocation_id"`
	Operation     string `json:"operation"`
	RequestDigest string `json:"request_digest"`
	ResultDigest  string `json:"result_digest,omitempty"`
	Attempt       int    `json:"attempt"`
}
type MaterialRecoveryPlan struct {
	DocumentID         string                    `json:"document_id"`
	TaskID             string                    `json:"task_id"`
	SourceRevision     int64                     `json:"source_revision"`
	SourceDigest       string                    `json:"source_digest"`
	InputDigest        string                    `json:"input_digest"`
	PolicyDigest       string                    `json:"policy_digest"`
	Model              MaterialModelPolicy       `json:"model_policy"`
	Unknown            MaterialRecoveryReceipt   `json:"unknown"`
	Reusable           []MaterialRecoveryReceipt `json:"reusable"`
	MayDuplicateCharge bool                      `json:"may_duplicate_charge"`
	Fingerprint        string                    `json:"fingerprint"`
}
type MaterialRecoveryResult struct {
	DecisionID              string `json:"decision_id"`
	TaskID                  string `json:"task_id"`
	State                   string `json:"state"`
	ReplacementInvocationID string `json:"replacement_invocation_id,omitempty"`
}
type materialRecoveryDecision struct {
	ID, Original, Operation, Request, Fingerprint, PlanJSON, Replacement string
	Attempt                                                              int
}

func materialRecoveryTask(ctx context.Context, db dbHandle, owner, document, task string) (MaterialPreparation, error) {
	p, err := scanMaterial(db.QueryRowContext(ctx, `SELECT `+materialColumns+` FROM k12_material_preparations WHERE owner_id=? AND document_id=? AND task_id=?`, owner, document, task))
	if err != nil {
		return p, err
	}
	if err = materialSourceCurrent(ctx, db, p); err != nil {
		return p, err
	}
	var raw string
	if err = db.QueryRowContext(ctx, `SELECT candidate_json FROM k12_material_preparations WHERE task_id=?`, task).Scan(&raw); err != nil {
		return p, err
	}
	if problemAssetRequestDigest([]byte(raw)) != p.InputDigest {
		return p, ErrMaterialRecoveryConflict
	}
	return p, nil
}
func loadMaterialRecoveryPlan(ctx context.Context, db dbHandle, owner, document, task string) (MaterialRecoveryPlan, error) {
	var plan MaterialRecoveryPlan
	p, err := materialRecoveryTask(ctx, db, owner, document, task)
	if err != nil {
		return plan, err
	}
	if p.State != "outcome_unknown" {
		return plan, ErrMaterialRecoveryConflict
	}
	plan = MaterialRecoveryPlan{DocumentID: document, TaskID: task, SourceRevision: p.SourceRevision, InputDigest: p.InputDigest, PolicyDigest: problemAssetRequestDigest([]byte(p.Policy)), MayDuplicateCharge: true}
	if json.Unmarshal([]byte(p.Policy), &plan.Model) != nil || plan.Model.Model.Provider == "" || plan.Model.Model.Model == "" {
		return plan, ErrMaterialRecoveryConflict
	}
	if err = db.QueryRowContext(ctx, `SELECT source_digest FROM k12_material_manifests WHERE owner_id=? AND document_id=? AND source_revision=?`, owner, document, p.SourceRevision).Scan(&plan.SourceDigest); err != nil {
		return plan, err
	}
	rows, err := db.QueryContext(ctx, `SELECT i.invocation_id,i.operation,i.request_digest,i.result_digest,i.attempt,i.status,i.execution_kind,i.result_json,(`+materialUnresolvedPredicate+`) FROM k12_material_invocations i WHERE i.task_id=? ORDER BY i.invocation_id`, task)
	if err != nil {
		return plan, err
	}
	defer rows.Close()
	unknown, generated, visual := 0, 0, 0
	for rows.Next() {
		var r MaterialRecoveryReceipt
		var status, kind, raw string
		var unresolved bool
		if err = rows.Scan(&r.InvocationID, &r.Operation, &r.RequestDigest, &r.ResultDigest, &r.Attempt, &status, &kind, &raw, &unresolved); err != nil {
			return plan, err
		}
		if unresolved {
			unknown++
			if r.Operation != "solve_verify" || kind != "provider" {
				return plan, ErrMaterialRecoveryConflict
			}
			plan.Unknown = r
		}
		if status == "succeeded" {
			if r.ResultDigest != problemAssetRequestDigest([]byte(raw)) {
				return plan, ErrProblemAssetEvidence
			}
			plan.Reusable = append(plan.Reusable, r)
			if r.Operation == "solve_generate" {
				generated++
			}
			if r.Operation == "visual_extract" {
				visual++
			}
		}
	}
	if err = rows.Err(); err != nil {
		return plan, err
	}
	if unknown != 1 || generated == 0 || (len(p.Candidate.VisualObjectIDs)+len(p.Candidate.VisualPDFPages) > 0 && visual != 1) {
		return plan, ErrMaterialRecoveryConflict
	}
	raw, _ := json.Marshal(plan)
	plan.Fingerprint = problemAssetRequestDigest(append([]byte(owner+"\x00"), raw...))
	return plan, nil
}
func (s *Store) MaterialRecoveryPlan(ctx context.Context, owner, document, task string) (MaterialRecoveryPlan, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MaterialRecoveryPlan{}, err
	}
	defer tx.Rollback()
	return loadMaterialRecoveryPlan(ctx, tx, owner, document, task)
}
func materialRecoveryResult(ctx context.Context, db dbHandle, id string) (MaterialRecoveryResult, error) {
	var r MaterialRecoveryResult
	err := db.QueryRowContext(ctx, `SELECT r.decision_id,r.task_id,p.state,COALESCE(r.replacement_invocation_id,'') FROM k12_material_recovery_decisions r JOIN k12_material_preparations p ON p.task_id=r.task_id WHERE r.decision_id=?`, id).Scan(&r.DecisionID, &r.TaskID, &r.State, &r.ReplacementInvocationID)
	return r, err
}

// RecoverMaterialPreparation 只将明确确认的原任务重新排队，实际发送由原 worker 消费一次授权。
func (s *Store) RecoverMaterialPreparation(ctx context.Context, owner, document, task, key, fingerprint string, confirmed bool) (MaterialRecoveryResult, error) {
	if !confirmed || strings.TrimSpace(key) == "" || len(key) > 220 || fingerprint == "" {
		return MaterialRecoveryResult{}, ErrMaterialRecoveryInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MaterialRecoveryResult{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE k12_material_preparations SET updated_at=updated_at WHERE owner_id=? AND document_id=? AND task_id=?`, owner, document, task); err != nil {
		return MaterialRecoveryResult{}, err
	}
	var existing, existingTask, existingDoc, existingFingerprint string
	err = tx.QueryRowContext(ctx, `SELECT r.decision_id,r.task_id,p.document_id,r.plan_fingerprint FROM k12_material_recovery_decisions r JOIN k12_material_preparations p ON p.task_id=r.task_id WHERE r.owner_id=? AND r.idempotency_key=?`, owner, key).Scan(&existing, &existingTask, &existingDoc, &existingFingerprint)
	if err == nil {
		if existingTask != task || existingDoc != document || existingFingerprint != fingerprint {
			return MaterialRecoveryResult{}, ErrMaterialRecoveryConflict
		}
		result, readErr := materialRecoveryResult(ctx, tx, existing)
		if readErr != nil {
			return result, readErr
		}
		if result.State == "needs_review" {
			if readErr = requeueSavedMaterialVerification(ctx, tx, owner, document, task, existing); readErr != nil {
				return result, readErr
			}
			result, readErr = materialRecoveryResult(ctx, tx, existing)
			if readErr != nil {
				return result, readErr
			}
		}
		return result, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MaterialRecoveryResult{}, err
	}
	plan, err := loadMaterialRecoveryPlan(ctx, tx, owner, document, task)
	if err != nil {
		return MaterialRecoveryResult{}, err
	}
	if plan.Fingerprint != fingerprint {
		return MaterialRecoveryResult{}, ErrMaterialRecoveryConflict
	}
	var decided bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_material_recovery_decisions WHERE original_invocation_id=?)`, plan.Unknown.InvocationID).Scan(&decided); err != nil {
		return MaterialRecoveryResult{}, err
	}
	if decided {
		return MaterialRecoveryResult{}, ErrMaterialRecoveryConflict
	}
	id := problemAssetRequestDigest([]byte(owner + "\x00" + key + "\x00" + fingerprint))
	raw, _ := json.Marshal(plan)
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_material_recovery_decisions(decision_id,owner_id,task_id,original_invocation_id,operation,request_digest,replacement_attempt,plan_fingerprint,plan_json,idempotency_key,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, owner, task, plan.Unknown.InvocationID, plan.Unknown.Operation, plan.Unknown.RequestDigest, plan.Unknown.Attempt+1, fingerprint, string(raw), key, nowUnix())
	if err != nil {
		return MaterialRecoveryResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE k12_material_preparations SET state='queued',reason='Explicit verification recovery queued',updated_at=? WHERE task_id=? AND state='outcome_unknown'`, nowUnix(), task); err != nil {
		return MaterialRecoveryResult{}, err
	}
	r, err := materialRecoveryResult(ctx, tx, id)
	if err != nil {
		return r, err
	}
	return r, tx.Commit()
}
func loadMaterialRecoveryDecision(ctx context.Context, db dbHandle, p MaterialPreparation) (*materialRecoveryDecision, error) {
	var d materialRecoveryDecision
	err := db.QueryRowContext(ctx, `SELECT decision_id,original_invocation_id,operation,request_digest,replacement_attempt,plan_fingerprint,plan_json,COALESCE(replacement_invocation_id,'') FROM k12_material_recovery_decisions WHERE owner_id=? AND task_id=? ORDER BY replacement_attempt DESC LIMIT 1`, p.OwnerID, p.TaskID).Scan(&d.ID, &d.Original, &d.Operation, &d.Request, &d.Attempt, &d.Fingerprint, &d.PlanJSON, &d.Replacement)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	current, currentErr := materialRecoveryTask(ctx, db, p.OwnerID, p.DocumentID, p.TaskID)
	if currentErr != nil {
		return nil, currentErr
	}
	if current.InputDigest != p.InputDigest || current.Policy != p.Policy {
		return nil, ErrMaterialRecoveryConflict
	}
	var plan MaterialRecoveryPlan
	if json.Unmarshal([]byte(d.PlanJSON), &plan) != nil {
		return nil, ErrMaterialRecoveryConflict
	}
	if plan.TaskID != p.TaskID || plan.DocumentID != p.DocumentID || plan.SourceRevision != p.SourceRevision || plan.InputDigest != p.InputDigest || plan.PolicyDigest != problemAssetRequestDigest([]byte(p.Policy)) {
		return nil, ErrMaterialRecoveryConflict
	}
	var digest string
	if err = db.QueryRowContext(ctx, `SELECT source_digest FROM k12_material_manifests WHERE owner_id=? AND document_id=? AND source_revision=?`, p.OwnerID, p.DocumentID, p.SourceRevision).Scan(&digest); err != nil {
		return nil, err
	}
	if digest != plan.SourceDigest {
		return nil, ErrMaterialRecoveryConflict
	}
	return &d, nil
}
func (s *Store) MaterialHasRecovery(ctx context.Context, p MaterialPreparation) (bool, error) {
	d, err := loadMaterialRecoveryDecision(ctx, s.db, p)
	return d != nil, err
}

func materialUnknownIDs(ctx context.Context, db dbHandle, task string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT i.invocation_id FROM k12_material_invocations i WHERE i.task_id=? AND `+materialUnresolvedPredicate, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// MaterialRecoveryPending 只认尚未消费、且唯一未决调用就是已授权原验证的恢复。
func (s *Store) MaterialRecoveryPending(ctx context.Context, p MaterialPreparation) (bool, error) {
	d, err := loadMaterialRecoveryDecision(ctx, s.db, p)
	if err != nil || d == nil || d.Replacement != "" {
		return false, err
	}
	ids, err := materialUnknownIDs(ctx, s.db, p.TaskID)
	return err == nil && len(ids) == 1 && ids[0] == d.Original, err
}

// 已成功的替代验证仅重放缓存评估；不会创建新授权或新的模型调用。
func requeueSavedMaterialVerification(ctx context.Context, tx *sql.Tx, owner, document, task, decisionID string) error {
	p, err := materialRecoveryTask(ctx, tx, owner, document, task)
	if err != nil {
		return err
	}
	if p.Reason != "No independent executable verification" && p.Reason != "Independent verification evidence is incomplete" {
		return nil
	}
	d, err := loadMaterialRecoveryDecision(ctx, tx, p)
	if err != nil {
		return err
	}
	if d == nil || d.ID != decisionID || d.Replacement == "" {
		return ErrMaterialRecoveryConflict
	}
	ids, err := materialUnknownIDs(ctx, tx, task)
	if err != nil {
		return err
	}
	if len(ids) != 0 {
		return ErrMaterialPreparationUnknown
	}
	var status, payload, digest string
	if err = tx.QueryRowContext(ctx, `SELECT status,result_json,result_digest FROM k12_material_invocations WHERE invocation_id=? AND task_id=? AND operation=? AND request_digest=?`, d.Replacement, task, d.Operation, d.Request).Scan(&status, &payload, &digest); err != nil {
		return err
	}
	if status != "succeeded" || digest != problemAssetRequestDigest([]byte(payload)) {
		return ErrProblemAssetEvidence
	}
	_, err = tx.ExecContext(ctx, `UPDATE k12_material_preparations SET state='queued',reason='Saved verification reassessment queued',updated_at=? WHERE task_id=? AND state='needs_review'`, nowUnix(), task)
	return err
}
