package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

const (
	GroundingSourceModeSemantic     = "semantic"
	GroundingSourceModeVerifiedText = "verified_text"
)

// EffectiveGroundingSourceMode 保持缺省历史快照的语义检索含义。
func EffectiveGroundingSourceMode(mode string) string {
	if mode == "" {
		return GroundingSourceModeSemantic
	}
	return mode
}

// SubjectGroundingWriter 是分科教材写入的可选扩展缝（§4.3：TextbookBinding 按
// Learner × Subject，不用单一全局教材字段）。subject 为六学科中文名；空 = 不分科旧语义。
type SubjectGroundingWriter interface {
	AddGroundingSubject(ctx context.Context, agentName, subject, title, content string) error
}

// SubjectGrounding 是分科教材检索的可选扩展缝：按当前题目学科优先取本学科教材，
// 无本学科教材回退通用（不分科）教材，绝不跨学科串取。subject 空 = 只检索旧版不分科桶。
type SubjectGrounding interface {
	GroundSubject(ctx context.Context, agentName, subject, knowledgePoint, grade string) (text string, found bool, err error)
}

// GroundingSnapshot freezes every mutable selector used by one tutoring-tips
// generation. A blank TextbookBindingID is an explicit legacy/text-only read
// and must never be labelled as verified textbook evidence.
type GroundingSnapshot struct {
	AgentName          string
	LearnerID          string
	Subject            string
	TextbookBindingID  string
	TextbookManifestID string
	DocumentID         string
	DocumentGeneration int64
	SourceDigest       string
	Edition            string
	Volume             string
	SegmentRefs        []string
	PageRefs           []k12.TextbookGroundingPageRef
	VectorRevisionID   string
	OwnerID            string `json:",omitempty"`
	SourceMode         string `json:",omitempty"`
}

// GroundingEvidenceReceipt 只记录本次批改实际消费的教材命中身份，不包含
// 教材正文、检索分数、提示词或儿童资料。
type GroundingEvidenceReceipt struct {
	TextbookBindingID  string `json:"textbook_binding_id"`
	TextbookManifestID string `json:"textbook_manifest_id"`
	DocumentID         string `json:"document_id"`
	DocumentGeneration int64  `json:"document_generation"`
	VectorRevisionID   string `json:"vector_revision_id"`
	QueryDigest        string `json:"query_digest"`
	ChunkID            string `json:"chunk_id"`
	LogicalPage        int    `json:"logical_page"`
	PDFPage            int    `json:"pdf_page"`
	SourceDigest       string `json:"source_digest"`
	CitationDigest     string `json:"citation_digest"`
	SourceMode         string `json:"source_mode,omitempty"`
}

// GroundingTextSource 保存本次实际采用的验证页正文及确定性定位依据。
type GroundingTextSource struct {
	LogicalPage    int      `json:"logical_page"`
	PDFPage        int      `json:"pdf_page"`
	Content        string   `json:"content"`
	ContentDigest  string   `json:"content_digest"`
	SegmentRefs    []string `json:"segment_refs"`
	LocationMethod string   `json:"location_method"`
	MatchedTerms   []string `json:"matched_terms"`
}

// GroundingSnapshotResult 把本次 pinned 查询的可消费正文与命中回执绑定在
// 同一返回值中，避免由另一次独立 search 事后补造引用。
type GroundingSnapshotResult struct {
	Text     string                     `json:"text"`
	Found    bool                       `json:"found"`
	Receipts []GroundingEvidenceReceipt `json:"receipts"`
	Sources  []GroundingTextSource      `json:"sources,omitempty"`
}

// SnapshotGrounding is the canonical multi-query retrieval seam.
type SnapshotGrounding interface {
	FreezeGroundingSnapshot(ctx context.Context, requested GroundingSnapshot) (GroundingSnapshot, error)
	GroundSnapshot(ctx context.Context, snapshot GroundingSnapshot, knowledgePoint, grade string) (text string, found bool, err error)
}

// SnapshotGroundingEvidence 是加法证据缝。旧实现仍可满足 SnapshotGrounding，
// 但只有实现本接口并返回完整回执的命中才可声明为已核验教材依据。
type SnapshotGroundingEvidence interface {
	GroundSnapshotWithEvidence(
		ctx context.Context,
		snapshot GroundingSnapshot,
		knowledgePoint, grade string,
	) (GroundingSnapshotResult, error)
}

func (result GroundingSnapshotResult) validate(snapshot GroundingSnapshot) error {
	mode := EffectiveGroundingSourceMode(snapshot.SourceMode)
	if mode != GroundingSourceModeSemantic && mode != GroundingSourceModeVerifiedText {
		return fmt.Errorf("grounding: invalid source mode")
	}
	if mode == GroundingSourceModeVerifiedText && snapshot.VectorRevisionID != "" {
		return fmt.Errorf("grounding: verified text carries a vector revision")
	}
	if !result.Found {
		if strings.TrimSpace(result.Text) != "" || len(result.Receipts) != 0 || len(result.Sources) != 0 {
			return fmt.Errorf("grounding: no-hit result carries evidence")
		}
		return nil
	}
	if strings.TrimSpace(result.Text) == "" || len(result.Receipts) == 0 {
		return fmt.Errorf("grounding: verified hit has no receipt")
	}
	allowedSegments := make(map[string]struct{}, len(snapshot.SegmentRefs))
	for _, segmentRef := range snapshot.SegmentRefs {
		allowedSegments[segmentRef] = struct{}{}
	}
	type pageSegmentKey struct {
		segmentRef  string
		logicalPage int
		pdfPage     int
	}
	pageSegments := make(map[pageSegmentKey]struct{})
	for _, page := range snapshot.PageRefs {
		for _, segmentRef := range page.SegmentRefs {
			pageSegments[pageSegmentKey{
				segmentRef: segmentRef, logicalPage: page.LogicalPage, pdfPage: page.PDFPage,
			}] = struct{}{}
		}
	}
	seen := make(map[GroundingEvidenceReceipt]struct{}, len(result.Receipts))
	for index, receipt := range result.Receipts {
		if receipt.TextbookBindingID != snapshot.TextbookBindingID ||
			EffectiveGroundingSourceMode(receipt.SourceMode) != mode ||
			receipt.TextbookManifestID != snapshot.TextbookManifestID ||
			receipt.DocumentID != snapshot.DocumentID ||
			receipt.DocumentGeneration != snapshot.DocumentGeneration ||
			receipt.VectorRevisionID != snapshot.VectorRevisionID ||
			receipt.SourceDigest != snapshot.SourceDigest ||
			receipt.ChunkID == "" || receipt.ChunkID != strings.TrimSpace(receipt.ChunkID) ||
			receipt.LogicalPage < 1 || receipt.PDFPage < 1 ||
			!validGroundingReceiptDigest(receipt.SourceDigest) ||
			!validGroundingReceiptDigest(receipt.CitationDigest) ||
			!strings.HasPrefix(receipt.QueryDigest, "sha256:") ||
			!validGroundingReceiptDigest(strings.TrimPrefix(receipt.QueryDigest, "sha256:")) {
			return fmt.Errorf("grounding: invalid evidence receipt %d", index)
		}
		if _, allowed := allowedSegments[receipt.ChunkID]; !allowed {
			return fmt.Errorf("grounding: receipt chunk is outside frozen scope")
		}
		if _, allowed := pageSegments[pageSegmentKey{
			segmentRef: receipt.ChunkID, logicalPage: receipt.LogicalPage, pdfPage: receipt.PDFPage,
		}]; !allowed {
			return fmt.Errorf("grounding: receipt page is outside frozen scope")
		}
		if _, duplicate := seen[receipt]; duplicate {
			return fmt.Errorf("grounding: duplicate evidence receipt")
		}
		seen[receipt] = struct{}{}
	}
	if mode == GroundingSourceModeVerifiedText {
		return result.validateTextSources()
	}
	if len(result.Sources) != 0 {
		return fmt.Errorf("grounding: semantic result carries direct text sources")
	}
	return nil
}

func (result GroundingSnapshotResult) validateTextSources() error {
	if len(result.Sources) == 0 {
		return fmt.Errorf("grounding: verified text has no frozen source")
	}
	type pageKey struct{ logical, physical int }
	sources := make(map[pageKey]GroundingTextSource, len(result.Sources))
	var parts []string
	for _, source := range result.Sources {
		key := pageKey{source.LogicalPage, source.PDFPage}
		_, duplicate := sources[key]
		digest := sha256.Sum256([]byte(source.Content))
		if duplicate || source.LogicalPage < 1 || source.PDFPage < 1 ||
			strings.TrimSpace(source.Content) == "" || len(source.SegmentRefs) == 0 ||
			hex.EncodeToString(digest[:]) != source.ContentDigest ||
			(source.LocationMethod != "exact_lesson_title" && source.LocationMethod != "exact_knowledge_term") ||
			len(source.MatchedTerms) == 0 {
			return fmt.Errorf("grounding: invalid frozen text source")
		}
		for _, term := range source.MatchedTerms {
			if strings.TrimSpace(term) == "" {
				return fmt.Errorf("grounding: invalid frozen location evidence")
			}
		}
		sources[key] = source
		parts = append(parts, strings.TrimSpace(source.Content))
	}
	if result.Text != strings.Join(parts, "\n\n") {
		return fmt.Errorf("grounding: frozen text differs from consumed context")
	}
	seen := make(map[pageKey]map[string]struct{}, len(sources))
	for _, receipt := range result.Receipts {
		key := pageKey{receipt.LogicalPage, receipt.PDFPage}
		source, found := sources[key]
		if !found || source.ContentDigest != receipt.CitationDigest {
			return fmt.Errorf("grounding: citation differs from frozen text")
		}
		if seen[key] == nil {
			seen[key] = make(map[string]struct{})
		}
		seen[key][receipt.ChunkID] = struct{}{}
	}
	for key, source := range sources {
		if len(seen[key]) != len(source.SegmentRefs) {
			return fmt.Errorf("grounding: frozen text segment set differs from receipts")
		}
		for _, segment := range source.SegmentRefs {
			if _, found := seen[key][segment]; !found {
				return fmt.Errorf("grounding: frozen text segment has no receipt")
			}
		}
	}
	return nil
}

func validateGroundingEvidenceReceiptIdentity(receipt GroundingEvidenceReceipt) error {
	mode := EffectiveGroundingSourceMode(receipt.SourceMode)
	if mode != GroundingSourceModeSemantic && mode != GroundingSourceModeVerifiedText ||
		mode == GroundingSourceModeSemantic && (receipt.VectorRevisionID == "" || receipt.VectorRevisionID != strings.TrimSpace(receipt.VectorRevisionID)) ||
		mode == GroundingSourceModeVerifiedText && receipt.VectorRevisionID != "" {
		return fmt.Errorf("grounding: invalid durable source mode")
	}
	for _, value := range []string{
		receipt.TextbookBindingID,
		receipt.TextbookManifestID,
		receipt.DocumentID,
		receipt.ChunkID,
	} {
		if value == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("grounding: invalid durable receipt identity")
		}
	}
	if receipt.DocumentGeneration < 1 || receipt.LogicalPage < 1 || receipt.PDFPage < 1 ||
		!validGroundingReceiptDigest(receipt.SourceDigest) ||
		!validGroundingReceiptDigest(receipt.CitationDigest) ||
		!strings.HasPrefix(receipt.QueryDigest, "sha256:") ||
		!validGroundingReceiptDigest(strings.TrimPrefix(receipt.QueryDigest, "sha256:")) {
		return fmt.Errorf("grounding: invalid durable receipt proof")
	}
	return nil
}

func validGroundingReceiptDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func cloneGroundingEvidenceReceipts(
	values []GroundingEvidenceReceipt,
) []GroundingEvidenceReceipt {
	if len(values) == 0 {
		return []GroundingEvidenceReceipt{}
	}
	return append([]GroundingEvidenceReceipt(nil), values...)
}

// validateTextbookSubject 校验分科教材学科：空 = 不分科旧语义（合法）；非空必须是六学科之一。
func validateTextbookSubject(subject string) error {
	if subject == "" || k12.ValidTextbookSubject(subject) {
		return nil
	}
	return fmt.Errorf("%w: 学科只允许 %s 或空（不分科），got %q",
		ErrInvalidInput, strings.Join(k12.TextbookSubjects, "/"), subject)
}

// AddGrounding 把家长上传的教材内容按辅导实例隔离入库，供辅导要点召回。
// subject 非空时按学科分科入库（六学科枚举校验）；空保持不分科旧语义（前向兼容）。
// 写侧不支持分科而请求带学科时报错，绝不静默丢学科降级。
func (d Deps) AddGrounding(ctx context.Context, agentName, subject, title, content string) error {
	if strings.TrimSpace(agentName) == "" || strings.TrimSpace(title) == "" || strings.TrimSpace(content) == "" {
		return fmt.Errorf("%w: agent / title / content 必填", ErrInvalidInput)
	}
	if err := validateTextbookSubject(subject); err != nil {
		return err
	}
	if subject != "" {
		sw, ok := d.Grounding.(SubjectGroundingWriter)
		if !ok {
			return fmt.Errorf("usecase: 未配置分科教材写入能力（不降级为不分科，防学科错绑）")
		}
		return sw.AddGroundingSubject(ctx, agentName, subject, title, content)
	}
	w, ok := d.Grounding.(GroundingWriter)
	if !ok {
		return fmt.Errorf("usecase: 未配置教材写入能力")
	}
	return w.AddGrounding(ctx, agentName, title, content)
}
