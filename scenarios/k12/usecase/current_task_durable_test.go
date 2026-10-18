package usecase

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func mathSubproblemDurableFixture(t *testing.T) (*GradingOrchestrator, *gradingRun, GradingJobView, RecognizedQuestion, *itemResumeSolver) {
	t.Helper()
	solver := &itemResumeSolver{calls: map[string]int{}, solutions: map[string]string{}}
	grader := &itemResumeGrader{calls: map[string]int{}, outcomes: map[string]GradeOutcome{}}
	questions := []RecognizedQuestion{
		{ProblemID: "parent", ProblemKind: ProblemKindCompoundParent, Question: "小明2/3小时走2千米；另有一道乘法结合律填空。", Subject: "数学"},
		{ProblemKind: ProblemKindSubproblem, ParentProblemID: "parent", SubproblemNo: "2", Question: "1/3小时走了？km", RawTranscription: "1/3小时走了？km", Subject: "数学", AnswerState: AnswerStateBlank},
	}
	o := newItemResumeOrchestrator(t, t.TempDir(), questions, solver, grader)
	o.deps.TextbookOwnerID = DefaultLocalOwnerScope
	jobID := runItemResumeJobToAssessing(t, o, "current-math-task")
	o.lookup(jobID).req.Grade = "六年级上"
	run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
	assessed := RecognizedQuestionsForAssessment(run.questions)
	if len(assessed) != 1 {
		t.Fatal("current source lost its single graph question")
	}
	return o, run, job, assessed[0], solver
}

func TestMathSubproblemSQLiteDigestSeparatesOrdinalAnswerAndReplays(t *testing.T) {
	o, run, job, q, solver := mathSubproblemDurableFixture(t)
	ctx := t.Context()
	legacy, current := fractionSlotLegacyRequest(run.req, q), photoItemGradeRequest(run.req, q)
	solver.solutions[legacy.Problem], solver.solutions[current.Problem] = "乘法结合律", "1千米"
	_, oldID, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, legacy)
	if err != nil {
		t.Fatal(err)
	}
	old, err := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, oldID)
	if err != nil {
		t.Fatal(err)
	}
	result, newID, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, current)
	if err != nil || oldID == newID || result.Solution != "1千米" || result.AnswerSource != nil || result.assetPublication != nil {
		t.Fatalf("current task reused an ordinal answer: %+v %v", result, err)
	}
	newReceipt, err := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, newID)
	if err != nil || newReceipt.RequestDigest == old.RequestDigest {
		t.Fatal("exact current task did not change the durable request digest")
	}
	if _, id, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, current); err != nil || id != newID || solver.callCount(current.Problem) != 1 {
		t.Fatalf("current task replay sent the successful operation again: %s %v", id, err)
	}
	after, err := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, oldID)
	if err != nil || !reflect.DeepEqual(old, after) {
		t.Fatal("new scope changed the original response")
	}
}

func TestMathSubproblemSQLiteUnknownTaskIsNotRestarted(t *testing.T) {
	o, run, job, q, _ := mathSubproblemDurableFixture(t)
	solver := &fractionSlotUnknownSolver{}
	o.deps.Solver = solver
	ctx := t.Context()
	legacy := fractionSlotLegacyRequest(run.req, q)
	_, id, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, legacy)
	if !errors.Is(err, context.DeadlineExceeded) || solver.calls != 1 {
		t.Fatalf("unknown fixture did not send exactly once: %v", err)
	}
	before, err := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, id)
	if err != nil || before.Status != k12.ModelInvocationOutcomeUnknown {
		t.Fatal("unknown receipt was not retained")
	}
	if _, err := o.markGradingOutcomeUnknown(ctx, run, job.Record.RecordID, "item_invocation_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RetryAndRun(ctx, job.Record.RecordID); err == nil {
		t.Fatal("new target enabled retry of an unknown task")
	}
	if _, _, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, legacy); !errors.Is(err, ErrModelInvocationRequiresReconciliation) {
		t.Fatalf("unknown original invocation was resent: %v", err)
	}
	after, err := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, id)
	if err != nil || !reflect.DeepEqual(before, after) || solver.calls != 1 {
		t.Fatal("unknown record changed or was resent")
	}
}
