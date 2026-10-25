package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestTeachingReportProjectionKeepsOriginalAndUsesOrientedDisplay(t *testing.T) {
	encode := func(rows []string) []byte {
		img := image.NewNRGBA(image.Rect(0, 0, len(rows[0]), len(rows)))
		for y, row := range rows {
			for x, v := range []byte(row) {
				img.SetNRGBA(x, y, color.NRGBA{R: v, A: 255})
			}
		}
		var data bytes.Buffer
		if err := png.Encode(&data, img); err != nil {
			t.Fatal(err)
		}
		return data.Bytes()
	}
	original := encode([]string{"abc", "def"})
	input := teachingReportBuildFixture(t)
	input.SourcePages = []k12.TeachingReportSourcePage{{AssetID: "whole-page", AssetDigest: teachingReportResourceDigest(original), SourceDigest: teachingReportResourceDigest(original), MIME: "image/png", Width: 3, Height: 2}}
	report, err := BuildGradingTeachingReport(input)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(report)
	for _, tc := range []struct {
		angle int
		rows  []string
	}{{90, []string{"da", "eb", "fc"}}, {180, []string{"fed", "cba"}}, {270, []string{"cf", "be", "ad"}}} {
		t.Run(strconv.Itoa(tc.angle), func(t *testing.T) {
			display := encode(tc.rows)
			asset := TeachingReportAsset{Data: append([]byte(nil), original...), MIME: "image/png"}
			extra, _ := json.Marshal(map[string]any{"DisplayData": display, "DisplayMIME": "image/png"})
			if err := json.Unmarshal(extra, &asset); err != nil {
				t.Fatal(err)
			}
			projection, err := ProjectTeachingReportPDFV2(report, TeachingReportBranding{}, map[string]TeachingReportAsset{"whole-page": asset})
			if err != nil {
				t.Fatal(err)
			}
			if len(projection.Resources) != 1 {
				t.Fatalf("whole image resource count=%d", len(projection.Resources))
			}
			for _, got := range projection.Resources {
				if !bytes.Equal(got, display) {
					t.Fatalf("%d-degree display retained uncorrected image", tc.angle)
				}
			}
			after, _ := json.Marshal(report)
			if !bytes.Equal(before, after) || !bytes.Equal(asset.Data, original) {
				t.Fatal("display rotation mutated frozen source")
			}
		})
	}
}

// 已授权方向来自真实SQLite识题计划；新版打印入口必须消费同源计划而不重写历史终稿。
func TestTeachingReportPrintRequestUsesPersistedPageOrientation(t *testing.T) {
	for _, angle := range []int{90, 180, 270} {
		t.Run(strconv.Itoa(angle), func(t *testing.T) {
			ctx := context.Background()
			original := recognitionPhysicalExecutorV2PagePNG(t, 40, 20)
			f := prepareCompletedSourceFixtureForIntent(t, k12.ImageTaskIntentCompletedHomework, completedSourceImageFixture{data: original, unannotated: true})
			page, err := k12.CanonicalizeRecognitionPageV2(original)
			if err != nil {
				t.Fatal(err)
			}
			parent := k12.ModelInvocation{InvocationID: "orientation-parent", AgentName: "mingming", JobID: f.job.Record.RecordID, Stage: k12.GradingStageRecognizing, RequestDigest: modelInvocationDigest([]byte("orientation-source")), RouteSnapshot: f.job.Fields.ModelSnapshot, Attempt: 1, CreatedAt: time.Now().Unix()}
			header := k12.RecognitionLayoutPlanHeaderV2{PlanID: "orientation-plan", ParentInvocationID: parent.InvocationID, AgentName: parent.AgentName, JobID: parent.JobID, PageDigest: page.Digest, ParentRequestDigest: parent.RequestDigest, RouteSnapshot: parent.RouteSnapshot, StageStartedAtUnixMillis: time.Now().UnixMilli(), PhysicalCallCapMillis: 120000, BudgetBuckets: k12.RecognitionLayoutBudgetBucketsV2{UpTo1ProblemMillis: 120000, UpTo8ProblemsMillis: 120000, UpTo16ProblemsMillis: 120000, UpTo32ProblemsMillis: 120000}, AdapterWorkerHardCap: 2, EffectiveConcurrency: 1}
			headerDigest, err := k12.RecognitionLayoutPlanHeaderDigestV2(header)
			if err != nil {
				t.Fatal(err)
			}
			call := k12.RecognitionPhysicalCall{PlanVersion: k12.RecognitionPlanVersionV2, PlanDigest: headerDigest, Unit: k12.RecognitionPhysicalUnitWholePage, Image: page.PNG}
			requestDigest, err := recognizingPhysicalInvocationDigest(parent, call)
			if err != nil {
				t.Fatal(err)
			}
			physicalID, err := stableRecognitionPhysicalInvocationIDForCall(parent.InvocationID, call)
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, err = f.store.PrepareRecognizingInvocationWithInitialLayoutPlanV2(ctx, parent, k12.ModelPhysicalInvocation{PhysicalInvocationID: physicalID, ParentInvocationID: parent.InvocationID, AgentName: parent.AgentName, JobID: parent.JobID, Stage: parent.Stage, PhysicalUnit: call.Unit, RecognitionPlanVersion: call.PlanVersion, PlanDigest: call.PlanDigest, RequestDigest: requestDigest, RouteSnapshot: parent.RouteSnapshot, Attempt: 1, CreatedAt: parent.CreatedAt}, header)
			if err != nil {
				t.Fatal(err)
			}
			if _, claimed, err := f.store.ClaimModelPhysicalInvocationSent(ctx, parent.AgentName, physicalID); err != nil || !claimed {
				t.Fatalf("claim orientation source: %v", err)
			}
			physical, err := f.store.MarkModelPhysicalInvocationSucceededWithContent(ctx, parent.AgentName, physicalID, `{"targets":["whole"],"orientation":`+strconv.Itoa(angle)+`}`, "")
			if err != nil {
				t.Fatal(err)
			}
			manifest := k12.RecognitionLayoutManifestSuccessV2{InvocationID: physicalID, ResultDigest: physical.ResultDigest}
			plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{PagePNG: page.PNG, Manifest: manifest, RecognitionFormat: k12.RecognitionLayoutCompactV4, PageOrientation: &k12.RecognitionPageOrientationV1{Version: "page_orientation_v1", PageDigest: page.Digest, ClockwiseDegrees: angle, Confidence: 1}, Targets: []k12.RecognitionLayoutManifestTargetV2{{ManifestRef: "manifest_0001", ManifestOrder: 1, DisplayLabel: "1", SourceNumberPath: []string{"1"}, Region: k12.SourcePixelRegion{Width: 40, Height: 20}}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = f.store.AuthorizeRecognitionLayoutPlanV2(ctx, parent.AgentName, parent.InvocationID, manifest, plan); err != nil {
				t.Fatal(err)
			}
			storedParent, err := f.store.GetModelInvocation(ctx, parent.AgentName, parent.InvocationID)
			if err != nil {
				t.Fatal(err)
			}
			if storedParent.Status == k12.ModelInvocationPrepared {
				if _, err = f.store.MarkModelInvocationSent(ctx, parent.AgentName, parent.InvocationID, ""); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = f.store.MarkModelInvocationSucceeded(ctx, parent.AgentName, parent.InvocationID, modelInvocationDigest([]byte("orientation-complete")), ""); err != nil {
				t.Fatal(err)
			}
			questions, ok := f.o.typedRecognizedQuestions(ctx, f.job.Record.RecordID, parent.AgentName)
			if !ok {
				t.Fatal("source questions unavailable")
			}
			assessments, err := f.store.ListEffectiveGradingAssessments(ctx, parent.AgentName, f.job.Record.RecordID)
			if err != nil {
				t.Fatal(err)
			}
			input := GradingTeachingReportInput{AgentName: parent.AgentName, Source: k12.TeachingReportSource{Kind: k12.TeachingReportSourceGrading, SourceID: f.job.Record.RecordID, Revision: 1, Digest: teachingReportResourceDigest(original)}, Metadata: k12.TeachingReportMetadata{Title: "方向回归", AgentDisplayName: "数学辅导", GradeTerm: "六年级", Subject: "数学", ReportDate: "2026-10-25", Timezone: "Asia/Shanghai"}, Questions: questions, Assessments: assessments, SourcePages: []k12.TeachingReportSourcePage{{AssetID: "whole-source", AssetDigest: teachingReportResourceDigest(original), SourceDigest: teachingReportResourceDigest(original), MIME: "image/png", Width: 40, Height: 20}}}
			for _, q := range questions {
				input.Content = append(input.Content, TeachingReportQuestionContent{ProblemID: q.ProblemID, ShortTitle: "本题", AnswerAnalysis: k12.TeachingReportAnswerAnalysis{State: k12.TeachingReportReferenceUnavailable, UnavailableReason: "来源未提供参考解答"}})
			}
			report, err := BuildGradingTeachingReport(input)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := TeachingReportFrozenSnapshot{Report: report, Bundle: reportBundleFixture(t)}
			artifact := f.old
			artifact.CanonicalMarkdown, err = TeachingReportMarkdown(report)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			artifact.ReportSnapshotJSON = string(raw)
			request, err := f.o.deps.gradingFinalArtifactPrintRequest(ctx, artifact, "方向回归")
			if err != nil {
				t.Fatal(err)
			}
			asset := request.TeachingReport.Assets["whole-source"]
			expected, err := rotatePDFImage(original, angle)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(asset.DisplayData, expected) || !bytes.Equal(asset.Data, original) {
				t.Fatalf("new report did not consume %d-degree persisted orientation", angle)
			}
			if out := os.Getenv("HEXCLAW_TEST_PDF_EVIDENCE_DIR"); out != "" && os.Getenv("HEXCLAW_TEST_PANDOC") != "" && os.Getenv("HEXCLAW_TEST_TYPST") != "" {
				r, err := render.NewPandocRenderer(os.Getenv("HEXCLAW_TEST_PANDOC"), os.Getenv("HEXCLAW_TEST_TYPST"), t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				service, err := render.NewService(render.ServiceConfig{Renderer: r})
				if err != nil {
					t.Fatal(err)
				}
				f.o.deps.Renderer = &actualReportRenderer{service: service}
				v, _, err := f.o.deps.PreparePrintableArtifact(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.MkdirAll(out, 0700); err != nil {
					t.Fatal(err)
				}
				pdf := filepath.Join(out, "oriented-"+strconv.Itoa(angle)+".pdf")
				if err = os.WriteFile(pdf, v.Render.Payload, 0600); err != nil {
					t.Fatal(err)
				}
				prefix := filepath.Join(t.TempDir(), "image")
				if output, err := exec.Command("pdfimages", "-png", pdf, prefix).CombinedOutput(); err != nil {
					t.Fatalf("extract oriented source: %v %s", err, output)
				}
				want, err := png.Decode(bytes.NewReader(expected))
				if err != nil {
					t.Fatal(err)
				}
				files, err := filepath.Glob(prefix + "*.png")
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, path := range files {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					got, err := png.Decode(bytes.NewReader(data))
					if err != nil {
						t.Fatal(err)
					}
					if got.Bounds() != want.Bounds() {
						continue
					}
					same := true
					for y := 0; y < want.Bounds().Dy(); y++ {
						for x := 0; x < want.Bounds().Dx(); x++ {
							if color.NRGBAModel.Convert(got.At(x, y)) != color.NRGBAModel.Convert(want.At(x, y)) {
								same = false
							}
						}
					}
					found = found || same
				}
				if !found {
					t.Fatalf("actual PDF did not contain complete %d-degree source pixels", angle)
				}
			}
			old, err := f.store.GetGradingFinalArtifact(ctx, parent.AgentName, f.old.ArtifactID)
			if err != nil || old.ArtifactDigest != f.old.ArtifactDigest || old.ReportSnapshotJSON != f.old.ReportSnapshotJSON {
				t.Fatalf("old final artifact changed: %v", err)
			}
		})
	}
}
