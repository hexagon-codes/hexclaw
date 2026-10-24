package usecase

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/requestcancel"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// PrintReplyConsumer 复用现有 Outbox 与 FinalReply 批次，绝不重新渲染或求解。
type PrintReplyConsumer struct{ Deps Deps }

type ConversationPrintReplyView struct {
	Source k12.ConversationPrintReply `json:"source"`
	Batch  *k12.DeliveryBatch         `json:"batch,omitempty"`
	State  string                     `json:"state"`
}

// GetConversationPrintReply 读取持久交付事实，不因读取触发发送或重新生成。
func (d Deps) GetConversationPrintReply(ctx context.Context, source k12.ConversationPrintReply) (ConversationPrintReplyView, error) {
	view := ConversationPrintReplyView{Source: source, State: "pending"}
	if err := d.validateConversationPrintReplySource(ctx, source); err != nil {
		return view, err
	}
	kind, id, _, _, err := conversationPrintReplyIdentity(source)
	if err != nil {
		return view, err
	}
	batch, err := d.Records.GetLatestDeliveryBatchForObject(ctx, source.AgentName, kind, id)
	if errors.Is(err, records.ErrNotFound) {
		return d.checkConversationPrintReplyView(ctx, view)
	}
	if err != nil {
		return view, err
	}
	view.Batch = &batch
	view.State = string(batch.Status)
	return d.checkConversationPrintReplyView(ctx, view)
}

func (d Deps) checkConversationPrintReplyView(ctx context.Context, view ConversationPrintReplyView) (ConversationPrintReplyView, error) {
	record, err := d.Records.ConversationPrintReplyMessage(ctx, view.Source)
	if err != nil {
		return view, err
	}
	err = d.checkConversationReplyAttachments(ctx, view.Source.Owner, record.Metadata)
	if errors.Is(err, requestcancel.ErrUserCancelled) {
		// 已发送及结果未知的回执保持事实，尚未物理执行的计划可明确显示停止。
		if view.Batch == nil || view.Batch.Status == k12.DeliveryBatchPending || view.Batch.Status == k12.DeliveryBatchFailed {
			view.State = "cancelled"
		}
		return view, nil
	}
	return view, err
}

func (d Deps) checkConversationReplyAttachments(ctx context.Context, owner, metadata string) error {
	var saved map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &saved); err != nil {
		return err
	}
	if assets := saved["media_assets"]; len(assets) == 0 || string(assets) == "null" || string(assets) == "[]" {
		return nil
	}
	if d.ReplyAttachments == nil {
		return fmt.Errorf("reply attachment resolver is unavailable")
	}
	_, err := d.ReplyAttachments(ctx, owner, metadata)
	return err
}

// checkConversationPrintReplyAttempt 使原批次的首次发送和任何恢复入口遵从同一媒体停止意图。
func (d Deps) checkConversationPrintReplyAttempt(ctx context.Context, receipt k12.DeliveryReceipt) error {
	if receipt.ObjectKind != "conversation_print_reply" && receipt.ObjectKind != "im_final_reply" {
		return nil
	}
	var payload struct {
		Source *k12.DeliveryProjectionSource `json:"delivery_source"`
	}
	if json.Unmarshal([]byte(receipt.PayloadJSON), &payload) != nil || payload.Source == nil || payload.Source.SourceRef == "" {
		return nil
	}
	source, err := d.Records.GetConversationPrintReply(ctx, receipt.AgentName, payload.Source.SourceRef)
	if errors.Is(err, records.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	record, err := d.Records.ConversationPrintReplyMessage(ctx, source)
	if err != nil {
		return err
	}
	return d.checkConversationReplyAttachments(ctx, source.Owner, record.Metadata)
}

func (d Deps) validateConversationPrintReplySource(ctx context.Context, source k12.ConversationPrintReply) error {
	actual, err := d.Records.GetConversationPrintReply(ctx, source.AgentName, source.AssistantMessageID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, source) {
		return fmt.Errorf("print reply persisted source mismatch")
	}
	return nil
}

func conversationPrintReplyIdentity(source k12.ConversationPrintReply) (kind, id, binding string, proof *k12.AcceptedPrivateReplySource, err error) {
	if source.BindingID != "" {
		return "conversation_print_reply", source.AssistantMessageID + ":" + k12.DeliveryProjectionVersionIMFinalPDFV1, source.BindingID, nil, nil
	}
	proof = &k12.AcceptedPrivateReplySource{AgentName: source.AgentName, Platform: source.Platform, InstanceID: source.InstanceID, SenderID: source.Owner, MessageID: source.SourceMessageID, AssistantMessageID: source.AssistantMessageID}
	id, err = k12.PrivateReplyObjectID(*proof)
	if err != nil {
		return
	}
	binding, err = k12.PrivateReplyAuthorityID(*proof)
	kind = "im_final_reply"
	return
}

func (PrintReplyConsumer) Name() string { return "conversation-print-replies" }
func (c PrintReplyConsumer) Handle(ctx context.Context, event k12storage.OutboxEvent) error {
	if event.EventType != k12.EventConversationPrintReply {
		return nil
	}
	var source k12.ConversationPrintReply
	if event.PayloadVersion != 1 || json.Unmarshal([]byte(event.Payload), &source) != nil || source.AgentName != event.AgentName || source.AssistantMessageID != event.AggregateID {
		return fmt.Errorf("invalid conversation print reply event")
	}
	batch, err := c.Deps.SendConversationPrintReply(ctx, source)
	if errors.Is(err, requestcancel.ErrUserCancelled) {
		return nil
	}
	if err == nil && (batch.Status == k12.DeliveryBatchPending || batch.Status == k12.DeliveryBatchSending) {
		return k12storage.ErrOutboxDeferred
	}
	return err
}

// SendConversationPrintReply 统一投递冻结到原回合的 PDF 和其他附件；重复执行接续原批次。
func (d Deps) SendConversationPrintReply(ctx context.Context, source k12.ConversationPrintReply) (k12.DeliveryBatch, error) {
	if err := d.validateConversationPrintReplySource(ctx, source); err != nil {
		return k12.DeliveryBatch{}, err
	}
	objectKind, objectID, bindingID, proof, err := conversationPrintReplyIdentity(source)
	if err != nil {
		return k12.DeliveryBatch{}, err
	}
	request := FinalReplyDeliveryRequest{AgentName: source.AgentName, SourceRef: source.AssistantMessageID, Producer: messagecontent.ProducerChat, Locale: source.Locale}
	request.PrivateEvent = proof
	if previous, getErr := d.Records.GetLatestDeliveryBatchForObject(ctx, source.AgentName, objectKind, objectID); getErr == nil {
		batch, _, resumeErr := d.ResumeFinalReplyBatch(ctx, source.AgentName, previous.BatchID)
		return batch, resumeErr
	} else if !errors.Is(getErr, records.ErrNotFound) {
		return k12.DeliveryBatch{}, getErr
	}
	record, err := d.Records.ConversationPrintReplyMessage(ctx, source)
	if err != nil {
		return k12.DeliveryBatch{}, err
	}
	message := DeliveryMessage{Content: record.Content}
	for _, ref := range source.Artifacts {
		actual, _, err := d.ResolvePrintReplyArtifact(ctx, source.AgentName, ref.ArtifactID)
		if err != nil {
			return k12.DeliveryBatch{}, err
		}
		if actual != ref {
			return k12.DeliveryBatch{}, fmt.Errorf("print reply frozen reference mismatch")
		}
		pdf, err := d.GetPrintableArtifactPDF(ctx, source.AgentName, ref.ArtifactID)
		if err != nil {
			return k12.DeliveryBatch{}, err
		}
		message.Attachments = append(message.Attachments, DeliveryAttachment{Name: ref.Name, MIME: "application/pdf", Data: pdf.Render.Payload})
	}
	var savedMetadata map[string]json.RawMessage
	if err := json.Unmarshal([]byte(record.Metadata), &savedMetadata); err != nil {
		return k12.DeliveryBatch{}, err
	}
	if assets := savedMetadata["media_assets"]; len(assets) != 0 && string(assets) != "null" && string(assets) != "[]" {
		if d.ReplyAttachments == nil {
			return k12.DeliveryBatch{}, fmt.Errorf("reply attachment resolver is unavailable")
		}
		attachments, err := d.ReplyAttachments(ctx, source.Owner, record.Metadata)
		if err != nil {
			return k12.DeliveryBatch{}, err
		}
		if len(attachments) == 0 {
			return k12.DeliveryBatch{}, fmt.Errorf("frozen reply attachments are missing")
		}
		for _, attachment := range attachments {
			if attachment.Source == nil || attachment.Source.OpenReader == nil {
				return k12.DeliveryBatch{}, fmt.Errorf("frozen reply attachment source is unavailable")
			}
			reader, err := attachment.Source.OpenReader(ctx)
			if err != nil {
				return k12.DeliveryBatch{}, err
			}
			raw, readErr := io.ReadAll(reader)
			if err := errors.Join(readErr, reader.Close()); err != nil {
				return k12.DeliveryBatch{}, err
			}
			sum := sha256.Sum256(raw)
			if int64(len(raw)) != attachment.Size || "sha256:"+hex.EncodeToString(sum[:]) != attachment.ContentDigest {
				return k12.DeliveryBatch{}, fmt.Errorf("frozen reply attachment digest mismatch")
			}
			message.Attachments = append(message.Attachments, DeliveryAttachment{Name: attachment.Name, MIME: attachment.Mime, Data: raw})
		}
	}
	message.Source, err = finalReplySource(request, message)
	if err != nil {
		return k12.DeliveryBatch{}, err
	}
	reply := &adapter.Reply{Content: record.Content, Metadata: make(map[string]string)}
	for key, raw := range savedMetadata {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			reply.Metadata[key] = value
		}
	}
	_ = json.Unmarshal(savedMetadata["tool_calls"], &reply.ToolCalls)
	if suffix := adapter.DingTalkReplyFooter(reply, source.Locale); suffix != "" {
		message.Source.DingTalkSuffix = suffix
		projected := message
		projected.Content += "\n\n" + suffix
		projected.Projections = nil
		message.Projections = map[string]DeliveryMessage{"dingtalk": projected}
	}
	targets := []ResolvedDeliveryTarget{{BindingID: bindingID, Target: k12.DeliveryTarget{Platform: source.Platform, InstanceID: source.InstanceID, ChatID: source.ChatID}}}
	batch, _, err := d.PrepareAndSendMessageBatchForTargets(ctx, source.AgentName, objectKind, objectID, message, targets)
	return batch, err
}
