package k12

// GradingAssessmentCorrection 保留同一原始作答，显式关联原评估和纠正前驱。
// Assessment 是新结论，不是新学生输入；原终稿与原调用回执保持不变。
type GradingAssessmentCorrection struct {
	CorrectionID         string                `json:"correction_id"`
	PreviousCorrectionID string                `json:"previous_correction_id"`
	OriginalResultDigest string                `json:"original_result_digest"`
	OriginalAnswerSource *ProblemAnswerSource  `json:"original_answer_source,omitempty"`
	Reason               string                `json:"reason"`
	Assessment           GradingAssessmentItem `json:"assessment"`
	Revision             int                   `json:"revision"`
	CreatedAt            int64                 `json:"created_at"`
}

const (
	AssessmentCorrectionAnswer   = "answer"
	AssessmentCorrectionGrading  = "grading"
	AssessmentCorrectionGuidance = "guidance"
)

// EffectiveGradingAssessment 区分历史回执与当前结论，不能把纠正伪装成输入修订。
type EffectiveGradingAssessment struct {
	Original   GradingAssessmentItem        `json:"original"`
	Current    GradingAssessmentItem        `json:"current"`
	Correction *GradingAssessmentCorrection `json:"correction,omitempty"`
}
