package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

type reparseFixtureProcessor struct{ text string }

func (p reparseFixtureProcessor) Prepare(ctx context.Context, source PersistedIngestDocument) (PreparedIngestDocument, error) {
	prepared, err := (deterministicIngestProcessor{}).Prepare(ctx, source)
	prepared.Document.Content = p.text
	prepared.Chunks[0].Content = p.text
	prepared.Chunks[0].SourceOffsetEnd = int64(len(p.text))
	return prepared, err
}

func newReparseFixture(t *testing.T, vectors bool) (*semanticMutationHarness, CreateDocumentResult, *SemanticIndexWorker, *scriptedWorkerExecutor) {
	t.Helper()
	h := newSemanticMutationHarness(t)
	if err := migrate.Run(h.ctx, h.db, []migrate.Migration{migrate.K12KnowledgeInvocationLedgersV91, migrate.KnowledgeReparseV105, migrate.KnowledgeRecoveryV106}); err != nil {
		t.Fatal(err)
	}
	if !vectors {
		if _, err := h.service.ApplyPolicy(h.ctx, "owner-1", "default", 1, EmbeddingSelection{Kind: EmbeddingSelectionDisabled}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.service.ConfigureDocumentIngest(filepath.Join(t.TempDir(), "objects")); err != nil {
		t.Fatal(err)
	}
	const original = "original preserved source bytes"
	created, err := h.service.CreateDocument(h.ctx, "owner-1", "default", CreateDocumentInput{IdempotencyKey: "old", Filename: "lesson.txt", MediaType: "text/plain", SizeBytes: int64(len(original)), Body: strings.NewReader(original)})
	if err != nil {
		t.Fatal(err)
	}
	executor := &scriptedWorkerExecutor{dimension: 3}
	worker := NewSemanticIndexWorker(h.repo, &workerExecutorRegistry{executors: map[string]ProfileEmbeddingExecutor{"profile-a": executor}}, SemanticIndexWorkerConfig{OwnerID: "owner-1", CorpusID: "default", WorkerID: "reparse-worker", LeaseDuration: time.Minute})
	worker.SetDocumentIngestProcessor(reparseFixtureProcessor{"oldquartz lesson"})
	if worked, err := worker.RunOnce(h.ctx); err != nil || !worked {
		t.Fatalf("initial ingest worked=%v err=%v", worked, err)
	}
	if vectors {
		if worked, err := worker.RunOnce(h.ctx); err != nil || !worked {
			t.Fatalf("initial embedding worked=%v err=%v", worked, err)
		}
	}
	return h, created, worker, executor
}

func assertReparseSearch(t *testing.T, h *semanticMutationHarness, docID, visible, absent string) {
	t.Helper()
	doc, err := h.store.Get(h.ctx, docID)
	if err != nil || doc.Content != visible+" lesson" {
		t.Fatalf("document=%+v err=%v", doc, err)
	}
	hits, err := h.store.TextSearch(h.ctx, visible, 5, Filter{})
	if err != nil || len(hits) != 1 || hits[0].Chunk.DocID != docID {
		t.Fatalf("visible %q hits=%v err=%v", visible, hits, err)
	}
	hits, err = h.store.TextSearch(h.ctx, absent, 5, Filter{})
	if err != nil || len(hits) != 0 {
		t.Fatalf("unpublished %q hits=%v err=%v", absent, hits, err)
	}
}

// 两种索引模式均从公开命令和 worker 执行，检查跨事务可见性及原文件保留。
func TestDocumentReparseKeepsOldUntilAtomicPublish(t *testing.T) {
	for _, vectors := range []bool{false, true} {
		name := "text"
		if vectors {
			name = "text_and_vectors"
		}
		t.Run(name, func(t *testing.T) {
			h, old, worker, executor := newReparseFixture(t, vectors)
			accepted, err := h.service.ReparseDocument(h.ctx, "owner-1", "default", old.DocumentID, "upgrade", 1)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := h.service.ReparseDocument(h.ctx, "owner-1", "default", old.DocumentID, "upgrade", 1)
			if err != nil || replay.JobID != accepted.JobID {
				t.Fatalf("replay=%+v err=%v", replay, err)
			}
			wantVector := VectorIndexDisabled
			if vectors {
				wantVector = VectorIndexReady
			}
			if accepted.VectorIndexState != wantVector || replay.VectorIndexState != wantVector {
				t.Fatalf("current index state missing: accepted=%+v replay=%+v", accepted, replay)
			}
			assertReparseSearch(t, h, old.DocumentID, "oldquartz", "newcobalt")
			worker.SetDocumentIngestProcessor(reparseFixtureProcessor{"newcobalt lesson"})
			if worked, err := worker.RunOnce(h.ctx); err != nil || !worked {
				t.Fatalf("candidate parse worked=%v err=%v", worked, err)
			}
			if vectors {
				assertReparseSearch(t, h, old.DocumentID, "oldquartz", "newcobalt")
				executor.afterCall = func() { assertReparseSearch(t, h, old.DocumentID, "oldquartz", "newcobalt") }
				if worked, err := worker.RunOnce(h.ctx); err != nil || !worked {
					t.Fatalf("candidate embed worked=%v err=%v", worked, err)
				}
				if executor.calls != 2 {
					t.Fatalf("embedding calls=%d", executor.calls)
				}
			}
			assertReparseSearch(t, h, old.DocumentID, "newcobalt", "oldquartz")
			file, source, err := h.service.OpenDocumentSource(h.ctx, "owner-1", "default", old.DocumentID)
			if err != nil {
				t.Fatal(err)
			}
			bytes, err := io.ReadAll(file)
			file.Close()
			if err != nil || string(bytes) != "original preserved source bytes" || source.ContentGeneration != 2 {
				t.Fatalf("source=%+v bytes=%q err=%v", source, bytes, err)
			}
			var oldContent, oldChunks string
			var published sql.NullInt64
			if err = h.db.QueryRowContext(h.ctx, `SELECT old_document_json,old_chunks_json,published_at FROM kb_document_reparses WHERE job_id=?`, accepted.JobID).Scan(&oldContent, &oldChunks, &published); err != nil || !published.Valid || !strings.Contains(oldContent, "oldquartz") || !strings.Contains(oldChunks, "oldquartz") {
				t.Fatalf("old snapshot lost: %s %s %v %v", oldContent, oldChunks, published, err)
			}
			oldJob, err := h.repo.GetJob(h.ctx, "owner-1", old.JobID)
			if err != nil || oldJob.State != KnowledgeJobSucceeded {
				t.Fatalf("old receipt changed: %+v %v", oldJob, err)
			}
			if vectors {
				var gen, visible int
				if err = h.db.QueryRowContext(h.ctx, `SELECT b.content_generation,COUNT(v.chunk_id) FROM kb_semantic_document_bindings b JOIN kb_revision_documents rd ON rd.document_id=b.document_id AND rd.content_generation=b.content_generation AND rd.revision_id=? JOIN kb_revision_vectors v ON v.revision_id=rd.revision_id AND v.document_id=rd.document_id AND v.content_generation=rd.content_generation WHERE b.document_id=? AND rd.visible_at IS NOT NULL GROUP BY b.content_generation`, h.active, old.DocumentID).Scan(&gen, &visible); err != nil || gen != 2 || visible != 1 {
					t.Fatalf("visible gen=%d vectors=%d err=%v", gen, visible, err)
				}
			}
		})
	}
	t.Run("definite_failure_preserves_old", func(t *testing.T) {
		h, old, _, _ := newReparseFixture(t, false)
		if _, err := h.service.ReparseDocument(h.ctx, "owner-1", "default", old.DocumentID, "fails", 1); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		job, ok, err := h.repo.ClaimNextJobForCorpus(h.ctx, "owner-1", "default", "worker", now, time.Minute)
		if err != nil || !ok {
			t.Fatal(err)
		}
		if _, err = h.repo.FailJob(h.ctx, job.Lease(), now, "explicit parse failure"); err != nil {
			t.Fatal(err)
		}
		assertReparseSearch(t, h, old.DocumentID, "oldquartz", "newcobalt")
	})
}

func TestDocumentReparseRestartUnknownAndPublicationFences(t *testing.T) {
	for _, unresolved := range []bool{false, true} {
		name := "explicit_recovery_publishes_candidate_vectors"
		if unresolved {
			name = "explicit_recovery_unknown_keeps_old_version"
		}
		t.Run(name, func(t *testing.T) {
			h, old, worker, _ := newReparseFixture(t, true)
			h.service.ConfigureVisionRouteResolver(VisionRouteSnapshotResolverFunc(func(context.Context) (VisionRouteSnapshot, error) { return testOCRVisionRoute(), nil }))
			accepted, err := h.service.ReparseDocument(h.ctx, "owner-1", "default", old.DocumentID, "recover-candidate", 1)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			job, ok, err := h.repo.ClaimNextJobForCorpus(h.ctx, "owner-1", "default", "candidate", now, time.Minute)
			if err != nil || !ok {
				t.Fatalf("candidate claim=%v %v", ok, err)
			}
			source, err := h.repo.GetIngestDocumentForJob(h.ctx, "owner-1", job.CorpusUID, old.DocumentID, job.JobID)
			if err != nil {
				t.Fatal(err)
			}
			if err = h.repo.SetIngestPageTotal(h.ctx, job.Lease(), now, source.SHA256, 2); err != nil {
				t.Fatal(err)
			}
			claim := OCRPageInvocationClaim{PageNumber: 1, PagesTotal: 2, SourceDigest: source.SHA256, RequestDigest: strings.Repeat("a", 64), Provider: source.VisionRoute.ProviderName, Model: source.VisionRoute.Model}
			first, err := h.repo.ClaimOCRPageInvocation(h.ctx, job.Lease(), now, claim)
			if err != nil {
				t.Fatal(err)
			}
			receipt := *testOCRRouteReceipt()
			if err = h.repo.SaveOCRPageInvocation(h.ctx, job.Lease(), now, first, OCRPageInvocationResult{Content: "preserved page one", RouteReceipt: receipt}); err != nil {
				t.Fatal(err)
			}
			if err = h.repo.SaveIngestPageCheckpoint(h.ctx, job.Lease(), now, IngestPageCheckpoint{PageNumber: 1, PagesTotal: 2, SourceDigest: source.SHA256, ExtractionMode: "ocr_vlm", Content: "preserved page one", OCRRouteReceipt: &receipt}); err != nil {
				t.Fatal(err)
			}
			claim.PageNumber = 2
			claim.RequestDigest = strings.Repeat("b", 64)
			unknown, err := h.repo.ClaimOCRPageInvocation(h.ctx, job.Lease(), now, claim)
			if err != nil {
				t.Fatal(err)
			}
			if err = h.repo.MarkOCRPageInvocationOutcomeUnknown(h.ctx, job.Lease(), now, unknown, "upstream timeout"); err != nil {
				t.Fatal(err)
			}
			if _, err = h.repo.FailJob(h.ctx, job.Lease(), now, "upstream timeout"); err != nil {
				t.Fatal(err)
			}
			original, err := scanOCRPageInvocation(h.db.QueryRowContext(h.ctx, ocrPageInvocationSelect+` WHERE invocation_id=?`, unknown.InvocationID))
			if err != nil {
				t.Fatal(err)
			}
			plan, err := h.service.DocumentRecoveryPlan(h.ctx, "owner-1", "default", old.DocumentID)
			if err != nil || plan.Generation != 2 || plan.CompletedPages != 1 || len(plan.Pages) != 1 || plan.Pages[0].PageNumber != 2 {
				t.Fatalf("candidate plan=%+v %v", plan, err)
			}
			resumed, err := h.service.RecoverDocument(h.ctx, "owner-1", "default", old.DocumentID, "approved-once", plan.Fingerprint)
			if err != nil {
				t.Fatal(err)
			}
			if resumed.TextIndexState != TextIndexReady || resumed.VectorIndexState != VectorIndexReady {
				t.Fatalf("published state changed=%+v", resumed)
			}
			replay, err := h.service.RecoverDocument(h.ctx, "owner-1", "default", old.DocumentID, "approved-once", plan.Fingerprint)
			if err != nil || replay.JobID != resumed.JobID {
				t.Fatalf("replay=%+v %v", replay, err)
			}
			assertReparseSearch(t, h, old.DocumentID, "oldquartz", "newcobalt")
			job, ok, err = h.repo.ClaimNextJobForCorpus(h.ctx, "owner-1", "default", "recovery", now, time.Minute)
			if err != nil || !ok || job.JobID != resumed.JobID || job.ParentJobID != accepted.JobID {
				t.Fatalf("resumed claim=%+v %v %v", job, ok, err)
			}
			pages, err := h.repo.LoadIngestPageCheckpoints(h.ctx, job.Lease(), now, source.SHA256, 2)
			if err != nil || len(pages) != 1 || pages[0].Content != "preserved page one" {
				t.Fatalf("inherited=%+v %v", pages, err)
			}
			replacement, err := h.repo.ClaimOCRPageInvocation(h.ctx, job.Lease(), now, claim)
			if err != nil || !replacement.Fresh {
				t.Fatalf("replacement=%+v %v", replacement, err)
			}
			duplicate, err := h.repo.ClaimOCRPageInvocation(h.ctx, job.Lease(), now, claim)
			if err != nil || duplicate.Fresh || duplicate.InvocationID != replacement.InvocationID {
				t.Fatalf("duplicate=%+v %v", duplicate, err)
			}
			if unresolved {
				if err = h.repo.MarkOCRPageInvocationOutcomeUnknown(h.ctx, job.Lease(), now, replacement, "another timeout"); err != nil {
					t.Fatal(err)
				}
				if _, err = h.repo.FailJob(h.ctx, job.Lease(), now, "another timeout"); err != nil {
					t.Fatal(err)
				}
				if _, err = h.service.ReparseDocument(h.ctx, "owner-1", "default", old.DocumentID, "cannot-bypass", 1); !errors.Is(err, ErrOCRPageInvocationOutcomeUnknown) {
					t.Fatalf("unknown bypass=%v", err)
				}
				assertReparseSearch(t, h, old.DocumentID, "oldquartz", "newcobalt")
			} else {
				if err = h.repo.SaveOCRPageInvocation(h.ctx, job.Lease(), now, replacement, OCRPageInvocationResult{Content: "newcobalt lesson", RouteReceipt: receipt}); err != nil {
					t.Fatal(err)
				}
				prepared, err := (reparseFixtureProcessor{"newcobalt lesson"}).Prepare(h.ctx, source)
				if err != nil {
					t.Fatal(err)
				}
				if err = h.repo.CompleteIngestDocument(h.ctx, job.Lease(), now, prepared); err != nil {
					t.Fatal(err)
				}
				assertReparseSearch(t, h, old.DocumentID, "oldquartz", "newcobalt")
				if worked, err := worker.RunOnce(h.ctx); err != nil || !worked {
					t.Fatalf("candidate embedding=%v %v", worked, err)
				}
				assertReparseSearch(t, h, old.DocumentID, "newcobalt", "oldquartz")
			}
			preserved, err := scanOCRPageInvocation(h.db.QueryRowContext(h.ctx, ocrPageInvocationSelect+` WHERE invocation_id=?`, unknown.InvocationID))
			if err != nil || preserved != original {
				t.Fatalf("original receipt changed=%+v %v", preserved, err)
			}
			var count int
			if err = h.db.QueryRowContext(h.ctx, `SELECT COUNT(*) FROM kb_ingest_page_invocations WHERE job_id=?`, resumed.JobID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("new calls=%d %v", count, err)
			}
		})
	}
	t.Run("restart_reuses_receipt_and_unknown_cannot_create_new_generation", func(t *testing.T) {
		h, old, _, _ := newReparseFixture(t, false)
		accepted, err := h.service.ReparseDocument(h.ctx, "owner-1", "default", old.DocumentID, "restart", 1)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		job, ok, err := h.repo.ClaimNextJobForCorpus(h.ctx, "owner-1", "default", "first", now, time.Second)
		if err != nil || !ok {
			t.Fatal(err)
		}
		source, err := h.repo.GetIngestDocumentForJob(h.ctx, "owner-1", job.CorpusUID, old.DocumentID, accepted.JobID)
		if err != nil || source.ContentGeneration != 2 {
			t.Fatalf("source=%+v err=%v", source, err)
		}
		claim := OCRPageInvocationClaim{PageNumber: 1, PagesTotal: 2, SourceDigest: source.SHA256, RequestDigest: strings.Repeat("a", 64), Provider: "test-provider", Model: "vision"}
		first, err := h.repo.ClaimOCRPageInvocation(h.ctx, job.Lease(), now, claim)
		if err != nil || !first.Fresh {
			t.Fatalf("claim=%+v err=%v", first, err)
		}
		if err = h.repo.SaveOCRPageInvocation(h.ctx, job.Lease(), now, first, OCRPageInvocationResult{Content: "durable successful page", RouteReceipt: OCRRouteReceipt{Provider: "test-provider", Model: "vision", Operation: OCRRouteOperationPDFPage, Status: OCRRouteStatusSucceeded}}); err != nil {
			t.Fatal(err)
		}
		unknownClaim := claim
		unknownClaim.PageNumber = 2
		unknownClaim.RequestDigest = strings.Repeat("b", 64)
		unknown, err := h.repo.ClaimOCRPageInvocation(h.ctx, job.Lease(), now, unknownClaim)
		if err != nil || !unknown.Fresh {
			t.Fatal(err)
		}
		var seq int
		var name, path string
		if err = h.db.QueryRowContext(h.ctx, `PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
			t.Fatal(err)
		}
		if err = h.db.Close(); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		repo := NewSQLiteSemanticIndexRepository(db)
		future := now.Add(2 * time.Second)
		restarted, ok, err := repo.ClaimNextJobForCorpus(h.ctx, "owner-1", "default", "second", future, time.Minute)
		if err != nil || !ok || restarted.JobID != accepted.JobID {
			t.Fatalf("restart=%+v %v %v", restarted, ok, err)
		}
		reused, err := repo.ClaimOCRPageInvocation(h.ctx, restarted.Lease(), future, claim)
		if err != nil || reused.Fresh || reused.Content != "durable successful page" || reused.InvocationID != first.InvocationID {
			t.Fatalf("reused=%+v err=%v", reused, err)
		}
		parked, err := repo.ClaimOCRPageInvocation(h.ctx, restarted.Lease(), future, unknownClaim)
		if err != nil || parked.Fresh || parked.InvocationID != unknown.InvocationID {
			t.Fatalf("unknown resent=%+v %v", parked, err)
		}
		if _, err = repo.ReparseDocument(h.ctx, "owner-1", "default", old.DocumentID, "bypass", 1, nil); !errors.Is(err, ErrOCRPageInvocationOutcomeUnknown) {
			t.Fatalf("new generation bypass: %v", err)
		}
		var count int
		if err = db.QueryRowContext(h.ctx, `SELECT COUNT(*) FROM kb_ingest_page_invocations WHERE job_id=?`, accepted.JobID).Scan(&count); err != nil || count != 2 {
			t.Fatalf("invocations=%d %v", count, err)
		}
		h.db = db
		h.repo = repo
		h.store = NewSQLiteStore(db, WithSQLiteSemanticMutations("owner-1", "default"))
		assertReparseSearch(t, h, old.DocumentID, "oldquartz", "newcobalt")
	})
	t.Run("unknown_embedding_preserves_old_and_blocks_new_key", func(t *testing.T) {
		h, old, worker, _ := newReparseFixture(t, true)
		if _, err := h.service.ReparseDocument(h.ctx, "owner-1", "default", old.DocumentID, "embedding-unknown", 1); err != nil {
			t.Fatal(err)
		}
		worker.SetDocumentIngestProcessor(reparseFixtureProcessor{"newcobalt lesson"})
		if _, err := worker.RunOnce(h.ctx); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		job, ok, err := h.repo.ClaimNextJobForCorpus(h.ctx, "owner-1", "default", "embed", now, time.Minute)
		if err != nil || !ok {
			t.Fatal(err)
		}
		inputs, err := h.repo.ListRevisionChunkInputs(h.ctx, job.Lease(), now, nil, 10)
		if err != nil || len(inputs) != 1 {
			t.Fatalf("inputs=%v %v", inputs, err)
		}
		manifest, err := h.repo.CreateEmbeddingBatchManifest(h.ctx, job.Lease(), now, EmbeddingBatchManifest{ChunkIDsDigest: "candidate", PayloadDigest: "payload", ClientRequestKey: "candidate-call", Chunks: []EmbeddingBatchChunk{{Ordinal: 0, ChunkID: inputs[0].ChunkID, ContentHash: inputs[0].ContentHash}}})
		if err != nil {
			t.Fatal(err)
		}
		if err = h.repo.BeginEmbeddingBatch(h.ctx, job.Lease(), now, manifest.BatchID); err != nil {
			t.Fatal(err)
		}
		if err = h.repo.MarkEmbeddingBatchOutcomeUnknown(h.ctx, job.Lease(), now, manifest.BatchID, "connection closed after sending"); err != nil {
			t.Fatal(err)
		}
		if _, err = h.repo.FailJob(h.ctx, job.Lease(), now, "embedding outcome unknown"); err != nil {
			t.Fatal(err)
		}
		if _, err = h.service.ReparseDocument(h.ctx, "owner-1", "default", old.DocumentID, "new-key", 1); !errors.Is(err, ErrEmbeddingBatchOutcomeUnknown) {
			t.Fatalf("unknown bypass=%v", err)
		}
		assertReparseSearch(t, h, old.DocumentID, "oldquartz", "newcobalt")
	})

	for _, mutation := range []string{"edit", "delete", "source"} {
		t.Run(mutation+"_fences_completion", func(t *testing.T) {
			h, old, worker, _ := newReparseFixture(t, true)
			if _, err := h.service.ReparseDocument(h.ctx, "owner-1", "default", old.DocumentID, "fence", 1); err != nil {
				t.Fatal(err)
			}
			worker.SetDocumentIngestProcessor(reparseFixtureProcessor{"newcobalt lesson"})
			if _, err := worker.RunOnce(h.ctx); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			job, ok, err := h.repo.ClaimNextJobForCorpus(h.ctx, "owner-1", "default", "embedding", now, time.Minute)
			if err != nil || !ok {
				t.Fatal(err)
			}
			switch mutation {
			case "edit":
				prepared, prepErr := (reparseFixtureProcessor{"oldquartz lesson"}).Prepare(h.ctx, PersistedIngestDocument{DocumentID: old.DocumentID, Filename: "lesson.txt"})
				if prepErr != nil {
					t.Fatal(prepErr)
				}
				err = h.store.Replace(h.ctx, prepared.Document, prepared.Chunks)
			case "delete":
				err = h.store.Delete(h.ctx, old.DocumentID)
			case "source":
				_, err = h.db.ExecContext(h.ctx, `UPDATE kb_ingest_document_sources SET blob_sha256=? WHERE document_id=? AND content_generation=2`, strings.Repeat("c", 64), old.DocumentID)
				if err != nil { // 外键存在时只改候选摘要模拟损坏的旧任务，原文件不变。
					_, err = h.db.ExecContext(h.ctx, `UPDATE kb_document_reparses SET source_digest=? WHERE job_id=?`, strings.Repeat("c", 64), job.ParentJobID)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = h.repo.ListRevisionChunkInputs(h.ctx, job.Lease(), now, nil, 10); !errors.Is(err, ErrJobFenced) {
				t.Fatalf("stale inputs error=%v", err)
			}
			if err = h.repo.CompleteActiveRevisionJob(h.ctx, job.Lease(), now, 0); !errors.Is(err, ErrJobFenced) {
				t.Fatalf("stale publish error=%v", err)
			}
			if mutation != "delete" {
				assertReparseSearch(t, h, old.DocumentID, "oldquartz", "newcobalt")
			} else {
				if worked, err := worker.RunOnce(h.ctx); err != nil || !worked {
					t.Fatalf("deleted candidate GC worked=%v err=%v", worked, err)
				}
				var rows int
				if err := h.db.QueryRowContext(h.ctx, `SELECT COUNT(*) FROM kb_document_reparses WHERE document_id=?`, old.DocumentID).Scan(&rows); err != nil || rows != 0 {
					t.Fatalf("GC candidates=%d err=%v", rows, err)
				}
			}
		})
	}
}
