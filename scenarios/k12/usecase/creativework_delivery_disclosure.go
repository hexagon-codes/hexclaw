package usecase

import (
	"context"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type creativeWorkDeliveryDisclosureKey struct{}

type creativeWorkDeliveryDisclosure struct {
	canonicalDigest string
	locale          string
	markdown        string
}

// WithCreativeWorkDeliveryDisclosure 仅从当前作品来源读取同代次的真实执行事实。
// 历史或缺少来源证明时省略说明，不阻断作品正文与原图投递。
func (d Deps) WithCreativeWorkDeliveryDisclosure(ctx context.Context, view CreativeWorkView, canonical string) context.Context {
	if d.Records == nil || d.ImageTaskResult == nil || view.Record == nil || view.Fields.SourceIntakeID == "" {
		return ctx
	}
	intake, err := d.Records.GetCreativeWorkIntake(ctx, view.Record.AgentName, view.Fields.SourceIntakeID)
	if err != nil || intake.PromotedWorkID != view.Record.RecordID || intake.DispatchID == "" {
		return ctx
	}
	result, err := d.ImageTaskResult(ctx, view.Record.AgentName, intake.DispatchID)
	if err != nil {
		return ctx
	}
	return WithCreativeWorkResultDeliveryDisclosure(ctx, view, result, canonical)
}

// WithCreativeWorkResultDeliveryDisclosure 为 API 和自动图片入口投影同一创作事实。
// 上下文只传递当前 canonical 对应的渠道说明，不改 canonical、来源附件或去重身份。
func WithCreativeWorkResultDeliveryDisclosure(ctx context.Context, view CreativeWorkView, result ImageTaskResult, canonical string) context.Context {
	if view.Record == nil || view.GenerationState.Latest == nil || result.Creative == nil ||
		result.CreativeWork == nil || result.CreativeWork.Record == nil || result.CreativeWork.GenerationState.Latest == nil {
		return ctx
	}
	latest := view.GenerationState.Latest
	actual := result.CreativeWork.GenerationState.Latest
	dispatch := result.Dispatch
	if result.Kind != "creative" || dispatch.AgentName != view.Record.AgentName ||
		result.Creative.AgentName != view.Record.AgentName || result.CreativeWork.Record.AgentName != view.Record.AgentName ||
		result.Creative.PromotedWorkID != view.Record.RecordID || result.CreativeWork.Record.RecordID != view.Record.RecordID ||
		result.Creative.IntakeID != view.Fields.SourceIntakeID || result.Creative.DispatchID != dispatch.DispatchID ||
		dispatch.TargetObjectID != result.Creative.IntakeID || dispatch.SourceDigest == "" || dispatch.SourceDigest != result.Creative.SourceDigest ||
		latest.Status != k12.WorkFeedbackSucceeded || actual.Status != k12.WorkFeedbackSucceeded ||
		latest.Feedback == nil || actual.Feedback == nil || latest.GenerationID == "" || latest.GenerationID != actual.GenerationID ||
		result.Creative.PromotedGenerationID != latest.GenerationID ||
		latest.Feedback.ProjectionMarkdown != actual.Feedback.ProjectionMarkdown || strings.TrimSpace(canonical) == "" {
		return ctx
	}
	locale := strings.TrimSpace(dispatch.OperationRouteRequest.MessageLocale)
	switch locale {
	case "zh-CN", "en", "ug-CN":
	default:
		return ctx
	}
	facts := adapter.ExecutionSummary{}
	// 独立 OCR 必须确有本次回执；分类已包含转写时不虚构第二次调用。
	for _, operation := range []string{"classification", "writing_ocr", "work_feedback"} {
		for _, receipt := range result.OperationReceipts {
			if receipt.Operation != operation || receipt.Status != "succeeded" || receipt.InvocationID == "" || receipt.ResultDigest == "" ||
				receipt.CanonicalInputDigest != dispatch.SourceDigest {
				continue
			}
			stage := "image_recognition"
			if operation == "work_feedback" {
				stage = "answer_generation"
			}
			facts.Steps = append(facts.Steps, adapter.ExecutionSummaryStep{Key: stage, Status: "success"})
			if receipt.ExecutionKind == string(k12.GradingExecutionProvider) && strings.TrimSpace(receipt.Model) != "" {
				facts.Models = append(facts.Models, adapter.ExecutionSummaryModel{Stage: stage, Provider: receipt.Provider, Model: receipt.Model})
			}
		}
	}
	// 点评方法来源属于加载依据，不能作为已执行 Skill 或 MCP 的回执。
	markdown := adapter.RenderExecutionSummary(locale, facts)
	if markdown == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, creativeWorkDeliveryDisclosureKey{}, creativeWorkDeliveryDisclosure{
		canonicalDigest: deliveryDigest(strings.TrimSpace(canonical)), locale: locale, markdown: markdown,
	})
}

// CreativeWorkDeliveryDisclosure 仅读取当前正文的已证明说明，供渠道冻结投影使用。
func CreativeWorkDeliveryDisclosure(ctx context.Context, canonical string) (locale, markdown string) {
	if ctx == nil {
		return "", ""
	}
	disclosure, ok := ctx.Value(creativeWorkDeliveryDisclosureKey{}).(creativeWorkDeliveryDisclosure)
	if !ok || disclosure.canonicalDigest != deliveryDigest(strings.TrimSpace(canonical)) {
		return "", ""
	}
	return disclosure.locale, disclosure.markdown
}
