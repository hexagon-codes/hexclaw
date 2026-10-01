package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/hexagon-codes/hexclaw/records"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// 可控求解边界只提供确定性回执，验证复用编排，不冒充真实模型或数学质量验收。
type assetReuseSolver struct {
	calls int
	model bool
}

func TestProblemAssets_FinalizationRecoversInvalidCommittedAnswer(t *testing.T) {
	for _, outcomeUnknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "unknown"}[outcomeUnknown], func(t *testing.T) {
			ctx := context.Background()
			solver := &assetReuseSolver{}
			grader := &assetReuseGrader{}
			o := newParallelAnchorOrchestrator(t, &countingRecognizer{}, nil, WithGradingRunDir(t.TempDir()))
			o.deps.Solver, o.deps.Grader, o.deps.VerifiedGrader = solver, grader, nil
			o.deps.TextbookOwnerID = DefaultLocalOwnerScope
			o.deps.Recognizer = &countingRecognizer{questions: []RecognizedQuestion{{Question: "2+2=", Subject: "数学", StudentAnswer: "4", AnswerState: AnswerStatePresent}}}
			dispatcher := k12storage.NewDispatcher(o.deps.Records, ProblemAssetConsumer{Records: o.deps.Records})
			seedJob := runItemResumeJobToAssessing(t, o, "final-correction-seed")
			if _, err := o.ConfirmAndRun(ctx, seedJob, nil); err != nil {
				t.Fatal(err)
			}
			if err := dispatcher.ProcessPending(ctx); err != nil {
				t.Fatal(err)
			}
			jobID := runItemResumeJobToAssessing(t, o, "final-correction-target")
			_, job := confirmItemResumeJobWithoutRun(t, o, jobID)
			run := o.lookup(jobID)
			q := run.questions[0]
			job, err := o.runAssessItems(ctx, run, job)
			if err != nil {
				t.Fatal(err)
			}
			original, err := o.deps.Records.GetGradingAssessmentItem(ctx, "mingming", jobID, q.ProblemID)
			if err != nil || original.AnswerSource == nil {
				t.Fatalf("original asset receipt: %+v %v", original, err)
			}
			source := original.AnswerSource
			if err = o.deps.Records.ArchiveProblemAsset(ctx, DefaultLocalOwnerScope, source.AssetID, source.AssetRevision); err != nil {
				t.Fatal(err)
			}
			if outcomeUnknown {
				grader.failure = context.DeadlineExceeded
			}
			artifact, err := o.finalizeGradingPage(ctx, run, job)
			if outcomeUnknown {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("first recovery must retain unknown: %v", err)
				}
				if _, err = o.finalizeGradingPage(ctx, run, job); err == nil {
					t.Fatal("unknown recovery falsely succeeded")
				}
				view, readErr := o.deps.Records.GetEffectiveGradingAssessment(ctx, "mingming", jobID, q.ProblemID)
				if readErr != nil || view.Correction != nil || !reflect.DeepEqual(view.Original, original) {
					t.Fatalf("unknown changed assessment: %+v %v", view, readErr)
				}
				if _, err = o.deps.Records.GetGradingFinalArtifactByJob(ctx, "mingming", jobID); !errors.Is(err, records.ErrNotFound) {
					t.Fatalf("unknown published artifact: %v", err)
				}
				if solver.calls != 2 || len(grader.answers) != 3 {
					t.Fatalf("unknown resent: solve=%d grade=%d", solver.calls, len(grader.answers))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if artifact.ArtifactID == "" || artifact.CanonicalMarkdown == "" {
				t.Fatal("missing final artifact")
			}
			view, err := o.deps.Records.GetEffectiveGradingAssessment(ctx, "mingming", jobID, q.ProblemID)
			if err != nil {
				t.Fatal(err)
			}
			if view.Correction == nil || view.Current.SolveInvocationID == "" || view.Current.GradeInvocationID == original.GradeInvocationID || view.Current.AnswerSource != nil {
				t.Fatalf("correction did not bind new proof: %+v", view)
			}
			if !reflect.DeepEqual(original, view.Original) {
				t.Fatal("recovery overwrote historical assessment")
			}
			if solver.calls != 2 || len(grader.answers) != 3 {
				t.Fatalf("solve=%d grade=%d", solver.calls, len(grader.answers))
			}
			replayed, err := o.finalizeGradingPage(ctx, run, job)
			if err != nil || !reflect.DeepEqual(artifact, replayed) || solver.calls != 2 || len(grader.answers) != 3 {
				t.Fatalf("final replay repeated recovery: %v", err)
			}
		})
	}

}

func (s *assetReuseSolver) UsesGradingPhysicalCalls() bool { return true }
func (s *assetReuseSolver) Solve(ctx context.Context, _ string, _ string, _ string) (SolveResult, error) {
	s.calls++
	if s.model {
		_, err := ExecuteGradingPhysicalCall(ctx, GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveGenerate, RequestDigest: "fixture-generate"}, func(context.Context) (string, error) { return `{"Output":"4"}`, nil })
		if err != nil {
			return SolveResult{}, err
		}
		_, err = ExecuteGradingPhysicalCall(ctx, GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveVerify, RequestDigest: "fixture-verify"}, func(context.Context) (string, error) {
			return `{"Output":"VERDICT: AGREE","execution_receipt":{"input_digest":"fixture-verification-task","run_id":"fixture-code-run","status":"success","exit_code":0,"stdout_bytes":1,"stdout":"4"}}`, nil
		})
		if err != nil {
			return SolveResult{}, err
		}
		digest := sha256.Sum256([]byte("4"))
		return SolveResult{Solution: "4", Evidence: SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec,
			SolverOutputDigest: hex.EncodeToString(digest[:]), VerificationInputDigest: "fixture-verification-task", VerificationRunID: "fixture-code-run"}}, nil
	}
	return SolveResult{Solution: "4", Evidence: SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec}}, nil
}

type assetReuseGrader struct {
	answers []string
	failure error
}

func (g *assetReuseGrader) Grade(_ context.Context, _ string, answer, solution string) (GradeOutcome, error) {
	g.answers = append(g.answers, answer)
	if g.failure != nil {
		return GradeOutcome{}, g.failure
	}
	correct := answer == solution
	if correct {
		return GradeOutcome{Verdict: VerdictAgree, FinalAnswerCorrect: &correct}, nil
	}
	return GradeOutcome{Verdict: VerdictDisagree, FinalAnswerCorrect: &correct, WrongStep: "2+2=5", ErrorCause: "Addition result is incorrect.", KnowledgePoint: "整数加法"}, nil
}

func TestProblemAssets_ReusedAnswerStillGradesEachAttempt(t *testing.T) {
	for _, tc := range []struct {
		name            string
		model, grounded bool
	}{
		{"local_deterministic", false, false},
		{"model_with_execution", true, false},
		{"grounded_local_deterministic", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) { testProblemAssetReuse(t, tc.model, tc.grounded) })
	}
}

func testProblemAssetReuse(t *testing.T, model, grounded bool) {
	ctx := context.Background()
	if grounded {
		receipt := groundingRecoveryReceipt()
		snapshot := GroundingSnapshot{AgentName: "mingming", LearnerID: "learner", Subject: "数学",
			TextbookBindingID: receipt.TextbookBindingID, TextbookManifestID: receipt.TextbookManifestID,
			DocumentID: receipt.DocumentID, DocumentGeneration: receipt.DocumentGeneration,
			SourceDigest: receipt.SourceDigest, Edition: "人教版", Volume: "下册", VectorRevisionID: receipt.VectorRevisionID,
			SegmentRefs: []string{receipt.ChunkID}, PageRefs: []k12.TextbookGroundingPageRef{{LogicalPage: receipt.LogicalPage,
				PDFPage: receipt.PDFPage, SegmentRefs: []string{receipt.ChunkID}}}}
		var err error
		ctx, err = WithVerifiedGradingGrounding(ctx, snapshot, GroundingSnapshotResult{
			Text: "本次批改实际消费的教材命中", Found: true, Receipts: []GroundingEvidenceReceipt{receipt}})
		if err != nil {
			t.Fatal(err)
		}
	}
	solver := &assetReuseSolver{model: model}
	grader := &assetReuseGrader{}
	o := newParallelAnchorOrchestrator(t, &countingRecognizer{}, nil, WithGradingRunDir(t.TempDir()))
	o.deps.Solver = solver
	o.deps.Grader = grader
	o.deps.VerifiedGrader = nil
	o.deps.TextbookOwnerID = DefaultLocalOwnerScope
	dispatcher := k12storage.NewDispatcher(o.deps.Records, InsightsConsumer{Insights: o.deps.Insights}, ProblemAssetConsumer{Records: o.deps.Records})
	var previousJob string
	for i, answer := range []string{"4", "5"} {
		o.deps.Recognizer = &countingRecognizer{questions: []RecognizedQuestion{{Question: "2+2=", Subject: "数学", StudentAnswer: answer, AnswerState: AnswerStatePresent}}}
		jobID := runItemResumeJobToAssessing(t, o, "asset-answer-"+answer)
		if jobID == previousJob {
			t.Fatal("different answers reused a grading job")
		}
		previousJob = jobID
		view, err := o.ConfirmAndRun(ctx, jobID, nil)
		if err != nil || view.Record.Status != k12.GradingStageCompleted {
			t.Fatalf("attempt %d status=%s err=%v", i, view.Record.Status, err)
		}
		receipts, err := o.deps.Records.ListGradingAssessmentItems(ctx, "mingming", jobID)
		if err != nil || len(receipts) != 1 {
			t.Fatalf("receipts=%+v err=%v", receipts, err)
		}
		r := receipts[0]
		want := k12.GradingAssessmentCorrect
		if i == 1 {
			want = k12.GradingAssessmentWrong
		}
		if r.Status != want {
			t.Fatalf("answer %q status=%s want=%s", answer, r.Status, want)
		}
		if i == 0 {
			var originalInvocation k12.GradingItemInvocation
			var originalEvent, groundedPayload string
			if grounded {
				originalInvocation, err = o.deps.Records.GetGradingItemInvocation(ctx, "mingming", r.SolveInvocationID)
				if err != nil || originalInvocation.ExecutionKind != k12.GradingExecutionLocalDeterministic {
					t.Fatalf("grounded local solve receipt: %+v %v", originalInvocation, err)
				}
				payload, _, enveloped, decodeErr := decodeGroundedPhysicalPayload(originalInvocation.ResultJSON, nil)
				if decodeErr != nil || !enveloped {
					t.Fatalf("normal execution did not persist grounding envelope: %v", decodeErr)
				}
				groundedPayload = payload
				if err := o.deps.Records.DB().QueryRow(`SELECT payload_json FROM outbox_events WHERE event_type='k12.problem_asset.prepare'`).Scan(&originalEvent); err != nil {
					t.Fatal(err)
				}
				var publication k12.ProblemAssetPublication
				if err := json.Unmarshal([]byte(originalEvent), &publication); err != nil || publication.AnswerResultJSON != originalInvocation.ResultJSON || publication.Verification.ResultDigest != originalInvocation.ResultDigest {
					t.Fatal("normal prepare event lost original grounded result or digest")
				}
			}
			var assets, pending int
			if err := o.deps.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_problem_assets`).Scan(&assets); err != nil || assets != 0 {
				t.Fatalf("foreground published assets=%d err=%v", assets, err)
			}
			if err := o.deps.Records.DB().QueryRow(`SELECT COUNT(*) FROM outbox_events WHERE event_type='k12.problem_asset.prepare' AND status='pending'`).Scan(&pending); err != nil || pending != 1 {
				t.Fatalf("durable publication tasks=%d err=%v", pending, err)
			}
			for replay := 0; replay < 2; replay++ {
				if err := dispatcher.ProcessPending(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if err := o.deps.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_problem_assets`).Scan(&assets); err != nil || assets != 1 {
				t.Fatalf("background published assets=%d err=%v", assets, err)
			}
			if grounded {
				invocationAfter, err := o.deps.Records.GetGradingItemInvocation(ctx, "mingming", originalInvocation.InvocationID)
				if err != nil || !reflect.DeepEqual(originalInvocation, invocationAfter) {
					t.Fatal("publication rewrote successful invocation or complete result digest")
				}
				var eventAfter, assetID string
				if err := o.deps.Records.DB().QueryRow(`SELECT payload_json FROM outbox_events WHERE event_type='k12.problem_asset.prepare'`).Scan(&eventAfter); err != nil || eventAfter != originalEvent {
					t.Fatal("publication rewrote original prepare event")
				}
				if err := o.deps.Records.DB().QueryRow(`SELECT asset_id FROM k12_problem_assets WHERE owner_id=?`, DefaultLocalOwnerScope).Scan(&assetID); err != nil {
					t.Fatal(err)
				}
				asset, err := o.deps.Records.GetProblemAssetVersion(ctx, DefaultLocalOwnerScope, assetID, 1)
				if err != nil || asset.AnswerResultJSON != groundedPayload {
					t.Fatal("grounded publication did not preserve the actual inner solve payload")
				}
			}
			if model {
				var payload string
				if err := o.deps.Records.DB().QueryRow(`SELECT payload_json FROM outbox_events WHERE event_type='k12.problem_asset.prepare'`).Scan(&payload); err != nil {
					t.Fatal(err)
				}
				var publication k12.ProblemAssetPublication
				if err := json.Unmarshal([]byte(payload), &publication); err != nil {
					t.Fatal(err)
				}
				publication.PublicationID = "invalid-execution-binding"
				publication.Verification.VerificationRunID = "unrelated-run"
				if _, _, err := o.deps.Records.PublishProblemAsset(ctx, publication); err == nil {
					t.Fatal("unrelated execution was accepted")
				}
			}
		}
		if i == 1 {
			if r.AnswerSource == nil || r.AnswerSource.Kind != k12.ProblemAnswerAsset || r.SolveInvocationID != "" || r.GradeInvocationID == "" {
				t.Fatalf("asset source must replace solve only: %+v", r)
			}
			invocations, err := o.deps.Records.ListGradingItemInvocations(ctx, "mingming", jobID)
			if err != nil {
				t.Fatal(err)
			}
			for _, inv := range invocations {
				if inv.Operation == k12.GradingItemOperationSolve || inv.Operation == k12.GradingItemOperationSolveGenerate || inv.Operation == k12.GradingItemOperationSolveVerify {
					t.Fatalf("reuse fabricated a solve invocation: %+v", inv)
				}
			}
		}
	}
	if solver.calls != 1 || len(grader.answers) != 2 || grader.answers[0] != "4" || grader.answers[1] != "5" {
		t.Fatalf("solve=%d graded=%v", solver.calls, grader.answers)
	}
	// 停用发生在采用之后、评估提交之前时，保留原采用并回到实际求解，
	// 不能使新任务永久卡住，也不能覆盖上一任务已提交的历史批改。
	jobID := runItemResumeJobToAssessing(t, o, "asset-disabled-before-assessment")
	_, job := confirmItemResumeJobWithoutRun(t, o, jobID)
	q := o.lookup(jobID).questions[0]
	var assetID string
	if err := o.deps.Records.DB().QueryRow(`SELECT asset_id FROM k12_problem_assets WHERE owner_id=?`, DefaultLocalOwnerScope).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	asset, err := o.deps.Records.GetProblemAssetVersion(ctx, DefaultLocalOwnerScope, assetID, 1)
	if err != nil {
		t.Fatal(err)
	}
	adoption, _, err := o.deps.Records.AdoptProblemAsset(ctx, k12.ProblemAssetAdoption{
		OwnerID: asset.OwnerID, JobID: jobID, ProblemID: q.ProblemID, InputRevision: q.ConfirmedVersion,
		InputDigest: q.InputDigest, AssetID: asset.AssetID, AssetVersion: asset.Version,
		AssetRevision: asset.Revision, FactsDigest: asset.FactsDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.deps.Records.ArchiveProblemAsset(ctx, asset.OwnerID, asset.AssetID, asset.Revision); err != nil {
		t.Fatal(err)
	}
	view, err := o.RunGradingJob(ctx, job.Record.RecordID)
	if err != nil || view.Record.Status != k12.GradingStageCompleted {
		t.Fatalf("archived adoption recovery status=%s err=%v", view.Record.Status, err)
	}
	current, err := o.deps.Records.GetGradingAssessmentItem(ctx, "mingming", jobID, q.ProblemID)
	if err != nil || current.SolveInvocationID == "" || current.AnswerSource != nil {
		t.Fatalf("recovered task must use an actual solve: %+v err=%v", current, err)
	}
	retained, err := o.deps.Records.GetProblemAssetAdoption(ctx, asset.OwnerID, adoption.AdoptionID)
	if err != nil || retained != adoption {
		t.Fatalf("original adoption changed: %+v err=%v", retained, err)
	}
	history, err := o.deps.Records.ListGradingAssessmentItems(ctx, "mingming", previousJob)
	if err != nil || len(history) != 1 || history[0].AnswerSource == nil || history[0].Status != k12.GradingAssessmentWrong {
		t.Fatalf("prior assessment changed: %+v err=%v", history, err)
	}
	if solver.calls != 2 || len(grader.answers) != 3 {
		t.Fatalf("fallback solve=%d grade=%d", solver.calls, len(grader.answers))
	}
}

const assetFormatOldBread = `一袋面包重 \(\frac{3}{10}\,\text{kg}\)，3 袋重多少千克？`
const assetFormatHistoricalBread = `一袋面包重 \\(\\frac{3}{10}\\,\\text{kg}\\)，3 袋重多少千克？`
const assetFormatNewBread = "一袋面包重 $\\frac{3}{10}\\,\\mathrm{kg}$，3 袋重多少千克？\n\n$\\square\\times\\square=\\square\\（\\mathrm{kg}）$"

// 真实 SQLite 发布、采用及运行文件重载只验证来源合同，不冒充数学或模型质量验收。
func TestGradingProblemAssetSourceFormat(t *testing.T) {
	for _, scenario := range []string{"format", "historical_escaping", "exact_priority", "candidate_conflict", "archived_source", "owner_scope", "filled_template", "student_answer", "different_unit", "unknown_not_resent"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			runDir := t.TempDir()
			solver, grader := &feedbackReassessmentSolver{answer: "0.9kg"}, &assetReuseGrader{}
			o := newParallelAnchorOrchestrator(t, &countingRecognizer{}, nil, WithGradingRunDir(runDir))
			o.deps.Solver, o.deps.Grader, o.deps.VerifiedGrader = solver, grader, nil
			o.deps.TextbookOwnerID = DefaultLocalOwnerScope
			dispatcher := k12storage.NewDispatcher(o.deps.Records, ProblemAssetConsumer{Records: o.deps.Records})
			seedStem := assetFormatOldBread
			if scenario == "historical_escaping" {
				seedStem = assetFormatHistoricalBread
			}
			publish := func(stem, source string) k12.ProblemAssetVersion {
				t.Helper()
				o.deps.Recognizer = &countingRecognizer{questions: []RecognizedQuestion{{Question: stem, Subject: "数学", AnswerState: AnswerStateBlank}}}
				id := runItemResumeJobToAssessing(t, o, source)
				seedRun := o.lookup(id)
				grade := seedRun.req.Grade
				if source != "source-format-seed" {
					// 已独立开始的求解沿真实账本完成发布，构造两个有效原版本，不借资产复用伪造第二份来源。
					seedRun, seedJob := confirmItemResumeJobWithoutRun(t, o, id)
					seedQuestion := seedRun.questions[0]
					if _, _, err := executeUncachedDurableSolveOperation(ctx, o, o.deps, seedJob, seedQuestion, photoItemGradeRequest(seedRun.req, seedQuestion)); err != nil {
						t.Fatal(err)
					}
					if _, err := o.RunGradingJob(ctx, id); err != nil {
						t.Fatal(err)
					}
				} else if _, err := o.ConfirmAndRun(ctx, id, nil); err != nil {
					t.Fatal(err)
				}
				if err := dispatcher.ProcessPending(ctx); err != nil {
					t.Fatal(err)
				}
				asset, err := o.deps.Records.FindExactProblemAsset(ctx, DefaultLocalOwnerScope,
					k12.ProblemAssetFacts{Subject: "数学", Stem: stem, AnswerContext: map[string]string{"grade_term": grade}})
				if err != nil {
					t.Fatalf("normal publication of original source: %v", err)
				}
				return asset
			}
			original := publish(seedStem, "source-format-seed")
			wantAsset := original
			if scenario == "exact_priority" {
				wantAsset = publish(assetFormatNewBread, "source-format-exact-seed")
			}
			if scenario == "candidate_conflict" {
				publish(assetFormatHistoricalBread, "source-format-conflicting-seed")
			}
			if scenario == "archived_source" {
				if err := o.deps.Records.ArchiveProblemAsset(ctx, original.OwnerID, original.AssetID, original.Revision); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "owner_scope" {
				o.deps.TextbookOwnerID = "other-format-owner"
			}
			seedCalls := solver.calls
			stem, answer, state := assetFormatNewBread, "", AnswerStateBlank
			if scenario == "filled_template" {
				stem = "一袋面包重 $\\frac{3}{10}\\,\\mathrm{kg}$，3 袋重多少千克？\n\n$\\square\\times\\square=0.9\\（\\mathrm{kg}）$"
			}
			if scenario == "different_unit" {
				stem = "一袋面包重 $\\frac{3}{10}\\,\\mathrm{g}$，3 袋重多少千克？\n\n$\\square\\times\\square=\\square\\（\\mathrm{g}）$"
			}
			if scenario == "student_answer" {
				answer, state = "0.8kg", AnswerStatePresent
			}
			o.deps.Recognizer = &countingRecognizer{questions: []RecognizedQuestion{{Question: stem, Subject: "数学", StudentAnswer: answer, AnswerState: state}}}
			jobID := runItemResumeJobToAssessing(t, o, "source-format-target")
			run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
			q := run.questions[0]
			qBefore := cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
			req := photoItemGradeRequest(run.req, q)
			if scenario == "unknown_not_resent" {
				inv := k12.GradingItemInvocation{InvocationID: "source-format-existing-unknown", AgentName: "mingming", JobID: jobID, ProblemID: q.ProblemID, AttemptID: q.AttemptID,
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
				before, _ := o.deps.Records.GetGradingItemInvocation(ctx, "mingming", inv.InvocationID)
				physical := &candidatePhysicalSolver{}
				o.deps.Solver = physical
				if _, err := o.runAssessItems(ctx, run, job); !errors.Is(err, ErrModelInvocationRequiresReconciliation) {
					t.Fatalf("unknown not retained: %v", err)
				}
				after, err := o.deps.Records.GetGradingItemInvocation(ctx, "mingming", inv.InvocationID)
				if err != nil || !reflect.DeepEqual(before, after) || physical.calls != 0 || solver.calls != seedCalls {
					t.Fatal("format match changed or resent unknown")
				}
				if _, err := o.deps.Records.FindProblemAssetAdoption(ctx, DefaultLocalOwnerScope, jobID, q.ProblemID, q.ConfirmedVersion); err == nil {
					t.Fatal("format match masked unknown with an adoption")
				}
				return
			}
			solved, invocationID, err := executeDurableSolveOperation(ctx, o, o.deps, job, q, req)
			if err != nil {
				t.Fatal(err)
			}
			shouldAdopt := scenario == "format" || scenario == "historical_escaping" || scenario == "exact_priority"
			if !shouldAdopt {
				if solved.AnswerSource != nil || invocationID == "" || solver.calls != seedCalls+1 || !reflect.DeepEqual(q, qBefore) || req.StudentAnswer != answer {
					t.Fatal("source condition or student answer was erased to adopt")
				}
				return
			}
			if solved.AnswerSource == nil || solved.AnswerSource.AssetID != wantAsset.AssetID || solved.AnswerSource.FactsDigest != wantAsset.FactsDigest || invocationID != "" || solver.calls != seedCalls {
				t.Fatalf("source-format asset miss or exact priority lost: source=%+v invocation=%q calls=%d", solved.AnswerSource, invocationID, solver.calls)
			}
			adoption, err := o.deps.Records.FindProblemAssetAdoption(ctx, DefaultLocalOwnerScope, jobID, q.ProblemID, q.ConfirmedVersion)
			if err != nil || adoption.InputDigest != q.InputDigest || adoption.FactsDigest != wantAsset.FactsDigest {
				t.Fatal("adoption lost current input or original facts digest")
			}
			o2 := trackGradingOrchestrator(t, NewGradingOrchestrator(o.deps, orchestratorSnapshotResolver, WithGradingRunDir(runDir)))
			reloaded, err := o2.ensureRun(ctx, jobID)
			if err != nil {
				t.Fatal(err)
			}
			q2 := reloaded.questions[0]
			if !reflect.DeepEqual(q, qBefore) || !reflect.DeepEqual(q2, qBefore) {
				t.Fatal("format lookup changed frozen source or reload input")
			}
			replayed, replayID, err := executeDurableSolveOperation(ctx, o2, o2.deps, job, q2, photoItemGradeRequest(reloaded.req, q2))
			if err != nil || replayID != "" || replayed.AnswerSource == nil || replayed.AnswerSource.AdoptionID != adoption.AdoptionID || solver.calls != seedCalls {
				t.Fatalf("reload failed to restore original adoption: source=%+v err=%v", replayed.AnswerSource, err)
			}
			assetAfter, err := o.deps.Records.GetProblemAssetVersion(ctx, original.OwnerID, original.AssetID, original.Version)
			if err != nil || !reflect.DeepEqual(original, assetAfter) {
				t.Fatal("source-format lookup changed original v1 asset")
			}
		})
	}
}
