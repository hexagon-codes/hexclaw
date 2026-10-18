package k12

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// RecognitionFinalAnswerEvidenceV1 保存独立确认的最终答案，BBox 使用原页绝对像素。
// 它不证明中间步骤完整，不替换整份作答及其既有风险。
type RecognitionFinalAnswerEvidenceV1 struct {
	RawTranscription         string            `json:"raw_transcription"`
	BBox                     SourcePixelRegion `json:"bbox"`
	IsFinalAnswer            bool              `json:"is_final_answer"`
	TargetOwnershipConfirmed bool              `json:"target_ownership_confirmed"`
	NotCancelled             bool              `json:"not_cancelled"`
	CompleteAndLegible       bool              `json:"complete_and_legible"`
}

// RecognitionFinalAnswerReceiptV1 把答案证据绑定到同题的独立裁决与持久结算。
type RecognitionFinalAnswerReceiptV1 struct {
	Evidence                   RecognitionFinalAnswerEvidenceV1 `json:"evidence"`
	PlanDigest                 string                           `json:"plan_digest"`
	CandidateID                string                           `json:"candidate_id"`
	SourcePhysicalInvocationID string                           `json:"source_physical_invocation_id"`
	SourcePhysicalResultDigest string                           `json:"source_physical_result_digest"`
	AuthorizationID            string                           `json:"authorization_id"`
	SettlementDigest           string                           `json:"settlement_digest"`
}

// ParseRecognitionFinalAnswerEvidenceV1 解析 nullable 的六字段原始对象，并把裁片坐标转换到原页。
// 明确未确认的观察返回 nil；不从转写末值、置信度或几何位置推断独立确认。
func ParseRecognitionFinalAnswerEvidenceV1(raw json.RawMessage, contextRegion SourcePixelRegion) (*RecognitionFinalAnswerEvidenceV1, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	fields, err := recognitionFinalAnswerObjectV1(raw, []string{"raw_transcription", "bbox", "is_final_answer", "target_ownership_confirmed", "not_cancelled", "complete_and_legible"})
	if err != nil {
		return nil, err
	}
	var evidence RecognitionFinalAnswerEvidenceV1
	if err := json.Unmarshal(fields["raw_transcription"], &evidence.RawTranscription); err != nil || bytes.Equal(fields["raw_transcription"], []byte("null")) {
		return nil, fmt.Errorf("%w: final answer transcription is invalid", ErrRecognitionProtocolInvalid)
	}
	for name, target := range map[string]*bool{
		"is_final_answer":            &evidence.IsFinalAnswer,
		"target_ownership_confirmed": &evidence.TargetOwnershipConfirmed,
		"not_cancelled":              &evidence.NotCancelled,
		"complete_and_legible":       &evidence.CompleteAndLegible,
	} {
		if !bytes.Equal(bytes.TrimSpace(fields[name]), []byte("true")) && !bytes.Equal(bytes.TrimSpace(fields[name]), []byte("false")) {
			return nil, fmt.Errorf("%w: final answer confirmation is invalid", ErrRecognitionProtocolInvalid)
		}
		if err := json.Unmarshal(fields[name], target); err != nil {
			return nil, err
		}
	}
	if !evidence.IsFinalAnswer || !evidence.TargetOwnershipConfirmed || !evidence.NotCancelled || !evidence.CompleteAndLegible {
		return nil, nil
	}
	if _, err := recognitionFinalAnswerObjectV1(fields["bbox"], []string{"x", "y", "width", "height"}); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(fields["bbox"], &evidence.BBox); err != nil {
		return nil, fmt.Errorf("%w: final answer geometry is invalid", ErrRecognitionProtocolInvalid)
	}
	local := SourcePixelRegion{Width: contextRegion.Width, Height: contextRegion.Height}
	if err := ValidateRecognitionFinalAnswerEvidenceV1(evidence, local); err != nil {
		return nil, err
	}
	evidence.BBox.X += contextRegion.X
	evidence.BBox.Y += contextRegion.Y
	if err := ValidateRecognitionFinalAnswerEvidenceV1(evidence, contextRegion); err != nil {
		return nil, err
	}
	return &evidence, nil
}

// ValidateRecognitionFinalAnswerEvidenceV1 仅验证已确认的原页答案证据及所属裁片，不验证数学对错。
func ValidateRecognitionFinalAnswerEvidenceV1(evidence RecognitionFinalAnswerEvidenceV1, contextRegion SourcePixelRegion) error {
	b := evidence.BBox
	if strings.TrimSpace(evidence.RawTranscription) == "" || !evidence.IsFinalAnswer || !evidence.TargetOwnershipConfirmed || !evidence.NotCancelled || !evidence.CompleteAndLegible ||
		contextRegion.X < 0 || contextRegion.Y < 0 || contextRegion.Width <= 0 || contextRegion.Height <= 0 ||
		b.X < contextRegion.X || b.Y < contextRegion.Y || b.Width <= 0 || b.Height <= 0 || b.Width > contextRegion.Width || b.Height > contextRegion.Height ||
		b.X-contextRegion.X > contextRegion.Width-b.Width || b.Y-contextRegion.Y > contextRegion.Height-b.Height ||
		(b.Width == contextRegion.Width && b.Height == contextRegion.Height) {
		return fmt.Errorf("%w: final answer evidence is incomplete or outside its source", ErrRecognitionProtocolInvalid)
	}
	return nil
}

func recognitionFinalAnswerObjectV1(raw json.RawMessage, keys []string) (map[string]json.RawMessage, error) {
	fail := func() (map[string]json.RawMessage, error) {
		return nil, fmt.Errorf("%w: final answer object fields are invalid", ErrRecognitionProtocolInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return fail()
	}
	fields := make(map[string]json.RawMessage, len(keys))
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return fail()
		}
		if _, duplicate := fields[key]; duplicate {
			return fail()
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fail()
		}
		fields[key] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return fail()
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF || len(fields) != len(keys) {
		return fail()
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return fail()
		}
	}
	return fields, nil
}
