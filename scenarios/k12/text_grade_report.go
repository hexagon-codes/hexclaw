package k12

const TextGradeAssessmentVersion = "text-grade-assessment-v1"

// TextGradeReportReference 绑定同一文字求解之后实际完成的评分回执，不改变原求解最终化。
type TextGradeReportReference struct {
	InvocationID string `json:"invocation_id"`
	ResultDigest string `json:"result_digest"`
}
