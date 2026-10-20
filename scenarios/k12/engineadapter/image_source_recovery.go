package engineadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/knowledge"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// WithImageSourcePageLookup 只接入现有允许的搜索与读取，不改变工具设置。
func (a *GroundingAdapter) WithImageSourcePageLookup(lookup func(context.Context, string, string) ([]engine.ReferenceSourcePage, error)) *GroundingAdapter {
	a.sourcePages = lookup
	return a
}

// 教材相似命中只作候选；练习指令须与完整可见题面及来源上下文吻合。
func (a *GroundingAdapter) FindImageSourceCandidate(ctx context.Context, agent, subject, grade, visible string, gap k12.RecognitionImageSourceGapV1) (k12.RecognitionImageSourceRecoveryV1, error) {
	result := k12.RecognitionImageSourceRecoveryV1{Gap: gap, State: "no_verified_bound_textbook"}
	reader, ok := a.kb.(kbHitQuerier)
	if gap.Kind == "handwriting" {
		result.State = "handwriting_requires_original_pixels"
		return result, nil
	}
	query := strings.TrimSpace(strings.Join([]string{subject, grade, visible, gap.Before, gap.After}, " "))
	var hits []knowledge.SearchHit
	if ok {
		_, hits, _ = reader.QueryHitsWithFilter(ctx, query, a.topK, knowledge.Filter{SourceTypes: []string{"upload"}})
	}
	var adopted []k12.RecognitionImageSourceRecoveryV1
	for _, hit := range hits {
		if !strings.Contains(hit.DocTitle, subject) || hit.DocumentGeneration < 1 || hit.SourceDigest == "" || hit.CitationDigest == "" {
			continue
		}
		result.State, result.SourceKind = "candidate_requires_source_verification", "knowledge_candidate"
		result.DocumentID, result.DocumentGeneration, result.SourceDigest = hit.DocID, hit.DocumentGeneration, hit.SourceDigest
		result.CitationDigest, result.PDFPage = hit.CitationDigest, hit.PageStart
		result.VectorRevisionID, result.QueryDigest = hit.SemanticRevisionID, "sha256:"+sha256Hex(query)
		result.SourceOffsetStart, result.SourceOffsetEnd = hit.SourceOffsetStart, hit.SourceOffsetEnd
		if sourceExerciseContextMatches(hit.DocTitle, subject, grade) && strings.TrimPrefix(hit.CitationDigest, "sha256:") == sha256Hex(hit.Content) {
			if text, matched := exactVisibleExerciseGap(hit.Content, visible, gap); matched {
				verified := result
				verified.State, verified.AdoptedText, verified.SourceKind = "verified", text, "knowledge_same_exercise"
				adopted = append(adopted, verified)
			}
		}
	}
	if len(adopted) > 0 {
		return uniqueSourceRecovery(adopted), nil
	}
	// 只有本地证据不足才联网；返回值必须是读取到的正文，而不是搜索摘要。
	if a.sourcePages != nil {
		pages, err := a.sourcePages(ctx, agent, query)
		if err != nil {
			result.State = "source_lookup_failed"
			return result, nil
		}
		for _, page := range pages {
			if page.URL == "" || strings.TrimPrefix(page.Digest, "sha256:") != sha256Hex(page.Content) {
				continue
			}
			if sourceExerciseContextMatches(page.Content, subject, grade) {
				if text, matched := exactVisibleExerciseGap(page.Content, visible, gap); matched {
					adopted = append(adopted, k12.RecognitionImageSourceRecoveryV1{Gap: gap, State: "verified", AdoptedText: text, SourceKind: "web_same_exercise", URL: page.URL, SourceDigest: page.Digest, CitationDigest: page.Digest, QueryDigest: "sha256:" + sha256Hex(query)})
				}
			}
		}
		if len(adopted) > 0 {
			return uniqueSourceRecovery(adopted), nil
		}
	}
	return result, nil
}

func sourceExerciseContextMatches(text, subject, grade string) bool {
	if grade == "" || !strings.Contains(text, subject) || !strings.Contains(text, grade) {
		return false
	}
	return strings.Contains(text, "练习") || strings.Contains(text, "随堂") || strings.Contains(text, "校本") || strings.Contains(text, "同步")
}

// 完整可见题面必须在同一来源连续出现；保留字词、数字、否定及标点差异。
func exactVisibleExerciseGap(content, visible string, gap k12.RecognitionImageSourceGapV1) (string, bool) {
	if gap.Kind != "instruction" && gap.Kind != "condition" {
		return "", false
	}
	normalize := func(text string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, text)
	}
	visible, before, after := normalize(visible), normalize(gap.Before), normalize(gap.After)
	if before == "" || after == "" || strings.Count(visible, before) != 1 || strings.Count(visible, after) != 1 {
		return "", false
	}
	start, end := strings.Index(visible, before)+len(before), strings.Index(visible, after)
	if end < start {
		return "", false
	}
	prefix, suffix := visible[:start], strings.TrimRight(visible[end:], "_＿")
	if utf8.RuneCountInString(prefix) < 24 || utf8.RuneCountInString(suffix) < 8 {
		return "", false
	}
	content = normalize(content)
	if strings.Count(content, prefix) != 1 {
		return "", false
	}
	start = strings.Index(content, prefix) + len(prefix)
	end = strings.Index(content[start:], suffix)
	if end <= 0 {
		return "", false
	}
	text := content[start : start+end]
	if utf8.RuneCountInString(text) > 80 || strings.ContainsAny(text, "_＿□") {
		return "", false
	}
	return text, true
}

func uniqueSourceRecovery(results []k12.RecognitionImageSourceRecoveryV1) k12.RecognitionImageSourceRecoveryV1 {
	result := results[0]
	for _, other := range results[1:] {
		if other.AdoptedText != result.AdoptedText {
			result.State, result.AdoptedText = "ambiguous_source_match", ""
			return result
		}
	}
	return result
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
