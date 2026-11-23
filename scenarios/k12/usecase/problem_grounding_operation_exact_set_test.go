package usecase

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestProblemGroundingProjectionDistinguishesLocalSolveFromMissingProviderEvidence(t *testing.T) {
	for _, execution := range []k12.GradingExecutionKind{k12.GradingExecutionLocalDeterministic, k12.GradingExecutionProvider} {
		t.Run(string(execution), func(t *testing.T) {
			o, jobID, _, _, _ := completedProblemGroundingFixture(t)
			ctx := t.Context()
			records := o.deps.Records
			items, err := records.ListGradingAssessmentItems(ctx, "mingming", jobID)
			if err != nil || len(items) != 2 {
				t.Fatalf("assessment fixture: %d items, %v", len(items), err)
			}
			source := items[0]
			original, err := records.GetGradingItemInvocation(ctx, source.AgentName, source.SolveInvocationID)
			if err != nil {
				t.Fatal(err)
			}
			solve := k12.GradingItemInvocation{
				InvocationID: "program-solve", AgentName: source.AgentName, JobID: jobID, ProblemID: source.ProblemID,
				AttemptID: source.AttemptID, InputRevision: source.InputRevision, InputDigest: source.InputDigest,
				Operation: k12.GradingItemOperationSolve, OperationAttempt: original.OperationAttempt + 1,
				ExecutionKind: execution, RequestDigest: "sha256:program-input", RouteSnapshot: original.RouteSnapshot,
			}
			if _, _, err = records.PrepareGradingItemInvocation(ctx, solve); err != nil {
				t.Fatal(err)
			}
			if _, err = records.MarkGradingItemInvocationSent(ctx, source.AgentName, solve.InvocationID); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(SolveHomeworkResult{Evidence: SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = records.MarkGradingItemInvocationSucceeded(ctx, source.AgentName, solve.InvocationID, modelInvocationDigest(raw), string(raw)); err != nil {
				t.Fatal(err)
			}
			// 复原同一题链接裸数值解法与带教材封套的评分回执，其他题的完整回执继续保留。
			if _, err = records.DB().ExecContext(ctx, `UPDATE k12_grading_assessment_items SET solve_invocation_id=? WHERE agent_name=? AND job_id=? AND problem_id=?`, solve.InvocationID, source.AgentName, jobID, source.ProblemID); err != nil {
				t.Fatal(err)
			}
			projection, err := o.ImageTaskHomeworkProjection(ctx, source.AgentName, jobID)
			if execution == k12.GradingExecutionProvider {
				if err == nil {
					t.Fatal("unwrapped provider result claiming numeric execution was accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("linked local solve blocked the grounded grade: %v", err)
			}
			operations := make(map[string][]string)
			for _, receipt := range projection.ProblemGroundingReceipts {
				operations[receipt.ProblemID] = append(operations[receipt.ProblemID], receipt.Operation)
			}
			if got := operations[source.ProblemID]; len(got) != 1 || got[0] != "grade" {
				t.Fatalf("local calculation must not publish a textbook receipt: %v", got)
			}
			if got := operations[items[1].ProblemID]; len(got) != 2 || got[0] != "solve" || got[1] != "grade" {
				t.Fatalf("grounding on other problem changed: %v", got)
			}
		})
	}
}

func TestProblemGroundingProjectionAcceptsDeterministicOutOfScopeWithoutProviderOperation(t *testing.T) {
	o, jobID, grounding, solver, grader := completedProblemGroundingFixture(t)
	items, err := o.deps.Records.ListGradingAssessmentItems(
		context.Background(), "mingming", jobID,
	)
	if err != nil || len(items) != 2 {
		t.Fatalf("list completed mixed-status fixture: items=%d err=%v", len(items), err)
	}
	if _, err := o.deps.Records.DB().ExecContext(
		context.Background(),
		`UPDATE k12_grading_assessment_items
		 SET status=?,solve_invocation_id=NULL,grade_invocation_id=NULL
		 WHERE agent_name=? AND job_id=? AND problem_id=?`,
		k12.GradingAssessmentOutOfScope, "mingming", jobID, items[0].ProblemID,
	); err != nil {
		t.Fatalf("seed mixed-status missing-solve receipt: %v", err)
	}
	freezesBefore, legacyBefore, queriesBefore := grounding.snapshot()
	solveBefore, gradeBefore := solver.callCount(), grader.callCount()
	projection, err := o.ImageTaskHomeworkProjection(
		context.Background(), "mingming", jobID,
	)
	if err != nil {
		t.Fatalf("completed public projection rejected deterministic out_of_scope: %v", err)
	}
	for _, receipt := range projection.ProblemGroundingReceipts {
		if receipt.ProblemID == items[0].ProblemID {
			t.Fatalf("deterministic out_of_scope fabricated provider receipt: %+v", receipt)
		}
	}
	freezesAfter, legacyAfter, queriesAfter := grounding.snapshot()
	if freezesAfter != freezesBefore || legacyAfter != legacyBefore ||
		len(queriesAfter) != len(queriesBefore) ||
		solver.callCount() != solveBefore || grader.callCount() != gradeBefore {
		t.Fatal("read-only projection performed external work")
	}
}
