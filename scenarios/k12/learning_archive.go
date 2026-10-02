package k12

const (
	LearningArchiveSchemaVersion = "v1"
	PrintSourceLearningArchive   = "learning_archive"
)

// LearningArchiveScope 冻结一次学习档案导出的 Tutor 与当前学期范围。
type LearningArchiveScope struct {
	Agent     string `json:"agent"`
	GradeTerm string `json:"grade_term"`
}

// LearningArchiveObjectCounts 是五个顶层对象的唯一计数投影。
type LearningArchiveObjectCounts struct {
	WeeklyReview  int `json:"weekly_review"`
	Mistakes      int `json:"mistakes"`
	PracticeSets  int `json:"practice_sets"`
	Accumulation  int `json:"accumulation"`
	CreativeWorks int `json:"creative_works"`
}

// LearningArchiveAttachment 携带 canonical 相对引用所需的原始图片与内容身份。
type LearningArchiveAttachment struct {
	RelativePath string `json:"relative_path"`
	SHA256       string `json:"sha256"`
	MediaType    string `json:"media_type"`
	ByteSize     int64  `json:"byte_size"`
	DataBase64   string `json:"data_base64"`
}

// LearningArchiveExportV1 是 Markdown/PDF/Word 共用的冻结导出事实。
type LearningArchiveExportV1 struct {
	SchemaVersion     string                      `json:"schema_version"`
	Scope             LearningArchiveScope        `json:"scope"`
	AsOf              int64                       `json:"as_of"`
	SourceDigest      string                      `json:"source_digest"`
	ObjectCounts      LearningArchiveObjectCounts `json:"object_counts"`
	ArtifactID        string                      `json:"artifact_id"`
	CanonicalMarkdown string                      `json:"canonical_markdown"`
	Attachments       []LearningArchiveAttachment `json:"attachments,omitempty"`
}
