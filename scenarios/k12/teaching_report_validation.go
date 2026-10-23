package k12

import (
	"fmt"
	"strings"
	"time"
)

// Validate 只核对快照内部的题目覆盖、原答保真、状态和来源引用。
// 真实来源、数学正确性、采用资格及产物字节由各自用例核验，不能由本方法推定。
func (r TeachingReport) Validate() error {
	if r.SchemaVersion != TeachingReportSchemaVersion || r.ContentVersion != TeachingReportContentVersion {
		return teachingReportInvalid("unsupported report contract")
	}
	if r.Source.Kind != TeachingReportSourceGrading && r.Source.Kind != TeachingReportSourceTextSolve {
		return teachingReportInvalid("unknown source kind")
	}
	if strings.TrimSpace(r.Source.SourceID) == "" || r.Source.Revision < 1 || strings.TrimSpace(r.Source.Digest) == "" {
		return teachingReportInvalid("source identity is incomplete")
	}
	if strings.TrimSpace(r.Metadata.Title) == "" {
		return teachingReportInvalid("report title is missing")
	}
	if r.Metadata.ReportDate != "" {
		if _, err := time.Parse("2006-01-02", r.Metadata.ReportDate); err != nil {
			return teachingReportInvalid("report date is invalid")
		}
	}
	if len(r.ProblemSources) == 0 || len(r.Items) != len(r.ProblemSources) {
		return teachingReportInvalid("report items do not cover the source problems")
	}
	sources := make(map[string]TeachingReportProblemSource, len(r.ProblemSources))
	for _, source := range r.ProblemSources {
		if strings.TrimSpace(source.ProblemID) == "" || source.InputRevision < 1 || strings.TrimSpace(source.InputDigest) == "" {
			return teachingReportInvalid("problem source identity is incomplete")
		}
		if _, exists := sources[source.ProblemID]; exists {
			return teachingReportInvalid("duplicate problem source")
		}
		if !teachingReportAnswerStateValid(source.AnswerState) {
			return teachingReportInvalid("unknown source answer state")
		}
		if source.AssessmentStatus != "" && !source.AssessmentStatus.Valid() {
			return teachingReportInvalid("unknown source assessment state")
		}
		if r.Source.Kind == TeachingReportSourceGrading && strings.TrimSpace(source.AttemptID) == "" {
			return teachingReportInvalid("grading source attempt is missing")
		}
		if source.AssessmentStatus != "" && strings.TrimSpace(source.AssessmentDigest) == "" {
			return teachingReportInvalid("assessment source digest is missing")
		}
		if source.AnswerSource != nil {
			if err := source.AnswerSource.Validate(); err != nil {
				return teachingReportInvalid("answer source: " + err.Error())
			}
		}
		sources[source.ProblemID] = source
	}
	for i, item := range r.Items {
		if item.ProblemID != r.ProblemSources[i].ProblemID {
			return teachingReportInvalid("item order or identity differs from the frozen source")
		}
		if err := validateTeachingReportItem(item, sources[item.ProblemID]); err != nil {
			return err
		}
	}
	for _, page := range r.SourcePages {
		if strings.TrimSpace(page.AssetID) == "" || strings.TrimSpace(page.AssetDigest) == "" ||
			strings.TrimSpace(page.SourceDigest) == "" || strings.TrimSpace(page.MIME) == "" || page.Width <= 0 || page.Height <= 0 {
			return teachingReportInvalid("source page reference is incomplete")
		}
	}
	sectionIDs := make(map[string]bool, len(r.DeepSections))
	for _, section := range r.DeepSections {
		if strings.TrimSpace(section.ID) == "" || sectionIDs[section.ID] {
			return teachingReportInvalid("deep section identity is missing or duplicated")
		}
		sectionIDs[section.ID] = true
		if err := validateTeachingReportDeepSection(section, sources); err != nil {
			return err
		}
	}
	return nil
}

func teachingReportInvalid(message string) error {
	return fmt.Errorf("%w: %s", ErrTeachingReportInvalid, message)
}

func teachingReportAnswerStateValid(state TeachingReportAnswerState) bool {
	switch state {
	case TeachingReportAnswerPresent, TeachingReportAnswerBlank, TeachingReportAnswerUnreadable,
		TeachingReportAnswerUnknown, TeachingReportAnswerNotProvided:
		return true
	default:
		return false
	}
}

func validateTeachingReportItem(item TeachingReportItem, source TeachingReportProblemSource) error {
	if item.Assessment.Status != source.AssessmentStatus || item.OriginalAnswer.State != source.AnswerState {
		return teachingReportInvalid("assessment or answer state differs from the frozen source")
	}
	if item.OriginalAnswer.RawText != source.AnswerRaw || item.OriginalAnswer.CanonicalText != source.AnswerCanonical {
		return teachingReportInvalid("original answer differs from the frozen source")
	}
	if item.Assessment.Status == "" && strings.TrimSpace(item.Assessment.Reason) == "" {
		return teachingReportInvalid("missing assessment requires a reason")
	}
	switch item.Assessment.Status {
	case GradingAssessmentCorrect, GradingAssessmentWrong, GradingAssessmentProcessIssue:
		if item.OriginalAnswer.State != TeachingReportAnswerPresent {
			return teachingReportInvalid("a grading verdict requires a readable student answer")
		}
	}
	slots := make(map[string]bool, len(item.OriginalAnswer.Slots))
	for _, slot := range item.OriginalAnswer.Slots {
		if strings.TrimSpace(slot.ID) == "" || slots[slot.ID] || strings.TrimSpace(slot.EvidenceRef) == "" ||
			!teachingReportAnswerStateValid(slot.State) {
			return teachingReportInvalid("answer slot identity, state or evidence is invalid")
		}
		slots[slot.ID] = true
		if slot.State == TeachingReportAnswerBlank || slot.State == TeachingReportAnswerNotProvided {
			if strings.TrimSpace(slot.Text) != "" || strings.TrimSpace(slot.LaTeX) != "" {
				return teachingReportInvalid("an empty answer slot cannot contain an answer")
			}
		}
		if slot.State == TeachingReportAnswerPresent && !teachingReportHasContent(slot.Text, slot.LaTeX) {
			return teachingReportInvalid("a readable answer slot requires content")
		}
	}
	analysis := item.AnswerAnalysis
	switch analysis.State {
	case TeachingReportReferenceUnavailable:
		if strings.TrimSpace(analysis.UnavailableReason) == "" || len(analysis.Answers) > 0 || len(analysis.Steps) > 0 {
			return teachingReportInvalid("unavailable reference requires a reason and no fabricated answer")
		}
	case TeachingReportReferenceAvailable, TeachingReportReferenceConditional:
		if len(analysis.Answers) == 0 || strings.TrimSpace(source.SolutionDigest) == "" ||
			analysis.Evidence.SolutionDigest != source.SolutionDigest {
			return teachingReportInvalid("available reference requires answers bound to the source solution")
		}
	default:
		return teachingReportInvalid("unknown reference state")
	}
	if analysis.Evidence.SolutionDigest != "" && analysis.Evidence.SolutionDigest != source.SolutionDigest {
		return teachingReportInvalid("reference evidence belongs to another solution")
	}
	contentIDs := make(map[string]bool, len(analysis.Answers)+len(analysis.Steps))
	for _, answer := range analysis.Answers {
		if strings.TrimSpace(answer.ID) == "" || contentIDs[answer.ID] || !teachingReportHasContent(answer.Text, answer.LaTeX) {
			return teachingReportInvalid("reference answer identity or content is invalid")
		}
		contentIDs[answer.ID] = true
		if analysis.State == TeachingReportReferenceConditional && !teachingReportHasNonEmptyString(answer.Conditions) {
			return teachingReportInvalid("conditional answer requires its conditions")
		}
		if err := validateTeachingReportOrigin(answer.Origin, source); err != nil {
			return err
		}
	}
	for _, step := range analysis.Steps {
		if strings.TrimSpace(step.ID) == "" || contentIDs[step.ID] || !teachingReportHasContent(step.Text, step.LaTeX) {
			return teachingReportInvalid("calculation step identity or content is invalid")
		}
		contentIDs[step.ID] = true
		if err := validateTeachingReportOrigin(step.Origin, source); err != nil {
			return err
		}
	}
	return nil
}

func teachingReportHasContent(text, latex string) bool {
	return strings.TrimSpace(text) != "" || strings.TrimSpace(latex) != ""
}

func teachingReportHasNonEmptyString(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func validateTeachingReportOrigin(origin TeachingReportContentOrigin, source TeachingReportProblemSource) error {
	if origin.ProblemID != source.ProblemID || origin.InputRevision != source.InputRevision ||
		origin.InputDigest != source.InputDigest || origin.SolutionDigest != source.SolutionDigest {
		return teachingReportInvalid("content origin differs from the problem input or solution")
	}
	return nil
}

func validateTeachingReportDeepSection(section TeachingReportDeepSection, sources map[string]TeachingReportProblemSource) error {
	if len(section.ProblemIDs) == 0 || len(section.Blocks)+len(section.Diagrams) == 0 {
		return teachingReportInvalid("deep section requires covered problems and content")
	}
	covered := make(map[string]bool, len(section.ProblemIDs))
	for _, id := range section.ProblemIDs {
		if _, exists := sources[id]; !exists || covered[id] {
			return teachingReportInvalid("deep section refers to an unknown or repeated problem")
		}
		covered[id] = true
	}
	blocks := make(map[string]bool, len(section.Blocks))
	for _, block := range section.Blocks {
		if strings.TrimSpace(block.ID) == "" || blocks[block.ID] || len(block.Origins) == 0 {
			return teachingReportInvalid("explanation block identity, content or origin is missing")
		}
		if err := validateTeachingReportExplanationContent(block); err != nil {
			return err
		}
		blocks[block.ID] = true
		seenOrigins := make(map[string]bool, len(block.Origins))
		for _, origin := range block.Origins {
			if !covered[origin.ProblemID] || seenOrigins[origin.ProblemID] {
				return teachingReportInvalid("explanation origin is outside its section or duplicated")
			}
			seenOrigins[origin.ProblemID] = true
			if err := validateTeachingReportOrigin(origin, sources[origin.ProblemID]); err != nil {
				return err
			}
		}
	}
	for _, diagram := range section.Diagrams {
		if !covered[diagram.ProblemID] || strings.TrimSpace(diagram.AssetID) == "" || strings.TrimSpace(diagram.AssetDigest) == "" ||
			strings.TrimSpace(diagram.SpecDigest) == "" || strings.TrimSpace(diagram.SolutionDigest) == "" ||
			strings.TrimSpace(diagram.RendererVersion) == "" || strings.TrimSpace(diagram.MIME) == "" || diagram.Width <= 0 || diagram.Height <= 0 {
			return teachingReportInvalid("teaching diagram reference is incomplete or outside its section")
		}
		origin := TeachingReportContentOrigin{
			ProblemID: diagram.ProblemID, InputRevision: diagram.InputRevision,
			InputDigest: diagram.ProblemInputDigest, SolutionDigest: diagram.SolutionDigest,
		}
		if err := validateTeachingReportOrigin(origin, sources[diagram.ProblemID]); err != nil {
			return err
		}
	}
	for _, method := range section.MethodNotes {
		if strings.TrimSpace(method.Name) == "" || strings.TrimSpace(method.Description) == "" ||
			strings.TrimSpace(method.EvidenceRef) == "" || len(method.BlockIDs) == 0 {
			return teachingReportInvalid("method note requires its application and evidence")
		}
		for _, id := range method.BlockIDs {
			if !blocks[id] {
				return teachingReportInvalid("method note refers to an unknown explanation block")
			}
		}
	}
	return nil
}

func validateTeachingReportExplanationContent(block TeachingReportExplanationBlock) error {
	if block.Purpose != "" {
		if block.Purpose != TeachingReportExplanationSupplement && block.Purpose != TeachingReportExplanationPitfall {
			return teachingReportInvalid("unknown explanation purpose")
		}
		if strings.TrimSpace(block.Text) == "" || strings.TrimSpace(block.LaTeX) != "" || block.Table != nil {
			return teachingReportInvalid("explanation purpose requires a text block")
		}
	}
	carriers := 0
	if strings.TrimSpace(block.Text) != "" {
		carriers++
	}
	if strings.TrimSpace(block.LaTeX) != "" {
		carriers++
	}
	if block.Table != nil {
		carriers++
	}
	if carriers != 1 {
		return teachingReportInvalid("explanation block requires exactly one content representation")
	}
	if block.Table == nil {
		return nil
	}
	table := block.Table
	if len(table.Headers) == 0 || len(table.Rows) == 0 {
		return teachingReportInvalid("explanation table requires headers and rows")
	}
	validateCell := func(cell TeachingReportTableCell) error {
		if (strings.TrimSpace(cell.Text) == "") == (strings.TrimSpace(cell.LaTeX) == "") {
			return teachingReportInvalid("table cell requires exactly one text or formula representation")
		}
		return nil
	}
	for _, cell := range table.Headers {
		if err := validateCell(cell); err != nil {
			return err
		}
	}
	for _, row := range table.Rows {
		if len(row) != len(table.Headers) {
			return teachingReportInvalid("explanation table row does not match its columns")
		}
		for _, cell := range row {
			if err := validateCell(cell); err != nil {
				return err
			}
		}
	}
	return nil
}
