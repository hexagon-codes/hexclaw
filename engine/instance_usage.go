package engine

import (
	"context"
	"encoding/json"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon/runtime"

	"github.com/hexagon-codes/hexclaw/adapter"
)

type usageAccountingContextKey struct{}

// 真实已知用量与预算估算分开保留；缺少货币金额回执不解释为免费。
type usageAccounting struct {
	Source        string
	Reported      *llm.Usage
	Estimated     *llm.Usage
	ReportedCalls int
	MissingCalls  int
}

func withResponseUsageAccounting(ctx context.Context, usage llm.Usage, receipt *llm.Usage) context.Context {
	if _, exists := ctx.Value(usageAccountingContextKey{}).(usageAccounting); exists {
		return ctx
	}
	value := usageAccounting{Source: "unknown", MissingCalls: 1}
	if receipt != nil || usage != (llm.Usage{}) {
		if receipt != nil {
			usage = *receipt
		}
		value.Source, value.ReportedCalls, value.MissingCalls = "reported", 1, 0
		value.Reported = &usage
	} else if cache, ok := ctx.Value(responseCacheContextKey{}).(*responseCacheState); ok {
		cache.mu.Lock()
		if cache.hit {
			value.MissingCalls = 0
		}
		cache.mu.Unlock()
	}
	return context.WithValue(ctx, usageAccountingContextKey{}, value)
}

func withRuntimeUsageAccounting(ctx context.Context, usage llm.Usage, metadata map[string]any) context.Context {
	source, _ := metadata[runtime.UsageSourceMetadataKey].(string)
	if source != "reported" && source != "mixed" && source != "unknown" {
		return withResponseUsageAccounting(ctx, usage, nil)
	}
	count := func(raw any) int {
		switch value := raw.(type) {
		case int:
			return value
		case int64:
			return int(value)
		case float64:
			return int(value)
		}
		return 0
	}
	value := usageAccounting{Source: source,
		ReportedCalls: count(metadata[runtime.UsageReportedCallsMetadataKey]), MissingCalls: count(metadata[runtime.UsageMissingCallsMetadataKey])}
	if source == "reported" || source == "mixed" {
		value.Reported = &usage
	}
	return context.WithValue(ctx, usageAccountingContextKey{}, value)
}

func withEstimatedUsageAccounting(ctx context.Context, usage llm.Usage) context.Context {
	value, ok := ctx.Value(usageAccountingContextKey{}).(usageAccounting)
	if !ok {
		value = usageAccounting{Source: "unknown"}
	}
	value.Estimated = &usage
	return context.WithValue(ctx, usageAccountingContextKey{}, value)
}

func shouldRecordUsageAccounting(ctx context.Context, usage llm.Usage) bool {
	value, _ := ctx.Value(usageAccountingContextKey{}).(usageAccounting)
	return usage.TotalTokens > 0 || value.ReportedCalls > 0 || value.MissingCalls > 0
}

// instanceCostMetadata 保留成本发生时的稳定账号身份和计量来源，不把估算记为上游实报。
func instanceCostMetadata(ctx context.Context) string {
	values := make(map[string]any)
	if identity, ok := adapter.PlatformInstanceIdentityFromContext(ctx); ok {
		raw, _ := json.Marshal(identity)
		_ = json.Unmarshal(raw, &values)
	}
	if usage, ok := ctx.Value(usageAccountingContextKey{}).(usageAccounting); ok {
		values[runtime.UsageSourceMetadataKey] = usage.Source
		values[runtime.UsageReportedCallsMetadataKey] = usage.ReportedCalls
		values[runtime.UsageMissingCallsMetadataKey] = usage.MissingCalls
		if usage.Reported != nil {
			values["usage_reported"] = usage.Reported
		}
		if usage.Estimated != nil {
			values["usage_estimated"] = usage.Estimated
		}
		values["cost_source"] = "unknown"
	}
	if len(values) == 0 {
		return ""
	}
	raw, _ := json.Marshal(values)
	return string(raw)
}
