package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

var ErrDocumentParseSourceConflict = errors.New("knowledge: document parse source evidence conflicts")

// CanonicalPDFPagePrefix 与 PDF 正文及页偏移共用同一页头，避免证明与生产字节漂移。
func CanonicalPDFPagePrefix(page, from, to int) string {
	return fmt.Sprintf("## PDF 第 %d 页\n\n<!-- source_page_span=%d-%d -->\n\n", page, from, to)
}

type documentSourceQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadExactPDFParsePages(ctx context.Context, q documentSourceQueryer, owner, corpus, document, jobID, digest, content string) ([]IngestPageCheckpoint, int64, error) {
	var generation, total, done, epoch int64
	var state string
	err := q.QueryRowContext(ctx, `SELECT j.document_generation,j.state,COALESCE(j.pages_total,0),COALESCE(j.pages_done,0),j.lease_epoch
	 FROM kb_knowledge_jobs j JOIN kb_ingest_document_sources s ON s.owner_id=j.owner_id AND s.corpus_uid=j.corpus_uid AND s.document_id=j.document_id AND s.content_generation=j.document_generation
	 WHERE j.job_id=? AND j.owner_id=? AND j.corpus_uid=? AND j.document_id=? AND j.kind='ingest' AND s.blob_sha256=? AND s.extension='.pdf'`, jobID, owner, corpus, document, digest).Scan(&generation, &state, &total, &done, &epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrDocumentParseSourceConflict
	}
	if err != nil {
		return nil, 0, err
	}
	if state != "succeeded" || total <= 0 || done != total {
		return nil, 0, ErrDocumentParseSourceConflict
	}
	rows, err := q.QueryContext(ctx, `SELECT page_number,pages_total,source_digest,extraction_mode,content,content_digest,source_offset_start,source_offset_end,lease_epoch FROM kb_ingest_page_checkpoints WHERE job_id=? ORDER BY page_number`, jobID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	pages := []IngestPageCheckpoint{}
	var canonical strings.Builder
	for rows.Next() {
		var p IngestPageCheckpoint
		var pageEpoch int64
		if err := rows.Scan(&p.PageNumber, &p.PagesTotal, &p.SourceDigest, &p.ExtractionMode, &p.Content, &p.ContentDigest, &p.SourceOffsetStart, &p.SourceOffsetEnd, &pageEpoch); err != nil {
			return nil, 0, err
		}
		if p.PageNumber != len(pages)+1 || p.PagesTotal != total || p.SourceDigest != digest || sha256String(p.Content) != p.ContentDigest || pageEpoch <= 0 || pageEpoch > epoch || strings.TrimSpace(p.Content) == "" {
			return nil, 0, ErrDocumentParseSourceConflict
		}
		if canonical.Len() > 0 {
			canonical.WriteString("\n\n")
		}
		canonical.WriteString(CanonicalPDFPagePrefix(p.PageNumber, p.PageNumber, p.PageNumber))
		start := int64(canonical.Len())
		canonical.WriteString(strings.TrimSpace(p.Content))
		if p.SourceOffsetStart != start || p.SourceOffsetEnd != int64(canonical.Len()) {
			return nil, 0, ErrDocumentParseSourceConflict
		}
		pages = append(pages, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if int64(len(pages)) != total || strings.TrimSpace(canonical.String()) != content {
		return nil, 0, ErrDocumentParseSourceConflict
	}
	return pages, generation, nil
}

func sha256String(value string) string {
	d := sha256.Sum256([]byte(value))
	return hex.EncodeToString(d[:])
}

// ResolveReindexedPDFIngest 只为当前代次的同源正文证明原解析任务，不改变真实任务代次。
// pinnedJob 为空时逐个核验真实成功任务；有固定任务时只核验该来源，不换任务。
func ResolveReindexedPDFIngest(ctx context.Context, q documentSourceQueryer, owner, corpus, document string, generation int64, digest, pinnedJob string) (string, int64, error) {
	var content string
	err := q.QueryRowContext(ctx, `SELECT d.content FROM kb_documents d JOIN kb_semantic_document_bindings b ON b.document_id=d.id AND b.corpus_uid=d.corpus_uid JOIN kb_ingest_document_sources s ON s.document_id=b.document_id AND s.owner_id=b.owner_id AND s.corpus_uid=b.corpus_uid AND s.content_generation=b.content_generation WHERE b.owner_id=? AND b.corpus_uid=? AND b.document_id=? AND b.content_generation=? AND b.lifecycle_state='active' AND b.text_state='ready' AND d.deleted=0 AND s.blob_sha256=?`, owner, corpus, document, generation, digest).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrDocumentParseSourceConflict
	}
	if err != nil {
		return "", 0, err
	}
	ids := []string{pinnedJob}
	if pinnedJob == "" {
		rows, err := q.QueryContext(ctx, `SELECT j.job_id FROM kb_knowledge_jobs j JOIN kb_ingest_document_sources s ON s.owner_id=j.owner_id AND s.corpus_uid=j.corpus_uid AND s.document_id=j.document_id AND s.content_generation=j.document_generation WHERE j.owner_id=? AND j.corpus_uid=? AND j.document_id=? AND j.document_generation<=? AND j.kind='ingest' AND j.state='succeeded' AND s.blob_sha256=? ORDER BY j.document_generation DESC,j.job_id`, owner, corpus, document, generation, digest)
		if err != nil {
			return "", 0, err
		}
		ids = nil
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return "", 0, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return "", 0, err
		}
		rows.Close()
	}
	for _, id := range ids {
		_, parsedGeneration, err := loadExactPDFParsePages(ctx, q, owner, corpus, document, id, digest, content)
		if err == nil && parsedGeneration <= generation {
			return id, parsedGeneration, nil
		}
		if err != nil && !errors.Is(err, ErrDocumentParseSourceConflict) {
			return "", 0, err
		}
	}
	return "", 0, ErrDocumentParseSourceConflict
}

var contextualPDFPage = regexp.MustCompile(`PDF 第 ([0-9]+) 页`)

type reindexedChunkSourceStats struct{ Matched, Ambiguous, Unmatched int }

// restoreChunkSourceSpans 只映射可可靠对应的原文；情境前缀不作为原文，重复匹配不猜。
func restoreChunkSourceSpans(doc *Document, chunks []*Chunk, digest string, pages []IngestPageCheckpoint) reindexedChunkSourceStats {
	stats := reindexedChunkSourceStats{}
	for _, chunk := range chunks {
		body := strings.TrimSpace(chunk.Content)
		pageHint := 0
		if prefix, tail, found := strings.Cut(body, "\n\n"); found {
			loc := "【定位】" + strings.TrimSpace(doc.Title)
			if prefix == loc || strings.HasPrefix(prefix, loc+" › ") || strings.HasPrefix(prefix, loc+"  【情境】") {
				body = strings.TrimSpace(tail)
				if match := contextualPDFPage.FindStringSubmatch(prefix[len(loc):]); len(match) == 2 {
					_, _ = fmt.Sscanf(match[1], "%d", &pageHint)
				}
			}
		}
		if body == "" {
			stats.Unmatched++
			continue
		}
		foundAt, matches := -1, 0
		for cursor := 0; cursor <= len(doc.Content)-len(body); {
			rel := strings.Index(doc.Content[cursor:], body)
			if rel < 0 {
				break
			}
			start := cursor + rel
			end := start + len(body)
			eligible := pageHint == 0
			if pageHint > 0 && pageHint <= len(pages) {
				p := pages[pageHint-1]
				eligible = int64(end) > p.SourceOffsetStart && int64(start) < p.SourceOffsetEnd
			}
			if eligible {
				foundAt = start
				matches++
			}
			cursor = start + 1
		}
		if matches != 1 {
			if matches > 1 {
				stats.Ambiguous++
			} else {
				stats.Unmatched++
			}
			continue
		}
		start, end := int64(foundAt), int64(foundAt+len(body))
		from, to := 0, 0
		for _, page := range pages {
			if end > page.SourceOffsetStart && start < page.SourceOffsetEnd {
				if from == 0 {
					from = page.PageNumber
					if start < page.SourceOffsetStart {
						start = page.SourceOffsetStart
					}
				}
				to = page.PageNumber
			}
		}
		if from == 0 {
			stats.Unmatched++
			continue
		}
		if end > pages[to-1].SourceOffsetEnd {
			end = pages[to-1].SourceOffsetEnd
		}
		if end <= start {
			stats.Unmatched++
			continue
		}
		chunk.SourceDigest, chunk.PageStart, chunk.PageEnd = digest, from, to
		chunk.SourceOffsetStart, chunk.SourceOffsetEnd = start, end
		stats.Matched++
	}
	return stats
}

func inheritDocumentSourceTx(ctx context.Context, tx *sql.Tx, owner, corpus, document string, previous, current int64, now int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO kb_ingest_document_sources(document_id,owner_id,corpus_uid,content_generation,blob_sha256,original_name,extension,media_type,size_bytes,agent_id,learner_id,subject,grade,page_count,warnings_json,created_at,updated_at) SELECT document_id,owner_id,corpus_uid,?,blob_sha256,original_name,extension,media_type,size_bytes,agent_id,learner_id,subject,grade,page_count,warnings_json,?,? FROM kb_ingest_document_sources WHERE document_id=? AND content_generation=? AND owner_id=? AND corpus_uid=?`, current, now, now, document, previous, owner, corpus)
	if err != nil {
		return err
	}
	// 老版本数据库可以没有结构化解析表；已存在的来源结构才随同源代次继承。
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='kb_ingest_source_manifests')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO kb_ingest_source_manifests(owner_id,document_id,content_generation,source_digest,manifest_json,created_at) SELECT owner_id,document_id,?,source_digest,manifest_json,? FROM kb_ingest_source_manifests WHERE owner_id=? AND document_id=? AND content_generation=?`, current, now, owner, document, previous)
	return err
}

type reindexedSourceRecoveryRequest struct {
	OwnerID        string `json:"owner_id"`
	CorpusUID      string `json:"corpus_uid"`
	DocumentID     string `json:"document_id"`
	ContentSHA256  string `json:"content_sha256"`
	IngestJobID    string `json:"ingest_job_id"`
	SourceDigest   string `json:"source_digest"`
	Generation     int64  `json:"generation"`
	BindingVersion int64  `json:"binding_version"`
}

type reindexedSourceRecoveryResult struct {
	DocumentID             string `json:"document_id"`
	Generation             int64  `json:"generation"`
	IngestJobID            string `json:"ingest_job_id"`
	ParseGeneration        int64  `json:"parse_generation"`
	MappedChunks           int    `json:"mapped_chunks"`
	AmbiguousChunks        int    `json:"ambiguous_chunks"`
	UnmatchedChunks        int    `json:"unmatched_chunks"`
	TotalChunks            int    `json:"total_chunks"`
	AlreadyRestored        bool   `json:"already_restored"`
	ContentSHA256          string `json:"content_sha256"`
	ProjectionSHA256Before string `json:"content_vector_sha256_before"`
	ProjectionSHA256After  string `json:"content_vector_sha256_after"`
}

func reindexedContentVectorDigest(ctx context.Context, q documentSourceQueryer, document string) (string, error) {
	h := sha256.New()
	for _, query := range []string{
		`SELECT id,chunk_index,content,embedding FROM kb_chunks WHERE doc_id=? ORDER BY chunk_index,id`,
		`SELECT revision_id||':'||chunk_id,content_generation,chunk_content_hash,embedding FROM kb_revision_vectors WHERE document_id=? ORDER BY revision_id,content_generation,chunk_id`,
	} {
		rows, err := q.QueryContext(ctx, query, document)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var id, content string
			var index int64
			var vector []byte
			if err := rows.Scan(&id, &index, &content, &vector); err != nil {
				rows.Close()
				return "", err
			}
			v := sha256.Sum256(vector)
			fmt.Fprintf(h, "%d:%s:%d:%s:%t:%x\n", len(id), id, index, sha256String(content), vector == nil, v)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return "", err
		}
		rows.Close()
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// recoverReindexedDocumentSources 仅处理显式列出的同源重建，所有对象共用一个事务。
// dryRun 不写入；执行固定正文、归属、binding版本和原真实解析任务，失败全部回滚。
func recoverReindexedDocumentSources(ctx context.Context, db *sql.DB, requests []reindexedSourceRecoveryRequest, dryRun bool) ([]reindexedSourceRecoveryResult, error) {
	if len(requests) == 0 {
		return nil, ErrDocumentParseSourceConflict
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: dryRun})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if !dryRun {
		if _, err := tx.ExecContext(ctx, `UPDATE kb_semantic_document_bindings SET version=version WHERE 0`); err != nil {
			return nil, err
		}
	}
	results := make([]reindexedSourceRecoveryResult, 0, len(requests))
	seen := map[string]bool{}
	for _, request := range requests {
		if seen[request.DocumentID] || request.Generation <= 1 || request.IngestJobID == "" || len(request.SourceDigest) != 64 || len(request.ContentSHA256) != 64 {
			return nil, ErrDocumentParseSourceConflict
		}
		seen[request.DocumentID] = true
		var doc Document
		var generation, version int64
		err := tx.QueryRowContext(ctx, `SELECT d.id,d.title,d.content,d.source,b.content_generation,b.version FROM kb_documents d JOIN kb_semantic_document_bindings b ON b.document_id=d.id AND b.corpus_uid=d.corpus_uid WHERE b.owner_id=? AND b.corpus_uid=? AND b.document_id=? AND b.lifecycle_state='active' AND b.text_state='ready' AND d.deleted=0`, request.OwnerID, request.CorpusUID, request.DocumentID).Scan(&doc.ID, &doc.Title, &doc.Content, &doc.Source, &generation, &version)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDocumentParseSourceConflict
		}
		if err != nil {
			return nil, err
		}
		if generation != request.Generation || version != request.BindingVersion || sha256String(doc.Content) != request.ContentSHA256 {
			return nil, ErrDocumentParseSourceConflict
		}
		var currentParseExists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM kb_knowledge_jobs WHERE owner_id=? AND corpus_uid=? AND document_id=? AND document_generation=? AND kind='ingest')`, request.OwnerID, request.CorpusUID, request.DocumentID, generation).Scan(&currentParseExists); err != nil {
			return nil, err
		}
		if currentParseExists {
			return nil, ErrDocumentParseSourceConflict
		}
		pages, parsedGeneration, err := loadExactPDFParsePages(ctx, tx, request.OwnerID, request.CorpusUID, request.DocumentID, request.IngestJobID, request.SourceDigest, doc.Content)
		if err != nil {
			return nil, err
		}
		if parsedGeneration >= generation {
			return nil, ErrDocumentParseSourceConflict
		}
		var path, name string
		var size int64
		err = tx.QueryRowContext(ctx, `SELECT bl.storage_path,s.original_name,s.size_bytes FROM kb_ingest_document_sources s JOIN kb_ingest_blobs bl ON bl.owner_id=s.owner_id AND bl.corpus_uid=s.corpus_uid AND bl.sha256=s.blob_sha256 WHERE s.owner_id=? AND s.corpus_uid=? AND s.document_id=? AND s.content_generation=? AND s.blob_sha256=?`, request.OwnerID, request.CorpusUID, request.DocumentID, parsedGeneration, request.SourceDigest).Scan(&path, &name, &size)
		if err != nil {
			return nil, err
		}
		if doc.Source != "upload:"+name {
			return nil, ErrDocumentParseSourceConflict
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		actual, readErr := io.Copy(h, file)
		closeErr := file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if actual != size || hex.EncodeToString(h.Sum(nil)) != request.SourceDigest {
			return nil, ErrDocumentParseSourceConflict
		}
		var currentDigest string
		err = tx.QueryRowContext(ctx, `SELECT blob_sha256 FROM kb_ingest_document_sources WHERE owner_id=? AND corpus_uid=? AND document_id=? AND content_generation=?`, request.OwnerID, request.CorpusUID, request.DocumentID, generation).Scan(&currentDigest)
		already := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if already && currentDigest != request.SourceDigest {
			return nil, ErrDocumentParseSourceConflict
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,content,chunk_index,page_start,page_end,source_digest,source_offset_start,source_offset_end FROM kb_chunks WHERE doc_id=? ORDER BY chunk_index,id`, doc.ID)
		if err != nil {
			return nil, err
		}
		chunks := []*Chunk{}
		for rows.Next() {
			c := &Chunk{}
			var ps, pe, os, oe sql.NullInt64
			var sd sql.NullString
			if err := rows.Scan(&c.ID, &c.Content, &c.Index, &ps, &pe, &sd, &os, &oe); err != nil {
				rows.Close()
				return nil, err
			}
			c.DocID = doc.ID
			c.PageStart = int(ps.Int64)
			c.PageEnd = int(pe.Int64)
			c.SourceDigest = sd.String
			c.SourceOffsetStart = os.Int64
			c.SourceOffsetEnd = oe.Int64
			if c.SourceDigest != "" && c.SourceDigest != request.SourceDigest {
				rows.Close()
				return nil, ErrDocumentParseSourceConflict
			}
			chunks = append(chunks, c)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		previousSpans := make([]Chunk, len(chunks))
		for index, chunk := range chunks {
			previousSpans[index] = *chunk
		}
		beforeProjection, err := reindexedContentVectorDigest(ctx, tx, doc.ID)
		if err != nil {
			return nil, err
		}
		stats := restoreChunkSourceSpans(&doc, chunks, request.SourceDigest, pages)
		for index, previous := range previousSpans {
			if previous.SourceDigest != "" && (previous.PageStart != chunks[index].PageStart || previous.PageEnd != chunks[index].PageEnd || previous.SourceOffsetStart != chunks[index].SourceOffsetStart || previous.SourceOffsetEnd != chunks[index].SourceOffsetEnd) {
				return nil, ErrDocumentParseSourceConflict
			}
		}
		if stats.Matched == 0 {
			return nil, ErrDocumentParseSourceConflict
		}
		if !dryRun && !already {
			if err := inheritDocumentSourceTx(ctx, tx, request.OwnerID, request.CorpusUID, doc.ID, parsedGeneration, generation, semanticNowMillis()); err != nil {
				return nil, err
			}
		}
		if !dryRun {
			for _, c := range chunks {
				if c.SourceDigest == request.SourceDigest {
					result, err := tx.ExecContext(ctx, `UPDATE kb_chunks SET page_start=?,page_end=?,source_digest=?,source_offset_start=?,source_offset_end=? WHERE id=? AND doc_id=? AND (COALESCE(source_digest,'')='' OR source_digest=?)`, c.PageStart, c.PageEnd, c.SourceDigest, c.SourceOffsetStart, c.SourceOffsetEnd, c.ID, doc.ID, request.SourceDigest)
					if err != nil {
						return nil, err
					}
					if changed, _ := result.RowsAffected(); changed != 1 {
						return nil, ErrDocumentParseSourceConflict
					}
				}
			}
		}
		afterProjection, err := reindexedContentVectorDigest(ctx, tx, doc.ID)
		if err != nil {
			return nil, err
		}
		if beforeProjection != afterProjection {
			return nil, ErrDocumentParseSourceConflict
		}
		results = append(results, reindexedSourceRecoveryResult{DocumentID: doc.ID, Generation: generation, IngestJobID: request.IngestJobID, ParseGeneration: parsedGeneration, MappedChunks: stats.Matched, AmbiguousChunks: stats.Ambiguous, UnmatchedChunks: stats.Unmatched, TotalChunks: len(chunks), AlreadyRestored: already, ContentSHA256: sha256String(doc.Content), ProjectionSHA256Before: beforeProjection, ProjectionSHA256After: afterProjection})
	}
	if dryRun {
		return results, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return results, nil
}
