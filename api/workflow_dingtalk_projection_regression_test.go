package api

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/messagecontent"
)

func TestWorkflowDingTalkExcerptKeepsMarkdownBoundaries(t *testing.T) {
	prefix := strings.Repeat("学", 220)
	for _, tc := range []struct {
		name, source, want string
	}{
		{"long emphasis", "**" + strings.Repeat("重点学习方向：分数应用题需要结合数量关系进行练习；", 12) + "**", ""},
		{"link at boundary", prefix + " [资料](https://example.invalid/" + strings.Repeat("a", 40) + ")", prefix + "…"},
		{"emphasis around link", "**" + strings.Repeat("A", 210) + " [ref](https://example.invalid/" + strings.Repeat("x", 60) + ")**", "**" + strings.Repeat("A", 210) + "…**"},
		{"code at boundary", prefix + " `" + strings.Repeat("x", 40) + "`", prefix + "…"},
		{"short code", "使用 `score_total` 查看分数。", "使用 `score_total` 查看分数。"},
		{"linked emphasis next to Chinese", "**[资料](https://example.invalid/a)**继续阅读。", "**[资料](https://example.invalid/a)** 继续阅读。"},
		{"autolink at boundary", strings.Repeat("A", 230) + " <https://example.invalid/" + strings.Repeat("x", 60) + ">", strings.Repeat("A", 230) + "…"},
		{"projected budget", "前文**（原因：**说明" + strings.Repeat("学", 300), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, err := messagecontent.New(messagecontent.ProducerWorkflow, "zh-CN", tc.source, nil)
			if err != nil {
				t.Fatal(err)
			}
			sourceDigest := source.SourceDigest
			delivery := WorkflowDelivery{Kind: "dingtalk", ContentMode: "both", Content: tc.source, MessageContent: &source}
			if err := prepareWorkflowDingTalkProjection(&delivery, false); err != nil {
				t.Fatal(err)
			}
			text := delivery.RenderManifest.Parts[0].Text
			if len([]rune(text)) > 240 || text == "" {
				t.Fatalf("excerpt length is invalid: %d", len([]rune(text)))
			}
			if tc.want != "" && text != tc.want {
				t.Fatalf("excerpt broke an atomic Markdown unit: got %q, want %q", text, tc.want)
			}
			if tc.name == "long emphasis" && (!strings.HasPrefix(text, "**") || !strings.HasSuffix(text, "…**") || strings.Count(text, "**") != 2) {
				t.Fatalf("excerpt lost its paired emphasis: %q", text)
			}
			if source.Markdown != tc.source || source.SourceDigest != sourceDigest {
				t.Fatal("excerpt changed the original canonical content")
			}
		})
	}
}

func TestWorkflowDingTalkFormulaProjectionPreservesSourceAndFrozenParts(t *testing.T) {
	const raw = "## 学习分析\n\n分数目标：$\\frac{3}{4}$，继续巩固。"
	for _, tc := range []struct {
		name, source, want string
	}{
		{"fraction at boundary", strings.Repeat("A", 230) + `$\frac{3}{4}$` + strings.Repeat("B", 30), strings.Repeat("A", 230) + "3/4BBBBBB…"},
		{"power at boundary", strings.Repeat("A", 237) + `$x^2$` + strings.Repeat("B", 30), strings.Repeat("A", 237) + "x²…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, err := messagecontent.New(messagecontent.ProducerWorkflow, "en", tc.source, nil)
			if err != nil {
				t.Fatal(err)
			}
			delivery := WorkflowDelivery{Kind: "dingtalk", ContentMode: "both", Content: tc.source, MessageContent: &source}
			if err := prepareWorkflowDingTalkProjection(&delivery, false); err != nil {
				t.Fatalf("a complete formula was rejected at the excerpt boundary: %v", err)
			}
			if text := delivery.RenderManifest.Parts[0].Text; text != tc.want || len([]rune(text)) > 240 {
				t.Fatalf("formula was cut before projection: got %q, want %q", text, tc.want)
			}
			if delivery.MessageContent.Markdown != tc.source || delivery.MessageContent.SourceDigest != source.SourceDigest {
				t.Fatal("excerpt replaced the complete canonical source")
			}
		})
	}
	t.Run("currency is not a formula", func(t *testing.T) {
		const price = "价格 $5-$10，今天 $4。"
		source, err := messagecontent.New(messagecontent.ProducerWorkflow, "zh-CN", price, nil)
		if err != nil {
			t.Fatal(err)
		}
		delivery := WorkflowDelivery{Kind: "dingtalk", ContentMode: "message", Content: price, MessageContent: &source}
		if err := prepareWorkflowDingTalkProjection(&delivery, false); err != nil {
			t.Fatalf("valid currency was rejected as a formula: %v", err)
		}
		if delivery.RenderManifest.Parts[0].Text != price || delivery.RenderManifest.CapabilitySnapshot.TeXMath {
			t.Fatal("currency text or channel capability changed")
		}
	})
	for _, mode := range []string{"", "message", "both"} {
		t.Run("new/"+mode, func(t *testing.T) {
			source, err := messagecontent.New(messagecontent.ProducerWorkflow, "zh-CN", raw, nil)
			if err != nil {
				t.Fatal(err)
			}
			sourceDigest := source.SourceDigest
			delivery := WorkflowDelivery{Kind: "dingtalk", ContentMode: mode, Content: raw, MessageContent: &source}
			if err := prepareWorkflowDingTalkProjection(&delivery, false); err != nil {
				t.Fatal(err)
			}
			want := "## 学习分析\n\n分数目标：3/4，继续巩固。"
			if mode == "both" {
				want = "分数目标：3/4，继续巩固。"
			}
			manifest := delivery.RenderManifest
			if manifest.Parts[0].Text != want || manifest.CapabilitySnapshot.TeXMath || !manifest.CapabilitySnapshot.UnicodeMath || manifest.FallbackReason == "" {
				t.Fatalf("workflow did not use readable channel projection: %#v", manifest)
			}
			if source.Markdown != raw || source.SourceDigest != sourceDigest || !strings.Contains(delivery.MessageContent.Markdown, `\frac{3}{4}`) || manifest.SourceDigest != delivery.MessageContent.SourceDigest {
				t.Fatal("channel projection changed canonical source or its digest")
			}
			if err := manifest.ValidateFor(*delivery.MessageContent); err != nil {
				t.Fatalf("channel projection evidence is invalid: %v", err)
			}
		})
	}
	t.Run("append file keeps frozen body and receipt", func(t *testing.T) {
		original, _ := messagecontent.New(messagecontent.ProducerWorkflow, "zh-CN", raw, nil)
		frozen, err := messagecontent.BuildManifest(original, messagecontent.RenderRequest{
			Surface: messagecontent.SurfaceChannel, RendererVersion: "workflow-delivery-v1",
			Capabilities: messagecontent.CapabilitySnapshot{Markdown: true, TeXMath: true},
			Parts:        []messagecontent.RenderPart{{Kind: messagecontent.PartMarkdown, Text: raw}},
		})
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(raw))
		body := WorkflowDeliveryPart{Ordinal: 1, Kind: messagecontent.PartMarkdown, Digest: "sha256:" + hex.EncodeToString(sum[:]), Status: "delivered", Attempts: 1, ExternalMessageID: "frozen-provider-id"}
		artifactDigest := strings.Repeat("a", 64)
		withFile, _ := messagecontent.New(messagecontent.ProducerWorkflow, "zh-CN", raw, []messagecontent.AttachmentRef{{AssetID: "attachment:" + artifactDigest, Name: "report.pdf", AltText: "report.pdf", MIME: "application/pdf", Digest: "sha256:" + artifactDigest}})
		delivery := WorkflowDelivery{Kind: "dingtalk", Content: raw, MessageContent: &withFile, RenderManifest: &frozen, Parts: []WorkflowDeliveryPart{body}}
		if err := prepareWorkflowDeliveryParts(&delivery); err != nil {
			t.Fatal(err)
		}
		if len(delivery.Parts) != 2 || delivery.Parts[0] != body || delivery.Parts[1].Status != "pending" || delivery.RenderManifest.Parts[0].Text != raw || delivery.RenderManifest.RendererVersion != frozen.RendererVersion {
			t.Fatal("appending a file rebuilt the frozen body or its delivery receipt")
		}
	})
}
