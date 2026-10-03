package usecase

import (
	"context"
	"fmt"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 来源选择使用真实 SQLite 中的历史事实；模型判定作为已收敛输入，事务回放另行验证。
func TestGradingReviewSourceExactHistory(t *testing.T) {
	tests := []struct {
		name       string
		agent      string
		subject    string
		history    string
		question   string
		wantReview bool
	}{
		{"same child and subject", "mingming", "数学", "3+2=", "3+2=", true},
		{"full width basic operators", "mingming", "数学", "(3+2)-1=", "（3＋2）－1＝", true},
		{"other child is not a source", "eval-agent", "数学", "3+2=", "3+2=", false},
		{"other subject is not a source", "mingming", "语文", "3+2=", "3+2=", false},
		{"same answer is not an exact question", "mingming", "数学", "3+2=", "4+1=", false},
		{"different units remain different", "mingming", "数学", "3米+2米=", "3厘米+2厘米=", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _ := newPipeline(t, panicSolver{}, panicGrader{}, nil)
			source := putGradingReviewSource(t, d, tt.agent, tt.subject, tt.history)
			effects, err := d.gradingAssessmentEffects(context.Background(), GradeRequest{
				AgentName: "mingming", Subject: "数学", Problem: tt.question, StudentAnswer: "5",
			}, GradeResult{Outcome: GradeOutcome{Verdict: VerdictAgree}})
			if err != nil {
				t.Fatal(err)
			}
			if effects.Mistake != nil {
				t.Fatal("a correct answer must not create a mistake")
			}
			if !tt.wantReview {
				if effects.Review != nil {
					t.Fatalf("an unrelated historical record was selected: %+v", effects.Review)
				}
				return
			}
			if effects.Review == nil || effects.Review.RecordID != source.RecordID ||
				effects.Review.ExpectedVersion != source.Version {
				t.Fatalf("the exact historical source must be selected with its stored version: got=%+v source=%+v", effects.Review, source)
			}
		})
	}
}

func TestGradingReviewSourcePublishedPractice(t *testing.T) {
	tests := []struct {
		name          string
		firstQuestion string
		paperQuestion string
		ocrQuestion   string
		answer        string
		secondSource  bool
		secondPaper   bool
		wantReview    bool
	}{
		{name: "published variant links to its original mistake", firstQuestion: "3+1=", paperQuestion: "计算：3.48＋1.52＝", ocrQuestion: `\(3.48+1.52=\)`, answer: "5", wantReview: true},
		{name: "same source across two published sheets is still unique", firstQuestion: "3+1=", secondPaper: true, wantReview: true},
		{name: "different source records are ambiguous", firstQuestion: "3+1=", secondSource: true, secondPaper: true},
		{name: "exact original and a different practice source are ambiguous", firstQuestion: "5+1=", secondSource: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.paperQuestion == "" {
				tt.paperQuestion, tt.ocrQuestion, tt.answer = "5+1=", "5＋1＝", "6"
			}
			d, _ := newPipeline(t, panicSolver{}, panicGrader{}, nil)
			ctx := context.Background()
			first := putGradingReviewSource(t, d, "mingming", "数学", tt.firstQuestion)
			paperSource := first
			if tt.secondSource {
				paperSource = putGradingReviewSource(t, d, "mingming", "数学", "4+3=")
			}
			publishedSources := []*records.AgentRecord{paperSource}
			if tt.secondPaper {
				publishedSources = append(publishedSources, first)
			}
			for i, source := range publishedSources {
				setID, created, err := d.CreatePracticeSet(ctx, "mingming", "practice-history", k12.PracticeSetFields{
					SourceKind: k12.PracticeSourceSingleVariant, Title: fmt.Sprintf("历史练习卷 %d", i+1),
					Items: []k12.PracticeItem{{
						ItemID: "variant", SourceProblemID: source.RecordID, Subject: "数学",
						QuestionMarkdown: tt.paperQuestion, ExpectedAnswerMarkdown: tt.answer,
						VerificationStatus: k12.PracticeItemVerified, VerificationEvidence: "6-1=5",
					}},
				})
				if err != nil || !created {
					t.Fatalf("create historical practice: created=%v err=%v", created, err)
				}
				published, skipped, err := d.FinalizeBasket(ctx, "mingming", setID, "print")
				if err != nil || skipped != 0 {
					t.Fatalf("publish historical practice: skipped=%d err=%v", skipped, err)
				}
				if published.Fields.FinalizedAt <= 0 || published.Fields.Items[0].PaperSeq != 1 {
					t.Fatalf("the practice must have a durable publication and paper position: %+v", published.Fields)
				}
			}
			effects, err := d.gradingAssessmentEffects(ctx, GradeRequest{
				AgentName: "mingming", Subject: "数学", Problem: tt.ocrQuestion, StudentAnswer: tt.answer,
			}, GradeResult{Outcome: GradeOutcome{Verdict: VerdictAgree}})
			if err != nil {
				t.Fatal(err)
			}
			if !tt.wantReview {
				if effects.Review != nil || effects.Mistake != nil {
					t.Fatalf("ambiguous sources must not create a review or mistake effect: %+v", effects)
				}
				return
			}
			if effects.Review == nil || effects.Review.RecordID != first.RecordID || effects.Mistake != nil {
				t.Fatalf("published practice must update only its unique original source: %+v", effects)
			}
		})
	}
}

func TestGradingReviewSourceRejectsUnpublishedOrUnusablePractice(t *testing.T) {
	tests := []struct {
		name         string
		agent        string
		finalizedAt  int64
		verification string
		paperSeq     int
		status       string
	}{
		{"unpublished draft", "mingming", 0, k12.PracticeItemVerified, 1, k12.PracticeStatusDraft},
		{"unverified item", "mingming", 1000, k12.PracticeItemPending, 1, k12.PracticeStatusAssigned},
		{"verified item not on the paper", "mingming", 1000, k12.PracticeItemVerified, 0, k12.PracticeStatusAssigned},
		{"cancelled paper", "mingming", 1000, k12.PracticeItemVerified, 1, k12.PracticeStatusCancelled},
		{"other child's paper", "eval-agent", 1000, k12.PracticeItemVerified, 1, k12.PracticeStatusAssigned},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _ := newPipeline(t, panicSolver{}, panicGrader{}, nil)
			ctx := context.Background()
			source := putGradingReviewSource(t, d, "mingming", "数学", "3+1=")
			// 已有记录也可能缺少发布或卷面信息，不能仅凭题面相同接受来源。
			paper, err := k12.NewPracticeSetRecord(tt.agent, "practice-history", k12.PracticeSetFields{
				SourceKind: k12.PracticeSourceSingleVariant, Title: "历史练习记录", FinalizedAt: tt.finalizedAt,
				Items: []k12.PracticeItem{{
					ItemID: "variant", SourceProblemID: source.RecordID, Subject: "数学",
					QuestionMarkdown: "5+1=", ExpectedAnswerMarkdown: "6",
					VerificationStatus: tt.verification, VerificationEvidence: "6-1=5", PaperSeq: tt.paperSeq,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			paper.Status = tt.status
			if created, err := d.Records.Put(ctx, paper); err != nil || !created {
				t.Fatalf("persist historical practice: created=%v err=%v", created, err)
			}
			effects, err := d.gradingAssessmentEffects(ctx, GradeRequest{
				AgentName: "mingming", Subject: "数学", Problem: "5+1=", StudentAnswer: "6",
			}, GradeResult{Outcome: GradeOutcome{Verdict: VerdictAgree}})
			if err != nil {
				t.Fatal(err)
			}
			if effects.Review != nil || effects.Mistake != nil {
				t.Fatalf("unusable practice must not update the original mistake: %+v", effects)
			}
		})
	}
}

func TestGradingReviewSourceRequiresAnswerAndConclusiveAssessment(t *testing.T) {
	tests := []struct {
		name    string
		answer  string
		verdict Verdict
	}{
		{"unanswered", "", VerdictAgree},
		{"whitespace only answer", " \n\t", VerdictAgree},
		{"unrecognizable answer", "?", VerdictUnverifiable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _ := newPipeline(t, panicSolver{}, panicGrader{}, nil)
			putGradingReviewSource(t, d, "mingming", "数学", "3+2=")
			effects, err := d.gradingAssessmentEffects(context.Background(), GradeRequest{
				AgentName: "mingming", Subject: "数学", Problem: "3+2=", StudentAnswer: tt.answer,
			}, GradeResult{Outcome: GradeOutcome{Verdict: tt.verdict}})
			if err != nil {
				t.Fatal(err)
			}
			if effects.Review != nil || effects.Mistake != nil {
				t.Fatalf("a missing or unrecognizable answer must not update learning records: %+v", effects)
			}
		})
	}
}

func putGradingReviewSource(t *testing.T, d Deps, agent, subject, question string) *records.AgentRecord {
	t.Helper()
	ctx := context.Background()
	source, err := k12.NewMistakeRecord(agent, "mistake-history", k12.MistakeFields{
		GradeTerm: "五年级上", Subject: subject, Question: question,
		KnowledgePoint: "整数加减法", ErrorCause: "计算失误", WrongProcess: "原计算错误",
		EntrySource: k12.MistakeEntryPhoto,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created, err := d.Records.Put(ctx, source); err != nil || !created {
		t.Fatalf("persist historical mistake: created=%v err=%v", created, err)
	}
	stored, err := d.Records.Get(ctx, source.RecordID)
	if err != nil || stored == nil {
		t.Fatalf("read historical mistake: record=%+v err=%v", stored, err)
	}
	return stored
}
