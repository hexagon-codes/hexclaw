package k12storage_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

func compactInitialReadStorageFixtureV1(t *testing.T, raw string, executionPolicies ...string) initialReadFixtureV1 {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "compact-initial.db")
	store, db := newPhysicalLedgerFileStore(t, path)
	if err := migrate.Run(ctx, db, migrate.All); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO agents(name) VALUES('mingming')`); err != nil {
		t.Fatal(err)
	}
	job := newGradingJobRecord(t, "mingming", "compact-initial")
	if _, err := store.Put(ctx, job); err != nil {
		t.Fatal(err)
	}
	parent := unpreparedPhysicalInvocationParent(job.RecordID)
	page := recognitionLayoutRuntimeTestPagePNG(t)
	targets := []k12.RecognitionLayoutManifestTargetV2{
		{ManifestRef: "manifest_0001", ManifestOrder: 1, SourceNumberPath: []string{"1"}, DisplayLabel: "1.", SourceSectionPath: []string{"一"}, SourceSectionLabel: "一、解决问题", Region: k12.SourcePixelRegion{X: 0, Y: 0, Width: 20, Height: 10}},
		{ManifestRef: "manifest_0002", ManifestOrder: 2, SourceNumberPath: []string{"2"}, DisplayLabel: "2.", SourceSectionPath: []string{"一"}, SourceSectionLabel: "一、解决问题", Region: k12.SourcePixelRegion{X: 0, Y: 20, Width: 20, Height: 10}},
	}
	digest := recognitionLayoutRuntimeTestDigest(raw)
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{InitialReadMode: k12.RecognitionLayoutManifestWithContentV1, EnableSourceAdjudication: true, RecognitionFormat: k12.RecognitionLayoutCompactV4, PagePNG: page, Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: "whole-compact", ResultDigest: digest}, Targets: targets})
	if err != nil {
		t.Fatal(err)
	}
	header := k12.RecognitionLayoutPlanHeaderV2{ExecutionPolicy: k12.RecognitionExecutionPolicyCompactDAGV1, InitialReadMode: k12.RecognitionLayoutManifestWithContentV1, PlanID: "compact-plan", ParentInvocationID: parent.InvocationID, AgentName: parent.AgentName, JobID: parent.JobID, PageDigest: plan.PageDigest, ParentRequestDigest: parent.RequestDigest, RouteSnapshot: parent.RouteSnapshot, RequestPolicySnapshot: parent.RequestPolicySnapshot, StageStartedAtUnixMillis: time.Now().UnixMilli(), PhysicalCallCapMillis: 120000, BudgetBuckets: k12.RecognitionLayoutBudgetBucketsV2{UpTo1ProblemMillis: 600000, UpTo8ProblemsMillis: 600000, UpTo16ProblemsMillis: 600000, UpTo32ProblemsMillis: 600000}, AdapterWorkerHardCap: 2, EffectiveConcurrency: 2}
	if len(executionPolicies) != 0 {
		header.ExecutionPolicy = executionPolicies[0]
	}
	headerDigest, err := k12.RecognitionLayoutPlanHeaderDigestV2(header)
	if err != nil {
		t.Fatal(err)
	}
	child := newPhysicalInvocation(parent, plan.ManifestInvocationID, k12.RecognitionPhysicalUnitWholePage)
	child.RecognitionPlanVersion, child.PlanDigest = k12.RecognitionPlanVersionV2, headerDigest
	parent, child, created, err := store.PrepareRecognizingInvocationWithInitialLayoutPlanV2(ctx, parent, child, header)
	if err != nil || !created {
		t.Fatalf("prepare compact source: created=%v err=%v", created, err)
	}
	if _, claimed, err := store.ClaimModelPhysicalInvocationSent(ctx, parent.AgentName, child.PhysicalInvocationID); err != nil || !claimed {
		t.Fatalf("claim compact source: claimed=%v err=%v", claimed, err)
	}
	if _, err := store.MarkModelPhysicalInvocationSucceededWithContent(ctx, parent.AgentName, child.PhysicalInvocationID, raw, "provider-compact"); err != nil {
		t.Fatal(err)
	}
	fixtureRaw, err := os.ReadFile("../testdata/initial_read_compact_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := k12.CanonicalRecognitionLayoutInitialReadEntriesV1(string(fixtureRaw))
	if err != nil {
		t.Fatal(err)
	}
	input := k12.RecognitionLayoutInitialReadSettlementV1{PlanDigest: plan.AuthorizedPlanDigest, SourcePhysicalInvocationID: child.PhysicalInvocationID, SourcePhysicalUnit: child.PhysicalUnit, SourcePhysicalResultDigest: digest, Classification: k12.RecognitionLayoutBatchClassifiedV2}
	for i, target := range plan.Targets {
		result, err := json.Marshal(map[string]any{"student_answer": []string{"3/8×2=3/4\n答：3/4张纸。", "3/8×4=3\n答：3张纸。"}[i], "text": []string{"每朵花用\\(\\frac{3}{8}\\)张纸。\n做2朵花，共用多少张纸？", "每朵花用\\(\\frac{3}{8}\\)张纸。\n做4朵花，共用多少张纸？"}[i]})
		if err != nil {
			t.Fatal(err)
		}
		input.Candidates = append(input.Candidates, k12.RecognitionLayoutInitialCandidateV1{CandidateID: target.TargetID, Classification: k12.RecognitionLayoutCandidateReviewRequiredV2, ResultKind: k12.RecognitionLayoutCandidateQuestionV2, FirstReadJSON: entries[i], ResultJSON: result})
	}
	return initialReadFixtureV1{store, db, path, parent, plan, page, input}
}

func TestRecognitionInitialReadCompactV1RawSettlementAndReplay(t *testing.T) {
	raw, err := os.ReadFile("../testdata/initial_read_compact_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f := compactInitialReadStorageFixtureV1(t, string(raw))
	changed := f.input
	changed.Candidates = append([]k12.RecognitionLayoutInitialCandidateV1(nil), f.input.Candidates...)
	changed.Candidates[0].FirstReadJSON = json.RawMessage(strings.Replace(string(changed.Candidates[0].FirstReadJSON), "做2朵花", "做9朵花", 1))
	if _, created, err := f.store.AuthorizeAndSettleRecognitionLayoutInitialReadV1(ctx, f.parent.AgentName, f.parent.InvocationID, f.plan, changed); err == nil || created {
		t.Fatal("unproven expanded question was committed")
	}
	assertCompactInitialReadUnsettledV1(t, f.db)
	initial := f.settle(t)
	if len(initial.FirstReads) != 2 || len(initial.ReviewAuthorizations) != 2 || len(initial.FrozenResults) != 0 {
		t.Fatal("compact transport changed required review or target coverage")
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, db := openPhysicalLedgerFileStore(t, f.path)
	defer db.Close()
	replay := f.input
	replay.Classification, replay.Candidates = "", nil
	again, created, err := reopened.AuthorizeAndSettleRecognitionLayoutInitialReadV1(ctx, f.parent.AgentName, f.parent.InvocationID, f.plan, replay)
	if err != nil || created || !reflect.DeepEqual(initial, again) {
		t.Fatalf("compact cold replay changed settlement: created=%v err=%v", created, err)
	}
	var persistedRaw, persistedDigest string
	var physicalCount int
	if err := db.QueryRowContext(ctx, `SELECT result_content,result_digest FROM k12_model_physical_invocations WHERE physical_invocation_id=?`, f.plan.ManifestInvocationID).Scan(&persistedRaw, &persistedDigest); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_model_physical_invocations`).Scan(&physicalCount); err != nil {
		t.Fatal(err)
	}
	if persistedRaw != string(raw) || persistedDigest != f.plan.ManifestResultDigest || physicalCount != 1 {
		t.Fatal("raw provider response, digest or physical-call count changed")
	}
}

func TestRecognitionInitialReadCompactV1InvalidReferenceCannotSettle(t *testing.T) {
	raw, err := os.ReadFile("../testdata/initial_read_compact_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	invalid := strings.Replace(string(raw), `"shared": [0]`, `"shared": [9]`, 1)
	f := compactInitialReadStorageFixtureV1(t, invalid)
	if _, created, err := f.store.AuthorizeAndSettleRecognitionLayoutInitialReadV1(context.Background(), f.parent.AgentName, f.parent.InvocationID, f.plan, f.input); err == nil || created {
		t.Fatal("invalid physical response was replaced by a valid derived entry")
	}
	assertCompactInitialReadUnsettledV1(t, f.db)
}

func assertCompactInitialReadUnsettledV1(t *testing.T, db *sql.DB) {
	t.Helper()
	var settlement sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT initial_read_settlement_json FROM k12_recognition_layout_plans`).Scan(&settlement); err != nil {
		t.Fatal(err)
	}
	if settlement.Valid && settlement.String != "" {
		t.Fatal("failed source verification persisted a settlement")
	}
}
