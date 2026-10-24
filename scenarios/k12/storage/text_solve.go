package k12storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

var ErrTextSolveConflict = errors.New("text solve identity or result conflict")
var ErrTextSolveUnknown = errors.New("text solve invocation outcome requires reconciliation")

func textSolveDigest(raw string) string {
	h := sha256.Sum256(append([]byte{0}, []byte(raw)...))
	return "sha256:" + hex.EncodeToString(h[:])
}

func (s *Store) PrepareTextSolveRun(ctx context.Context, run k12.TextSolveRun) (k12.TextSolveRun, bool, error) {
	if run.OwnerID == "" || run.AgentName == "" || run.RunID == "" || run.InputRevision < 1 || !json.Valid([]byte(run.RequestJSON)) || run.InputDigest != textSolveDigest(run.RequestJSON) {
		return run, false, ErrTextSolveConflict
	}
	identity, err := run.Facts.ExactIdentity(run.OwnerID)
	if err != nil || identity.FactsDigest != run.FactsDigest {
		return run, false, ErrTextSolveConflict
	}
	if err = ensureAgentRegistered(ctx, s.db, run.AgentName); err != nil {
		return run, false, err
	}
	if run.SourceMessageID != "" && run.SourceSessionID == "" {
		return run, false, ErrTextSolveConflict
	}
	if run.SourceSessionID != "" {
		var exists bool
		if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id=?)`, run.SourceSessionID).Scan(&exists); err != nil {
			return run, false, err
		}
		if !exists {
			return run, false, ErrTextSolveConflict
		}
	}
	if run.SourceMessageID != "" {
		var exists bool
		if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE id=? AND session_id=? AND role='user')`, run.SourceMessageID, run.SourceSessionID).Scan(&exists); err != nil {
			return run, false, err
		}
		if !exists {
			return run, false, ErrTextSolveConflict
		}
	}
	run.CreatedAt = nowUnix()
	raw, _ := json.Marshal(run)
	res, err := s.db.ExecContext(ctx, `INSERT INTO k12_text_solve_runs(owner_id,agent_name,solve_run_id,input_digest,facts_digest,data,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(owner_id,agent_name,solve_run_id) DO NOTHING`, run.OwnerID, run.AgentName, run.RunID, run.InputDigest, run.FactsDigest, string(raw), run.CreatedAt)
	if err != nil {
		return run, false, err
	}
	n, _ := res.RowsAffected()
	prior, err := s.GetTextSolveRun(ctx, run.OwnerID, run.AgentName, run.RunID)
	if err != nil {
		return run, false, err
	}
	a, b := prior, run
	a.CreatedAt, b.CreatedAt = 0, 0
	if !reflect.DeepEqual(a, b) {
		return prior, false, ErrTextSolveConflict
	}
	return prior, n == 1, nil
}

func getTextSolveRun(ctx context.Context, db dbHandle, owner, agent, id string) (k12.TextSolveRun, error) {
	var run k12.TextSolveRun
	var raw string
	err := db.QueryRowContext(ctx, `SELECT data FROM k12_text_solve_runs WHERE owner_id=? AND agent_name=? AND solve_run_id=?`, owner, agent, id).Scan(&raw)
	if err != nil {
		return run, err
	}
	err = json.Unmarshal([]byte(raw), &run)
	return run, err
}
func (s *Store) GetTextSolveRun(ctx context.Context, owner, agent, id string) (k12.TextSolveRun, error) {
	return getTextSolveRun(ctx, s.db, owner, agent, id)
}

func scanTextSolveInvocation(row rowScanner) (k12.TextSolveInvocation, error) {
	var inv k12.TextSolveInvocation
	var raw string
	var attempt int
	err := row.Scan(&raw, &attempt)
	if err != nil {
		return inv, err
	}
	err = json.Unmarshal([]byte(raw), &inv)
	inv.OperationAttempt = attempt
	return inv, err
}
func (s *Store) GetTextSolveInvocation(ctx context.Context, owner, agent, id string) (k12.TextSolveInvocation, error) {
	return scanTextSolveInvocation(s.db.QueryRowContext(ctx, `SELECT data,operation_attempt FROM k12_text_solve_invocations WHERE owner_id=? AND agent_name=? AND invocation_id=?`, owner, agent, id))
}
func (s *Store) ListTextSolveInvocations(ctx context.Context, owner, agent, run string) ([]k12.TextSolveInvocation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data,operation_attempt FROM k12_text_solve_invocations WHERE owner_id=? AND agent_name=? AND solve_run_id=? ORDER BY created_at,operation,operation_attempt`, owner, agent, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []k12.TextSolveInvocation{}
	for rows.Next() {
		v, e := scanTextSolveInvocation(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// PrepareTextSolveInvocation 的首条 INSERT 在同一事务中分配本次操作序号。
func (s *Store) PrepareTextSolveInvocation(ctx context.Context, inv k12.TextSolveInvocation) (k12.TextSolveInvocation, error) {
	run, err := s.GetTextSolveRun(ctx, inv.OwnerID, inv.AgentName, inv.RunID)
	if err != nil {
		return inv, err
	}
	if inv.InvocationID == "" || inv.RequestDigest == "" || inv.Operation == "" || !inv.ExecutionKind.Valid() || inv.InputDigest != run.InputDigest || inv.FactsDigest != run.FactsDigest || inv.TextSolveOrigin != run.TextSolveOrigin || !reflect.DeepEqual(inv.RouteSnapshot, run.RouteSnapshot) {
		return inv, ErrTextSolveConflict
	}
	inv.Status = k12.ModelInvocationPrepared
	inv.CreatedAt = nowUnix()
	inv.UpdatedAt = inv.CreatedAt
	raw, _ := json.Marshal(inv)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return inv, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_text_solve_invocations(invocation_id,owner_id,agent_name,solve_run_id,operation,execution_kind,operation_attempt,request_digest,status,data,created_at,updated_at)
 VALUES(?,?,?,?,?,?,COALESCE((SELECT MAX(operation_attempt) FROM k12_text_solve_invocations WHERE owner_id=? AND agent_name=? AND solve_run_id=? AND operation=?),0)+1,?,?,?,?,?)
 ON CONFLICT(owner_id,agent_name,solve_run_id,operation,execution_kind,request_digest) DO NOTHING`, inv.InvocationID, inv.OwnerID, inv.AgentName, inv.RunID, inv.Operation, inv.ExecutionKind, inv.OwnerID, inv.AgentName, inv.RunID, inv.Operation, inv.RequestDigest, inv.Status, string(raw), inv.CreatedAt, inv.UpdatedAt)
	if err != nil {
		return inv, err
	}
	prior, err := scanTextSolveInvocation(tx.QueryRowContext(ctx, `SELECT data,operation_attempt FROM k12_text_solve_invocations WHERE owner_id=? AND agent_name=? AND solve_run_id=? AND operation=? AND execution_kind=? AND request_digest=?`, inv.OwnerID, inv.AgentName, inv.RunID, inv.Operation, inv.ExecutionKind, inv.RequestDigest))
	if err != nil {
		return inv, err
	}
	if prior.InputDigest != inv.InputDigest || prior.FactsDigest != inv.FactsDigest || prior.InputRevision != inv.InputRevision || !reflect.DeepEqual(prior.RouteSnapshot, inv.RouteSnapshot) {
		return prior, ErrTextSolveConflict
	}
	return prior, tx.Commit()
}

func (s *Store) ClaimTextSolveInvocation(ctx context.Context, owner, agent, id string) (k12.TextSolveInvocation, bool, error) {
	inv, err := s.GetTextSolveInvocation(ctx, owner, agent, id)
	if err != nil {
		return inv, false, err
	}
	if inv.Status != k12.ModelInvocationPrepared {
		return inv, false, nil
	}
	inv.Status = k12.ModelInvocationSent
	inv.UpdatedAt = nowUnix()
	raw, _ := json.Marshal(inv)
	res, err := s.db.ExecContext(ctx, `UPDATE k12_text_solve_invocations SET status=?,data=?,updated_at=? WHERE owner_id=? AND agent_name=? AND invocation_id=? AND status=?`, inv.Status, string(raw), inv.UpdatedAt, owner, agent, id, k12.ModelInvocationPrepared)
	if err != nil {
		return inv, false, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		inv, err = s.GetTextSolveInvocation(ctx, owner, agent, id)
	}
	return inv, n == 1, err
}

func (s *Store) FinishTextSolveInvocation(ctx context.Context, owner, agent, id string, status k12.ModelInvocationStatus, payload, failureClass, failureCode string) (k12.TextSolveInvocation, error) {
	inv, err := s.GetTextSolveInvocation(ctx, owner, agent, id)
	if err != nil {
		return inv, err
	}
	if status != k12.ModelInvocationSucceeded && status != k12.ModelInvocationFailed && status != k12.ModelInvocationOutcomeUnknown {
		return inv, ErrTextSolveConflict
	}
	if status == k12.ModelInvocationSucceeded && !json.Valid([]byte(payload)) {
		return inv, ErrTextSolveConflict
	}
	if inv.Status != k12.ModelInvocationSent {
		if inv.Status == status && inv.ResultJSON == payload && inv.FailureClass == failureClass && inv.FailureCode == failureCode {
			return inv, nil
		}
		return inv, ErrTextSolveConflict
	}
	inv.Status, inv.ResultJSON, inv.FailureClass, inv.FailureCode = status, payload, failureClass, failureCode
	inv.ResultDigest = textSolveDigest(payload)
	inv.UpdatedAt = nowUnix()
	raw, _ := json.Marshal(inv)
	res, err := s.db.ExecContext(ctx, `UPDATE k12_text_solve_invocations SET status=?,data=?,updated_at=? WHERE owner_id=? AND agent_name=? AND invocation_id=? AND status=?`, status, string(raw), inv.UpdatedAt, owner, agent, id, k12.ModelInvocationSent)
	if err != nil {
		return inv, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return inv, ErrTextSolveConflict
	}
	return inv, nil
}

func getTextSolveFinalization(ctx context.Context, db dbHandle, owner, agent, runID string) (k12.TextSolveFinalization, error) {
	var result k12.TextSolveFinalization
	var raw string
	err := db.QueryRowContext(ctx, `SELECT data FROM k12_text_solve_finalizations WHERE owner_id=? AND agent_name=? AND solve_run_id=?`, owner, agent, runID).Scan(&raw)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal([]byte(raw), &result)
	return result, err
}
func (s *Store) GetTextSolveFinalization(ctx context.Context, owner, agent, runID string) (k12.TextSolveFinalization, error) {
	return getTextSolveFinalization(ctx, s.db, owner, agent, runID)
}

// FinalizeTextSolve 将真实结果、来源和待发布事件原子提交，失败发布不再次求解。
func (s *Store) FinalizeTextSolve(ctx context.Context, f k12.TextSolveFinalization) (k12.TextSolveFinalization, bool, error) {
	if f.SourceKind == "" {
		f.SourceKind = k12.ProblemAssetSourceTextSolve
	}
	if f.SourceKind != k12.ProblemAssetSourceTextSolve {
		return f, false, ErrTextSolveConflict
	}
	if f.FinalizationID == "" || f.InvocationID == "" || !json.Valid([]byte(f.ResultJSON)) {
		return f, false, ErrTextSolveConflict
	}
	f.ResultDigest = textSolveDigest(f.ResultJSON)
	f.CreatedAt = nowUnix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return f, false, err
	}
	defer tx.Rollback()
	// 先取得写锁，不能在只读资格检查后被失效命令插入。
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_text_solve_finalizations(finalization_id,owner_id,agent_name,solve_run_id,result_digest,data,created_at) VALUES(?,?,?,?,?,'{}',?) ON CONFLICT(owner_id,agent_name,solve_run_id) DO NOTHING`, f.FinalizationID, f.OwnerID, f.AgentName, f.RunID, f.ResultDigest, f.CreatedAt)
	if err != nil {
		return f, false, err
	}
	var storedRaw string
	if err = tx.QueryRowContext(ctx, `SELECT data FROM k12_text_solve_finalizations WHERE owner_id=? AND agent_name=? AND solve_run_id=?`, f.OwnerID, f.AgentName, f.RunID).Scan(&storedRaw); err != nil {
		return f, false, err
	}
	if storedRaw != "{}" {
		var prior k12.TextSolveFinalization
		if json.Unmarshal([]byte(storedRaw), &prior) != nil {
			return f, false, ErrTextSolveConflict
		}
		a, b := prior, f
		a.CreatedAt, b.CreatedAt = 0, 0
		if !reflect.DeepEqual(a, b) {
			return prior, false, ErrTextSolveConflict
		}
		return prior, false, tx.Commit()
	}
	run, err := getTextSolveRun(ctx, tx, f.OwnerID, f.AgentName, f.RunID)
	if err != nil {
		return f, false, err
	}
	if f.InputDigest != run.InputDigest || f.FactsDigest != run.FactsDigest || f.TextSolveOrigin != run.TextSolveOrigin {
		return f, false, ErrTextSolveConflict
	}
	inv, err := scanTextSolveInvocation(tx.QueryRowContext(ctx, `SELECT data,operation_attempt FROM k12_text_solve_invocations WHERE owner_id=? AND agent_name=? AND solve_run_id=? AND invocation_id=?`, f.OwnerID, f.AgentName, f.RunID, f.InvocationID))
	if err != nil {
		return f, false, err
	}
	if inv.Status != k12.ModelInvocationSucceeded || inv.ResultJSON != f.ResultJSON || inv.ResultDigest != f.ResultDigest || inv.InputDigest != f.InputDigest {
		return f, false, ErrTextSolveConflict
	}
	if f.AnswerSource != nil && f.AnswerSource.Kind == k12.ProblemAnswerAsset {
		a, err := scanProblemAssetAdoption(tx.QueryRowContext(ctx, `SELECT `+assetAdoptionColumns+` FROM k12_problem_asset_adoptions WHERE owner_id=? AND adoption_id=?`, f.OwnerID, f.AnswerSource.AdoptionID))
		if err != nil {
			return f, false, err
		}
		if a.JobID != k12.TextSolveAdoptionTaskID(f.AgentName, f.RunID) || a.InputDigest != f.InputDigest || a.InputRevision != f.InputRevision || a.AssetID != f.AnswerSource.AssetID || a.AssetVersion != f.AnswerSource.AssetVersion || a.AssetRevision != f.AnswerSource.AssetRevision || a.FactsDigest != f.AnswerSource.FactsDigest {
			return f, false, ErrTextSolveConflict
		}
		if a.FactsDigest != f.FactsDigest {
			version, err := getProblemAssetVersion(ctx, tx, a.OwnerID, a.AssetID, a.AssetVersion)
			if err != nil {
				return f, false, err
			}
			if !k12.EquivalentProblemAssetExpression(run.Facts, version.Facts) && !k12.EquivalentProblemAssetSourceFormat(run.Facts, version.Facts, true) {
				return f, false, ErrTextSolveConflict
			}
		}
		if err = validateProblemAssetCurrent(ctx, tx, a); err != nil {
			return f, false, err
		}
	}
	if f.AnswerSource != nil {
		if f.AnswerSource.Validate() != nil || f.AnswerSource.Kind != k12.ProblemAnswerAsset && f.AnswerSource.FactsDigest != f.FactsDigest {
			return f, false, ErrTextSolveConflict
		}
		if f.AnswerSource.Kind != k12.ProblemAnswerAsset {
			source, err := scanTextSolveInvocation(tx.QueryRowContext(ctx, `SELECT data,operation_attempt FROM k12_text_solve_invocations WHERE owner_id=? AND agent_name=? AND solve_run_id=? AND invocation_id=?`, f.OwnerID, f.AgentName, f.RunID, f.AnswerSource.InvocationID))
			if err != nil {
				return f, false, err
			}
			if source.Status != k12.ModelInvocationSucceeded || source.InputDigest != f.InputDigest || source.FactsDigest != f.FactsDigest {
				return f, false, ErrTextSolveConflict
			}
			if f.AnswerSource.Kind == k12.ProblemAnswerDeterministic && (source.ExecutionKind != k12.GradingExecutionLocalDeterministic || source.Operation != k12.GradingItemOperationSolve) {
				return f, false, ErrTextSolveConflict
			}
			if f.AnswerSource.Kind == k12.ProblemAnswerModel && source.Operation != k12.GradingItemOperationSolveVerify {
				return f, false, ErrTextSolveConflict
			}
		}
	}
	if f.Publication != nil {
		if err = validateTextSolvePublication(ctx, tx, f, *f.Publication); err != nil {
			return f, false, err
		}
	}
	// 占位只在未提交事务中出现，最终记录以同一主键一次替换后保持不可变。
	if _, err = tx.ExecContext(ctx, `DELETE FROM k12_text_solve_finalizations WHERE owner_id=? AND agent_name=? AND solve_run_id=? AND data='{}'`, f.OwnerID, f.AgentName, f.RunID); err != nil {
		return f, false, err
	}
	raw, _ := json.Marshal(f)
	if _, err = tx.ExecContext(ctx, `INSERT INTO k12_text_solve_finalizations(finalization_id,owner_id,agent_name,solve_run_id,result_digest,data,created_at) VALUES(?,?,?,?,?,?,?)`, f.FinalizationID, f.OwnerID, f.AgentName, f.RunID, f.ResultDigest, string(raw), f.CreatedAt); err != nil {
		return f, false, err
	}
	if f.Publication != nil {
		payload, _ := json.Marshal(f.Publication)
		_, err = appendOutboxEvent(ctx, tx, OutboxEvent{EventID: "text-solve-asset:" + f.FinalizationID, AgentName: f.AgentName, AggregateID: f.RunID, EventType: EventProblemAssetPrepare, PayloadVersion: 1, Payload: string(payload)})
		if err != nil {
			return f, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return f, false, err
	}
	if s.notifyOutbox != nil {
		s.notifyOutbox()
	}
	return f, true, nil
}

func validateTextSolvePublication(ctx context.Context, db dbHandle, f k12.TextSolveFinalization, p k12.ProblemAssetPublication) error {
	if p.OwnerID != f.OwnerID || p.Verification.SourceKind != k12.ProblemAssetSourceTextSolve || p.Verification.AgentName != f.AgentName || p.Verification.InputDigest != f.InputDigest || p.Verification.FactsDigest != f.FactsDigest || p.PublicationID != "text-solve:"+f.FinalizationID {
		return ErrProblemAssetEvidence
	}
	identity, err := p.Facts.ExactIdentity(p.OwnerID)
	if err != nil || identity.FactsDigest != f.FactsDigest {
		return ErrProblemAssetEvidence
	}
	inv, err := loadProblemAssetInvocation(ctx, db, p.OwnerID, p.Verification, p.Verification.InvocationID)
	if err != nil {
		return err
	}
	if inv.JobID != f.RunID || inv.InputRevision != f.InputRevision || inv.InputDigest != f.InputDigest || inv.Status != k12.ModelInvocationSucceeded || inv.ResultDigest != p.Verification.ResultDigest {
		return ErrProblemAssetEvidence
	}
	var answer struct {
		Solution   string
		OutOfScope bool
		Evidence   struct{ Verdict, EvidenceType, SolverOutputDigest, VerificationInputDigest, VerificationRunID string }
	}
	if json.Unmarshal([]byte(p.AnswerResultJSON), &answer) != nil || answer.Solution != p.Answer || answer.OutOfScope || answer.Evidence.Verdict != "agree" || answer.Evidence.EvidenceType != "numeric_exec" || strings.TrimSpace(p.Answer) == "" {
		return ErrProblemAssetEvidence
	}
	if p.Verification.Kind == k12.ProblemAnswerModel {
		_, err = validateModelAssetProof(ctx, db, p.OwnerID, p.Verification, inv)
		return err
	}
	if p.Verification.Kind != k12.ProblemAnswerDeterministic || inv.ExecutionKind != k12.GradingExecutionLocalDeterministic || inv.Operation != k12.GradingItemOperationSolve || inv.ResultJSON != p.AnswerResultJSON {
		return ErrProblemAssetEvidence
	}
	if answer.Evidence.VerificationRunID != "" {
		if p.Verification.VerificationRunID != answer.Evidence.VerificationRunID || p.Verification.VerificationInputDigest != answer.Evidence.VerificationInputDigest || p.Verification.SolverOutputDigest != answer.Evidence.SolverOutputDigest {
			return ErrProblemAssetEvidence
		}
		if err := validateTextSolveProgramProof(ctx, db, f, p.Verification); err != nil {
			return err
		}
	}
	return nil
}

// ValidateTextSolvePublication 在报告采用等读取边界复用同一真实执行证明核验。
func (s *Store) ValidateTextSolvePublication(ctx context.Context, f k12.TextSolveFinalization, p k12.ProblemAssetPublication) error {
	return validateTextSolvePublication(ctx, s.db, f, p)
}
