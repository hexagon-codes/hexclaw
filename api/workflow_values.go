package api

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/messagecontent"
)

// workflowSelectedValue 先选来源，再投影字段或已有附件引用；不让任务模板改变来源选择。
func (e *workflowExecutor) workflowSelectedValue(state hexagon.MapState, node *workflowNode) (string, error) {
	var selectedArtifacts []messagecontent.AttachmentRef
	value := e.upstreamOutputs(state, node.ID)
	if node.Type == "input" {
		value = renderTemplate(firstNonEmpty(stringValue(node.Data["value"]), "{{input}}"), state)
	}
	mapped := stringValue(node.Data["input"])
	if mapped != "" {
		value = renderTemplate(mapped, state)
	}
	selection := resourceMap(node.Data["input_selection"])
	switch stringValue(selection["kind"]) {
	case "", "text":
	case "structured":
		var decoded any
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			return "", fmt.Errorf("Selected workflow input is not valid JSON: %w", err)
		}
		path := strings.TrimSpace(stringValue(selection["field"]))
		path = strings.TrimPrefix(strings.TrimPrefix(path, "$"), ".")
		if path != "" {
			for _, part := range strings.Split(path, ".") {
				switch current := decoded.(type) {
				case map[string]any:
					var found bool
					decoded, found = current[part]
					if !found {
						return "", fmt.Errorf("Workflow input field is missing: %s", path)
					}
				case []any:
					index, err := strconv.Atoi(part)
					if err != nil || index < 0 || index >= len(current) {
						return "", fmt.Errorf("Workflow input index is invalid: %s", path)
					}
					decoded = current[index]
				default:
					return "", fmt.Errorf("Workflow input field cannot be selected: %s", path)
				}
			}
		}
		if text, ok := decoded.(string); ok {
			value = text
		} else {
			if node.Type == "output" && node.Data["output_object"] != nil {
				return "", fmt.Errorf("Workflow output object requires a string value")
			}
			encoded, _ := json.Marshal(decoded)
			value = string(encoded)
		}
	case "artifact":
		refs := e.workflowSourceArtifacts(node)
		if len(refs) == 0 {
			if json.Unmarshal([]byte(value), &refs) != nil {
				var content struct {
					Attachments []messagecontent.AttachmentRef `json:"attachments"`
				}
				if json.Unmarshal([]byte(value), &content) == nil {
					refs = content.Attachments
				}
			}
		}
		if len(refs) == 0 {
			return "", fmt.Errorf("Selected workflow input contains no artifact references")
		}
		seen := map[string]bool{}
		unique := []messagecontent.AttachmentRef{}
		for _, ref := range refs {
			if ref.AssetID == "" || ref.MIME == "" || ref.Digest == "" {
				return "", fmt.Errorf("Workflow artifact reference is incomplete")
			}
			if !seen[ref.AssetID] {
				unique = append(unique, ref)
				seen[ref.AssetID] = true
			}
		}
		selectedArtifacts = unique
		encoded, _ := json.Marshal(unique)
		value = string(encoded)
	default:
		return "", fmt.Errorf("Unsupported workflow input selection kind")
	}
	if node.Type == "output" {
		if raw, exists := node.Data["output_object"]; exists {
			fields := resourceMap(raw)
			if len(fields) != 1 {
				return "", fmt.Errorf("Workflow output object must contain one named string field")
			}
			result := map[string]string{}
			for field, expression := range fields {
				if strings.TrimSpace(field) == "" || stringValue(expression) != "{{value}}" {
					return "", fmt.Errorf("Workflow output object must map a named field to the selected value")
				}
				result[field] = value
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				return "", fmt.Errorf("Workflow output object could not be encoded: %w", err)
			}
			value = string(encoded)
		} else if mapping := stringValue(node.Data["output_template"]); mapping != "" {
			value = strings.ReplaceAll(mapping, "{{value}}", value)
		}
	}
	if len(selectedArtifacts) > 0 && (node.Type == "input" || node.Type == "output") {
		content, err := messagecontent.New(messagecontent.ProducerWorkflow, firstNonEmpty(e.req.Metadata["locale"], "und"), value, selectedArtifacts)
		if err != nil {
			return "", err
		}
		e.mu.Lock()
		e.ensureNodeRun(node).MessageContent = &content
		e.mu.Unlock()
	}
	return value, nil
}

// workflowSourceArtifacts 只读取选定来源的真实附件回执，不按相同文本猜测附件归属。
func (e *workflowExecutor) workflowSourceArtifacts(node *workflowNode) []messagecontent.AttachmentRef {
	var refs []messagecontent.AttachmentRef
	source := ""
	mapped := stringValue(node.Data["input"])
	if strings.HasPrefix(mapped, "{{node.") && strings.HasSuffix(mapped, "}}") {
		source = strings.TrimSuffix(strings.TrimPrefix(mapped, "{{node."), "}}")
	} else if mapped == "" || mapped == "{{previous}}" {
		if parents := e.incoming[node.ID]; len(parents) == 1 {
			source = parents[0]
		}
	}
	if source != "" {
		e.mu.Lock()
		if result := e.nodeRuns[source]; result != nil {
			if result.MessageContent != nil {
				refs = append(refs, result.MessageContent.Attachments...)
			}
			for _, receipt := range result.ResourceReceipts {
				if receipt != nil {
					refs = append(refs, receipt.Artifacts...)
				}
			}
		}
		e.mu.Unlock()
	}

	unique := []messagecontent.AttachmentRef{}
	seen := map[string]bool{}
	for _, ref := range refs {
		if !seen[ref.AssetID] {
			unique = append(unique, ref)
			seen[ref.AssetID] = true
		}
	}
	return unique
}
