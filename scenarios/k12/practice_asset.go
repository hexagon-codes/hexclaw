package k12

// PracticeAssetSource 冻结练习采用的答案版本与教学目标；不携带学生作答或模型调用身份。
type PracticeAssetSource struct {
	OwnerID        string `json:"owner_id"`
	AssetID        string `json:"asset_id"`
	AssetVersion   int    `json:"asset_version"`
	AssetRevision  int    `json:"asset_revision"`
	FactsDigest    string `json:"facts_digest"`
	GradeTerm      string `json:"grade_term"`
	KnowledgePoint string `json:"knowledge_point"`
	OriginalReview bool   `json:"original_review,omitempty"`
}

// PracticeAssetQuery 先确定教学目标，再查权威资产；原题复习只接受指定原题。
type PracticeAssetQuery struct {
	OwnerID, AgentName, Subject, GradeTerm, KnowledgePoint string
	OriginalQuestion                                       string
	OriginalReview                                         bool
	ExcludedHashes                                         []string
}

type PracticeAssetCandidate struct {
	Version ProblemAssetVersion
	Source  PracticeAssetSource
}
