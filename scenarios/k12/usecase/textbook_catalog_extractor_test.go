package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func TestTextbookCatalogCheckpointExtractorUsesTOCAndPrintedFooterProof(t *testing.T) {
	source := syntheticTextbookCatalogSource()
	source.DocumentTitle = "五年级下册-数学-冒烟0905.pdf"

	publication, err := (TextbookCatalogCheckpointExtractor{}).Extract(
		context.Background(), source,
	)
	if err != nil {
		t.Fatalf("extract deterministic catalog: %v", err)
	}
	var catalog struct {
		TextbookEdition string `json:"textbook_edition"`
		TextbookVersion string `json:"textbook_version"`
		Title           string `json:"title"`
		Volume          string `json:"volume"`
		PageMin         int    `json:"page_min"`
		PageMax         int    `json:"page_max"`
		Units           []struct {
			Title    string `json:"title"`
			PageFrom int    `json:"page_from"`
			PageTo   int    `json:"page_to"`
		} `json:"units"`
		PageRefs []struct {
			LogicalPage int `json:"logical_page"`
			PDFPage     int `json:"pdf_page"`
		} `json:"page_refs"`
	}
	if err := json.Unmarshal(publication.CatalogJSON, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.TextbookEdition != "人教版" || catalog.TextbookVersion != "2022" ||
		catalog.Title != "五年级下册-数学-冒烟0905" || catalog.Volume != "下册" {
		t.Fatalf("catalog metadata was not derived from exact evidence: %+v", catalog)
	}
	if catalog.PageMin != 1 || catalog.PageMax != 3 || len(catalog.Units) != 2 {
		t.Fatalf("catalog range/units=%d..%d %+v", catalog.PageMin, catalog.PageMax, catalog.Units)
	}
	if len(catalog.PageRefs) != 3 || catalog.PageRefs[0].LogicalPage != 1 ||
		catalog.PageRefs[0].PDFPage != 3 || catalog.PageRefs[2].PDFPage != 5 {
		t.Fatalf("logical->physical map=%+v; must use footer evidence, not logical=PDF", catalog.PageRefs)
	}
	if len(publication.PageProofs) != 3 {
		t.Fatalf("page proofs=%+v want 3", publication.PageProofs)
	}
	firstProof := publication.PageProofs[0]
	if firstProof.EvidencePage != 3 || firstProof.Method != "printed_anchor" ||
		strings.TrimSpace(source.Pages[2].Content[firstProof.EvidenceOffsetFrom:firstProof.EvidenceOffsetTo]) != "1" {
		t.Fatalf("page proof does not bind the persisted footer span: %+v", publication.PageProofs)
	}
}

func TestTextbookCatalogCheckpointExtractorPrefersCurrentApprovalYearAcrossPDFLineBreaks(t *testing.T) {
	source := syntheticTextbookCatalogSource()
	source.Pages[0].Content = strings.Replace(
		source.Pages[0].Content,
		"2022年经国家教材委员会专家委员会审核通过",
		"《义务教育教科书数学五年级下册》（2014 年版）基础上修订，\n"+
			"2022 年经国家教材委员会专家\n委员会审核通过",
		1,
	)
	source.Pages[0].ContentDigest = testTextbookContentDigest(source.Pages[0].Content)

	publication, err := (TextbookCatalogCheckpointExtractor{}).Extract(
		context.Background(), source,
	)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		TextbookVersion string `json:"textbook_version"`
	}
	if err := json.Unmarshal(publication.CatalogJSON, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.TextbookVersion != "2022" {
		t.Fatalf("current approval year=%q, want 2022 rather than superseded base edition 2014",
			catalog.TextbookVersion)
	}
}

func TestTextbookCatalogCheckpointExtractorAcceptsUnicodeTOCWhitespace(t *testing.T) {
	source := syntheticTextbookCatalogSource()
	source.Pages[1].Content = "目 录\n1. **观察物体（三）**　1\n2. **因数和倍数**　2\n"
	source.Pages[1].ContentDigest = testTextbookContentDigest(source.Pages[1].Content)
	publication, err := (TextbookCatalogCheckpointExtractor{}).Extract(
		context.Background(), source,
	)
	if err != nil {
		t.Fatalf("extract catalog with full-width TOC whitespace: %v", err)
	}
	var catalog struct {
		Units []struct {
			Title    string `json:"title"`
			PageFrom int    `json:"page_from"`
		} `json:"units"`
	}
	if err := json.Unmarshal(publication.CatalogJSON, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Units) != 2 || catalog.Units[0].Title != "观察物体（三）" ||
		catalog.Units[0].PageFrom != 1 || catalog.Units[1].PageFrom != 2 {
		t.Fatalf("unicode TOC units=%+v", catalog.Units)
	}
}

func TestTextbookCatalogCheckpointExtractorUsesNamedFootersAndCoverYear(t *testing.T) {
	source := syntheticTextbookCatalogSource()
	contents := []string{
		"义务教育教科书\n# 数学\n## 六年级\n### 上册\n人民教育出版社\n2024\n",
		"目录\n一 确定位置 1\n二 分数乘法 4\n",
		"确定位置\n正文\n1\n",
		"正文\n2　确定位置\n",
		"正文没有页脚\n",
		"分数乘法\n正文\n4　分数乘法\n",
		"正文\n5\n",
		"后记\n依据《义务教育数学课程标准（2022年版）》编写。\n",
	}
	source.Pages = nil
	offset := int64(0)
	for i, content := range contents {
		source.Pages = append(source.Pages, k12storage.TextbookCatalogSourcePage{
			PDFPage: i + 1, Content: content, ContentDigest: testTextbookContentDigest(content),
			SourceOffsetFrom: offset, SourceOffsetTo: offset + int64(len(content)),
			SegmentRefs: []string{fmt.Sprintf("chunk-%d", i+1)},
		})
		offset += int64(len(content))
	}
	publication, err := (TextbookCatalogCheckpointExtractor{}).Extract(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	var catalog textbookCatalogJSON
	if err := json.Unmarshal(publication.CatalogJSON, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.TextbookVersion != "2024" || len(catalog.PageRefs) != 5 || len(catalog.Units) != 2 {
		t.Fatalf("catalog metadata=%+v", catalog)
	}
	for i, ref := range catalog.PageRefs {
		if ref.LogicalPage != i+1 || ref.PDFPage != i+3 {
			t.Fatalf("page map[%d]=%+v", i, ref)
		}
	}
	if publication.PageProofs[2].Method != "adjacent_printed_anchors" {
		t.Fatalf("missing footer proof=%+v", publication.PageProofs[2])
	}
	proof := publication.PageProofs[1]
	if proof.Method != "printed_anchor" || source.Pages[3].Content[proof.EvidenceOffsetFrom:proof.EvidenceOffsetTo] != "2" {
		t.Fatalf("named footer must retain exact digit span: %+v", proof)
	}
	for _, tc := range []struct{ name, footer string }{
		{"conflicting page", "20　确定位置"},
		{"unrelated body title", "2　练习题答案"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := source
			changed.Pages = append([]k12storage.TextbookCatalogSourcePage(nil), source.Pages...)
			changed.Pages[3].Content = "正文\n" + tc.footer + "\n"
			changed.Pages[3].ContentDigest = testTextbookContentDigest(changed.Pages[3].Content)
			changed.Pages[3].SourceOffsetTo = changed.Pages[3].SourceOffsetFrom + int64(len(changed.Pages[3].Content))
			if _, err := (TextbookCatalogCheckpointExtractor{}).Extract(context.Background(), changed); !errors.Is(err, ErrTextbookCatalogEvidenceInsufficient) {
				t.Fatalf("unproved mapping error=%v", err)
			}
		})
	}
}

func TestTextbookCatalogCheckpointExtractorFailsClosedOnMissingVersionOrPage(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*k12storage.TextbookCatalogSource)
	}{
		{
			name: "no provable edition year",
			mutate: func(source *k12storage.TextbookCatalogSource) {
				source.Pages[0].Content = strings.ReplaceAll(
					source.Pages[0].Content,
					"2022年经国家教材委员会专家委员会审核通过",
					"经国家教材委员会专家委员会审核通过",
				)
				source.Pages[0].ContentDigest = testTextbookContentDigest(source.Pages[0].Content)
			},
		},
		{
			name: "conflicting boundary footer",
			mutate: func(source *k12storage.TextbookCatalogSource) {
				source.Pages[4].Content = strings.ReplaceAll(source.Pages[4].Content, "\n3\n", "\n30\n")
				source.Pages[4].ContentDigest = testTextbookContentDigest(source.Pages[4].Content)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := syntheticTextbookCatalogSource()
			tt.mutate(&source)
			publication, err := (TextbookCatalogCheckpointExtractor{}).Extract(
				context.Background(), source,
			)
			if !errors.Is(err, ErrTextbookCatalogEvidenceInsufficient) {
				t.Fatalf("error=%v want evidence-insufficient", err)
			}
			if len(publication.CatalogJSON) != 0 || len(publication.PageProofs) != 0 {
				t.Fatalf("fail-closed extractor returned partial proposal: %+v", publication)
			}
		})
	}
}

func TestTextbookCatalogCheckpointExtractorRepairsIsolatedConflictingFooterWithoutChangingSource(t *testing.T) {
	source := controlledTextbookFooterSource("114", "155", "116")
	before, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := (TextbookCatalogCheckpointExtractor{}).Extract(context.Background(), source)
	if err != nil {
		t.Fatalf("extract isolated conflicting footer: %v", err)
	}
	var catalog textbookCatalogJSON
	if err := json.Unmarshal(publication.CatalogJSON, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.PageMin != 114 || catalog.PageMax != 116 || len(catalog.PageRefs) != 3 ||
		catalog.PageRefs[1].LogicalPage != 115 || catalog.PageRefs[1].PDFPage != 4 {
		t.Fatalf("isolated footer page map=%+v", catalog)
	}
	proof := publication.PageProofs[1]
	page := source.Pages[3]
	if proof.Method != "adjacent_printed_anchors" || proof.EvidencePage != 4 ||
		proof.EvidenceDigest != page.ContentDigest || proof.EvidenceOffsetFrom != 0 ||
		proof.EvidenceOffsetTo != len(page.Content) || len(proof.SegmentRefs) != 1 ||
		proof.SegmentRefs[0] != page.SegmentRefs[0] || !strings.HasSuffix(page.Content, "\n155\n") {
		t.Fatalf("isolated footer lost original evidence: %+v", proof)
	}
	after, err := json.Marshal(source)
	if err != nil || string(after) != string(before) {
		t.Fatalf("catalog extraction changed persisted source: %v", err)
	}
}

func TestTextbookCatalogCheckpointExtractorRejectsUnprovedFooterConflicts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		footers []string
	}{
		{name: "first boundary", footers: []string{"154", "115", "116"}},
		{name: "last boundary", footers: []string{"114", "115", "156"}},
		{name: "consecutive conflicts", footers: []string{"114", "155", "156", "117"}},
		{name: "multiple offsets", footers: []string{"114", "155", "116", "157", "118"}},
		{name: "missing direct neighbor", footers: []string{"114", "155", "unreadable footer", "117"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := controlledTextbookFooterSource(tc.footers...)
			publication, err := (TextbookCatalogCheckpointExtractor{}).Extract(context.Background(), source)
			if !errors.Is(err, ErrTextbookCatalogEvidenceInsufficient) ||
				len(publication.CatalogJSON) != 0 || len(publication.PageProofs) != 0 {
				t.Fatalf("unproved footer conflict produced publication=%+v err=%v", publication, err)
			}
		})
	}
}

func controlledTextbookFooterSource(footers ...string) k12storage.TextbookCatalogSource {
	source := syntheticTextbookCatalogSource()
	contents := []string{source.Pages[0].Content, "目 录\n1 第一单元 114\n"}
	for _, footer := range footers {
		contents = append(contents, "第一单元\n正文\n"+footer+"\n")
	}
	source.Pages = nil
	offset := int64(0)
	for index, content := range contents {
		page := k12storage.TextbookCatalogSourcePage{
			PDFPage: index + 1, Content: content, ContentDigest: testTextbookContentDigest(content),
			SourceOffsetFrom: offset, SourceOffsetTo: offset + int64(len(content)),
		}
		if index >= 2 {
			page.SegmentRefs = []string{fmt.Sprintf("chunk-page-%d", index+1)}
		}
		source.Pages = append(source.Pages, page)
		offset += int64(len(content))
	}
	return source
}

func syntheticTextbookCatalogSource() k12storage.TextbookCatalogSource {
	contents := []string{
		"义务教育教科书\n数学 五年级 下册\n人民教育出版社\n2022年经国家教材委员会专家委员会审核通过\n",
		"目 录\n1 观察物体（三） 1\n2 因数和倍数 2\n",
		"1 观察物体（三）\n正文\n1\n",
		"2 因数和倍数\n正文\n2\n",
		"练习\n正文\n3\n",
	}
	pages := make([]k12storage.TextbookCatalogSourcePage, 0, len(contents))
	offset := int64(0)
	for index, content := range contents {
		page := k12storage.TextbookCatalogSourcePage{
			PDFPage:          index + 1,
			Content:          content,
			ContentDigest:    testTextbookContentDigest(content),
			SourceOffsetFrom: offset,
			SourceOffsetTo:   offset + int64(len(content)),
		}
		if index >= 2 {
			page.SegmentRefs = []string{"chunk-page-" + string(rune('1'+index-2))}
		}
		pages = append(pages, page)
		offset += int64(len(content))
	}
	return k12storage.TextbookCatalogSource{
		IngestJobID:      "ingest-proof-1",
		SourcePlanDigest: strings.Repeat("b", 64),
		DocumentTitle:    "义务教育教科书·数学五年级下册.pdf",
		SourceDigest:     strings.Repeat("a", 64),
		Pages:            pages,
	}
}

func testTextbookContentDigest(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}
