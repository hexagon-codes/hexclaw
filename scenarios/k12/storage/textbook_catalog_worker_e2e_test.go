package k12storage_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestREGTextbookCatalog_ProductionWorkerPublishesPersistedCheckpointCatalog(t *testing.T) {
	store, _, _ := seedTextbookCatalogMaterialization(t)
	if _, err := store.DB().Exec(`DELETE FROM k12_textbook_catalog_jobs
		WHERE manifest_id='catalog-manifest'`); err != nil {
		t.Fatal(err)
	}
	worker := usecase.NewTextbookCatalogWorker(
		store,
		usecase.TextbookCatalogCheckpointExtractor{},
		usecase.TextbookCatalogWorkerConfig{
			WorkerID: "integration-worker", Lease: 30 * time.Second,
			HeartbeatInterval: 10 * time.Second, ExtractTimeout: time.Second,
			MaxAttempts: 3, RetryBase: time.Second, RetryMax: time.Minute,
			RecoveryBatch: 8,
		},
	)
	processed, err := worker.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("production worker processed=%v err=%v", processed, err)
	}
	var manifestState, jobState string
	var logicalPage, pdfPage int
	if err := store.DB().QueryRow(`SELECT state FROM k12_textbook_manifests
		WHERE manifest_id='catalog-manifest'`).Scan(&manifestState); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT state FROM k12_textbook_catalog_jobs
		WHERE manifest_id='catalog-manifest'`).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT logical_page,pdf_page
		FROM k12_textbook_page_mappings WHERE manifest_id='catalog-manifest'`).Scan(
		&logicalPage, &pdfPage,
	); err != nil {
		t.Fatal(err)
	}
	if manifestState != "ready_for_confirmation" || jobState != "succeeded" ||
		logicalPage != 1 || pdfPage != 3 {
		t.Fatalf("closed loop states/map=%s/%s %d->%d",
			manifestState, jobState, logicalPage, pdfPage)
	}
}

func TestREGTextbookCatalog_IsolatedFooterConflictPublishesWithUnchangedCheckpoint(t *testing.T) {
	store, pageContent, _ := seedTextbookCatalogMaterialization(t, "第一单元\n114")
	db := store.DB()
	ctx := context.Background()
	sourceDigest := strings.Repeat("a", 64)
	toc := "目 录\n1 第一单元 114"
	var cover string
	if err := db.QueryRow(`SELECT content FROM kb_ingest_page_checkpoints
		WHERE job_id='catalog-ingest' AND page_number=1`).Scan(&cover); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE kb_ingest_document_sources SET page_count=5 WHERE document_id='catalog-doc'`, nil},
		{`UPDATE kb_knowledge_jobs SET pages_total=5,pages_done=5 WHERE job_id='catalog-ingest'`, nil},
		{`UPDATE kb_ingest_page_checkpoints SET pages_total=5 WHERE job_id='catalog-ingest'`, nil},
		{`UPDATE kb_ingest_page_checkpoints SET content=?,content_digest=?,source_offset_end=?
			WHERE job_id='catalog-ingest' AND page_number=2`,
			[]any{toc, fmt.Sprintf("%x", sha256.Sum256([]byte(toc))), len(cover) + len(toc)}},
		{`UPDATE kb_ingest_page_checkpoints SET source_offset_start=?,source_offset_end=?
			WHERE job_id='catalog-ingest' AND page_number=3`,
			[]any{len(cover) + len(toc), len(cover) + len(toc) + len(pageContent)}},
		{`UPDATE kb_chunks SET source_offset_start=?,source_offset_end=? WHERE id='catalog-segment-3'`,
			[]any{len(cover) + len(toc), len(cover) + len(toc) + len(pageContent)}},
		{`DELETE FROM k12_textbook_catalog_jobs WHERE manifest_id='catalog-manifest'`, nil},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	offset := len(cover) + len(toc) + len(pageContent)
	for index, content := range []string{"第一单元\n155", "第一单元\n116"} {
		page := index + 4
		if _, err := db.Exec(`INSERT INTO kb_ingest_page_checkpoints
			(job_id,page_number,pages_total,source_digest,extraction_mode,content,
			 content_digest,source_offset_start,source_offset_end,lease_epoch,created_at,updated_at)
			VALUES('catalog-ingest',?,5,?,'ocr_vlm',?,?,?,?,1,1,1)`, page,
			sourceDigest, content, fmt.Sprintf("%x", sha256.Sum256([]byte(content))), offset, offset+len(content)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO kb_chunks
			(id,doc_id,content,chunk_index,created_at,page_start,page_end,source_digest,
			 source_offset_start,source_offset_end)
			VALUES(?,'catalog-doc',?,?,CURRENT_TIMESTAMP,?,?,?,?,?)`,
			fmt.Sprintf("catalog-segment-%d", page), content, index+1, page, page,
			sourceDigest, offset, offset+len(content)); err != nil {
			t.Fatal(err)
		}
		offset += len(content)
	}
	var beforeContent, beforeDigest string
	if err := db.QueryRow(`SELECT content,content_digest FROM kb_ingest_page_checkpoints
		WHERE job_id='catalog-ingest' AND page_number=4`).Scan(&beforeContent, &beforeDigest); err != nil {
		t.Fatal(err)
	}
	worker := usecase.NewTextbookCatalogWorker(store, usecase.TextbookCatalogCheckpointExtractor{},
		usecase.TextbookCatalogWorkerConfig{WorkerID: "footer-integration", Lease: 30 * time.Second,
			HeartbeatInterval: 10 * time.Second, ExtractTimeout: time.Second, MaxAttempts: 3,
			RetryBase: time.Second, RetryMax: time.Minute, RecoveryBatch: 8})
	processed, err := worker.RunOnce(ctx)
	if err != nil || !processed {
		t.Fatalf("isolated footer worker processed=%v err=%v", processed, err)
	}
	var manifestState, jobState, afterContent, afterDigest string
	var pdfPage int
	if err := db.QueryRow(`SELECT state FROM k12_textbook_manifests WHERE manifest_id='catalog-manifest'`).Scan(&manifestState); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state FROM k12_textbook_catalog_jobs WHERE manifest_id='catalog-manifest'`).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if manifestState != "ready_for_confirmation" || jobState != "succeeded" {
		t.Fatalf("isolated footer persisted states=%s/%s", manifestState, jobState)
	}
	if err := db.QueryRow(`SELECT pdf_page FROM k12_textbook_page_mappings
		WHERE manifest_id='catalog-manifest' AND logical_page=115`).Scan(&pdfPage); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT content,content_digest FROM kb_ingest_page_checkpoints
		WHERE job_id='catalog-ingest' AND page_number=4`).Scan(&afterContent, &afterDigest); err != nil {
		t.Fatal(err)
	}
	if pdfPage != 4 || afterContent != beforeContent || afterDigest != beforeDigest ||
		!strings.HasSuffix(afterContent, "\n155") {
		t.Fatalf("isolated footer mapping or checkpoint changed: physical=%d content=%q digest=%q", pdfPage, afterContent, afterDigest)
	}
}
