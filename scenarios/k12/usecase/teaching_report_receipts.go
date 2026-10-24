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

func (o *GradingOrchestrator) restoreTeachingReportGuide(ctx context.Context, job GradingJobView, q RecognizedQuestion, solved SolveHomeworkResult) (ParentTeachingGuide, string, string, error) {
	reader, ok := o.deps.Solver.(teachingReportReceiptReader)
	if !ok {
		return ParentTeachingGuide{}, "", "", ErrTeachingReportContentRequired
	}
	calls, err := o.deps.Records.ListGradingItemInvocations(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil {
		return ParentTeachingGuide{}, "", "", err
	}
	var receipts []TeachingReportSolveReceipt
	for _, call := range calls {
		if call.ProblemID != q.ProblemID || call.InputRevision != q.ConfirmedVersion || call.InputDigest != q.InputDigest || call.Status != k12.ModelInvocationSucceeded ||
			(call.Operation != k12.GradingItemOperationSolveGenerate && call.Operation != k12.GradingItemOperationSolveVerify) {
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
		receipts = append(receipts, TeachingReportSolveReceipt{InvocationID: call.InvocationID, Operation: call.Operation, Payload: json.RawMessage(payload)})
	}
	return reader.RestoreTeachingReportGuide(solved.Solution, solved.Evidence.SolverOutputDigest, receipts)
}
