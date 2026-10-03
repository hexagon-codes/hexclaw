package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type materialVisualSolver struct {
	t    *testing.T
	next *materialControlledSolver
}

func (s materialVisualSolver) Solve(ctx context.Context, problem, grade, constraint string) (usecase.SolveResult, error) {
	if !strings.Contains(problem, "图示商店3本书售价18元") || strings.Contains(problem, "http://") {
		s.t.Fatalf("persisted visual facts not consumed: %s", problem)
	}
	return s.next.Solve(ctx, problem, grade, constraint)
}
func materialVisualPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

const materialVisualReading = `{"complete":true,"visual_facts":["图示商店3本书售价18元"],"issues":[]}`

func TestMaterialPreparationVisualPersistsFactsAndReusesSuccessfulReceipt(t *testing.T) {
	pixels := materialVisualPNG(t)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pixels)
	}))
	defer source.Close()
	db, records, worker, doc := materialImportFixture(t, "1. 根据图示，买7本同样的书多少钱？\n![原图]("+source.URL+"/figure.png)\n答案：999元\n")
	source.Close()
	pending, pendingErr := records.NextMaterialPreparation(t.Context())
	if pendingErr != nil {
		t.Fatal(pendingErr)
	}
	if _, _, sourceErr := records.MaterialSourceImage(t.Context(), pending); sourceErr != nil {
		t.Fatalf("source image reader: %v", sourceErr)
	}
	var original string
	if err := db.QueryRow(`SELECT candidate_json FROM k12_material_preparations`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	visualCalls := 0
	worker.ResolveVisualModel = materialModelRoute
	worker.ResolveModel = materialModelRoute
	worker.ReadVisual = func(ctx context.Context, image []byte, prompt string) (string, error) {
		visualCalls++
		snapshot, ok := k12.GradingModelSnapshotFromContext(ctx)
		if !ok || snapshot.Provider != "controlled" || !bytes.Equal(image, pixels) || strings.Contains(prompt, source.URL) || strings.Contains(prompt, "999") {
			t.Fatalf("source image/model/prompt mismatch: %+v %s", snapshot, prompt)
		}
		return materialVisualReading, nil
	}
	boundary := &materialControlledSolver{t: t, afterGenerate: func() {
		if _, err := db.Exec(`INSERT INTO agents(name) VALUES('foreground')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO k12_grading_jobs(record_id,agent_name,status,dedupe_key,created_at,updated_at) VALUES('foreground-job','foreground','recognizing','foreground-job',1,1)`); err != nil {
			t.Fatal(err)
		}
	}}
	worker.Solver = materialVisualSolver{t, boundary}
	if did, err := worker.RunOnce(t.Context()); err != nil || !did {
		t.Fatalf("initial preparation %v %v", did, err)
	}
	if visualCalls != 1 || boundary.calls != 1 {
		t.Fatalf("foreground did not defer: visual=%d solve=%d", visualCalls, boundary.calls)
	}
	if _, err := db.Exec(`UPDATE k12_grading_jobs SET status='completed'`); err != nil {
		t.Fatal(err)
	}
	// 新 worker 仍通过同一持久回执恢复，不重读外链、不重复模型请求。
	restarted := &usecase.MaterialPreparationWorker{Records: records, Solver: worker.Solver, ResolveModel: worker.ResolveModel, ResolveVisualModel: worker.ResolveVisualModel, ReadVisual: worker.ReadVisual}
	if did, err := restarted.RunOnce(t.Context()); err != nil || !did {
		t.Fatalf("resumed preparation %v %v", did, err)
	}
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || summary.Counts["ready"] != 1 || visualCalls != 1 || boundary.calls != 2 {
		t.Fatalf("actual ready %+v %v visual=%d solve=%d", summary, err, visualCalls, boundary.calls)
	}
	var current, result, factsRaw string
	if err = db.QueryRow(`SELECT candidate_json,result_json FROM k12_material_preparations`).Scan(&current, &result); err != nil {
		t.Fatal(err)
	}
	if current != original || !strings.Contains(current, source.URL) || !strings.Contains(result, "visual_evidence") {
		t.Fatalf("original candidate/evidence lost: %s %s", current, result)
	}
	if err = db.QueryRow(`SELECT facts_json FROM k12_problem_asset_versions`).Scan(&factsRaw); err != nil {
		t.Fatal(err)
	}
	var facts k12.ProblemAssetFacts
	if err = json.Unmarshal([]byte(factsRaw), &facts); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(pixels)
	if len(facts.VisualFacts) != 1 || facts.VisualFacts[0] != "图示商店3本书售价18元" || len(facts.Objects) != 1 || facts.Objects[0].Digest != hex.EncodeToString(hash[:]) {
		t.Fatalf("published source facts lost: %+v", facts)
	}
	var calls, studentWrites int
	if err = db.QueryRow(`SELECT COUNT(*) FROM k12_material_invocations WHERE status='succeeded'`).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM k12_grading_assessment_items`).Scan(&studentWrites); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || studentWrites != 0 {
		t.Fatalf("actual receipts=%d learning=%d", calls, studentWrites)
	}
}

func TestMaterialPreparationVisualUnknownAndIncompleteNeverPublish(t *testing.T) {
	for _, mode := range []string{"unknown", "incomplete", "invalid", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			pixels := materialVisualPNG(t)
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(pixels) }))
			defer source.Close()
			db, _, worker, _ := materialImportFixture(t, "1. 根据图示，买7本同样的书多少钱？\n![原图]("+source.URL+"/figure.png)\n")
			worker.ResolveVisualModel = materialModelRoute
			calls := 0
			worker.ReadVisual = func(context.Context, []byte, string) (string, error) {
				calls++
				switch mode {
				case "unknown":
					return "", context.DeadlineExceeded
				case "invalid":
					return "not json", nil
				case "deleted":
					_, err := db.Exec(`UPDATE kb_semantic_document_bindings SET lifecycle_state='tombstoned',deleted_at=1`)
					if err != nil {
						t.Fatal(err)
					}
					return materialVisualReading, nil
				default:
					return `{"complete":false,"visual_facts":[],"issues":["Length is unreadable"]}`, nil
				}
			}
			_, err := worker.RunOnce(t.Context())
			if mode == "unknown" && !errors.Is(err, k12storage.ErrMaterialPreparationUnknown) {
				t.Fatalf("unknown lost: %v", err)
			}
			if mode != "unknown" && mode != "deleted" && err != nil {
				t.Fatal(err)
			}
			if did, _ := worker.RunOnce(t.Context()); did {
				t.Fatal("unresolved visual result was retried")
			}
			var state string
			var assets int
			if err = db.QueryRow(`SELECT state FROM k12_material_preparations`).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow(`SELECT COUNT(*) FROM k12_problem_assets`).Scan(&assets); err != nil {
				t.Fatal(err)
			}
			expected := "needs_review"
			if mode == "unknown" {
				expected = "outcome_unknown"
			}
			if mode == "deleted" {
				expected = "stopped"
			}
			if state != expected || assets != 0 || calls != 1 {
				t.Fatalf("untrusted source published: state=%s assets=%d calls=%d", state, assets, calls)
			}
		})
	}
}

func TestMaterialPreparationVisualReadsActualPackagedObjects(t *testing.T) {
	pixels := materialVisualPNG(t)
	for _, format := range []string{"docx", "hexbank"} {
		t.Run(format, func(t *testing.T) {
			data := materialDOCXFixture(t, `<w:p><w:r><w:t>1. 图中买7本同样的书多少钱？</w:t></w:r></w:p><w:p><w:r><w:drawing><a:blip r:embed="rId4"/></w:drawing></w:r></w:p>`, true)
			input := materialDOCXInput(data)
			if format == "hexbank" {
				sum := sha256.Sum256(pixels)
				manifest := `{"schema_version":1,"questions":"questions.jsonl","objects":[{"object_id":"figure","path":"objects/figure.png","kind":"image","digest":"` + hex.EncodeToString(sum[:]) + `"}]}`
				data = materialHexbankFixture(t, manifest, `{"schema_version":1,"id":"q1","subject":"数学","grade_term":"六年级上","stem":"图中买7本同样的书多少钱？","object_ids":["figure"]}`, map[string][]byte{"objects/figure.png": pixels})
				input = materialExchangeInput("exercise.hexbank", data)
			}
			_, records, worker, doc := materialImportFixture(t, "", input)
			worker.ResolveModel = materialModelRoute
			worker.ResolveVisualModel = materialModelRoute
			worker.ReadVisual = func(_ context.Context, data []byte, _ string) (string, error) {
				if !bytes.Equal(data, pixels) {
					t.Fatal("package object bytes changed")
				}
				return materialVisualReading, nil
			}
			worker.Solver = materialVisualSolver{t, &materialControlledSolver{t: t}}
			if did, err := worker.RunOnce(t.Context()); err != nil || !did {
				t.Fatalf("package preparation %v %v", did, err)
			}
			summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
			if err != nil || summary.Counts["ready"] != 1 {
				t.Fatalf("package visual preparation %+v %v", summary, err)
			}
		})
	}
}
