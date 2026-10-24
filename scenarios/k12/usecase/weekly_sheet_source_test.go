package usecase

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func TestWeeklyMistakeSheetRestoresHistoricalSourcesWithoutWritingRecords(t *testing.T) {
	d, _ := newPipeline(t, fakeSolver{}, fakeGrader{}, nil)
	ctx := context.Background()
	shared := `小明\(\frac{2}{3}\)小时走了2千米。Read this condition in English.`
	current := "从上图看出：已知（ ）份是2千米。"
	scoped, ok := elementarymath.BuildPartCountSlotProblemFromSources(shared, current, current)
	if !ok {
		t.Fatal("valid source did not form the legacy request")
	}
	question := RecognizedQuestion{
		Question: "这个硬盘的总容量是多少？", Subject: "数学",
		ImageSourceContext: &k12.RecognitionImageSourceContextV1{
			Materials: []k12.RecognitionImageScopeDependencyV1{{Text: `已用空间占总容量的\(\frac{2}{5}\)，已用4.8 TB。`}},
		},
	}
	// 材料使用实际执行输入组装器，核对其包裹能在历史读侧逆转。
	materialProblem, _ := photoQuestionProblemTexts(question)
	if !strings.Contains(materialProblem, "Read-only source material") {
		t.Fatal("fixture did not pass through the real material-envelope producer")
	}
	legacy := []string{
		scoped,
		materialProblem,
		"Read and translate this sentence into Chinese.",
	}
	before := make(map[string]string)
	var scopedRecordID string
	for index, text := range legacy {
		fields := k12.MistakeFields{GradeTerm: "五年级上", Subject: "数学", Question: text, EntrySource: k12.MistakeEntryPhoto, CanonicalAnswer: "DO_NOT_PRINT_THE_STORED_ANSWER"}
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		due := int64(500 + index)
		// 直接存入历史字段形态，避免NewMistakeRecord的新写侧投影掩盖读侧问题。
		record := &records.AgentRecord{AgentName: "mingming", Collection: k12.CollectionMistakes, SourceSession: "weekly-source-fixture", Fields: string(raw), DueAt: &due}
		if _, err := d.Records.Put(ctx, record); err != nil {
			t.Fatal(err)
		}
		stored, err := d.Records.Get(ctx, record.RecordID)
		if err != nil {
			t.Fatal(err)
		}
		before[stored.RecordID] = stored.Fields
		if index == 0 {
			scopedRecordID = stored.RecordID
		}
	}
	seedAccumDue(t, d, "英语", "错词", "Read-only source material is an English phrase.", 900)
	markdown, err := d.MistakeSheetMarkdown(ctx, "mingming")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{current, shared, "这个硬盘的总容量是多少？", `已用空间占总容量的\(\frac{2}{5}\)，已用4.8 TB。`, "Read and translate this sentence into Chinese.", "Read-only source material is an English phrase."} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("source content was lost: %q", want)
		}
	}
	for _, unwanted := range []string{"k12-part-count-slot-v1", "Shared context:", "Answer target:", "Read-only source material (do not answer as another question):", "DO_NOT_PRINT_THE_STORED_ANSWER"} {
		if strings.Contains(markdown, unwanted) {
			t.Fatalf("execution protocol or reference answer leaked: %q", unwanted)
		}
	}
	if strings.Count(markdown, "\n\n（　　）\n\n") != 4 {
		t.Fatal("source recovery dropped or duplicated a review item")
	}
	exported, err := d.ExportMistakesMarkdown(ctx, "mingming")
	if err != nil {
		t.Fatal(err)
	}
	records, err := d.Records.ListByScope(ctx, "mingming", k12.CollectionMistakes, "")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := renderLearningArchiveMarkdown(k12storage.LearningArchiveSourceSnapshot{
		Mistakes: records, WeeklyReview: []k12.WeeklyPracticeItem{{SourceRef: scopedRecordID, PromptMarkdown: scoped}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{exported, archive} {
		for _, wanted := range []string{current, shared, "Read and translate this sentence into Chinese."} {
			if !strings.Contains(content, wanted) {
				t.Fatalf("another human-readable export lost source content: %q", wanted)
			}
		}
		for _, unwanted := range []string{"k12-part-count-slot-v1", "Answer target:", "Read-only source material (do not answer as another question):"} {
			if strings.Contains(content, unwanted) {
				t.Fatalf("another human-readable export leaked an execution protocol: %q", unwanted)
			}
		}
	}
	for id, fields := range before {
		after, err := d.Records.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if after.Fields != fields {
			t.Fatal("rendering rewrote a historical mistake record")
		}
	}
}
