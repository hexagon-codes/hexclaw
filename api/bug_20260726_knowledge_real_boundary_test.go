package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hexagon-codes/hexagon/rag/splitter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/knowledge"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
	_ "modernc.org/sqlite"
)

const (
	bug20260726MathFivePDFSHA  = "657e1547074668dbb50f2bf37f13c20f292127be64c26c5334190aa34d06de83"
	bug20260726MathFivePDFSize = int64(14_621_452)
	bug20260726DesktopToken    = "real-pdf-desktop-fixture"
)

type bug20260726EmbeddingResolver struct{}

func (bug20260726EmbeddingResolver) Resolve(
	context.Context,
	string,
	string,
	knowledge.EmbeddingSelection,
) (knowledge.EmbeddingProfileSnapshot, error) {
	return knowledge.EmbeddingProfileSnapshot{}, knowledge.ErrProfileUnavailable
}

func (bug20260726EmbeddingResolver) Catalog(
	context.Context,
	string,
	string,
) (knowledge.EmbeddingProfileCatalog, error) {
	return knowledge.EmbeddingProfileCatalog{}, nil
}

type bug20260726MutableVisionRoute struct {
	mu    sync.Mutex
	route knowledge.VisionRouteSnapshot
}

func (r *bug20260726MutableVisionRoute) FreezeDefaultVisionRoute(
	context.Context,
) (knowledge.VisionRouteSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.route, nil
}

func (r *bug20260726MutableVisionRoute) set(route knowledge.VisionRouteSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.route = route
}

type bug20260726KnowledgeHarness struct {
	ctx        context.Context
	db         *sql.DB
	repository *knowledge.SQLiteSemanticIndexRepository
	service    *knowledge.SemanticIndexService
	manager    *knowledge.Manager
	route      *bug20260726MutableVisionRoute
	objectRoot string
	http       *httptest.Server

	captionMu     sync.Mutex
	captionRoutes []knowledge.VisionRouteSnapshot
}

func newBug20260726KnowledgeHarness(
	t *testing.T,
	route knowledge.VisionRouteSnapshot,
) *bug20260726KnowledgeHarness {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "knowledge.db")+
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })

	baseStore := knowledge.NewSQLiteStore(db)
	if err := baseStore.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(ctx, db, []migrate.Migration{
		migrate.KnowledgeIndexV23,
		migrate.KnowledgeIngestV24,
		migrate.KnowledgeIngestGenerationsV26,
		migrate.KnowledgeDocumentScopeV27,
		migrate.KnowledgeIngestCheckpointV28,
		migrate.KnowledgeIngestExecutionV46,
		migrate.KnowledgeUploadOperationsV71,
		migrate.KnowledgeOCRRouteReceiptsV87,
		migrate.K12KnowledgeInvocationLedgersV91,
		migrate.KnowledgeUploadDismissalV99,
		migrate.KnowledgeReparseV105,
		migrate.KnowledgeRecoveryV106,
	}); err != nil {
		t.Fatal(err)
	}

	repository := knowledge.NewSQLiteSemanticIndexRepository(db)
	if _, err := repository.BindLegacyDefaultCorpus(ctx, "desktop-user", "default"); err != nil {
		t.Fatal(err)
	}
	service := knowledge.NewSemanticIndexService(repository, bug20260726EmbeddingResolver{})
	objectRoot := filepath.Join(t.TempDir(), "objects")
	if err := service.ConfigureDocumentIngest(objectRoot); err != nil {
		t.Fatal(err)
	}
	routeResolver := &bug20260726MutableVisionRoute{route: route}
	service.ConfigureVisionRouteResolver(routeResolver)

	harness := &bug20260726KnowledgeHarness{
		ctx: ctx, db: db, repository: repository, service: service,
		route: routeResolver, objectRoot: objectRoot,
	}
	scopedStore := knowledge.NewSQLiteStore(
		db,
		knowledge.WithSQLiteSemanticMutations("desktop-user", "default"),
	)
	harness.manager = knowledge.NewManager(
		scopedStore,
		scopedStore,
		nil,
		knowledge.WithSplitter(splitter.NewMarkdownSplitter(
			splitter.WithMarkdownChunkSize(400),
			splitter.WithMarkdownChunkOverlap(80),
		)),
		knowledge.WithCaptioner(knowledge.CaptionerWithReceiptFunc(func(
			ctx context.Context,
			image []byte,
			mime string,
		) (knowledge.CaptionResult, error) {
			snapshot, ok := knowledge.VisionRouteSnapshotFromContext(ctx)
			if !ok {
				return knowledge.CaptionResult{}, errors.New("BUG-20260726-024: VLM call lost frozen route snapshot")
			}
			if len(image) == 0 || mime != "image/png" {
				return knowledge.CaptionResult{}, fmt.Errorf("invalid local VLM input bytes=%d mime=%q", len(image), mime)
			}
			harness.captionMu.Lock()
			harness.captionRoutes = append(harness.captionRoutes, snapshot)
			call := len(harness.captionRoutes)
			harness.captionMu.Unlock()
			return knowledge.CaptionResult{
				Content: fmt.Sprintf("local fake VLM page %d arithmetic lesson", call),
				RouteReceipt: knowledge.OCRRouteReceipt{
					Provider: snapshot.ProviderName, Model: snapshot.Model,
					Operation: knowledge.OCRRouteOperationPDFPage,
					Status:    knowledge.OCRRouteStatusSucceeded, Fake: true,
				},
			}, nil
		})),
	)
	harness.http = newBug20260726KnowledgeHTTPServer(t, harness.manager, service)
	return harness
}

func newBug20260726KnowledgeHTTPServer(
	t *testing.T,
	manager *knowledge.Manager,
	service *knowledge.SemanticIndexService,
) *httptest.Server {
	t.Helper()
	server := NewServer(config.DefaultConfig(), nil, nil, nil)
	server.SetDesktopAPIToken(bug20260726DesktopToken)
	server.SetKnowledgeBase(manager)
	server.SetSemanticIndexService(service)
	httpServer := httptest.NewServer(server.routes())
	t.Cleanup(httpServer.Close)
	return httpServer
}

func (h *bug20260726KnowledgeHarness) captionRouteSnapshots() []knowledge.VisionRouteSnapshot {
	h.captionMu.Lock()
	defer h.captionMu.Unlock()
	return append([]knowledge.VisionRouteSnapshot(nil), h.captionRoutes...)
}

func bug20260726MathFiveFixture(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("real 14.6MB/131-page PDF boundary")
	}
	// 冻结的大型 PDF 仅由本次测试显式提供，不读取仓库外的默认素材路径。
	fixture := strings.TrimSpace(os.Getenv("HEXCLAW_REAL_PDF_FIXTURE"))
	if fixture == "" {
		t.Skip("HEXCLAW_REAL_PDF_FIXTURE is not set; frozen real PDF boundary is not verified")
	}
	file, err := os.Open(fixture)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("frozen 14.6MB/131-page PDF fixture is unavailable; real textbook boundary is not verified")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		t.Fatal(err)
	}
	gotSHA := hex.EncodeToString(digest.Sum(nil))
	if info.Size() != bug20260726MathFivePDFSize || gotSHA != bug20260726MathFivePDFSHA {
		t.Fatalf("fixture bytes=%d sha256=%s want=%d/%s",
			info.Size(), gotSHA, bug20260726MathFivePDFSize, bug20260726MathFivePDFSHA)
	}
	return fixture
}

func bug20260726PostPDF(
	t *testing.T,
	baseURL string,
	fixture string,
	idempotencyKey string,
) knowledge.CreateDocumentResult {
	t.Helper()
	file, err := os.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("corpus_id", "default"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("subject", "\u6570\u5b66"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("grade", "\u4e94\u5e74\u7ea7\u4e0b"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", filepath.Base(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(part, file); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/knowledge/documents", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+bug20260726DesktopToken)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result knowledge.CreateDocumentResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted || result.DocumentID == "" || result.JobID == "" {
		t.Fatalf("upload status=%d result=%+v", resp.StatusCode, result)
	}
	return result
}

func bug20260726Route(
	providerID string,
	providerName string,
	displayName string,
	model string,
	capabilities ...string,
) knowledge.VisionRouteSnapshot {
	return knowledge.VisionRouteSnapshot{
		ProviderInstanceID: providerID, ProviderName: providerName,
		ProviderDisplayName: displayName, Model: model, Capabilities: capabilities,
	}.Canonical()
}

func bug20260726AssertPersistedRoute(
	t *testing.T,
	db *sql.DB,
	jobID string,
	want knowledge.VisionRouteSnapshot,
) {
	t.Helper()
	var got knowledge.VisionRouteSnapshot
	var capabilitiesJSON string
	if err := db.QueryRow(`SELECT provider_instance_id,provider_name,provider_display_name,
		model,capabilities_json,selection_fingerprint
		FROM kb_ingest_execution_snapshots WHERE job_id=?`, jobID).Scan(
		&got.ProviderInstanceID,
		&got.ProviderName,
		&got.ProviderDisplayName,
		&got.Model,
		&capabilitiesJSON,
		&got.Fingerprint,
	); err != nil {
		t.Fatal(err)
	}
	if err := got.UnmarshalCapabilitiesJSON(capabilitiesJSON); err != nil {
		t.Fatal(err)
	}
	want = want.Canonical()
	if got.ProviderInstanceID != want.ProviderInstanceID ||
		got.ProviderName != want.ProviderName ||
		got.ProviderDisplayName != want.ProviderDisplayName ||
		got.Model != want.Model ||
		got.Fingerprint != want.Fingerprint {
		t.Fatalf("job %s route=%+v want=%+v", jobID, got, want)
	}
}

func bug20260726RunIngest(
	h *bug20260726KnowledgeHarness,
) (bool, error) {
	worker := knowledge.NewSemanticIndexWorker(
		h.repository,
		nil,
		knowledge.SemanticIndexWorkerConfig{
			OwnerID: "desktop-user", CorpusID: "default",
			WorkerID: "bug-20260726-ingest", BatchSize: 64,
			LeaseDuration: 10 * time.Minute, RetryDelay: time.Second,
		},
	)
	worker.SetDocumentIngestProcessor(NewKnowledgeDocumentIngestProcessor(h.manager))
	return worker.RunOnce(h.ctx)
}

func TestBUG20260726024RealPDFConsumesFrozenDefaultVisionRoute(t *testing.T) {
	requirePopplerForAsyncPDFTest(t)
	fixture := bug20260726MathFiveFixture(t)
	t.Setenv("HEXCLAW_DOC_VLM_MAX_PAGES", "250")
	t.Setenv("HEXCLAW_DOC_VLM_RENDER_BATCH_PAGES", "2")
	t.Setenv("HEXCLAW_DOC_VLM_RENDER_DPI", "72")

	frozen := bug20260726Route(
		"provider-hexclaw-gpt", "hexclaw-gpt", "HexClaw-GPT", "gpt-5.6-sol",
		"text", "vision",
	)
	h := newBug20260726KnowledgeHarness(t, frozen)
	accepted := bug20260726PostPDF(t, h.http.URL, fixture, "bug-20260726-024-frozen-route")
	bug20260726AssertPersistedRoute(t, h.db, accepted.JobID, frozen)

	h.route.set(bug20260726Route(
		"provider-ollama", "ollama", "Ollama (local)", "qwen3-vl",
		"text", "vision",
	))
	worked, err := bug20260726RunIngest(h)
	if !worked || !errors.Is(err, knowledge.ErrInvalidDocumentUpload) {
		t.Fatalf("fake OCR must fail closed before ready worked=%v err=%v", worked, err)
	}

	calls := h.captionRouteSnapshots()
	if len(calls) != 7 {
		t.Fatalf("real PDF fake VLM calls=%d want=7", len(calls))
	}
	for index, route := range calls {
		route = route.Canonical()
		if route.ProviderInstanceID != frozen.ProviderInstanceID ||
			route.ProviderName != frozen.ProviderName ||
			route.Model != frozen.Model ||
			route.Fingerprint != frozen.Fingerprint {
			t.Fatalf("VLM call %d route=%+v want frozen=%+v", index+1, route, frozen)
		}
	}
	bug20260726AssertPersistedRoute(t, h.db, accepted.JobID, frozen)

	job, err := h.service.GetJob(h.ctx, "desktop-user", accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != knowledge.KnowledgeJobFailed || job.PagesDone == nil ||
		job.PagesTotal == nil || *job.PagesDone != 131 || *job.PagesTotal != 131 {
		t.Fatalf("fake OCR terminal job=%+v", job)
	}
	var documents, sources, jobs, nonReadySegments, fakeReceipts, chunks int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM kb_documents WHERE id=?`, accepted.DocumentID).
		Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM kb_ingest_document_sources WHERE document_id=?`,
		accepted.DocumentID).Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM kb_knowledge_jobs WHERE document_id=?`,
		accepted.DocumentID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM kb_ingest_segments
		WHERE document_id=? AND state<>'ready'`, accepted.DocumentID).Scan(&nonReadySegments); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM kb_ingest_page_route_receipts
		WHERE job_id=? AND fake=1`, accepted.JobID).Scan(&fakeReceipts); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM kb_chunks WHERE doc_id=?`,
		accepted.DocumentID).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if documents != 1 || sources != 1 || jobs != 1 || nonReadySegments != 0 {
		t.Fatalf("real PDF duplicate/residue documents=%d sources=%d jobs=%d non_ready_segments=%d",
			documents, sources, jobs, nonReadySegments)
	}
	if fakeReceipts != 7 || chunks != 0 {
		t.Fatalf("fake OCR receipts/chunks=%d/%d, want 7/0 before ready", fakeReceipts, chunks)
	}
}

func TestBUG20260726024UnknownVisionPDFUsesFrozenRoute(t *testing.T) {
	requirePopplerForAsyncPDFTest(t)
	fixture := writeAsyncProcessorPDF(t, buildImageOnlyTestPDF(t, 2)).StoragePath
	t.Setenv("HEXCLAW_DOC_VLM_MAX_PAGES", "250")
	t.Setenv("HEXCLAW_DOC_VLM_RENDER_DPI", "72")

	textOnly := bug20260726Route(
		"provider-hexclaw-gpt", "hexclaw-gpt", "HexClaw-GPT",
		"gpt-5.6-sol", "text",
	)
	h := newBug20260726KnowledgeHarness(t, textOnly)
	h.manager, _ = newKnowledgeOCRHTTPManager(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != textOnly.Model {
			t.Errorf("OCR did not use frozen model: request=%+v err=%v", request, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"controlled PDF transcription"},"finish_reason":"stop"}]}`)
	})
	accepted := bug20260726PostPDF(t, h.http.URL, fixture, "bug-20260726-024-preflight")
	h.route.set(bug20260726Route("other-provider", "other-provider", "Other", "other-model", "vision"))
	worked, runErr := bug20260726RunIngest(h)
	if !worked || runErr != nil {
		t.Fatalf("unknown vision OCR worked=%v err=%v", worked, runErr)
	}
	job, err := h.service.GetJob(h.ctx, "desktop-user", accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != knowledge.KnowledgeJobSucceeded || job.PagesDone == nil || *job.PagesDone != 2 || job.Failure != nil {
		t.Fatalf("unknown vision did not finish: %+v", job)
	}
	var chunks, receipts int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM kb_chunks WHERE doc_id=?`, accepted.DocumentID).
		Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM kb_ingest_page_route_receipts
		WHERE job_id=? AND provider=? AND model=? AND status='succeeded' AND fake=0`,
		accepted.JobID, textOnly.ProviderName, textOnly.Model).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if chunks == 0 || receipts != 2 {
		t.Fatalf("successful OCR lost its artifacts: chunks=%d receipts=%d", chunks, receipts)
	}
	bug20260726AssertPersistedRoute(t, h.db, accepted.JobID, textOnly)
}

func TestKnowledgeUnknownVisionImageWorkerPersistsOutcomeAndPreventsReplay(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		name := "success"
		if interrupted {
			name = "interrupted"
		}
		t.Run(name, func(t *testing.T) {
			frozen := bug20260726Route("provider-hexclaw-gpt", "hexclaw-gpt", "HexClaw-GPT", "gpt-5.6-sol", "text")
			h := newBug20260726KnowledgeHarness(t, frozen)
			var calls func() int
			h.manager, calls = newKnowledgeOCRHTTPManager(t, func(w http.ResponseWriter, r *http.Request, _ int) {
				if interrupted {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"persisted image transcription"},"finish_reason":"stop"}]}`)
			})
			accepted := bug20260726PostPDF(t, h.http.URL, writeAsyncProcessorImage(t).StoragePath, "unknown-vision-image")
			worked, runErr := bug20260726RunIngest(h)
			if !worked || calls() != 1 {
				t.Fatalf("image worker calls=%d worked=%v err=%v", calls(), worked, runErr)
			}
			job, err := h.service.GetJob(h.ctx, "desktop-user", accepted.JobID)
			if err != nil {
				t.Fatal(err)
			}
			var invocationState string
			if err := h.db.QueryRow(`SELECT status FROM kb_ingest_page_invocations WHERE job_id=? AND page_number=1`, accepted.JobID).Scan(&invocationState); err != nil {
				t.Fatal(err)
			}
			if interrupted {
				if !errors.Is(runErr, knowledge.ErrOCRPageInvocationOutcomeUnknown) || job.State != knowledge.KnowledgeJobFailed ||
					invocationState != string(knowledge.OCRPageInvocationStatusOutcomeUnknown) || !strings.Contains(job.LastError, "EOF") {
					t.Fatalf("image unknown lost durable cause: job=%+v invocation=%s err=%v", job, invocationState, runErr)
				}
				if _, err := h.service.RetryDocument(h.ctx, "desktop-user", "default", accepted.DocumentID, "unknown-image-retry"); !errors.Is(err, knowledge.ErrOCRPageInvocationOutcomeUnknown) {
					t.Fatalf("unknown image allowed a replacement request: %v", err)
				}
			} else {
				var receipts, checkpoints int
				if err := h.db.QueryRow(`SELECT COUNT(*) FROM kb_ingest_page_route_receipts WHERE job_id=? AND fake=0`, accepted.JobID).Scan(&receipts); err != nil {
					t.Fatal(err)
				}
				if err := h.db.QueryRow(`SELECT COUNT(*) FROM kb_ingest_page_checkpoints WHERE job_id=?`, accepted.JobID).Scan(&checkpoints); err != nil {
					t.Fatal(err)
				}
				if runErr != nil || job.State != knowledge.KnowledgeJobSucceeded || invocationState != string(knowledge.OCRPageInvocationStatusSucceeded) || receipts != 1 || checkpoints != 1 {
					t.Fatalf("image success not durable: job=%+v invocation=%s receipts=%d checkpoints=%d err=%v", job, invocationState, receipts, checkpoints, runErr)
				}
			}
			if worked, err := bug20260726RunIngest(h); worked || err != nil || calls() != 1 {
				t.Fatalf("terminal image sent another request: worked=%v calls=%d err=%v", worked, calls(), err)
			}
			bug20260726AssertPersistedRoute(t, h.db, accepted.JobID, frozen)
		})
	}
}

func TestKnowledgeUnknownVisionPDFHTTPFailureDoesNotBlindWorkerRetry(t *testing.T) {
	requirePopplerForAsyncPDFTest(t)
	t.Setenv("HEXCLAW_DOC_VLM_RENDER_DPI", "72")
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			frozen := bug20260726Route("provider-hexclaw-gpt", "hexclaw-gpt", "HexClaw-GPT", "gpt-5.6-sol", "text")
			h := newBug20260726KnowledgeHarness(t, frozen)
			var calls func() int
			h.manager, calls = newKnowledgeOCRHTTPManager(t, func(w http.ResponseWriter, r *http.Request, _ int) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"message":"sentinel-secret-request-body","type":"fixture_failure"}}`)
			})
			accepted := bug20260726PostPDF(t, h.http.URL, writeAsyncProcessorPDF(t, buildImageOnlyTestPDF(t, 3)).StoragePath, "unknown-vision-pdf-rejection")
			worked, runErr := bug20260726RunIngest(h)
			if !worked || runErr == nil || calls() != 1 {
				t.Fatalf("first rejection continued: worked=%v calls=%d err=%v", worked, calls(), runErr)
			}
			job, err := h.service.GetJob(h.ctx, "desktop-user", accepted.JobID)
			if err != nil {
				t.Fatal(err)
			}
			var invocationState string
			if err := h.db.QueryRow(`SELECT status FROM kb_ingest_page_invocations WHERE job_id=? AND page_number=1`, accepted.JobID).Scan(&invocationState); err != nil {
				t.Fatal(err)
			}
			wantState := string(knowledge.OCRPageInvocationStatusFailed)
			if status == http.StatusServiceUnavailable {
				wantState = string(knowledge.OCRPageInvocationStatusOutcomeUnknown)
			}
			if job.State != knowledge.KnowledgeJobFailed || invocationState != wantState || strings.Contains(job.LastError, "sentinel-secret") ||
				!strings.Contains(job.LastError, fmt.Sprintf("HTTP %d", status)) {
				t.Fatalf("HTTP rejection lost safe cause or status: job=%+v invocation=%s err=%v", job, invocationState, runErr)
			}
			if status == http.StatusTooManyRequests && errors.Is(runErr, knowledge.ErrOCRPageInvocationOutcomeUnknown) {
				t.Fatalf("known HTTP rejection became unknown: %v", runErr)
			}
			if worked, err := bug20260726RunIngest(h); worked || err != nil || calls() != 1 {
				t.Fatalf("worker retried a failed physical OCR: worked=%v calls=%d err=%v", worked, calls(), err)
			}
		})
	}
}

func TestKnowledgeRetryCurrentOperationCreatesNewFrozenJobsAfterTwoFailures(t *testing.T) {
	requirePopplerForAsyncPDFTest(t)
	t.Setenv("HEXCLAW_DOC_VLM_RENDER_DPI", "72")
	routes := []knowledge.VisionRouteSnapshot{
		bug20260726Route("provider-hexclaw-gpt", "hexclaw-gpt", "HexClaw-GPT", "model-a", "text"),
		bug20260726Route("provider-hexclaw-gpt", "hexclaw-gpt", "HexClaw-GPT", "model-b", "text"),
		bug20260726Route("provider-hexclaw-gpt", "hexclaw-gpt", "HexClaw-GPT", "model-c", "text"),
	}
	h := newBug20260726KnowledgeHarness(t, routes[0])
	var calls func() int
	h.manager, calls = newKnowledgeOCRHTTPManager(t, func(w http.ResponseWriter, r *http.Request, call int) {
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || call > len(routes) || request.Model != routes[call-1].Model {
			t.Errorf("retry changed frozen selection: call=%d request=%+v err=%v", call, request, err)
		}
		status := http.StatusTooManyRequests
		if call == 3 {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"message":"controlled upstream failure","type":"fixture_failure"}}`)
	})
	accepted := bug20260726PostPDF(t, h.http.URL, writeAsyncProcessorPDF(t, buildImageOnlyTestPDF(t, 1)).StoragePath, "retry-current-operation")
	req, err := http.NewRequest(http.MethodPost, h.http.URL+"/api/v1/knowledge/operations/"+accepted.OperationID+"/ack", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bug20260726DesktopToken)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("upload acknowledgement status=%d", response.StatusCode)
	}
	if worked, err := bug20260726RunIngest(h); !worked || !errors.Is(err, knowledge.ErrOCRPageInvocationFailed) {
		t.Fatalf("original failure worked=%v err=%v", worked, err)
	}
	current := getKnowledgeCurrentOperation(t, h, accepted.DocumentID)
	if current.JobID != accepted.JobID || current.State != knowledge.UploadOperationFailed {
		t.Fatalf("first failure projection=%+v", current)
	}
	for index := 1; index < len(routes); index++ {
		h.route.set(routes[index])
		key := "knowledge-retry:v2:" + current.JobID
		retry, status := postKnowledgeCurrentRetry(t, h, accepted.DocumentID, key)
		if status != http.StatusAccepted || retry.JobID == current.JobID || retry.JobID == accepted.JobID {
			t.Fatalf("retry did not create an independent execution: status=%d retry=%+v current=%+v", status, retry, current)
		}
		replayed, status := postKnowledgeCurrentRetry(t, h, accepted.DocumentID, key)
		if status != http.StatusAccepted || replayed.JobID != retry.JobID || calls() != index {
			t.Fatalf("same retry command replay changed identity or sent OCR: status=%d replay=%+v calls=%d", status, replayed, calls())
		}
		bug20260726AssertPersistedRoute(t, h.db, retry.JobID, routes[index])
		queued := getKnowledgeCurrentOperation(t, h, accepted.DocumentID)
		if queued.JobID != retry.JobID || queued.State != knowledge.UploadOperationQueued {
			t.Fatalf("retry projection remained stale: %+v", queued)
		}
		if worked, err := bug20260726RunIngest(h); !worked || err == nil {
			t.Fatalf("retry failure worked=%v err=%v", worked, err)
		}
		current = getKnowledgeCurrentOperation(t, h, accepted.DocumentID)
		if current.JobID != retry.JobID || current.State != knowledge.UploadOperationFailed || calls() != index+1 {
			t.Fatalf("current failed retry projection=%+v calls=%d", current, calls())
		}
	}
	if _, status := postKnowledgeCurrentRetry(t, h, accepted.DocumentID, "knowledge-retry:v2:"+current.JobID); status != http.StatusConflict || calls() != 3 {
		t.Fatalf("unknown execution obtained a new request: status=%d calls=%d", status, calls())
	}
	var originalJobID string
	var jobs int
	if err := h.db.QueryRow(`SELECT job_id FROM kb_upload_operations WHERE operation_id=?`, accepted.OperationID).Scan(&originalJobID); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM kb_knowledge_jobs WHERE document_id=? AND kind='ingest'`, accepted.DocumentID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if originalJobID != accepted.JobID || jobs != 3 {
		t.Fatalf("retry rewrote upload provenance or created unknown replacement: original=%s jobs=%d", originalJobID, jobs)
	}
}

func TestKnowledgeCurrentOperationUsesInsertionOrderAndOriginalScope(t *testing.T) {
	h := newBug20260726KnowledgeHarness(t, bug20260726Route("provider-hexclaw-gpt", "hexclaw-gpt", "HexClaw-GPT", "model-a", "text"))
	accepted := bug20260726PostPDF(t, h.http.URL, writeAsyncProcessorImage(t).StoragePath, "operation-same-clock")
	if err := h.service.MarkUploadResponseDelivered(h.ctx, "desktop-user", "default", accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	var sameClock int64
	var corpusUID string
	if err := h.db.QueryRow(`SELECT created_at,corpus_uid FROM kb_knowledge_jobs WHERE job_id=?`, accepted.JobID).Scan(&sameClock, &corpusUID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO kb_semantic_corpora(corpus_uid,owner_id,corpus_alias,created_at,updated_at)
		VALUES('corpus-other-owner','other-owner','default',?,?)`, sameClock, sameClock); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO kb_semantic_document_generations(owner_id,corpus_uid,document_id,content_generation,created_at)
		VALUES('other-owner','corpus-other-owner',?,1,?),('desktop-user',?,?,2,?)`,
		accepted.DocumentID, sameClock, corpusUID, accepted.DocumentID, sameClock); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		jobID      string
		owner      string
		corpus     string
		generation int
		createdAt  int64
	}{
		{jobID: "job_z_older", owner: "desktop-user", corpus: corpusUID, generation: 1, createdAt: sameClock},
		{jobID: "job_a_latest", owner: "desktop-user", corpus: corpusUID, generation: 1, createdAt: sameClock},
		{jobID: "job_other_owner", owner: "other-owner", corpus: "corpus-other-owner", generation: 1, createdAt: sameClock + 1},
		{jobID: "job_other_generation", owner: "desktop-user", corpus: corpusUID, generation: 2, createdAt: sameClock + 1},
	} {
		if _, err := h.db.Exec(`INSERT INTO kb_knowledge_jobs
			(job_id,kind,owner_id,corpus_uid,document_id,document_generation,idempotency_key,state,stage,last_error,created_at,updated_at,finished_at)
			SELECT ?,'ingest',?,?,document_id,?,?,'failed','ocr',?,?,?,?
			FROM kb_knowledge_jobs WHERE job_id=?`, fixture.jobID, fixture.owner, fixture.corpus, fixture.generation,
			"fixture|"+fixture.jobID, fixture.jobID+" safe error", fixture.createdAt, fixture.createdAt, fixture.createdAt, accepted.JobID); err != nil {
			t.Fatal(err)
		}
	}
	for _, jobID := range []string{"job_z_older", "job_a_latest"} {
		content := jobID + " fixture transcription"
		digest := sha256.Sum256([]byte(content))
		contentDigest := hex.EncodeToString(digest[:])
		if _, err := h.db.Exec(`INSERT INTO kb_ingest_page_checkpoints
			(job_id,page_number,pages_total,source_digest,extraction_mode,content,content_digest,
			 source_offset_start,source_offset_end,lease_epoch,created_at,updated_at)
			SELECT ?,1,1,blob_sha256,'ocr_vlm',?,?,0,?,1,?,?
			FROM kb_ingest_document_sources WHERE document_id=? AND content_generation=1`,
			jobID, content, contentDigest, len(content), sameClock, sameClock, accepted.DocumentID); err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.Exec(`INSERT INTO kb_ingest_page_route_receipts
			(job_id,page_number,pages_total,provider,model,operation,status,source_digest,content_digest,fake,created_at)
			SELECT ?,1,1,'hexclaw-gpt',?,'knowledge_pdf_page_ocr','succeeded',blob_sha256,?,1,?
			FROM kb_ingest_document_sources WHERE document_id=? AND content_generation=1`,
			jobID, jobID, contentDigest, sameClock, accepted.DocumentID); err != nil {
			t.Fatal(err)
		}
	}
	current := getKnowledgeCurrentOperation(t, h, accepted.DocumentID)
	if current.JobID != "job_a_latest" || current.State != knowledge.UploadOperationFailed || current.Error != "job_a_latest safe error" || current.Stage != "ocr" {
		t.Fatalf("projection selected random ID or crossed scope: %+v", current)
	}
	request, err := http.NewRequest(http.MethodGet, h.http.URL+"/api/v1/knowledge/documents/"+accepted.DocumentID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+bug20260726DesktopToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var document struct {
		Receipts []knowledge.OCRPageRouteReceipt `json:"ocr_page_route_receipts"`
	}
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil || response.StatusCode != http.StatusOK || len(document.Receipts) != 1 || document.Receipts[0].Model != "job_a_latest" {
		t.Fatalf("document receipt selected a different current identity: status=%d document=%+v err=%v", response.StatusCode, document, err)
	}
	const legacyError = "OCR failure (HTTP 429) request_preview: sentinel-legacy-secret"
	if _, err := h.db.Exec(`UPDATE kb_knowledge_jobs SET last_error=? WHERE job_id='job_a_latest'`, legacyError); err != nil {
		t.Fatal(err)
	}
	current = getKnowledgeCurrentOperation(t, h, accepted.DocumentID)
	var persistedError string
	if err := h.db.QueryRow(`SELECT last_error FROM kb_knowledge_jobs WHERE job_id='job_a_latest'`).Scan(&persistedError); err != nil || persistedError != legacyError ||
		!strings.Contains(current.Error, "HTTP 429") || strings.Contains(current.Error, "sentinel-legacy-secret") {
		t.Fatalf("public error lost cause, exposed raw preview, or rewrote history: current=%+v persisted=%q err=%v", current, persistedError, err)
	}
	var originalJobID string
	if err := h.db.QueryRow(`SELECT job_id FROM kb_upload_operations WHERE operation_id=?`, accepted.OperationID).Scan(&originalJobID); err != nil || originalJobID != accepted.JobID {
		t.Fatalf("read-only projection changed original upload: job=%s err=%v", originalJobID, err)
	}
}

func getKnowledgeCurrentOperation(t *testing.T, h *bug20260726KnowledgeHarness, documentID string) KnowledgeOperationProjection {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.http.URL+"/api/v1/knowledge/operations", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bug20260726DesktopToken)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Operations []KnowledgeOperationProjection `json:"operations"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("operations status=%d err=%v", response.StatusCode, err)
	}
	for _, operation := range body.Operations {
		if operation.DocumentID == documentID {
			return operation
		}
	}
	t.Fatalf("document %s operation missing: %+v", documentID, body.Operations)
	return KnowledgeOperationProjection{}
}

func postKnowledgeCurrentRetry(t *testing.T, h *bug20260726KnowledgeHarness, documentID, key string) (knowledge.CreateDocumentResult, int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.http.URL+"/api/v1/knowledge/documents/"+documentID+"/retry", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bug20260726DesktopToken)
	req.Header.Set("Idempotency-Key", key)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result knowledge.CreateDocumentResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result, response.StatusCode
}

func bug20260726PostCancel(t *testing.T, baseURL string, jobID string) {
	t.Helper()
	req, err := http.NewRequest(
		http.MethodPost,
		baseURL+"/api/v1/knowledge/jobs/"+jobID+"/cancel",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bug20260726DesktopToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("cancel status=%d body=%s", resp.StatusCode, raw)
	}
}

func bug20260726DeleteDocument(
	t *testing.T,
	baseURL string,
	documentID string,
	idempotencyKey string,
) {
	t.Helper()
	req, err := http.NewRequest(
		http.MethodDelete,
		baseURL+"/api/v1/knowledge/documents/"+documentID,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Idempotency-Key", idempotencyKey)
	req.Header.Set("Authorization", "Bearer "+bug20260726DesktopToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("delete status=%d body=%s", resp.StatusCode, raw)
	}
}

func TestBUG20260726026RealPDFDeleteFromDocumentRootConvergesWithoutOrphans(t *testing.T) {
	fixture := bug20260726MathFiveFixture(t)
	route := bug20260726Route(
		"provider-hexclaw-gpt", "hexclaw-gpt", "HexClaw-GPT", "gpt-5.6-sol",
		"text", "vision",
	)
	h := newBug20260726KnowledgeHarness(t, route)
	accepted := bug20260726PostPDF(t, h.http.URL, fixture, "bug-20260726-026-delete")

	var sourcePath string
	if err := h.db.QueryRow(`SELECT blob.storage_path
		FROM kb_ingest_document_sources source JOIN kb_ingest_blobs blob
		  ON blob.owner_id=source.owner_id AND blob.corpus_uid=source.corpus_uid
		 AND blob.sha256=source.blob_sha256
		WHERE source.document_id=?`, accepted.DocumentID).Scan(&sourcePath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("managed source missing before cleanup: %v", err)
	}

	bug20260726PostCancel(t, h.http.URL, accepted.JobID)
	result, err := h.db.Exec(`DELETE FROM kb_semantic_document_bindings WHERE document_id=?`,
		accepted.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		t.Fatalf("missing-resource fixture removed bindings=%d want=1", affected)
	}

	const deleteKey = "bug-20260726-026-delete-replay"
	bug20260726DeleteDocument(t, h.http.URL, accepted.DocumentID, deleteKey)
	bug20260726DeleteDocument(t, h.http.URL, accepted.DocumentID, deleteKey)

	gcWorker := knowledge.NewSemanticIndexWorker(
		h.repository,
		nil,
		knowledge.SemanticIndexWorkerConfig{
			OwnerID: "desktop-user", CorpusID: "default",
			WorkerID: "bug-20260726-gc", BatchSize: 64,
			LeaseDuration: time.Minute, RetryDelay: time.Second,
		},
	)
	worked, err := gcWorker.RunOnce(h.ctx)
	if err != nil || !worked {
		t.Fatalf("GC worked=%v err=%v", worked, err)
	}
	bug20260726DeleteDocument(t, h.http.URL, accepted.DocumentID, deleteKey)

	if _, err := os.Stat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed source survived cleanup: %v", err)
	}
	for name, query := range map[string]string{
		"documents":           `SELECT COUNT(*) FROM kb_documents WHERE id=?`,
		"chunks":              `SELECT COUNT(*) FROM kb_chunks WHERE doc_id=?`,
		"fts":                 `SELECT COUNT(*) FROM kb_chunks_fts`,
		"bindings":            `SELECT COUNT(*) FROM kb_semantic_document_bindings WHERE document_id=?`,
		"generations":         `SELECT COUNT(*) FROM kb_semantic_document_generations WHERE document_id=?`,
		"revision_documents":  `SELECT COUNT(*) FROM kb_revision_documents WHERE document_id=?`,
		"vectors":             `SELECT COUNT(*) FROM kb_revision_vectors WHERE document_id=?`,
		"jobs":                `SELECT COUNT(*) FROM kb_knowledge_jobs`,
		"job_checkpoints":     `SELECT COUNT(*) FROM kb_job_stage_checkpoints`,
		"page_checkpoints":    `SELECT COUNT(*) FROM kb_ingest_page_checkpoints`,
		"execution_snapshots": `SELECT COUNT(*) FROM kb_ingest_execution_snapshots`,
		"segments":            `SELECT COUNT(*) FROM kb_ingest_segments`,
		"job_failures":        `SELECT COUNT(*) FROM kb_job_failures`,
		"sources":             `SELECT COUNT(*) FROM kb_ingest_document_sources WHERE document_id=?`,
		"blobs":               `SELECT COUNT(*) FROM kb_ingest_blobs`,
		"batch_manifests":     `SELECT COUNT(*) FROM kb_embedding_batch_manifests`,
		"batch_chunks":        `SELECT COUNT(*) FROM kb_embedding_batch_chunks`,
	} {
		var count int
		args := []any{}
		switch name {
		case "documents", "chunks", "bindings", "generations",
			"revision_documents", "vectors", "sources":
			args = append(args, accepted.DocumentID)
		}
		if err := h.db.QueryRow(query, args...).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 0 {
			t.Errorf("cleanup left %s=%d", name, count)
		}
	}

	restartedRepository := knowledge.NewSQLiteSemanticIndexRepository(h.db)
	restartedService := knowledge.NewSemanticIndexService(
		restartedRepository,
		bug20260726EmbeddingResolver{},
	)
	if err := restartedService.ConfigureDocumentIngest(h.objectRoot); err != nil {
		t.Fatal(err)
	}
	restartedStore := knowledge.NewSQLiteStore(
		h.db,
		knowledge.WithSQLiteSemanticMutations("desktop-user", "default"),
	)
	restartedManager := knowledge.NewManager(restartedStore, restartedStore, nil)
	restartedHTTP := newBug20260726KnowledgeHTTPServer(t, restartedManager, restartedService)
	bug20260726DeleteDocument(t, restartedHTTP.URL, accepted.DocumentID, deleteKey)
}
