package k12

// TeachingDiagramAsset 是随当前题目讲法冻结的图示引用；原始SVG不进入公开JSON。
type TeachingDiagramAsset struct {
	AssetID            string `json:"asset_id"`
	AssetDigest        string `json:"asset_digest"`
	SpecDigest         string `json:"spec_digest"`
	RendererVersion    string `json:"renderer_version"`
	ProblemID          string `json:"problem_id"`
	InputRevision      int    `json:"input_revision"`
	ProblemInputDigest string `json:"problem_input_digest"`
	SolutionDigest     string `json:"solution_digest"`
	MIME               string `json:"mime"`
	Width              int    `json:"width"`
	Height             int    `json:"height"`
	AltText            string `json:"alt_text"`
}
