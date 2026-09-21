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

const documentRecoveryPrefix = "document-recovery|"

// 已消费的替代决定只解除原调用的阻塞；替代调用自身未知时仍由同一账本阻塞。
const unsupersededOCRInvocation = `NOT EXISTS (SELECT 1 FROM kb_ocr_recovery_decisions recovery
 WHERE recovery.original_invocation_id=i.invocation_id AND recovery.replacement_invocation_id IS NOT NULL)`

type DocumentRecoveryPage struct {
	InvocationID string `json:"invocation_id"`
	PageNumber   int    `json:"page_number"`
}

// DocumentRecoveryPlan 是只读确认快照，指纹绑定任务、原文件和全部待替代页。
type DocumentRecoveryPlan struct {
	DocumentID     string                 `json:"document_id"`
	FailedJobID    string                 `json:"failed_job_id"`
	Generation     int64                  `json:"generation"`
	SourceDigest   string                 `json:"source_digest"`
	CompletedPages int                    `json:"completed_pages"`
	PagesTotal     int64                  `json:"pages_total"`
	Pages          []DocumentRecoveryPage `json:"pages"`
	Fingerprint    string                 `json:"fingerprint"`
	route          VisionRouteSnapshot
}

type documentRecoveryRepository interface {
	DocumentRecoveryPlan(context.Context, string, string, string) (DocumentRecoveryPlan, error)
	RecoverDocument(context.Context, string, string, string, string, string) (CreateDocumentResult, error)
}

func (s *SemanticIndexService) DocumentRecoveryPlan(ctx context.Context, ownerID, corpusID, documentID string) (DocumentRecoveryPlan, error) {
	r, ok := s.ingestRepo.(documentRecoveryRepository)
	if !ok {
		return DocumentRecoveryPlan{}, ErrDocumentIngestUnavailable
	}
	return r.DocumentRecoveryPlan(ctx, ownerID, corpusID, documentID)
}

func (s *SemanticIndexService) RecoverDocument(ctx context.Context, ownerID, corpusID, documentID, key, fingerprint string) (CreateDocumentResult, error) {
	r, ok := s.ingestRepo.(documentRecoveryRepository)
	if !ok {
		return CreateDocumentResult{}, ErrDocumentIngestUnavailable
	}
	return r.RecoverDocument(ctx, ownerID, corpusID, documentID, key, fingerprint)
}

func (r *SQLiteSemanticIndexRepository) DocumentRecoveryPlan(ctx context.Context, ownerID, corpusID, documentID string) (DocumentRecoveryPlan, error) {
	if err := validateSemanticScope(ownerID, corpusID); err != nil {
		return DocumentRecoveryPlan{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return DocumentRecoveryPlan{}, err
	}
	defer tx.Rollback()
	state, err := loadSemanticPolicyState(ctx, tx, ownerID, corpusID)
	if err != nil {
		return DocumentRecoveryPlan{}, err
	}
	return loadDocumentRecoveryPlan(ctx, tx, ownerID, state.corpusUID, documentID)
}

func loadDocumentRecoveryPlan(ctx context.Context, q *sql.Tx, ownerID, corpusUID, documentID string) (DocumentRecoveryPlan, error) {
	p := DocumentRecoveryPlan{DocumentID: documentID}
	var bindingVersion int64
	err := q.QueryRowContext(ctx, `SELECT b.content_generation,b.version,s.blob_sha256
 FROM kb_semantic_document_bindings b JOIN kb_documents d ON d.id=b.document_id AND d.corpus_uid=b.corpus_uid
 JOIN kb_ingest_document_sources s ON s.document_id=b.document_id AND s.content_generation=b.content_generation
 WHERE b.owner_id=? AND b.corpus_uid=? AND b.document_id=? AND b.lifecycle_state='active'
 AND b.text_state='failed' AND d.deleted=0 AND d.status='failed'`, ownerID, corpusUID, documentID).Scan(&p.Generation, &bindingVersion, &p.SourceDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrDocumentRetryNotAllowed
	}
	if err != nil {
		return p, err
	}
	var active int
	if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM kb_knowledge_jobs WHERE owner_id=? AND corpus_uid=? AND document_id=?
 AND document_generation=? AND state IN ('queued','running','retry_wait')`, ownerID, corpusUID, documentID, p.Generation).Scan(&active); err != nil {
		return p, err
	}
	if active != 0 {
		return p, ErrIdempotencyConflict
	}
	err = q.QueryRowContext(ctx, `SELECT job_id,COALESCE(pages_total,0) FROM kb_knowledge_jobs WHERE owner_id=? AND corpus_uid=?
 AND document_id=? AND document_generation=? AND kind='ingest' AND state='failed' AND cancel_requested=0
 ORDER BY COALESCE(finished_at,updated_at) DESC,job_id DESC LIMIT 1`, ownerID, corpusUID, documentID, p.Generation).Scan(&p.FailedJobID, &p.PagesTotal)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrDocumentRetryNotAllowed
	}
	if err != nil {
		return p, err
	}
	var capabilities string
	err = q.QueryRowContext(ctx, `SELECT provider_instance_id,provider_name,provider_display_name,model,capabilities_json
 FROM kb_ingest_execution_snapshots WHERE job_id=?`, p.FailedJobID).Scan(&p.route.ProviderInstanceID, &p.route.ProviderName, &p.route.ProviderDisplayName, &p.route.Model, &capabilities)
	if err != nil {
		return p, err
	}
	if err = p.route.UnmarshalCapabilitiesJSON(capabilities); err != nil {
		return p, err
	}
	rows, err := q.QueryContext(ctx, `SELECT i.invocation_id,i.page_number,i.pages_total,i.provider,i.model
 FROM kb_ingest_page_invocations i JOIN kb_knowledge_jobs j ON j.job_id=i.job_id
 WHERE j.owner_id=? AND j.corpus_uid=? AND j.document_id=? AND j.document_generation=?
 AND i.source_digest=? AND (i.status='outcome_unknown' OR (i.status='running' AND j.state='failed'))
 AND `+unsupersededOCRInvocation+` ORDER BY i.page_number,i.invocation_id`, ownerID, corpusUID, documentID, p.Generation, p.SourceDigest)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var page DocumentRecoveryPage
		var total int64
		var provider, model string
		if err = rows.Scan(&page.InvocationID, &page.PageNumber, &total, &provider, &model); err != nil {
			return p, err
		}
		if provider != p.route.ProviderName || model != p.route.Model || (p.PagesTotal != 0 && total != p.PagesTotal) {
			return p, ErrJobFenced
		}
		p.PagesTotal = total
		if len(p.Pages) > 0 && p.Pages[len(p.Pages)-1].PageNumber == page.PageNumber {
			return p, ErrDocumentRetryNotAllowed
		}
		p.Pages = append(p.Pages, page)
	}
	if err = rows.Err(); err != nil {
		return p, err
	}
	rows.Close()
	if len(p.Pages) == 0 {
		return p, ErrDocumentRetryNotAllowed
	}
	if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM kb_ingest_page_checkpoints c
 JOIN kb_ingest_page_route_receipts rr ON rr.job_id=c.job_id AND rr.page_number=c.page_number
 WHERE c.job_id=? AND c.source_digest=? AND rr.status='succeeded' AND rr.source_digest=c.source_digest
 AND rr.content_digest=c.content_digest AND rr.provider=? AND rr.model=?`, p.FailedJobID, p.SourceDigest, p.route.ProviderName, p.route.Model).Scan(&p.CompletedPages); err != nil {
		return p, err
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	p.Fingerprint = hashStrings(ownerID, corpusUID, fmt.Sprint(bindingVersion), p.route.Fingerprint, string(encoded))
	return p, nil
}

func (r *SQLiteSemanticIndexRepository) RecoverDocument(ctx context.Context, ownerID, corpusID, documentID, key, fingerprint string) (CreateDocumentResult, error) {
	if err := validateSemanticScope(ownerID, corpusID); err != nil {
		return CreateDocumentResult{}, err
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 220 || !isRawSHA256(fingerprint) {
		return CreateDocumentResult{}, ErrInvalidDocumentRetry
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return CreateDocumentResult{}, err
	}
	defer tx.Rollback()
	state, err := loadSemanticPolicyState(ctx, tx, ownerID, corpusID)
	if err != nil {
		return CreateDocumentResult{}, err
	}
	storageKey := documentRecoveryPrefix + key
	var existingJob, existingDoc, existingFingerprint string
	err = tx.QueryRowContext(ctx, `SELECT j.job_id,j.document_id,d.plan_fingerprint FROM kb_knowledge_jobs j
 JOIN kb_ocr_recovery_decisions d ON d.replacement_job_id=j.job_id WHERE j.owner_id=? AND j.corpus_uid=? AND j.idempotency_key=? LIMIT 1`, ownerID, state.corpusUID, storageKey).Scan(&existingJob, &existingDoc, &existingFingerprint)
	if err == nil {
		if existingDoc != documentID || existingFingerprint != fingerprint {
			return CreateDocumentResult{}, ErrIdempotencyConflict
		}
		return documentReparseResult(ctx, tx, state.corpusUID, documentID, existingJob)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CreateDocumentResult{}, err
	}
	plan, err := loadDocumentRecoveryPlan(ctx, tx, ownerID, state.corpusUID, documentID)
	if err != nil {
		return CreateDocumentResult{}, err
	}
	if plan.Fingerprint != fingerprint {
		return CreateDocumentResult{}, ErrIdempotencyConflict
	}
	jobID, err := semanticID("job")
	if err != nil {
		return CreateDocumentResult{}, err
	}
	now := semanticNowMillis()
	// 先建立任务再写决定，仍处于同一事务；普通重试必须核对精确的决定记录。
	if err = queueFailedTextRetryTx(ctx, tx, ownerID, state.corpusUID, documentID, plan.Generation, storageKey, jobID, now, &plan.route, &plan); err != nil {
		return CreateDocumentResult{}, err
	}
	if err = r.reconcileDocumentIngestLifecycleTx(ctx, tx, KnowledgeJob{JobID: jobID, Kind: KnowledgeJobIngest, OwnerID: ownerID, CorpusUID: state.corpusUID, DocumentID: documentID, DocumentGeneration: plan.Generation}, time.UnixMilli(now)); err != nil {
		return CreateDocumentResult{}, err
	}
	vector, err := vectorStateForPolicyTx(ctx, tx, state)
	if err != nil {
		return CreateDocumentResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return CreateDocumentResult{}, err
	}
	return CreateDocumentResult{DocumentID: documentID, JobID: jobID, TextIndexState: TextIndexPending, VectorIndexState: vector}, nil
}

// consumeOCRRecoveryTx 与调用身份的首次落盘同事务，已消费授权不能换请求指纹重复发送。
func consumeOCRRecoveryTx(ctx context.Context, tx *sql.Tx, job KnowledgeJob, claim OCRPageInvocationClaim, invocationID string, now int64) error {
	var original string
	var consumed sql.NullString
	var owner, corpus, document, digest, provider, model string
	var generation int64
	err := tx.QueryRowContext(ctx, `SELECT d.original_invocation_id,d.replacement_invocation_id,d.owner_id,d.corpus_uid,d.document_id,d.content_generation,d.source_digest,i.provider,i.model
 FROM kb_ocr_recovery_decisions d JOIN kb_ingest_page_invocations i ON i.invocation_id=d.original_invocation_id
 WHERE d.replacement_job_id=? AND d.page_number=?`, job.JobID, claim.PageNumber).Scan(&original, &consumed, &owner, &corpus, &document, &generation, &digest, &provider, &model)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner != job.OwnerID || corpus != job.CorpusUID || document != job.DocumentID || generation != job.DocumentGeneration || digest != claim.SourceDigest || provider != claim.Provider || model != claim.Model {
		return ErrJobFenced
	}
	if err := validateIngestPageSource(ctx, tx, job, claim.SourceDigest); err != nil {
		return err
	}
	if consumed.Valid {
		return ErrOCRPageInvocationOutcomeUnknown
	}
	res, err := tx.ExecContext(ctx, `UPDATE kb_ocr_recovery_decisions SET replacement_invocation_id=?,consumed_at=? WHERE original_invocation_id=? AND replacement_invocation_id IS NULL`, invocationID, now, original)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrJobFenced
	}
	return nil
}
