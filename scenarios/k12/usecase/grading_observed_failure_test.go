package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestGradingObservedFailureRetainsPayloadWithoutResending(t *testing.T) {
	o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{{Question: "q1", Subject: "数学", StudentAnswer: "1", AnswerState: AnswerStatePresent}}, &itemResumeSolver{calls: map[string]int{}}, &itemResumeGrader{calls: map[string]int{}})
	jobID := runItemResumeJobToAssessing(t, o, "observed-failed-response")
	run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
	spec := GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveVerify, RequestDigest: "sha256:observed-failed-response"}
	payload := `{"Output":"Observed but unverified answer","execution_receipt":{"input_digest":"input","run_id":"actual-execution","status":"failed","exit_code":1}}`
	calls := 0
	executor := newDurableGradingPhysicalCallExecutor(o, job, run.questions[0])
	result, err := executor.ExecuteGradingPhysicalCall(t.Context(), spec, func(context.Context) (string, error) {
		calls++
		return payload, errors.Join(egress.ErrProviderResponseProcessed, errors.New("local validation failed"))
	})
	if err == nil || result.Payload != payload || result.InvocationID == "" {
		t.Fatalf("observed failure lost: result=%+v err=%v", result, err)
	}
	stored, err := o.deps.Records.GetGradingItemInvocation(t.Context(), job.Record.AgentName, result.InvocationID)
	if err != nil || stored.Status != k12.ModelInvocationFailed || stored.ResultJSON != payload || stored.ResultDigest == "" || stored.CostReceiptID != "" {
		t.Fatalf("observed response promoted or discarded: %+v err=%v", stored, err)
	}
	executor = newDurableGradingPhysicalCallExecutor(o, job, run.questions[0])
	replayed, replayErr := executor.ExecuteGradingPhysicalCall(t.Context(), spec, func(context.Context) (string, error) {
		calls++
		return "{}", nil
	})
	if !errors.Is(replayErr, ErrGradingItemInvocationFailed) || replayed.Payload != payload || calls != 1 {
		t.Fatalf("failed response replay resent or lost evidence: calls=%d result=%+v err=%v", calls, replayed, replayErr)
	}
}
