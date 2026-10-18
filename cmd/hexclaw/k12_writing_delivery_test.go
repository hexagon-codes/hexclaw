package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/channel"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestK12WritingImageDeliveryProjectsOnlyDingTalkVisibleOriginal(t *testing.T) {
	original := "我的爸爸很忙。每天晚上他都会陪我读书。"
	feedback := "## 具体肯定\n原句：我的爸爸很忙。\n\n## 关键问题\n两句话的时间关系还不清楚。\n\n## 局部示范\n保留‘我的爸爸很忙’，只补真实时间。\n\n## 孩子下一步\n补一句后读给家长听。\n\n### 旧家长参考稿\n历史点评的这一段完整保留，不因渠道去重删减。"
	canonical := "语文写作\n\n我的好爸爸\n\n" + original + "\n\n" + feedback
	visible := "语文写作\n\n我的好爸爸\n\n" + feedback
	image := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}
	for _, tc := range []struct {
		name, platform, workType, sourceAsset string
		withImage, mismatched                 bool
		wantProjection                        bool
	}{
		{"dingtalk_image", "dingtalk", k12.WorkTypeWriting, "asset://child/image.png", true, false, true},
		{"other_channel", "feishu", k12.WorkTypeWriting, "asset://child/image.png", true, false, false},
		{"text_only", "dingtalk", k12.WorkTypeWriting, "asset://child/image.png", false, false, false},
		{"no_source_identity", "dingtalk", k12.WorkTypeWriting, "", true, false, false},
		{"art_unchanged", "dingtalk", k12.WorkTypeArt, "asset://child/image.png", true, false, false},
		{"canonical_mismatch", "dingtalk", k12.WorkTypeWriting, "asset://child/image.png", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			generation := &k12.WorkFeedbackGeneration{GenerationID: "feedback-1", Status: k12.WorkFeedbackSucceeded,
				Source:   k12.CreativeWorkSourceSnapshot{WorkType: tc.workType, DisplayName: "语文写作", WorkTitle: "我的好爸爸", ContentMarkdown: original, SourceAssetID: tc.sourceAsset},
				Feedback: &k12.WorkFeedback{ProjectionMarkdown: feedback}}
			view := k12usecase.CreativeWorkView{Record: &records.AgentRecord{AgentName: "child", RecordID: "work-1"},
				Fields:          k12.CreativeWorkFields{WorkType: tc.workType, DisplayName: "语文写作", WorkTitle: "我的好爸爸"},
				GenerationState: k12.CreativeWorkGenerationState{Initial: generation, Latest: generation}}
			ctx := (k12usecase.Deps{}).WithCreativeWorkDeliveryDisclosure(context.Background(), view, canonical)
			content := canonical
			if tc.mismatched {
				content += "\n\n另一版内容"
			}
			message := k12usecase.DeliveryMessage{Content: content}
			if tc.withImage {
				message.Attachments = []k12usecase.DeliveryAttachment{{Name: "作文.png", MIME: "image/png", Data: image}}
			}
			prepared, err := (&k12IMDeliverer{}).PrepareMessageForTargets(ctx, message, []k12usecase.ResolvedDeliveryTarget{{BindingID: "binding-1", Target: k12.DeliveryTarget{Platform: tc.platform, InstanceID: "bot-1", ChatID: "parent-1"}}})
			if err != nil {
				t.Fatal(err)
			}
			wantParts := 1
			if tc.withImage {
				wantParts++
			}
			if len(prepared) != wantParts {
				t.Fatalf("got %d parts, want %d", len(prepared), wantParts)
			}
			var textPart channel.DeliveryPart
			if err := json.Unmarshal([]byte(prepared[0].PayloadJSON), &textPart); err != nil {
				t.Fatal(err)
			}
			wantText := content
			if tc.wantProjection {
				wantText = visible
			}
			if textPart.Text != wantText || textPart.MessageContent == nil || textPart.MessageContent.Markdown != content {
				t.Fatalf("visible/canonical boundary changed: text=%q canonical=%+v", textPart.Text, textPart.MessageContent)
			}
			if !strings.Contains(textPart.Text, feedback) {
				t.Fatal("feedback examples or old full reference were truncated")
			}
			if tc.withImage {
				var imagePart channel.DeliveryPart
				if err := json.Unmarshal([]byte(prepared[1].PayloadJSON), &imagePart); err != nil {
					t.Fatal(err)
				}
				if imagePart.Attachment == nil || !bytes.Equal(imagePart.Attachment.Data, image) || imagePart.Attachment.MIME != "image/png" || imagePart.MessageContent.Markdown != content {
					t.Fatalf("original image or canonical was changed: %+v", imagePart)
				}
			}
		})
	}
}
