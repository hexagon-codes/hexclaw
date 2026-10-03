package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// OllamaTargetConfig 保存管理目标及明确关联的推理实例，不包含运行探测状态。
type OllamaTargetConfig struct {
	Mode                          string   `yaml:"mode,omitempty" json:"mode"`
	CustomBaseURL                 string   `yaml:"custom_base_url,omitempty" json:"custom_base_url"`
	TargetRevision                uint64   `yaml:"target_revision,omitempty" json:"target_revision"`
	AssociatedProviderInstanceIDs []string `yaml:"associated_provider_instance_ids,omitempty" json:"associated_provider_instance_ids"`
}

func NormalizeOllamaServiceRoot(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("Invalid Ollama service URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return strings.TrimRight(u.String(), "/"), nil
}

func (c OllamaTargetConfig) Resolve(defaultBase string) (string, error) {
	if c.Mode == "custom" {
		return NormalizeOllamaServiceRoot(c.CustomBaseURL)
	}
	if c.Mode != "" && c.Mode != "default" {
		return "", fmt.Errorf("Invalid Ollama target mode")
	}
	if defaultBase == "" {
		defaultBase = "http://127.0.0.1:11434"
	}
	return NormalizeOllamaServiceRoot(defaultBase)
}

func OllamaTargetHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
func (c OllamaTargetConfig) Digest(base string) string {
	ids := append([]string{}, c.AssociatedProviderInstanceIDs...)
	sort.Strings(ids)
	mode := c.Mode
	if mode == "" {
		mode = "default"
	}
	body, _ := json.Marshal(struct {
		Mode      string
		Base      string
		Providers []string
	}{mode, base, ids})
	return OllamaTargetHash(string(body))
}

// 仅联合提交绑定的精确目标使用 Ollama 传输，普通 Provider 不继承此授权。
func (p LLMProviderConfig) HasOllamaTarget() bool {
	root, err := NormalizeOllamaServiceRoot(p.OllamaTargetBaseURL)
	return err == nil && (strings.TrimRight(p.BaseURL, "/") == root || strings.TrimRight(p.BaseURL, "/") == root+"/v1")
}
