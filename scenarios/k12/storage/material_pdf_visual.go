package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
)

// MaterialPDFVisualObject 是由同一原 PDF 派生的持久页图或合成图。
type MaterialPDFVisualObject struct {
	Page        int    `json:"page,omitempty"`
	Digest      string `json:"digest"`
	StoragePath string `json:"storage_path"`
	SizeBytes   int64  `json:"size_bytes"`
	MediaType   string `json:"media_type"`
}
type MaterialPDFVisualSource struct {
	InputDigest     string                    `json:"input_digest"`
	SourceDigest    string                    `json:"source_digest"`
	SourceRevision  int64                     `json:"source_revision"`
	Pages           []int                     `json:"pages"`
	DPI             int                       `json:"dpi"`
	Layout          string                    `json:"layout"`
	Objects         []MaterialPDFVisualObject `json:"objects"`
	CompositeDigest string                    `json:"composite_digest"`
}

func (s *Store) MaterialPDFSourceIdentity(ctx context.Context, p MaterialPreparation) (corpus, digest string, err error) {
	if err = materialSourceCurrent(ctx, s.db, p); err != nil {
		return
	}
	err = s.db.QueryRowContext(ctx, `SELECT c.corpus_alias,m.source_digest FROM k12_material_manifests m JOIN kb_semantic_corpora c ON c.corpus_uid=m.corpus_uid AND c.owner_id=m.owner_id WHERE m.owner_id=? AND m.document_id=? AND m.source_revision=?`, p.OwnerID, p.DocumentID, p.SourceRevision).Scan(&corpus, &digest)
	return
}

func materialPDFVisualSourceValid(p MaterialPreparation, source MaterialPDFVisualSource) bool {
	if len(p.Candidate.VisualPDFPages) == 0 || source.InputDigest != p.InputDigest || source.SourceRevision != p.SourceRevision || source.SourceDigest == "" || !reflect.DeepEqual(source.Pages, p.Candidate.VisualPDFPages) || source.DPI != 150 || source.Layout != "vertical-source-order-v1" || len(source.Objects) != len(source.Pages)+1 {
		return false
	}
	for i, o := range source.Objects {
		if len(o.Digest) != 64 || o.StoragePath == "" || o.SizeBytes <= 0 || o.MediaType != "image/png" {
			return false
		}
		if i < len(source.Pages) && o.Page != source.Pages[i] {
			return false
		}
	}
	return source.Objects[len(source.Objects)-1].Page == 0 && source.Objects[len(source.Objects)-1].Digest == source.CompositeDigest
}

func materialPDFVisualSource(ctx context.Context, db dbHandle, p MaterialPreparation) (*MaterialPDFVisualSource, error) {
	var raw, digest string
	err := db.QueryRowContext(ctx, `SELECT result_json,result_digest FROM k12_material_invocations WHERE task_id=? AND operation='local_pdf_source' AND status='succeeded'`, p.TaskID).Scan(&raw, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var source MaterialPDFVisualSource
	if json.Unmarshal([]byte(raw), &source) != nil || digest != problemAssetRequestDigest([]byte(raw)) || !materialPDFVisualSourceValid(p, source) {
		return nil, ErrProblemAssetEvidence
	}
	var original string
	if err = db.QueryRowContext(ctx, `SELECT source_digest FROM k12_material_manifests WHERE owner_id=? AND document_id=? AND source_revision=?`, p.OwnerID, p.DocumentID, p.SourceRevision).Scan(&original); err != nil {
		return nil, err
	}
	if original != source.SourceDigest {
		return nil, ErrProblemAssetEvidence
	}
	return &source, nil
}

// LoadMaterialPDFSourceImage 冷恢复只使用已提交对象，不重复渲染，更不再次读取外部资源。
func (s *Store) LoadMaterialPDFSourceImage(ctx context.Context, p MaterialPreparation) ([]byte, string, bool, error) {
	if err := materialSourceCurrent(ctx, s.db, p); err != nil {
		return nil, "", false, err
	}
	source, err := materialPDFVisualSource(ctx, s.db, p)
	if err != nil || source == nil {
		return nil, "", false, err
	}
	for _, o := range source.Objects {
		data, e := os.ReadFile(o.StoragePath)
		if e != nil {
			return nil, "", true, e
		}
		if int64(len(data)) != o.SizeBytes || strings.TrimPrefix(problemAssetRequestDigest(data), "sha256:") != o.Digest {
			return nil, "", true, ErrProblemAssetEvidence
		}
		if o.Page == 0 {
			return data, o.Digest, true, nil
		}
	}
	return nil, "", true, ErrProblemAssetEvidence
}

// SaveMaterialPDFVisualSource 将对象元数据及本地来源回执原子提交，原候选与 manifest 不变。
func (s *Store) SaveMaterialPDFVisualSource(ctx context.Context, p MaterialPreparation, source MaterialPDFVisualSource) error {
	if !materialPDFVisualSourceValid(p, source) {
		return ErrProblemAssetEvidence
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	locked, err := tx.ExecContext(ctx, `UPDATE k12_material_preparations SET updated_at=updated_at WHERE task_id=? AND state='running' AND input_digest=?`, p.TaskID, p.InputDigest)
	if err != nil {
		return err
	}
	n, _ := locked.RowsAffected()
	if n != 1 {
		return ErrMaterialPreparationFenced
	}
	if err = materialSourceCurrent(ctx, tx, p); err != nil {
		return err
	}
	var corpus, original string
	if err = tx.QueryRowContext(ctx, `SELECT corpus_uid,source_digest FROM k12_material_manifests WHERE owner_id=? AND document_id=? AND source_revision=?`, p.OwnerID, p.DocumentID, p.SourceRevision).Scan(&corpus, &original); err != nil {
		return err
	}
	if original != source.SourceDigest {
		return ErrProblemAssetEvidence
	}
	var unresolved bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_material_invocations WHERE task_id=? AND status IN ('sent','outcome_unknown'))`, p.TaskID).Scan(&unresolved); err != nil {
		return err
	}
	if unresolved {
		return ErrMaterialPreparationUnknown
	}
	raw, _ := json.Marshal(source)
	digest := problemAssetRequestDigest(raw)
	for _, o := range source.Objects {
		data, e := os.ReadFile(o.StoragePath)
		if e != nil {
			return e
		}
		if int64(len(data)) != o.SizeBytes || strings.TrimPrefix(problemAssetRequestDigest(data), "sha256:") != o.Digest {
			return ErrProblemAssetEvidence
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO kb_ingest_blobs(owner_id,corpus_uid,sha256,storage_path,size_bytes,media_type,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(owner_id,corpus_uid,sha256) DO NOTHING`, p.OwnerID, corpus, o.Digest, o.StoragePath, o.SizeBytes, o.MediaType, nowUnix()); err != nil {
			return err
		}
		var path string
		var size int64
		if err = tx.QueryRowContext(ctx, `SELECT storage_path,size_bytes FROM kb_ingest_blobs WHERE owner_id=? AND corpus_uid=? AND sha256=?`, p.OwnerID, corpus, o.Digest).Scan(&path, &size); err != nil {
			return err
		}
		if path != o.StoragePath || size != o.SizeBytes {
			return ErrProblemAssetEvidence
		}
	}
	id := problemAssetRequestDigest([]byte(p.TaskID + "\x00local_pdf_source"))
	if _, err = tx.ExecContext(ctx, `INSERT INTO k12_material_invocations(invocation_id,task_id,operation,request_digest,execution_kind,status,result_json,result_digest,created_at,updated_at) VALUES(?,?,'local_pdf_source',?,'local_source','succeeded',?,?,?,?) ON CONFLICT(invocation_id) DO NOTHING`, id, p.TaskID, p.InputDigest, string(raw), digest, nowUnix(), nowUnix()); err != nil {
		return err
	}
	var saved string
	if err = tx.QueryRowContext(ctx, `SELECT result_digest FROM k12_material_invocations WHERE invocation_id=?`, id).Scan(&saved); err != nil {
		return err
	}
	if saved != digest {
		return ErrProblemAssetEvidence
	}
	return tx.Commit()
}
