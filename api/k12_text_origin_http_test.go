package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/apihttp"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assembly"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

type k12HTTPReportResources struct{}

func (k12HTTPReportResources) ReadResource(name, path string) (marketplace.Resource, error) {
	fs := k12.BundledSkillsFS()
	manifestRaw, err := fs.ReadFile("skill-bundles/" + name + "/bundle.json")
	if err != nil {
		return marketplace.Resource{}, err
	}
	var manifest marketplace.BundleManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return marketplace.Resource{}, err
	}
	raw, err := fs.ReadFile("skill-bundles/" + name + "/" + path)
	digest := sha256.Sum256(raw)
	return marketplace.Resource{SkillID: name, Version: manifest.Version, Path: path, Content: raw, Size: int64(len(raw)), SHA256: hex.EncodeToString(digest[:])}, err
}

type k12HTTPReportRenderer struct{ calls int }

func (*k12HTTPReportRenderer) Render(context.Context, string, string) ([]byte, string, error) {
	return nil, "", fmt.Errorf("unexpected generic render")
}
func (r *k12HTTPReportRenderer) RenderTeachingReport(_ context.Context, _ render.ReportBundle, input render.ReportRenderInput) ([]byte, string, error) {
	r.calls++
	return append([]byte("%PDF-1.7\n"), input.Document...), "application/pdf", nil
}

// 真实Bearer中间件、Agent注册API、K12装配和SQLite，模型边界遇到调用即失败。
func TestK12AuthenticatedDirectHTTPTextOriginAndStandaloneScope(t *testing.T) {
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "direct-text.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	agents := router.NewSQLiteStore(store.DB())
	if err := agents.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	solver := engine.NewSolveSkill(func(context.Context, engine.SubAgentSpec) (engine.SubAgentResult, error) {
		providerCalls++
		return engine.SubAgentResult{}, fmt.Errorf("unexpected provider invocation")
	}, nil)
	renderer := &k12HTTPReportRenderer{}
	runtime, err := assembly.Wire(store.DB(), solver, assembly.WithTeachingReportResources(k12HTTPReportResources{}), assembly.WithRenderer(renderer))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Server.APIToken = "k12-direct-http-fixture"
	newHandler := func(owner string) http.Handler {
		srv := &Server{cfg: cfg, store: store, agentRouter: router.New(), agentStore: agents}
		srv.SetAgentMetadataGuard(func(meta map[string]string) error { return k12.ValidateProfileGradeTerm(meta[k12.MetaKeyGradeTerm]) })
		rt := apihttp.Runtime{Views: runtime.Registry.Views, Records: runtime.Records, Deps: runtime.Deps, PrincipalMode: "remote",
			AuthenticatedOwnerScope: func(ctx context.Context) (string, error) {
				if skill.AuthenticatedUserID(ctx) != "api-user" {
					return "", fmt.Errorf("authenticated service principal required")
				}
				return owner, nil
			},
			AuthorizeAgentScope: func(ctx context.Context, actualOwner, agent string) error {
				var allowed bool
				err := store.DB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE name=? AND json_extract(metadata,'$.scenario')='k12-tutor')`, agent).Scan(&allowed)
				if err != nil {
					return err
				}
				if !allowed || actualOwner != usecase.DefaultLocalOwnerScope {
					return fmt.Errorf("agent owner mismatch")
				}
				return nil
			},
			AuthorizeSessionScope: func(ctx context.Context, _, session string) error {
				var allowed bool
				err := store.DB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id=? AND user_id=?)`, session, skill.AuthenticatedUserID(ctx)).Scan(&allowed)
				if err != nil {
					return err
				}
				if !allowed {
					return fmt.Errorf("session owner mismatch")
				}
				return nil
			},
		}
		srv.Mount("/api/k12", apihttp.NewHandler(rt))
		return srv.routes()
	}
	h := newHandler(usecase.DefaultLocalOwnerScope)
	request := func(handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "192.0.2.10:1234"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	for _, body := range []string{
		`{"name":"http-tutor","display_name":"验收辅导","metadata":{"scenario":"k12-tutor","k12.child_name":"验收学生","k12.grade_term":"六年级上"},"curriculum_progress":null}`,
		`{"name":"http-helper","display_name":"普通助手"}`,
	} {
		if rec := request(h, http.MethodPost, "/api/v1/agents", body, cfg.Server.APIToken); rec.Code != http.StatusOK {
			t.Fatalf("normal Agent registration failed: %d %s", rec.Code, rec.Body.String())
		}
	}
	var defaultFinal k12.TextSolveFinalization
	for _, actor := range []string{"http-tutor", "http-helper", "default"} {
		for _, endpoint := range []string{"grade", "solve"} {
			t.Run(actor+"/"+endpoint, func(t *testing.T) {
				body := fmt.Sprintf(`{"agent":%q,"subject":"数学","grade":"六年级上","problem":"1+1=","student_answer":"3"}`, actor)
				rec := request(h, http.MethodPost, "/api/k12/"+endpoint, body, cfg.Server.APIToken)
				if rec.Code != http.StatusOK {
					t.Fatalf("authenticated direct HTTP must not require a tool turn: %d %s", rec.Code, rec.Body.String())
				}
				var result struct {
					Finalization  *k12.TextSolveFinalization `json:"finalization"`
					RecordCreated bool                       `json:"record_created"`
					Solution      string                     `json:"solution"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Finalization == nil || result.Finalization.OwnerID != usecase.DefaultLocalOwnerScope || result.Finalization.SourceSessionID != "" || result.Finalization.SourceMessageID != "" || result.Solution == "" {
					t.Fatalf("direct HTTP source was missing or invented a conversation: %s", rec.Body.String())
				}
				if actor != "http-tutor" && result.RecordCreated {
					t.Fatal("standalone assessment created a student record")
				}
				if endpoint == "grade" && actor == "http-tutor" && !result.RecordCreated {
					t.Fatal("registered learner lost the normal mistake projection")
				}
				if read := request(h, http.MethodGet, "/api/k12/text-solves/"+result.Finalization.RunID+"?agent="+actor, "", cfg.Server.APIToken); read.Code != http.StatusOK {
					t.Fatalf("own result cannot be read: %d %s", read.Code, read.Body.String())
				}
				if actor == "default" && endpoint == "solve" {
					defaultFinal = *result.Finalization
				}
			})
		}
	}
	if defaultFinal.FinalizationID == "" {
		return
	}
	printBody, _ := json.Marshal(map[string]string{"agent": "default", "solve_run_id": defaultFinal.RunID, "text_finalization_id": defaultFinal.FinalizationID, "text_finalization_digest": defaultFinal.ResultDigest})
	printed := request(h, http.MethodPost, "/api/k12/print-artifacts", string(printBody), cfg.Server.APIToken)
	if printed.Code != http.StatusCreated {
		t.Fatalf("standalone finalized source could not print: %d %s", printed.Code, printed.Body.String())
	}
	var printResult struct {
		Artifact struct {
			ID string `json:"artifact_id"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal(printed.Body.Bytes(), &printResult); err != nil {
		t.Fatal(err)
	}
	path := "/api/k12/print-artifacts/" + printResult.Artifact.ID + "/content?agent=default"
	if read := request(h, http.MethodGet, path, "", cfg.Server.APIToken); read.Code != http.StatusOK || !strings.HasPrefix(read.Body.String(), "%PDF-") {
		t.Fatalf("own PDF cannot be read: %d", read.Code)
	}
	other := newHandler("different-owner")
	for _, path := range []string{path, "/api/k12/text-solves/" + defaultFinal.RunID + "?agent=default"} {
		if denied := request(other, http.MethodGet, path, "", cfg.Server.APIToken); denied.Code != http.StatusNotFound {
			t.Fatalf("another owner read a text result: %d %s", denied.Code, denied.Body.String())
		}
	}
	if denied := request(h, http.MethodPost, "/api/k12/grade", `{"agent":"default","grade":"六年级上","problem":"1+1=","student_answer":"3","source_session":"not-owned"}`, cfg.Server.APIToken); denied.Code != http.StatusNotFound {
		t.Fatalf("unowned conversation accepted: %d", denied.Code)
	}
	if denied := request(h, http.MethodPost, "/api/k12/grade", `{"agent":"default","grade":"六年级上","problem":"1+1="}`, ""); denied.Code != http.StatusUnauthorized {
		t.Fatalf("missing authentication accepted: %d", denied.Code)
	}
	for _, query := range []string{`SELECT COUNT(*) FROM agents WHERE name='default'`, `SELECT COUNT(*) FROM sessions`, `SELECT COUNT(*) FROM messages`, `SELECT COUNT(*) FROM k12_mistakes WHERE agent_name!='http-tutor'`} {
		var count int
		if err := store.DB().QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("direct actor created unrelated student/conversation state: %s count=%d err=%v", query, count, err)
		}
	}
	if providerCalls != 0 {
		t.Fatalf("unexpected model calls=%d", providerCalls)
	}
	origin := k12.TextSolveOrigin{OwnerID: usecase.DefaultLocalOwnerScope, RunID: "untrusted-tool-turn", InputRevision: 1}
	toolCtx := skill.WithInvocationContext(skill.WithAuthenticatedUser(t.Context(), "api-user"), skill.InvocationContext{RoutedAgentName: "http-tutor", RequestID: "missing-original-message"})
	toolCtx = usecase.WithAuthorizedTextSolveOrigin(toolCtx, origin, "http-tutor")
	if got, err := runtime.Deps.SolveTextProblem(toolCtx, origin, usecase.GradeRequest{AgentName: "http-tutor", Subject: "数学", Grade: "六年级上", Problem: "1+1="}); !errors.Is(err, k12storage.ErrTextSolveConflict) || got.Finalization != nil {
		t.Fatalf("direct HTTP scope bypassed an actual tool turn's source check: %v", err)
	}
}
