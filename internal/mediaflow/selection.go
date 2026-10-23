package mediaflow

import (
	"errors"
	"github.com/hexagon-codes/ai-core/transport"
	"github.com/hexagon-codes/hexclaw/config"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Selection 来自可信用户原文或显式入口，工具参数不能覆盖用户选择。
type Selection struct {
	Mode           string   `json:"mode"`
	Provider       string   `json:"provider,omitempty"`
	Models         []string `json:"models,omitempty"`
	ExcludedModels []string `json:"excluded_models,omitempty"`
}

type GenerationAttempt struct {
	ProviderID string `json:"provider_instance_id"`
	Model      string `json:"model"`
	Outcome    string `json:"outcome"`
	ErrorCode  string `json:"error_code,omitempty"`
	ElapsedMS  int64  `json:"elapsed_ms"`
}

func mediaNameIndex(text, name string) int {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || unicode.IsSpace(r) })
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	if len(parts) == 0 {
		return -1
	}
	re := regexp.MustCompile(`(?i)` + strings.Join(parts, `[-_\s]*`))
	word := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' }
	for _, span := range re.FindAllStringIndex(text, -1) {
		if span[1] < len(text) && word(text[span[1]]) || span[0] > 0 && word(text[span[0]-1]) {
			continue
		}
		return span[0]
	}
	return -1
}

// UserSelection 只从当前用户原文识别配置目录中的名字，不消费历史或模型补写文本。
func (r *Router) UserSelection(text, kind string) *Selection {
	p := &Selection{Mode: "auto"}
	s := r.Current()
	if s == nil {
		return p
	}
	names := make(map[string]bool)
	for _, route := range s.Routes {
		if route.Kind == kind {
			names[route.Model] = true
		}
	}
	capability := config.LLMModelCapabilityImageGeneration
	if kind == "video" {
		capability = config.LLMModelCapabilityVideoGeneration
	}
	for _, pc := range s.providers {
		_, specs := config.NormalizeProviderModelSpecs(pc)
		for _, spec := range specs {
			for _, c := range spec.Capabilities {
				if c == capability {
					names[spec.ID] = true
				}
			}
		}
	}
	type mention struct {
		name string
		at   int
	}
	var mentions []mention
	for name := range names {
		at := mediaNameIndex(text, name)
		if at < 0 {
			continue
		}
		prefix := strings.TrimSpace(strings.ToLower(text[:at]))
		negative := false
		for _, marker := range []string{"不要用", "不要使用", "别用", "不用", "do not use", "don't use"} {
			if strings.HasSuffix(prefix, marker) {
				negative = true
			}
		}
		if negative {
			p.ExcludedModels = append(p.ExcludedModels, name)
		} else {
			mentions = append(mentions, mention{name, at})
		}
	}
	sort.Slice(mentions, func(i, j int) bool {
		if mentions[i].at == mentions[j].at {
			return len(mentions[i].name) > len(mentions[j].name)
		}
		return mentions[i].at < mentions[j].at
	})
	for _, m := range mentions {
		p.Models = append(p.Models, m.name)
	}
	sort.Strings(p.ExcludedModels)
	for id, key := range s.providerKeys {
		if mediaNameIndex(text, key) >= 0 || mediaNameIndex(text, id) >= 0 {
			if p.Provider != "" && p.Provider != id {
				p.Provider = ""
				break
			}
			p.Provider = id
		}
	}
	if len(p.Models) > 0 || p.Provider != "" {
		p.Mode = "pinned"
		lower := strings.ToLower(text)
		for _, marker := range []string{"优先", "prefer", "不行就换", "失败可以换", "失败可换"} {
			if strings.Contains(lower, marker) {
				p.Mode = "preferred"
			}
		}
		for _, marker := range []string{"只用", "必须用", "不要换", "不能换", "only use", "do not switch"} {
			if strings.Contains(lower, marker) {
				p.Mode = "pinned"
			}
		}
	}
	return p
}

func hasModel(models []string, model string) bool {
	for _, candidate := range models {
		if candidate == model {
			return true
		}
	}
	return false
}

// planRoutes 冻结少量候选；同名不同实例保持独立，显式选择不自动换模型。
func (r *Router) planRoutes(req Request) ([]Route, error) {
	if req.Kind != "image" && req.Kind != "video" || req.Selection == nil && (req.Provider != "" || req.Model != "") {
		route, err := r.Resolve(req.Kind, req.Action, req.Provider, req.Model, req.PreferredProvider)
		return []Route{route}, err
	}
	s := r.Current()
	if s == nil {
		return nil, ErrUnavailable
	}
	p := req.Selection
	if p == nil {
		p = &Selection{Mode: "auto"}
	}
	var routes []Route
	for _, route := range s.Routes {
		if route.Kind != req.Kind || route.Action != req.Action || hasModel(p.ExcludedModels, route.Model) {
			continue
		}
		if p.Mode == "pinned" && (p.Provider != "" && route.ProviderID != p.Provider || len(p.Models) > 0 && !hasModel(p.Models, route.Model)) {
			continue
		}
		routes = append(routes, route)
	}
	if len(routes) == 0 {
		return nil, ErrUnavailable
	}
	score := func(route Route) int {
		score := 0
		for i, model := range p.Models {
			if route.Model == model {
				score += 10000 - i*100
				break
			}
		}
		if route.ProviderID == p.Provider {
			score += 5000
		}
		if route.Model == req.Model {
			score += 1000
		}
		if req.Provider != "" && (req.Provider == route.ProviderID || req.Provider == route.ProviderKey) {
			score += 500
		}
		if req.PreferredProvider != "" && (req.PreferredProvider == route.ProviderID || req.PreferredProvider == route.ProviderKey) {
			score += 100
		}
		if route.ProviderID == s.defaultProvider {
			score += 50
		}
		if route.DefaultModel {
			score += 10
		}
		return score
	}
	sort.SliceStable(routes, func(i, j int) bool { return score(routes[i]) > score(routes[j]) })
	limit := 3
	if p.Mode == "pinned" {
		limit = 1
	}
	if len(routes) > limit {
		routes = routes[:limit]
	}
	return routes, nil
}

func (s *Service) recordAttempt(op *Operation, started time.Time, outcome, code string) {
	a := GenerationAttempt{ProviderID: op.Route.ProviderID, Model: op.Route.Model, Outcome: outcome, ErrorCode: code, ElapsedMS: time.Since(started).Milliseconds()}
	if len(op.Attempts) > op.AttemptIndex {
		op.Attempts[op.AttemptIndex] = a
	} else {
		op.Attempts = append(op.Attempts, a)
	}
}

func (s *Service) failover(op *Operation, err error) bool {
	if op.CancelRequested || op.UpstreamID != "" || len(op.Assets) > 0 || op.SaveState != "not_started" {
		return false
	}
	var upstream *transport.ProviderError
	if !errors.As(err, &upstream) {
		return false
	}
	// 仅明确未完成的鉴权、模型不存在或额度拒绝可换候选，未知与内容/参数拒绝不重发。
	if upstream.StatusCode != 401 && upstream.StatusCode != 404 && upstream.StatusCode != 429 {
		return false
	}
	for next := op.AttemptIndex + 1; next < len(op.CandidateRoutes); next++ {
		route := op.CandidateRoutes[next]
		pc, ok := s.router.Current().providers[route.ProviderID]
		if !ok || pc.APIKey == "" || pc.Enabled != nil && !*pc.Enabled {
			continue
		}
		op.AttemptIndex, op.Route = next, route
		op.GenerationState = "queued"
		op.Error, op.ErrorCode = "", ""
		if s.repo.Update(s.ctx, op, op.Revision) == nil {
			s.signal()
			return true
		}
		return false
	}
	return false
}
