package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type originalReportFixtureRenderer struct{}

func (originalReportFixtureRenderer) Render(context.Context, string, string) ([]byte, string, error) {
	return nil, "", fmt.Errorf("legacy rendering is unavailable in report fixture")
}

func (originalReportFixtureRenderer) RenderTeachingReport(context.Context, render.ReportBundle, render.ReportRenderInput) ([]byte, string, error) {
	return []byte("%PDF-1.4\ncontrolled report delivery fixture"), "application/pdf", nil
}

func originalTeachingReportFixture(t *testing.T, sources ...[]byte) (Deps, *ImageTaskCoordinator, *GradingOrchestrator, k12.GradingFinalArtifact, []byte) {
	t.Helper()
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	d, _, _ := reportResilienceDeps(t)
	teaching := &reportResilienceTeaching{validContent: true}
	d.ParentTeachingGuide, d.ParentTeachingGuideAudit = teaching, teaching
	d.TextbookOwnerID = DefaultLocalOwnerScope
	d.GradingBudgetSnapshot = orchestratorTestBudget()
	d.PhotoAnnotator = nil
	pixels := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			pixels.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 30), G: uint8(y * 70), B: 180, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	original := encoded.Bytes()
	if len(sources) != 0 {
		original = sources[0]
	}
	geometry, _, err := image.DecodeConfig(bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	confidence := .99
	recognizer := &countingRecognizer{questions: []RecognizedQuestion{{
		Question: reportResilienceProblem, RawTranscription: reportResilienceProblem,
		CanonicalMarkdown:      reportResilienceProblem,
		EvidenceTranscriptions: []string{reportResilienceProblem, reportResilienceProblem}, RecognitionConfidence: &confidence,
		Subject: "数学", AnswerState: AnswerStateUnclear, SourceWidth: geometry.Width, SourceHeight: geometry.Height,
	}}}
	d.Recognizer = recognizer
	req := orchestratorPhotoRequest()
	req.Subject, req.TaskIntent, req.Image = "数学", PhotoTaskCompletedHomework, original
	o := trackGradingOrchestrator(t, NewGradingOrchestrator(d, orchestratorSnapshotResolver, WithGradingRunDir(t.TempDir())))
	repository := &PageAssetRepository{Records: d.Records}
	ready, err := repository.Persist(t.Context(), DefaultLocalOwnerScope, "mingming", original)
	if err != nil {
		t.Fatal(err)
	}
	canonicalSource, err := canonicalProblemSourceImage(ready)
	if err != nil {
		t.Fatal(err)
	}
	req.Image = canonicalSource.Data
	recognizer.questions[0].PageAssetID = ready.Metadata.PageAssetID
	recognizer.questions[0].SourceWidth, recognizer.questions[0].SourceHeight = canonicalSource.Width, canonicalSource.Height
	coordinator := &ImageTaskCoordinator{Records: d.Records, PageAssets: repository, Grading: o, Classifier: &imageTaskClassifierStub{}, ResolveRoute: imageTaskRouteForTest, Now: d.Now}
	input := testCreateImageTaskInput()
	input.OwnerScope, input.SourceAssetRefs = DefaultLocalOwnerScope, []string{ready.Metadata.PageAssetID}
	input.RouteRequest.MessageLocale = "zh-CN"
	prepared, _, err := coordinator.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	dispatch, target, err := d.Records.CommitImageTaskRouting(t.Context(), "mingming", prepared.Dispatch.DispatchID, prepared.Dispatch.Version,
		k12storage.ImageTaskRoutingDecision{Intent: k12.ImageTaskIntentCompletedHomework, Confidence: 1, Evidence: []string{"controlled worksheet"}, InvocationResultDigest: "sha256:controlled-classification"})
	if err != nil {
		t.Fatal(err)
	}
	req.SourcePageAssetID, req.SourceSession = ready.Metadata.PageAssetID, input.SourceSessionID
	job, _, err := o.StartPhotoGradingJob(t.Context(), StartPhotoGradingInput{Photo: req, SourceKind: "image_task", SourceKey: dispatch.DispatchID,
		BudgetSnapshot: d.GradingBudgetSnapshot, ParentAutomaticAttemptID: dispatch.DispatchID + ":fixture", ParentAutomaticDeadlineAt: d.now() + 300})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Records.BindHomeworkSubmissionGradingJob(t.Context(), "mingming", target.HomeworkSubmission.SubmissionID, job.Record.RecordID, target.HomeworkSubmission.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RunGradingJob(t.Context(), job.Record.RecordID); err != nil {
		t.Fatal(err)
	}
	completed := waitGradingView(t, o, job.Record.RecordID, func(view GradingJobView) bool {
		return view.Record.Status == k12.GradingStageCompleted || view.Record.Status == k12.GradingStageFailedRetryable || view.Record.Status == k12.GradingStageOutcomeUnknown
	})
	if completed.Record.Status != k12.GradingStageCompleted {
		t.Fatalf("report with original source did not complete: %+v", completed)
	}
	artifact, err := d.Records.GetGradingFinalArtifactByJob(t.Context(), "mingming", job.Record.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.HasAnnotatedAsset() || artifact.TeachingReport == nil || len(artifact.TeachingReport.DeepSections) == 0 ||
		!strings.Contains(artifact.CanonicalMarkdown, "4支") || !strings.Contains(artifact.CanonicalMarkdown, "2×2=4支") {
		t.Fatalf("missing complete report or fabricated annotation: artifact=%+v", artifact)
	}
	return d, coordinator, o, artifact, original
}

func TestParentTeachingOriginalImageCompletesReportAndSharedDelivery(t *testing.T) {
	d, coordinator, o, artifact, original := originalTeachingReportFixture(t)
	job, err := d.GetGradingJob(t.Context(), artifact.AgentName, artifact.JobID)
	if err != nil {
		t.Fatal(err)
	}
	dispatchID, err := gradingFinalImageTaskDispatchID(job)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Result(t.Context(), artifact.AgentName, dispatchID)
	if err != nil || result.FinalArtifact == nil || result.FinalArtifact.ArtifactID != artifact.ArtifactID || result.Photo == nil || result.Photo.AnnotatedImage != nil {
		t.Fatalf("public image result lost the complete original-image report: result=%+v err=%v", result, err)
	}
	photo, ok := o.PhotoResult(artifact.JobID)
	if !ok || photo.AnnotatedImage != nil {
		t.Fatalf("image result fabricated annotation: available=%v image=%v", ok, photo.AnnotatedImage)
	}
	source, err := GradingTeachingReportOriginalImage(artifact, DeliveryAttachment{Data: original, MIME: "image/png"})
	if err != nil || source.Name != "原图.png" || !bytes.Equal(source.Data, original) {
		t.Fatalf("original image identity was lost: %+v err=%v", source, err)
	}
	request, err := d.gradingFinalArtifactPrintRequest(t.Context(), artifact, "")
	if err != nil || request.TeachingReport == nil {
		t.Fatalf("report PDF source is unavailable: %v", err)
	}
	page := artifact.TeachingReport.SourcePages[0]
	if media := request.TeachingReport.Assets[page.AssetID]; !bytes.Equal(media.Data, original) || page.AssetDigest != page.SourceDigest {
		t.Fatal("report PDF lost or relabeled the original image")
	}
	d.Renderer = originalReportFixtureRenderer{}
	if os.Getenv("HEXCLAW_TEST_PANDOC") != "" && os.Getenv("HEXCLAW_TEST_TYPST") != "" {
		renderer, err := render.NewPandocRenderer(os.Getenv("HEXCLAW_TEST_PANDOC"), os.Getenv("HEXCLAW_TEST_TYPST"), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		service, err := render.NewService(render.ServiceConfig{Renderer: renderer})
		if err != nil {
			t.Fatal(err)
		}
		d.Renderer = &actualReportRenderer{service: service}
	}
	transport := &finalReplyContractTransport{finalReplyHTTPTransport: newFinalReplyHTTPTransport(t, d.Records)}
	d.Delivery = transport
	targets := []ResolvedDeliveryTarget{{BindingID: "parent-teaching-binding", Target: k12.DeliveryTarget{Platform: "dingtalk", InstanceID: "controlled-bot", ChatID: "controlled-parent"}}}
	batch, _, err := d.PrepareAndSendK12FinalReplyBatch(t.Context(), artifact.AgentName, "dingtalk_photo_grading_reply", "original-report-once",
		artifact.ArtifactID, artifact.ArtifactDigest, DeliveryMessage{}, targets)
	if err != nil || batch.Status != k12.DeliveryBatchDelivered || len(batch.Receipts) != 2 {
		t.Fatalf("complete original-image report was not delivered: batch=%+v err=%v", batch, err)
	}
	if batch.Receipts[0].PartKind != messagecontent.PartMarkdown || batch.Receipts[1].PartKind != messagecontent.PartArtifact {
		t.Fatal("final reply has an invalid part set")
	}
	for _, receipt := range batch.Receipts {
		payload := readFinalReplyContractPayload(t, batch, receipt)
		if payload.Source == nil || payload.Source.Canonical.Markdown != artifact.CanonicalMarkdown {
			t.Fatal("final reply lost complete quick/deep canonical content")
		}
	}
	before := len(transport.sends)
	if _, _, err := d.PrepareAndSendK12FinalReplyBatch(t.Context(), artifact.AgentName, "dingtalk_photo_grading_reply", "original-report-once",
		artifact.ArtifactID, artifact.ArtifactDigest, DeliveryMessage{}, targets); err != nil || len(transport.sends) != before {
		t.Fatalf("replay sent the frozen batch again: sends=%d/%d err=%v", len(transport.sends), before, err)
	}
	if os.Getenv("HEXCLAW_TEST_PANDOC") != "" && os.Getenv("HEXCLAW_TEST_TYPST") != "" {
		pdf, _, err := d.PreparePrintableArtifact(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		out := t.TempDir()
		if selected := os.Getenv("HEXCLAW_TEST_PDF_EVIDENCE_DIR"); selected != "" {
			out = selected
			if err := os.MkdirAll(out, 0700); err != nil {
				t.Fatal(err)
			}
		}
		pdfPath := filepath.Join(out, "parent-teaching-original-report.pdf")
		if err := os.WriteFile(pdfPath, pdf.Render.Payload, 0600); err != nil {
			t.Fatal(err)
		}
		text, err := exec.Command("pdftotext", pdfPath, "-").CombinedOutput()
		readable := strings.Join(strings.Fields(string(text)), "")
		if err != nil || !strings.Contains(readable, "快速解答") || !strings.Contains(readable, "深度讲解") || !strings.Contains(readable, "4支") || !strings.Contains(readable, "每盒2支") {
			t.Fatalf("rendered PDF lost the answer/deep explanation: %s err=%v", text, err)
		}
		prefix := filepath.Join(out, "report-source")
		if output, err := exec.Command("pdfimages", "-png", pdfPath, prefix).CombinedOutput(); err != nil {
			t.Fatalf("extract report original image: %s %v", output, err)
		}
		matches, err := filepath.Glob(prefix + "-*.png")
		if err != nil || len(matches) == 0 {
			t.Fatal("rendered PDF contains no source image")
		}
		wantImage, _, err := image.Decode(bytes.NewReader(original))
		if err != nil {
			t.Fatal(err)
		}
		foundOriginal := false
		for _, path := range matches {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := image.Decode(bytes.NewReader(data))
			if err != nil || got.Bounds() != wantImage.Bounds() {
				continue
			}
			equal := true
			for y := 0; y < wantImage.Bounds().Dy(); y++ {
				for x := 0; x < wantImage.Bounds().Dx(); x++ {
					r, g, b, a := wantImage.At(x, y).RGBA()
					r2, g2, b2, a2 := got.At(x, y).RGBA()
					equal = equal && r == r2 && g == g2 && b == b2 && a == a2
				}
			}
			foundOriginal = foundOriginal || equal
		}
		if !foundOriginal {
			t.Fatal("rendered PDF omitted or changed the original page pixels")
		}
		t.Logf("actual_report_pdf=%s bytes=%d extracted_images=%d", pdfPath, len(pdf.Render.Payload), len(matches))
	}
}

func TestParentTeachingOriginalImageRejectsBadSourceAndDoesNotChangeFrozenReport(t *testing.T) {
	_, _, _, artifact, original := originalTeachingReportFixture(t)
	before, _ := json.Marshal(artifact)
	for _, source := range []DeliveryAttachment{{MIME: "image/png", Data: []byte("broken source")}, {MIME: "image/jpeg", Data: original}, {MIME: "image/png", Data: validPNGFixture(t, "different-original")}} {
		if _, err := GradingTeachingReportOriginalImage(artifact, source); err == nil {
			t.Fatal("bad or different source image became a successful report fallback")
		}
	}
	after, _ := json.Marshal(artifact)
	if !bytes.Equal(before, after) {
		t.Fatal("fallback validation changed the frozen artifact")
	}
}

func TestParentTeachingOriginalImageKeepsEXIF6RawPageIdentity(t *testing.T) {
	raw := photoEXIF6JPEGFixture(t, 120, 80)
	d, _, _, artifact, original := originalTeachingReportFixture(t, raw)
	page := artifact.TeachingReport.SourcePages[0]
	if page.MIME != "image/jpeg" || page.Width != 120 || page.Height != 80 || page.AssetDigest != teachingReportResourceDigest(raw) {
		t.Fatalf("report did not freeze its actual raw PageAsset: %+v", page)
	}
	got, err := GradingTeachingReportOriginalImage(artifact, DeliveryAttachment{Data: original, MIME: "image/jpeg"})
	if err != nil || !bytes.Equal(got.Data, raw) || got.Name != "原图.jpg" {
		t.Fatalf("EXIF6 raw original was rejected: image=%+v err=%v", got, err)
	}
	repository := &PageAssetRepository{Records: d.Records}
	ready, err := repository.Persist(t.Context(), DefaultLocalOwnerScope, artifact.AgentName, raw)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalProblemSourceImage(ready)
	if err != nil || canonical.Width != 80 || canonical.Height != 120 {
		t.Fatalf("EXIF6 fixture was not normalized by the existing source contract: %dx%d err=%v", canonical.Width, canonical.Height, err)
	}
	if _, err := GradingTeachingReportOriginalImage(artifact, DeliveryAttachment{Data: canonical.Data, MIME: "image/png"}); err == nil {
		t.Fatal("normalized model pixels impersonated the frozen raw original")
	}
}
