package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestMaterialPreparationGivesEachPhysicalCallItsFrozenBudget(t *testing.T) {
	_, records, worker, doc := materialImportFixture(t, "1. 商店3本书售价18元，7本同样的书一共多少元？\n")
	boundary := &materialControlledSolver{t: t}
	boundary.beforeCall = func(ctx context.Context, operation k12.GradingItemOperation) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 400*time.Millisecond {
			t.Fatalf("operation %s lost its independent call budget", operation)
		}
		select {
		case <-time.After(300 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	worker.Solver = boundary
	worker.ResolveModel = func(ctx context.Context, prior k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) {
		snapshot, err := materialModelRoute(ctx, prior)
		snapshot.TimeoutMS = 500
		return snapshot, err
	}
	if did, err := worker.RunOnce(t.Context()); err != nil || !did {
		t.Fatalf("prepare: did=%v err=%v", did, err)
	}
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || summary.State != "ready" || boundary.calls != 2 {
		t.Fatalf("sequential calls did not publish: summary=%+v calls=%d err=%v", summary, boundary.calls, err)
	}
	if err = records.RecoverMaterialPreparations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if did, err := worker.RunOnce(t.Context()); err != nil || did || boundary.calls != 2 {
		t.Fatalf("successful recovery repeated calls: did=%v calls=%d err=%v", did, boundary.calls, err)
	}
}

func TestMaterialPreparationPhysicalTimeoutRemainsUnknownWithoutReplay(t *testing.T) {
	db, records, worker, doc := materialImportFixture(t, "1. 商店3本书售价18元，7本同样的书一共多少元？\n")
	boundary := &materialControlledSolver{t: t, beforeCall: func(ctx context.Context, _ k12.GradingItemOperation) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	worker.Solver = boundary
	worker.ResolveModel = func(ctx context.Context, prior k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) {
		snapshot, err := materialModelRoute(ctx, prior)
		snapshot.TimeoutMS = 30
		return snapshot, err
	}
	if did, err := worker.RunOnce(t.Context()); !did || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("call timeout: did=%v err=%v", did, err)
	}
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || summary.Counts["outcome_unknown"] != 1 || boundary.calls != 1 {
		t.Fatalf("timeout classification: summary=%+v calls=%d err=%v", summary, boundary.calls, err)
	}
	var status string
	if err = db.QueryRow(`SELECT status FROM k12_material_invocations`).Scan(&status); err != nil || status != "outcome_unknown" {
		t.Fatalf("timeout receipt: status=%s err=%v", status, err)
	}
	if err = records.RecoverMaterialPreparations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if did, err := worker.RunOnce(t.Context()); err != nil || did || boundary.calls != 1 {
		t.Fatalf("unknown recovery repeated calls: did=%v calls=%d err=%v", did, boundary.calls, err)
	}
}
