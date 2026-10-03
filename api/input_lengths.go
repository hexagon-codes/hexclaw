package api

import (
	"encoding/json"
	"strings"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/instances"
	"github.com/hexagon-codes/hexclaw/internal/inputlimits"
	"github.com/hexagon-codes/hexclaw/library"
	"github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func validateAgentInputLengths(next, previous router.AgentConfig) error {
	if err := inputlimits.Text("display_name", next.DisplayName, previous.DisplayName, inputlimits.DisplayName); err != nil {
		return err
	}
	if err := inputlimits.Text("description", next.Description, previous.Description, inputlimits.Description); err != nil {
		return err
	}
	return inputlimits.Text("metadata.k12.child_name", next.Metadata[k12.MetaKeyChildName], previous.Metadata[k12.MetaKeyChildName], inputlimits.ChildName)
}

func inputString(value any) string {
	text, _ := value.(string)
	return text
}

func inputStringList(value any) []string {
	switch list := value.(type) {
	case string:
		return strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ';' || r == '\n' })
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func validateInstanceInputLengths(next, previous *instances.Instance) error {
	if previous == nil {
		previous = &instances.Instance{}
	}
	if err := inputlimits.Text("name", next.Name, previous.Name, inputlimits.DisplayName); err != nil {
		return err
	}
	var values, oldValues map[string]any
	if len(next.Config) > 0 {
		if err := json.Unmarshal(next.Config, &values); err != nil {
			return err
		}
	}
	if len(previous.Config) > 0 {
		if err := json.Unmarshal(previous.Config, &oldValues); err != nil {
			return err
		}
	}
	// 平台契约明确的凭据字段；消息、模板及扩展 JSON 不套用短字段限制。
	fields := map[string][]string{
		"telegram": {"token"}, "discord": {"token"},
		"slack":    {"token", "signing_secret"},
		"feishu":   {"app_secret", "verification_token"},
		"dingtalk": {"app_key", "app_secret"},
		"wechat":   {"app_secret", "token", "aes_key"},
		"wecom":    {"secret", "token", "aes_key"},
		"whatsapp": {"token", "verify_token", "app_secret"},
		"line":     {"channel_secret", "channel_token"},
		"matrix":   {"access_token"}, "email": {"password"},
	}
	for _, field := range fields[next.Provider] {
		if err := inputlimits.Bytes("config."+field, inputString(values[field]), inputString(oldValues[field]), inputlimits.SecretBytes); err != nil {
			return err
		}
	}
	if next.Provider == "matrix" {
		return inputlimits.Bytes("config.homeserver_url", inputString(values["homeserver_url"]), inputString(oldValues["homeserver_url"]), inputlimits.URLBytes)
	}
	if next.Provider == "email" {
		for _, field := range []string{"smtp_host", "imap_host"} {
			if err := inputlimits.Bytes("config."+field, inputString(values[field]), inputString(oldValues[field]), inputlimits.URLBytes); err != nil {
				return err
			}
		}
		for _, section := range []string{"smtp", "imap"} {
			nextSection, _ := values[section].(map[string]any)
			oldSection, _ := oldValues[section].(map[string]any)
			if err := inputlimits.Bytes("config."+section+".host", inputString(nextSection["host"]), inputString(oldSection["host"]), inputlimits.URLBytes); err != nil {
				return err
			}
			if err := inputlimits.Bytes("config."+section+".password", inputString(nextSection["password"]), inputString(oldSection["password"]), inputlimits.SecretBytes); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCronInputLengths(name, schedule, previousName, previousSchedule string) error {
	if err := inputlimits.Text("name", name, previousName, inputlimits.DisplayName); err != nil {
		return err
	}
	return inputlimits.Text("schedule", schedule, previousSchedule, inputlimits.TimeExpression)
}

func validateWorkflowInputLengths(next WorkflowData, previous *WorkflowData) error {
	if previous == nil {
		previous = &WorkflowData{}
	}
	if err := inputlimits.Text("name", next.Name, previous.Name, inputlimits.DisplayName); err != nil {
		return err
	}
	if err := inputlimits.Text("description", next.Description, previous.Description, inputlimits.Description); err != nil {
		return err
	}
	oldNodes := make(map[string]map[string]any, len(previous.Nodes))
	for _, raw := range previous.Nodes {
		if node, ok := raw.(map[string]any); ok {
			oldNodes[inputString(node["id"])] = node
		}
	}
	for _, raw := range next.Nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		old := oldNodes[inputString(node["id"])]
		if err := inputlimits.Text("node.label", inputString(node["label"]), inputString(old["label"]), inputlimits.DisplayName); err != nil {
			return err
		}
		data, _ := node["data"].(map[string]any)
		oldData, _ := old["data"].(map[string]any)
		if len(data) == 0 {
			data, _ = node["config"].(map[string]any)
		}
		if len(oldData) == 0 {
			oldData, _ = old["config"].(map[string]any)
		}
		for _, field := range []string{"role", "tool", "model"} {
			if err := inputlimits.Text("node."+field, inputString(data[field]), inputString(oldData[field]), inputlimits.TechnicalID); err != nil {
				return err
			}
		}
		if err := validateIdentifierList("node.roles", inputStringList(data["roles"]), inputStringList(oldData["roles"])); err != nil {
			return err
		}
		if inputString(node["type"]) == "tool" {
			if err := inputlimits.Text("node.name", inputString(data["name"]), inputString(oldData["name"]), inputlimits.TechnicalID); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePromptInputLengths(next, previous library.Prompt) error {
	if err := inputlimits.Text("title", next.Title, previous.Title, inputlimits.Title); err != nil {
		return err
	}
	if err := inputlimits.Text("category", next.Category, previous.Category, inputlimits.TechnicalID); err != nil {
		return err
	}
	if err := inputlimits.Text("model", next.Model, previous.Model, inputlimits.TechnicalID); err != nil {
		return err
	}
	return validateIdentifierList("tool_scope", strings.Split(next.ToolScope, ","), strings.Split(previous.ToolScope, ","))
}

func validateIdentifierList(field string, next, previous []string) error {
	oldValues := make(map[string]bool, len(previous))
	for _, value := range previous {
		oldValues[value] = true
	}
	for _, value := range next {
		if !oldValues[value] {
			if err := inputlimits.Text(field, value, "", inputlimits.TechnicalID); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateProviderInputLengths(next, previous config.LLMProviderConfig) error {
	if err := inputlimits.Text("provider.display_name", next.DisplayName, previous.DisplayName, inputlimits.DisplayName); err != nil {
		return err
	}
	if err := inputlimits.Bytes("provider.base_url", next.BaseURL, previous.BaseURL, inputlimits.URLBytes); err != nil {
		return err
	}
	if err := inputlimits.Bytes("provider.api_key", next.APIKey, previous.APIKey, inputlimits.SecretBytes); err != nil {
		return err
	}
	oldSpecs := make(map[string]config.LLMProviderModelSpec, len(previous.ModelSpecs))
	for _, spec := range previous.ModelSpecs {
		oldSpecs[spec.ID] = spec
	}
	for _, spec := range next.ModelSpecs {
		oldSpec := oldSpecs[spec.ID]
		// 目录返回的模型标识不是用户填写字段；只校验自定义模型。
		if spec.IsCustom {
			if err := inputlimits.Text("model.id", spec.ID, oldSpec.ID, inputlimits.TechnicalID); err != nil {
				return err
			}
		}
		if spec.DisplayName != spec.ID {
			if err := inputlimits.Text("model.display_name", spec.DisplayName, oldSpec.DisplayName, inputlimits.Title); err != nil {
				return err
			}
		}
	}
	return nil
}
