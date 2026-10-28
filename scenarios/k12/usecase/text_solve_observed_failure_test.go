package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestTextSolveObservedFailureRetainsPayloadWithoutResending(t *testing.T) {
	d, _, _ := reportResilienceDeps(t)
	run, err := d.Records.GetTextSolveRun(t.Context(), DefaultLocalOwnerScope, "mingming", "seed")
	if err != nil {
		t.Fatal(err)
	}
	executor := textSolvePhysicalExecutor{records: d.Records, run: run}
	spec := GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveGenerate, ExecutionKind: k12.GradingExecutionProvider, RequestDigest: "observed-response-with-local-failure"}
	payload := `{"Output":"Observed but unverified answer","execution_receipt":{"input_digest":"input","run_id":"actual-execution","status":"failed","exit_code":1}}`
	calls := 0
	result, err := executor.ExecuteGradingPhysicalCall(t.Context(), spec, func(_ context.Context) (string, error) {
		calls++
		return payload, errors.Join(egress.ErrProviderResponseProcessed, errors.New("local validation failed"))
	})
	if err == nil || result.Payload != payload {
		t.Fatalf("observed failure lost: result=%+v err=%v", result, err)
	}
	stored, err := d.Records.GetTextSolveInvocation(t.Context(), run.OwnerID, run.AgentName, textSolveInvocationID(run, spec))
	if err != nil || stored.Status != k12.ModelInvocationFailed || stored.ResultJSON != payload {
		t.Fatalf("failure not persisted: %+v err=%v", stored, err)
	}
	replayed, replayErr := executor.ExecuteGradingPhysicalCall(t.Context(), spec, func(_ context.Context) (string, error) { calls++; return "{}", nil })
	if replayErr == nil || replayed.Payload != payload || calls != 1 {
		t.Fatalf("failed response replay resent or lost evidence: calls=%d result=%+v err=%v", calls, replayed, replayErr)
	}
}
