package mediaflow

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hexagon-codes/ai-core/transport"

	"github.com/hexagon-codes/hexclaw/config"
)

// Selection 来自可信用户原文或显式入口，工具参数不能覆盖用户选择。
type Selection struct {
	Mode              string   `json:"mode"`
	Provider          string   `json:"provider,omitempty"`
	ProviderOrder     []string `json:"provider_order,omitempty"`
	ExcludedProviders []string `json:"excluded_providers,omitempty"`
	Models            []string `json:"models,omitempty"`
	ExcludedModels    []string `json:"excluded_models,omitempty"`
}

type GenerationAttempt struct {
	ProviderID string `json:"provider_instance_id"`
	Model      string `json:"model"`
	Outcome    string `json:"outcome"`
	ErrorCode  string `json:"error_code,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	ElapsedMS  int64  `json:"elapsed_ms"`
}

func mediaSelectionIndex(text, name string) int {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || unicode.IsSpace(r) })
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	if len(parts) == 0 {
		return -1
	}
	re := regexp.MustCompile(`(?i)` + strings.Join(parts, `[-_\s]*`))
	word := func(b byte) bool {
		return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b == '.' || b == '/'
	}
	for _, span := range re.FindAllStringIndex(text, -1) {
		if span[1] < len(text) && word(text[span[1]]) || span[0] > 0 && word(text[span[0]-1]) {
			continue
		}
		if !selectionDirective(text, span[0]) {
			continue
		}
		return span[0]
	}
	return -1
}

// selectionDirective 区分模型选择与图片文字中的型号；只识别当前句子的明确使用表达。
func selectionDirective(text string, at int) bool {
	if at == 0 {
		return true
	}
	prefix := strings.ToLower(text[:at])
	if i := strings.LastIndexAny(prefix, "，,。；;!?？\n"); i >= 0 {
		_, width := utf8.DecodeRuneInString(prefix[i:])
		prefix = prefix[i+width:]
	}
	prefix = selectionLead(prefix)
	for _, marker := range []string{"用", "使用", "调用", "选择", "选用", "优先", "只用", "必须用", "不要用", "不要使用", "别用", "不用", "模型", "失败可以换", "失败可换", "失败换", "不行就换", "use ", "using ", "prefer ", "model ", "only use ", "do not use ", "don't use ", "otherwise use ", "fallback to "} {
		if strings.HasPrefix(prefix, marker) || prefix == strings.TrimSpace(marker) {
			return true
		}
	}
	return false
}

func selectionLead(text string) string {
	prefix := strings.TrimSpace(strings.ToLower(text))
	for {
		changed := false
		for _, lead := range []string{"请", "帮我", "我想", "这次", "本次", "这张图", "这个视频", "可以", "能不能", "please "} {
			if strings.HasPrefix(prefix, lead) {
				prefix = strings.TrimSpace(strings.TrimPrefix(prefix, lead))
				changed = true
				break
			}
		}
		if !changed {
			break
		}
	}
	return prefix
}

// selectionPolicyText 屏蔽引用正文，只保留独立引用的已选模型或供应商名称。
func selectionPolicyText(text string, choices []string) string {
	quoted := map[rune]rune{'"': '"', '\'': '\'', '`': '`', '“': '”', '‘': '’'}
	runes := []rune(text)
	for start := 0; start < len(runes); start++ {
		endQuote, ok := quoted[runes[start]]
		if !ok {
			continue
		}
		if runes[start] == '\'' && start > 0 && start+1 < len(runes) && unicode.IsLetter(runes[start-1]) && unicode.IsLetter(runes[start+1]) {
			continue
		}
		end := start + 1
		for end < len(runes) && runes[end] != endQuote {
			end++
		}
		keep := false
		if end < len(runes) {
			content := strings.TrimSpace(string(runes[start+1 : end]))
			for _, choice := range choices {
				keep = keep || strings.EqualFold(content, choice)
			}
		}
		for i := start; i < len(runes) && i <= end; i++ {
			if !keep || i == start || i == end {
				runes[i] = ' '
			}
		}
		start = end
	}
	return string(runes)
}

// selectionPolicyMode 只接受选型或失败切换指令，不从画面文字推断降级授权。
func selectionPolicyMode(text string, choices []string) string {
	mode, fixed := "pinned", false
	text = selectionPolicyText(text, choices)
	for _, part := range strings.FieldsFunc(text, func(r rune) bool { return strings.ContainsRune("，,。；;!?？\n", r) }) {
		prefix := selectionLead(part)
		conditional := prefix
		for _, lead := range []string{"如果", "若"} {
			conditional = strings.TrimSpace(strings.TrimPrefix(conditional, lead))
		}
		hasChoice := false
		for _, choice := range choices {
			hasChoice = hasChoice || mediaSelectionIndex(part, choice) >= 0
		}
		if hasChoice && (strings.HasPrefix(prefix, "优先") || strings.HasPrefix(prefix, "prefer ")) {
			mode = "preferred"
		}
		for _, marker := range []string{"不行就换", "失败可以换", "失败可换", "otherwise use ", "fallback to "} {
			if strings.HasPrefix(conditional, marker) {
				mode = "preferred"
			}
		}
		for _, marker := range []string{"只用", "必须用", "only use "} {
			if hasChoice && strings.HasPrefix(prefix, marker) {
				fixed = true
			}
		}
		for _, lead := range []string{"即使失败也", "即便失败也", "失败后也", "失败也", "失败后"} {
			conditional = strings.TrimSpace(strings.TrimPrefix(conditional, lead))
		}
		for _, marker := range []string{"不要换", "不能换", "do not switch"} {
			if tail, ok := strings.CutPrefix(conditional, marker); ok {
				tail = strings.TrimSpace(tail)
				if tail == "" || strings.Contains(tail, "模型") || strings.Contains(tail, "供应商") || strings.Contains(tail, "model") || strings.Contains(tail, "provider") {
					fixed = true
				}
			}
		}
	}
	if fixed {
		return "pinned"
	}
	return mode
}

// UserSelection 只从当前用户原文识别配置目录中的名字，不消费历史或模型补写文本。
func (r *Router) UserSelection(text, kind string) *Selection {
	p := &Selection{Mode: "auto"}
	s := r.Current()
	if s == nil {
		return p
	}
	names := make(map[string]bool)
	for _, name := range s.MediaModels[kind] {
		names[name] = true
	}
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
			if kind == "image" && len(spec.Capabilities) == 0 {
				names[spec.ID] = true
			}
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
		at := mediaSelectionIndex(text, name)
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
	var providers []mention
	for id, key := range s.providerKeys {
		at := mediaSelectionIndex(text, key)
		if at < 0 {
			at = mediaSelectionIndex(text, id)
		}
		chosen := at >= 0 && selectionDirective(text, at)
		if at >= 0 && chosen {
			prefix := strings.TrimSpace(strings.ToLower(text[:at]))
			negative := false
			for _, marker := range []string{"不要用", "不要使用", "别用", "不用", "do not use", "don't use"} {
				if strings.HasSuffix(prefix, marker) {
					negative = true
				}
			}
			if negative {
				p.ExcludedProviders = append(p.ExcludedProviders, id)
			} else {
				providers = append(providers, mention{id, at})
			}
		}
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].at < providers[j].at })
	for _, provider := range providers {
		p.ProviderOrder = append(p.ProviderOrder, provider.name)
	}
	if len(p.ProviderOrder) > 0 {
		p.Provider = p.ProviderOrder[0]
	}
	sort.Strings(p.ExcludedProviders)
	if len(p.Models) > 0 || p.Provider != "" {
		choices := append([]string(nil), p.Models...)
		for _, id := range p.ProviderOrder {
			choices = append(choices, id, s.providerKeys[id])
		}
		p.Mode = selectionPolicyMode(text, choices)
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
		if route.Kind != req.Kind || route.Action != req.Action || hasModel(p.ExcludedModels, route.Model) || hasModel(p.ExcludedProviders, route.ProviderID) {
			continue
		}
		if p.Mode == "pinned" && (p.Provider != "" && route.ProviderID != p.Provider || len(p.Models) > 0 && !hasModel(p.Models, route.Model)) {
			continue
		}
		routes = append(routes, route)
	}
	// 已配置但尚未分类的图片模型沿既有显式协议入口试行，不自动改能力声明。
	models := p.Models
	provider := p.Provider
	for _, model := range models {
		if hasModel(p.ExcludedModels, model) {
			continue
		}
		route, err := r.Resolve(req.Kind, req.Action, provider, model, req.PreferredProvider)
		if err != nil || hasModel(p.ExcludedProviders, route.ProviderID) {
			continue
		}
		found := false
		for _, existing := range routes {
			if existing.ProviderID == route.ProviderID && existing.Model == route.Model {
				found = true
			}
		}
		if !found {
			routes = append(routes, route)
		}
	}
	if len(routes) == 0 {
		return nil, ErrUnavailable
	}
	score := func(route Route) int {
		score := 0
		for i, model := range p.Models {
			if route.Model == model {
				score += 1000000 - i*10000
				break
			}
		}
		if route.ProviderID == p.Provider {
			score += 5000
		}
		for i, provider := range p.ProviderOrder {
			if route.ProviderID == provider {
				score += 100000 - i*1000
				break
			}
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
		if !s.router.Current().allowsGeneration(route, op.Request) {
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
