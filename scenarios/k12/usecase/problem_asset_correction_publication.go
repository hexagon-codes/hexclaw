package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func (c ProblemAssetConsumer) publishCorrectedAsset(ctx context.Context, event k12storage.OutboxEvent) error {
	if c.Records == nil {
		return fmt.Errorf("problem asset store is unavailable")
	}
	var payload k12storage.AssessmentCorrectedPayload
	if event.PayloadVersion != 1 || json.Unmarshal([]byte(event.Payload), &payload) != nil ||
		payload.AgentName != event.AgentName || payload.CorrectionID == "" || event.AggregateID != payload.JobID+":"+payload.ProblemID {
		return fmt.Errorf("invalid corrected asset publication event")
	}
	source, err := c.Records.GetProblemAssetCorrectionPublicationSource(ctx, event.AgentName, payload.CorrectionID)
	if errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		return nil
	}
	if err != nil || source.AlreadyPublished {
		return err
	}
	assessment := source.Correction.Assessment
	if assessment.JobID != payload.JobID || assessment.ProblemID != payload.ProblemID || assessment.InputRevision != payload.InputRevision || source.Correction.Revision != payload.Revision {
		return fmt.Errorf("corrected asset publication identity mismatch")
	}
	publication, err := c.correctedAssetPublication(ctx, source)
	if err != nil || publication == nil {
		return err
	}
	_, _, err = c.Records.PublishCorrectedProblemAsset(ctx, *publication, assessment.JobID, source.Correction.CorrectionID)
	if errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		// 新纠正或新版资产已提交时，迟到事件不能替代当前答案。
		return nil
	}
	return err
}

// correctedAssetPublication 只整理纠正中已有的成功证明，不求解、不生成新的学习证据。
func (c ProblemAssetConsumer) correctedAssetPublication(ctx context.Context, source k12storage.ProblemAssetCorrectionPublicationSource) (*k12.ProblemAssetPublication, error) {
	assessment := source.Correction.Assessment
	var item PhotoGradeItem
	if json.Unmarshal([]byte(assessment.ResultJSON), &item) != nil || assessment.ResultDigest != modelInvocationDigest([]byte(assessment.ResultJSON)) {
		return nil, k12storage.ErrProblemAssetEvidence
	}
	solved := item.Solve
	if assessment.Status == k12.GradingAssessmentCorrect || assessment.Status == k12.GradingAssessmentWrong || assessment.Status == k12.GradingAssessmentProcessIssue {
		solved = SolveHomeworkResult{Solution: item.Grade.Solution, Evidence: item.Grade.Evidence, OutOfScope: item.Grade.OutOfScope,
			OutOfScopeKP: item.Grade.OutOfScopeKP, CurriculumUnmapped: item.Grade.CurriculumUnmapped}
	}
	if solved.OutOfScope || solved.Evidence.Verdict != VerdictAgree || solved.Evidence.EvidenceType != EvidenceNumericExec {
		return nil, nil
	}
	inv, err := c.Records.GetGradingItemInvocation(ctx, assessment.AgentName, assessment.SolveInvocationID)
	if err != nil {
		return nil, err
	}
	if inv.Status != k12.ModelInvocationSucceeded || inv.JobID != assessment.JobID || inv.ProblemID != assessment.ProblemID ||
		inv.InputRevision != assessment.InputRevision || inv.InputDigest != assessment.InputDigest {
		return nil, nil
	}
	version := source.OriginalVersion
	proof := k12.ProblemAssetVerification{AgentName: inv.AgentName, InvocationID: inv.InvocationID, InputDigest: inv.InputDigest,
		ResultDigest: inv.ResultDigest, FactsDigest: version.FactsDigest, Kind: k12.ProblemAnswerDeterministic, Policy: "local-deterministic-v1"}
	answer, answerJSON := solved.Solution, inv.ResultJSON
	if inv.ExecutionKind != k12.GradingExecutionLocalDeterministic || inv.Operation != k12.GradingItemOperationSolve {
		if inv.Operation != k12.GradingItemOperationSolveVerify || solved.Evidence.SolverOutputDigest == "" ||
			solved.Evidence.VerificationInputDigest == "" || solved.Evidence.VerificationRunID == "" {
			return nil, nil
		}
		calls, err := c.Records.ListGradingItemInvocations(ctx, inv.AgentName, inv.JobID)
		if err != nil {
			return nil, err
		}
		for _, call := range calls {
			if call.Operation != k12.GradingItemOperationSolveGenerate || call.Status != k12.ModelInvocationSucceeded ||
				call.ProblemID != inv.ProblemID || call.InputRevision != inv.InputRevision || call.InputDigest != inv.InputDigest {
				continue
			}
			payload, _, _, err := decodeGroundedPhysicalPayload(call.ResultJSON, nil)
			var generated struct{ Output string }
			if err != nil || json.Unmarshal([]byte(payload), &generated) != nil {
				continue
			}
			sum := sha256.Sum256([]byte(generated.Output))
			if hex.EncodeToString(sum[:]) == solved.Evidence.SolverOutputDigest {
				proof.GenerationInvocationID, proof.GenerationResultDigest = call.InvocationID, call.ResultDigest
				answer = generated.Output
				break
			}
		}
		if proof.GenerationInvocationID == "" {
			return nil, nil
		}
		proof.Kind, proof.Policy = k12.ProblemAnswerModel, "model-with-execution-v1"
		proof.SolverOutputDigest, proof.VerificationInputDigest, proof.VerificationRunID = solved.Evidence.SolverOutputDigest, solved.Evidence.VerificationInputDigest, solved.Evidence.VerificationRunID
		solved.Solution = answer
		raw, err := json.Marshal(solved)
		if err != nil {
			return nil, err
		}
		answerJSON = string(raw)
	}
	return &k12.ProblemAssetPublication{OwnerID: version.OwnerID, PublicationID: source.PublicationID, Facts: version.Facts,
		Answer: answer, AnswerResultJSON: answerJSON, Verification: proof, ReplacesVersion: version.Version, ExpectedRevision: source.ExpectedRevision}, nil
}
