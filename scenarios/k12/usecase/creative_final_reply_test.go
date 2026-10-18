package usecase

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 保留正式 canonical 与本地 HTTP 边界，只记录领域交给通道的原始完整消息和单次图文组。
type creativeFinalReplyTestTransport struct {
	*finalReplyContractTransport
	messages      []DeliveryMessage
	envelopes     [][]k12.DeliveryReceipt
	resolveCalls  int
	forbidResolve bool
	afterSend     func()
}

func (f *creativeFinalReplyTestTransport) ResolveTextTargets(ctx context.Context, agent string) ([]ResolvedDeliveryTarget, error) {
	f.resolveCalls++
	if f.forbidResolve {
		return nil, errors.New("current bindings must not be resolved during frozen replay")
	}
	return f.finalReplyContractTransport.ResolveTextTargets(ctx, agent)
}

func (f *creativeFinalReplyTestTransport) PrepareMessageForTargets(ctx context.Context, message DeliveryMessage, targets []ResolvedDeliveryTarget) ([]PreparedTextDelivery, error) {
	f.messages = append(f.messages, message)
	return f.finalReplyContractTransport.PrepareMessageForTargets(ctx, message, targets)
}

func (f *creativeFinalReplyTestTransport) PreflightPreparedEnvelope(_ context.Context, receipts []k12.DeliveryReceipt) error {
	if len(receipts) < 2 || receipts[0].PartOrdinal != 1 {
		return errors.New("complete creative envelope is required")
	}
	for _, receipt := range receipts[1:] {
		if receipt.PreparedResourceID == "" || receipt.Target != receipts[0].Target {
			return errors.New("original image resource or target is missing")
		}
	}
	return nil
}

func (f *creativeFinalReplyTestTransport) SendPrepared(ctx context.Context, receipt k12.DeliveryReceipt) (DeliveryTransportAck, error) {
	ack, err := f.finalReplyContractTransport.SendPrepared(ctx, receipt)
	if f.afterSend != nil {
		hook := f.afterSend
		f.afterSend = nil
		hook()
	}
	return ack, err
}

func (f *creativeFinalReplyTestTransport) SendPreparedEnvelope(ctx context.Context, receipts []k12.DeliveryReceipt) (DeliveryTransportAck, error) {
	f.envelopes = append(f.envelopes, append([]k12.DeliveryReceipt(nil), receipts...))
	// 同组只有一次外部提交；完整组在上面的通道边界记录，HTTP 回执共享给全部组成部分。
	return f.SendPrepared(ctx, receipts[0])
}

func (f *creativeFinalReplyTestTransport) QueryPreparedEnvelope(ctx context.Context, receipts []k12.DeliveryReceipt) (DeliveryTransportAck, error) {
	return f.finalReplyContractTransport.QueryPrepared(ctx, receipts[0])
}

func TestCreativeFinalReplySeparatesAutomaticAndExplicitDeliverySQLite(t *testing.T) {
	d, store, base, renderer, _, targets := newFinalReplyContractFixture(t)
	transport := &creativeFinalReplyTestTransport{finalReplyContractTransport: base}
	d.Delivery = transport
	allTargets := append(append([]ResolvedDeliveryTarget(nil), targets...), ResolvedDeliveryTarget{BindingID: "other-bound-target", Target: k12.DeliveryTarget{Platform: "feishu", InstanceID: "stable-feishu", ChatID: "other-authorized-parent", Label: "feishu"}})
	base.targets = allTargets
	const original = "课间，我和朋友一起跳绳。"
	const feedback = "## 具体肯定\n动作写得清楚。\n\n## 关键问题\n地点还不明确。\n\n## 局部示范\n只补实际地点。\n\n## 孩子下一步\n补一句后读一遍。"
	generation := &k12.WorkFeedbackGeneration{GenerationID: "same-feedback-generation", WorkID: "same-work", AgentName: "mingming", Status: k12.WorkFeedbackSucceeded,
		Source:   k12.CreativeWorkSourceSnapshot{WorkType: k12.WorkTypeWriting, DisplayName: "语文写作", WorkTitle: "我的课间", ContentMarkdown: original, SourceAssetID: "asset://mingming/frozen-original.png"},
		Feedback: &k12.WorkFeedback{ProjectionMarkdown: feedback}}
	view := CreativeWorkView{Record: &records.AgentRecord{RecordID: generation.WorkID, AgentName: generation.AgentName}, Fields: k12.CreativeWorkFields{WorkType: k12.WorkTypeWriting, DisplayName: "语文写作", WorkTitle: "我的课间"}, GenerationState: k12.CreativeWorkGenerationState{Initial: generation, Latest: generation}}
	autoID, err := CreativeFinalReplyObjectID(view, "inbound_reply")
	if err != nil {
		t.Fatal(err)
	}
	manualID, err := CreativeFinalReplyObjectID(view, "send_to_phone")
	if err != nil || autoID == manualID {
		t.Fatalf("different existing user intents share delivery identity: %q %q %v", autoID, manualID, err)
	}
	automatic, _, err := d.PrepareAndSendCreativeFinalReply(t.Context(), view, DeliveryMessage{Content: feedback}, targets, "en")
	if err != nil || automatic.Status != k12.DeliveryBatchDelivered || automatic.ObjectID != autoID || len(automatic.Receipts) != 1 || transport.resolveCalls != 0 {
		t.Fatalf("automatic reply lost its original single target: %+v %v", automatic, err)
	}
	image := validPNGFixture(t, "explicit-original")
	manual := DeliveryMessage{Content: "语文写作\n\n我的课间\n\n" + original + "\n\n" + feedback, Attachments: []DeliveryAttachment{{Name: "原作文.png", MIME: "image/png", Data: image}}}
	explicit, _, err := d.PrepareAndSendCreativeFinalReply(t.Context(), view, manual, nil, "en")
	if err != nil || explicit.Status != k12.DeliveryBatchDelivered || explicit.ObjectID != manualID || explicit.BatchID == automatic.BatchID || len(explicit.Receipts) != 4 || transport.resolveCalls != 1 || renderer.calls != 0 {
		t.Fatalf("explicit send reused automatic reply or lost all-bound plan: %+v %v", explicit, err)
	}
	images, seen := 0, map[string]bool{}
	wantVisible := "语文写作\n\n我的课间\n\n" + feedback
	for _, receipt := range explicit.Receipts {
		seen[receipt.Target.Platform] = true
		payload := readFinalReplyContractPayload(t, explicit, receipt)
		if payload.Source.Canonical.Markdown != manual.Content || payload.Content.Locale != "en" {
			t.Fatal("explicit source or locale changed")
		}
		if !payload.Source.WritingFeedback || payload.Source.DingTalkFullProjection != wantVisible {
			t.Fatal("explicit reply did not freeze its typed writing projection for later resume")
		}
		if receipt.PartMIME == "image/png" {
			images++
			if payload.Attachment == nil || !bytes.Equal(payload.Attachment.Data, image) {
				t.Fatal("explicit send lost the original image bytes")
			}
		}
	}
	if images != 2 || !seen["dingtalk"] || !seen["feishu"] || len(transport.envelopes) != 1 || len(transport.envelopes[0]) != 2 {
		t.Fatal("explicit send did not retain both physical targets and the DingTalk image envelope")
	}
	beforeSends, beforeUploads, beforePrepares := len(base.sends), len(base.uploads), base.preparations
	transport.forbidResolve = true
	replayed, _, err := d.PrepareAndSendCreativeFinalReply(t.Context(), view, manual, nil, "en")
	storedAuto, autoErr := store.GetDeliveryBatch(t.Context(), "mingming", automatic.BatchID)
	if err != nil || autoErr != nil || replayed.BatchID != explicit.BatchID || !reflect.DeepEqual(storedAuto, automatic) ||
		len(base.sends) != beforeSends || len(base.uploads) != beforeUploads || base.preparations != beforePrepares || transport.resolveCalls != 1 {
		t.Fatalf("explicit replay resent, rebound or changed original automatic batch: %+v %v %v", replayed, err, autoErr)
	}
}
