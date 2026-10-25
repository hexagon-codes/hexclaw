package usecase

import (
	"bytes"
	"context"
	"strings"
)

// conversationReportDeliveryMessage 只改变钉钉投影，规范正文保留完整报告和其他工具结果。
// reports 已由原消息可信引用解析；短回复不必投递文件，显式重发保留文件意图。
func conversationReportDeliveryMessage(ctx context.Context, req FinalReplyDeliveryRequest, message DeliveryMessage, reports []PrintableArtifactView, explicitDocument bool, suffix string) (DeliveryMessage, error) {
	var err error
	message.Source, err = finalReplySource(req, message)
	if err != nil {
		return DeliveryMessage{}, err
	}
	message.Source.DingTalkSuffix = suffix
	projected := finalReplyFullProjection(ctx, message, "")
	if len(reports) > 0 && FinalReplyUsesPDF(message.Content, explicitDocument) {
		for _, report := range reports {
			body := strings.TrimSpace(report.Artifact.CanonicalMarkdown)
			if body != "" {
				summary := finalReplyPDFSummary(FinalReplyDeliveryRequest{Title: report.Artifact.Title, Locale: req.Locale})
				projected.Content = strings.ReplaceAll(projected.Content, body, summary)
			}
			projected.Attachments = append(projected.Attachments, DeliveryAttachment{
				Name: finalReplyAttachmentName(report.Artifact.Title), MIME: "application/pdf", Data: bytes.Clone(report.Render.Payload),
			})
		}
	}
	if suffix != "" {
		projected.Content += "\n\n" + suffix
	}
	message.Projections = map[string]DeliveryMessage{"dingtalk": projected}
	return message, nil
}
