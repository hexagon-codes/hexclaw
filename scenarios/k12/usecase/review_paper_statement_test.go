package usecase

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func TestReviewPaperStatementRestoresCurrentAndSharedWithoutDuplication(t *testing.T) {
	for _, current := range []string{
		"（1）先求一份是多少，再求总容量是多少。\n列式：________",
		"（2）根据数量关系：（ ）＝（ ）×2/5。\n列式：________",
	} {
		t.Run(current[:len("（1）")], func(t *testing.T) {
			shared := "一项容量已用4.8 TB，占总容量的2/5。\n\n图示共5份，已用2份。"
			q := RecognizedQuestion{ProblemID: "problem-statement", AttemptID: "attempt-statement", PageAssetID: "asset://mingming/page.jpg", InputDigest: "input-statement", ConfirmedVersion: 1, CanonicalVersion: 1,
				RawTranscription: current, Question: shared + "\n\n" + current, CanonicalMarkdown: shared + "\n\n" + current, SourceNumberPath: []string{"3", "1"}}
			item := reviewPaperFixture("statement", current+"\n\n"+q.CanonicalMarkdown)
			source := reviewPaperSourceForQuestion(t, "statement", "page", q)
			before, _ := json.Marshal(item)
			want := current + "\n\n" + shared
			if got := restoreReviewPaperStatement(item, []k12storage.ReviewPaperSource{source}); got != want {
				t.Fatalf("current task or complete shared facts duplicated/lost:\ngot %q\nwant %q", got, want)
			}
			after, _ := json.Marshal(item)
			if string(before) != string(after) {
				t.Fatal("statement restoration changed the original mistake")
			}
			changed := item
			changed.Fields.Question = strings.Replace(item.Fields.Question, "4.8 TB", "8.4 TB", 1)
			if reviewPaperRecordMatchesQuestion(changed, q) || restoreReviewPaperStatement(changed, []k12storage.ReviewPaperSource{source}) != changed.Fields.Question {
				t.Fatal("manually changed complete conditions borrowed an old source")
			}
			source.ResultDigest = "not-the-frozen-digest"
			if got := restoreReviewPaperStatement(item, []k12storage.ReviewPaperSource{source}); got != item.Fields.Question {
				t.Fatal("unverified source replaced the original statement")
			}
		})
	}
}

func TestReviewPaperStatementNormalizesOnlyProvenMaterialParentNumber(t *testing.T) {
	condition := "已用占总容量的2/5，已用4.8 TB。"
	instruction := "用不同的方法解决问题。"
	q := RecognizedQuestion{ProblemID: "problem-parent", AttemptID: "attempt-parent", PageAssetID: "asset://mingming/page.jpg", InputDigest: "input-parent", ConfirmedVersion: 1, CanonicalVersion: 1,
		RawTranscription: "先求一份，再求总量。列式：", Question: "先求一份，再求总量。列式：", CanonicalMarkdown: "先求一份，再求总量。列式：", SourceNumberPath: []string{"3", "（1）"},
		ImageSourceContext: &k12.RecognitionImageSourceContextV1{Materials: []k12.RecognitionImageScopeDependencyV1{{Text: "3. " + instruction + "\n" + condition}, {Text: instruction + "\n" + condition}, {Text: "（2）根据数量关系填空。列式："}}}}
	problem, _ := photoQuestionProblemTexts(q)
	item := reviewPaperFixture("parent", problem)
	source := reviewPaperSourceForQuestion(t, "parent", "page", q)
	got := restoreReviewPaperStatement(item, []k12storage.ReviewPaperSource{source})
	if strings.Count(got, instruction) != 1 || strings.Count(got, condition) != 1 || !strings.Contains(got, "（2）根据数量关系填空。列式：") {
		t.Fatalf("proven outer number prevented compaction or a different child was dropped: %q", got)
	}
	if strings.Contains(got, "3. ") {
		t.Fatal("proven outer parent label remained as a duplicate material scope")
	}
	q.SourceNumberPath = nil
	source = reviewPaperSourceForQuestion(t, "parent", "page", q)
	got = restoreReviewPaperStatement(item, []k12storage.ReviewPaperSource{source})
	if !strings.Contains(got, "3. ") {
		t.Fatal("unproven parent label was removed")
	}
}

func TestReviewPaperStatementDoesNotRestoreUnadoptedRawConditions(t *testing.T) {
	adopted := "已用4.8 TB，占总容量的2/5，求总容量。"
	q := RecognizedQuestion{ProblemID: "problem-conflict", AttemptID: "attempt-conflict", PageAssetID: "asset://mingming/page.jpg", InputDigest: "input-conflict", ConfirmedVersion: 1, CanonicalVersion: 1,
		RawTranscription: "已用8.4 TB，占总容量的2/5，求总容量。", Question: adopted, CanonicalMarkdown: adopted, SourceNumberPath: []string{"3", "1"}}
	item := reviewPaperFixture("conflict", adopted)
	source := reviewPaperSourceForQuestion(t, "conflict", "page", q)
	if reviewPaperRecordMatchesQuestion(item, q) {
		t.Fatal("unadopted raw condition was accepted as the current statement")
	}
	if got := restoreReviewPaperStatement(item, []k12storage.ReviewPaperSource{source}); got != adopted {
		t.Fatalf("restoration added an unadopted raw condition: %q", got)
	}
	q.RawTranscription, q.Question, q.CanonicalMarkdown = adopted, adopted, "已用4.8 GB，占总容量的2/5，求总容量。"
	source = reviewPaperSourceForQuestion(t, "conflict", "page", q)
	if got := restoreReviewPaperStatement(item, []k12storage.ReviewPaperSource{source}); got != adopted {
		t.Fatal("restoration added an unadopted canonical unit")
	}
}

func TestReviewPaperStatementUsesOnlyVerifiedSamePageParentHeading(t *testing.T) {
	current := "已知（ ）份是2千米。"
	parent := "1. 小明2/3小时走了2千米。"
	q := RecognizedQuestion{ProblemID: "problem-heading", ParentProblemID: "parent-heading", AttemptID: "attempt-heading", PageAssetID: "asset://mingming/page.jpg", InputDigest: "input-heading", ConfirmedVersion: 1, CanonicalVersion: 1,
		RawTranscription: current, Question: parent + "\n\n" + current, CanonicalMarkdown: parent + "\n\n" + current}
	item := reviewPaperFixture("heading", current+"\n\n"+q.Question)
	source := reviewPaperSourceForQuestion(t, "heading", "page", q)
	source.ParentProblemID, source.ParentPageAssetID, source.ParentKind = q.ParentProblemID, q.PageAssetID, "compound_parent"
	source.ParentCanonicalVersion, source.ParentSourceNumberPathJSON, source.ParentStemRaw = 1, `["3","1"]`, parent
	got := restoreReviewPaperStatement(item, []k12storage.ReviewPaperSource{source})
	if got != current+"\n\n小明2/3小时走了2千米。" {
		t.Fatalf("verified parent heading did not remove only its repeated current item: %q", got)
	}
	for _, mutate := range []func(*k12storage.ReviewPaperSource){
		func(s *k12storage.ReviewPaperSource) { s.ParentProblemID = "other-parent" },
		func(s *k12storage.ReviewPaperSource) { s.ParentPageAssetID = "other-page" },
		func(s *k12storage.ReviewPaperSource) { s.ParentCanonicalVersion = 2 },
		func(s *k12storage.ReviewPaperSource) { s.ParentStemRaw = "1. 另一道不同条件的问题。" },
	} {
		changed := source
		mutate(&changed)
		if got := restoreReviewPaperStatement(item, []k12storage.ReviewPaperSource{changed}); !strings.Contains(got, parent) {
			t.Fatal("unverified parent heading changed the adopted source")
		}
	}
}
