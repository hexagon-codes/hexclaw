package engineadapter

import (
	"encoding/json"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestInitialReadSharedConditionsSurviveLocalReview(t *testing.T) {
	const shared = "一个硬盘，已用空间是总容量的2/5，正好用了4.8 TB。"
	const raw = `{"format":"initial_read_compact_v1","sections":[],"conditions":["一个硬盘，已用空间是总容量的2/5，正好用了4.8 TB。"],"targets":[{"number":["3","1"],"label":"（1）","section":null,"shared":[0],"region":[10,10,30,20],"kind":"question","question":"先求一份是多少，再求总容量是多少。","subject":"数学","answer_state":"blank","student_answer":"","confidence":0.99,"risks":[],"answer_bbox":null,"ownership":"blank answer area"}]}`
	entries, err := parseRecognitionInitialReadEnvelopeV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	target := k12.RecognitionLayoutTargetV2{TargetID: "child-one", ManifestOrder: 1, SourceNumberPath: []string{"3", "1"}, DisplayLabel: "（1）", Region: entries[0].manifest.Region}
	plan := k12.RecognitionLayoutPlanV2{AuthorizedPlanDigest: "frozen-plan", Targets: []k12.RecognitionLayoutTargetV2{target}}
	question := usecase.RecognizedQuestion{SourceNumberPath: []string{"3", "1"}, SourceRegion: &target.Region, Question: "先求一份是多少，再求总容量是多少。", RawTranscription: "先求一份是多少，再求总容量是多少。", StudentAnswer: "2/5÷2=1/5"}
	got, err := retainInitialReadSharedMaterials([]usecase.RecognizedQuestion{question}, plan, raw, 80, 100)
	if err != nil || got[0].ImageSourceContext == nil || len(got[0].ImageSourceContext.Materials) != 1 || got[0].ImageSourceContext.Materials[0].Text != shared {
		t.Fatalf("shared conditions lost: %+v %v", got, err)
	}
	if got[0].Question != question.Question || got[0].RawTranscription != question.RawTranscription || got[0].StudentAnswer != question.StudentAnswer {
		t.Fatal("source projection replaced a transcription or student answer")
	}
	frozen, _ := json.Marshal(got)
	var replay []usecase.RecognizedQuestion
	if err := json.Unmarshal(frozen, &replay); err != nil {
		t.Fatal(err)
	}
	again, err := retainInitialReadSharedMaterials(replay, plan, raw, 80, 100)
	if err != nil || len(again[0].ImageSourceContext.Materials) != 1 {
		t.Fatal("replay duplicated shared source")
	}
	question.SourceNumberPath = []string{"4", "1"}
	other, err := retainInitialReadSharedMaterials([]usecase.RecognizedQuestion{question}, plan, raw, 80, 100)
	if err != nil || other[0].ImageSourceContext != nil {
		t.Fatal("another problem borrowed shared conditions")
	}
}
