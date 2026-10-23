package main

import (
	"github.com/hexagon-codes/ai-core/media/voice"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
)

// configuredVoiceService 复用媒体用例的唯一语音配置装配。
func configuredVoiceService(cfg *config.Config) *voice.Service { return mediaflow.ConfiguredVoice(cfg) }
