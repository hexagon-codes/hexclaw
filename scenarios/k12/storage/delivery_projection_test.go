package k12storage_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func projectionDigest(raw string) string {
	hash := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(hash[:])
}

// 仅构造真实内容合同与附件字节，不代表 PDF 渲染或远端投递验收。
func projectedDeliveryFixture(t *testing.T, private, secondTarget, fallback bool, originalPDF ...bool) k12.DeliveryBatch {
	t.Helper()
	imageBytes, pdfBytes := []byte("original-image-bytes"), []byte("complete-source-pdf-bytes")
	original := messagecontent.AttachmentRef{AssetID: "original-image", Name: "作业.png", MIME: "image/png", Digest: projectionDigest(string(imageBytes)), AltText: "原作业图"}
	if len(originalPDF) > 0 && originalPDF[0] {
		original.AssetID, original.Name, original.MIME, original.AltText = "original-pdf", "原资料.pdf", "application/pdf", "原资料"
	}
	canonical, err := messagecontent.New(messagecontent.ProducerK12, "zh-CN", "完整且不可丢失的正文与教学说明。", []messagecontent.AttachmentRef{original})
	if err != nil {
		t.Fatal(err)
	}
	source := k12.DeliveryProjectionSource{Version: k12.DeliveryProjectionVersionIMFinalPDFV1, SourceRef: "source-result-1", Canonical: canonical, DingTalkSuffix: "处理：本地测试 · 模型：测试回执"}
	batch := k12.DeliveryBatch{BatchID: "projection-batch", AgentName: "mingming", ObjectKind: "creative_work", ObjectID: "work-1:" + k12.DeliveryProjectionVersionIMFinalPDFV1, ContentDigest: canonical.SourceDigest}
	if private {
		source.PrivateEvent = &k12.AcceptedPrivateReplySource{AgentName: batch.AgentName, Platform: "dingtalk", InstanceID: "bot-a", SenderID: "parent-a", MessageID: "inbound-1", AssistantMessageID: "assistant-1"}
		batch.ObjectKind = "im_final_reply"
		batch.ObjectID, err = k12.PrivateReplyObjectID(*source.PrivateEvent)
		if err != nil {
			t.Fatal(err)
		}
	}
	if fallback {
		batch.BatchID += "-fallback"
	}
	batch.DedupeKey = projectionDigest(strings.Join([]string{batch.AgentName, batch.ObjectKind, batch.ObjectID, batch.ContentDigest}, "\x00"))
	if fallback {
		batch.DedupeKey += ":fallback"
	}
	targets := []k12.DeliveryTarget{{Platform: "dingtalk", InstanceID: "bot-a", ChatID: "parent-a"}}
	if secondTarget {
		targets = append(targets, k12.DeliveryTarget{Platform: "feishu", InstanceID: "bot-b", ChatID: "parent-b"})
	}
	for targetIndex, target := range targets {
		body := canonical.Markdown
		attachments := append([]messagecontent.AttachmentRef(nil), canonical.Attachments...)
		data := [][]byte{imageBytes}
		if target.Platform == "dingtalk" && !fallback {
			body = "短摘要，完整内容见附件。"
			attachments = append(attachments, messagecontent.AttachmentRef{AssetID: "complete-pdf", Name: "完整回复.pdf", MIME: "application/pdf", Digest: projectionDigest(string(pdfBytes)), AltText: "完整回复"})
			data = append(data, pdfBytes)
		}
		if target.Platform == "dingtalk" {
			body += "\n\n" + source.DingTalkSuffix
		}
		content, err := messagecontent.New(canonical.ProducerKind, canonical.Locale, body, attachments)
		if err != nil {
			t.Fatal(err)
		}
		parts := []messagecontent.RenderPart{{Kind: messagecontent.PartMarkdown, Text: body}}
		for _, attachment := range attachments {
			parts = append(parts, messagecontent.RenderPart{Kind: messagecontent.PartArtifact, ArtifactRef: attachment.AssetID, ArtifactDigest: attachment.Digest, AltText: attachment.AltText})
		}
		manifest, err := messagecontent.BuildManifest(content, messagecontent.RenderRequest{Surface: messagecontent.SurfaceChannel, Capabilities: messagecontent.CapabilitySnapshot{Markdown: true, Attachments: true}, RendererVersion: "fixture-v1", Parts: parts})
		if err != nil {
			t.Fatal(err)
		}
		renderJSON, _ := json.Marshal(manifest)
		for index, part := range parts {
			payload := map[string]any{"kind": part.Kind, "ordinal": index + 1, "digest": projectionDigest(body), "text": body, "message_content": content, "render_manifest": manifest, "delivery_source": source}
			mime, partDigest := "", projectionDigest(body)
			if index > 0 {
				attachment := attachments[index-1]
				mime, partDigest = attachment.MIME, attachment.Digest
				delete(payload, "text")
				payload["mime"], payload["digest"] = mime, partDigest
				payload["attachment"] = map[string]any{"Name": attachment.Name, "MIME": mime, "Data": data[index-1]}
			}
			raw, _ := json.Marshal(payload)
			id := fmt.Sprintf("%s-%d-%d", batch.BatchID, targetIndex, index)
			bindingID := fmt.Sprintf("binding-%d", targetIndex)
			if private {
				bindingID, err = k12.PrivateReplyAuthorityID(*source.PrivateEvent)
				if err != nil {
					t.Fatal(err)
				}
			}
			payloadDigest := projectionDigest(string(raw))
			dedupe := projectionDigest(strings.Join([]string{batch.AgentName, batch.ObjectKind, batch.ObjectID, bindingID, string(part.Kind), mime, fmt.Sprint(index + 1), partDigest, payloadDigest}, "\x00"))
			batch.Receipts = append(batch.Receipts, k12.DeliveryReceipt{DeliveryID: id, BindingID: bindingID, Target: target, PartKind: part.Kind, PartMIME: mime, PartOrdinal: index + 1, PartDigest: partDigest, DedupeKey: dedupe, PayloadDigest: payloadDigest, PayloadJSON: string(raw), RenderJSON: string(renderJSON)})
		}
	}
	return batch
}

func mutateProjectionPayload(t *testing.T, receipt *k12.DeliveryReceipt, change func(map[string]any)) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(receipt.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	change(payload)
	raw, _ := json.Marshal(payload)
	receipt.PayloadJSON, receipt.PayloadDigest = string(raw), projectionDigest(string(raw))
}

func TestDeliveryProjectionSQLiteAllowsOnlyCompletePerTargetManifests(t *testing.T) {
	store := setupDeliveryStore(t)
	for _, name := range []string{"missing part", "missing source", "changed source", "wrong root digest", "truncated canonical attachment"} {
		t.Run(name, func(t *testing.T) {
			batch := projectedDeliveryFixture(t, false, true, false)
			switch name {
			case "missing part":
				batch.Receipts = append(batch.Receipts[:2], batch.Receipts[3:]...)
			case "missing source":
				for i := range batch.Receipts {
					mutateProjectionPayload(t, &batch.Receipts[i], func(p map[string]any) { delete(p, "delivery_source") })
				}
			case "changed source":
				mutateProjectionPayload(t, &batch.Receipts[3], func(p map[string]any) { p["delivery_source"].(map[string]any)["source_ref"] = "different-source" })
			case "wrong root digest":
				batch.ContentDigest = projectionDigest("other")
			case "truncated canonical attachment":
				mutateProjectionPayload(t, &batch.Receipts[0], func(p map[string]any) { p["message_content"].(map[string]any)["attachments"] = []any{} })
			}
			if _, _, err := store.PrepareDeliveryBatch(t.Context(), batch); err == nil {
				t.Fatal("incomplete projection accepted")
			}
		})
	}
	input := projectedDeliveryFixture(t, false, true, false)
	batch, created, err := store.PrepareDeliveryBatch(t.Context(), input)
	if err != nil || !created || len(batch.Receipts) != 5 {
		t.Fatalf("complete 3-part/2-part targets rejected: created=%v err=%v", created, err)
	}
	if _, created, err := store.PrepareDeliveryBatch(t.Context(), input); err != nil || created {
		t.Fatalf("projection replay changed plan: %v %v", created, err)
	}
}

func TestDeliveryProjectionSQLiteFirstObjectPlanSurvivesNewPDFAndTargets(t *testing.T) {
	store := setupDeliveryStore(t)
	first, created, err := store.PrepareDeliveryBatch(t.Context(), projectedDeliveryFixture(t, false, false, false))
	if err != nil || !created {
		t.Fatalf("first plan: %v %v", created, err)
	}
	if claimed, err := store.BeginDeliveryK12MediaPreparation(t.Context(), first.AgentName, first.Receipts[2].DeliveryID); err != nil || !claimed {
		t.Fatalf("preparation fixture: %v %v", claimed, err)
	}
	first, err = store.GetDeliveryBatch(t.Context(), first.AgentName, first.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	second := projectedDeliveryFixture(t, false, true, false)
	second.BatchID, second.DedupeKey = "second-render-plan", "different-render-key"
	newPDF := []byte("same-source-different-render-bytes")
	for i := range second.Receipts {
		r := &second.Receipts[i]
		r.DeliveryID = "second-" + r.DeliveryID
		if r.Target.Platform == "dingtalk" {
			var payload map[string]json.RawMessage
			var content messagecontent.MessageContent
			var manifest messagecontent.RenderManifest
			if err := json.Unmarshal([]byte(r.PayloadJSON), &payload); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(payload["message_content"], &content); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(payload["render_manifest"], &manifest); err != nil {
				t.Fatal(err)
			}
			last := len(content.Attachments) - 1
			content.Attachments[last].AssetID = "second-generated-pdf"
			content.Attachments[last].Digest = projectionDigest(string(newPDF))
			content, err = messagecontent.New(content.ProducerKind, content.Locale, content.Markdown, content.Attachments)
			if err != nil {
				t.Fatal(err)
			}
			manifest.Parts[last+1].ArtifactRef = content.Attachments[last].AssetID
			manifest.Parts[last+1].ArtifactDigest = content.Attachments[last].Digest
			manifest, err = messagecontent.BuildManifest(content, messagecontent.RenderRequest{Surface: manifest.Surface, Capabilities: manifest.CapabilitySnapshot, RendererVersion: manifest.RendererVersion, Parts: manifest.Parts})
			if err != nil {
				t.Fatal(err)
			}
			payload["message_content"], _ = json.Marshal(content)
			payload["render_manifest"], _ = json.Marshal(manifest)
			r.RenderJSON = string(payload["render_manifest"])
			if r.PartMIME == "application/pdf" {
				r.PartDigest = content.Attachments[last].Digest
				payload["digest"], _ = json.Marshal(r.PartDigest)
				payload["attachment"], _ = json.Marshal(map[string]any{"Name": content.Attachments[last].Name, "MIME": "application/pdf", "Data": newPDF})
			}
			raw, _ := json.Marshal(payload)
			r.PayloadJSON, r.PayloadDigest = string(raw), projectionDigest(string(raw))
		}
		r.DedupeKey = "second-" + r.DedupeKey
	}
	got, created, err := store.PrepareDeliveryBatch(t.Context(), second)
	if err != nil || created || !reflect.DeepEqual(first, got) {
		t.Fatalf("new rendering or target list replaced first plan: created=%v err=%v", created, err)
	}
	var roots int
	if err := store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM k12_delivery_batches WHERE agent_name=? AND object_kind=? AND object_id=?`, first.AgentName, first.ObjectKind, first.ObjectID).Scan(&roots); err != nil || roots != 1 {
		t.Fatalf("second root created: roots=%d err=%v", roots, err)
	}
	for i := range second.Receipts {
		mutateProjectionPayload(t, &second.Receipts[i], func(payload map[string]any) {
			payload["delivery_source"].(map[string]any)["source_ref"] = "another-result"
		})
	}
	if _, _, err := store.PrepareDeliveryBatch(t.Context(), second); !errors.Is(err, k12storage.ErrDeliveryBatchConflict) {
		t.Fatalf("different source was silently rebound: %v", err)
	}
}

func TestDeliveryProjectionSQLitePrivateEventCannotChangeRecipient(t *testing.T) {
	store := setupDeliveryStore(t)
	for _, name := range []string{"sender", "instance", "platform", "binding", "missing authority", "event", "owner", "missing proof", "ordinary source"} {
		t.Run(name, func(t *testing.T) {
			batch := projectedDeliveryFixture(t, true, false, false)
			for i := range batch.Receipts {
				r := &batch.Receipts[i]
				switch name {
				case "sender":
					r.Target.ChatID = "other"
				case "instance":
					r.Target.InstanceID = "other"
				case "platform":
					r.Target.Platform = "feishu"
				case "binding":
					r.BindingID = "invented-binding"
				case "missing authority":
					r.BindingID = ""
				case "event":
					mutateProjectionPayload(t, r, func(p map[string]any) {
						p["delivery_source"].(map[string]any)["private_event"].(map[string]any)["message_id"] = "other"
					})
				case "owner":
					batch.AgentName = "lele"
				case "missing proof":
					mutateProjectionPayload(t, r, func(p map[string]any) { delete(p, "delivery_source") })
				case "ordinary source":
					batch.ObjectKind, batch.ObjectID = "creative_work", "work-1:"+k12.DeliveryProjectionVersionIMFinalPDFV1
					mutateProjectionPayload(t, r, func(p map[string]any) { delete(p["delivery_source"].(map[string]any), "private_event") })
				}
			}
			if _, _, err := store.PrepareDeliveryBatch(t.Context(), batch); err == nil {
				t.Fatal("private source changed target or lost proof")
			}
		})
	}
	input := projectedDeliveryFixture(t, true, false, false)
	batch, created, err := store.PrepareDeliveryBatch(t.Context(), input)
	if err != nil || !created || !strings.HasPrefix(batch.Receipts[0].BindingID, "accepted-event:") {
		t.Fatalf("accepted event rejected: created=%v err=%v", created, err)
	}
}

func TestDeliveryProjectionSQLitePreparationFailureAndUnknownPreserveSource(t *testing.T) {
	for _, tc := range []struct {
		name               string
		known, originalPDF bool
	}{{"true", true, false}, {"false", false, false}, {"original_pdf", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			known := tc.known
			store := setupDeliveryStore(t)
			input := projectedDeliveryFixture(t, false, false, false, tc.originalPDF)
			batch, _, err := store.PrepareDeliveryBatch(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			image, pdf := batch.Receipts[1], batch.Receipts[2]
			if ok, err := store.BeginDeliveryK12MediaPreparation(t.Context(), batch.AgentName, image.DeliveryID); err != nil || !ok {
				t.Fatalf("image claim: %v %v", ok, err)
			}
			if err := store.SaveDeliveryK12PreparedResource(t.Context(), batch.AgentName, image.DeliveryID, "image-resource"); err != nil {
				t.Fatal(err)
			}
			if ok, err := store.BeginDeliveryK12MediaPreparation(t.Context(), batch.AgentName, pdf.DeliveryID); err != nil || !ok {
				t.Fatalf("pdf claim: %v %v", ok, err)
			}
			if ok, err := store.BeginDeliveryK12MediaPreparation(t.Context(), batch.AgentName, pdf.DeliveryID); err != nil || ok {
				t.Fatalf("duplicate preparation claim: %v %v", ok, err)
			}
			failed, err := store.MarkDeliveryK12PreparationFailure(t.Context(), batch.AgentName, batch.BatchID, pdf.DeliveryID, known)
			if err != nil || k12storage.PDFPreparationFallbackAllowed(failed) != known {
				t.Fatalf("failure classification: known=%v err=%v", known, err)
			}
			fallback := projectedDeliveryFixture(t, false, false, true, tc.originalPDF)
			if fallback.ContentDigest != batch.ContentDigest || fallback.DedupeKey == batch.DedupeKey {
				t.Fatal("fallback must be a distinct plan for the same complete source")
			}
			if known && !tc.originalPDF {
				withoutSuffix := fallback
				withoutSuffix.Receipts = append([]k12.DeliveryReceipt(nil), fallback.Receipts...)
				for i := range withoutSuffix.Receipts {
					r := &withoutSuffix.Receipts[i]
					var payload map[string]json.RawMessage
					if err := json.Unmarshal([]byte(r.PayloadJSON), &payload); err != nil {
						t.Fatal(err)
					}
					var source k12.DeliveryProjectionSource
					var manifest messagecontent.RenderManifest
					if err := json.Unmarshal(payload["delivery_source"], &source); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(payload["render_manifest"], &manifest); err != nil {
						t.Fatal(err)
					}
					manifest.Parts[0].Text = source.Canonical.Markdown
					manifest, err = messagecontent.BuildManifest(source.Canonical, messagecontent.RenderRequest{Surface: manifest.Surface, Capabilities: manifest.CapabilitySnapshot, RendererVersion: manifest.RendererVersion, Parts: manifest.Parts})
					if err != nil {
						t.Fatal(err)
					}
					payload["message_content"], _ = json.Marshal(source.Canonical)
					payload["render_manifest"], _ = json.Marshal(manifest)
					r.RenderJSON = string(payload["render_manifest"])
					if i == 0 {
						payload["text"], _ = json.Marshal(source.Canonical.Markdown)
						r.PartDigest = projectionDigest(source.Canonical.Markdown)
						payload["digest"], _ = json.Marshal(r.PartDigest)
					}
					raw, _ := json.Marshal(payload)
					r.PayloadJSON, r.PayloadDigest = string(raw), projectionDigest(string(raw))
				}
				if _, _, err := store.PrepareDeliveryPDFFallback(t.Context(), batch.BatchID, withoutSuffix); err == nil {
					t.Fatal("full-text fallback dropped its frozen DingTalk execution suffix")
				}
			}
			result, created, err := store.PrepareDeliveryPDFFallback(t.Context(), batch.BatchID, fallback)
			if known {
				if err != nil || !created || len(result.Receipts) != 2 || result.ContentDigest != batch.ContentDigest || result.Receipts[1].PreparedResourceID != "image-resource" {
					t.Fatalf("complete fallback lost source/image: created=%v result=%+v err=%v", created, result, err)
				}
				replay, created, err := store.PrepareDeliveryBatch(t.Context(), input)
				if err != nil || created || !reflect.DeepEqual(result, replay) {
					t.Fatalf("normal prepare did not reuse the committed fallback plan: created=%v err=%v", created, err)
				}
			} else {
				if err == nil || created {
					t.Fatal("unknown preparation generated a fallback")
				}
				if ok, err := store.BeginDeliveryK12MediaPreparation(t.Context(), batch.AgentName, pdf.DeliveryID); err != nil || ok {
					t.Fatalf("unknown preparation resent: %v %v", ok, err)
				}
				if err := store.SaveDeliveryK12PreparedResource(t.Context(), batch.AgentName, pdf.DeliveryID, "late"); err == nil {
					t.Fatal("unknown source was replaced by unowned completion")
				}
			}
			after, err := store.GetDeliveryBatch(t.Context(), batch.AgentName, batch.BatchID)
			if err != nil || !reflect.DeepEqual(failed, after) {
				t.Fatalf("fallback rewrote source: %v", err)
			}
		})
	}
	t.Run("already_sending", func(t *testing.T) {
		store := setupDeliveryStore(t)
		batch, _, err := store.PrepareDeliveryBatch(t.Context(), projectedDeliveryFixture(t, false, false, false))
		if err != nil {
			t.Fatal(err)
		}
		if _, started, err := store.BeginDeliveryAttempt(t.Context(), batch.AgentName, batch.Receipts[0].DeliveryID); err != nil || !started {
			t.Fatalf("send boundary fixture: %v %v", started, err)
		}
		before, err := store.GetDeliveryBatch(t.Context(), batch.AgentName, batch.BatchID)
		if err != nil {
			t.Fatal(err)
		}
		pdf := batch.Receipts[2]
		if claimed, err := store.BeginDeliveryK12MediaPreparation(t.Context(), batch.AgentName, pdf.DeliveryID); err != nil || claimed {
			t.Fatalf("preparation crossed visible send: %v %v", claimed, err)
		}
		if _, err := store.MarkDeliveryK12PreparationFailure(t.Context(), batch.AgentName, batch.BatchID, pdf.DeliveryID, true); err == nil {
			t.Fatal("visible send was reclassified as zero-send preparation failure")
		}
		after, err := store.GetDeliveryBatch(t.Context(), batch.AgentName, batch.BatchID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("preparation mutated sending batch: %v", err)
		}
	})
}
