package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/knowledge"
	"github.com/hexagon-codes/hexclaw/library"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/skill"
)

var errWorkflowConfiguration = errors.New("Invalid workflow configuration")
var errWorkflowResourcePreparation = errors.New("Workflow resources could not be prepared")

func workflowConfigError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errWorkflowConfiguration, fmt.Sprintf(format, args...))
}

func writeWorkflowLaunchError(w http.ResponseWriter, err error, fallback string) {
	if errors.Is(err, errWorkflowConfiguration) {
		writeAPIError(w, 400, "INVALID_WORKFLOW_CONFIG", err.Error())
		return
	}
	if errors.Is(err, errWorkflowResourcePreparation) {
		writeAPIError(w, 503, "WORKFLOW_RESOURCES_UNAVAILABLE", err.Error())
		return
	}
	writeAPIError(w, 500, "WORKFLOW_PERSIST_FAILED", fallback)
}

type workflowResourcePolicy struct {
	Mode  string   `json:"mode"`
	Value []string `json:"value"`
}

type workflowStepSettings struct {
	Resources map[string]workflowResourcePolicy `json:"resources"`
	Memory    struct {
		Retrieval struct {
			Mode  string `json:"mode"`
			Query string `json:"query"`
		} `json:"retrieval"`
		Writeback struct {
			Mode string `json:"mode"`
		} `json:"writeback"`
	} `json:"memory"`
	Output struct {
		Kind          string   `json:"kind"`
		Instructions  string   `json:"instructions"`
		Fields        string   `json:"fields"`
		ArtifactKinds []string `json:"artifactKinds"`
	} `json:"output"`
}

type workflowResolvedPrompt struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Digest    string    `json:"digest"`
	UpdatedAt time.Time `json:"updated_at"`
}

type workflowResolvedResources struct {
	Options  engine.WorkflowStepOptions `json:"options"`
	Prompts  []workflowResolvedPrompt   `json:"prompts,omitempty"`
	Settings workflowStepSettings       `json:"settings"`
	Digest   string                     `json:"digest"`
}

// WorkflowStepReceipt 把请求资源和真实采用结果分开，未执行时不制造命中或产物。
type WorkflowStepReceipt struct {
	Effective           *workflowResolvedResources     `json:"effective_resources,omitempty"`
	KnowledgeHits       []adapter.KnowledgeHit         `json:"knowledge_hits,omitempty"`
	MemoryHits          []adapter.MemoryHit            `json:"memory_hits,omitempty"`
	ToolCalls           []adapter.ToolCall             `json:"tool_calls,omitempty"`
	Artifacts           []messagecontent.AttachmentRef `json:"artifacts,omitempty"`
	SkillReceipts       json.RawMessage                `json:"skill_receipts,omitempty"`
	OutputError         string                         `json:"output_error,omitempty"`
	RetryRequiresReview bool                           `json:"retry_requires_review,omitempty"`
}

func resourceMode(value string) (string, error) {
	if value == "" {
		return "inherit", nil
	}
	switch value {
	case "inherit", "override", "none":
		return value, nil
	}
	return "", workflowConfigError("Invalid workflow resource mode: %s", value)
}

func resourceJSON(value any, target any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func resourceMap(value any) map[string]any {
	var result map[string]any
	_ = resourceJSON(value, &result)
	if result == nil {
		result = map[string]any{}
	}
	return result
}

func workflowNodeData(node map[string]any) map[string]any {
	data, _ := node["data"].(map[string]any)
	if len(data) == 0 {
		data, _ = node["config"].(map[string]any)
	}
	if data == nil {
		data = map[string]any{}
	}
	return data
}

func (s *Server) resolveWorkflowResources(ctx context.Context, data map[string]any, role string) (*workflowResolvedResources, error) {
	resolved := &workflowResolvedResources{}
	if err := resourceJSON(data, &resolved.Settings); err != nil {
		return nil, workflowConfigError("Invalid workflow resources: %v", err)
	}
	if s.agentRouter != nil {
		name := firstNonEmpty(role, s.agentRouter.DefaultAgent())
		if cfg, ok := s.agentRouter.GetAgent(name); ok && cfg != nil {
			if err := resourceJSON(cfg, &resolved.Options.Agent); err != nil {
				return nil, err
			}
		}
	}
	policy := func(key string) (workflowResourcePolicy, error) {
		p := resolved.Settings.Resources[key]
		mode, err := resourceMode(p.Mode)
		p.Mode = mode
		if p.Mode == "inherit" && resolved.Options.Agent != nil {
			if key == "skills" {
				p.Value = append([]string(nil), resolved.Options.Agent.Skills...)
			} else {
				_ = json.Unmarshal([]byte(resolved.Options.Agent.Metadata["workflow_"+key]), &p.Value)
			}
		}
		if p.Mode == "none" {
			p.Value = nil
		}
		return p, err
	}
	skills, err := policy("skills")
	if err != nil {
		return nil, err
	}
	if freezer, ok := s.engine.(interface {
		FreezeWorkflowSkills([]string, bool) (string, []skill.ContextReceipt, error)
	}); ok {
		prompt, receipts, err := freezer.FreezeWorkflowSkills(skills.Value, skills.Mode == "override")
		if err != nil {
			return nil, err
		}
		resolved.Options.SkillPrompt = &prompt
		resolved.Options.SkillReceipts = receipts
	} else if skills.Mode != "inherit" && len(skills.Value) > 0 {
		return nil, fmt.Errorf("Workflow Skill loading is unavailable")
	}
	prompts, err := policy("prompts")
	if err != nil {
		return nil, err
	}
	for _, id := range prompts.Value {
		if s.promptStore == nil {
			return nil, workflowConfigError("Prompt is unavailable: %s", id)
		}
		prompt, found, err := s.promptStore.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if !found || !prompt.Enabled {
			return nil, workflowConfigError("Prompt is unavailable: %s", id)
		}
		digest := sha256.Sum256([]byte(prompt.BodyMD))
		resolved.Prompts = append(resolved.Prompts, workflowResolvedPrompt{ID: id, Title: prompt.Title, Body: prompt.BodyMD, Digest: hex.EncodeToString(digest[:]), UpdatedAt: prompt.UpdatedAt})
	}
	documents, err := policy("knowledge")
	if err != nil {
		return nil, err
	}
	resolved.Options.KnowledgeMode = documents.Mode
	if len(documents.Value) > 0 {
		if s.kb == nil {
			return nil, fmt.Errorf("Knowledge retrieval is unavailable")
		}
		for _, id := range documents.Value {
			doc, err := s.kb.GetDocument(ctx, id)
			if err != nil {
				return nil, err
			}
			if doc == nil {
				return nil, workflowConfigError("Knowledge document is unavailable: %s", id)
			}
		}
		resolved.Options.KnowledgeMode = "override"
		resolved.Options.KnowledgeFilter = knowledge.Filter{DocumentIDs: documents.Value}
	}
	tools, err := policy("tools")
	if err != nil {
		return nil, err
	}
	resolved.Options.ToolsMode = tools.Mode
	if len(tools.Value) > 0 {
		if s.mcpMgr == nil {
			return nil, fmt.Errorf("MCP tools are unavailable")
		}
		definitions := s.mcpMgr.ListToolDefinitions()
		for _, name := range tools.Value {
			found := false
			for _, def := range definitions {
				if def.Function.Name == name {
					resolved.Options.ToolDefinitions = append(resolved.Options.ToolDefinitions, def)
					found = true
					break
				}
			}
			if !found {
				return nil, workflowConfigError("Tool is unavailable: %s", name)
			}
		}
		resolved.Options.ToolsMode = "override"
	}
	resolved.Options.MemoryRetrieval = firstNonEmpty(resolved.Settings.Memory.Retrieval.Mode, "inherit")
	if !oneOf(resolved.Options.MemoryRetrieval, "inherit", "related", "none") {
		return nil, workflowConfigError("Invalid workflow memory retrieval mode")
	}
	if resolved.Options.MemoryRetrieval == "related" {
		resolved.Options.MemoryQuery = resolved.Settings.Memory.Retrieval.Query
	}
	resolved.Options.MemoryWriteback = firstNonEmpty(resolved.Settings.Memory.Writeback.Mode, "inherit")
	if !oneOf(resolved.Options.MemoryWriteback, "inherit", "suggest", "none") {
		return nil, workflowConfigError("Invalid workflow memory writeback mode")
	}
	if !oneOf(resolved.Settings.Output.Kind, "", "inherit", "text", "structured", "artifact") {
		return nil, workflowConfigError("Invalid workflow output kind")
	}
	encoded, _ := json.Marshal(resolved)
	sum := sha256.Sum256(encoded)
	resolved.Digest = hex.EncodeToString(sum[:])
	return resolved, nil
}

func oneOf(value string, values ...string) bool {
	for _, item := range values {
		if value == item {
			return true
		}
	}
	return false
}

// prepareWorkflowResources 冻结到运行记录，原定义和修订号保持；恢复不重新解析目录。
func (s *Server) prepareWorkflowResources(wf *WorkflowData) (map[string]map[string]any, error) {
	snapshots := map[string]map[string]any{}
	for _, raw := range wf.Nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		data := workflowNodeData(node)
		snapshot := map[string]any{}
		switch node["type"] {
		case "agent":
			resolved, err := s.resolveWorkflowResources(context.Background(), data, firstNonEmpty(stringValue(data["agent"]), stringValue(data["role"])))
			if err != nil {
				return nil, err
			}
			snapshot["main"] = resourceMap(resolved)
		case "parallel":
			var resolvedBranches []any
			if branches, ok := data["branches"].([]any); ok {
				for _, rawBranch := range branches {
					branch, _ := rawBranch.(map[string]any)
					resolved, err := s.resolveWorkflowResources(context.Background(), map[string]any{}, stringValue(branch["agent"]))
					if err != nil {
						return nil, err
					}
					resolvedBranches = append(resolvedBranches, resourceMap(resolved))
				}
			}
			snapshot["branches"] = resolvedBranches
		}
		if len(snapshot) > 0 {
			snapshots[stringValue(node["id"])] = snapshot
		}
	}
	return snapshots, nil
}

func workflowRunTimeout(wf *WorkflowData) (time.Duration, error) {
	settings := resourceMap(wf.Data["run_settings"])
	timeout := resourceMap(settings["timeout"])
	if timeout["mode"] != "custom" {
		return 10 * time.Minute, nil
	}
	var minutes float64
	if err := resourceJSON(timeout["minutes"], &minutes); err != nil || math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes <= 0 || minutes > float64(math.MaxInt64)/float64(time.Minute) {
		return 0, workflowConfigError("Workflow timeout must be a finite positive number of minutes")
	}
	return time.Duration(minutes * float64(time.Minute)), nil
}

func (e *workflowExecutor) prepareStepInvocation(ctx context.Context, node *workflowNode, metadata map[string]string, input string, branchIndex ...int) (context.Context, string, *workflowResolvedResources, error) {
	if e.run == nil {
		return ctx, input, nil, nil
	}
	snapshot := e.run.ResourceSnapshots[node.ID]
	raw := snapshot["main"]
	if len(branchIndex) > 0 {
		if values, ok := snapshot["branches"].([]any); ok && branchIndex[0] < len(values) {
			raw = values[branchIndex[0]]
		}
	}
	if raw == nil {
		return ctx, input, nil, nil
	}
	var resolved workflowResolvedResources
	if err := resourceJSON(raw, &resolved); err != nil {
		return ctx, "", nil, fmt.Errorf("Invalid frozen workflow resources: %w", err)
	}
	if cfg := resolved.Options.Agent; cfg != nil {
		if metadata["model"] == "" && cfg.Model != "" {
			metadata["model"] = cfg.Model
		}
		if metadata["provider"] == "" && cfg.Provider != "" {
			metadata["provider"] = cfg.Provider
		}
		metadata["skills"] = strings.Join(cfg.Skills, ",")
	}
	if policy, ok := resolved.Settings.Resources["skills"]; ok && policy.Mode != "" && policy.Mode != "inherit" {
		metadata["skills"] = strings.Join(policy.Value, ",")
		if policy.Mode == "none" {
			metadata["skills"] = ""
		}
	}
	if resolved.Options.KnowledgeMode == "none" {
		metadata["knowledge"] = "off"
	}
	if resolved.Options.ToolsMode == "none" {
		metadata["tools_enabled"] = "off"
	} else if resolved.Options.ToolsMode == "override" {
		metadata["tools_enabled"] = "on"
	}
	for _, prompt := range resolved.Prompts {
		input = library.Render(library.Prompt{BodyMD: prompt.Body}, input) + "\n\n" + input
	}
	if resolved.Options.MemoryWriteback == "suggest" {
		input += "\nSuggest useful memory updates only; do not write memories."
	}
	output := resolved.Settings.Output
	if output.Instructions != "" {
		input += "\n\n[Output requirements]\n" + output.Instructions
	}
	if output.Kind == "structured" {
		input += "\nReturn a JSON object without Markdown fences. Required fields:\n" + output.Fields
	}
	if output.Kind == "artifact" {
		input += "\nReturn actual file or image artifacts using the available capabilities. Do not invent artifact references."
	}
	return engine.WithWorkflowStepOptions(ctx, &resolved.Options), input, &resolved, nil
}

func validateWorkflowOutput(resolved *workflowResolvedResources, reply *adapter.Reply) error {
	if resolved == nil {
		return nil
	}
	output := resolved.Settings.Output
	if output.Kind == "structured" {
		var value map[string]json.RawMessage
		if err := json.Unmarshal([]byte(reply.Content), &value); err != nil || value == nil {
			return fmt.Errorf("Workflow output must be a JSON object")
		}
		for _, line := range strings.Split(output.Fields, "\n") {
			parts := strings.FieldsFunc(line, func(r rune) bool { return r == ':' || r == '：' })
			if len(parts) == 0 {
				continue
			}
			name := strings.TrimSpace(parts[0])
			if name != "" {
				if _, ok := value[name]; !ok {
					return fmt.Errorf("Workflow output is missing field: %s", name)
				}
				if len(parts) > 1 {
					var field any
					_ = json.Unmarshal(value[name], &field)
					kind := strings.ToLower(strings.TrimSpace(parts[1]))
					valid := true
					switch kind {
					case "文本", "text", "string":
						_, valid = field.(string)
					case "列表", "array", "list":
						_, valid = field.([]any)
					case "数字", "number":
						_, valid = field.(float64)
					case "integer":
						number, ok := field.(float64)
						valid = ok && math.Trunc(number) == number
					case "布尔", "boolean", "bool":
						_, valid = field.(bool)
					case "对象", "object":
						_, valid = field.(map[string]any)
					}
					if !valid {
						return fmt.Errorf("Workflow output field has an unexpected type: %s", name)
					}
				}
			}
		}
	}
	if output.Kind == "artifact" && (reply.MessageContent == nil || len(reply.MessageContent.Attachments) == 0) {
		return fmt.Errorf("Workflow output contains no actual artifact references")
	}
	if output.Kind == "artifact" {
		if err := reply.MessageContent.Validate(); err != nil {
			return fmt.Errorf("Invalid workflow artifact content: %w", err)
		}
		if len(output.ArtifactKinds) > 0 {
			matched := false
			for _, ref := range reply.MessageContent.Attachments {
				kind := "file"
				if strings.HasPrefix(ref.MIME, "image/") {
					kind = "image"
				}
				for _, allowed := range output.ArtifactKinds {
					if kind == allowed {
						matched = true
					}
				}
			}
			if !matched {
				return fmt.Errorf("Workflow output has no artifacts of the requested kind")
			}
		}
	}
	return nil
}

func (e *workflowExecutor) recordStepReceipt(node *workflowNode, resolved *workflowResolvedResources, reply *adapter.Reply, outputErr error, branchIndex ...int) {
	key := "main"
	if len(branchIndex) > 0 {
		key = fmt.Sprintf("branch-%d", branchIndex[0]+1)
	}
	receipt := &WorkflowStepReceipt{Effective: resolved}
	if reply != nil {
		receipt.KnowledgeHits = reply.KnowledgeHits
		receipt.MemoryHits = reply.MemoryHits
		receipt.ToolCalls = reply.ToolCalls
		if reply.MessageContent != nil {
			receipt.Artifacts = reply.MessageContent.Attachments
		}
		if value := reply.Metadata["skill_context_receipts"]; json.Valid([]byte(value)) {
			receipt.SkillReceipts = json.RawMessage(value)
		}
	}
	if outputErr != nil {
		receipt.OutputError = outputErr.Error()
		receipt.RetryRequiresReview = reply != nil && len(reply.ToolCalls) > 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	result := e.ensureNodeRun(node)
	if result.ResourceReceipts == nil {
		result.ResourceReceipts = map[string]*WorkflowStepReceipt{}
	}
	result.ResourceReceipts[key] = receipt
}

func cloneStepReceipts(values map[string]*WorkflowStepReceipt) map[string]*WorkflowStepReceipt {
	if values == nil {
		return nil
	}
	result := make(map[string]*WorkflowStepReceipt, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
