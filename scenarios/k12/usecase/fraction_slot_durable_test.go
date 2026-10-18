package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

const fractionSlotSharedFixture = `小明 $\frac{2}{3}$ 小时走了 $2$ 千米，照这样的速度，他每小时行走多少千米？

线段图标注：$1$ 小时走了？$\mathrm{km}$；$\frac{1}{3}$ 小时走了？$\mathrm{km}$；$\frac{2}{3}$ 小时走了 $2\mathrm{km}$。`

func fractionSlotDurableFixture(t *testing.T, phantom bool) (*GradingOrchestrator, *gradingRun, GradingJobView, []RecognizedQuestion, *itemResumeSolver, *itemResumeGrader) {
	t.Helper()
	solver := &itemResumeSolver{calls: map[string]int{}, solutions: map[string]string{}}
	grader := &itemResumeGrader{calls: map[string]int{}, outcomes: map[string]GradeOutcome{}}
	questions := []RecognizedQuestion{
		{ProblemID: "source-parent", ProblemKind: ProblemKindCompoundParent, Question: fractionSlotSharedFixture, Subject: "数学"},
		{ProblemKind: ProblemKindSubproblem, ParentProblemID: "source-parent", SubproblemNo: "求一份", Question: `也就是 $2\div2=2\times\frac{\square}{\square}$。`, Subject: "数学", StudentAnswer: "1/2", AnswerState: AnswerStatePresent},
		{ProblemKind: ProblemKindSubproblem, ParentProblemID: "source-parent", SubproblemNo: "求三份", Question: `也就是 $2\times\frac{\square}{\square}\times3$。`, Subject: "数学", StudentAnswer: "1/2", AnswerState: AnswerStatePresent},
		{Question: "1+1=", Subject: "数学", StudentAnswer: "2", AnswerState: AnswerStatePresent},
	}
	if phantom {
		questions[1].SubproblemNo, questions[2].SubproblemNo = "c", "e"
		questions[1].Question, questions[1].RawTranscription = `$2\div 2=2\times\frac{\phantom{1}}{\phantom{2}}$；`, "2÷2＝2×　/　；"
		questions[2].Question, questions[2].RawTranscription = `也就是 $2\times\frac{\phantom{1}}{\phantom{2}}\times3$。`, "也就是2×　/　×3。"
	}
	o := newItemResumeOrchestrator(t, t.TempDir(), questions, solver, grader)
	o.deps.TextbookOwnerID = DefaultLocalOwnerScope
	jobID := runItemResumeJobToAssessing(t, o, "fraction-slot-durable")
	o.lookup(jobID).req.Grade = "六年级上"
	run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
	questions = RecognizedQuestionsForAssessment(run.questions)
	if len(questions) != 3 || questions[0].ParentProblemID == "" || questions[0].ParentProblemID != questions[1].ParentProblemID {
		t.Fatalf("compound source fixture did not preserve children: %+v", questions)
	}
	return o, run, job, questions, solver, grader
}

// 保留缺陷发生时的请求事实，用于证明新投影不会匹配旧路程回执。
func fractionSlotLegacyRequest(req PhotoGradeRequest, q RecognizedQuestion) GradeRequest {
	legacy := photoItemGradeRequest(req, q)
	legacy.Problem = q.Question + "\n\nFor this request, solve or assess only subproblem " + q.SubproblemNo + ". Use the shared material as context; do not answer the other subproblems."
	return legacy
}

func TestFractionSlotSQLiteNewScopeDoesNotReuseLegacyReceipts(t *testing.T) {
	for _, shape := range []string{"boxes", "phantom"} {
		t.Run(shape, func(t *testing.T) { fractionSlotSQLiteNewScopeDoesNotReuseLegacyReceipts(t, shape == "phantom") })
	}
}

func fractionSlotSQLiteNewScopeDoesNotReuseLegacyReceipts(t *testing.T, phantom bool) {
	o, run, job, questions, solver, grader := fractionSlotDurableFixture(t, phantom)
	ctx := t.Context()
	other, err := o.assessDurablePhotoItem(ctx, o.deps, job, run.req, PhotoModeGrade, questions[2])
	if err != nil || other.Status != PhotoCorrect {
		t.Fatalf("unaffected item: %s %v", other.Status, err)
	}
	otherReceipt, err := o.deps.Records.GetGradingAssessmentItem(ctx, job.Record.AgentName, job.Record.RecordID, questions[2].ProblemID)
	if err != nil {
		t.Fatal(err)
	}
	for index, q := range questions[:2] {
		before, _ := json.Marshal(q)
		legacy := fractionSlotLegacyRequest(run.req, q)
		current := photoItemGradeRequest(run.req, q)
		if !strings.Contains(current.Problem, "k12-fraction-slot-v1") || legacy.Problem == current.Problem || current.StudentAnswer != "1/2" {
			t.Fatalf("child %s lacks scoped fraction target: %q", q.SubproblemNo, current.Problem)
		}
		oldSolution := []string{"1 千米", "3 千米"}[index]
		solver.solutions[legacy.Problem], solver.solutions[current.Problem] = oldSolution, "1/2"
		wrong, correct := false, true
		grader.outcomes[legacy.Problem] = GradeOutcome{Verdict: VerdictDisagree, FinalAnswerCorrect: &wrong, ErrorCause: "Missing distance unit."}
		grader.outcomes[current.Problem] = GradeOutcome{Verdict: VerdictAgree, FinalAnswerCorrect: &correct}
		oldSolved, oldSolveID, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, legacy)
		if err != nil || oldSolved.Solution != oldSolution {
			t.Fatalf("legacy solve fixture: %+v %v", oldSolved, err)
		}
		_, oldGradeID, err := executeDurableGradeOperation(ctx, o, o.deps, job, q, legacy, oldSolved)
		if err != nil {
			t.Fatal(err)
		}
		oldSolve, _ := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, oldSolveID)
		oldGrade, _ := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, oldGradeID)
		newSolved, newSolveID, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, current)
		if err != nil || newSolved.Solution != "1/2" || newSolveID == oldSolveID || newSolved.AnswerSource != nil || newSolved.assetPublication != nil {
			t.Fatalf("new target reused old distance answer or published child asset: %+v id=%s err=%v", newSolved, newSolveID, err)
		}
		newGraded, newGradeID, err := executeDurableGradeOperation(ctx, o, o.deps, job, q, current, newSolved)
		if err != nil || newGradeID == oldGradeID || newGraded.Outcome.Verdict != VerdictAgree || newGraded.Outcome.FinalAnswerCorrect == nil || !*newGraded.Outcome.FinalAnswerCorrect {
			t.Fatalf("new fraction answer reused old grade: %+v id=%s err=%v", newGraded, newGradeID, err)
		}
		newSolve, _ := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, newSolveID)
		newGrade, _ := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, newGradeID)
		if oldSolve.RequestDigest == newSolve.RequestDigest || oldGrade.RequestDigest == newGrade.RequestDigest {
			t.Fatal("changed fraction target did not change immutable solve/grade digest")
		}
		item, err := o.assessDurablePhotoItem(ctx, o.deps, job, run.req, PhotoModeGrade, q)
		if err != nil || item.Status != PhotoCorrect || solver.callCount(current.Problem) != 1 || grader.callCount(current.Problem) != 1 {
			t.Fatalf("new receipt replay repeated execution: %s solve=%d grade=%d err=%v", item.Status, solver.callCount(current.Problem), grader.callCount(current.Problem), err)
		}
		gotOldSolve, _ := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, oldSolveID)
		gotOldGrade, _ := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, oldGradeID)
		after, _ := json.Marshal(q)
		if !reflect.DeepEqual(oldSolve, gotOldSolve) || !reflect.DeepEqual(oldGrade, gotOldGrade) || string(before) != string(after) {
			t.Fatal("scoped execution rewrote old receipts or original source")
		}
	}
	otherAfter, err := o.deps.Records.GetGradingAssessmentItem(ctx, job.Record.AgentName, job.Record.RecordID, questions[2].ProblemID)
	if err != nil || !reflect.DeepEqual(otherReceipt, otherAfter) || solver.callCount("1+1=") != 1 || grader.callCount("1+1=") != 1 {
		t.Fatalf("successful sibling was changed or repeated: %v", err)
	}
	var assets int
	if err := o.deps.Records.DB().QueryRowContext(ctx, `SELECT count(*) FROM k12_problem_assets`).Scan(&assets); err != nil || assets != 0 {
		t.Fatalf("child slot created a cross-question asset: count=%d err=%v", assets, err)
	}
}

type fractionSlotUnknownSolver struct{ calls int }

func (s *fractionSlotUnknownSolver) Solve(context.Context, string, string, string) (SolveResult, error) {
	s.calls++
	return SolveResult{}, context.DeadlineExceeded
}

func TestFractionSlotSQLiteUnknownLegacyCallIsNotResent(t *testing.T) {
	for _, shape := range []string{"boxes", "phantom"} {
		t.Run(shape, func(t *testing.T) { fractionSlotSQLiteUnknownLegacyCallIsNotResent(t, shape == "phantom") })
	}
}

func fractionSlotSQLiteUnknownLegacyCallIsNotResent(t *testing.T, phantom bool) {
	o, run, job, questions, _, _ := fractionSlotDurableFixture(t, phantom)
	ctx := t.Context()
	solver := &fractionSlotUnknownSolver{}
	o.deps.Solver = solver
	q := questions[0]
	legacy := fractionSlotLegacyRequest(run.req, q)
	if photoItemGradeRequest(run.req, q).Problem == legacy.Problem {
		t.Fatal("new target was not installed")
	}
	_, invocationID, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, legacy)
	if !errors.Is(err, context.DeadlineExceeded) || solver.calls != 1 {
		t.Fatalf("unknown fixture was not sent exactly once: %d %v", solver.calls, err)
	}
	before, err := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, invocationID)
	if err != nil || before.Status != k12.ModelInvocationOutcomeUnknown {
		t.Fatalf("missing durable unknown: %+v %v", before, err)
	}
	unknown, err := o.markGradingOutcomeUnknown(ctx, run, job.Record.RecordID, "item_invocation_outcome_unknown")
	if err != nil || unknown.Record.Status != k12.GradingStageOutcomeUnknown {
		t.Fatalf("unknown task state: %s %v", unknown.Record.Status, err)
	}
	if _, err := o.RetryAndRun(ctx, job.Record.RecordID); err == nil {
		t.Fatal("unknown old task was restarted by the new projection")
	}
	if _, _, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, legacy); !errors.Is(err, ErrModelInvocationRequiresReconciliation) {
		t.Fatalf("unknown exact request replay did not stop: %v", err)
	}
	after, err := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, invocationID)
	if err != nil || !reflect.DeepEqual(before, after) || solver.calls != 1 {
		t.Fatalf("unknown receipt changed or resent: calls=%d err=%v", solver.calls, err)
	}
}

func TestFractionSlotSQLiteCompletedLegacyArtifactRemainsFrozen(t *testing.T) {
	for _, shape := range []string{"boxes", "phantom"} {
		t.Run(shape, func(t *testing.T) { fractionSlotSQLiteCompletedLegacyArtifactRemainsFrozen(t, shape == "phantom") })
	}
}

func fractionSlotSQLiteCompletedLegacyArtifactRemainsFrozen(t *testing.T, phantom bool) {
	o, run, job, questions, solver, grader := fractionSlotDurableFixture(t, phantom)
	ctx := t.Context()
	for _, q := range questions {
		if q.ProblemKind != ProblemKindSubproblem {
			if _, err := o.assessDurablePhotoItem(ctx, o.deps, job, run.req, PhotoModeGrade, q); err != nil {
				t.Fatal(err)
			}
			continue
		}
		legacy := fractionSlotLegacyRequest(run.req, q)
		solver.solutions[legacy.Problem] = "3 千米"
		wrong := false
		grader.outcomes[legacy.Problem] = GradeOutcome{Verdict: VerdictDisagree, FinalAnswerCorrect: &wrong, ErrorCause: "Missing distance unit."}
		solved, solveID, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, legacy)
		if err != nil {
			t.Fatal(err)
		}
		graded, gradeID, err := executeDurableGradeOperation(ctx, o, o.deps, job, q, legacy, solved)
		if err != nil {
			t.Fatal(err)
		}
		guide := ParentTeachingGuide{Answer: "3 千米", FullSolutionSteps: []string{"按旧请求计算路程。"}, GradeLevelMethod: "分步计算", LikelyMistakes: []string{"旧请求包含路程单位。"}, ParentTeachingSequence: []string{"读题。"}, FollowUpQuestions: []string{"路程单位是什么？"}, CheckingMethod: "代回旧请求。"}
		_, guideID, err := executeGradingItemOperation(ctx, o, job, q, k12.GradingItemOperationParentGuide, legacy, func(context.Context) (ParentTeachingGuide, error) { return guide, nil })
		if err != nil {
			t.Fatal(err)
		}
		if _, err := commitGradingAssessmentItem(ctx, o.deps, job, q, PhotoGradeItem{Recognized: q, Status: PhotoWrong, Grade: graded, ParentGuide: &guide}, solveID, gradeID, guideID, k12storage.GradingAssessmentEffects{}); err != nil {
			t.Fatal(err)
		}
	}
	completed, err := o.RunGradingJob(ctx, job.Record.RecordID)
	if err != nil || completed.Record.Status != k12.GradingStageCompleted {
		t.Fatalf("legacy completed fixture: %s %v", completed.Record.Status, err)
	}
	before, err := o.deps.Records.GetGradingFinalArtifactByJob(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil || before.CanonicalMarkdown == "" {
		t.Fatalf("legacy artifact missing: %v", err)
	}
	oldCalls := 0
	for _, count := range solver.calls {
		oldCalls += count
	}
	if _, err := o.RunGradingJob(ctx, job.Record.RecordID); err != nil {
		t.Fatal(err)
	}
	after, err := o.deps.Records.GetGradingFinalArtifactByJob(ctx, job.Record.AgentName, job.Record.RecordID)
	newCalls := 0
	for _, count := range solver.calls {
		newCalls += count
	}
	if err != nil || !reflect.DeepEqual(before, after) || oldCalls != newCalls {
		t.Fatalf("new projection changed a completed artifact or resent solve: before=%d after=%d err=%v", oldCalls, newCalls, err)
	}
}
