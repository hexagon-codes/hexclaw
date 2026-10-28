package k12

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/hexagon-codes/hexclaw/messagecontent"
)

const DeliveryProjectionVersionIMFinalPDFV1 = "im-final-pdf-v1"

// AcceptedPrivateReplySource 绑定已接纳的私聊事件及其实际助手回复，不构造持久绑定。
type AcceptedPrivateReplySource struct {
	AgentName          string `json:"agent_name"`
	Platform           string `json:"platform"`
	InstanceID         string `json:"instance_id"`
	SenderID           string `json:"sender_id"`
	MessageID          string `json:"message_id"`
	AssistantMessageID string `json:"assistant_message_id"`
}

// DeliveryProjectionSource 保存不同渠道投影共同对应的完整原文及原附件。
type DeliveryProjectionSource struct {
	Version                string                        `json:"version"`
	SourceRef              string                        `json:"source_ref"`
	Canonical              messagecontent.MessageContent `json:"canonical"`
	PrivateEvent           *AcceptedPrivateReplySource   `json:"private_event,omitempty"`
	DingTalkSuffix         string                        `json:"dingtalk_suffix,omitempty"`
	DingTalkFullProjection string                        `json:"dingtalk_full_projection,omitempty"`
	WritingFeedback        bool                          `json:"writing_feedback,omitempty"`
	EmbeddedGradingImage   *PDFEmbeddedGradingImage      `json:"embedded_grading_image,omitempty"`
	PreparationID          string                        `json:"preparation_id,omitempty"`
	RenderRevision         string                        `json:"render_revision,omitempty"`
}

// PDFEmbeddedGradingImage 保留已嵌入PDF的原图，供零发送的已知准备失败恢复。
type PDFEmbeddedGradingImage struct {
	Name      string `json:"name"`
	MIME      string `json:"mime"`
	Data      []byte `json:"data"`
	PDFDigest string `json:"pdf_digest"`
}

// HasEmbeddedGradingImagePDF 仅识别冻结图所对应的完整PDF可见集合。
func (s *DeliveryProjectionSource) HasEmbeddedGradingImagePDF(attachments []messagecontent.AttachmentRef) bool {
	return s != nil && s.EmbeddedGradingImage != nil && len(attachments) == 1 &&
		attachments[0].MIME == "application/pdf" && attachments[0].Digest == s.EmbeddedGradingImage.PDFDigest
}

func privateReplySourceDigest(source AcceptedPrivateReplySource) (string, error) {
	if source.Platform != "dingtalk" {
		return "", fmt.Errorf("private reply source platform is invalid")
	}
	for _, value := range []string{source.AgentName, source.InstanceID, source.SenderID, source.MessageID, source.AssistantMessageID} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || strings.ContainsRune(value, '\x00') {
			return "", fmt.Errorf("private reply source identity is incomplete")
		}
	}
	raw, err := json.Marshal(source)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(deliveryProjectionDigest(string(raw)), "sha256:"), nil
}

// PrivateReplyObjectID 由同一接纳事件及助手回复产生稳定投递对象身份。
func PrivateReplyObjectID(source AcceptedPrivateReplySource) (string, error) {
	digest, err := privateReplySourceDigest(source)
	if err != nil {
		return "", err
	}
	return "private-reply-" + digest + ":" + DeliveryProjectionVersionIMFinalPDFV1, nil
}

// PrivateReplyRevisionObjectID 为同一原结果的显式重排保留独立投递身份。
func PrivateReplyRevisionObjectID(source AcceptedPrivateReplySource, revision string) (string, error) {
	id, err := PrivateReplyObjectID(source)
	if err != nil || revision == "" {
		return id, err
	}
	if strings.TrimSpace(revision) != revision || len(revision) > 256 {
		return "", fmt.Errorf("document render revision is invalid")
	}
	sum := sha256.Sum256([]byte(revision))
	return strings.TrimSuffix(id, ":"+DeliveryProjectionVersionIMFinalPDFV1) + ":revision-" + hex.EncodeToString(sum[:]) + ":" + DeliveryProjectionVersionIMFinalPDFV1, nil
}

// PrivateReplyAuthorityID 在既有回执身份列中明确记录事件回送授权，不代表路由绑定实体。
func PrivateReplyAuthorityID(source AcceptedPrivateReplySource) (string, error) {
	digest, err := privateReplySourceDigest(source)
	if err != nil {
		return "", err
	}
	return "accepted-event:" + digest, nil
}

type deliveryProjectionPayload struct {
	Source     *DeliveryProjectionSource      `json:"delivery_source"`
	Content    *messagecontent.MessageContent `json:"message_content"`
	Manifest   *messagecontent.RenderManifest `json:"render_manifest"`
	Kind       messagecontent.PartKind        `json:"kind"`
	MIME       string                         `json:"mime,omitempty"`
	Ordinal    int                            `json:"ordinal"`
	Digest     string                         `json:"digest"`
	Text       string                         `json:"text,omitempty"`
	Attachment *struct {
		Name string `json:"Name"`
		MIME string `json:"MIME"`
		Data []byte `json:"Data"`
	} `json:"attachment,omitempty"`
}

func deliveryProjectionDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ValidateDeliveryProjection 仅完整新来源合同返回 true；未声明来源的旧载荷保持原规则。
func ValidateDeliveryProjection(batchAgent, objectKind, objectID, contentDigest string, target DeliveryTarget, bindingID, payloadJSON string) (bool, error) {
	fail := func() (bool, error) { return false, fmt.Errorf("delivery projection source proof is invalid") }
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(payloadJSON), &fields) != nil {
		if json.Valid([]byte(payloadJSON)) {
			return false, nil
		}
		return fail()
	}
	if _, exists := fields["delivery_source"]; !exists {
		return false, nil
	}
	var payload deliveryProjectionPayload
	if json.Unmarshal([]byte(payloadJSON), &payload) != nil || payload.Source == nil || payload.Content == nil || payload.Manifest == nil {
		return fail()
	}
	source := payload.Source
	if source.Version != DeliveryProjectionVersionIMFinalPDFV1 || strings.TrimSpace(source.SourceRef) == "" ||
		source.Canonical.Validate() != nil || source.Canonical.SourceDigest != contentDigest ||
		payload.Content.Validate() != nil || payload.Manifest.ValidateFor(*payload.Content) != nil ||
		payload.Content.ProducerKind != source.Canonical.ProducerKind || payload.Content.Locale != source.Canonical.Locale ||
		strings.TrimSpace(batchAgent) == "" || strings.TrimSpace(target.Platform) == "" || strings.TrimSpace(target.ChatID) == "" ||
		!strings.HasSuffix(objectID, ":"+DeliveryProjectionVersionIMFinalPDFV1) {
		return fail()
	}
	switch objectKind {
	case "im_final_reply", "creative_work", "accumulation", PrintSourceGradingFinalArtifact, "dingtalk_photo_grading_reply", "conversation_print_reply":
	default:
		return fail()
	}
	if source.PrivateEvent != nil {
		private := *source.PrivateEvent
		wantID, err := PrivateReplyRevisionObjectID(private, source.RenderRevision)
		authority, authorityErr := PrivateReplyAuthorityID(private)
		if err != nil || objectKind != "im_final_reply" || objectID != wantID || private.AgentName != batchAgent ||
			authorityErr != nil || bindingID != authority || target.Platform != private.Platform || target.InstanceID != private.InstanceID || target.ChatID != private.SenderID {
			return fail()
		}
	} else if objectKind == "im_final_reply" || strings.TrimSpace(bindingID) == "" || strings.HasPrefix(bindingID, "accepted-event:") {
		return fail()
	}
	originals := source.Canonical.Attachments
	if image := source.EmbeddedGradingImage; image != nil {
		if (objectKind != PrintSourceGradingFinalArtifact && objectKind != "dingtalk_photo_grading_reply") ||
			source.Canonical.ProducerKind != messagecontent.ProducerK12 || len(originals) != 1 ||
			!strings.HasPrefix(image.MIME, "image/") || image.Name != originals[0].Name || image.MIME != originals[0].MIME ||
			len(image.Data) == 0 || deliveryProjectionDigest(string(image.Data)) != originals[0].Digest ||
			len(image.PDFDigest) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(image.PDFDigest, "sha256:") {
			return fail()
		}
		if target.Platform == "dingtalk" && source.HasEmbeddedGradingImagePDF(payload.Content.Attachments) {
			originals = nil
		}
	}
	// 仅同摘要批改PDF可替代独立图，其余渠道与旧计划仍保留原附件顺序。
	if len(payload.Content.Attachments) < len(originals) {
		return fail()
	}
	for index, original := range originals {
		if payload.Content.Attachments[index] != original {
			return fail()
		}
	}
	fullText := payload.Content.Markdown == source.Canonical.Markdown
	if target.Platform == "dingtalk" && source.DingTalkSuffix != "" && payload.Content.Markdown == source.Canonical.Markdown+"\n\n"+source.DingTalkSuffix {
		fullText = true
	}
	if !fullText {
		hasAddedPDF := false
		for _, attachment := range payload.Content.Attachments[len(originals):] {
			if attachment.MIME == "application/pdf" {
				hasAddedPDF = true
			}
		}
		if !hasAddedPDF {
			return fail()
		}
	}
	return true, nil
}

// ValidateDeliveryProjectionTarget 共用完整目标校验，不能用部分附件回执代表完整投递计划。
func ValidateDeliveryProjectionTarget(batch DeliveryBatch, parts []DeliveryReceipt) error {
	fail := func() error { return fmt.Errorf("delivery projection target manifest is incomplete or inconsistent") }
	if len(parts) == 0 {
		return fail()
	}
	var first deliveryProjectionPayload
	for index, receipt := range parts {
		proved, err := ValidateDeliveryProjection(batch.AgentName, batch.ObjectKind, batch.ObjectID, batch.ContentDigest, receipt.Target, receipt.BindingID, receipt.PayloadJSON)
		if err != nil || !proved || receipt.AgentName != batch.AgentName || receipt.ObjectKind != batch.ObjectKind || receipt.ObjectID != batch.ObjectID ||
			receipt.BatchID != batch.BatchID || receipt.Target != parts[0].Target || receipt.BindingID != parts[0].BindingID ||
			receipt.PartOrdinal != index+1 || receipt.BatchOrdinal != parts[0].BatchOrdinal+index || receipt.PayloadDigest != deliveryProjectionDigest(receipt.PayloadJSON) {
			return fail()
		}
		var payload deliveryProjectionPayload
		if json.Unmarshal([]byte(receipt.PayloadJSON), &payload) != nil {
			return fail()
		}
		if index == 0 {
			first = payload
		}
		manifestJSON, _ := json.Marshal(payload.Manifest)
		if !reflect.DeepEqual(payload.Source, first.Source) || !reflect.DeepEqual(payload.Content, first.Content) || !reflect.DeepEqual(payload.Manifest, first.Manifest) ||
			string(manifestJSON) != receipt.RenderJSON || len(payload.Manifest.Parts) != len(parts) || len(parts) != len(payload.Content.Attachments)+1 ||
			payload.Kind != receipt.PartKind || payload.Ordinal != receipt.PartOrdinal || payload.MIME != receipt.PartMIME || payload.Digest != receipt.PartDigest {
			return fail()
		}
		selected := payload.Manifest.Parts[index]
		if selected.Kind != payload.Kind {
			return fail()
		}
		if index == 0 {
			if payload.Kind != messagecontent.PartMarkdown || payload.MIME != "" || payload.Attachment != nil || payload.Text != selected.Text ||
				payload.Digest != deliveryProjectionDigest(payload.Content.Markdown) {
				return fail()
			}
			continue
		}
		attachment := payload.Content.Attachments[index-1]
		if payload.Kind != messagecontent.PartArtifact || payload.Text != "" || payload.Attachment == nil || payload.Attachment.Name != attachment.Name ||
			payload.Attachment.MIME != attachment.MIME || payload.MIME != attachment.MIME || payload.Digest != attachment.Digest ||
			deliveryProjectionDigest(string(payload.Attachment.Data)) != payload.Digest || selected.ArtifactRef != attachment.AssetID ||
			selected.ArtifactDigest != attachment.Digest || selected.AltText != attachment.AltText {
			return fail()
		}
	}
	return nil
}
