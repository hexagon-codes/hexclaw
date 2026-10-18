package k12

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRecognitionFinalAnswerEvidenceV1RequiresIndependentConfirmation(t *testing.T) {
	region := SourcePixelRegion{X: 400, Y: 100, Width: 500, Height: 400}
	const raw = `{"raw_transcription":"1/3","bbox":{"x":200,"y":180,"width":30,"height":25},"is_final_answer":true,"target_ownership_confirmed":true,"not_cancelled":true,"complete_and_legible":true}`
	evidence, err := ParseRecognitionFinalAnswerEvidenceV1(json.RawMessage(raw), region)
	if err != nil || evidence == nil || evidence.RawTranscription != "1/3" || evidence.BBox != (SourcePixelRegion{X: 600, Y: 280, Width: 30, Height: 25}) {
		t.Fatalf("independent evidence=%+v err=%v", evidence, err)
	}
	if err := ValidateRecognitionFinalAnswerEvidenceV1(*evidence, region); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"is_final_answer", "target_ownership_confirmed", "not_cancelled", "complete_and_legible"} {
		t.Run(field, func(t *testing.T) {
			changed := strings.Replace(raw, `"`+field+`":true`, `"`+field+`":false`, 1)
			got, err := ParseRecognitionFinalAnswerEvidenceV1(json.RawMessage(changed), region)
			if err != nil || got != nil {
				t.Fatalf("uncertain answer became evidence: %+v %v", got, err)
			}
		})
	}
	if got, err := ParseRecognitionFinalAnswerEvidenceV1(json.RawMessage("null"), region); err != nil || got != nil {
		t.Fatalf("null became evidence: %+v %v", got, err)
	}
	for _, changed := range []string{
		strings.Replace(raw, `"not_cancelled":true,`, "", 1),
		strings.Replace(raw, `"not_cancelled":true`, `"not_cancelled":null`, 1),
		strings.Replace(raw, `"not_cancelled":true`, `"not_cancelled":"true"`, 1),
		strings.Replace(raw, `"not_cancelled":true`, `"not_cancelled":false,"not_cancelled":true`, 1),
		strings.Replace(raw, `"x":200`, `"x":-1`, 1),
		strings.Replace(raw, `"x":200`, `"x":490`, 1),
		strings.Replace(raw, `"x":200`, `"x":20.5`, 1),
		strings.Replace(raw, `"width":30`, `"width":0`, 1),
		strings.Replace(raw, `"raw_transcription":"1/3"`, `"raw_transcription":" "`, 1),
		strings.Replace(raw, `"raw_transcription":"1/3"`, `"raw_transcription":null`, 1),
		strings.Replace(raw, `{"x":200,"y":180,"width":30,"height":25}`, `{"x":0,"y":0,"width":500,"height":400}`, 1),
		raw + ` {"other":true}`,
	} {
		if got, err := ParseRecognitionFinalAnswerEvidenceV1(json.RawMessage(changed), region); err == nil || got != nil {
			t.Fatalf("invalid observation accepted: %s => %+v %v", changed, got, err)
		}
	}
}
