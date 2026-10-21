package k12storage_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type finalAnswerStorageFixtureV1 struct {
	initialReadFixtureV1
	authorization k12.RecognitionLayoutAdjudicationAuthorizationV2
	child         k12.ModelPhysicalInvocation
	original      json.RawMessage
	question      string
}

func newFinalAnswerStorageFixtureV1(t *testing.T, policy string, conflictingQuestion bool, sameReadings ...bool) finalAnswerStorageFixtureV1 {
	t.Helper()
	raw, err := os.ReadFile("../testdata/initial_read_compact_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if policy == k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV2 {
		var fixture map[string]json.RawMessage
		var targets []map[string]json.RawMessage
		if json.Unmarshal(raw, &fixture) != nil || json.Unmarshal(fixture["targets"], &targets) != nil {
			t.Fatal("invalid initial fixture")
		}
		targets[0]["confidence"] = json.RawMessage(`0.76`)
		fixture["targets"], err = json.Marshal(targets)
		if err != nil {
			t.Fatal(err)
		}
		raw, err = json.Marshal(fixture)
		if err != nil {
			t.Fatal(err)
		}
	}
	opts := []string{policy}
	if len(sameReadings) > 1 && sameReadings[1] {
		raw = []byte(strings.ReplaceAll(string(raw), "数学", "语文"))
		opts = append(opts, "upright_printed")
	}
	f := compactInitialReadStorageFixtureV1(t, string(raw), opts...)
	if policy == k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV2 {
		entries, err := k12.CanonicalRecognitionLayoutInitialReadEntriesV1(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		for i := range f.input.Candidates {
			f.input.Candidates[i].FirstReadJSON = entries[i]
		}
	}
	for i := range f.input.Candidates {
		var entry struct {
			InitialRead struct {
				Recognition map[string]json.RawMessage `json:"recognition"`
			} `json:"initial_read"`
		}
		if err := json.Unmarshal(f.input.Candidates[i].FirstReadJSON, &entry); err != nil {
			t.Fatal(err)
		}
		f.input.Candidates[i].ResultJSON, err = json.Marshal(entry.InitialRead.Recognition)
		if err != nil {
			t.Fatal(err)
		}
	}
	initial := f.settle(t)
	auth := f.review(t, initial)
	review := f.prepareReview(t, auth)
	var first map[string]json.RawMessage
	if err := json.Unmarshal(f.input.Candidates[0].ResultJSON, &first); err != nil {
		t.Fatal(err)
	}
	var question, priorAnswer string
	if json.Unmarshal(first["question"], &question) != nil || json.Unmarshal(first["student_answer"], &priorAnswer) != nil {
		t.Fatal("initial source text is missing")
	}
	if policy == k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV2 {
		first["recognition_confidence"] = json.RawMessage(`0.99`)
	}
	secondQuestion := question
	sameObservation := len(sameReadings) != 0 && sameReadings[0]
	if sameObservation {
		secondQuestion = strings.Replace(question, `\(\frac{3}{8}\)`, "3/8", 1)
	}
	if conflictingQuestion {
		secondQuestion = strings.Replace(question, "2朵花", "9朵花", 1)
	}
	first["question"], _ = json.Marshal(secondQuestion)
	first["student_answer"], _ = json.Marshal("3/8×2=5/4\n答：3/4张纸。")
	first["evidence_transcriptions"], _ = json.Marshal([]string{question, secondQuestion})
	first["answer_evidence_transcriptions"], _ = json.Marshal([]string{priorAnswer, "3/8×2=5/4\n答：3/4张纸。"})
	first["ocr_signals"], _ = json.Marshal([]string{"中间手写分数及取消性质不明确。", "evidence_conflict"})
	if sameObservation {
		first["student_answer"], _ = json.Marshal(priorAnswer)
		first["answer_evidence_transcriptions"], _ = json.Marshal([]string{priorAnswer, priorAnswer})
		first["ocr_signals"], _ = json.Marshal([]string{"手写的第一个分数上有斜划线，是否将该分数整体划去不明确；暂保留其可读内容。"})
	}
	original, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	reviewPayload, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{"target_id": "t1", "kind": "question", "recognition": first},
		map[string]any{"target_id": "t2", "kind": "question", "recognition": f.input.Candidates[1].ResultJSON},
	}})
	review, err = f.store.MarkModelPhysicalInvocationSucceededWithContent(t.Context(), f.parent.AgentName, review.PhysicalInvocationID, string(reviewPayload), "provider-review")
	if err != nil {
		t.Fatal(err)
	}
	settlement := k12.RecognitionLayoutReviewBatchSettlementV1{PlanDigest: f.plan.AuthorizedPlanDigest, AuthorizationID: auth.AuthorizationID, AuthorizationDigest: auth.AuthorizationDigest, SourcePhysicalInvocationID: review.PhysicalInvocationID, SourcePhysicalUnit: review.PhysicalUnit, SourcePhysicalResultDigest: review.ResultDigest, Classification: k12.RecognitionLayoutBatchClassifiedV2}
	for i, candidate := range f.input.Candidates {
		result := candidate.ResultJSON
		if i == 0 {
			result = original
		}
		settlement.Candidates = append(settlement.Candidates, k12.RecognitionLayoutCandidateSettlementV2{CandidateID: candidate.CandidateID, Classification: k12.RecognitionLayoutCandidateValidV2, ResultKind: candidate.ResultKind, ResultJSON: result})
	}
	if _, _, err := f.store.SettleRecognitionLayoutReviewBatchV1(t.Context(), f.parent.AgentName, f.parent.InvocationID, settlement); err != nil {
		t.Fatal(err)
	}
	request := k12.RecognitionLayoutAdjudicationRequestV2{PlanDigest: f.plan.AuthorizedPlanDigest, CandidateID: f.plan.Targets[0].TargetID, PrimaryPhysicalInvocationID: f.input.SourcePhysicalInvocationID, PrimaryPhysicalResultDigest: f.input.SourcePhysicalResultDigest, RepairPhysicalInvocationID: review.PhysicalInvocationID, RepairPhysicalResultDigest: review.ResultDigest, ConflictKind: "answer"}
	if sameObservation {
		request.ConflictKind = "observation_uncertainty"
	}
	adjudication, _, err := f.store.AuthorizeRecognitionLayoutAdjudicationV2(t.Context(), f.parent.AgentName, f.parent.InvocationID, request)
	if err != nil {
		t.Fatal(err)
	}
	child := newPhysicalInvocation(f.parent, "physical-final-answer", adjudication.PhysicalUnit)
	child.RecognitionPlanVersion, child.PlanDigest = k12.RecognitionPlanVersionV2, f.plan.AuthorizedPlanDigest
	child.CandidateExactSetDigest, _ = k12.RecognitionLayoutTargetExactSetDigestV2([]string{adjudication.CandidateID})
	child, _, err = f.store.PrepareModelPhysicalInvocation(t.Context(), child)
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := f.store.ClaimModelPhysicalInvocationSent(t.Context(), f.parent.AgentName, child.PhysicalInvocationID); err != nil || !claimed {
		t.Fatalf("adjudication claim: %v %v", claimed, err)
	}
	return finalAnswerStorageFixtureV1{f, adjudication, child, original, question}
}

func TestRecognitionFinalAnswerObservationUncertaintyAuthorizesAndReplays(t *testing.T) {
	f := newFinalAnswerStorageFixtureV1(t, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1, false, true)
	raw := finalAnswerStorageRawV1(t, f.question)
	parsed, err := k12.ParseRecognitionFinalAnswerResponseV1(raw, *f.plan.Targets[0].AdjudicationRegion)
	if err != nil || parsed.Evidence == nil {
		t.Fatal(err)
	}
	if _, err := f.store.MarkModelPhysicalInvocationSucceededWithContent(t.Context(), f.parent.AgentName, f.child.PhysicalInvocationID, raw, "provider-final-answer"); err != nil {
		t.Fatal(err)
	}
	in := f.settlementInput(t, raw, parsed.Evidence)
	if _, created, err := f.store.SettleRecognitionLayoutAdjudicationV2(t.Context(), f.parent.AgentName, f.parent.InvocationID, in); err != nil || !created {
		t.Fatalf("same-read evidence settle: %v", err)
	}
	final, created, err := f.store.FinalizeRecognitionLayoutPlanV2(t.Context(), f.parent.AgentName, f.parent.InvocationID)
	if err != nil || !created || final.CandidateResults[0].FinalAnswerReceipt == nil || string(final.CandidateResults[0].ResultJSON) != string(f.original) {
		t.Fatalf("same-read finalization changed source: %v", err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	store, db := openPhysicalLedgerFileStore(t, f.path)
	defer db.Close()
	if again, created, err := store.FinalizeRecognitionLayoutPlanV2(t.Context(), f.parent.AgentName, f.parent.InvocationID); err != nil || created || !reflect.DeepEqual(final, again) {
		t.Fatalf("same-read evidence replay drift: %v", err)
	}
}

func TestRecognitionFinalAnswerV2LowInitialConfidenceSQLiteProof(t *testing.T) {
	for _, scenario := range []string{"valid independent proof", "third confidence low", "third question incomplete", "third question changed", "missing final evidence"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFinalAnswerStorageFixtureV1(t, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV2, false, true)
			t.Cleanup(func() { f.db.Close() })
			var first struct {
				Confidence float64 `json:"recognition_confidence"`
			}
			if json.Unmarshal(f.input.Candidates[0].ResultJSON, &first) != nil || first.Confidence != 0.76 {
				t.Fatal("first source confidence was rewritten")
			}
			raw := finalAnswerStorageRawV1(t, f.question)
			proof, err := k12.ParseRecognitionFinalAnswerResponseV1(raw, *f.plan.Targets[0].AdjudicationRegion)
			if err != nil || proof.Evidence == nil {
				t.Fatal("independent proof fixture is unavailable")
			}
			switch scenario {
			case "third confidence low":
				raw = strings.Replace(raw, `"recognition_confidence":0.99`, `"recognition_confidence":0.89`, 1)
			case "third question incomplete":
				raw = strings.Replace(raw, `"question_complete_and_legible":true`, `"question_complete_and_legible":false`, 1)
			case "third question changed":
				raw = strings.Replace(raw, "做2朵花", "做9朵花", 1)
			case "missing final evidence":
				var envelope map[string]json.RawMessage
				if json.Unmarshal([]byte(raw), &envelope) != nil {
					t.Fatal("invalid response")
				}
				envelope["final_answer"] = json.RawMessage(`null`)
				body, e := json.Marshal(envelope)
				if e != nil {
					t.Fatal(e)
				}
				raw = string(body)
			}
			if _, err := f.store.MarkModelPhysicalInvocationSucceededWithContent(t.Context(), f.parent.AgentName, f.child.PhysicalInvocationID, raw, "provider-independent"); err != nil {
				t.Fatal(err)
			}
			in := f.settlementInput(t, raw, proof.Evidence)
			_, created, err := f.store.SettleRecognitionLayoutAdjudicationV2(t.Context(), f.parent.AgentName, f.parent.InvocationID, in)
			if scenario != "valid independent proof" {
				if err == nil || created {
					t.Fatal("unproven third observation was accepted")
				}
				return
			}
			if err != nil || !created {
				t.Fatalf("settlement: %v", err)
			}
			final, created, err := f.store.FinalizeRecognitionLayoutPlanV2(t.Context(), f.parent.AgentName, f.parent.InvocationID)
			if err != nil || !created || final.CandidateResults[0].FinalAnswerReceipt == nil || string(final.CandidateResults[0].ResultJSON) != string(f.original) {
				t.Fatalf("proof lost original working: %v", err)
			}
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			store, db := openPhysicalLedgerFileStore(t, f.path)
			defer db.Close()
			replay, fresh, err := store.FinalizeRecognitionLayoutPlanV2(t.Context(), f.parent.AgentName, f.parent.InvocationID)
			if err != nil || fresh || !reflect.DeepEqual(final, replay) {
				t.Fatalf("persisted evidence replay changed: %v", err)
			}
			if _, claimed, err := store.ClaimModelPhysicalInvocationSent(t.Context(), f.parent.AgentName, f.child.PhysicalInvocationID); claimed {
				t.Fatalf("successful third reading was resent: %v", err)
			}
		})
	}
}

func finalAnswerStorageRawV1(t *testing.T, question string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"verification": map[string]bool{"target_ownership_confirmed": true, "active_answer_complete": false, "question_complete_and_legible": true},
		"items": []any{map[string]any{"target_id": "t1", "kind": "question", "recognition": map[string]any{
			"question": question, "subject": "数学", "answer_state": "present", "student_answer": "3/8×2=2/4\n答：3/4张纸。",
			"recognition_confidence": 0.99, "ocr_signals": []string{"中间步骤笔画不明确。"}, "answer_bbox": map[string]int{"x": 10, "y": 2, "width": 8, "height": 5},
		}}},
		"final_answer": map[string]any{"raw_transcription": "3/4张纸", "bbox": map[string]int{"x": 10, "y": 5, "width": 7, "height": 2}, "is_final_answer": true, "target_ownership_confirmed": true, "not_cancelled": true, "complete_and_legible": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (f finalAnswerStorageFixtureV1) settlementInput(t *testing.T, raw string, evidence *k12.RecognitionFinalAnswerEvidenceV1) k12.RecognitionLayoutAdjudicationSettlementV2 {
	t.Helper()
	return k12.RecognitionLayoutAdjudicationSettlementV2{PlanDigest: f.plan.AuthorizedPlanDigest, AuthorizationID: f.authorization.AuthorizationID, AuthorizationDigest: f.authorization.AuthorizationDigest,
		CandidateID: f.authorization.CandidateID, SourcePhysicalInvocationID: f.child.PhysicalInvocationID, SourcePhysicalUnit: f.child.PhysicalUnit,
		SourcePhysicalResultDigest: recognitionLayoutRuntimeTestDigest(raw), FinalAnswerEvidence: evidence}
}

func TestRecognitionFinalAnswerEvidenceV1PersistsWithoutAdoptingWholeAnswer(t *testing.T) {
	ctx := t.Context()
	f := newFinalAnswerStorageFixtureV1(t, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1, false)
	raw := finalAnswerStorageRawV1(t, f.question)
	parsed, err := k12.ParseRecognitionFinalAnswerResponseV1(raw, *f.plan.Targets[0].AdjudicationRegion)
	if err != nil || parsed.Evidence == nil {
		t.Fatalf("independent final answer absent: %v", err)
	}
	if _, err := f.store.MarkModelPhysicalInvocationSucceededWithContent(ctx, f.parent.AgentName, f.child.PhysicalInvocationID, raw, "provider-final-answer"); err != nil {
		t.Fatal(err)
	}
	in := f.settlementInput(t, raw, parsed.Evidence)
	settled, created, err := f.store.SettleRecognitionLayoutAdjudicationV2(ctx, f.parent.AgentName, f.parent.InvocationID, in)
	if err != nil || !created || settled.Adopted || settled.FrozenResult != nil {
		t.Fatalf("independent evidence changed whole-answer adoption: created=%v err=%v", created, err)
	}
	final, created, err := f.store.FinalizeRecognitionLayoutPlanV2(ctx, f.parent.AgentName, f.parent.InvocationID)
	if err != nil || !created {
		t.Fatalf("finalize independent evidence: %v", err)
	}
	candidate := final.CandidateResults[0]
	if candidate.FinalAnswerReceipt == nil || !reflect.DeepEqual(candidate.FinalAnswerReceipt.Evidence, *parsed.Evidence) || candidate.FinalAnswerReceipt.SourcePhysicalInvocationID != f.child.PhysicalInvocationID ||
		candidate.Adjudication != nil || string(candidate.ResultJSON) != string(f.original) || candidate.SourcePhysicalInvocationID == f.child.PhysicalInvocationID {
		t.Fatal("independent evidence overwrote source facts, OCR risk or adoption")
	}
	stored, err := f.store.ValidateRecognitionFinalAnswerReceiptV1(ctx, f.parent.AgentName, f.parent.JobID, *candidate.FinalAnswerReceipt)
	if err != nil || string(stored) != string(f.original) {
		t.Fatalf("trusted finalized receipt could not be read: %v", err)
	}
	for _, scenario := range []string{"owner", "job", "source", "answer"} {
		t.Run(scenario, func(t *testing.T) {
			owner, job, receipt := f.parent.AgentName, f.parent.JobID, *candidate.FinalAnswerReceipt
			switch scenario {
			case "owner":
				owner = "other-owner"
			case "job":
				job = "other-job"
			case "source":
				receipt.SourcePhysicalInvocationID = f.input.SourcePhysicalInvocationID
			case "answer":
				receipt.Evidence.RawTranscription = "9张纸"
			}
			if _, err := f.store.ValidateRecognitionFinalAnswerReceiptV1(ctx, owner, job, receipt); err == nil {
				t.Fatal("unrelated or changed receipt was accepted")
			}
		})
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, db := openPhysicalLedgerFileStore(t, f.path)
	defer db.Close()
	if again, fresh, err := reopened.FinalizeRecognitionLayoutPlanV2(ctx, f.parent.AgentName, f.parent.InvocationID); err != nil || fresh || !reflect.DeepEqual(final, again) {
		t.Fatalf("cold finalization replay changed evidence: %v", err)
	}
	if _, fresh, err := reopened.SettleRecognitionLayoutAdjudicationV2(ctx, f.parent.AgentName, f.parent.InvocationID, in); err != nil || fresh {
		t.Fatalf("settlement replay drifted: %v", err)
	}
	var content, candidateJSON string
	if err := db.QueryRowContext(ctx, `SELECT result_content FROM k12_model_physical_invocations WHERE physical_invocation_id=?`, f.child.PhysicalInvocationID).Scan(&content); err != nil || content != raw {
		t.Fatal("original provider response changed")
	}
	if err := db.QueryRowContext(ctx, `SELECT result_json FROM k12_recognition_layout_candidate_results WHERE candidate_id=? AND plan_id=?`, candidate.CandidateID, final.PlanID).Scan(&candidateJSON); err != nil || candidateJSON != string(f.original) {
		t.Fatal("original frozen candidate changed")
	}
	// 已终稿回执归档不要求长期保留 Provider 私有正文。
	if _, err := db.ExecContext(ctx, `UPDATE k12_model_physical_invocations SET result_content=NULL WHERE parent_invocation_id=?`, f.parent.InvocationID); err != nil {
		t.Fatal(err)
	}
	if again, fresh, err := reopened.FinalizeRecognitionLayoutPlanV2(ctx, f.parent.AgentName, f.parent.InvocationID); err != nil || fresh || !reflect.DeepEqual(final, again) {
		t.Fatalf("archived physical payload invalidated the frozen finalization: %v", err)
	}
	if source, err := reopened.ValidateRecognitionFinalAnswerReceiptV1(ctx, f.parent.AgentName, f.parent.JobID, *candidate.FinalAnswerReceipt); err != nil || string(source) != string(f.original) {
		t.Fatalf("archived physical payload invalidated the trusted receipt: %v", err)
	}
}

func TestRecognitionFinalAnswerEvidenceV1RejectsUnprovenSource(t *testing.T) {
	for _, scenario := range []string{"old policy", "unknown", "other target", "changed third question", "conflicting originals", "missing question confirmation", "low confidence", "altered final answer"} {
		t.Run(scenario, func(t *testing.T) {
			policy := k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1
			if scenario == "old policy" {
				policy = k12.RecognitionExecutionPolicyCompactDAGV1
			}
			f := newFinalAnswerStorageFixtureV1(t, policy, scenario == "conflicting originals")
			raw := finalAnswerStorageRawV1(t, f.question)
			parsed, err := k12.ParseRecognitionFinalAnswerResponseV1(raw, *f.plan.Targets[0].AdjudicationRegion)
			if err != nil || parsed.Evidence == nil {
				t.Fatal(err)
			}
			switch scenario {
			case "other target":
				raw = strings.Replace(raw, `"target_id":"t1"`, `"target_id":"t2"`, 1)
			case "changed third question":
				raw = strings.Replace(raw, "做2朵花", "做9朵花", 1)
			case "missing question confirmation":
				raw = strings.Replace(raw, `"question_complete_and_legible":true`, `"question_complete_and_legible":false`, 1)
			case "low confidence":
				raw = strings.Replace(raw, `"recognition_confidence":0.99`, `"recognition_confidence":0.5`, 1)
			case "altered final answer":
				parsed.Evidence.RawTranscription = "9张纸"
			}
			if scenario == "unknown" {
				if _, err := f.store.MarkModelPhysicalInvocationOutcomeUnknown(t.Context(), f.parent.AgentName, f.child.PhysicalInvocationID, "transport_unknown"); err != nil {
					t.Fatal(err)
				}
			} else if _, err := f.store.MarkModelPhysicalInvocationSucceededWithContent(t.Context(), f.parent.AgentName, f.child.PhysicalInvocationID, raw, "provider-final-answer"); err != nil {
				t.Fatal(err)
			}
			if _, created, err := f.store.SettleRecognitionLayoutAdjudicationV2(t.Context(), f.parent.AgentName, f.parent.InvocationID, f.settlementInput(t, raw, parsed.Evidence)); err == nil || created {
				t.Fatal("unproven final-answer evidence committed")
			}
			var count int
			if err := f.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM k12_recognition_layout_adjudication_settlements`).Scan(&count); err != nil || count != 0 {
				t.Fatal("rejected evidence left a partial settlement")
			}
		})
	}
}
