package k12

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const ReviewSourceEvidenceReviewed = "reviewed_sources"

var ErrInvalidReviewSourceGroup = errors.New("invalid reviewed source group")

// ReviewSourceGroup 冻结已经核验的同题来源关联与代表题面，不承载新的求解结论。
// 学习状态不属于成员身份；原题或批改输入变化后，旧关联不再用于归并。
type ReviewSourceGroup struct {
	GroupID                  string                    `json:"group_id"`
	AgentName                string                    `json:"agent_name"`
	CanonicalRecordID        string                    `json:"canonical_record_id"`
	CanonicalStatement       string                    `json:"canonical_statement"`
	CanonicalStatementDigest string                    `json:"canonical_statement_digest"`
	Evidence                 ReviewSourceGroupEvidence `json:"evidence"`
	Members                  []ReviewSourceGroupMember `json:"members"`
	CreatedAt                int64                     `json:"created_at"`
}

// ReviewSourceGroupEvidence 记录显式核验依据；说明文字不改变相同来源关联的幂等身份。
type ReviewSourceGroupEvidence struct {
	Kind          string   `json:"kind"`
	Note          string   `json:"note"`
	SourceDigests []string `json:"source_digests"`
}

// ReviewSourceGroupMember 绑定原错题原文与一份完整冻结批改来源。
type ReviewSourceGroupMember struct {
	RecordID               string   `json:"record_id"`
	QuestionDigest         string   `json:"question_digest"`
	JobID                  string   `json:"job_id"`
	ProblemID              string   `json:"problem_id"`
	AttemptID              string   `json:"attempt_id"`
	ConfirmedVersion       int      `json:"confirmed_version"`
	InputRevision          int      `json:"input_revision"`
	InputDigest            string   `json:"input_digest"`
	ResultDigest           string   `json:"result_digest"`
	PageDigest             string   `json:"page_digest"`
	SourceNumberPath       []string `json:"source_number_path,omitempty"`
	ParentSourceNumberPath []string `json:"parent_source_number_path,omitempty"`
}

// ReviewSourceTextDigest 对原文逐字节取摘要，不折叠空格、公式或单位。
func ReviewSourceTextDigest(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}

// Normalized 深拷贝并校验关联，按完整成员身份生成稳定ID。
// 空ID和题面摘要由服务端补齐；已提供的值必须与正文一致。
func (g ReviewSourceGroup) Normalized() (ReviewSourceGroup, error) {
	if !reviewSourceIdentifier(g.AgentName) || !reviewSourceIdentifier(g.CanonicalRecordID) ||
		strings.TrimSpace(g.CanonicalStatement) == "" || g.CreatedAt <= 0 || len(g.Members) < 2 ||
		g.Evidence.Kind != ReviewSourceEvidenceReviewed || strings.TrimSpace(g.Evidence.Note) == "" {
		return ReviewSourceGroup{}, fmt.Errorf("%w: owner, canonical statement, reviewed evidence and at least two members are required", ErrInvalidReviewSourceGroup)
	}
	statementDigest := ReviewSourceTextDigest(g.CanonicalStatement)
	if g.CanonicalStatementDigest != "" && g.CanonicalStatementDigest != statementDigest {
		return ReviewSourceGroup{}, fmt.Errorf("%w: canonical statement digest mismatch", ErrInvalidReviewSourceGroup)
	}
	g.CanonicalStatementDigest = statementDigest
	g.Members = append([]ReviewSourceGroupMember(nil), g.Members...)
	records, pages := map[string]bool{}, map[string]bool{}
	for i := range g.Members {
		member := &g.Members[i]
		if !reviewSourceIdentifier(member.RecordID) || !reviewSourceIdentifier(member.JobID) ||
			!reviewSourceIdentifier(member.ProblemID) || !reviewSourceIdentifier(member.AttemptID) ||
			!reviewSourceIdentifier(member.InputDigest) || !reviewSourceIdentifier(member.ResultDigest) ||
			member.ConfirmedVersion < 1 || member.InputRevision < 1 ||
			!reviewSourceSHA256(member.QuestionDigest) || !reviewSourceSHA256(member.PageDigest) || records[member.RecordID] {
			return ReviewSourceGroup{}, fmt.Errorf("%w: member identity is incomplete or duplicated", ErrInvalidReviewSourceGroup)
		}
		member.SourceNumberPath = append([]string(nil), member.SourceNumberPath...)
		member.ParentSourceNumberPath = append([]string(nil), member.ParentSourceNumberPath...)
		for _, path := range [][]string{member.SourceNumberPath, member.ParentSourceNumberPath} {
			for _, label := range path {
				if strings.TrimSpace(label) == "" {
					return ReviewSourceGroup{}, fmt.Errorf("%w: source numbering contains an empty label", ErrInvalidReviewSourceGroup)
				}
			}
		}
		records[member.RecordID], pages[member.PageDigest] = true, true
	}
	if !records[g.CanonicalRecordID] {
		return ReviewSourceGroup{}, fmt.Errorf("%w: canonical record is not a member", ErrInvalidReviewSourceGroup)
	}
	sort.Slice(g.Members, func(i, j int) bool { return g.Members[i].RecordID < g.Members[j].RecordID })
	pageDigests := make([]string, 0, len(pages))
	for digest := range pages {
		pageDigests = append(pageDigests, digest)
	}
	sort.Strings(pageDigests)
	if len(g.Evidence.SourceDigests) > 0 {
		provided := map[string]bool{}
		for _, digest := range g.Evidence.SourceDigests {
			if !pages[digest] {
				return ReviewSourceGroup{}, fmt.Errorf("%w: evidence source digest is not a member page", ErrInvalidReviewSourceGroup)
			}
			provided[digest] = true
		}
		if len(provided) != len(pages) {
			return ReviewSourceGroup{}, fmt.Errorf("%w: evidence does not cover every source page", ErrInvalidReviewSourceGroup)
		}
	}
	g.Evidence.SourceDigests = pageDigests
	// 题面清理器升级不改变同一次来源审核的请求身份，重放仍读取首次冻结的正文。
	identity := struct {
		AgentName, CanonicalRecordID string
		Members                      []ReviewSourceGroupMember
	}{g.AgentName, g.CanonicalRecordID, g.Members}
	raw, err := json.Marshal(identity)
	if err != nil {
		return ReviewSourceGroup{}, fmt.Errorf("%w: encode identity: %v", ErrInvalidReviewSourceGroup, err)
	}
	id := "rsg-" + ReviewSourceTextDigest("k12:review-source-group:v1\x00"+string(raw))
	if g.GroupID != "" && g.GroupID != id {
		return ReviewSourceGroup{}, fmt.Errorf("%w: group identity mismatch", ErrInvalidReviewSourceGroup)
	}
	g.GroupID = id
	return g, nil
}

func reviewSourceIdentifier(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
}

func reviewSourceSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
