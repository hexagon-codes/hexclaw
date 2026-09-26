package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// SourceManifest 保存解析器实际提取的结构，不把检索切片当作来源结构。
type SourceManifest struct {
	SchemaVersion int                     `json:"schema_version"`
	ParserVersion string                  `json:"parser_version"`
	SourceDigest  string                  `json:"source_digest"`
	Blocks        []SourceContentBlock    `json:"blocks"`
	Objects       []SourceContentObject   `json:"objects,omitempty"`
	Relations     []SourceContentRelation `json:"relations,omitempty"`
	Questions     []SourceQuestion        `json:"questions,omitempty"`
}

// SourceQuestion 是交换资料声明的题目事实；它不携带验证资格、数据库身份或学生作答。
type SourceQuestion struct {
	RecordID        string              `json:"record_id"`
	BlockID         string              `json:"block_id"`
	Line            int                 `json:"line"`
	Facts           SourceQuestionFacts `json:"facts"`
	ReferenceAnswer string              `json:"reference_answer,omitempty"`
	SourceLabel     string              `json:"source_label,omitempty"`
	SourceLocation  string              `json:"source_location,omitempty"`
	SourcePage      int                 `json:"source_page,omitempty"`
	Issues          []string            `json:"issues,omitempty"`
}
type SourceQuestionFacts struct {
	Subject        string                 `json:"subject"`
	Stem           string                 `json:"stem"`
	SharedMaterial []string               `json:"shared_material,omitempty"`
	Options        []SourceQuestionOption `json:"options,omitempty"`
	VisualFacts    []string               `json:"visual_facts,omitempty"`
	Objects        []SourceQuestionObject `json:"objects,omitempty"`
	AnswerContext  map[string]string      `json:"answer_context,omitempty"`
}
type SourceQuestionOption struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}
type SourceQuestionObject struct {
	Role   string `json:"role"`
	Digest string `json:"digest"`
}
type SourceContentBlock struct {
	ID         string            `json:"block_id"`
	Kind       string            `json:"kind"`
	Text       string            `json:"text"`
	Page       int               `json:"page,omitempty"`
	ListID     string            `json:"list_id,omitempty"`
	ListLevel  int               `json:"list_level,omitempty"`
	ObjectIDs  []string          `json:"object_ids,omitempty"`
	Cells      []SourceTableCell `json:"cells,omitempty"`
	Incomplete bool              `json:"incomplete,omitempty"`
}
type SourceTableCell struct {
	Row           int                  `json:"row"`
	Column        int                  `json:"column"`
	ColumnSpan    int                  `json:"column_span"`
	VerticalMerge string               `json:"vertical_merge,omitempty"`
	Blocks        []SourceContentBlock `json:"blocks"`
}
type SourceContentObject struct {
	Issue       string `json:"issue,omitempty"`
	StoragePath string `json:"storage_path,omitempty"`
	MediaType   string `json:"media_type,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	ID          string `json:"object_id"`
	Path        string `json:"path"`
	Digest      string `json:"digest,omitempty"`
	Kind        string `json:"kind"`
	Caption     string `json:"caption,omitempty"`
	Missing     bool   `json:"missing,omitempty"`
}
type SourceContentRelation struct {
	Kind string `json:"kind"`
	From string `json:"from"`
	To   string `json:"to"`
}

// saveSourceManifestTx 与解析产物同事务提交；重解析候选使用独立代次，切换前不可被旧代次读到。
func saveSourceManifestTx(ctx context.Context, tx *sql.Tx, job KnowledgeJob, source PersistedIngestDocument, manifest *SourceManifest, at int64) error {
	if manifest == nil {
		return nil
	}
	if manifest.SchemaVersion != 1 || manifest.ParserVersion == "" || (manifest.SourceDigest != "" && manifest.SourceDigest != source.SHA256) {
		return fmt.Errorf("%w: invalid source manifest", ErrInvalidDocumentUpload)
	}
	if err := saveSourceAttachmentBlobsTx(ctx, tx, job, manifest, at); err != nil {
		return err
	}
	manifest.SourceDigest = source.SHA256
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO kb_ingest_source_manifests(owner_id,document_id,content_generation,source_digest,manifest_json,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(owner_id,document_id,content_generation) DO NOTHING`, job.OwnerID, job.DocumentID, job.DocumentGeneration, source.SHA256, string(raw), at)
	if err != nil {
		return err
	}
	if inserted, _ := result.RowsAffected(); inserted == 1 {
		return nil
	}
	var existing string
	if err = tx.QueryRowContext(ctx, `SELECT manifest_json FROM kb_ingest_source_manifests WHERE owner_id=? AND document_id=? AND content_generation=?`, job.OwnerID, job.DocumentID, job.DocumentGeneration).Scan(&existing); err != nil {
		return err
	}
	if existing != string(raw) {
		return fmt.Errorf("%w: source manifest changed within the same generation", ErrIdempotencyConflict)
	}
	return nil
}
