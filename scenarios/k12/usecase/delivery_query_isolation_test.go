package usecase_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestDeliveryBatchQueryUnavailableDoesNotBlockIndependentImage(t *testing.T) {
	d := newDataDeps(t)
	transport := newMessagePartTransport()
	transport.targets = batchTargets()[:1]
	bindingID := transport.targets[0].BindingID
	transport.sendPlans[bindingID+"/1"] = []usecase.DeliveryTransportAck{{
		Status: k12.DeliveryOutcomeUnknown,
		Detail: "provider timed out before returning a query identifier",
	}}
	transport.sendPlans[bindingID+"/2"] = []usecase.DeliveryTransportAck{{
		Status: k12.DeliverySending, ExternalMessageID: "image-query-key",
	}}
	d.Delivery = transport
	message := messageWithImageAndPDF()
	message.Attachments = message.Attachments[:1]
	ctx := context.Background()

	batch, created, err := d.PrepareAndSendMessageBatch(
		ctx, "xiaoming", "practice_set", "independent-image-query", message,
	)
	if err != nil || !created || batch.Status != k12.DeliveryBatchOutcomeUnknown || len(batch.Receipts) != 2 {
		t.Fatalf("prepare unknown text and in-flight image: created=%v batch=%+v err=%v", created, batch, err)
	}
	textReceipt, imageReceipt := batch.Receipts[0], batch.Receipts[1]
	if textReceipt.Status != k12.DeliveryOutcomeUnknown || textReceipt.ExternalMessageID != "" ||
		imageReceipt.Status != k12.DeliverySending || imageReceipt.ExternalMessageID != "image-query-key" ||
		imageReceipt.PartMIME != "image/png" || len(transport.sends) != 2 {
		t.Fatalf("unexpected initial receipt evidence: receipts=%+v sends=%d", batch.Receipts, len(transport.sends))
	}

	for query := 1; query <= 2; query++ {
		current, queryErr := d.QueryDeliveryBatch(ctx, "xiaoming", batch.BatchID)
		if !errors.Is(queryErr, usecase.ErrDeliveryQueryUnavailable) {
			t.Fatalf("query %d must retain the unqueryable text error: %v", query, queryErr)
		}
		if current.Status != k12.DeliveryBatchOutcomeUnknown || len(current.Receipts) != 2 {
			t.Fatalf("query %d must not mark the entire batch delivered: %+v", query, current)
		}
		if !reflect.DeepEqual(current.Receipts[0], textReceipt) {
			t.Fatalf("query %d changed the unknown text receipt: before=%+v after=%+v", query, textReceipt, current.Receipts[0])
		}
		if current.Receipts[1].DeliveryID != imageReceipt.DeliveryID ||
			current.Receipts[1].Status != k12.DeliveryDelivered ||
			current.Receipts[1].ExternalMessageID != imageReceipt.ExternalMessageID ||
			current.Receipts[1].Attempt != 1 {
			t.Fatalf("query %d did not independently resolve the original image: %+v", query, current.Receipts[1])
		}
		if len(transport.sends) != 2 || len(transport.queries) != 1 ||
			transport.queries[0].DeliveryID != imageReceipt.DeliveryID {
			t.Fatalf("query %d resent a part or queried a completed/unqueryable part: sends=%+v queries=%+v", query, transport.sends, transport.queries)
		}
		stored, readErr := d.GetDeliveryBatch(ctx, "xiaoming", batch.BatchID)
		if readErr != nil || !reflect.DeepEqual(stored, current) {
			t.Fatalf("query %d did not persist the returned receipt states: stored=%+v current=%+v err=%v", query, stored, current, readErr)
		}
	}
}
