package usecase

import (
	"fmt"
	"slices"
	"strings"
)

const teachingAuditCoreOnly = "teaching_audit_core_only"

// 降级只逐字投影已验证解答的明确答案和完整过程，不消费未审核教学候选。
func degradeVerifiedReportTeaching(solved SolveHomeworkResult, reason string) (SolveHomeworkResult, bool) {
	if solved.OutOfScope || !photoEvidenceTrusted(solved.Evidence) || strings.TrimSpace(reason) == "" {
		return solved, false
	}
	content, ok := verifiedSolutionReportContent(solved.Solution)
	if !ok {
		return solved, false
	}
	solved.ReportContent = content
	solved.Generation = nil
	solved.ReportDegradationReason = reason
	return solved, true
}

// 只投影原解明确标出的答案与步骤。调用方负责验证这些内容的来源。
func verifiedSolutionReportContent(solution string) (*TeachingReportContentDraft, bool) {
	answers, explicit := verifiedReportAnswerSections(solution)
	steps := verifiedFullSolutionSteps(solution)
	if !explicit || len(steps) == 0 {
		return nil, false
	}
	blocks := make([]TeachingReportDraftBlock, 0, len(steps))
	for i, step := range steps {
		blocks = append(blocks, TeachingReportDraftBlock{ID: fmt.Sprintf("source-step-%d", i+1), ProblemIDs: []string{"current"}, Text: step})
	}
	return &TeachingReportContentDraft{
		Version:  TeachingReportContentContractVersion,
		Items:    []TeachingReportDraftItem{{ProblemID: "current", ShortTitle: "解题结果", Answers: answers}},
		Sections: []TeachingReportDraftSection{{ID: "source-solution", Title: "解题过程", ProblemIDs: []string{"current"}, Blocks: blocks}},
	}, true
}

// 多小问可能各有明确答案段；只按已有标签取原文，不把最后一段当成全部答案。
func verifiedReportAnswerSections(solution string) ([]TeachingReportDraftAnswer, bool) {
	lines := strings.Split(strings.ReplaceAll(solution, "\r\n", "\n"), "\n")
	var answers []TeachingReportDraftAnswer
	for i := 0; i < len(lines); i++ {
		inline, explicit := splitExplicitAnswerLabel(lines[i])
		if !explicit {
			continue
		}
		content := []string{inline}
		for i+1 < len(lines) {
			next := lines[i+1]
			_, anotherAnswer := splitExplicitAnswerLabel(next)
			if isMarkdownHeading(next) || anotherAnswer {
				break
			}
			i++
			content = append(content, next)
		}
		answer := strings.TrimSpace(strings.Join(content, "\n"))
		if answer == "" {
			return nil, false
		}
		answers = append(answers, TeachingReportDraftAnswer{ID: fmt.Sprintf("source-answer-%d", len(answers)+1), Text: answer})
	}
	return answers, len(answers) > 0
}

// 持久回放须与同一解答的确定性投影一致；降级标记不授予任意正文可信资格。
func degradedReportParentGuide(solved SolveHomeworkResult) (ParentTeachingGuide, bool) {
	if solved.ReportDegradationReason == "" {
		return ParentTeachingGuide{}, false
	}
	if solved.ReportDegradationReason == teachingAuditCoreOnly {
		guide, ok := auditedCoreReportGuide(solved)
		return guide, ok && solved.ReportContent != nil && modelInvocationResultDigest(guide.ReportContent) == modelInvocationResultDigest(solved.ReportContent)
	}
	projected, ok := degradeVerifiedReportTeaching(solved, solved.ReportDegradationReason)
	if !ok || solved.ReportContent == nil || modelInvocationResultDigest(projected.ReportContent) != modelInvocationResultDigest(solved.ReportContent) {
		return ParentTeachingGuide{}, false
	}
	var answers []string
	for _, answer := range projected.ReportContent.Items[0].Answers {
		answers = append(answers, answer.Text)
	}
	return ParentTeachingGuide{Answer: strings.Join(answers, "\n\n"), FullSolutionSteps: verifiedFullSolutionSteps(solved.Solution), ReportContent: projected.ReportContent}, true
}

// 独立补充审核的请求绑定原解、答案和步骤。只投影这两个实际通过的字段；
// 原数值证据等级、整份指南的否决以及物理回执维持各自的事实。
func auditedCoreReportGuide(solved SolveHomeworkResult) (ParentTeachingGuide, bool) {
	g := solved.Generation
	source := firstNonEmpty(solved.Evidence.SolverOutputDigest, teachingReportResourceDigest([]byte(solved.Solution)))
	if solved.OutOfScope || solved.Evidence.Verdict != VerdictAgree || g == nil ||
		g.OutputVersion != SolveOutputWithTeachingReportV2 || g.GuideAudit != "INVALID" || g.SourceSolutionDigest != source ||
		g.AdaptationGenerationID == "" || g.AdaptationAuditID == "" || g.ParentGuideCandidate == nil {
		return ParentTeachingGuide{}, false
	}
	core := g.ParentGuideCandidate
	draft, ok := verifiedSolutionReportContent(solved.Solution)
	if !ok || len(draft.Items[0].Answers) != 1 ||
		normalizeAnswerAnchorText(draft.Items[0].Answers[0].Text) != normalizeAnswerAnchorText(core.Answer) ||
		!slices.Equal(verifiedFullSolutionSteps(solved.Solution), core.FullSolutionSteps) {
		return ParentTeachingGuide{}, false
	}
	draft.Items[0].Answers[0].Text = core.Answer
	return ParentTeachingGuide{Answer: core.Answer, FullSolutionSteps: core.FullSolutionSteps, ReportContent: draft}, true
}

func validateDegradedReportGuide(guide ParentTeachingGuide, solved SolveHomeworkResult) error {
	expected, ok := degradedReportParentGuide(solved)
	// 图示和其失败记录由后续宿主追加，教学正文仍必须是同一冻结原解的投影。
	guide.TeachingDiagrams, guide.TeachingDiagramWarnings = nil, nil
	if !ok || modelInvocationResultDigest(guide) != modelInvocationResultDigest(expected) {
		return fmt.Errorf("degraded teaching content differs from the verified solution")
	}
	return nil
}
