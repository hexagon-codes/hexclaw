package k12storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 发布层核对持久调用与实际执行回执；解法选择和数学判定仍由原求解引擎负责。
func validateModelAssetProof(ctx context.Context, db dbHandle, proof k12.ProblemAssetVerification, verifier k12.GradingItemInvocation) (k12.GradingItemInvocation, error) {
	var zero k12.GradingItemInvocation
	if verifier.Operation != k12.GradingItemOperationSolveVerify || verifier.ExecutionKind != k12.GradingExecutionProvider ||
		proof.GenerationInvocationID == "" || proof.VerificationRunID == "" || proof.VerificationInputDigest == "" || proof.SolverOutputDigest == "" {
		return zero, ErrProblemAssetEvidence
	}
	generator, err := scanGradingItemInvocation(db.QueryRowContext(ctx, `SELECT `+gradingItemInvocationColumns+
		` FROM k12_grading_item_invocations WHERE agent_name=? AND item_invocation_id=?`, proof.AgentName, proof.GenerationInvocationID))
	if err != nil || generator.Operation != k12.GradingItemOperationSolveGenerate || generator.ExecutionKind != k12.GradingExecutionProvider ||
		generator.Status != k12.ModelInvocationSucceeded || generator.ResultDigest != proof.GenerationResultDigest ||
		generator.JobID != verifier.JobID || generator.ProblemID != verifier.ProblemID || generator.InputRevision != verifier.InputRevision || generator.InputDigest != verifier.InputDigest {
		return zero, ErrProblemAssetEvidence
	}
	var generated struct{ Output string }
	var verified struct {
		Execution json.RawMessage `json:"execution_receipt"`
	}
	if decodeAssetPhysicalResult(generator, &generated) != nil || generated.Output == "" || decodeAssetPhysicalResult(verifier, &verified) != nil {
		return zero, ErrProblemAssetEvidence
	}
	var execution struct {
		InputDigest     string `json:"input_digest"`
		RunID           string `json:"run_id"`
		Status          string `json:"status"`
		Error           string `json:"error"`
		ExitCode        int    `json:"exit_code"`
		Timeout         bool   `json:"timeout"`
		RuntimeMissing  bool   `json:"runtime_missing"`
		Truncated       bool   `json:"truncated"`
		StdoutTruncated bool   `json:"stdout_truncated"`
		StderrTruncated bool   `json:"stderr_truncated"`
		StdoutBytes     int64  `json:"stdout_bytes"`
		Stdout          string `json:"stdout"`
	}
	if json.Unmarshal(verified.Execution, &execution) != nil || execution.RunID != proof.VerificationRunID || execution.InputDigest != proof.VerificationInputDigest ||
		execution.Status != "success" || execution.ExitCode != 0 || execution.Error != "" || execution.Timeout || execution.RuntimeMissing ||
		execution.Truncated || execution.StdoutTruncated || execution.StderrTruncated || execution.StdoutBytes <= 0 || int64(len(execution.Stdout)) != execution.StdoutBytes {
		return zero, ErrProblemAssetEvidence
	}
	digest := sha256.Sum256([]byte(generated.Output))
	if hex.EncodeToString(digest[:]) != proof.SolverOutputDigest {
		return zero, ErrProblemAssetEvidence
	}
	return generator, nil
}

func decodeAssetPhysicalResult(inv k12.GradingItemInvocation, target any) error {
	// 物理回执摘要沿用调用账本的分段前缀规则。
	digest := sha256.Sum256(append([]byte{0}, []byte(inv.ResultJSON)...))
	if inv.ResultDigest != "sha256:"+hex.EncodeToString(digest[:]) {
		return ErrProblemAssetEvidence
	}
	var envelope struct {
		Schema  string          `json:"schema"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal([]byte(inv.ResultJSON), &envelope) != nil {
		return ErrProblemAssetEvidence
	}
	payload := []byte(inv.ResultJSON)
	if envelope.Schema != "" {
		if envelope.Schema != "k12_grading_grounded_physical_v1" || len(envelope.Payload) == 0 {
			return ErrProblemAssetEvidence
		}
		payload = envelope.Payload
	}
	return json.Unmarshal(payload, target)
}
