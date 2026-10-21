package k12

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"strings"
)

// AdjudicationImageRotation 仅为新解题计划采用同页可信方向；持久坐标仍属于原图。
func (p RecognitionLayoutPlanV2) AdjudicationImageRotation() int {
	if p.ScopeInput == nil || p.ScopeInput.Version != RecognitionImageScopeVersionV1 ||
		p.ScopeInput.Action != "solve" || p.ScopeInput.SourceReviewPolicy != RecognitionImageSourceUprightReviewPolicyV1 ||
		p.PageOrientation == nil || p.PageOrientation.PageDigest != p.PageDigest {
		return 0
	}
	return p.PageOrientation.Rotation()
}

func recognitionAdjudicationViewPNG(raw []byte, rotation int) ([]byte, error) {
	if rotation == 0 {
		return raw, nil
	}
	source, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	w, h := source.Bounds().Dx(), source.Bounds().Dy()
	width, height := w, h
	if rotation == 90 || rotation == 270 {
		width, height = h, w
	}
	view := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := x, y
			switch rotation {
			case 90:
				dx, dy = h-1-y, x
			case 180:
				dx, dy = w-1-x, h-1-y
			case 270:
				dx, dy = y, w-1-x
			default:
				return nil, fmt.Errorf("invalid adjudication view rotation")
			}
			view.Set(dx, dy, source.At(x, y))
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, view); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

// RecognitionLayoutAdjudicationViewTargetV1 派生 Provider 的局部身份框，不修改授权目标。
func RecognitionLayoutAdjudicationViewTargetV1(plan RecognitionLayoutPlanV2, target RecognitionLayoutTargetV2) (RecognitionLayoutTargetV2, error) {
	rotation := plan.AdjudicationImageRotation()
	if rotation == 0 {
		return target, nil
	}
	if target.AdjudicationRegion == nil || target.OriginalRegion == nil {
		return target, fmt.Errorf("adjudication target has no frozen context")
	}
	r := *target.AdjudicationRegion
	focus := *target.OriginalRegion
	focus.X, focus.Y = focus.X-r.X, focus.Y-r.Y
	if err := validateRecognitionLayoutRegionV2(focus, image.Rect(0, 0, r.Width, r.Height)); err != nil {
		return target, err
	}
	switch rotation {
	case 90:
		focus = SourcePixelRegion{X: r.Height - focus.Y - focus.Height, Y: focus.X, Width: focus.Height, Height: focus.Width}
	case 180:
		focus.X, focus.Y = r.Width-focus.X-focus.Width, r.Height-focus.Y-focus.Height
	case 270:
		focus = SourcePixelRegion{X: focus.Y, Y: r.Width - focus.X - focus.Width, Width: focus.Height, Height: focus.Width}
	}
	view := SourcePixelRegion{Width: r.Width, Height: r.Height}
	if rotation == 90 || rotation == 270 {
		view.Width, view.Height = r.Height, r.Width
	}
	target.AdjudicationRegion, target.OriginalRegion = &view, &focus
	return target, nil
}

// RecognitionLayoutAdjudicationSourceResponseV1 将派生图的框逆映射到原裁片。
// 仅变换几何；原 Provider 响应、文字和置信观察由调用账本完整保留。
func RecognitionLayoutAdjudicationSourceResponseV1(raw string, plan RecognitionLayoutPlanV2, target RecognitionLayoutTargetV2) (string, error) {
	rotation := plan.AdjudicationImageRotation()
	if rotation == 0 {
		return raw, nil
	}
	view, err := RecognitionLayoutAdjudicationViewTargetV1(plan, target)
	if err != nil {
		return "", err
	}
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return "", fmt.Errorf("%w: adjudication response is invalid", ErrRecognitionProtocolInvalid)
	}
	data := []byte(sanitizeInitialReadModelJSONV1(raw[start : end+1]))
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return "", err
	}
	keys := []string{"verification", "items"}
	if _, ok := probe["final_answer"]; ok {
		keys = append(keys, "final_answer")
	}
	envelope, err := recognitionFinalAnswerObjectV1(data, keys)
	if err != nil {
		return "", err
	}
	verificationKeys := []string{"target_ownership_confirmed", "active_answer_complete"}
	if len(keys) == 3 {
		verificationKeys = append(verificationKeys, "question_complete_and_legible")
	}
	if _, err := recognitionFinalAnswerObjectV1(envelope["verification"], verificationKeys); err != nil {
		return "", err
	}
	mapBox := func(object map[string]json.RawMessage, key string) error {
		boxRaw, exists := object[key]
		if !exists || bytes.Equal(bytes.TrimSpace(boxRaw), []byte("null")) {
			return nil
		}
		if _, err := recognitionFinalAnswerObjectV1(boxRaw, []string{"x", "y", "width", "height"}); err != nil {
			return err
		}
		var box SourcePixelRegion
		if err := json.Unmarshal(boxRaw, &box); err != nil {
			return err
		}
		if err := validateRecognitionLayoutRegionV2(box, image.Rect(0, 0, view.AdjudicationRegion.Width, view.AdjudicationRegion.Height)); err != nil {
			return err
		}
		r := *target.AdjudicationRegion
		switch rotation {
		case 90:
			box = SourcePixelRegion{X: box.Y, Y: r.Height - box.X - box.Width, Width: box.Height, Height: box.Width}
		case 180:
			box.X, box.Y = r.Width-box.X-box.Width, r.Height-box.Y-box.Height
		case 270:
			box = SourcePixelRegion{X: r.Width - box.Y - box.Height, Y: box.X, Width: box.Height, Height: box.Width}
		}
		object[key], err = json.Marshal(box)
		return err
	}
	var itemRaws []json.RawMessage
	if err := json.Unmarshal(envelope["items"], &itemRaws); err != nil || len(itemRaws) != 1 {
		return "", fmt.Errorf("%w: adjudication item is invalid", ErrRecognitionProtocolInvalid)
	}
	item, err := recognitionFinalAnswerObjectV1(itemRaws[0], []string{"target_id", "kind", "recognition"})
	if err != nil {
		return "", err
	}
	var recognition map[string]json.RawMessage
	if !bytes.Equal(bytes.TrimSpace(item["recognition"]), []byte("null")) {
		recognition, err = recognitionFinalAnswerObjectV1(item["recognition"], []string{"question", "subject", "answer_state", "student_answer", "recognition_confidence", "ocr_signals", "answer_bbox"})
		if err != nil {
			return "", err
		}
	}
	if recognition != nil {
		if err := mapBox(recognition, "answer_bbox"); err != nil {
			// 普通作答框不是独立答案证据；不可用时保留未知，不伪造或裁出有效框。
			recognition["answer_bbox"] = json.RawMessage("null")
		}
		item["recognition"], _ = json.Marshal(recognition)
	}
	envelope["items"], _ = json.Marshal([]map[string]json.RawMessage{item})
	if finalRaw, exists := envelope["final_answer"]; exists && !bytes.Equal(bytes.TrimSpace(finalRaw), []byte("null")) {
		final, err := recognitionFinalAnswerObjectV1(finalRaw, []string{"raw_transcription", "bbox", "is_final_answer", "target_ownership_confirmed", "not_cancelled", "complete_and_legible"})
		if err != nil {
			return "", err
		}
		// 复用答案解析器的确认语义：未确认观察不要求有效框，已确认答案仍严格验框。
		proof, err := ParseRecognitionFinalAnswerEvidenceV1(finalRaw, *view.AdjudicationRegion)
		if err != nil {
			return "", err
		}
		if proof == nil {
			final["bbox"] = json.RawMessage("null")
		} else if err := mapBox(final, "bbox"); err != nil {
			return "", err
		}
		envelope["final_answer"], _ = json.Marshal(final)
	}
	encoded, err := json.Marshal(envelope)
	return string(encoded), err
}
