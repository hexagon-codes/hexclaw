package usecase

import (
	"context"
	"strings"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type creativeWorkDeliveryDisclosureKey struct{}
type creativeWorkDeliveryProjectionKey struct{}

type creativeWorkDeliveryProjection struct {
	canonicalDigest string
	markdown        string
}

// 带图写作只省略渠道中的整篇原稿，点评里的原句、局部示范和历史参考稿均完整保留。
// 原稿仍在 canonical 中用于导出、复制及去重；实际投递必须带原图才可使用本投影。
func withCreativeWorkDeliveryProjection(ctx context.Context, view CreativeWorkView, canonical string) context.Context {
	if view.Record == nil || view.Fields.WorkType != k12.WorkTypeWriting || view.GenerationState.Latest == nil ||
		view.GenerationState.Latest.Status != k12.WorkFeedbackSucceeded || view.GenerationState.Latest.Feedback == nil {
		return ctx
	}
	source := k12.CreativeWorkSourceSnapshot{WorkType: view.Fields.WorkType, DisplayName: view.Fields.DisplayName, WorkTitle: view.Fields.WorkTitle}
	if view.GenerationState.Initial != nil {
		source = view.GenerationState.Initial.Source
	} else if len(view.Fields.Versions) > 0 {
		version := view.Fields.Versions[len(view.Fields.Versions)-1]
		source.ContentMarkdown, source.SourceAssetID = version.ContentMarkdown, version.SourceAssetID
	}
	if source.WorkType != k12.WorkTypeWriting || strings.TrimSpace(source.SourceAssetID) == "" || source.ContentMarkdown == "" {
		return ctx
	}
	displayName, title := strings.TrimSpace(source.DisplayName), strings.TrimSpace(source.WorkTitle)
	if displayName == "" {
		displayName = view.Fields.DisplayName
	}
	if title == "" {
		title = view.Fields.WorkTitle
	}
	header := []string{displayName}
	if title != "" && title != displayName {
		header = append(header, title)
	}
	feedback := view.GenerationState.Latest.Feedback.ProjectionMarkdown
	whole := append(append([]string(nil), header...), source.ContentMarkdown, feedback)
	if strings.TrimSpace(strings.Join(whole, "\n\n")) != strings.TrimSpace(canonical) {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, creativeWorkDeliveryProjectionKey{}, creativeWorkDeliveryProjection{
		canonicalDigest: deliveryDigest(strings.TrimSpace(canonical)),
		markdown:        strings.Join(append(header, feedback), "\n\n"),
	})
}

// CreativeWorkImageDeliveryProjection 只返回同一 canonical 的带图作文投影，不解析或裁剪任意正文。
func CreativeWorkImageDeliveryProjection(ctx context.Context, canonical string) (string, bool) {
	if ctx == nil {
		return "", false
	}
	projection, ok := ctx.Value(creativeWorkDeliveryProjectionKey{}).(creativeWorkDeliveryProjection)
	if !ok || projection.canonicalDigest != deliveryDigest(strings.TrimSpace(canonical)) {
		return "", false
	}
	return projection.markdown, true
}

type creativeWorkDeliveryDisclosure struct {
	canonicalDigest string
	locale          string
	markdown        string
	facts           messagecontent.ExecutionSummary
}

// WithCreativeWorkDeliveryDisclosure 仅从当前作品来源读取同代次的真实执行事实。
// 历史或缺少来源证明时省略说明，不阻断作品正文与原图投递。
func (d Deps) WithCreativeWorkDeliveryDisclosure(ctx context.Context, view CreativeWorkView, canonical string) context.Context {
	ctx = withCreativeWorkDeliveryProjection(ctx, view, canonical)
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
	ctx = withCreativeWorkDeliveryProjection(ctx, view, canonical)
	if view.Record == nil || view.GenerationState.Initial == nil || view.GenerationState.Latest == nil || result.Creative == nil ||
		result.CreativeWork == nil || result.CreativeWork.Record == nil ||
		result.CreativeWork.GenerationState.Initial == nil || result.CreativeWork.GenerationState.Latest == nil {
		return ctx
	}
	// 推广绑定初始点评；后续点评必须保留同一冻结来源，并使用当前代次的执行回执。
	initial := view.GenerationState.Initial
	actualInitial := result.CreativeWork.GenerationState.Initial
	latest := view.GenerationState.Latest
	actual := result.CreativeWork.GenerationState.Latest
	dispatch := result.Dispatch
	if result.Kind != "creative" || dispatch.AgentName != view.Record.AgentName ||
		result.Creative.AgentName != view.Record.AgentName || result.CreativeWork.Record.AgentName != view.Record.AgentName ||
		result.Creative.PromotedWorkID != view.Record.RecordID || result.CreativeWork.Record.RecordID != view.Record.RecordID ||
		result.Creative.IntakeID != view.Fields.SourceIntakeID || result.Creative.DispatchID != dispatch.DispatchID ||
		dispatch.TargetObjectID != result.Creative.IntakeID || dispatch.SourceDigest == "" || dispatch.SourceDigest != result.Creative.SourceDigest ||
		initial.AgentName != view.Record.AgentName || actualInitial.AgentName != view.Record.AgentName ||
		initial.WorkID != view.Record.RecordID || actualInitial.WorkID != view.Record.RecordID ||
		latest.AgentName != view.Record.AgentName || actual.AgentName != view.Record.AgentName ||
		latest.WorkID != view.Record.RecordID || actual.WorkID != view.Record.RecordID ||
		latest.Status != k12.WorkFeedbackSucceeded || actual.Status != k12.WorkFeedbackSucceeded ||
		latest.Feedback == nil || actual.Feedback == nil || latest.GenerationID == "" || latest.GenerationID != actual.GenerationID ||
		initial.GenerationID == "" || result.Creative.PromotedGenerationID != initial.GenerationID || initial.GenerationID != actualInitial.GenerationID ||
		initial.Source != actualInitial.Source || latest.Source != initial.Source || actual.Source != latest.Source ||
		latest.Feedback.ProjectionMarkdown != actual.Feedback.ProjectionMarkdown || strings.TrimSpace(canonical) == "" {
		return ctx
	}
	locale := strings.TrimSpace(dispatch.OperationRouteRequest.MessageLocale)
	switch locale {
	case "zh-CN", "en", "ug-CN":
	default:
		return ctx
	}
	facts := messagecontent.ExecutionSummary{}
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
			facts.Steps = append(facts.Steps, messagecontent.ExecutionSummaryStep{Key: stage, Status: "success"})
			if receipt.ExecutionKind == string(k12.GradingExecutionProvider) && strings.TrimSpace(receipt.Model) != "" {
				model := messagecontent.ExecutionSummaryModel{Stage: stage, Provider: receipt.Provider, Model: receipt.Model}
				if receipt.Provider == dispatch.OperationRouteRequest.Provider {
					model.ProviderDisplayName = dispatch.OperationRouteRequest.ProviderDisplayName
					model.ProviderInstanceID = dispatch.OperationRouteRequest.ProviderInstanceID
				}
				facts.Models = append(facts.Models, model)
			}
		}
	}
	// 点评方法来源属于加载依据，不能作为已执行 Skill 或 MCP 的回执。
	markdown := messagecontent.RenderExecutionSummary(locale, facts)
	if markdown == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, creativeWorkDeliveryDisclosureKey{}, creativeWorkDeliveryDisclosure{
		canonicalDigest: deliveryDigest(strings.TrimSpace(canonical)), locale: locale, markdown: markdown, facts: facts,
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

// CreativeWorkDeliveryExecutionSummary 只向装配层提供同一正文的执行事实。
func CreativeWorkDeliveryExecutionSummary(ctx context.Context, canonical string) (string, messagecontent.ExecutionSummary, bool) {
	if ctx == nil {
		return "", messagecontent.ExecutionSummary{}, false
	}
	disclosure, ok := ctx.Value(creativeWorkDeliveryDisclosureKey{}).(creativeWorkDeliveryDisclosure)
	if !ok || disclosure.canonicalDigest != deliveryDigest(strings.TrimSpace(canonical)) {
		return "", messagecontent.ExecutionSummary{}, false
	}
	return disclosure.locale, disclosure.facts, true
}

func (d Deps) executionSummaryFooter(locale string, facts messagecontent.ExecutionSummary) string {
	if d.ExecutionSummaryFooter != nil {
		return d.ExecutionSummaryFooter(locale, facts)
	}
	return messagecontent.RenderExecutionSummary(locale, facts)
}
