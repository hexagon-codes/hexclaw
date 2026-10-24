package k12storage

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// loadProblemAssetInvocation 只适配验证所需值；文字来源不创建或读取批改任务。
func loadProblemAssetInvocation(ctx context.Context, db dbHandle, owner string, proof k12.ProblemAssetVerification, id string) (k12.GradingItemInvocation, error) {
	if proof.SourceKind == "" || proof.SourceKind == k12.ProblemAssetSourceGrading {
		return scanGradingItemInvocation(db.QueryRowContext(ctx, `SELECT `+gradingItemInvocationColumns+` FROM k12_grading_item_invocations WHERE agent_name=? AND item_invocation_id=?`, proof.AgentName, id))
	}
	if proof.SourceKind != k12.ProblemAssetSourceTextSolve {
		return k12.GradingItemInvocation{}, ErrProblemAssetEvidence
	}
	v, err := scanTextSolveInvocation(db.QueryRowContext(ctx, `SELECT data,operation_attempt FROM k12_text_solve_invocations WHERE owner_id=? AND agent_name=? AND invocation_id=?`, owner, proof.AgentName, id))
	if err != nil {
		return k12.GradingItemInvocation{}, err
	}
	run, err := getTextSolveRun(ctx, db, owner, proof.AgentName, v.RunID)
	if err != nil {
		return k12.GradingItemInvocation{}, err
	}
	if v.OwnerID != owner || v.InputDigest != run.InputDigest || v.FactsDigest != run.FactsDigest || v.InputRevision != run.InputRevision || !reflect.DeepEqual(v.RouteSnapshot, run.RouteSnapshot) {
		return k12.GradingItemInvocation{}, ErrProblemAssetEvidence
	}
	return k12.GradingItemInvocation{InvocationID: v.InvocationID, AgentName: v.AgentName, JobID: v.RunID, ProblemID: v.FactsDigest, AttemptID: v.RunID, Operation: v.Operation, ExecutionKind: v.ExecutionKind, OperationAttempt: v.OperationAttempt, RequestDigest: v.RequestDigest, InputRevision: v.InputRevision, InputDigest: v.InputDigest, RouteSnapshot: v.RouteSnapshot, Status: v.Status, ResultDigest: v.ResultDigest, ResultJSON: v.ResultJSON, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}, nil
}

func validateTextSolveProgramProof(ctx context.Context, db dbHandle, f k12.TextSolveFinalization, proof k12.ProblemAssetVerification) error {
	rows, err := db.QueryContext(ctx, `SELECT data,operation_attempt FROM k12_text_solve_invocations WHERE owner_id=? AND agent_name=? AND solve_run_id=? AND execution_kind=? AND status=?`, f.OwnerID, f.AgentName, f.RunID, k12.GradingExecutionLocalDeterministic, k12.ModelInvocationSucceeded)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		inv, err := scanTextSolveInvocation(rows)
		if err != nil {
			return err
		}
		if inv.InputDigest != f.InputDigest || inv.FactsDigest != f.FactsDigest || inv.ResultDigest != textSolveDigest(inv.ResultJSON) {
			continue
		}
		var payload struct {
			Receipt json.RawMessage `json:"execution_receipt"`
		}
		if json.Unmarshal([]byte(inv.ResultJSON), &payload) == nil && validProblemAssetExecutionReceipt(payload.Receipt, proof) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return ErrProblemAssetEvidence
}
