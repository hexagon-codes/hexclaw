package usecase

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"strconv"
	"strings"
	"unicode"

	"github.com/hexagon-codes/hexclaw/internal/mathtext"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// TeachingReportBranding 由宿主产品事实提供，不从题目或模型输出推断。
type TeachingReportBranding struct {
	Name            string `json:"name"`
	Tagline         string `json:"tagline"`
	WebsiteURL      string `json:"website_url"`
	WebsiteLabel    string `json:"website_label"`
	WeChatID        string `json:"wechat_id"`
	GitHubURL       string `json:"github_url"`
	GitHubLabel     string `json:"github_label"`
	LogoAssetID     string `json:"-"`
	LogoAssetDigest string `json:"-"`
	LogoMIME        string `json:"-"`
}

// TeachingReportAsset 是调用方按原归属读回的冻结字节；投影只核内容摘要。
type TeachingReportAsset struct {
	Data []byte
	MIME string
	// 展示副本只来自宿主核对后的同源方向变换，不替换原资产身份。
	DisplayData []byte
	DisplayMIME string
}

// TeachingReportPDFProjection 仅包含渲染数据与资源，不携带数据库或交付动作。
type TeachingReportPDFProjection struct {
	Document  json.RawMessage
	Formulas  map[string]string
	Resources map[string][]byte
}

type reportProjectionBuilder struct {
	formulas  map[string]string
	resources map[string][]byte
	assets    map[string]TeachingReportAsset
}

// ProjectTeachingReportPDF 把已校验报告转换为Hub模板输入，不求解、不改状态或原始作答。
// 同源方向副本由宿主提供；先验证原资产，再选择展示副本，不猜测旋转。
func ProjectTeachingReportPDF(report k12.TeachingReport, branding TeachingReportBranding, assets map[string]TeachingReportAsset) (TeachingReportPDFProjection, error) {
	return projectTeachingReportPDF(report, branding, assets, false)
}

// ProjectTeachingReportPDFV2 复用同一语义投影，增加完整文字原题及来源标签。
func ProjectTeachingReportPDFV2(report k12.TeachingReport, branding TeachingReportBranding, assets map[string]TeachingReportAsset) (TeachingReportPDFProjection, error) {
	return projectTeachingReportPDF(report, branding, assets, true)
}

func projectTeachingReportPDF(report k12.TeachingReport, branding TeachingReportBranding, assets map[string]TeachingReportAsset, textSourceSupported bool) (TeachingReportPDFProjection, error) {
	report, err := k12.NewTeachingReport(report)
	if err != nil {
		return TeachingReportPDFProjection{}, err
	}
	b := reportProjectionBuilder{formulas: make(map[string]string), resources: make(map[string][]byte), assets: assets}
	sourceText := make([]any, 0)
	if report.Source.Kind == k12.TeachingReportSourceTextSolve {
		if !textSourceSupported {
			return TeachingReportPDFProjection{}, fmt.Errorf("text source requires report render input v2")
		}
		for _, source := range report.ProblemSources {
			if strings.TrimSpace(source.QuestionText) == "" {
				return TeachingReportPDFProjection{}, fmt.Errorf("text source question is unavailable: %s", source.ProblemID)
			}
			for _, paragraph := range strings.Split(source.QuestionText, "\n\n") {
				sourceText = append(sourceText, map[string]any{"kind": "paragraph", "content": b.value(paragraph, "")})
			}
		}
	}
	images := make([]any, 0, len(report.SourcePages))
	for _, source := range report.SourcePages {
		path, err := b.asset(source.AssetID, source.AssetDigest, source.MIME)
		if err != nil {
			return TeachingReportPDFProjection{}, err
		}
		images = append(images, map[string]any{"path": path, "alt": "完整作业图"})
	}
	questions := make([]any, 0, len(report.Items))
	for _, item := range report.Items {
		answer := make([]any, 0)
		for index, value := range item.AnswerAnalysis.Answers {
			if index > 0 {
				answer = append(answer, reportText("；"))
			}
			if value.Label != "" {
				answer = append(answer, reportText(value.Label+"："))
			}
			answer = append(answer, b.value(value.Text, value.LaTeX)...)
			for _, condition := range value.Conditions {
				answer = append(answer, reportText("（"+condition+"）"))
			}
		}
		if item.AnswerAnalysis.State == k12.TeachingReportReferenceUnavailable {
			answer = append(answer, reportText(item.AnswerAnalysis.UnavailableReason))
		}
		calculations := make([]any, 0, len(item.AnswerAnalysis.Steps))
		for _, step := range item.AnswerAnalysis.Steps {
			calculations = append(calculations, b.value(step.Text, step.LaTeX))
		}
		original := make([]any, 0)
		if len(item.OriginalAnswer.Slots) > 0 {
			for _, slot := range item.OriginalAnswer.Slots {
				value := b.value(slot.Text, slot.LaTeX)
				if slot.State == k12.TeachingReportAnswerBlank {
					value = []any{reportText("未填写")}
				}
				original = append(original, map[string]any{"label": slot.Label, "content": value})
			}
		} else {
			text := item.OriginalAnswer.CanonicalText
			if text == "" {
				text = item.OriginalAnswer.RawText
			}
			if item.OriginalAnswer.State == k12.TeachingReportAnswerBlank {
				text = "未填写"
			}
			if textSourceSupported && item.OriginalAnswer.State == k12.TeachingReportAnswerNotProvided {
				text = "未提供"
			}
			original = append(original, map[string]any{"content": b.value(text, "")})
		}
		label, tone := teachingReportItemStatus(report.Source.Kind, item)
		columns := 1
		if len(calculations) == 2 {
			columns = 2
		}
		questions = append(questions, map[string]any{
			"id": item.ProblemID, "number": item.DisplayLabel, "title": item.ShortTitle,
			"status": map[string]string{"label": label, "tone": tone}, "answer": answer,
			"calculations": calculations, "calculation_columns": columns, "original_answer": original,
		})
	}
	steps := make([]any, 0, len(report.DeepSections))
	for index, section := range report.DeepSections {
		blocks := make([]any, 0)
		for _, method := range section.MethodNotes {
			blocks = append(blocks, map[string]any{"kind": "method_note", "name": method.Name, "application": method.Description})
		}
		for _, block := range section.Blocks {
			if block.Table != nil {
				headers := make([]any, 0, len(block.Table.Headers))
				for _, cell := range block.Table.Headers {
					headers = append(headers, b.value(cell.Text, cell.LaTeX))
				}
				rows := make([]any, 0, len(block.Table.Rows))
				for _, row := range block.Table.Rows {
					cells := make([]any, 0, len(row))
					for _, cell := range row {
						cells = append(cells, b.value(cell.Text, cell.LaTeX))
					}
					rows = append(rows, cells)
				}
				blocks = append(blocks, map[string]any{"kind": "table", "headers": headers, "rows": rows})
			} else {
				kind := "paragraph"
				if block.LaTeX != "" {
					kind = "equation"
				}
				content := map[string]any{"kind": kind, "content": b.value(block.Text, block.LaTeX)}
				switch block.Purpose {
				case k12.TeachingReportExplanationSupplement:
					content["kind"] = "supplement"
				case k12.TeachingReportExplanationPitfall:
					content["kind"], content["label"] = "note", "注意"
				}
				blocks = append(blocks, content)
			}
		}
		for diagramIndex, diagram := range section.Diagrams {
			path, err := b.asset(diagram.AssetID, diagram.AssetDigest, diagram.MIME)
			if err != nil {
				return TeachingReportPDFProjection{}, err
			}
			diagramBlock := map[string]any{"kind": "diagram", "path": path, "alt": diagram.AltText}
			// 同一讲解组的首段与首图并列，其余内容沿原顺序自然续排。
			if diagramIndex == 0 && len(section.Blocks) > 0 && section.Blocks[0].Text != "" && section.Blocks[0].Purpose == "" {
				position := len(section.MethodNotes)
				blocks[position] = map[string]any{"kind": "columns", "columns": []any{[]any{blocks[position]}, []any{diagramBlock}}}
			} else {
				blocks = append(blocks, diagramBlock)
			}
		}
		steps = append(steps, map[string]any{"number": strconv.Itoa(index + 1), "title": section.Title, "blocks": blocks})
	}
	metadata := report.Metadata
	brandJSON, err := json.Marshal(branding)
	if err != nil {
		return TeachingReportPDFProjection{}, err
	}
	var brand map[string]any
	if err := json.Unmarshal(brandJSON, &brand); err != nil {
		return TeachingReportPDFProjection{}, err
	}
	if branding.LogoAssetID != "" {
		path, err := b.asset(branding.LogoAssetID, branding.LogoAssetDigest, branding.LogoMIME)
		if err != nil {
			return TeachingReportPDFProjection{}, err
		}
		brand["logo_path"] = path
	}
	document := map[string]any{
		"schema_version": "homework-report-render-v1",
		"document": map[string]string{"title": metadata.Title, "agent_name": metadata.AgentDisplayName,
			"grade_term": metadata.GradeTerm, "subject": metadata.Subject, "date": metadata.ReportDate},
		"branding": brand, "source_images": images, "questions": questions,
		"deep": map[string]any{"steps": steps},
	}
	if textSourceSupported {
		document["schema_version"] = "homework-report-render-v2"
		document["source_text"] = sourceText
		document["document"].(map[string]string)["source_kind"] = string(report.Source.Kind)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return TeachingReportPDFProjection{}, err
	}
	return TeachingReportPDFProjection{Document: raw, Formulas: b.formulas, Resources: b.resources}, nil
}

func (b *reportProjectionBuilder) asset(id, expectedDigest, expectedMIME string) (string, error) {
	asset, ok := b.assets[id]
	if !ok || len(asset.Data) == 0 {
		return "", fmt.Errorf("report asset is unavailable: %s", id)
	}
	sum := sha256.Sum256(asset.Data)
	if expectedDigest == "" || hex.EncodeToString(sum[:]) != expectedDigest {
		return "", fmt.Errorf("report asset digest mismatch: %s", id)
	}
	if asset.MIME != expectedMIME {
		return "", fmt.Errorf("report asset MIME mismatch: %s", id)
	}
	data, mediaType := asset.Data, expectedMIME
	if len(asset.DisplayData) > 0 {
		data, mediaType = asset.DisplayData, asset.DisplayMIME
		sum = sha256.Sum256(data)
	}
	extensions, err := mime.ExtensionsByType(mediaType)
	if err != nil || len(extensions) == 0 {
		return "", fmt.Errorf("report asset has no file extension: %s", id)
	}
	name := "resources/" + hex.EncodeToString(sum[:]) + extensions[0]
	b.resources[name] = append([]byte(nil), data...)
	return name, nil
}

func reportText(value string) map[string]string {
	return map[string]string{"kind": "text", "text": value}
}

func (b *reportProjectionBuilder) math(latex string) any {
	sum := sha256.Sum256([]byte(latex))
	id := "f_" + hex.EncodeToString(sum[:])
	b.formulas[id] = latex
	return map[string]string{"kind": "math", "math_ref": id}
}

func (b *reportProjectionBuilder) value(text, latex string) []any {
	if latex != "" {
		return []any{b.math(latex)}
	}
	// 复用已有数学规范化，只把完整定界公式转成数学节点，其余文字完整保留。
	value := mathtext.FormatPDFMarkdown(text)
	parts := make([]any, 0)
	for value != "" {
		start := strings.Index(value, "$")
		if start < 0 {
			parts = append(parts, reportText(value))
			break
		}
		if start > 0 && value[start-1] == '\\' {
			parts = append(parts, reportText(value[:start+1]))
			value = value[start+1:]
			continue
		}
		delimiter := "$"
		if strings.HasPrefix(value[start:], "$$") {
			delimiter = "$$"
		}
		end := strings.Index(value[start+len(delimiter):], delimiter)
		if end < 0 {
			parts = append(parts, reportText(value))
			break
		}
		if start > 0 {
			parts = append(parts, reportText(value[:start]))
		}
		end += start + len(delimiter)
		parts = append(parts, b.math(value[start+len(delimiter):end]))
		value = value[end+len(delimiter):]
	}
	return parts
}

func teachingReportItemStatus(kind k12.TeachingReportSourceKind, item k12.TeachingReportItem) (string, string) {
	if kind == k12.TeachingReportSourceTextSolve && item.Assessment.Status == "" {
		if item.AnswerAnalysis.State == k12.TeachingReportReferenceUnavailable {
			return "待核对", "amber"
		}
		return "已解答", "green"
	}
	return teachingReportStatus(item.Assessment)
}

func teachingReportStatus(assessment k12.TeachingReportAssessment) (string, string) {
	if assessment.Status == k12.GradingAssessmentBlankSolved {
		return "已解答", "green"
	}
	if assessment.Status == "" {
		return assessment.Reason, "neutral"
	}
	label := strings.TrimLeftFunc(gradingAssessmentParentStatus(assessment.Status), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	tone := "amber"
	if assessment.Status == k12.GradingAssessmentCorrect {
		tone = "green"
	}
	if assessment.Status == k12.GradingAssessmentWrong {
		tone = "red"
	}
	if assessment.Status == k12.GradingAssessmentUnanswered || assessment.Status == k12.GradingAssessmentOutOfScope {
		tone = "neutral"
	}
	return label, tone
}
