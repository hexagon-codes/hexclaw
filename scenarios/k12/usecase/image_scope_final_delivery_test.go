package usecase

import (
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestImageScopeFinalReplyAndPDFShareFrozenSolveIntent(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    k12.ImageTaskIntent
	}{{"请解答图片中的题目", k12.ImageTaskIntentBlankWorksheet}, {"", k12.ImageTaskIntentCompletedHomework}} {
		t.Run("message="+tc.message, func(t *testing.T) {
			d, artifact := gradingPDFProjectionFixtureForIntent(t, []ParentTeachingGuide{{Answer: "2"}}, tc.message)
			intent, err := d.finalReplyIntent(t.Context(), artifact)
			if err != nil || intent != tc.want {
				t.Fatalf("final reply intent=%s want=%s err=%v", intent, tc.want, err)
			}
			source, pdfIntent, err := d.gradingFinalArtifactImageSource(t.Context(), artifact)
			if err != nil || pdfIntent != tc.want || len(source.Data) == 0 {
				t.Fatalf("PDF source intent=%s want=%s bytes=%d err=%v", pdfIntent, tc.want, len(source.Data), err)
			}
		})
	}
}
