package engine

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

//go:embed product_facts.json
var productFactsSource []byte

var productFactsOnce sync.Once
var productFactsPublicJSON string
var productFactsLoadError error

// PublicProductFactsJSON 返回与客户端公开副本同源的产品事实，不包含当前用户配置。
func PublicProductFactsJSON() (string, error) {
	productFactsOnce.Do(func() {
		productFactsPublicJSON, productFactsLoadError = buildPublicProductFacts(productFactsSource)
	})
	return productFactsPublicJSON, productFactsLoadError
}

func buildPublicProductFacts(source []byte) (string, error) {
	var document map[string]any
	if err := json.Unmarshal(source, &document); err != nil {
		return "", fmt.Errorf("decode product facts: %w", err)
	}
	public, ok := publicProductFactValue(document)
	if !ok {
		return "", fmt.Errorf("product facts have no public projection")
	}
	projection, ok := public.(map[string]any)
	if !ok || projection["product_id"] != "hexclaw" {
		return "", fmt.Errorf("invalid product facts identity")
	}
	payload, err := json.Marshal(projection)
	if err != nil {
		return "", fmt.Errorf("encode public product facts: %w", err)
	}
	digest := sha256.Sum256(payload)
	projection["facts_digest"] = "sha256:" + hex.EncodeToString(digest[:])
	encoded, err := json.Marshal(projection)
	if err != nil {
		return "", fmt.Errorf("encode product facts envelope: %w", err)
	}
	return string(encoded), nil
}

// publicProductFactValue 在每层去掉内部项，公开摘要只覆盖消费者实际获得的内容。
func publicProductFactValue(value any) (any, bool) {
	switch item := value.(type) {
	case map[string]any:
		if item["visibility"] == "internal" {
			return nil, false
		}
		out := make(map[string]any, len(item))
		for key, child := range item {
			if strings.HasPrefix(key, "_") || key == "visibility" || key == "facts_digest" || key == "source_digest" {
				continue
			}
			if public, ok := publicProductFactValue(child); ok {
				out[key] = public
			}
		}
		return out, true
	case []any:
		out := make([]any, 0, len(item))
		for _, child := range item {
			if public, ok := publicProductFactValue(child); ok {
				out = append(out, public)
			}
		}
		return out, true
	default:
		return value, true
	}
}

func productFactsContext() string {
	facts, err := PublicProductFactsJSON()
	if err != nil {
		return "\n[Host product facts]\nProduct facts are currently unavailable; do not invent missing facts.\n"
	}
	return "\n[Host product facts]\n" +
		"These are versioned facts about the HexClaw host, not a replacement for this agent's name or persona. " +
		"Use known links directly without web search. Unknown official account details must remain unknown. " +
		"Configured endpoints do not prove a direct connection or a complete forwarding chain. " +
		"Product capabilities do not imply that all tools are enabled for this request.\n" + facts + "\n"
}
