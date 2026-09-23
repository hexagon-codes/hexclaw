package usecase

import (
	"context"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 模型仅提供可控响应；分类回执、草稿、作品与重复提交都经过真实 SQLite。
func autoCreativeInput() CreateImageTaskInput {
	in := testCreateImageTaskInput()
	in.CreativeEntry = &k12.ImageTaskCreativeEntry{Kind: k12.CreativeWorkEntryNewWork, TaskIntent: k12.ImageTaskIntentUnknown}
	return in
}

func TestAutoCreativeEntryWaitsForSaveAndReplaysOnce(t *testing.T) {
	for _, intent := range []k12.ImageTaskIntent{k12.ImageTaskIntentWriting, k12.ImageTaskIntentArtwork} {
		t.Run(string(intent), func(t *testing.T) {
			ctx := context.Background()
			classifier := &imageTaskClassifierStub{result: ImageTaskClassification{Intent: intent, IntentEvidence: []string{"visible work"}, Confidence: .99}}
			c, grading := newImageTaskCoordinatorForTest(t, classifier)
			solver := c.WorkFeedback.(*Deps).Solver.(*imageTaskFeedbackSolver)
			in := autoCreativeInput()
			view, created, err := createAndRunImageTask(t, c, in)
			if err != nil || !created {
				t.Fatalf("prepare: %+v %v", view, err)
			}
			if view.Creative == nil || view.Creative.Status != k12.CreativeWorkIntakeReady || view.Creative.PromotedWorkID != "" || solver.calls != 0 || grading.starts != 0 {
				t.Fatalf("draft archived or not ready: %+v", view)
			}
			if view.Dispatch.RoutingProvenance != k12.ImageTaskRoutingModelClassified || view.Dispatch.CreativeEntry == nil || view.Dispatch.CreativeEntry.TaskIntent != k12.ImageTaskIntentUnknown {
				t.Fatalf("classification identity lost: %+v", view.Dispatch)
			}
			receipt, err := c.Records.GetImageTaskInvocation(ctx, in.AgentName, view.Dispatch.ClassificationInvocationID)
			if err != nil || string(receipt.Status) != "succeeded" {
				t.Fatalf("classification receipt: %+v %v", receipt, err)
			}
			c = reopenWritingCoordinator(t, c)
			replay, again, err := c.Create(ctx, in)
			if err != nil || again || replay.Dispatch.DispatchID != view.Dispatch.DispatchID || replay.Dispatch.CreativeEntry == nil {
				t.Fatalf("restart create replay: %+v %v %v", replay, again, err)
			}
			replay, err = c.Run(ctx, in.AgentName, replay.Dispatch.DispatchID)
			if err != nil || replay.Creative.PromotedWorkID != "" || classifier.calls != 1 {
				t.Fatalf("restart re-executed classification or saved work: %+v %v", replay, err)
			}
			save := ConfirmImageTaskInput{AgentName: in.AgentName, DispatchID: view.Dispatch.DispatchID, ExpectedVersion: view.Dispatch.Version, Creative: &ConfirmCreativeImageTaskInput{Action: CreativeImageTaskActionCommit}}
			saved, err := c.Confirm(ctx, save)
			if err != nil {
				t.Fatal(err)
			}
			assertCurrentCreativeIntakePromotion(t, c, saved.Creative)
			repeated, err := c.Confirm(ctx, save)
			if err != nil || repeated.Creative.PromotedWorkID != saved.Creative.PromotedWorkID || repeated.Creative.PromotedGenerationID != saved.Creative.PromotedGenerationID {
				t.Fatalf("duplicate save: %+v %v", repeated, err)
			}
			for i := 0; i < 2; i++ {
				if _, err = c.Run(ctx, in.AgentName, view.Dispatch.DispatchID); err != nil {
					t.Fatal(err)
				}
			}
			if classifier.calls != 1 || solver.calls != 1 {
				t.Fatalf("repeated calls: classify=%d feedback=%d", classifier.calls, solver.calls)
			}
			ocr := c.WritingOCR.(*imageTaskOCRStub)
			expected := 0
			if intent == k12.ImageTaskIntentWriting {
				expected = 1
			}
			if ocr.calls != expected {
				t.Fatalf("OCR calls=%d want %d", ocr.calls, expected)
			}
		})
	}
}

func TestAutoCreativeEntryDoesNotAskParentOrCreateOtherTasks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result ImageTaskClassification
	}{
		{"unknown", ImageTaskClassification{Intent: k12.ImageTaskIntentUnknown, IntentEvidence: []string{"unreadable image"}, Confidence: .2, ConfirmationCandidates: []k12.ImageTaskIntent{k12.ImageTaskIntentWriting, k12.ImageTaskIntentCompletedHomework}}},
		{"homework", ImageTaskClassification{Intent: k12.ImageTaskIntentCompletedHomework, IntentEvidence: []string{"math worksheet"}, Confidence: .99}},
		{"ambiguous", ImageTaskClassification{Intent: k12.ImageTaskIntentUnknown, IntentEvidence: []string{"mixed page"}, Confidence: .5, ConfirmationCandidates: []k12.ImageTaskIntent{k12.ImageTaskIntentWriting, k12.ImageTaskIntentArtwork}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			classifier := &imageTaskClassifierStub{result: tc.result}
			c, grading := newImageTaskCoordinatorForTest(t, classifier)
			view, _, err := createAndRunImageTask(t, c, autoCreativeInput())
			if err != nil {
				t.Fatal(err)
			}
			if view.Dispatch.Status != k12.ImageTaskStatusFailed || view.Dispatch.FailureKind != "creative_type_unrecognized" || view.Dispatch.RetrySafe || len(view.Dispatch.ConfirmationCandidates) != 0 || view.Creative != nil || grading.starts != 0 {
				t.Fatalf("unexpected confirmation/work/task: %+v", view)
			}
			if _, err = c.Run(context.Background(), "mingming", view.Dispatch.DispatchID); err != nil {
				t.Fatal(err)
			}
			if classifier.calls != 1 || c.WorkFeedback.(*Deps).Solver.(*imageTaskFeedbackSolver).calls != 0 {
				t.Fatal("terminal content repeated a call")
			}
		})
	}
}

func TestAutoCreativeEntryBoundsOCRWithoutParentConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, content, outcome string
		risks                  []k12.CreativeWorkIntakeOCRRisk
		reviews                int
	}{
		{name: "empty", content: "", outcome: "unreadable"},
		{name: "no_reliable_text", content: "〔模糊片段〕", outcome: "unreadable", risks: []k12.CreativeWorkIntakeOCRRisk{{SegmentID: "line-2", RawText: "〔模糊片段〕", Reasons: []string{"illegible"}}}, reviews: 1},
		{name: "partial", content: "春天到了。〔模糊片段〕小鸟在唱歌。", outcome: "partial", risks: []k12.CreativeWorkIntakeOCRRisk{{SegmentID: "line-2", RawText: "〔模糊片段〕", Reasons: []string{"illegible"}}}, reviews: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ocr, solver := newWritingReviewCoordinator(t, tc.content, tc.risks)
			view, _, err := createAndRunImageTask(t, c, autoCreativeInput())
			if err != nil {
				t.Fatal(err)
			}
			if view.Creative == nil || view.Creative.OCREvidence == nil || view.Creative.OCREvidence.Outcome != tc.outcome || view.Creative.PromotedWorkID != "" || solver.calls != 0 || ocr.calls != 1 || ocr.reviewCalls != tc.reviews {
				t.Fatalf("automatic OCR bounds: %+v calls=%d/%d err=%v", view, ocr.calls, ocr.reviewCalls, err)
			}
			want := k12.CreativeWorkIntakeUnreadable
			if tc.outcome == "partial" {
				want = k12.CreativeWorkIntakeReady
			}
			if view.Creative.Status != want {
				t.Fatalf("state=%s want=%s", view.Creative.Status, want)
			}
			if tc.outcome == "partial" && view.Creative.OCREvidence.CanonicalContent != "春天到了。[无法识别]小鸟在唱歌。" {
				t.Fatalf("unreliable text retained: %+v", view.Creative.OCREvidence)
			}
		})
	}
}

func TestAutoCreativeEntryUnknownDoesNotResendOrSave(t *testing.T) {
	ctx := context.Background()
	classifier := &imageTaskClassifierStub{result: ImageTaskClassification{Intent: k12.ImageTaskIntentWriting, IntentEvidence: []string{"essay"}, Confidence: .99}}
	c, _ := newImageTaskCoordinatorForTest(t, classifier)
	ocr := c.WritingOCR.(*imageTaskOCRStub)
	ocr.err = context.DeadlineExceeded
	view, _, _ := createAndRunImageTask(t, c, autoCreativeInput())
	c = reopenWritingCoordinator(t, c)
	for i := 0; i < 2; i++ {
		_, _ = c.Run(ctx, "mingming", view.Dispatch.DispatchID)
	}
	receipt, err := c.Records.GetImageTaskInvocation(ctx, "mingming", "writing-ocr-1")
	if err != nil || string(receipt.Status) != "outcome_unknown" || ocr.calls != 1 || classifier.calls != 1 {
		t.Fatalf("unknown request replayed: receipt=%+v calls=%d/%d err=%v", receipt, ocr.calls, classifier.calls, err)
	}
	latest, err := c.Get(ctx, "mingming", view.Dispatch.DispatchID)
	if err != nil || latest.Creative == nil || latest.Creative.PromotedWorkID != "" || c.WorkFeedback.(*Deps).Solver.(*imageTaskFeedbackSolver).calls != 0 {
		t.Fatalf("unknown OCR created a work: %+v %v", latest, err)
	}
}

func TestAutoCreativeCombinedOCRPersistsOneCallAndOneDraft(t *testing.T) {
	ctx := context.Background()
	text := "我的爸爸\n爸爸每天陪我读书。我喜欢和爸爸一起观察公园里的小鸟。"
	classifier := &imageTaskClassifierStub{result: ImageTaskClassification{
		Intent: k12.ImageTaskIntentWriting, IntentEvidence: []string{"连续手写作文"}, Confidence: .99,
		WritingOCR: &ImageTaskWritingOCRResult{Raw: text, CanonicalContent: text, Confidence: .99},
	}}
	c, _ := newImageTaskCoordinatorForTest(t, classifier)
	ocr := c.WritingOCR.(*imageTaskOCRStub)
	in := autoCreativeInput()
	view, _, err := createAndRunImageTask(t, c, in)
	if err != nil || view.Creative == nil || view.Creative.Status != k12.CreativeWorkIntakeReady {
		t.Fatalf("combined preparation: %+v %v", view, err)
	}
	if view.Creative.OCREvidence.CanonicalContent != text || view.Creative.OCREvidence.SourceInvocationID != view.Dispatch.ClassificationInvocationID ||
		view.Creative.PromotedWorkID != "" || classifier.calls != 1 || ocr.calls != 0 {
		t.Fatalf("combined OCR lost evidence or repeated a call: %+v classify=%d OCR=%d", view, classifier.calls, ocr.calls)
	}
	var count int
	if err := c.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_image_task_invocations WHERE dispatch_id=? OR intake_id=?`, view.Dispatch.DispatchID, view.Creative.IntakeID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("physical receipts=%d err=%v", count, err)
	}
	c = reopenWritingCoordinator(t, c)
	replay, created, err := c.Create(ctx, in)
	if err != nil || created || replay.Creative.IntakeID != view.Creative.IntakeID {
		t.Fatalf("replay created another draft: %+v %v", replay, err)
	}
	if _, err = c.Run(ctx, in.AgentName, replay.Dispatch.DispatchID); err != nil || classifier.calls != 1 || ocr.calls != 0 {
		t.Fatalf("replay repeated recognition: %v", err)
	}
	result, err := c.Result(ctx, in.AgentName, replay.Dispatch.DispatchID)
	if err != nil || len(result.OperationReceipts) != 1 {
		t.Fatalf("result misreported calls: %+v %v", result.OperationReceipts, err)
	}
	saved, err := c.Confirm(ctx, ConfirmImageTaskInput{AgentName: in.AgentName, DispatchID: replay.Dispatch.DispatchID,
		ExpectedVersion: replay.Dispatch.Version, Creative: &ConfirmCreativeImageTaskInput{Action: CreativeImageTaskActionCommit}})
	if err != nil {
		t.Fatal(err)
	}
	assertCurrentCreativeIntakePromotion(t, c, saved.Creative)
}

func TestAutoCreativeCombinedOCRReviewsOnlyUncertainSegments(t *testing.T) {
	for _, tc := range []struct {
		name, content, outcome string
		risks                  []k12.CreativeWorkIntakeOCRRisk
		reviews                int
	}{
		{"partial", "春天到了。〔模糊片段〕小鸟在唱歌。", "partial", []k12.CreativeWorkIntakeOCRRisk{{SegmentID: "line-2", RawText: "〔模糊片段〕", Reasons: []string{"illegible"}}}, 1},
		{"document", "〔模糊片段〕", "unreadable", []k12.CreativeWorkIntakeOCRRisk{{SegmentID: "document", RawText: "〔模糊片段〕", Reasons: []string{"document_unreadable"}}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ocr, solver := newWritingReviewCoordinator(t, tc.content, tc.risks)
			classifier := c.Classifier.(*imageTaskClassifierStub)
			classifier.result.WritingOCR = &ImageTaskWritingOCRResult{Raw: tc.content, CanonicalContent: tc.content, Confidence: .7, RiskSegments: tc.risks}
			view, _, err := createAndRunImageTask(t, c, autoCreativeInput())
			if err != nil || view.Creative == nil || view.Creative.OCREvidence.Outcome != tc.outcome || ocr.calls != 0 || ocr.reviewCalls != tc.reviews || solver.calls != 0 {
				t.Fatalf("bounded reuse: %+v OCR=%d reviews=%d err=%v", view, ocr.calls, ocr.reviewCalls, err)
			}
			if _, err = c.Run(context.Background(), view.Dispatch.AgentName, view.Dispatch.DispatchID); err != nil || classifier.calls != 1 || ocr.reviewCalls != tc.reviews {
				t.Fatalf("replay repeated review: %v", err)
			}
		})
	}
}

func TestAutoCreativeCombinedOCRUnknownReviewDoesNotResend(t *testing.T) {
	ctx := context.Background()
	content := "春天到了。〔模糊片段〕小鸟在唱歌。"
	risks := []k12.CreativeWorkIntakeOCRRisk{{SegmentID: "line-2", RawText: "〔模糊片段〕", Reasons: []string{"illegible"}}}
	c, ocr, solver := newWritingReviewCoordinator(t, content, risks)
	classifier := c.Classifier.(*imageTaskClassifierStub)
	classifier.result.WritingOCR = &ImageTaskWritingOCRResult{Raw: content, CanonicalContent: content, Confidence: .7, RiskSegments: risks}
	ocr.reviewErr = context.DeadlineExceeded
	in := autoCreativeInput()
	view, _, runErr := createAndRunImageTask(t, c, in)
	if runErr == nil || view.Creative == nil {
		t.Fatalf("unknown review checkpoint missing: %+v %v", view, runErr)
	}
	c = reopenWritingCoordinator(t, c)
	ocr.reviewErr = nil
	for i := 0; i < 2; i++ {
		if _, err := c.Run(ctx, in.AgentName, view.Dispatch.DispatchID); err != nil {
			t.Fatal(err)
		}
	}
	if classifier.calls != 1 || ocr.calls != 0 || ocr.reviewCalls != 1 || solver.calls != 0 {
		t.Fatalf("unknown review resent: classification=%d fullOCR=%d review=%d feedback=%d", classifier.calls, ocr.calls, ocr.reviewCalls, solver.calls)
	}
	receipt, err := c.Records.GetImageTaskInvocation(ctx, in.AgentName, "writing-review-1")
	if err != nil || receipt.Status != k12.ImageTaskInvocationOutcomeUnknown {
		t.Fatalf("unknown receipt overwritten: %+v %v", receipt, err)
	}
	latest, err := c.Get(ctx, in.AgentName, view.Dispatch.DispatchID)
	if err != nil || latest.Creative.PromotedWorkID != "" || latest.Creative.OCREvidence.Raw != content || latest.Creative.Status == k12.CreativeWorkIntakeUnreadable {
		t.Fatalf("unknown result archived or disguised as unreadable: %+v %v", latest, err)
	}
}
