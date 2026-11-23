package usecase

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// TeachingReportSolveReceipt 只携带已核对归属和摘要的原调用结果，不具备执行能力。
type TeachingReportSolveReceipt struct {
	InvocationID string
	Operation    k12.GradingItemOperation
	Payload      json.RawMessage
}

type teachingReportReceiptReader interface {
	RestoreTeachingReportGuide(string, string, []TeachingReportSolveReceipt) (ParentTeachingGuide, string, string, error)
}

func (o *GradingOrchestrator) restoreTeachingReportGuide(ctx context.Context, job GradingJobView, q RecognizedQuestion, assessment k12.GradingAssessmentItem, solved SolveHomeworkResult) (ParentTeachingGuide, string, string, error) {
	calls, err := o.deps.Records.ListGradingItemInvocations(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil {
		return ParentTeachingGuide{}, "", "", err
	}
	evidence := solved.Evidence
	localTeaching := assessment.Status == k12.GradingAssessmentCorrect && !solved.OutOfScope &&
		evidence.Verdict == VerdictAgree && evidence.EvidenceType == EvidenceNumericExec &&
		evidence.SolverOutputDigest != "" && evidence.VerificationInputDigest != "" && evidence.VerificationRunID != ""
	var receipts []TeachingReportSolveReceipt
	var localSolutionMatches bool
	var localVerificationID string
	for _, call := range calls {
		localSolve := localTeaching && call.Operation == k12.GradingItemOperationSolve &&
			call.ExecutionKind == k12.GradingExecutionLocalDeterministic && call.InvocationID == assessment.SolveInvocationID
		if call.ProblemID != q.ProblemID || call.InputRevision != q.ConfirmedVersion || call.InputDigest != q.InputDigest || call.Status != k12.ModelInvocationSucceeded ||
			(!localSolve && call.Operation != k12.GradingItemOperationSolveGenerate && call.Operation != k12.GradingItemOperationSolveVerify) {
			continue
		}
		if err := validateGradingItemInvocationIdentity(call, job, q, call.RequestDigest, call.ExecutionKind); err != nil {
			return ParentTeachingGuide{}, "", "", err
		}
		if call.ResultDigest != modelInvocationDigest([]byte(call.ResultJSON)) {
			return ParentTeachingGuide{}, "", "", fmt.Errorf("report solve receipt digest differs")
		}
		payload, _, _, err := decodeGroundedPhysicalPayload(call.ResultJSON, nil)
		if err != nil {
			return ParentTeachingGuide{}, "", "", err
		}
		if localSolve {
			var original SolveHomeworkResult
			if err := json.Unmarshal([]byte(payload), &original); err != nil {
				return ParentTeachingGuide{}, "", "", err
			}
			localSolutionMatches = !original.OutOfScope && original.Solution == solved.Solution && original.Evidence == evidence
			continue
		}
		if localTeaching && call.Operation == k12.GradingItemOperationSolveVerify &&
			call.ExecutionKind == k12.GradingExecutionLocalDeterministic && numericExecutionReceiptMatches(call, evidence) {
			localVerificationID = call.InvocationID
		}
		receipts = append(receipts, TeachingReportSolveReceipt{InvocationID: call.InvocationID, Operation: call.Operation, Payload: json.RawMessage(payload)})
	}
	// 正确题不额外生成家长讲法，沿同题原解和物理验算回执恢复完整步骤。
	if localSolutionMatches && localVerificationID != "" {
		if content, ok := verifiedSolutionReportContent(solved.Solution); ok {
			return ParentTeachingGuide{ReportContent: content}, evidence.SolverOutputDigest, localVerificationID, nil
		}
	}
	reader, ok := o.deps.Solver.(teachingReportReceiptReader)
	if !ok {
		return ParentTeachingGuide{}, "", "", ErrTeachingReportContentRequired
	}
	return reader.RestoreTeachingReportGuide(solved.Solution, solved.Evidence.SolverOutputDigest, receipts)
}
