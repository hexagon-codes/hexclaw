package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/skill"
)

type visualQuantityCallbackSolver struct {
	physical bool
	answer   string
	images   int
	target   string
}

func (s *visualQuantityCallbackSolver) UsesGradingPhysicalCalls() bool { return s.physical }

func (s *visualQuantityCallbackSolver) QuantityAnswerCompleteness(problem, answer string) (bool, bool) {
	s.target = problem
	return engine.QuantityAnswerCompleteness(problem, answer)
}

func (s *visualQuantityCallbackSolver) Solve(ctx context.Context, _, _, _ string) (SolveResult, error) {
	s.images = len(skill.InputImages(ctx))
	if s.physical {
		_, err := ExecuteGradingPhysicalCall(ctx, GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveGenerate, RequestDigest: "controlled-visual-response"}, func(context.Context) (string, error) {
			raw, marshalErr := json.Marshal(map[string]string{"answer": s.answer})
			return string(raw), marshalErr
		})
		if err != nil {
			return SolveResult{}, err
		}
	}
	return SolveResult{Solution: "答案：" + s.answer, Evidence: SolveEvidence{Verdict: VerdictUnverifiable, EvidenceType: EvidenceNone}}, nil
}

func TestGradingVisualQuantityCallbacksRejectMissingResultBeforeLogicalSuccess(t *testing.T) {
	for _, physical := range []bool{false, true} {
		for _, answer := range []string{"图②缺少，无法给出面积。", "15.7 cm²"} {
			name := "logical/" + answer
			if physical {
				name = "physical/" + answer
			}
			t.Run(name, func(t *testing.T) {
				o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{{Question: "将两圆按图②放置，涂色面积是（ ）cm²。", Subject: "数学", AnswerState: AnswerStateBlank}}, &itemResumeSolver{calls: map[string]int{}}, &itemResumeGrader{calls: map[string]int{}})
				jobID := runItemResumeJobToAssessing(t, o, "visual-quantity-callback")
				run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
				data, _ := problemSourceOrientationFixture(t)
				assetID := seedGradingImageTaskOwnerScopeForTest(t, o.deps, "visual-quantity-source", data)
				job.Fields.SourceKind = "image_task"
				job.Fields.IdempotencyKey = k12.BuildGradingIdempotencyKey("image_task", "visual-quantity-source", job.Fields.ConfirmedVersion)
				job.Fields.ModelSnapshot.QuantityAnswerPolicy = k12.QuantityAnswerPolicySlotV1
				q := run.questions[0]
				q.RawTranscription = q.Question
				q.PageAssetID, q.SourceWidth, q.SourceHeight = assetID, 3, 2
				q.SourceRegion = &k12.SourcePixelRegion{Width: 3, Height: 2}
				solver := &visualQuantityCallbackSolver{physical: physical, answer: answer}
				deps := o.deps
				deps.Solver = solver
				ctx := k12.WithGradingModelSnapshot(t.Context(), job.Fields.ModelSnapshot)
				req := photoItemGradeRequest(run.req, q)
				// 共享材料也有空位，质量核对仍只针对当前题本身。
				req.Problem += "\nShared material: 其它题面积是（ ）cm²。"
				_, _, err := executeUncachedDurableSolveOperation(ctx, o, deps, job, q, req)
				missing := answer != "15.7 cm²"
				if missing && (!errors.Is(err, ErrSolveFailed) || !errors.Is(err, egress.ErrProviderResponseProcessed)) || !missing && err != nil {
					t.Fatalf("quantity callback did not classify current output: missing=%v err=%v", missing, err)
				}
				if solver.images != 1 || solver.target != q.RawTranscription {
					t.Fatalf("callback lost actual image or inspected unrelated targets: images=%d target=%q", solver.images, solver.target)
				}
				rows, err := deps.Records.ListGradingItemInvocations(t.Context(), job.Record.AgentName, jobID)
				if err != nil || len(rows) != 1 {
					t.Fatalf("callback ledger=%+v err=%v", rows, err)
				}
				if physical {
					if rows[0].Status != k12.ModelInvocationSucceeded || rows[0].ResultJSON == "" {
						t.Fatal("quality check lost the actual observed provider response")
					}
				} else if missing && rows[0].Status == k12.ModelInvocationSucceeded {
					t.Fatal("missing numerical result became a logical success")
				}
			})
		}
	}
}
