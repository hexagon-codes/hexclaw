package k12

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestRecognitionFinalAnswerResponseRepeatedNullObservation(t *testing.T) {
	data, err := os.ReadFile("testdata/recognition_duplicate_null_observation.json")
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	region := SourcePixelRegion{Width: 960, Height: 628}
	response, err := ParseRecognitionFinalAnswerResponseV1(raw, region)
	if err != nil || response.Evidence != nil || response.QuestionCompleteAndLegible || response.QuestionRawTranscription == "" {
		t.Fatalf("repeated null observation failed or invented source evidence: %+v %v", response, err)
	}
	const evidence = `{"raw_transcription":"24","bbox":{"x":180,"y":232,"width":76,"height":24},"is_final_answer":true,"target_ownership_confirmed":true,"not_cancelled":true,"complete_and_legible":true}`
	single := strings.Replace(raw, `"final_answer":null,`, "", 1)
	for _, invalid := range []string{
		strings.ReplaceAll(raw, `"final_answer":null`, `"final_answer":`+evidence),
		strings.Replace(raw, `"final_answer":null`, `"final_answer":`+evidence, 1),
		strings.Replace(single, `"verification":{`, `"verification":null,"verification":{`, 1),
		strings.Replace(single, `"subject":"数学"`, `"subject":"数学","subject":"数学"`, 1),
	} {
		if _, err := ParseRecognitionFinalAnswerResponseV1(invalid, region); !errors.Is(err, ErrRecognitionProtocolInvalid) {
			t.Fatal("non-null, conflicting or unrelated repeated field was accepted")
		}
	}
}

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
