package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexagon/rag/splitter"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/knowledge"
	"github.com/hexagon-codes/hexclaw/scenario"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

type materialTestResolver struct{}

func (materialTestResolver) Resolve(context.Context, string, string, knowledge.EmbeddingSelection) (knowledge.EmbeddingProfileSnapshot, error) {
	return knowledge.EmbeddingProfileSnapshot{}, knowledge.ErrProfileUnavailable
}
func (materialTestResolver) Catalog(context.Context, string, string) (knowledge.EmbeddingProfileCatalog, error) {
	return knowledge.EmbeddingProfileCatalog{}, nil
}

func materialImportFixture(t *testing.T, body string, configure ...func(*sql.DB, *knowledge.CreateDocumentInput)) (*sql.DB, *k12storage.Store, *usecase.MaterialPreparationWorker, string) {
	t.Helper()
	ctx := t.Context()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "material.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err = migrate.Run(ctx, db, migrate.All); err != nil {
		t.Fatal(err)
	}
	reg := scenario.NewRegistry()
	if err = reg.Assemble(k12.Pack(k12.NewCurriculumStub())); err != nil {
		t.Fatal(err)
	}
	records := k12storage.NewStore(db, reg.Records)
	repo := knowledge.NewSQLiteSemanticIndexRepository(db)
	if _, err = repo.BindLegacyDefaultCorpus(ctx, "desktop-user", "default"); err != nil {
		t.Fatal(err)
	}
	repo.SetDocumentIngestLifecycleObserver(engineadapter.NewTextbookManifestLifecycleAdapter(records))
	service := knowledge.NewSemanticIndexService(repo, materialTestResolver{})
	if err = service.ConfigureDocumentIngest(filepath.Join(t.TempDir(), "objects")); err != nil {
		t.Fatal(err)
	}
	input := knowledge.CreateDocumentInput{IdempotencyKey: "material-fixture", Filename: "exercises.md", MediaType: "text/markdown", SizeBytes: int64(len(body)), Body: strings.NewReader(body), Grade: "六年级上", Subject: "数学"}
	for _, config := range configure {
		config(db, &input)
	}
	accepted, err := service.CreateDocument(ctx, "desktop-user", "default", input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	job, claimed, err := repo.ClaimNextJobForCorpus(ctx, "desktop-user", "default", "test", now, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	source, err := repo.GetIngestDocument(ctx, "desktop-user", accepted.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	kb := knowledge.NewSQLiteStore(db)
	manager := knowledge.NewManager(kb, kb, nil, knowledge.WithSplitter(splitter.NewMarkdownSplitter()))
	prepared, err := NewKnowledgeDocumentIngestProcessor(manager).Prepare(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteIngestDocument(ctx, job.Lease(), now, prepared); err != nil {
		t.Fatal(err)
	}
	solver := engineadapter.NewSolveAdapter(engine.NewSolveSkill(func(context.Context, engine.SubAgentSpec) (engine.SubAgentResult, error) {
		t.Fatal("material arithmetic must not call model")
		return engine.SubAgentResult{}, nil
	}, nil))
	return db, records, &usecase.MaterialPreparationWorker{Records: records, Solver: solver}, accepted.DocumentID
}

func TestMaterialPreparationMarkdownPublishesVerifiedAnswersWithoutLearning(t *testing.T) {
	db, records, worker, doc := materialImportFixture(t, "# 算式练习\n1. 4.5×2=999\n2. 12÷3=\n")
	ctx := t.Context()
	for i := 0; i < 2; i++ {
		did, err := worker.RunOnce(ctx)
		if err != nil || !did {
			t.Fatalf("run %d: %v %v", i, did, err)
		}
	}
	summary, err := records.GetMaterialPreparationSummary(ctx, "desktop-user", doc)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Counts["ready"] != 2 || !summary.ExtractionComplete {
		t.Fatalf("summary: %+v", summary)
	}
	for _, item := range summary.Items {
		if item.ReferenceAnswer == "999" && strings.Contains(item.Answer, "999") {
			t.Fatalf("reference answer was trusted: %+v", item)
		}
	}
	asset, err := records.FindExactProblemAsset(ctx, "desktop-user", k12.ProblemAssetFacts{Subject: "数学", Stem: "4.5×2=", AnswerContext: map[string]string{"grade_term": "六年级上"}})
	if err != nil || !strings.Contains(asset.Answer, "9") {
		t.Fatalf("independent answer: %+v %v", asset, err)
	}
	restarted := &usecase.MaterialPreparationWorker{Records: k12storage.NewStore(db, nil), Solver: worker.Solver}
	if did, err := restarted.RunOnce(ctx); err != nil || did {
		t.Fatalf("replayed work: %v %v", did, err)
	}
	for _, table := range []string{"k12_grading_jobs", "k12_grading_item_invocations", "k12_grading_assessment_items", "k12_mistakes"} {
		var n int
		if err = db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("unexpected student side effect %s: %d %v", table, n, err)
		}
	}
	var receipts int
	if err = db.QueryRow(`SELECT COUNT(*) FROM k12_material_invocations WHERE execution_kind='local_deterministic' AND status='succeeded'`).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatalf("receipts %d %v", receipts, err)
	}
}

func TestMaterialPreparationUnknownAndSourceChangesDoNotResend(t *testing.T) {
	db, records, worker, doc := materialImportFixture(t, "1. 4.5×2=\n")
	p, err := records.NextMaterialPreparation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = records.ClaimMaterialPreparation(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if err = records.RecoverMaterialPreparations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if did, err := worker.RunOnce(t.Context()); err != nil || did {
		t.Fatalf("unknown replay: %v %v", did, err)
	}
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || summary.Counts["outcome_unknown"] != 1 {
		t.Fatalf("unknown: %+v %v", summary, err)
	}
	if _, err = db.Exec(`UPDATE kb_semantic_document_bindings SET lifecycle_state='tombstoned',deleted_at=1 WHERE document_id=?`, doc); err != nil {
		t.Fatal(err)
	}
	if _, err = records.PublishPreparedMaterialAsset(t.Context(), p.TaskID); !errors.Is(err, k12storage.ErrMaterialPreparationFenced) {
		t.Fatalf("deleted source publication: %v", err)
	}
}

func TestMaterialPreparationArticleAndMissingFigureRemainIndependent(t *testing.T) {
	db, records, worker, doc := materialImportFixture(t, "# 练习\n这是第1篇文章，为什么天空很蓝？\n1. 如图求面积\n2. 4.5×2=\n")
	did, err := worker.RunOnce(t.Context())
	if err != nil || !did {
		t.Fatalf("run: %v %v", did, err)
	}
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ExtractionComplete || summary.Counts["ready"] != 1 || len(summary.Items) != 1 || summary.State != "needs_review" {
		t.Fatalf("partial readiness: %+v", summary)
	}
	var textState string
	if err = db.QueryRow(`SELECT text_state FROM kb_semantic_document_bindings WHERE document_id=?`, doc).Scan(&textState); err != nil || textState != "ready" {
		t.Fatalf("knowledge blocked: %s %v", textState, err)
	}
}

func TestMaterialPreparationForegroundDefersUnsentWork(t *testing.T) {
	db, records, worker, _ := materialImportFixture(t, "1. 4.5×2=\n")
	if _, err := db.Exec(`INSERT INTO agents(name) VALUES('foreground')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO k12_grading_jobs(record_id,agent_name,status,dedupe_key,created_at,updated_at) VALUES('active-job','foreground','recognizing','active-job',1,1)`); err != nil {
		t.Fatal(err)
	}
	if did, err := worker.RunOnce(t.Context()); err != nil || did {
		t.Fatalf("foreground yielded: %v %v", did, err)
	}
	p, err := records.NextMaterialPreparation(t.Context())
	if err != nil || p.State != "queued" {
		t.Fatalf("unsent preserved: %+v %v", p, err)
	}
	if _, err = db.Exec(`UPDATE k12_grading_jobs SET status='completed' WHERE record_id='active-job'`); err != nil {
		t.Fatal(err)
	}
	if did, err := worker.RunOnce(t.Context()); err != nil || !did {
		t.Fatalf("foreground finished: %v %v", did, err)
	}
}

// materialControlledSolver 只替代模型及执行器边界，实际任务、冻结策略和调用回执使用 SQLite。
type materialControlledSolver struct {
	t             *testing.T
	calls         int
	verifyError   error
	invalidProof  bool
	afterGenerate func()
}

func (s *materialControlledSolver) Solve(ctx context.Context, problem, grade, constraint string) (usecase.SolveResult, error) {
	const answer = "先计算单价，再乘数量。答案：42元"
	_, err := usecase.ExecuteGradingPhysicalCall(ctx, usecase.GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveGenerate, RequestDigest: "material-generation"}, func(callCtx context.Context) (string, error) {
		s.calls++
		snapshot, ok := k12.GradingModelSnapshotFromContext(callCtx)
		if !ok || snapshot.Provider != "controlled" || snapshot.Model != "text-model" || grade != "六年级上" {
			s.t.Fatalf("unfrozen source scope: %+v %s", snapshot, grade)
		}
		if strings.Contains(problem, "999") {
			s.t.Fatal("reference answer leaked into independent generation")
		}
		if s.afterGenerate != nil {
			s.afterGenerate()
		}
		raw, _ := json.Marshal(map[string]any{"Output": answer})
		return string(raw), nil
	})
	if err != nil {
		return usecase.SolveResult{}, err
	}
	_, err = usecase.ExecuteGradingPhysicalCall(ctx, usecase.GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveVerify, RequestDigest: "material-verification"}, func(context.Context) (string, error) {
		s.calls++
		if s.verifyError != nil {
			return "", s.verifyError
		}
		return `{"Output":"VERDICT: AGREE\nCOMPUTED: 42","execution_receipt":{"run_id":"material-code-run","input_digest":"independent-verification-task","status":"success","exit_code":0,"stdout":"42","stdout_bytes":2}}`, nil
	})
	if err != nil {
		return usecase.SolveResult{}, err
	}
	digest := sha256.Sum256([]byte(answer))
	runID := "material-code-run"
	if s.invalidProof {
		runID = "unrelated-execution"
	}
	return usecase.SolveResult{Solution: answer, Evidence: usecase.SolveEvidence{Verdict: usecase.VerdictAgree, EvidenceType: usecase.EvidenceNumericExec, SolverOutputDigest: hex.EncodeToString(digest[:]), VerificationInputDigest: "independent-verification-task", VerificationRunID: runID}}, nil
}
func materialModelRoute(context.Context, k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) {
	return k12.GradingModelSnapshot{Provider: "controlled", Model: "text-model", TimeoutMS: 30000}, nil
}
func TestMaterialPreparationModelVerifiesProvidedAndMissingAnswers(t *testing.T) {
	for _, reference := range []string{"", " 答案：999元"} {
		t.Run(reference, func(t *testing.T) {
			db, records, worker, doc := materialImportFixture(t, "# 应用题\n1. 商店3本书售价18元，7本同样的书一共多少元？"+reference+"\n")
			boundary := &materialControlledSolver{t: t}
			worker.Solver = boundary
			worker.ResolveModel = materialModelRoute
			if did, err := worker.RunOnce(t.Context()); err != nil || !did {
				t.Fatalf("prepare: %v %v", did, err)
			}
			summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
			if err != nil || summary.State != "ready" || len(summary.Items) != 1 || !strings.Contains(summary.Items[0].Answer, "42元") {
				t.Fatalf("published: %+v %v", summary, err)
			}
			if boundary.calls != 2 {
				t.Fatalf("physical calls: %d", boundary.calls)
			}
			if did, err := worker.RunOnce(t.Context()); err != nil || did || boundary.calls != 2 {
				t.Fatalf("duplicate preparation: %v %v %d", did, err, boundary.calls)
			}
			var receipts, student int
			if err = db.QueryRow(`SELECT COUNT(*) FROM k12_material_invocations WHERE status='succeeded' AND execution_kind='provider'`).Scan(&receipts); err != nil || receipts != 2 {
				t.Fatalf("receipts: %d %v", receipts, err)
			}
			if err = db.QueryRow(`SELECT COUNT(*) FROM k12_grading_jobs`).Scan(&student); err != nil || student != 0 {
				t.Fatalf("student jobs: %d %v", student, err)
			}
		})
	}
}
func TestMaterialPreparationModelUnknownAndInvalidProofDoNotPublish(t *testing.T) {
	for _, mode := range []string{"timeout", "invalid-proof"} {
		t.Run(mode, func(t *testing.T) {
			db, records, worker, doc := materialImportFixture(t, "1. 商店3本书售价18元，7本同样的书一共多少元？\n")
			boundary := &materialControlledSolver{t: t}
			if mode == "timeout" {
				boundary.verifyError = context.DeadlineExceeded
			} else {
				boundary.invalidProof = true
			}
			worker.Solver = boundary
			worker.ResolveModel = materialModelRoute
			_, _ = worker.RunOnce(t.Context())
			summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
			if err != nil {
				t.Fatal(err)
			}
			want := "needs_review"
			if mode == "timeout" {
				want = "outcome_unknown"
			}
			if summary.State != want {
				t.Fatalf("expected %s, got %+v", want, summary)
			}
			if err = records.RecoverMaterialPreparations(t.Context()); err != nil {
				t.Fatal(err)
			}
			if did, err := worker.RunOnce(t.Context()); err != nil || did || boundary.calls != 2 {
				t.Fatalf("unexpected resend: %v %v calls=%d", did, err, boundary.calls)
			}
			var assets int
			if err = db.QueryRow(`SELECT COUNT(*) FROM k12_problem_assets`).Scan(&assets); err != nil || assets != 0 {
				t.Fatalf("unverified asset: %d %v", assets, err)
			}
		})
	}
}
func TestMaterialPreparationFreezesOnlyExplicitSourceChildScope(t *testing.T) {
	_, records, worker, _ := materialImportFixture(t, "1. 4.5×2=\n", func(db *sql.DB, input *knowledge.CreateDocumentInput) {
		input.Grade = ""
		input.AgentID = "source-child"
		input.LearnerID = "source-child"
		if _, err := db.Exec(`INSERT INTO agents(name,metadata) VALUES('source-child','{"k12.grade_term":"六年级上"}')`); err != nil {
			t.Fatal(err)
		}
	})
	p, err := records.NextMaterialPreparation(t.Context())
	if err != nil || p.Candidate.Facts.AnswerContext["grade_term"] != "六年级上" {
		t.Fatalf("source scope: %+v %v", p, err)
	}
	if _, err = worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = records.FindExactProblemAsset(t.Context(), "desktop-user", k12.ProblemAssetFacts{Subject: "数学", Stem: "4.5×2=", AnswerContext: map[string]string{"grade_term": "六年级上"}}); err != nil {
		t.Fatal(err)
	}
}

func TestMaterialPreparationTitleScopeAndUnknownScopeRemainDistinct(t *testing.T) {
	for _, name := range []string{"数学六年级上册.md", "练习.md"} {
		t.Run(name, func(t *testing.T) {
			_, records, _, _ := materialImportFixture(t, "1. 4.5×2=\n", func(_ *sql.DB, input *knowledge.CreateDocumentInput) { input.Grade = ""; input.Filename = name })
			p, err := records.NextMaterialPreparation(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if name == "数学六年级上册.md" {
				want = "六年级上"
			}
			if p.Candidate.Facts.AnswerContext["grade_term"] != want {
				t.Fatalf("scope: %+v", p.Candidate.Facts)
			}
		})
	}
}
func TestMaterialPreparationModelDefersOnlyUnsentVerification(t *testing.T) {
	db, records, worker, doc := materialImportFixture(t, "1. 商店3本书售价18元，7本同样的书一共多少元？\n")
	boundary := &materialControlledSolver{t: t, afterGenerate: func() {
		if _, err := db.Exec(`INSERT INTO agents(name) VALUES('foreground')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO k12_grading_jobs(record_id,agent_name,status,dedupe_key,created_at,updated_at) VALUES('foreground-job','foreground','recognizing','foreground-job',1,1)`); err != nil {
			t.Fatal(err)
		}
	}}
	worker.Solver = boundary
	worker.ResolveModel = materialModelRoute
	if _, err := worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if boundary.calls != 1 {
		t.Fatalf("verification should remain unsent: %d", boundary.calls)
	}
	if _, err := db.Exec(`UPDATE k12_grading_jobs SET status='completed'`); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || summary.State != "ready" || boundary.calls != 2 {
		t.Fatalf("reuse generation: %+v %v calls=%d", summary, err, boundary.calls)
	}
}
func TestMaterialPreparationSourceRevisionFencesInFlightModel(t *testing.T) {
	db, records, worker, _ := materialImportFixture(t, "1. 商店3本书售价18元，7本同样的书一共多少元？\n")
	boundary := &materialControlledSolver{t: t, afterGenerate: func() {
		if _, err := db.Exec(`UPDATE kb_semantic_document_bindings SET lifecycle_state='tombstoned',deleted_at=1`); err != nil {
			t.Fatal(err)
		}
	}}
	worker.Solver = boundary
	worker.ResolveModel = materialModelRoute
	if _, err := worker.RunOnce(t.Context()); !errors.Is(err, k12storage.ErrMaterialPreparationFenced) {
		t.Fatalf("source fence: %v", err)
	}
	var assets int
	var state string
	if err := db.QueryRow(`SELECT COUNT(*) FROM k12_problem_assets`).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state FROM k12_material_preparations`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if assets != 0 || state != "stopped" || boundary.calls != 1 {
		t.Fatalf("stale source: assets=%d state=%s calls=%d", assets, state, boundary.calls)
	}
	_ = records
}
