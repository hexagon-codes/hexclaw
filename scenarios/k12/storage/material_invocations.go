package k12storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type MaterialModelPolicy struct {
	Version string                   `json:"version"`
	Model   k12.GradingModelSnapshot `json:"model"`
}

// FreezeMaterialModel 在首次 Provider 发送前冻结模型；之后不能随设置变化替换。
func (s *Store) FreezeMaterialModel(ctx context.Context, p MaterialPreparation, model k12.GradingModelSnapshot) (MaterialModelPolicy, error) {
	policy := MaterialModelPolicy{Version: "independent-solve-v1", Model: k12.NormalizeGradingModelSnapshot(model)}
	if policy.Model.Provider == "" || policy.Model.Model == "" {
		return policy, errors.New("material model route is unavailable")
	}
	raw, _ := json.Marshal(policy)
	_, err := s.db.ExecContext(ctx, `UPDATE k12_material_preparations SET policy=? WHERE task_id=? AND state='running' AND policy='independent-solve-v1'`, string(raw), p.TaskID)
	if err != nil {
		return policy, err
	}
	var stored string
	if err = s.db.QueryRowContext(ctx, `SELECT policy FROM k12_material_preparations WHERE task_id=?`, p.TaskID).Scan(&stored); err != nil {
		return policy, err
	}
	err = json.Unmarshal([]byte(stored), &policy)
	return policy, err
}

type MaterialInvocation struct{ ID, TaskID, Operation, RequestDigest, Status, ResultJSON, ResultDigest string }

// ClaimMaterialInvocation 复用已完成回执；已发送无终态只返回未知，不创建第二次尝试。
func (s *Store) ClaimMaterialInvocation(ctx context.Context, p MaterialPreparation, operation, request string) (MaterialInvocation, bool, error) {
	inv := MaterialInvocation{ID: problemAssetRequestDigest([]byte(p.TaskID + "\x00" + operation + "\x00" + request)), TaskID: p.TaskID, Operation: operation, RequestDigest: request}
	if operation != "solve_generate" && operation != "solve_verify" && operation != "visual_extract" {
		return inv, false, errors.New("unsupported material operation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return inv, false, err
	}
	defer tx.Rollback()
	locked, err := tx.ExecContext(ctx, `UPDATE k12_material_preparations SET updated_at=updated_at WHERE task_id=? AND state='running' AND input_digest=?`, p.TaskID, p.InputDigest)
	if err != nil {
		return inv, false, err
	}
	active, _ := locked.RowsAffected()
	if active != 1 {
		return inv, false, ErrMaterialPreparationUnknown
	}
	if err = materialSourceCurrent(ctx, tx, p); err != nil {
		return inv, false, err
	}
	decision, err := loadMaterialRecoveryDecision(ctx, tx, p)
	if err != nil {
		return inv, false, err
	}
	unresolved, err := materialUnknownIDs(ctx, tx, p.TaskID)
	if err != nil {
		return inv, false, err
	}
	pendingRecovery := decision != nil && decision.Replacement == "" && len(unresolved) == 1 && unresolved[0] == decision.Original
	if len(unresolved) > 0 && !pendingRecovery {
		return inv, false, ErrMaterialPreparationUnknown
	}
	foundErr := tx.QueryRowContext(ctx, `SELECT invocation_id,status,result_json,result_digest FROM k12_material_invocations WHERE task_id=? AND operation=? AND request_digest=? ORDER BY attempt DESC LIMIT 1`, p.TaskID, operation, request).Scan(&inv.ID, &inv.Status, &inv.ResultJSON, &inv.ResultDigest)
	if foundErr == nil && inv.Status == "succeeded" {
		if inv.ResultDigest != problemAssetRequestDigest([]byte(inv.ResultJSON)) {
			return inv, false, ErrProblemAssetEvidence
		}
		if decision != nil {
			var plan MaterialRecoveryPlan
			if json.Unmarshal([]byte(decision.PlanJSON), &plan) != nil {
				return inv, false, ErrMaterialRecoveryConflict
			}
			reusable := inv.ID == decision.Replacement
			for _, r := range plan.Reusable {
				if r.InvocationID == inv.ID && r.Operation == operation && r.RequestDigest == request && r.ResultDigest == inv.ResultDigest {
					reusable = true
				}
			}
			if !reusable {
				return inv, false, ErrMaterialRecoveryConflict
			}
		}
		return inv, false, tx.Commit()
	}
	if foundErr != nil && !errors.Is(foundErr, sql.ErrNoRows) {
		return inv, false, foundErr
	}
	attempt := 0
	if decision != nil {
		if !pendingRecovery || decision.Operation != operation || decision.Request != request || foundErr != nil || inv.ID != decision.Original {
			return inv, false, ErrMaterialPreparationUnknown
		}
		attempt = decision.Attempt
		inv.ID = problemAssetRequestDigest([]byte(p.TaskID + "\x00" + operation + "\x00" + request + "\x00" + decision.ID))
	} else if foundErr == nil {
		if inv.Status == "not_sent" {
			return inv, false, egress.ErrProviderNotSent
		}
		return inv, false, ErrMaterialPreparationUnknown
	}
	inv.Status = "sent"
	inv.ResultJSON = ""
	inv.ResultDigest = ""
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_material_invocations(invocation_id,task_id,operation,request_digest,execution_kind,status,created_at,updated_at,attempt) VALUES(?,?,?,?,'provider','sent',?,?,?)`, inv.ID, p.TaskID, operation, request, nowUnix(), nowUnix(), attempt)
	if err != nil {
		return inv, false, err
	}
	if decision != nil {
		res, e := tx.ExecContext(ctx, `UPDATE k12_material_recovery_decisions SET replacement_invocation_id=? WHERE decision_id=? AND replacement_invocation_id IS NULL`, inv.ID, decision.ID)
		if e != nil {
			return inv, false, e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return inv, false, ErrMaterialPreparationUnknown
		}
	}
	return inv, true, tx.Commit()
}
func (s *Store) FinishMaterialInvocation(ctx context.Context, inv MaterialInvocation, payload string, callErr error) error {
	status := "succeeded"
	if callErr != nil {
		status = "outcome_unknown"
		if errors.Is(callErr, egress.ErrProviderNotSent) {
			status = "not_sent"
		}
	}
	res, err := s.db.ExecContext(ctx, `UPDATE k12_material_invocations SET status=?,result_json=?,result_digest=?,updated_at=? WHERE invocation_id=? AND status='sent'`, status, payload, problemAssetRequestDigest([]byte(payload)), nowUnix(), inv.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrMaterialPreparationUnknown
	}
	return nil
}

// materialModelVerifiedAnswer 将真实生成内容、独立程序执行回执和资产答案绑定。
func materialModelVerifiedAnswer(ctx context.Context, db dbHandle, p MaterialPreparation, result string) (string, error) {
	var solved struct {
		Solution string
		Evidence struct{ Verdict, EvidenceType, SolverOutputDigest, VerificationInputDigest, VerificationRunID string }
	}
	if json.Unmarshal([]byte(result), &solved) != nil || solved.Evidence.Verdict != "agree" || solved.Evidence.EvidenceType != "numeric_exec" || solved.Evidence.SolverOutputDigest == "" || solved.Evidence.VerificationRunID == "" {
		return "", ErrProblemAssetEvidence
	}
	rows, err := db.QueryContext(ctx, `SELECT operation,result_json,result_digest FROM k12_material_invocations WHERE task_id=? AND execution_kind='provider' AND status='succeeded'`, p.TaskID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	selected := ""
	verified := false
	for rows.Next() {
		var op, payload, digest string
		if err = rows.Scan(&op, &payload, &digest); err != nil {
			return "", err
		}
		if digest != problemAssetRequestDigest([]byte(payload)) {
			return "", ErrProblemAssetEvidence
		}
		var r struct {
			Output    string
			Execution struct {
				InputDigest     string `json:"input_digest"`
				RunID           string `json:"run_id"`
				Status          string `json:"status"`
				ExitCode        int    `json:"exit_code"`
				Timeout         bool   `json:"timeout"`
				RuntimeMissing  bool   `json:"runtime_missing"`
				Truncated       bool   `json:"truncated"`
				Error           string
				StdoutBytes     int64  `json:"stdout_bytes"`
				Stdout          string `json:"stdout"`
				StdoutTruncated bool   `json:"stdout_truncated"`
				StderrTruncated bool   `json:"stderr_truncated"`
			} `json:"execution_receipt"`
		}
		if json.Unmarshal([]byte(payload), &r) != nil {
			return "", ErrProblemAssetEvidence
		}
		if op == "solve_generate" {
			d := sha256.Sum256([]byte(r.Output))
			if hex.EncodeToString(d[:]) == solved.Evidence.SolverOutputDigest {
				selected = r.Output
			}
		}
		if op == "solve_verify" {
			e := r.Execution
			if e.RunID == solved.Evidence.VerificationRunID && e.InputDigest == solved.Evidence.VerificationInputDigest && e.Status == "success" && e.ExitCode == 0 && e.Error == "" && !e.Timeout && !e.RuntimeMissing && !e.Truncated && !e.StdoutTruncated && !e.StderrTruncated && e.StdoutBytes > 0 && int64(len(e.Stdout)) == e.StdoutBytes {
				verified = true
			}
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if selected == "" || !verified {
		return "", ErrProblemAssetEvidence
	}
	return selected, nil
}
func (s *Store) SaveMaterialModelVerification(ctx context.Context, p MaterialPreparation, result string) (string, error) {
	var visualErr error
	result, visualErr = materialResultWithVisual(p, result)
	if visualErr != nil {
		return "", visualErr
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = materialFactsForResult(ctx, tx, p, result); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE k12_material_preparations SET updated_at=updated_at WHERE task_id=? AND state='running'`, p.TaskID); err != nil {
		return "", err
	}
	answer, err := materialModelVerifiedAnswer(ctx, tx, p, result)
	if err != nil {
		return "", err
	}
	var raw map[string]any
	if err = json.Unmarshal([]byte(result), &raw); err != nil {
		return "", err
	}
	raw["Solution"] = answer
	encoded, _ := json.Marshal(raw)
	result = string(encoded)
	res, err := tx.ExecContext(ctx, `UPDATE k12_material_preparations SET state='verified',result_json=?,result_digest=?,updated_at=? WHERE task_id=? AND state='running'`, result, problemAssetRequestDigest(encoded), nowUnix(), p.TaskID)
	if err != nil {
		return "", err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return "", ErrMaterialPreparationFenced
	}
	if err = materialSourceCurrent(ctx, tx, p); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return result, nil
}

func materialModelProofJSON(p MaterialPreparation) string {
	return fmt.Sprintf(`{"source_kind":"material_preparation","task_id":%q,"execution_kind":"provider"}`, p.TaskID)
}

// MaterialHasUnknownInvocation 以物理回执为准，不把求解器降级文本当成已知完成。
func (s *Store) MaterialHasUnknownInvocation(ctx context.Context, task string) (bool, error) {
	ids, err := materialUnknownIDs(ctx, s.db, task)
	return len(ids) > 0, err
}
