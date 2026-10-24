package usecase

import (
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

const TeachingReportContentContractVersion = "teaching-report-content-v1"

// TeachingReportContentSource 是服务端读回的题目、原解和既有讲法，模型不能改其身份与状态。
type TeachingReportContentSource struct {
	Source            k12.TeachingReportProblemSource `json:"source"`
	DisplayLabel      string                          `json:"display_label"`
	Question          string                          `json:"question"`
	Solution          string                          `json:"solution"`
	UnavailableReason string                          `json:"unavailable_reason,omitempty"`
	Guide             *ParentTeachingGuide            `json:"guide,omitempty"`
}

// TeachingReportContentContract 在原任务接纳时冻结，不在恢复时重读表达设置。
type TeachingReportContentContract struct {
	Version string `json:"version"`
}

type TeachingReportContentRequest struct {
	Subject  string                        `json:"subject"`
	Grade    string                        `json:"grade"`
	Sources  []TeachingReportContentSource `json:"sources"`
	Contract TeachingReportContentContract `json:"contract"`
}

// TeachingReportDraftAnswer 只表达答案内容，不接受模型自填的来源或核验结论。
type TeachingReportDraftAnswer struct {
	ID         string   `json:"id"`
	Label      string   `json:"label,omitempty"`
	Text       string   `json:"text"`
	LaTeX      string   `json:"latex,omitempty"`
	Conditions []string `json:"conditions,omitempty"`
}

type TeachingReportDraftStep struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	LaTeX string `json:"latex,omitempty"`
}

type TeachingReportDraftItem struct {
	ProblemID  string                      `json:"problem_id"`
	ShortTitle string                      `json:"short_title"`
	Answers    []TeachingReportDraftAnswer `json:"answers"`
	Steps      []TeachingReportDraftStep   `json:"steps"`
}

type TeachingReportDraftBlock struct {
	ID         string                               `json:"id"`
	ProblemIDs []string                             `json:"problem_ids"`
	Text       string                               `json:"text"`
	LaTeX      string                               `json:"latex,omitempty"`
	Table      *k12.TeachingReportTable             `json:"table,omitempty"`
	Purpose    k12.TeachingReportExplanationPurpose `json:"purpose,omitempty"`
}

type TeachingReportDraftMethod struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	BlockIDs    []string `json:"block_ids"`
}

type TeachingReportDraftSection struct {
	ID          string                      `json:"id"`
	Title       string                      `json:"title"`
	ProblemIDs  []string                    `json:"problem_ids"`
	Blocks      []TeachingReportDraftBlock  `json:"blocks"`
	DiagramIDs  []string                    `json:"diagram_ids"`
	MethodNotes []TeachingReportDraftMethod `json:"method_notes"`
}

// TeachingReportContentDraft 没有学生作答、评分、验证状态或任意SVG输入字段。
type TeachingReportContentDraft struct {
	Version  string                       `json:"version"`
	Items    []TeachingReportDraftItem    `json:"items"`
	Sections []TeachingReportDraftSection `json:"sections"`
}

// AssembleTeachingReportContent 仅绑定真实来源；模型不能用自己的状态或摘要替换它们。
func AssembleTeachingReportContent(req TeachingReportContentRequest, draft TeachingReportContentDraft, auditReceipt string) ([]TeachingReportQuestionContent, []k12.TeachingReportDeepSection, error) {
	invalid := func(message string) ([]TeachingReportQuestionContent, []k12.TeachingReportDeepSection, error) {
		return nil, nil, fmt.Errorf("%w: %s", k12.ErrTeachingReportInvalid, message)
	}
	if req.Contract.Version != TeachingReportContentContractVersion || draft.Version != req.Contract.Version ||
		len(req.Sources) == 0 || len(req.Sources) != len(draft.Items) || auditReceipt == "" {
		return invalid("report content contract, coverage or audit identity is incomplete")
	}
	sources := make(map[string]TeachingReportContentSource, len(req.Sources))
	diagrams := make(map[string]k12.TeachingDiagramAsset)
	for _, source := range req.Sources {
		if source.Source.ProblemID == "" || sources[source.Source.ProblemID].Source.ProblemID != "" {
			return invalid("duplicate or empty source problem")
		}
		sources[source.Source.ProblemID] = source
		if source.Guide != nil {
			for _, diagram := range source.Guide.TeachingDiagrams {
				if diagram.ProblemID != source.Source.ProblemID || diagram.InputRevision != source.Source.InputRevision ||
					diagram.ProblemInputDigest != source.Source.InputDigest || diagram.SolutionDigest != source.Source.SolutionDigest {
					return invalid("diagram does not belong to its source problem")
				}
				diagrams[diagram.AssetID] = diagram
			}
		}
	}
	origin := func(source k12.TeachingReportProblemSource) k12.TeachingReportContentOrigin {
		return k12.TeachingReportContentOrigin{ProblemID: source.ProblemID, InputRevision: source.InputRevision,
			InputDigest: source.InputDigest, SolutionDigest: source.SolutionDigest}
	}
	content := make(map[string]TeachingReportQuestionContent, len(draft.Items))
	for _, item := range draft.Items {
		source, ok := sources[item.ProblemID]
		if !ok || content[item.ProblemID].ProblemID != "" || strings.TrimSpace(item.ShortTitle) == "" {
			return invalid("report item belongs to another or duplicate source problem")
		}
		analysis := k12.TeachingReportAnswerAnalysis{State: k12.TeachingReportReferenceAvailable}
		if source.UnavailableReason != "" {
			if len(item.Answers) != 0 || len(item.Steps) != 0 {
				return invalid("unavailable source was assigned a generated answer")
			}
			analysis.State, analysis.UnavailableReason = k12.TeachingReportReferenceUnavailable, source.UnavailableReason
		} else if strings.TrimSpace(source.Solution) == "" || len(item.Answers) == 0 {
			return invalid("available source solution or report answers are missing")
		}
		for _, answer := range item.Answers {
			analysis.Answers = append(analysis.Answers, k12.TeachingReportAnswer{ID: answer.ID, Label: answer.Label,
				Text: answer.Text, LaTeX: answer.LaTeX, Conditions: append([]string(nil), answer.Conditions...), Origin: origin(source.Source)})
		}
		for _, step := range item.Steps {
			analysis.Steps = append(analysis.Steps, k12.TeachingReportCalculationStep{ID: step.ID,
				Text: step.Text, LaTeX: step.LaTeX, Origin: origin(source.Source)})
		}
		content[item.ProblemID] = TeachingReportQuestionContent{ProblemID: item.ProblemID, SourceSolutionDigest: source.Source.SolutionDigest, ShortTitle: item.ShortTitle, AnswerAnalysis: analysis}
	}
	var ordered []TeachingReportQuestionContent
	for _, source := range req.Sources {
		ordered = append(ordered, content[source.Source.ProblemID])
	}
	var sections []k12.TeachingReportDeepSection
	for _, section := range draft.Sections {
		allowed := make(map[string]bool, len(section.ProblemIDs))
		for _, id := range section.ProblemIDs {
			if sources[id].Source.ProblemID == "" || allowed[id] {
				return invalid("deep section refers to an unrelated or duplicate problem")
			}
			allowed[id] = true
		}
		out := k12.TeachingReportDeepSection{ID: section.ID, Title: section.Title, ProblemIDs: append([]string(nil), section.ProblemIDs...)}
		for _, block := range section.Blocks {
			if len(block.ProblemIDs) == 0 {
				return invalid("explanation block has no source problem")
			}
			value := k12.TeachingReportExplanationBlock{ID: block.ID, Text: block.Text, LaTeX: block.LaTeX, Table: block.Table, Purpose: block.Purpose}
			for _, id := range block.ProblemIDs {
				if !allowed[id] {
					return invalid("explanation block is outside its section sources")
				}
				value.Origins = append(value.Origins, origin(sources[id].Source))
			}
			out.Blocks = append(out.Blocks, value)
		}
		for _, id := range section.DiagramIDs {
			diagram, ok := diagrams[id]
			if !ok || !allowed[diagram.ProblemID] {
				return invalid("report diagram has no matching source asset")
			}
			out.Diagrams = append(out.Diagrams, diagram)
		}
		for _, method := range section.MethodNotes {
			out.MethodNotes = append(out.MethodNotes, k12.TeachingReportMethodNote{Name: method.Name, Description: method.Description,
				BlockIDs: append([]string(nil), method.BlockIDs...), EvidenceRef: auditReceipt})
		}
		sections = append(sections, out)
	}
	return ordered, sections, nil
}
