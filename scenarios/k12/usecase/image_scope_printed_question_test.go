package usecase

import (
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestImageScopePrintedProofIgnoresUnrelatedHandwritingOnlyForSolve(t *testing.T) {
	confidence := .72
	q := NormalizeRecognizedQuestionForInitialReadMode(RecognizedQuestion{Question: "内金水河像玉带。", RawTranscription: "内金水河像玉带。", CanonicalMarkdown: "内金水河像玉带。", Subject: "语文", AnswerState: AnswerStateUnclear, StudentAnswer: "原有手写", AnswerRawTranscription: "原有手写", RecognitionConfidence: &confidence, EvidenceTranscriptions: []string{"内金水河像玉带。", "金水河像玉带。"}, AnswerEvidenceTranscriptions: []string{"甲", "乙"}, OCRSignals: []string{"evidence_conflict", "unclear_handwriting"}, ImageSourceContext: &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1, Action: "solve", ScopeDigest: "plan", TargetIDs: []string{"target"}, PrintedQuestionReceipt: &k12.RecognitionPrintedQuestionReceiptV1{Evidence: k12.RecognitionPrintedQuestionEvidenceV1{RawTranscription: "内金水河像玉带。", Confidence: .99, MatchedQuestionPrior: "primary"}, PlanDigest: "plan", CandidateID: "target", AuthorizationID: "authorization", SettlementDigest: "settlement"}}}, k12.RecognitionLayoutManifestWithContentV1)
	before := cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
	if recognizedQuestionRequiresGuardianConfirmationForInitialReadMode(q, PhotoTaskBlankWorksheet, k12.RecognitionLayoutManifestWithContentV1) {
		t.Fatal("verified printed question blocked by handwriting")
	}
	if !recognizedQuestionRequiresGuardianConfirmationForInitialReadMode(q, PhotoTaskCompletedHomework, k12.RecognitionLayoutManifestWithContentV1) {
		t.Fatal("printed proof authorized grading unclear writing")
	}
	if !reflect.DeepEqual(q, before) {
		t.Fatal("source observations changed")
	}
	for _, scenario := range []string{"missing proof", "wrong target", "wrong plan", "unresolved instruction"} {
		copy := cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
		switch scenario {
		case "missing proof":
			copy.ImageSourceContext.PrintedQuestionReceipt = nil
		case "wrong target":
			copy.ImageSourceContext.PrintedQuestionReceipt.CandidateID = "other"
		case "wrong plan":
			copy.ImageSourceContext.PrintedQuestionReceipt.PlanDigest = "other"
		case "unresolved instruction":
			copy.ImageSourceContext.Recoveries = []k12.RecognitionImageSourceRecoveryV1{{Gap: k12.RecognitionImageSourceGapV1{Kind: "instruction"}, State: "unresolved"}}
		}
		if !recognizedQuestionRequiresGuardianConfirmationForInitialReadMode(copy, PhotoTaskBlankWorksheet, k12.RecognitionLayoutManifestWithContentV1) {
			t.Fatalf("%s passed", scenario)
		}
	}
	for _, signal := range []string{"source_gap_instruction", "印刷题干右侧被裁切，缺少条件", "evidence_conflict"} {
		copy := cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
		copy.ImageSourceContext.PrintedQuestionReceipt.Evidence.OCRSignals = []string{signal}
		if !recognizedQuestionRequiresGuardianConfirmationForInitialReadMode(copy, PhotoTaskBlankWorksheet, k12.RecognitionLayoutManifestWithContentV1) {
			t.Fatalf("third reading source risk cleared: %s", signal)
		}
	}
}
