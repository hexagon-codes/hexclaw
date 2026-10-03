package engineadapter

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func (a *GroundingAdapter) groundVerifiedText(
	ctx context.Context,
	snapshot usecase.GroundingSnapshot,
	knowledgePoint, grade string,
) (usecase.GroundingSnapshotResult, error) {
	if a.verifiedReader == nil {
		return usecase.GroundingSnapshotResult{}, fmt.Errorf("grounding: verified textbook reader unavailable")
	}
	subject := snapshot.Subject
	if subject == "数学" {
		subject = "math"
	}
	pages, err := a.verifiedReader.ReadVerifiedTextbookPages(ctx, k12.VerifiedTextbookReadRequest{
		OwnerID: snapshot.OwnerID, AgentName: snapshot.AgentName, Subject: subject,
		Scope: k12.TextbookGroundingScope{
			TextbookBindingID: snapshot.TextbookBindingID, TextbookManifestID: snapshot.TextbookManifestID,
			DocumentID: snapshot.DocumentID, DocumentGeneration: snapshot.DocumentGeneration,
			SourceDigest: snapshot.SourceDigest, Edition: snapshot.Edition, Volume: snapshot.Volume,
			SegmentRefs: append([]string(nil), snapshot.SegmentRefs...), PageRefs: cloneGroundingPageRefs(snapshot.PageRefs),
		},
	})
	if err != nil {
		return usecase.GroundingSnapshotResult{}, fmt.Errorf("grounding: read verified textbook: %w", err)
	}
	if err := validateVerifiedTextPages(snapshot, pages); err != nil {
		return usecase.GroundingSnapshotResult{}, err
	}
	result := usecase.GroundingSnapshotResult{Receipts: []usecase.GroundingEvidenceReceipt{}}
	query := strings.Join(nonEmptyGroundingFacts(snapshot.Edition, snapshot.Volume, grade, knowledgePoint, "教材讲法"), " ")
	queryDigest := "sha256:" + sha256Hex(query)
	var parts []string
	for _, page := range pages {
		method, terms := locateVerifiedTextPage(page, knowledgePoint)
		if len(terms) == 0 {
			continue
		}
		result.Sources = append(result.Sources, usecase.GroundingTextSource{
			LogicalPage: page.LogicalPage, PDFPage: page.PDFPage,
			Content: page.Content, ContentDigest: page.ContentDigest,
			SegmentRefs:    append([]string(nil), page.SegmentRefs...),
			LocationMethod: method, MatchedTerms: terms,
		})
		parts = append(parts, strings.TrimSpace(page.Content))
		for _, segment := range page.SegmentRefs {
			result.Receipts = append(result.Receipts, usecase.GroundingEvidenceReceipt{
				TextbookBindingID: snapshot.TextbookBindingID, TextbookManifestID: snapshot.TextbookManifestID,
				DocumentID: snapshot.DocumentID, DocumentGeneration: snapshot.DocumentGeneration,
				SourceDigest: snapshot.SourceDigest, ChunkID: segment,
				LogicalPage: page.LogicalPage, PDFPage: page.PDFPage,
				QueryDigest: queryDigest, CitationDigest: page.ContentDigest,
				SourceMode: usecase.GroundingSourceModeVerifiedText,
			})
		}
		if len(result.Sources) >= a.topK {
			break
		}
	}
	result.Text = strings.Join(parts, "\n\n")
	result.Found = result.Text != ""
	return result, nil
}

func validateVerifiedTextPages(snapshot usecase.GroundingSnapshot, pages []k12.VerifiedTextbookPage) error {
	type pageKey struct{ logical, physical int }
	allowed := make(map[pageKey][]string, len(snapshot.PageRefs))
	for _, page := range snapshot.PageRefs {
		allowed[pageKey{page.LogicalPage, page.PDFPage}] = page.SegmentRefs
	}
	seen := make(map[pageKey]struct{}, len(pages))
	for _, page := range pages {
		key := pageKey{page.LogicalPage, page.PDFPage}
		segments, found := allowed[key]
		_, duplicate := seen[key]
		if !found || duplicate || strings.TrimSpace(page.Content) == "" ||
			!validSHA256(page.ContentDigest) || sha256Hex(page.Content) != page.ContentDigest ||
			!sameVerifiedTextSegments(segments, page.SegmentRefs) {
			return fmt.Errorf("grounding: verified page differs from frozen source")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func sameVerifiedTextSegments(expected, actual []string) bool {
	if len(expected) == 0 || len(expected) != len(actual) {
		return false
	}
	remaining := make(map[string]struct{}, len(expected))
	for _, segment := range expected {
		remaining[segment] = struct{}{}
	}
	for _, segment := range actual {
		if _, found := remaining[segment]; !found {
			return false
		}
		delete(remaining, segment)
	}
	return len(remaining) == 0
}

// locateVerifiedTextPage 只接受完整知识点或可靠目录课时名，不用关键词排名代替相关性。
func locateVerifiedTextPage(page k12.VerifiedTextbookPage, knowledgePoint string) (string, []string) {
	query := compactGroundingTerm(knowledgePoint)
	if query == "" {
		return "", nil
	}
	var terms []string
	seen := map[string]struct{}{}
	for _, title := range page.LessonTitles {
		term := compactGroundingTerm(title)
		if usableGroundingTerm(term) && strings.Contains(query, term) {
			if _, duplicate := seen[term]; !duplicate {
				terms = append(terms, strings.TrimSpace(title))
				seen[term] = struct{}{}
			}
		}
	}
	if len(terms) > 0 {
		return "exact_lesson_title", terms
	}
	content := compactGroundingTerm(page.Content)
	for _, value := range strings.Split(knowledgePoint, "、") {
		term := compactGroundingTerm(value)
		if usableGroundingTerm(term) && strings.Contains(content, term) {
			if _, duplicate := seen[term]; !duplicate {
				terms = append(terms, strings.TrimSpace(value))
				seen[term] = struct{}{}
			}
		}
	}
	if len(terms) > 0 {
		return "exact_knowledge_term", terms
	}
	return "", nil
}

func compactGroundingTerm(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

func usableGroundingTerm(value string) bool {
	switch strings.ToLower(value) {
	case "", "其他", "未知", "unknown":
		return false
	}
	return strings.ContainsFunc(value, unicode.IsLetter)
}
