package usecase

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/toolkit/util/logger"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// gradingProblemAssetFacts 只为当前已可靠冻结的独立题建立精确身份。
// 复合题和图表依赖尚未形成完整事实快照时继续原求解链，不以一个片段发布资产。
func gradingProblemAssetFacts(q RecognizedQuestion, req GradeRequest) (k12.ProblemAssetFacts, bool) {
	if q.ParentProblemID != "" || q.ProblemKind == ProblemKindCompoundParent || q.parentSourceUnclear ||
		q.ConfirmationRequired || q.ConfirmedVersion < 1 || q.InputDigest == "" || req.Subject != "数学" ||
		strings.TrimSpace(req.Problem) == "" {
		return k12.ProblemAssetFacts{}, false
	}
	for _, marker := range []string{"如图", "下图", "上图", "图中", "图示", "表中", "下表", "上表", "![", "<img", "根据材料"} {
		if strings.Contains(req.Problem, marker) {
			return k12.ProblemAssetFacts{}, false
		}
	}
	return k12.ProblemAssetFacts{Subject: req.Subject, Stem: req.Problem,
		AnswerContext: map[string]string{"grade_term": req.Grade}}, true
}

func executeDurableSolveOperation(ctx context.Context, o *GradingOrchestrator, deps Deps,
	job GradingJobView, q RecognizedQuestion, req GradeRequest,
) (SolveHomeworkResult, string, error) {
	facts, eligible := gradingProblemAssetFacts(q, req)
	owner, ownerErr := resolveGradingGroundingTextbookOwner(ctx, deps, job)
	if !eligible || deps.Records == nil || ownerErr != nil || owner == "" {
		return executeUncachedDurableSolveOperation(ctx, o, deps, job, q, req)
	}
	identity, err := facts.ExactIdentity(owner)
	if err != nil {
		return SolveHomeworkResult{}, "", err
	}
	adoption, priorErr := deps.Records.FindProblemAssetAdoption(ctx, owner, job.Record.RecordID, q.ProblemID, q.ConfirmedVersion)
	if priorErr == nil {
		if adoption.InputDigest != q.InputDigest || adoption.FactsDigest != identity.FactsDigest {
			return SolveHomeworkResult{}, "", k12storage.ErrProblemAssetConflict
		}
		if err := deps.Records.ValidateProblemAssetAdoption(ctx, owner, adoption.AdoptionID); err != nil {
			if !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
				return SolveHomeworkResult{}, "", err
			}
			// 采用尚未提交为评估时，失效答案退出当前求解；保留原采用作审计，
			// 沿现有调用账本取得新答案。已发送未定请求不能借答案变更绕过核实。
			calls, err := deps.Records.ListGradingItemInvocations(ctx, job.Record.AgentName, job.Record.RecordID)
			if err != nil {
				return SolveHomeworkResult{}, "", err
			}
			for _, call := range calls {
				if call.ProblemID != q.ProblemID || (call.InputRevision != q.ConfirmedVersion && call.InputRevision != 0) {
					continue
				}
				if call.Status == k12.ModelInvocationOutcomeUnknown || call.Status == k12.ModelInvocationReconciled ||
					(call.Status == k12.ModelInvocationSent && call.ExecutionKind != k12.GradingExecutionLocalDeterministic) {
					return SolveHomeworkResult{}, "", gradingPhysicalNoRetryError{cause: fmt.Errorf(
						"%w: invocation=%s status=%s", ErrModelInvocationRequiresReconciliation, call.InvocationID, call.Status)}
				}
			}
			return executeUncachedDurableSolveOperation(ctx, o, deps, job, q, req)
		}
		v, err := deps.Records.GetProblemAssetVersion(ctx, owner, adoption.AssetID, adoption.AssetVersion)
		if err != nil {
			return SolveHomeworkResult{}, "", err
		}
		return adoptedSolveResult(v, adoption)
	}
	if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
		return SolveHomeworkResult{}, "", priorErr
	}
	// 任一本次求解调用已建立时，沿用原账本恢复；未知请求不能被后来的资产命中掩盖。
	invocations, err := deps.Records.ListGradingItemInvocations(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil {
		return SolveHomeworkResult{}, "", err
	}
	started := false
	for _, inv := range invocations {
		if inv.ProblemID == q.ProblemID && (inv.InputRevision == q.ConfirmedVersion || inv.InputRevision == 0) &&
			(inv.Operation == k12.GradingItemOperationSolve || inv.Operation == k12.GradingItemOperationSolveGenerate || inv.Operation == k12.GradingItemOperationSolveVerify) {
			started = true
		}
	}
	if !started {
		v, lookupErr := deps.Records.FindExactProblemAsset(ctx, owner, facts)
		if lookupErr == nil {
			a, _, adoptErr := deps.Records.AdoptProblemAsset(ctx, k12.ProblemAssetAdoption{
				OwnerID: owner, JobID: job.Record.RecordID, ProblemID: q.ProblemID, InputRevision: q.ConfirmedVersion,
				InputDigest: q.InputDigest, AssetID: v.AssetID, AssetVersion: v.Version, AssetRevision: v.Revision, FactsDigest: identity.FactsDigest,
			})
			if adoptErr == nil {
				return adoptedSolveResult(v, a)
			}
			if !errors.Is(adoptErr, k12storage.ErrProblemAssetUnavailable) {
				return SolveHomeworkResult{}, "", adoptErr
			}
		} else if !errors.Is(lookupErr, k12storage.ErrProblemAssetUnavailable) {
			// 查询故障不阻断原求解；没有采用回执就不宣称命中。
			logger.Warn("Problem asset lookup failed", "error", lookupErr)
		}
	}
	solved, invocationID, err := executeUncachedDurableSolveOperation(ctx, o, deps, job, q, req)
	if err != nil || solved.OutOfScope || solved.Evidence.Verdict != VerdictAgree || solved.Evidence.EvidenceType != EvidenceNumericExec || invocationID == "" {
		return solved, invocationID, err
	}
	inv, err := deps.Records.GetGradingItemInvocation(context.WithoutCancel(ctx), job.Record.AgentName, invocationID)
	if err != nil {
		return solved, invocationID, err
	}
	proof := k12.ProblemAssetVerification{AgentName: job.Record.AgentName, InvocationID: invocationID, InputDigest: q.InputDigest,
		ResultDigest: inv.ResultDigest, FactsDigest: identity.FactsDigest, Kind: k12.ProblemAnswerDeterministic, Policy: "local-deterministic-v1"}
	answerJSON := inv.ResultJSON
	assetAnswer := solved.Solution
	if inv.ExecutionKind != k12.GradingExecutionLocalDeterministic || inv.Operation != k12.GradingItemOperationSolve {
		if inv.Operation != k12.GradingItemOperationSolveVerify || solved.Evidence.SolverOutputDigest == "" ||
			solved.Evidence.VerificationInputDigest == "" || solved.Evidence.VerificationRunID == "" {
			return solved, invocationID, nil
		}
		calls, err := deps.Records.ListGradingItemInvocations(context.WithoutCancel(ctx), job.Record.AgentName, job.Record.RecordID)
		if err != nil {
			return solved, invocationID, err
		}
		for _, call := range calls {
			if call.Operation != k12.GradingItemOperationSolveGenerate || call.Status != k12.ModelInvocationSucceeded ||
				call.ProblemID != q.ProblemID || call.InputRevision != q.ConfirmedVersion || call.InputDigest != q.InputDigest {
				continue
			}
			payload, _, _, err := decodeGroundedPhysicalPayload(call.ResultJSON, nil)
			var generated struct{ Output string }
			if err != nil || json.Unmarshal([]byte(payload), &generated) != nil {
				continue
			}
			digest := sha256.Sum256([]byte(generated.Output))
			if hex.EncodeToString(digest[:]) == solved.Evidence.SolverOutputDigest {
				proof.GenerationInvocationID, proof.GenerationResultDigest = call.InvocationID, call.ResultDigest
				assetAnswer = generated.Output
				break
			}
		}
		if proof.GenerationInvocationID == "" {
			return solved, invocationID, nil
		}
		proof.Kind, proof.Policy = k12.ProblemAnswerModel, "model-with-execution-v1"
		proof.SolverOutputDigest, proof.VerificationInputDigest, proof.VerificationRunID = solved.Evidence.SolverOutputDigest, solved.Evidence.VerificationInputDigest, solved.Evidence.VerificationRunID
		// 资产只保存实际核验的解法，不携带其他候选答案及本次采样说明。
		assetResult := solved
		assetResult.Solution = assetAnswer
		raw, err := json.Marshal(assetResult)
		if err != nil {
			return solved, invocationID, err
		}
		answerJSON = string(raw)
	}
	solved.assetPublication = &k12.ProblemAssetPublication{
		OwnerID: owner, PublicationID: "solve:" + invocationID, Facts: facts, Answer: assetAnswer, AnswerResultJSON: answerJSON, Verification: proof,
	}
	return solved, invocationID, nil
}

func adoptedSolveResult(v k12.ProblemAssetVersion, a k12.ProblemAssetAdoption) (SolveHomeworkResult, string, error) {
	var solved SolveHomeworkResult
	if err := json.Unmarshal([]byte(v.AnswerResultJSON), &solved); err != nil {
		return solved, "", err
	}
	if solved.Solution != v.Answer || solved.OutOfScope {
		return SolveHomeworkResult{}, "", k12storage.ErrProblemAssetEvidence
	}
	solved.AnswerSource = &k12.ProblemAnswerSource{Kind: k12.ProblemAnswerAsset, FactsDigest: a.FactsDigest,
		AssetID: a.AssetID, AssetVersion: a.AssetVersion, AssetRevision: a.AssetRevision, AdoptionID: a.AdoptionID}
	return solved, "", nil
}
