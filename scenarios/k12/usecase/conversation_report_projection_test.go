package usecase

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestConversationReportProjectionKeepsChannelPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		count    int
		explicit bool
		pdf      bool
	}{
		{"short formal report", 999, false, false},
		{"exact threshold", 1000, false, true},
		{"long formal report", 1200, false, true},
		{"explicit old file resend", 20, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, transport, renderer, req, targets := newFinalReplyContractFixture(t)
			body := strings.Repeat("题", tc.count)
			if !tc.pdf {
				body = "## 一、快速解答\n答案：12 TB。\n\n## 二、深度讲解\n先求每份容量，再求总容量。\n"
				body += strings.Repeat("题", tc.count-len([]rune(body)))
			}
			pdf, _, err := d.PreparePrintableArtifact(t.Context(), PreparePrintableArtifactRequest{AgentName: req.AgentName, SourceKind: k12.PrintSourceTutoringTips, SourceRef: "original-report", Title: "完整讲解", CanonicalMarkdown: body})
			if err != nil {
				t.Fatal(err)
			}
			original := DeliveryAttachment{Name: "other.png", MIME: "image/png", Data: validPNGFixture(t, "other-result")}
			full := DeliveryMessage{Content: body, Attachments: []DeliveryAttachment{original}}
			// 尾注超长也不能改变正文分流；原图和其他工具产物必须保留。
			suffix := strings.Repeat("模型信息", 300)
			message, err := conversationReportDeliveryMessage(t.Context(), req, full, []PrintableArtifactView{pdf}, tc.explicit, suffix)
			if err != nil {
				t.Fatal(err)
			}
			if message.Source.Canonical.Markdown != body || len(message.Source.Canonical.Attachments) != 1 {
				t.Fatal("channel projection changed the full report source")
			}
			projected := message.Projections["dingtalk"]
			wantParts := 2
			if tc.pdf {
				wantParts++
				if len(projected.Attachments) != 2 || !bytes.Equal(projected.Attachments[1].Data, pdf.Render.Payload) || projected.Attachments[1].MIME != "application/pdf" || strings.Contains(projected.Content, body) {
					t.Fatal("document projection did not reuse the original PDF")
				}
			} else if len(projected.Attachments) != 1 || !strings.HasPrefix(projected.Content, body) {
				t.Fatal("short report should retain readable text without forcing a file send")
			}
			if !bytes.Equal(projected.Attachments[0].Data, original.Data) {
				t.Fatal("another tool's attachment was lost")
			}
			req.ObjectID += ":" + k12.DeliveryProjectionVersionIMFinalPDFV1
			batch, _, err := d.PrepareAndSendMessageBatchForTargets(t.Context(), req.AgentName, req.ObjectKind, req.ObjectID, message, targets)
			if err != nil || len(batch.Receipts) != wantParts {
				t.Fatalf("frozen channel plan changed: parts=%d err=%v", len(batch.Receipts), err)
			}
			if renderer.calls != 1 || len(transport.sends) != wantParts {
				t.Fatalf("report was rendered twice or parts were lost: renders=%d sends=%d", renderer.calls, len(transport.sends))
			}
			for _, receipt := range batch.Receipts {
				if receipt.PartKind == messagecontent.PartArtifact && receipt.PartMIME == "application/pdf" && !tc.pdf {
					t.Fatal("short report unexpectedly sent a PDF")
				}
			}
		})
	}
}
