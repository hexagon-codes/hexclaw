package k12

// VerifiedTextbookReadRequest 按已冻结的教材身份读取正文，不跟随当前活动版本。
type VerifiedTextbookReadRequest struct {
	OwnerID   string
	AgentName string
	Subject   string
	Scope     TextbookGroundingScope
}

// VerifiedTextbookPage 将正文摘要、验证页码与原目录段引用保存在同一来源记录中。
type VerifiedTextbookPage struct {
	LogicalPage   int      `json:"logical_page"`
	PDFPage       int      `json:"pdf_page"`
	Content       string   `json:"content"`
	ContentDigest string   `json:"content_digest"`
	SegmentRefs   []string `json:"segment_refs"`
	LessonTitles  []string `json:"lesson_titles,omitempty"`
}
