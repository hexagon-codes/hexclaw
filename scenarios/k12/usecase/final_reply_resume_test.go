package usecase

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// 使用既有准备事务截取进程中断点；不直接修改业务状态，不注册任何可重读的原图文件。
func seedFrozenCreativeReplyPlan(t *testing.T) (Deps, *k12storage.Store, *creativeFinalReplyTestTransport, *finalReplyPDFRenderer, k12.DeliveryBatch, []byte, string, string) {
	t.Helper()
	d, store, base, renderer, req, targets := newFinalReplyContractFixture(t)
	transport := &creativeFinalReplyTestTransport{finalReplyContractTransport: base, forbidResolve: true}
	d.Delivery = transport
	image := validPNGFixture(t, "frozen-source-only")
	const canonical = "语文写作\n\n原稿完整保留。\n\n具体肯定、关键问题、局部示范、孩子下一步。"
	const suffix = "处理过程：已完成\n模型：原冻结模型"
	const visible = "语文写作\n\n具体肯定、关键问题、局部示范、孩子下一步。\n\n" + suffix
	req.ObjectKind, req.ObjectID = "creative_work", "frozen-writing:send_to_phone:"+k12.DeliveryProjectionVersionIMFinalPDFV1
	req.Producer, req.Locale, req.SourceKind, req.SourceRef = messagecontent.ProducerK12, "zh-CN", k12.PrintSourceCreativeObservation, "frozen-writing-source"
	req.Message = DeliveryMessage{Content: canonical, Attachments: []DeliveryAttachment{{Name: "原作文.png", MIME: "image/png", Data: image}}, WritingFeedback: true}
	req.DingTalkSuffix = suffix
	source, err := finalReplySource(req, req.Message)
	if err != nil {
		t.Fatal(err)
	}
	source.WritingFeedback, source.DingTalkFullProjection = true, visible
	full := req.Message
	full.Source = source
	full.Projections = map[string]DeliveryMessage{"dingtalk": {Content: "完整内容见附件\n\n" + suffix, Source: source, WritingFeedback: true,
		Attachments: []DeliveryAttachment{full.Attachments[0], {Name: "完整点评.pdf", MIME: "application/pdf", Data: []byte("%PDF-1.7\nfrozen PDF payload\n%%EOF")}}}}
	plan, err := d.buildPreparedMessageBatch(t.Context(), req.AgentName, req.ObjectKind, req.ObjectID, full, targets)
	if err != nil {
		t.Fatal(err)
	}
	pending, created, err := store.PrepareDeliveryBatch(t.Context(), plan)
	if err != nil || !created || pending.Status != k12.DeliveryBatchPending || len(pending.Receipts) != 3 {
		t.Fatalf("freeze before-send plan: %+v created=%t err=%v", pending, created, err)
	}
	renderer.err = errors.New("resume must not render or reopen source")
	return d, store, transport, renderer, pending, image, canonical, visible
}

func TestFinalReplyResumePendingUsesOnlyFrozenPlanSQLite(t *testing.T) {
	d, store, transport, renderer, pending, image, _, _ := seedFrozenCreativeReplyPlan(t)
	prepares := transport.preparations
	resumed, created, err := d.ResumeFinalReplyBatch(t.Context(), pending.AgentName, pending.BatchID)
	if err != nil || created || resumed.BatchID != pending.BatchID || resumed.Status != k12.DeliveryBatchDelivered || renderer.calls != 0 || transport.preparations != prepares || transport.resolveCalls != 0 || len(transport.uploads) != 2 || len(transport.sends) != 2 {
		t.Fatalf("pending resume crossed source/render/target boundary: %+v %v", resumed, err)
	}
	stored, err := store.GetDeliveryBatch(t.Context(), pending.AgentName, pending.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	for i, receipt := range stored.Receipts {
		if receipt.PayloadJSON != pending.Receipts[i].PayloadJSON || receipt.Target != pending.Receipts[i].Target || receipt.Attempt != 1 {
			t.Fatal("pending resume changed frozen bytes/target or repeated an attempt")
		}
		payload := readFinalReplyContractPayload(t, stored, receipt)
		if receipt.PartMIME == "image/png" && (payload.Attachment == nil || !bytes.Equal(payload.Attachment.Data, image)) {
			t.Fatal("pending resume dropped original frozen image")
		}
	}
}

func TestFinalReplyResumeKnownFailurePreservesFrozenWritingAndUnknownDoesNotSend(t *testing.T) {
	for _, known := range []bool{true, false} {
		name := "unknown"
		if known {
			name = "known_zero_send"
		}
		t.Run(name, func(t *testing.T) {
			d, store, transport, renderer, pending, image, canonical, visible := seedFrozenCreativeReplyPlan(t)
			transport.uploadStatus = 503
			if known {
				transport.uploadStatus = 400
			}
			failed, err := d.prepareDeliveryBatchResources(t.Context(), pending)
			if err == nil || failed.BatchID != pending.BatchID || len(transport.sends) != 0 || len(transport.uploads) != 1 || k12storage.PDFPreparationFallbackAllowed(failed) != known {
				t.Fatalf("preparation failure evidence mismatch: %+v %v", failed, err)
			}
			prepares := transport.preparations
			transport.uploadStatus = 200
			resumed, created, resumeErr := d.ResumeFinalReplyBatch(t.Context(), pending.AgentName, pending.BatchID)
			if renderer.calls != 0 || transport.resolveCalls != 0 {
				t.Fatal("resume rendered or re-resolved mutable sources/targets")
			}
			if !known {
				if !errors.Is(resumeErr, ErrDeliveryQueryUnavailable) || created || resumed.BatchID != pending.BatchID || resumed.Status != k12.DeliveryBatchOutcomeUnknown || len(transport.uploads) != 1 || len(transport.sends) != 0 || transport.preparations != prepares {
					t.Fatalf("unknown preparation was reissued: %+v %v", resumed, resumeErr)
				}
				return
			}
			if resumeErr != nil || !created || resumed.BatchID == pending.BatchID || resumed.Status != k12.DeliveryBatchDelivered || len(resumed.Receipts) != 2 || len(transport.uploads) != 2 || len(transport.sends) != 1 {
				t.Fatalf("known failure did not deliver frozen full fallback once: %+v %v", resumed, resumeErr)
			}
			last := transport.messages[len(transport.messages)-1]
			if !last.WritingFeedback || last.ProjectedContent != visible || last.Source == nil || last.Source.Canonical.Markdown != canonical || !last.Source.WritingFeedback || last.Source.DingTalkFullProjection != visible || last.Content != canonical+"\n\n"+last.Source.DingTalkSuffix {
				t.Fatalf("fallback lost frozen writing projection or suffix: %+v", last)
			}
			imagePart := readFinalReplyContractPayload(t, resumed, resumed.Receipts[1])
			if imagePart.Attachment == nil || !bytes.Equal(imagePart.Attachment.Data, image) || imagePart.Attachment.MIME != "image/png" {
				t.Fatal("fallback reopened or replaced original image bytes")
			}
			beforeSends, beforeUploads := len(transport.sends), len(transport.uploads)
			again, _, err := d.ResumeFinalReplyBatch(t.Context(), pending.AgentName, pending.BatchID)
			oldAfter, oldErr := store.GetDeliveryBatch(t.Context(), pending.AgentName, pending.BatchID)
			if err != nil || oldErr != nil || again.BatchID != resumed.BatchID || len(transport.sends) != beforeSends || len(transport.uploads) != beforeUploads || !reflect.DeepEqual(oldAfter, failed) {
				t.Fatalf("fallback replay changed old plan or repeated delivery: %+v %v %v", again, err, oldErr)
			}
		})
	}
}

func TestFinalReplyAcceptedReadbackFailureKeepsIdentitySQLite(t *testing.T) {
	d, store, base, _, req, targets := newFinalReplyContractFixture(t)
	transport := &creativeFinalReplyTestTransport{finalReplyContractTransport: base}
	d.Delivery = transport
	req.Message.Content = "complete short reply"
	transport.afterSend = func() { _ = store.DB().Close() }
	batch, accepted, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if err == nil || !accepted || batch.BatchID == "" || batch.AgentName != req.AgentName || len(base.sends) != 1 {
		t.Fatalf("post-send readback lost acceptance identity: %+v accepted=%t err=%v", batch, accepted, err)
	}
	again, _, err := d.ResumeFinalReplyBatch(t.Context(), req.AgentName, batch.BatchID)
	if err == nil || again.BatchID != batch.BatchID || again.AgentName != batch.AgentName || len(base.sends) != 1 {
		t.Fatalf("unavailable readback erased known batch or reissued send: %+v %v", again, err)
	}
}
