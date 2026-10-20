package engine

import (
	"encoding/json"
	"strings"

	"github.com/hexagon-codes/hexclaw/config"
)

const providerPresentationSnapshotKey = "_provider_presentation_snapshot"

type providerPresentation struct {
	InstanceID  string `json:"instance_id"`
	DisplayName string `json:"display_name"`
}

// freezeProviderPresentation 只冻结请求选择时的身份及显示名，不携带连接或凭据。
func (e *ReActEngine) freezeProviderPresentation(metadata map[string]string) {
	snapshot := make(map[string]providerPresentation)
	for key, provider := range e.ActiveLLMConfig().Providers {
		name := strings.TrimSpace(provider.DisplayName)
		if name == "" {
			name = key
		}
		snapshot[key] = providerPresentation{InstanceID: config.EffectiveProviderInstanceID(key, provider), DisplayName: name}
	}
	encoded, err := json.Marshal(snapshot)
	if err == nil {
		metadata[providerPresentationSnapshotKey] = string(encoded)
	}
}

func frozenProviderPresentation(metadata map[string]string, provider string) (providerPresentation, bool) {
	var snapshot map[string]providerPresentation
	if json.Unmarshal([]byte(metadata[providerPresentationSnapshotKey]), &snapshot) != nil {
		return providerPresentation{}, false
	}
	value, ok := snapshot[provider]
	return value, ok
}
