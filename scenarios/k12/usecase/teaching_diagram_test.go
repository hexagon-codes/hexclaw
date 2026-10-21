package usecase

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type frozenDiagramRenderer struct {
	calls int
	input string
}

func TestTeachingDiagramUsesScopedSourceAndCompleteAnswer(t *testing.T) {
	q := RecognizedQuestion{ProblemID: "scoped-disk", ConfirmedVersion: 1, InputDigest: strings.Repeat("a", 64), Subject: "数学", Question: "先求一份，再求总容量。",
		ImageSourceContext: &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1, Materials: []k12.RecognitionImageScopeDependencyV1{{Text: `一个硬盘，已用空间是总容量的\\(\\frac{2}{5}\\)，正好用厂4.8 TB。`}}}}
	evidence := SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec, SolverOutputDigest: strings.Repeat("b", 64)}
	ctx, candidate, ok := teachingDiagramSourceContext(q, "每份2.4 TB；总容量12 TB。", evidence)
	if !ok || ctx.Quantities["whole"].Value != "12" || BuildTeachingDiagram(ctx, candidate).Status != "ready" {
		t.Fatal("scoped source or a multi-part reference answer lost the verified diagram")
	}
	if _, _, ok := teachingDiagramSourceContext(q, "每份2.4 GB；总容量12 GB。", evidence); ok {
		t.Fatal("a diagram accepted a different answer unit")
	}
	q.Question = "家到学校多少米？"
	q.ImageSourceContext.Materials[0].Text = "妈妈走完全程要走10分钟，红红走完全程要走15分钟，两人同时从两端相向而行，5分钟后两人相距200米。"
	ctx, candidate, ok = teachingDiagramSourceContext(q, "妈妈走600米，红红走400米，余200米，全程1200米。", evidence)
	if !ok || ctx.Quantities["whole"].Value != "1200" || BuildTeachingDiagram(ctx, candidate).Status != "ready" {
		t.Fatalf("journey components were mistaken for the whole route: supported=%t context=%+v result=%+v", ok, ctx, BuildTeachingDiagram(ctx, candidate))
	}
}

func (r *frozenDiagramRenderer) Render(_ context.Context, markdown, _ string) ([]byte, string, error) {
	r.calls++
	r.input = markdown
	return []byte("%PDF-1.7\nfixture for artifact replay only"), "application/pdf", nil
}

// 验证来源绑定、真实资产/SQLite、首次内联和重放边界；实际PDF几何由文件渲染另验。
func TestTeachingDiagramFrozenSourceAndPDFReplay(t *testing.T) {
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	d, store := newPipeline(t, fakeSolver{}, fakeGrader{}, nil)
	var photo bytes.Buffer
	if err := png.Encode(&photo, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	ready, err := (&PageAssetRepository{Records: store}).Persist(t.Context(), "owner", "mingming", photo.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	q := RecognizedQuestion{ProblemID: "source-problem", PageAssetID: ready.Metadata.PageAssetID, ConfirmedVersion: 1,
		InputDigest: strings.Repeat("a", 64), Subject: "数学", AnswerState: AnswerStateUnclear,
		Question: "一个硬盘，已用空间是总容量的2/5，正好用了4.8 TB。这个硬盘的总容量是多少太字节？"}
	evidence := SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec, SolverOutputDigest: strings.Repeat("b", 64)}
	guide := ParentTeachingGuide{Answer: "12 TB", FullSolutionSteps: []string{"先求1份：4.8÷2=2.4（TB）。", "再求总量：2.4×5=12（TB）。"}}
	item := PhotoGradeItem{ParentGuide: &guide, Solve: SolveHomeworkResult{Solution: "12 TB", Evidence: evidence}, Status: PhotoAnswerUnclear}
	attached := d.attachPhotoTeachingDiagram(t.Context(), "mingming", q, item)
	if len(attached.ParentGuide.TeachingDiagrams) != 1 {
		t.Fatal("clear source and verified answer produced no diagram")
	}
	asset := attached.ParentGuide.TeachingDiagrams[0]
	if asset.ProblemID != q.ProblemID || asset.ProblemInputDigest != q.InputDigest || asset.SolutionDigest != evidence.SolverOutputDigest || q.AnswerState != AnswerStateUnclear {
		t.Fatal("diagram changed or lost source and observation identity")
	}
	data, err := store.GetTeachingDiagramAsset(t.Context(), "owner", "mingming", asset.AssetID)
	if err != nil || !bytes.Contains(data, []byte("4.8 TB")) || !bytes.Contains(data, []byte("12 TB")) || diagramSHA(data) != asset.AssetDigest {
		t.Fatalf("frozen asset differs: %v", err)
	}
	if _, err := store.GetTeachingDiagramAsset(t.Context(), "other-owner", "mingming", asset.AssetID); err == nil {
		t.Fatal("asset crossed owner scope")
	}
	for _, wrong := range []string{
		"另一个硬盘，条件无法辨认。",
		"一个硬盘，已用空间是总容量的2/5，正好用了4.8 GB。",
		"一个硬盘，已用空间是总容量的3/5，正好用了4.8 TB。",
	} {
		copy := q
		copy.Question = wrong
		rejected := d.attachPhotoTeachingDiagram(t.Context(), "mingming", copy, attached)
		if len(rejected.ParentGuide.TeachingDiagrams) != 0 || rejected.ParentGuide.Answer != guide.Answer || rejected.Status != PhotoAnswerUnclear {
			t.Fatal("invalid source borrowed a diagram or changed answer state")
		}
	}
	var markdown strings.Builder
	writeParentGuideStepsMarkdown(&markdown, "必要步骤", *attached.ParentGuide, true)
	if strings.Index(markdown.String(), asset.AssetID) > strings.Index(markdown.String(), "再求总量") {
		t.Fatal("diagram is not after the first step")
	}
	renderer := &frozenDiagramRenderer{}
	d.Renderer = renderer
	req := PreparePrintableArtifactRequest{AgentName: "mingming", SourceKind: k12.PrintSourceGradingFinalArtifact,
		SourceRef: "diagram-frozen-test", Title: "Frozen diagram", CanonicalMarkdown: markdown.String()}
	first, replay, err := d.PreparePrintableArtifact(t.Context(), req)
	if err != nil || replay || renderer.calls != 1 || !strings.Contains(renderer.input, "data:image/svg+xml;base64,") || strings.Contains(renderer.input, "asset://") {
		t.Fatalf("first render did not consume frozen SVG: replay=%t calls=%d err=%v", replay, renderer.calls, err)
	}
	if _, err := store.DB().ExecContext(t.Context(), `DELETE FROM k12_teaching_diagram_assets WHERE asset_id=?`, asset.AssetID); err != nil {
		t.Fatal(err)
	}
	second, replay, err := d.PreparePrintableArtifact(t.Context(), req)
	if err != nil || !replay || renderer.calls != 1 || first.Render.ByteDigest != second.Render.ByteDigest {
		t.Fatalf("complete frozen PDF incorrectly depends on separate SVG: %v", err)
	}
	req.SourceRef = "new-diagram-render"
	if _, _, err := d.PreparePrintableArtifact(t.Context(), req); err == nil || renderer.calls != 1 {
		t.Fatal("new render silently omitted its required diagram")
	}
	if mistakes, err := store.ListByScope(t.Context(), "mingming", k12.CollectionMistakes, ""); err != nil || len(mistakes) != 0 {
		t.Fatal("diagram construction wrote student learning facts")
	}
}
