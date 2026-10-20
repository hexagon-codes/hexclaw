package engineadapter

import (
	"context"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/knowledge"
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

func TestImageSourceRecoveryRequiresWholeVisibleExerciseAndProvenance(t *testing.T) {
	visible := "这样宏伟的建筑物，这样和谐统一的布局，不能不令人惊叹。这句话先运用____的句式，后以____的表达加强了语势，我也要[missing]校园，仿写一句话：____"
	text := strings.ReplaceAll(visible, "[missing]", "赞美")
	gap := k12.RecognitionImageSourceGapV1{Order: 2, Kind: "instruction", Before: "我也要", After: "校园，仿写一句话："}
	hit := knowledge.SearchHit{DocID: "exercise-source", DocTitle: "六年级上语文同步练习", DocumentGeneration: 2, SourceDigest: "source-file-digest", Content: text, CitationDigest: sha256Hex(text), PageStart: 8}
	for _, scenario := range []string{"exact", "quotation only", "different visible word", "changed digest", "conflicting sources"} {
		t.Run(scenario, func(t *testing.T) {
			kb := &groundingEvidenceKB{hits: []knowledge.SearchHit{hit}}
			switch scenario {
			case "quotation only":
				kb.hits[0].Content = "这样宏伟的建筑物，这样和谐统一的布局，不能不令人惊叹。"
			case "different visible word":
				kb.hits[0].Content = strings.ReplaceAll(text, "建筑物", "建筑群")
				kb.hits[0].CitationDigest = sha256Hex(kb.hits[0].Content)
			case "changed digest":
				kb.hits[0].CitationDigest = "changed"
			case "conflicting sources":
				other := hit
				other.Content = strings.ReplaceAll(text, "赞美", "装点")
				other.CitationDigest = sha256Hex(other.Content)
				kb.hits = append(kb.hits, other)
			}
			result, err := NewGroundingAdapter(kb).FindImageSourceCandidate(t.Context(), "test-agent", "语文", "六年级上", visible, gap)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "exact" {
				if result.State != "verified" || result.AdoptedText != "赞美" || result.DocumentID != "exercise-source" || result.PDFPage != 8 {
					t.Fatalf("result=%+v", result)
				}
			} else if result.State == "verified" || result.AdoptedText != "" {
				t.Fatalf("unverified source adopted: %+v", result)
			}
		})
	}
	lookups := 0
	adapter := NewGroundingAdapter(&groundingEvidenceKB{}).WithImageSourcePageLookup(func(context.Context, string, string) ([]engine.ReferenceSourcePage, error) {
		lookups++
		page := "六年级上语文同步练习\n" + text
		return []engine.ReferenceSourcePage{{URL: "https://school.example/original-exercise", Content: page, Digest: "sha256:" + sha256Hex(page)}}, nil
	})
	result, err := adapter.FindImageSourceCandidate(t.Context(), "test-agent", "语文", "六年级上", visible, gap)
	if err != nil || lookups != 1 || result.State != "verified" || result.AdoptedText != "赞美" || result.URL != "https://school.example/original-exercise" {
		t.Fatalf("web source=%+v err=%v", result, err)
	}
	gap.Kind = "handwriting"
	result, err = adapter.FindImageSourceCandidate(t.Context(), "test-agent", "语文", "六年级上", visible, gap)
	if err != nil || lookups != 1 || result.State == "verified" {
		t.Fatal("handwriting recovered from a reference source")
	}
}
