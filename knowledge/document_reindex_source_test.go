package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexagon/rag/splitter"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

const sourceReindexCanonical = "## PDF 第 1 页\n\n<!-- source_page_span=1-1 -->\n\nalpha lesson 2+3=5\nshared note\n\n## PDF 第 2 页\n\n<!-- source_page_span=2-2 -->\n\nbeta lesson 4+5=9\nshared note"
const sourceReindexOriginal = "%PDF-1.7\noriginal immutable source\n%%EOF"

type sourceReindexProcessor struct{}

func (sourceReindexProcessor) Prepare(ctx context.Context, source PersistedIngestDocument) (PreparedIngestDocument, error) {
	return sourceReindexProcessor{}.PrepareResumable(ctx, source, nil)
}

func (sourceReindexProcessor) PrepareResumable(ctx context.Context, source PersistedIngestDocument, progress IngestPageProgress) (PreparedIngestDocument, error) {
	if err := progress.SetPageTotal(ctx, source.SHA256, 2); err != nil {
		return PreparedIngestDocument{}, err
	}
	texts := []string{"alpha lesson 2+3=5\nshared note", "beta lesson 4+5=9\nshared note"}
	now := time.Now().UTC()
	doc := &Document{ID: source.DocumentID, Title: source.Filename, Content: sourceReindexCanonical, Source: "upload:" + source.Filename, SourceType: "upload", Status: "indexed", CreatedAt: now, UpdatedAt: now, ChunkCount: 2}
	chunks := []*Chunk{}
	for index, text := range texts {
		start := strings.Index(sourceReindexCanonical, text)
		p := IngestPageCheckpoint{PageNumber: index + 1, PagesTotal: 2, SourceDigest: source.SHA256, ExtractionMode: "text", Content: text, SourceOffsetStart: int64(start), SourceOffsetEnd: int64(start + len(text))}
		if err := progress.CommitPage(ctx, p); err != nil {
			return PreparedIngestDocument{}, err
		}
		chunks = append(chunks, &Chunk{ID: source.DocumentID + string(rune('a'+index)), DocID: source.DocumentID, Content: text, Index: index, CreatedAt: now, PageStart: index + 1, PageEnd: index + 1, SourceDigest: source.SHA256, SourceOffsetStart: p.SourceOffsetStart, SourceOffsetEnd: p.SourceOffsetEnd})
	}
	return PreparedIngestDocument{Document: doc, Chunks: chunks, PageCount: 2, Warnings: []string{}, SourceManifest: &SourceManifest{SchemaVersion: 1, ParserVersion: "local-text-fixture", SourceDigest: source.SHA256, Blocks: []SourceContentBlock{{ID: "p1", Kind: "paragraph", Text: texts[0], Page: 1}}}}, nil
}

func sourceReindexFixture(t *testing.T) (*sql.DB, *SemanticIndexService, *SQLiteStore, *Manager, CreateDocumentResult, context.Context) {
	t.Helper()
	db, service, ctx := newAsyncIngestHarness(t)
	if _, err := db.ExecContext(ctx, migrate.KnowledgeSourceManifestV112.SQL); err != nil {
		t.Fatal(err)
	}
	accepted, err := service.CreateDocument(ctx, "desktop-user", "default", CreateDocumentInput{IdempotencyKey: "source-reindex", Filename: "lesson.pdf", MediaType: "application/pdf", SizeBytes: int64(len(sourceReindexOriginal)), Body: strings.NewReader(sourceReindexOriginal)})
	if err != nil {
		t.Fatal(err)
	}
	repo := NewSQLiteSemanticIndexRepository(db)
	worker := NewSemanticIndexWorker(repo, nil, SemanticIndexWorkerConfig{OwnerID: "desktop-user", CorpusID: "default", WorkerID: "source-worker", LeaseDuration: time.Minute})
	worker.SetDocumentIngestProcessor(sourceReindexProcessor{})
	if worked, err := worker.RunOnce(ctx); err != nil || !worked {
		t.Fatalf("ingest worked=%v err=%v", worked, err)
	}
	store := NewSQLiteStore(db, WithSQLiteSemanticMutations("desktop-user", "default"))
	return db, service, store, NewManager(store, store, nil, WithSplitter(splitter.NewMarkdownSplitter())), accepted, ctx
}

func sourceReindexFrozenRows(t *testing.T, db *sql.DB) string {
	t.Helper()
	result := [][]any{}
	for _, query := range []string{`SELECT * FROM kb_ingest_page_checkpoints ORDER BY job_id,page_number`, `SELECT * FROM kb_knowledge_jobs WHERE kind='ingest' ORDER BY job_id`, `SELECT * FROM kb_ingest_document_sources WHERE content_generation=1 ORDER BY document_id`, `SELECT * FROM kb_ingest_source_manifests WHERE content_generation=1 ORDER BY document_id`} {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			result = append(result, values)
		}
		rows.Close()
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSourceReindexPreservesCurrentOriginalAndRealParseProvenance(t *testing.T) {
	db, service, _, manager, accepted, ctx := sourceReindexFixture(t)
	frozen := sourceReindexFrozenRows(t, db)
	for generation := int64(2); generation <= 3; generation++ {
		if _, err := manager.ReindexDocument(ctx, accepted.DocumentID); err != nil {
			t.Fatal(err)
		}
		file, source, err := service.OpenDocumentSource(ctx, "desktop-user", "default", accepted.DocumentID)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(file)
		file.Close()
		if err != nil || string(data) != sourceReindexOriginal || source.ContentGeneration != generation {
			t.Fatalf("original/generation changed: source=%+v err=%v", source, err)
		}
		id, parsedGeneration, err := ResolveReindexedPDFIngest(ctx, db, "desktop-user", source.CorpusUID, source.DocumentID, generation, source.SHA256, "")
		if err != nil || id != accepted.JobID || parsedGeneration != 1 {
			t.Fatalf("invented parse task: id=%s generation=%d err=%v", id, parsedGeneration, err)
		}
		var spans, manifests int
		if err := db.QueryRow(`SELECT COUNT(*) FROM kb_chunks WHERE doc_id=? AND source_digest=? AND page_start>0 AND source_offset_end>source_offset_start`, source.DocumentID, source.SHA256).Scan(&spans); err != nil || spans == 0 {
			t.Fatalf("source spans missing: count=%d err=%v", spans, err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM kb_ingest_source_manifests WHERE document_id=?`, source.DocumentID).Scan(&manifests); err != nil || manifests != int(generation) {
			t.Fatalf("actual parse manifest not inherited: %d %v", manifests, err)
		}
	}
	if got := sourceReindexFrozenRows(t, db); got != frozen {
		t.Fatal("original parse/source facts changed")
	}
}

func TestSourceReindexReplacementAndAmbiguousChunkDoNotBorrowSource(t *testing.T) {
	db, service, store, _, accepted, ctx := sourceReindexFixture(t)
	doc, err := store.Get(ctx, accepted.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	var corpus, digest string
	if err := db.QueryRow(`SELECT corpus_uid,blob_sha256 FROM kb_ingest_document_sources WHERE document_id=?`, doc.ID).Scan(&corpus, &digest); err != nil {
		t.Fatal(err)
	}
	pages, _, err := loadExactPDFParsePages(ctx, db, "desktop-user", corpus, doc.ID, accepted.JobID, digest, doc.Content)
	if err != nil {
		t.Fatal(err)
	}
	chunks := []*Chunk{{ID: "ambiguous", Content: "shared note"}, {ID: "located", Content: "【定位】lesson.pdf › ## PDF 第 2 页\n\nshared note"}, {ID: "invented", Content: "model invented explanation"}}
	if got := restoreChunkSourceSpans(doc, chunks, digest, pages); got.Matched != 1 || got.Ambiguous != 1 || got.Unmatched != 1 || chunks[0].SourceDigest != "" || chunks[1].PageStart != 2 || chunks[2].SourceDigest != "" {
		t.Fatalf("ambiguous/model content got source proof: %+v", chunks)
	}
	doc.Content = "actual replacement content"
	doc.ChunkCount = 1
	if err := store.Replace(ctx, doc, []*Chunk{{ID: "replacement", DocID: doc.ID, Content: doc.Content, CreatedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if file, _, err := service.OpenDocumentSource(ctx, "desktop-user", "default", doc.ID); err == nil {
		file.Close()
		t.Fatal("replacement borrowed old PDF")
	}
}

func sourceReindexBrokenFixture(t *testing.T) (*sql.DB, *SemanticIndexService, reindexedSourceRecoveryRequest, context.Context) {
	t.Helper()
	db, service, _, manager, accepted, ctx := sourceReindexFixture(t)
	if _, err := manager.ReindexDocument(ctx, accepted.DocumentID); err != nil {
		t.Fatal(err)
	}
	request := reindexedSourceRecoveryRequest{OwnerID: "desktop-user", DocumentID: accepted.DocumentID, ContentSHA256: sha256String(sourceReindexCanonical), IngestJobID: accepted.JobID, Generation: 2}
	if err := db.QueryRow(`SELECT b.corpus_uid,b.version,s.blob_sha256 FROM kb_semantic_document_bindings b JOIN kb_ingest_document_sources s ON s.document_id=b.document_id AND s.content_generation=1 WHERE b.document_id=?`, accepted.DocumentID).Scan(&request.CorpusUID, &request.BindingVersion, &request.SourceDigest); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`DELETE FROM kb_ingest_document_sources WHERE document_id=? AND content_generation=2`, `DELETE FROM kb_ingest_source_manifests WHERE document_id=? AND content_generation=2`, `UPDATE kb_chunks SET page_start=NULL,page_end=NULL,source_digest='',source_offset_start=0,source_offset_end=0 WHERE doc_id=?`} {
		if _, err := db.Exec(query, accepted.DocumentID); err != nil {
			t.Fatal(err)
		}
	}
	return db, service, request, ctx
}

func TestSourceReindexRecoveryCASDryRunReplayAndHistoricalFacts(t *testing.T) {
	db, service, request, ctx := sourceReindexBrokenFixture(t)
	frozen := sourceReindexFrozenRows(t, db)
	for _, dryRun := range []bool{true, false, false} {
		results, err := recoverReindexedDocumentSources(ctx, db, []reindexedSourceRecoveryRequest{request}, dryRun)
		if err != nil || len(results) != 1 || results[0].ParseGeneration != 1 || results[0].MappedChunks == 0 {
			t.Fatalf("recover dry=%v results=%+v err=%v", dryRun, results, err)
		}
		file, _, err := service.OpenDocumentSource(ctx, request.OwnerID, "default", request.DocumentID)
		if dryRun {
			if err == nil {
				file.Close()
				t.Fatal("dry-run persisted source")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			file.Close()
		}
		if sourceReindexFrozenRows(t, db) != frozen {
			t.Fatal("historical sources/parse facts changed")
		}
	}
	for _, change := range []func(*reindexedSourceRecoveryRequest){func(r *reindexedSourceRecoveryRequest) { r.BindingVersion++ }, func(r *reindexedSourceRecoveryRequest) { r.OwnerID = "other-owner" }, func(r *reindexedSourceRecoveryRequest) { r.ContentSHA256 = strings.Repeat("0", 64) }, func(r *reindexedSourceRecoveryRequest) { r.SourceDigest = strings.Repeat("0", 64) }, func(r *reindexedSourceRecoveryRequest) { r.IngestJobID = "different-document-job" }} {
		bad := request
		change(&bad)
		if _, err := recoverReindexedDocumentSources(ctx, db, []reindexedSourceRecoveryRequest{bad}, false); !errors.Is(err, ErrDocumentParseSourceConflict) {
			t.Fatalf("conflicting recovery accepted: %v", err)
		}
	}
}

func TestSourceReindexRecoveryRejectsIncompleteOrChangedOriginalProof(t *testing.T) {
	for _, change := range []string{
		`UPDATE kb_knowledge_jobs SET state='failed' WHERE job_id=?`,
		`UPDATE kb_ingest_page_checkpoints SET content='different text' WHERE job_id=? AND page_number=1`,
		`DELETE FROM kb_ingest_page_checkpoints WHERE job_id=? AND page_number=2`,
		`INSERT INTO kb_knowledge_jobs(job_id,kind,owner_id,corpus_uid,document_id,document_generation,idempotency_key,state,stage,lease_epoch,created_at,updated_at) SELECT job_id||'-new-parse',kind,owner_id,corpus_uid,document_id,2,idempotency_key||'-new-parse','queued','extracting',0,created_at,updated_at FROM kb_knowledge_jobs WHERE job_id=?`,
	} {
		t.Run(strings.Fields(change)[0]+strings.Fields(change)[1], func(t *testing.T) {
			db, _, request, ctx := sourceReindexBrokenFixture(t)
			if _, err := db.Exec(change, request.IngestJobID); err != nil {
				t.Fatal(err)
			}
			frozen := sourceReindexFrozenRows(t, db)
			if _, err := recoverReindexedDocumentSources(ctx, db, []reindexedSourceRecoveryRequest{request}, false); !errors.Is(err, ErrDocumentParseSourceConflict) {
				t.Fatalf("unproved original accepted: %v", err)
			}
			if sourceReindexFrozenRows(t, db) != frozen {
				t.Fatal("failed proof recovery changed historical evidence")
			}
		})
	}
}

func TestSourceReindexRecoveryRollsBackSourceWhenSpanWriteFails(t *testing.T) {
	db, _, request, ctx := sourceReindexBrokenFixture(t)
	frozen := sourceReindexFrozenRows(t, db)
	if _, err := db.Exec(`CREATE TRIGGER fail_source_span BEFORE UPDATE OF source_digest ON kb_chunks BEGIN SELECT RAISE(ABORT,'controlled span failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := recoverReindexedDocumentSources(ctx, db, []reindexedSourceRecoveryRequest{request}, false); err == nil {
		t.Fatal("controlled failure unexpectedly committed")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM kb_ingest_document_sources WHERE document_id=? AND content_generation=2`, request.DocumentID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial source persisted: %d %v", count, err)
	}
	if sourceReindexFrozenRows(t, db) != frozen {
		t.Fatal("rollback changed original facts")
	}
}
