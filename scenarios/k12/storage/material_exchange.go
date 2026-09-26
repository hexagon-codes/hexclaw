package k12storage

import "github.com/hexagon-codes/hexclaw/scenarios/k12"

// MaterialStructuredQuestion 从通用来源快照映射，交换包 ID 只作为出处，不替代本服务任务与资产身份。
type MaterialStructuredQuestion struct {
	RecordID        string                `json:"record_id"`
	BlockID         string                `json:"block_id"`
	Line            int                   `json:"line"`
	Facts           k12.ProblemAssetFacts `json:"facts"`
	ReferenceAnswer string                `json:"reference_answer,omitempty"`
	SourceLabel     string                `json:"source_label,omitempty"`
	SourceLocation  string                `json:"source_location,omitempty"`
	SourcePage      int                   `json:"source_page,omitempty"`
	Issues          []string              `json:"issues,omitempty"`
}

func extractStructuredMaterialQuestions(records []MaterialStructuredQuestion) ([]MaterialCandidate, bool) {
	complete := true
	items := make([]MaterialCandidate, 0, len(records))
	for _, record := range records {
		candidate := MaterialCandidate{ID: record.BlockID + ":question", BlockID: record.BlockID, Line: record.Line, Facts: record.Facts, ReferenceAnswer: record.ReferenceAnswer, SourceRecordID: record.RecordID, SourceLabel: record.SourceLabel, SourceLocation: record.SourceLocation, SourcePage: record.SourcePage, Issues: record.Issues, SourceBlockIDs: []string{record.BlockID}, SourceLocations: []MaterialSourceLocation{{BlockID: record.BlockID, Line: record.Line}}}
		if record.ReferenceAnswer != "" {
			candidate.ReferenceBlockIDs = []string{record.BlockID + ":answer"}
			candidate.ReferenceLocations = []MaterialSourceLocation{{BlockID: record.BlockID + ":answer", Line: record.Line}}
		}
		if len(candidate.Issues) > 0 {
			complete = false
		}
		items = append(items, candidate)
	}
	return items, complete
}
