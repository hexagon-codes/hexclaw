package usecase

import (
	"context"
	"fmt"
	"strings"
	"unicode"

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
		// 来源证明使用原题干；开头的精确来源标签只在规范投影中移除，由Origin统一展示。
		content.Examples[i].PromptMD = unitSummaryExamplePrompt(content.Examples[i].PromptMD)
	}
	content.TransferChecks = append([]k12.UnitSummaryTransferCheck(nil), content.TransferChecks...)
	for i := range content.TransferChecks {
		content.TransferChecks[i].QuestionMD = unitSummaryExamplePrompt(content.TransferChecks[i].QuestionMD)
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

// 精确的开头来源自称由应用统一标注；题干内部提及、数学括号和原始响应保持不变。
func unitSummaryExamplePrompt(prompt string) string {
	trimmed := strings.TrimLeftFunc(prompt, unicode.IsSpace)
	for _, label := range []string{"【教材题】", "【教材例题】", "【教材练习】", "【AI创作例题】", "【原资料AI诊断例】", "【原资料教材题整理】", "【保留原资料AI诊断例】"} {
		if rest, ok := strings.CutPrefix(trimmed, label); ok {
			return strings.TrimLeftFunc(rest, unicode.IsSpace)
		}
	}
	label, rest, closed := strings.Cut(trimmed, "】")
	label, opened := strings.CutPrefix(label, "【")
	if !closed || !opened {
		return prompt
	}
	positiveDecimal := func(value string) bool {
		return value != "" && strings.Trim(value, "0123456789") == "" && strings.Trim(value, "0") != ""
	}
	// 组合标签只允许完整的编辑元数据；数学条件或未知内容不随来源声明一起删除。
	textbook := false
	for _, part := range strings.Split(strings.NewReplacer("；", ";", "·", ";").Replace(label), ";") {
		part = strings.TrimSpace(part)
		if part == "代表例" || part == "迁移检查" {
			continue
		}
		if goal, ok := strings.CutPrefix(part, "对应目标"); ok {
			goal = strings.TrimSpace(goal)
			if goal == "" {
				return prompt
			}
			for _, r := range goal {
				if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
					return prompt
				}
			}
			continue
		}
		pageLabel, ok := strings.CutPrefix(part, "教材第")
		if !ok {
			return prompt
		}
		page, suffix, hasPage := strings.Cut(pageLabel, "页")
		if !hasPage || !positiveDecimal(page) {
			return prompt
		}
		if suffix != "" {
			question, hasQuestion := strings.CutPrefix(suffix, "第")
			if !hasQuestion {
				return prompt
			}
			question, suffix, hasQuestion = strings.Cut(question, "题")
			if !hasQuestion || !positiveDecimal(question) || suffix != "" && suffix != "，附解释要求" {
				return prompt
			}
		}
		textbook = true
	}
	if !textbook {
		return prompt
	}
	return strings.TrimLeftFunc(rest, unicode.IsSpace)
}
