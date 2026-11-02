package llmrouter

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hexagon-codes/ai-core/llm/openai"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/egress"
)

// ComputerUseSelection 区分当前聊天首选与任务明确指定的 CU 配置。
type ComputerUseSelection struct {
	CurrentProvider, CurrentModel string
	Provider, Model               string
}

type ComputerUseRoute struct {
	ProviderName string
	Model        string
	Client       openai.ResponsesProvider
}

type computerUseProvider struct {
	next   openai.ResponsesProvider
	policy *egress.Policy
	model  string
}

func (p *computerUseProvider) CreateResponse(ctx context.Context, request openai.ResponsesRequest) (*openai.ResponsesResponse, error) {
	if request.Model != p.model {
		return nil, fmt.Errorf("computer use route requires exact model %q", p.model)
	}
	if p.policy != nil {
		if err := p.policy.GuardContext(ctx); err != nil {
			return nil, fmt.Errorf("computer use egress: %w", err)
		}
	}
	return p.next.CreateResponse(ctx, request)
}

// SelectComputerUse 只认精确模型的 computer_use 声明，不把视觉或函数调用当作 CU。
// 原始可选协议能力在相同读锁下捕获，返回的客户端保留原远端出口检查。
func (r *Selector) SelectComputerUse(selection ComputerUseSelection) (ComputerUseRoute, error) {
	if r == nil {
		return ComputerUseRoute{}, ErrNoProvider
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	clean := func(value string) string {
		value = strings.TrimSpace(value)
		if strings.EqualFold(value, "auto") {
			return ""
		}
		return value
	}
	selection.Provider, selection.Model = clean(selection.Provider), clean(selection.Model)
	current := clean(selection.CurrentProvider)
	if current == "" {
		current = r.defaultP
	}
	current, _ = r.canonicalNameLocked(current)
	preferred := current
	if selection.Provider != "" {
		var ok bool
		preferred, ok = r.canonicalNameLocked(selection.Provider)
		if !ok {
			return ComputerUseRoute{}, fmt.Errorf("%w: computer use provider is not configured", ErrModelCapabilityMismatch)
		}
	}
	resolve := func(name, exactModel string) (ComputerUseRoute, bool) {
		pc, exists := r.cfg.Providers[name]
		if !exists || (pc.Enabled != nil && !*pc.Enabled) || !r.isProviderHealthyLocked(name) {
			return ComputerUseRoute{}, false
		}
		pc = r.effectiveModelSpecsLocked(name, pc)
		client, ok := r.providers[name].(openai.ResponsesProvider)
		if !ok || client == nil {
			return ComputerUseRoute{}, false
		}
		model := exactModel
		if model != "" {
			if !config.ModelHasCapabilities(pc, model, "computer_use") {
				return ComputerUseRoute{}, false
			}
		} else {
			var found bool
			model, found = config.PreferredModelWithCapabilities(pc, "computer_use")
			if !found {
				return ComputerUseRoute{}, false
			}
		}
		var policy *egress.Policy
		if !r.isLocalProviderName(name) {
			policy = r.egressPolicy
		}
		return ComputerUseRoute{ProviderName: name, Model: model, Client: &computerUseProvider{next: client, policy: policy, model: model}}, true
	}
	if selection.Model != "" {
		if route, ok := resolve(preferred, selection.Model); ok {
			return route, nil
		}
		return ComputerUseRoute{}, fmt.Errorf("%w: exact computer use model is unavailable or lacks the required protocol", ErrModelCapabilityMismatch)
	}
	if preferred == current {
		if route, ok := resolve(preferred, clean(selection.CurrentModel)); ok {
			return route, nil
		}
	}
	if route, ok := resolve(preferred, ""); ok {
		return route, nil
	}
	if selection.Provider != "" {
		return ComputerUseRoute{}, fmt.Errorf("%w: selected provider has no computer use model", ErrNoCapableModel)
	}
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		if name != preferred {
			names = append(names, name)
		}
	}
	var priorities map[string]int
	if r.cfg.Routing.Enabled {
		switch r.cfg.Routing.Strategy {
		case "cost-aware":
			priorities = costPriority
		case "quality-first":
			priorities = qualityPriority
		case "latency-first":
			priorities = latencyPriority
		}
	}
	priority := func(name string) int {
		if value, ok := priorities[name]; ok {
			return value
		}
		return 999
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := names[i], names[j]
		if priorities != nil && priority(a) != priority(b) {
			return priority(a) < priority(b)
		}
		if a == r.defaultP || b == r.defaultP {
			return a == r.defaultP
		}
		if priorities == nil && r.isLocalProviderName(a) != r.isLocalProviderName(b) {
			return !r.isLocalProviderName(a)
		}
		return a < b
	})
	for _, name := range names {
		if route, ok := resolve(name, ""); ok {
			return route, nil
		}
	}
	return ComputerUseRoute{}, fmt.Errorf("%w: no configured model declares computer_use and supports Responses", ErrNoCapableModel)
}
