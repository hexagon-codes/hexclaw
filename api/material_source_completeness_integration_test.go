package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/knowledge"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestMaterialPreparationMarkdownRemoteObjectsPersistWithoutBlockingText(t *testing.T) {
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path]++
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pixels.Bytes())
	}))
	defer server.Close()
	body := "# 图片练习\n1. 4.5×2=\n![图][shape]\n![相同图](" + server.URL + "/second)\n2. 8÷2=\n![未取得图](" + server.URL + "/missing)\n# 独立练习\n3. 12÷3=\n[shape]: " + server.URL + "/first\n"
	db, records, worker, doc := materialImportFixture(t, body)
	for n := 0; n < 3; n++ {
		if did, err := worker.RunOnce(t.Context()); err != nil {
			t.Fatalf("independent question: %v %v", did, err)
		} else if !did {
			break
		}
	}
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || summary.Counts["ready"] != 1 || summary.ExtractionComplete {
		t.Fatalf("dependent or independent question incorrect: %+v %v", summary, err)
	}
	var raw string
	if err = db.QueryRow(`SELECT manifest_json FROM kb_ingest_source_manifests WHERE document_id=?`, doc).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m knowledge.SourceManifest
	if err = json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Objects) != 3 {
		t.Fatalf("objects: %+v", m.Objects)
	}
	sum := sha256.Sum256(pixels.Bytes())
	digest := hex.EncodeToString(sum[:])
	var path string
	for _, object := range m.Objects {
		if strings.HasSuffix(object.Path, "/missing") {
			if !object.Missing || object.StoragePath != "" || object.Issue == "" {
				t.Fatalf("failed retrieval lost: %+v", object)
			}
			continue
		}
		data, e := os.ReadFile(object.StoragePath)
		if e != nil || !bytes.Equal(data, pixels.Bytes()) || object.Digest != digest || object.Missing {
			t.Fatalf("actual attachment: %+v %v", object, e)
		}
		if path != "" && path != object.StoragePath {
			t.Fatal("identical content was not deduplicated")
		}
		path = object.StoragePath
	}
	for _, n := range seen {
		if n != 1 {
			t.Fatalf("download repeated: %+v", seen)
		}
	}
	if len(m.Relations) != 3 {
		t.Fatalf("source relationships: %+v", m.Relations)
	}
	repo := knowledge.NewSQLiteSemanticIndexRepository(db)
	referenced, err := repo.IsIngestBlobPathReferenced(t.Context(), path)
	if err != nil || !referenced {
		t.Fatalf("attachment not referenced: %v %v", referenced, err)
	}
	service := knowledge.NewSemanticIndexService(repo, materialTestResolver{})
	root := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	if err = service.ConfigureDocumentIngest(root); err != nil {
		t.Fatal(err)
	}
	if data, e := os.ReadFile(path); e != nil || !bytes.Equal(data, pixels.Bytes()) {
		t.Fatalf("restart removed attachment: %v", e)
	}
	server.Close()
	kb := knowledge.NewSQLiteStore(db)
	manager := knowledge.NewManager(kb, kb, nil)
	apiServer := NewServer(config.DefaultConfig(), nil, nil, nil)
	apiServer.SetKnowledgeBase(manager)
	apiServer.SetSemanticIndexService(service)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge/documents/"+doc, nil)
	req.SetPathValue("id", doc)
	rec := httptest.NewRecorder()
	apiServer.handleGetDocument(rec, req)
	var detail knowledge.Document
	if err = json.Unmarshal(rec.Body.Bytes(), &detail); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("offline detail: %d %s %v", rec.Code, rec.Body.String(), err)
	}
	imageURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pixels.Bytes())
	if strings.Count(detail.Content, imageURI) != 2 || strings.Contains(detail.Content, server.URL+"/first") || strings.Contains(detail.Content, server.URL+"/second") {
		t.Fatalf("offline images were not projected from actual bytes: %s", detail.Content)
	}
	stored, err := manager.GetDocument(t.Context(), doc)
	if err != nil || strings.Contains(stored.Content, "data:image") || !strings.Contains(stored.Content, server.URL+"/second") {
		t.Fatalf("display bytes polluted indexed source: %+v %v", stored, err)
	}
	t.Logf("Actual Markdown source manifest: %s", raw)
}

type materialSharedContextSolver struct {
	next *materialControlledSolver
	t    *testing.T
}

func (s materialSharedContextSolver) Solve(ctx context.Context, problem, grade, constraint string) (usecase.SolveResult, error) {
	if !strings.Contains(problem, "商店3本书售价18元") || !strings.Contains(problem, "买7本同样的书多少钱") {
		s.t.Fatalf("shared source was detached: %s", problem)
	}
	return s.next.Solve(ctx, problem, grade, constraint)
}
func TestMaterialPreparationExplicitSharedMaterialRemainsInFactsAndSolver(t *testing.T) {
	db, records, worker, doc := materialImportFixture(t, "# 公共材料\n商店3本书售价18元。\n1. 买7本同样的书多少钱？\n# 独立练习\n2. 12÷3=\n")
	local := worker.Solver
	boundary := &materialControlledSolver{t: t}
	for i := 0; i < 2; i++ {
		p, err := records.NextMaterialPreparation(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		worker.Solver = local
		if strings.Contains(p.Candidate.Facts.Stem, "买7本") {
			if len(p.Candidate.Facts.SharedMaterial) != 2 || p.Candidate.Facts.SharedMaterial[1] != "商店3本书售价18元。" || len(p.Candidate.SourceLocations) != 3 {
				t.Fatalf("shared facts/locations lost: %+v", p.Candidate)
			}
			worker.Solver = materialSharedContextSolver{next: boundary, t: t}
			worker.ResolveModel = materialModelRoute
		} else if len(p.Candidate.Facts.SharedMaterial) != 0 {
			t.Fatal("shared context escaped its section")
		}
		if did, err := worker.RunOnce(t.Context()); err != nil || !did {
			t.Fatalf("prepare %v %v", did, err)
		}
	}
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || summary.Counts["ready"] != 2 || boundary.calls != 2 {
		t.Fatalf("shared material result %+v %v calls=%d", summary, err, boundary.calls)
	}
	var facts string
	if err = db.QueryRow(`SELECT facts_json FROM k12_problem_asset_versions WHERE answer LIKE '%42元%'`).Scan(&facts); err != nil || !strings.Contains(facts, "商店3本书售价18元") {
		t.Fatalf("asset identity lost shared facts: %s %v", facts, err)
	}
}
func TestMaterialPreparationUnresolvedSharedImageCannotPublishBareArithmetic(t *testing.T) {
	_, records, worker, doc := materialImportFixture(t, "# 公共材料\n根据下图确定每题的倍率。\n1. 4.5×2=\n# 独立练习\n2. 12÷3=\n")
	if did, err := worker.RunOnce(t.Context()); err != nil || !did {
		t.Fatalf("independent question: %v %v", did, err)
	}
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || summary.Counts["ready"] != 1 || summary.Counts["needs_review"] != 1 {
		t.Fatalf("unresolved context published: %+v %v", summary, err)
	}
	for _, item := range summary.Items {
		if item.Stem == "4.5×2=" && (item.Answer != "" || item.State != "needs_review") {
			t.Fatalf("bare arithmetic published: %+v", item)
		}
	}
}
