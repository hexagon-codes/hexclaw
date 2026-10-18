package api

import (
	"encoding/json"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/messagecontent"
)

func TestDingTalkEmphasisWorkflowNewManifestPreservesCanonicalAndArtifacts(t *testing.T) {
	const source = "**原因：**后文已经写了使用过程。"
	const projected = "**原因**：后文已经写了使用过程。"
	server := workflowDeliveryServer(t)
	ref, image := workflowArtifactFixture(t, "original.png", "image/png", []byte("frozen-image-content"))
	canonical, err := messagecontent.New(messagecontent.ProducerWorkflow, "zh-CN", source, []messagecontent.AttachmentRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	run := workflowDeliveryRun("emphasis-new-workflow", map[string]any{"dingtalk": map[string]any{"enabled": true, "instance_id": "bot-id", "chat_id": "staff-id"}})
	run.Output, run.NodeResults[0].Output, run.NodeResults[0].MessageContent = source, source, &canonical
	run.ArtifactSnapshots = server.freezeWorkflowArtifacts(t.Context(), &adapter.Reply{Content: source, MessageContent: &canonical, Attachments: []adapter.Attachment{image}})
	run.Deliveries = prepareWorkflowDeliveries(run)
	if len(run.Deliveries) != 1 || len(run.Deliveries[0].Parts) != 2 {
		t.Fatal("workflow did not freeze the complete message and image")
	}
	delivery := run.Deliveries[0]
	if delivery.MessageContent.Markdown != source || delivery.MessageContent.SourceDigest != canonical.SourceDigest || delivery.RenderManifest.Parts[0].Text != projected || delivery.Parts[1].Digest != ref.Digest || delivery.RenderManifest.Parts[1].ArtifactDigest != ref.Digest {
		t.Fatal("workflow normalization changed canonical or attachment identity")
	}
	if err := server.workflowStore.addRun(run); err != nil {
		t.Fatal(err)
	}
	transport := newWorkflowDeliveryTestTransport()
	server.dispatchWorkflowDeliveryWithTransport(t.Context(), run.ID, delivery.ID, false, transport)
	_, completed := server.workflowDeliverySnapshot(run.ID, delivery.ID)
	if completed.Status != "delivered" || transport.sends[1] != 1 || transport.sends[2] != 1 || len(transport.payloads) != 2 || transport.payloads[0].Text != projected || transport.payloads[1].Digest != ref.Digest {
		t.Fatalf("prepared workflow payload changed before delivery: status=%s sends=%v", completed.Status, transport.sends)
	}
	if transport.payloads[1].Attachment == nil || transport.payloads[1].Attachment.Data != image.Data || completed.RenderManifest.ValidateFor(*completed.MessageContent) != nil {
		t.Fatal("workflow image bytes or canonical render evidence changed")
	}
}

func TestDingTalkEmphasisWorkflowFrozenRetryAndUnknownPreserveBytes(t *testing.T) {
	for _, tc := range []struct {
		name, status, queryID string
		retry                 bool
		wantSends, wantQuery  int
	}{
		{"old pending", "pending", "", false, 1, 0},
		{"known failed retry", "failed", "", true, 1, 0},
		{"unknown with receipt", "outcome_unknown", "provider-receipt", false, 0, 1},
		{"unknown without receipt", "outcome_unknown", "", false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const old = "**原因：**已冻结旧正文，不重新格式化。"
			server := workflowDeliveryServer(t)
			canonical, err := messagecontent.New(messagecontent.ProducerWorkflow, "zh-CN", old, nil)
			if err != nil {
				t.Fatal(err)
			}
			run := workflowDeliveryRun("emphasis-old-"+tc.name, map[string]any{"dingtalk": map[string]any{"enabled": true, "instance_id": "bot-id", "chat_id": "staff-id"}})
			run.Output, run.NodeResults[0].Output, run.NodeResults[0].MessageContent = old, old, &canonical
			run.Deliveries = prepareWorkflowDeliveries(run)
			legacy, err := messagecontent.BuildManifest(canonical, messagecontent.RenderRequest{Surface: messagecontent.SurfaceChannel, RendererVersion: "workflow-delivery-v1", Capabilities: messagecontent.CapabilitySnapshot{Markdown: true, TeXMath: true, Attachments: true}, Parts: []messagecontent.RenderPart{{Kind: messagecontent.PartMarkdown, Text: old}}})
			if err != nil {
				t.Fatal(err)
			}
			delivery := &run.Deliveries[0]
			delivery.RenderManifest = &legacy
			delivery.Status, delivery.Parts[0].Status, delivery.Parts[0].ExternalMessageID = tc.status, tc.status, tc.queryID
			if tc.status != "pending" {
				delivery.Attempts, delivery.Parts[0].Attempts = 1, 1
			}
			canonicalBytes, _ := json.Marshal(delivery.MessageContent)
			manifestBytes, _ := json.Marshal(delivery.RenderManifest)
			if err := server.workflowStore.addRun(run); err != nil {
				t.Fatal(err)
			}
			restarted := newWorkflowTestServer()
			restarted.store, restarted.genStore = server.store, server.genStore
			restarted.workflowStore.runsFilePath = server.workflowStore.runsFilePath
			restarted.workflowStore.loadRunsFromFile()
			transport := newWorkflowDeliveryTestTransport()
			restarted.dispatchWorkflowDeliveryWithTransport(t.Context(), run.ID, delivery.ID, tc.retry, transport)
			_, after := restarted.workflowDeliverySnapshot(run.ID, delivery.ID)
			afterCanonical, _ := json.Marshal(after.MessageContent)
			afterManifest, _ := json.Marshal(after.RenderManifest)
			if string(afterCanonical) != string(canonicalBytes) || string(afterManifest) != string(manifestBytes) || transport.sends[1] != tc.wantSends || transport.queries != tc.wantQuery {
				t.Fatalf("old delivery was reformatted or resent: sends=%v queries=%d", transport.sends, transport.queries)
			}
			for _, part := range transport.payloads {
				if part.Text != old {
					t.Fatal("known retry did not reuse the old exact payload")
				}
			}
			restarted.dispatchWorkflowDeliveryWithTransport(t.Context(), run.ID, delivery.ID, false, transport)
			if transport.sends[1] != tc.wantSends || transport.queries != tc.wantQuery {
				t.Fatal("terminal or unqueryable recovery performed another physical send/query")
			}
		})
	}
}
