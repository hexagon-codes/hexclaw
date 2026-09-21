package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const documentReparsePrefix = "document-reparse|"
const documentParserVersion = "layout-visual-v2"

// ReparseDocument 只建立候选代次，原正文、原文件和已发布索引保持可读。
func (s *SemanticIndexService) ReparseDocument(ctx context.Context, ownerID, corpusID, documentID, key string, expectedGeneration int64) (CreateDocumentResult, error) {
	repo, ok := s.ingestRepo.(interface {
		ReparseDocument(context.Context, string, string, string, string, int64, *VisionRouteSnapshot) (CreateDocumentResult, error)
	})
	if !ok {
		return CreateDocumentResult{}, ErrDocumentIngestUnavailable
	}
	route, err := s.freezeVisionRoute(ctx)
	if err != nil {
		return CreateDocumentResult{}, err
	}
	return repo.ReparseDocument(ctx, ownerID, corpusID, documentID, key, expectedGeneration, route)
}

type documentReparse struct {
	JobID, OwnerID, CorpusUID, DocumentID             string
	Generation, BaseGeneration, BindingVersion        int64
	SourceDigest, ParserVersion, RevisionID, Prepared string
	Published                                         sql.NullInt64
}

// reparseForJob 先识别命令，再以持久化候选记录核对身份；普通任务无需候选表。
func reparseForJob(ctx context.Context, q semanticDBQueryer, job KnowledgeJob) (*documentReparse, error) {
	var root, key string
	err := q.QueryRowContext(ctx, `SELECT j.job_id,j.idempotency_key FROM kb_knowledge_jobs j
 WHERE j.job_id=? OR j.job_id=? ORDER BY CASE WHEN j.job_id=? THEN 0 ELSE 1 END LIMIT 1`,
		job.ParentJobID, job.JobID, job.ParentJobID).Scan(&root, &key)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(key, documentReparsePrefix) {
		return nil, nil
	}
	var c documentReparse
	err = q.QueryRowContext(ctx, `SELECT job_id,owner_id,corpus_uid,document_id,content_generation,
 base_generation,base_binding_version,source_digest,parser_version,target_revision_id,published_at
 FROM kb_document_reparses WHERE job_id=?`, root).Scan(&c.JobID, &c.OwnerID, &c.CorpusUID, &c.DocumentID,
		&c.Generation, &c.BaseGeneration, &c.BindingVersion, &c.SourceDigest, &c.ParserVersion, &c.RevisionID, &c.Published)
	if err != nil {
		return nil, err
	}
	if c.OwnerID != job.OwnerID || c.CorpusUID != job.CorpusUID || c.DocumentID != job.DocumentID || c.Generation != job.DocumentGeneration {
		return nil, ErrJobFenced
	}
	return &c, nil
}

func validateReparseBase(ctx context.Context, q semanticDBQueryer, c *documentReparse) error {
	if c.Published.Valid || c.ParserVersion != documentParserVersion {
		return ErrJobFenced
	}
	var valid int
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM kb_semantic_document_bindings b
 JOIN kb_documents d ON d.id=b.document_id AND d.corpus_uid=b.corpus_uid
 JOIN kb_semantic_corpora co ON co.corpus_uid=b.corpus_uid AND co.owner_id=b.owner_id
 JOIN kb_ingest_document_sources s ON s.document_id=b.document_id AND s.content_generation=b.content_generation
 JOIN kb_ingest_document_sources n ON n.document_id=b.document_id AND n.content_generation=?
 WHERE b.document_id=? AND b.owner_id=? AND b.corpus_uid=? AND b.content_generation=? AND b.version=?
 AND b.lifecycle_state='active' AND b.text_state='ready' AND d.deleted=0 AND d.status='indexed'
 AND s.blob_sha256=? AND n.blob_sha256=? AND COALESCE(co.active_revision_id,'')=?)`,
		c.Generation, c.DocumentID, c.OwnerID, c.CorpusUID, c.BaseGeneration, c.BindingVersion, c.SourceDigest, c.SourceDigest, c.RevisionID).Scan(&valid)
	if err != nil {
		return err
	}
	if valid != 1 {
		return ErrJobFenced
	}
	return nil
}

func (r *SQLiteSemanticIndexRepository) ReparseDocument(ctx context.Context, ownerID, corpusID, documentID, key string, expected int64, route *VisionRouteSnapshot) (CreateDocumentResult, error) {
	if err := validateSemanticScope(ownerID, corpusID); err != nil {
		return CreateDocumentResult{}, err
	}
	key = strings.TrimSpace(key)
	if key == "" || expected < 1 || documentID == "" {
		return CreateDocumentResult{}, ErrInvalidDocumentRetry
	}
	storageKey := documentReparsePrefix + key
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return CreateDocumentResult{}, err
	}
	defer tx.Rollback()
	state, err := loadSemanticPolicyState(ctx, tx, ownerID, corpusID)
	if err != nil {
		return CreateDocumentResult{}, err
	}
	var priorDoc, priorJob string
	var priorGeneration int64
	err = tx.QueryRowContext(ctx, `SELECT j.document_id,j.job_id,c.base_generation FROM kb_knowledge_jobs j
 JOIN kb_document_reparses c ON c.job_id=j.job_id WHERE j.owner_id=? AND j.corpus_uid=? AND j.idempotency_key=?`, ownerID, state.corpusUID, storageKey).Scan(&priorDoc, &priorJob, &priorGeneration)
	if err == nil {
		if priorDoc != documentID || priorGeneration != expected {
			return CreateDocumentResult{}, ErrIdempotencyConflict
		}
		return documentReparseResult(ctx, tx, state.corpusUID, documentID, priorJob)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CreateDocumentResult{}, err
	}
	var base, version int64
	var digest, oldContent, title, source, sourceType string
	err = tx.QueryRowContext(ctx, `SELECT b.content_generation,b.version,s.blob_sha256,d.content,d.title,d.source,d.source_type
 FROM kb_semantic_document_bindings b JOIN kb_documents d ON d.id=b.document_id
 JOIN kb_ingest_document_sources s ON s.document_id=b.document_id AND s.content_generation=b.content_generation
 WHERE b.owner_id=? AND b.corpus_uid=? AND b.document_id=? AND b.lifecycle_state='active' AND b.text_state='ready'
 AND d.deleted=0 AND d.status='indexed'`, ownerID, state.corpusUID, documentID).Scan(&base, &version, &digest, &oldContent, &title, &source, &sourceType)
	if errors.Is(err, sql.ErrNoRows) {
		return CreateDocumentResult{}, ErrDocumentRetryNotAllowed
	}
	if err != nil {
		return CreateDocumentResult{}, err
	}
	if base != expected {
		return CreateDocumentResult{}, ErrIdempotencyConflict
	}
	var unresolved int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM kb_ingest_page_invocations i JOIN kb_knowledge_jobs j ON j.job_id=i.job_id
 WHERE j.owner_id=? AND j.corpus_uid=? AND j.document_id=? AND i.source_digest=? AND i.status IN ('running','outcome_unknown') AND `+unsupersededOCRInvocation, ownerID, state.corpusUID, documentID, digest).Scan(&unresolved)
	if err != nil {
		return CreateDocumentResult{}, err
	}
	if unresolved > 0 {
		return CreateDocumentResult{}, ErrOCRPageInvocationOutcomeUnknown
	}
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM kb_embedding_batch_manifests m JOIN kb_knowledge_jobs j ON j.job_id=m.job_id
 WHERE j.owner_id=? AND j.corpus_uid=? AND j.document_id=? AND m.state IN ('in_flight','outcome_unknown')`, ownerID, state.corpusUID, documentID).Scan(&unresolved)
	if err != nil {
		return CreateDocumentResult{}, err
	}
	if unresolved > 0 {
		return CreateDocumentResult{}, ErrEmbeddingBatchOutcomeUnknown
	}
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM kb_knowledge_jobs WHERE owner_id=? AND corpus_uid=? AND document_id=?
 AND state IN ('queued','running','retry_wait')`, ownerID, state.corpusUID, documentID).Scan(&unresolved)
	if err != nil {
		return CreateDocumentResult{}, err
	}
	if unresolved > 0 {
		return CreateDocumentResult{}, ErrDocumentRetryNotAllowed
	}
	// 索引切换中的候选不能冻结到即将被替换的 revision。
	if (state.desiredRevision != "" && state.desiredRevision != state.activeRevision) || (state.activeRevision == "" && state.selection.Kind != EmbeddingSelectionDisabled) {
		return CreateDocumentResult{}, ErrDocumentRetryNotAllowed
	}
	var generation int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(content_generation),0)+1 FROM kb_semantic_document_generations WHERE corpus_uid=? AND document_id=?`, state.corpusUID, documentID).Scan(&generation); err != nil {
		return CreateDocumentResult{}, err
	}
	jobID, err := semanticID("job")
	if err != nil {
		return CreateDocumentResult{}, err
	}
	now := semanticNowMillis()
	oldDoc, _ := json.Marshal(&Document{ID: documentID, Title: title, Content: oldContent, Source: source, SourceType: sourceType, Status: "indexed"})
	rows, err := tx.QueryContext(ctx, `SELECT id,content,chunk_index,page_start,page_end,source_digest,source_offset_start,source_offset_end FROM kb_chunks WHERE doc_id=? ORDER BY chunk_index`, documentID)
	if err != nil {
		return CreateDocumentResult{}, err
	}
	oldChunks := []*Chunk{}
	for rows.Next() {
		var c Chunk
		var ps, pe, os, oe sql.NullInt64
		var sd sql.NullString
		if err = rows.Scan(&c.ID, &c.Content, &c.Index, &ps, &pe, &sd, &os, &oe); err != nil {
			rows.Close()
			return CreateDocumentResult{}, err
		}
		c.DocID = documentID
		c.PageStart = int(ps.Int64)
		c.PageEnd = int(pe.Int64)
		c.SourceDigest = sd.String
		c.SourceOffsetStart = os.Int64
		c.SourceOffsetEnd = oe.Int64
		oldChunks = append(oldChunks, &c)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return CreateDocumentResult{}, err
	}
	rows.Close()
	oldJSON, _ := json.Marshal(oldChunks)
	if _, err = tx.ExecContext(ctx, `INSERT INTO kb_semantic_document_generations(owner_id,corpus_uid,document_id,content_generation,created_at) VALUES(?,?,?,?,?)`, ownerID, state.corpusUID, documentID, generation, now); err != nil {
		return CreateDocumentResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO kb_ingest_document_sources(document_id,owner_id,corpus_uid,content_generation,blob_sha256,original_name,extension,media_type,size_bytes,agent_id,learner_id,subject,grade,created_at,updated_at)
 SELECT document_id,owner_id,corpus_uid,?,blob_sha256,original_name,extension,media_type,size_bytes,agent_id,learner_id,subject,grade,?,? FROM kb_ingest_document_sources
 WHERE document_id=? AND content_generation=? AND owner_id=? AND corpus_uid=?`, generation, now, now, documentID, base, ownerID, state.corpusUID); err != nil {
		return CreateDocumentResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO kb_knowledge_jobs(job_id,parent_job_id,kind,owner_id,corpus_uid,document_id,document_generation,target_revision_id,idempotency_key,state,stage,attempt,cancel_requested,lease_owner,lease_epoch,last_error,created_at,updated_at)
 VALUES(?,NULL,'ingest',?,?,?,?,NULL,?,'queued','extracting',0,0,'',0,'',?,?)`, jobID, ownerID, state.corpusUID, documentID, generation, storageKey, now, now); err != nil {
		return CreateDocumentResult{}, err
	}
	if err = persistVisionRouteSnapshotTx(ctx, tx, jobID, route, now); err != nil {
		return CreateDocumentResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO kb_document_reparses(job_id,owner_id,corpus_uid,document_id,content_generation,base_generation,base_binding_version,source_digest,parser_version,target_revision_id,old_document_json,old_chunks_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		jobID, ownerID, state.corpusUID, documentID, generation, base, version, digest, documentParserVersion, state.activeRevision, string(oldDoc), string(oldJSON), now); err != nil {
		return CreateDocumentResult{}, err
	}
	result, err := documentReparseResult(ctx, tx, state.corpusUID, documentID, jobID)
	if err != nil {
		return CreateDocumentResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return CreateDocumentResult{}, err
	}
	return result, nil
}

// 命令回执描述仍在服务的当前版本，候选状态由独立 job 提供。
func documentReparseResult(ctx context.Context, q semanticDBQueryer, corpusUID, documentID, jobID string) (CreateDocumentResult, error) {
	result := CreateDocumentResult{DocumentID: documentID, JobID: jobID}
	err := q.QueryRowContext(ctx, `SELECT b.text_state,COALESCE(rd.vector_state,'disabled')
 FROM kb_semantic_document_bindings b JOIN kb_semantic_corpora c ON c.corpus_uid=b.corpus_uid
 LEFT JOIN kb_revision_documents rd ON rd.corpus_uid=b.corpus_uid AND rd.document_id=b.document_id
 AND rd.content_generation=b.content_generation AND rd.revision_id=c.active_revision_id
 WHERE b.corpus_uid=? AND b.document_id=?`, corpusUID, documentID).Scan(&result.TextIndexState, &result.VectorIndexState)
	return result, err
}

// reparseBuildPrefix 仅给该候选的嵌入任务提供隔离视图；搜索与普通重建仍读正式表。
func reparseBuildPrefix(ctx context.Context, q semanticDBQueryer, job KnowledgeJob) (string, error) {
	c, err := reparseForJob(ctx, q, job)
	if err != nil || c == nil {
		return "", err
	}
	if err = validateReparseBase(ctx, q, c); err != nil {
		return "", err
	}
	root := "'" + strings.ReplaceAll(c.JobID, "'", "''") + "'"
	return `WITH kb_chunks AS (SELECT id,doc_id,content,chunk_index FROM kb_reparse_chunks WHERE job_id=` + root + `),
 kb_semantic_document_bindings AS (SELECT document_id,owner_id,corpus_uid,content_generation,'active' AS lifecycle_state,'ready' AS text_state FROM kb_document_reparses WHERE job_id=` + root + `) `, nil
}

func (r *SQLiteSemanticIndexRepository) prepareReparseTx(ctx context.Context, tx *sql.Tx, job KnowledgeJob, c *documentReparse, prepared PreparedIngestDocument, now time.Time) error {
	if err := validateReparseBase(ctx, tx, c); err != nil {
		return err
	}
	for i, chunk := range prepared.Chunks {
		chunk.ID = fmt.Sprintf("%s-g%d-%d", job.DocumentID, job.DocumentGeneration, i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO kb_reparse_chunks(job_id,id,doc_id,content,chunk_index) VALUES(?,?,?,?,?)`, job.JobID, chunk.ID, job.DocumentID, chunk.Content, i); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(prepared)
	if err != nil {
		return err
	}
	c.Prepared = string(payload)
	if _, err = tx.ExecContext(ctx, `UPDATE kb_document_reparses SET prepared_json=? WHERE job_id=? AND prepared_json='' AND published_at IS NULL`, c.Prepared, c.JobID); err != nil {
		return err
	}
	warnings, err := json.Marshal(prepared.Warnings)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE kb_ingest_document_sources SET page_count=?,warnings_json=?,updated_at=? WHERE document_id=? AND content_generation=?`, prepared.PageCount, string(warnings), now.UnixMilli(), job.DocumentID, job.DocumentGeneration); err != nil {
		return err
	}
	state, err := loadSemanticPolicyStateByUID(ctx, tx, job.OwnerID, job.CorpusUID)
	if err != nil {
		return err
	}
	if c.RevisionID != "" && len(prepared.Chunks) > 0 {
		if err = insertRevisionDocumentTx(ctx, tx, job.CorpusUID, c.RevisionID, job.DocumentID, job.DocumentGeneration, int64(len(prepared.Chunks)), now.UnixMilli()); err != nil {
			return err
		}
		if err = createEmbedDocumentJobWithParentTx(ctx, tx, job.OwnerID, state, c.RevisionID, job.DocumentID, job.DocumentGeneration, int64(len(prepared.Chunks)), job.JobID, now.UnixMilli()); err != nil {
			return err
		}
	} else {
		if err = r.publishReparseTx(ctx, tx, job, c, now); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE kb_knowledge_jobs SET state='succeeded',stage='text_indexing',pages_done=?,pages_total=?,chunks_done=?,chunks_total=?,lease_epoch=lease_epoch+1,lease_owner='',lease_expires_at=NULL,heartbeat_at=NULL,updated_at=?,finished_at=? WHERE job_id=?`,
		prepared.PageCount, prepared.PageCount, len(prepared.Chunks), len(prepared.Chunks), now.UnixMilli(), now.UnixMilli(), job.JobID)
	return err
}

func (r *SQLiteSemanticIndexRepository) publishReparseTx(ctx context.Context, tx *sql.Tx, job KnowledgeJob, c *documentReparse, now time.Time) error {
	if err := validateReparseBase(ctx, tx, c); err != nil {
		return err
	}
	if c.Prepared == "" {
		if err := tx.QueryRowContext(ctx, `SELECT prepared_json FROM kb_document_reparses WHERE job_id=?`, c.JobID).Scan(&c.Prepared); err != nil {
			return err
		}
	}
	var prepared PreparedIngestDocument
	if err := json.Unmarshal([]byte(c.Prepared), &prepared); err != nil {
		return err
	}
	if err := validatePreparedIngestDocument(prepared); err != nil {
		return err
	}
	if c.RevisionID != "" && len(prepared.Chunks) > 0 {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM kb_revision_vectors WHERE corpus_uid=? AND revision_id=? AND document_id=? AND content_generation=?`, c.CorpusUID, c.RevisionID, c.DocumentID, c.Generation).Scan(&count); err != nil {
			return err
		}
		if count != len(prepared.Chunks) {
			return ErrJobFenced
		}
	}
	projection, err := cjkFTSProjectionCurrentTx(ctx, tx)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM kb_chunks_fts WHERE chunk_id IN (SELECT id FROM kb_chunks WHERE doc_id=?)`, c.DocumentID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM kb_chunks_fts_v2 WHERE chunk_id IN (SELECT id FROM kb_chunks WHERE doc_id=?)`, c.DocumentID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM kb_chunks WHERE doc_id=?`, c.DocumentID); err != nil {
		return err
	}
	for _, chunk := range prepared.Chunks {
		if _, err = tx.ExecContext(ctx, `INSERT INTO kb_chunks(id,doc_id,content,chunk_index,embedding,created_at,page_start,page_end,source_digest,source_offset_start,source_offset_end) VALUES(?,?,?,?,NULL,?,?,?,?,?,?)`, chunk.ID, c.DocumentID, chunk.Content, chunk.Index, now, nullablePositiveInt(chunk.PageStart), nullablePositiveInt(chunk.PageEnd), chunk.SourceDigest, nullableOffset(chunk.SourceOffsetStart, chunk.SourceOffsetEnd, false), nullableOffset(chunk.SourceOffsetStart, chunk.SourceOffsetEnd, true)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO kb_chunks_fts(content,chunk_id) VALUES(?,?)`, chunk.Content, chunk.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO kb_chunks_fts_v2(tokens,chunk_id) VALUES(?,?)`, cjkFTSIndexText(chunk.Content), chunk.ID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE kb_documents SET content=?,chunk_count=?,updated_at=? WHERE id=? AND corpus_uid=? AND deleted=0`, prepared.Document.Content, len(prepared.Chunks), now, c.DocumentID, c.CorpusUID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE kb_semantic_document_bindings SET content_generation=?,version=version+1,text_state='ready',updated_at=? WHERE document_id=? AND corpus_uid=? AND content_generation=? AND version=? AND lifecycle_state='active'`, c.Generation, now.UnixMilli(), c.DocumentID, c.CorpusUID, c.BaseGeneration, c.BindingVersion)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrJobFenced
	}
	if _, err = tx.ExecContext(ctx, `UPDATE kb_document_reparses SET published_at=? WHERE job_id=? AND published_at IS NULL`, now.UnixMilli(), c.JobID); err != nil {
		return err
	}
	state, err := loadSemanticPolicyStateByUID(ctx, tx, c.OwnerID, c.CorpusUID)
	if err != nil {
		return err
	}
	if err = bumpSemanticContentVersionTx(ctx, tx, &state, now.UnixMilli()); err != nil {
		return err
	}
	if c.RevisionID != "" {
		if len(prepared.Chunks) == 0 {
			if err = insertRevisionDocumentTx(ctx, tx, c.CorpusUID, c.RevisionID, c.DocumentID, c.Generation, 0, now.UnixMilli()); err != nil {
				return err
			}
			if err = markEmptyRevisionDocumentReadyTx(ctx, tx, c.CorpusUID, c.RevisionID, c.DocumentID, c.Generation, now.UnixMilli()); err != nil {
				return err
			}
		}
		if err = refreshActiveRevisionAggregatesTx(ctx, tx, c.CorpusUID, c.RevisionID, now.UnixMilli()); err != nil {
			return err
		}
	}
	if err = restoreCJKFTSCurrentTx(ctx, tx, projection); err != nil {
		return err
	}
	job.Kind = KnowledgeJobIngest
	job.JobID = c.JobID
	return r.reconcileDocumentIngestLifecycleTx(ctx, tx, job, now)
}
