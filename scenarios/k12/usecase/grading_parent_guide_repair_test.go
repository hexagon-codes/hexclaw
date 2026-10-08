package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type parentGuideRepairGenerator struct {
	requests []ParentTeachingGuideRequest
	after    func()
}

func (g *parentGuideRepairGenerator) GenerateParentTeachingGuide(_ context.Context, req ParentTeachingGuideRequest) (ParentTeachingGuide, error) {
	g.requests = append(g.requests, req)
	if g.after != nil {
		g.after()
	}
	return solveTeachingNumericGuide(), nil
}

type parentGuideRepairAuditor struct {
	requests []ParentTeachingGuideAuditRequest
	failure  error
	scope    string
	invalid  bool
	missing  bool
}

func (a *parentGuideRepairAuditor) AuditParentTeachingGuide(_ context.Context, req ParentTeachingGuideAuditRequest) (ParentTeachingGuideAudit, error) {
	a.requests = append(a.requests, req)
	if a.failure != nil {
		return ParentTeachingGuideAudit{}, a.failure
	}
	scope := a.scope
	if scope == "" {
		scope = "IN_SCOPE"
	}
	var fields []map[string]any
	for _, field := range []string{"answer", "full_solution_steps", "grade_level_method", "likely_mistakes", "parent_teaching_sequence", "follow_up_questions", "checking_method"} {
		if a.missing && field == "checking_method" {
			continue
		}
		fields = append(fields, map[string]any{
			"field": field, "valid": !(a.invalid && field == "likely_mistakes"),
			"reason": "Four halves total two sheets; the supplied statement was checked against that calculation.",
		})
	}
	raw, err := json.Marshal([]map[string]any{{"source_digest": req.SourceSolutionDigest, "scope": scope, "fields": fields}})
	if err != nil {
		return ParentTeachingGuideAudit{}, err
	}
	output := "PARENT_GUIDE_AUDITS: " + string(raw)
	return ParentTeachingGuideAudit{SourceSolutionDigest: req.SourceSolutionDigest,
		Verdict: engine.ParentGuideAuditVerdict(output, req.SourceSolutionDigest), Output: output}, nil
}

func newParentGuideRepairFixture(t *testing.T, count int) (*GradingOrchestrator, *gradingRun, GradingJobView, *solveTeachingPhysicalSolver) {
	t.Helper()
	solver := newSolveTeachingSolver()
	solver.numeric = true
	if count == 1 {
		solver.audit = "INVALID"
		o, run, job := newSolveTeachingJob(t, solver, false)
		return o, run, job, solver
	}
	questions := make([]RecognizedQuestion, count)
	for i := range questions {
		questions[i] = RecognizedQuestion{Question: fmt.Sprintf("第%d题：每朵纸花用1/2张纸，做4朵纸花一共需要多少张纸？", i+1),
			Subject: "数学", AnswerState: AnswerStateBlank, KnowledgePoints: []string{"分数乘整数"}}
	}
	o := newItemResumeOrchestrator(t, t.TempDir(), questions,
		&itemResumeSolver{calls: map[string]int{}}, &itemResumeGrader{calls: map[string]int{}})
	o.deps.Solver = solver
	req := orchestratorPhotoRequest()
	req.Subject, req.TaskIntent = "数学", PhotoTaskBlankWorksheet
	job, created, err := o.StartPhotoGradingJob(context.Background(), StartPhotoGradingInput{Photo: req, SourceKind: "im", SourceKey: t.Name()})
	if err != nil || !created {
		t.Fatalf("start guide repair job: created=%t err=%v", created, err)
	}
	freezeItemResumeBudget(t, o, job.Record.RecordID)
	if view, err := o.RunGradingJob(context.Background(), job.Record.RecordID); err != nil || view.Record.Status != k12.GradingStageAwaitingConfirmation {
		t.Fatalf("recognize guide repair job: stage=%s err=%v", view.Record.Status, err)
	}
	waitGradingView(t, o, job.Record.RecordID, func(v GradingJobView) bool {
		return v.Fields.AnchorState == k12.GradingAnchorLocated || v.Fields.AnchorState == k12.GradingAnchorDegraded
	})
	run, job := confirmItemResumeJobWithoutRun(t, o, job.Record.RecordID)
	for i, question := range run.questions {
		if i == len(run.questions)-1 {
			solver.audit = "INVALID"
			break
		}
		item, err := o.assessDurablePhotoItem(context.Background(), o.deps, job, run.req, PhotoModeSolve, question)
		if err != nil || item.Status != PhotoBlankSolved {
			t.Fatalf("successful preceding item %d: item=%+v err=%v", i, item, err)
		}
	}
	return o, run, job, solver
}

func failParentGuideRepairFixture(t *testing.T, o *GradingOrchestrator, run *gradingRun, job GradingJobView) GradingJobView {
	t.Helper()
	q := run.questions[len(run.questions)-1]
	item, err := o.assessDurablePhotoItem(context.Background(), o.deps, job, run.req, PhotoModeSolve, q)
	if !errors.Is(err, ErrSolveFailed) || item.ParentGuide != nil {
		t.Fatalf("original invalid guide accepted: item=%+v err=%v", item, err)
	}
	failed, err := o.deps.AdvanceGradingStage(context.Background(), job.Record.AgentName, job.Record.RecordID,
		AdvanceGradingInput{Outcome: GradingOutcomeFailed, FailureKind: "assess_item_failed", Retryable: true})
	if err != nil || failed.Record.Status != k12.GradingStageFailedRetryable {
		t.Fatalf("save explicit guide failure: stage=%s err=%v", failed.Record.Status, err)
	}
	return failed
}

func retryParentGuideRepairFixture(t *testing.T, o *GradingOrchestrator, job GradingJobView) GradingJobView {
	t.Helper()
	if _, err := o.deps.RetryGradingJob(context.Background(), job.Record.AgentName, job.Record.RecordID); err != nil {
		t.Fatalf("normal guide retry: %v", err)
	}
	resumed, err := o.deps.AdvanceGradingStage(context.Background(), job.Record.AgentName, job.Record.RecordID,
		AdvanceGradingInput{Outcome: GradingOutcomeOK})
	if err != nil || resumed.Record.Status != k12.GradingStageAssessing {
		t.Fatalf("resume assessing: stage=%s err=%v", resumed.Record.Status, err)
	}
	return resumed
}

func assertParentGuideRepairHistory(t *testing.T, before, after []k12.GradingItemInvocation) {
	t.Helper()
	byID := make(map[string]k12.GradingItemInvocation, len(after))
	for _, row := range after {
		byID[row.InvocationID] = row
	}
	for _, row := range before {
		if !reflect.DeepEqual(row, byID[row.InvocationID]) {
			t.Fatalf("old invocation was rewritten: id=%s before=%+v after=%+v", row.InvocationID, row, byID[row.InvocationID])
		}
	}
}

func TestFailedParentGuideRepairOnlyMissingTeaching(t *testing.T) {
	ctx := context.Background()
	o, run, job, solver := newParentGuideRepairFixture(t, 16)
	failed := failParentGuideRepairFixture(t, o, run, job)
	before := gradingVerifierResponseRows(t, o, job)
	assessments, err := o.deps.Records.ListEffectiveGradingAssessments(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil || len(assessments) != 15 {
		t.Fatalf("preceding assessments=%d err=%v", len(assessments), err)
	}
	generator, auditor := &parentGuideRepairGenerator{}, &parentGuideRepairAuditor{}
	o.deps.ParentTeachingGuide, o.deps.ParentTeachingGuideAudit = generator, auditor
	resumed := retryParentGuideRepairFixture(t, o, failed)
	q := run.questions[15]
	item, err := o.assessDurablePhotoItem(ctx, o.deps, resumed, run.req, PhotoModeSolve, q)
	if err != nil || item.Status != PhotoBlankSolved || item.ParentGuide == nil || !reflect.DeepEqual(*item.ParentGuide, solveTeachingNumericGuide()) {
		t.Fatalf("repaired guide assessment: item=%+v err=%v", item, err)
	}
	if len(generator.requests) != 1 || len(auditor.requests) != 1 ||
		generator.requests[0].FrozenTeachingContract != run.req.ParentTeachingContract || generator.requests[0].RejectedCandidate == nil ||
		auditor.requests[0].Request.VerifiedSolution != item.Solve.Solution || !reflect.DeepEqual(auditor.requests[0].Guide, *item.ParentGuide) {
		t.Fatalf("repair did not freeze complete guide and mathematics: generated=%+v audited=%+v", generator.requests, auditor.requests)
	}
	after := gradingVerifierResponseRows(t, o, resumed)
	assertParentGuideRepairHistory(t, before, after)
	if len(after) != len(before)+3 || !reflect.DeepEqual(solveTeachingMathRows(before), solveTeachingMathRows(after)) ||
		solver.count(k12.GradingItemOperationSolveGenerate) != 16 || solver.count(k12.GradingItemOperationSolveVerify) != 16 {
		t.Fatalf("repair resent mathematics or did not retain separate phases: before=%d after=%d", len(before), len(after))
	}
	for _, old := range assessments {
		current, err := o.deps.Records.GetEffectiveGradingAssessment(ctx, job.Record.AgentName, job.Record.RecordID, old.ProblemID)
		if err != nil || !reflect.DeepEqual(old, current.Current) {
			t.Fatalf("preceding successful assessment changed: problem=%s err=%v", old.ProblemID, err)
		}
	}
	for _, question := range run.questions {
		if _, err := o.assessDurablePhotoItem(ctx, o.deps, resumed, run.req, PhotoModeSolve, question); err != nil {
			t.Fatalf("completed assessment replay: %v", err)
		}
	}
	if len(generator.requests) != 1 || len(auditor.requests) != 1 || !reflect.DeepEqual(after, gradingVerifierResponseRows(t, o, resumed)) {
		t.Fatal("completed guide replay sent or rewrote a phase")
	}
	all, err := o.deps.Records.ListEffectiveGradingAssessments(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil || len(all) != 16 {
		t.Fatalf("complete assessments=%d err=%v", len(all), err)
	}
	for _, changed := range []string{"input_revision", "input_digest", "solution_digest"} {
		t.Run(changed, func(t *testing.T) {
			base, baseRun, baseJob, _ := newParentGuideRepairFixture(t, 1)
			baseFailed := failParentGuideRepairFixture(t, base, baseRun, baseJob)
			baseJob = retryParentGuideRepairFixture(t, base, baseFailed)
			question := baseRun.questions[0]
			req := photoItemGradeRequest(baseRun.req, question)
			req.StudentAnswer = ""
			solved, _, err := executeDurableSolveOperation(ctx, base, base.deps, baseJob, question, req)
			if err != nil {
				t.Fatal(err)
			}
			if changed == "input_revision" {
				question.ConfirmedVersion++
			} else if changed == "input_digest" {
				question.InputDigest = "changed-input"
			} else {
				solved.Evidence.SolverOutputDigest = "changed-solution"
			}
			original := struct {
				ExecutionKind k12.GradingExecutionKind   `json:"execution_kind"`
				InputDigest   string                     `json:"input_digest"`
				Request       ParentTeachingGuideRequest `json:"request"`
				Generation    *SolveGeneration           `json:"generation,omitempty"`
			}{k12.GradingExecutionLocalDeterministic, question.InputDigest, base.deps.parentTeachingGuideRequest(ctx, req, solved, GradeOutcome{}), solved.Generation}
			gen, audit := &parentGuideRepairGenerator{}, &parentGuideRepairAuditor{}
			base.deps.ParentTeachingGuide, base.deps.ParentTeachingGuideAudit = gen, audit
			_, _, selected, repairErr := executeDurableParentTeachingGuideRepair(ctx, base, base.deps, baseJob, question, req, solved, original)
			if selected || len(gen.requests) != 0 || len(audit.requests) != 0 || (changed == "input_revision" && repairErr == nil) {
				t.Fatalf("changed source entered repair: selected=%t err=%v", selected, repairErr)
			}
		})
	}
}

func TestFailedParentGuideRepairDurableRestartReusesGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	o, run, job, solver := newParentGuideRepairFixture(t, 1)
	failed := failParentGuideRepairFixture(t, o, run, job)
	before := gradingVerifierResponseRows(t, o, job)
	generator, auditor := &parentGuideRepairGenerator{after: cancel}, &parentGuideRepairAuditor{}
	o.deps.ParentTeachingGuide, o.deps.ParentTeachingGuideAudit = generator, auditor
	resumed := retryParentGuideRepairFixture(t, o, failed)
	q := run.questions[0]
	if _, err := o.assessDurablePhotoItem(ctx, o.deps, resumed, run.req, PhotoModeSolve, q); !errors.Is(err, context.Canceled) {
		t.Fatalf("interruption boundary: %v", err)
	}
	interrupted := gradingVerifierResponseRows(t, o, resumed)
	assertParentGuideRepairHistory(t, before, interrupted)
	if len(interrupted) != len(before)+1 || len(generator.requests) != 1 || len(auditor.requests) != 0 {
		t.Fatalf("generation was not durable before audit: rows=%d generated=%d audited=%d", len(interrupted), len(generator.requests), len(auditor.requests))
	}
	restarted := trackGradingOrchestrator(t, NewGradingOrchestrator(o.deps, orchestratorSnapshotResolver, WithGradingRunDir(o.runDir)))
	restored, err := restarted.ensureRun(context.Background(), job.Record.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	item, err := restarted.assessDurablePhotoItem(context.Background(), restarted.deps, resumed, restored.req, PhotoModeSolve, q)
	if err != nil || item.ParentGuide == nil || item.Status != PhotoBlankSolved {
		t.Fatalf("restart did not finish guide: item=%+v err=%v", item, err)
	}
	after := gradingVerifierResponseRows(t, restarted, resumed)
	assertParentGuideRepairHistory(t, interrupted, after)
	if len(after) != len(before)+3 || len(generator.requests) != 1 || len(auditor.requests) != 1 ||
		solver.count(k12.GradingItemOperationSolveGenerate) != 1 || solver.count(k12.GradingItemOperationSolveVerify) != 1 ||
		!reflect.DeepEqual(auditor.requests[0].Guide, *item.ParentGuide) {
		t.Fatal("restart resent generated guide or original mathematics, or audited a different guide")
	}
	if _, err := restarted.assessDurablePhotoItem(context.Background(), restarted.deps, resumed, restored.req, PhotoModeSolve, q); err != nil ||
		!reflect.DeepEqual(after, gradingVerifierResponseRows(t, restarted, resumed)) || len(auditor.requests) != 1 {
		t.Fatalf("restart completion replay: %v", err)
	}
}

func TestFailedParentGuideRepairRejectsInvalidAndUnknown(t *testing.T) {
	for _, failure := range []string{"invalid", "out_of_scope", "missing", "unknown"} {
		t.Run(failure, func(t *testing.T) {
			o, run, job, solver := newParentGuideRepairFixture(t, 1)
			failed := failParentGuideRepairFixture(t, o, run, job)
			before := gradingVerifierResponseRows(t, o, job)
			generator, auditor := &parentGuideRepairGenerator{}, &parentGuideRepairAuditor{}
			if failure == "invalid" {
				auditor.invalid = true
			} else if failure == "out_of_scope" {
				auditor.scope = "OUT_OF_SCOPE"
			} else if failure == "missing" {
				auditor.missing = true
			} else {
				auditor.failure = io.ErrUnexpectedEOF
			}
			o.deps.ParentTeachingGuide, o.deps.ParentTeachingGuideAudit = generator, auditor
			resumed := retryParentGuideRepairFixture(t, o, failed)
			q := run.questions[0]
			item, err := o.assessDurablePhotoItem(context.Background(), o.deps, resumed, run.req, PhotoModeSolve, q)
			if err == nil || item.ParentGuide != nil || item.Status == PhotoBlankSolved {
				t.Fatalf("invalid or unknown guide was accepted: item=%+v err=%v", item, err)
			}
			after := gradingVerifierResponseRows(t, o, resumed)
			assertParentGuideRepairHistory(t, before, after)
			if len(generator.requests) != 1 || len(auditor.requests) != 1 || solver.count(k12.GradingItemOperationSolveGenerate) != 1 || solver.count(k12.GradingItemOperationSolveVerify) != 1 {
				t.Fatal("invalid or unknown path sent extra calls")
			}
			if failure == "unknown" {
				if !errors.Is(err, ErrGradingPhysicalCallOutcomeUnknown) || len(after) != len(before)+2 {
					t.Fatalf("audit unknown did not stop before local finalization: %v", err)
				}
				var unknown int
				for _, row := range after {
					if row.Operation == k12.GradingItemOperationParentGuide && row.Status == k12.ModelInvocationOutcomeUnknown {
						unknown++
					}
				}
				if unknown != 1 {
					t.Fatalf("unknown audit receipts=%d", unknown)
				}
				failedAgain, err := o.deps.AdvanceGradingStage(context.Background(), job.Record.AgentName, job.Record.RecordID,
					AdvanceGradingInput{Outcome: GradingOutcomeFailed, FailureKind: "assess_item_failed", Retryable: true})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := o.deps.RetryGradingJob(context.Background(), job.Record.AgentName, job.Record.RecordID); !errors.Is(err, records.ErrIllegalTransition) {
					t.Fatalf("ordinary retry accepted unknown guide: %v", err)
				}
				current, err := o.deps.GetGradingJob(context.Background(), failedAgain.Record.AgentName, failedAgain.Record.RecordID)
				if err != nil || current.Record.Status != k12.GradingStageOutcomeUnknown || current.Fields.Retryable {
					t.Fatalf("unknown top-level projection: status=%s err=%v", current.Record.Status, err)
				}
			} else if !errors.Is(err, ErrSolveFailed) || len(after) != len(before)+3 {
				t.Fatalf("complete invalid audit did not produce explicit failure: %v", err)
			}
			if _, err := o.assessDurablePhotoItem(context.Background(), o.deps, resumed, run.req, PhotoModeSolve, q); err == nil ||
				len(generator.requests) != 1 || len(auditor.requests) != 1 || !reflect.DeepEqual(after, gradingVerifierResponseRows(t, o, resumed)) {
				t.Fatalf("failed or unknown repair replay sent or changed a phase: %v", err)
			}
		})
	}
}
