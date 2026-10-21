package k12

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompactInitialReadPreservesConditionTextWithRegionAnnotation(t *testing.T) {
	// 合成同形协议样例，验证附加区域不能使已读出的共享正文整体丢失。
	raw := `{"format":"initial_read_compact_v1","sections":[],"conditions":[{"text":"共有12本书。","region":{"x":10,"y":5,"width":100,"height":20}}],"targets":[{"number":["1"],"label":"1","section":null,"shared":[0],"region":[0,30,200,100],"kind":"question","question":"平均分给3人，每人几本？","subject":"数学","answer_state":"blank","student_answer":"","confidence":0.98,"risks":[],"answer_bbox":null,"ownership":"答题区无笔迹。"}]}`
	entries, err := CanonicalRecognitionLayoutInitialReadEntriesV1(raw)
	if err != nil || len(entries) != 1 {
		t.Fatalf("source annotation discarded the target: %v", err)
	}
	_, read, _, err := ProjectRecognitionLayoutInitialObservationV1(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	var observation struct {
		Shared      string `json:"shared_conditions"`
		Recognition struct {
			Question string `json:"question"`
		} `json:"recognition"`
	}
	if err := json.Unmarshal(read, &observation); err != nil || observation.Shared != "共有12本书。" || !strings.Contains(observation.Recognition.Question, "共有12本书。") {
		t.Fatalf("shared source text changed or disappeared: %+v %v", observation, err)
	}
	for _, invalid := range []string{
		strings.Replace(raw, `"width":100`, `"width":0`, 1),
		strings.Replace(raw, `"x":10`, `"x":-1`, 1),
		strings.Replace(raw, `"text":"共有12本书。"`, `"text":"共有12本书。","answer":"4本"`, 1),
	} {
		if _, err := CanonicalRecognitionLayoutInitialReadEntriesV1(invalid); err == nil {
			t.Fatal("invalid geometry or an unrelated answer entered the condition contract")
		}
	}
}
