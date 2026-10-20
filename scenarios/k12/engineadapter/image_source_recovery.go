package engineadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/hexagon-codes/hexclaw/knowledge"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// 没有已核验的学科绑定时只记录库内候选，不能把全库相似文本冒充当前教材。
func (a *GroundingAdapter) FindImageSourceCandidate(ctx context.Context, subject, grade string, gap k12.RecognitionImageSourceGapV1) (k12.RecognitionImageSourceRecoveryV1, error) {
	result := k12.RecognitionImageSourceRecoveryV1{Gap: gap, State: "no_verified_bound_textbook"}
	reader, ok := a.kb.(kbHitQuerier)
	if !ok || gap.Kind == "handwriting" {
		return result, nil
	}
	query := strings.TrimSpace(strings.Join([]string{subject, grade, gap.Before, gap.After}, " "))
	_, hits, err := reader.QueryHitsWithFilter(ctx, query, a.topK, knowledge.Filter{SourceTypes: []string{"upload"}})
	if err != nil {
		return result, err
	}
	for _, hit := range hits {
		if !strings.Contains(hit.DocTitle, subject) || hit.DocumentGeneration < 1 || hit.SourceDigest == "" || hit.CitationDigest == "" {
			continue
		}
		result.State, result.SourceKind = "candidate_requires_source_verification", "knowledge_candidate"
		result.DocumentID, result.DocumentGeneration, result.SourceDigest = hit.DocID, hit.DocumentGeneration, hit.SourceDigest
		result.CitationDigest, result.PDFPage = hit.CitationDigest, hit.PageStart
		result.VectorRevisionID, result.QueryDigest = hit.SemanticRevisionID, "sha256:"+sha256Hex(query)
		result.SourceOffsetStart, result.SourceOffsetEnd = hit.SourceOffsetStart, hit.SourceOffsetEnd
		break
	}
	return result, nil
}

// 教材只能补证引文；练习指令、关键条件和笔迹不由课文相似度推定。
func (a *GroundingAdapter) RecoverImageSource(ctx context.Context, snapshot usecase.GroundingSnapshot, gap k12.RecognitionImageSourceGapV1) (k12.RecognitionImageSourceRecoveryV1, error) {
	result := k12.RecognitionImageSourceRecoveryV1{Gap: gap, State: "unresolved"}
	result.TextbookBindingID, result.TextbookManifestID = snapshot.TextbookBindingID, snapshot.TextbookManifestID
	result.Edition, result.Volume, result.VectorRevisionID = snapshot.Edition, snapshot.Volume, snapshot.VectorRevisionID
	if gap.Kind == "handwriting" {
		result.State = "handwriting_requires_original_pixels"
		return result, nil
	}
	if gap.Kind != "quotation" {
		result.State = "same_exercise_source_required"
		return result, nil
	}
	if utf8.RuneCountInString(gap.Before) < 6 || utf8.RuneCountInString(gap.After) < 6 {
		result.State = "insufficient_visible_anchors"
		return result, nil
	}
	// 精确来源路径只读冻结教材页，不触发新索引或更换 embedding 配置。
	if a.verifiedReader == nil || snapshot.SourceMode != usecase.GroundingSourceModeVerifiedText {
		return result, nil
	}
	pages, err := a.verifiedReader.ReadVerifiedTextbookPages(ctx, k12.VerifiedTextbookReadRequest{OwnerID: snapshot.OwnerID, AgentName: snapshot.AgentName, Subject: imageSourceSubject(snapshot.Subject), Scope: k12.TextbookGroundingScope{TextbookBindingID: snapshot.TextbookBindingID, TextbookManifestID: snapshot.TextbookManifestID, DocumentID: snapshot.DocumentID, DocumentGeneration: snapshot.DocumentGeneration, SourceDigest: snapshot.SourceDigest, Edition: snapshot.Edition, Volume: snapshot.Volume, SegmentRefs: append([]string(nil), snapshot.SegmentRefs...), PageRefs: cloneGroundingPageRefs(snapshot.PageRefs)}})
	if err != nil {
		return result, err
	}
	if err = validateVerifiedTextPages(snapshot, pages); err != nil {
		return result, err
	}
	for _, page := range pages {
		before := strings.Index(page.Content, gap.Before)
		if before < 0 || strings.Count(page.Content, gap.Before) != 1 {
			continue
		}
		start := before + len(gap.Before)
		after := strings.Index(page.Content[start:], gap.After)
		if after <= 0 {
			continue
		}
		text := page.Content[start : start+after]
		if utf8.RuneCountInString(text) > 80 {
			continue
		}
		if result.State == "verified" {
			result.State, result.AdoptedText = "ambiguous_source_match", ""
			return result, nil
		}
		digest := sha256.Sum256([]byte(page.Content))
		result.State, result.AdoptedText, result.SourceKind = "verified", text, "textbook_quotation"
		result.DocumentID, result.DocumentGeneration, result.SourceDigest = snapshot.DocumentID, snapshot.DocumentGeneration, snapshot.SourceDigest
		result.CitationDigest, result.LogicalPage, result.PDFPage = "sha256:"+hex.EncodeToString(digest[:]), page.LogicalPage, page.PDFPage
	}
	return result, nil
}

func imageSourceSubject(subject string) string {
	switch subject {
	case "语文":
		return "chinese"
	case "数学":
		return "math"
	case "英语":
		return "english"
	case "科学":
		return "science"
	}
	return subject
}
