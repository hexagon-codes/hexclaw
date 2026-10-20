package k12

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const RecognitionLayoutInitialReadCompactFormatV1 = "initial_read_compact_v1"

type recognitionInitialReadCompactSectionV1 struct {
	Path  []string
	Label string
}

// 紧凑格式只减少传输重复；规范来源条目仍由原物理响应确定性展开。
func canonicalRecognitionLayoutInitialReadCompactV1(envelope map[string]json.RawMessage) ([]json.RawMessage, error) {
	fail := func(detail string) ([]json.RawMessage, error) {
		return nil, fmt.Errorf("%w: compact initial read %s", ErrRecognitionProtocolInvalid, detail)
	}
	if !initialReadCompactExactFieldsV1(envelope, "format", "sections", "conditions", "targets") {
		return fail("envelope fields are invalid")
	}
	var format string
	if json.Unmarshal(envelope["format"], &format) != nil || format != RecognitionLayoutInitialReadCompactFormatV1 {
		return fail("format is unsupported")
	}
	var sectionEntries, targets []map[string]json.RawMessage
	var rawConditions []json.RawMessage
	if json.Unmarshal(envelope["sections"], &sectionEntries) != nil || sectionEntries == nil ||
		json.Unmarshal(envelope["conditions"], &rawConditions) != nil || rawConditions == nil ||
		json.Unmarshal(envelope["targets"], &targets) != nil || len(targets) < 1 || len(targets) > 32 {
		return fail("envelope values are invalid")
	}
	conditions := make([]string, len(rawConditions))
	for index, raw := range rawConditions {
		if json.Unmarshal(raw, &conditions[index]) == nil {
			continue
		}
		// 同一正文的单字段包装可无损展开；其他形态仍拒绝，原响应不改写。
		var wrapped map[string]json.RawMessage
		if json.Unmarshal(raw, &wrapped) != nil || len(wrapped) != 1 || json.Unmarshal(wrapped["text"], &conditions[index]) != nil {
			return fail("shared condition is invalid")
		}
	}
	sections := make([]recognitionInitialReadCompactSectionV1, len(sectionEntries))
	for i, entry := range sectionEntries {
		var label *string
		if !initialReadCompactExactFieldsV1(entry, "path", "label") ||
			json.Unmarshal(entry["path"], &sections[i].Path) != nil || sections[i].Path == nil ||
			json.Unmarshal(entry["label"], &label) != nil || label == nil {
			return fail("section is invalid")
		}
		sections[i].Label = *label
	}
	for _, condition := range conditions {
		if strings.TrimSpace(condition) == "" {
			return fail("shared condition is empty")
		}
	}
	result := make([]json.RawMessage, len(targets))
	for i, target := range targets {
		entry, err := expandRecognitionInitialReadCompactTargetV1(target, i, sections, conditions)
		if err != nil {
			return fail(err.Error())
		}
		result[i] = entry
	}
	return result, nil
}

func expandRecognitionInitialReadCompactTargetV1(
	target map[string]json.RawMessage, index int,
	sections []recognitionInitialReadCompactSectionV1, conditions []string,
) (json.RawMessage, error) {
	if !initialReadCompactExactFieldsV1(target, "number", "label", "section", "shared", "region", "kind", "question", "subject", "answer_state", "student_answer", "confidence", "risks", "answer_bbox", "ownership") {
		return nil, fmt.Errorf("target fields are invalid")
	}
	var number []string
	var label, kind, ownership *string
	var sharedIndices []*int
	if json.Unmarshal(target["number"], &number) != nil || number == nil ||
		json.Unmarshal(target["label"], &label) != nil || label == nil ||
		json.Unmarshal(target["kind"], &kind) != nil || kind == nil ||
		json.Unmarshal(target["ownership"], &ownership) != nil || ownership == nil ||
		json.Unmarshal(target["shared"], &sharedIndices) != nil || sharedIndices == nil {
		return nil, fmt.Errorf("target values are invalid")
	}
	section := recognitionInitialReadCompactSectionV1{Path: []string{}}
	if !bytes.Equal(bytes.TrimSpace(target["section"]), []byte("null")) {
		var sectionIndex int
		if json.Unmarshal(target["section"], &sectionIndex) != nil || sectionIndex < 0 || sectionIndex >= len(sections) {
			return nil, fmt.Errorf("section reference is invalid")
		}
		section = sections[sectionIndex]
	}
	shared := make([]string, 0, len(sharedIndices))
	seen := make(map[int]struct{}, len(sharedIndices))
	for _, reference := range sharedIndices {
		if reference == nil || *reference < 0 || *reference >= len(conditions) {
			return nil, fmt.Errorf("shared condition reference is invalid")
		}
		conditionIndex := *reference
		if _, duplicate := seen[conditionIndex]; duplicate {
			return nil, fmt.Errorf("shared condition reference is duplicated")
		}
		seen[conditionIndex] = struct{}{}
		shared = append(shared, conditions[conditionIndex])
	}
	region, err := initialReadCompactRegionV1(target["region"])
	if err != nil {
		return nil, err
	}
	var recognition any
	switch *kind {
	case "question":
		var question, subject, answerState, answer *string
		var confidence *float64
		var risks []string
		if json.Unmarshal(target["question"], &question) != nil || question == nil ||
			json.Unmarshal(target["subject"], &subject) != nil || subject == nil ||
			json.Unmarshal(target["answer_state"], &answerState) != nil || answerState == nil ||
			json.Unmarshal(target["student_answer"], &answer) != nil || answer == nil ||
			json.Unmarshal(target["confidence"], &confidence) != nil || confidence == nil ||
			json.Unmarshal(target["risks"], &risks) != nil || risks == nil {
			return nil, fmt.Errorf("recognition values are invalid")
		}
		var answerBox any
		if !bytes.Equal(bytes.TrimSpace(target["answer_bbox"]), []byte("null")) {
			answerBox, err = initialReadCompactRegionV1(target["answer_bbox"])
			if err != nil {
				return nil, err
			}
		}
		// 共享条件按来源指定顺序补回；不修改题干、作答或数学符号。
		completeQuestion := *question
		if len(shared) > 0 {
			completeQuestion = strings.Join(shared, "\n") + "\n" + completeQuestion
		}
		recognition = map[string]any{
			"question": completeQuestion, "subject": *subject,
			"answer_state": *answerState, "student_answer": *answer,
			"recognition_confidence": *confidence, "ocr_signals": risks, "answer_bbox": answerBox,
		}
	case "non_question":
		for _, key := range []string{"question", "subject", "answer_state", "student_answer", "confidence", "risks", "answer_bbox"} {
			if !bytes.Equal(bytes.TrimSpace(target[key]), []byte("null")) {
				return nil, fmt.Errorf("non-question contains recognition content")
			}
		}
	default:
		return nil, fmt.Errorf("target kind is invalid")
	}
	return json.Marshal(map[string]any{
		"manifest_ref": fmt.Sprintf("manifest_%04d", index+1), "manifest_order": index + 1,
		"source_number_path": number, "display_label": *label,
		"source_section_path": section.Path, "source_section_label": section.Label, "region": region,
		"initial_read": map[string]any{
			"kind": *kind, "recognition": recognition,
			"shared_conditions": strings.Join(shared, "\n"), "answer_ownership": *ownership,
		},
	})
}

func initialReadCompactExactFieldsV1(fields map[string]json.RawMessage, names ...string) bool {
	if len(fields) != len(names) {
		return false
	}
	for _, name := range names {
		if _, exists := fields[name]; !exists {
			return false
		}
	}
	return true
}

func initialReadCompactRegionV1(raw json.RawMessage) (SourcePixelRegion, error) {
	var coordinates []*int
	if json.Unmarshal(raw, &coordinates) != nil || len(coordinates) != 4 {
		return SourcePixelRegion{}, fmt.Errorf("region must contain four integer coordinates")
	}
	for _, coordinate := range coordinates {
		if coordinate == nil {
			return SourcePixelRegion{}, fmt.Errorf("region must contain four integer coordinates")
		}
	}
	return SourcePixelRegion{X: *coordinates[0], Y: *coordinates[1], Width: *coordinates[2], Height: *coordinates[3]}, nil
}
