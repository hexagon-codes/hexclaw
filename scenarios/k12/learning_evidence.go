package k12

// LearningEvidence 描述实际事件的来源，不参与既有掌握策略计算。
type LearningEvidence struct {
	Kind       string `json:"kind"`
	Source     string `json:"source,omitempty"`
	EventID    string `json:"event_id,omitempty"`
	OccurredAt int64  `json:"occurred_at,omitempty"`
	BasisID    string `json:"basis_id,omitempty"`
	BasisAt    int64  `json:"basis_at,omitempty"`
}

const (
	LearningEvidenceUnknown     = "unknown"
	LearningEvidencePrompted    = "prompted_answer"
	LearningEvidenceIndependent = "independent_answer"
	LearningEvidenceDelayed     = "delayed_retest"
	LearningEvidenceManual      = "human_confirmed"
)

// LearningEvidenceOrUnknown 只补读取语义，不为旧记录回填历史事件。
func LearningEvidenceOrUnknown(events []LearningEvidence) []LearningEvidence {
	if len(events) == 0 {
		return []LearningEvidence{{Kind: LearningEvidenceUnknown}}
	}
	return append([]LearningEvidence(nil), events...)
}
