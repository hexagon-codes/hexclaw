package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

// TeachingReportTaskSnapshot 随原任务持久化；恢复不能重新选择日期、身份或模板。
type TeachingReportTaskSnapshot struct {
	Metadata k12.TeachingReportMetadata   `json:"metadata"`
	Bundle   TeachingReportBundleSnapshot `json:"bundle"`
}

// TeachingReportFrozenSnapshot 是原业务终稿的关联快照，不承担求解、任务或投递状态。
type TeachingReportFrozenSnapshot struct {
	Report k12.TeachingReport           `json:"report"`
	Bundle TeachingReportBundleSnapshot `json:"bundle"`
}

type TeachingReportRenderer interface {
	RenderTeachingReport(context.Context, render.ReportBundle, render.ReportRenderInput) ([]byte, string, error)
}

// TeachingReportSpecRenderer 扩展现有渲染端口；旧适配器仍可回放原合同。
type TeachingReportSpecRenderer interface {
	ResolveTeachingReportSpec(context.Context, render.ReportBundle) (render.ReportRenderSpec, error)
	RenderTeachingReportResult(context.Context, render.ReportBundle, render.ReportRenderInput) (TeachingReportRendered, error)
}

type TeachingReportRendered struct {
	Data        []byte
	ContentType string
	Spec        render.ReportRenderSpec
	LayoutCheck string
}

type TeachingReportPrintInput struct {
	Snapshot TeachingReportFrozenSnapshot
	Assets   map[string]TeachingReportAsset
	Spec     *render.ReportRenderSpec
}

func (d Deps) freezeTeachingReportTask(ctx context.Context, agent, subject, grade, title string) (*TeachingReportTaskSnapshot, error) {
	if d.TeachingReportResources == nil {
		return nil, nil
	}
	display := agent
	zoneName := "Asia/Shanghai"
	if d.Profiles != nil {
		if profile, err := d.GetProfileBundleSnapshot(ctx, agent, ""); err == nil {
			if profile.AgentConfig.DisplayName != "" {
				display = profile.AgentConfig.DisplayName
			}
			if grade == "" {
				grade = profile.Profile.GradeTerm
			}
			if profile.WeeklyPracticeSettings.Timezone != "" {
				zoneName = profile.WeeklyPracticeSettings.Timezone
			}
		}
	}
	// 仅为已确定的小学年级启用新合同，其他既有解题能力保持原路径。
	elementary := false
	normalizedGrade := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(grade), "小学"))
	for _, year := range []string{"一", "二", "三", "四", "五", "六", "1", "2", "3", "4", "5", "6"} {
		if strings.HasPrefix(normalizedGrade, year+"年级") {
			elementary = true
			break
		}
	}
	if !elementary {
		return nil, nil
	}
	bundle, err := FreezeTeachingReportBundle(ctx, d.TeachingReportResources)
	if errors.Is(err, marketplace.ErrSkillNotInstalled) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("freeze report resources: %w", err)
	}
	zone, zoneErr := time.LoadLocation(zoneName)
	if zoneErr != nil {
		zoneName = "Asia/Shanghai"
		zone = time.FixedZone("CST", 8*60*60)
	}
	return &TeachingReportTaskSnapshot{Bundle: bundle, Metadata: k12.TeachingReportMetadata{
		Title: title, AgentDisplayName: display, GradeTerm: grade, Subject: subject,
		ReportDate: time.Unix(d.now(), 0).In(zone).Format("2006-01-02"), Timezone: zoneName,
	}}, nil
}

// bindSingleProblemTeachingDraft 仅重命名同一次候选中的局部ID，不从旧正文猜测槽位。
func bindSingleProblemTeachingDraft(source TeachingReportContentSource, guide ParentTeachingGuide, receipt string) ([]TeachingReportQuestionContent, []k12.TeachingReportDeepSection, error) {
	if guide.ReportContent == nil {
		return nil, nil, ErrTeachingReportContentRequired
	}
	raw, err := json.Marshal(guide.ReportContent)
	if err != nil {
		return nil, nil, err
	}
	var draft TeachingReportContentDraft
	if err := json.Unmarshal(raw, &draft); err != nil {
		return nil, nil, err
	}
	if len(draft.Items) != 1 || draft.Items[0].ProblemID != "current" {
		return nil, nil, fmt.Errorf("%w: report candidate is not the current single problem", k12.ErrTeachingReportInvalid)
	}
	prefix := source.Source.ProblemID + ":"
	draft.Items[0].ProblemID = source.Source.ProblemID
	for i := range draft.Items[0].Answers {
		draft.Items[0].Answers[i].ID = prefix + draft.Items[0].Answers[i].ID
	}
	for i := range draft.Items[0].Steps {
		draft.Items[0].Steps[i].ID = prefix + draft.Items[0].Steps[i].ID
	}
	bind := func(ids []string) error {
		for i, id := range ids {
			if id != "current" {
				return fmt.Errorf("%w: report candidate refers to another problem", k12.ErrTeachingReportInvalid)
			}
			ids[i] = source.Source.ProblemID
		}
		return nil
	}
	for i := range draft.Sections {
		section := &draft.Sections[i]
		section.ID = prefix + section.ID
		if err := bind(section.ProblemIDs); err != nil {
			return nil, nil, err
		}
		for j := range section.Blocks {
			block := &section.Blocks[j]
			block.ID = prefix + block.ID
			if err := bind(block.ProblemIDs); err != nil {
				return nil, nil, err
			}
		}
		for j := range section.MethodNotes {
			for k := range section.MethodNotes[j].BlockIDs {
				section.MethodNotes[j].BlockIDs[k] = prefix + section.MethodNotes[j].BlockIDs[k]
			}
		}
	}
	return AssembleTeachingReportContent(TeachingReportContentRequest{Sources: []TeachingReportContentSource{source}, Contract: TeachingReportContentContract{Version: TeachingReportContentContractVersion}}, draft, receipt)
}

// TeachingReportMarkdown 与PDF消费同一值对象；不改写原求解正文或数值回执。
func TeachingReportMarkdown(report k12.TeachingReport) (string, error) {
	report, err := k12.NewTeachingReport(report)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n\n", report.Metadata.Title)
	if report.Source.Kind == k12.TeachingReportSourceTextSolve {
		for _, source := range report.ProblemSources {
			out.WriteString(source.QuestionText + "\n\n")
		}
	}
	out.WriteString("## 一、快速解答\n\n")
	value := func(text, latex string) string {
		if latex != "" {
			return "$" + latex + "$"
		}
		return text
	}
	for _, item := range report.Items {
		status, _ := teachingReportItemStatus(report.Source.Kind, item)
		label := "批改状态"
		if report.Source.Kind == k12.TeachingReportSourceTextSolve {
			label = "解题状态"
		}
		fmt.Fprintf(&out, "### %s %s · %s：%s\n\n", item.DisplayLabel, item.ShortTitle, label, status)
		for _, answer := range item.AnswerAnalysis.Answers {
			if answer.Label != "" {
				out.WriteString(answer.Label + "：")
			}
			out.WriteString(value(answer.Text, answer.LaTeX))
			for _, condition := range answer.Conditions {
				out.WriteString("（" + condition + "）")
			}
			out.WriteString("\n\n")
		}
		if item.AnswerAnalysis.State == k12.TeachingReportReferenceUnavailable {
			out.WriteString(item.AnswerAnalysis.UnavailableReason + "\n\n")
		}
		for _, step := range item.AnswerAnalysis.Steps {
			if step.LaTeX != "" {
				out.WriteString("$$\n" + step.LaTeX + "\n$$\n\n")
			} else {
				out.WriteString(step.Text + "\n\n")
			}
		}
		answer := item.OriginalAnswer.CanonicalText
		if answer == "" {
			switch item.OriginalAnswer.State {
			case k12.TeachingReportAnswerNotProvided:
				answer = "未提供"
			case k12.TeachingReportAnswerBlank:
				answer = "未填写"
			case k12.TeachingReportAnswerUnreadable:
				answer = "笔迹不清"
			default:
				answer = "未知"
			}
		}
		out.WriteString("原始作答：" + answer + "\n\n")
	}
	if len(report.DeepSections) > 0 {
		out.WriteString("## 二、深度讲解\n\n")
	}
	for _, section := range report.DeepSections {
		out.WriteString("### " + section.Title + "\n\n")
		for _, note := range section.MethodNotes {
			out.WriteString("> 教学方法：" + note.Name + " · " + note.Description + "\n\n")
		}
		for _, block := range section.Blocks {
			if block.Table != nil {
				for _, header := range block.Table.Headers {
					out.WriteString("| " + strings.ReplaceAll(value(header.Text, header.LaTeX), "|", "\\|"))
				}
				out.WriteString(" |\n")
				for range block.Table.Headers {
					out.WriteString("| --- ")
				}
				out.WriteString("|\n")
				for _, row := range block.Table.Rows {
					for _, cell := range row {
						out.WriteString("| " + strings.ReplaceAll(value(cell.Text, cell.LaTeX), "|", "\\|"))
					}
					out.WriteString(" |\n")
				}
				out.WriteString("\n")
			} else if block.LaTeX != "" {
				out.WriteString("$$\n" + block.LaTeX + "\n$$\n\n")
			} else {
				out.WriteString(block.Text + "\n\n")
			}
		}
		for _, diagram := range section.Diagrams {
			fmt.Fprintf(&out, "![%s](%s)\n\n", diagram.AltText, diagram.AssetID)
		}
	}
	return strings.TrimSpace(out.String()), nil
}
