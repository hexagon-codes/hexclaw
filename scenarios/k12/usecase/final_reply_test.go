package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/channel"
	"github.com/hexagon-codes/hexclaw/internal/mathtext"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func TestFinalReplyAutomaticTitleUsesReadableMarkdown(t *testing.T) {
	for _, tc := range []struct{ name, firstLine, title string }{
		{"actual bold answer", "**答案：这根绳子原来长40米。**从最后剩下的20米倒推。", "答案：这根绳子原来长40米。 从最后剩下的20米倒推。"},
		{"linked heading", "### **[教材讲解](https://example.invalid/book?page=36)**", "教材讲解"},
		{"literal code", "原样 `a*b` 与 `2 ** 3`", "原样 a*b 与 2 ** 3"},
		{"unicode cap", "## **" + strings.Repeat("题", 60) + "قايتا**", strings.Repeat("题", 60)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, _, renderer, req, targets := newFinalReplyContractFixture(t)
			req.Title, req.SourceKind, req.Locale = "", "accumulation", "zh-CN"
			req.Message.Content = tc.firstLine + "\n\n" + strings.Repeat("完整正文。", 240)
			batch, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
			if err != nil || len(batch.Receipts) != 2 || renderer.calls != 1 {
				t.Fatalf("PDF preparation changed: batch=%+v renders=%d err=%v", batch, renderer.calls, err)
			}
			for _, receipt := range batch.Receipts {
				payload := readFinalReplyContractPayload(t, batch, receipt)
				if payload.Source.Canonical.Markdown != req.Message.Content {
					t.Fatal("title projection changed original body")
				}
				want := tc.title + " 完整内容见附件"
				if got := mathtext.PlainMarkdownText(payload.Content.Markdown); got != want {
					t.Fatalf("summary lost literal semantics or retained markup: got=%q want=%q", got, want)
				}
			}
		})
	}
}

// 复用真实本地 HTTP 上传/发送边界，只让来源和渠道载荷经过正式 canonical 组装。
type finalReplyContractTransport struct {
	*finalReplyHTTPTransport
	preparations int
	queries      int
}

func (f *finalReplyContractTransport) PrepareTextForTargets(ctx context.Context, content string, targets []ResolvedDeliveryTarget) ([]PreparedTextDelivery, error) {
	return f.PrepareMessageForTargets(ctx, DeliveryMessage{Content: content}, targets)
}

func (f *finalReplyContractTransport) PrepareMessageForTargets(_ context.Context, message DeliveryMessage, targets []ResolvedDeliveryTarget) ([]PreparedTextDelivery, error) {
	f.preparations++
	if message.Source == nil {
		return nil, errors.New("final reply canonical source is missing")
	}
	attachments := make([]channel.Attachment, 0, len(message.Attachments))
	for _, attachment := range message.Attachments {
		attachments = append(attachments, channel.Attachment{Name: attachment.Name, MIME: attachment.MIME, Data: append([]byte(nil), attachment.Data...)})
	}
	canonical, err := channel.NewCanonicalMarkdownMessageWithAttachments(message.Source.Canonical.ProducerKind, message.Source.Canonical.Locale, message.Content, message.Content, "", attachments)
	if err != nil {
		return nil, err
	}
	parts, err := canonical.DeliveryParts()
	if err != nil {
		return nil, err
	}
	renderJSON, err := json.Marshal(canonical.RenderManifest)
	if err != nil {
		return nil, err
	}
	var prepared []PreparedTextDelivery
	for _, target := range targets {
		for _, part := range parts {
			payload, err := json.Marshal(part)
			if err != nil {
				return nil, err
			}
			prepared = append(prepared, PreparedTextDelivery{BindingID: target.BindingID, Target: target.Target, PartKind: part.Kind, PartMIME: part.MIME, PartOrdinal: part.Ordinal, PartDigest: part.Digest, PayloadJSON: string(payload), RenderJSON: string(renderJSON)})
		}
	}
	return prepared, nil
}

func (f *finalReplyContractTransport) QueryPrepared(context.Context, k12.DeliveryReceipt) (DeliveryTransportAck, error) {
	f.queries++
	return DeliveryTransportAck{Status: k12.DeliveryOutcomeUnknown}, nil
}

func newFinalReplyContractFixture(t *testing.T) (Deps, *k12storage.Store, *finalReplyContractTransport, *finalReplyPDFRenderer, FinalReplyDeliveryRequest, []ResolvedDeliveryTarget) {
	t.Helper()
	d, store := newPipeline(t, panicSolver{}, panicGrader{}, &fakeInsights{})
	transport := &finalReplyContractTransport{finalReplyHTTPTransport: newFinalReplyHTTPTransport(t, store)}
	renderer := &finalReplyPDFRenderer{}
	d.Delivery, d.Renderer = transport, renderer
	req := FinalReplyDeliveryRequest{AgentName: "mingming", ObjectKind: "accumulation", ObjectID: "current-final", SourceKind: k12.PrintSourceTutoringTips, SourceRef: "saved-final-source", Title: "Frozen final reply", Producer: messagecontent.ProducerSkill, Locale: "en", Message: DeliveryMessage{Content: strings.Repeat("甲", 1000)}}
	targets := []ResolvedDeliveryTarget{{BindingID: "existing-direct-binding", Target: k12.DeliveryTarget{Platform: "dingtalk", InstanceID: "stable-dingtalk", ChatID: "authorized-parent", Label: "dingtalk"}}}
	return d, store, transport, renderer, req, targets
}

type finalReplyContractPayload struct {
	Source     *k12.DeliveryProjectionSource  `json:"delivery_source"`
	Content    *messagecontent.MessageContent `json:"message_content"`
	Manifest   *messagecontent.RenderManifest `json:"render_manifest"`
	Text       string                         `json:"text"`
	Attachment *struct {
		Name, MIME string
		Data       []byte
	} `json:"attachment"`
}

type embeddedGradingTransport struct {
	*finalReplyContractTransport
	failPDF bool
}

func (f *embeddedGradingTransport) PrepareDeliveryPartResource(ctx context.Context, receipt k12.DeliveryReceipt) (string, error) {
	if f.failPDF && receipt.PartMIME == "application/pdf" {
		return "", &adapter.ResourcePreparationError{Known: true, Cause: errors.New("PDF upload rejected before send")}
	}
	return f.finalReplyContractTransport.PrepareDeliveryPartResource(ctx, receipt)
}

func TestGradingPDFEmbeddedImageDeliveryAndRecoverySQLite(t *testing.T) {
	for _, scenario := range []string{"mixed_targets", "render_failure", "upload_failure", "unknown", "frozen_old_plan"} {
		t.Run(scenario, func(t *testing.T) {
			d, store, transport, renderer, req, targets := newFinalReplyContractFixture(t)
			boundary := &embeddedGradingTransport{finalReplyContractTransport: transport}
			d.Delivery = boundary
			image := []byte("complete original annotated image")
			req.SourceKind, req.ObjectKind, req.Producer = k12.PrintSourceGradingFinalArtifact, "dingtalk_photo_grading_reply", messagecontent.ProducerK12
			req.GradingImageInPDF = true
			req.Message.Attachments = []DeliveryAttachment{{Name: "annotated.png", MIME: "image/png", Data: image}}
			if scenario == "mixed_targets" {
				targets = append(targets, ResolvedDeliveryTarget{BindingID: "other-direct-binding", Target: k12.DeliveryTarget{Platform: "feishu", InstanceID: "other-stable", ChatID: "other-parent"}})
			}
			pdf, _, err := d.PreparePrintableArtifact(t.Context(), PreparePrintableArtifactRequest{
				AgentName: req.AgentName, SourceKind: req.SourceKind, SourceRef: req.SourceRef,
				Title: req.Title, CanonicalMarkdown: req.Message.Content,
				RenderMarkdown: frozenFinalPDFMarkdown(req.Message.Content, req.Message.Attachments[0]),
			})
			if err != nil {
				t.Fatal(err)
			}
			req.PDF = &pdf
			switch scenario {
			case "render_failure":
				req.PDF, renderer.err = nil, errors.New("renderer unavailable")
				req.SourceRef += "-not-prepared"
			case "upload_failure":
				boundary.failPDF = true
			case "unknown":
				transport.visibleUnknown = true
			case "frozen_old_plan":
				req.GradingImageInPDF = false
			}
			batch, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "frozen_old_plan" {
				old := batch
				req.GradingImageInPDF = true
				batch, _, err = d.PrepareAndSendFinalReply(t.Context(), req, targets)
				if err != nil || !reflect.DeepEqual(batch, old) || len(batch.Receipts) != 3 {
					t.Fatalf("new policy changed old frozen plan: %v", err)
				}
			}
			for _, receipt := range batch.Receipts {
				payload := readFinalReplyContractPayload(t, batch, receipt)
				if payload.Source.Canonical.Markdown != req.Message.Content || len(payload.Source.Canonical.Attachments) != 1 || payload.Source.Canonical.Attachments[0].Digest != deliveryDigest(string(image)) {
					t.Fatal("image deduplication changed the original canonical source")
				}
				full := scenario == "render_failure" || scenario == "upload_failure" || receipt.Target.Platform != "dingtalk"
				if full {
					if payload.Content.Markdown != req.Message.Content || len(payload.Content.Attachments) < 1 || payload.Content.Attachments[0].MIME != "image/png" {
						t.Fatal("full-result path lost original text or image")
					}
				} else if scenario != "frozen_old_plan" && (len(payload.Content.Attachments) != 1 || payload.Content.Attachments[0].MIME != "application/pdf" || payload.Source.EmbeddedGradingImage == nil || !bytes.Equal(payload.Source.EmbeddedGradingImage.Data, image)) {
					t.Fatal("DingTalk PDF plan still sends image separately or cannot restore it")
				}
			}
			if scenario == "upload_failure" && len(transport.sends) != 2 {
				t.Fatalf("known zero-send PDF failure did not restore exactly text/image: %d", len(transport.sends))
			}
			beforeSends, beforeRenders := len(transport.sends), renderer.calls
			again, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
			if (err != nil && !(scenario == "unknown" && errors.Is(err, ErrDeliveryQueryUnavailable))) || again.BatchID != batch.BatchID || len(transport.sends) != beforeSends || renderer.calls != beforeRenders {
				t.Fatalf("replay resent or rerendered the original plan: %v", err)
			}
			if scenario == "unknown" && batch.Status != k12.DeliveryBatchOutcomeUnknown {
				t.Fatalf("unknown PDF was not retained as unknown: %s", batch.Status)
			}
			if _, err := store.GetDeliveryBatch(t.Context(), req.AgentName, batch.BatchID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func readFinalReplyContractPayload(t *testing.T, batch k12.DeliveryBatch, receipt k12.DeliveryReceipt) finalReplyContractPayload {
	t.Helper()
	var payload finalReplyContractPayload
	if err := json.Unmarshal([]byte(receipt.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Source == nil || payload.Content == nil || payload.Manifest == nil || payload.Source.Canonical.Validate() != nil || payload.Content.Validate() != nil || payload.Manifest.ValidateFor(*payload.Content) != nil {
		t.Fatal("persisted final reply is not a valid canonical channel payload")
	}
	if valid, err := k12.ValidateDeliveryProjection(batch.AgentName, batch.ObjectKind, batch.ObjectID, batch.ContentDigest, receipt.Target, receipt.BindingID, receipt.PayloadJSON); err != nil || !valid {
		t.Fatalf("persisted source proof failed: valid=%t err=%v", valid, err)
	}
	return payload
}

func TestFinalReplySharedPolicyVisibleBoundary(t *testing.T) {
	for _, tc := range []struct {
		chars         int
		document, pdf bool
	}{{999, false, false}, {1000, false, true}, {1, true, true}} {
		if got := FinalReplyUsesPDF(strings.Repeat("甲", tc.chars), tc.document); got != tc.pdf {
			t.Fatalf("visible chars=%d document=%t: PDF=%t", tc.chars, tc.document, got)
		}
	}
}

func TestFinalReplySharedShortReplyRemainsCompleteSQLite(t *testing.T) {
	d, store, transport, renderer, req, targets := newFinalReplyContractFixture(t)
	req.Message.Content = strings.Repeat("甲", 999)
	batch, created, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if err != nil || !created || batch.Status != k12.DeliveryBatchDelivered || len(batch.Receipts) != 1 || renderer.calls != 0 || len(transport.uploads) != 0 || len(transport.sends) != 1 {
		t.Fatalf("short final reply changed delivery shape: %+v %v", batch, err)
	}
	stored, err := store.GetDeliveryBatch(t.Context(), req.AgentName, batch.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	payload := readFinalReplyContractPayload(t, stored, stored.Receipts[0])
	if payload.Content.Markdown != req.Message.Content || payload.Source.Canonical.Markdown != req.Message.Content || payload.Content.ProducerKind != req.Producer || payload.Content.Locale != req.Locale || payload.Source.Canonical.SourceDigest != batch.ContentDigest {
		t.Fatal("short final reply changed canonical content, producer or locale")
	}
}

func TestFinalReplySharedMixedTargetsPreserveFullImageAndPDFSQLite(t *testing.T) {
	d, store, transport, renderer, req, targets := newFinalReplyContractFixture(t)
	image := validPNGFixture(t, "shared-final-image")
	req.Message.Attachments = []DeliveryAttachment{{Name: "original.png", MIME: "image/png", Data: image}}
	targets = append(targets, ResolvedDeliveryTarget{BindingID: "existing-feishu-binding", Target: k12.DeliveryTarget{Platform: "feishu", InstanceID: "stable-feishu", ChatID: "authorized-parent", Label: "feishu"}})
	batch, created, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if err != nil || !created || batch.Status != k12.DeliveryBatchDelivered || len(batch.Receipts) != 5 || renderer.calls != 1 || len(transport.uploads) != 3 || len(transport.sends) != 5 {
		t.Fatalf("mixed target delivery was incomplete: status=%s parts=%d render=%d upload=%d send=%d error=%v", batch.Status, len(batch.Receipts), renderer.calls, len(transport.uploads), len(transport.sends), err)
	}
	if !strings.Contains(renderer.markdown, "data:image/png;base64,") || !strings.Contains(renderer.markdown, req.Message.Content) {
		t.Fatal("PDF rendering lost the full text or original image")
	}
	var groups = map[string][]k12.DeliveryReceipt{}
	for _, receipt := range batch.Receipts {
		groups[receipt.Target.Platform] = append(groups[receipt.Target.Platform], receipt)
		payload := readFinalReplyContractPayload(t, batch, receipt)
		if payload.Source.Canonical.Markdown != req.Message.Content || payload.Source.Canonical.SourceDigest != batch.ContentDigest || payload.Content.Locale != "en" || payload.Content.ProducerKind != messagecontent.ProducerSkill {
			t.Fatal("platform projection changed full source or canonical identity")
		}
		if receipt.PartMIME == "image/png" && (payload.Attachment == nil || !bytes.Equal(payload.Attachment.Data, image)) {
			t.Fatal("platform projection dropped or modified original image bytes")
		}
		if receipt.Target.Platform == "dingtalk" {
			if payload.Content.Markdown != "Frozen final reply\n\nSee the attachment for the full content." || len(payload.Content.Attachments) != 2 {
				t.Fatal("DingTalk did not receive the complete summary-image-PDF plan")
			}
		} else if payload.Content.Markdown != req.Message.Content || len(payload.Content.Attachments) != 1 {
			t.Fatal("non-DingTalk target lost full text or received an extra PDF")
		}
	}
	for _, receipts := range groups {
		if err := k12.ValidateDeliveryProjectionTarget(batch, receipts); err != nil {
			t.Fatal(err)
		}
	}
	var artifactID string
	if err := store.DB().QueryRowContext(t.Context(), `SELECT artifact_id FROM k12_print_artifacts WHERE agent_name=?`, req.AgentName).Scan(&artifactID); err != nil {
		t.Fatal(err)
	}
	frozen, err := d.GetPrintableArtifactPDF(t.Context(), req.AgentName, artifactID)
	if err != nil || frozen.Artifact.CanonicalMarkdown != req.Message.Content {
		t.Fatalf("frozen full source/PDF missing: %v", err)
	}
	pdfPart := readFinalReplyContractPayload(t, batch, groups["dingtalk"][2])
	if pdfPart.Attachment == nil || !bytes.Equal(pdfPart.Attachment.Data, frozen.Render.Payload) {
		t.Fatal("delivery used a different PDF than the frozen same-source artifact")
	}
	again, created, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if err != nil || created || again.BatchID != batch.BatchID || renderer.calls != 1 || transport.preparations != 2 || len(transport.uploads) != 3 || len(transport.sends) != 5 {
		t.Fatalf("completed replay repeated preparation or sends: %+v %v", again, err)
	}
}

func TestFinalReplySharedExplicitDocumentReusesFrozenPDFSQLite(t *testing.T) {
	d, _, transport, renderer, req, targets := newFinalReplyContractFixture(t)
	frozen, _, err := d.PreparePrintableArtifact(t.Context(), PreparePrintableArtifactRequest{AgentName: req.AgentName, SourceKind: k12.PrintSourceUnitSummary, SourceRef: "doc-1/revision-2", Title: "Existing unit material", CanonicalMarkdown: "Complete immutable material body"})
	if err != nil {
		t.Fatal(err)
	}
	req.Message.Content, req.SourceKind, req.SourceRef, req.Title = "Saved material is ready.", k12.PrintSourceUnitSummary, "doc-1/revision-2", frozen.Artifact.Title
	req.Document, req.PDF, req.Summary = true, &frozen, req.Message.Content
	renderer.err = errors.New("a second render is forbidden")
	batch, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if err != nil || batch.Status != k12.DeliveryBatchDelivered || len(batch.Receipts) != 2 || renderer.calls != 1 || len(transport.uploads) != 1 || len(transport.sends) != 2 {
		t.Fatalf("short explicit document did not reuse the existing PDF: %+v %v", batch, err)
	}
	payload := readFinalReplyContractPayload(t, batch, batch.Receipts[1])
	if payload.Content.Markdown != req.Message.Content || payload.Source.Canonical.Markdown != req.Message.Content || payload.Attachment == nil || !bytes.Equal(payload.Attachment.Data, frozen.Render.Payload) {
		t.Fatal("document reuse replaced the real assistant reply or immutable PDF")
	}
}

func TestFinalReplySharedKnownRendererFailureFallsBackOnceSQLite(t *testing.T) {
	d, store, transport, renderer, req, targets := newFinalReplyContractFixture(t)
	renderer.err = errors.New("known renderer failure")
	batch, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if err != nil || batch.Status != k12.DeliveryBatchDelivered || len(batch.Receipts) != 1 || renderer.calls != 1 || len(transport.uploads) != 0 || len(transport.sends) != 1 {
		t.Fatalf("known renderer failure lost the complete reply: %+v %v", batch, err)
	}
	payload := readFinalReplyContractPayload(t, batch, batch.Receipts[0])
	if payload.Content.Markdown != req.Message.Content {
		t.Fatal("renderer failure fallback sent a summary without the missing PDF")
	}
	var frozen int
	if err := store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM k12_print_artifacts`).Scan(&frozen); err != nil || frozen != 0 {
		t.Fatalf("failed renderer produced a frozen artifact: %d %v", frozen, err)
	}
	again, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if err != nil || again.BatchID != batch.BatchID || renderer.calls != 1 || len(transport.sends) != 1 || transport.preparations != 1 {
		t.Fatalf("renderer fallback replay repeated work: %+v %v", again, err)
	}
}

func TestFinalReplySharedKnownUploadFailureKeepsOriginalPlanSQLite(t *testing.T) {
	d, store, transport, renderer, req, targets := newFinalReplyContractFixture(t)
	transport.uploadStatus = 400
	batch, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if err != nil || batch.Status != k12.DeliveryBatchDelivered || len(batch.Receipts) != 1 || renderer.calls != 1 || len(transport.uploads) != 1 || len(transport.sends) != 1 {
		t.Fatalf("known zero-send upload failure did not deliver full fallback: %+v %v", batch, err)
	}
	var oldID string
	if err := store.DB().QueryRowContext(t.Context(), `SELECT batch_id FROM k12_delivery_batches WHERE agent_name=? AND batch_id<>?`, req.AgentName, batch.BatchID).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	old, err := store.GetDeliveryBatch(t.Context(), req.AgentName, oldID)
	if err != nil || !k12storage.PDFPreparationFallbackAllowed(old) || old.ContentDigest != batch.ContentDigest || old.DedupeKey == batch.DedupeKey || old.ObjectID != batch.ObjectID || len(old.Receipts) != 2 {
		t.Fatalf("PDF and full fallback plans did not preserve distinct same-source identities: %+v %v", old, err)
	}
	for _, part := range old.Receipts {
		if part.Attempt != 0 || part.ExternalMessageID != "" || part.Status != k12.DeliveryFailed {
			t.Fatal("original failed PDF plan crossed the visible-send boundary")
		}
		readFinalReplyContractPayload(t, old, part)
	}
	if readFinalReplyContractPayload(t, batch, batch.Receipts[0]).Content.Markdown != req.Message.Content {
		t.Fatal("known upload fallback lost full text")
	}
	again, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	oldAfter, oldErr := store.GetDeliveryBatch(t.Context(), req.AgentName, oldID)
	if err != nil || oldErr != nil || again.BatchID != batch.BatchID || !reflect.DeepEqual(old, oldAfter) || renderer.calls != 1 || len(transport.uploads) != 1 || len(transport.sends) != 1 {
		t.Fatalf("fallback replay rewrote the original plan or resent: %+v %v %v", again, err, oldErr)
	}
}

func TestFinalReplySharedUnknownUploadIsQueryOnlySQLite(t *testing.T) {
	d, store, transport, renderer, req, targets := newFinalReplyContractFixture(t)
	transport.uploadStatus = 503
	batch, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if err == nil || batch.BatchID == "" || batch.Status != k12.DeliveryBatchOutcomeUnknown || renderer.calls != 1 || len(transport.uploads) != 1 || len(transport.sends) != 0 {
		t.Fatalf("unknown upload became a successful reply or blind fallback: %+v %v", batch, err)
	}
	before, err := store.GetDeliveryBatch(t.Context(), req.AgentName, batch.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	again, created, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if !errors.Is(err, ErrDeliveryQueryUnavailable) || created || again.BatchID != batch.BatchID || renderer.calls != 1 || transport.preparations != 1 || len(transport.uploads) != 1 || len(transport.sends) != 0 || transport.queries != 0 {
		t.Fatalf("unknown replay prepared/uploaded/sent instead of reading the existing unqueryable receipt: %+v %v", again, err)
	}
	after, err := store.GetDeliveryBatch(t.Context(), req.AgentName, batch.BatchID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("unqueryable upload record changed on replay")
	}
}

func TestFinalReplySharedPrivatePDFStoredOnlyInBatchSQLite(t *testing.T) {
	d, store, transport, renderer, req, targets := newFinalReplyContractFixture(t)
	proof := k12.AcceptedPrivateReplySource{AgentName: req.AgentName, Platform: "dingtalk", InstanceID: targets[0].Target.InstanceID, SenderID: targets[0].Target.ChatID, MessageID: "accepted-original-event", AssistantMessageID: "saved-assistant-final"}
	objectID, err := k12.PrivateReplyObjectID(proof)
	if err != nil {
		t.Fatal(err)
	}
	authorityID, err := k12.PrivateReplyAuthorityID(proof)
	if err != nil {
		t.Fatal(err)
	}
	req.ObjectKind, req.ObjectID, req.SourceKind, req.SourceRef, req.PrivateEvent = "im_final_reply", objectID, k12.PrintSourceIMFinalReply, proof.AssistantMessageID, &proof
	targets[0].BindingID = authorityID
	batch, accepted, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if err != nil || !accepted || batch.BatchID == "" || batch.Status != k12.DeliveryBatchDelivered || len(batch.Receipts) != 2 || renderer.calls != 1 || len(transport.uploads) != 1 || len(transport.sends) != 2 {
		t.Fatalf("ordinary private reply did not deliver the complete PDF plan: %+v %v", batch, err)
	}
	for _, table := range []string{"k12_print_artifacts", "k12_print_artifact_renders"} {
		var count int
		if err := store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("ordinary private reply created a K12 print source: %s=%d error=%v", table, count, err)
		}
	}
	stored, err := store.GetDeliveryBatch(t.Context(), req.AgentName, batch.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	pdf := readFinalReplyContractPayload(t, stored, stored.Receipts[1])
	wantPDF := []byte("%PDF-1.7\nfrozen test payload\n%%EOF")
	if pdf.Source.PrivateEvent == nil || *pdf.Source.PrivateEvent != proof || pdf.Attachment == nil || !bytes.Equal(pdf.Attachment.Data, wantPDF) || stored.Receipts[1].PartMIME != "application/pdf" || stored.Receipts[1].BindingID != authorityID {
		t.Fatal("private event authority or full renderer PDF was not frozen in the original batch")
	}
	if pdf.Source.Canonical.Markdown != req.Message.Content || renderer.markdown != req.Message.Content {
		t.Fatal("ordinary reply PDF did not preserve the complete assistant body")
	}
	again, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if err != nil || again.BatchID != batch.BatchID || renderer.calls != 1 || len(transport.uploads) != 1 || len(transport.sends) != 2 || transport.preparations != 1 {
		t.Fatalf("private batch replay repeated PDF generation or sends: %+v %v", again, err)
	}
}
