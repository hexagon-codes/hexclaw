package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/mathtext"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// FinalReplyDeliveryRequest 只投影已完成的真实结果，不生成新的业务或模型任务。
type FinalReplyDeliveryRequest struct {
	AgentName, ObjectKind, ObjectID string
	SourceKind, SourceRef, Title    string
	Message                         DeliveryMessage
	Producer                        messagecontent.ProducerKind
	Locale                          string
	Document                        bool
	PrivateEvent                    *k12.AcceptedPrivateReplySource
	PDF                             *PrintableArtifactView
	Summary                         string
	DingTalkSuffix                  string
	WritingFeedback                 bool
	RenderRevision                  string
	PDFStyleDigest                  string
	// 仅精确批改PDF路径可声明图已嵌入；规范源与其他渠道仍保留原图。
	GradingImageInPDF bool
}

// FinalReplyPDFMinCharacters 是钉钉长回复生成PDF的统一可读字符阈值。
const FinalReplyPDFMinCharacters = 1000

func FinalReplyUsesPDF(markdown string, document bool) bool {
	return document || K12FinalReplyReadableCharacters(markdown) >= FinalReplyPDFMinCharacters
}

func finalReplySource(req FinalReplyDeliveryRequest, message DeliveryMessage) (*k12.DeliveryProjectionSource, error) {
	refs := make([]messagecontent.AttachmentRef, 0, len(message.Attachments))
	for _, a := range message.Attachments {
		sum := sha256.Sum256(a.Data)
		raw := hex.EncodeToString(sum[:])
		refs = append(refs, messagecontent.AttachmentRef{AssetID: "inline:" + raw, Name: a.Name, MIME: a.MIME, Digest: "sha256:" + raw, AltText: a.Name})
	}
	canonical, err := messagecontent.New(req.Producer, req.Locale, message.Content, refs)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.SourceRef) == "" {
		return nil, errors.New("final reply source reference is required")
	}
	source := &k12.DeliveryProjectionSource{Version: k12.DeliveryProjectionVersionIMFinalPDFV1, SourceRef: req.SourceRef, Canonical: canonical, PrivateEvent: req.PrivateEvent, DingTalkSuffix: req.DingTalkSuffix, RenderRevision: req.RenderRevision}
	if req.PrivateEvent != nil {
		source.PreparationID = k12storage.FinalReplyPreparationID(req.AgentName, req.SourceRef, req.RenderRevision)
	}
	return source, nil
}

func finalReplyAttachmentName(title string) string {
	_, filename := render.SanitizeFilename(strings.ReplaceAll(title, "/", "-"), "pdf")
	return filename
}

func finalReplyPDFSummary(req FinalReplyDeliveryRequest) string {
	if req.Summary != "" {
		return strings.TrimSpace(req.Summary)
	}
	note := "完整内容见附件"
	switch req.Locale {
	case "en", "en-US":
		note = "See the attachment for the full content."
	case "ug", "ug-CN":
		note = "تولۇق مەزمۇننى قوشۇمچە ھۆججەتتىن كۆرۈڭ."
	}
	return mathtext.EscapeMarkdownInline(strings.TrimSpace(req.Title)) + "\n\n" + note
}

func finalReplyFullProjection(ctx context.Context, full DeliveryMessage, suffix string) DeliveryMessage {
	projected := full
	projected.Projections = nil
	if len(full.Attachments) > 0 {
		if visible, ok := CreativeWorkImageDeliveryProjection(ctx, full.Content); ok {
			projected.ProjectedContent = visible
		}
	}
	if suffix != "" {
		projected.Content += "\n\n" + suffix
		if projected.ProjectedContent != "" {
			projected.ProjectedContent += "\n\n" + suffix
		}
	}
	return projected
}

// PrepareAndSendFinalReply 在原批次中冻结各渠道完整计划；任何已接纳批次都接续原回执。
func (d Deps) PrepareAndSendFinalReply(ctx context.Context, req FinalReplyDeliveryRequest, targets []ResolvedDeliveryTarget) (k12.DeliveryBatch, bool, error) {
	if req.PrivateEvent != nil {
		return d.preparePrivateFinalReply(ctx, req, targets)
	}
	return d.prepareAndSendFinalReply(ctx, req, targets)
}

func (d Deps) prepareAndSendFinalReply(ctx context.Context, req FinalReplyDeliveryRequest, targets []ResolvedDeliveryTarget) (k12.DeliveryBatch, bool, error) {
	if d.Records == nil || d.Delivery == nil {
		return k12.DeliveryBatch{}, false, ErrDeliveryUnavailable
	}
	full, err := normalizeDeliveryMessage(req.Message)
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	full.WritingFeedback = req.WritingFeedback
	if strings.TrimSpace(req.Title) == "" {
		line, _, _ := strings.Cut(full.Content, "\n")
		line = mathtext.PlainMarkdownText(line)
		runes := []rune(line)
		if len(runes) > 60 {
			runes = runes[:60]
		}
		req.Title = string(runes)
	}
	if strings.TrimSpace(req.AgentName) == "" || strings.TrimSpace(req.ObjectKind) == "" || strings.TrimSpace(req.ObjectID) == "" {
		return k12.DeliveryBatch{}, false, errors.New("final reply identity is incomplete")
	}
	if !strings.HasSuffix(req.ObjectID, ":"+k12.DeliveryProjectionVersionIMFinalPDFV1) {
		req.ObjectID += ":" + k12.DeliveryProjectionVersionIMFinalPDFV1
	}
	if existing, lookupErr := d.Records.GetLatestDeliveryBatchForObject(ctx, req.AgentName, req.ObjectKind, req.ObjectID); lookupErr == nil {
		return d.resumeFinalReplyBatch(ctx, existing)
	} else if !errors.Is(lookupErr, records.ErrNotFound) {
		return k12.DeliveryBatch{}, false, lookupErr
	}
	if req.PDF == nil && req.PDFStyleDigest != "" && req.PDFStyleDigest != d.currentPDFStyleDigest() {
		return k12.DeliveryBatch{}, false, ErrDocumentStyleRevisionConflict
	}
	full.Source, err = finalReplySource(req, full)
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	full.Projections = map[string]DeliveryMessage{"dingtalk": finalReplyFullProjection(ctx, full, req.DingTalkSuffix)}
	full.Source.WritingFeedback = req.WritingFeedback
	full.Source.DingTalkFullProjection = full.Projections["dingtalk"].ProjectedContent
	fullFallback := full
	if targets == nil {
		targets, err = d.ResolveDeliveryTargets(ctx, req.AgentName)
		if err != nil {
			return k12.DeliveryBatch{}, false, err
		}
	}
	if len(targets) == 0 {
		return k12.DeliveryBatch{}, false, ErrNoActiveDirectBindings
	}
	hasDing := false
	for _, target := range targets {
		hasDing = hasDing || target.Target.Platform == "dingtalk"
	}
	if hasDing && FinalReplyUsesPDF(full.Content, req.Document) {
		pdf := req.PDF
		if pdf == nil {
			renderMarkdown := full.Content
			for i := len(full.Attachments) - 1; i >= 0; i-- {
				if strings.HasPrefix(full.Attachments[i].MIME, "image/") {
					renderMarkdown = frozenFinalPDFMarkdown(renderMarkdown, full.Attachments[i])
				}
			}
			printRequest := PreparePrintableArtifactRequest{AgentName: req.AgentName, SourceKind: req.SourceKind, SourceRef: req.SourceRef, Title: req.Title, CanonicalMarkdown: full.Content, RenderMarkdown: renderMarkdown}
			if req.RenderRevision != "" {
				printRequest.SourceRef += ":render-revision:" + deliveryDigest(req.RenderRevision)
			}
			var prepared PrintableArtifactView
			var prepareErr error
			if (req.SourceKind == k12.PrintSourceIMFinalReply && req.PrivateEvent != nil) || (req.SourceKind == "accumulation" && req.ObjectKind == "accumulation") {
				// 普通IM不是K12档案：完整PDF字节直接随原投递批次冻结，不伪造档案来源。
				artifact := buildPrintArtifact(printRequest, d.now())
				pdfRender, renderErr := d.renderPrintableArtifact(ctx, artifact, renderMarkdown)
				prepared, prepareErr = PrintableArtifactView{Artifact: artifact, Render: pdfRender}, renderErr
			} else {
				prepared, _, prepareErr = d.PreparePrintableArtifact(ctx, printRequest)
			}
			if prepareErr == nil {
				pdf = &prepared
			} else if req.PrivateEvent != nil {
				return k12.DeliveryBatch{}, false, prepareErr
			}
			// 此时尚无投递批次或可见发送，已知准备失败沿原全文继续。
		}
		if pdf != nil {
			if err := validateFinalReplyPDF(req, *pdf, full.Content); err != nil {
				return k12.DeliveryBatch{}, false, err
			}
			if len(pdf.Render.Payload) <= messagecontent.DingTalkMaxOutboundPDFBytes {
				projected := DeliveryMessage{Content: finalReplyPDFSummary(req), Source: full.Source, Attachments: append([]DeliveryAttachment(nil), full.Attachments...), WritingFeedback: req.WritingFeedback}
				if req.GradingImageInPDF && req.SourceKind == k12.PrintSourceGradingFinalArtifact && req.PDF != nil {
					if len(full.Attachments) != 1 || !strings.HasPrefix(full.Attachments[0].MIME, "image/") {
						return k12.DeliveryBatch{}, false, errors.New("grading PDF requires its original annotated image")
					}
					image := full.Attachments[0]
					full.Source.EmbeddedGradingImage = &k12.PDFEmbeddedGradingImage{Name: image.Name, MIME: image.MIME,
						Data: append([]byte(nil), image.Data...), PDFDigest: "sha256:" + pdf.Render.ByteDigest}
					projected.Attachments = nil
				}
				if req.DingTalkSuffix != "" {
					projected.Content += "\n\n" + req.DingTalkSuffix
				}
				projected.Attachments = append(projected.Attachments, DeliveryAttachment{Name: finalReplyAttachmentName(pdf.Artifact.Title), MIME: "application/pdf", Data: append([]byte(nil), pdf.Render.Payload...)})
				full.Projections = map[string]DeliveryMessage{"dingtalk": projected}
			}
		}
	}
	batch, created, err := d.PrepareAndSendMessageBatchForTargets(ctx, req.AgentName, req.ObjectKind, req.ObjectID, full, targets)
	if req.PrivateEvent == nil && k12storage.PDFPreparationFallbackAllowed(batch) {
		return d.sendK12FinalReplyFallback(ctx, batch, fullFallback, targets)
	}
	return batch, created, err
}

func validateFinalReplyPDF(req FinalReplyDeliveryRequest, pdf PrintableArtifactView, canonical string) error {
	if pdf.Artifact.AgentName != req.AgentName || pdf.Render.ArtifactID != pdf.Artifact.ArtifactID || pdf.Render.ContentType != "application/pdf" || pdf.Render.ByteSize != int64(len(pdf.Render.Payload)) || pdf.Render.ByteSize == 0 {
		return errors.New("final reply PDF identity mismatch")
	}
	sum := sha256.Sum256(pdf.Render.Payload)
	if pdf.Render.ByteDigest != hex.EncodeToString(sum[:]) {
		return errors.New("final reply PDF digest mismatch")
	}
	if req.SourceKind == k12.PrintSourceUnitSummary && req.PDF != nil {
		if pdf.Artifact.SourceKind != k12.PrintSourceUnitSummary {
			return errors.New("unit reply PDF source mismatch")
		}
	} else if req.SourceKind == k12.PrintSourceGradingFinalArtifact && req.PDF != nil {
		if pdf.Artifact.SourceKind != k12.PrintSourceGradingFinalArtifact || !strings.HasPrefix(strings.TrimSpace(canonical), strings.TrimSpace(pdf.Artifact.CanonicalMarkdown)) {
			return errors.New("grading reply PDF source mismatch")
		}
	} else if strings.TrimSpace(pdf.Artifact.CanonicalMarkdown) != strings.TrimSpace(canonical) {
		return errors.New("final reply PDF canonical mismatch")
	}
	return nil
}

func (d Deps) prepareProjectedMessageTargets(ctx context.Context, message DeliveryMessage, targets []ResolvedDeliveryTarget) ([]PreparedTextDelivery, error) {
	transport, ok := d.Delivery.(BatchMessageDeliveryTransport)
	if !ok {
		return nil, ErrDeliveryUnavailable
	}
	if err := message.Source.Canonical.Validate(); err != nil {
		return nil, err
	}
	var all []PreparedTextDelivery
	for _, target := range targets {
		projected := message
		if other, ok := message.Projections[target.Target.Platform]; ok {
			projected = other
		}
		projected.Source = message.Source
		projected.Projections = nil
		parts, err := transport.PrepareMessageForTargets(ctx, projected, []ResolvedDeliveryTarget{target})
		if err != nil {
			return nil, err
		}
		if len(parts) != 1+len(projected.Attachments) {
			return nil, errors.New("projected final reply parts are incomplete")
		}
		for i := range parts {
			part := normalizePreparedDelivery(parts[i])
			if part.BindingID != target.BindingID || part.Target != target.Target || part.PartOrdinal != i+1 {
				return nil, errors.New("projected final reply target identity changed")
			}
			kind, mime, digest := messagecontent.PartMarkdown, "", deliveryDigest(projected.Content)
			if i > 0 {
				a := projected.Attachments[i-1]
				kind, mime, digest = messagecontent.PartArtifact, a.MIME, deliveryDigest(string(a.Data))
			}
			if part.PartKind != kind || part.PartMIME != mime || part.PartDigest != digest {
				return nil, errors.New("projected final reply part identity changed")
			}
			// 完整来源与每目标平台载荷在同一不可变子回执里提交。
			var payload map[string]json.RawMessage
			if err = json.Unmarshal([]byte(part.PayloadJSON), &payload); err != nil {
				return nil, err
			}
			payload["delivery_source"], err = json.Marshal(message.Source)
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			part.PayloadJSON = string(raw)
			all = append(all, part)
		}
	}
	return all, nil
}
