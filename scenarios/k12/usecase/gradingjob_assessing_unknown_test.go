package usecase

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// 边界主动丢掉子调用错误包装，复现外层仅剩普通 deadline 的真实形状。
type assessingUnknownPlainDeadlineSolver struct {
	physical *grading20260726PhysicalSolver
}

func (*assessingUnknownPlainDeadlineSolver) UsesGradingPhysicalCalls() bool { return true }

func (s *assessingUnknownPlainDeadlineSolver) Solve(ctx context.Context, problem, grade, scope string) (SolveResult, error) {
	_, _ = s.physical.Solve(ctx, problem, grade, scope)
	return SolveResult{}, context.DeadlineExceeded
}

func newAssessingUnknownFixture(t *testing.T, key string) (*GradingOrchestrator, *gradingRun, GradingJobView) {
	t.Helper()
	o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{{
		Question: "1+1=", Subject: "数学", StudentAnswer: "3", AnswerState: AnswerStatePresent,
	}}, &itemResumeSolver{calls: map[string]int{}}, &itemResumeGrader{calls: map[string]int{}})
	jobID := runItemResumeJobToAssessing(t, o, key)
	run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
	return o, run, job
}

func prepareAssessingUnknownReceipt(t *testing.T, o *GradingOrchestrator, job GradingJobView,
	q RecognizedQuestion, operation k12.GradingItemOperation, attempt int,
	status k12.ModelInvocationStatus, revise func(*k12.GradingItemInvocation),
) k12.GradingItemInvocation {
	t.Helper()
	ctx := context.Background()
	item := k12.GradingItemInvocation{
		InvocationID: job.Record.RecordID + ":" + string(operation) + ":" + string(rune('0'+attempt)),
		AgentName:    job.Record.AgentName, JobID: job.Record.RecordID,
		ProblemID: q.ProblemID, AttemptID: q.AttemptID, Operation: operation,
		OperationAttempt: attempt, ExecutionKind: k12.GradingExecutionProvider,
		RequestDigest: "original-request-digest", InputRevision: q.ConfirmedVersion, InputDigest: q.InputDigest,
		RouteSnapshot: job.Fields.ModelSnapshot, CreatedAt: time.Now().Unix(),
	}
	if revise != nil {
		revise(&item)
	}
	stored, _, err := o.deps.Records.PrepareGradingItemInvocation(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if status == k12.ModelInvocationPrepared {
		return stored
	}
	stored, err = o.deps.Records.MarkGradingItemInvocationSent(ctx, item.AgentName, item.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	switch status {
	case k12.ModelInvocationSucceeded:
		stored, err = o.deps.Records.MarkGradingItemInvocationSucceeded(ctx, item.AgentName, item.InvocationID, "successful-result", `{"solution":"2"}`)
	case k12.ModelInvocationFailed:
		stored, err = o.deps.Records.MarkGradingItemInvocationFailed(ctx, item.AgentName, item.InvocationID, "provider_response", "503")
	case k12.ModelInvocationOutcomeUnknown:
		stored, err = o.deps.Records.MarkGradingItemInvocationOutcomeUnknown(ctx, item.AgentName, item.InvocationID, "transport", "deadline")
	}
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func failAssessingUnknownFixture(t *testing.T, o *GradingOrchestrator, job GradingJobView) GradingJobView {
	t.Helper()
	v, err := o.deps.AdvanceGradingStage(context.Background(), job.Record.AgentName, job.Record.RecordID,
		AdvanceGradingInput{Outcome: GradingOutcomeFailed, FailureKind: "assess_item_failed", Retryable: true})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAssessingUnknownPlainDeadlinePreservesPhysicalHistory(t *testing.T) {
	ctx := context.Background()
	o, run, job := newAssessingUnknownFixture(t, "assessing-plain-deadline")
	physical := &grading20260726PhysicalSolver{
		calls:     map[k12.GradingItemOperation]int{},
		failFirst: map[k12.GradingItemOperation]error{k12.GradingItemOperationSolveVerify: context.DeadlineExceeded},
	}
	o.deps.Solver = &assessingUnknownPlainDeadlineSolver{physical: physical}
	v, err := o.runAssessItems(ctx, run, job)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrGradingPhysicalCallOutcomeUnknown) {
		t.Fatalf("outer error=%v, want plain deadline", err)
	}
	if v.Record.Status != k12.GradingStageOutcomeUnknown || v.Fields.Retryable ||
		v.Fields.AttemptCount != job.Fields.AttemptCount || v.Fields.Deadline != 0 ||
		v.Fields.FailedStage != k12.GradingStageAssessing || v.Fields.FailureKind != "item_invocation_outcome_unknown" {
		t.Fatalf("unknown projection=%+v status=%s", v.Fields, v.Record.Status)
	}
	assertAssessingUnknownOnlyChangesDerivedFields(t, job, v)
	items, err := o.deps.Records.ListGradingItemInvocations(ctx, "mingming", job.Record.RecordID)
	if err != nil || len(items) != 2 {
		t.Fatalf("physical history=%+v err=%v", items, err)
	}
	if items[0].Operation != k12.GradingItemOperationSolveGenerate || items[0].Status != k12.ModelInvocationSucceeded ||
		items[1].Operation != k12.GradingItemOperationSolveVerify || items[1].Status != k12.ModelInvocationOutcomeUnknown {
		t.Fatalf("successful generation and unknown verification lost: %+v", items)
	}
	aggregates, err := o.deps.Records.ListModelInvocations(ctx, "mingming", job.Record.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	for _, aggregate := range aggregates {
		if aggregate.Stage == k12.GradingStageAssessing && aggregate.Status != k12.ModelInvocationOutcomeUnknown {
			t.Fatalf("assessing aggregate=%+v", aggregate)
		}
	}
	assertAssessingUnknownParkedWithoutCalls(t, o, job.Record.RecordID, v.Fields.AttemptCount, items, aggregates)
	if physical.count(k12.GradingItemOperationSolveGenerate) != 1 || physical.count(k12.GradingItemOperationSolveVerify) != 1 {
		t.Fatal("startup or retry resent a physical provider call")
	}
	if _, err := o.deps.Records.GetGradingFinalArtifactByJob(ctx, "mingming", job.Record.RecordID); err == nil {
		t.Fatal("unknown assessment must not publish a final artifact")
	}
}

func assertAssessingUnknownParkedWithoutCalls(t *testing.T, o *GradingOrchestrator, jobID string, attempt int,
	before []k12.GradingItemInvocation, aggregates []k12.ModelInvocation,
) {
	t.Helper()
	ctx := context.Background()
	restarted := trackGradingOrchestrator(t, NewGradingOrchestrator(o.deps, orchestratorSnapshotResolver, WithGradingRunDir(o.runDir)))
	for i := 0; i < 2; i++ {
		if _, err := restarted.RecoverGradingJobs(ctx, []string{"mingming"}); err != nil {
			t.Fatal(err)
		}
		idleCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := restarted.WaitForIdle(idleCtx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := restarted.deps.RetryGradingJob(ctx, "mingming", jobID); err == nil {
			t.Fatal("ordinary retry queued an unknown task")
		}
		if _, err := restarted.deps.RetryGradingJobWithParentAutomaticWindow(ctx, "mingming", jobID, "new-parent-window", 9999999999); err == nil {
			t.Fatal("fresh parent window bypassed unknown receipts")
		}
		if eligible, err := restarted.CanRetryPhotoGradingWithParentAutomaticWindow(ctx, jobID); err != nil || eligible {
			t.Fatalf("parent retry preflight=%v err=%v", eligible, err)
		}
		view, err := restarted.deps.GetGradingJob(ctx, "mingming", jobID)
		if err != nil || view.Record.Status != k12.GradingStageOutcomeUnknown || view.Fields.AttemptCount != attempt || view.Fields.Retryable {
			t.Fatalf("startup/retry changed unknown task: %+v err=%v", view, err)
		}
	}
	after, err := o.deps.Records.ListGradingItemInvocations(ctx, "mingming", jobID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("immutable item history changed: before=%+v after=%+v err=%v", before, after, err)
	}
	afterAggregates, err := o.deps.Records.ListModelInvocations(ctx, "mingming", jobID)
	if err != nil || !reflect.DeepEqual(aggregates, afterAggregates) {
		t.Fatalf("aggregate history changed: before=%+v after=%+v err=%v", aggregates, afterAggregates, err)
	}
}

func assertAssessingUnknownOnlyChangesDerivedFields(t *testing.T, before, after GradingJobView) {
	t.Helper()
	oldFields, newFields := before.Fields, after.Fields
	oldFields.FailedStage, newFields.FailedStage = "", ""
	oldFields.FailureKind, newFields.FailureKind = "", ""
	oldFields.Retryable, newFields.Retryable = false, false
	oldFields.Deadline, newFields.Deadline = 0, 0
	if !reflect.DeepEqual(oldFields, newFields) {
		t.Fatalf("unknown convergence changed immutable or successful job facts: before=%+v after=%+v", before.Fields, after.Fields)
	}
	oldRecord, newRecord := *before.Record, *after.Record
	oldRecord.Status, newRecord.Status = "", ""
	oldRecord.Fields, newRecord.Fields = "", ""
	oldRecord.UpdatedAt, newRecord.UpdatedAt = 0, 0
	oldRecord.Version, newRecord.Version = 0, 0
	if !reflect.DeepEqual(oldRecord, newRecord) {
		t.Fatalf("unknown convergence changed unrelated record metadata: before=%+v after=%+v", before.Record, after.Record)
	}
}

func TestAssessingUnknownRetryAndStartupUseCurrentReceipts(t *testing.T) {
	for _, status := range []k12.ModelInvocationStatus{k12.ModelInvocationSent, k12.ModelInvocationOutcomeUnknown} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			o, run, job := newAssessingUnknownFixture(t, "historical-assess-"+string(status))
			prepareAssessingUnknownReceipt(t, o, job, run.questions[0], k12.GradingItemOperationSolveGenerate, 1, k12.ModelInvocationSucceeded, nil)
			prepareAssessingUnknownReceipt(t, o, job, run.questions[0], k12.GradingItemOperationSolveVerify, 1, status, nil)
			aggregate, err := o.beginFrozenAssessInvocation(ctx, run, job)
			if err != nil {
				t.Fatal(err)
			}
			if err := o.markFrozenAssessInvocationFailed(ctx, aggregate, "assess_item_failed"); err != nil {
				t.Fatal(err)
			}
			failed := failAssessingUnknownFixture(t, o, job)
			before, _ := o.deps.Records.ListGradingItemInvocations(ctx, "mingming", job.Record.RecordID)
			aggregates, _ := o.deps.Records.ListModelInvocations(ctx, "mingming", job.Record.RecordID)
			assertAssessingUnknownParkedWithoutCalls(t, o, job.Record.RecordID, failed.Fields.AttemptCount, before, aggregates)
			parked, err := o.deps.GetGradingJob(ctx, "mingming", job.Record.RecordID)
			if err != nil {
				t.Fatal(err)
			}
			assertAssessingUnknownOnlyChangesDerivedFields(t, failed, parked)
			if o.deps.Solver.(*itemResumeSolver).callCount("1+1=") != 0 || o.deps.Grader.(*itemResumeGrader).callCount("1+1=") != 0 {
				t.Fatal("startup or retry sent a model call")
			}
		})
	}
}

func TestAssessingUnknownCurrentInputAndDefiniteFailureBoundaries(t *testing.T) {
	cases := []struct {
		name        string
		status      k12.ModelInvocationStatus
		revise      func(*k12.GradingItemInvocation)
		oldRevision bool
		otherJob    bool
	}{
		{name: "definite_failed", status: k12.ModelInvocationFailed},
		{name: "succeeded", status: k12.ModelInvocationSucceeded},
		{name: "not_sent", status: k12.ModelInvocationPrepared},
		{name: "old_revision", status: k12.ModelInvocationOutcomeUnknown, oldRevision: true},
		{name: "different_input_digest", status: k12.ModelInvocationOutcomeUnknown, revise: func(v *k12.GradingItemInvocation) { v.InputDigest = "other-input" }},
		{name: "other_job", status: k12.ModelInvocationOutcomeUnknown, otherJob: true},
		{name: "local_deterministic_sent", status: k12.ModelInvocationSent, revise: func(v *k12.GradingItemInvocation) { v.ExecutionKind = k12.GradingExecutionLocalDeterministic }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			o, run, job := newAssessingUnknownFixture(t, "assess-boundary-"+tc.name)
			receiptJob, receiptQ := job, run.questions[0]
			if tc.otherJob {
				otherID := runItemResumeJobToAssessing(t, o, "other-job")
				otherRun, other := confirmItemResumeJobWithoutRun(t, o, otherID)
				receiptJob, receiptQ = other, otherRun.questions[0]
			}
			prepareAssessingUnknownReceipt(t, o, receiptJob, receiptQ, k12.GradingItemOperationSolveVerify, 1, tc.status, tc.revise)
			if tc.oldRevision {
				snapshot, err := o.deps.Records.GetProblemAttemptSnapshot(ctx, "mingming", job.Fields.SubmissionID)
				if err != nil {
					t.Fatal(err)
				}
				snapshot.Problems[0].CanonicalVersion++
				snapshot.Attempts[0].ConfirmedVersion++
				if err := o.deps.Records.PutProblemAttemptSnapshot(ctx, snapshot); err != nil {
					t.Fatal(err)
				}
			}
			failed := failAssessingUnknownFixture(t, o, job)
			v, err := o.deps.RetryGradingJob(ctx, "mingming", job.Record.RecordID)
			if err != nil || v.Record.Status != k12.GradingStageQueued || v.Fields.AttemptCount != failed.Fields.AttemptCount {
				t.Fatalf("unrelated or resolved receipt blocked safe retry: view=%+v err=%v", v, err)
			}
		})
	}
}

func TestAssessingUnknownOnlyReferencedSuccessfulReplacementResolves(t *testing.T) {
	for _, referenced := range []bool{false, true} {
		t.Run(map[bool]string{false: "success_without_assessment", true: "current_assessment_replacement"}[referenced], func(t *testing.T) {
			ctx := context.Background()
			o, run, job := newAssessingUnknownFixture(t, "assess-replacement")
			q := run.questions[0]
			original := prepareAssessingUnknownReceipt(t, o, job, q, k12.GradingItemOperationSolveVerify, 1, k12.ModelInvocationOutcomeUnknown, nil)
			replacement := prepareAssessingUnknownReceipt(t, o, job, q, k12.GradingItemOperationSolveVerify, 2, k12.ModelInvocationSucceeded,
				func(v *k12.GradingItemInvocation) { v.RequestDigest = "new-binary-request-digest" })
			if referenced {
				grade := prepareAssessingUnknownReceipt(t, o, job, q, k12.GradingItemOperationGrade, 1, k12.ModelInvocationSucceeded, nil)
				_, _, err := o.deps.Records.CommitGradingAssessmentItem(ctx, k12.GradingAssessmentItem{
					AgentName: "mingming", JobID: job.Record.RecordID, ProblemID: q.ProblemID, AttemptID: q.AttemptID,
					ConfirmedVersion: q.ConfirmedVersion, InputRevision: q.ConfirmedVersion, InputDigest: q.InputDigest,
					Status: k12.GradingAssessmentCorrect, ResultJSON: `{"status":"correct"}`, ResultDigest: "current-assessment-result",
					SolveInvocationID: replacement.InvocationID, GradeInvocationID: grade.InvocationID,
					ProjectionStatus: k12.GradingProjectionCommitted,
				}, k12storage.GradingAssessmentEffects{})
				if err != nil {
					t.Fatal(err)
				}
			}
			failed := failAssessingUnknownFixture(t, o, job)
			before, err := o.deps.Records.ListGradingItemInvocations(ctx, "mingming", job.Record.RecordID)
			if err != nil {
				t.Fatal(err)
			}
			assessments, err := o.deps.Records.ListGradingAssessmentItems(ctx, "mingming", job.Record.RecordID)
			if err != nil {
				t.Fatal(err)
			}
			v, err := o.deps.RetryGradingJob(ctx, "mingming", job.Record.RecordID)
			if referenced {
				if err != nil || v.Record.Status != k12.GradingStageQueued {
					t.Fatalf("referenced current success did not resolve unknown: %+v %v", v, err)
				}
			} else {
				if err == nil {
					t.Fatal("changed request digest and unreferenced success bypassed unknown")
				}
				v, err = o.deps.GetGradingJob(ctx, "mingming", job.Record.RecordID)
				if err != nil || v.Record.Status != k12.GradingStageOutcomeUnknown || v.Fields.AttemptCount != failed.Fields.AttemptCount {
					t.Fatalf("unresolved projection=%+v err=%v", v, err)
				}
			}
			stored, err := o.deps.Records.GetGradingItemInvocation(ctx, "mingming", original.InvocationID)
			if err != nil || !reflect.DeepEqual(original, stored) {
				t.Fatalf("original unknown overwritten: %+v err=%v", stored, err)
			}
			after, err := o.deps.Records.ListGradingItemInvocations(ctx, "mingming", job.Record.RecordID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("successful replacement or call history changed: %+v err=%v", after, err)
			}
			afterAssessments, err := o.deps.Records.ListGradingAssessmentItems(ctx, "mingming", job.Record.RecordID)
			if err != nil || !reflect.DeepEqual(assessments, afterAssessments) {
				t.Fatalf("successful assessment changed: %+v err=%v", afterAssessments, err)
			}
		})
	}
}

func TestAssessingUnknownFailedAggregateCannotIncrementAttemptOnResume(t *testing.T) {
	ctx := context.Background()
	o, run, job := newAssessingUnknownFixture(t, "assess-failed-aggregate")
	prepareAssessingUnknownReceipt(t, o, job, run.questions[0], k12.GradingItemOperationSolveVerify, 1, k12.ModelInvocationOutcomeUnknown, nil)
	aggregate, err := o.beginFrozenAssessInvocation(ctx, run, job)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.markFrozenAssessInvocationFailed(ctx, aggregate, "assess_item_failed"); err != nil {
		t.Fatal(err)
	}
	v, err := o.runAssessItems(ctx, run, job)
	if !errors.Is(err, ErrModelInvocationRequiresReconciliation) || v.Record.Status != k12.GradingStageOutcomeUnknown || v.Fields.AttemptCount != job.Fields.AttemptCount {
		t.Fatalf("failed aggregate resumed as ordinary failure: %+v err=%v", v, err)
	}
	if o.deps.Solver.(*itemResumeSolver).callCount("1+1=") != 0 {
		t.Fatal("aggregate recovery made a model call")
	}
}
