package k12storage_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type knownLocalRecoveryFixture struct {
	store      *k12storage.Store
	db         *sql.DB
	dispatch   k12.ImageTaskDispatch
	job        *records.AgentRecord
	failedItem k12.GradingItemInvocation
	parent     k12.ModelInvocation
	physical   k12.ModelPhysicalInvocation
	command    k12storage.ImageTaskKnownLocalTechnicalRecovery
}

func seedKnownLocalRecovery(t *testing.T, reconciledLogical bool, failedCounts ...int) knownLocalRecoveryFixture {
	t.Helper()
	failedCount := 3
	if len(failedCounts) > 0 {
		failedCount = failedCounts[0]
	}
	store, db := setup(t)
	ctx := context.Background()
	dispatch := testImageTaskDispatch()
	dispatch.OwnerScope = "parent-original"
	source := sha256.Sum256([]byte("original photo bytes"))
	dispatch.SourceDigest = "sha256:" + hex.EncodeToString(source[:])
	dispatch.SourceAssetRefs = []string{"asset://mingming/" + hex.EncodeToString(source[:]) + ".png"}
	invocation := k12.ImageTaskInvocation{
		InvocationID: dispatch.ClassificationInvocationID, AgentName: dispatch.AgentName,
		DispatchID: dispatch.DispatchID, Operation: k12.ImageTaskOperationClassification,
		OperationKey:  "dispatch:" + dispatch.DispatchID + ":classification",
		RequestDigest: dispatch.RequestDigest, RouteSnapshot: dispatch.ClassificationRouteSnapshot,
		Status: k12.ImageTaskInvocationPrepared, Attempt: 1, CreatedAt: 100, UpdatedAt: 100,
	}
	prepared, created, err := store.PrepareImageTaskDispatch(ctx, dispatch, invocation)
	if err != nil || !created {
		t.Fatalf("prepare recovery dispatch: created=%v err=%v", created, err)
	}
	routed, target, err := store.CommitImageTaskRouting(ctx, dispatch.AgentName, dispatch.DispatchID,
		prepared.Version, k12storage.ImageTaskRoutingDecision{
			Intent: k12.ImageTaskIntentCompletedHomework, Evidence: []string{"worksheet"},
			Confidence: .99, InvocationResultDigest: "sha256:original-classification",
		})
	if err != nil || target.HomeworkSubmission == nil {
		t.Fatalf("route recovery homework: target=%+v err=%v", target, err)
	}
	// 原照片输入和路由目标各自保留身份，通过原 job 绑定关联。
	snapshot := problemAttemptFixture("mingming", "photo-original")
	for i := range snapshot.Attempts {
		snapshot.Attempts[i].ConfirmedVersion = 1
		snapshot.Attempts[i].InputDigest = "sha256:original-" + snapshot.Attempts[i].ProblemID
	}
	if err := store.PutProblemAttemptSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	fields := k12.GradingJobFields{
		SubmissionID: "photo-original", SourceKind: "image_task", ConfirmedVersion: 1,
		IdempotencyKey:    k12.BuildGradingIdempotencyKey("image_task", dispatch.DispatchID, 1),
		ConfirmationState: k12.GradingConfirmationConfirmed, AnchorState: k12.GradingAnchorLocated,
		Deadline: 400, ParentAutomaticAttemptID: "dispatch-1:100",
		ParentAutomaticDeadlineAt: 400, ParentAutomaticRemainingSeconds: 300,
		ModelSnapshot: k12.GradingModelSnapshot{
			Provider: "hexclaw-gpt", Model: k12.RecognizingPolicyModel,
			Route: "hexclaw-gpt/" + k12.RecognizingPolicyModel, Capability: "vision",
			ProviderInstanceID: "frozen-provider-instance", ConfigFingerprint: "frozen-config",
			CapabilityReceiptDigest: "sha256:frozen-capability", ProbePolicyVersion: "frozen-probe",
			TimeoutMS: 91000, Fallback: "disabled",
			RecognizingRequestPolicy: k12.ApprovedRecognizingRequestPolicy(),
		},
		BudgetSnapshot: k12.GradingBudgetSnapshot{
			PolicyVersion: 1, ItemConcurrency: 2, RecognitionPlanVersion: k12.RecognitionPlanVersionV1,
			StageSeconds: k12.GradingStageBudgets{
				Queued: 17, Normalizing: 29, Recognizing: 41, Locating: 53, Rendering: 67, Projecting: 79,
			},
			AssessingBuckets: []k12.GradingAssessingBudgetBucket{
				{MaxProblems: 1, Seconds: 101}, {MaxProblems: 8, Seconds: 103},
				{MaxProblems: 16, Seconds: 107}, {MaxProblems: 32, Seconds: 109},
			},
		},
		StageCheckpoints: []k12.GradingStageCheckpoint{
			{Stage: k12.GradingStageNormalizing, ArtifactDigest: "sha256:normalizing", RecordedAt: 110},
			{Stage: k12.GradingStageRecognizing, ArtifactDigest: "sha256:recognizing", RecordedAt: 120},
			{Stage: k12.GradingStageAwaitingConfirmation, ArtifactDigest: "sha256:confirmed", RecordedAt: 130},
		},
		AttemptCount: failedCount, FailureKind: "provider_response_processed", FailedStage: k12.GradingStageAssessing,
	}
	job, err := k12.NewGradingJobRecord("mingming", "session-original", fields)
	if err != nil {
		t.Fatal(err)
	}
	job.CreatedAt, job.UpdatedAt = 100, 100
	if created, err := store.Put(ctx, job); err != nil || !created {
		t.Fatalf("put recovery job: created=%v err=%v", created, err)
	}
	if _, err := store.BindHomeworkSubmissionGradingJob(ctx, "mingming",
		target.HomeworkSubmission.SubmissionID, job.RecordID, target.HomeworkSubmission.Version); err != nil {
		t.Fatal(err)
	}
	parent := preparePhysicalInvocationParent(t, store, job.RecordID)
	physical, created, err := store.PrepareModelPhysicalInvocation(ctx,
		newPhysicalInvocation(parent, "original-successful-physical", k12.RecognitionPhysicalUnitWholePage))
	if err != nil || !created {
		t.Fatalf("prepare historical physical: created=%v err=%v", created, err)
	}
	if _, err := store.MarkModelPhysicalInvocationSent(ctx, "mingming", physical.PhysicalInvocationID); err != nil {
		t.Fatal(err)
	}
	physical, err = store.MarkModelPhysicalInvocationSucceededWithContent(ctx, "mingming",
		physical.PhysicalInvocationID, `{"recognized":"original page"}`, "original-provider-request")
	if err != nil {
		t.Fatal(err)
	}
	if reconciledLogical {
		if _, err := store.MarkModelInvocationOutcomeUnknown(ctx, "mingming", parent.InvocationID, "response_lost"); err != nil {
			t.Fatal(err)
		}
		parent, err = store.ReconcileModelInvocationSucceeded(ctx, "mingming", parent.InvocationID,
			"sha256:original-model-success", "original-provider-request")
	} else {
		parent, err = store.MarkModelInvocationSucceeded(ctx, "mingming", parent.InvocationID,
			"sha256:original-model-success", "original-provider-request")
	}
	if err != nil {
		t.Fatal(err)
	}
	solveID, gradeID := successfulAssessmentInvocations(t, store, job.RecordID, snapshot.Attempts[1])
	if _, created, err := store.CommitGradingAssessmentItem(ctx,
		assessmentReceipt(job.RecordID, snapshot.Attempts[1], solveID, gradeID), k12storage.GradingAssessmentEffects{}); err != nil || !created {
		t.Fatalf("persist historical assessment: created=%v err=%v", created, err)
	}
	failed := itemInvocation(job.RecordID, snapshot.Attempts[0], k12.GradingItemOperationGrade, failedCount*1000+1)
	failed.RouteSnapshot = fields.ModelSnapshot
	failed, created, err = store.PrepareGradingItemInvocation(ctx, failed)
	if err != nil || !created {
		t.Fatalf("prepare local failure: created=%v err=%v", created, err)
	}
	if _, err := store.MarkGradingItemInvocationSent(ctx, "mingming", failed.InvocationID); err != nil {
		t.Fatal(err)
	}
	failed, err = store.MarkGradingItemInvocationFailed(ctx, "mingming", failed.InvocationID,
		"local", "provider_response_processed")
	if err != nil {
		t.Fatal(err)
	}
	knownLocalExec(t, db, `UPDATE k12_grading_jobs SET status='failed_terminal',version=7,updated_at=200 WHERE record_id=?`, job.RecordID)
	knownLocalExec(t, db, `UPDATE k12_image_task_dispatches SET status='failed',failure_kind='provider_response_processed',
		retry_safe=0,automatic_deadline_at=0,automatic_remaining_seconds=0,version=5,updated_at=200 WHERE dispatch_id=?`, routed.DispatchID)
	job, err = store.Get(ctx, job.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	routed, err = store.GetImageTaskDispatch(ctx, "mingming", routed.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	return knownLocalRecoveryFixture{
		store: store, db: db, dispatch: routed, job: job, failedItem: failed, parent: parent, physical: physical,
		command: k12storage.ImageTaskKnownLocalTechnicalRecovery{
			AgentName: "mingming", OwnerScope: "parent-original", DispatchID: routed.DispatchID, JobID: job.RecordID,
			ExpectedDispatchVersion: 5, ExpectedJobVersion: 7, Now: 1000, QueuedDeadline: 1017,
		},
	}
}

func knownLocalExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

// 全字段读取同时覆盖归属、输入、冻结快照、历史结果和副作用记录。
func knownLocalDatabaseSnapshot(t *testing.T, db *sql.DB) map[string][]map[string]any {
	t.Helper()
	snapshot := make(map[string][]map[string]any)
	for _, table := range []string{
		"k12_image_task_dispatches", "k12_image_task_owner_scopes", "k12_image_task_invocations",
		"k12_homework_submissions", "k12_grading_jobs", "k12_problems", "k12_attempts",
		"k12_model_invocations", "k12_model_physical_invocations", "k12_grading_item_invocations",
		"k12_grading_assessment_items", "k12_assessment_corrections", "k12_grading_final_artifacts",
		"k12_mistakes", "outbox_events",
	} {
		rows, err := db.QueryContext(context.Background(), "SELECT * FROM "+table+" ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		snapshot[table] = nil
		for rows.Next() {
			values, dest := make([]any, len(columns)), make([]any, len(columns))
			for i := range dest {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			row := make(map[string]any, len(columns))
			for i, column := range columns {
				if raw, ok := values[i].([]byte); ok {
					row[column] = string(raw)
				} else {
					row[column] = values[i]
				}
			}
			snapshot[table] = append(snapshot[table], row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func assertKnownLocalSnapshotEqual(t *testing.T, before, after map[string][]map[string]any) {
	t.Helper()
	for table, want := range before {
		if !reflect.DeepEqual(want, after[table]) {
			t.Fatalf("recovery changed immutable or rejected table %s: before=%v after=%v", table, want, after[table])
		}
	}
}

func TestKnownLocalTechnicalRecoveryPreservesOriginalHistory(t *testing.T) {
	for _, test := range []struct {
		name              string
		reconciledLogical bool
		failedCount       int
	}{
		{"succeeded logical history", false, 3},
		{"conclusive reconciled logical history", true, 3},
		{"count4 current generation4001", true, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := seedKnownLocalRecovery(t, test.reconciledLogical, test.failedCount)
			if test.reconciledLogical && (f.parent.Status != k12.ModelInvocationReconciled ||
				f.parent.FailureKind != "reconciled_succeeded" || f.parent.ResultDigest != "sha256:original-model-success" ||
				f.parent.ResultJSON != "") {
				t.Fatalf("legacy conclusive logical fixture lost its durable success evidence: %+v", f.parent)
			}
			assertKnownLocalRecoveryOriginalHistory(t, f)
		})
	}
}

func assertKnownLocalRecoveryOriginalHistory(t *testing.T, f knownLocalRecoveryFixture) {
	t.Helper()
	originalFields, err := k12.ParseGradingJobFields(f.job.Fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpdateStatus(context.Background(), f.job.RecordID, k12.GradingStageQueued, nil, 7); !errors.Is(err, records.ErrIllegalTransition) {
		t.Fatalf("ordinary terminal state gained a queued transition: %v", err)
	}
	before := knownLocalDatabaseSnapshot(t, f.db)
	dispatch, job, err := f.store.RestoreImageTaskKnownLocalTechnical(context.Background(), f.command)
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.Version != 6 || dispatch.Status != k12.ImageTaskStatusRouted ||
		dispatch.AutomaticStartedAt != 1000 || dispatch.AutomaticDeadlineAt != 1300 ||
		dispatch.AutomaticBudgetSeconds != 300 || dispatch.AutomaticRemainingSeconds != 300 ||
		dispatch.FailureKind != "" || dispatch.RetrySafe {
		t.Fatalf("recovery returned wrong parent window: %+v", dispatch)
	}
	if job == nil || job.RecordID != f.job.RecordID || job.Version != 8 || job.Status != k12.GradingStageQueued {
		t.Fatalf("recovery returned wrong original job: %+v", job)
	}
	fields, err := k12.ParseGradingJobFields(job.Fields)
	if err != nil {
		t.Fatal(err)
	}
	if fields.AttemptCount != originalFields.AttemptCount || fields.Deadline != 1017 ||
		fields.ParentAutomaticAttemptID != "dispatch-1:1000" ||
		fields.ParentAutomaticDeadlineAt != 1300 || fields.ParentAutomaticRemainingSeconds != 300 {
		t.Fatalf("recovery reset attempts or returned wrong job window: %+v", fields)
	}
	persisted, err := f.store.Get(context.Background(), job.RecordID)
	if err != nil || persisted.Status != k12.GradingStageQueued || persisted.Version != 8 || persisted.Fields != job.Fields {
		t.Fatalf("recovery did not persist returned job: persisted=%+v err=%v", persisted, err)
	}
	after := knownLocalDatabaseSnapshot(t, f.db)
	for _, snapshots := range []map[string][]map[string]any{before, after} {
		for _, key := range []string{"status", "failure_kind", "retry_safe", "automatic_budget_seconds",
			"automatic_started_at", "automatic_deadline_at", "automatic_remaining_seconds", "version", "updated_at"} {
			delete(snapshots["k12_image_task_dispatches"][0], key)
		}
		jobRow := snapshots["k12_grading_jobs"][0]
		var budget map[string]any
		if err := json.Unmarshal([]byte(jobRow["budget_snapshot_json"].(string)), &budget); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"parent_automatic_attempt_id", "parent_automatic_deadline_at", "parent_automatic_remaining_seconds"} {
			delete(budget, key)
		}
		jobRow["budget_snapshot_json"] = budget
		for _, key := range []string{"status", "deadline", "version", "updated_at"} {
			delete(jobRow, key)
		}
	}
	assertKnownLocalSnapshotEqual(t, before, after)
	if k12.GradingMaxStageAttempts != 3 {
		t.Fatalf("ordinary retry limit changed to %d", k12.GradingMaxStageAttempts)
	}
}

func TestKnownLocalTechnicalRecoveryRejectsWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, *knownLocalRecoveryFixture)
	}{
		{"different owner", func(_ *testing.T, f *knownLocalRecoveryFixture) { f.command.OwnerScope = "other-parent" }},
		{"different agent", func(_ *testing.T, f *knownLocalRecoveryFixture) { f.command.AgentName = "lele" }},
		{"different bound job", func(_ *testing.T, f *knownLocalRecoveryFixture) { f.command.JobID = "different-job" }},
		{"different failed stage", func(t *testing.T, f *knownLocalRecoveryFixture) {
			knownLocalExec(t, f.db, `UPDATE k12_grading_jobs SET failed_stage='rendering' WHERE record_id=?`, f.job.RecordID)
		}},
		{"successful assessing checkpoint", func(t *testing.T, f *knownLocalRecoveryFixture) {
			knownLocalExec(t, f.db, `UPDATE k12_grading_jobs SET stage_checkpoints_json=json_insert(stage_checkpoints_json,'$[#]',json(?)) WHERE record_id=?`,
				`{"stage":"assessing","artifact_digest":"sha256:assessment","recorded_at":200}`, f.job.RecordID)
		}},
		{"not exhausted", func(t *testing.T, f *knownLocalRecoveryFixture) {
			knownLocalExec(t, f.db, `UPDATE k12_grading_jobs SET attempt_count=2 WHERE record_id=?`, f.job.RecordID)
		}},
		{"different failure code", func(t *testing.T, f *knownLocalRecoveryFixture) {
			knownLocalExec(t, f.db, `UPDATE k12_grading_item_invocations SET failure_code='provider_timeout' WHERE item_invocation_id=?`, f.failedItem.InvocationID)
		}},
		{"prior generation", func(t *testing.T, f *knownLocalRecoveryFixture) {
			knownLocalExec(t, f.db, `UPDATE k12_grading_item_invocations SET operation_attempt=2001 WHERE item_invocation_id=?`, f.failedItem.InvocationID)
		}},
		{"count4 only has old generation3001", func(t *testing.T, f *knownLocalRecoveryFixture) {
			knownLocalExec(t, f.db, `UPDATE k12_grading_jobs SET attempt_count=4 WHERE record_id=?`, f.job.RecordID)
		}},
		{"newer operation receipt exists", func(t *testing.T, f *knownLocalRecoveryFixture) {
			newer := f.failedItem
			newer.InvocationID, newer.OperationAttempt = "newer-current-input-operation", 3002
			if _, created, err := f.store.PrepareGradingItemInvocation(context.Background(), newer); err != nil || !created {
				t.Fatalf("prepare newer operation receipt: created=%v err=%v", created, err)
			}
		}},
		{"changed current input", func(t *testing.T, f *knownLocalRecoveryFixture) {
			knownLocalExec(t, f.db, `UPDATE k12_attempts SET input_digest='sha256:changed-answer' WHERE attempt_id=?`, f.failedItem.AttemptID)
		}},
		{"changed frozen route detail", func(t *testing.T, f *knownLocalRecoveryFixture) {
			knownLocalExec(t, f.db, `UPDATE k12_grading_item_invocations SET route_snapshot_json=json_set(route_snapshot_json,'$.config_fingerprint','other-config') WHERE item_invocation_id=?`, f.failedItem.InvocationID)
		}},
		{"logical sent", func(t *testing.T, f *knownLocalRecoveryFixture) {
			knownLocalExec(t, f.db, `UPDATE k12_model_invocations SET status='sent' WHERE invocation_id=?`, f.parent.InvocationID)
		}},
		{"logical reconciliation is not conclusive success", func(t *testing.T, f *knownLocalRecoveryFixture) {
			knownLocalExec(t, f.db, `UPDATE k12_model_invocations SET failure_kind='reconciled_partial_succeeded' WHERE invocation_id=?`, f.parent.InvocationID)
		}},
		{"logical reconciled success has empty digest", func(t *testing.T, f *knownLocalRecoveryFixture) {
			knownLocalExec(t, f.db, `UPDATE k12_model_invocations SET result_digest='   ' WHERE invocation_id=?`, f.parent.InvocationID)
		}},
		{"physical outcome unknown", func(t *testing.T, f *knownLocalRecoveryFixture) {
			knownLocalExec(t, f.db, `UPDATE k12_model_physical_invocations SET status='outcome_unknown',result_content=NULL,
				result_digest='',failure_kind='provider_result_unknown' WHERE physical_invocation_id=?`, f.physical.PhysicalInvocationID)
		}},
		{"item reconciled", func(t *testing.T, f *knownLocalRecoveryFixture) {
			knownLocalExec(t, f.db, `UPDATE k12_grading_item_invocations SET status='reconciled' WHERE item_invocation_id=?`, f.failedItem.InvocationID)
		}},
		{"final artifact exists", func(t *testing.T, f *knownLocalRecoveryFixture) {
			knownLocalExec(t, f.db, `INSERT INTO k12_grading_final_artifacts(artifact_id,agent_name,job_id,
				structure_version,coverage_status,total_count,published_count,skipped_count,
				ordered_current_digests_json,canonical_markdown,artifact_digest,summary_invocation_id,created_at,updated_at)
				VALUES('original-final','mingming',?,1,'complete',1,1,0,'["sha256:assessment"]','Original final',?,'original-summary',200,200)`,
				f.job.RecordID, strings.Repeat("a", 64))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := seedKnownLocalRecovery(t, true)
			test.change(t, &f)
			before := knownLocalDatabaseSnapshot(t, f.db)
			if _, _, err := f.store.RestoreImageTaskKnownLocalTechnical(context.Background(), f.command); err == nil {
				t.Fatal("ineligible recovery succeeded")
			}
			assertKnownLocalSnapshotEqual(t, before, knownLocalDatabaseSnapshot(t, f.db))
		})
	}
}

func TestKnownLocalTechnicalRecoveryDualCASAndRollback(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*knownLocalRecoveryFixture)
		wantErr error
	}{
		{"stale dispatch", func(f *knownLocalRecoveryFixture) { f.command.ExpectedDispatchVersion = 4 }, k12storage.ErrImageTaskVersionConflict},
		{"stale job", func(f *knownLocalRecoveryFixture) { f.command.ExpectedJobVersion = 6 }, records.ErrVersionConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := seedKnownLocalRecovery(t, false)
			test.prepare(&f)
			before := knownLocalDatabaseSnapshot(t, f.db)
			if _, _, err := f.store.RestoreImageTaskKnownLocalTechnical(context.Background(), f.command); !errors.Is(err, test.wantErr) {
				t.Fatalf("stale version recovery error=%v, want %v", err, test.wantErr)
			}
			assertKnownLocalSnapshotEqual(t, before, knownLocalDatabaseSnapshot(t, f.db))
		})
	}
	t.Run("same versions apply only once", func(t *testing.T) {
		f := seedKnownLocalRecovery(t, false)
		if _, _, err := f.store.RestoreImageTaskKnownLocalTechnical(context.Background(), f.command); err != nil {
			t.Fatal(err)
		}
		committed := knownLocalDatabaseSnapshot(t, f.db)
		if _, _, err := f.store.RestoreImageTaskKnownLocalTechnical(context.Background(), f.command); !errors.Is(err, k12storage.ErrImageTaskVersionConflict) {
			t.Fatalf("same versions applied twice: %v", err)
		}
		assertKnownLocalSnapshotEqual(t, committed, knownLocalDatabaseSnapshot(t, f.db))
	})
	t.Run("job update failure rolls back parent window", func(t *testing.T) {
		f := seedKnownLocalRecovery(t, false)
		knownLocalExec(t, f.db, `CREATE TRIGGER fail_known_local_job_update
			BEFORE UPDATE OF status ON k12_grading_jobs WHEN NEW.status='queued'
			BEGIN SELECT RAISE(ABORT, 'controlled job update failure'); END`)
		before := knownLocalDatabaseSnapshot(t, f.db)
		if _, _, err := f.store.RestoreImageTaskKnownLocalTechnical(context.Background(), f.command); err == nil ||
			!strings.Contains(err.Error(), "controlled job update failure") {
			t.Fatalf("controlled job update failure was not observed: %v", err)
		}
		assertKnownLocalSnapshotEqual(t, before, knownLocalDatabaseSnapshot(t, f.db))
	})
}
