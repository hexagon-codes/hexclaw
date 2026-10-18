package dingtalk

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/messagecontent"
)

func TestDingTalkEmphasisNewAdapterManifestKeepsCanonicalAndAttachment(t *testing.T) {
	const source = "**原因：**后文已经写了使用过程。\n\n**“先检查。”**再说明。"
	const want = "**原因**：后文已经写了使用过程。\n\n**“先检查。”** 再说明。"
	attachment := adapter.Attachment{Type: "image", Name: "original.png", Mime: "image/png", Data: base64.StdEncoding.EncodeToString(testPNGBytes(t))}
	digest, _, err := dingTalkAttachmentIdentity(attachment)
	if err != nil {
		t.Fatal(err)
	}
	for _, producer := range []messagecontent.ProducerKind{messagecontent.ProducerChat, messagecontent.ProducerK12, messagecontent.ProducerSkill} {
		t.Run(string(producer), func(t *testing.T) {
			reply := &adapter.Reply{Content: source, Attachments: []adapter.Attachment{attachment}, Metadata: map[string]string{"producer_kind": string(producer), "locale": "zh-CN"}}
			if err := ensureDingTalkRenderEvidence(reply); err != nil {
				t.Fatal(err)
			}
			if reply.Content != source || reply.MessageContent.Markdown != source || reply.MessageContent.ProducerKind != producer || reply.RenderManifest.Parts[0].Text != want {
				t.Fatalf("fresh adapter projection changed source or missed normalized boundary: %+v", reply.RenderManifest)
			}
			if reply.MessageContent.Attachments[0].Digest != digest || reply.RenderManifest.Parts[1].ArtifactDigest != digest || reply.Attachments[0] != attachment || reply.RenderManifest.ValidateFor(*reply.MessageContent) != nil {
				t.Fatal("emphasis projection changed attachment identity or canonical evidence")
			}
		})
	}
}

func TestDingTalkEmphasisFrozenAdapterManifestRemainsByteIdentical(t *testing.T) {
	const old = "**原因：**旧回执原字节。"
	attachment := adapter.Attachment{Type: "image", Name: "original.png", Mime: "image/png", Data: base64.StdEncoding.EncodeToString(testPNGBytes(t))}
	parts := canonicalDingTalkDeliveryPartsForTest(t, old, attachment)
	canonical := *parts[0].MessageContent
	manifest := *parts[0].RenderManifest
	beforeCanonical, _ := json.Marshal(canonical)
	beforeManifest, _ := json.Marshal(manifest)
	reply := &adapter.Reply{Content: old, MessageContent: &canonical, RenderManifest: &manifest, Attachments: []adapter.Attachment{attachment}}
	if err := ensureDingTalkRenderEvidence(reply); err != nil {
		t.Fatal(err)
	}
	afterCanonical, _ := json.Marshal(reply.MessageContent)
	afterManifest, _ := json.Marshal(reply.RenderManifest)
	if !reflect.DeepEqual(beforeCanonical, afterCanonical) || !reflect.DeepEqual(beforeManifest, afterManifest) || reply.RenderManifest.Parts[0].Text != old {
		t.Fatal("a frozen compatible manifest was reformatted at send time")
	}
}
