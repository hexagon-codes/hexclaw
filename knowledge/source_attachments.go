package knowledge

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// DocumentSourceImageURIs 只为详情投影读取当前来源附件，不修改原文或检索内容。
func (s *SemanticIndexService) DocumentSourceImageURIs(ctx context.Context, owner, corpus, document string, generation int64) (map[string]string, error) {
	repo, ok := s.ingestRepo.(interface {
		SourceManifestForGeneration(context.Context, string, string, int64) (*SourceManifest, error)
	})
	if !ok {
		return nil, nil
	}
	source, err := s.ingestRepo.GetIngestDocument(ctx, owner, document)
	if err != nil {
		return nil, err
	}
	if source.CorpusAlias != corpus || source.ContentGeneration != generation {
		return nil, ErrSemanticIndexNotFound
	}
	m, err := repo.SourceManifestForGeneration(ctx, owner, document, generation)
	if err != nil || m == nil {
		return nil, err
	}
	images := make(map[string]string)
	for _, object := range m.Objects {
		if object.Missing || object.StoragePath == "" || object.Kind != "image" {
			continue
		}
		data, readErr := os.ReadFile(object.StoragePath)
		if readErr != nil {
			return nil, fmt.Errorf("Read source attachment: %w", readErr)
		}
		images[object.Path] = "data:" + object.MediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
	}
	return images, nil
}

func (r *SQLiteSemanticIndexRepository) SourceManifestForGeneration(ctx context.Context, owner, document string, generation int64) (*SourceManifest, error) {
	exists, err := sourceManifestTableExists(ctx, r.db.QueryRowContext)
	if err != nil || !exists {
		return nil, err
	}
	var raw string
	err = r.db.QueryRowContext(ctx, `SELECT manifest_json FROM kb_ingest_source_manifests WHERE owner_id=? AND document_id=? AND content_generation=?`, owner, document, generation).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest SourceManifest
	if err = json.Unmarshal([]byte(raw), &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// PersistSourceAttachment 复用同一对象仓库；释放函数须在来源事务提交或放弃后调用。
func (s *SemanticIndexService) PersistSourceAttachment(ctx context.Context, source PersistedIngestDocument, media string, body io.Reader) (IngestBlob, func(), error) {
	if s == nil || s.blobStore == nil {
		return IngestBlob{}, nil, ErrDocumentIngestUnavailable
	}
	return s.blobStore.Persist(ctx, source.OwnerID, source.CorpusAlias, CreateDocumentInput{IdempotencyKey: "source-attachment", Filename: "attachment.png", MediaType: media, Body: body})
}

func sourceManifestTableExists(ctx context.Context, query func(context.Context, string, ...any) *sql.Row) (bool, error) {
	var exists bool
	err := query(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='kb_ingest_source_manifests')`).Scan(&exists)
	return exists, err
}

func sourceAttachmentReferenced(ctx context.Context, query func(context.Context, string, ...any) *sql.Row, path string) (bool, error) {
	exists, err := sourceManifestTableExists(ctx, query)
	if err != nil || !exists {
		return false, err
	}
	var referenced bool
	err = query(ctx, `SELECT EXISTS(SELECT 1 FROM kb_ingest_source_manifests m,json_each(m.manifest_json,'$.objects') o WHERE json_extract(o.value,'$.storage_path')=?)`, path).Scan(&referenced)
	if err != nil || referenced {
		return referenced, err
	}
	if exists, e := materialSourceReceiptTableExists(ctx, query); e != nil || !exists {
		return false, e
	}
	err = query(ctx, `SELECT EXISTS(SELECT 1 FROM k12_material_invocations i JOIN k12_material_preparations p ON p.task_id=i.task_id JOIN kb_ingest_document_sources d ON d.owner_id=p.owner_id AND d.document_id=p.document_id AND d.content_generation=p.source_revision,json_each(CASE WHEN i.operation='local_pdf_source' AND i.status='succeeded' THEN i.result_json ELSE '{}' END,'$.objects') o WHERE i.operation='local_pdf_source' AND i.status='succeeded' AND json_extract(o.value,'$.storage_path')=?)`, path).Scan(&referenced)
	return referenced, err
}

func sourceAttachmentGCPaths(ctx context.Context, tx *sql.Tx, owner, doc string) ([]string, error) {
	exists, err := sourceManifestTableExists(ctx, tx.QueryRowContext)
	if err != nil || !exists {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT json_extract(o.value,'$.storage_path') FROM kb_ingest_source_manifests m,json_each(m.manifest_json,'$.objects') o WHERE m.owner_id=? AND m.document_id=? AND COALESCE(json_extract(o.value,'$.storage_path'),'')<>''`, owner, doc)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if exists, e := materialSourceReceiptTableExists(ctx, tx.QueryRowContext); e != nil || !exists {
		return paths, e
	}
	derived, e := tx.QueryContext(ctx, `SELECT DISTINCT json_extract(o.value,'$.storage_path') FROM k12_material_invocations i JOIN k12_material_preparations p ON p.task_id=i.task_id,json_each(CASE WHEN i.operation='local_pdf_source' AND i.status='succeeded' THEN i.result_json ELSE '{}' END,'$.objects') o WHERE p.owner_id=? AND p.document_id=? AND i.operation='local_pdf_source' AND i.status='succeeded'`, owner, doc)
	if e != nil {
		return nil, e
	}
	defer derived.Close()
	for derived.Next() {
		var path string
		if e = derived.Scan(&path); e != nil {
			return nil, e
		}
		paths = append(paths, path)
	}
	return paths, derived.Err()
}

func saveSourceAttachmentBlobsTx(ctx context.Context, tx *sql.Tx, job KnowledgeJob, manifest *SourceManifest, at int64) error {
	for _, o := range manifest.Objects {
		if o.StoragePath == "" {
			continue
		}
		if o.Missing || len(o.Digest) != 64 || o.SizeBytes <= 0 || strings.TrimSpace(o.MediaType) == "" {
			return fmt.Errorf("%w: invalid source attachment", ErrInvalidDocumentUpload)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO kb_ingest_blobs(owner_id,corpus_uid,sha256,storage_path,size_bytes,media_type,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(owner_id,corpus_uid,sha256) DO NOTHING`, job.OwnerID, job.CorpusUID, o.Digest, o.StoragePath, o.SizeBytes, o.MediaType, at); err != nil {
			return err
		}
		var path string
		var size int64
		if err := tx.QueryRowContext(ctx, `SELECT storage_path,size_bytes FROM kb_ingest_blobs WHERE owner_id=? AND corpus_uid=? AND sha256=?`, job.OwnerID, job.CorpusUID, o.Digest).Scan(&path, &size); err != nil {
			return err
		}
		if path != o.StoragePath || size != o.SizeBytes {
			return fmt.Errorf("%w: source attachment metadata changed", ErrIdempotencyConflict)
		}
	}
	return nil
}

func materialSourceReceiptTableExists(ctx context.Context, query func(context.Context, string, ...any) *sql.Row) (bool, error) {
	var exists bool
	err := query(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='k12_material_invocations')`).Scan(&exists)
	return exists, err
}
