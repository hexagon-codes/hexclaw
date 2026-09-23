package k12

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"golang.org/x/text/unicode/norm"
)

const ProblemAssetNormalizationVersion = "k12-facts-v1"

// ProblemAssetVerification 绑定用例已判定合格的题目事实和实际成功执行；不接受前端 verified 标记。
// 存储核对回执身份与结果摘要，题目完整性和答案质量由发布用例负责。
type ProblemAssetVerification struct {
	GenerationInvocationID  string                  `json:"generation_invocation_id,omitempty"`
	GenerationResultDigest  string                  `json:"generation_result_digest,omitempty"`
	SolverOutputDigest      string                  `json:"solver_output_digest,omitempty"`
	VerificationInputDigest string                  `json:"verification_input_digest,omitempty"`
	VerificationRunID       string                  `json:"verification_run_id,omitempty"`
	AgentName               string                  `json:"agent_name"`
	InvocationID            string                  `json:"invocation_id"`
	InputDigest             string                  `json:"input_digest"`
	ResultDigest            string                  `json:"result_digest"`
	FactsDigest             string                  `json:"facts_digest"`
	Kind                    ProblemAnswerSourceKind `json:"kind"`
	Policy                  string                  `json:"policy"`
}

type ProblemAssetPublication struct {
	AnswerResultJSON string                   `json:"answer_result_json"`
	OwnerID          string                   `json:"owner_id"`
	PublicationID    string                   `json:"publication_id"`
	Facts            ProblemAssetFacts        `json:"facts"`
	Answer           string                   `json:"answer"`
	Verification     ProblemAssetVerification `json:"verification"`
	// 替代已存在版本须绑定已读取的资格修订，普通自动积累不携带替代字段。
	ReplacesVersion  int `json:"replaces_version,omitempty"`
	ExpectedRevision int `json:"expected_revision,omitempty"`
}

// ProblemAssetVersion 是不可变答案版本；Revision 是发布时的资格修订，不是产品版本号。
type ProblemAssetVersion struct {
	AnswerResultJSON string            `json:"answer_result_json"`
	OwnerID          string            `json:"owner_id"`
	AssetID          string            `json:"asset_id"`
	Version          int               `json:"version"`
	Revision         int               `json:"revision"`
	Facts            ProblemAssetFacts `json:"facts"`
	FactsDigest      string            `json:"facts_digest"`
	Answer           string            `json:"answer"`
	CreatedAt        int64             `json:"created_at"`
}

// ProblemAssetAdoption 每个任务题目修订独立记录采用，不携带学生评分或伪造调用身份。
type ProblemAssetAdoption struct {
	AdoptionID    string `json:"adoption_id"`
	OwnerID       string `json:"owner_id"`
	JobID         string `json:"job_id"`
	ProblemID     string `json:"problem_id"`
	InputRevision int    `json:"input_revision"`
	InputDigest   string `json:"input_digest"`
	AssetID       string `json:"asset_id"`
	AssetVersion  int    `json:"asset_version"`
	AssetRevision int    `json:"asset_revision"`
	FactsDigest   string `json:"facts_digest"`
	CreatedAt     int64  `json:"created_at"`
}

// ProblemAssetFacts 只描述决定答案的题目事实，不包含学生作答、批改结论或个人偏好。
// 公共材料、选项顺序、图表对象与影响答案的上下文均参与身份，不能仅以题干相似匹配。
type ProblemAssetFacts struct {
	Subject        string               `json:"subject"`
	Stem           string               `json:"stem"`
	SharedMaterial []string             `json:"shared_material,omitempty"`
	Options        []ProblemAssetOption `json:"options,omitempty"`
	VisualFacts    []string             `json:"visual_facts,omitempty"`
	Objects        []ProblemAssetObject `json:"objects,omitempty"`
	AnswerContext  map[string]string    `json:"answer_context,omitempty"`
}

type ProblemAssetOption struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}

type ProblemAssetObject struct {
	Role   string `json:"role"`
	Digest string `json:"digest"`
}

// ProblemAssetIdentity 的 FactsDigest 可用于绑定验证输入，Key 另外包含可信 owner 作用域。
// 身份相同只证明规范化输入相同，不证明事实完整、答案正确或当前版本仍可用。
type ProblemAssetIdentity struct {
	OwnerID              string `json:"owner_id"`
	NormalizationVersion string `json:"normalization_version"`
	FactsDigest          string `json:"facts_digest"`
	Key                  string `json:"key"`
}

// ExactIdentity 仅规范化 NFC、换行和文本外围空白；保留大小写、数值、单位和内部空白。
// 不改写数学运算、选项对应关系，不以感知图像相似度代替对象摘要。
func (f ProblemAssetFacts) ExactIdentity(ownerID string) (ProblemAssetIdentity, error) {
	if strings.TrimSpace(ownerID) == "" || strings.TrimSpace(f.Subject) == "" || strings.TrimSpace(f.Stem) == "" {
		return ProblemAssetIdentity{}, errors.New("problem asset identity requires owner, subject and stem")
	}
	normalized := ProblemAssetFacts{Subject: normalizeAssetText(f.Subject), Stem: normalizeAssetText(f.Stem)}
	for _, material := range f.SharedMaterial {
		normalized.SharedMaterial = append(normalized.SharedMaterial, normalizeAssetText(material))
	}
	for _, option := range f.Options {
		normalized.Options = append(normalized.Options, ProblemAssetOption{
			Label: normalizeAssetText(option.Label), Text: normalizeAssetText(option.Text),
		})
	}
	for _, fact := range f.VisualFacts {
		normalized.VisualFacts = append(normalized.VisualFacts, normalizeAssetText(fact))
	}
	for _, object := range f.Objects {
		if strings.TrimSpace(object.Role) == "" || strings.TrimSpace(object.Digest) == "" {
			return ProblemAssetIdentity{}, errors.New("problem asset object requires role and digest")
		}
		// 摘要是对象仓库提供的身份，不进行大小写或前缀猜测。
		normalized.Objects = append(normalized.Objects, object)
	}
	if len(f.AnswerContext) > 0 {
		normalized.AnswerContext = make(map[string]string, len(f.AnswerContext))
		for key, value := range f.AnswerContext {
			normalized.AnswerContext[key] = normalizeAssetText(value)
		}
	}
	encoded, err := json.Marshal(struct {
		Version string            `json:"normalization_version"`
		Facts   ProblemAssetFacts `json:"facts"`
	}{ProblemAssetNormalizationVersion, normalized})
	if err != nil {
		return ProblemAssetIdentity{}, err
	}
	identity := ProblemAssetIdentity{OwnerID: ownerID, NormalizationVersion: ProblemAssetNormalizationVersion,
		FactsDigest: problemAssetDigest(encoded)}
	scope, err := json.Marshal([]string{ownerID, identity.NormalizationVersion, identity.FactsDigest})
	if err != nil {
		return ProblemAssetIdentity{}, err
	}
	identity.Key = problemAssetDigest(scope)
	return identity, nil
}

func normalizeAssetText(value string) string {
	return norm.NFC.String(strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n")))
}

func problemAssetDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type ProblemAnswerSourceKind string

const (
	ProblemAnswerModel         ProblemAnswerSourceKind = "model"
	ProblemAnswerDeterministic ProblemAnswerSourceKind = "deterministic"
	ProblemAnswerAsset         ProblemAnswerSourceKind = "asset"
)

// ProblemAnswerSource 显式区分本次执行与跨任务资产采用，资产 ID 不得填入调用 ID。
// Validate 只校验契约形状；实际回执、执行类型、资产资格和输入绑定必须由存储层核验。
type ProblemAnswerSource struct {
	Kind          ProblemAnswerSourceKind `json:"kind"`
	FactsDigest   string                  `json:"facts_digest"`
	InvocationID  string                  `json:"invocation_id,omitempty"`
	AssetID       string                  `json:"asset_id,omitempty"`
	AssetVersion  int                     `json:"asset_version,omitempty"`
	AssetRevision int                     `json:"asset_revision,omitempty"`
	AdoptionID    string                  `json:"adoption_id,omitempty"`
}

func (s ProblemAnswerSource) Validate() error {
	if strings.TrimSpace(s.FactsDigest) == "" {
		return errors.New("answer source requires a facts digest")
	}
	switch s.Kind {
	case ProblemAnswerModel, ProblemAnswerDeterministic:
		if strings.TrimSpace(s.InvocationID) == "" || s.AssetID != "" || s.AssetVersion != 0 || s.AssetRevision != 0 || s.AdoptionID != "" {
			return errors.New("executed answer source requires only its actual invocation")
		}
	case ProblemAnswerAsset:
		if s.InvocationID != "" || strings.TrimSpace(s.AssetID) == "" || s.AssetVersion < 1 || s.AssetRevision < 1 || strings.TrimSpace(s.AdoptionID) == "" {
			return errors.New("asset answer source requires version, revision and adoption without an invocation")
		}
	default:
		return errors.New("unknown answer source kind")
	}
	return nil
}
