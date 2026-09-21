package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type recoveryFixture struct {
	db             *sql.DB
	ctx            context.Context
	repo           *SQLiteSemanticIndexRepository
	service        *SemanticIndexService
	document       CreateDocumentResult
	job            KnowledgeJob
	source         PersistedIngestDocument
	first, unknown OCRPageInvocation
	now            time.Time
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	db, service, ctx := newAsyncIngestHarness(t)
	f := &recoveryFixture{db: db, ctx: ctx, service: service, repo: NewSQLiteSemanticIndexRepository(db), now: time.Now().UTC()}
	body := "%PDF-1.7\nthree preserved pages"
	var err error
	f.document, err = service.CreateDocument(ctx, "desktop-user", "default", CreateDocumentInput{IdempotencyKey: "initial", Filename: "lesson.pdf", MediaType: "application/pdf", SizeBytes: int64(len(body)), Body: strings.NewReader(body)})
	if err != nil {
		t.Fatal(err)
	}
	var ok bool
	f.job, ok, err = f.repo.ClaimNextJobForCorpus(ctx, "desktop-user", "default", "initial", f.now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%v %v", ok, err)
	}
	f.source, err = f.repo.GetIngestDocumentForJob(ctx, "desktop-user", f.job.CorpusUID, f.document.DocumentID, f.job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.repo.SetIngestPageTotal(ctx, f.job.Lease(), f.now, f.source.SHA256, 3); err != nil {
		t.Fatal(err)
	}
	f.first, err = f.repo.ClaimOCRPageInvocation(ctx, f.job.Lease(), f.now, f.claim(1))
	if err != nil {
		t.Fatal(err)
	}
	f.save(t, f.job, f.first, "preserved page one")
	f.unknown, err = f.repo.ClaimOCRPageInvocation(ctx, f.job.Lease(), f.now, f.claim(2))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.repo.MarkOCRPageInvocationOutcomeUnknown(ctx, f.job.Lease(), f.now, f.unknown, "upstream timeout"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.FailJob(ctx, f.job.Lease(), f.now, "upstream timeout"); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *recoveryFixture) claim(page int) OCRPageInvocationClaim {
	return OCRPageInvocationClaim{PageNumber: page, PagesTotal: 3, SourceDigest: f.source.SHA256, RequestDigest: fmt.Sprintf("%064x", page), Provider: f.source.VisionRoute.ProviderName, Model: f.source.VisionRoute.Model}
}
func (f *recoveryFixture) save(t *testing.T, job KnowledgeJob, inv OCRPageInvocation, content string) {
	t.Helper()
	receipt := OCRRouteReceipt{Provider: inv.Provider, Model: inv.Model, Operation: OCRRouteOperationPDFPage, Status: OCRRouteStatusSucceeded}
	if err := f.repo.SaveOCRPageInvocation(f.ctx, job.Lease(), f.now, inv, OCRPageInvocationResult{Content: content, RouteReceipt: receipt}); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.SaveIngestPageCheckpoint(f.ctx, job.Lease(), f.now, IngestPageCheckpoint{PageNumber: inv.PageNumber, PagesTotal: 3, SourceDigest: f.source.SHA256, ExtractionMode: "ocr_vlm", Content: content, OCRRouteReceipt: &receipt}); err != nil {
		t.Fatal(err)
	}
}
func (f *recoveryFixture) plan(t *testing.T) DocumentRecoveryPlan {
	t.Helper()
	p, err := f.service.DocumentRecoveryPlan(f.ctx, "desktop-user", "default", f.document.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func (f *recoveryFixture) recover(t *testing.T, p DocumentRecoveryPlan) CreateDocumentResult {
	t.Helper()
	r, err := f.service.RecoverDocument(f.ctx, "desktop-user", "default", f.document.DocumentID, "explicit-once", p.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f *recoveryFixture) claimJob(t *testing.T) KnowledgeJob {
	t.Helper()
	j, ok, err := f.repo.ClaimNextJobForCorpus(f.ctx, "desktop-user", "default", "recovery", f.now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("recovery claim=%v %v", ok, err)
	}
	return j
}

// 真实 SQLite 记录成功页、未决页与新任务，断言跨重开及重复命令的调用身份。
func TestDocumentRecoveryPreservesReceiptsAndResumesOnce(t *testing.T) {
	f := newRecoveryFixture(t)
	original, err := scanOCRPageInvocation(f.db.QueryRowContext(f.ctx, ocrPageInvocationSelect+` WHERE invocation_id=?`, f.unknown.InvocationID))
	if err != nil {
		t.Fatal(err)
	}
	plan := f.plan(t)
	if plan.CompletedPages != 1 || plan.PagesTotal != 3 || len(plan.Pages) != 1 || plan.Pages[0].PageNumber != 2 || plan.Pages[0].InvocationID != f.unknown.InvocationID {
		t.Fatalf("plan=%+v", plan)
	}
	if _, err = f.service.RetryDocument(f.ctx, "desktop-user", "default", f.document.DocumentID, "ordinary"); !errors.Is(err, ErrOCRPageInvocationOutcomeUnknown) {
		t.Fatalf("ordinary retry bypass=%v", err)
	}
	accepted := f.recover(t, plan)
	replay := f.recover(t, plan)
	if replay.JobID != accepted.JobID {
		t.Fatalf("duplicate command=%+v", replay)
	}
	var seq int
	var name, path string
	if err = f.db.QueryRowContext(f.ctx, `PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	f.repo = NewSQLiteSemanticIndexRepository(f.db)
	replay, err = f.repo.RecoverDocument(f.ctx, "desktop-user", "default", f.document.DocumentID, "explicit-once", plan.Fingerprint)
	if err != nil || replay.JobID != accepted.JobID {
		t.Fatalf("reopened replay=%+v %v", replay, err)
	}
	job := f.claimJob(t)
	pages, err := f.repo.LoadIngestPageCheckpoints(f.ctx, job.Lease(), f.now, f.source.SHA256, 3)
	if err != nil || len(pages) != 1 || pages[0].Content != "preserved page one" {
		t.Fatalf("inherited=%+v %v", pages, err)
	}
	for _, page := range []int{2, 3} {
		invocation, err := f.repo.ClaimOCRPageInvocation(f.ctx, job.Lease(), f.now, f.claim(page))
		if err != nil || !invocation.Fresh {
			t.Fatalf("new page=%+v %v", invocation, err)
		}
		duplicate, err := f.repo.ClaimOCRPageInvocation(f.ctx, job.Lease(), f.now, f.claim(page))
		if err != nil || duplicate.Fresh || duplicate.InvocationID != invocation.InvocationID {
			t.Fatalf("duplicate send=%+v %v", duplicate, err)
		}
		f.save(t, job, invocation, fmt.Sprintf("completed page %d", page))
	}
	var sent, oldCount int
	if err = f.db.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM kb_ingest_page_invocations WHERE job_id=?`, job.JobID).Scan(&sent); err != nil || sent != 2 {
		t.Fatalf("new calls=%d %v", sent, err)
	}
	if err = f.db.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM kb_ingest_page_checkpoints WHERE job_id=?`, f.job.JobID).Scan(&oldCount); err != nil || oldCount != 1 {
		t.Fatalf("old checkpoints=%d %v", oldCount, err)
	}
	preserved, err := scanOCRPageInvocation(f.db.QueryRowContext(f.ctx, ocrPageInvocationSelect+` WHERE invocation_id=?`, f.unknown.InvocationID))
	if err != nil || preserved != original {
		t.Fatalf("old unknown changed: %+v %+v %v", preserved, original, err)
	}
	projection, err := f.repo.ListDocumentVectorProjections(f.ctx, "desktop-user", "default")
	if err != nil || projection[f.document.DocumentID].TextOutcomeUnknown {
		t.Fatalf("resolved projection=%+v %v", projection, err)
	}
}

func TestDocumentRecoveryStopsUnknownAndRejectsStalePlans(t *testing.T) {
	t.Run("replacement_unknown_requires_new_decision", func(t *testing.T) {
		f := newRecoveryFixture(t)
		plan := f.plan(t)
		accepted := f.recover(t, plan)
		job := f.claimJob(t)
		inv, err := f.repo.ClaimOCRPageInvocation(f.ctx, job.Lease(), f.now, f.claim(2))
		if err != nil {
			t.Fatal(err)
		}
		drift := f.claim(2)
		drift.RequestDigest = strings.Repeat("f", 64)
		if _, err = f.repo.ClaimOCRPageInvocation(f.ctx, job.Lease(), f.now, drift); !errors.Is(err, ErrOCRPageInvocationOutcomeUnknown) {
			t.Fatalf("changed request bypass=%v", err)
		}
		if err = f.repo.MarkOCRPageInvocationOutcomeUnknown(f.ctx, job.Lease(), f.now, inv, "timeout again"); err != nil {
			t.Fatal(err)
		}
		if _, err = f.repo.FailJob(f.ctx, job.Lease(), f.now, "timeout again"); err != nil {
			t.Fatal(err)
		}
		replay := f.recover(t, plan)
		if replay.JobID != accepted.JobID {
			t.Fatal("old approval started another job")
		}
		if _, err = f.service.RetryDocument(f.ctx, "desktop-user", "default", f.document.DocumentID, "ordinary"); !errors.Is(err, ErrOCRPageInvocationOutcomeUnknown) {
			t.Fatalf("new unknown bypass=%v", err)
		}
		if _, err = f.service.RecoverDocument(f.ctx, "desktop-user", "default", f.document.DocumentID, "new-key", plan.Fingerprint); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("stale approval=%v", err)
		}
		next := f.plan(t)
		if len(next.Pages) != 1 || next.Pages[0].InvocationID != inv.InvocationID {
			t.Fatalf("next decision=%+v", next)
		}
		var count int
		if err = f.db.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM kb_ingest_page_invocations WHERE job_id=?`, job.JobID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("replacement calls=%d %v", count, err)
		}
	})
	for _, change := range []string{"delete", "source", "binding"} {
		t.Run(change, func(t *testing.T) {
			f := newRecoveryFixture(t)
			p := f.plan(t)
			var err error
			switch change {
			case "delete":
				_, err = f.db.ExecContext(f.ctx, `UPDATE kb_documents SET deleted=1 WHERE id=?`, f.document.DocumentID)
			case "source":
				_, err = f.db.ExecContext(f.ctx, `INSERT INTO kb_ingest_blobs(owner_id,corpus_uid,sha256,storage_path,size_bytes,media_type,created_at)
 SELECT owner_id,corpus_uid,?,storage_path||'.changed',size_bytes,media_type,created_at FROM kb_ingest_blobs WHERE sha256=?`, strings.Repeat("c", 64), f.source.SHA256)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.db.ExecContext(f.ctx, `UPDATE kb_ingest_document_sources SET blob_sha256=? WHERE document_id=?`, strings.Repeat("c", 64), f.document.DocumentID)
			case "binding":
				_, err = f.db.ExecContext(f.ctx, `UPDATE kb_semantic_document_bindings SET version=version+1 WHERE document_id=?`, f.document.DocumentID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.RecoverDocument(f.ctx, "desktop-user", "default", f.document.DocumentID, "stale", p.Fingerprint); err == nil {
				t.Fatal("stale plan accepted")
			}
			var count int
			if err = f.db.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM kb_ocr_recovery_decisions`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("stale decision=%d %v", count, err)
			}
		})
	}
}
