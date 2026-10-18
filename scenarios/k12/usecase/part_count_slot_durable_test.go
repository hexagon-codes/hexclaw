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

func partCountSlotDurableFixture(t *testing.T) (*GradingOrchestrator, *gradingRun, GradingJobView, []RecognizedQuestion, *itemResumeSolver, *itemResumeGrader) {
	t.Helper()
	solver := &itemResumeSolver{calls: map[string]int{}, solutions: map[string]string{}}
	grader := &itemResumeGrader{calls: map[string]int{}, outcomes: map[string]GradeOutcome{}}
	questions := []RecognizedQuestion{
		{ProblemID: "source-parent", ProblemKind: ProblemKindCompoundParent, Question: fractionSlotSharedFixture, RawTranscription: fractionSlotSharedFixture, CanonicalMarkdown: fractionSlotSharedFixture, Subject: "数学"},
		{ProblemKind: ProblemKindSubproblem, ParentProblemID: "source-parent", SubproblemNo: "b", Question: partCountSlotSquareCanonicalFixture, CanonicalMarkdown: partCountSlotSquareCanonicalFixture,
			RawTranscription: partCountSlotSquareRawFixture, Subject: "数学", StudentAnswer: "2/3", AnswerState: AnswerStatePresent},
		{Question: "1+1=", Subject: "数学", StudentAnswer: "2", AnswerState: AnswerStatePresent},
	}
	o := newItemResumeOrchestrator(t, t.TempDir(), questions, solver, grader)
	o.deps.TextbookOwnerID = DefaultLocalOwnerScope
	jobID := runItemResumeJobToAssessing(t, o, "part-count-slot-durable")
	o.lookup(jobID).req.Grade = "六年级上"
	run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
	questions = RecognizedQuestionsForAssessment(run.questions)
	if len(questions) != 2 || questions[0].ProblemKind != ProblemKindSubproblem {
		t.Fatal("count source fixture lost its child identity")
	}
	return o, run, job, questions, solver, grader
}

func TestPartCountSlotSQLiteNewTargetChangesDigestAndReplays(t *testing.T) {
	o, run, job, questions, solver, grader := partCountSlotDurableFixture(t)
	ctx := t.Context()
	if other, err := o.assessDurablePhotoItem(ctx, o.deps, job, run.req, PhotoModeGrade, questions[1]); err != nil || other.Status != PhotoCorrect {
		t.Fatalf("sibling fixture failed: %s %v", other.Status, err)
	}
	siblingBefore, err := o.deps.Records.GetGradingAssessmentItem(ctx, job.Record.AgentName, job.Record.RecordID, questions[1].ProblemID)
	if err != nil {
		t.Fatal(err)
	}
	q := questions[0]
	before, _ := json.Marshal(q)
	legacy, current := fractionSlotLegacyRequest(run.req, q), photoItemGradeRequest(run.req, q)
	if !strings.HasPrefix(current.Problem, "k12-part-count-slot-v1\n") || legacy.Problem == current.Problem || current.StudentAnswer != "2/3" {
		t.Fatal("durable request lost the independent count target")
	}
	solver.solutions[legacy.Problem], solver.solutions[current.Problem] = "1千米；2份；2/3小时", "2份"
	wrong := false
	grader.outcomes[legacy.Problem] = GradeOutcome{Verdict: VerdictUnverifiable, FinalAnswerCorrect: &wrong, ErrorCause: "Parent distance is not the current count."}
	grader.outcomes[current.Problem] = GradeOutcome{Verdict: VerdictDisagree, FinalAnswerCorrect: &wrong, ErrorCause: "The printed count is 2, not 2/3."}
	oldSolved, oldSolveID, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, legacy)
	if err != nil {
		t.Fatal(err)
	}
	_, oldGradeID, err := executeDurableGradeOperation(ctx, o, o.deps, job, q, legacy, oldSolved)
	if err != nil {
		t.Fatal(err)
	}
	oldSolve, _ := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, oldSolveID)
	oldGrade, _ := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, oldGradeID)
	newSolved, newSolveID, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, current)
	if err != nil || newSolveID == oldSolveID || newSolved.Solution != "2份" || newSolved.AnswerSource != nil || newSolved.assetPublication != nil {
		t.Fatalf("new target reused old answer or published a child asset: %+v %v", newSolved, err)
	}
	newGraded, newGradeID, err := executeDurableGradeOperation(ctx, o, o.deps, job, q, current, newSolved)
	if err != nil || newGradeID == oldGradeID || newGraded.Outcome.Verdict != VerdictDisagree {
		t.Fatalf("new target reused old grade: %+v %v", newGraded, err)
	}
	newSolve, _ := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, newSolveID)
	newGrade, _ := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, newGradeID)
	if oldSolve.RequestDigest == newSolve.RequestDigest || oldGrade.RequestDigest == newGrade.RequestDigest {
		t.Fatal("changed count target did not change the durable request digests")
	}
	if _, replayID, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, current); err != nil || replayID != newSolveID || solver.callCount(current.Problem) != 1 {
		t.Fatalf("successful count solve was resent: %s %v", replayID, err)
	}
	if _, replayID, err := executeDurableGradeOperation(ctx, o, o.deps, job, q, current, newSolved); err != nil || replayID != newGradeID || grader.callCount(current.Problem) != 1 {
		t.Fatalf("successful count grade was resent: %s %v", replayID, err)
	}
	gotOldSolve, _ := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, oldSolveID)
	gotOldGrade, _ := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, oldGradeID)
	siblingAfter, err := o.deps.Records.GetGradingAssessmentItem(ctx, job.Record.AgentName, job.Record.RecordID, questions[1].ProblemID)
	after, _ := json.Marshal(q)
	if err != nil || !reflect.DeepEqual(oldSolve, gotOldSolve) || !reflect.DeepEqual(oldGrade, gotOldGrade) || !reflect.DeepEqual(siblingBefore, siblingAfter) || string(before) != string(after) {
		t.Fatal("new request changed an existing receipt, sibling, or recognition source")
	}
	var assets int
	if err := o.deps.Records.DB().QueryRowContext(ctx, `SELECT count(*) FROM k12_problem_assets`).Scan(&assets); err != nil || assets != 0 {
		t.Fatalf("count child created an extra asset: %d %v", assets, err)
	}
}

func TestPartCountSlotSQLiteUnknownLegacyInvocationIsNotResent(t *testing.T) {
	o, run, job, questions, _, _ := partCountSlotDurableFixture(t)
	ctx := t.Context()
	solver := &fractionSlotUnknownSolver{}
	o.deps.Solver = solver
	q := questions[0]
	legacy := fractionSlotLegacyRequest(run.req, q)
	_, invocationID, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, legacy)
	if !errors.Is(err, context.DeadlineExceeded) || solver.calls != 1 {
		t.Fatalf("unknown fixture was not one physical call: %v calls=%d", err, solver.calls)
	}
	before, err := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, invocationID)
	if err != nil || before.Status != k12.ModelInvocationOutcomeUnknown {
		t.Fatalf("unknown receipt missing: %+v %v", before, err)
	}
	if _, err := o.markGradingOutcomeUnknown(ctx, run, job.Record.RecordID, "item_invocation_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RetryAndRun(ctx, job.Record.RecordID); err == nil {
		t.Fatal("new count target restarted an unknown task")
	}
	if _, _, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, legacy); !errors.Is(err, ErrModelInvocationRequiresReconciliation) {
		t.Fatalf("unknown exact replay was not blocked: %v", err)
	}
	after, err := o.deps.Records.GetGradingItemInvocation(ctx, job.Record.AgentName, invocationID)
	if err != nil || !reflect.DeepEqual(before, after) || solver.calls != 1 {
		t.Fatal("unknown receipt was changed or resent")
	}
}

func TestPartCountSlotSQLiteCompletedArtifactRemainsFrozen(t *testing.T) {
	o, run, job, questions, solver, grader := partCountSlotDurableFixture(t)
	ctx := t.Context()
	if _, err := o.assessDurablePhotoItem(ctx, o.deps, job, run.req, PhotoModeGrade, questions[1]); err != nil {
		t.Fatal(err)
	}
	q := questions[0]
	legacy := fractionSlotLegacyRequest(run.req, q)
	solver.solutions[legacy.Problem] = "3千米"
	wrong := false
	grader.outcomes[legacy.Problem] = GradeOutcome{Verdict: VerdictDisagree, FinalAnswerCorrect: &wrong}
	solved, solveID, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, legacy)
	if err != nil {
		t.Fatal(err)
	}
	graded, gradeID, err := executeDurableGradeOperation(ctx, o, o.deps, job, q, legacy, solved)
	if err != nil {
		t.Fatal(err)
	}
	guide := ParentTeachingGuide{Answer: "3千米", FullSolutionSteps: []string{"计算旧路程。"}, GradeLevelMethod: "分步计算", LikelyMistakes: []string{"旧题面单位。"}, ParentTeachingSequence: []string{"读原题。"}, FollowUpQuestions: []string{"原单位是什么？"}, CheckingMethod: "核对旧路程。"}
	_, guideID, err := executeGradingItemOperation(ctx, o, job, q, k12.GradingItemOperationParentGuide, legacy, func(context.Context) (ParentTeachingGuide, error) { return guide, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commitGradingAssessmentItem(ctx, o.deps, job, q, PhotoGradeItem{Recognized: q, Status: PhotoWrong, Grade: graded, ParentGuide: &guide}, solveID, gradeID, guideID, k12storage.GradingAssessmentEffects{}); err != nil {
		t.Fatal(err)
	}
	completed, err := o.RunGradingJob(ctx, job.Record.RecordID)
	if err != nil || completed.Record.Status != k12.GradingStageCompleted {
		t.Fatalf("legacy artifact fixture did not complete: %s %v", completed.Record.Status, err)
	}
	before, err := o.deps.Records.GetGradingFinalArtifactByJob(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil || before.CanonicalMarkdown == "" {
		t.Fatalf("legacy artifact missing: %v", err)
	}
	calls := solver.callCount(legacy.Problem)
	if _, err := o.RunGradingJob(ctx, job.Record.RecordID); err != nil {
		t.Fatal(err)
	}
	after, err := o.deps.Records.GetGradingFinalArtifactByJob(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil || !reflect.DeepEqual(before, after) || solver.callCount(legacy.Problem) != calls || solver.callCount(photoItemGradeRequest(run.req, q).Problem) != 0 {
		t.Fatal("new target rewrote or reran a completed legacy artifact")
	}
}
