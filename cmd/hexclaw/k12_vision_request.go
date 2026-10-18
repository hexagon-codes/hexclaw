package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/toolkit/util/logger"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// completeK12VisionRequest 是生产代码和真实模型探针共用的唯一 K12 图片与提示词到
// Provider 请求边界。调用方负责路由和出网用途；该函数负责与线路相关的请求契约，
// 避免强类型识题策略、操作安全性、MIME 处理和持久 deadline 在不同入口间漂移。
func completeK12VisionRequest(
	ctx context.Context,
	provider llm.Provider,
	model string,
	image []byte,
	prompt string,
) (string, error) {
	if provider == nil {
		return "", fmt.Errorf("K12 vision provider is not configured")
	}
	if len(image) == 0 {
		return "", fmt.Errorf("K12 vision image is empty")
	}
	ctx = egress.WithRequest(
		ctx,
		egress.PurposeVisionOCR,
		"",
		egress.ClassSensitiveMedia,
	)

	requestMetadata, reasoningPolicyScope, err := k12VisionRequestMetadata(ctx)
	if err != nil {
		return "", err
	}
	mime := http.DetectContentType(image)
	if !strings.HasPrefix(strings.ToLower(mime), "image/") {
		mime = "image/png"
	}
	dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(image)

	// 调用方的持久 deadline 始终具有权威性。这里只把相同的剩余预算传入受保护的
	// 请求局部 HTTP transport；绝不修改共享客户端，也不为旧调用虚构 deadline。
	ctx = withK12StageResponseHeaderDeadline(ctx)
	request := llm.CompletionRequest{
		Model:                model,
		Metadata:             requestMetadata,
		ReasoningPolicyScope: reasoningPolicyScope,
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			MultiContent: []llm.ContentPart{
				llm.NewTextPart(prompt),
				llm.NewImageURLPart(dataURL, "high"),
			},
		}},
	}
	if k12.RecognitionLayoutInitialReadJSONOutputFromContext(ctx) {
		request.ResponseFormat = &llm.ResponseFormat{Type: "json_object"}
	}
	started := time.Now()
	response, err := provider.Complete(k12NonIdempotentLLMContext(ctx), request)
	routeProvider, adapterProvider := k12ProviderLogIdentity(ctx, provider)
	fields := []any{
		"route_provider", routeProvider, "adapter_provider", adapterProvider, "model", model,
		"prompt_bytes", len(prompt), "image_bytes", len(image), "elapsed_ms", time.Since(started).Milliseconds(),
		"response_available", response != nil,
	}
	if key, present := egress.ProviderClientRequestKeyFromContext(ctx); present {
		fields = append(fields, "provider_request_key", key)
	}
	if policy, marked := k12.GradingModelRequestPolicyFromContext(ctx); marked {
		fields = append(fields, "request_policy_version", policy.PolicyVersion,
			"request_stage", policy.Stage, "expected_reasoning_effort", policy.ReasoningEffort)
	}
	// 只记录 SDK 已返回的计数与控制回执；缺少回执不解释为已关闭推理或零推理 Token。
	if response != nil {
		usageAvailable := response.Usage.PromptTokens > 0 || response.Usage.CompletionTokens > 0 ||
			response.Usage.TotalTokens > 0 || response.Usage.CacheReadTokens > 0 || response.Usage.CacheCreationTokens > 0
		fields = append(fields, "output_bytes", len(response.Content),
			"usage_available", usageAvailable,
			"reasoning_receipt_available", response.ReasoningReceipt != nil)
		// SDK 的零值不能区分真实零用量与上游省略 usage，缺证时不输出确定性计数。
		if usageAvailable {
			fields = append(fields, "prompt_tokens", response.Usage.PromptTokens,
				"completion_tokens", response.Usage.CompletionTokens, "total_tokens", response.Usage.TotalTokens,
				"cache_read_tokens", response.Usage.CacheReadTokens, "cache_creation_tokens", response.Usage.CacheCreationTokens)
		}
		if receipt := response.ReasoningReceipt; receipt != nil {
			fields = append(fields, "reasoning_sent", receipt.Sent, "reasoning_accepted", receipt.Accepted,
				"reasoning_observed", receipt.Observed, "reasoning_tokens", receipt.ReasoningTokens)
		}
	}
	if err != nil {
		logger.FromContext(ctx).Warn("K12 vision request finished", append(fields,
			"outcome", "provider_error", "error_type", fmt.Sprintf("%T", err))...)
		return "", err
	}
	if response == nil {
		logger.FromContext(ctx).Warn("K12 vision request finished", append(fields, "outcome", "empty_response")...)
		return "", fmt.Errorf("K12 vision provider returned an empty response")
	}
	logger.FromContext(ctx).Info("K12 vision request finished", append(fields, "outcome", "completed")...)
	return response.Content, nil
}
