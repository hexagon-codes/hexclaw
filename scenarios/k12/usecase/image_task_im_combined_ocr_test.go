package usecase

import (
	"context"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 外部响应可控，分类正文、作品、调用回执与重启重放使用真实 SQLite。
type imCombinedOCRClassifier struct {
	*imageTaskClassifierStub
	includeWritingOCR []bool
}

func (s *imCombinedOCRClassifier) ClassifyImageTask(ctx context.Context, in ImageTaskClassificationInput) (ImageTaskClassification, error) {
	s.includeWritingOCR = append(s.includeWritingOCR, in.IncludeWritingOCR)
	return s.imageTaskClassifierStub.ClassifyImageTask(ctx, in)
}

func TestImageTaskIMCombinedOCRAutomaticallyPromotesFrozenBodyOnce(t *testing.T) {
	ctx := context.Background()
	text := "我的爸爸\n爸爸每天陪我读书。我喜欢和爸爸一起观察公园里的小鸟。"
	classifier := &imageTaskClassifierStub{result: ImageTaskClassification{
		Intent: k12.ImageTaskIntentWriting, IntentEvidence: []string{"连续手写作文"}, Confidence: .99,
		WritingOCR: &ImageTaskWritingOCRResult{Raw: text, CanonicalContent: text, Confidence: .99},
	}}
	c, grading := newImageTaskCoordinatorForTest(t, classifier)
	capture := &imCombinedOCRClassifier{imageTaskClassifierStub: classifier}
	c.Classifier = capture
	ocr := c.WritingOCR.(*imageTaskOCRStub)
	solver := c.WorkFeedback.(*Deps).Solver.(*imageTaskFeedbackSolver)
	in := testCreateImageTaskInput()
	in.SourceKind = k12.ImageTaskSourceIM
	view, created, err := createAndRunImageTask(t, c, in)
	if err != nil || !created {
		t.Fatalf("combined IM preparation: %+v %v", view, err)
	}
	_, state := assertCurrentCreativeIntakePromotion(t, c, view.Creative)
	if view.Dispatch.CreativeEntry != nil || view.Creative.EntryKind != k12.CreativeWorkEntryAuto ||
		view.Creative.PromotionPolicy != k12.CreativeWorkPromotionAutomatic ||
		state.Initial.Source.ContentMarkdown != text || view.Creative.OCREvidence.Raw != text ||
		view.Creative.OCREvidence.SourceInvocationID != view.Dispatch.ClassificationInvocationID {
		t.Fatalf("IM automatic archive or frozen evidence changed: %+v", view)
	}
	if len(capture.includeWritingOCR) != 1 || !capture.includeWritingOCR[0] || classifier.calls != 1 || ocr.calls != 0 || solver.calls != 1 || grading.starts != 0 {
		t.Fatalf("unexpected calls: combined=%v classify=%d OCR=%d feedback=%d grading=%d", capture.includeWritingOCR, classifier.calls, ocr.calls, solver.calls, grading.starts)
	}
	var count int
	if err = c.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_image_task_invocations WHERE intake_id=? AND operation='writing_ocr'`, view.Creative.IntakeID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("full OCR receipt count=%d err=%v", count, err)
	}
	c = reopenWritingCoordinator(t, c)
	replay, again, err := c.Create(ctx, in)
	if err != nil || again || replay.Creative.PromotedWorkID != view.Creative.PromotedWorkID || replay.Creative.PromotedGenerationID != view.Creative.PromotedGenerationID {
		t.Fatalf("restart duplicated archive: %+v created=%v err=%v", replay, again, err)
	}
	if _, err = c.Run(ctx, in.AgentName, replay.Dispatch.DispatchID); err != nil || classifier.calls != 1 || ocr.calls != 0 || solver.calls != 1 {
		t.Fatalf("restart repeated a successful call: err=%v classify=%d OCR=%d feedback=%d", err, classifier.calls, ocr.calls, solver.calls)
	}
}

func TestImageTaskIMCombinedOCRUsesOnlyExistingRiskReview(t *testing.T) {
	content := "春天到了。〔模糊片段〕小鸟在唱歌。"
	risks := []k12.CreativeWorkIntakeOCRRisk{{SegmentID: "line-2", RawText: "〔模糊片段〕", Reasons: []string{"illegible"}}}
	c, ocr, solver := newWritingReviewCoordinator(t, content, risks)
	classifier := c.Classifier.(*imageTaskClassifierStub)
	classifier.result.WritingOCR = &ImageTaskWritingOCRResult{Raw: content, CanonicalContent: content, Confidence: .7, RiskSegments: risks}
	ocr.review = []k12.CreativeWorkOCRReviewSegment{{SegmentID: "line-2", Readable: true, Text: "柳树发芽了。", VisualEvidence: "该行字形清晰"}}
	in := testCreateImageTaskInput()
	in.SourceKind = k12.ImageTaskSourceIM
	view, _, err := createAndRunImageTask(t, c, in)
	if err != nil || view.Creative == nil || view.Creative.OCREvidence == nil ||
		view.Creative.OCREvidence.Raw != content || view.Creative.OCREvidence.CanonicalContent != "春天到了。柳树发芽了。小鸟在唱歌。" ||
		classifier.calls != 1 || ocr.calls != 0 || ocr.reviewCalls != 1 || len(ocr.reviewRisks) != 1 || ocr.reviewRisks[0].SegmentID != "line-2" {
		t.Fatalf("combined evidence bypassed local review or repeated full OCR: %+v calls=%d/%d/%d err=%v", view, classifier.calls, ocr.calls, ocr.reviewCalls, err)
	}
	view, err = c.Run(context.Background(), in.AgentName, view.Dispatch.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	_, state := assertCurrentCreativeIntakePromotion(t, c, view.Creative)
	if state.Initial.Source.ContentMarkdown != "春天到了。柳树发芽了。小鸟在唱歌。" || solver.calls != 1 || ocr.calls != 0 || ocr.reviewCalls != 1 {
		t.Fatalf("reviewed body did not reach existing automatic feedback: %+v calls=%d/%d/%d", state, solver.calls, ocr.calls, ocr.reviewCalls)
	}
}

func TestImageTaskIMCombinedOCRUnknownReplayNeverResends(t *testing.T) {
	for _, stage := range []string{"classification", "writing_review"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			content := "春天到了。〔模糊片段〕小鸟在唱歌。"
			risks := []k12.CreativeWorkIntakeOCRRisk{{SegmentID: "line-2", RawText: "〔模糊片段〕", Reasons: []string{"illegible"}}}
			c, ocr, solver := newWritingReviewCoordinator(t, content, risks)
			classifier := c.Classifier.(*imageTaskClassifierStub)
			classifier.result.WritingOCR = &ImageTaskWritingOCRResult{Raw: content, CanonicalContent: content, Confidence: .7, RiskSegments: risks}
			invocationID := "classification-1"
			expectedReviews := 0
			if stage == "classification" {
				classifier.err = context.DeadlineExceeded
			} else {
				ocr.reviewErr = context.DeadlineExceeded
				invocationID = "writing-review-1"
				expectedReviews = 1
			}
			in := testCreateImageTaskInput()
			in.SourceKind = k12.ImageTaskSourceIM
			view, _, runErr := createAndRunImageTask(t, c, in)
			if runErr == nil {
				t.Fatal("unknown provider result must stop the task")
			}
			before, err := c.Records.GetImageTaskInvocation(ctx, in.AgentName, invocationID)
			if err != nil || before.Status != k12.ImageTaskInvocationOutcomeUnknown {
				t.Fatalf("unknown receipt missing: %+v %v", before, err)
			}
			c = reopenWritingCoordinator(t, c)
			classifier.err, ocr.reviewErr = nil, nil
			replay, created, err := c.Create(ctx, in)
			if err != nil || created || replay.Dispatch.DispatchID != view.Dispatch.DispatchID {
				t.Fatalf("unknown replay created another task: %+v %v", replay, err)
			}
			for i := 0; i < 2; i++ {
				if _, err = c.Run(ctx, in.AgentName, view.Dispatch.DispatchID); err != nil {
					t.Fatal(err)
				}
			}
			after, err := c.Records.GetImageTaskInvocation(ctx, in.AgentName, invocationID)
			if err != nil || after.Status != before.Status || after.Attempt != before.Attempt || after.ResultDigest != before.ResultDigest ||
				classifier.calls != 1 || ocr.calls != 0 || ocr.reviewCalls != expectedReviews || solver.calls != 0 {
				t.Fatalf("unknown replay changed evidence or resent: %+v calls=%d/%d/%d/%d err=%v", after, classifier.calls, ocr.calls, ocr.reviewCalls, solver.calls, err)
			}
			latest, err := c.Get(ctx, in.AgentName, view.Dispatch.DispatchID)
			if err != nil || latest.Dispatch.RetrySafe || (latest.Creative != nil && latest.Creative.PromotedWorkID != "") {
				t.Fatalf("unknown result became retryable or archived: %+v %v", latest, err)
			}
		})
	}
}

func TestImageTaskIMCombinedOCRLeavesMathRoutingUnchanged(t *testing.T) {
	for _, source := range []k12.ImageTaskSourceKind{k12.ImageTaskSourceIM, k12.ImageTaskSourceDesktop} {
		t.Run(string(source), func(t *testing.T) {
			classifier := &imageTaskClassifierStub{result: ImageTaskClassification{
				Intent: k12.ImageTaskIntentCompletedHomework, IntentEvidence: []string{"有学生作答的数学题页"}, Confidence: .99,
				WritingOCR: &ImageTaskWritingOCRResult{Raw: "不得写入数学题的作文正文", CanonicalContent: "不得写入数学题的作文正文", Confidence: .99},
			}}
			c, grading := newImageTaskCoordinatorForTest(t, classifier)
			capture := &imCombinedOCRClassifier{imageTaskClassifierStub: classifier}
			c.Classifier = capture
			gate := &imageTaskIMRoutingGateStub{allow: true}
			c.IMCompletedHomeworkRoutingGate = gate
			in := testCreateImageTaskInput()
			in.SourceKind = source
			view, _, err := createAndRunImageTask(t, c, in)
			if err != nil || view.Dispatch.TaskIntent != k12.ImageTaskIntentCompletedHomework || view.Homework == nil || view.Creative != nil ||
				grading.starts != 1 || grading.async != 1 || grading.input.Photo.TaskIntent != PhotoTaskCompletedHomework ||
				c.WritingOCR.(*imageTaskOCRStub).calls != 0 || c.WorkFeedback.(*Deps).Solver.(*imageTaskFeedbackSolver).calls != 0 {
				t.Fatalf("math classification entered creative flow: %+v grading=%+v err=%v", view, grading, err)
			}
			if len(capture.includeWritingOCR) != 1 || capture.includeWritingOCR[0] != (source == k12.ImageTaskSourceIM) ||
				grading.input.ModelSnapshot.Provider != in.RouteRequest.Provider || grading.input.ModelSnapshot.Model != in.RouteRequest.Model {
				t.Fatalf("classification or grading route changed: combined=%v model=%+v", capture.includeWritingOCR, grading.input.ModelSnapshot)
			}
		})
	}
}
