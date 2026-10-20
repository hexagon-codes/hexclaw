package k12

import (
	"encoding/json"
	"math"
	"strings"
)

// RecognitionPageOrientationV1 绑定首读原图的阅读方向，仅用于派生展示图。
// 所有识题、定位及批改坐标继续使用原图，不随PDF旋转而改变。
type RecognitionPageOrientationV1 struct {
	Version          string  `json:"version"`
	PageDigest       string  `json:"page_digest"`
	ClockwiseDegrees int     `json:"clockwise_degrees"`
	Confidence       float64 `json:"confidence"`
}

// Rotation 只采用明确且可信的直角方向；旧回执或不确定观察保留原图。
func (o *RecognitionPageOrientationV1) Rotation() int {
	if o == nil || !o.Valid() || o.Confidence < 0.90 {
		return 0
	}
	return o.ClockwiseDegrees
}

func (o RecognitionPageOrientationV1) Valid() bool {
	return o.Version == "page_orientation_v1" && validRecognitionLayoutSHA256(o.PageDigest) &&
		(o.ClockwiseDegrees == 0 || o.ClockwiseDegrees == 90 || o.ClockwiseDegrees == 180 || o.ClockwiseDegrees == 270) &&
		!math.IsNaN(o.Confidence) && !math.IsInf(o.Confidence, 0) && o.Confidence >= 0 && o.Confidence <= 1
}

// RecognitionPageOrientationFromInitialReadV1 读取同次首读的可选元数据。
// 方向不合法不能破坏原本可用的题目转写，也不能据图片长宽猜测方向。
func RecognitionPageOrientationFromInitialReadV1(raw, pageDigest string) *RecognitionPageOrientationV1 {
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(sanitizeInitialReadModelJSONV1(raw[start:end+1])), &envelope) != nil {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(envelope["page_orientation"], &fields) != nil || len(fields) != 2 {
		return nil
	}
	var angle *int
	var confidence *float64
	if json.Unmarshal(fields["clockwise_degrees"], &angle) != nil || angle == nil ||
		json.Unmarshal(fields["confidence"], &confidence) != nil || confidence == nil {
		return nil
	}
	value := &RecognitionPageOrientationV1{Version: "page_orientation_v1", PageDigest: pageDigest, ClockwiseDegrees: *angle, Confidence: *confidence}
	if !value.Valid() {
		return nil
	}
	return value
}
