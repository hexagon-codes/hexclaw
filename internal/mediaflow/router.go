// Package mediaflow 承载应用媒体路由、操作和资产生命周期。
package mediaflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"

	imagegen "github.com/hexagon-codes/ai-core/media/image"
	videogen "github.com/hexagon-codes/ai-core/media/video"
	"github.com/hexagon-codes/ai-core/media/voice"
	"github.com/hexagon-codes/ai-core/media/voicechat"
	"github.com/hexagon-codes/hexclaw/config"
)

// Route 保存首次选定的非敏感执行身份；凭据始终从原配置解析。
type Route struct {
	LegacyProviderAlias string           `json:"legacy_provider_alias,omitempty"`
	DefaultModel        bool             `json:"default_model,omitempty"`
	SpeechProviders     []SpeechProvider `json:"speech_providers,omitempty"`
	SpeechVoice         string           `json:"speech_voice,omitempty"`
	SpeechSpeed         float64          `json:"speech_speed,omitempty"`
	Revision            string           `json:"config_revision"`
	ProviderID          string           `json:"provider_instance_id"`
	ProviderKey         string           `json:"provider_key"`
	Model               string           `json:"model"`
	Kind                string           `json:"kind"`
	Action              string           `json:"action"`
	Protocol            string           `json:"protocol"`
	Endpoint            string           `json:"endpoint"`
	CredentialRef       string           `json:"credential_ref,omitempty"`
}

// Snapshot 在请求内只读，热更新整体替换，不修改已有服务或路由。
type Snapshot struct {
	Revision        string
	ToolEnabled     bool
	Routes          []Route
	Images          *imagegen.Service
	Videos          *videogen.Service
	Voice           *voice.Service
	Chat            *voicechat.Service
	providers       map[string]config.LLMProviderConfig
	providerKeys    map[string]string
	defaultProvider string
}

type Router struct{ current atomic.Pointer[Snapshot] }

func NewRouter() *Router             { return &Router{} }
func (r *Router) Current() *Snapshot { return r.current.Load() }

// Reload 只消费已有能力声明和兼容协议，不以域名推断供应商。
func (r *Router) Reload(cfg *config.Config, speech *voice.Service) {
	s := &Snapshot{ToolEnabled: cfg.Skill.Builtin.MediaGen, Voice: speech, providers: make(map[string]config.LLMProviderConfig), providerKeys: make(map[string]string)}
	images := make(map[string]imagegen.Provider)
	videos := make(map[string]videogen.Provider)
	chats := make(map[string]voicechat.Provider)
	keys := make([]string, 0, len(cfg.LLM.Providers))
	for key := range cfg.LLM.Providers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		pc := cfg.LLM.Providers[key]
		id := config.EffectiveProviderInstanceID(key, pc)
		s.providers[id] = pc
		s.providerKeys[id] = key
		if key == cfg.LLM.Default {
			s.defaultProvider = id
		}
		if pc.Enabled != nil && !*pc.Enabled || pc.APIKey == "" {
			continue
		}
		protocol := strings.ToLower(strings.TrimSpace(pc.Compatible))
		if protocol == "" && (key == "openai" || key == "zhipu" || key == "智谱 AI") {
			protocol = "openai"
		}
		if protocol != "openai" {
			continue
		}
		_, specs := config.NormalizeProviderModelSpecs(pc)
		var imageModels, videoModels, chatModels []string
		for _, spec := range specs {
			for _, capability := range spec.Capabilities {
				switch capability {
				case config.LLMModelCapabilityImageGeneration:
					imageModels = append(imageModels, spec.ID)
				case config.LLMModelCapabilityVideoGeneration:
					videoModels = append(videoModels, spec.ID)
				case config.LLMModelCapabilityAudio:
					// 单次音频协议仅复用已有明确支持的模型，宽泛audio不猜为TTS。
					for _, known := range voicechat.NewOpenAIVoiceChat("", "").SupportedModels() {
						if spec.ID == known {
							chatModels = append(chatModels, spec.ID)
						}
					}
				}
			}
		}
		// 原生端点沿已有SDK的精确协议与模型目录，保留旧媒体API能力。
		// 私有网关不能通过域名包含品牌名获得这个原生协议判断。
		nativeImageDefault := ""
		nativeChatDefault := ""
		nativeZhipu := false
		if endpoint, err := url.Parse(pc.BaseURL); err == nil && endpoint.Scheme == "https" && endpoint.Host == "open.bigmodel.cn" && strings.TrimRight(endpoint.Path, "/") == "/api/paas/v4" {
			nativeZhipu = true
			if len(imageModels) == 0 {
				imageModels = append(imageModels, imagegen.NewZhipuCogView("", "").SupportedModels()...)
				if len(imageModels) > 0 {
					nativeImageDefault = imageModels[0]
				}
			}
			if len(videoModels) == 0 {
				videoModels = append(videoModels, videogen.NewZhipuCogVideoX("", "").SupportedModels()...)
			}
		}
		if endpoint, err := url.Parse(pc.BaseURL); err == nil && endpoint.Scheme == "https" && endpoint.Host == "api.openai.com" && strings.TrimRight(endpoint.Path, "/") == "/v1" {
			chatModels = voicechat.NewOpenAIVoiceChat("", "").SupportedModels()
			if len(chatModels) > 0 {
				nativeChatDefault = chatModels[0]
			}
		}
		base := Route{ProviderID: id, ProviderKey: key, Endpoint: pc.BaseURL, CredentialRef: pc.CredentialRef}
		for _, model := range imageModels {
			route := base
			route.Kind = "image"
			if nativeZhipu {
				route.LegacyProviderAlias = "zhipu-cogview"
			}
			route.Action = "generate"
			route.Protocol = "openai-image"
			route.Model = model
			route.DefaultModel = model == nativeImageDefault || model == pc.Model
			s.Routes = append(s.Routes, route)
		}
		if len(imageModels) > 0 {
			images[id] = imagegen.NewOpenAICompatible(id, pc.APIKey, pc.BaseURL, imageModels)
		}
		for _, model := range videoModels {
			route := base
			route.Kind = "video"
			if nativeZhipu {
				route.LegacyProviderAlias = "zhipu-cogvideox"
			}
			route.Action = "generate"
			route.Model = model
			route.Protocol = ""
			for _, known := range videogen.NewZhipuCogVideoX("", "").SupportedModels() {
				if model == known {
					route.Protocol = "zhipu-video"
				}
			}
			if route.Protocol == "" {
				continue
			}
			route.DefaultModel = model == pc.Model || model == "cogvideox-2"
			s.Routes = append(s.Routes, route)
			// 请求用例按冻结路由构造适配器，目录服务仅用于现有状态查询。
			videos[id] = videoProvider(route, pc.APIKey)
		}
		if len(chatModels) > 0 {
			chats[id] = voicechat.NewOpenAIVoiceChat(pc.APIKey, pc.BaseURL)
			for _, model := range chatModels {
				route := base
				route.Kind = "audio"
				if nativeChatDefault != "" {
					route.LegacyProviderAlias = "openai-gpt4o-audio"
					route.DefaultModel = model == nativeChatDefault
				}
				route.Action = "chat"
				route.Protocol = "openai-voicechat"
				route.Model = model
				s.Routes = append(s.Routes, route)
			}
		}
	}
	if cfg.Voice.Enabled && speech != nil && speech.HasTTS() {
		providers := speechProviders(cfg)
		selected := speech.TTSName()
		if len(providers) == 1 {
			selected = providers[0].ID
		} else if len(providers) > 1 {
			selected = "speech-chain:" + digest(providers)
		}
		s.Routes = append(s.Routes, Route{ProviderID: selected, ProviderKey: cfg.Voice.TTS.Provider, Kind: "audio", Action: "synthesize", Protocol: "tts", SpeechProviders: providers, SpeechVoice: cfg.Voice.TTS.Voice, SpeechSpeed: cfg.Voice.TTS.Speed})
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%v|%t", s.Routes, s.ToolEnabled)))
	s.Revision = hex.EncodeToString(digest[:])
	for i := range s.Routes {
		s.Routes[i].Revision = s.Revision
	}
	s.Images = imagegen.NewService(images, "")
	s.Videos = videogen.NewService(videos, "")
	s.Chat = voicechat.NewService(chats, "")
	r.current.Store(s)
}

func videoProvider(route Route, key string) videogen.Provider {
	if route.Protocol == "zhipu-video" {
		return videogen.NewZhipuCogVideoX(key, route.Endpoint)
	}
	return nil
}

// Resolve 不以同名模型替代显式实例；多个未指定候选沿已有默认，否则明确缺口。
func (r *Router) Resolve(kind, action, provider, model, preferred string) (Route, error) {
	s := r.Current()
	if s == nil {
		return Route{}, fmt.Errorf("media service is not configured")
	}
	var candidates []Route
	for _, route := range s.Routes {
		if route.Kind != kind || route.Action != action {
			continue
		}
		if provider != "" && provider != route.ProviderID && provider != route.ProviderKey && provider != route.LegacyProviderAlias {
			continue
		}
		if model != "" && model != route.Model {
			continue
		}
		candidates = append(candidates, route)
	}
	// 已明确选择的未分类模型可按已知图片协议执行一次，不修改其能力声明。
	if len(candidates) == 0 && kind == "image" && action == "generate" && model != "" {
		for id, pc := range s.providers {
			if pc.Enabled != nil && !*pc.Enabled || pc.APIKey == "" || strings.ToLower(strings.TrimSpace(pc.Compatible)) != "openai" {
				continue
			}
			key := s.providerKeys[id]
			if provider != "" && provider != id && provider != key {
				continue
			}
			_, specs := config.NormalizeProviderModelSpecs(pc)
			for _, spec := range specs {
				if spec.ID == model && len(spec.Capabilities) == 0 {
					candidates = append(candidates, Route{Revision: s.Revision, ProviderID: id, ProviderKey: key, Model: model, Kind: kind, Action: action, Protocol: "openai-image", Endpoint: pc.BaseURL, CredentialRef: pc.CredentialRef})
					break
				}
			}
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if provider == "" {
		for _, want := range []string{preferred, s.defaultProvider} {
			var matches []Route
			for _, route := range candidates {
				if want != "" && (want == route.ProviderID || want == route.ProviderKey) {
					matches = append(matches, route)
				}
			}
			if len(matches) == 1 {
				return matches[0], nil
			}
			if len(matches) > 1 {
				if route, ok := uniqueDefaultRoute(matches); ok {
					return route, nil
				}
				return Route{}, ErrAmbiguous
			}
		}
	}
	if len(candidates) == 0 {
		return Route{}, ErrUnavailable
	}
	if route, ok := uniqueDefaultRoute(candidates); ok {
		return route, nil
	}
	return Route{}, ErrAmbiguous
}

func uniqueDefaultRoute(routes []Route) (Route, bool) {
	var selected Route
	count := 0
	for _, route := range routes {
		if route.DefaultModel {
			selected = route
			count++
		}
	}
	return selected, count == 1
}

func digest(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
