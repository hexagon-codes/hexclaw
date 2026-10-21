package usecase

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

const gradingPDFProjectionVersion = "grading-pdf-v13"
const blankWorksheetPDFProjectionVersion = "blank-worksheet-pdf-v4"

// 空白卷的打印副本保留题面字面横线，去除答案的重复标签；冻结原文及评估回执不改写。
func renderBlankWorksheetPDF(entries []gradingFinalEntry, coverage k12.GradingFinalArtifactCoverageStatus) (string, bool) {
	projected := append([]gradingFinalEntry(nil), entries...)
	for i := range projected {
		question := &projected[i].question
		if question.Subject == "语文" && strings.TrimSpace(question.CanonicalMarkdown) == strings.TrimSpace(question.RawTranscription) {
			question.CanonicalMarkdown = strings.NewReplacer(`\_`, `\_`, "_", `\_`).Replace(question.CanonicalMarkdown)
		}
		if projected[i].assessment == nil {
			continue
		}
		assessment := *projected[i].assessment
		var item PhotoGradeItem
		if json.Unmarshal([]byte(assessment.ResultJSON), &item) != nil {
			return "", false
		}
		if item.ParentGuide != nil {
			if answer, labeled := splitExplicitAnswerLabel(item.ParentGuide.Answer); labeled && answer != "" {
				item.ParentGuide.Answer = answer
			}
		}
		raw, err := json.Marshal(item)
		if err != nil {
			return "", false
		}
		assessment.ResultJSON = string(raw)
		projected[i].assessment = &assessment
	}
	return renderCanonicalFinalForTask(projected, PhotoTaskBlankWorksheet, coverage), true
}

// PDF只重排可核对的结构化终稿，独有讲法与来源不做语义删减。
func renderGradingPDF(entries []gradingFinalEntry, coverage k12.GradingFinalArtifactCoverageStatus) (string, bool) {
	type detail struct {
		entry gradingFinalEntry
		item  *PhotoGradeItem
	}
	var details []detail
	var detailEntries []gradingFinalEntry
	var guides []ParentTeachingGuide
	for _, entry := range entries {
		if entry.assessment != nil && entry.assessment.Status == k12.GradingAssessmentCorrect {
			continue
		}
		d := detail{entry: entry}
		if entry.assessment != nil {
			_, status, ok := RenderCanonicalGradingAssessmentDetails(entry.assessment.ResultJSON)
			if !ok || string(status) != string(entry.assessment.Status) {
				return "", false
			}
			var item PhotoGradeItem
			if json.Unmarshal([]byte(entry.assessment.ResultJSON), &item) != nil {
				return "", false
			}
			d.item = &item
			if item.ParentGuide != nil {
				guides = append(guides, *item.ParentGuide)
			}
		}
		details = append(details, d)
		detailEntries = append(detailEntries, entry)
	}
	canonical := renderCanonicalGradingFinal(entries, nil)
	end := len(canonical)
	for _, heading := range []string{"\n## 需关注的题", "\n## 已解答", "\n## 未作答"} {
		if i := strings.Index(canonical, heading); i >= 0 && i < end {
			end = i
		}
	}
	var out strings.Builder
	out.WriteString(strings.TrimSpace(canonical[:end]))
	if len(details) > 0 {
		out.WriteString("\n\n## 订正与解答\n\n")
	}
	label := func(entry gradingFinalEntry) string {
		if s := RecognizedQuestionSourceDisplayLabel(entry.question); s != "" {
			return s
		}
		return "题目位置待确认"
	}
	questionMarkdown := gradingPDFQuestionProjections(detailEntries)
	for index, d := range details {
		out.WriteString("```{=typst}\n#hc-item[\n```\n\n")
		fmt.Fprintf(&out, "### %s\n\n", label(d.entry))
		if q := questionMarkdown[index]; q != "" {
			q = stripPrintedInstructionNumber(d.entry.question, q)
			// 数学定界符交由共享reader解析，题面中的代码定界符仍只作为文字。
			out.WriteString(strings.ReplaceAll(q, "`", "\\`"))
			out.WriteString("\n\n")
		}
		if d.entry.skip != nil {
			out.WriteString("**批改状态：** 已跳过 · 未判断对错\n\n**原始作答：**\n\n**正确答案：**\n\n")
			out.WriteString("```{=typst}\n]\n```\n\n")
			continue
		}
		item := *d.item
		fmt.Fprintf(&out, "**批改状态：** %s\n\n", gradingAssessmentParentStatus(d.entry.assessment.Status))
		studentAnswer := strings.TrimSpace(item.Recognized.AnswerCanonicalMarkdown)
		if studentAnswer == "" {
			studentAnswer = strings.TrimSpace(item.Recognized.StudentAnswer)
		}
		if studentAnswer == "" {
			studentAnswer = strings.TrimSpace(item.Recognized.AnswerRawTranscription)
		}
		fmt.Fprintf(&out, "**原始作答：** %s\n\n", studentAnswer)
		answer := ""
		if item.ParentGuide != nil {
			answer = strings.TrimSpace(item.ParentGuide.Answer)
		} else if reference, _, ok := untrustedReferenceAnswer(item); ok {
			answer = reference
		}
		fmt.Fprintf(&out, "**正确答案：** %s\n\n", answer)
		if item.Status == PhotoWrong || item.Status == PhotoCorrectWithProcessIssue {
			if s := strings.TrimSpace(item.Grade.Outcome.WrongStep); s != "" {
				fmt.Fprintf(&out, "**第一个错步**：%s\n\n", s)
			}
			if s := strings.TrimSpace(item.Grade.Outcome.ErrorCause); s != "" {
				fmt.Fprintf(&out, "**错因**：%s\n\n", s)
			}
		}
		out.WriteString("```{=typst}\n]\n```\n\n")
	}
	// 只有全部讲题对象逐字一致的栏目才合并，不能用相似语义吞掉某题的特别提醒。
	common := [5]bool{}
	fields := func(g ParentTeachingGuide) [5]string {
		encode := func(v []string) string { b, _ := json.Marshal(v); return string(b) }
		return [5]string{g.GradeLevelMethod, encode(g.LikelyMistakes), encode(g.ParentTeachingSequence), encode(g.FollowUpQuestions), g.CheckingMethod}
	}
	if len(guides) > 1 {
		first := fields(guides[0])
		for i := range common {
			common[i] = true
			for _, g := range guides[1:] {
				if fields(g)[i] != first[i] {
					common[i] = false
				}
			}
		}
	}
	// 已验证的讲法没有展示长度上限；PDF保留全文，不能把尾部独有提醒截断。
	writeGuideList := func(label string, values []string) {
		out.WriteString(label)
		out.WriteString("\n\n")
		for i, value := range values {
			fmt.Fprintf(&out, "%d. %s\n", i+1, strings.TrimSpace(value))
		}
		out.WriteString("\n")
	}
	writeFields := func(g ParentTeachingGuide, include [5]bool) {
		if include[0] {
			fmt.Fprintf(&out, "**本年级方法**：%s\n\n", strings.TrimSpace(g.GradeLevelMethod))
		}
		if include[1] {
			writeGuideList("**易错点**：", g.LikelyMistakes)
		}
		if include[2] {
			writeGuideList("**家长怎么讲**：", g.ParentTeachingSequence)
		}
		if include[3] {
			writeGuideList("**可以追问**：", g.FollowUpQuestions)
		}
		if include[4] {
			fmt.Fprintf(&out, "**怎么检查**：%s\n\n", strings.TrimSpace(g.CheckingMethod))
		}
	}
	hasReference := false
	for _, d := range details {
		if d.item != nil {
			_, _, ok := untrustedReferenceAnswer(*d.item)
			hasReference = hasReference || ok
		}
	}
	if len(guides) > 0 || hasReference {
		out.WriteString("## 家长讲题\n\n")
		for _, d := range details {
			if d.item == nil {
				continue
			}
			if d.item.ParentGuide == nil {
				if _, solution, ok := untrustedReferenceAnswer(*d.item); ok {
					fmt.Fprintf(&out, "### %s 必要步骤\n\n%s\n\n", label(d.entry), solution)
				}
				continue
			}
			out.WriteString("```{=typst}\n#hc-item[\n```\n\n")
			fmt.Fprintf(&out, "### %s 必要步骤\n\n", label(d.entry))
			if len(d.item.ParentGuide.TeachingDiagrams) > 0 {
				writeParentGuideStepsMarkdown(&out, "", *d.item.ParentGuide, true)
			} else {
				writeGuideList("", d.item.ParentGuide.FullSolutionSteps)
			}
			var unique [5]bool
			for i := range unique {
				unique[i] = !common[i]
			}
			writeFields(*d.item.ParentGuide, unique)
			out.WriteString("```{=typst}\n]\n```\n\n")
		}
		for _, yes := range common {
			if yes {
				out.WriteString("## 通用辅导提醒\n\n")
				writeFields(guides[0], common)
				break
			}
		}
	}
	if coverage == k12.GradingFinalArtifactCoverageGeneralGuidance {
		out.WriteString("## 说明\n\n本次没有可核验的课本依据，以上批改与家长讲法为通用参考。")
	}
	return strings.TrimSpace(out.String()), true
}

// 同源子问只合并逐字相同的完整前导段落，当前题面和冻结来源保持完整。
func gradingPDFQuestionProjections(entries []gradingFinalEntry) []string {
	type sourceKey struct{ owner, parent, page string }
	groups := make(map[sourceKey][]int)
	projected := make([]string, len(entries))
	for i, entry := range entries {
		q := entry.question
		projected[i] = strings.TrimSpace(q.CanonicalMarkdown)
		if entry.assessment != nil && entry.assessment.AgentName != "" && q.ProblemKind == ProblemKindSubproblem && q.ParentProblemID != "" && q.PageAssetID != "" {
			key := sourceKey{entry.assessment.AgentName, q.ParentProblemID, q.PageAssetID}
			groups[key] = append(groups[key], i)
		}
	}
	for _, indices := range groups {
		if len(indices) < 2 {
			continue
		}
		parts := strings.Split(projected[indices[0]], "\n\n")
		sharedCount := len(parts) - 1
		valid := sharedCount > 0
		for _, index := range indices {
			q := entries[index].question
			// 当前作答不明不改变已证明的父材料；父来源本身不明时仍保留完整原文。
			if q.InputDigest == "" || q.CanonicalVersion < 1 || q.ConfirmedVersion < 1 || q.parentSourceUnclear || !CanonicalMarkdownValid(q.CanonicalMarkdown) || currentAssessmentCanonical(q) == "" {
				valid = false
				break
			}
			other := strings.Split(projected[index], "\n\n")
			if sharedCount >= len(other) {
				sharedCount = len(other) - 1
			}
			for n := 0; n < sharedCount; n++ {
				if strings.TrimSpace(parts[n]) == "" || parts[n] != other[n] {
					sharedCount = n
					break
				}
			}
		}
		if !valid || sharedCount == 0 {
			continue
		}
		prefix := strings.Join(parts[:sharedCount], "\n\n") + "\n\n"
		for _, index := range indices {
			remainder := strings.TrimPrefix(projected[index], prefix)
			if remainder == projected[index] || !CanonicalMarkdownValid(remainder) || !strings.Contains(remainder, currentAssessmentCanonical(entries[index].question)) {
				valid = false
				break
			}
		}
		if valid {
			for _, index := range indices[1:] {
				projected[index] = strings.TrimPrefix(projected[index], prefix)
			}
		}
	}
	return projected
}
