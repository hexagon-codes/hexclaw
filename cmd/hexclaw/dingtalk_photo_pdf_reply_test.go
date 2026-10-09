package main

import (
	"context"
	"testing"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	k12 "github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type sharedPhotoPDFReplyPort struct {
	photoReplyBatchPortFake
	calls    int
	received k12usecase.DeliveryMessage
}

func (p *sharedPhotoPDFReplyPort) PrepareAndSendK12FinalReplyBatch(_ context.Context, agent, kind, id, finalID, digest string, message k12usecase.DeliveryMessage, targets []k12usecase.ResolvedDeliveryTarget) (k12.DeliveryBatch, bool, error) {
	p.calls++
	p.received = message
	target := targets[0]
	parts := []k12.DeliveryReceipt{{DeliveryID: "summary", PartKind: messagecontent.PartMarkdown, PartOrdinal: 1, BindingID: target.BindingID, Target: target.Target}}
	if len(message.Attachments) > 0 {
		parts = append(parts, k12.DeliveryReceipt{DeliveryID: "image", PartKind: messagecontent.PartArtifact, PartMIME: message.Attachments[0].MIME, PartOrdinal: 2, BindingID: target.BindingID, Target: target.Target})
	}
	parts = append(parts, k12.DeliveryReceipt{DeliveryID: "pdf", PartKind: messagecontent.PartArtifact, PartMIME: "application/pdf", PartDigest: "sha256:frozen-pdf", PartOrdinal: len(parts) + 1, BindingID: target.BindingID, Target: target.Target})
	return k12.DeliveryBatch{BatchID: "shared-pdf-batch", AgentName: agent, ObjectKind: kind, ObjectID: id, Receipts: parts}, true, nil
}

func TestDingTalkPhotoPDFUsesSharedFinalReplyPort(t *testing.T) {
	for _, intent := range []k12.ImageTaskIntent{k12.ImageTaskIntentCompletedHomework, k12.ImageTaskIntentBlankWorksheet} {
		t.Run(string(intent), func(t *testing.T) {
			port := &sharedPhotoPDFReplyPort{}
			command := photoReplyCommandFixture()
			command.TaskIntent = intent
			if intent == k12.ImageTaskIntentBlankWorksheet {
				command.Message.Attachments = nil
			}
			batch, _, err := newK12DingtalkPhotoReplyCoordinator(port).Deliver(context.Background(), command)
			if err != nil || port.calls != 1 || len(port.prepareCalls) != 0 || batch.Receipts[len(batch.Receipts)-1].PartMIME != "application/pdf" {
				t.Fatalf("shared PDF routing calls=%d legacy=%d err=%v", port.calls, len(port.prepareCalls), err)
			}
			if port.received.Content != command.Message.Content {
				t.Fatal("coordinator replaced frozen input with a local summary")
			}
			if intent == k12.ImageTaskIntentCompletedHomework && len(port.received.Attachments) != 1 {
				t.Fatal("coordinator lost grading image")
			}
		})
	}
}
