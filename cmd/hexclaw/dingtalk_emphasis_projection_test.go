package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/channel"
	"github.com/hexagon-codes/hexclaw/cron"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestDingTalkEmphasisK12AndWritingTargetsShareProjection(t *testing.T) {
	const source = "**原因：**后文已经写了使用过程。"
	const want = "**原因**：后文已经写了使用过程。"
	image := []byte("unchanged-source-image-bytes")
	sum := sha256.Sum256(image)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	for _, writing := range []bool{false, true} {
		deliverer := &k12IMDeliverer{}
		prepared, err := deliverer.PrepareMessageForTargets(t.Context(), k12usecase.DeliveryMessage{Content: source, WritingFeedback: writing, Attachments: []k12usecase.DeliveryAttachment{{Name: "original.png", MIME: "image/png", Data: image}}}, []k12usecase.ResolvedDeliveryTarget{
			{BindingID: "dingtalk-binding", Target: k12.DeliveryTarget{Platform: "dingtalk", InstanceID: "dingtalk-instance", ChatID: "parent"}},
			{BindingID: "feishu-binding", Target: k12.DeliveryTarget{Platform: "feishu", InstanceID: "feishu-instance", ChatID: "parent"}},
		})
		if err != nil || len(prepared) != 4 {
			t.Fatalf("prepare complete K12/writing targets: parts=%d err=%v", len(prepared), err)
		}
		for _, receipt := range prepared {
			var part channel.DeliveryPart
			if err := json.Unmarshal([]byte(receipt.PayloadJSON), &part); err != nil {
				t.Fatal(err)
			}
			if part.MessageContent.Markdown != source || part.MessageContent.Attachments[0].Digest != digest || part.RenderManifest.ValidateFor(*part.MessageContent) != nil {
				t.Fatal("new emphasis projection changed original K12 content or attachment evidence")
			}
			if part.Kind == messagecontent.PartMarkdown {
				expected := source
				if receipt.Target.Platform == "dingtalk" {
					expected = want
				}
				if part.Text != expected {
					t.Fatalf("writing=%t platform=%s projected=%q want=%q", writing, receipt.Target.Platform, part.Text, expected)
				}
			} else if part.Attachment == nil || string(part.Attachment.Data) != string(image) || part.Digest != digest {
				t.Fatal("projection changed original image bytes")
			}
		}
	}
}

func TestDingTalkEmphasisCronAndSendMessageKeepOtherChannels(t *testing.T) {
	const source = "**原因：**定时或工具正文。"
	const normalized = "**原因**：定时或工具正文。"
	for _, platform := range []string{"dingtalk", "feishu"} {
		t.Run(platform, func(t *testing.T) {
			want := source
			if platform == "dingtalk" {
				want = normalized
			}
			var cronMessage channel.Message
			calls := 0
			deliver := newCronIMDeliver(t.Context(), nil, func(_ context.Context, target, chatID string, message channel.Message) error {
				if target != platform || chatID != "parent" {
					t.Fatal("cron target changed")
				}
				calls++
				cronMessage = message
				return nil
			})
			if err := deliver(&cron.Job{ID: "existing-cron", ChatID: "parent"}, platform, source); err != nil || calls != 1 {
				t.Fatalf("cron projection failed: calls=%d err=%v", calls, err)
			}
			if cronMessage.Content.Markdown != source || cronMessage.Content.ProducerKind != messagecontent.ProducerCron || cronMessage.RenderManifest.Parts[0].Text != want || cronMessage.RenderManifest.ValidateFor(*cronMessage.Content) != nil {
				t.Fatal("cron failed to preserve canonical while applying only the DingTalk projection")
			}
			capture := &producerCaptureSender{}
			sender := &instanceMessageSender{mgr: capture}
			raw := []byte("unchanged-tool-image")
			attachment := adapter.Attachment{Type: "image", Name: "tool-result.png", Mime: "image/png", Data: base64.StdEncoding.EncodeToString(raw)}
			if err := sender.Send(t.Context(), platform, "parent", source, []adapter.Attachment{attachment}); err != nil || capture.calls != 1 {
				t.Fatalf("send_message projection failed: calls=%d err=%v", capture.calls, err)
			}
			reply := capture.reply
			sum := sha256.Sum256(raw)
			if reply.Content != source || reply.MessageContent.Markdown != source || reply.MessageContent.ProducerKind != messagecontent.ProducerTool || reply.RenderManifest.Parts[0].Text != want || reply.MessageContent.Attachments[0].Digest != "sha256:"+hex.EncodeToString(sum[:]) || reply.Attachments[0] != attachment {
				t.Fatal("send_message changed source/attachment identity or normalized another channel")
			}
		})
	}
}

type emphasisResolvedSender struct {
	producerCaptureSender
	platforms map[string]adapter.Platform
	resolved  []string
}

func (s *emphasisResolvedSender) RunningPlatform(target string) (adapter.Platform, bool) {
	s.resolved = append(s.resolved, target)
	platform, ok := s.platforms[target]
	return platform, ok
}

func TestDingTalkEmphasisSenderResolvesActualPlatform(t *testing.T) {
	const source = "**原因：**按真实运行实例投递。"
	for _, tc := range []struct {
		target   string
		platform adapter.Platform
		want     string
	}{
		{"stable-opaque-id", adapter.PlatformDingtalk, "**原因**：按真实运行实例投递。"},
		{"old-exact-instance-name", adapter.PlatformDingtalk, "**原因**：按真实运行实例投递。"},
		{"dingtalk-looking-feishu-id", adapter.PlatformFeishu, source},
	} {
		t.Run(tc.target, func(t *testing.T) {
			capture := &emphasisResolvedSender{platforms: map[string]adapter.Platform{tc.target: tc.platform}}
			sender := &instanceMessageSender{mgr: capture}
			if err := sender.Send(t.Context(), tc.target, "original-recipient", source, nil); err != nil {
				t.Fatal(err)
			}
			if len(capture.resolved) != 1 || capture.resolved[0] != tc.target || capture.calls != 1 || capture.target != tc.target || capture.chatID != "original-recipient" || capture.reply.MessageContent.Markdown != source || capture.reply.RenderManifest.Parts[0].Text != tc.want {
				t.Fatal("stable identity/name was guessed from text or changed during projection")
			}
		})
	}
}
