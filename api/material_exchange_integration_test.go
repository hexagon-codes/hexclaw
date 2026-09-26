package api

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/knowledge"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func materialExchangeInput(filename string, data []byte) func(*sql.DB, *knowledge.CreateDocumentInput) {
	return func(_ *sql.DB, in *knowledge.CreateDocumentInput) {
		in.Filename = filename
		in.MediaType = "application/octet-stream"
		in.Body = bytes.NewReader(data)
		in.SizeBytes = int64(len(data))
	}
}
func materialHexbankFixture(t *testing.T, manifest, questions string, objects map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	write := func(name string, data []byte) {
		t.Helper()
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", []byte(manifest))
	write("questions.jsonl", []byte(questions))
	for name, data := range objects {
		write(name, data)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestMaterialPreparationJSONLValidatesReferencesWithoutTrustingPackageIDs(t *testing.T) {
	data := []byte(`{"schema_version":1,"id":"foreign-asset","owner_id":"other-user","asset_id":"foreign-asset","verified":true,"subject":"数学","stem":"4.5×2=","reference_answer":"999","grade_term":"六年级上","source":{"label":"练习一","location":"第1题","page":45}}
{"schema_version":1,"id":"second","subject":"数学","stem":"12÷3=","grade_term":"六年级上"}
`)
	db, records, worker, doc := materialImportFixture(t, "", materialExchangeInput("练习.jsonl", data))
	for i := 0; i < 2; i++ {
		if _, err := worker.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || summary.State != "ready" || summary.Counts["ready"] != 2 {
		t.Fatalf("JSONL preparation %+v %v", summary, err)
	}
	if summary.Items[0].ReferenceAnswer != "999" || strings.Contains(summary.Items[0].Answer, "999") || summary.Items[0].AssetID == "foreign-asset" || summary.Items[0].Page != 0 {
		t.Fatalf("package authority leaked: %+v", summary.Items)
	}
	var otherOwner, studentJobs, providerCalls int
	for query, target := range map[string]*int{`SELECT COUNT(*) FROM k12_problem_assets WHERE owner_id<>'desktop-user'`: &otherOwner, `SELECT COUNT(*) FROM k12_grading_jobs`: &studentJobs, `SELECT COUNT(*) FROM k12_material_invocations WHERE execution_kind='provider'`: &providerCalls} {
		if err = db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if otherOwner != 0 || studentJobs != 0 || providerCalls != 0 {
		t.Fatalf("unexpected side effects owners=%d jobs=%d calls=%d", otherOwner, studentJobs, providerCalls)
	}
	if did, err := worker.RunOnce(t.Context()); err != nil || did {
		t.Fatalf("completed replay %v %v", did, err)
	}
}

type materialExchangeModel struct {
	inner *materialControlledSolver
	t     *testing.T
}

func (s materialExchangeModel) Solve(ctx context.Context, problem, grade, constraint string) (usecase.SolveResult, error) {
	if !strings.Contains(problem, "3本书售价18元") || !strings.Contains(problem, "A. 42元") || !strings.Contains(problem, "7本") || strings.Contains(problem, "999") {
		s.t.Fatalf("incomplete independent input: %s", problem)
	}
	return s.inner.Solve(ctx, problem, grade, constraint)
}
func TestMaterialPreparationHexbankPreservesFullFactsAndUnresolvedObjects(t *testing.T) {
	questions := `{"schema_version":1,"id":"shared","subject":"数学","stem":"7本同样的书一共多少元？","shared_material":["商店3本书售价18元"],"options":[{"label":"A","text":"42元"},{"label":"B","text":"40元"}],"reference_answer":"999","grade_term":"六年级上"}
{"schema_version":1,"id":"figure","subject":"数学","stem":"求图形的面积","object_ids":["figure"]}
{"schema_version":1,"id":"missing","subject":"数学","stem":"根据图片回答","object_ids":["absent"]}
{"schema_version":1,"id":"formula","subject":"数学","stem":"计算结果","formulas":["x^2+1"]}
`
	data := materialHexbankFixture(t, `{"schema_version":1,"questions":"questions.jsonl","objects":[{"object_id":"figure","path":"objects/figure.svg","kind":"image"}]}`, questions, map[string][]byte{"objects/figure.svg": []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="5" height="5"/></svg>`)})
	db, records, worker, doc := materialImportFixture(t, "", materialExchangeInput("练习.hexbank", data))
	boundary := &materialControlledSolver{t: t}
	worker.Solver = materialExchangeModel{inner: boundary, t: t}
	worker.ResolveModel = materialModelRoute
	for n := 0; n < 3; n++ {
		if did, err := worker.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		} else if !did {
			break
		}
	}
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || summary.Counts["ready"] != 1 || summary.Counts["needs_review"] != 3 || summary.ExtractionComplete || boundary.calls != 2 {
		t.Fatalf("package partial preparation %+v %v calls=%d", summary, err, boundary.calls)
	}
	var facts, raw string
	if err = db.QueryRow(`SELECT facts_json FROM k12_problem_asset_versions`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(facts, `"shared_material":["商店3本书售价18元"]`) || !strings.Contains(facts, `"label":"A"`) {
		t.Fatalf("asset facts missing: %s", facts)
	}
	if err = db.QueryRow(`SELECT manifest_json FROM kb_ingest_source_manifests WHERE document_id=?`, doc).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var manifest knowledge.SourceManifest
	if err = json.Unmarshal([]byte(raw), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Objects) != 1 || manifest.Objects[0].Digest == "" || manifest.Objects[0].Missing || len(manifest.Relations) < 2 {
		t.Fatalf("package objects lost: %+v", manifest)
	}
	t.Logf("Actual hexbank source manifest: %s", raw)
}

func TestMaterialPreparationExchangeInvalidRecordsDoNotBlockIndependentQuestions(t *testing.T) {
	data := []byte(`{"schema_version":1,"id":"same","subject":"数学","stem":"4.5×2="}
{"schema_version":1,"id":"same","subject":"数学","stem":"8÷2="}
{"schema_version":2,"id":"future","subject":"数学","stem":"6×2="}
not-json
{"schema_version":1,"id":"independent","subject":"数学","stem":"12÷3="}
`)
	_, records, worker, doc := materialImportFixture(t, "", materialExchangeInput("部分题目.jsonl", data))
	if _, err := worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || summary.Counts["ready"] != 1 || summary.Counts["needs_review"] != 4 || len(summary.Items) != 5 {
		t.Fatalf("partial records: %+v %v", summary, err)
	}
}

func TestMaterialPreparationHexbankUnknownVersionKeepsAcceptedOriginal(t *testing.T) {
	db, _, _, _ := materialImportFixture(t, "1. 4.5×2=\n")
	repo := knowledge.NewSQLiteSemanticIndexRepository(db)
	service := knowledge.NewSemanticIndexService(repo, materialTestResolver{})
	if err := service.ConfigureDocumentIngest(filepath.Join(t.TempDir(), "objects")); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(config.DefaultConfig(), nil, nil, nil)
	srv.SetSemanticIndexService(service)
	data := materialHexbankFixture(t, `{"schema_version":99,"questions":"questions.jsonl"}`, "", nil)
	upload := func() knowledge.CreateDocumentResult {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		file, err := form.CreateFormFile("file", "future.hexbank")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write(data); err != nil {
			t.Fatal(err)
		}
		_ = form.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge/documents?user_id=desktop-user", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("Idempotency-Key", "future-version")
		response := httptest.NewRecorder()
		srv.handleCreateKnowledgeDocument(response, req)
		if response.Code != http.StatusAccepted {
			t.Fatalf("upload %d %s", response.Code, response.Body.String())
		}
		var result knowledge.CreateDocumentResult
		if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	accepted := upload()
	replay := upload()
	if accepted.DocumentID != replay.DocumentID || accepted.JobID != replay.JobID {
		t.Fatalf("upload replay changed identity %+v %+v", accepted, replay)
	}
	now := time.Now().UTC()
	job, claimed, err := repo.ClaimNextJobForCorpus(t.Context(), "desktop-user", "default", "format-test", now, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim %v %v", claimed, err)
	}
	source, err := repo.GetIngestDocument(t.Context(), "desktop-user", accepted.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	kb := knowledge.NewSQLiteStore(db)
	manager := knowledge.NewManager(kb, kb, nil)
	_, err = NewKnowledgeDocumentIngestProcessor(manager).Prepare(t.Context(), source)
	if err == nil || !strings.Contains(err.Error(), "unsupported hexbank schema version 99") {
		t.Fatalf("unknown version accepted: %v", err)
	}
	if _, err = repo.FailJob(t.Context(), job.Lease(), now, err.Error()); err != nil {
		t.Fatal(err)
	}
	reader, _, err := service.OpenDocumentSource(t.Context(), "desktop-user", "default", accepted.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	preserved, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(preserved, data) {
		t.Fatalf("accepted original was not preserved %v", err)
	}
}
