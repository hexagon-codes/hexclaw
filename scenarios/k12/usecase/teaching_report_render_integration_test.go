package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

type actualReportRenderer struct {
	service *render.Service
	calls   int
}

func (r *actualReportRenderer) Render(context.Context, string, string) ([]byte, string, error) {
	panic("unexpected legacy render")
}
func (r *actualReportRenderer) ResolveTeachingReportSpec(ctx context.Context, b render.ReportBundle) (render.ReportRenderSpec, error) {
	return r.service.ResolveReportSpec(ctx, b)
}
func (r *actualReportRenderer) RenderTeachingReport(ctx context.Context, b render.ReportBundle, i render.ReportRenderInput) ([]byte, string, error) {
	v, e := r.RenderTeachingReportResult(ctx, b, i)
	return v.Data, v.ContentType, e
}
func (r *actualReportRenderer) RenderTeachingReportResult(ctx context.Context, b render.ReportBundle, i render.ReportRenderInput) (TeachingReportRendered, error) {
	r.calls++
	v, err := r.service.RenderReport(ctx, b, i)
	if err != nil {
		if v != nil {
			return TeachingReportRendered{Spec: v.Spec, LayoutCheck: v.LayoutCheck}, err
		}
		return TeachingReportRendered{}, err
	}
	defer os.Remove(v.File.Path)
	data, err := os.ReadFile(v.File.Path)
	return TeachingReportRendered{Data: data, ContentType: v.File.ContentType, Spec: v.Spec, LayoutCheck: v.LayoutCheck}, err
}

func reportBundleFixture(t *testing.T) TeachingReportBundleSnapshot {
	t.Helper()
	dir := filepath.Join("..", "skill-bundles", "homework-pdf")
	raw, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m marketplace.BundleManifest
	if err = json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	digest, err := m.SemanticDigest()
	if err != nil {
		t.Fatal(err)
	}
	s := TeachingReportBundleSnapshot{Manifest: raw, Resources: map[string][]byte{}, Reference: render.ReportBundleRef{SkillID: m.SkillID, Version: m.Version, Digest: digest}}
	for _, f := range m.Files {
		data, err := os.ReadFile(filepath.Join(dir, f.Path))
		if err != nil {
			t.Fatal(err)
		}
		s.Resources[f.Path] = data
	}
	b, err := render.LoadReportBundleSnapshot(s.Manifest, s.Resources, digest)
	if err != nil {
		t.Fatal(err)
	}
	s.InputSchema = b.InputSchema()
	return s
}

// 真实编译产物和SQLite读回共同验证新身份；旧冻结文件不需要引擎或字体。
func TestTeachingReportActualRenderSpecSQLiteAndLegacyPDF(t *testing.T) {
	pandoc, typst := os.Getenv("HEXCLAW_TEST_PANDOC"), os.Getenv("HEXCLAW_TEST_TYPST")
	if pandoc == "" || typst == "" {
		t.Skip("actual report render engines were not selected")
	}
	for _, tool := range []string{"pdftohtml", "pdffonts"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("selected report verification requires %s", tool)
		}
	}
	ctx := context.Background()
	r, err := render.NewPandocRenderer(pandoc, typst, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := render.NewService(render.ServiceConfig{Renderer: r})
	if err != nil {
		t.Fatal(err)
	}
	renderer := &actualReportRenderer{service: s}
	d, _ := newPipeline(t, nil, nil, nil)
	d.Renderer = renderer
	report, err := BuildGradingTeachingReport(teachingReportBuildFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := TeachingReportFrozenSnapshot{Report: report, Bundle: reportBundleFixture(t)}
	req, err := prepareTeachingReportPrintRequest("mingming", "final_artifact:render-fixture:source", "Actual render identity", snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := buildPrintArtifact(req, 100)
	view, replay, err := d.PreparePrintableArtifact(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if replay || view.Artifact.ArtifactID == legacy.ArtifactID || view.Artifact.RenderSpecDigest == "" {
		t.Fatal("resolved rendering did not create its own identity")
	}
	if view.Render.LayoutCheck != "passed" || view.Render.RenderSpecDigest != view.Artifact.RenderSpecDigest {
		t.Fatalf("lost actual check/spec: %+v", view.Render)
	}
	var spec render.ReportRenderSpec
	if err = json.Unmarshal([]byte(view.Render.RenderSpecJSON), &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.EngineDigests) != 2 || spec.Fonts.ManifestDigest == "" || !spec.FontManifest.Complete {
		t.Fatalf("actual font/engine identity unresolved: %+v", spec)
	}
	embedded := false
	file := false
	for _, face := range spec.FontManifest.Faces {
		if face.Source == "typst_embedded" {
			embedded = true
			if face.FileDigest != "" {
				t.Fatal("embedded font pretended to have a font file digest")
			}
		}
		if face.Source == "font_file" && len(face.FileDigest) == 64 {
			file = true
		}
	}
	if !embedded || !file {
		t.Fatalf("expected actual host and embedded font identities: %+v", spec.FontManifest)
	}
	d.Renderer = nil
	again, err := d.GetPrintableArtifactPDF(ctx, "mingming", view.Artifact.ArtifactID)
	if err != nil || !bytes.Equal(again.Render.Payload, view.Render.Payload) || again.Render.RenderSpecJSON != view.Render.RenderSpecJSON {
		t.Fatalf("frozen PDF/spec replay depended on current renderer: %v", err)
	}
	oldReq := req
	oldReq.Title = "Existing legacy PDF"
	old := buildPrintArtifact(oldReq, 100)
	payload := []byte("%PDF-1.7\npre-spec immutable PDF")
	oldRender := view.Render
	oldRender.ArtifactID = old.ArtifactID
	oldRender.Payload = payload
	oldRender.ByteDigest = teachingReportResourceDigest(payload)
	oldRender.ByteSize = int64(len(payload))
	oldRender.RenderSpecDigest = ""
	oldRender.RenderSpecJSON = ""
	oldRender.LayoutCheck = ""
	if _, _, _, err = d.Records.FreezePrintArtifact(ctx, old, oldRender); err != nil {
		t.Fatal(err)
	}
	oldView, replay, err := d.PreparePrintableArtifact(ctx, oldReq)
	if err != nil || !replay || !bytes.Equal(oldView.Render.Payload, payload) || oldView.Artifact.ArtifactID != old.ArtifactID {
		t.Fatalf("legacy PDF changed or required renderer: %v", err)
	}
	t.Run("optional layout tool absent", func(t *testing.T) {
		d.Renderer = renderer
		t.Setenv("PATH", t.TempDir())
		request := req
		request.Title = "No layout tool"
		request.RenderSpecDigest, request.RenderSpecJSON, err = spec.Identity()
		if err != nil {
			t.Fatal(err)
		}
		input := *request.TeachingReport
		input.Spec = &spec
		request.TeachingReport = &input
		v, _, err := d.PreparePrintableArtifact(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		got, err := d.GetPrintableArtifactPDF(ctx, "mingming", v.Artifact.ArtifactID)
		if err != nil || got.Render.LayoutCheck != "not_run" || !bytes.Equal(v.Render.Payload, got.Render.Payload) {
			t.Fatalf("optional layout check was not preserved: %v", err)
		}
	})
	t.Run("failed layout never freezes a PDF", func(t *testing.T) {
		d.Renderer = renderer
		bad := snapshot
		bad.Report = report.Clone()
		bad.Report.Items[0].AnswerAnalysis.Steps[0].Text = ""
		bad.Report.Items[0].AnswerAnalysis.Steps[0].LaTeX = strings.Repeat("1+", 150) + "1"
		request, err := prepareTeachingReportPrintRequest("mingming", "final_artifact:render-overflow:source", "Overflow", bad, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.RenderSpecDigest, request.RenderSpecJSON, err = spec.Identity()
		if err != nil {
			t.Fatal(err)
		}
		input := *request.TeachingReport
		input.Spec = &spec
		request.TeachingReport = &input
		_, _, err = d.PreparePrintableArtifact(ctx, request)
		if err == nil || !strings.Contains(err.Error(), "layout_check=failed") {
			t.Fatalf("layout failure lost its state: %v", err)
		}
		if _, err = d.GetPrintableArtifactPDF(ctx, "mingming", buildPrintArtifact(request, 100).ArtifactID); err == nil {
			t.Fatal("invalid PDF was frozen")
		}
	})
	if out := os.Getenv("HEXCLAW_TEST_PDF_EVIDENCE_DIR"); out != "" {
		if err = os.MkdirAll(out, 0700); err != nil {
			t.Fatal(err)
		}
		pdf := filepath.Join(out, "actual-report-render.pdf")
		if err = os.WriteFile(pdf, view.Render.Payload, 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(out, "actual-render-spec.json"), []byte(view.Render.RenderSpecJSON), 0600); err != nil {
			t.Fatal(err)
		}
		fonts, err := exec.Command("pdffonts", pdf).CombinedOutput()
		if err != nil {
			t.Fatalf("font inspection: %v %s", err, fonts)
		}
		if !strings.Contains(string(fonts), "yes") {
			t.Fatal("PDF has no verified embedded font")
		}
		if err = os.WriteFile(filepath.Join(out, "actual-pdf-fonts.txt"), fonts, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
