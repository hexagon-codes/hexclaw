package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func reviewPaperFixture(id, question string) ReviewItem {
	return ReviewItem{
		Record: &records.AgentRecord{RecordID: id, AgentName: "mingming", Collection: k12.CollectionMistakes, Status: "pending"},
		Fields: k12.MistakeFields{Subject: "数学", Question: question, EntrySource: k12.MistakeEntryPhoto},
	}
}

// 只构造冻结来源值对象，不冒充模型执行或平台投递回执。
func reviewPaperSourceFixture(t *testing.T, id, page string, path []string, raw string) k12storage.ReviewPaperSource {
	t.Helper()
	q := RecognizedQuestion{
		ProblemID: "problem-" + id, AttemptID: "attempt-" + id, PageAssetID: "asset://mingming/" + page + ".jpg",
		SourceNumberPath: path, RawTranscription: raw, CanonicalMarkdown: raw, Question: raw,
		InputDigest: "input-" + id, ConfirmedVersion: 1, CanonicalVersion: 1,
	}
	return reviewPaperSourceForQuestion(t, id, page, q)
}

func reviewPaperSourceWithStatement(t *testing.T, source k12storage.ReviewPaperSource, statement string) k12storage.ReviewPaperSource {
	t.Helper()
	var frozen PhotoGradeItem
	if err := json.Unmarshal([]byte(source.ResultJSON), &frozen); err != nil {
		t.Fatal(err)
	}
	frozen.Recognized.Question, frozen.Recognized.CanonicalMarkdown = statement, statement
	updated := reviewPaperSourceForQuestion(t, source.RecordID, source.PageDigest, frozen.Recognized)
	updated.AgentName, updated.InputRevision = source.AgentName, source.InputRevision
	return updated
}

func reviewPaperSourceForQuestion(t *testing.T, id, page string, q RecognizedQuestion) k12storage.ReviewPaperSource {
	t.Helper()
	raw, err := json.Marshal(PhotoGradeItem{Recognized: q})
	if err != nil {
		t.Fatal(err)
	}
	return k12storage.ReviewPaperSource{
		AgentName: "mingming", RecordID: id, JobID: "job-" + id, ProblemID: q.ProblemID, AttemptID: q.AttemptID,
		InputRevision: 1, ConfirmedVersion: q.ConfirmedVersion, InputDigest: q.InputDigest,
		ResultJSON: string(raw), ResultDigest: modelInvocationDigest(raw), PageAssetID: q.PageAssetID, PageDigest: page,
	}
}

func TestReviewPaperProjectionDeduplicatesSourcesAndKeepsEveryRecord(t *testing.T) {
	queue := []ReviewItem{
		reviewPaperFixture("first", "已用4.8 TB，占总容量2/5。\n先求一份，再求总容量。"),
		reviewPaperFixture("second", "已用 $4.8$ TB，占总容量 $\\frac{2}{5}$。\n先求一份，再求总容量。"),
		reviewPaperFixture("bridge", "已用 4.8 TB，占总容量 2/5。先求一份，再求总容量。"),
	}
	sources := []k12storage.ReviewPaperSource{
		reviewPaperSourceFixture(t, "first", "page-one", []string{"3", "1"}, "先求一份，再求总容量。"),
		reviewPaperSourceFixture(t, "second", "page-two", []string{"3", "（1）"}, "先求一份，再求总容量。"),
		reviewPaperSourceFixture(t, "bridge", "page-one", []string{"3", "1"}, "先求一份，再求总容量。"),
		reviewPaperSourceFixture(t, "bridge", "page-two", []string{"3", "（1）"}, "先求一份，再求总容量。"),
	}
	for i, source := range sources {
		for _, item := range queue {
			if item.Record.RecordID == source.RecordID {
				sources[i] = reviewPaperSourceWithStatement(t, source, item.Fields.Question)
			}
		}
	}
	before, _ := json.Marshal(queue)
	projected := projectReviewPaper(queue, sources)
	if len(projected) != 1 || projected[0].Record.RecordID != "first" || !strings.Contains(projected[0].Statement, "4.8 TB") {
		t.Fatalf("same source did not retain the earliest representative: %+v", projected)
	}
	if !reflect.DeepEqual(projected[0].SourceRecordIDs, []string{"first", "second", "bridge"}) {
		t.Fatalf("source records lost: %v", projected[0].SourceRecordIDs)
	}
	after, _ := json.Marshal(queue)
	if string(before) != string(after) {
		t.Fatal("read-only projection changed original records")
	}
}

func TestReviewPaperProjectionKeepsDistinctTargetsConditionsAndUnits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*RecognizedQuestion, *k12storage.ReviewPaperSource)
	}{
		{"different child", func(q *RecognizedQuestion, _ *k12storage.ReviewPaperSource) { q.SourceNumberPath = []string{"3", "2"} }},
		{"different condition", func(q *RecognizedQuestion, _ *k12storage.ReviewPaperSource) {
			q.RawTranscription = "已用8.4 TB，求总容量。"
		}},
		{"different unit", func(q *RecognizedQuestion, _ *k12storage.ReviewPaperSource) {
			q.RawTranscription = "已用4.8 GB，求总容量。"
		}},
		{"different page", func(q *RecognizedQuestion, source *k12storage.ReviewPaperSource) {
			q.PageAssetID = "asset://mingming/other-page.jpg"
			source.PageDigest = "other-page"
		}},
		{"corrected canonical conditions", func(q *RecognizedQuestion, source *k12storage.ReviewPaperSource) {
			q.CanonicalMarkdown = "已用4.8 TB，占总容量3/5。"
			q.RawTranscription = q.CanonicalMarkdown
			q.CanonicalVersion, q.ConfirmedVersion = 2, 2
			source.InputRevision = 2
		}},
		{"missing page proof", func(_ *RecognizedQuestion, source *k12storage.ReviewPaperSource) { source.PageDigest = "" }},
		{"missing current target", func(q *RecognizedQuestion, _ *k12storage.ReviewPaperSource) { q.RawTranscription = "" }},
		{"other owner", func(_ *RecognizedQuestion, source *k12storage.ReviewPaperSource) { source.AgentName = "eval-agent" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := reviewPaperSourceFixture(t, "first", "same-page", []string{"3", "1"}, "已用4.8 TB，求总容量。")
			second := reviewPaperSourceFixture(t, "second", "same-page", []string{"3", "1"}, "已用4.8 TB，求总容量。")
			second = reviewPaperSourceWithStatement(t, second, "已用4.8 TB，求总容量。\n列式：________")
			var frozen PhotoGradeItem
			if err := json.Unmarshal([]byte(second.ResultJSON), &frozen); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&frozen.Recognized, &second)
			if tc.name == "different condition" || tc.name == "different unit" {
				frozen.Recognized.CanonicalMarkdown = frozen.Recognized.RawTranscription + "\n列式：________"
			}
			frozen.Recognized.Question = frozen.Recognized.CanonicalMarkdown
			updated := reviewPaperSourceForQuestion(t, "second", second.PageDigest, frozen.Recognized)
			updated.AgentName, updated.InputRevision = second.AgentName, second.InputRevision
			queue := []ReviewItem{reviewPaperFixture("first", "已用4.8 TB，求总容量。"), reviewPaperFixture("second", frozen.Recognized.CanonicalMarkdown)}
			if !reviewPaperRecordMatchesQuestion(queue[1], frozen.Recognized) {
				t.Fatal("fixture did not bind the changed complete question to its source")
			}
			if got := projectReviewPaper(queue, []k12storage.ReviewPaperSource{first, updated}); len(got) != 2 {
				t.Fatalf("distinct or unproven question was merged: %+v", got)
			}
		})
	}
	first := reviewPaperSourceFixture(t, "first", "same-page", []string{"3", "1"}, "已用4.8 TB，求总容量。")
	second := reviewPaperSourceFixture(t, "second", "same-page", []string{"3", "1"}, "已用4.8 TB，求总容量。")
	second = reviewPaperSourceWithStatement(t, second, "已用4.8 TB，求总容量。\n列式：________")
	second.ResultJSON += " "
	if got := projectReviewPaper([]ReviewItem{reviewPaperFixture("first", "已用4.8 TB，求总容量。"), reviewPaperFixture("second", "已用4.8 TB，求总容量。\n列式：________")}, []k12storage.ReviewPaperSource{first, second}); len(got) != 2 {
		t.Fatal("source with changed immutable bytes was trusted")
	}
	t.Run("manually changed record cannot borrow its earlier source", func(t *testing.T) {
		old := "已用4.8 TB，求总容量。"
		sources := []k12storage.ReviewPaperSource{
			reviewPaperSourceFixture(t, "first", "same-page", []string{"3", "1"}, old),
			reviewPaperSourceFixture(t, "second", "same-page", []string{"3", "1"}, old),
		}
		queue := []ReviewItem{reviewPaperFixture("first", old), reviewPaperFixture("second", "已用4.8 GB，求总容量。")}
		if got := projectReviewPaper(queue, sources); len(got) != 2 {
			t.Fatal("changed unit in the record borrowed an obsolete matching source")
		}
	})
	t.Run("same page slot requires the complete parent conditions", func(t *testing.T) {
		current := "已知（ ）份是2千米。"
		first := reviewPaperSourceFixture(t, "first", "same-page", nil, current)
		second := reviewPaperSourceFixture(t, "second", "same-page", nil, current)
		sources := []k12storage.ReviewPaperSource{first, second}
		parents := []string{"小明2/3小时走了2千米。", "小明1/3小时走了2千米。"}
		queue := make([]ReviewItem, 0, 2)
		for i, source := range sources {
			var frozen PhotoGradeItem
			if err := json.Unmarshal([]byte(source.ResultJSON), &frozen); err != nil {
				t.Fatal(err)
			}
			frozen.Recognized.ParentProblemID = "parent-" + source.RecordID
			frozen.Recognized.CanonicalMarkdown = parents[i] + current
			sources[i] = reviewPaperSourceForQuestion(t, source.RecordID, "same-page", frozen.Recognized)
			queue = append(queue, reviewPaperFixture(source.RecordID, parents[i]+current))
		}
		if reviewPaperSourceKey(queue[0], sources[0]) == "" || reviewPaperSourceKey(queue[1], sources[1]) == "" {
			t.Fatal("complete frozen parent and single-slot sources were not eligible")
		}
		if got := projectReviewPaper(queue, sources); len(got) != 2 {
			t.Fatal("same current slot hid distinct complete parent conditions")
		}
	})
	t.Run("same printed number still requires the adopted parent conditions", func(t *testing.T) {
		current := "先求一份，再求总容量。"
		queue := []ReviewItem{
			reviewPaperFixture("first", "已用4.8 TB，占总容量2/5。"+current),
			reviewPaperFixture("second", "已用8.4 TB，占总容量2/5。"+current),
		}
		var sources []k12storage.ReviewPaperSource
		for _, item := range queue {
			source := reviewPaperSourceFixture(t, item.Record.RecordID, "same-page", []string{"3", "1"}, current)
			source = reviewPaperSourceWithStatement(t, source, item.Fields.Question)
			if reviewPaperSourceKey(item, source) == "" {
				t.Fatal("complete adopted source was not eligible")
			}
			sources = append(sources, source)
		}
		if got := projectReviewPaper(queue, sources); len(got) != 2 {
			t.Fatal("printed numbering hid different adopted parent conditions")
		}
	})
	t.Run("equal photo text does not prove equal diagram resources", func(t *testing.T) {
		question := "看图填空：（ ）厘米。"
		queue := []ReviewItem{reviewPaperFixture("first", question), reviewPaperFixture("second", question)}
		sources := []k12storage.ReviewPaperSource{
			reviewPaperSourceFixture(t, "first", "diagram-one", []string{"1"}, question),
			reviewPaperSourceFixture(t, "second", "diagram-two", []string{"1"}, question),
		}
		if got := projectReviewPaper(queue, sources); len(got) != 2 {
			t.Fatal("different original diagrams were merged by equal text")
		}
		sources[1] = reviewPaperSourceFixture(t, "second", "diagram-one", []string{"2"}, question)
		if got := projectReviewPaper(queue, sources); len(got) != 2 {
			t.Fatal("different printed subquestions on one page were merged by equal text")
		}
		if got := projectReviewPaper(queue, nil); len(got) != 2 {
			t.Fatal("missing photo source identity was treated as equal diagrams")
		}
		for i := range queue {
			queue[i].Fields.EntrySource = ""
		}
		if got := projectReviewPaper(queue, nil); len(got) != 2 {
			t.Fatal("legacy missing source metadata was treated as proof of equal diagrams")
		}
	})
}

func TestReviewPaperReadProjectionPreservesRecordsAndFrozenPDF(t *testing.T) {
	d, store := newPipeline(t, fakeSolver{}, fakeGrader{}, nil)
	ctx := context.Background()
	var before []*records.AgentRecord
	questions := []string{"已用4.8 TB，求总容量。", "已用4.8 TB，求总容量。", "已用4.8 GB，求总容量。", "Read this condition in English."}
	for index, question := range questions {
		fields := k12.MistakeFields{Subject: "数学", Question: question, EntrySource: k12.MistakeEntryVerified, CanonicalAnswer: "DO_NOT_EXPORT_ANSWER"}
		raw, _ := json.Marshal(fields)
		due := int64(100 + index)
		record := &records.AgentRecord{AgentName: "mingming", Collection: k12.CollectionMistakes, SourceSession: string(rune('a' + index)), Fields: string(raw), DueAt: &due}
		if _, err := store.Put(ctx, record); err != nil {
			t.Fatal(err)
		}
		stored, err := store.Get(ctx, record.RecordID)
		if err != nil {
			t.Fatal(err)
		}
		before = append(before, stored)
	}
	// 这里只核对已冻结二进制不被读侧重写，实际PDF渲染由真实HTTP验收覆盖。
	payload := []byte("%PDF-1.7\nfrozen historical bytes\n%%EOF")
	digest := sha256.Sum256(payload)
	artifact := k12.PrintArtifact{ArtifactID: "historical-sheet", AgentName: "mingming", SourceKind: k12.PrintSourcePracticeQuestion, SourceRef: "historical", Title: "旧错题卷", CanonicalMarkdown: "旧卷题目", SourceDigest: strings.Repeat("a", 64), CreatedAt: 100}
	render := k12.PrintArtifactRender{ArtifactID: artifact.ArtifactID, Format: "pdf", RenderContractVersion: "historical-v1", ContentType: "application/pdf", ByteDigest: hex.EncodeToString(digest[:]), ByteSize: int64(len(payload)), Payload: payload, CreatedAt: 100}
	if _, _, _, err := store.FreezePrintArtifact(ctx, artifact, render); err != nil {
		t.Fatal(err)
	}
	markdown, err := d.MistakeSheetMarkdown(ctx, "mingming")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(markdown, "\n\n（　　）\n\n") != 3 || !strings.Contains(markdown, "共3题") || strings.Contains(markdown, "DO_NOT_EXPORT_ANSWER") {
		t.Fatalf("wrong unique exercise projection: %s", markdown)
	}
	track, _, err := d.weeklyDueTrack(ctx, "mingming")
	if err != nil || len(track.Items) != 3 || track.Items[0].SourceRef != before[0].RecordID {
		t.Fatalf("weekly plan did not share the same original question projection: %+v err=%v", track, err)
	}
	for _, original := range before {
		after, err := store.Get(ctx, original.RecordID)
		if err != nil || !reflect.DeepEqual(original, after) {
			t.Fatalf("read projection changed record state or fields: id=%s err=%v", original.RecordID, err)
		}
	}
	afterArtifact, err := store.GetPrintArtifact(ctx, "mingming", artifact.ArtifactID)
	if err != nil || !reflect.DeepEqual(artifact, afterArtifact) {
		t.Fatalf("historical artifact changed: %+v err=%v", afterArtifact, err)
	}
	afterRender, err := store.GetPrintArtifactRender(ctx, "mingming", artifact.ArtifactID)
	if err != nil || !reflect.DeepEqual(render, afterRender) {
		t.Fatalf("historical PDF changed: %+v err=%v", afterRender, err)
	}
}
