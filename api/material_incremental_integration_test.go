package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
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

type materialPDFProgress struct {
	repo  *knowledge.SQLiteSemanticIndexRepository
	job   knowledge.KnowledgeJob
	after func(knowledge.IngestPageCheckpoint)
}

func (p materialPDFProgress) SetPageTotal(ctx context.Context, digest string, total int64) error {
	return p.repo.SetIngestPageTotal(ctx, p.job.Lease(), time.Now().UTC(), digest, total)
}
func (p materialPDFProgress) LoadCompletedPages(ctx context.Context, digest string, total int64) ([]knowledge.IngestPageCheckpoint, error) {
	return p.repo.LoadIngestPageCheckpoints(ctx, p.job.Lease(), time.Now().UTC(), digest, total)
}
func (p materialPDFProgress) CommitPage(ctx context.Context, c knowledge.IngestPageCheckpoint) error {
	if err := p.repo.SaveIngestPageCheckpoint(ctx, p.job.Lease(), time.Now().UTC(), c); err != nil {
		return err
	}
	if p.after != nil {
		p.after(c)
	}
	return nil
}

type materialPDFHarness struct {
	db        *sql.DB
	records   *k12storage.Store
	worker    *usecase.MaterialPreparationWorker
	repo      *knowledge.SQLiteSemanticIndexRepository
	service   *knowledge.SemanticIndexService
	source    knowledge.PersistedIngestDocument
	job       knowledge.KnowledgeJob
	processor knowledge.ResumableDocumentIngestProcessor
	bytes     []byte
}

func materialIncrementalPDF(t *testing.T, withAnswers ...bool) *materialPDFHarness {
	t.Helper()
	requirePopplerForAsyncPDFTest(t)
	db, records, worker, _ := materialImportFixture(t, "# Introduction\nA document without numbered exercises.\n")
	repo := knowledge.NewSQLiteSemanticIndexRepository(db)
	repo.SetDocumentIngestLifecycleObserver(engineadapter.NewTextbookManifestLifecycleAdapter(records))
	service := knowledge.NewSemanticIndexService(repo, materialTestResolver{})
	if err := service.ConfigureDocumentIngest(filepath.Join(t.TempDir(), "objects")); err != nil {
		t.Fatal(err)
	}
	// PDF 文字流使用真实逐行绘制，后续由实际 Poppler 解析，不直接注入题目结果。
	pages := [][]string{
		{"# Mathematics addition and division exercises", "# Elementary arithmetic practice with complete numbered questions", "1. 4.5*2=9", "2. 12+"},
		{"3=", "3. 18/3=", "4. 20/4=", "5. 100+20+30+40+50+60+70+80+90+", "100+110+120+130+140+150+160+170+180+190+200+210="},
		{"# Answers", "1. 999 is listed in the source answer key.", "2. The result is 15.", "3. The result is 6.", "4. The result is 5.", "5. The result is listed in the printed answer key.", "# End of the elementary mathematics exercise answer key"},
	}
	if len(withAnswers) > 0 && !withAnswers[0] {
		pages = pages[:2]
	}
	streams := make([]string, len(pages))
	for i, lines := range pages {
		streams[i] = strings.Join(lines, ") Tj 0 -20 Td (")
	}
	data := buildTextLayerPagesForOffsetTest(t, streams)
	accepted, err := service.CreateDocument(t.Context(), "desktop-user", "default", knowledge.CreateDocumentInput{IdempotencyKey: "incremental-pdf", Filename: "六年级上数学.pdf", MediaType: "application/pdf", SizeBytes: int64(len(data)), Body: bytes.NewReader(data), Grade: "六年级上", Subject: "数学"})
	if err != nil {
		t.Fatal(err)
	}
	job, claimed, err := repo.ClaimNextJobForCorpus(t.Context(), "desktop-user", "default", "material-pdf", time.Now().UTC(), 5*time.Minute)
	if err != nil || !claimed || job.DocumentID != accepted.DocumentID {
		t.Fatalf("PDF claim: %+v %v %v", job, claimed, err)
	}
	source, err := repo.GetIngestDocument(t.Context(), "desktop-user", accepted.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	actualPages, warning, err := extractPDFTextPagesFromPath(t.Context(), source.StoragePath, len(pages), 1<<20)
	if err != nil || warning != "" || len(actualPages) != len(pages) {
		t.Fatalf("PDF fixture text layer: %d %s %v", len(actualPages), warning, err)
	}
	for i, page := range actualPages {
		if !pdfPageHasUsableTextLayer(page) {
			t.Fatalf("PDF fixture page %d is not an intact text layer: %q", i+1, page)
		}
	}
	kb := knowledge.NewSQLiteStore(db)
	manager := knowledge.NewManager(kb, kb, nil, knowledge.WithSplitter(splitter.NewMarkdownSplitter()), knowledge.WithCaptioner(knowledge.CaptionerFunc(func(context.Context, []byte, string) (string, error) {
		t.Fatal("text-layer PDF must not invoke VLM")
		return "", nil
	})))
	return &materialPDFHarness{db: db, records: records, worker: worker, repo: repo, service: service, source: source, job: job, processor: NewKnowledgeDocumentIngestProcessor(manager).(knowledge.ResumableDocumentIngestProcessor), bytes: data}
}

func materialRunReady(t *testing.T, h *materialPDFHarness, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if did, err := h.worker.RunOnce(t.Context()); err != nil || !did {
			t.Fatalf("prepare %d/%d: %v %v", i, n, did, err)
		}
	}
}

func TestMaterialPreparationPDFIncrementalClosedGroupsAndLateAnswers(t *testing.T) {
	h := materialIncrementalPDF(t)
	seen := map[int]bool{}
	var firstBefore string
	prepared, err := h.processor.PrepareResumable(t.Context(), h.source, materialPDFProgress{repo: h.repo, job: h.job, after: func(c knowledge.IngestPageCheckpoint) {
		if seen[c.PageNumber] {
			return
		}
		seen[c.PageNumber] = true
		summary, err := h.records.GetMaterialPreparationSummary(t.Context(), "desktop-user", h.source.DocumentID)
		if err != nil {
			t.Fatal(err)
		}
		switch c.PageNumber {
		case 1:
			if len(summary.Items) != 1 || summary.Items[0].Stem != "4.5*2=" || summary.ExtractionComplete {
				t.Fatalf("unfinished tail was prepared: %+v", summary)
			}
			materialRunReady(t, h, 1)
			if err := h.db.QueryRow(`SELECT candidate_json || '|' || input_digest || '|' || result_json FROM k12_material_preparations WHERE document_id=?`, h.source.DocumentID).Scan(&firstBefore); err != nil {
				t.Fatal(err)
			}
		case 2:
			if len(summary.Items) != 4 {
				t.Fatalf("closed cross-page groups: %+v", summary)
			}
			for _, item := range summary.Items {
				if strings.HasPrefix(item.Stem, "100+") {
					t.Fatal("last question published before its closing boundary")
				}
			}
			materialRunReady(t, h, 3)
		case 3:
			materialRunReady(t, h, 1)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.repo.CompleteIngestDocument(t.Context(), h.job.Lease(), time.Now().UTC(), prepared); err != nil {
		t.Fatal(err)
	}
	summary, err := h.records.GetMaterialPreparationSummary(t.Context(), "desktop-user", h.source.DocumentID)
	if err != nil || summary.Counts["ready"] != 5 || summary.State != "needs_review" {
		t.Fatalf("published groups and conflicting source: %+v %v", summary, err)
	}
	var firstAfter, raw string
	if err = h.db.QueryRow(`SELECT candidate_json || '|' || input_digest || '|' || result_json FROM k12_material_preparations WHERE document_id=? AND json_extract(candidate_json,'$.question_number')='1'`, h.source.DocumentID).Scan(&firstAfter); err != nil {
		t.Fatal(err)
	}
	if firstBefore != firstAfter {
		t.Fatal("late reference answer changed the completed input or result")
	}
	if err = h.db.QueryRow(`SELECT manifest_json FROM k12_material_manifests WHERE document_id=?`, h.source.DocumentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var manifest k12storage.MaterialManifest
	if err = json.Unmarshal([]byte(raw), &manifest); err != nil || manifest.Progress.PagesReady != 3 || !manifest.Progress.SourceComplete {
		t.Fatalf("manifest progress: %+v %v", manifest.Progress, err)
	}
	var foundConflict, foundCrossPage bool
	for _, observation := range manifest.ReferenceObservations {
		if observation.QuestionNumber == "1" && strings.HasPrefix(observation.Text, "999") && observation.ReviewRequired && observation.Locations[0].BlockID == "page:3" {
			foundConflict = true
		}
		if observation.QuestionNumber == "2" && observation.ReviewRequired {
			t.Fatal("late arrival alone was mislabeled as an answer conflict")
		}
	}
	for _, item := range summary.Items {
		if item.Stem == "12+\n3=" && item.Page == 1 && strings.Contains(item.Answer, "15") {
			foundCrossPage = true
		}
	}
	if !foundConflict || !foundCrossPage {
		t.Fatalf("source evidence: conflict=%v cross_page=%v manifest=%s summary=%+v", foundConflict, foundCrossPage, raw, summary)
	}
	if dir := os.Getenv("HEXCLAW_MATERIAL_EVIDENCE_DIR"); dir != "" {
		if err = os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		summaryJSON, _ := json.MarshalIndent(summary, "", "  ")
		for name, data := range map[string][]byte{"incremental-source.pdf": h.bytes, "incremental-manifest.json": []byte(raw), "incremental-summary.json": summaryJSON} {
			if err = os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	file, _, err := h.service.OpenDocumentSource(t.Context(), "desktop-user", "default", h.source.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := io.ReadAll(file)
	file.Close()
	if err != nil || !bytes.Equal(actual, h.bytes) {
		t.Fatal("original PDF changed")
	}
	hits, err := knowledge.NewSQLiteStore(h.db).TextSearch(t.Context(), "Mathematics", 10, knowledge.Filter{})
	if err != nil || len(hits) == 0 {
		t.Fatalf("text publication blocked: %v %v", hits, err)
	}
	if did, err := h.worker.RunOnce(t.Context()); err != nil || did {
		t.Fatalf("completed groups replayed: %v %v", did, err)
	}
	for _, table := range []string{"k12_grading_jobs", "k12_grading_assessment_items", "k12_mistakes"} {
		var count int
		if err = h.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("student side effect %s: %d %v", table, count, err)
		}
	}
	t.Logf("actual_pdf_pages=%d ready=%d reference_conflict_preserved=%v provider_calls=0", manifest.Progress.PagesReady, summary.Counts["ready"], foundConflict)
}

func TestMaterialPreparationPDFLastQuestionWaitsForCompleteSource(t *testing.T) {
	h := materialIncrementalPDF(t, false)
	prepared, err := h.processor.PrepareResumable(t.Context(), h.source, materialPDFProgress{repo: h.repo, job: h.job})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := h.records.GetMaterialPreparationSummary(t.Context(), "desktop-user", h.source.DocumentID)
	if err != nil || len(summary.Items) != 4 || summary.ExtractionComplete {
		t.Fatalf("end of pages was mistaken for complete source: %+v %v", summary, err)
	}
	materialRunReady(t, h, 4)
	if err = h.repo.CompleteIngestDocument(t.Context(), h.job.Lease(), time.Now().UTC(), prepared); err != nil {
		t.Fatal(err)
	}
	materialRunReady(t, h, 1)
	summary, err = h.records.GetMaterialPreparationSummary(t.Context(), "desktop-user", h.source.DocumentID)
	if err != nil || summary.Counts["ready"] != 5 || !summary.ExtractionComplete {
		t.Fatalf("complete source did not release its last question: %+v %v", summary, err)
	}
}

func TestMaterialPreparationPDFGapAndUnknownDoNotReplay(t *testing.T) {
	h := materialIncrementalPDF(t)
	pages, warning, err := extractPDFTextPagesFromPath(t.Context(), h.source.StoragePath, 3, 1<<20)
	if err != nil || warning != "" || len(pages) != 3 {
		t.Fatalf("real PDF pages: %d %s %v", len(pages), warning, err)
	}
	p := materialPDFProgress{repo: h.repo, job: h.job}
	if err = p.SetPageTotal(t.Context(), h.source.SHA256, 3); err != nil {
		t.Fatal(err)
	}
	commit := func(page int) {
		t.Helper()
		if err := p.CommitPage(t.Context(), knowledge.IngestPageCheckpoint{PageNumber: page, PagesTotal: 3, SourceDigest: h.source.SHA256, ExtractionMode: "text", Content: strings.TrimSpace(pages[page-1])}); err != nil {
			t.Fatal(err)
		}
	}
	commit(1)
	commit(3)
	summary, err := h.records.GetMaterialPreparationSummary(t.Context(), "desktop-user", h.source.DocumentID)
	if err != nil || len(summary.Items) != 1 {
		t.Fatalf("crossed missing page: %+v %v", summary, err)
	}
	task, err := h.records.NextMaterialPreparation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = h.records.ClaimMaterialPreparation(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	invocation, _, err := h.records.ClaimMaterialInvocation(t.Context(), task, "solve_generate", "unknown-original")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.records.FinishMaterialInvocation(t.Context(), invocation, "original unknown response", context.DeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	if err = h.records.RecoverMaterialPreparations(t.Context()); err != nil {
		t.Fatal(err)
	}
	commit(1)
	commit(3)
	if did, err := h.worker.RunOnce(t.Context()); err != nil || did {
		t.Fatalf("unknown or gap replay: %v %v", did, err)
	}
	var status, result string
	if err = h.db.QueryRow(`SELECT status,result_json FROM k12_material_invocations WHERE invocation_id=?`, invocation.ID).Scan(&status, &result); err != nil || status != "outcome_unknown" || result != "original unknown response" {
		t.Fatalf("original receipt changed: %s %s %v", status, result, err)
	}
	commit(2)
	task, err = h.records.GetMaterialPreparation(t.Context(), task.TaskID)
	if err != nil || task.State != "outcome_unknown" {
		t.Fatalf("later pages reset unknown task: %+v %v", task, err)
	}
	var count int
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM k12_material_invocations WHERE task_id=?`, task.TaskID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("receipt duplicated: %d %v", count, err)
	}
}

func TestMaterialPreparationPDFReparseKeepsOldUntilSourceCAS(t *testing.T) {
	h := materialIncrementalPDF(t)
	prepared, err := h.processor.PrepareResumable(t.Context(), h.source, materialPDFProgress{repo: h.repo, job: h.job})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.repo.CompleteIngestDocument(t.Context(), h.job.Lease(), time.Now().UTC(), prepared); err != nil {
		t.Fatal(err)
	}
	materialRunReady(t, h, 1)
	old, err := h.records.NextMaterialPreparation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = h.records.ClaimMaterialPreparation(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	accepted, err := h.service.ReparseDocument(t.Context(), "desktop-user", "default", h.source.DocumentID, "explicit-reparse", 1)
	if err != nil {
		t.Fatal(err)
	}
	job, claimed, err := h.repo.ClaimNextJobForCorpus(t.Context(), "desktop-user", "default", "candidate-worker", time.Now().UTC(), 5*time.Minute)
	if err != nil || !claimed || job.JobID != accepted.JobID {
		t.Fatalf("candidate: %+v %v %v", job, claimed, err)
	}
	source, err := h.repo.GetIngestDocumentForJob(t.Context(), "desktop-user", job.CorpusUID, h.source.DocumentID, job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err = h.processor.PrepareResumable(t.Context(), source, materialPDFProgress{repo: h.repo, job: job, after: func(knowledge.IngestPageCheckpoint) {
		summary, err := h.records.GetMaterialPreparationSummary(t.Context(), "desktop-user", h.source.DocumentID)
		if err != nil || summary.SourceRevision != 1 || summary.Counts["ready"] != 1 {
			t.Fatalf("unpublished generation replaced old preparation: %+v %v", summary, err)
		}
		var count int
		if err = h.db.QueryRow(`SELECT COUNT(*) FROM k12_material_preparations WHERE document_id=? AND source_revision=2`, h.source.DocumentID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("unpublished candidate escaped: %d %v", count, err)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.repo.CompleteIngestDocument(t.Context(), job.Lease(), time.Now().UTC(), prepared); err != nil {
		t.Fatal(err)
	}
	summary, err := h.records.GetMaterialPreparationSummary(t.Context(), "desktop-user", h.source.DocumentID)
	if err != nil || summary.SourceRevision != 2 || len(summary.Items) != 4 {
		// 题 1 自带答案 9 与末页 999 冲突，新代不能推测参考答案。
		t.Fatalf("new source projection: %+v %v", summary, err)
	}
	if _, err = h.records.PublishPreparedMaterialAsset(t.Context(), old.TaskID); !errors.Is(err, k12storage.ErrMaterialPreparationFenced) {
		t.Fatalf("stale publication: %v", err)
	}
	old, err = h.records.GetMaterialPreparation(t.Context(), old.TaskID)
	if err != nil || old.State != "stopped" {
		t.Fatalf("stale unsent task: %+v %v", old, err)
	}
	var count int
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM k12_material_preparations WHERE document_id=? AND source_revision=1 AND state='published'`, h.source.DocumentID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("successful old history changed: %d %v", count, err)
	}
}
