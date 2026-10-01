package k12storage_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"image/png"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

type initialReadFixtureV1 struct {
	store  *k12storage.Store
	db     *sql.DB
	path   string
	parent k12.ModelInvocation
	plan   k12.RecognitionLayoutPlanV2
	page   []byte
	input  k12.RecognitionLayoutInitialReadSettlementV1
}

func newInitialReadFixtureV1(t *testing.T, review bool, legacy ...bool) initialReadFixtureV1 {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "initial.db")
	store, db := openPhysicalLedgerFileStore(t, path)
	migrations := migrate.All
	mode := k12.RecognitionLayoutManifestWithContentV1
	if len(legacy) > 0 && legacy[0] {
		mode = ""
		migrations = append([]migrate.Migration(nil), migrate.All[:len(migrate.All)-1]...)
	}
	if err := migrate.Run(ctx, db, migrations); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO agents(name) VALUES('mingming')`); err != nil {
		t.Fatal(err)
	}
	job := newGradingJobRecord(t, "mingming", "combined-read")
	if _, err := store.Put(ctx, job); err != nil {
		t.Fatal(err)
	}
	parent := unpreparedPhysicalInvocationParent(job.RecordID)
	page := recognitionLayoutRuntimeTestPagePNG(t)
	targets := []k12.RecognitionLayoutManifestTargetV2{{ManifestRef: "manifest_0001", ManifestOrder: 1, SourceNumberPath: []string{"1"}, DisplayLabel: "1", Region: k12.SourcePixelRegion{X: 0, Y: 0, Width: 20, Height: 10}}, {ManifestRef: "manifest_0002", ManifestOrder: 2, SourceNumberPath: []string{"2"}, DisplayLabel: "2", Region: k12.SourcePixelRegion{X: 0, Y: 20, Width: 20, Height: 10}}}
	entries := make([]json.RawMessage, 2)
	for i, target := range targets {
		entries[i], _ = json.Marshal(map[string]any{"manifest_ref": target.ManifestRef, "manifest_order": target.ManifestOrder, "source_number_path": target.SourceNumberPath, "display_label": target.DisplayLabel, "source_section_path": []string{}, "source_section_label": "", "region": target.Region, "initial_read": map[string]any{"kind": "question", "recognition": map[string]any{"question": "每朵花用3/8张纸", "student_answer": []string{"3/4张纸", "3张纸"}[i]}, "shared_conditions": "每朵花用3/8张纸", "answer_ownership": []string{"第一题手写", "第二题手写"}[i]}})
	}
	body, _ := json.Marshal(map[string]any{"targets": entries})
	entries, canonicalErr := k12.CanonicalRecognitionLayoutInitialReadEntriesV1(string(body))
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	digest := recognitionLayoutRuntimeTestDigest(string(body))
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{InitialReadMode: mode, EnableSourceAdjudication: mode != "", RecognitionFormat: k12.RecognitionLayoutCompactV4, PagePNG: page, Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: "whole-combined", ResultDigest: digest}, Targets: targets})
	if err != nil {
		t.Fatal(err)
	}
	header := k12.RecognitionLayoutPlanHeaderV2{InitialReadMode: mode, PlanID: "combined-plan", ParentInvocationID: parent.InvocationID, AgentName: parent.AgentName, JobID: parent.JobID, PageDigest: plan.PageDigest, ParentRequestDigest: parent.RequestDigest, RouteSnapshot: parent.RouteSnapshot, RequestPolicySnapshot: parent.RequestPolicySnapshot, StageStartedAtUnixMillis: time.Now().UnixMilli(), PhysicalCallCapMillis: 120000, BudgetBuckets: k12.RecognitionLayoutBudgetBucketsV2{UpTo1ProblemMillis: 600000, UpTo8ProblemsMillis: 600000, UpTo16ProblemsMillis: 600000, UpTo32ProblemsMillis: 600000}, AdapterWorkerHardCap: 2, EffectiveConcurrency: 1}
	headerDigest, err := k12.RecognitionLayoutPlanHeaderDigestV2(header)
	if err != nil {
		t.Fatal(err)
	}
	child := newPhysicalInvocation(parent, plan.ManifestInvocationID, k12.RecognitionPhysicalUnitWholePage)
	child.RecognitionPlanVersion = k12.RecognitionPlanVersionV2
	child.PlanDigest = headerDigest
	parent, child, created, err := store.PrepareRecognizingInvocationWithInitialLayoutPlanV2(ctx, parent, child, header)
	if err != nil || !created {
		t.Fatalf("prepare %v %v", created, err)
	}
	if _, claimed, err := store.ClaimModelPhysicalInvocationSent(ctx, parent.AgentName, child.PhysicalInvocationID); err != nil || !claimed {
		t.Fatalf("claim %v %v", claimed, err)
	}
	if _, err := store.MarkModelPhysicalInvocationSucceededWithContent(ctx, parent.AgentName, child.PhysicalInvocationID, string(body), "provider-whole"); err != nil {
		t.Fatal(err)
	}
	in := k12.RecognitionLayoutInitialReadSettlementV1{PlanDigest: plan.AuthorizedPlanDigest, SourcePhysicalInvocationID: child.PhysicalInvocationID, SourcePhysicalUnit: child.PhysicalUnit, SourcePhysicalResultDigest: digest, Classification: k12.RecognitionLayoutBatchClassifiedV2}
	for i, target := range plan.Targets {
		class := k12.RecognitionLayoutCandidateValidV2
		if review {
			class = k12.RecognitionLayoutCandidateReviewRequiredV2
		}
		result, _ := json.Marshal(map[string]any{"student_answer": []string{"3/4张纸", "3张纸"}[i], "text": "每朵花用3/8张纸"})
		in.Candidates = append(in.Candidates, k12.RecognitionLayoutInitialCandidateV1{CandidateID: target.TargetID, Classification: class, ResultKind: k12.RecognitionLayoutCandidateQuestionV2, FirstReadJSON: entries[i], ResultJSON: result})
	}
	return initialReadFixtureV1{store, db, path, parent, plan, page, in}
}

func (f initialReadFixtureV1) settle(t *testing.T) k12.RecognitionLayoutInitialReadSettlementResultV1 {
	t.Helper()
	out, created, err := f.store.AuthorizeAndSettleRecognitionLayoutInitialReadV1(context.Background(), f.parent.AgentName, f.parent.InvocationID, f.plan, f.input)
	if err != nil || !created {
		t.Fatalf("initial settle created=%v err=%v", created, err)
	}
	return out
}

func (f initialReadFixtureV1) review(t *testing.T, initial k12.RecognitionLayoutInitialReadSettlementResultV1, batchNumber ...int) k12.RecognitionLayoutReviewBatchAuthorizationV1 {
	t.Helper()
	ids := make([]string, 0, len(initial.ReviewAuthorizations))
	for _, member := range initial.ReviewAuthorizations {
		ids = append(ids, member.CandidateID)
	}
	image, err := k12.BuildRecognitionLayoutReviewBatchImageV1(f.page, f.plan, ids)
	if err != nil {
		t.Fatal(err)
	}
	size, err := png.DecodeConfig(bytes.NewReader(image))
	if err != nil {
		t.Fatal(err)
	}
	number := 1
	if len(batchNumber) > 0 {
		number = batchNumber[0]
	}
	unit, err := k12.RecognitionLayoutReviewUnitV1(number)
	if err != nil {
		t.Fatal(err)
	}
	in := k12.RecognitionLayoutReviewBatchAuthorizationRequestV1{PlanDigest: f.plan.AuthorizedPlanDigest, PhysicalUnit: unit, Members: initial.ReviewAuthorizations, ImageDigest: recognitionLayoutRuntimeTestDigest(string(image)), ImageWidth: size.Width, ImageHeight: size.Height, PromptDigest: recognitionLayoutRuntimeTestDigest("independent pixels only"), RecognitionFormat: k12.RecognitionLayoutCompactV4}
	in.InputDigest, err = k12.RecognitionLayoutReviewBatchInputDigestV1(in)
	if err != nil {
		t.Fatal(err)
	}
	out, created, err := f.store.AuthorizeRecognitionLayoutReviewBatchV1(context.Background(), f.parent.AgentName, f.parent.InvocationID, in)
	if err != nil || !created {
		t.Fatalf("review authorize created=%v err=%v", created, err)
	}
	return out
}

func (f initialReadFixtureV1) prepareReview(t *testing.T, auth k12.RecognitionLayoutReviewBatchAuthorizationV1) k12.ModelPhysicalInvocation {
	t.Helper()
	child := newPhysicalInvocation(f.parent, "physical-"+string(auth.PhysicalUnit), auth.PhysicalUnit)
	child.RecognitionPlanVersion = k12.RecognitionPlanVersionV2
	child.PlanDigest = f.plan.AuthorizedPlanDigest
	child.CandidateExactSetDigest = auth.ExactSetDigest
	stored, created, err := f.store.PrepareModelPhysicalInvocation(context.Background(), child)
	if err != nil || !created {
		t.Fatalf("prepare review %v %v", created, err)
	}
	if _, claimed, err := f.store.ClaimModelPhysicalInvocationSent(context.Background(), f.parent.AgentName, stored.PhysicalInvocationID); err != nil || !claimed {
		t.Fatalf("claim review %v %v", claimed, err)
	}
	return stored
}

func TestRecognitionInitialReadV1AtomicReplayAndReview(t *testing.T) {
	ctx := context.Background()
	t.Run("atomic_coverage_and_raw_binding", func(t *testing.T) {
		f := newInitialReadFixtureV1(t, false)
		bad := f.input
		bad.Candidates = append([]k12.RecognitionLayoutInitialCandidateV1(nil), bad.Candidates[:1]...)
		if _, _, err := f.store.AuthorizeAndSettleRecognitionLayoutInitialReadV1(ctx, f.parent.AgentName, f.parent.InvocationID, f.plan, bad); err == nil {
			t.Fatal("missing target accepted")
		}
		var count int
		f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_recognition_layout_candidates`).Scan(&count)
		if count != 0 {
			t.Fatal("partial plan escaped rollback")
		}
		bad = f.input
		bad.Candidates = append([]k12.RecognitionLayoutInitialCandidateV1(nil), bad.Candidates...)
		bad.Candidates[0].FirstReadJSON, bad.Candidates[1].FirstReadJSON = bad.Candidates[1].FirstReadJSON, bad.Candidates[0].FirstReadJSON
		if _, _, err := f.store.AuthorizeAndSettleRecognitionLayoutInitialReadV1(ctx, f.parent.AgentName, f.parent.InvocationID, f.plan, bad); err == nil {
			t.Fatal("neighbor source swap accepted")
		}
		if _, err := f.db.ExecContext(ctx, `CREATE TRIGGER reject_second_initial BEFORE INSERT ON k12_recognition_layout_candidate_results WHEN NEW.candidate_id='`+f.plan.Targets[1].TargetID+`' BEGIN SELECT RAISE(ABORT,'controlled second candidate failure'); END;`); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.store.AuthorizeAndSettleRecognitionLayoutInitialReadV1(ctx, f.parent.AgentName, f.parent.InvocationID, f.plan, f.input); err == nil {
			t.Fatal("controlled insertion failure did not occur")
		}
		var status string
		var private sql.NullString
		if err := f.db.QueryRowContext(ctx, `SELECT status,initial_read_settlement_json FROM k12_recognition_layout_plans`).Scan(&status, &private); err != nil {
			t.Fatal(err)
		}
		if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_recognition_layout_candidates`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 || private.Valid || status != "manifest_succeeded" {
			t.Fatalf("partial atomic settlement escaped: count=%d private=%v status=%s", count, private.Valid, status)
		}
		if _, err := f.db.ExecContext(ctx, `DROP TRIGGER reject_second_initial`); err != nil {
			t.Fatal(err)
		}
		f.settle(t)
	})
	t.Run("whole_only_final_and_cold_replay", func(t *testing.T) {
		f := newInitialReadFixtureV1(t, false)
		initial := f.settle(t)
		final, created, err := f.store.FinalizeRecognitionLayoutPlanV2(ctx, f.parent.AgentName, f.parent.InvocationID)
		if err != nil || !created || final.PhysicalResultCount != 1 || final.CandidateResultCount != 2 {
			t.Fatalf("whole final %+v created=%v err=%v", final, created, err)
		}
		if err := f.db.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, db := openPhysicalLedgerFileStore(t, f.path)
		defer db.Close()
		replay := f.input
		replay.Classification = ""
		replay.Candidates = nil
		out, created, err := reopened.AuthorizeAndSettleRecognitionLayoutInitialReadV1(ctx, f.parent.AgentName, f.parent.InvocationID, f.plan, replay)
		if err != nil || created || !reflect.DeepEqual(initial, out) {
			t.Fatalf("cold initial replay created=%v err=%v", created, err)
		}
		again, created, err := reopened.FinalizeRecognitionLayoutPlanV2(ctx, f.parent.AgentName, f.parent.InvocationID)
		if err != nil || created || !reflect.DeepEqual(final, again) {
			t.Fatalf("cold final replay %v %v", created, err)
		}
		var count int
		db.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_model_physical_invocations`).Scan(&count)
		if count != 1 {
			t.Fatalf("replay changed physical count %d", count)
		}
	})
	t.Run("one_real_review_multiple_targets_and_frozen_input", func(t *testing.T) {
		f := newInitialReadFixtureV1(t, true)
		initial := f.settle(t)
		auth := f.review(t, initial)
		bad := auth.RecognitionLayoutReviewBatchAuthorizationRequestV1
		bad.PromptDigest = recognitionLayoutRuntimeTestDigest("changed prompt")
		bad.InputDigest, _ = k12.RecognitionLayoutReviewBatchInputDigestV1(bad)
		if _, _, err := f.store.AuthorizeRecognitionLayoutReviewBatchV1(ctx, f.parent.AgentName, f.parent.InvocationID, bad); err == nil {
			t.Fatal("frozen prompt changed")
		}
		child := f.prepareReview(t, auth)
		child, err := f.store.MarkModelPhysicalInvocationSucceededWithContent(ctx, f.parent.AgentName, child.PhysicalInvocationID, `{"items":["one","two"]}`, "provider-review")
		if err != nil {
			t.Fatal(err)
		}
		in := k12.RecognitionLayoutReviewBatchSettlementV1{PlanDigest: f.plan.AuthorizedPlanDigest, AuthorizationID: auth.AuthorizationID, AuthorizationDigest: auth.AuthorizationDigest, SourcePhysicalInvocationID: child.PhysicalInvocationID, SourcePhysicalUnit: child.PhysicalUnit, SourcePhysicalResultDigest: child.ResultDigest, Classification: k12.RecognitionLayoutBatchClassifiedV2}
		for _, candidate := range f.input.Candidates {
			in.Candidates = append(in.Candidates, k12.RecognitionLayoutCandidateSettlementV2{CandidateID: candidate.CandidateID, Classification: k12.RecognitionLayoutCandidateValidV2, ResultKind: candidate.ResultKind, ResultJSON: candidate.ResultJSON})
		}
		settled, created, err := f.store.SettleRecognitionLayoutReviewBatchV1(ctx, f.parent.AgentName, f.parent.InvocationID, in)
		if err != nil || !created || len(settled.FrozenResults) != 2 {
			t.Fatalf("review settle %v %v", created, err)
		}
		final, _, err := f.store.FinalizeRecognitionLayoutPlanV2(ctx, f.parent.AgentName, f.parent.InvocationID)
		if err != nil || final.PhysicalResultCount != 2 {
			t.Fatalf("final review %v %v", final.PhysicalResultCount, err)
		}
		for _, candidate := range final.CandidateResults {
			if candidate.SourcePhysicalInvocationID != child.PhysicalInvocationID {
				t.Fatal("fabricated per target call")
			}
		}
		if err = f.db.Close(); err != nil {
			t.Fatal(err)
		}
		store, db := openPhysicalLedgerFileStore(t, f.path)
		defer db.Close()
		in.Classification = ""
		in.Candidates = nil
		again, created, err := store.SettleRecognitionLayoutReviewBatchV1(ctx, f.parent.AgentName, f.parent.InvocationID, in)
		if err != nil || created || !reflect.DeepEqual(settled, again) {
			t.Fatalf("review cold replay %v %v", created, err)
		}
		runtime, err := store.LoadRecognitionLayoutPlanRuntimeV2(ctx, f.parent.AgentName, f.parent.InvocationID)
		if err != nil || len(runtime.ReviewBatches) != 1 || !reflect.DeepEqual(runtime.ReviewBatches[0], auth) {
			t.Fatalf("runtime frozen reviews %v", err)
		}
	})
	t.Run("review_conflict_uses_existing_adjudication", func(t *testing.T) {
		f := newInitialReadFixtureV1(t, true)
		initial := f.settle(t)
		review := f.review(t, initial)
		child := f.prepareReview(t, review)
		child, err := f.store.MarkModelPhysicalInvocationSucceededWithContent(ctx, f.parent.AgentName, child.PhysicalInvocationID, `{"items":["one","two"]}`, "provider-review")
		if err != nil {
			t.Fatal(err)
		}
		conflict := json.RawMessage(`{"answer_evidence_transcriptions":["3/4","5/4"],"evidence_transcriptions":["3/8*2","3/8*2"],"text":"3/8*2"}`)
		in := k12.RecognitionLayoutReviewBatchSettlementV1{PlanDigest: f.plan.AuthorizedPlanDigest, AuthorizationID: review.AuthorizationID, AuthorizationDigest: review.AuthorizationDigest, SourcePhysicalInvocationID: child.PhysicalInvocationID, SourcePhysicalUnit: child.PhysicalUnit, SourcePhysicalResultDigest: child.ResultDigest, Classification: k12.RecognitionLayoutBatchClassifiedV2}
		for i, candidate := range f.input.Candidates {
			raw := candidate.ResultJSON
			if i == 0 {
				raw = conflict
			}
			in.Candidates = append(in.Candidates, k12.RecognitionLayoutCandidateSettlementV2{CandidateID: candidate.CandidateID, Classification: k12.RecognitionLayoutCandidateValidV2, ResultKind: candidate.ResultKind, ResultJSON: raw})
		}
		if _, _, err = f.store.SettleRecognitionLayoutReviewBatchV1(ctx, f.parent.AgentName, f.parent.InvocationID, in); err != nil {
			t.Fatal(err)
		}
		request := k12.RecognitionLayoutAdjudicationRequestV2{PlanDigest: f.plan.AuthorizedPlanDigest, CandidateID: f.plan.Targets[0].TargetID, PrimaryPhysicalInvocationID: f.input.SourcePhysicalInvocationID, PrimaryPhysicalResultDigest: f.input.SourcePhysicalResultDigest, RepairPhysicalInvocationID: child.PhysicalInvocationID, RepairPhysicalResultDigest: child.ResultDigest, ConflictKind: "answer"}
		auth, created, err := f.store.AuthorizeRecognitionLayoutAdjudicationV2(ctx, f.parent.AgentName, f.parent.InvocationID, request)
		if err != nil || !created {
			t.Fatalf("B adjudication authorization %v %v", created, err)
		}
		adjud := newPhysicalInvocation(f.parent, "review-adjudication", auth.PhysicalUnit)
		adjud.RecognitionPlanVersion = k12.RecognitionPlanVersionV2
		adjud.PlanDigest = f.plan.AuthorizedPlanDigest
		adjud.CandidateExactSetDigest, _ = k12.RecognitionLayoutTargetExactSetDigestV2([]string{auth.CandidateID})
		adjud, _, err = f.store.PrepareModelPhysicalInvocation(ctx, adjud)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = f.store.ClaimModelPhysicalInvocationSent(ctx, f.parent.AgentName, adjud.PhysicalInvocationID); err != nil {
			t.Fatal(err)
		}
		adjud, err = f.store.MarkModelPhysicalInvocationSucceededWithContent(ctx, f.parent.AgentName, adjud.PhysicalInvocationID, `{"answer":"3/4"}`, "provider-adjudication")
		if err != nil {
			t.Fatal(err)
		}
		settlement := k12.RecognitionLayoutAdjudicationSettlementV2{PlanDigest: f.plan.AuthorizedPlanDigest, AuthorizationID: auth.AuthorizationID, AuthorizationDigest: auth.AuthorizationDigest, CandidateID: auth.CandidateID, SourcePhysicalInvocationID: adjud.PhysicalInvocationID, SourcePhysicalUnit: adjud.PhysicalUnit, SourcePhysicalResultDigest: adjud.ResultDigest, Adopted: true, MatchedQuestionPrior: "both", MatchedAnswerPrior: "primary", ResultKind: k12.RecognitionLayoutCandidateQuestionV2, ResultJSON: f.input.Candidates[0].ResultJSON}
		if _, _, err = f.store.SettleRecognitionLayoutAdjudicationV2(ctx, f.parent.AgentName, f.parent.InvocationID, settlement); err != nil {
			t.Fatal(err)
		}
		final, _, err := f.store.FinalizeRecognitionLayoutPlanV2(ctx, f.parent.AgentName, f.parent.InvocationID)
		if err != nil {
			t.Fatal(err)
		}
		if final.PhysicalResultCount != 3 || final.CandidateResults[0].SourcePhysicalInvocationID != adjud.PhysicalInvocationID || string(final.CandidateResults[0].OriginalCandidateJSON) != string(conflict) || final.CandidateResults[1].SourcePhysicalInvocationID != child.PhysicalInvocationID {
			t.Fatalf("B adjudication changed provenance %+v", final)
		}
	})
	t.Run("unknown_does_not_split_or_regroup", func(t *testing.T) {
		f := newInitialReadFixtureV1(t, true)
		initial := f.settle(t)
		auth := f.review(t, initial)
		child := f.prepareReview(t, auth)
		if _, err := f.store.MarkModelPhysicalInvocationOutcomeUnknown(ctx, f.parent.AgentName, child.PhysicalInvocationID, "timeout"); err != nil {
			t.Fatal(err)
		}
		changed := auth.RecognitionLayoutReviewBatchAuthorizationRequestV1
		changed.PhysicalUnit, _ = k12.RecognitionLayoutReviewUnitV1(2)
		changed.Members = changed.Members[:1]
		changed.InputDigest, _ = k12.RecognitionLayoutReviewBatchInputDigestV1(changed)
		if _, _, err := f.store.AuthorizeRecognitionLayoutReviewBatchV1(ctx, f.parent.AgentName, f.parent.InvocationID, changed); err == nil {
			t.Fatal("unknown batch split accepted")
		}
		if _, _, err := f.store.FinalizeRecognitionLayoutPlanV2(ctx, f.parent.AgentName, f.parent.InvocationID); err == nil {
			t.Fatal("unknown batch finalized")
		}
		if _, claimed, err := f.store.ClaimModelPhysicalInvocationSent(ctx, f.parent.AgentName, child.PhysicalInvocationID); err == nil && claimed {
			t.Fatal("unknown physical resent")
		}
		var count int
		f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_model_physical_invocations`).Scan(&count)
		if count != 2 {
			t.Fatal("unknown added physical invocation")
		}
	})
}

func TestRecognitionInitialReadV1PreservesLegacyMigrationBytes(t *testing.T) {
	ctx := context.Background()
	f := newInitialReadFixtureV1(t, false, true)
	if err := f.store.AuthorizeRecognitionLayoutPlanV2(ctx, f.parent.AgentName, f.parent.InvocationID, k12.RecognitionLayoutManifestSuccessV2{InvocationID: f.plan.ManifestInvocationID, ResultDigest: f.plan.ManifestResultDigest}, f.plan); err != nil {
		t.Fatal(err)
	}
	for index, batch := range f.plan.Batches {
		child := recognitionLayoutRuntimeBatchInvocation(t, f.parent, f.plan, index)
		child, _, err := f.store.PrepareModelPhysicalInvocation(ctx, child)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = f.store.ClaimModelPhysicalInvocationSent(ctx, f.parent.AgentName, child.PhysicalInvocationID); err != nil {
			t.Fatal(err)
		}
		child, err = f.store.MarkModelPhysicalInvocationSucceededWithContent(ctx, f.parent.AgentName, child.PhysicalInvocationID, `{"items":["legacy"]}`, "legacy-provider")
		if err != nil {
			t.Fatal(err)
		}
		in := k12.RecognitionLayoutPrimaryBatchSettlementV2{PlanDigest: f.plan.AuthorizedPlanDigest, SourcePhysicalInvocationID: child.PhysicalInvocationID, SourcePhysicalUnit: child.PhysicalUnit, SourcePhysicalResultDigest: child.ResultDigest, Classification: k12.RecognitionLayoutBatchClassifiedV2}
		for _, id := range batch.TargetIDs {
			in.Candidates = append(in.Candidates, k12.RecognitionLayoutCandidateSettlementV2{CandidateID: id, Classification: k12.RecognitionLayoutCandidateValidV2, ResultKind: k12.RecognitionLayoutCandidateQuestionV2, ResultJSON: json.RawMessage(`{"student_answer":"3/4","text":"3/8*2"}`)})
		}
		if _, _, err = f.store.SettleRecognitionLayoutPrimaryBatchV2(ctx, f.parent.AgentName, f.parent.InvocationID, in); err != nil {
			t.Fatal(err)
		}
	}
	beforeFinal, _, err := f.store.FinalizeRecognitionLayoutPlanV2(ctx, f.parent.AgentName, f.parent.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	queries := []string{`SELECT physical_invocation_id,parent_invocation_id,physical_unit,request_digest,route_snapshot_json,request_policy_snapshot_json,status,attempt,result_digest,result_content,external_request_id,failure_kind,created_at,updated_at,recognition_plan_version,plan_digest,candidate_exact_set_digest FROM k12_model_physical_invocations ORDER BY physical_invocation_id`, `SELECT layout_header_json,header_digest,authorized_plan_json,authorized_plan_digest,status,manifest_result_digest,candidate_exact_set_digest FROM k12_recognition_layout_plans`, `SELECT candidate_json,crop_digest FROM k12_recognition_layout_candidates ORDER BY ordinal`, `SELECT result_json,result_digest,source_physical_invocation_id,source_physical_result_digest FROM k12_recognition_layout_candidate_results ORDER BY candidate_id`, `SELECT finalization_json,finalization_digest FROM k12_recognition_layout_finalizations`}
	snapshot := func() [][]byte {
		var all [][]byte
		for _, query := range queries {
			rows, e := f.db.QueryContext(ctx, query)
			if e != nil {
				t.Fatal(e)
			}
			columns, e := rows.Columns()
			if e != nil {
				t.Fatal(e)
			}
			for rows.Next() {
				values := make([]any, len(columns))
				pointers := make([]any, len(columns))
				for i := range values {
					pointers[i] = &values[i]
				}
				if e = rows.Scan(pointers...); e != nil {
					t.Fatal(e)
				}
				raw, e := json.Marshal(values)
				if e != nil {
					t.Fatal(e)
				}
				all = append(all, raw)
			}
			if e = rows.Err(); e != nil {
				t.Fatal(e)
			}
			rows.Close()
		}
		return all
	}
	before := snapshot()
	if err = migrate.Run(ctx, f.db, migrate.All); err != nil {
		t.Fatal(err)
	}
	after := snapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("V122 changed legacy canonical bytes or physical receipts")
	}
	var initial, review, settlement int
	if err = f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_recognition_layout_plans WHERE initial_read_settlement_json IS NOT NULL`).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_recognition_layout_batches WHERE review_authorization_json IS NOT NULL`).Scan(&review); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_recognition_layout_batch_settlements WHERE review_settlement_json IS NOT NULL`).Scan(&settlement); err != nil {
		t.Fatal(err)
	}
	if initial+review+settlement != 0 {
		t.Fatal("legacy rows received new mode facts")
	}
	afterFinal, created, err := f.store.FinalizeRecognitionLayoutPlanV2(ctx, f.parent.AgentName, f.parent.InvocationID)
	if err != nil || created || !reflect.DeepEqual(beforeFinal, afterFinal) {
		t.Fatalf("legacy final replay drifted created=%v err=%v", created, err)
	}
}
