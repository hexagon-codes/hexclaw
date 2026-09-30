package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type recognitionRepairRecoveryFixture struct {
	*recognitionRecoveryFixture
	repairs map[k12.RecognitionPhysicalUnit]k12.RecognitionLayoutRepairAuthorizationV2
}

const recognitionRepairStudentResult = `{"student_answer":"300÷2÷2=50","text":"一个周长是300米的长方形鱼塘，长是宽的2倍，求宽。"}`

func newRecognitionRepairRecoveryFixture(t *testing.T) *recognitionRepairRecoveryFixture {
	t.Helper()
	ctx := context.Background()
	f := &recognitionRecoveryFixture{path: filepath.Join(t.TempDir(), "repair-recovery.db"), dir: t.TempDir(), dispatchID: "repair-recovery-dispatch"}
	f.db, f.store = openRecognitionPhysicalExecutorV2Store(t, f.path)
	t.Cleanup(func() { _ = f.db.Close() })
	owner := "repair-recovery-owner"
	if _, err := f.db.Exec(`INSERT INTO agents(name) VALUES(?)`, owner); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	route := k12.GradingModelSnapshot{Provider: "hexclaw-gpt", Model: "gpt-6-sol", Route: "hexclaw-gpt/gpt-6-sol", Capability: "vision", TimeoutMS: 120000}
	page, err := k12.CanonicalizeRecognitionPageV2(recognitionPhysicalExecutorV2PagePNG(t, 200, 3200))
	if err != nil {
		t.Fatal(err)
	}
	f.image = page.PNG
	fields := k12.GradingJobFields{SubmissionID: legacyPhotoSubmissionID(f.image), SourceKind: "image_task", IdempotencyKey: k12.BuildGradingIdempotencyKey("image_task", f.dispatchID, 0), ModelSnapshot: route, BudgetSnapshot: recognitionLayoutInitialV2Budget(), ConfirmationState: k12.GradingConfirmationPending, AnchorState: k12.GradingAnchorPending, Deadline: now + 300, StageCheckpoints: []k12.GradingStageCheckpoint{{Stage: k12.GradingStageNormalizing, ArtifactDigest: "original-image", RecordedAt: now}}}
	rec, err := k12.NewGradingJobRecord(owner, "repair-recovery-session", fields)
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
	if _, claimed, claimErr := f.store.ClaimModelPhysicalInvocationSent(ctx, owner, runtime.ManifestPhysicalInvocationID); claimErr != nil || !claimed {
		t.Fatalf("claim manifest: claimed=%v err=%v", claimed, claimErr)
	}
	manifest, err := f.store.MarkModelPhysicalInvocationSucceededWithContent(ctx, owner, runtime.ManifestPhysicalInvocationID, `{"fixture_targets":16}`, "")
	if err != nil {
		t.Fatal(err)
	}
	targets := make([]k12.RecognitionLayoutManifestTargetV2, 16)
	for i := range targets {
		region := k12.SourcePixelRegion{X: 0, Y: i * 230, Width: 200, Height: 210}
		if i >= 12 {
			region.Y, region.Height = 2760+(i-12)*70, 40
		}
		targets[i] = k12.RecognitionLayoutManifestTargetV2{ManifestRef: fmt.Sprintf("manifest_%04d", i+1), ManifestOrder: i + 1, DisplayLabel: fmt.Sprint(i + 1), SourceNumberPath: []string{fmt.Sprint(i + 1)}, Region: region}
	}
	f.plan, err = k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{PagePNG: f.image, Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: manifest.PhysicalInvocationID, ResultDigest: manifest.ResultDigest}, Targets: targets, RecognitionFormat: k12.RecognitionLayoutCompactV4})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.plan.Targets) != 16 || len(f.plan.Batches) != 5 {
		t.Fatalf("fixture layout: targets=%d batches=%d", len(f.plan.Targets), len(f.plan.Batches))
	}
	if err = f.store.AuthorizeRecognitionLayoutPlanV2(ctx, owner, f.parent.InvocationID, k12.RecognitionLayoutManifestSuccessV2{InvocationID: manifest.PhysicalInvocationID, ResultDigest: manifest.ResultDigest}, f.plan); err != nil {
		t.Fatal(err)
	}
	r := &recognitionRepairRecoveryFixture{recognitionRecoveryFixture: f, repairs: make(map[k12.RecognitionPhysicalUnit]k12.RecognitionLayoutRepairAuthorizationV2)}
	executor := newDurableRecognitionPhysicalCallExecutor(f.o, f.parent)
	for i, batch := range f.plan.Batches {
		source, executeErr := executor.ExecuteRecognitionPhysicalCall(ctx, f.call(t, f.plan, batch), func(context.Context) (string, error) { return fmt.Sprintf(`{"fixture_batch":%d}`, i+1), nil })
		if executeErr != nil {
			t.Fatal(executeErr)
		}
		for unit, auth := range r.settlePrimary(t, f.parent, f.plan, batch, source) {
			r.repairs[unit] = auth
		}
	}
	if len(r.repairs) != 4 {
		t.Fatalf("repair authorization count=%d, want 4", len(r.repairs))
	}
	for _, ordinal := range []int{13, 14, 15, 16} {
		unit, unitErr := k12.RecognitionLayoutRepairUnitV2(ordinal)
		if unitErr != nil {
			t.Fatal(unitErr)
		}
		call := r.repairCall(t, f.plan, r.repairs[unit])
		source, executeErr := executor.ExecuteRecognitionPhysicalCall(ctx, call, func(context.Context) (string, error) {
			if ordinal == 15 {
				return "", context.DeadlineExceeded
			}
			return `{"student_answer":"4","text":"2+2="}`, nil
		})
		if ordinal == 15 {
			if executeErr == nil {
				t.Fatal("unknown repair reported success")
			}
			id, idErr := stableRecognitionPhysicalInvocationIDForCall(f.parent.InvocationID, call)
			if idErr != nil {
				t.Fatal(idErr)
			}
			f.source, err = f.store.GetModelPhysicalInvocation(ctx, owner, id)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			if executeErr != nil {
				t.Fatal(executeErr)
			}
			r.settleRepair(t, f.parent, f.plan, r.repairs[unit], source, `{"student_answer":"4","text":"2+2="}`)
		}
	}
	if _, err = f.store.MarkModelInvocationOutcomeUnknown(ctx, owner, f.parent.InvocationID, "provider_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`UPDATE k12_grading_jobs SET status='outcome_unknown',failure_kind='provider_outcome_unknown',failed_stage='recognizing',retryable=0,deadline=0 WHERE record_id=?`, f.jobID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`INSERT INTO k12_image_task_dispatches(dispatch_id,agent_name,learner_id,source_kind,source_ref,source_asset_refs_json,source_digest,task_intent,status,target_object_type,target_object_id,classification_route_snapshot_json,classification_invocation_id,route_policy_snapshot_json,idempotency_key,request_digest,created_at,updated_at) VALUES(?,?,'learner','desktop','fixture','[]','source','completed_homework','routed','homework_submission','repair-recovery-homework','{}','classification','{}','dispatch-key','dispatch-request',?,?)`, f.dispatchID, owner, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`INSERT INTO k12_homework_submissions(submission_id,dispatch_id,agent_name,learner_id,source_kind,source_ref,source_asset_refs_json,task_intent,status,grading_job_id,idempotency_key,created_at,updated_at) VALUES('repair-recovery-homework',?,?,'learner','desktop','fixture','[]','completed_homework','processing',?,'homework-key',?,?)`, f.dispatchID, owner, f.jobID, now, now); err != nil {
		t.Fatal(err)
	}
	current, err := f.o.deps.GetGradingJob(ctx, owner, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	f.in = RecognitionRecoveryInput{Agent: owner, JobVersion: current.Record.Version, SourcePhysicalID: f.source.PhysicalInvocationID, IdempotencyKey: "repair-recover-once", AcceptDuplicateExecution: true}
	if f.source.EffectiveTimeoutMS != 0 || f.source.RequestPolicySnapshot != f.parent.RequestPolicySnapshot {
		t.Fatalf("original repair changed frozen request policy: timeout=%d policy=%+v", f.source.EffectiveTimeoutMS, f.source.RequestPolicySnapshot)
	}
	f.before = f.originalReceipts(t)
	var succeeded int
	if err = f.db.QueryRow(`SELECT count(*) FROM k12_model_physical_invocations WHERE parent_invocation_id=? AND status='succeeded'`, f.parent.InvocationID).Scan(&succeeded); err != nil || succeeded != 9 {
		t.Fatalf("original success count=%d, want 9; err=%v", succeeded, err)
	}
	return r
}

func (r *recognitionRepairRecoveryFixture) settlePrimary(t *testing.T, parent k12.ModelInvocation, plan k12.RecognitionLayoutPlanV2, batch k12.RecognitionLayoutBatchV2, source k12.RecognitionPhysicalCallResult) map[k12.RecognitionPhysicalUnit]k12.RecognitionLayoutRepairAuthorizationV2 {
	t.Helper()
	candidates := make([]k12.RecognitionLayoutCandidateSettlementV2, len(batch.TargetIDs))
	for i, id := range batch.TargetIDs {
		candidates[i] = k12.RecognitionLayoutCandidateSettlementV2{CandidateID: id, Classification: k12.RecognitionLayoutCandidateValidV2, ResultKind: k12.RecognitionLayoutCandidateQuestionV2, ResultJSON: json.RawMessage(`{"student_answer":"4","text":"2+2="}`)}
		for _, target := range plan.Targets[12:] {
			if id == target.TargetID {
				candidates[i] = k12.RecognitionLayoutCandidateSettlementV2{CandidateID: id, Classification: k12.RecognitionLayoutCandidateMissingV2}
			}
		}
	}
	projection, _, err := r.store.SettleRecognitionLayoutPrimaryBatchV2(context.Background(), parent.AgentName, parent.InvocationID, k12.RecognitionLayoutPrimaryBatchSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, SourcePhysicalInvocationID: source.InvocationID, SourcePhysicalUnit: batch.Unit, SourcePhysicalResultDigest: source.ResultDigest, Classification: k12.RecognitionLayoutBatchClassifiedV2, Candidates: candidates})
	if err != nil {
		t.Fatal(err)
	}
	auths := make(map[k12.RecognitionPhysicalUnit]k12.RecognitionLayoutRepairAuthorizationV2)
	for _, auth := range projection.RepairAuthorizations {
		auths[auth.PhysicalUnit] = auth
	}
	return auths
}

func (r *recognitionRepairRecoveryFixture) repairCall(t *testing.T, plan k12.RecognitionLayoutPlanV2, auth k12.RecognitionLayoutRepairAuthorizationV2) k12.RecognitionPhysicalCall {
	t.Helper()
	image, err := k12.BuildRecognitionLayoutRepairImageV2(r.image, plan, auth.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	return k12.RecognitionPhysicalCall{PlanVersion: k12.RecognitionPlanVersionV2, PlanDigest: plan.AuthorizedPlanDigest, Unit: auth.PhysicalUnit, TargetIDs: []string{auth.CandidateID}, Image: image}
}

func (r *recognitionRepairRecoveryFixture) settleRepair(t *testing.T, parent k12.ModelInvocation, plan k12.RecognitionLayoutPlanV2, auth k12.RecognitionLayoutRepairAuthorizationV2, source k12.RecognitionPhysicalCallResult, raw string) {
	t.Helper()
	projection, _, err := r.store.SettleRecognitionLayoutRepairV2(context.Background(), parent.AgentName, parent.InvocationID, recognitionRepairSettlement(plan, auth, source, raw))
	if err != nil || projection.FrozenResult == nil {
		t.Fatalf("settle repair: projection=%+v err=%v", projection, err)
	}
}

func recognitionRepairSettlement(plan k12.RecognitionLayoutPlanV2, auth k12.RecognitionLayoutRepairAuthorizationV2, source k12.RecognitionPhysicalCallResult, raw string) k12.RecognitionLayoutRepairSettlementV2 {
	return k12.RecognitionLayoutRepairSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, AuthorizationID: auth.AuthorizationID, AuthorizationDigest: auth.AuthorizationDigest, CandidateID: auth.CandidateID, SourcePhysicalInvocationID: source.InvocationID, SourcePhysicalUnit: auth.PhysicalUnit, SourcePhysicalResultDigest: source.ResultDigest, Classification: k12.RecognitionLayoutCandidateValidV2, ResultKind: k12.RecognitionLayoutCandidateQuestionV2, ResultJSON: json.RawMessage(raw)}
}

func (r *recognitionRepairRecoveryFixture) reuseSuccesses(t *testing.T, parent k12.ModelInvocation, plan k12.RecognitionLayoutPlanV2, calls map[k12.RecognitionPhysicalUnit]int) map[k12.RecognitionPhysicalUnit]k12.RecognitionLayoutRepairAuthorizationV2 {
	t.Helper()
	ctx := context.Background()
	executor := newDurableRecognitionPhysicalCallExecutor(r.o, parent)
	runtime, err := r.store.LoadRecognitionLayoutPlanRuntimeV2(ctx, parent.AgentName, parent.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	headerDigest, err := k12.RecognitionLayoutPlanHeaderDigestV2(runtime.Header)
	if err != nil {
		t.Fatal(err)
	}
	manifestCall := k12.RecognitionPhysicalCall{PlanVersion: k12.RecognitionPlanVersionV2, PlanDigest: headerDigest, Unit: k12.RecognitionPhysicalUnitWholePage, Image: r.image}
	if _, err = executor.ExecuteRecognitionPhysicalCall(ctx, manifestCall, func(context.Context) (string, error) {
		calls[manifestCall.Unit]++
		return "", errors.New("manifest must reuse successful receipt")
	}); err != nil {
		t.Fatal(err)
	}
	auths := make(map[k12.RecognitionPhysicalUnit]k12.RecognitionLayoutRepairAuthorizationV2)
	for _, batch := range plan.Batches {
		source, executeErr := executor.ExecuteRecognitionPhysicalCall(ctx, r.call(t, plan, batch), func(context.Context) (string, error) {
			calls[batch.Unit]++
			return "", errors.New("successful batch must not resend")
		})
		if executeErr != nil {
			t.Fatal(executeErr)
		}
		for unit, auth := range r.settlePrimary(t, parent, plan, batch, source) {
			auths[unit] = auth
		}
	}
	for _, ordinal := range []int{13, 14, 16} {
		unit, _ := k12.RecognitionLayoutRepairUnitV2(ordinal)
		auth, exists := auths[unit]
		if !exists {
			t.Fatalf("missing rebound repair authorization %s", unit)
		}
		source, executeErr := executor.ExecuteRecognitionPhysicalCall(ctx, r.repairCall(t, plan, auth), func(context.Context) (string, error) {
			calls[unit]++
			return "", errors.New("successful repair must not resend")
		})
		if executeErr != nil {
			t.Fatal(executeErr)
		}
		r.settleRepair(t, parent, plan, auth, source, `{"student_answer":"4","text":"2+2="}`)
	}
	return auths
}

func TestRecognitionRepairRecoverySourceIdentity(t *testing.T) {
	r := newRecognitionRepairRecoveryFixture(t)
	ctx := context.Background()
	originalImage := r.o.runs[r.jobID].req.Image
	r.o.runs[r.jobID].req.Image = recognitionPhysicalExecutorV2PagePNG(t, 200, 3201)
	if _, err := r.o.AuthorizeRecognitionRecovery(ctx, r.dispatchID, r.in); err == nil {
		t.Fatal("changed original pixels accepted")
	}
	r.o.runs[r.jobID].req.Image = originalImage
	wrong := r.in
	wrong.TimeoutOverrideMS = 180000
	if _, err := r.o.AuthorizeRecognitionRecovery(ctx, r.dispatchID, wrong); err == nil {
		t.Fatal("repair inherited primary batch timeout extension")
	}
	auth := r.repairs[r.source.PhysicalUnit]
	if _, err := r.db.Exec(`UPDATE k12_recognition_layout_repair_authorizations SET authorization_digest=? WHERE repair_authorization_id=?`, recognitionPhysicalExecutorV2Digest("drift"), auth.AuthorizationID); err == nil {
		t.Fatal("durable repair authorization allowed an ordinary mutation")
	}
	// 仅临时库模拟已损坏证据，验证恢复事务不只依赖写入触发器。
	restoreAuthorizationTrigger := r.removeFixtureTrigger(t, "k12_recognition_layout_repair_authorization_immutable")
	defer restoreAuthorizationTrigger()
	if _, err := r.db.Exec(`UPDATE k12_recognition_layout_repair_authorizations SET authorization_digest=? WHERE repair_authorization_id=?`, recognitionPhysicalExecutorV2Digest("drift"), auth.AuthorizationID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.o.AuthorizeRecognitionRecovery(ctx, r.dispatchID, r.in); err == nil {
		t.Fatal("changed durable repair authorization accepted")
	}
	if _, err := r.db.Exec(`UPDATE k12_recognition_layout_repair_authorizations SET authorization_digest=? WHERE repair_authorization_id=?`, auth.AuthorizationDigest, auth.AuthorizationID); err != nil {
		t.Fatal(err)
	}
	restoreAuthorizationTrigger()
	if _, err := r.db.Exec(`UPDATE k12_model_physical_invocations SET request_digest=? WHERE physical_invocation_id=?`, recognitionPhysicalExecutorV2Digest("changed-request"), r.source.PhysicalInvocationID); err == nil {
		t.Fatal("physical request identity allowed an ordinary mutation")
	}
	restorePhysicalTrigger := r.removeFixtureTrigger(t, "k12_model_physical_invocation_identity_immutable")
	defer restorePhysicalTrigger()
	if _, err := r.db.Exec(`UPDATE k12_model_physical_invocations SET request_digest=? WHERE physical_invocation_id=?`, recognitionPhysicalExecutorV2Digest("changed-request"), r.source.PhysicalInvocationID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.o.AuthorizeRecognitionRecovery(ctx, r.dispatchID, r.in); err == nil {
		t.Fatal("changed original repair request accepted")
	}
	if _, err := r.db.Exec(`UPDATE k12_model_physical_invocations SET request_digest=? WHERE physical_invocation_id=?`, r.source.RequestDigest, r.source.PhysicalInvocationID); err != nil {
		t.Fatal(err)
	}
	restorePhysicalTrigger()
	var rejectedAuthorizations int
	if err := r.db.QueryRow(`SELECT count(*) FROM k12_recognition_recovery_authorizations WHERE job_id=?`, r.jobID).Scan(&rejectedAuthorizations); err != nil || rejectedAuthorizations != 0 {
		t.Fatalf("rejected source created authorization: count=%d err=%v", rejectedAuthorizations, err)
	}
	var rejectedParents int
	if err := r.db.QueryRow(`SELECT count(*) FROM k12_model_invocations WHERE job_id=?`, r.jobID).Scan(&rejectedParents); err != nil || rejectedParents != 1 {
		t.Fatalf("rejected source created a new invocation: count=%d err=%v", rejectedParents, err)
	}
	accepted, err := r.o.AuthorizeRecognitionRecovery(ctx, r.dispatchID, r.in)
	if err != nil || accepted.Replayed || accepted.Authorization.SourcePhysicalID != r.source.PhysicalInvocationID || accepted.Authorization.CandidateExactSetDigest != r.source.CandidateExactSetDigest || accepted.Authorization.TimeoutOverrideMS != 0 || accepted.Authorization.SourceTimeoutMS != 120000 {
		t.Fatalf("valid singleton repair rejected or rebound incorrectly: %+v err=%v", accepted, err)
	}
	if after := r.originalReceipts(t); after != r.before {
		t.Fatal("authorization changed original receipts")
	}
}

func (r *recognitionRepairRecoveryFixture) removeFixtureTrigger(t *testing.T, name string) func() {
	t.Helper()
	var ddl string
	if err := r.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`DROP TRIGGER "` + name + `"`); err != nil {
		t.Fatal(err)
	}
	restored := false
	return func() {
		t.Helper()
		if restored {
			return
		}
		if _, err := r.db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
		restored = true
	}
}

func TestRecognitionRepairRecoveryColdSuccessReuse(t *testing.T) {
	r := newRecognitionRepairRecoveryFixture(t)
	ctx := context.Background()
	accepted, err := r.o.AuthorizeRecognitionRecovery(ctx, r.dispatchID, r.in)
	if err != nil {
		t.Fatal(err)
	}
	r.reopen(t)
	replay, err := r.o.AuthorizeRecognitionRecovery(ctx, r.dispatchID, r.in)
	if err != nil || !replay.Replayed || replay.Authorization != accepted.Authorization {
		t.Fatalf("cold authorization replay changed identity: %+v err=%v", replay, err)
	}
	parent, plan := r.begin(t)
	calls := make(map[k12.RecognitionPhysicalUnit]int)
	auths := r.reuseSuccesses(t, parent, plan, calls)
	auth := auths[r.source.PhysicalUnit]
	if auth.CandidateID != r.repairs[r.source.PhysicalUnit].CandidateID || auth.PhysicalUnit != r.source.PhysicalUnit {
		t.Fatalf("repair target changed: %+v", auth)
	}
	executor := newDurableRecognitionPhysicalCallExecutor(r.o, parent)
	source, err := executor.ExecuteRecognitionPhysicalCall(ctx, r.repairCall(t, plan, auth), func(context.Context) (string, error) {
		calls[auth.PhysicalUnit]++
		return recognitionRepairStudentResult, nil
	})
	if err != nil || source.InvocationID != accepted.Authorization.NewPhysicalID {
		t.Fatalf("authorized repair: source=%+v err=%v", source, err)
	}
	physical, err := r.store.GetModelPhysicalInvocation(ctx, r.in.Agent, source.InvocationID)
	if err != nil || physical.EffectiveTimeoutMS != 0 || physical.RequestPolicySnapshot != r.source.RequestPolicySnapshot || physical.RouteSnapshot != r.source.RouteSnapshot {
		t.Fatalf("recovered repair changed original route or request policy: physical=%+v err=%v", physical, err)
	}
	if _, _, err = r.store.SettleRecognitionLayoutRepairV2(ctx, parent.AgentName, parent.InvocationID, recognitionRepairSettlement(plan, auth, source, "{")); err == nil {
		t.Fatal("malformed settlement reported success")
	}
	r.reopen(t)
	parent, plan = r.begin(t)
	executor = newDurableRecognitionPhysicalCallExecutor(r.o, parent)
	repeated, err := executor.ExecuteRecognitionPhysicalCall(ctx, r.repairCall(t, plan, auth), func(context.Context) (string, error) {
		calls[auth.PhysicalUnit]++
		return "", errors.New("successful response must replay after settlement failure")
	})
	if err != nil || repeated != source {
		t.Fatalf("successful raw receipt lost on restart: %+v err=%v", repeated, err)
	}
	r.settleRepair(t, parent, plan, auth, repeated, recognitionRepairStudentResult)
	final, created, err := r.store.FinalizeRecognitionLayoutPlanV2(ctx, parent.AgentName, parent.InvocationID)
	if err != nil || !created || final.CandidateResultCount != 16 || len(final.CandidateResults) != 16 {
		t.Fatalf("fixture exact set incomplete: count=%d created=%v err=%v", final.CandidateResultCount, created, err)
	}
	var question struct {
		StudentAnswer string `json:"student_answer"`
	}
	if err = json.Unmarshal(final.CandidateResults[14].ResultJSON, &question); err != nil || question.StudentAnswer != "300÷2÷2=50" {
		t.Fatalf("student wrong step rewritten: answer=%q err=%v", question.StudentAnswer, err)
	}
	finalReplay, replayCreated, err := r.store.FinalizeRecognitionLayoutPlanV2(ctx, parent.AgentName, parent.InvocationID)
	if err != nil || replayCreated || !reflect.DeepEqual(finalReplay, final) {
		t.Fatalf("finalization replay changed result: created=%v err=%v", replayCreated, err)
	}
	if !reflect.DeepEqual(calls, map[k12.RecognitionPhysicalUnit]int{r.source.PhysicalUnit: 1}) {
		t.Fatalf("unexpected physical sends: %v", calls)
	}
	if after := r.originalReceipts(t); after != r.before {
		t.Fatal("old successful or unknown receipt changed")
	}
	var count int
	if err = r.db.QueryRow(`SELECT count(*) FROM k12_recognition_recovery_authorizations WHERE job_id=?`, r.jobID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replay duplicated authorization: count=%d err=%v", count, err)
	}
}

func TestRecognitionRepairRecoveryNewUnknownStops(t *testing.T) {
	r := newRecognitionRepairRecoveryFixture(t)
	ctx := context.Background()
	accepted, err := r.o.AuthorizeRecognitionRecovery(ctx, r.dispatchID, r.in)
	if err != nil {
		t.Fatal(err)
	}
	parent, plan := r.begin(t)
	calls := make(map[k12.RecognitionPhysicalUnit]int)
	auths := r.reuseSuccesses(t, parent, plan, calls)
	auth := auths[r.source.PhysicalUnit]
	executor := newDurableRecognitionPhysicalCallExecutor(r.o, parent)
	if _, err = executor.ExecuteRecognitionPhysicalCall(ctx, r.repairCall(t, plan, auth), func(context.Context) (string, error) { calls[auth.PhysicalUnit]++; return "", context.DeadlineExceeded }); err == nil {
		t.Fatal("new repair unknown reported success")
	}
	if _, err = r.store.MarkModelInvocationOutcomeUnknown(ctx, parent.AgentName, parent.InvocationID, "provider_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	run, err := r.o.ensureRun(ctx, r.jobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.o.markGradingOutcomeUnknown(ctx, run, r.jobID, "provider_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	r.reopen(t)
	replay, err := r.o.AuthorizeRecognitionRecovery(ctx, r.dispatchID, r.in)
	if err != nil || !replay.Replayed || replay.Status != "outcome_unknown" {
		t.Fatalf("unknown authorization replay: %+v err=%v", replay, err)
	}
	if _, err = r.o.deps.RetryGradingJob(ctx, r.in.Agent, r.jobID); err == nil {
		t.Fatal("ordinary retry resent unknown repair")
	}
	parent, err = r.store.GetModelInvocation(ctx, r.in.Agent, accepted.Authorization.NewParentID)
	if err != nil {
		t.Fatal(err)
	}
	executor = newDurableRecognitionPhysicalCallExecutor(r.o, parent)
	if _, err = executor.ExecuteRecognitionPhysicalCall(ctx, r.repairCall(t, plan, auth), func(context.Context) (string, error) { calls[auth.PhysicalUnit]++; return "", nil }); err == nil {
		t.Fatal("unknown cold replay permitted another physical send")
	}
	if _, _, err = r.store.FinalizeRecognitionLayoutPlanV2(ctx, parent.AgentName, parent.InvocationID); err == nil {
		t.Fatal("unknown singleton was fabricated into completed exact set")
	}
	if !reflect.DeepEqual(calls, map[k12.RecognitionPhysicalUnit]int{r.source.PhysicalUnit: 1}) {
		t.Fatalf("unknown repeated or successful units resent: %v", calls)
	}
	if replaced, replaceErr := r.store.RecognitionUnknownHasReplacement(ctx, r.in.Agent, r.source.PhysicalInvocationID); replaceErr != nil || replaced {
		t.Fatalf("new unknown erased original unknown: replaced=%v err=%v", replaced, replaceErr)
	}
	if after := r.originalReceipts(t); after != r.before {
		t.Fatal("new unknown changed historical receipts")
	}
}
