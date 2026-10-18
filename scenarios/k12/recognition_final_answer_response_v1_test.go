package k12

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestRecognitionFinalAnswerResponseV1RetainsNonQuestionDisposition(t *testing.T) {
	raw := `{"verification":{"target_ownership_confirmed":false,"active_answer_complete":false,"question_complete_and_legible":false},"items":[{"target_id":"t1","kind":"non_question","recognition":null}],"final_answer":null}`
	response, err := ParseRecognitionFinalAnswerResponseV1(raw, SourcePixelRegion{Width: 100, Height: 100})
	if err != nil || response.Evidence != nil || response.QuestionRawTranscription != "" {
		t.Fatalf("existing non-question disposition became a protocol failure: %v", err)
	}
	var legacy struct {
		Verification map[string]bool `json:"verification"`
		Items        []struct {
			Kind        string          `json:"kind"`
			Recognition json.RawMessage `json:"recognition"`
		} `json:"items"`
	}
	if json.Unmarshal(response.LegacyEnvelope, &legacy) != nil || len(legacy.Verification) != 2 || len(legacy.Items) != 1 || legacy.Items[0].Kind != "non_question" || string(legacy.Items[0].Recognition) != "null" {
		t.Fatal("legacy parser envelope changed")
	}
	invalid := `{"verification":{"target_ownership_confirmed":true,"active_answer_complete":true,"question_complete_and_legible":true},"items":[{"target_id":"t1","kind":"non_question","recognition":null}],"final_answer":{"raw_transcription":"4"}}`
	if _, err := ParseRecognitionFinalAnswerResponseV1(invalid, SourcePixelRegion{Width: 100, Height: 100}); !errors.Is(err, ErrRecognitionProtocolInvalid) {
		t.Fatal("non-question fabricated a final answer")
	}
}
