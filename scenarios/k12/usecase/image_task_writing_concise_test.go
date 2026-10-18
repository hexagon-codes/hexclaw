package usecase

import (
	"context"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type conciseImageWritingSolver struct{ *imageTaskFeedbackSolver }

func (s *conciseImageWritingSolver) GenerateWorkFeedback(ctx context.Context, req WorkFeedbackRequest) (WorkFeedbackOutput, error) {
	out, err := s.imageTaskFeedbackSolver.GenerateWorkFeedback(ctx, req)
	out.Feedback = "## 具体肯定\n“小鸟在唱歌”写出了春天的声音。\n\n## 关键问题\n可辨认片段中的位置还不明确。\n\n## 局部示范\n原句：小鸟在唱歌。\n建议：只补实际听到小鸟叫声的位置。\n原因：便于读者理解。\n\n## 孩子下一步\n在可辨认的一句旁补真实位置，读一遍检查。"
	out.SkillStamp = "writing-feedback@1.0.2/test"
	out.ProjectionContract = k12.WorkFeedbackProjectionWritingConciseV1
	return out, err
}

func TestImageTaskPartialWritingKeepsConciseContractAndOriginalEvidence(t *testing.T) {
	raw := "春天到了。〔模糊片段〕小鸟在唱歌。"
	c, ocr, solver := newWritingReviewCoordinator(t, raw, []k12.CreativeWorkIntakeOCRRisk{{SegmentID: "line-2", RawText: "〔模糊片段〕", Reasons: []string{"illegible"}}})
	ocr.review = []k12.CreativeWorkOCRReviewSegment{{SegmentID: "line-2", Readable: false, Text: "猜测的文字"}}
	c.WorkFeedback.(*Deps).Solver = &conciseImageWritingSolver{solver}
	view, _, err := createAndRunImageTask(t, c, testCreateImageTaskInput())
	if err != nil || view.Creative == nil {
		t.Fatalf("partial writing: %+v err=%v", view, err)
	}
	_, state := assertCurrentCreativeIntakePromotion(t, c, view.Creative)
	if solver.calls != 1 || len(solver.requests) != 1 || !solver.requests[0].PartialContent ||
		state.Initial.Source.ContentMarkdown != "春天到了。[无法识别]小鸟在唱歌。" ||
		view.Creative.OCREvidence.Raw != raw || view.Creative.OCREvidence.OriginalCanonical != raw {
		t.Fatalf("partial evidence or call identity changed: requests=%+v state=%+v", solver.requests, state)
	}
	projection := state.Initial.Feedback.ProjectionMarkdown
	for _, heading := range []string{"## 具体肯定", "## 关键问题", "## 局部示范", "## 孩子下一步"} {
		if strings.Count(projection, heading) != 1 {
			t.Fatalf("partial re-projection lost new contract: %s", projection)
		}
	}
	if !strings.Contains(projection, solver.requests[0].ContentLimitations) || strings.Contains(projection, "猜测的文字") || strings.Contains(projection, "## 可见证据") {
		t.Fatalf("partial scope was lost or reinterpreted: %s", projection)
	}
	result, err := c.Result(context.Background(), "mingming", view.Dispatch.DispatchID)
	if err != nil || result.Kind != "creative" || solver.calls != 1 || ocr.calls != 1 || ocr.reviewCalls != 1 {
		t.Fatalf("reading frozen partial result reran processing: kind=%s calls=%d err=%v", result.Kind, solver.calls, err)
	}
}
