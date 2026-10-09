package usecase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type finalReplyPDFRenderer struct {
	calls    int
	markdown string
	err      error
}

type finalReplyRealRenderer struct {
	renderer *render.PandocRenderer
	calls    int
}

func (r *finalReplyRealRenderer) Render(ctx context.Context, markdown, format string) ([]byte, string, error) {
	r.calls++
	result, err := r.renderer.Render(ctx, markdown, render.Format(format), render.RenderOptions{Locale: "zh-CN"})
	if err != nil {
		return nil, "", err
	}
	defer os.Remove(result.Path)
	data, err := os.ReadFile(result.Path)
	return data, result.ContentType, err
}

func TestK12FinalPDFRealRenderedFrozenGuide(t *testing.T) {
	fixture := os.Getenv("HEXCLAW_TEST_FROZEN_GUIDE")
	if fixture == "" {
		t.Skip("real frozen guide fixture was not selected")
	}
	for _, key := range []string{"HEXCLAW_TEST_PANDOC", "HEXCLAW_TEST_TYPST", "HEXCLAW_TEST_SOURCE_IMAGE", "HEXCLAW_TEST_PDF_EVIDENCE_DIR"} {
		if os.Getenv(key) == "" {
			t.Fatalf("required selected fixture setting %s is missing", key)
		}
	}
	// 与安装版启动环境一致，让 pandoc 子进程能发现现有捆绑的 typst。
	t.Setenv("PATH", filepath.Dir(os.Getenv("HEXCLAW_TEST_TYPST"))+string(os.PathListSeparator)+os.Getenv("PATH"))
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != os.Getenv("HEXCLAW_TEST_FROZEN_GUIDE_SHA256") {
		t.Fatalf("frozen canonical fixture digest differs: %s", got)
	}
	canonical := string(raw)
	if strings.Count(canonical, "### 家长辅导指南") != 16 {
		t.Fatal("selected complete fixture must retain all sixteen guides")
	}
	image, err := os.ReadFile(os.Getenv("HEXCLAW_TEST_SOURCE_IMAGE"))
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := render.NewPandocRenderer(os.Getenv("HEXCLAW_TEST_PANDOC"), os.Getenv("HEXCLAW_TEST_TYPST"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d, _ := newPipeline(t, nil, nil, nil)
	real := &finalReplyRealRenderer{renderer: renderer}
	d.Renderer = real
	request := PreparePrintableArtifactRequest{AgentName: "mingming", SourceKind: k12.PrintSourceGradingFinalArtifact, SourceRef: "frozen-canonical:" + hex.EncodeToString(sum[:]) + ":" + imageFinalPDFRenderContractVersion, Title: "空白卷 · 家长讲题指南", CanonicalMarkdown: canonical, RenderMarkdown: frozenFinalPDFMarkdown(canonical, DeliveryAttachment{MIME: "image/jpeg", Data: image})}
	view, _, err := d.PreparePrintableArtifact(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	again, replay, err := d.PreparePrintableArtifact(context.Background(), request)
	if err != nil || !replay || real.calls != 1 || again.Render.ByteDigest != view.Render.ByteDigest || !bytes.Equal(again.Render.Payload, view.Render.Payload) {
		t.Fatalf("real PDF immutable replay calls=%d replay=%v err=%v", real.calls, replay, err)
	}
	dir := os.Getenv("HEXCLAW_TEST_PDF_EVIDENCE_DIR")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	pdf := filepath.Join(dir, "frozen-sixteen-guide.pdf")
	if err = os.WriteFile(pdf, view.Render.Payload, 0600); err != nil {
		t.Fatal(err)
	}
	textPath := filepath.Join(dir, "frozen-sixteen-guide.txt")
	if output, err := exec.Command("pdftotext", "-layout", pdf, textPath).CombinedOutput(); err != nil {
		t.Fatalf("extract actual PDF: %v %s", err, output)
	}
	text, err := os.ReadFile(textPath)
	if err != nil {
		t.Fatal(err)
	}
	readable := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, string(text))
	for _, label := range []string{"正确答案", "必要步骤", "本年级方法", "易错点", "家长怎么讲", "可以追问", "怎么检查"} {
		if strings.Count(readable, label) < 16 {
			t.Fatalf("actual PDF dropped guide fields: %s count=%d", label, strings.Count(readable, label))
		}
	}
	if !strings.Contains(readable, "x=24") {
		t.Fatal("actual PDF lost the elementary equation solution")
	}
	info, err := exec.Command("pdfinfo", pdf).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect actual PDF: %v", err)
	}
	images, err := exec.Command("pdfimages", "-list", pdf).CombinedOutput()
	if err != nil || len(strings.Split(strings.TrimSpace(string(images)), "\n")) < 3 {
		t.Fatalf("actual PDF lost source image: %v %s", err, images)
	}
	if err = os.WriteFile(filepath.Join(dir, "pdfinfo.txt"), info, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "pdfimages.txt"), images, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("real PDF bytes=%d canonical_bytes=%d immutable_digest=%s", len(view.Render.Payload), len(raw), view.Render.ByteDigest)
}

func (r *finalReplyPDFRenderer) Render(_ context.Context, markdown, format string) ([]byte, string, error) {
	r.calls++
	r.markdown = markdown
	return []byte("%PDF-1.7\nfrozen test payload\n%%EOF"), "application/pdf", r.err
}

func TestK12FinalPDFPolicyUsesVisibleCanonicalOnly(t *testing.T) {
	for _, tt := range []struct {
		name              string
		intent            k12.ImageTaskIntent
		count, characters int
		want              bool
	}{
		{"multiple_blank", k12.ImageTaskIntentBlankWorksheet, 2, 1, true},
		{"single_short_blank", k12.ImageTaskIntentBlankWorksheet, 1, 599, false},
		{"single_long_blank", k12.ImageTaskIntentBlankWorksheet, 1, 600, true},
		{"short_grade", k12.ImageTaskIntentCompletedHomework, 2, 599, false},
		{"long_grade", k12.ImageTaskIntentCompletedHomework, 2, 600, true},
		{"non_image_legacy", "", 2, 900, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := k12.GradingFinalArtifact{TotalCount: tt.count, CanonicalMarkdown: strings.Repeat("甲", tt.characters)}
			if got := k12FinalReplyUsesPDF(a, tt.intent); got != tt.want {
				t.Fatalf("PDF policy=%v want=%v", got, tt.want)
			}
		})
	}
	text := "**甲** [乙](https://example.invalid/" + strings.Repeat("target", 500) + ") <!--hidden--> ![](data:image/png;base64," + strings.Repeat("AAAA", 1000) + ") $x+1$"
	if got := K12FinalReplyReadableCharacters(text); got != 7 {
		t.Fatalf("visible characters=%d want 7", got)
	}
}

type finalReplyHTTPTransport struct {
	gradingFinalDeliveryTransport
	store          *k12storage.Store
	server         *httptest.Server
	uploadStatus   int
	visibleUnknown bool
	uploads        []string
	sends          []k12.DeliveryReceipt
	targets        []ResolvedDeliveryTarget
}

func newFinalReplyHTTPTransport(t *testing.T, store *k12storage.Store) *finalReplyHTTPTransport {
	t.Helper()
	f := &finalReplyHTTPTransport{store: store, uploadStatus: 200}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/upload" {
			if f.uploadStatus != 200 {
				w.WriteHeader(f.uploadStatus)
				return
			}
			_, _ = w.Write([]byte("prepared-media"))
			return
		}
		var receipt k12.DeliveryReceipt
		if err := json.NewDecoder(r.Body).Decode(&receipt); err != nil {
			t.Errorf("decode frozen part: %v", err)
			w.WriteHeader(400)
			return
		}
		batch, err := store.GetDeliveryBatch(context.Background(), receipt.AgentName, receipt.BatchID)
		if err != nil {
			t.Errorf("read durable batch before visible send: %v", err)
		}
		for _, part := range batch.Receipts {
			if part.PartKind == messagecontent.PartArtifact && part.PreparedResourceID == "" {
				t.Error("visible send preceded complete resource preparation")
			}
		}
		f.sends = append(f.sends, receipt)
		ack := DeliveryTransportAck{ExternalMessageID: fmt.Sprintf("local-OTO-%d", len(f.sends)), Status: k12.DeliveryDelivered}
		if f.visibleUnknown && receipt.PartMIME == "application/pdf" {
			ack = DeliveryTransportAck{Status: k12.DeliveryOutcomeUnknown}
		}
		_ = json.NewEncoder(w).Encode(ack)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *finalReplyHTTPTransport) ResolveTextTargets(ctx context.Context, agent string) ([]ResolvedDeliveryTarget, error) {
	if f.targets != nil {
		return f.targets, nil
	}
	return f.gradingFinalDeliveryTransport.ResolveTextTargets(ctx, agent)
}
func (f *finalReplyHTTPTransport) PrepareDeliveryPartResource(ctx context.Context, r k12.DeliveryReceipt) (string, error) {
	f.uploads = append(f.uploads, r.PartMIME)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, f.server.URL+"/upload", strings.NewReader(r.PayloadJSON))
	if err != nil {
		return "", err
	}
	response, err := f.server.Client().Do(request)
	if err != nil {
		return "", &adapter.ResourcePreparationError{Known: false, Cause: errors.New("local upload disconnected")}
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", &adapter.ResourcePreparationError{Known: response.StatusCode == 400, Cause: errors.New("local upload rejected or unknown")}
	}
	data, err := io.ReadAll(response.Body)
	return string(data), err
}
func (f *finalReplyHTTPTransport) SendPrepared(ctx context.Context, r k12.DeliveryReceipt) (DeliveryTransportAck, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return DeliveryTransportAck{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, f.server.URL+"/send", bytes.NewReader(raw))
	if err != nil {
		return DeliveryTransportAck{}, err
	}
	response, err := f.server.Client().Do(request)
	if err != nil {
		return DeliveryTransportAck{}, err
	}
	defer response.Body.Close()
	var ack DeliveryTransportAck
	err = json.NewDecoder(response.Body).Decode(&ack)
	return ack, err
}

func TestK12FinalPDFSharedSourceAndReplaySQLite(t *testing.T) {
	f := prepareCompletedSourceFixtureForIntent(t, k12.ImageTaskIntentBlankWorksheet)
	d := f.o.deps
	renderer := &finalReplyPDFRenderer{}
	d.Renderer = renderer
	transport := newFinalReplyHTTPTransport(t, f.store)
	d.Delivery = transport
	ctx := context.Background()
	printed, _, err := d.PrepareGradingFinalArtifactPDFExact(ctx, "mingming", f.old.ArtifactID, f.old.ArtifactDigest, K12FinalArtifactPDFTitle(f.old))
	if err != nil {
		t.Fatal(err)
	}
	if printed.Artifact.CanonicalMarkdown != f.old.CanonicalMarkdown || !strings.Contains(renderer.markdown, "![](data:image/png;base64,") {
		t.Fatal("PDF lost frozen canonical or owned source image")
	}
	batch, created, err := d.PrepareAndSendGradingFinalArtifactExact(ctx, "mingming", f.old.ArtifactID, f.old.ArtifactDigest)
	if err != nil || !created || batch.Status != k12.DeliveryBatchDelivered {
		t.Fatalf("first delivery created=%v status=%s err=%v", created, batch.Status, err)
	}
	if renderer.calls != 1 || len(transport.uploads) != 1 || len(transport.sends) != 2 {
		t.Fatalf("render/upload/send counts=%d/%d/%d", renderer.calls, len(transport.uploads), len(transport.sends))
	}
	if transport.sends[0].PartKind != messagecontent.PartMarkdown || transport.sends[1].PartMIME != "application/pdf" {
		t.Fatal("blank worksheet part order changed")
	}
	if len(transport.preparedMessages) != 1 || !strings.HasSuffix(transport.preparedMessages[0].Content, "完整答案与讲法见附件") {
		t.Fatal("structural summary missing fixed attachment guidance")
	}
	again, created, err := d.PrepareAndSendGradingFinalArtifactExact(ctx, "mingming", f.old.ArtifactID, f.old.ArtifactDigest)
	if err != nil || created || again.BatchID != batch.BatchID || renderer.calls != 1 || len(transport.uploads) != 1 || len(transport.sends) != 2 {
		t.Fatalf("replay repeated a boundary: batch=%s err=%v", again.BatchID, err)
	}
	stored, err := f.store.GetGradingFinalArtifact(ctx, "mingming", f.old.ArtifactID)
	if err != nil || stored.ArtifactDigest != f.old.ArtifactDigest || stored.CanonicalMarkdown != f.old.CanonicalMarkdown {
		t.Fatal("delivery altered original final artifact")
	}
}

func TestK12FinalPDFSourceDigestFamiliesSQLite(t *testing.T) {
	for _, scenario := range []string{"canonical_jpeg", "legacy_raw", "wrong_task_digest", "raw_sha_is_not_task_digest", "wrong_owner"} {
		t.Run(scenario, func(t *testing.T) {
			image := photoEXIF6JPEGFixture(t, 120, 80)
			rawSum := sha256.Sum256(image)
			rawDigest := hex.EncodeToString(rawSum[:])
			source := completedSourceImageFixture{
				data: image, rawReader: scenario == "legacy_raw", unannotated: true,
			}
			switch scenario {
			case "wrong_task_digest":
				source.frozenDigest = "sha256:" + strings.Repeat("0", 64)
			case "raw_sha_is_not_task_digest":
				source.frozenDigest = "sha256:" + rawDigest
			case "wrong_owner":
				source.frozenOwner = "unrelated-guardian"
			}
			f := prepareCompletedSourceFixtureForIntent(t, k12.ImageTaskIntentBlankWorksheet, source)
			d := f.o.deps
			renderer := &finalReplyPDFRenderer{}
			d.Renderer = renderer
			transport := newFinalReplyHTTPTransport(t, f.store)
			d.Delivery = transport
			ctx := context.Background()
			frozenSource, err := f.store.GetImageTaskDispatch(ctx, "mingming", f.dispatch)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("source_bytes=%d raw_sha256=%s frozen_task_digest=%s raw_reader=%v", len(image), rawDigest, frozenSource.SourceDigest, source.rawReader)
			snapshot := func() string {
				dispatch, err := f.store.GetImageTaskDispatch(ctx, "mingming", f.dispatch)
				if err != nil {
					t.Fatal(err)
				}
				owner, err := f.store.GetImageTaskOwnerScope(ctx, "mingming", f.dispatch)
				if err != nil {
					t.Fatal(err)
				}
				asset, err := f.store.GetReadyPageAsset(ctx, "guardian-final", "mingming", dispatch.SourceAssetRefs[0])
				if err != nil {
					t.Fatal(err)
				}
				final, err := f.store.GetGradingFinalArtifact(ctx, "mingming", f.old.ArtifactID)
				if err != nil {
					t.Fatal(err)
				}
				job, err := d.GetGradingJob(ctx, "mingming", f.job.Record.RecordID)
				if err != nil {
					t.Fatal(err)
				}
				value, err := json.Marshal(struct {
					Dispatch k12.ImageTaskDispatch
					Owner    string
					Asset    k12storage.PageAssetMetadata
					Final    k12.GradingFinalArtifact
					Job      GradingJobView
				}{dispatch, owner, asset, final, job})
				if err != nil {
					t.Fatal(err)
				}
				return string(value)
			}
			before := snapshot()
			printed, _, err := d.PrepareGradingFinalArtifactPDFExact(ctx, "mingming", f.old.ArtifactID, f.old.ArtifactDigest, K12FinalArtifactPDFTitle(f.old))
			valid := scenario == "canonical_jpeg" || scenario == "legacy_raw"
			if valid {
				if err != nil {
					t.Fatalf("owned frozen %s source must prepare PDF: %v", scenario, err)
				}
				if renderer.calls != 1 || printed.Artifact.CanonicalMarkdown != f.old.CanonicalMarkdown ||
					!strings.Contains(renderer.markdown, "![](data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(image)+")") ||
					!strings.HasSuffix(printed.Artifact.SourceRef, ":"+rawDigest+":"+imageFinalPDFRenderContractVersion) {
					t.Fatal("PDF did not preserve frozen body and original JPEG bytes/MIME/identity")
				}
				if again, replay, err := d.PrepareGradingFinalArtifactPDFExact(ctx, "mingming", f.old.ArtifactID, f.old.ArtifactDigest, K12FinalArtifactPDFTitle(f.old)); err != nil || !replay || renderer.calls != 1 || !bytes.Equal(again.Render.Payload, printed.Render.Payload) || again.Render.ByteDigest != printed.Render.ByteDigest {
					t.Fatalf("source family replay changed frozen PDF: replay=%v calls=%d err=%v", replay, renderer.calls, err)
				}
			} else {
				if err == nil || renderer.calls != 0 {
					t.Fatalf("invalid frozen source rendered: calls=%d err=%v", renderer.calls, err)
				}
				if scenario != "wrong_owner" && err.Error() != "final artifact image task digest mismatch" {
					t.Fatalf("source digest failure contract changed: %v", err)
				}
				for _, table := range []string{"k12_print_artifacts", "k12_print_artifact_renders"} {
					var count int
					if err := f.store.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
						t.Fatalf("invalid source persisted %s: rows=%d err=%v", table, count, err)
					}
				}
			}
			if snapshot() != before {
				t.Fatal("PDF preparation changed frozen dispatch/owner/PageAsset/final/job")
			}
			if len(transport.uploads) != 0 || len(transport.sends) != 0 || len(transport.preparedMessages) != 0 {
				t.Fatal("PDF source preparation crossed a delivery boundary")
			}
			for _, table := range []string{"k12_delivery_batches", "k12_delivery_receipts"} {
				var count int
				if err := f.store.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("PDF source preparation persisted %s: rows=%d err=%v", table, count, err)
				}
			}
		})
	}
}

func TestK12FinalPDFPreparationAndVisibleBoundariesSQLite(t *testing.T) {
	for _, mode := range []string{"known_upload", "unknown_upload", "visible_unknown", "local_renderer", "mixed_platform"} {
		t.Run(mode, func(t *testing.T) {
			f := prepareCompletedSourceFixtureForIntent(t, k12.ImageTaskIntentBlankWorksheet)
			d := f.o.deps
			renderer := &finalReplyPDFRenderer{}
			d.Renderer = renderer
			transport := newFinalReplyHTTPTransport(t, f.store)
			d.Delivery = transport
			switch mode {
			case "known_upload":
				transport.uploadStatus = 400
			case "unknown_upload":
				transport.uploadStatus = 503
			case "visible_unknown":
				transport.visibleUnknown = true
			case "local_renderer":
				renderer.err = errors.New("renderer unavailable")
			case "mixed_platform":
				targets, _ := transport.ResolveTextTargets(context.Background(), "mingming")
				targets = append(targets, ResolvedDeliveryTarget{BindingID: "other-platform", Target: k12.DeliveryTarget{Platform: "feishu", InstanceID: "bound-feishu", ChatID: "same-authorized-parent"}})
				transport.targets = targets
			}
			ctx := context.Background()
			batch, _, err := d.PrepareAndSendGradingFinalArtifactExact(ctx, "mingming", f.old.ArtifactID, f.old.ArtifactDigest)
			switch mode {
			case "known_upload":
				if err != nil || batch.Status != k12.DeliveryBatchDelivered || len(transport.uploads) != 1 || len(transport.sends) != 1 {
					t.Fatalf("known preparation did not preserve full body: status=%s uploads=%d sends=%d err=%v", batch.Status, len(transport.uploads), len(transport.sends), err)
				}
				var failedID string
				if e := f.store.DB().QueryRow(`SELECT batch_id FROM k12_delivery_receipts WHERE part_mime='application/pdf'`).Scan(&failedID); e != nil {
					t.Fatal(e)
				}
				failed, e := f.store.GetDeliveryBatch(ctx, "mingming", failedID)
				if e != nil || !k12storage.PDFPreparationFallbackAllowed(failed) || failedID == batch.BatchID {
					t.Fatalf("failed frozen plan was mutated: %v", e)
				}
			case "unknown_upload":
				if err == nil || batch.Status != k12.DeliveryBatchOutcomeUnknown || len(transport.uploads) != 1 || len(transport.sends) != 0 {
					t.Fatalf("unknown preparation caused external work: status=%s sends=%d err=%v", batch.Status, len(transport.sends), err)
				}
				for _, r := range batch.Receipts {
					if r.Attempt != 0 {
						t.Fatal("media reservation fabricated a visible send attempt")
					}
				}
			case "visible_unknown":
				if batch.Status != k12.DeliveryBatchOutcomeUnknown || len(transport.sends) != 2 {
					t.Fatalf("visible unknown status=%s sends=%d err=%v", batch.Status, len(transport.sends), err)
				}
			case "local_renderer", "mixed_platform":
				want := 1
				if mode == "mixed_platform" {
					want = 2
				}
				if err != nil || batch.Status != k12.DeliveryBatchDelivered || len(transport.uploads) != 0 || len(transport.sends) != want {
					t.Fatalf("full body compatibility status=%s sends=%d err=%v", batch.Status, len(transport.sends), err)
				}
			}
			if mode == "known_upload" || mode == "local_renderer" || mode == "mixed_platform" {
				if transport.preparedMessages[len(transport.preparedMessages)-1].Content != f.old.CanonicalMarkdown {
					t.Fatal("fallback truncated frozen full body")
				}
			}
			uploads, sends, renders := len(transport.uploads), len(transport.sends), renderer.calls
			again, _, _ := d.PrepareAndSendGradingFinalArtifactExact(ctx, "mingming", f.old.ArtifactID, f.old.ArtifactDigest)
			if again.BatchID != batch.BatchID || len(transport.uploads) != uploads || len(transport.sends) != sends || renderer.calls != renders {
				t.Fatal("replay changed frozen plan or repeated unknown/visible work")
			}
		})
	}
}

func TestK12FinalPDFGradeKeepsAnnotatedImageAndPreparationOrderSQLite(t *testing.T) {
	d, artifact, image := seedGradingFinalDeliveryArtifact(t, "# 作业批改结果\n\n完整的冻结批改说明")
	transport := newFinalReplyHTTPTransport(t, d.Records)
	d.Delivery = transport
	targets, _ := transport.ResolveTextTargets(context.Background(), artifact.AgentName)
	message := DeliveryMessage{Content: "# 作业批改结果\n\n完整答案与讲法见附件", Attachments: []DeliveryAttachment{{Name: "批注原图.png", MIME: "image/png", Data: image}, {Name: "作业讲解.pdf", MIME: "application/pdf", Data: []byte("%PDF-1.7\nfrozen\n%%EOF")}}}
	batch, _, err := d.PrepareAndSendMessageBatchForTargets(context.Background(), artifact.AgentName, k12.PrintSourceGradingFinalArtifact, artifact.ArtifactID+":"+imageFinalPDFRenderContractVersion, message, targets)
	if err != nil || batch.Status != k12.DeliveryBatchDelivered {
		t.Fatalf("grade PDF status=%s err=%v", batch.Status, err)
	}
	if strings.Join(transport.uploads, ",") != "application/pdf,image/png" || len(transport.sends) != 3 {
		t.Fatalf("preparation/sends=%v/%d", transport.uploads, len(transport.sends))
	}
	if transport.sends[0].PartKind != messagecontent.PartMarkdown || transport.sends[1].PartMIME != "image/png" || transport.sends[2].PartMIME != "application/pdf" || transport.sends[1].PartDigest != deliveryDigest(string(image)) {
		t.Fatal("grade image or visible order drifted")
	}
}
