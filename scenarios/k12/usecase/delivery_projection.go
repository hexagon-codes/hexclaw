package usecase

import (
	"encoding/json"
	"errors"

	"github.com/hexagon-codes/hexclaw/messagecontent"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 同一来源的PDF与全文退路是不同冻结计划；逻辑命令重放仍先按对象读取原计划。
func projectedDeliveryBatchDedupeKey(batch k12.DeliveryBatch) string {
	variant := "full"
	for _, r := range batch.Receipts {
		var payload struct {
			Source  *k12.DeliveryProjectionSource  `json:"delivery_source"`
			Content *messagecontent.MessageContent `json:"message_content"`
		}
		_ = json.Unmarshal([]byte(r.PayloadJSON), &payload)
		if r.PartMIME == "application/pdf" && payload.Source != nil &&
			(r.PartOrdinal > len(payload.Source.Canonical.Attachments)+1 ||
				(payload.Content != nil && payload.Source.HasEmbeddedGradingImagePDF(payload.Content.Attachments))) {
			variant = "pdf"
			break
		}
	}
	return deliveryBatchDedupeKey(batch.AgentName, batch.ObjectKind, batch.ObjectID, batch.ContentDigest+"\x00"+variant)
}

// 新计划仍以原批次/部件去重算法校验来源及逐目标完整性。
func validateProjectedDeliveryBatch(batch k12.DeliveryBatch) error {
	groups := map[string][]k12.DeliveryReceipt{}
	projected := 0
	for _, r := range batch.Receipts {
		present, err := k12.ValidateDeliveryProjection(batch.AgentName, batch.ObjectKind, batch.ObjectID, batch.ContentDigest, r.Target, r.BindingID, r.PayloadJSON)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		projected++
		if r.DedupeKey != deliveryPartDedupeKey(r.AgentName, r.ObjectKind, r.ObjectID, r.BindingID, r.PartKind, r.PartMIME, r.PartOrdinal, r.PartDigest, r.PayloadDigest) {
			return errors.New("final reply part dedupe identity mismatch")
		}
		key := deliveryEnvelopeTargetKey(r)
		groups[key] = append(groups[key], r)
	}
	if projected == 0 {
		return nil
	}
	if projected != len(batch.Receipts) || batch.DedupeKey != projectedDeliveryBatchDedupeKey(batch) {
		return errors.New("final reply batch source identity mismatch")
	}
	for _, parts := range groups {
		if err := k12.ValidateDeliveryProjectionTarget(batch, parts); err != nil {
			return err
		}
	}
	return nil
}
