package usecase

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
	_ "modernc.org/sqlite"
)

type unitUnknownWaitGenerator struct{ calls atomic.Int32 }

func (g *unitUnknownWaitGenerator) ResolveUnitSummary(context.Context, UnitSummaryResolveInput) (UnitSummaryResolution, error) {
	g.calls.Add(1)
	return UnitSummaryResolution{}, errors.New("unexpected resolver call")
}

func (g *unitUnknownWaitGenerator) GenerateUnitSummary(context.Context, UnitSummaryGenerationInput) (k12.UnitSummaryContentV1, error) {
	g.calls.Add(1)
	return k12.UnitSummaryContentV1{}, errors.New("unexpected generation call")
}

func TestUnitSummaryUnknownWaitReturnsOriginalTaskSQLite(t *testing.T) {
	for _, operation := range []string{"start_replay", "resume_replay"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "unit.db")+"?_pragma=foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			if err = migrate.Run(ctx, db, migrate.All); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO agents(name,display_name) VALUES('unit-child','Unit child')`); err != nil {
				t.Fatal(err)
			}
			store := k12storage.NewStore(db, nil)
			req := UnitSummaryRequest{OwnerID: "unit-owner", AgentName: "unit-child", SessionID: "unit-session", SourceMessageID: "unit-message", IdempotencyKey: "unit-original", FinalUserText: "整理当前数学单元"}
			a, _, err := store.CreateUnitSummaryAttempt(ctx, k12.UnitSummaryAttempt{OwnerID: req.OwnerID, AgentName: req.AgentName, SessionID: req.SessionID, SourceMessageID: req.SourceMessageID, IdempotencyKey: req.IdempotencyKey, RequestDigest: k12.UnitSummaryDigest(req), FinalUserText: req.FinalUserText})
			if err != nil {
				t.Fatal(err)
			}
			a, claimed, err := store.ClaimUnitSummaryAttempt(ctx, req.OwnerID, req.AgentName, a.AttemptID, "original-worker", time.Now().Unix()+600)
			if err != nil || !claimed {
				t.Fatalf("claim original task: %v, claimed=%v", err, claimed)
			}
			inv, _, err := store.PrepareUnitSummaryModelInvocation(ctx, k12.ModelInvocation{InvocationID: "modelinv-original", AgentName: req.AgentName, JobID: a.AttemptID, Stage: "unit_summary_generate", RequestDigest: k12.UnitSummaryDigest("original generation"), Attempt: 1, RouteSnapshot: k12.GradingModelSnapshot{Provider: "fixture", Model: "fixture", Route: "fixture/fixture"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.MarkUnitSummaryModelInvocationSent(ctx, req.AgentName, inv.InvocationID, "original-provider-key"); err != nil {
				t.Fatal(err)
			}
			if _, err = store.MarkUnitSummaryModelInvocationOutcomeUnknown(ctx, req.AgentName, inv.InvocationID, "provider_outcome_unknown"); err != nil {
				t.Fatal(err)
			}
			next := a
			next.State, next.Stage, next.InvocationRef = "reconciling", "generate", inv.InvocationID
			a, err = store.CheckpointUnitSummaryAttempt(ctx, a, next)
			if err != nil {
				t.Fatal(err)
			}
			resume := UnitSummaryResumeRequest{IdempotencyKey: "original-resume", ExpectedRevision: a.Revision}
			if operation == "resume_replay" {
				a, _, err = store.ResumeUnitSummaryAttempt(ctx, a, a, resume.IdempotencyKey, k12.UnitSummaryDigest(resume))
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := store.GetUnitSummaryModelInvocation(ctx, req.AgentName, inv.InvocationID)
			if err != nil {
				t.Fatal(err)
			}
			generator := &unitUnknownWaitGenerator{}
			coordinator := NewUnitSummaryCoordinator(Deps{Records: store}, generator, func(context.Context, k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) {
				return k12.GradingModelSnapshot{}, errors.New("unexpected route resolution")
			})
			waitCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
			var view UnitSummaryView
			var replayed bool
			if operation == "start_replay" {
				view, replayed, err = coordinator.StartAndWait(waitCtx, req)
			} else {
				view, replayed, err = coordinator.ResumeAndWait(waitCtx, req.OwnerID, req.AgentName, a.AttemptID, resume)
			}
			if err != nil || !replayed || waitCtx.Err() != nil {
				t.Fatalf("unknown observation waited until cancellation: error=%v, replayed=%v, context=%v", err, replayed, waitCtx.Err())
			}
			if view.Job.State != "reconciling" || view.Job.AttemptID != a.AttemptID || view.Job.InvocationRef != inv.InvocationID || view.Delivery.Complete {
				t.Fatalf("unknown original task changed: %+v", view.Job)
			}
			// 等待原观察启动的本地租约检查结束，不把测试数据库关闭造成的错误当业务结果。
			for deadline := time.Now().Add(time.Second); ; {
				coordinator.mu.Lock()
				active := len(coordinator.active)
				coordinator.mu.Unlock()
				if active == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("observation worker did not release")
				}
				time.Sleep(time.Millisecond)
			}
			after, err := store.GetUnitSummaryModelInvocation(ctx, req.AgentName, inv.InvocationID)
			if err != nil || !reflect.DeepEqual(before, after) || generator.calls.Load() != 0 {
				t.Fatalf("unknown invocation replayed or changed: error=%v, calls=%d", err, generator.calls.Load())
			}
			for _, table := range []string{"k12_unit_summary_model_invocations", "k12_unit_summary_attempts"} {
				var count int
				if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
					t.Fatalf("%s count=%d, error=%v", table, count, err)
				}
			}
		})
	}
}
