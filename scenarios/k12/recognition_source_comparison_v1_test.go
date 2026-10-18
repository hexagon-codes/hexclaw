package k12

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"
)

func TestRecognitionFinalAnswerV2SeparatesInitialConfidenceFromIndependentProof(t *testing.T) {
	value := func(v float64) *float64 { return &v }
	base := RecognitionFinalAnswerSourceV1{Question: "7/9×3/14=", Subject: "数学", AnswerState: "present", Confidence: value(0.99)}
	for _, tc := range []struct {
		name                   string
		initial, review, third *float64
		changedQuestion        bool
		want                   bool
	}{
		{"low initial with trusted independent sources", value(0.76), value(0.99), value(0.99), false, true},
		{"missing initial metadata", nil, value(0.99), value(0.99), false, false},
		{"negative initial metadata", value(-0.01), value(0.99), value(0.99), false, false},
		{"initial above valid range", value(1.01), value(0.99), value(0.99), false, false},
		{"non finite initial", value(math.NaN()), value(0.99), value(0.99), false, false},
		{"infinite initial", value(math.Inf(1)), value(0.99), value(0.99), false, false},
		{"review not trusted", value(0.76), value(0.89), value(0.99), false, false},
		{"third read not trusted", value(0.76), value(0.99), value(0.89), false, false},
		{"first question differs", value(0.76), value(0.99), value(0.99), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, review, third := base, base, base
			first.Confidence, review.Confidence, third.Confidence = tc.initial, tc.review, tc.third
			if tc.changedQuestion {
				first.Question = "7/9×3/11="
			}
			if got := RecognitionFinalAnswerSourcesTrustedForPolicyV1(RecognitionExecutionPolicyCompactDAGFinalAnswerV2, first, review, third); got != tc.want {
				t.Fatalf("independent proof eligibility=%v want=%v", got, tc.want)
			}
			if RecognitionFinalAnswerSourcesTrustedForPolicyV1(RecognitionExecutionPolicyCompactDAGFinalAnswerV1, first, review, third) {
				t.Fatal("old policy acquired low-confidence source permission")
			}
		})
	}
}

func TestRecognitionSourceComparisonV1KeepsNumbersAndConditions(t *testing.T) {
	for _, tc := range []struct {
		name, left, right string
		equal             bool
	}{
		{"actual multiply display", `\(\frac{7}{9}×\frac{3}{14}=\)`, `\(\frac{7}{9}\times\frac{3}{14}=\)`, true},
		{"plain fraction display", `\(\frac{7}{9}\times\frac{3}{14}=\)`, "7/9×3/14=", true},
		{"different denominator", "7/9×3/14=", "7/9×3/11=", false},
		{"different operator", "7/9×3/14=", "7/9÷3/14=", false},
		{"equal numeric value is not same source", "2/4", "1/2", false},
		{"different condition", "每包3个，买2包", "每包2个，买3包", false},
		{"number boundary", "数字1 2", "数字12", false},
		{"mixed number boundary", "长1 1/2米", "长11/2米", false},
		{"different grouping", "1/(2+3)", "1/2+3", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RecognitionSourceQuestionsEqualV1(tc.left, tc.right); got != tc.equal {
				t.Fatalf("source equality=%v want=%v", got, tc.equal)
			}
		})
	}
}

func TestRecognitionSourceObservationV1UsesActualUnresolvedSignal(t *testing.T) {
	raw, err := os.ReadFile("testdata/recognition_same_read_uncertainty.json")
	if err != nil {
		t.Fatal(err)
	}
	var source RecognitionFinalAnswerSourceV1
	var readings struct {
		Questions []string `json:"evidence_transcriptions"`
		Answers   []string `json:"answer_evidence_transcriptions"`
	}
	if json.Unmarshal(raw, &source) != nil || json.Unmarshal(raw, &readings) != nil {
		t.Fatal("invalid actual observation fixture")
	}
	before := append([]string(nil), source.OCRSignals...)
	if !RecognitionSourceObservationUnresolvedV1(source.OCRSignals) || !RecognitionFinalAnswerSourcesTrustedV1(source) ||
		!RecognitionFinalAnswerQuestionSourcesMatchV1(readings.Questions, source.Question) || len(readings.Answers) != 2 || readings.Answers[0] != readings.Answers[1] {
		t.Fatal("same reading discarded its independent unresolved observation")
	}
	if !reflect.DeepEqual(before, source.OCRSignals) {
		t.Fatal("source risk was modified")
	}
	if RecognitionSourceObservationUnresolvedV1([]string{"", "  "}) {
		t.Fatal("empty signal became an unresolved observation")
	}
}
