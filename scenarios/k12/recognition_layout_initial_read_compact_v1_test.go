package k12

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestRecognitionInitialReadCompactV1PreservesSourceObservations(t *testing.T) {
	raw, err := os.ReadFile("testdata/initial_read_compact_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	original := append([]byte(nil), raw...)
	entries, err := CanonicalRecognitionLayoutInitialReadEntriesV1(string(raw))
	if err != nil || len(entries) != 2 {
		t.Fatalf("compact source rejected: count=%d err=%v", len(entries), err)
	}
	var first, second struct {
		ManifestRef        string            `json:"manifest_ref"`
		ManifestOrder      int               `json:"manifest_order"`
		SourceNumberPath   []string          `json:"source_number_path"`
		SourceSectionPath  []string          `json:"source_section_path"`
		SourceSectionLabel string            `json:"source_section_label"`
		Region             SourcePixelRegion `json:"region"`
		InitialRead        struct {
			SharedConditions string `json:"shared_conditions"`
			AnswerOwnership  string `json:"answer_ownership"`
			Recognition      struct {
				Question      string            `json:"question"`
				StudentAnswer string            `json:"student_answer"`
				OCRSignals    []string          `json:"ocr_signals"`
				AnswerBBox    SourcePixelRegion `json:"answer_bbox"`
			} `json:"recognition"`
		} `json:"initial_read"`
	}
	if json.Unmarshal(entries[0], &first) != nil || json.Unmarshal(entries[1], &second) != nil {
		t.Fatal("expanded source is not decodable")
	}
	if first.ManifestRef != "manifest_0001" || first.ManifestOrder != 1 || second.ManifestRef != "manifest_0002" || second.ManifestOrder != 2 ||
		!reflect.DeepEqual(first.SourceNumberPath, []string{"1"}) || !reflect.DeepEqual(second.SourceNumberPath, []string{"2"}) {
		t.Fatal("array position or printed numbering changed")
	}
	if first.SourceSectionLabel != "一、解决问题" || !reflect.DeepEqual(second.SourceSectionPath, []string{"一"}) ||
		first.InitialRead.SharedConditions != `每朵花用\(\frac{3}{8}\)张纸。` ||
		first.InitialRead.Recognition.Question != "每朵花用\\(\\frac{3}{8}\\)张纸。\n做2朵花，共用多少张纸？" ||
		second.InitialRead.Recognition.Question != "每朵花用\\(\\frac{3}{8}\\)张纸。\n做4朵花，共用多少张纸？" {
		t.Fatal("section, shared condition or complete question changed")
	}
	if first.InitialRead.Recognition.StudentAnswer != "3/8×2=3/4\n答：3/4张纸。" ||
		second.InitialRead.Recognition.StudentAnswer != "3/8×4=3\n答：3张纸。" ||
		!reflect.DeepEqual(second.InitialRead.Recognition.OCRSignals, []string{"分母有涂改痕迹"}) ||
		second.InitialRead.AnswerOwnership != "第二题下方的两行手写。" ||
		second.Region != (SourcePixelRegion{X: 0, Y: 20, Width: 20, Height: 10}) ||
		second.InitialRead.Recognition.AnswerBBox != (SourcePixelRegion{X: 10, Y: 22, Width: 8, Height: 5}) {
		t.Fatal("active working, wrong answer, risk, ownership or geometry was lost")
	}
	if !bytes.Equal(raw, original) {
		t.Fatal("source response was mutated")
	}
}

func TestRecognitionInitialReadCompactV1RejectsInvalidReferencesAndOverrides(t *testing.T) {
	raw, err := os.ReadFile("testdata/initial_read_compact_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"unknown format", "section outside", "condition outside", "condition duplicated", "condition null", "coordinate null", "missing observation", "manifest override", "recognition hidden in non-question"} {
		t.Run(scenario, func(t *testing.T) {
			var envelope map[string]any
			if json.Unmarshal(raw, &envelope) != nil {
				t.Fatal("invalid fixture")
			}
			target := envelope["targets"].([]any)[0].(map[string]any)
			switch scenario {
			case "unknown format":
				envelope["format"] = "initial_read_compact_unknown"
			case "section outside":
				target["section"] = 1
			case "condition outside":
				target["shared"] = []int{1}
			case "condition duplicated":
				target["shared"] = []int{0, 0}
			case "condition null":
				target["shared"] = []any{nil}
			case "coordinate null":
				target["region"] = []any{nil, 0, 20, 10}
			case "missing observation":
				delete(target, "ownership")
			case "manifest override":
				target["manifest_ref"] = "manifest_0099"
			case "recognition hidden in non-question":
				target["kind"] = "non_question"
			}
			mutated, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = CanonicalRecognitionLayoutInitialReadEntriesV1(string(mutated)); !errors.Is(err, ErrRecognitionProtocolInvalid) {
				t.Fatalf("invalid compact source accepted: %v", err)
			}
		})
	}
}
