package usecase

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 保留第一轮替代未知；故意不复制其他批，验证下一轮沿授权链读取原成功来源。
func recognitionRecoveryUnknownPredecessor(t *testing.T, f *recognitionRecoveryFixture) RecognitionRecoveryResult {
	t.Helper()
	ctx := context.Background()
	accepted, err := f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, f.in)
	if err != nil {
		t.Fatal(err)
	}
	parent, plan := f.begin(t)
	executor := newDurableRecognitionPhysicalCallExecutor(f.o, parent)
	_, err = executor.ExecuteRecognitionPhysicalCall(ctx, f.call(t, plan, plan.Batches[4]), func(context.Context) (string, error) { return "", context.DeadlineExceeded })
	if err == nil {
		t.Fatal("predecessor unexpectedly succeeded")
	}
	if _, err = f.store.MarkModelInvocationOutcomeUnknown(ctx, f.in.Agent, parent.InvocationID, "provider_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	run, err := f.o.ensureRun(ctx, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.o.markGradingOutcomeUnknown(ctx, run, f.jobID, "provider_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	dispatch, err := f.store.GetImageTaskDispatch(ctx, f.in.Agent, f.dispatchID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.o.deps.GetGradingJob(ctx, f.in.Agent, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	f.in.Version = dispatch.Version
	f.in.JobVersion = job.Record.Version
	f.in.SourcePhysicalID = accepted.Authorization.NewPhysicalID
	f.in.IdempotencyKey = "second-recovery-with-bounded-wait"
	f.in.TimeoutOverrideMS = 180000
	return accepted
}

func TestRecognitionRecoveryTimeoutScopedSuccessAndReplay(t *testing.T) {
	f := newRecognitionRecoveryFixture(t)
	ctx := context.Background()
	first := recognitionRecoveryUnknownPredecessor(t, f)
	old, err := f.store.GetModelPhysicalInvocation(ctx, f.in.Agent, first.Authorization.NewPhysicalID)
	if err != nil {
		t.Fatal(err)
	}
	invalid := f.in
	invalid.TimeoutOverrideMS = 240000
	if _, err = f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, invalid); err == nil {
		t.Fatal("unapproved timeout accepted")
	}
	f.reopen(t)
	accepted, err := f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, f.in)
	if err != nil {
		t.Fatal(err)
	}
	changedTimeout := f.in
	changedTimeout.TimeoutOverrideMS = 0
	if _, err = f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, changedTimeout); err == nil {
		t.Fatal("same-key timeout change accepted")
	}
	if accepted.Authorization.SourceTimeoutMS != 120000 || accepted.Authorization.TimeoutOverrideMS != 180000 {
		t.Fatalf("wrong frozen timeout: %+v", accepted.Authorization)
	}
	f.reopen(t)
	parent, plan := f.begin(t)
	executor := newDurableRecognitionPhysicalCallExecutor(f.o, parent)
	runtime, err := executor.LoadRecognitionLayoutPlanV2Runtime(ctx)
	if err != nil || runtime.RecoveryPhysicalUnit != "layout_batch_0005" || runtime.RecoveryTimeoutOverrideMS != 180000 || runtime.Header.PhysicalCallCapMillis != 120000 {
		t.Fatalf("timeout scope runtime: %+v %v", runtime, err)
	}
	if parent.RouteSnapshot != f.parent.RouteSnapshot || parent.RequestPolicySnapshot != f.parent.RequestPolicySnapshot {
		t.Fatal("parent model/policy changed")
	}
	badCall := f.call(t, plan, plan.Batches[4])
	badCall.Image = append([]byte(nil), badCall.Image...)
	badCall.Image[len(badCall.Image)-1] ^= 1
	forbiddenSends := 0
	if _, err = executor.ExecuteRecognitionPhysicalCall(ctx, badCall, func(context.Context) (string, error) { forbiddenSends++; return "", nil }); err == nil {
		t.Fatal("changed frozen pixels accepted")
	}
	changedParent := parent
	changedParent.RouteSnapshot.Model = "different-model"
	changed := newDurableRecognitionPhysicalCallExecutor(f.o, changedParent)
	if _, err = changed.ExecuteRecognitionPhysicalCall(ctx, f.call(t, plan, plan.Batches[4]), func(context.Context) (string, error) { forbiddenSends++; return "", nil }); err == nil {
		t.Fatal("changed frozen model accepted")
	}
	if forbiddenSends != 0 {
		t.Fatal("identity change reached provider")
	}
	calls := map[k12.RecognitionPhysicalUnit]int{}
	for _, batch := range plan.Batches {
		result, err := executor.ExecuteRecognitionPhysicalCall(ctx, f.call(t, plan, batch), func(sendCtx context.Context) (string, error) {
			calls[batch.Unit]++
			snapshot, ok := k12.GradingModelSnapshotFromContext(sendCtx)
			want := f.parent.RouteSnapshot
			want.TimeoutMS = 180000
			deadline, bounded := sendCtx.Deadline()
			if !ok || snapshot != want || !bounded || time.Until(deadline) < 179*time.Second || time.Until(deadline) > 180*time.Second {
				t.Fatalf("effective send policy: %+v deadline=%v", snapshot, deadline)
			}
			return `{"source":"second-recovery"}`, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		f.settle(t, parent, plan, batch, result)
		child, err := f.store.GetModelPhysicalInvocation(ctx, f.in.Agent, result.InvocationID)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Unit == "layout_batch_0005" {
			if child.EffectiveTimeoutMS != 180000 {
				t.Fatal("new effective policy was not persisted")
			}
		} else if child.EffectiveTimeoutMS != 0 || child.ReusedFromPhysicalInvocationID == "" {
			t.Fatal("successful source was resent or given an override")
		}
	}
	if !reflect.DeepEqual(calls, map[k12.RecognitionPhysicalUnit]int{"layout_batch_0005": 1}) {
		t.Fatalf("unexpected sends: %v", calls)
	}
	f.reopen(t)
	replay, err := f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, f.in)
	if err != nil || !replay.Replayed || replay.Authorization != accepted.Authorization || replay.Status != "succeeded" {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	storedParent, err := f.store.GetModelInvocation(ctx, f.in.Agent, accepted.Authorization.NewParentID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = newDurableRecognitionPhysicalCallExecutor(f.o, storedParent).ExecuteRecognitionPhysicalCall(ctx, f.call(t, plan, plan.Batches[4]), func(context.Context) (string, error) {
		forbiddenSends++
		return "", errors.New("unexpected replay send")
	})
	if err != nil || forbiddenSends != 0 {
		t.Fatalf("cold succeeded replay resent: %d %v", forbiddenSends, err)
	}
	if _, _, err = f.store.FinalizeRecognitionLayoutPlanV2(ctx, f.in.Agent, parent.InvocationID); err != nil {
		t.Fatal(err)
	}
	for _, sourceID := range []string{f.source.PhysicalInvocationID, first.Authorization.NewPhysicalID} {
		if replaced, err := f.store.RecognitionUnknownHasReplacement(ctx, f.in.Agent, sourceID); err != nil || !replaced {
			t.Fatalf("successor chain: %v %v", replaced, err)
		}
	}
	stillOld, err := f.store.GetModelPhysicalInvocation(ctx, f.in.Agent, first.Authorization.NewPhysicalID)
	if err != nil || stillOld != old || f.originalReceipts(t) != f.before {
		t.Fatal("old 120-second receipts changed")
	}
	if _, err = f.db.Exec(`UPDATE k12_model_physical_invocations SET effective_timeout_ms=0 WHERE physical_invocation_id=?`, accepted.Authorization.NewPhysicalID); err == nil {
		t.Fatal("effective policy was mutable")
	}
}

func TestRecognitionRecoveryTimeoutUnknownColdStops(t *testing.T) {
	f := newRecognitionRecoveryFixture(t)
	ctx := context.Background()
	first := recognitionRecoveryUnknownPredecessor(t, f)
	accepted, err := f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, f.in)
	if err != nil {
		t.Fatal(err)
	}
	parent, plan := f.begin(t)
	executor := newDurableRecognitionPhysicalCallExecutor(f.o, parent)
	calls := 0
	_, err = executor.ExecuteRecognitionPhysicalCall(ctx, f.call(t, plan, plan.Batches[4]), func(context.Context) (string, error) { calls++; return "", context.DeadlineExceeded })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unknown failure: %v", err)
	}
	if _, err = f.store.MarkModelInvocationOutcomeUnknown(ctx, f.in.Agent, parent.InvocationID, "provider_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	run, err := f.o.ensureRun(ctx, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.o.markGradingOutcomeUnknown(ctx, run, f.jobID, "provider_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	if count, err := f.o.RecoverGradingJobs(ctx, []string{f.in.Agent}); err != nil || count != 1 {
		t.Fatalf("cold recovery: %d %v", count, err)
	}
	replay, err := f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, f.in)
	if err != nil || !replay.Replayed || replay.Status != "outcome_unknown" {
		t.Fatalf("unknown replay: %+v %v", replay, err)
	}
	if _, err = f.o.deps.RetryGradingJob(ctx, f.in.Agent, f.jobID); err == nil {
		t.Fatal("ordinary retry allowed unknown")
	}
	parent, err = f.store.GetModelInvocation(ctx, f.in.Agent, accepted.Authorization.NewParentID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = newDurableRecognitionPhysicalCallExecutor(f.o, parent).ExecuteRecognitionPhysicalCall(ctx, f.call(t, plan, plan.Batches[4]), func(context.Context) (string, error) { calls++; return "", nil })
	if err == nil || calls != 1 {
		t.Fatalf("unknown resent: calls=%d err=%v", calls, err)
	}
	for _, sourceID := range []string{f.source.PhysicalInvocationID, first.Authorization.NewPhysicalID} {
		if replaced, err := f.store.RecognitionUnknownHasReplacement(ctx, f.in.Agent, sourceID); err != nil || replaced {
			t.Fatalf("unknown chain hidden: %v %v", replaced, err)
		}
	}
	if f.originalReceipts(t) != f.before {
		t.Fatal("old source receipts changed")
	}
}
