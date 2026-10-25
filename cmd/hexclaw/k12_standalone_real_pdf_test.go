package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/apihttp"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assembly"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/skilladapter"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
)

type standalonePDFCandidate struct {
	Subject                string                         `json:"subject"`
	Grade                  string                         `json:"grade"`
	Problem                string                         `json:"problem"`
	VerifiedSolution       string                         `json:"verified_solution"`
	FrozenTeachingContract string                         `json:"frozen_teaching_contract"`
	Guide                  k12usecase.ParentTeachingGuide `json:"guide"`
}

// 正确多句候选只替代模型回包；输入冻结、最终化、模板资源、实际渲染和HTTP下载走正式链路。
func (c *standalonePDFCandidate) FreezeReportSolveTeachingContext(context.Context, string, string) (string, []skill.ContextReceipt, error) {
	return c.FrozenTeachingContract, nil, nil
}

func (c *standalonePDFCandidate) Solve(ctx context.Context, problem, _, _ string) (k12usecase.SolveResult, error) {
	if problem != c.Problem {
		return k12usecase.SolveResult{}, fmt.Errorf("candidate belongs to a different problem")
	}
	version, _ := k12usecase.SolveTeachingRequest(ctx)
	if version != k12usecase.SolveOutputWithTeachingReportV2 {
		return k12usecase.SolveResult{}, fmt.Errorf("teaching report contract was not selected")
	}
	sum := sha256.Sum256([]byte(c.VerifiedSolution))
	digest := hex.EncodeToString(sum[:])
	return k12usecase.SolveResult{
		Solution:   c.VerifiedSolution,
		Evidence:   k12usecase.SolveEvidence{Verdict: k12usecase.VerdictAgree, EvidenceType: k12usecase.EvidenceNone, SolverOutputDigest: digest},
		Generation: &k12usecase.SolveGeneration{OutputVersion: version, ParentGuideCandidate: &c.Guide, GuideAudit: "VALID", SourceSolutionDigest: digest},
	}, nil
}

func TestDefaultK12TeachingReportRealPDF(t *testing.T) {
	if os.Getenv("HEXCLAW_STANDALONE_REAL_PDF") != "1" {
		t.Skip("set HEXCLAW_STANDALONE_REAL_PDF=1 with explicit local tool and output paths")
	}
	pandoc, typst, output := os.Getenv("HEXCLAW_STANDALONE_PANDOC"), os.Getenv("HEXCLAW_STANDALONE_TYPST"), os.Getenv("HEXCLAW_STANDALONE_PDF_OUTPUT")
	if pandoc == "" || typst == "" || output == "" || !filepath.IsAbs(output) {
		t.Fatal("explicit pandoc, typst and absolute output directory are required")
	}
	fixturePath := filepath.Join("..", "..", "scenarios", "k12", "engineadapter", "testdata", "parent_guide_road_work_audit.json")
	rawFixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var candidate standalonePDFCandidate
	if err := json.Unmarshal(rawFixture, &candidate); err != nil || candidate.Guide.ReportContent == nil || candidate.Problem == "" {
		t.Fatalf("invalid teaching candidate: %v", err)
	}
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "real-pdf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	pandocRenderer, err := render.NewPandocRenderer(pandoc, typst, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := render.NewService(render.ServiceConfig{Renderer: pandocRenderer})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := assembly.Wire(store.DB(), &printReplyAPITool{}, assembly.WithRenderer(engineadapter.NewRenderAdapter(service)), assembly.WithTeachingReportResources(standaloneIMReportResources{}), func(d *k12usecase.Deps) {
		d.Solver, d.TextbookOwnerID = &candidate, "application-owner"
	})
	if err != nil {
		t.Fatal(err)
	}
	const sessionID, sourceID, requestID = "real-pdf-session", "real-pdf-user", "real-pdf-request"
	ctx := t.Context()
	if err := store.CreateSession(ctx, &storage.Session{ID: sessionID, UserID: "dingtalk-staff", Platform: "dingtalk", InstanceID: "original-robot", ChatID: "dingtalk-staff"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(ctx, &storage.MessageRecord{ID: sourceID, SessionID: sessionID, Role: "user", Content: candidate.Problem, RequestID: requestID}); err != nil {
		t.Fatal(err)
	}
	ctx = skill.WithAuthenticatedUser(skill.WithRoutedAgent(ctx, "default"), "dingtalk-staff")
	ctx = skill.WithInvocationContext(ctx, skill.InvocationContext{AuthenticatedUserID: "dingtalk-staff", RoutedAgentName: "default", SessionID: sessionID, SourceMessageID: sourceID, RequestID: requestID})
	result, err := skilladapter.NewGradeSkill(runtime.Deps).Execute(ctx, map[string]any{"problem": candidate.Problem, "subject": candidate.Subject, "grade": candidate.Grade})
	if err != nil {
		t.Fatal(err)
	}
	final, err := runtime.Records.GetTextSolveFinalization(ctx, "application-owner", "default", result.Metadata["text_solve_run_id"])
	if err != nil || final.SourceSessionID != sessionID || final.SourceMessageID != sourceID {
		t.Fatalf("original text solve was not finalized: %+v %v", final, err)
	}
	printable, _, err := runtime.Deps.PrepareTextSolveTeachingReportPDF(ctx, "application-owner", "default", final.RunID, final.FinalizationID, final.ResultDigest, "")
	if err != nil {
		t.Fatal(err)
	}
	handler := apihttp.NewHandler(apihttp.Runtime{Views: runtime.Registry.Views, Records: runtime.Records, Deps: runtime.Deps, PrincipalMode: "local_loopback", OwnerScope: "application-owner"})
	request := httptest.NewRequest(http.MethodGet, "/print-artifacts/"+printable.Artifact.ArtifactID+"/content?agent=default", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/pdf" || !bytes.Equal(response.Body.Bytes(), printable.Render.Payload) {
		t.Fatalf("formal download did not return the frozen PDF: status=%d content_type=%s", response.Code, response.Header().Get("Content-Type"))
	}
	digest := sha256.Sum256(response.Body.Bytes())
	if hex.EncodeToString(digest[:]) != printable.Render.ByteDigest || response.Header().Get("X-Content-SHA256") != printable.Render.ByteDigest || !strings.HasPrefix(response.Body.String(), "%PDF-") {
		t.Fatal("downloaded PDF identity differs from the frozen render")
	}
	var agents int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM agents").Scan(&agents); err != nil || agents != 0 {
		t.Fatalf("standalone PDF required an assistant: count=%d err=%v", agents, err)
	}
	if err := os.MkdirAll(output, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"road-work.pdf": response.Body.Bytes(), "source-candidate.json": rawFixture, "frozen-markdown.md": []byte(printable.Artifact.CanonicalMarkdown)} {
		if err := os.WriteFile(filepath.Join(output, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	summary, _ := json.MarshalIndent(map[string]any{"source": "correct multi-sentence road-work candidate", "route": "GradeSkill > durable finalization > production Pandoc/Typst > frozen artifact > public download handler", "agent": "default", "assistant_rows": agents, "artifact_id": printable.Artifact.ArtifactID, "source_digest": printable.Artifact.SourceDigest, "pdf_sha256": printable.Render.ByteDigest, "pdf_bytes": len(response.Body.Bytes()), "download_status": response.Code, "model": "fixture", "platform_network": "not used"}, "", "  ")
	if err := os.WriteFile(filepath.Join(output, "result.json"), append(summary, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("REAL_PDF_OK artifact=%s bytes=%d sha256=%s", printable.Artifact.ArtifactID, len(response.Body.Bytes()), printable.Render.ByteDigest)
}
