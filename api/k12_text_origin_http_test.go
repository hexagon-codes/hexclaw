package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
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
	var activeRenderer usecase.Renderer = renderer
	realOutput := ""
	if os.Getenv("HEXCLAW_GRADED_REAL_PDF") == "1" {
		pandoc, typst := os.Getenv("HEXCLAW_GRADED_PANDOC"), os.Getenv("HEXCLAW_GRADED_TYPST")
		realOutput = os.Getenv("HEXCLAW_GRADED_PDF_OUTPUT")
		if pandoc == "" || typst == "" || realOutput == "" || !filepath.IsAbs(realOutput) {
			t.Fatal("explicit pandoc, typst and absolute output directory are required")
		}
		compiler, err := render.NewPandocRenderer(pandoc, typst, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		service, err := render.NewService(render.ServiceConfig{Renderer: compiler})
		if err != nil {
			t.Fatal(err)
		}
		activeRenderer = engineadapter.NewRenderAdapter(service)
		if err := os.MkdirAll(realOutput, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := assembly.Wire(store.DB(), solver, assembly.WithTeachingReportResources(k12HTTPReportResources{}), assembly.WithRenderer(activeRenderer))
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
	var defaultGradedFinal k12.TextSolveFinalization
	var defaultGradeRef k12.TextGradeReportReference
	var defaultGradedBody string
	var defaultGradeResponse []byte
	for _, actor := range []string{"http-tutor", "http-helper", "default"} {
		for _, endpoint := range []string{"grade", "solve"} {
			t.Run(actor+"/"+endpoint, func(t *testing.T) {
				body := fmt.Sprintf(`{"agent":%q,"subject":"数学","grade":"六年级上","problem":"1+1=","student_answer":"3"}`, actor)
				rec := request(h, http.MethodPost, "/api/k12/"+endpoint, body, cfg.Server.APIToken)
				if rec.Code != http.StatusOK {
					t.Fatalf("authenticated direct HTTP must not require a tool turn: %d %s", rec.Code, rec.Body.String())
				}
				var result struct {
					Finalization   *k12.TextSolveFinalization    `json:"finalization"`
					RecordCreated  bool                          `json:"record_created"`
					Solution       string                        `json:"solution"`
					GradeReportRef *k12.TextGradeReportReference `json:"grade_report_ref"`
					TeachingReport *k12.TeachingReport           `json:"teaching_report"`
					ReportMarkdown string                        `json:"report_markdown"`
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
				if endpoint == "grade" {
					if result.GradeReportRef == nil || result.TeachingReport == nil || len(result.TeachingReport.Items) != 1 || result.TeachingReport.Items[0].Assessment.Status != k12.GradingAssessmentWrong || result.TeachingReport.Items[0].OriginalAnswer.RawText != "3" || !strings.Contains(result.ReportMarkdown, "一、快速解答") || !strings.Contains(result.ReportMarkdown, "二、深度讲解") {
						t.Fatalf("HTTP lost actual grading or the submitted answer: %s", rec.Body.String())
					}
					if actor == "default" {
						defaultGradedFinal, defaultGradeRef, defaultGradedBody = *result.Finalization, *result.GradeReportRef, result.ReportMarkdown
						defaultGradeResponse = bytes.Clone(rec.Body.Bytes())
					}
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
	gradedInput := map[string]any{"agent": "default", "solve_run_id": defaultGradedFinal.RunID, "text_finalization_id": defaultGradedFinal.FinalizationID, "text_finalization_digest": defaultGradedFinal.ResultDigest, "grade_report_ref": defaultGradeRef}
	postPrint := func(input map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		return request(h, http.MethodPost, "/api/k12/print-artifacts", string(raw), cfg.Server.APIToken)
	}
	gradedPrint := postPrint(gradedInput)
	if gradedPrint.Code != http.StatusCreated {
		t.Fatalf("actual grading reference could not print: %d %s", gradedPrint.Code, gradedPrint.Body.String())
	}
	var gradedPrintResult struct {
		Artifact struct {
			ID        string `json:"artifact_id"`
			SourceRef string `json:"source_ref"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal(gradedPrint.Body.Bytes(), &gradedPrintResult); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gradedPrintResult.Artifact.SourceRef, ":grade:"+defaultGradeRef.InvocationID+":"+defaultGradeRef.ResultDigest) || gradedPrintResult.Artifact.ID == printResult.Artifact.ID {
		t.Fatalf("graded PDF lost exact assessment identity: %s", gradedPrint.Body.String())
	}
	gradedPDF, err := runtime.Deps.GetPrintableArtifactPDF(t.Context(), "default", gradedPrintResult.Artifact.ID)
	if err != nil || gradedPDF.Artifact.CanonicalMarkdown != defaultGradedBody {
		t.Fatalf("HTTP PDF and graded body differ: %v", err)
	}
	// 显式启用时，沿同一正式HTTP链路保存真实编译和下载产物。
	if realOutput != "" {
		saveRealPDF := func(name, answer string, gradeResponse []byte, input map[string]any, prepared *httptest.ResponseRecorder, expected k12.GradingAssessmentStatus) {
			t.Helper()
			var identity struct {
				Artifact struct {
					ID string `json:"artifact_id"`
				} `json:"artifact"`
			}
			if err := json.Unmarshal(prepared.Body.Bytes(), &identity); err != nil {
				t.Fatal(err)
			}
			frozen, err := runtime.Deps.GetPrintableArtifactPDF(t.Context(), "default", identity.Artifact.ID)
			if err != nil {
				t.Fatal(err)
			}
			download := request(h, http.MethodGet, "/api/k12/print-artifacts/"+identity.Artifact.ID+"/content?agent=default", "", cfg.Server.APIToken)
			if download.Code != http.StatusOK || download.Header().Get("Content-Type") != "application/pdf" || !bytes.Equal(download.Body.Bytes(), frozen.Render.Payload) {
				t.Fatalf("real grading PDF download changed the frozen result: %d", download.Code)
			}
			digest := sha256.Sum256(download.Body.Bytes())
			if hex.EncodeToString(digest[:]) != frozen.Render.ByteDigest || download.Header().Get("X-Content-SHA256") != frozen.Render.ByteDigest {
				t.Fatal("real grading PDF download digest does not match")
			}
			var result struct {
				Report *k12.TeachingReport           `json:"teaching_report"`
				Ref    *k12.TextGradeReportReference `json:"grade_report_ref"`
			}
			if err := json.Unmarshal(gradeResponse, &result); err != nil || result.Report == nil || result.Ref == nil || len(result.Report.Items) != 1 || result.Report.Items[0].Assessment.Status != expected || result.Report.Items[0].OriginalAnswer.RawText != answer {
				t.Fatalf("real grading result has incorrect facts: %v", err)
			}
			prepareInput, err := json.MarshalIndent(input, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			gradeInput, err := json.MarshalIndent(map[string]string{"agent": "default", "subject": "数学", "grade": "六年级上", "problem": "1+1=", "student_answer": answer}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			summary, err := json.MarshalIndent(map[string]any{"grade_status": http.StatusOK, "prepare_status": prepared.Code, "download_status": download.Code, "download_headers": download.Header(), "assessment_status": expected, "grade_report_ref": result.Ref, "artifact_id": frozen.Artifact.ArtifactID, "source_ref": frozen.Artifact.SourceRef, "source_digest": frozen.Artifact.SourceDigest, "pdf_sha256": frozen.Render.ByteDigest, "pdf_bytes": len(download.Body.Bytes()), "model_calls": providerCalls}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			for suffix, content := range map[string][]byte{".pdf": download.Body.Bytes(), "-grade-request.json": gradeInput, "-grade-response.json": gradeResponse, "-prepare-request.json": prepareInput, "-prepare-response.json": prepared.Body.Bytes(), "-result.json": summary, "-frozen-markdown.md": []byte(frozen.Artifact.CanonicalMarkdown)} {
				if err := os.WriteFile(filepath.Join(realOutput, name+suffix), content, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("REAL_GRADED_PDF_OK case=%s artifact=%s bytes=%d sha256=%s", name, frozen.Artifact.ArtifactID, len(download.Body.Bytes()), frozen.Render.ByteDigest)
		}
		saveRealPDF("wrong", "3", defaultGradeResponse, gradedInput, gradedPrint, k12.GradingAssessmentWrong)
		t.Run("real_pdf_correct", func(t *testing.T) {
			graded := request(h, http.MethodPost, "/api/k12/grade", `{"agent":"default","subject":"数学","grade":"六年级上","problem":"1+1=","student_answer":"2"}`, cfg.Server.APIToken)
			if graded.Code != http.StatusOK {
				t.Fatalf("real correct grading failed: %d %s", graded.Code, graded.Body.String())
			}
			var correct struct {
				Final *k12.TextSolveFinalization    `json:"finalization"`
				Ref   *k12.TextGradeReportReference `json:"grade_report_ref"`
			}
			if err := json.Unmarshal(graded.Body.Bytes(), &correct); err != nil || correct.Final == nil || correct.Ref == nil {
				t.Fatalf("real correct grading identity missing: %v", err)
			}
			input := map[string]any{"agent": "default", "solve_run_id": correct.Final.RunID, "text_finalization_id": correct.Final.FinalizationID, "text_finalization_digest": correct.Final.ResultDigest, "grade_report_ref": correct.Ref}
			prepared := postPrint(input)
			if prepared.Code != http.StatusCreated {
				t.Fatalf("real correct grading PDF failed: %d %s", prepared.Code, prepared.Body.String())
			}
			saveRealPDF("correct", "2", graded.Body.Bytes(), input, prepared, k12.GradingAssessmentCorrect)
		})
	}
	beforeRejected := renderer.calls
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		status int
	}{
		{"incomplete", func(v map[string]any) {
			v["grade_report_ref"] = map[string]string{"invocation_id": defaultGradeRef.InvocationID}
		}, http.StatusBadRequest},
		{"mixed_source", func(v map[string]any) { v["canonical_markdown"] = "other report" }, http.StatusBadRequest},
		{"wrong_digest", func(v map[string]any) {
			v["grade_report_ref"] = k12.TextGradeReportReference{InvocationID: defaultGradeRef.InvocationID, ResultDigest: "sha256:wrong"}
		}, http.StatusConflict},
		{"different_run", func(v map[string]any) {
			v["solve_run_id"], v["text_finalization_id"], v["text_finalization_digest"] = defaultFinal.RunID, defaultFinal.FinalizationID, defaultFinal.ResultDigest
		}, http.StatusConflict},
	} {
		t.Run("graded_print/"+tc.name, func(t *testing.T) {
			input := make(map[string]any, len(gradedInput))
			for key, value := range gradedInput {
				input[key] = value
			}
			tc.change(input)
			if rec := postPrint(input); rec.Code != tc.status {
				t.Fatalf("invalid grading reference accepted or changed error contract: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
	if renderer.calls != beforeRejected {
		t.Fatal("invalid grading reference rendered a replacement PDF")
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
