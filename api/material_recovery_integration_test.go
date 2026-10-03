package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/apihttp"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type materialRecoverySolver struct {
	base         *materialControlledSolver
	extraCalls   int
	alterRequest bool
	afterSolve   func()
	beforeBase   func()
	weakEvidence bool
}

func (s *materialRecoverySolver) Solve(ctx context.Context, problem, grade, constraint string) (usecase.SolveResult, error) {
	request := "material-second-method"
	if s.alterRequest {
		request = "unauthorized-new-method"
	}
	_, err := usecase.ExecuteGradingPhysicalCall(ctx, usecase.GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveGenerate, RequestDigest: request}, func(context.Context) (string, error) {
		s.extraCalls++
		return `{"Output":"用重复相加，答案：42元"}`, nil
	})
	if err != nil {
		return usecase.SolveResult{}, err
	}
	if s.beforeBase != nil {
		s.beforeBase()
	}
	result, err := s.base.Solve(ctx, problem, grade, constraint)
	if err == nil && s.afterSolve != nil {
		s.afterSolve()
	}
	if s.weakEvidence {
		result.Evidence.EvidenceType = usecase.EvidenceHeuristic
	}
	return result, err
}

type materialRecoveryFixture struct {
	db          *sql.DB
	records     *k12storage.Store
	worker      *usecase.MaterialPreparationWorker
	solver      *materialRecoverySolver
	doc, task   string
	visualCalls *int
}

func newMaterialRecoveryFixture(t *testing.T) materialRecoveryFixture {
	t.Helper()
	pixels := materialVisualPNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(pixels) }))
	defer server.Close()
	db, records, worker, doc := materialImportFixture(t, "1. 根据图示，买7本同样的书多少钱？\n![原图]("+server.URL+"/figure.png)\n")
	visual := new(int)
	worker.ResolveVisualModel = materialModelRoute
	worker.ResolveModel = materialModelRoute
	worker.ReadVisual = func(context.Context, []byte, string) (string, error) { *visual++; return materialVisualReading, nil }
	solver := &materialRecoverySolver{base: &materialControlledSolver{t: t, verifyError: context.DeadlineExceeded}}
	worker.Solver = solver
	if did, err := worker.RunOnce(t.Context()); !did || !errors.Is(err, k12storage.ErrMaterialPreparationUnknown) {
		t.Fatalf("initial unknown did=%v err=%v", did, err)
	}
	var task string
	if err := db.QueryRow(`SELECT task_id FROM k12_material_preparations`).Scan(&task); err != nil {
		t.Fatal(err)
	}
	return materialRecoveryFixture{db, records, worker, solver, doc, task, visual}
}
func recoveryHTTP(t *testing.T, h http.Handler, method, path, key string, body any) (int, []byte) {
	t.Helper()
	var data string
	if body != nil {
		raw, _ := json.Marshal(body)
		data = string(raw)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(data))
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}
func TestMaterialRecoveryOriginalTaskReusesAllSuccessAndPublishes(t *testing.T) {
	f := newMaterialRecoveryFixture(t)
	h := apihttp.NewHandler(apihttp.Runtime{Records: f.records})
	path := "/materials/" + f.doc + "/preparation/" + f.task + "/recovery"
	status, raw := recoveryHTTP(t, h, http.MethodGet, path, "", nil)
	if status != 200 {
		t.Fatalf("plan status=%d %s", status, raw)
	}
	var plan k12storage.MaterialRecoveryPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Reusable) != 3 || plan.Unknown.Operation != "solve_verify" || !plan.MayDuplicateCharge {
		t.Fatalf("plan=%+v", plan)
	}
	var old string
	if err := f.db.QueryRow(`SELECT json_array(invocation_id,task_id,operation,request_digest,execution_kind,status,result_json,result_digest,created_at,updated_at,attempt) FROM k12_material_invocations WHERE invocation_id=?`, plan.Unknown.InvocationID).Scan(&old); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"fingerprint": plan.Fingerprint, "allow_possible_duplicate_charge": true}
	if status, _ = recoveryHTTP(t, h, http.MethodPost, path, "same-command", map[string]any{"fingerprint": plan.Fingerprint}); status != 400 {
		t.Fatalf("missing acknowledgement=%d", status)
	}
	if status, raw = recoveryHTTP(t, h, http.MethodPost, path, "same-command", body); status != 202 {
		t.Fatalf("recovery=%d %s", status, raw)
	}
	var accepted k12storage.MaterialRecoveryResult
	_ = json.Unmarshal(raw, &accepted)
	// 两个有界重复命令复用同一决定，不创建第二个替代请求。
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.records.RecoverMaterialPreparation(t.Context(), "desktop-user", f.doc, f.task, "same-command", plan.Fingerprint, true)
			if e != nil || r.DecisionID != accepted.DecisionID {
				t.Errorf("duplicate command %+v %v", r, e)
			}
		}()
	}
	wg.Wait()
	f.solver.base.verifyError = nil
	if did, err := f.worker.RunOnce(t.Context()); !did || err != nil {
		t.Fatalf("recovered did=%v err=%v", did, err)
	}
	summary, err := f.records.GetMaterialPreparationSummary(t.Context(), "desktop-user", f.doc)
	if err != nil || summary.Counts["ready"] != 1 {
		t.Fatalf("published=%+v err=%v", summary, err)
	}
	if *f.visualCalls != 1 || f.solver.extraCalls != 1 || f.solver.base.calls != 3 {
		t.Fatalf("successful calls resent: visual=%d second=%d solve=%d", *f.visualCalls, f.solver.extraCalls, f.solver.base.calls)
	}
	var after string
	if err = f.db.QueryRow(`SELECT json_array(invocation_id,task_id,operation,request_digest,execution_kind,status,result_json,result_digest,created_at,updated_at,attempt) FROM k12_material_invocations WHERE invocation_id=?`, plan.Unknown.InvocationID).Scan(&after); err != nil || after != old {
		t.Fatalf("old unknown changed %s %v", after, err)
	}
	for query, want := range map[string]int{`SELECT COUNT(*) FROM k12_material_invocations`: 5, `SELECT COUNT(*) FROM k12_material_invocations WHERE operation='solve_verify' AND attempt=1 AND status='succeeded'`: 1, `SELECT COUNT(*) FROM k12_material_recovery_decisions`: 1, `SELECT COUNT(*) FROM k12_grading_jobs`: 0, `SELECT COUNT(*) FROM k12_grading_assessment_items`: 0, `SELECT COUNT(*) FROM k12_problem_asset_publications`: 1} {
		var n int
		if err = f.db.QueryRow(query).Scan(&n); err != nil || n != want {
			t.Fatalf("%s: %d want%d %v", query, n, want, err)
		}
	}
	if status, raw = recoveryHTTP(t, h, http.MethodPost, path, "same-command", body); status != 202 || !strings.Contains(string(raw), "published") {
		t.Fatalf("completed replay=%d %s", status, raw)
	}
	if did, err := f.worker.RunOnce(t.Context()); did || err != nil {
		t.Fatalf("published was replayed=%v %v", did, err)
	}
}
func TestMaterialRecoveryStopsNewUnknownAndRefusesDrift(t *testing.T) {
	for _, mode := range []string{"unknown", "new-request", "source-before-confirm", "input-before-confirm", "model-before-confirm", "source-after-confirm", "model-after-confirm", "restart-before-send", "restart-after-success", "foreground-before-send"} {
		t.Run(mode, func(t *testing.T) {
			f := newMaterialRecoveryFixture(t)
			plan, err := f.records.MaterialRecoveryPlan(t.Context(), "desktop-user", f.doc, f.task)
			if err != nil {
				t.Fatal(err)
			}
			change := func(sql string) {
				t.Helper()
				if _, e := f.db.Exec(sql); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "source-before-confirm" {
				change(`UPDATE kb_semantic_document_bindings SET lifecycle_state='tombstoned',deleted_at=1`)
			}
			if mode == "input-before-confirm" {
				change(`UPDATE k12_material_preparations SET input_digest='changed'`)
			}
			if mode == "model-before-confirm" {
				change(`UPDATE k12_material_preparations SET policy=json_set(policy,'$.model.model','changed')`)
			}
			_, err = f.records.RecoverMaterialPreparation(t.Context(), "desktop-user", f.doc, f.task, "recovery-1", plan.Fingerprint, true)
			if strings.HasSuffix(mode, "before-confirm") {
				if err == nil {
					t.Fatal("drift was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			f.solver.base.verifyError = nil
			switch mode {
			case "unknown":
				f.solver.base.verifyError = context.DeadlineExceeded
			case "new-request":
				f.solver.alterRequest = true
			case "source-after-confirm":
				change(`UPDATE kb_semantic_document_bindings SET lifecycle_state='tombstoned',deleted_at=1`)
			case "model-after-confirm":
				change(`UPDATE k12_material_preparations SET policy=json_set(policy,'$.model.model','changed')`)
			case "restart-after-success":
				f.solver.afterSolve = func() { panic("simulated exit after durable verification") }
			case "foreground-before-send":
				f.solver.beforeBase = func() {
					change(`INSERT INTO agents(name) VALUES('foreground')`)
					change(`INSERT INTO k12_grading_jobs(record_id,agent_name,status,dedupe_key,created_at,updated_at) VALUES('foreground-job','foreground','recognizing','foreground-job',1,1)`)
				}
			case "restart-before-send":
				pending, e := f.records.NextMaterialPreparation(t.Context())
				if e != nil {
					t.Fatal(e)
				}
				if e = f.records.ClaimMaterialPreparation(t.Context(), pending); e != nil {
					t.Fatal(e)
				}
				if e = f.records.RecoverMaterialPreparations(t.Context()); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "restart-after-success" {
				func() {
					defer func() {
						if r := recover(); r != "simulated exit after durable verification" {
							t.Fatalf("unexpected interrupted state: %v", r)
						}
					}()
					_, _ = f.worker.RunOnce(t.Context())
				}()
				f.solver.afterSolve = nil
				if e := f.records.RecoverMaterialPreparations(t.Context()); e != nil {
					t.Fatal(e)
				}
			}
			_, _ = f.worker.RunOnce(t.Context())
			if mode == "foreground-before-send" {
				var state string
				if e := f.db.QueryRow(`SELECT state FROM k12_material_preparations WHERE task_id=?`, f.task).Scan(&state); e != nil || state != "queued" || f.solver.base.calls != 2 {
					t.Fatalf("unsent recovery did not defer: %s calls=%d %v", state, f.solver.base.calls, e)
				}
				f.solver.beforeBase = nil
				change(`UPDATE k12_grading_jobs SET status='completed'`)
				if _, e := f.worker.RunOnce(t.Context()); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "restart-before-send" || mode == "restart-after-success" || mode == "foreground-before-send" {
				summary, e := f.records.GetMaterialPreparationSummary(t.Context(), "desktop-user", f.doc)
				if e != nil || summary.Counts["ready"] != 1 || f.solver.base.calls != 3 || *f.visualCalls != 1 || f.solver.extraCalls != 1 {
					t.Fatalf("unsent recovery did not resume %+v %v", summary, e)
				}
				return
			}
			want := 2
			if mode == "unknown" {
				want = 3
			}
			if f.solver.base.calls != want || *f.visualCalls != 1 || f.solver.extraCalls != 1 {
				t.Fatalf("unexpected resend base=%d visual=%d second=%d", f.solver.base.calls, *f.visualCalls, f.solver.extraCalls)
			}
			if err = f.records.RecoverMaterialPreparations(t.Context()); err != nil {
				t.Fatal(err)
			}
			before := f.solver.base.calls
			_, _ = f.worker.RunOnce(t.Context())
			if f.solver.base.calls != before {
				t.Fatal("unknown restarted a call")
			}
			var n int
			if err = f.db.QueryRow(`SELECT COUNT(*) FROM k12_problem_asset_publications`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("invalid recovery published %d %v", n, err)
			}
		})
	}
}

func TestMaterialRecoveryReassessesSavedSuccessfulVerificationWithoutSending(t *testing.T) {
	f := newMaterialRecoveryFixture(t)
	plan, err := f.records.MaterialRecoveryPlan(t.Context(), "desktop-user", f.doc, f.task)
	if err != nil {
		t.Fatal(err)
	}
	h := apihttp.NewHandler(apihttp.Runtime{Records: f.records})
	path := "/materials/" + f.doc + "/preparation/" + f.task + "/recovery"
	body := map[string]any{"fingerprint": plan.Fingerprint, "allow_possible_duplicate_charge": true}
	if status, raw := recoveryHTTP(t, h, http.MethodPost, path, "original-recovery", body); status != 202 {
		t.Fatalf("recovery=%d %s", status, raw)
	}
	f.solver.base.verifyError = nil
	f.solver.weakEvidence = true
	if _, err = f.worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := f.records.GetMaterialPreparationSummary(t.Context(), "desktop-user", f.doc)
	if err != nil || before.Counts["needs_review"] != 1 {
		t.Fatalf("expected saved verification review: %+v %v", before, err)
	}
	snapshot := func() string {
		var result string
		if e := f.db.QueryRow(`SELECT json_group_array(json_array(invocation_id,status,result_json,result_digest,attempt,created_at,updated_at)) FROM (SELECT * FROM k12_material_invocations ORDER BY invocation_id)`).Scan(&result); e != nil {
			t.Fatal(e)
		}
		return result
	}
	receipts := snapshot()
	calls := f.solver.base.calls
	f.solver.weakEvidence = false
	if status, raw := recoveryHTTP(t, h, http.MethodPost, path, "original-recovery", body); status != 202 || !strings.Contains(string(raw), "queued") {
		t.Fatalf("local reassessment=%d %s", status, raw)
	}
	if _, err = f.worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := f.records.GetMaterialPreparationSummary(t.Context(), "desktop-user", f.doc)
	if err != nil || after.Counts["ready"] != 1 {
		t.Fatalf("saved successful verification did not publish: %+v %v", after, err)
	}
	if f.solver.base.calls != calls || f.solver.extraCalls != 1 || *f.visualCalls != 1 || snapshot() != receipts {
		t.Fatal("local reassessment changed or resent a receipt")
	}
	var count int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM k12_material_recovery_decisions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("reassessment added authorization: %d %v", count, err)
	}
}
