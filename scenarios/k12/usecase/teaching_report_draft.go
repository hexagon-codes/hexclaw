package usecase

import (
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// ValidateSingleProblemReportDraft 在结算前检查局部引用，不赋予模型任何来源或评分权。
func ValidateSingleProblemReportDraft(draft *TeachingReportContentDraft) error {
	invalid := func() error {
		return fmt.Errorf("%w: single-problem teaching structure is incomplete or inconsistent", k12.ErrTeachingReportInvalid)
	}
	if draft == nil || draft.Version != TeachingReportContentContractVersion || len(draft.Items) != 1 || draft.Items[0].ProblemID != "current" || strings.TrimSpace(draft.Items[0].ShortTitle) == "" || len(draft.Items[0].Answers) == 0 || len(draft.Sections) == 0 {
		return invalid()
	}
	seen := map[string]bool{}
	id := func(value string) bool {
		if strings.TrimSpace(value) == "" || seen[value] {
			return false
		}
		seen[value] = true
		return true
	}
	value := func(text, latex string) bool {
		return (strings.TrimSpace(text) != "") != (strings.TrimSpace(latex) != "")
	}
	for _, answer := range draft.Items[0].Answers {
		if !id(answer.ID) || !value(answer.Text, answer.LaTeX) {
			return invalid()
		}
	}
	for _, step := range draft.Items[0].Steps {
		if !id(step.ID) || !value(step.Text, step.LaTeX) {
			return invalid()
		}
	}
	current := func(ids []string) bool { return len(ids) == 1 && ids[0] == "current" }
	for _, section := range draft.Sections {
		if !id(section.ID) || strings.TrimSpace(section.Title) == "" || !current(section.ProblemIDs) || len(section.Blocks) == 0 || len(section.DiagramIDs) != 0 {
			return invalid()
		}
		blocks := map[string]bool{}
		for _, block := range section.Blocks {
			if !id(block.ID) || !current(block.ProblemIDs) {
				return invalid()
			}
			blocks[block.ID] = true
			if block.Purpose != "" && block.Purpose != k12.TeachingReportExplanationSupplement && block.Purpose != k12.TeachingReportExplanationPitfall {
				return invalid()
			}
			if block.Table == nil {
				if !value(block.Text, block.LaTeX) {
					return invalid()
				}
				continue
			}
			if block.Text != "" || block.LaTeX != "" || block.Purpose != "" || len(block.Table.Headers) == 0 || len(block.Table.Rows) == 0 {
				return invalid()
			}
			for _, cell := range block.Table.Headers {
				if !value(cell.Text, cell.LaTeX) {
					return invalid()
				}
			}
			for _, row := range block.Table.Rows {
				if len(row) != len(block.Table.Headers) {
					return invalid()
				}
				for _, cell := range row {
					if !value(cell.Text, cell.LaTeX) {
						return invalid()
					}
				}
			}
		}
		for _, note := range section.MethodNotes {
			if strings.TrimSpace(note.Name) == "" || strings.TrimSpace(note.Description) == "" || len(note.BlockIDs) == 0 {
				return invalid()
			}
			for _, ref := range note.BlockIDs {
				if !blocks[ref] {
					return invalid()
				}
			}
		}
	}
	return nil
}
