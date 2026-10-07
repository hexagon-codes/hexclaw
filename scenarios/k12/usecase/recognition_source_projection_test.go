package usecase

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRecognitionSourceProjectionSurvivesColdReceiptWithoutChangingCanonicalDigest(t *testing.T) {
	question := NormalizeRecognizedQuestion(RecognizedQuestion{Question: "同一道完整应用题？", AnswerState: AnswerStatePresent, StudentAnswer: "第一行演算\n第二行答案", Subject: "数学"})
	canonical := CanonicalRecognizedQuestionsDigest([]RecognizedQuestion{question})
	question.LayoutSourceProjection = RecognitionLayoutSourceProjection{PageDigest: "simulated-page-digest", PlanID: "simulated-plan", PlanDigest: "simulated-plan-digest", FinalizationDigest: "simulated-finalization-digest", TargetIDs: [2]string{"simulated-target-a", "simulated-target-b"}}
	if CanonicalRecognizedQuestionsDigest([]RecognizedQuestion{question}) != canonical {
		t.Fatal("lineage changed canonical content identity")
	}
	orchestrator := &GradingOrchestrator{runDir: t.TempDir()}
	if err := orchestrator.persistRecognitionReceipt("simulated-job", "simulated-invocation", &gradingRun{agentName: "simulated-owner", questions: []RecognizedQuestion{question}}); err != nil {
		t.Fatal(err)
	}
	cold := &GradingOrchestrator{runDir: orchestrator.runDir}
	receipt, found := cold.readRecognitionReceipt("simulated-job")
	if !found || len(receipt.Questions) != 1 || receipt.Questions[0].LayoutSourceProjection != question.LayoutSourceProjection || receipt.Questions[0].AnswerRawTranscription != question.AnswerRawTranscription {
		t.Fatal("cold recognition receipt lost derived source or complete answer")
	}
	cloned := cloneRecognizedQuestions(receipt.Questions)
	cloned[0].LayoutSourceProjection.TargetIDs[0] = "other-target"
	if receipt.Questions[0].LayoutSourceProjection.TargetIDs[0] != "simulated-target-a" {
		t.Fatal("source projection clone aliases original")
	}
	question.LayoutSourceProjection = RecognitionLayoutSourceProjection{}
	raw, _ := json.Marshal(question)
	if strings.Contains(string(raw), "layout_source_projection") {
		t.Fatal("legacy results gained empty source metadata")
	}
}
