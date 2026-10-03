package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexagon/rag/splitter"
	"github.com/hexagon-codes/hexclaw/knowledge"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func materialDiagramPDF(t *testing.T, otherQuestion bool) *materialPDFHarness {
	t.Helper()
	requirePopplerForAsyncPDFTest(t)
	db, records, worker, _ := materialImportFixture(t, "# Empty introduction\n")
	repo := knowledge.NewSQLiteSemanticIndexRepository(db)
	repo.SetDocumentIngestLifecycleObserver(engineadapter.NewTextbookManifestLifecycleAdapter(records))
	service := knowledge.NewSemanticIndexService(repo, materialTestResolver{})
	if err := service.ConfigureDocumentIngest(filepath.Join(t.TempDir(), "objects")); err != nil {
		t.Fatal(err)
	}
	first := "1. The two-page diagram is one complete rectangle exercise. Read both source pages before finding the total area of the rectangle."
	second := "The diagram continues on this page with the remaining labeled side. Use only these two perpendicular sides to find the rectangle area."
	if otherQuestion {
		second += "\\n2. 4+3="
	}
	// 两页分别绘制红、蓝矩形；真实 Poppler 渲染用于核对合成页序，文字仍经原解析入口。
	pages := []string{first + ") Tj ET q 1 0 0 rg 100 500 140 90 re f Q BT /F1 12 Tf 40 450 Td (", strings.ReplaceAll(second, "\\n", ") Tj 0 -20 Td (") + ") Tj ET q 0 0 1 rg 100 500 140 90 re f Q BT /F1 12 Tf 40 450 Td ("}
	data := buildTextLayerPagesForOffsetTest(t, pages)
	accepted, err := service.CreateDocument(t.Context(), "desktop-user", "default", knowledge.CreateDocumentInput{IdempotencyKey: "two-page-diagram", Filename: "数学六年级上册.pdf", MediaType: "application/pdf", Body: bytes.NewReader(data), SizeBytes: int64(len(data)), Grade: "六年级上", Subject: "数学"})
	if err != nil {
		t.Fatal(err)
	}
	job, claimed, err := repo.ClaimNextJobForCorpus(t.Context(), "desktop-user", "default", "diagram", time.Now(), time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	source, err := repo.GetIngestDocument(t.Context(), "desktop-user", accepted.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	kb := knowledge.NewSQLiteStore(db)
	manager := knowledge.NewManager(kb, kb, nil, knowledge.WithSplitter(splitter.NewMarkdownSplitter()), knowledge.WithCaptioner(knowledge.CaptionerFunc(func(context.Context, []byte, string) (string, error) {
		t.Fatal("text PDF must use original text parser")
		return "", nil
	})))
	processor := NewKnowledgeDocumentIngestProcessor(manager).(knowledge.ResumableDocumentIngestProcessor)
	prepared, err := processor.PrepareResumable(t.Context(), source, materialPDFProgress{repo: repo, job: job})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteIngestDocument(t.Context(), job.Lease(), time.Now(), prepared); err != nil {
		t.Fatal(err)
	}
	worker.PreparePDFSource = NewMaterialPDFSourcePreparer(service, records)
	worker.ResolveModel = materialModelRoute
	worker.ResolveVisualModel = materialModelRoute
	return &materialPDFHarness{db: db, records: records, worker: worker, repo: repo, service: service, source: source, job: job, processor: processor, bytes: data}
}

func TestMaterialPreparationPDFVisualPersistsOrderedPagesAndColdRecovery(t *testing.T) {
	h := materialDiagramPDF(t, false)
	p, err := h.records.NextMaterialPreparation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Candidate.VisualPDFPages) != 2 || p.Candidate.VisualPDFPages[0] != 1 || p.Candidate.VisualPDFPages[1] != 2 {
		t.Fatalf("actual parsed page dependencies: %+v", p.Candidate)
	}
	calls := 0
	h.worker.ReadVisual = func(_ context.Context, data []byte, prompt string) (string, error) {
		calls++
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dy() != 3300 || img.Bounds().Dx() != 1275 {
			t.Fatalf("actual page geometry: %v", img.Bounds())
		}
		r, _, b, _ := img.At(250, 500).RGBA()
		if r < 60000 || b > 1000 {
			t.Fatal("first page pixels lost")
		}
		r, _, b, _ = img.At(250, 2150).RGBA()
		if b < 60000 || r > 1000 {
			t.Fatal("second page order lost")
		}
		if !strings.Contains(prompt, "two-page diagram") || !strings.Contains(prompt, "diagram continues") {
			t.Fatal("cross-page stem lost")
		}
		return materialVisualReading, nil
	}
	boundary := &materialControlledSolver{t: t, afterGenerate: func() {
		if _, err := h.db.Exec(`INSERT INTO agents(name) VALUES('pdf-foreground')`); err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.Exec(`INSERT INTO k12_grading_jobs(record_id,agent_name,status,dedupe_key,created_at,updated_at) VALUES('pdf-foreground-job','pdf-foreground','recognizing','pdf-foreground-job',1,1)`); err != nil {
			t.Fatal(err)
		}
	}}
	h.worker.Solver = materialVisualSolver{t, boundary}
	if did, err := h.worker.RunOnce(t.Context()); err != nil || !did {
		t.Fatalf("initial: %v %v", did, err)
	}
	if calls != 1 || boundary.calls != 1 {
		t.Fatalf("initial actual calls %d %d", calls, boundary.calls)
	}
	var raw, kind string
	if err = h.db.QueryRow(`SELECT result_json,execution_kind FROM k12_material_invocations WHERE task_id=? AND operation='local_pdf_source'`, p.TaskID).Scan(&raw, &kind); err != nil {
		t.Fatal(err)
	}
	var receipt k12storage.MaterialPDFVisualSource
	if json.Unmarshal([]byte(raw), &receipt) != nil || len(receipt.Objects) != 3 || receipt.SourceDigest != h.source.SHA256 || kind != "local_source" {
		t.Fatalf("source receipt %s %s", raw, kind)
	}
	// 重建对象仓库完成孤儿清理后，来源回执引用的页图仍须可读。
	root := filepath.Dir(filepath.Dir(filepath.Dir(receipt.Objects[0].StoragePath)))
	if err = h.service.ConfigureDocumentIngest(root); err != nil {
		t.Fatal(err)
	}
	for _, o := range receipt.Objects {
		if _, err = os.Stat(o.StoragePath); err != nil {
			t.Fatalf("restart collected referenced page: %v", err)
		}
	}
	if err = os.Rename(h.source.StoragePath, h.source.StoragePath+".held"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(h.source.StoragePath+".held", h.source.StoragePath)
	if _, err = h.db.Exec(`UPDATE k12_grading_jobs SET status='completed'`); err != nil {
		t.Fatal(err)
	}
	restarted := &usecase.MaterialPreparationWorker{Records: h.records, Solver: h.worker.Solver, ResolveModel: h.worker.ResolveModel, ResolveVisualModel: h.worker.ResolveVisualModel, ReadVisual: h.worker.ReadVisual, PreparePDFSource: NewMaterialPDFSourcePreparer(h.service, h.records)}
	if did, err := restarted.RunOnce(t.Context()); err != nil || !did {
		t.Fatalf("cold recovery: %v %v", did, err)
	}
	summary, err := h.records.GetMaterialPreparationSummary(t.Context(), "desktop-user", h.source.DocumentID)
	if err != nil || summary.Counts["ready"] != 1 || calls != 1 || boundary.calls != 2 {
		t.Fatalf("actual published: %+v %v visual=%d solve=%d", summary, err, calls, boundary.calls)
	}
	var facts string
	if err = h.db.QueryRow(`SELECT facts_json FROM k12_problem_asset_versions`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(facts, receipt.CompositeDigest) {
		t.Fatal("published asset lost actual composite digest")
	}
	if err = os.Rename(h.source.StoragePath+".held", h.source.StoragePath); err != nil {
		t.Fatal(err)
	}
	if err = knowledge.NewSQLiteStore(h.db, knowledge.WithSQLiteSemanticMutations("desktop-user", "default")).Delete(t.Context(), h.source.DocumentID); err != nil {
		t.Fatal(err)
	}
	gc := knowledge.NewSemanticIndexWorker(h.repo, nil, knowledge.SemanticIndexWorkerConfig{OwnerID: "desktop-user", CorpusID: "default", WorkerID: "pdf-gc"})
	if did, err := gc.RunOnce(t.Context()); err != nil || !did {
		t.Fatalf("actual GC: %v %v", did, err)
	}
	for _, o := range receipt.Objects {
		if _, err = os.Stat(o.StoragePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unreferenced derived object retained: %s %v", o.StoragePath, err)
		}
	}
	t.Log("actual 2-page PDF parsed/rendered; ordered pixels preserved; visual=1 generate=1 verify=1; local source receipt=1; source SHA and asset composite SHA bound; cold recovery and GC passed")
}

func TestMaterialPreparationPDFVisualUnknownMissingSourceAndMixedPages(t *testing.T) {
	for _, mode := range []string{"unknown", "source_changed", "source_changed_after_visual", "missing_source", "missing_page", "mixed_page"} {
		t.Run(mode, func(t *testing.T) {
			h := materialDiagramPDF(t, mode == "mixed_page")
			calls := 0
			h.worker.ReadVisual = func(context.Context, []byte, string) (string, error) {
				calls++
				if mode == "unknown" {
					return "", context.DeadlineExceeded
				}
				if mode == "source_changed_after_visual" {
					if _, err := h.db.Exec(`UPDATE kb_semantic_document_bindings SET content_generation=content_generation+1 WHERE document_id=?`, h.source.DocumentID); err != nil {
						t.Fatal(err)
					}
				}
				return materialVisualReading, nil
			}
			switch mode {
			case "source_changed":
				if _, err := h.db.Exec(`UPDATE kb_semantic_document_bindings SET content_generation=content_generation+1 WHERE document_id=?`, h.source.DocumentID); err != nil {
					t.Fatal(err)
				}
			case "missing_page":
				original := h.worker.PreparePDFSource
				h.worker.PreparePDFSource = func(ctx context.Context, p k12storage.MaterialPreparation) ([]byte, string, error) {
					p.Candidate.VisualPDFPages = []int{1, 3}
					return original(ctx, p)
				}
			case "missing_source":
				if err := os.Remove(h.source.StoragePath); err != nil {
					t.Fatal(err)
				}
			}
			_, err := h.worker.RunOnce(t.Context())
			if mode == "unknown" && !errors.Is(err, k12storage.ErrMaterialPreparationUnknown) {
				t.Fatalf("unknown not preserved: %v", err)
			}
			if mode == "unknown" {
				if did, _ := h.worker.RunOnce(t.Context()); did {
					t.Fatal("unknown was retried")
				}
			}
			var assets int
			if err = h.db.QueryRow(`SELECT COUNT(*) FROM k12_problem_assets`).Scan(&assets); err != nil {
				t.Fatal(err)
			}
			expectedAssets := 0
			if mode == "mixed_page" {
				expectedAssets = 1
				var state string
				if err = h.db.QueryRow(`SELECT state FROM k12_material_preparations WHERE document_id=? AND json_extract(candidate_json,'$.question_number')='1'`, h.source.DocumentID).Scan(&state); err != nil || state != "needs_review" {
					t.Fatalf("mixed diagram published: %s %v", state, err)
				}
			}
			if assets != expectedAssets {
				t.Fatalf("actual assets %d want %d", assets, expectedAssets)
			}
			expected := 0
			if mode == "unknown" || mode == "source_changed_after_visual" {
				expected = 1
			}
			if calls != expected {
				t.Fatalf("visual calls %d want %d", calls, expected)
			}
		})
	}
}
