package api

import (
	"context"
	"errors"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type materialFailedSolver struct {
	t       *testing.T
	failure error
	calls   int
}

func (s *materialFailedSolver) Solve(ctx context.Context, _, _, _ string) (usecase.SolveResult, error) {
	if !k12.IsMaterialPreparation(ctx) {
		s.t.Fatal("material context lost")
	}
	_, err := usecase.ExecuteGradingPhysicalCall(ctx, usecase.GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveGenerate, RequestDigest: "source-facts"}, func(context.Context) (string, error) { s.calls++; return "", s.failure })
	return usecase.SolveResult{}, err
}

func TestMaterialPreparationNotSentAndUnknownRemainDistinctOnRestart(t *testing.T) {
	for _, tc := range []struct {
		name, invocation, state string
		err                     error
	}{
		{"denied-before-send", "not_sent", "needs_review", egress.ErrProviderNotSent},
		{"timeout-after-dispatch", "outcome_unknown", "outcome_unknown", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, records, worker, doc := materialImportFixture(t, "1. 商店3本书售价18元，买7本同样的书多少钱？\n")
			solver := &materialFailedSolver{t: t, failure: tc.err}
			worker.Solver, worker.ResolveModel = solver, materialModelRoute
			_, err := worker.RunOnce(t.Context())
			if tc.state == "needs_review" && err != nil {
				t.Fatal(err)
			}
			if tc.state == "outcome_unknown" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("unknown error=%v", err)
			}
			view, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
			if err != nil || view.Counts[tc.state] != 1 {
				t.Fatalf("summary=%+v err=%v", view, err)
			}
			var status string
			if err = db.QueryRow(`SELECT status FROM k12_material_invocations`).Scan(&status); err != nil || status != tc.invocation {
				t.Fatalf("status=%s err=%v", status, err)
			}
			if err = records.RecoverMaterialPreparations(t.Context()); err != nil {
				t.Fatal(err)
			}
			if did, err := worker.RunOnce(t.Context()); err != nil || did || solver.calls != 1 {
				t.Fatalf("replay did=%v calls=%d err=%v", did, solver.calls, err)
			}
			var assets int
			if err = db.QueryRow(`SELECT COUNT(*) FROM k12_problem_asset_versions`).Scan(&assets); err != nil || assets != 0 {
				t.Fatalf("assets=%d err=%v", assets, err)
			}
		})
	}
}
