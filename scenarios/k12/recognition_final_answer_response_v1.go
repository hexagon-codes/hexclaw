package k12

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// RecognitionFinalAnswerResponseV1 仅投影独立裁决的原始观察，不推导答案正确性。
type RecognitionFinalAnswerResponseV1 struct {
	Evidence                   *RecognitionFinalAnswerEvidenceV1
	QuestionRawTranscription   string
	LegacyEnvelope             json.RawMessage
	TargetOwnershipConfirmed   bool
	ActiveAnswerComplete       bool
	QuestionCompleteAndLegible bool
}

// ParseRecognitionFinalAnswerResponseV1 由适配器与持久化边界共用，原响应字节由调用账本保存。
func ParseRecognitionFinalAnswerResponseV1(raw string, region SourcePixelRegion) (RecognitionFinalAnswerResponseV1, error) {
	var out RecognitionFinalAnswerResponseV1
	fail := func() (RecognitionFinalAnswerResponseV1, error) {
		return out, fmt.Errorf("%w: final-answer response fields are invalid", ErrRecognitionProtocolInvalid)
	}
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return fail()
	}
	envelope, err := recognitionFinalAnswerObjectV1([]byte(sanitizeInitialReadModelJSONV1(raw[start:end+1])), []string{"verification", "items", "final_answer"})
	if err != nil {
		return fail()
	}
	verification, err := recognitionFinalAnswerObjectV1(envelope["verification"], []string{"target_ownership_confirmed", "active_answer_complete", "question_complete_and_legible"})
	var ownership, complete, questionComplete *bool
	if err != nil ||
		json.Unmarshal(verification["target_ownership_confirmed"], &ownership) != nil || ownership == nil ||
		json.Unmarshal(verification["active_answer_complete"], &complete) != nil || complete == nil ||
		json.Unmarshal(verification["question_complete_and_legible"], &questionComplete) != nil || questionComplete == nil {
		return fail()
	}
	var items []json.RawMessage
	if json.Unmarshal(envelope["items"], &items) != nil || len(items) != 1 {
		return fail()
	}
	item, err := recognitionFinalAnswerObjectV1(items[0], []string{"target_id", "kind", "recognition"})
	if err != nil {
		return fail()
	}
	var targetID, kind string
	if json.Unmarshal(item["target_id"], &targetID) != nil || targetID != "t1" || json.Unmarshal(item["kind"], &kind) != nil {
		return fail()
	}
	switch kind {
	case "question":
		recognition, err := recognitionFinalAnswerObjectV1(item["recognition"], []string{"question", "subject", "answer_state", "student_answer", "recognition_confidence", "ocr_signals", "answer_bbox"})
		if err != nil || json.Unmarshal(recognition["question"], &out.QuestionRawTranscription) != nil || strings.TrimSpace(out.QuestionRawTranscription) == "" {
			return fail()
		}
		out.Evidence, err = ParseRecognitionFinalAnswerEvidenceV1(envelope["final_answer"], region)
		if err != nil {
			return out, err
		}
	case "non_question":
		if !bytes.Equal(bytes.TrimSpace(item["recognition"]), []byte("null")) || !bytes.Equal(bytes.TrimSpace(envelope["final_answer"]), []byte("null")) {
			return fail()
		}
	default:
		return fail()
	}
	out.TargetOwnershipConfirmed, out.ActiveAnswerComplete = *ownership, *complete
	out.QuestionCompleteAndLegible = *questionComplete
	if !*ownership || !*questionComplete {
		out.Evidence = nil
	}
	delete(verification, "question_complete_and_legible")
	envelope["verification"], err = json.Marshal(verification)
	if err != nil {
		return out, err
	}
	delete(envelope, "final_answer")
	out.LegacyEnvelope, err = json.Marshal(envelope)
	return out, err
}

// RecognitionFinalAnswerQuestionSourcesMatchV1 只比较三份完整题干，不计算或改写数学事实。
func RecognitionFinalAnswerQuestionSourcesMatchV1(priors []string, third string) bool {
	if len(priors) != 2 || strings.TrimSpace(third) == "" {
		return false
	}
	return RecognitionSourceQuestionsEqualV1(priors[0], third) && RecognitionSourceQuestionsEqualV1(priors[1], third)
}
