// Package agentcatalog 保存通用智能体模板的唯一机读主源。
package agentcatalog

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

//go:embed templates.json
var raw []byte

type Locale struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	SystemPrompt string   `json:"system_prompt"`
	Expertise    []string `json:"expertise"`
}
type Template struct {
	Key     string            `json:"template_key"`
	Version string            `json:"template_version"`
	Slug    string            `json:"slug"`
	IconKey string            `json:"icon_key"`
	Locales map[string]Locale `json:"locales"`
}
type Catalog struct {
	SchemaVersion int        `json:"schema_version"`
	Version       string     `json:"catalog_version"`
	Templates     []Template `json:"templates"`
}

type Snapshot struct {
	Source         string   `json:"catalog_source"`
	CatalogVersion string   `json:"catalog_version"`
	Key            string   `json:"template_key"`
	Version        string   `json:"template_version"`
	Digest         string   `json:"template_digest"`
	Locale         string   `json:"locale"`
	Slug           string   `json:"slug"`
	IconKey        string   `json:"icon_key"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	SystemPrompt   string   `json:"system_prompt"`
	Expertise      []string `json:"expertise"`
}

func Read() (Catalog, error) {
	var catalog Catalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return catalog, err
	}
	if catalog.SchemaVersion != 1 || catalog.Version == "" {
		return catalog, fmt.Errorf("unsupported agent template catalog")
	}
	seen := map[string]bool{}
	for _, template := range catalog.Templates {
		if template.Key == "" || template.Version == "" || template.Slug == "" || seen[template.Key] {
			return catalog, fmt.Errorf("invalid agent template identity")
		}
		seen[template.Key] = true
		for _, language := range []string{"zh-CN", "en", "ug-CN"} {
			value, ok := template.Locales[language]
			if !ok || value.Name == "" || value.SystemPrompt == "" {
				return catalog, fmt.Errorf("agent template locale is incomplete")
			}
		}
	}
	return catalog, nil
}

func List(locale string) ([]Snapshot, error) {
	if locale == "" || locale == "zh" {
		locale = "zh-CN"
	}
	catalog, err := Read()
	if err != nil {
		return nil, err
	}
	result := make([]Snapshot, 0, len(catalog.Templates))
	for _, template := range catalog.Templates {
		value, ok := template.Locales[locale]
		if !ok {
			return nil, fmt.Errorf("agent template locale is unavailable")
		}
		snapshot := Snapshot{Source: "backend", CatalogVersion: catalog.Version, Key: template.Key, Version: template.Version, Locale: locale, Slug: template.Slug, IconKey: template.IconKey, Name: value.Name, Description: value.Description, SystemPrompt: value.SystemPrompt, Expertise: append([]string(nil), value.Expertise...)}
		root := map[string]any{"catalog_version": snapshot.CatalogVersion, "template_key": snapshot.Key, "template_version": snapshot.Version, "locale": snapshot.Locale, "slug": snapshot.Slug, "icon_key": snapshot.IconKey, "name": snapshot.Name, "description": snapshot.Description, "system_prompt": snapshot.SystemPrompt, "expertise": snapshot.Expertise}
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(root); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
		snapshot.Digest = hex.EncodeToString(sum[:])
		result = append(result, snapshot)
	}
	return result, nil
}

func Get(key, locale string) (Snapshot, error) {
	list, err := List(locale)
	if err != nil {
		return Snapshot{}, err
	}
	for _, snapshot := range list {
		if snapshot.Key == key {
			return snapshot, nil
		}
	}
	return Snapshot{}, fmt.Errorf("agent template is unavailable")
}

// ValidateMetadata 核对后端来源快照；不以同 key 新正文代替已选版本，也不覆盖用户表单编辑。
func ValidateMetadata(metadata map[string]string) error {
	if metadata["catalog_source"] != "backend" {
		return nil
	}
	snapshot, err := Get(metadata["template_key"], metadata["template_locale"])
	if err != nil {
		return err
	}
	if snapshot.CatalogVersion != metadata["catalog_version"] || snapshot.Version != metadata["template_version"] || snapshot.Digest != metadata["template_digest"] {
		return fmt.Errorf("selected agent template snapshot is unavailable")
	}
	if encoded := strings.TrimSpace(metadata["template_snapshot"]); encoded != "" {
		var previous Snapshot
		if json.Unmarshal([]byte(encoded), &previous) != nil || !reflect.DeepEqual(previous, snapshot) {
			return fmt.Errorf("agent template snapshot does not match its catalog identity")
		}
	}
	return nil
}

// ValidateMetadataUpdate 保留已创建实例的原来源，只对明确替换的模板快照核对当前目录。
func ValidateMetadataUpdate(previous, next map[string]string) error {
	unchanged := true
	for _, key := range []string{"catalog_source", "catalog_version", "template_key", "template_version", "template_digest", "template_locale", "template_snapshot"} {
		before, beforePresent := previous[key]
		after, afterPresent := next[key]
		if before != after || beforePresent != afterPresent {
			unchanged = false
			break
		}
	}
	if unchanged {
		return nil
	}
	return ValidateMetadata(next)
}
