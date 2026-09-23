package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// 可控求解边界只提供确定性回执，验证复用编排，不冒充真实模型或数学质量验收。
type assetReuseSolver struct {
	calls int
	model bool
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

type assetReuseGrader struct{ answers []string }

func (g *assetReuseGrader) Grade(_ context.Context, _ string, answer, solution string) (GradeOutcome, error) {
	g.answers = append(g.answers, answer)
	correct := answer == solution
	if correct {
		return GradeOutcome{Verdict: VerdictAgree, FinalAnswerCorrect: &correct}, nil
	}
	return GradeOutcome{Verdict: VerdictDisagree, FinalAnswerCorrect: &correct, WrongStep: "2+2=5", ErrorCause: "Addition result is incorrect.", KnowledgePoint: "整数加法"}, nil
}

func TestProblemAssets_ReusedAnswerStillGradesEachAttempt(t *testing.T) {
	for _, model := range []bool{false, true} {
		name := "local_deterministic"
		if model {
			name = "model_with_execution"
		}
		t.Run(name, func(t *testing.T) { testProblemAssetReuse(t, model) })
	}
}

func testProblemAssetReuse(t *testing.T, model bool) {
	ctx := context.Background()
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
