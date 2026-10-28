package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type finalReplyPreparation struct {
	Request    FinalReplyDeliveryRequest `json:"request"`
	Targets    []ResolvedDeliveryTarget  `json:"targets"`
	PDFPayload []byte                    `json:"pdf_payload,omitempty"`
}

func decodeFinalReplyPreparation(event k12storage.OutboxEvent) (finalReplyPreparation, error) {
	var frozen finalReplyPreparation
	if event.EventType != k12storage.EventFinalReplyPreparation || event.PayloadVersion != 1 || json.Unmarshal([]byte(event.Payload), &frozen) != nil || frozen.Request.AgentName != event.AgentName || frozen.Request.SourceRef != event.AggregateID || frozen.Request.PrivateEvent == nil || event.EventID != k12storage.FinalReplyPreparationID(event.AgentName, event.AggregateID, frozen.Request.RenderRevision) {
		return frozen, errors.New("invalid final reply preparation")
	}
	if frozen.Request.PDF != nil {
		frozen.Request.PDF.Render.Payload = frozen.PDFPayload
	}
	return frozen, nil
}

func (d Deps) preparePrivateFinalReply(ctx context.Context, req FinalReplyDeliveryRequest, targets []ResolvedDeliveryTarget) (k12.DeliveryBatch, bool, error) {
	if d.Records == nil || d.Delivery == nil {
		return k12.DeliveryBatch{}, false, ErrDeliveryUnavailable
	}
	full, err := normalizeDeliveryMessage(req.Message)
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	projection, err := finalReplySource(req, full)
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	frozen := finalReplyPreparation{Request: req, Targets: targets}
	if req.PDF != nil {
		frozen.PDFPayload = req.PDF.Render.Payload
	}
	raw, err := json.Marshal(frozen)
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	event, err := d.Records.FreezeFinalReplyPreparation(ctx, req.AgentName, req.SourceRef, req.RenderRevision, string(raw), projection)
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	return d.executeFinalReplyPreparation(ctx, event)
}

func (d Deps) executeFinalReplyPreparation(ctx context.Context, event k12storage.OutboxEvent) (k12.DeliveryBatch, bool, error) {
	frozen, err := decodeFinalReplyPreparation(event)
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	batch, accepted, err := d.prepareAndSendFinalReply(ctx, frozen.Request, frozen.Targets)
	detail := ""
	if err != nil {
		detail = "document_preparation_or_delivery_failed"
	}
	if saveErr := d.Records.MarkFinalReplyPreparation(context.WithoutCancel(ctx), event.AgentName, event.EventID, err == nil && batch.Status == k12.DeliveryBatchDelivered, detail); saveErr != nil {
		err = errors.Join(err, saveErr)
	}
	return batch, accepted, err
}

// ResumeFinalReplyPreparation 接续原正文与目标快照，仅重做尚未完成的文件或投递阶段。
func (d Deps) ResumeFinalReplyPreparation(ctx context.Context, agent, assistantMessageID string) (k12.DeliveryBatch, bool, error) {
	event, err := d.Records.GetFinalReplyPreparation(ctx, agent, assistantMessageID, "")
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	return d.executeFinalReplyPreparation(ctx, event)
}

// ReexportFinalReply 为原结果创建显式排版修订，revisionKey 由宿主请求身份提供。
func (d Deps) ReexportFinalReply(ctx context.Context, agent, assistantMessageID, revisionKey string) (k12.DeliveryBatch, bool, error) {
	if revisionKey == "" {
		return k12.DeliveryBatch{}, false, fmt.Errorf("document revision key is required")
	}
	event, err := d.Records.GetFinalReplyPreparation(ctx, agent, assistantMessageID, "")
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	frozen, err := decodeFinalReplyPreparation(event)
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	if frozen.Request.PDF != nil {
		return k12.DeliveryBatch{}, false, fmt.Errorf("typed report re-export requires its original report projection")
	}
	frozen.Request.RenderRevision = revisionKey
	frozen.Request.ObjectID, err = k12.PrivateReplyRevisionObjectID(*frozen.Request.PrivateEvent, revisionKey)
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	return d.preparePrivateFinalReply(ctx, frozen.Request, frozen.Targets)
}

// FinalReplyPreparationConsumer 消费持久交付意图，不重新调用原助手或专业模型。
type FinalReplyPreparationConsumer struct{ Deps Deps }

func (FinalReplyPreparationConsumer) Name() string { return "final-reply-document-preparation" }
func (c FinalReplyPreparationConsumer) Handle(ctx context.Context, event k12storage.OutboxEvent) error {
	if event.EventType != k12storage.EventFinalReplyPreparation {
		return nil
	}
	batch, _, err := c.Deps.executeFinalReplyPreparation(ctx, event)
	if batch.Status == k12.DeliveryBatchOutcomeUnknown || errors.Is(err, ErrDeliveryQueryUnavailable) {
		return k12storage.ErrOutboxDeferred
	}
	if err == nil && batch.Status != k12.DeliveryBatchDelivered {
		return k12storage.ErrOutboxDeferred
	}
	return err
}
