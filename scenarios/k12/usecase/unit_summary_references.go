package usecase

import (
	"context"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 引用别名只映射本次实际冻结来源；未知或重名不猜，模型原始JSON仍在调用账本。
func (c *UnitSummaryCoordinator) normalizeContentReferences(ctx context.Context, owner string, content k12.UnitSummaryContentV1, frozen k12.UnitSummaryContext) (k12.UnitSummaryContentV1, error) {
	// 没有真实学生证据时不交付个人表现字段；教材知识与通用家长讲法保持原内容。
	if len(frozen.Evidence) == 0 {
		content.PersonalGuidance = []k12.UnitSummaryPersonalGuidance{}
	}
	aliases := map[string]map[string]bool{}
	sourceTexts := map[string]string{}
	add := func(alias, ref string) {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return
		}
		if aliases[alias] == nil {
			aliases[alias] = map[string]bool{}
		}
		aliases[alias][ref] = true
	}
	for _, source := range append(append([]k12.UnitSummaryFrozenSource{}, frozen.Sources...), frozen.Evidence...) {
		sourceTexts[source.Ref] = source.Text
		add(source.Ref, source.Ref)
		add(source.DocumentID, source.Ref)
		add(source.Label, source.Ref)
		if source.DocumentID != "" && source.Generation > 0 {
			var name string
			err := c.Deps.Records.DB().QueryRowContext(ctx, `SELECT original_name FROM kb_ingest_document_sources WHERE owner_id=? AND document_id=? AND content_generation=? AND blob_sha256=?`, owner, source.DocumentID, source.Generation, source.SourceDigest).Scan(&name)
			if err == nil {
				add(name, source.Ref)
			}
		}
	}
	refs := func(values []string) ([]string, error) {
		out := make([]string, 0, len(values))
		seen := map[string]bool{}
		for _, value := range values {
			matches := aliases[strings.TrimSpace(value)]
			if len(matches) != 1 {
				return nil, fmt.Errorf("unit summary source reference is unknown or ambiguous")
			}
			for ref := range matches {
				if !seen[ref] {
					out = append(out, ref)
					seen[ref] = true
				}
			}
		}
		return out, nil
	}
	content.Goals = append([]k12.UnitSummaryGoal(nil), content.Goals...)
	for i := range content.Goals {
		mapped, err := refs(content.Goals[i].SourceRefs)
		if err != nil {
			return content, err
		}
		content.Goals[i].SourceRefs = mapped
	}
	content.Knowledge = append([]k12.UnitSummaryKnowledge(nil), content.Knowledge...)
	for i := range content.Knowledge {
		mapped, err := refs(content.Knowledge[i].SourceRefs)
		if err != nil {
			return content, err
		}
		content.Knowledge[i].SourceRefs = mapped
	}
	content.Examples = append([]k12.UnitSummaryExample(nil), content.Examples...)
	for i := range content.Examples {
		mapped, err := refs(content.Examples[i].SourceRefs)
		if err != nil {
			return content, err
		}
		content.Examples[i].SourceRefs = mapped
		if content.Examples[i].Origin == "source" {
			original := false
			prompt := strings.TrimSpace(content.Examples[i].PromptMD)
			for _, ref := range mapped {
				if prompt != "" && strings.Contains(sourceTexts[ref], prompt) {
					original = true
					break
				}
			}
			// 不能证明为实际原文的改写/新任务归为AI创作，不改变题意或知识来源引用。
			if !original {
				content.Examples[i].Origin = "ai_created"
			}
		}
	}
	content.PersonalGuidance = append([]k12.UnitSummaryPersonalGuidance{}, content.PersonalGuidance...)
	for i := range content.PersonalGuidance {
		mapped, err := refs(content.PersonalGuidance[i].EvidenceRefs)
		if err != nil {
			return content, err
		}
		content.PersonalGuidance[i].EvidenceRefs = mapped
	}
	return content, nil
}
