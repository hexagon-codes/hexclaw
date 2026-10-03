package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type candidatePhysicalSolver struct{ calls int }

func (*candidatePhysicalSolver) UsesGradingPhysicalCalls() bool { return true }
func (s *candidatePhysicalSolver) Solve(ctx context.Context, _, _, _ string) (SolveResult, error) {
	_, err := ExecuteGradingPhysicalCall(ctx, GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveGenerate, RequestDigest: "candidate-existing-request"}, func(context.Context) (string, error) {
		s.calls++
		return `{"Output":"6"}`, nil
	})
	return SolveResult{}, err
}

func TestGradingProblemAssetCandidates(t *testing.T) {
	for _, scenario := range []string{"format_candidate", "exact_without_fts", "fts_failure_fallback", "changed_operator", "unknown_not_resent"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			solver, grader := &feedbackReassessmentSolver{answer: "6"}, &assetReuseGrader{}
			o := newParallelAnchorOrchestrator(t, &countingRecognizer{}, nil, WithGradingRunDir(t.TempDir()))
			o.deps.Solver, o.deps.Grader, o.deps.VerifiedGrader = solver, grader, nil
			o.deps.TextbookOwnerID = DefaultLocalOwnerScope
			seedStem := `\(18 \div 3 =\)`
			o.deps.Recognizer = &countingRecognizer{questions: []RecognizedQuestion{{Question: seedStem, Subject: "数学", StudentAnswer: "5", AnswerState: AnswerStatePresent}}}
			dispatcher := k12storage.NewDispatcher(o.deps.Records, ProblemAssetConsumer{Records: o.deps.Records})
			seed := runItemResumeJobToAssessing(t, o, "candidate-seed")
			if _, err := o.ConfirmAndRun(ctx, seed, nil); err != nil {
				t.Fatal(err)
			}
			if err := dispatcher.ProcessPending(ctx); err != nil {
				t.Fatal(err)
			}
			stem, answer := "18 / 3 =", "6"
			if scenario == "exact_without_fts" {
				stem = seedStem
			}
			if scenario == "changed_operator" {
				stem, answer, solver.answer = "18+3=", "21", "21"
			}
			if scenario == "exact_without_fts" || scenario == "fts_failure_fallback" {
				if _, err := o.deps.Records.DB().Exec(`DROP TABLE k12_problem_asset_candidates_fts`); err != nil {
					t.Fatal(err)
				}
			}
			o.deps.Recognizer = &countingRecognizer{questions: []RecognizedQuestion{{Question: stem, Subject: "数学", StudentAnswer: answer, AnswerState: AnswerStatePresent}}}
			jobID := runItemResumeJobToAssessing(t, o, "candidate-target")
			if scenario == "unknown_not_resent" {
				_, job := confirmItemResumeJobWithoutRun(t, o, jobID)
				run := o.lookup(jobID)
				q := run.questions[0]
				inv := k12.GradingItemInvocation{InvocationID: "candidate-existing-unknown", AgentName: "mingming", JobID: jobID, ProblemID: q.ProblemID, AttemptID: q.AttemptID,
					Operation: k12.GradingItemOperationSolveGenerate, ExecutionKind: k12.GradingExecutionProvider, OperationAttempt: 1, RequestDigest: "candidate-existing-request",
					InputRevision: q.ConfirmedVersion, InputDigest: q.InputDigest, RouteSnapshot: job.Fields.ModelSnapshot}
				if _, _, err := o.deps.Records.PrepareGradingItemInvocation(ctx, inv); err != nil {
					t.Fatal(err)
				}
				if _, err := o.deps.Records.MarkGradingItemInvocationSent(ctx, "mingming", inv.InvocationID); err != nil {
					t.Fatal(err)
				}
				if _, err := o.deps.Records.MarkGradingItemInvocationOutcomeUnknown(ctx, "mingming", inv.InvocationID, "provider_transport", "outcome_unknown"); err != nil {
					t.Fatal(err)
				}
				physical := &candidatePhysicalSolver{}
				o.deps.Solver = physical
				if _, err := o.runAssessItems(ctx, run, job); !errors.Is(err, ErrModelInvocationRequiresReconciliation) {
					t.Fatalf("unknown not retained: %v", err)
				}
				if solver.calls != 1 || len(grader.answers) != 1 || physical.calls != 0 {
					t.Fatalf("unknown resent: solve=%d grade=%d physical=%d", solver.calls, len(grader.answers), physical.calls)
				}
				unknown, err := o.deps.Records.GetGradingItemInvocation(ctx, "mingming", inv.InvocationID)
				if err != nil || unknown.Status != k12.ModelInvocationOutcomeUnknown {
					t.Fatalf("unknown receipt changed: %+v %v", unknown, err)
				}
				return
			}
			if _, err := o.ConfirmAndRun(ctx, jobID, nil); err != nil {
				t.Fatal(err)
			}
			items, err := o.deps.Records.ListGradingAssessmentItems(ctx, "mingming", jobID)
			if err != nil || len(items) != 1 || items[0].Status != k12.GradingAssessmentCorrect {
				t.Fatalf("independent assessment: %+v %v", items, err)
			}
			adopted := scenario == "format_candidate" || scenario == "exact_without_fts"
			if (items[0].AnswerSource != nil) != adopted {
				t.Fatalf("unexpected adoption: %+v", items[0].AnswerSource)
			}
			wantCalls := 2
			if adopted {
				wantCalls = 1
			}
			if solver.calls != wantCalls || len(grader.answers) != 2 {
				t.Fatalf("independent calls: solve=%d grade=%d", solver.calls, len(grader.answers))
			}
			if adopted {
				if items[0].SolveInvocationID != "" || items[0].GradeInvocationID == "" {
					t.Fatal("asset was recorded as a solver call or skipped grading")
				}
				job, err := o.deps.GetGradingJob(ctx, "mingming", jobID)
				if err != nil {
					t.Fatal(err)
				}
				run := o.lookup(jobID)
				q := run.questions[0]
				req := GradeRequest{AgentName: "mingming", Subject: "数学", Grade: run.req.Grade, Problem: stem}
				// 使用已冻结任务的年级，恢复原采用不创建第二份作答或求解。
				if _, _, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, req); err != nil {
					t.Fatalf("adoption restore: %v", err)
				}
				if solver.calls != 1 {
					t.Fatal("restored candidate adoption resent solve")
				}
			}
		})
	}
}
