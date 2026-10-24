package mediaflow_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/media/voice"
	"github.com/hexagon-codes/ai-core/media/voicechat"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
)

func TestDisabledNativeMediaNameRemainsAnExplicitConstraint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("disabled named model must not switch to another configured model")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	cfg := mediaConfig(upstream.URL)
	enabled := false
	cfg.LLM.Providers["native-account"] = config.LLMProviderConfig{Enabled: &enabled, APIKey: "fixture-key", Compatible: "openai", BaseURL: "https://open.bigmodel.cn/api/paas/v4", ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: []config.LLMProviderModelSpec{{ID: "glm-4", Capabilities: []string{config.LLMModelCapabilityText}}}}
	svc, router, _, _ := newMedia(t, cfg)
	req := request("disabled-native")
	req.Selection = router.UserSelection("请使用 CogView-4 生成图片", "image")
	if req.Selection.Mode != "pinned" || len(req.Selection.Models) != 1 || req.Selection.Models[0] != "cogview-4" {
		t.Fatalf("disabled catalog lost user selection: %+v", req.Selection)
	}
	if _, err := svc.Begin(t.Context(), req); !errors.Is(err, mediaflow.ErrUnavailable) {
		t.Fatalf("disabled selection was replaced: %v", err)
	}
}

func TestAudioOnlyCapabilityPersistsPlayableSpeech(t *testing.T) {
	data, err := os.ReadFile("testdata/short-audio.wav")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Voice.Enabled = true
	cfg.Voice.TTS.Provider = "fixture-tts"
	cfg.Skill.Builtin.MediaGen = true
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{}
	svc, router, _, _ := newMedia(t, cfg)
	speech := &fixtureSpeech{audio: data}
	router.Reload(cfg, voice.NewService(nil, speech))
	if len(router.Current().Routes) != 1 || router.Current().Routes[0].Kind != "audio" || router.Current().Images.HasProvider() || router.Current().Videos.HasProvider() {
		t.Fatal("audio capability depends on image or video")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	op, err := svc.Begin(ctx, mediaflow.Request{Origin: mediaflow.Origin{Owner: "owner", Kind: "direct_api", RequestID: "one-speech"}, Kind: "audio", Action: "synthesize", Speech: &mediaflow.SpeechRequest{Text: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	op, err = svc.Wait(ctx, "owner", op.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if op.SaveState != "ready" || len(op.Assets) != 1 || op.Assets[0].MIME != "audio/wav" || speech.calls.Load() != 1 {
		t.Fatalf("audio output failed: %+v calls=%d", op, speech.calls.Load())
	}
}

func TestNativeMediaDirectoryKeepsExistingDefaultModels(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"native-account": {APIKey: "fixture-key", Compatible: "openai", BaseURL: "https://open.bigmodel.cn/api/paas/v4", ModelSpecsMode: config.LLMModelSpecsModeLegacy, Model: "glm-4"}}
	router := mediaflow.NewRouter()
	router.Reload(cfg, nil)
	imageRoute, err := router.Resolve("image", "generate", "native-account", "", "")
	if err != nil || imageRoute.Model != "cogview-4" {
		t.Fatalf("existing image default lost: %+v %v", imageRoute, err)
	}
	videoRoute, err := router.Resolve("video", "generate", "native-account", "", "")
	if err != nil || videoRoute.Model != "cogvideox-2" {
		t.Fatalf("existing video default lost: %+v %v", videoRoute, err)
	}
	knownChatModels := voicechat.NewOpenAIVoiceChat("", "").SupportedModels()
	for _, tc := range []struct {
		name, endpoint string
		explicitAudio  bool
		native         bool
	}{
		{name: "official text configuration", endpoint: "https://api.openai.com/v1", native: true},
		{name: "official partial audio configuration", endpoint: "https://api.openai.com/v1/", explicitAudio: true, native: true},
		{name: "custom text configuration", endpoint: "https://api.openai.com.gateway.invalid/v1"},
		{name: "different official path", endpoint: "https://api.openai.com/custom/v1"},
		{name: "custom explicit audio", endpoint: "https://custom.invalid/v1", explicitAudio: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			specs := []config.LLMProviderModelSpec{{ID: "gpt-4o", Capabilities: []string{config.LLMModelCapabilityText}}}
			configuredAudio := knownChatModels[len(knownChatModels)-1]
			if tc.explicitAudio {
				specs = append(specs, config.LLMProviderModelSpec{ID: configuredAudio, Capabilities: []string{config.LLMModelCapabilityAudio}})
			}
			// 完整启用目录保留原生别名与默认；自定义端点仍只使用明确声明。
			if tc.native {
				for _, model := range knownChatModels {
					if tc.explicitAudio && model == configuredAudio {
						continue
					}
					specs = append(specs, config.LLMProviderModelSpec{ID: model, Capabilities: []string{config.LLMModelCapabilityAudio}})
				}
			}
			cfg.LLM.Providers = map[string]config.LLMProviderConfig{"audio-account": {APIKey: "fixture-key", Compatible: "openai", BaseURL: tc.endpoint, ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: specs}}
			router := mediaflow.NewRouter()
			router.Reload(cfg, nil)
			if tc.native {
				defaultRoute, err := router.Resolve("audio", "chat", "openai-gpt4o-audio", "", "")
				if err != nil || defaultRoute.ProviderKey != "audio-account" || defaultRoute.Model != knownChatModels[0] {
					t.Fatalf("existing voicechat alias or default lost: %+v %v", defaultRoute, err)
				}
				for _, model := range knownChatModels {
					route, err := router.Resolve("audio", "chat", "openai-gpt4o-audio", model, "")
					if err != nil || route.ProviderKey != "audio-account" || route.Model != model || route.Protocol != "openai-voicechat" {
						t.Fatalf("existing voicechat model lost: model=%s route=%+v err=%v", model, route, err)
					}
				}
				return
			}
			if _, err := router.Resolve("audio", "chat", "openai-gpt4o-audio", "", ""); err == nil {
				t.Fatal("custom endpoint acquired the official provider alias")
			}
			if tc.explicitAudio {
				if _, err := router.Resolve("audio", "chat", "audio-account", configuredAudio, ""); err != nil {
					t.Fatalf("explicit custom audio capability lost: %v", err)
				}
				if _, err := router.Resolve("audio", "chat", "audio-account", knownChatModels[0], ""); err == nil {
					t.Fatal("custom endpoint acquired undeclared audio models")
				}
			} else if router.Current().Chat.HasProvider() {
				t.Fatal("custom endpoint acquired inferred audio capability")
			}
		})
	}
}
