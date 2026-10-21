package k12storage_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestPrintedQuestionReceiptPreservesAnswerAndColdReplay(t *testing.T) {
	for _, scenario := range []string{"verified", "incomplete source", "changed words", "source risk"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFinalAnswerStorageFixtureV1(t, k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV2, false, false, true)
			raw := strings.ReplaceAll(finalAnswerStorageRawV1(t, f.question), "数学", "语文")
			if scenario == "source risk" {
				raw = strings.Replace(raw, `"ocr_signals":["中间步骤笔画不明确。"]`, `"ocr_signals":["source_gap_instruction","印刷题干右侧被裁切"]`, 1)
			}
			proof, err := k12.RecognitionPrintedQuestionEvidenceFromResponseV1(raw, f.plan, f.plan.Targets[0], []string{f.question, f.question})
			if err != nil || proof == nil {
				t.Fatalf("printed proof missing: %v", err)
			}
			if scenario == "incomplete source" {
				raw = strings.Replace(raw, `"question_complete_and_legible":true`, `"question_complete_and_legible":false`, 1)
			}
			if scenario == "changed words" {
				proof.RawTranscription += "额外字词"
			}
			if _, err := f.store.MarkModelPhysicalInvocationSucceededWithContent(t.Context(), f.parent.AgentName, f.child.PhysicalInvocationID, raw, "printed-source"); err != nil {
				t.Fatal(err)
			}
			in := f.settlementInput(t, raw, nil)
			in.PrintedQuestionEvidence = proof
			_, created, err := f.store.SettleRecognitionLayoutAdjudicationV2(t.Context(), f.parent.AgentName, f.parent.InvocationID, in)
			if scenario != "verified" && scenario != "source risk" {
				if err == nil || created {
					t.Fatal("unproved printed text settled")
				}
				return
			}
			if err != nil || !created {
				t.Fatalf("printed settlement: %v", err)
			}
			final, _, err := f.store.FinalizeRecognitionLayoutPlanV2(t.Context(), f.parent.AgentName, f.parent.InvocationID)
			if err != nil {
				t.Fatal(err)
			}
			candidate := final.CandidateResults[0]
			if scenario == "source risk" && len(candidate.PrintedQuestionReceipt.Evidence.OCRSignals) != 2 {
				t.Fatal("source risks lost in durable receipt")
			}
			if candidate.PrintedQuestionReceipt == nil || !reflect.DeepEqual(candidate.PrintedQuestionReceipt.Evidence, *proof) || string(candidate.ResultJSON) != string(f.original) || candidate.Adjudication != nil || candidate.FinalAnswerReceipt != nil {
				t.Fatal("printed proof replaced whole-answer facts")
			}
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			store, db := openPhysicalLedgerFileStore(t, f.path)
			defer db.Close()
			again, fresh, err := store.FinalizeRecognitionLayoutPlanV2(t.Context(), f.parent.AgentName, f.parent.InvocationID)
			if err != nil || fresh || !reflect.DeepEqual(again, final) {
				t.Fatalf("cold printed proof changed: %v", err)
			}
			var original string
			if err := db.QueryRowContext(t.Context(), `SELECT result_content FROM k12_model_physical_invocations WHERE physical_invocation_id=?`, f.child.PhysicalInvocationID).Scan(&original); err != nil || original != raw {
				t.Fatal("provider raw response changed")
			}
			if _, claimed, err := store.ClaimModelPhysicalInvocationSent(t.Context(), f.parent.AgentName, f.child.PhysicalInvocationID); claimed {
				t.Fatalf("successful source resent: %v", err)
			}
		})
	}
}
