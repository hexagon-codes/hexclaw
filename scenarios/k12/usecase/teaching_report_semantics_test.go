package usecase

import (
	"bytes"
	"image"
	"image/png"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestTeachingReportSharedEquationsDoNotLeaveEmptySection(t *testing.T) {
	input := teachingReportBuildFixture(t)
	report, err := BuildGradingTeachingReport(input)
	if err != nil {
		t.Fatal(err)
	}
	questions := input.Questions[:2]
	for i := range questions {
		questions[i].PageAssetID = "same-frozen-page"
		questions[i].ParentProblemID = "same-parent"
	}
	report.DeepSections = nil
	for i, title := range []string{"先求一份", "由部分求整体"} {
		id := questions[i].ProblemID
		origin := input.Content[i].AnswerAnalysis.Answers[0].Origin
		report.DeepSections = append(report.DeepSections, k12.TeachingReportDeepSection{
			ID: id + "-section", Title: title, ProblemIDs: []string{id},
			Blocks: []k12.TeachingReportExplanationBlock{
				{ID: id + "-first", LaTeX: `4.8\div2=2.4\,\mathrm{TB}`, Origins: []k12.TeachingReportContentOrigin{origin}},
				{ID: id + "-second", LaTeX: `2.4\times5=12\,\mathrm{TB}`, Origins: []k12.TeachingReportContentOrigin{origin}},
			},
		})
	}
	if _, err := k12.NewTeachingReport(report); err != nil {
		t.Fatalf("valid source sections: %v", err)
	}
	report.DeepSections = consolidateGradingReportSections(questions, report.DeepSections)
	frozen, err := k12.NewTeachingReport(report)
	if err != nil {
		t.Fatalf("deduplication must leave a valid report: %v", err)
	}
	if len(frozen.DeepSections) != 1 || len(frozen.DeepSections[0].Blocks) != 2 || len(frozen.DeepSections[0].ProblemIDs) != 2 {
		t.Fatalf("duplicate-only section or source coverage remains incorrect: %+v", frozen.DeepSections)
	}
	for _, block := range frozen.DeepSections[0].Blocks {
		if len(block.Origins) != 2 || block.Origins[0] != input.Content[0].AnswerAnalysis.Answers[0].Origin || block.Origins[1] != input.Content[1].AnswerAnalysis.Answers[0].Origin {
			t.Fatalf("shared formula lost its exact input/solution provenance: %+v", block.Origins)
		}
	}
}

func TestReportOptionalDiagramStorageFailurePreservesCompleteGuide(t *testing.T) {
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
	if _, err := store.DB().ExecContext(t.Context(), `CREATE TRIGGER reject_optional_diagram BEFORE INSERT ON k12_teaching_diagram_assets BEGIN SELECT RAISE(ABORT, 'diagram storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prior) })
	q := RecognizedQuestion{ProblemID: "optional-diagram-source", PageAssetID: ready.Metadata.PageAssetID,
		ConfirmedVersion: 1, InputDigest: strings.Repeat("a", 64), Subject: "数学",
		Question: "一个硬盘，已用空间是总容量的2/5，正好用了4.8 TB。总容量是多少？"}
	guide := ParentTeachingGuide{Answer: "12 TB", FullSolutionSteps: []string{"4.8÷2=2.4（TB）。", "2.4×5=12（TB）。"},
		GradeLevelMethod: "分数的意义", LikelyMistakes: []string{"把部分当成整体"}, ParentTeachingSequence: []string{"先找对应份数"},
		FollowUpQuestions: []string{"每份是多少？"}, CheckingMethod: "12×2/5=4.8（TB）。",
		ReportContent: &TeachingReportContentDraft{Version: TeachingReportContentContractVersion, Items: []TeachingReportDraftItem{{ProblemID: "current",
			Answers: []TeachingReportDraftAnswer{{ID: "whole", Text: "12 TB"}}, Steps: []TeachingReportDraftStep{{ID: "whole-step", Text: "4.8÷2/5=12 TB"}}}}}}
	got, err := d.attachReportTeachingDiagram(t.Context(), "mingming", q, "12 TB", strings.Repeat("b", 64), guide)
	if err != nil {
		t.Fatalf("optional illustration blocked complete teaching text: %v", err)
	}
	if len(got.TeachingDiagramWarnings) != 1 || !strings.Contains(got.TeachingDiagramWarnings[0], q.ProblemID) || !strings.Contains(got.TeachingDiagramWarnings[0], "storage unavailable") {
		t.Fatalf("optional illustration failure lost its frozen degradation reason: %+v", got.TeachingDiagramWarnings)
	}
	got.TeachingDiagramWarnings = nil
	if !reflect.DeepEqual(got, guide) {
		t.Fatalf("optional illustration failure changed teaching content: %+v", got)
	}
	var count int
	if err := store.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM k12_teaching_diagram_assets`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed optional diagram was persisted: count=%d err=%v", count, err)
	}
	if !strings.Contains(logs.String(), "Teaching report diagram omitted") || !strings.Contains(logs.String(), q.ProblemID) || !strings.Contains(logs.String(), "diagram storage unavailable") {
		t.Fatalf("optional degradation was not recorded with source and cause: %s", logs.String())
	}
}

func TestFrozenRequiredDiagramCorruptionStillFailsPrinting(t *testing.T) {
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
	q := RecognizedQuestion{ProblemID: "required-diagram-source", PageAssetID: ready.Metadata.PageAssetID,
		ConfirmedVersion: 1, InputDigest: strings.Repeat("a", 64), Subject: "数学",
		Question: "一个硬盘，已用空间是总容量的2/5，正好用了4.8 TB。总容量是多少？"}
	guide, err := d.attachReportTeachingDiagram(t.Context(), "mingming", q, "12 TB", strings.Repeat("b", 64), ParentTeachingGuide{Answer: "12 TB"})
	if err != nil || len(guide.TeachingDiagrams) != 1 {
		t.Fatalf("freeze required diagram: diagrams=%d err=%v", len(guide.TeachingDiagrams), err)
	}
	asset := guide.TeachingDiagrams[0]
	if _, err := store.DB().ExecContext(t.Context(), `UPDATE k12_teaching_diagram_assets SET svg=? WHERE asset_id=?`, []byte("corrupt frozen illustration"), asset.AssetID); err != nil {
		t.Fatal(err)
	}
	renderer := &frozenDiagramRenderer{}
	d.Renderer = renderer
	_, _, err = d.PreparePrintableArtifact(t.Context(), PreparePrintableArtifactRequest{
		AgentName: "mingming", SourceKind: k12.PrintSourceGradingFinalArtifact,
		SourceRef: "required-diagram-corruption", Title: "Frozen diagram",
		CanonicalMarkdown: "# 完整讲解\n\n答案：12 TB。\n\n![等分关系](" + asset.AssetID + ")\n",
	})
	if err == nil || renderer.calls != 0 {
		t.Fatalf("corrupt frozen required diagram was silently omitted: render_calls=%d err=%v", renderer.calls, err)
	}
}

func TestTeachingDiagramFractionRelationDoesNotDependOnTopic(t *testing.T) {
	for _, tc := range []struct{ name, question, answer, unit string }{
		{"storage device", "一个存储设备，已用空间是总容量的2/5，正好用了4.8 TB。总容量是多少？", "12 TB", "TB"},
		{"storage space", "某存储空间，已用容量占总空间的2/5，已用4.8 TB。总空间是多少？", "12 TB", "TB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := RecognizedQuestion{ProblemID: "fraction-source", ConfirmedVersion: 3, InputDigest: strings.Repeat("a", 64), Subject: "数学", Question: tc.question}
			digest := strings.Repeat("b", 64)
			source, candidate, ok := teachingDiagramQuantityContext(q, tc.answer, digest)
			if !ok {
				t.Fatal("explicit verified fraction relation was rejected for its topic")
			}
			result := BuildTeachingDiagram(source, candidate)
			if result.Status != "ready" || candidate.Kind != "fraction_bar" || source.Quantities["whole"].Value != "12" || source.Quantities["whole"].Unit != tc.unit || source.Quantities["used"].Value != "4.8" {
				t.Fatalf("quantity relation or physical unit changed: %+v %+v", source, result)
			}
			if source.ProblemID != q.ProblemID || source.InputRevision != 3 || source.ProblemInputDigest != q.InputDigest || source.SolutionDigest != digest {
				t.Fatal("fraction diagram lost its frozen problem/solution identity")
			}
			if _, _, ok := teachingDiagramQuantityContext(q, "13"+tc.unit, digest); ok {
				t.Fatal("a non-equivalent whole was accepted")
			}
			if _, _, ok := teachingDiagramQuantityContext(q, "12千克", digest); ok {
				t.Fatal("a different physical unit was accepted")
			}
		})
	}
}
