package k12storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

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
	if operation != "solve_generate" && operation != "solve_verify" {
		return inv, false, errors.New("unsupported material operation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return inv, false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO k12_material_invocations(invocation_id,task_id,operation,request_digest,execution_kind,status,created_at,updated_at) VALUES(?,?,?,?,'provider','sent',?,?) ON CONFLICT(task_id,operation,request_digest) DO NOTHING`, inv.ID, p.TaskID, operation, request, nowUnix(), nowUnix())
	if err != nil {
		return inv, false, err
	}
	fresh, _ := res.RowsAffected()
	if err = materialSourceCurrent(ctx, tx, p); err != nil {
		return inv, false, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT status,result_json,result_digest FROM k12_material_invocations WHERE invocation_id=?`, inv.ID).Scan(&inv.Status, &inv.ResultJSON, &inv.ResultDigest); err != nil {
		return inv, false, err
	}
	if fresh == 0 && inv.Status != "succeeded" {
		return inv, false, ErrMaterialPreparationUnknown
	}
	if err = tx.Commit(); err != nil {
		return inv, false, err
	}
	return inv, fresh == 1, nil
}
func (s *Store) FinishMaterialInvocation(ctx context.Context, inv MaterialInvocation, payload string, callErr error) error {
	status := "succeeded"
	if callErr != nil {
		status = "outcome_unknown"
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
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
	var unknown bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_material_invocations WHERE task_id=? AND status IN ('sent','outcome_unknown'))`, task).Scan(&unknown)
	return unknown, err
}
