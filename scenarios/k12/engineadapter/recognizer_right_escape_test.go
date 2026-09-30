package engineadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestSanitizeModelJSONPreservesRightDelimiterAndControlEscapes(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"raw latex", `{"value":"\left(1+2\right)"}`, `\left(1+2\right)`},
		{"escaped latex", `{"value":"\\left(1+2\\right)"}`, `\left(1+2\right)`},
		{"json control escapes", `{"value":"first\r\nsecond\tthird"}`, "first\r\nsecond\tthird"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got struct {
				Value string `json:"value"`
			}
			if err := json.Unmarshal([]byte(sanitizeModelJSON(tc.raw)), &got); err != nil {
				t.Fatal(err)
			}
			if got.Value != tc.want {
				t.Fatalf("decoded value = %q, want %q", got.Value, tc.want)
			}
		})
	}
}

// 两份成功物理响应原样作为输入，检查格式差异不触发额外来源核验。
func TestRecognitionLayoutSourceComparisonFromSuccessfulReceipts(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	primaryRaw := read("recognition-right-primary.txt")
	reviewRaw := read("recognition-right-review.txt")
	targets := make([]k12.RecognitionLayoutTargetV2, 3)
	for i := range targets {
		targets[i] = k12.RecognitionLayoutTargetV2{
			TargetID: fmt.Sprintf("target-%d", i+1),
			Region:   k12.SourcePixelRegion{Width: 500, Height: 400},
		}
	}
	first := classifyRecognitionLayoutBatchV2(primaryRaw, targets, k12.RecognitionLayoutCompactV4)
	if len(first.outcomes) != 3 || first.outcomes[0].question == nil {
		t.Fatalf("primary receipt did not parse: %+v", first)
	}
	primary := first.outcomes[0]
	_, reviewed := classifyRecognitionLayoutRepairV2(reviewRaw, targets[0], k12.RecognitionLayoutCompactV4)
	if reviewed == nil || reviewed.question == nil {
		t.Fatal("review receipt did not parse")
	}
	if strings.ContainsRune(primary.question.AnswerRawTranscription, '\r') {
		t.Fatal("LaTeX right delimiter was decoded as a carriage return")
	}
	questionMatch, answerMatch := usecase.RecognitionSourceReadingsMatch(*primary.question, *reviewed.question)
	if !questionMatch || !answerMatch {
		t.Fatalf("equivalent readings diverged: question=%t answer=%t", questionMatch, answerMatch)
	}
	reviewed.question.EvidenceTranscriptions = []string{primary.question.RawTranscription, reviewed.question.RawTranscription}
	reviewed.question.AnswerEvidenceTranscriptions = []string{primary.question.AnswerRawTranscription, reviewed.question.AnswerRawTranscription}
	for _, reason := range usecase.EvaluateOCRConfirmationRisk(*reviewed.question).ConfirmationReasons {
		if reason == usecase.OCRRiskEvidenceConflict {
			t.Fatal("equivalent successful readings still request source adjudication")
		}
	}
	adapter := NewRecognizerAdapter(func(context.Context, []byte, string) (string, error) {
		t.Fatal("equivalent receipts must not invoke a model")
		return "", nil
	})
	if err := adapter.recognizeLayoutAdjudicationsV2(t.Context(), nil,
		k12.RecognitionLayoutPlanV2{Targets: targets[:1]}, k12.RecognitionLayoutPlanRuntimeV2{},
		map[string]recognitionLayoutBatchOutcomeV2{targets[0].TargetID: primary},
		map[string]recognitionLayoutBatchOutcomeV2{targets[0].TargetID: *reviewed}); err != nil {
		t.Fatalf("equivalent receipts entered adjudication: %v", err)
	}
	_, changed := classifyRecognitionLayoutRepairV2(strings.ReplaceAll(reviewRaw, "=2", "=3"), targets[0], k12.RecognitionLayoutCompactV4)
	if changed == nil || changed.question == nil {
		t.Fatal("changed answer did not parse")
	}
	_, answerMatch = usecase.RecognitionSourceReadingsMatch(*primary.question, *changed.question)
	if answerMatch {
		t.Fatal("a changed numeric answer was accepted as equivalent")
	}
	changed.question.AnswerEvidenceTranscriptions = []string{primary.question.AnswerRawTranscription, changed.question.AnswerRawTranscription}
	for _, reason := range usecase.EvaluateOCRConfirmationRisk(*changed.question).ConfirmationReasons {
		if reason == usecase.OCRRiskEvidenceConflict {
			return
		}
	}
	t.Fatal("a changed numeric answer no longer requests source adjudication")
}
