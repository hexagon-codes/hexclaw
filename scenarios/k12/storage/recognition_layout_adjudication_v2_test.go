package k12storage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func TestRecognitionLayoutAdjudicationDurableEvidence(t *testing.T) {
	for _, scenario := range []string{"adopted", "unresolved", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			store, db, path, parent, plan, batches := prepareRecognitionLayoutSettlementFixture(t, ctx, true)
			defer func() { db.Close() }()
			primary, _, err := store.SettleRecognitionLayoutPrimaryBatchV2(ctx, parent.AgentName, parent.InvocationID, recognitionLayoutRepairablePrimarySettlement(plan, batches[0]))
			if err != nil {
				t.Fatal(err)
			}
			settleRecognitionLayoutAllPrimaryValid(t, ctx, store, parent, plan, batches[1])
			original := json.RawMessage(`{"answer_evidence_transcriptions":["6","8"],"evidence_transcriptions":["18/3=","18/3="],"text":"18/3="}`)
			for _, auth := range primary.RepairAuthorizations {
				settleRecognitionLayoutFinalizationRepair(t, ctx, store, parent, plan, auth, k12.RecognitionLayoutCandidateValidV2, original)
			}
			repair, err := store.GetModelPhysicalInvocation(ctx, parent.AgentName, "physical-finalization-"+string(primary.RepairAuthorizations[0].PhysicalUnit))
			if err != nil {
				t.Fatal(err)
			}
			request := k12.RecognitionLayoutAdjudicationRequestV2{PlanDigest: plan.AuthorizedPlanDigest, CandidateID: primary.RepairAuthorizations[0].CandidateID, PrimaryPhysicalInvocationID: batches[0].PhysicalInvocationID, PrimaryPhysicalResultDigest: batches[0].ResultDigest, RepairPhysicalInvocationID: repair.PhysicalInvocationID, RepairPhysicalResultDigest: repair.ResultDigest, ConflictKind: "answer"}
			wrong := request
			wrong.PrimaryPhysicalResultDigest = recognitionLayoutRuntimeTestDigest("wrong")
			if _, _, err := store.AuthorizeRecognitionLayoutAdjudicationV2(ctx, parent.AgentName, parent.InvocationID, wrong); err == nil {
				t.Fatal("drifted source authorized")
			}
			auth, created, err := store.AuthorizeRecognitionLayoutAdjudicationV2(ctx, parent.AgentName, parent.InvocationID, request)
			if err != nil || !created {
				t.Fatalf("authorize: %+v %v", auth, err)
			}
			if replay, fresh, err := store.AuthorizeRecognitionLayoutAdjudicationV2(ctx, parent.AgentName, parent.InvocationID, request); err != nil || fresh || !reflect.DeepEqual(auth, replay) {
				t.Fatalf("authorization replay drift: %+v %v", replay, err)
			}
			child := newPhysicalInvocation(parent, "physical-adjudication", auth.PhysicalUnit)
			child.RecognitionPlanVersion = k12.RecognitionPlanVersionV2
			child.PlanDigest = plan.AuthorizedPlanDigest
			child.CandidateExactSetDigest, _ = k12.RecognitionLayoutTargetExactSetDigestV2([]string{auth.CandidateID})
			prepared, created, err := store.PrepareModelPhysicalInvocation(ctx, child)
			if err != nil || !created {
				t.Fatalf("prepare adjudication: %+v %v", prepared, err)
			}
			if _, claimed, err := store.ClaimModelPhysicalInvocationSent(ctx, parent.AgentName, prepared.PhysicalInvocationID); err != nil || !claimed {
				t.Fatalf("claim: %v %v", claimed, err)
			}
			if scenario == "unknown" {
				unknown, err := store.MarkModelPhysicalInvocationOutcomeUnknown(ctx, parent.AgentName, prepared.PhysicalInvocationID, "transport_unknown")
				if err != nil {
					t.Fatal(err)
				}
				if _, claimed, err := store.ClaimModelPhysicalInvocationSent(ctx, parent.AgentName, prepared.PhysicalInvocationID); claimed {
					t.Fatalf("unknown resent: %v %v", claimed, err)
				}
				if _, _, err := store.FinalizeRecognitionLayoutPlanV2(ctx, parent.AgentName, parent.InvocationID); err == nil {
					t.Fatal("unknown finalized")
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				store, db = openPhysicalLedgerFileStore(t, path)
				restored, err := store.GetModelPhysicalInvocation(ctx, parent.AgentName, prepared.PhysicalInvocationID)
				if err != nil || !reflect.DeepEqual(restored, unknown) {
					t.Fatalf("unknown lost on restart: %+v %v", restored, err)
				}
				return
			}
			succeeded, err := store.MarkModelPhysicalInvocationSucceededWithContent(ctx, parent.AgentName, prepared.PhysicalInvocationID, `{"independent":"6"}`, "provider-adjudication")
			if err != nil {
				t.Fatal(err)
			}
			settlement := k12.RecognitionLayoutAdjudicationSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, AuthorizationID: auth.AuthorizationID, AuthorizationDigest: auth.AuthorizationDigest, CandidateID: auth.CandidateID, SourcePhysicalInvocationID: succeeded.PhysicalInvocationID, SourcePhysicalUnit: succeeded.PhysicalUnit, SourcePhysicalResultDigest: succeeded.ResultDigest, Adopted: scenario == "adopted"}
			if settlement.Adopted {
				settlement.MatchedQuestionPrior = "both"
				settlement.MatchedAnswerPrior = "primary"
				settlement.ResultKind = k12.RecognitionLayoutCandidateQuestionV2
				settlement.ResultJSON = json.RawMessage(`{"student_answer":"6","text":"18/3="}`)
			}
			receipt, created, err := store.SettleRecognitionLayoutAdjudicationV2(ctx, parent.AgentName, parent.InvocationID, settlement)
			if err != nil || !created {
				t.Fatalf("settle: %+v %v", receipt, err)
			}
			if replay, fresh, err := store.SettleRecognitionLayoutAdjudicationV2(ctx, parent.AgentName, parent.InvocationID, settlement); err != nil || fresh || !reflect.DeepEqual(receipt, replay) {
				t.Fatalf("settlement replay changed: %+v %v", replay, err)
			}
			changed := settlement
			changed.SourcePhysicalResultDigest = recognitionLayoutRuntimeTestDigest("wrong")
			if _, _, err := store.SettleRecognitionLayoutAdjudicationV2(ctx, parent.AgentName, parent.InvocationID, changed); err == nil {
				t.Fatal("settlement source drift accepted")
			}
			final, created, err := store.FinalizeRecognitionLayoutPlanV2(ctx, parent.AgentName, parent.InvocationID)
			if err != nil || !created {
				t.Fatalf("finalize: %+v %v", final, err)
			}
			if final.PhysicalResultCount != 6 {
				t.Fatalf("adjudication missing from exact set: %+v", final.PhysicalResults)
			}
			var candidate k12.RecognitionLayoutCandidateFinalResultV2
			for _, v := range final.CandidateResults {
				if v.CandidateID == auth.CandidateID {
					candidate = v
				}
			}
			if settlement.Adopted {
				if string(candidate.OriginalCandidateJSON) != string(original) || string(candidate.ResultJSON) != string(settlement.ResultJSON) || candidate.Adjudication == nil || candidate.SourcePhysicalInvocationID != succeeded.PhysicalInvocationID {
					t.Fatalf("adopted projection: %+v", candidate)
				}
			} else if string(candidate.ResultJSON) != string(original) || candidate.Adjudication != nil {
				t.Fatalf("unresolved projection changed: %+v", candidate)
			}
			var stored string
			if err := db.QueryRowContext(ctx, `SELECT result_json FROM k12_recognition_layout_candidate_results WHERE plan_id=? AND candidate_id=?`, final.PlanID, auth.CandidateID).Scan(&stored); err != nil || stored != string(original) {
				t.Fatalf("original candidate changed: %s %v", stored, err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			store, db = openPhysicalLedgerFileStore(t, path)
			if replay, fresh, err := store.FinalizeRecognitionLayoutPlanV2(ctx, parent.AgentName, parent.InvocationID); err != nil || fresh || !reflect.DeepEqual(final, replay) {
				t.Fatalf("restart finalized replay drift: %+v %v", replay, err)
			}
			if _, claimed, err := store.ClaimModelPhysicalInvocationSent(ctx, parent.AgentName, succeeded.PhysicalInvocationID); claimed {
				t.Fatalf("succeeded result resent: %v %v", claimed, err)
			}
			if scenario == "adopted" {
				assertRecognitionAdjudicationRetryReusesSuccess(t, ctx, store, parent, plan, original, succeeded)
			}
		})
	}
}

func assertRecognitionAdjudicationRetryReusesSuccess(t *testing.T, ctx context.Context, store *k12storage.Store, prior k12.ModelInvocation, priorPlan k12.RecognitionLayoutPlanV2, original json.RawMessage, priorAdjudication k12.ModelPhysicalInvocation) {
	t.Helper()
	runtime, err := store.LoadRecognitionLayoutPlanRuntimeV2(ctx, prior.AgentName, prior.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkModelInvocationFailed(ctx, prior.AgentName, prior.InvocationID, "local_finalization_failed"); err != nil {
		t.Fatal(err)
	}
	parent := unpreparedPhysicalInvocationParent(prior.JobID)
	parent.InvocationID = "recognition-adjudication-retry-parent"
	parent.Attempt = prior.Attempt + 1
	var targets []k12.RecognitionLayoutManifestTargetV2
	for i, target := range priorPlan.Targets {
		targets = append(targets, k12.RecognitionLayoutManifestTargetV2{ManifestRef: fmt.Sprintf("manifest_%04d", i+1), ManifestOrder: i + 1, DisplayLabel: target.DisplayLabel, SourceNumberPath: target.SourceNumberPath, SourceSectionPath: target.SourceSectionPath, SourceSectionLabel: target.SourceSectionLabel, Region: *target.OriginalRegion})
	}
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{PagePNG: recognitionLayoutRuntimeTestPagePNG(t), Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: "adjudication-retry-manifest", ResultDigest: priorPlan.ManifestResultDigest}, Targets: targets, RecognitionFormat: priorPlan.RecognitionFormat, EnableSourceAdjudication: true})
	if err != nil {
		t.Fatal(err)
	}
	header := runtime.Header
	header.PlanID, header.ParentInvocationID = "adjudication-retry-plan", parent.InvocationID
	headerDigest, err := k12.RecognitionLayoutPlanHeaderDigestV2(header)
	if err != nil {
		t.Fatal(err)
	}
	manifest := newPhysicalInvocation(parent, plan.ManifestInvocationID, k12.RecognitionPhysicalUnitWholePage)
	manifest.RecognitionPlanVersion, manifest.PlanDigest = k12.RecognitionPlanVersionV2, headerDigest
	parent, manifest, _, err = store.PrepareRecognizingInvocationWithInitialLayoutPlanV2(ctx, parent, manifest, header)
	if err != nil {
		t.Fatal(err)
	}
	if _, reused, err := store.ReuseSucceededRecognitionPhysicalInvocation(ctx, parent.AgentName, manifest.PhysicalInvocationID); err != nil || !reused {
		t.Fatalf("reuse retry manifest: %v %v", reused, err)
	}
	if err := store.AuthorizeRecognitionLayoutPlanV2(ctx, parent.AgentName, parent.InvocationID, k12.RecognitionLayoutManifestSuccessV2{InvocationID: manifest.PhysicalInvocationID, ResultDigest: priorPlan.ManifestResultDigest}, plan); err != nil {
		t.Fatal(err)
	}
	var primary k12.RecognitionLayoutPrimaryBatchSettlementResultV2
	var firstBatch k12.ModelPhysicalInvocation
	for i, batch := range plan.Batches {
		child := newPhysicalInvocation(parent, "retry-"+string(batch.Unit), batch.Unit)
		child.RecognitionPlanVersion, child.PlanDigest = k12.RecognitionPlanVersionV2, plan.AuthorizedPlanDigest
		child.CandidateExactSetDigest, _ = k12.RecognitionLayoutTargetExactSetDigestV2(batch.TargetIDs)
		if _, _, err := store.PrepareModelPhysicalInvocation(ctx, child); err != nil {
			t.Fatal(err)
		}
		child, reused, err := store.ReuseSucceededRecognitionPhysicalInvocation(ctx, parent.AgentName, child.PhysicalInvocationID)
		if err != nil || !reused {
			t.Fatalf("reuse batch: %v %v", reused, err)
		}
		if i == 0 {
			firstBatch = child
			primary, _, err = store.SettleRecognitionLayoutPrimaryBatchV2(ctx, parent.AgentName, parent.InvocationID, recognitionLayoutRepairablePrimarySettlement(plan, child))
			if err != nil {
				t.Fatal(err)
			}
		} else {
			settleRecognitionLayoutAllPrimaryValid(t, ctx, store, parent, plan, child)
		}
	}
	var firstRepair k12.ModelPhysicalInvocation
	for i, authorization := range primary.RepairAuthorizations {
		child := newPhysicalInvocation(parent, "retry-"+string(authorization.PhysicalUnit), authorization.PhysicalUnit)
		child.RecognitionPlanVersion, child.PlanDigest = k12.RecognitionPlanVersionV2, plan.AuthorizedPlanDigest
		child.CandidateExactSetDigest, _ = k12.RecognitionLayoutTargetExactSetDigestV2([]string{authorization.CandidateID})
		if _, _, err := store.PrepareModelPhysicalInvocation(ctx, child); err != nil {
			t.Fatal(err)
		}
		child, reused, err := store.ReuseSucceededRecognitionPhysicalInvocation(ctx, parent.AgentName, child.PhysicalInvocationID)
		if err != nil || !reused {
			t.Fatalf("reuse repair: %v %v", reused, err)
		}
		if i == 0 {
			firstRepair = child
		}
		if _, _, err := store.SettleRecognitionLayoutRepairV2(ctx, parent.AgentName, parent.InvocationID, k12.RecognitionLayoutRepairSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, AuthorizationID: authorization.AuthorizationID, AuthorizationDigest: authorization.AuthorizationDigest, CandidateID: authorization.CandidateID, SourcePhysicalInvocationID: child.PhysicalInvocationID, SourcePhysicalUnit: child.PhysicalUnit, SourcePhysicalResultDigest: child.ResultDigest, Classification: k12.RecognitionLayoutCandidateValidV2, ResultKind: k12.RecognitionLayoutCandidateQuestionV2, ResultJSON: original}); err != nil {
			t.Fatal(err)
		}
	}
	auth, _, err := store.AuthorizeRecognitionLayoutAdjudicationV2(ctx, parent.AgentName, parent.InvocationID, k12.RecognitionLayoutAdjudicationRequestV2{PlanDigest: plan.AuthorizedPlanDigest, CandidateID: primary.RepairAuthorizations[0].CandidateID, PrimaryPhysicalInvocationID: firstBatch.PhysicalInvocationID, PrimaryPhysicalResultDigest: firstBatch.ResultDigest, RepairPhysicalInvocationID: firstRepair.PhysicalInvocationID, RepairPhysicalResultDigest: firstRepair.ResultDigest, ConflictKind: "answer"})
	if err != nil {
		t.Fatal(err)
	}
	child := newPhysicalInvocation(parent, "retry-adjudication", auth.PhysicalUnit)
	child.RecognitionPlanVersion, child.PlanDigest = k12.RecognitionPlanVersionV2, plan.AuthorizedPlanDigest
	child.CandidateExactSetDigest, _ = k12.RecognitionLayoutTargetExactSetDigestV2([]string{auth.CandidateID})
	if _, _, err := store.PrepareModelPhysicalInvocation(ctx, child); err != nil {
		t.Fatal(err)
	}
	reusedChild, reused, err := store.ReuseSucceededRecognitionPhysicalInvocation(ctx, parent.AgentName, child.PhysicalInvocationID)
	if err != nil || !reused || reusedChild.ReusedFromPhysicalInvocationID != priorAdjudication.PhysicalInvocationID || reusedChild.ResultDigest != priorAdjudication.ResultDigest || reusedChild.ExternalRequestID != "" {
		t.Fatalf("adjudication success was not locally reused: %+v reused=%v err=%v", reusedChild, reused, err)
	}
	if _, _, err := store.SettleRecognitionLayoutAdjudicationV2(ctx, parent.AgentName, parent.InvocationID, k12.RecognitionLayoutAdjudicationSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, AuthorizationID: auth.AuthorizationID, AuthorizationDigest: auth.AuthorizationDigest, CandidateID: auth.CandidateID, SourcePhysicalInvocationID: reusedChild.PhysicalInvocationID, SourcePhysicalUnit: reusedChild.PhysicalUnit, SourcePhysicalResultDigest: reusedChild.ResultDigest, Adopted: true, MatchedQuestionPrior: "both", MatchedAnswerPrior: "primary", ResultKind: k12.RecognitionLayoutCandidateQuestionV2, ResultJSON: json.RawMessage(`{"student_answer":"6","text":"18/3="}`)}); err != nil {
		t.Fatal(err)
	}
	if final, _, err := store.FinalizeRecognitionLayoutPlanV2(ctx, parent.AgentName, parent.InvocationID); err != nil || final.PhysicalResultCount != 6 || final.CandidateResults[0].SourcePhysicalInvocationID != reusedChild.PhysicalInvocationID {
		t.Fatalf("reused adjudication finalization: %+v %v", final, err)
	}
	if _, claimed, _ := store.ClaimModelPhysicalInvocationSent(ctx, parent.AgentName, child.PhysicalInvocationID); claimed {
		t.Fatal("reused adjudication was sent")
	}
	if replay, reused, err := store.ReuseSucceededRecognitionPhysicalInvocation(ctx, parent.AgentName, child.PhysicalInvocationID); err != nil || !reused || !reflect.DeepEqual(replay, reusedChild) {
		t.Fatalf("reuse replay changed: %+v %v", replay, err)
	}
}
