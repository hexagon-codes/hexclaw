package engineadapter

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type verifiedTextbookReaderProbe struct {
	pages   []k12.VerifiedTextbookPage
	calls   int
	request k12.VerifiedTextbookReadRequest
}

func (p *verifiedTextbookReaderProbe) ReadVerifiedTextbookPages(_ context.Context, request k12.VerifiedTextbookReadRequest) ([]k12.VerifiedTextbookPage, error) {
	p.calls++
	p.request = request
	if request.Subject != "math" {
		return nil, fmt.Errorf("verified textbook subject must be math, got %q", request.Subject)
	}
	return p.pages, nil
}

func verifiedGroundingPage() k12.VerifiedTextbookPage {
	content := "小数除法：先确定商的小数点，再按整数除法计算。"
	return k12.VerifiedTextbookPage{
		LogicalPage: 1, PDFPage: 3, Content: content, ContentDigest: sha256Hex(content),
		SegmentRefs: []string{"segment-1"}, LessonTitles: []string{"小数除法"},
	}
}

func TestVerifiedTextbookGroundingDoesNotRequireEmbedding(t *testing.T) {
	kb := validGroundingEvidenceKB()
	kb.active, kb.activeRevision = false, ""
	reader := &verifiedTextbookReaderProbe{pages: []k12.VerifiedTextbookPage{verifiedGroundingPage()}}
	adapter := NewGroundingAdapter(kb, reader)
	snapshot := validGroundingEvidenceSnapshot()
	snapshot.OwnerID = "owner-1"
	frozen, err := adapter.FreezeGroundingSnapshot(context.Background(), snapshot)
	if err != nil || frozen.SourceMode != usecase.GroundingSourceModeVerifiedText || frozen.VectorRevisionID != "" {
		t.Fatalf("text source depends on embedding: snapshot=%+v err=%v", frozen, err)
	}
	result, err := adapter.GroundSnapshotWithEvidence(context.Background(), frozen, "小数除法", "五年级下")
	if err != nil || !result.Found || result.Text != reader.pages[0].Content || len(result.Sources) != 1 || len(result.Receipts) != 1 {
		t.Fatalf("verified source unavailable: %+v %v", result, err)
	}
	if reader.request.Subject != "math" || reader.request.OwnerID != snapshot.OwnerID || reader.request.Scope.DocumentGeneration != snapshot.DocumentGeneration ||
		reader.request.Scope.SourceDigest != snapshot.SourceDigest || !reflect.DeepEqual(reader.request.Scope.PageRefs, snapshot.PageRefs) {
		t.Fatalf("source identity changed: %+v", reader.request)
	}
	if result.Sources[0].LocationMethod != "exact_lesson_title" || result.Receipts[0].SourceMode != usecase.GroundingSourceModeVerifiedText ||
		result.Receipts[0].VectorRevisionID != "" || result.Receipts[0].CitationDigest != reader.pages[0].ContentDigest ||
		kb.pinnedCalls+kb.unpinnedCalls+kb.legacyCalls != 0 {
		t.Fatalf("text source misreported as semantic: %+v", result)
	}
}

func TestVerifiedTextbookGroundingRejectsUnrelatedOrChangedPages(t *testing.T) {
	for _, tc := range []struct {
		name      string
		query     string
		change    func(*k12.VerifiedTextbookPage)
		wantError bool
	}{
		{name: "unrelated", query: "三角形面积"},
		{name: "shared partial keyword", query: "整数除法的验算方法"},
		{name: "unknown knowledge point", query: "其他"},
		{name: "changed digest", query: "小数除法", change: func(p *k12.VerifiedTextbookPage) { p.Content += "changed" }, wantError: true},
		{name: "outside page", query: "小数除法", change: func(p *k12.VerifiedTextbookPage) { p.PDFPage = 4 }, wantError: true},
		{name: "outside segment", query: "小数除法", change: func(p *k12.VerifiedTextbookPage) { p.SegmentRefs = []string{"other"} }, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := verifiedGroundingPage()
			if tc.change != nil {
				tc.change(&page)
			}
			reader := &verifiedTextbookReaderProbe{pages: []k12.VerifiedTextbookPage{page}}
			kb := validGroundingEvidenceKB()
			snapshot := validGroundingEvidenceSnapshot()
			snapshot.OwnerID, snapshot.SourceMode, snapshot.VectorRevisionID = "owner-1", usecase.GroundingSourceModeVerifiedText, ""
			result, err := NewGroundingAdapter(kb, reader).GroundSnapshotWithEvidence(context.Background(), snapshot, tc.query, "五年级下")
			if (err != nil) != tc.wantError || result.Found || len(result.Receipts) != 0 || len(result.Sources) != 0 {
				t.Fatalf("invalid source adopted: %+v %v", result, err)
			}
			if kb.pinnedCalls+kb.unpinnedCalls+kb.legacyCalls != 0 {
				t.Fatal("local miss triggered an implicit semantic request")
			}
		})
	}
}

func TestVerifiedTextbookGroundingKeepsLegacySemanticMode(t *testing.T) {
	for _, mode := range []string{"", usecase.GroundingSourceModeSemantic} {
		t.Run("mode="+mode, func(t *testing.T) {
			kb := validGroundingEvidenceKB()
			reader := &verifiedTextbookReaderProbe{pages: []k12.VerifiedTextbookPage{verifiedGroundingPage()}}
			snapshot := validGroundingEvidenceSnapshot()
			snapshot.VectorRevisionID, snapshot.SourceMode = "revision-a", mode
			result, err := NewGroundingAdapter(kb, reader).GroundSnapshotWithEvidence(context.Background(), snapshot, "小数除法", "五年级下")
			if err != nil || !result.Found || kb.pinnedCalls != 1 || reader.calls != 0 || len(result.Sources) != 0 {
				t.Fatalf("semantic contract changed: %+v %v", result, err)
			}
		})
	}
}

func TestVerifiedTextbookGroundingRecordsExactKnowledgeTerm(t *testing.T) {
	page := verifiedGroundingPage()
	page.LessonTitles = nil
	method, terms := locateVerifiedTextPage(page, "小数除法")
	if method != "exact_knowledge_term" || !reflect.DeepEqual(terms, []string{"小数除法"}) {
		t.Fatalf("exact term evidence missing: %s %v", method, terms)
	}
}
