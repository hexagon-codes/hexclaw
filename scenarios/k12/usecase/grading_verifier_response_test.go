package usecase

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestGradingVerifierResponseDurableReceipts(t *testing.T) {
	tests := []struct {
		name         string
		callErr      error
		cause        error
		status       k12.ModelInvocationStatus
		failureClass string
		failureCode  string
		replayErr    error
	}{
		{
			name:         "processed_response_tool_timeout_is_failed",
			callErr:      errors.Join(egress.ErrProviderResponseProcessed, context.DeadlineExceeded),
			cause:        context.DeadlineExceeded,
			status:       k12.ModelInvocationFailed,
			failureClass: "local",
			failureCode:  "provider_response_processed",
			replayErr:    ErrGradingItemInvocationFailed,
		},
		{
			name:         "transport_timeout_remains_unknown",
			callErr:      context.DeadlineExceeded,
			cause:        context.DeadlineExceeded,
			status:       k12.ModelInvocationOutcomeUnknown,
			failureClass: "provider_transport",
			failureCode:  "outcome_unknown",
			replayErr:    ErrModelInvocationRequiresReconciliation,
		},
		{
			name:         "transport_disconnect_remains_unknown",
			callErr:      io.ErrUnexpectedEOF,
			cause:        io.ErrUnexpectedEOF,
			status:       k12.ModelInvocationOutcomeUnknown,
			failureClass: "provider_transport",
			failureCode:  "outcome_unknown",
			replayErr:    ErrModelInvocationRequiresReconciliation,
		},
	}
	for _, physical := range []bool{true, false} {
		path := "item"
		if physical {
			path = "physical"
		}
		t.Run(path, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{{
						Question: "q1", Subject: "数学", StudentAnswer: "1", AnswerState: AnswerStatePresent,
					}}, &itemResumeSolver{calls: map[string]int{}}, &itemResumeGrader{calls: map[string]int{}})
					jobID := runItemResumeJobToAssessing(t, o, "verifier-response-"+path+"-"+tt.name)
					run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
					question := run.questions[0]
					execute := func(operation k12.GradingItemOperation, request string,
						call func(context.Context) (string, error),
					) (string, string, error) {
						if physical {
							// 每次重建执行器，恢复必须读取 SQLite 回执而非内存缓存。
							executor := newDurableGradingPhysicalCallExecutor(o, job, question)
							ctx := withGradingPhysicalCallExecutor(context.Background(), executor)
							result, err := ExecuteGradingPhysicalCall(ctx, GradingPhysicalCallSpec{
								Operation: operation, RequestDigest: request,
							}, call)
							return result.Payload, result.InvocationID, err
						}
						return executeGradingItemOperation(context.Background(), o, job, question,
							operation, map[string]string{"request": request}, call)
					}
					successOperation, failureOperation := k12.GradingItemOperationSolve, k12.GradingItemOperationGrade
					if physical {
						successOperation, failureOperation = k12.GradingItemOperationSolveGenerate, k12.GradingItemOperationSolveVerify
					}
					const successPayload = `{"solution":"2"}`
					calls := 0
					payload, successID, err := execute(successOperation, "sha256:old-success", func(context.Context) (string, error) {
						calls++
						return successPayload, nil
					})
					if err != nil || payload != successPayload || successID == "" || calls != 1 {
						t.Fatalf("initial successful call: payload=%q invocation=%q calls=%d err=%v", payload, successID, calls, err)
					}
					before := gradingVerifierResponseRows(t, o, job)
					if len(before) != 1 || before[0].Status != k12.ModelInvocationSucceeded ||
						before[0].ResultDigest == "" || before[0].CostReceiptID == "" {
						t.Fatalf("missing successful durable receipt: %+v", before)
					}
					call := func(context.Context) (string, error) {
						calls++
						return "", tt.callErr
					}
					_, _, err = execute(failureOperation, "sha256:current-verifier", call)
					if !errors.Is(err, tt.cause) || calls != 2 {
						t.Fatalf("failed call: calls=%d err=%v, want cause %v", calls, err, tt.cause)
					}
					unknown := tt.status == k12.ModelInvocationOutcomeUnknown
					if errors.Is(err, ErrGradingPhysicalCallOutcomeUnknown) != unknown {
						t.Fatalf("unknown error classification: err=%v want_unknown=%t", err, unknown)
					}
					if !unknown && (!errors.Is(err, egress.ErrProviderResponseProcessed) ||
						errors.Is(err, ErrModelInvocationRequiresReconciliation)) {
						t.Fatalf("processed response lost its known-failure classification: %v", err)
					}
					if physical && unknown {
						var retryable interface{ SubAgentRetryable() bool }
						if !errors.As(err, &retryable) || retryable.SubAgentRetryable() {
							t.Fatalf("unknown physical call must prohibit automatic retry: %v", err)
						}
					}
					after := gradingVerifierResponseRows(t, o, job)
					if len(after) != 2 {
						t.Fatalf("durable receipts=%+v, want one old success and one current failure", after)
					}
					var failed, old k12.GradingItemInvocation
					for _, row := range after {
						if row.InvocationID == successID {
							old = row
						} else {
							failed = row
						}
					}
					if !reflect.DeepEqual(old, before[0]) {
						t.Fatalf("old successful receipt changed: before=%+v after=%+v", before[0], old)
					}
					if failed.Status != tt.status || failed.FailureClass != tt.failureClass ||
						failed.FailureCode != tt.failureCode || failed.FailureCode == "http_0" {
						t.Fatalf("durable failure=%+v, want status=%s class=%s code=%s", failed,
							tt.status, tt.failureClass, tt.failureCode)
					}
					_, _, err = execute(failureOperation, "sha256:current-verifier", call)
					if !errors.Is(err, tt.replayErr) || calls != 2 {
						t.Fatalf("recovered failure was resent: calls=%d err=%v, want %v", calls, err, tt.replayErr)
					}
					job.Fields.AttemptCount++
					payload, replayID, err := execute(successOperation, "sha256:old-success", func(context.Context) (string, error) {
						calls++
						return "must-not-send", nil
					})
					if err != nil || payload != successPayload || replayID != successID || calls != 2 {
						t.Fatalf("old success replay: payload=%q invocation=%q calls=%d err=%v", payload, replayID, calls, err)
					}
					if replayed := gradingVerifierResponseRows(t, o, job); !reflect.DeepEqual(after, replayed) {
						t.Fatalf("replay changed durable receipts: before=%+v after=%+v", after, replayed)
					}
				})
			}
		})
	}
}

func gradingVerifierResponseRows(t *testing.T, o *GradingOrchestrator, job GradingJobView) []k12.GradingItemInvocation {
	t.Helper()
	rows, err := o.deps.Records.ListGradingItemInvocations(context.Background(), job.Record.AgentName, job.Record.RecordID)
	if err != nil {
		t.Fatalf("read persisted grading item invocations: %v", err)
	}
	return rows
}
