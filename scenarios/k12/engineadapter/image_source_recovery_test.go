package engineadapter

import (
	"context"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestImageSourceRecoveryRequiresVerifiedQuotation(t *testing.T) {
	page := verifiedGroundingPage()
	reader := &verifiedTextbookReaderProbe{pages: []k12.VerifiedTextbookPage{page}}
	snapshot := validGroundingEvidenceSnapshot()
	snapshot.OwnerID, snapshot.SourceMode, snapshot.VectorRevisionID = "owner-1", usecase.GroundingSourceModeVerifiedText, ""
	adapter := NewGroundingAdapter(validGroundingEvidenceKB(), reader)
	gap := k12.RecognitionImageSourceGapV1{Order: 1, Kind: "quotation", Before: "小数除法：先确定", After: "，再按整数除法计算。"}
	result, err := adapter.RecoverImageSource(context.Background(), snapshot, gap)
	if err != nil || result.State != "verified" || result.AdoptedText != "商的小数点" || result.SourceDigest != snapshot.SourceDigest || result.PDFPage != 3 {
		t.Fatalf("verified gap=%+v error=%v", result, err)
	}
	for _, kind := range []string{"instruction", "condition", "handwriting"} {
		gap.Kind = kind
		result, err = adapter.RecoverImageSource(context.Background(), snapshot, gap)
		if err != nil || result.State == "verified" || result.AdoptedText != "" {
			t.Fatalf("textbook fabricated %s: %+v error=%v", kind, result, err)
		}
	}
	gap.Kind = "quotation"
	reader.pages[0].Content += "changed"
	if _, err = adapter.RecoverImageSource(context.Background(), snapshot, gap); err == nil {
		t.Fatal("changed source digest accepted")
	}
}
