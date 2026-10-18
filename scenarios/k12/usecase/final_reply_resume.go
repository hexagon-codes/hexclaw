package usecase

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// ResumeFinalReplyBatch 先读原计划，恢复不重新打开原图、解析目标或渲染PDF。
func (d Deps) ResumeFinalReplyBatch(ctx context.Context, agentName, batchID string) (k12.DeliveryBatch, bool, error) {
	batch, err := d.GetDeliveryBatch(ctx, agentName, batchID)
	if err != nil {
		return k12.DeliveryBatch{AgentName: agentName, BatchID: batchID}, false, err
	}
	return d.resumeFinalReplyBatch(ctx, batch)
}

func (d Deps) resumeFinalReplyBatch(ctx context.Context, existing k12.DeliveryBatch) (k12.DeliveryBatch, bool, error) {
	if k12storage.PDFPreparationFallbackAllowed(existing) {
		if err := validateProjectedDeliveryBatch(existing); err != nil {
			return existing, false, err
		}
		full, err := frozenFinalReplyFullMessage(existing)
		if err != nil {
			return existing, false, err
		}
		return d.sendK12FinalReplyFallback(ctx, existing, full, deliveryBatchResolvedTargets(existing))
	}
	batch, err := d.QueryDeliveryBatch(ctx, existing.AgentName, existing.BatchID)
	if err != nil {
		if batch.BatchID == "" {
			batch = existing
		}
		return batch, false, err
	}
	result, err := d.sendDeliveryBatch(ctx, batch)
	if result.BatchID == "" {
		result = batch
	}
	return result, false, err
}

func frozenFinalReplyFullMessage(batch k12.DeliveryBatch) (DeliveryMessage, error) {
	if len(batch.Receipts) == 0 {
		return DeliveryMessage{}, errors.New("final reply frozen plan is empty")
	}
	var first struct {
		Source *k12.DeliveryProjectionSource `json:"delivery_source"`
	}
	if json.Unmarshal([]byte(batch.Receipts[0].PayloadJSON), &first) != nil || first.Source == nil {
		return DeliveryMessage{}, errors.New("final reply frozen source is missing")
	}
	source := first.Source
	full := DeliveryMessage{Content: source.Canonical.Markdown, Source: source, WritingFeedback: source.WritingFeedback}
	for _, ref := range source.Canonical.Attachments {
		found := false
		for _, r := range batch.Receipts {
			if r.PartDigest != ref.Digest || r.PartMIME != ref.MIME {
				continue
			}
			var payload struct {
				Attachment *DeliveryAttachment `json:"attachment"`
			}
			if json.Unmarshal([]byte(r.PayloadJSON), &payload) != nil || payload.Attachment == nil {
				continue
			}
			a := *payload.Attachment
			if a.Name != ref.Name || a.MIME != ref.MIME || deliveryDigest(string(a.Data)) != ref.Digest {
				return DeliveryMessage{}, errors.New("final reply frozen attachment identity mismatch")
			}
			full.Attachments = append(full.Attachments, a)
			found = true
			break
		}
		if !found {
			return DeliveryMessage{}, errors.New("final reply original attachment is missing")
		}
	}
	ding := full
	if source.DingTalkSuffix != "" {
		ding.Content += "\n\n" + source.DingTalkSuffix
	}
	ding.ProjectedContent = source.DingTalkFullProjection
	full.Projections = map[string]DeliveryMessage{"dingtalk": ding}
	return full, nil
}
