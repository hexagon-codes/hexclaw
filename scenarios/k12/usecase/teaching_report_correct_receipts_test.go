package usecase

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestCorrectReportRestoresOnlyTheVerifiedLocalSolution(t *testing.T) {
	o := newParallelAnchorOrchestrator(t, &countingRecognizer{questions: []RecognizedQuestion{{
		Question: "x-1/4x=18", Subject: "数学", StudentAnswer: "x=24", AnswerState: AnswerStatePresent,
	}}}, nil, WithGradingRunDir(t.TempDir()))
	jobID := runItemResumeJobToAssessing(t, o, "correct-local-report")
	run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
	q := run.questions[0]
	ctx := t.Context()
	store := o.deps.Records
	evidence := SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec, SolverOutputDigest: "original-solution", VerificationInputDigest: "original-input", VerificationRunID: "execution-1"}
	solved := SolveHomeworkResult{Solution: "## 解答\n合并得 3/4x=18。\n\n验算：24-6=18。\n\n## 答案\nx=24", Evidence: evidence}
	save := func(op k12.GradingItemOperation, payload any) string {
		t.Helper()
		item := k12.GradingItemInvocation{InvocationID: string(op), AgentName: job.Record.AgentName, JobID: jobID,
			ProblemID: q.ProblemID, AttemptID: q.AttemptID, InputRevision: q.ConfirmedVersion, InputDigest: q.InputDigest,
			Operation: op, OperationAttempt: 1, ExecutionKind: k12.GradingExecutionLocalDeterministic, RequestDigest: string(op), RouteSnapshot: job.Fields.ModelSnapshot}
		if _, _, err := store.PrepareGradingItemInvocation(ctx, item); err != nil {
			t.Fatal(err)
		}
		if _, err := store.MarkGradingItemInvocationSent(ctx, item.AgentName, item.InvocationID); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.MarkGradingItemInvocationSucceeded(ctx, item.AgentName, item.InvocationID, modelInvocationDigest(raw), string(raw)); err != nil {
			t.Fatal(err)
		}
		return item.InvocationID
	}
	assessment := k12.GradingAssessmentItem{Status: k12.GradingAssessmentCorrect, SolveInvocationID: save(k12.GradingItemOperationSolve, solved)}
	o.deps.Solver = nil
	if _, _, _, err := o.restoreTeachingReportGuide(ctx, job, q, assessment, solved); !errors.Is(err, ErrTeachingReportContentRequired) {
		t.Fatalf("missing physical verification was accepted: %v", err)
	}
	verified := save(k12.GradingItemOperationSolveVerify, `{"execution_receipt":{"input_digest":"original-input","run_id":"execution-1","status":"success","exit_code":0,"stdout_bytes":2,"stdout":"24"}}`)
	guide, digest, receipt, err := o.restoreTeachingReportGuide(ctx, job, q, assessment, solved)
	if err != nil || guide.ReportContent == nil || digest != evidence.SolverOutputDigest || receipt != verified {
		t.Fatalf("local solution did not restore: guide=%+v digest=%q receipt=%q err=%v", guide, digest, receipt, err)
	}
	if guide.ReportContent.Items[0].Answers[0].Text != "x=24" || guide.ReportContent.Sections[0].Blocks[1].Text != "验算：24-6=18。" {
		t.Fatalf("solution and check were not preserved: %+v", guide.ReportContent)
	}
	solved.Solution = strings.ReplaceAll(solved.Solution, "24", "25")
	if _, _, _, err := o.restoreTeachingReportGuide(ctx, job, q, assessment, solved); !errors.Is(err, ErrTeachingReportContentRequired) {
		t.Fatalf("stored receipt was used for a different answer: %v", err)
	}
}
