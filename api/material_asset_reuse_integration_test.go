package api

import (
	"context"
	"database/sql"
	"errors"
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

func materialAdditionalSource(t *testing.T, db *sql.DB, records *k12storage.Store, filename, body, grade string) string {
	t.Helper()
	ctx := t.Context()
	repo := knowledge.NewSQLiteSemanticIndexRepository(db)
	repo.SetDocumentIngestLifecycleObserver(engineadapter.NewTextbookManifestLifecycleAdapter(records))
	service := knowledge.NewSemanticIndexService(repo, materialTestResolver{})
	if err := service.ConfigureDocumentIngest(filepath.Join(t.TempDir(), "objects")); err != nil {
		t.Fatal(err)
	}
	accepted, err := service.CreateDocument(ctx, "desktop-user", "default", knowledge.CreateDocumentInput{IdempotencyKey: filename, Filename: filename, MediaType: "text/markdown", SizeBytes: int64(len(body)), Body: strings.NewReader(body), Grade: grade, Subject: "数学"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	job, claimed, err := repo.ClaimNextJobForCorpus(ctx, "desktop-user", "default", "additional-source", now, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim source: %v %v", claimed, err)
	}
	source, err := repo.GetIngestDocument(ctx, "desktop-user", accepted.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	kb := knowledge.NewSQLiteStore(db)
	prepared, err := NewKnowledgeDocumentIngestProcessor(knowledge.NewManager(kb, kb, nil, knowledge.WithSplitter(splitter.NewMarkdownSplitter()))).Prepare(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteIngestDocument(ctx, job.Lease(), now, prepared); err != nil {
		t.Fatal(err)
	}
	return accepted.DocumentID
}

type materialNoSolve struct{ t *testing.T }

func (s materialNoSolve) Solve(context.Context, string, string, string) (usecase.SolveResult, error) {
	s.t.Fatal("matching verified asset must not enter the solver")
	return usecase.SolveResult{}, errors.New("unexpected solve")
}

func TestMaterialPreparationReusesVerifiedAssetAcrossSourcesBeforeSolving(t *testing.T) {
	const question = "商店3本书售价18元，7本同样的书一共多少元？"
	db, records, worker, first := materialImportFixture(t, "1. "+question+"\n")
	boundary := &materialControlledSolver{t: t}
	worker.Solver, worker.ResolveModel = boundary, materialModelRoute
	if did, err := worker.RunOnce(t.Context()); err != nil || !did {
		t.Fatalf("first source: %v %v", did, err)
	}
	second := materialAdditionalSource(t, db, records, "other.md", "# 第二份资料\n3. "+question+" 答案：999元\n", "六年级上")
	worker.Solver = materialNoSolve{t: t}
	if did, err := worker.RunOnce(t.Context()); err != nil || !did {
		t.Fatalf("reuse source: %v %v", did, err)
	}
	a, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", second)
	if err != nil || b.Counts["ready"] != 1 || len(b.Items) != 1 || b.Items[0].AssetID != a.Items[0].AssetID || b.Items[0].AssetVersion != a.Items[0].AssetVersion || b.Items[0].Answer != a.Items[0].Answer || b.Items[0].ReferenceAnswer != "999元" || b.DocumentID != second {
		t.Fatalf("source association: %+v %v", b, err)
	}
	if boundary.calls != 2 {
		t.Fatalf("duplicate provider calls: %d", boundary.calls)
	}
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM k12_material_invocations WHERE execution_kind='provider'`:                                                          2,
		`SELECT COUNT(*) FROM k12_material_invocations WHERE execution_kind='asset_reuse' AND operation='reuse_verified' AND status='succeeded'`: 1,
		`SELECT COUNT(*) FROM k12_problem_asset_versions`:                                                                                        1,
		`SELECT COUNT(*) FROM k12_problem_asset_publications`:                                                                                    1,
		`SELECT COUNT(*) FROM k12_grading_jobs`:                                                                                                  0,
		`SELECT COUNT(*) FROM k12_grading_item_invocations`:                                                                                      0,
		`SELECT COUNT(*) FROM k12_grading_assessment_items`:                                                                                      0,
		`SELECT COUNT(*) FROM k12_mistakes`:                                                                                                      0,
	} {
		var got int
		if err = db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got %d want %d err %v", query, got, want, err)
		}
	}
	restored := &usecase.MaterialPreparationWorker{Records: k12storage.NewStore(db, nil), Solver: materialNoSolve{t: t}}
	if did, err := restored.RunOnce(t.Context()); err != nil || did {
		t.Fatalf("completed reuse replay: %v %v", did, err)
	}
}

func TestMaterialPreparationReuseRechecksScopeSourceAssetAndUnknown(t *testing.T) {
	for _, mode := range []string{"grade", "source", "asset", "unknown", "sent"} {
		t.Run(mode, func(t *testing.T) {
			db, records, worker, first := materialImportFixture(t, "1. 4.5×2=\n")
			if _, err := worker.RunOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", first)
			if err != nil {
				t.Fatal(err)
			}
			asset, err := records.GetProblemAssetVersion(t.Context(), "desktop-user", summary.Items[0].AssetID, summary.Items[0].AssetVersion)
			if err != nil {
				t.Fatal(err)
			}
			grade := "六年级上"
			if mode == "grade" {
				grade = "五年级下"
			}
			doc := materialAdditionalSource(t, db, records, mode+".md", "2. 4.5×2=\n", grade)
			p, err := records.NextMaterialPreparation(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = records.ClaimMaterialPreparation(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			want := k12storage.ErrProblemAssetEvidence
			switch mode {
			case "source":
				_, err = db.Exec(`UPDATE kb_semantic_document_bindings SET lifecycle_state='tombstoned',deleted_at=1 WHERE document_id=?`, doc)
				want = k12storage.ErrMaterialPreparationFenced
			case "asset":
				err = records.ArchiveProblemAsset(t.Context(), "desktop-user", asset.AssetID, asset.Revision)
				want = k12storage.ErrProblemAssetUnavailable
			case "unknown", "sent":
				var inv k12storage.MaterialInvocation
				inv, _, err = records.ClaimMaterialInvocation(t.Context(), p, "solve_generate", "original-request")
				if err == nil && mode == "unknown" {
					err = records.FinishMaterialInvocation(t.Context(), inv, "original unknown receipt", context.DeadlineExceeded)
				}
				want = k12storage.ErrMaterialPreparationUnknown
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = records.ReuseMaterialProblemAsset(t.Context(), p, asset); !errors.Is(err, want) {
				t.Fatalf("reuse fence: got %v want %v", err, want)
			}
			var n int
			if err = db.QueryRow(`SELECT COUNT(*) FROM k12_material_invocations WHERE execution_kind='asset_reuse'`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("invalid reuse receipt %d %v", n, err)
			}
			stored, err := records.GetMaterialPreparation(t.Context(), p.TaskID)
			if err != nil || stored.State != "running" {
				t.Fatalf("fence mutated original task: %+v %v", stored, err)
			}
		})
	}
}
