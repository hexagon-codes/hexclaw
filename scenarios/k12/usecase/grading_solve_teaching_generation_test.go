package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type solveTeachingPhysicalSolver struct {
	grading20260726PhysicalSolver
	contract    string
	freezeCalls int
	audit       string
	missing     bool
	incomplete  bool
	verifyErr   error
	numeric     bool
}

func (s *solveTeachingPhysicalSolver) FreezeSolveTeachingContract(context.Context, string, string) (string, error) {
	s.freezeCalls++
	return s.contract, nil
}

func solveTeachingTestGuide() ParentTeachingGuide {
	return ParentTeachingGuide{
		Answer: "2", FullSolutionSteps: []string{"先数出两个单位。"},
		GradeLevelMethod:       "用本年级的分数单位解释重复相加。",
		LikelyMistakes:         []string{"把分母也乘了整数。"},
		ParentTeachingSequence: []string{"先说分数单位，再数重复次数。"},
		FollowUpQuestions:      []string{"分数单位为什么没有改变？"},
		CheckingMethod:         "把相同分数逐项相加，核对每一步。",
	}
}

func (s *solveTeachingPhysicalSolver) Solve(ctx context.Context, problem, _, _ string) (SolveResult, error) {
	version, contract := SolveTeachingRequest(ctx)
	generated, err := grading20260726PhysicalCall(ctx, k12.GradingItemOperationSolveGenerate,
		problem+"\x00"+version+"\x00"+contract, 0, func() (SolveResult, error) {
			solution := "## 完整方法\n先数出两个单位。\n## 答案\n**2**"
			digest := sha256.Sum256([]byte(solution))
			result := SolveResult{Solution: solution, Evidence: SolveEvidence{
				Verdict: VerdictUnverifiable, EvidenceType: EvidenceNone,
				SolverOutputDigest: hex.EncodeToString(digest[:]),
			}}
			if s.numeric {
				result.Evidence.Verdict, result.Evidence.EvidenceType = VerdictAgree, EvidenceNumericExec
			}
			if version != "" && !s.missing {
				guide := solveTeachingTestGuide()
				if s.incomplete {
					guide.FullSolutionSteps = nil
				}
				result.Generation = &SolveGeneration{
					OutputVersion: version, ParentGuideCandidate: &guide,
					SourceSolutionDigest: result.Evidence.SolverOutputDigest,
				}
			}
			return result, nil
		}, s)
	if err != nil {
		return SolveResult{}, err
	}
	return grading20260726PhysicalCall(ctx, k12.GradingItemOperationSolveVerify,
		problem+"\x00"+generated.Solution, 0, func() (SolveResult, error) {
			if s.verifyErr != nil {
				return SolveResult{}, s.verifyErr
			}
			if generated.Generation != nil {
				generated.Generation.GuideAudit = s.audit
			}
			return generated, nil
		}, s)
}

func newSolveTeachingJob(t *testing.T, solver *solveTeachingPhysicalSolver, legacy bool) (*GradingOrchestrator, *gradingRun, GradingJobView) {
	t.Helper()
	question := RecognizedQuestion{
		Question: "讨论一下：分数乘整数，怎样计算？", Subject: "数学", AnswerState: AnswerStateBlank,
		KnowledgePoints: []string{"分数乘整数"},
	}
	if !legacy {
		question.StudentAnswer, question.AnswerState = "1", AnswerStatePresent
	}
	o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{question},
		&itemResumeSolver{calls: map[string]int{}}, &itemResumeGrader{calls: map[string]int{}})
	o.deps.Solver = solver
	req := orchestratorPhotoRequest()
	req.Subject, req.TaskIntent = "数学", PhotoTaskBlankWorksheet
	if legacy {
		req.TaskIntent = ""
	}
	job, created, err := o.StartPhotoGradingJob(context.Background(), StartPhotoGradingInput{
		Photo: req, SourceKind: "im", SourceKey: t.Name(),
	})
	if err != nil || !created {
		t.Fatalf("start teaching job: created=%t err=%v", created, err)
	}
	jobID := job.Record.RecordID
	freezeItemResumeBudget(t, o, jobID)
	if view, err := o.RunGradingJob(context.Background(), jobID); err != nil || view.Record.Status != k12.GradingStageAwaitingConfirmation {
		t.Fatalf("recognize teaching job: stage=%s err=%v", view.Record.Status, err)
	}
	waitGradingView(t, o, jobID, func(v GradingJobView) bool {
		return v.Fields.AnchorState == k12.GradingAnchorLocated || v.Fields.AnchorState == k12.GradingAnchorDegraded
	})
	run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
	return o, run, job
}

func newSolveTeachingSolver() *solveTeachingPhysicalSolver {
	return &solveTeachingPhysicalSolver{
		grading20260726PhysicalSolver: grading20260726PhysicalSolver{calls: map[k12.GradingItemOperation]int{}},
		contract:                      "frozen pedagogy and grade methods", audit: "VALID",
	}
}

func solveTeachingMathRows(rows []k12.GradingItemInvocation) []k12.GradingItemInvocation {
	var mathRows []k12.GradingItemInvocation
	for _, row := range rows {
		if row.Operation != k12.GradingItemOperationParentGuide {
			mathRows = append(mathRows, row)
		}
	}
	return mathRows
}

func solveTeachingRow(rows []k12.GradingItemInvocation, operation k12.GradingItemOperation) *k12.GradingItemInvocation {
	for i := range rows {
		if rows[i].Operation == operation {
			return &rows[i]
		}
	}
	return nil
}

func TestSolveTeachingLocalGuideCrashReplay(t *testing.T) {
	solver := newSolveTeachingSolver()
	o, run, job := newSolveTeachingJob(t, solver, false)
	if run.req.SolveOutputVersion != SolveOutputWithParentGuideV1 || solver.freezeCalls != 1 {
		t.Fatalf("new solve version not frozen: request=%+v freezes=%d", run.req, solver.freezeCalls)
	}
	question := run.questions[0]
	req := photoItemGradeRequest(run.req, question)
	solved, _, err := executeDurableSolveOperation(context.Background(), o, o.deps, job, question, req)
	if err != nil || solved.Generation == nil || solved.Generation.GuideAudit != "VALID" {
		t.Fatalf("solve checkpoint: result=%+v err=%v", solved, err)
	}
	before := gradingVerifierResponseRows(t, o, job)
	if len(before) != 2 {
		t.Fatalf("math checkpoints=%+v", before)
	}
	// 重建运行时模拟数学回执提交后崩溃，当前 Skill 改动不能改变冻结合同。
	solver.contract = "changed current pedagogy"
	restarted := trackGradingOrchestrator(t, NewGradingOrchestrator(o.deps, orchestratorSnapshotResolver, WithGradingRunDir(o.runDir)))
	restored, err := restarted.ensureRun(context.Background(), job.Record.RecordID)
	if err != nil || restored.req.ParentTeachingContract != "frozen pedagogy and grade methods" || solver.freezeCalls != 1 {
		t.Fatalf("frozen run restore: run=%+v freezes=%d err=%v", restored, solver.freezeCalls, err)
	}
	item, err := restarted.assessDurablePhotoItem(context.Background(), restarted.deps, job, restored.req, PhotoModeSolve, question)
	if err != nil || item.Status != PhotoBlankSolved || item.ParentGuide == nil {
		t.Fatalf("guide commit: item=%+v err=%v", item, err)
	}
	if !reflect.DeepEqual(item.ParentGuide.FullSolutionSteps, []string{"先数出两个单位。"}) ||
		item.Solve.Evidence.EvidenceType != EvidenceNone || item.Warning == "" || item.Recognized.StudentAnswer != "1" {
		t.Fatalf("guide rewrote solution or fabricated verification: %+v", item)
	}
	after := gradingVerifierResponseRows(t, restarted, job)
	guideRow := solveTeachingRow(after, k12.GradingItemOperationParentGuide)
	if len(after) != 3 || !reflect.DeepEqual(before, solveTeachingMathRows(after)) || guideRow == nil ||
		guideRow.ExecutionKind != k12.GradingExecutionLocalDeterministic || guideRow.Status != k12.ModelInvocationSucceeded {
		t.Fatalf("local guide receipts: before=%+v after=%+v", before, after)
	}
	if solver.count(k12.GradingItemOperationSolveGenerate) != 1 || solver.count(k12.GradingItemOperationSolveVerify) != 1 ||
		len(o.deps.ParentTeachingGuide.(*parentTeachingGuideSpy).snapshot()) != 0 {
		t.Fatal("crash replay sent another solver/verifier/guide call")
	}
	replayed, err := restarted.assessDurablePhotoItem(context.Background(), restarted.deps, job, restored.req, PhotoModeSolve, question)
	if err != nil || !reflect.DeepEqual(item.ParentGuide, replayed.ParentGuide) ||
		!reflect.DeepEqual(after, gradingVerifierResponseRows(t, restarted, job)) {
		t.Fatalf("committed assessment replay changed guide or receipts: item=%+v err=%v", replayed, err)
	}
}

func TestSolveTeachingFailedAuditKeepsMathReceipts(t *testing.T) {
	for _, failure := range []string{"invalid", "missing", "incomplete"} {
		t.Run(failure, func(t *testing.T) {
			solver := newSolveTeachingSolver()
			if failure == "invalid" {
				solver.audit = "INVALID"
			}
			solver.missing, solver.incomplete = failure == "missing", failure == "incomplete"
			o, run, job := newSolveTeachingJob(t, solver, false)
			question := run.questions[0]
			_, _, err := executeDurableSolveOperation(context.Background(), o, o.deps, job, question, photoItemGradeRequest(run.req, question))
			if err != nil {
				t.Fatal(err)
			}
			before := gradingVerifierResponseRows(t, o, job)
			item, err := o.assessDurablePhotoItem(context.Background(), o.deps, job, run.req, PhotoModeSolve, question)
			if !errors.Is(err, ErrSolveFailed) || item.ParentGuide != nil || item.Status == PhotoBlankSolved {
				t.Fatalf("unaudited guide accepted: item=%+v err=%v", item, err)
			}
			after := gradingVerifierResponseRows(t, o, job)
			guideRow := solveTeachingRow(after, k12.GradingItemOperationParentGuide)
			if len(after) != 3 || !reflect.DeepEqual(before, solveTeachingMathRows(after)) || guideRow == nil || guideRow.Status != k12.ModelInvocationFailed ||
				guideRow.ExecutionKind != k12.GradingExecutionLocalDeterministic || guideRow.FailureClass != "local_execution" {
				t.Fatalf("guide failure changed successful math receipts: before=%+v after=%+v", before, after)
			}
			_, err = o.assessDurablePhotoItem(context.Background(), o.deps, job, run.req, PhotoModeSolve, question)
			if !errors.Is(err, ErrGradingItemInvocationFailed) || !reflect.DeepEqual(after, gradingVerifierResponseRows(t, o, job)) ||
				len(o.deps.ParentTeachingGuide.(*parentTeachingGuideSpy).snapshot()) != 0 {
				t.Fatalf("guide failure replay resent or changed receipts: %v", err)
			}
		})
	}
}

func TestSolveTeachingUnknownVerifyDoesNotSendGuideOrRetry(t *testing.T) {
	solver := newSolveTeachingSolver()
	solver.verifyErr = io.ErrUnexpectedEOF
	o, run, job := newSolveTeachingJob(t, solver, false)
	for attempt := 0; attempt < 2; attempt++ {
		_, err := o.assessDurablePhotoItem(context.Background(), o.deps, job, run.req, PhotoModeSolve, run.questions[0])
		if !errors.Is(err, ErrModelInvocationRequiresReconciliation) && !errors.Is(err, ErrGradingPhysicalCallOutcomeUnknown) {
			t.Fatalf("unknown verify bypassed reconciliation: %v", err)
		}
	}
	rows := gradingVerifierResponseRows(t, o, job)
	generated := solveTeachingRow(rows, k12.GradingItemOperationSolveGenerate)
	verified := solveTeachingRow(rows, k12.GradingItemOperationSolveVerify)
	if len(rows) != 2 || generated == nil || verified == nil || generated.Status != k12.ModelInvocationSucceeded || verified.Status != k12.ModelInvocationOutcomeUnknown ||
		solver.count(k12.GradingItemOperationSolveGenerate) != 1 || solver.count(k12.GradingItemOperationSolveVerify) != 1 ||
		len(o.deps.ParentTeachingGuide.(*parentTeachingGuideSpy).snapshot()) != 0 {
		t.Fatalf("unknown verify was resent or guide was sent: %+v", rows)
	}
}

func TestSolveTeachingLegacyAndNumericGuideRoutes(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		name := "numeric_priority"
		if legacy {
			name = "legacy_provider"
		}
		t.Run(name, func(t *testing.T) {
			solver := newSolveTeachingSolver()
			solver.numeric, solver.missing = !legacy, true
			o, run, job := newSolveTeachingJob(t, solver, legacy)
			item, err := o.assessDurablePhotoItem(context.Background(), o.deps, job, run.req, PhotoModeSolve, run.questions[0])
			if err != nil || item.ParentGuide == nil {
				t.Fatalf("guide route: item=%+v err=%v", item, err)
			}
			rows := gradingVerifierResponseRows(t, o, job)
			wantKind, wantCalls := k12.GradingExecutionLocalDeterministic, 0
			if legacy {
				wantKind, wantCalls = k12.GradingExecutionProvider, 1
				if solver.freezeCalls != 0 || run.req.SolveOutputVersion != "" {
					t.Fatal("legacy request unexpectedly frozen")
				}
				raw, err := json.Marshal(photoItemGradeRequest(run.req, run.questions[0]))
				if err != nil || strings.Contains(string(raw), "solve_output_version") || strings.Contains(string(raw), "parent_teaching_contract") {
					t.Fatalf("legacy request digest input changed: %s err=%v", raw, err)
				}
			}
			guideRow := solveTeachingRow(rows, k12.GradingItemOperationParentGuide)
			if len(rows) != 3 || guideRow == nil || guideRow.ExecutionKind != wantKind || len(o.deps.ParentTeachingGuide.(*parentTeachingGuideSpy).snapshot()) != wantCalls {
				t.Fatalf("guide route changed: %+v", rows)
			}
		})
	}
}
