package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 使用原装配的真实 SQLite，验证本地程序不会创建 Provider 物理调用和计费回执。
func TestGradingProgramReceiptSQLiteReplayAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success-cache", true: "known-failure"}[fail], func(t *testing.T) {
			o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{{Question: "6*7", Subject: "数学", StudentAnswer: "42", AnswerState: AnswerStatePresent}}, &itemResumeSolver{calls: map[string]int{}}, &itemResumeGrader{calls: map[string]int{}})
			jobID := runItemResumeJobToAssessing(t, o, "program-receipt-fixture")
			run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
			executor := newDurableGradingPhysicalCallExecutor(o, job, run.questions[0])
			ctx := withGradingPhysicalCallExecutor(t.Context(), executor)
			calls := 0
			next := func(context.Context) (string, error) {
				calls++
				if fail {
					return "", errors.New("sandbox execution failed")
				}
				return `{"output":"COMPUTED: 42","execution_receipt":{"run_id":"actual-test-run","status":"success"}}`, nil
			}
			spec := GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveVerify, RequestDigest: strings.Repeat("a", 64)}
			first, firstErr := ExecuteGradingProgramCall(ctx, spec, next)
			second, secondErr := ExecuteGradingProgramCall(ctx, spec, next)
			if fail {
				if firstErr == nil || secondErr == nil {
					t.Fatal("failed program must not become successful cache")
				}
			} else if firstErr != nil || secondErr != nil || first.Payload != second.Payload || first.InvocationID != second.InvocationID {
				t.Fatalf("replay first=%+v second=%+v errors=%v/%v", first, second, firstErr, secondErr)
			}
			if calls != 1 {
				t.Fatalf("executions=%d, want one", calls)
			}
			rows, err := o.deps.Records.ListGradingItemInvocations(t.Context(), "mingming", jobID)
			if err != nil || len(rows) != 1 || rows[0].ExecutionKind != k12.GradingExecutionLocalDeterministic || rows[0].CostReceiptID != "" {
				t.Fatalf("local ledger=%+v err=%v", rows, err)
			}
			status := k12.ModelInvocationSucceeded
			if fail {
				status = k12.ModelInvocationFailed
			}
			if rows[0].Status != status {
				t.Fatalf("status=%s want %s", rows[0].Status, status)
			}
		})
	}
}

func TestGradingProgramUnknownWeeklyReceiptNeverRuns(t *testing.T) {
	for _, status := range []string{"sent", "outcome_unknown"} {
		t.Run(status, func(t *testing.T) {
			spec := GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveVerify, RequestDigest: strings.Repeat("b", 64)}
			executor := &weeklyPhysicalExecutor{call: &weeklyCandidateCall{PhysicalCalls: []weeklyPhysicalReceipt{{Operation: spec.Operation, RequestDigest: spec.RequestDigest, ExecutionKind: k12.GradingExecutionLocalDeterministic, Status: status}}}, save: func() error { return nil }}
			calls := 0
			_, err := ExecuteGradingProgramCall(withGradingPhysicalCallExecutor(t.Context(), executor), spec, func(context.Context) (string, error) { calls++; return `{}`, nil })
			if err == nil || calls != 0 || executor.call.PhysicalCalls[0].Status != status {
				t.Fatalf("err=%v calls=%d status=%s", err, calls, executor.call.PhysicalCalls[0].Status)
			}
		})
	}
}

func TestGradingProgramContractDoesNotUpgradeFrozenLegacyRoute(t *testing.T) {
	old := k12.ImageTaskRouteSnapshot{Provider: "fixture", Model: "fixture", Route: "fixture/fixture"}
	snapshot := gradingSnapshotFromImageRoute(old)
	if snapshot.MathExecutionContract != "legacy" || old.MathExecutionContract != "" {
		t.Fatal("old image route must preserve legacy computation")
	}
	fresh := old
	fresh.MathExecutionContract = "program-first-v1"
	if gradingSnapshotFromImageRoute(fresh).MathExecutionContract != "program-first-v1" {
		t.Fatal("new image route lost its contract")
	}
	if k12.NormalizeGradingModelSnapshot(k12.GradingModelSnapshot{}).MathExecutionContract != "" {
		t.Fatal("read normalization must not assign current contract to old data")
	}
}

func TestGradingProgramSQLiteUncommittedCompletionCannotRunAgain(t *testing.T) {
	o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{{Question: "6*7", Subject: "数学", StudentAnswer: "42", AnswerState: AnswerStatePresent}}, &itemResumeSolver{calls: map[string]int{}}, &itemResumeGrader{calls: map[string]int{}})
	jobID := runItemResumeJobToAssessing(t, o, "program-unknown-fixture")
	run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
	ctx := withGradingPhysicalCallExecutor(t.Context(), newDurableGradingPhysicalCallExecutor(o, job, run.questions[0]))
	db := o.deps.Records.DB()
	_, err := db.Exec(`CREATE TRIGGER program_finish_fail BEFORE UPDATE OF status ON k12_grading_item_invocations WHEN NEW.status='succeeded' AND NEW.execution_kind='local_deterministic' BEGIN SELECT RAISE(ABORT,'controlled completion failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	next := func(context.Context) (string, error) { calls++; return `{"output":"COMPUTED: 42"}`, nil }
	spec := GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveVerify, RequestDigest: strings.Repeat("c", 64)}
	if _, err := ExecuteGradingProgramCall(ctx, spec, next); err == nil {
		t.Fatal("missing durable completion must remain unresolved")
	}
	if _, err := db.Exec(`DROP TRIGGER program_finish_fail`); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteGradingProgramCall(ctx, spec, next); err == nil {
		t.Fatal("sent execution without durable result must not run again")
	}
	if calls != 1 {
		t.Fatalf("executions=%d want one", calls)
	}
}
