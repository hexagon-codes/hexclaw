package k12storage_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type groundingSourceRecoveryFixture struct {
	store       *k12storage.Store
	path, jobID string
	dispatch    k12.ImageTaskDispatch
	jobVersion  int
	next        k12.GradingJobFields
	prior       []k12storage.GroundingRetrievalInvocation
	claim       k12storage.GroundingRetrievalInvocationClaim
	result      string
}

func newGroundingSourceRecoveryFixture(t *testing.T) *groundingSourceRecoveryFixture {
	t.Helper()
	ctx := context.Background()
	store, db := setup(t)
	now := time.Now().Unix()
	fields := k12.GradingJobFields{
		SubmissionID: "source-submission", SourceKind: "image_task", IdempotencyKey: "image_task|source-dispatch|v0",
		ConfirmationState: k12.GradingConfirmationConfirmed, AnchorState: k12.GradingAnchorLocated,
		ParentAutomaticAttemptID: "source-dispatch:g1", Deadline: now - 1,
		ParentAutomaticDeadlineAt: now - 1, ParentAutomaticRemainingSeconds: 0,
		ModelSnapshot: k12.GradingModelSnapshot{Provider: "test", Model: "frozen", Route: "test/frozen", Capability: "vision"},
		BudgetSnapshot: k12.GradingBudgetSnapshot{PolicyVersion: 1, RecognitionPlanVersion: k12.RecognitionPlanVersionV1,
			StageSeconds:     k12.GradingStageBudgets{Queued: 60, Normalizing: 60, Recognizing: 120, Locating: 60, Rendering: 60, Projecting: 60},
			AssessingBuckets: []k12.GradingAssessingBudgetBucket{{MaxProblems: 1, Seconds: 90}, {MaxProblems: 8, Seconds: 180}, {MaxProblems: 16, Seconds: 420}, {MaxProblems: 32, Seconds: 600}}, ItemConcurrency: 2},
		StageCheckpoints: []k12.GradingStageCheckpoint{
			{Stage: k12.GradingStageNormalizing, ArtifactDigest: "original-image", RecordedAt: now - 30},
			{Stage: k12.GradingStageRecognizing, ArtifactDigest: "original-recognition", RecordedAt: now - 20},
			{Stage: k12.GradingStageAwaitingConfirmation, ArtifactDigest: "original-input", RecordedAt: now - 10},
		},
		AttemptCount: 1, FailureKind: "assess_item_failed", FailedStage: k12.GradingStageAssessing, Retryable: true,
	}
	record, err := k12.NewGradingJobRecord("mingming", "source-session", fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE k12_grading_jobs SET status='failed_retryable' WHERE record_id=?`, record.RecordID); err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO k12_image_task_dispatches(dispatch_id,agent_name,learner_id,source_kind,source_ref,source_asset_refs_json,source_digest,task_intent,status,target_object_type,target_object_id,classification_route_snapshot_json,classification_invocation_id,route_policy_snapshot_json,idempotency_key,request_digest,automatic_budget_seconds,created_at,updated_at)
		VALUES('source-dispatch','mingming','learner','desktop','fixture','["asset://mingming/source.png"]','source','completed_homework','routed','homework_submission','source-homework','{}','classification','{}','dispatch-key','dispatch-request',300,?,?)`, []any{now, now}},
		{`INSERT INTO k12_homework_submissions(submission_id,dispatch_id,agent_name,learner_id,source_kind,source_ref,source_asset_refs_json,task_intent,status,grading_job_id,idempotency_key,created_at,updated_at)
		VALUES('source-homework','source-dispatch','mingming','learner','desktop','fixture','["asset://mingming/source.png"]','completed_homework','processing',?,'homework-key',?,?)`, []any{record.RecordID, now, now}},
		{`INSERT INTO k12_image_task_owner_scopes(dispatch_id,owner_scope,agent_name,created_at) VALUES('source-dispatch','desktop-user','mingming',?)`, []any{now}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	base := problemAttemptFixture("mingming", fields.SubmissionID)
	var snapshot k12.ProblemAttemptSnapshot
	for i := 1; i <= 15; i++ {
		problem := base.Problems[2]
		problem.ProblemID, problem.ParentProblemID, problem.SubproblemNo = fmt.Sprintf("source-problem-%02d", i), "", ""
		problem.ProblemKind, problem.Ordinal = k12.ProblemKindStandalone, i
		problem.SourceNumberPath, problem.DisplayLabel = []string{fmt.Sprint(i)}, fmt.Sprint(i)
		attempt := base.Attempts[1]
		attempt.AttemptID, attempt.ProblemID = fmt.Sprintf("source-attempt-%02d", i), problem.ProblemID
		snapshot.Problems, snapshot.Attempts = append(snapshot.Problems, problem), append(snapshot.Attempts, attempt)
	}
	if err := store.PutProblemAttemptSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	claim := k12storage.GroundingRetrievalInvocationClaim{OwnerID: "desktop-user", AgentName: "mingming", JobID: record.RecordID,
		Operation: "k12_grounding_retrieval", GroundingSnapshotDigest: strings.Repeat("a", 64), QueryDigest: strings.Repeat("b", 64),
		DocumentID: "original-doc", DocumentGeneration: 1, RevisionID: "original-revision", Provider: "original-provider", Model: "original-embedding"}
	for _, problem := range snapshot.Problems {
		claim.ProblemID = problem.ProblemID
		invocation, err := store.ClaimGroundingRetrievalInvocation(ctx, claim)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkGroundingRetrievalInvocationOutcomeUnknown(ctx, invocation, "unknown"); err != nil {
			t.Fatal(err)
		}
	}
	prior, err := store.ListGroundingRetrievalInvocations(ctx, claim.OwnerID, claim.AgentName, claim.JobID)
	if err != nil || len(prior) != 15 {
		t.Fatalf("original receipts: count=%d err=%v", len(prior), err)
	}
	job, err := store.Get(ctx, record.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := k12.ParseGradingJobFields(job.Fields)
	if err != nil {
		t.Fatal(err)
	}
	next.Deadline, next.ParentAutomaticDeadlineAt, next.ParentAutomaticRemainingSeconds = now+420, now+420, 420
	next.FailureKind, next.FailedStage, next.Retryable = "", "", false
	dispatch, err := store.GetImageTaskDispatch(ctx, "mingming", "source-dispatch")
	if err != nil {
		t.Fatal(err)
	}
	claim.Operation, claim.ProblemID = k12storage.GroundingSourceRecoveryOperation, "source-recovery"
	claim.Provider, claim.Model, claim.RevisionID = "", "", ""
	claim.GroundingSnapshotDigest = strings.Repeat("c", 64)
	f := &groundingSourceRecoveryFixture{store: store, path: filepath.Join(t.TempDir(), "source-recovery.db"), jobID: job.RecordID,
		dispatch: dispatch, jobVersion: job.Version, next: next, prior: prior, claim: claim,
		result: `{"source_mode":"verified_text","content":"原版教材正文","content_digest":"frozen"}`}
	if _, err := db.Exec(`VACUUM INTO ?`, f.path); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	return f
}

func (f *groundingSourceRecoveryFixture) reopen(t *testing.T) {
	t.Helper()
	if err := f.store.DB().Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	f.store = k12storage.NewStore(db, nil)
}

func (f *groundingSourceRecoveryFixture) recover(t *testing.T) (k12storage.GroundingRetrievalInvocation, bool, error) {
	t.Helper()
	return f.store.AuthorizeGroundingSourceRecovery(context.Background(), f.claim, f.result, f.prior,
		f.dispatch.DispatchID, f.dispatch.Version, f.jobVersion, f.next)
}

func (f *groundingSourceRecoveryFixture) assertOriginalReceipts(t *testing.T, extra int) {
	t.Helper()
	got, err := f.store.ListGroundingRetrievalInvocations(context.Background(), f.claim.OwnerID, f.claim.AgentName, f.jobID)
	if err != nil || len(got) != 15+extra {
		t.Fatalf("receipt count: %d err=%v", len(got), err)
	}
	var old []k12storage.GroundingRetrievalInvocation
	for _, invocation := range got {
		if invocation.Operation == "k12_grounding_retrieval" {
			old = append(old, invocation)
		}
	}
	if !reflect.DeepEqual(old, f.prior) {
		t.Fatal("original receipt fields changed")
	}
}

func TestGroundingSourceRecovery_RestartReplayPreservesUnknownAndAtomicWindow(t *testing.T) {
	f := newGroundingSourceRecoveryFixture(t)
	first, created, err := f.recover(t)
	if err != nil || !created || first.Status != k12storage.GroundingRetrievalInvocationStatusSucceeded || first.Provider != "" || first.Model != "" || first.RevisionID != "" || first.ResultJSON != f.result {
		t.Fatalf("recovery: created=%v result=%+v err=%v", created, first, err)
	}
	f.assertOriginalReceipts(t, 1)
	f.reopen(t)
	replayed, created, err := f.recover(t)
	if err != nil || created || !reflect.DeepEqual(replayed, first) {
		t.Fatalf("replay changed recovery: created=%v result=%+v err=%v", created, replayed, err)
	}
	originalResult := f.result
	f.result = `{"source_mode":"verified_text","content":"different source"}`
	if _, created, err := f.recover(t); err == nil || created {
		t.Fatalf("same recovery accepted changed source: created=%v err=%v", created, err)
	}
	f.result = originalResult
	f.assertOriginalReceipts(t, 1)
	job, err := f.store.Get(context.Background(), f.jobID)
	if err != nil || job.Status != k12.GradingStageQueued || job.Version != f.jobVersion+1 {
		t.Fatalf("job was not queued once: job=%+v err=%v", job, err)
	}
	fields, err := k12.ParseGradingJobFields(job.Fields)
	if err != nil || !reflect.DeepEqual(fields, f.next) {
		t.Fatalf("job input or window changed: fields=%+v err=%v", fields, err)
	}
	dispatch, err := f.store.GetImageTaskDispatch(context.Background(), "mingming", f.dispatch.DispatchID)
	if err != nil || dispatch.Version != f.dispatch.Version+1 || dispatch.AutomaticDeadlineAt != f.next.Deadline || dispatch.AutomaticRemainingSeconds != 420 || dispatch.AutomaticStartedAt != f.next.Deadline-420 || dispatch.AutomaticBudgetSeconds != 420 {
		t.Fatalf("dispatch window not synchronized: %+v err=%v", dispatch, err)
	}
}

func TestGroundingSourceRecovery_RejectsDriftAndRollsBack(t *testing.T) {
	for _, mutation := range []string{"prior", "job_version", "dispatch_version", "input", "budget", "rollback"} {
		t.Run(mutation, func(t *testing.T) {
			f := newGroundingSourceRecoveryFixture(t)
			before := append([]k12storage.GroundingRetrievalInvocation(nil), f.prior...)
			switch mutation {
			case "prior":
				f.prior[0].ResultJSON = `{"changed":true}`
			case "job_version":
				f.jobVersion++
			case "dispatch_version":
				f.dispatch.Version++
			case "input":
				f.next.ConfirmedVersion++
			case "budget":
				f.next.Deadline++
				f.next.ParentAutomaticDeadlineAt++
				f.next.ParentAutomaticRemainingSeconds++
			case "rollback":
				if _, err := f.store.DB().Exec(`CREATE TRIGGER reject_source_recovery_window BEFORE UPDATE ON k12_image_task_dispatches BEGIN SELECT RAISE(ABORT,'window commit rejected'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if _, created, err := f.recover(t); err == nil || created {
				t.Fatalf("invalid recovery reported success: created=%v err=%v", created, err)
			}
			f.prior = before
			f.assertOriginalReceipts(t, 0)
			job, err := f.store.Get(context.Background(), f.jobID)
			if err != nil || job.Status != k12.GradingStageFailedRetryable {
				t.Fatalf("failed recovery changed job: %+v %v", job, err)
			}
			dispatch, err := f.store.GetImageTaskDispatch(context.Background(), "mingming", f.dispatch.DispatchID)
			if err != nil || dispatch.AutomaticDeadlineAt != 0 {
				t.Fatalf("failed recovery changed dispatch window: %+v %v", dispatch, err)
			}
		})
	}
}

func TestGroundingSourceRecovery_QueuedTimeoutRequiresAssessmentCheckpoint(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(fmt.Sprintf("complete_checkpoint_%v", complete), func(t *testing.T) {
			f := newGroundingSourceRecoveryFixture(t)
			if _, err := f.store.DB().Exec(`UPDATE k12_grading_jobs SET failed_stage='queued',failure_kind='interactive_deadline_exceeded' WHERE record_id=?`, f.jobID); err != nil {
				t.Fatal(err)
			}
			if !complete {
				if _, err := f.store.DB().Exec(`UPDATE k12_grading_jobs SET stage_checkpoints_json='[]' WHERE record_id=?`, f.jobID); err != nil {
					t.Fatal(err)
				}
				f.next.StageCheckpoints = nil
			}
			_, created, err := f.recover(t)
			if complete {
				if err != nil || !created {
					t.Fatalf("queued timeout lost completed recognition: created=%v err=%v", created, err)
				}
				f.assertOriginalReceipts(t, 1)
			} else {
				if err == nil || created {
					t.Fatalf("queued task without assessment checkpoint recovered: created=%v err=%v", created, err)
				}
				f.assertOriginalReceipts(t, 0)
			}
		})
	}
}

func TestGroundingSourceSnapshot_RestartReusesOriginalBody(t *testing.T) {
	f := newGroundingSourceRecoveryFixture(t)
	f.claim.Operation = k12storage.GroundingSourceSnapshotOperation
	first, created, err := f.store.FreezeGroundingSource(context.Background(), f.claim, f.result)
	if err != nil || !created || first.ResultJSON != f.result {
		t.Fatalf("freeze: created=%v record=%+v err=%v", created, first, err)
	}
	f.reopen(t)
	f.claim.DocumentGeneration = 2
	f.claim.GroundingSnapshotDigest = strings.Repeat("d", 64)
	got, created, err := f.store.FreezeGroundingSource(context.Background(), f.claim, `{"source_mode":"verified_text","content":"新版正文"}`)
	if err != nil || created || !reflect.DeepEqual(got, first) {
		t.Fatalf("frozen body replaced after reparse: created=%v got=%+v err=%v", created, got, err)
	}
	f.assertOriginalReceipts(t, 1)
}

func TestGroundingSourceRecovery_ReconcileOnlyBeforeNewInvocations(t *testing.T) {
	for _, mutation := range []string{"none", "reconciliation_required", "no_aggregate", "item", "query", "source_digest"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			f := newGroundingSourceRecoveryFixture(t)
			ids := make([]string, 0, len(f.prior))
			for _, row := range f.prior {
				ids = append(ids, row.InvocationID)
			}
			payload, err := json.Marshal(map[string]any{"source_invocation_ids": ids, "content": "原版教材正文"})
			if err != nil {
				t.Fatal(err)
			}
			f.result = string(payload)
			recovery, created, err := f.recover(t)
			if err != nil || !created {
				t.Fatalf("source recovery: created=%v err=%v", created, err)
			}
			if _, err := f.store.DB().Exec(`UPDATE k12_grading_jobs SET status='outcome_unknown',retryable=0,attempt_count=2,failure_kind='item_invocation_outcome_unknown',failed_stage='assessing' WHERE record_id=?`, f.jobID); err != nil {
				t.Fatal(err)
			}
			var aggregate k12.ModelInvocation
			if mutation != "no_aggregate" {
				aggregate, _, err = f.store.PrepareModelInvocation(ctx, k12.ModelInvocation{
					InvocationID: "original-aggregate", AgentName: f.claim.AgentName, JobID: f.jobID,
					Stage: k12.GradingStageAssessing, Attempt: 3, RequestDigest: "original-assessing-request",
					RouteSnapshot: f.next.ModelSnapshot,
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.store.MarkModelInvocationSent(ctx, f.claim.AgentName, aggregate.InvocationID, ""); err != nil {
					t.Fatal(err)
				}
				aggregate, err = f.store.MarkModelInvocationOutcomeUnknown(ctx, f.claim.AgentName, aggregate.InvocationID, "item_invocation_outcome_unknown")
				if err != nil {
					t.Fatal(err)
				}
			}
			switch mutation {
			case "reconciliation_required":
				_, err = f.store.DB().Exec(`UPDATE k12_grading_jobs SET failure_kind='invocation_reconciliation_required' WHERE record_id=?`, f.jobID)
			case "item":
				_, _, err = f.store.PrepareGradingItemInvocation(ctx, k12.GradingItemInvocation{
					InvocationID: "prepared-item", AgentName: f.claim.AgentName, JobID: f.jobID,
					ProblemID: "source-problem-01", AttemptID: "source-attempt-01", Operation: k12.GradingItemOperationSolveGenerate,
					OperationAttempt: 1, RequestDigest: "new-item", RouteSnapshot: f.next.ModelSnapshot,
				})
			case "query":
				claim := f.claim
				claim.Operation, claim.ProblemID, claim.QueryDigest = "k12_grounding_retrieval", "source-problem-01", strings.Repeat("e", 64)
				_, err = f.store.ClaimGroundingRetrievalInvocation(ctx, claim)
			case "source_digest":
				_, err = f.store.DB().Exec(`UPDATE k12_grounding_retrieval_invocations SET query_receipt_digest=? WHERE invocation_id=?`, strings.Repeat("e", 64), recovery.InvocationID)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := f.store.Get(ctx, f.jobID)
			if err != nil {
				t.Fatal(err)
			}
			beforeFields, err := k12.ParseGradingJobFields(before.Fields)
			if err != nil {
				t.Fatal(err)
			}
			receiptsBefore, err := f.store.ListGroundingRetrievalInvocations(ctx, f.claim.OwnerID, f.claim.AgentName, f.jobID)
			if err != nil {
				t.Fatal(err)
			}
			dispatchBefore, err := f.store.GetImageTaskDispatch(ctx, f.claim.AgentName, f.dispatch.DispatchID)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := f.store.ReconcileGroundingSourceNotStarted(ctx, f.claim.OwnerID, f.claim.AgentName, f.jobID, recovery.InvocationID, before.Version)
			wantChanged := mutation == "none" || mutation == "reconciliation_required" || mutation == "no_aggregate"
			if err != nil || changed != wantChanged {
				t.Fatalf("reconcile: changed=%v want=%v err=%v", changed, wantChanged, err)
			}
			after, err := f.store.Get(ctx, f.jobID)
			if err != nil {
				t.Fatal(err)
			}
			afterFields, err := k12.ParseGradingJobFields(after.Fields)
			if err != nil {
				t.Fatal(err)
			}
			if wantChanged {
				beforeFields.FailureKind, beforeFields.Retryable = "interactive_deadline_exceeded", true
				if aggregate.InvocationID != "" {
					beforeFields.AttemptCount = 3
				}
				if after.Status != k12.GradingStageFailedRetryable || after.Version != before.Version+1 || !reflect.DeepEqual(afterFields, beforeFields) {
					t.Fatalf("reconcile changed frozen input or failed to unlock retry: %+v %+v", after, afterFields)
				}
				if changed, err := f.store.ReconcileGroundingSourceNotStarted(ctx, f.claim.OwnerID, f.claim.AgentName, f.jobID, recovery.InvocationID, after.Version); err != nil || changed {
					t.Fatalf("reconcile replay wrote again: changed=%v err=%v", changed, err)
				}
				f.assertOriginalReceipts(t, 1)
			} else if !reflect.DeepEqual(before, after) {
				t.Fatal("ineligible reconciliation changed job")
			}
			aggregates, err := f.store.ListModelInvocations(ctx, f.claim.AgentName, f.jobID)
			if err != nil {
				t.Fatal(err)
			}
			wantAggregates := 1
			if aggregate.InvocationID == "" {
				wantAggregates = 0
			} else if wantChanged {
				wantAggregates = 2
			}
			if len(aggregates) != wantAggregates {
				t.Fatalf("aggregate append/replay count: got=%d want=%d", len(aggregates), wantAggregates)
			}
			if aggregate.InvocationID != "" && !reflect.DeepEqual(aggregate, aggregates[0]) {
				t.Fatal("original aggregate unknown was changed")
			}
			if wantAggregates == 2 {
				next := aggregates[1]
				if next.Attempt != 4 || next.Status != k12.ModelInvocationPrepared || next.RequestDigest != aggregate.RequestDigest ||
					next.RouteSnapshot != aggregate.RouteSnapshot || next.RequestPolicySnapshot != aggregate.RequestPolicySnapshot ||
					next.ResultJSON != "" || next.ResultDigest != "" || next.FailureKind != "" || next.ExternalRequestID != "" || next.ProviderIdempotencyKey != "" {
					t.Fatalf("new aggregate did not preserve frozen request: %+v", next)
				}
			}
			receiptsAfter, err := f.store.ListGroundingRetrievalInvocations(ctx, f.claim.OwnerID, f.claim.AgentName, f.jobID)
			if err != nil || !reflect.DeepEqual(receiptsBefore, receiptsAfter) {
				t.Fatalf("reconcile changed original or recovery receipts: %v", err)
			}
			dispatchAfter, err := f.store.GetImageTaskDispatch(ctx, f.claim.AgentName, f.dispatch.DispatchID)
			if err != nil || !reflect.DeepEqual(dispatchBefore, dispatchAfter) {
				t.Fatalf("reconcile changed dispatch budget or window: %v", err)
			}
		})
	}
}
