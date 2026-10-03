package usecase

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type recognitionRecoveryFixture struct {
	db                           *sql.DB
	store                        *k12storage.Store
	o                            *GradingOrchestrator
	path, dir, jobID, dispatchID string
	image                        []byte
	plan                         k12.RecognitionLayoutPlanV2
	source                       k12.ModelPhysicalInvocation
	parent                       k12.ModelInvocation
	in                           RecognitionRecoveryInput
	before                       string
}

func newRecognitionRecoveryFixture(t *testing.T) *recognitionRecoveryFixture {
	t.Helper()
	ctx := context.Background()
	f := &recognitionRecoveryFixture{path: filepath.Join(t.TempDir(), "recovery.db"), dir: t.TempDir(), dispatchID: "recovery-dispatch"}
	f.db, f.store = openRecognitionPhysicalExecutorV2Store(t, f.path)
	t.Cleanup(func() { _ = f.db.Close() })
	owner := "recovery-owner"
	now := time.Now().Unix()
	if _, err := f.db.Exec(`INSERT INTO agents(name) VALUES(?)`, owner); err != nil {
		t.Fatal(err)
	}
	route := k12.GradingModelSnapshot{Provider: "hexclaw-gpt", Model: "gpt-5.6-luna", Route: "hexclaw-gpt/gpt-5.6-luna", Capability: "vision", TimeoutMS: 120000}
	original := recognitionPhysicalExecutorV2PagePNG(t, 200, 1200)
	page, err := k12.CanonicalizeRecognitionPageV2(original)
	if err != nil {
		t.Fatal(err)
	}
	f.image = page.PNG
	budget := recognitionLayoutInitialV2Budget()
	fields := k12.GradingJobFields{SubmissionID: legacyPhotoSubmissionID(f.image), SourceKind: "image_task", IdempotencyKey: k12.BuildGradingIdempotencyKey("image_task", f.dispatchID, 0), ModelSnapshot: route, BudgetSnapshot: budget, ConfirmationState: k12.GradingConfirmationPending, AnchorState: k12.GradingAnchorPending, Deadline: now + 300, StageCheckpoints: []k12.GradingStageCheckpoint{{Stage: k12.GradingStageNormalizing, ArtifactDigest: "original-image", RecordedAt: now}}}
	rec, err := k12.NewGradingJobRecord(owner, "recovery-session", fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.Put(ctx, rec); err != nil {
		t.Fatal(err)
	}
	f.jobID = rec.RecordID
	f.o = trackGradingOrchestrator(t, NewGradingOrchestrator(Deps{Records: f.store, Now: time.Now().Unix}, nil, WithGradingRunDir(f.dir)))
	run := &gradingRun{agentName: owner, req: PhotoGradeRequest{AgentName: owner, Grade: "六年级上", Image: f.image}}
	f.o.runs[f.jobID] = run
	if err = f.o.persistRun(f.jobID, run); err != nil {
		t.Fatal(err)
	}
	job, err := f.o.deps.GetGradingJob(ctx, owner, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	f.parent, err = f.o.beginRecognizingLayoutModelInvocationV2(ctx, job, f.image, recognizingInvocationDigest(f.image, route, k12.ModelRequestPolicySnapshot{}), k12.ModelRequestPolicySnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := f.store.LoadRecognitionLayoutPlanRuntimeV2(ctx, owner, f.parent.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.store.ClaimModelPhysicalInvocationSent(ctx, owner, runtime.ManifestPhysicalInvocationID); err != nil || !ok {
		t.Fatalf("manifest claim %v %v", ok, err)
	}
	manifest, err := f.store.MarkModelPhysicalInvocationSucceededWithContent(ctx, owner, runtime.ManifestPhysicalInvocationID, `{"frozen_manifest":"twenty-four-targets"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	targets := make([]k12.RecognitionLayoutManifestTargetV2, 24)
	for i := range targets {
		targets[i] = k12.RecognitionLayoutManifestTargetV2{ManifestRef: fmt.Sprintf("manifest_%04d", i+1), ManifestOrder: i + 1, DisplayLabel: fmt.Sprint(i + 1), SourceNumberPath: []string{fmt.Sprint(i + 1)}, Region: k12.SourcePixelRegion{X: 0, Y: i * 50, Width: 200, Height: 40}}
	}
	f.plan, err = k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{PagePNG: f.image, Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: manifest.PhysicalInvocationID, ResultDigest: manifest.ResultDigest}, Targets: targets, RecognitionFormat: k12.RecognitionLayoutCompactV4})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.plan.Batches) != 6 {
		t.Fatalf("fixture requires six batches, got %d", len(f.plan.Batches))
	}
	if err = f.store.AuthorizeRecognitionLayoutPlanV2(ctx, owner, f.parent.InvocationID, k12.RecognitionLayoutManifestSuccessV2{InvocationID: manifest.PhysicalInvocationID, ResultDigest: manifest.ResultDigest}, f.plan); err != nil {
		t.Fatal(err)
	}
	executor := newDurableRecognitionPhysicalCallExecutor(f.o, f.parent)
	for i, batch := range f.plan.Batches {
		call := f.call(t, f.plan, batch)
		result, err := executor.ExecuteRecognitionPhysicalCall(ctx, call, func(context.Context) (string, error) {
			if i == 4 {
				return "", context.DeadlineExceeded
			}
			return `{"source":"original-success"}`, nil
		})
		if i == 4 {
			if err == nil {
				t.Fatal("unknown fixture unexpectedly succeeded")
			}
			id, e := stableRecognitionPhysicalInvocationIDForCall(f.parent.InvocationID, call)
			if e != nil {
				t.Fatal(e)
			}
			f.source, e = f.store.GetModelPhysicalInvocation(ctx, owner, id)
			if e != nil {
				t.Fatal(e)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			f.settle(t, f.parent, f.plan, batch, result)
		}
	}
	if _, err = f.store.MarkModelInvocationOutcomeUnknown(ctx, owner, f.parent.InvocationID, "provider_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`UPDATE k12_grading_jobs SET status='outcome_unknown',failure_kind='provider_outcome_unknown',failed_stage='recognizing',retryable=0,deadline=0 WHERE record_id=?`, f.jobID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`INSERT INTO k12_image_task_dispatches(dispatch_id,agent_name,learner_id,source_kind,source_ref,source_asset_refs_json,source_digest,task_intent,status,target_object_type,target_object_id,classification_route_snapshot_json,classification_invocation_id,route_policy_snapshot_json,idempotency_key,request_digest,created_at,updated_at) VALUES(?,?,'learner','desktop','fixture','[]','source','completed_homework','routed','homework_submission','recovery-homework','{}','classification','{}','dispatch-key','dispatch-request',?,?)`, f.dispatchID, owner, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`INSERT INTO k12_homework_submissions(submission_id,dispatch_id,agent_name,learner_id,source_kind,source_ref,source_asset_refs_json,task_intent,status,grading_job_id,idempotency_key,created_at,updated_at) VALUES('recovery-homework',?,?,'learner','desktop','fixture','[]','completed_homework','processing',?,'homework-key',?,?)`, f.dispatchID, owner, f.jobID, now, now); err != nil {
		t.Fatal(err)
	}
	f.in = RecognitionRecoveryInput{Agent: owner, Version: 0, JobVersion: 0, SourcePhysicalID: f.source.PhysicalInvocationID, IdempotencyKey: "recover-once", AcceptDuplicateExecution: true}
	current, err := f.o.deps.GetGradingJob(ctx, owner, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	f.in.JobVersion = current.Record.Version
	f.before = f.originalReceipts(t)
	return f
}

func (f *recognitionRecoveryFixture) call(t *testing.T, plan k12.RecognitionLayoutPlanV2, batch k12.RecognitionLayoutBatchV2) k12.RecognitionPhysicalCall {
	t.Helper()
	image, err := k12.BuildRecognitionLayoutBatchImageV2(f.image, plan, batch.Unit)
	if err != nil {
		t.Fatal(err)
	}
	return k12.RecognitionPhysicalCall{PlanVersion: k12.RecognitionPlanVersionV2, PlanDigest: plan.AuthorizedPlanDigest, Unit: batch.Unit, TargetIDs: batch.TargetIDs, Image: image}
}

func (f *recognitionRecoveryFixture) settle(t *testing.T, parent k12.ModelInvocation, plan k12.RecognitionLayoutPlanV2, batch k12.RecognitionLayoutBatchV2, result k12.RecognitionPhysicalCallResult) {
	t.Helper()
	candidates := make([]k12.RecognitionLayoutCandidateSettlementV2, len(batch.TargetIDs))
	for i, id := range batch.TargetIDs {
		candidates[i] = k12.RecognitionLayoutCandidateSettlementV2{CandidateID: id, Classification: k12.RecognitionLayoutCandidateValidV2, ResultKind: k12.RecognitionLayoutCandidateQuestionV2, ResultJSON: json.RawMessage(`{"student_answer":"4","text":"2+2="}`)}
	}
	_, _, err := f.store.SettleRecognitionLayoutPrimaryBatchV2(context.Background(), parent.AgentName, parent.InvocationID, k12.RecognitionLayoutPrimaryBatchSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, SourcePhysicalInvocationID: result.InvocationID, SourcePhysicalUnit: batch.Unit, SourcePhysicalResultDigest: result.ResultDigest, Classification: k12.RecognitionLayoutBatchClassifiedV2, Candidates: candidates})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *recognitionRecoveryFixture) originalReceipts(t *testing.T) string {
	t.Helper()
	rows, err := f.db.Query(`SELECT physical_invocation_id,status,result_digest,coalesce(result_content,''),failure_kind,created_at,updated_at,route_snapshot_json,request_policy_snapshot_json,effective_timeout_ms FROM k12_model_physical_invocations WHERE parent_invocation_id=? ORDER BY physical_unit`, f.parent.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var data [][]any
	for rows.Next() {
		var id, status, digest, content, failure string
		var created, updated, effective int64
		var route, policy string
		if err = rows.Scan(&id, &status, &digest, &content, &failure, &created, &updated, &route, &policy, &effective); err != nil {
			t.Fatal(err)
		}
		data = append(data, []any{id, status, digest, content, failure, created, updated, route, policy, effective})
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(data)
	return string(raw)
}

func (f *recognitionRecoveryFixture) begin(t *testing.T) (k12.ModelInvocation, k12.RecognitionLayoutPlanV2) {
	t.Helper()
	ctx := context.Background()
	job, err := f.o.deps.GetGradingJob(ctx, f.in.Agent, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := f.o.beginRecognizingLayoutModelInvocationV2(ctx, job, f.image, recognizingInvocationDigest(f.image, job.Fields.ModelSnapshot, f.parent.RequestPolicySnapshot), f.parent.RequestPolicySnapshot)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := f.store.LoadRecognitionLayoutPlanRuntimeV2(ctx, f.in.Agent, parent.InvocationID)
	if err != nil || runtime.AuthorizedPlan == nil {
		t.Fatalf("recovery runtime %v", err)
	}
	return parent, *runtime.AuthorizedPlan
}

func (f *recognitionRecoveryFixture) reopen(t *testing.T) {
	t.Helper()
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, f.store = openRecognitionPhysicalExecutorV2Store(t, f.path)
	f.o = trackGradingOrchestrator(t, NewGradingOrchestrator(Deps{Records: f.store, Now: time.Now().Unix}, nil, WithGradingRunDir(f.dir)))
	if _, err := f.o.ensureRun(context.Background(), f.jobID); err != nil {
		t.Fatalf("restore actual persisted runtime: %v", err)
	}
}

func TestRecognitionRecoverySuccessReuse(t *testing.T) {
	f := newRecognitionRecoveryFixture(t)
	ctx := context.Background()
	wrong := f.in
	wrong.Agent = "other-owner"
	if _, err := f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, wrong); err == nil {
		t.Fatal("cross-owner recovery accepted")
	}
	originalImage := f.o.runs[f.jobID].req.Image
	f.o.runs[f.jobID].req.Image = recognitionPhysicalExecutorV2PagePNG(t, 200, 1201)
	if _, err := f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, f.in); err == nil {
		t.Fatal("changed original image accepted")
	}
	f.o.runs[f.jobID].req.Image = originalImage
	accepted, err := f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, f.in)
	if err != nil || accepted.Replayed {
		t.Fatalf("authorize: %+v %v", accepted, err)
	}
	parent, plan := f.begin(t)
	if plan.SourceAdjudication != f.plan.SourceAdjudication || plan.RecognitionFormat != f.plan.RecognitionFormat {
		t.Fatal("frozen recognition contract changed")
	}
	executor := newDurableRecognitionPhysicalCallExecutor(f.o, parent)
	calls := map[k12.RecognitionPhysicalUnit]int{}
	for _, batch := range plan.Batches {
		result, err := executor.ExecuteRecognitionPhysicalCall(ctx, f.call(t, plan, batch), func(context.Context) (string, error) { calls[batch.Unit]++; return `{"source":"replacement"}`, nil })
		if err != nil {
			t.Fatal(err)
		}
		f.settle(t, parent, plan, batch, result)
	}
	if !reflect.DeepEqual(calls, map[k12.RecognitionPhysicalUnit]int{"layout_batch_0005": 1}) {
		t.Fatalf("unexpected provider calls: %v", calls)
	}
	final, created, err := f.store.FinalizeRecognitionLayoutPlanV2(ctx, f.in.Agent, parent.InvocationID)
	if err != nil || !created {
		t.Fatalf("finalize exact set: %+v created=%v err=%v", final, created, err)
	}
	if after := f.originalReceipts(t); after != f.before {
		t.Fatal("historical physical receipts changed")
	}
	prior, err := f.store.GetModelInvocation(ctx, f.in.Agent, f.parent.InvocationID)
	if err != nil || prior.Status != k12.ModelInvocationOutcomeUnknown {
		t.Fatalf("old parent changed: %+v %v", prior, err)
	}
	if replaced, err := f.store.RecognitionUnknownHasReplacement(ctx, f.in.Agent, f.source.PhysicalInvocationID); err != nil || !replaced {
		t.Fatalf("successful replacement not recognized: %v %v", replaced, err)
	}
}

func TestRecognitionRecoveryIdempotentColdResume(t *testing.T) {
	f := newRecognitionRecoveryFixture(t)
	ctx := context.Background()
	first, err := f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, f.in)
	if err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	replay, err := f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, f.in)
	if err != nil || !replay.Replayed || replay.Authorization != first.Authorization {
		t.Fatalf("cold authorization replay: %+v %v", replay, err)
	}
	changed := f.in
	changed.JobVersion++
	if _, err := f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, changed); err == nil {
		t.Fatal("same key changed request accepted")
	}
	f.begin(t)
	f.reopen(t)
	parent, plan := f.begin(t)
	batch := plan.Batches[4]
	calls := 0
	executor := newDurableRecognitionPhysicalCallExecutor(f.o, parent)
	result, err := executor.ExecuteRecognitionPhysicalCall(ctx, f.call(t, plan, batch), func(context.Context) (string, error) { calls++; return `{"source":"only-new-send"}`, nil })
	if err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	parent, plan = f.begin(t)
	executor = newDurableRecognitionPhysicalCallExecutor(f.o, parent)
	repeated, err := executor.ExecuteRecognitionPhysicalCall(ctx, f.call(t, plan, plan.Batches[4]), func(context.Context) (string, error) { calls++; return "", errors.New("unexpected resend") })
	if err != nil || repeated != result || calls != 1 {
		t.Fatalf("cold result replay: %+v calls=%d err=%v", repeated, calls, err)
	}
	var count int
	if err = f.db.QueryRow(`SELECT count(*) FROM k12_recognition_recovery_authorizations WHERE job_id=?`, f.jobID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("authorization count=%d err=%v", count, err)
	}
	if after := f.originalReceipts(t); after != f.before {
		t.Fatal("historical receipts changed on replay")
	}
}

func TestRecognitionRecoveryNewUnknownStops(t *testing.T) {
	f := newRecognitionRecoveryFixture(t)
	ctx := context.Background()
	accepted, err := f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, f.in)
	if err != nil {
		t.Fatal(err)
	}
	parent, plan := f.begin(t)
	calls := 0
	executor := newDurableRecognitionPhysicalCallExecutor(f.o, parent)
	_, err = executor.ExecuteRecognitionPhysicalCall(ctx, f.call(t, plan, plan.Batches[4]), func(context.Context) (string, error) { calls++; return "", context.DeadlineExceeded })
	if err == nil {
		t.Fatal("unknown physical outcome reported success")
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
	if recovered, err := f.o.RecoverGradingJobs(ctx, []string{f.in.Agent}); err != nil || recovered != 1 {
		t.Fatalf("cold unknown recovery: recovered=%d err=%v", recovered, err)
	}
	replay, err := f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, f.in)
	if err != nil || !replay.Replayed || replay.Status != "outcome_unknown" {
		t.Fatalf("unknown replay: %+v %v", replay, err)
	}
	if _, err = f.o.deps.RetryGradingJob(ctx, f.in.Agent, f.jobID); err == nil {
		t.Fatal("ordinary retry accepted unknown")
	}
	parent, err = f.store.GetModelInvocation(ctx, f.in.Agent, accepted.Authorization.NewParentID)
	if err != nil {
		t.Fatal(err)
	}
	executor = newDurableRecognitionPhysicalCallExecutor(f.o, parent)
	if _, err = executor.ExecuteRecognitionPhysicalCall(ctx, f.call(t, plan, plan.Batches[4]), func(context.Context) (string, error) { calls++; return "", nil }); err == nil {
		t.Fatal("cold unknown execution unexpectedly allowed")
	}
	if calls != 1 {
		t.Fatalf("unknown repeated provider call: %d", calls)
	}
	if replaced, err := f.store.RecognitionUnknownHasReplacement(ctx, f.in.Agent, f.source.PhysicalInvocationID); err != nil || replaced {
		t.Fatalf("new unknown masked old unknown: %v %v", replaced, err)
	}
	if after := f.originalReceipts(t); after != f.before {
		t.Fatal("historical receipts changed after unknown")
	}
}

func TestRecognitionRecoveryOriginalPlanRepairAllowed(t *testing.T) {
	f := newRecognitionRecoveryFixture(t)
	ctx := context.Background()
	if _, err := f.o.AuthorizeRecognitionRecovery(ctx, f.dispatchID, f.in); err != nil {
		t.Fatal(err)
	}
	parent, plan := f.begin(t)
	executor := newDurableRecognitionPhysicalCallExecutor(f.o, parent)
	batch := plan.Batches[4]
	source, err := executor.ExecuteRecognitionPhysicalCall(ctx, f.call(t, plan, batch), func(context.Context) (string, error) { return `{"source":"replacement-with-one-missing"}`, nil })
	if err != nil {
		t.Fatal(err)
	}
	candidates := make([]k12.RecognitionLayoutCandidateSettlementV2, len(batch.TargetIDs))
	for i, id := range batch.TargetIDs {
		candidates[i] = k12.RecognitionLayoutCandidateSettlementV2{CandidateID: id, Classification: k12.RecognitionLayoutCandidateValidV2, ResultKind: k12.RecognitionLayoutCandidateQuestionV2, ResultJSON: json.RawMessage(`{"student_answer":"4","text":"2+2="}`)}
	}
	candidates[0] = k12.RecognitionLayoutCandidateSettlementV2{CandidateID: batch.TargetIDs[0], Classification: k12.RecognitionLayoutCandidateMissingV2}
	settled, _, err := f.store.SettleRecognitionLayoutPrimaryBatchV2(ctx, f.in.Agent, parent.InvocationID, k12.RecognitionLayoutPrimaryBatchSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, SourcePhysicalInvocationID: source.InvocationID, SourcePhysicalUnit: batch.Unit, SourcePhysicalResultDigest: source.ResultDigest, Classification: k12.RecognitionLayoutBatchClassifiedV2, Candidates: candidates})
	if err != nil || len(settled.RepairAuthorizations) != 1 {
		t.Fatalf("original plan repair authorization: %+v %v", settled, err)
	}
	image, err := k12.BuildRecognitionLayoutRepairImageV2(f.image, plan, batch.TargetIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	unit, err := k12.RecognitionLayoutRepairUnitV2(17)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = executor.ExecuteRecognitionPhysicalCall(ctx, k12.RecognitionPhysicalCall{PlanVersion: k12.RecognitionPlanVersionV2, PlanDigest: plan.AuthorizedPlanDigest, Unit: unit, TargetIDs: []string{batch.TargetIDs[0]}, Image: image}, func(context.Context) (string, error) { calls++; return `{"source":"normal-original-plan-repair"}`, nil })
	if err != nil || calls != 1 {
		t.Fatalf("normal pending repair blocked: calls=%d err=%v", calls, err)
	}
	if after := f.originalReceipts(t); after != f.before {
		t.Fatal("repair changed historical receipts")
	}
}
