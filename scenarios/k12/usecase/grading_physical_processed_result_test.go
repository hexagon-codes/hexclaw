package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func processedGradingCandidateFixture(t *testing.T) (*GradingOrchestrator, GradingJobView, RecognizedQuestion, context.Context, GradingPhysicalCallSpec, k12.GradingItemInvocation) {
	t.Helper()
	o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{{Question: "q1", Subject: "数学", StudentAnswer: "1", AnswerState: AnswerStatePresent}}, &itemResumeSolver{calls: map[string]int{}}, &itemResumeGrader{calls: map[string]int{}})
	jobID := runItemResumeJobToAssessing(t, o, "processed-generation")
	run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
	q := run.questions[0]
	ctx := context.WithValue(t.Context(), finalSourceReferenceContextKey{}, finalSourceReferenceScope{digest: "same-source-reference"})
	spec := GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveGenerate, RequestDigest: "sha256:original-generation-request"}
	executor := newDurableGradingPhysicalCallExecutor(o, job, q)
	result, err := executor.ExecuteGradingPhysicalCall(ctx, spec, func(context.Context) (string, error) {
		return `{"Output":"retained original response"}`, egress.ErrProviderResponseProcessed
	})
	if !errors.Is(err, egress.ErrProviderResponseProcessed) {
		t.Fatalf("processed fixture did not retain a known failure: %v", err)
	}
	row, err := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, result.InvocationID)
	if err != nil || row.Status != k12.ModelInvocationFailed || row.FailureClass != "local" || row.FailureCode != "provider_response_processed" {
		t.Fatalf("processed receipt=%+v err=%v", row, err)
	}
	return o, job, q, ctx, spec, row
}

func TestProcessedGradingCandidateReparseIsReadOnlyAndNotASuccessSource(t *testing.T) {
	o, job, q, ctx, spec, original := processedGradingCandidateFixture(t)
	parses, sends := 0, 0
	spec.ReparseProcessedResult = func(payload string) (string, error) {
		parses++
		if payload != original.ResultJSON {
			t.Fatal("reparser did not receive the exact original payload")
		}
		return `{"Output":"locally parsed candidate"}`, nil
	}
	send := func(context.Context) (string, error) { sends++; return `{"Output":"new provider result"}`, nil }
	executor := newDurableGradingPhysicalCallExecutor(o, job, q)
	spec.KnownExecutionFailure = func(string) error { return errors.New("trusted execution failed") }
	if _, err := executor.ExecuteGradingPhysicalCall(ctx, spec, send); !errors.Is(err, ErrGradingItemInvocationFailed) || parses != 0 || sends != 0 {
		t.Fatalf("trusted execution failure was bypassed: err=%v parses=%d sends=%d", err, parses, sends)
	}
	spec.KnownExecutionFailure = nil
	result, err := executor.ExecuteGradingPhysicalCall(ctx, spec, send)
	if err != nil || result.InvocationID != original.InvocationID || result.Payload != `{"Output":"locally parsed candidate"}` || sends != 0 || parses != 1 {
		t.Fatalf("processed candidate was resent: result=%+v err=%v sends=%d parses=%d", result, err, sends, parses)
	}
	if executor.lastInvocation(k12.GradingItemOperationSolveGenerate) != "" {
		t.Fatal("the original failed candidate became a succeeded assessment source")
	}
	changed := q
	changed.ConfirmedVersion++
	changed.InputDigest = "another-input"
	if _, err := newDurableGradingPhysicalCallExecutor(o, job, changed).ExecuteGradingPhysicalCall(ctx, spec, send); !errors.Is(err, ErrModelInvocationRequiresReconciliation) || parses != 1 || sends != 0 {
		t.Fatalf("failed candidate crossed input identity: err=%v parses=%d sends=%d", err, parses, sends)
	}
	rows, err := o.deps.Records.ListGradingItemInvocations(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil || len(rows) != 1 || rows[0].Status != k12.ModelInvocationFailed || rows[0].ResultJSON != original.ResultJSON || rows[0].ResultDigest != original.ResultDigest {
		t.Fatalf("reparse rewrote or replaced the original receipt: %+v err=%v", rows, err)
	}
}

func TestProcessedGradingCandidateDoesNotBypassNewerUnknown(t *testing.T) {
	o, job, q, ctx, spec, old := processedGradingCandidateFixture(t)
	newer := old
	newer.InvocationID += "-newer"
	newer.OperationAttempt++
	newer, _, err := o.deps.Records.PrepareGradingItemInvocation(ctx, newer)
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := o.deps.Records.ClaimGradingItemInvocationSent(ctx, job.Record.AgentName, newer.InvocationID); err != nil || !claimed {
		t.Fatalf("claim=%t err=%v", claimed, err)
	}
	if _, err := o.deps.Records.MarkGradingItemInvocationOutcomeUnknown(ctx, job.Record.AgentName, newer.InvocationID, "provider_transport", "outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	parses, sends := 0, 0
	spec.ReparseProcessedResult = func(string) (string, error) { parses++; return `{}`, nil }
	_, err = newDurableGradingPhysicalCallExecutor(o, job, q).ExecuteGradingPhysicalCall(ctx, spec, func(context.Context) (string, error) { sends++; return `{}`, nil })
	if !errors.Is(err, ErrModelInvocationRequiresReconciliation) || parses != 0 || sends != 0 {
		t.Fatalf("older failed payload bypassed an unknown request: err=%v parses=%d sends=%d", err, parses, sends)
	}
	rows, err := o.deps.Records.ListGradingItemInvocations(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil || len(rows) != 2 || rows[0].Status != k12.ModelInvocationFailed || rows[0].ResultJSON != old.ResultJSON || rows[1].Status != k12.ModelInvocationOutcomeUnknown {
		t.Fatalf("unknown priority changed the immutable ledger: %+v err=%v", rows, err)
	}
}

func TestUnparsableProcessedCandidateKeepsExplicitFailedRetrySemantics(t *testing.T) {
	o, job, q, ctx, spec, old := processedGradingCandidateFixture(t)
	sends := 0
	spec.ReparseProcessedResult = func(string) (string, error) { return "", errors.New("still invalid") }
	send := func(context.Context) (string, error) { sends++; return `{"Output":"explicit new attempt"}`, nil }
	if _, err := newDurableGradingPhysicalCallExecutor(o, job, q).ExecuteGradingPhysicalCall(ctx, spec, send); !errors.Is(err, ErrGradingItemInvocationFailed) || sends != 0 {
		t.Fatalf("same-generation failed candidate was retried: err=%v sends=%d", err, sends)
	}
	job.Fields.AttemptCount++
	fresh, err := newDurableGradingPhysicalCallExecutor(o, job, q).ExecuteGradingPhysicalCall(ctx, spec, send)
	if err != nil || sends != 1 || fresh.InvocationID == old.InvocationID {
		t.Fatalf("optional parser blocked the existing explicit retry: err=%v sends=%d", err, sends)
	}
	stored, err := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, old.InvocationID)
	if err != nil || stored.Status != k12.ModelInvocationFailed || stored.ResultJSON != old.ResultJSON || stored.ResultDigest != old.ResultDigest {
		t.Fatalf("explicit retry rewrote its predecessor: err=%v", err)
	}
}
