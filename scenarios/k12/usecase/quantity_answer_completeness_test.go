package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/skill"
)

type quantityCompletenessSolver struct {
	fakeSolver
	required, complete bool
	calls              int
	answer             string
}

func (s *quantityCompletenessSolver) QuantityAnswerCompleteness(_, answer string) (bool, bool) {
	s.calls++
	s.answer = answer
	return s.required, s.complete
}

func TestVisualQuantityResultGuardKeepsLegacyAndOpenAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, policy, subject                          string
		images, required, complete, wantError, checked bool
	}{
		{"missing numerical result", k12.QuantityAnswerPolicySlotV1, "数学", true, true, false, true, true},
		{"complete numerical result", k12.QuantityAnswerPolicySlotV1, "数学", true, true, true, false, true},
		{"open mathematical explanation", k12.QuantityAnswerPolicySlotV1, "数学", true, false, false, false, true},
		{"legacy snapshot", "", "数学", true, true, false, false, false},
		{"no image", k12.QuantityAnswerPolicySlotV1, "数学", false, true, false, false, false},
		{"other subject", k12.QuantityAnswerPolicySlotV1, "语文", true, true, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := k12.WithGradingModelSnapshot(t.Context(), k12.GradingModelSnapshot{Provider: "test", Model: "test", QuantityAnswerPolicy: tc.policy})
			if tc.images {
				ctx = skill.WithInputImages(ctx, []skill.InputImage{{ID: "source", MIME: "image/png", Data: "synthetic"}})
			}
			solver := &quantityCompletenessSolver{required: tc.required, complete: tc.complete}
			result := SolveHomeworkResult{Solution: "原始模型结果", Evidence: SolveEvidence{Verdict: VerdictUnverifiable, EvidenceType: EvidenceNone},
				Generation: &SolveGeneration{ParentGuideCandidate: &ParentTeachingGuide{Answer: "本次真实最终答案"}}}
			err := validateVisualQuantitySolveResult(ctx, Deps{Solver: solver}, GradeRequest{Subject: tc.subject, Problem: "面积是（ ）cm²。"}, result)
			if (err != nil) != tc.wantError || (solver.calls == 1) != tc.checked {
				t.Fatalf("err=%v calls=%d", err, solver.calls)
			}
			if err != nil && (!errors.Is(err, ErrSolveFailed) || !errors.Is(err, egress.ErrProviderResponseProcessed)) {
				t.Fatal("missing answer must remain a known solver response failure")
			}
			if tc.checked && solver.answer != result.Generation.ParentGuideCandidate.Answer {
				t.Fatal("the final answer must come from the actual generation")
			}
			if result.Solution != "原始模型结果" || result.Evidence.Verdict != VerdictUnverifiable {
				t.Fatal("the guard must not rewrite the original model output or evidence")
			}
		})
	}
}

func TestPhotoQuantityPolicyFreezesOnlyNewImageTaskJobs(t *testing.T) {
	d, _ := newPipeline(t, fakeSolver{}, fakeGrader{}, nil)
	d.GradingBudgetSnapshot = orchestratorTestBudget()
	o := trackGradingOrchestrator(t, NewGradingOrchestrator(d, orchestratorSnapshotResolver))
	job, _, _ := prepareInitiallyBlankImageTaskJob(t, d, o, "quantity-policy")
	if job.Fields.ModelSnapshot.QuantityAnswerPolicy != k12.QuantityAnswerPolicySlotV2 {
		t.Fatalf("new image task has no frozen quantity policy: %+v", job.Fields.ModelSnapshot)
	}
	// 历史快照没有该可选字段，同一来源重放不得按当前策略补写。
	if _, err := d.Records.DB().ExecContext(t.Context(), `UPDATE k12_grading_jobs SET model_snapshot_json=json_remove(model_snapshot_json,'$.quantity_answer_policy') WHERE record_id=?`, job.Record.RecordID); err != nil {
		t.Fatal(err)
	}
	prior := o.lookup(job.Record.RecordID).req
	replayed, created, err := o.StartPhotoGradingJob(t.Context(), StartPhotoGradingInput{
		Photo: prior, SourceKind: "image_task", SourceKey: "quantity-policy",
		BudgetSnapshot: job.Fields.BudgetSnapshot, ParentAutomaticAttemptID: job.Fields.ParentAutomaticAttemptID,
		ParentAutomaticDeadlineAt: job.Fields.ParentAutomaticDeadlineAt,
	})
	if err != nil || created || replayed.Record.RecordID != job.Record.RecordID || replayed.Fields.ModelSnapshot.QuantityAnswerPolicy != "" {
		t.Fatalf("legacy same-source replay acquired a new policy: created=%v snapshot=%+v err=%v", created, replayed.Fields.ModelSnapshot, err)
	}
	photo := orchestratorPhotoRequest()
	normal, _, err := o.StartPhotoGradingJob(context.Background(), StartPhotoGradingInput{Photo: photo, SourceKind: "im", SourceKey: "legacy-quantity-policy"})
	if err != nil || normal.Fields.ModelSnapshot.QuantityAnswerPolicy != "" {
		t.Fatalf("unrelated entry changed policy: %+v err=%v", normal.Fields.ModelSnapshot, err)
	}
}
