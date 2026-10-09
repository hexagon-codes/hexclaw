package usecase

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/toolkit/util/idgen"
)

var unitSummaryTimezone = time.FixedZone("Asia/Shanghai", 8*3600)

const unitSummaryStandardRenderContract = "unit-summary-v1"
const unitSummaryPreviousBriefRenderContract = "unit-summary-brief-v2"
const unitSummaryPreviousCompactRenderContract = "unit-summary-brief-v3"
const unitSummaryBriefRenderContract = "unit-summary-brief-v4"

func unitSummaryRenderContract(requirements k12.UnitSummaryRequirements) string {
	if requirements.Format == "brief" {
		return unitSummaryBriefRenderContract
	}
	return unitSummaryStandardRenderContract
}

// 固定应用样式仅进入brief私有RenderMarkdown，不读取用户来源头元数据或开放新的renderer允许项。
func unitSummaryRenderMarkdown(markdown, contract string) string {
	if contract == unitSummaryPreviousBriefRenderContract {
		return "```{=typst}\n#set par(spacing: 0.35em)\n#show heading: set block(above: 0.55em, below: 0.25em)\n```\n\n" + markdown
	}
	if contract == unitSummaryPreviousCompactRenderContract {
		return "```{=typst}\n#set par(spacing: 0.1em)\n#show heading: set block(above: 0.2em, below: 0.1em)\n```\n\n" + markdown
	}
	if contract == unitSummaryBriefRenderContract {
		return "```{=typst}\n#set par(spacing: 0.1em)\n#show heading: set block(above: 0.2em, below: 0.1em)\n#set block(spacing: 0.1em)\n#set list(spacing: 0.1em, tight: true)\n```\n\n" + markdown
	}
	return markdown
}

func unitSummaryFilename(ctx k12.UnitSummaryContext, req k12.UnitSummaryRequirements, date string, version int) string {
	clean := func(v string) string {
		return strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
				return '-'
			}
			return r
		}, v))
	}
	grade := ctx.GradeTerm
	if strings.HasSuffix(grade, "上") || strings.HasSuffix(grade, "下") {
		grade += "册"
	}
	unit := ctx.UnitTitle
	if ctx.UnitNumber != "" {
		if n, err := strconv.Atoi(ctx.UnitNumber); err == nil {
			unit = fmt.Sprintf("第%02d单元-%s", n, unit)
		} else {
			unit = ctx.UnitNumber + "-" + unit
		}
	}
	typ := "知识梳理"
	if req.Format == "brief" {
		typ += "-精简版"
	}
	if ctx.Coverage.Level == "partial" {
		typ += "-部分资料"
	}
	parts := []string{}
	for _, part := range []string{ctx.ChildName, k12.UnitSummarySubjectLabel(ctx.Subject) + grade, ctx.TextbookEdition, unit, typ, date, fmt.Sprintf("v%02d", version)} {
		if part = clean(part); part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "_") + ".pdf"
}

// Markdown是规范内容的唯一确定性投影，PDF包含所有讲法与参考，不读取网页折叠状态。
func UnitSummaryMarkdown(content k12.UnitSummaryContentV1, date string, version int) string {
	return unitSummaryMarkdown(content, date, version, false)
}

func unitSummaryMarkdown(content k12.UnitSummaryContentV1, date string, version int, brief bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s · 生成日期 %s · v%02d\n\n", content.Title, k12.UnitSummarySubjectLabel(content.Subject), date, version)
	if content.Coverage.Level == "partial" && !brief {
		b.WriteString("> 部分资料：仅覆盖本次实际读取的材料。\n\n")
	}
	b.WriteString("## 这次可以这样带孩子复习\n\n")
	for _, p := range content.ParentPlan {
		fmt.Fprintf(&b, "- %s\n", p)
	}
	b.WriteString("\n## 知识联系与学习目标\n\n")
	for _, g := range content.Goals {
		fmt.Fprintf(&b, "- %s\n", g.Text)
	}
	b.WriteString("\n## 主要知识与方法\n\n")
	for _, k := range content.Knowledge {
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", k.Title, k.BodyMD)
	}
	b.WriteString("## 代表例与家长讲解指南\n\n")
	for i, e := range content.Examples {
		origin := "来源材料例题"
		if e.Origin == "ai_created" {
			origin = "AI 创作例题"
		}
		// 语义heading沿既有Pandoc/Typst的标题连带正文规则，不能用孤立粗体段冒充标题。
		if brief {
			fmt.Fprintf(&b, "### 例 %d · %s\n\n%s\n\n#### 思路与关键步骤\n\n%s\n\n**先问什么：** %s\n\n**怎样解释：** %s\n\n**卡住时提示：** %s\n\n", i+1, origin, e.PromptMD, e.DemonstrationMD, e.ParentGuide.Ask, e.ParentGuide.Explain, e.ParentGuide.Hint)
		} else {
			fmt.Fprintf(&b, "### 例 %d · %s\n\n%s\n\n#### 思路与关键步骤\n\n%s\n\n#### 先问什么\n\n%s\n\n#### 怎样解释\n\n%s\n\n#### 卡住时提示\n\n%s\n\n", i+1, origin, e.PromptMD, e.DemonstrationMD, e.ParentGuide.Ask, e.ParentGuide.Explain, e.ParentGuide.Hint)
		}
		if e.ParentGuide.Alternative != "" {
			fmt.Fprintf(&b, "#### 替代讲法（按卡点使用）\n\n%s\n\n", e.ParentGuide.Alternative)
		}
	}
	b.WriteString("## 迁移检查\n\n")
	for i, q := range content.TransferChecks {
		fmt.Fprintf(&b, "### 检查 %d\n\n%s\n\n", i+1, q.QuestionMD)
	}
	b.WriteString("## 参考解读\n\n")
	explanations := map[string]string{}
	for _, r := range content.ReferenceExplanations {
		explanations[r.CheckID] = r.ExplanationMD
	}
	for i, q := range content.TransferChecks {
		fmt.Fprintf(&b, "### 检查 %d 的参考解读\n\n%s\n\n", i+1, explanations[q.ID])
	}
	if len(content.CommonPitfalls) > 0 {
		b.WriteString("## 常见卡点\n\n")
		for _, p := range content.CommonPitfalls {
			fmt.Fprintf(&b, "- %s\n", p)
		}
		b.WriteString("\n")
	}
	if len(content.PersonalGuidance) > 0 {
		b.WriteString("## 当前孩子的复习提示\n\n")
		for _, p := range content.PersonalGuidance {
			fmt.Fprintf(&b, "- %s\n", p.Text)
		}
		b.WriteString("\n")
	} else if !brief {
		b.WriteString("## 指导适用范围\n\n无对应作答证据，仅提供通用复习指导；阅读资料不代表已掌握。\n\n")
	}
	if brief {
		// 合组只减少重复的结构留白；每个规范来源、覆盖范围和适用说明均完整保留。
		b.WriteString("## 来源与适用范围\n\n")
		parts := []string{}
		if content.Coverage.Level == "partial" {
			parts = append(parts, "部分资料：仅覆盖本次实际读取的材料。")
		}
		if len(content.PersonalGuidance) == 0 {
			parts = append(parts, "无对应作答证据，仅提供通用复习指导；阅读资料不代表已掌握。")
		}
		labels := []string{}
		for _, source := range content.Sources {
			label := source.Label
			if source.Page != "" {
				label += " · 第" + source.Page + "页"
			}
			labels = append(labels, label)
		}
		// 仅逐项同值、同数量和同顺序时共用展示；含页码的来源不与更宽的覆盖主题混为一谈。
		sameCoverage := len(labels) > 0 && len(labels) == len(content.Coverage.Covered)
		if sameCoverage {
			for i, label := range labels {
				if label != content.Coverage.Covered[i] {
					sameCoverage = false
					break
				}
			}
		}
		if sameCoverage {
			parts = append(parts, "来源／已覆盖："+strings.Join(labels, "、")+"。")
		} else {
			if len(labels) > 0 {
				parts = append(parts, "来源："+strings.Join(labels, "、")+"。")
			}
			if len(content.Coverage.Covered) > 0 {
				parts = append(parts, "已覆盖："+strings.Join(content.Coverage.Covered, "、")+"。")
			}
		}
		if len(content.Coverage.Missing) > 0 {
			parts = append(parts, "未覆盖或未核对："+strings.Join(content.Coverage.Missing, "、")+"。")
		}
		parts = append(parts, "本资料为 AI 归纳，来源原文与 AI 创作例题已区分。")
		b.WriteString(strings.Join(parts, " ") + "\n")
		return unitSummaryMarkdownBlocks(b.String())
	}
	b.WriteString("## 来源与覆盖范围\n\n")
	for _, s := range content.Sources {
		label := s.Label
		if s.Page != "" {
			label += " · 第" + s.Page + "页"
		}
		fmt.Fprintf(&b, "- %s\n", label)
	}
	if len(content.Coverage.Covered) > 0 {
		fmt.Fprintf(&b, "\n已覆盖：%s。\n", strings.Join(content.Coverage.Covered, "、"))
	}
	if len(content.Coverage.Missing) > 0 {
		fmt.Fprintf(&b, "\n未覆盖或未核对：%s。\n", strings.Join(content.Coverage.Missing, "、"))
	}
	if len(content.Coverage.Covered) == 0 && len(content.Coverage.Missing) == 0 {
		b.WriteString("\n")
	}
	b.WriteString("本资料为 AI 归纳，来源原文与 AI 创作例题已区分。\n")
	return unitSummaryMarkdownBlocks(b.String())
}

// 只规范单元公开投影的块边界；不修改原生成JSON、冻结来源、词句或代码内容。
func unitSummaryMarkdownBlocks(markdown string) string {
	lines := strings.Split(markdown, "\n")
	out := make([]string, 0, len(lines))
	fence := ""
	mathBlock := false
	listLine := func(line string) bool {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ") {
			return true
		}
		prefix, _, ok := strings.Cut(line, " ")
		if !ok || len(prefix) < 2 || !strings.ContainsAny(prefix[len(prefix)-1:], ".)") {
			return false
		}
		_, err := strconv.Atoi(prefix[:len(prefix)-1])
		return err == nil
	}
	tableSeparator := func(line string) bool {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
			return false
		}
		for _, cell := range strings.Split(strings.Trim(line, "|"), "|") {
			cell = strings.Trim(strings.TrimSpace(cell), ":")
			if cell == "" || strings.Trim(cell, "-") != "" {
				return false
			}
		}
		return true
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if fence != "" {
			out = append(out, line)
			if strings.HasPrefix(trimmed, fence) && strings.TrimSpace(strings.TrimLeft(trimmed, fence[:1])) == "" {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, trimmed[:1]))]
			out = append(out, line)
			continue
		}
		if trimmed == "$$" {
			mathBlock = !mathBlock
			out = append(out, line)
			continue
		}
		if !mathBlock && !strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "\t") && len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			previous := strings.TrimSpace(out[len(out)-1])
			tableHeader := strings.HasPrefix(trimmed, "|") && i+1 < len(lines) && tableSeparator(lines[i+1])
			if tableHeader || listLine(line) && !listLine(previous) {
				out = append(out, "")
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func (c *UnitSummaryCoordinator) publish(ctx context.Context, a k12.UnitSummaryAttempt) error {
	if a.Content == nil || a.ContentDigest == "" {
		return fmt.Errorf("unit summary canonical content checkpoint is unavailable")
	}
	if err := k12.ValidateUnitSummaryContent(*a.Content, a.Context); err != nil {
		_, e := c.fail(a, "content_invalid", err)
		return e
	}
	if a.Requirements.Format == "brief" {
		// 语义复用指纹绑定brief输出合同，不能因正文相同返回旧两页排版或覆写其冻结文件。
		digest := unitSummaryContentDigest(*a.Content, a.Context, a.Requirements)
		if digest != a.ContentDigest {
			next := a
			next.ContentDigest = digest
			updated, err := c.checkpoint(ctx, a, next)
			if err != nil {
				return err
			}
			a = updated
		}
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 同内容复用和旧请求superseded也必须在Store事务重新核对head与成功水位。
		next := a
		next.State = "publishing"
		next.Stage = "publish"
		var err error
		a, err = c.checkpoint(ctx, a, next)
		if err != nil {
			return err
		}
		if _, _, err = c.Deps.Records.PublishUnitSummary(ctx, a, nil); err == nil {
			return nil
		} else if !errors.Is(err, k12storage.ErrUnitSummaryCAS) {
			_, e := c.fail(a, "persist_failed", err)
			return e
		}
		doc, err := c.Deps.Records.GetUnitSummaryDocument(ctx, a.OwnerID, a.AgentName, a.DocumentID)
		if err != nil {
			return err
		}
		date := time.Unix(c.Deps.now(), 0).In(unitSummaryTimezone).Format("2006-01-02")
		candidate := a.Candidate
		contract := unitSummaryRenderContract(a.Requirements)
		if candidate == nil || candidate.ExpectedHeadVersion != doc.HeadVersion || candidate.GeneratedDate != date || candidate.RenderContract != contract {
			markdown := unitSummaryMarkdown(*a.Content, date, doc.HeadVersion+1, a.Requirements.Format == "brief")
			candidate = &k12.UnitSummaryCandidate{CandidateID: "usc-" + idgen.ShortID(), ExpectedHeadVersion: doc.HeadVersion, CandidateVersion: doc.HeadVersion + 1, GeneratedDate: date, Timezone: "Asia/Shanghai", Filename: unitSummaryFilename(a.Context, a.Requirements, date, doc.HeadVersion+1), RenderContract: contract, CanonicalMarkdown: markdown, CanonicalDigest: k12.UnitSummaryDigest(markdown)}
			next = a
			next.Candidate = candidate
			next.State = "rendering"
			next.Stage = "render"
			a, err = c.checkpoint(ctx, a, next)
			if err != nil {
				return err
			}
		}
		if candidate.ArtifactID == "" {
			view, _, e := c.Deps.PreparePrintableArtifact(ctx, PreparePrintableArtifactRequest{AgentName: a.AgentName, SourceKind: k12.PrintSourceUnitSummary, SourceRef: a.DocumentID + ":" + candidate.CandidateID + ":" + candidate.RenderContract, Title: a.Content.Title, CanonicalMarkdown: candidate.CanonicalMarkdown, RenderMarkdown: unitSummaryRenderMarkdown(candidate.CanonicalMarkdown, candidate.RenderContract)})
			if e != nil {
				_, err = c.fail(a, "pdf_render_failed", e)
				return err
			}
			copy := *candidate
			copy.ArtifactID = view.Artifact.ArtifactID
			copy.ByteDigest = view.Render.ByteDigest
			candidate = &copy
			next = a
			next.Candidate = candidate
			next.ArtifactID = candidate.ArtifactID
			next.State = "publishing"
			next.Stage = "publish"
			a, err = c.checkpoint(ctx, a, next)
			if err != nil {
				return err
			}
		}
		render, e := c.Deps.Records.GetPrintArtifactRender(ctx, a.AgentName, candidate.ArtifactID)
		if e != nil {
			_, err = c.fail(a, "persist_failed", e)
			return err
		}
		revision := k12.UnitSummaryRevision{RevisionID: "usr-" + idgen.ShortID(), DocumentID: a.DocumentID, AgentName: a.AgentName, Version: candidate.CandidateVersion, AttemptID: a.AttemptID, Content: *a.Content, CanonicalMarkdown: candidate.CanonicalMarkdown, ContentDigest: a.ContentDigest, SourceSnapshot: unitSummaryPublicSources(a.Context.Sources), PersonalSnapshot: unitSummaryPublicSources(a.Context.Evidence), Requirements: a.Requirements, Context: a.Context, GeneratedAt: c.Deps.now(), GeneratedTimezone: candidate.Timezone, GeneratedDate: candidate.GeneratedDate, Filename: candidate.Filename, ArtifactID: candidate.ArtifactID, ByteDigest: candidate.ByteDigest, ByteSize: render.ByteSize}
		_, _, err = c.Deps.Records.PublishUnitSummary(ctx, a, &revision)
		if err == nil {
			return nil
		}
		if !errors.Is(err, k12storage.ErrUnitSummaryCAS) {
			_, e := c.fail(a, "persist_failed", err)
			return e
		}
		live, e := c.Deps.Records.GetUnitSummaryAttempt(ctx, a.OwnerID, a.AgentName, a.AttemptID)
		if e != nil {
			return e
		}
		if live.LeaseOwner != a.LeaseOwner || live.LeaseEpoch != a.LeaseEpoch || live.Revision != a.Revision {
			return k12storage.ErrUnitSummaryCAS
		}
		// 只有head或日期竞争导致候选变化时重排确定性PDF；已成功模型输入与输出不重跑。
	}
}
