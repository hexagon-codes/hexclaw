package mediaflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/hexagon-codes/ai-core/media/voice"
	"github.com/hexagon-codes/ai-core/transport"
	"github.com/hexagon-codes/hexclaw/config"
)

type SpeechProvider struct {
	ID       string `json:"provider_instance_id"`
	Adapter  string `json:"adapter"`
	Endpoint string `json:"endpoint,omitempty"`
	Region   string `json:"region,omitempty"`
}

type namedSpeechProvider struct {
	voice.TTSProvider
	id string
}

type speechAttemptContextKey struct{}
type SpeechAttempt struct {
	ProviderID string `json:"provider_instance_id"`
	Outcome    string `json:"outcome"`
}
type speechAttemptState struct {
	mu       sync.Mutex
	cancel   context.CancelFunc
	winner   string
	attempts []SpeechAttempt
}
type SpeechOutcome struct {
	Result     *voice.SynthesizeResult
	ProviderID string
	Attempts   []SpeechAttempt
}

func (p namedSpeechProvider) Synthesize(ctx context.Context, text string, opts voice.SynthesizeOptions) (*voice.SynthesizeResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := p.TTSProvider.Synthesize(ctx, text, opts)
	if state, ok := ctx.Value(speechAttemptContextKey{}).(*speechAttemptState); ok {
		state.mu.Lock()
		defer state.mu.Unlock()
		outcome := "succeeded"
		if err != nil {
			outcome = "unknown"
			if definitiveMediaFailure(err) {
				outcome = "failed"
			} else {
				state.cancel()
			}
		}
		if err == nil {
			state.winner = p.id
		}
		state.attempts = append(state.attempts, SpeechAttempt{ProviderID: p.id, Outcome: outcome})
	}
	return result, err
}

// definitiveMediaFailure 兼容已发布SDK；聚合失败必须每个尝试都明确未成功。
func definitiveMediaFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !definitiveMediaFailure(cause) {
				return false
			}
		}
		return true
	}
	if classified, ok := err.(interface{ DefinitiveFailure() bool }); ok {
		return classified.DefinitiveFailure()
	}
	if upstream, ok := err.(*transport.ProviderError); ok {
		switch upstream.StatusCode {
		case 400, 401, 403, 404, 422, 429:
			return true
		}
		return false
	}
	return definitiveMediaFailure(errors.Unwrap(err))
}

func (p namedSpeechProvider) Name() string { return p.TTSProvider.Name() }

func configuredProvider(cfg *config.Config, name string) (string, config.LLMProviderConfig, bool) {
	if provider, ok := cfg.LLM.Providers[name]; ok {
		return name, provider, true
	}
	for key, provider := range cfg.LLM.Providers {
		if config.EffectiveProviderInstanceID(key, provider) == name {
			return key, provider, true
		}
	}
	// 只兼容已有语音适配器后缀，不截断用户供应商名中的任意连字符。
	for _, suffix := range []string{"-tts", "-stt", "-whisper"} {
		if key, ok := strings.CutSuffix(name, suffix); ok {
			if provider, found := cfg.LLM.Providers[key]; found {
				return key, provider, true
			}
		}
	}
	return "", config.LLMProviderConfig{}, false
}

func speechProviders(cfg *config.Config) []SpeechProvider {
	var providers []SpeechProvider
	for _, name := range strings.Split(cfg.Voice.TTS.Provider, ",") {
		name = strings.TrimSpace(name)
		if name == "edge" || name == "edge-tts" {
			providers = append(providers, SpeechProvider{ID: "edge-tts", Adapter: "edge-tts"})
			continue
		}
		key, pc, ok := configuredProvider(cfg, name)
		if !ok || pc.APIKey == "" || pc.Enabled != nil && !*pc.Enabled {
			continue
		}
		adapter := "openai-tts"
		if name == "minimax" {
			adapter = "minimax"
		}
		providers = append(providers, SpeechProvider{ID: config.EffectiveProviderInstanceID(key, pc), Adapter: adapter, Endpoint: pc.BaseURL, Region: cfg.Voice.TTS.Region})
	}
	return providers
}

func frozenSpeech(providers []SpeechProvider, credentials map[string]config.LLMProviderConfig, defaultVoice string) (voice.TTSProvider, error) {
	var chain []voice.TTSProvider
	for _, route := range providers {
		var provider voice.TTSProvider
		if route.Adapter == "edge-tts" {
			var opts []voice.EdgeTTSOption
			if defaultVoice != "" {
				opts = append(opts, voice.EdgeTTSWithVoice(defaultVoice))
			}
			provider = voice.NewEdgeTTS(opts...)
		} else {
			credential, ok := credentials[route.ID]
			if !ok || credential.APIKey == "" {
				return nil, fmt.Errorf("original speech credentials are unavailable")
			}
			switch route.Adapter {
			case "openai-tts":
				var opts []voice.TTSOption
				if route.Endpoint != "" {
					opts = append(opts, voice.TTSWithBaseURL(route.Endpoint))
				}
				provider = voice.NewOpenAITTS(credential.APIKey, "", opts...)
			case "minimax":
				var opts []voice.MiniMaxTTSOption
				if route.Endpoint != "" {
					opts = append(opts, voice.WithMiniMaxBaseURL(route.Endpoint))
				}
				provider = voice.NewMiniMaxTTS(credential.APIKey, route.Region, opts...)
			default:
				return nil, fmt.Errorf("original speech protocol is unavailable")
			}
		}
		chain = append(chain, namedSpeechProvider{TTSProvider: provider, id: route.ID})
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("speech is not configured")
	}
	if len(chain) == 1 {
		return chain[0], nil
	}
	return voice.NewChainedTTS(chain...), nil
}

// ConfiguredVoice 为原语音API和会话工具提供同一配置装配。
func ConfiguredVoice(cfg *config.Config) *voice.Service {
	if !cfg.Voice.Enabled {
		return nil
	}
	var stt voice.STTProvider
	if _, pc, ok := configuredProvider(cfg, cfg.Voice.STT.Provider); ok && pc.APIKey != "" && (pc.Enabled == nil || *pc.Enabled) {
		var opts []voice.STTOption
		if pc.BaseURL != "" {
			opts = append(opts, voice.STTWithBaseURL(pc.BaseURL))
		}
		stt = voice.NewOpenAISTT(pc.APIKey, cfg.Voice.STT.Model, opts...)
	}
	credentials := make(map[string]config.LLMProviderConfig)
	for key, pc := range cfg.LLM.Providers {
		credentials[config.EffectiveProviderInstanceID(key, pc)] = pc
	}
	tts, _ := frozenSpeech(speechProviders(cfg), credentials, cfg.Voice.TTS.Voice)
	return voice.NewService(stt, tts)
}

func (s *Service) synthesize(ctx context.Context, op *Operation, snapshot *Snapshot) (*SpeechOutcome, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	state := &speechAttemptState{cancel: cancel}
	ctx = context.WithValue(ctx, speechAttemptContextKey{}, state)
	opts := op.Request.Speech.Options
	if opts.Voice == "" {
		opts.Voice = op.Route.SpeechVoice
	}
	if opts.Speed == 0 {
		opts.Speed = op.Route.SpeechSpeed
	}
	if len(op.Route.SpeechProviders) > 0 {
		provider, err := frozenSpeech(op.Route.SpeechProviders, snapshot.providers, op.Route.SpeechVoice)
		if err != nil {
			return nil, err
		}
		result, err := provider.Synthesize(ctx, op.Request.Speech.Text, opts)
		return &SpeechOutcome{Result: result, ProviderID: state.winner, Attempts: append([]SpeechAttempt(nil), state.attempts...)}, err
	}
	if snapshot.Voice == nil || snapshot.Revision != op.Route.Revision {
		return nil, fmt.Errorf("original speech service is unavailable")
	}
	result, err := snapshot.Voice.Synthesize(ctx, op.Request.Speech.Text, opts)
	return &SpeechOutcome{Result: result, ProviderID: snapshot.Voice.TTSName()}, err
}

func speechResultJSON(outcome *SpeechOutcome) json.RawMessage {
	encoded, _ := json.Marshal(outcome.Result)
	var result map[string]any
	_ = json.Unmarshal(encoded, &result)
	if result == nil {
		result = make(map[string]any)
	}
	result["provider_instance_id"] = outcome.ProviderID
	result["attempts"] = outcome.Attempts
	encoded, _ = json.Marshal(result)
	return encoded
}
