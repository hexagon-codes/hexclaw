package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/llmrouter"
)

type ollamaTargetSnapshot struct {
	config.OllamaTargetConfig
	ResolvedBaseURL string `json:"resolved_base_url"`
	TargetID        string `json:"target_id"`
	TargetDigest    string `json:"target_digest"`
	CanRestart      bool   `json:"can_restart"`
	warmupProviders []config.LLMProviderConfig
}

func (t ollamaTargetSnapshot) endpoint(path string) string {
	return t.ResolvedBaseURL + "/" + strings.TrimLeft(path, "/")
}
func (t ollamaTargetSnapshot) client(total, header time.Duration) *http.Client {
	c := egress.NewConfiguredOllamaClient(header)
	c.Timeout = total
	return c
}
func (s *Server) ollamaTargetLocked() ollamaTargetSnapshot {
	var cfg config.OllamaTargetConfig
	if s.cfg != nil {
		cfg = s.cfg.Ollama
	}
	cfg.AssociatedProviderInstanceIDs = append([]string{}, cfg.AssociatedProviderInstanceIDs...)
	if cfg.Mode == "" {
		cfg.Mode = "default"
	}
	base, err := cfg.Resolve(s.ollamaBaseURL)
	if err != nil {
		base = ""
	}
	target := ollamaTargetSnapshot{OllamaTargetConfig: cfg, ResolvedBaseURL: base, TargetID: config.OllamaTargetHash(base), TargetDigest: cfg.Digest(base), CanRestart: s.ollamaProcessManaged && cfg.Mode == "default" && (base == "http://localhost:11434" || base == "http://127.0.0.1:11434")}
	if s.cfg != nil {
		names := make([]string, 0, len(s.cfg.LLM.Providers))
		for name := range s.cfg.LLM.Providers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			provider := s.cfg.LLM.Providers[name]
			if strings.TrimRight(provider.BaseURL, "/") != base || (provider.Enabled != nil && !*provider.Enabled) {
				continue
			}
			for _, id := range cfg.AssociatedProviderInstanceIDs {
				if provider.ProviderInstanceID == id {
					target.warmupProviders = append(target.warmupProviders, provider)
					break
				}
			}
		}
	}
	return target
}

// 预热参数与目标在同一配置锁下冻结，不读取切换后的 Provider。
func (t ollamaTargetSnapshot) warmupParameters(model string, requested int) (int, string) {
	numCtx, keepAlive := resolveWarmupNumCtx(requested), defaultWarmupKeepAlive
	for _, provider := range t.warmupProviders {
		if provider.Model != model {
			continue
		}
		if provider.NumCtx > 0 {
			numCtx = provider.NumCtx
		}
		if value := strings.TrimSpace(provider.KeepAlive); value != "" {
			keepAlive = value
		}
		break
	}
	return numCtx, keepAlive
}
func (s *Server) ollamaTarget() ollamaTargetSnapshot {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.ollamaTargetLocked()
}
func (s *Server) SetOllamaProcessManaged(managed bool) { s.ollamaProcessManaged = managed }

// 已选目标的模型目录与连接探测沿用管理、下载和推理的精确地址合同。
func (s *Server) configuredOllamaTargetFor(base string) string {
	target := s.ollamaTarget().ResolvedBaseURL
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if target != "" && (base == target || base == target+"/v1") {
		return target
	}
	return ""
}

type ollamaTargetUpdateRequest struct {
	ExpectedConfigRevision *uint64                    `json:"expected_config_revision"`
	ExpectedConfigDigest   *string                    `json:"expected_config_digest"`
	ExpectedTargetRevision *uint64                    `json:"expected_target_revision"`
	ExpectedTargetDigest   *string                    `json:"expected_target_digest"`
	Ollama                 *config.OllamaTargetConfig `json:"ollama"`
}

func newOllamaTargetMutationProof(req ollamaTargetUpdateRequest, id string) (llmConfigMutationProof, error) {
	id = strings.TrimSpace(id)
	if id == "" || req.ExpectedConfigRevision == nil || req.ExpectedConfigDigest == nil || req.ExpectedTargetRevision == nil || req.ExpectedTargetDigest == nil || req.Ollama == nil || req.Ollama.AssociatedProviderInstanceIDs == nil {
		return llmConfigMutationProof{}, fmt.Errorf("Ollama target update requires conditions, provider IDs and Idempotency-Key")
	}
	if !llmConfigMutationRequestIDPattern.MatchString(id) {
		return llmConfigMutationProof{}, errLLMConfigMutationIDInvalid
	}
	raw, err := json.Marshal(struct {
		Operation string
		Request   ollamaTargetUpdateRequest
	}{"ollama_target", req})
	return llmConfigMutationProof{requestID: id, requestDigest: sha256Digest(raw), operationKind: "ollama_target"}, err
}
func (s *Server) prepareOllamaTargetUpdate(req ollamaTargetUpdateRequest, old config.OllamaTargetConfig, llm config.LLMConfig) (config.OllamaTargetConfig, config.LLMConfig, error) {
	base, err := old.Resolve(s.ollamaBaseURL)
	if err != nil {
		return old, llm, err
	}
	if *req.ExpectedTargetRevision != old.TargetRevision || *req.ExpectedTargetDigest != old.Digest(base) {
		return old, llm, fmt.Errorf("Ollama target configuration changed")
	}
	next := *req.Ollama
	base, err = next.Resolve(s.ollamaBaseURL)
	if err != nil {
		return old, llm, err
	}
	if next.Mode == "default" {
		next.CustomBaseURL = ""
	} else {
		next.CustomBaseURL = base
	}
	next.TargetRevision = old.TargetRevision + 1
	if next.TargetRevision == 0 {
		return old, llm, fmt.Errorf("Ollama target revision exhausted")
	}
	next.AssociatedProviderInstanceIDs = append([]string{}, next.AssociatedProviderInstanceIDs...)
	sort.Strings(next.AssociatedProviderInstanceIDs)
	providers := make(map[string]config.LLMProviderConfig, len(llm.Providers))
	for name, p := range llm.Providers {
		providers[name] = p
	}
	seen := map[string]bool{}
	for _, id := range next.AssociatedProviderInstanceIDs {
		if seen[id] {
			return old, llm, fmt.Errorf("Duplicate associated provider identity")
		}
		seen[id] = true
		found := false
		for name, p := range providers {
			if config.EffectiveProviderInstanceID(name, p) != id {
				continue
			}
			if !llmrouter.UsesOllamaNativeAdapter(name, p) && !p.HasOllamaTarget() {
				return old, llm, fmt.Errorf("Associated provider does not use an Ollama adapter")
			}
			if llmrouter.UsesOllamaNativeAdapter(name, p) {
				p.BaseURL = base
			} else {
				p.BaseURL = base + "/v1"
			}
			p.OllamaTargetBaseURL = base
			providers[name] = p
			found = true
			break
		}
		if !found {
			return old, llm, fmt.Errorf("Associated provider not found")
		}
	}
	llm.Providers = providers
	return next, llm, nil
}

func reconcileOllamaProviderAssociations(old config.OllamaTargetConfig, llm *config.LLMConfig) config.OllamaTargetConfig {
	ids := make([]string, 0, len(old.AssociatedProviderInstanceIDs))
	for _, id := range old.AssociatedProviderInstanceIDs {
		for name, p := range llm.Providers {
			if config.EffectiveProviderInstanceID(name, p) == id && p.HasOllamaTarget() {
				ids = append(ids, id)
				break
			}
		}
	}
	if len(ids) != len(old.AssociatedProviderInstanceIDs) {
		old.AssociatedProviderInstanceIDs = ids
		old.TargetRevision++
	}
	return old
}
