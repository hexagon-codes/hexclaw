package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/hexagon-codes/toolkit/util/idgen"
	"gopkg.in/yaml.v3"
)

const workflowPortableVersion = "1.0.0"

type workflowImportIssue struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	NodeID   string `json:"node_id,omitempty"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
}

type workflowImportResult struct {
	Format        string                `json:"format"`
	SchemaVersion string                `json:"schema_version"`
	Workflow      *WorkflowData         `json:"workflow"`
	Issues        []workflowImportIssue `json:"issues"`
	CanImport     bool                  `json:"can_import"`
}

type workflowPortableEnvelope struct {
	Format        string                `json:"format"`
	SchemaVersion string                `json:"schema_version"`
	Workflow      *WorkflowData         `json:"workflow"`
	Issues        []workflowImportIssue `json:"issues"`
}

func (result *workflowImportResult) issue(severity, code, nodeID, field, message string) {
	result.Issues = append(result.Issues, workflowImportIssue{severity, code, nodeID, field, message})
}

func (result *workflowImportResult) finish() {
	result.CanImport = result.Workflow != nil
	for _, issue := range result.Issues {
		if issue.Severity == "error" {
			result.CanImport = false
		}
	}
}

// 预检只读取定义与当前资源目录，不创建工作流、绑定或运行。
func (s *Server) handlePreviewWorkflowImport(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.readWorkflowImport(r))
}

func (s *Server) handleImportWorkflow(w http.ResponseWriter, r *http.Request) {
	result := s.readWorkflowImport(r)
	if !result.CanImport {
		writeJSON(w, http.StatusUnprocessableEntity, result)
		return
	}
	wf := result.Workflow
	wf.ID = "wf-" + idgen.ShortID()
	wf.CreatedAt = time.Now()
	wf.UpdatedAt = wf.CreatedAt
	wf.Revision = workflowRevision(wf)
	s.workflowStore.mu.Lock()
	for s.workflowStore.workflows[wf.ID] != nil {
		wf.ID = "wf-" + idgen.ShortID()
	}
	s.workflowStore.workflows[wf.ID] = wf
	if err := s.workflowStore.persistToFile(); err != nil {
		delete(s.workflowStore.workflows, wf.ID)
		s.workflowStore.mu.Unlock()
		writeAPIError(w, http.StatusInternalServerError, "WORKFLOW_PERSIST_FAILED", "Workflow could not be imported")
		return
	}
	s.workflowStore.mu.Unlock()
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleExportWorkflow(w http.ResponseWriter, r *http.Request) {
	s.workflowStore.mu.RLock()
	wf := cloneWorkflowDefinition(s.workflowStore.workflows[r.PathValue("id")])
	s.workflowStore.mu.RUnlock()
	if wf == nil {
		writeAPIError(w, http.StatusNotFound, "WORKFLOW_NOT_FOUND", "Workflow not found")
		return
	}
	envelope := portableWorkflow(wf, false)
	filename := strings.ReplaceAll(strings.ReplaceAll(wf.Name, "/", "-"), "\\", "-") + ".hexclaw.json"
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	writeJSON(w, http.StatusOK, envelope)
}

// 未保存草稿与已保存定义走同一导出封装，不为导出制造一次保存。
func (s *Server) handleExportWorkflowDefinition(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Workflow map[string]json.RawMessage `json:"workflow"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "WORKFLOW_EXPORT_INVALID_JSON", "Workflow export request must contain valid JSON")
		return
	}
	if request.Workflow == nil {
		writeAPIError(w, http.StatusBadRequest, "WORKFLOW_DEFINITION_REQUIRED", "A workflow definition is required")
		return
	}
	// 导出只接收定义，生命周期时间不属于草稿合同，也不参与时间解析。
	for _, field := range []string{"created_at", "updated_at", "revision"} {
		delete(request.Workflow, field)
	}
	encoded, _ := json.Marshal(request.Workflow)
	var definition WorkflowData
	if err := json.Unmarshal(encoded, &definition); err != nil {
		writeAPIError(w, http.StatusBadRequest, "WORKFLOW_DEFINITION_INVALID", "Workflow definition contains invalid fields")
		return
	}
	result := workflowImportResult{Workflow: &definition, Issues: []workflowImportIssue{}}
	s.validatePortableWorkflow(&result, false)
	result.finish()
	if !result.CanImport {
		writeJSON(w, http.StatusUnprocessableEntity, result)
		return
	}
	writeJSON(w, http.StatusOK, portableWorkflow(&definition, false))
}

func (s *Server) readWorkflowImport(r *http.Request) workflowImportResult {
	result := workflowImportResult{Format: "unknown", Issues: []workflowImportIssue{}}
	reader, err := r.MultipartReader()
	if err != nil {
		result.issue("error", "FILE_REQUIRED", "", "file", "Upload a workflow file using multipart field file")
		return result
	}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			result.issue("error", "FILE_REQUIRED", "", "file", "A workflow file is required")
			return result
		}
		if err != nil {
			result.issue("error", "INVALID_WORKFLOW_FILE", "", "file", "Workflow file could not be read")
			return result
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		content, readErr := io.ReadAll(part)
		name := part.FileName()
		_ = part.Close()
		if readErr != nil {
			result.issue("error", "INVALID_WORKFLOW_FILE", "", "file", "Workflow file could not be read")
			return result
		}
		return s.parseWorkflowImport(name, content)
	}
}

func (s *Server) parseWorkflowImport(filename string, content []byte) workflowImportResult {
	result := workflowImportResult{Format: "unknown", Issues: []workflowImportIssue{}}
	if strings.EqualFold(path.Ext(filename), ".zip") || bytes.HasPrefix(content, []byte("PK\x03\x04")) {
		result.Format = "coze-zip"
		archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
		if err != nil {
			result.issue("error", "INVALID_ZIP", "", "file", "Workflow ZIP is invalid")
			return result
		}
		var definitions []*zip.File
		hasManifest := false
		for _, entry := range archive.File {
			filePath := strings.ReplaceAll(entry.Name, "\\", "/")
			if !entry.FileInfo().IsDir() && path.Base(filePath) == "MANIFEST.yml" {
				hasManifest = true
			}
			extension := strings.ToLower(path.Ext(filePath))
			if !entry.FileInfo().IsDir() && !strings.HasPrefix(filePath, "__MACOSX/") && strings.Contains("/"+filePath, "/workflow/") && (extension == ".yaml" || extension == ".yml") {
				definitions = append(definitions, entry)
			}
		}
		if !hasManifest {
			result.issue("error", "COZE_MANIFEST_REQUIRED", "", "MANIFEST.yml", "A Coze ZIP must include MANIFEST.yml")
			return result
		}
		if len(definitions) != 1 {
			result.issue("error", "UNSUPPORTED_ZIP_CONTENTS", "", "file", "A Coze ZIP must contain exactly one workflow YAML; multiple workflows and subflows are not supported")
			return result
		}
		reader, err := definitions[0].Open()
		if err != nil {
			result.issue("error", "INVALID_ZIP", "", "file", "Workflow ZIP entry could not be opened")
			return result
		}
		content, err = io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			result.issue("error", "INVALID_ZIP", "", "file", "Workflow ZIP entry could not be read")
			return result
		}
		s.convertCozeWorkflow(content, &result)
	} else if bytes.HasPrefix(bytes.TrimSpace(bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})), []byte("{")) || strings.EqualFold(path.Ext(filename), ".json") {
		result.Format = "hexclaw-json"
		content = bytes.TrimSpace(bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf}))
		var envelope workflowPortableEnvelope
		if err := json.Unmarshal(content, &envelope); err != nil {
			result.issue("error", "INVALID_JSON", "", "file", "Workflow JSON is invalid")
			return result
		}
		result.SchemaVersion = envelope.SchemaVersion
		if envelope.Format != "hexclaw-workflow" || envelope.SchemaVersion != workflowPortableVersion || envelope.Workflow == nil {
			result.issue("error", "UNSUPPORTED_FORMAT", "", "format", "Expected a HexClaw workflow package with schema_version 1.0.0")
			return result
		}
		portable := portableWorkflow(envelope.Workflow, true)
		result.Workflow, result.Issues = portable.Workflow, portable.Issues
	} else {
		result.Format = "coze-yaml"
		s.convertCozeWorkflow(content, &result)
	}
	if result.Workflow != nil {
		s.validatePortableWorkflow(&result, true)
	}
	result.finish()
	return result
}

func portableWorkflow(source *WorkflowData, importing bool) workflowPortableEnvelope {
	wf := cloneWorkflowDefinition(source)
	result := workflowPortableEnvelope{Format: "hexclaw-workflow", SchemaVersion: workflowPortableVersion, Workflow: wf, Issues: []workflowImportIssue{}}
	if wf == nil {
		return result
	}
	wf.ID, wf.Revision = "", ""
	wf.CreatedAt, wf.UpdatedAt = time.Time{}, time.Time{}
	// 仅移除运行/绑定事实与服务凭据，不按普通业务参数名猜测并删去节点语义。
	for _, key := range []string{"runs", "run_history", "resource_snapshots", "checkpoint", "triggers", "trigger_bindings", "credentials", "credential", "provider_credentials"} {
		if _, ok := wf.Data[key]; ok {
			delete(wf.Data, key)
			result.Issues = append(result.Issues, workflowImportIssue{Severity: "warning", Code: "SERVICE_STATE_EXCLUDED", Path: "data." + key, Message: "Service state and credentials are excluded from workflow packages"})
		}
	}
	scrubPortableCredentials(wf.Data, "data", "", &result.Issues)
	for _, raw := range wf.Nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		nodeID := stringValue(node["id"])
		data := workflowNodeData(node)
		scrubPortableCredentials(data, "config", nodeID, &result.Issues)
		if !importing {
			continue
		}
		delivery, _ := data["delivery"].(map[string]any)
		changed := false
		if session, ok := delivery["session"].(map[string]any); ok && stringValue(session["mode"]) != "" && stringValue(session["mode"]) != "none" {
			session["mode"] = "none"
			changed = true
		}
		for _, kind := range []string{"dingtalk", "email", "directory"} {
			if target, ok := delivery[kind].(map[string]any); ok && target["enabled"] == true {
				target["enabled"] = false
				changed = true
			}
		}
		if changed {
			result.Issues = append(result.Issues, workflowImportIssue{Severity: "warning", Code: "DELIVERY_DISABLED", NodeID: nodeID, Path: "config.delivery", Message: "Imported delivery is disabled; destination references are retained for review"})
		}
	}
	return result
}

func scrubPortableCredentials(value any, field, nodeID string, issues *[]workflowImportIssue) {
	switch typed := value.(type) {
	case map[string]any:
		if portableCredentialKey(stringValue(typed["name"])) {
			if _, ok := typed["input"]; ok {
				delete(typed, "input")
				*issues = append(*issues, workflowImportIssue{Severity: "warning", Code: "CREDENTIAL_EXCLUDED", NodeID: nodeID, Path: field + ".input", Message: "Service credentials are excluded; configure the referenced service on the target installation"})
			}
		}
		for key, child := range typed {
			if portableCredentialKey(key) {
				delete(typed, key)
				*issues = append(*issues, workflowImportIssue{Severity: "warning", Code: "CREDENTIAL_EXCLUDED", NodeID: nodeID, Path: field + "." + key, Message: "Service credentials are excluded; configure the referenced service on the target installation"})
				continue
			}
			scrubPortableCredentials(child, field+"."+key, nodeID, issues)
		}
	case []any:
		for index, child := range typed {
			scrubPortableCredentials(child, fmt.Sprintf("%s[%d]", field, index), nodeID, issues)
		}
	}
}

func portableCredentialKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
	return oneOf(normalized, "credentials", "credential", "apikey", "xapikey", "accesstoken", "refreshtoken", "clientsecret", "authorization", "privatekey")
}

func (s *Server) validatePortableWorkflow(result *workflowImportResult, checkResources bool) {
	wf := result.Workflow
	if len(wf.Nodes) == 0 {
		result.issue("error", "WORKFLOW_NODES_REQUIRED", "", "nodes", "Add at least one workflow step before exporting or importing")
	}
	if strings.TrimSpace(wf.Name) == "" {
		result.issue("error", "WORKFLOW_NAME_REQUIRED", "", "name", "Workflow name is required")
	}
	if err := validateWorkflowInputLengths(*wf, nil); err != nil {
		result.issue("error", "INVALID_WORKFLOW_CONFIG", "", "workflow", err.Error())
	}
	if _, err := workflowRunTimeout(wf); err != nil {
		result.issue("error", "INVALID_WORKFLOW_CONFIG", "", "data.run_settings", err.Error())
	}
	for _, raw := range wf.Nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			result.issue("error", "INVALID_NODE", "", "nodes", "Each workflow node must be an object")
			continue
		}
		nodeID, kind := stringValue(node["id"]), stringValue(node["type"])
		if !oneOf(kind, "input", "agent", "tool", "output", "parallel", "condition", "handoff", "agent_handoff", "fanout") {
			result.issue("error", "UNSUPPORTED_NODE", nodeID, "type", "Unsupported workflow node type: "+kind)
		}
		if checkResources {
			s.inspectPortableResources(result, nodeID, kind, workflowNodeData(node))
		}
	}
	for _, raw := range wf.Edges {
		edge, ok := raw.(map[string]any)
		if !ok || firstNonEmpty(stringValue(edge["from"]), stringValue(edge["source"])) == "" || firstNonEmpty(stringValue(edge["to"]), stringValue(edge["target"])) == "" {
			result.issue("error", "INVALID_EDGE", "", "edges", "Each workflow edge must reference a source and target node")
		}
	}
	executor := newWorkflowExecutor(s, wf, RunWorkflowRequest{})
	if err := executor.parse(); err != nil {
		result.issue("error", "INVALID_GRAPH", "", "nodes", err.Error())
	} else if err := executor.buildStages(); err != nil {
		result.issue("error", "INVALID_GRAPH", "", "edges", err.Error())
	}
}

func (s *Server) inspectPortableResources(result *workflowImportResult, nodeID, kind string, data map[string]any) {
	warn := func(field, value string) {
		result.issue("warning", "RESOURCE_UNAVAILABLE", nodeID, field, "Resource is not available on this service: "+value)
	}
	if data["import_requires_review"] == true {
		result.issue("warning", "IMPORT_REVIEW_REQUIRED", nodeID, "config.import_source", "This imported step must be explicitly configured and reviewed before the workflow can run")
	}
	if name := stringValue(data["agent"]); name != "" {
		if s.agentRouter == nil {
			warn("config.agent", name)
		} else if _, ok := s.agentRouter.GetAgent(name); !ok {
			warn("config.agent", name)
		}
	}
	if kind == "parallel" || kind == "fanout" {
		if branches, ok := data["branches"].([]any); ok {
			for _, raw := range branches {
				branch, _ := raw.(map[string]any)
				name := stringValue(branch["agent"])
				if s.agentRouter == nil {
					warn("config.branches.agent", name)
				} else if _, ok := s.agentRouter.GetAgent(name); !ok {
					warn("config.branches.agent", name)
				}
			}
		}
	}
	toolExists := func(name string) bool {
		if s.mcpMgr != nil {
			for _, definition := range s.mcpMgr.ListToolDefinitions() {
				if definition.Function.Name == name {
					return true
				}
			}
		}
		return false
	}
	if kind == "tool" {
		if name := firstNonEmpty(stringValue(data["tool"]), stringValue(data["name"])); name != "" && !toolExists(name) {
			warn("config.tool", name)
		}
	}
	resources := resourceMap(data["resources"])
	for _, key := range []string{"skills", "prompts", "knowledge", "tools"} {
		policy := resourceMap(resources[key])
		if stringValue(policy["mode"]) != "override" {
			continue
		}
		for _, name := range stringSlice(policy["value"]) {
			found := false
			switch key {
			case "skills":
				if s.mp != nil {
					_, found = s.mp.Get(name)
				}
			case "prompts":
				if s.promptStore != nil {
					prompt, exists, err := s.promptStore.Get(context.Background(), name)
					found = err == nil && exists && prompt.Enabled
				}
			case "knowledge":
				if s.kb != nil {
					document, err := s.kb.GetDocument(context.Background(), name)
					found = err == nil && document != nil
				}
			case "tools":
				found = toolExists(name)
			}
			if !found {
				warn("config.resources."+key, name)
			}
		}
	}
}

type cozePortableNode struct {
	ID         string         `yaml:"id"`
	Type       string         `yaml:"type"`
	Title      string         `yaml:"title"`
	Position   map[string]any `yaml:"position"`
	Parameters map[string]any `yaml:"parameters"`
	Raw        map[string]any `yaml:"-"`
}

func (node *cozePortableNode) UnmarshalYAML(value *yaml.Node) error {
	type plain cozePortableNode
	if err := value.Decode((*plain)(node)); err != nil {
		return err
	}
	return value.Decode(&node.Raw)
}

type cozePortableWorkflow struct {
	SchemaVersion string             `yaml:"schema_version"`
	Name          string             `yaml:"name"`
	Description   string             `yaml:"description"`
	Mode          string             `yaml:"mode"`
	Nodes         []cozePortableNode `yaml:"nodes"`
	Edges         []struct {
		Source string `yaml:"source_node"`
		Target string `yaml:"target_node"`
		Port   string `yaml:"source_port"`
	} `yaml:"edges"`
}

func (s *Server) convertCozeWorkflow(content []byte, result *workflowImportResult) {
	var source cozePortableWorkflow
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&source); err != nil {
		result.issue("error", "INVALID_YAML", "", "file", "Coze workflow YAML is invalid")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		result.issue("error", "UNSUPPORTED_YAML_DOCUMENTS", "", "file", "Upload one workflow YAML document")
		return
	}
	result.SchemaVersion = source.SchemaVersion
	if source.SchemaVersion != workflowPortableVersion || source.Mode != "workflow" {
		result.issue("error", "UNSUPPORTED_COZE_SCHEMA", "", "schema_version", "Only Coze workflow schema 1.0.0 is supported; chatflows and other YAML formats are not supported")
		return
	}
	wf := &WorkflowData{Name: source.Name, Description: source.Description, Nodes: []any{}, Edges: []any{}, Data: map[string]any{"import_source": map[string]any{"format": result.Format, "schema_version": source.SchemaVersion}}}
	result.Workflow = wf
	sources := map[string]cozePortableNode{}
	for _, node := range source.Nodes {
		if node.ID == "" || sources[node.ID].ID != "" {
			result.issue("error", "INVALID_NODE_ID", node.ID, "id", "Coze node IDs must be nonempty and unique")
		}
		sources[node.ID] = node
	}
	incoming, outgoing := map[string]int{}, map[string]int{}
	for index, edge := range source.Edges {
		if edge.Port != "" {
			result.issue("error", "UNSUPPORTED_COZE_BRANCH", edge.Source, "edges.source_port", "Coze branch ports are not supported")
		}
		incoming[edge.Target]++
		outgoing[edge.Source]++
		wf.Edges = append(wf.Edges, map[string]any{"id": fmt.Sprintf("import-edge-%d", index+1), "from": edge.Source, "to": edge.Target})
	}
	starts, ends := 0, 0
	executableCount := 0
	for _, node := range source.Nodes {
		if incoming[node.ID] > 1 || outgoing[node.ID] > 1 {
			result.issue("error", "UNSUPPORTED_COZE_GRAPH", node.ID, "edges", "Only a single linear Coze workflow is supported")
		}
		if node.Type == "comment" {
			result.issue("warning", "COMMENT_EXCLUDED", node.ID, "nodes", "Canvas comments are not executable steps and are excluded")
			continue
		}
		executableCount++
		if node.Type == "start" && (incoming[node.ID] != 0 || outgoing[node.ID] != 1) || node.Type == "end" && (incoming[node.ID] != 1 || outgoing[node.ID] != 0) || node.Type != "start" && node.Type != "end" && (incoming[node.ID] != 1 || outgoing[node.ID] != 1) {
			result.issue("error", "UNSUPPORTED_COZE_GRAPH", node.ID, "edges", "Every executable node must belong to the single start-to-end chain")
		}
		config := map[string]any{}
		converted := map[string]any{"id": node.ID, "label": node.Title, "x": node.Position["x"], "y": node.Position["y"], "config": config}
		wf.Nodes = append(wf.Nodes, converted)
		parameters := node.Parameters
		allowed := map[string]bool{"node_inputs": true, "node_outputs": true, "settingOnError": true}
		switch node.Type {
		case "llm":
			allowed["llmParam"] = true
		case "plugin":
			allowed["apiParam"] = true
		case "end":
			allowed["terminatePlan"] = true
		}
		for key, value := range parameters {
			if !allowed[key] && !portableEmpty(value) {
				result.issue("error", "UNSUPPORTED_COZE_SETTING", node.ID, "parameters."+key, "Coze parameter cannot be represented by this workflow executor: "+key)
			}
		}
		for _, key := range []string{"batch", "skills", "pluginFCParam", "knowledgeFCParam", "workflowFCParam"} {
			if value, ok := parameters[key]; ok && !portableEmpty(value) {
				result.issue("error", "UNSUPPORTED_COZE_SETTING", node.ID, "parameters."+key, "Coze batch and attached platform resources require an explicit migration")
			}
		}
		if onError := resourceMap(parameters["settingOnError"]); len(onError) > 0 {
			if value := stringValue(onError["processType"]); value != "" && value != "1" || !portableEmpty(onError["retryTimes"]) || !portableEmpty(onError["timeoutMs"]) {
				result.issue("error", "UNSUPPORTED_COZE_ERROR_POLICY", node.ID, "parameters.settingOnError", "Coze node timeout, retry, and alternate error paths cannot be preserved by this workflow executor")
			}
		}
		switch node.Type {
		case "start":
			starts++
			converted["type"] = "input"
			config["value"] = "{{input}}"
			outputs := resourceMap(parameters["node_outputs"])
			if len(outputs) != 1 {
				result.issue("error", "UNSUPPORTED_COZE_INPUT", node.ID, "parameters.node_outputs", "Coze start must define exactly one string input")
			}
			for _, raw := range outputs {
				if stringValue(resourceMap(raw)["type"]) != "string" {
					result.issue("error", "UNSUPPORTED_COZE_INPUT", node.ID, "parameters.node_outputs", "Coze files, objects, and typed multiple inputs cannot be converted to one workflow string")
				}
			}
		case "end":
			ends++
			converted["type"] = "output"
			if stringValue(parameters["terminatePlan"]) != "returnVariables" {
				result.issue("error", "UNSUPPORTED_COZE_OUTPUT_MODE", node.ID, "parameters.terminatePlan", "Only explicit Coze returnVariables with one string field is supported")
			}
			inputs := portableList(parameters["node_inputs"])
			if len(inputs) != 1 {
				result.issue("error", "UNSUPPORTED_COZE_OUTPUT", node.ID, "parameters.node_inputs", "Coze end must select exactly one string result")
			} else {
				input := resourceMap(inputs[0])
				name := stringValue(input["name"])
				if name == "" {
					result.issue("error", "UNSUPPORTED_COZE_OUTPUT", node.ID, "parameters.node_inputs.name", "Coze return variable must have a name")
				}
				cozePortableOutput(input["input"], sources, node.ID, config, result)
				config["output_object"] = map[string]any{name: "{{value}}"}
			}
		case "llm":
			converted["type"] = "agent"
			config["agent"] = ""
			config["import_requires_review"] = true
			config["import_source"] = node.Raw
			llmParameters := cozeNamedParameters(parameters["llmParam"])
			for key, value := range llmParameters {
				if !oneOf(key, "prompt", "systemPrompt", "stableSystemPrompt", "modelName", "modelType", "modelID", "temperature", "topP", "maxTokens", "responseFormat", "generationDiversity", "frequencyPenalty", "presencePenalty", "enableChatHistory", "chatHistoryRound") {
					result.issue("error", "UNSUPPORTED_COZE_SETTING", node.ID, "parameters.llmParam."+key, "Coze model parameter has no supported migration contract: "+key)
				} else if key == "enableChatHistory" && !portableEmpty(value) {
					result.issue("error", "UNSUPPORTED_COZE_HISTORY", node.ID, "parameters.llmParam."+key, "Coze conversation history cannot be migrated to an Agent step")
				} else if key != "prompt" && key != "enableChatHistory" && key != "chatHistoryRound" {
					result.issue("warning", "COZE_SETTING_REQUIRES_REVIEW", node.ID, "parameters.llmParam."+key, "Source setting is retained for review and is not applied to the selected HexClaw Agent: "+key)
				}
			}
			prompt := stringValue(llmParameters["prompt"])
			variables := map[string]string{}
			for _, raw := range portableList(parameters["node_inputs"]) {
				input := resourceMap(raw)
				variables[stringValue(input["name"])] = cozePortableInput(input["input"], sources, node.ID, result)
			}
			convertedPrompt := cozePortablePrompt(prompt, variables, node.ID, result)
			if systemPrompt := firstNonEmpty(stringValue(llmParameters["systemPrompt"]), stringValue(llmParameters["stableSystemPrompt"])); strings.TrimSpace(systemPrompt) != "" {
				convertedPrompt = "Source system instructions:\n" + cozePortablePrompt(systemPrompt, variables, node.ID, result) + "\n\nSource user task:\n" + convertedPrompt
			}
			config["prompt"] = convertedPrompt
			outputs := resourceMap(parameters["node_outputs"])
			if len(outputs) != 1 {
				result.issue("error", "UNSUPPORTED_COZE_LLM_OUTPUT", node.ID, "parameters.node_outputs", "Only one text result can be converted from a Coze LLM node")
			}
			for _, raw := range outputs {
				if stringValue(resourceMap(raw)["type"]) != "string" {
					result.issue("error", "UNSUPPORTED_COZE_LLM_OUTPUT", node.ID, "parameters.node_outputs", "Coze structured LLM outputs require an explicit output schema migration")
				}
			}
			// 迁移草稿保留来源；核对并选择 Agent 前，运行入口必须拒绝执行。
			result.issue("warning", "COZE_AGENT_MAPPING_REQUIRED", node.ID, "parameters.llmParam", "Select a HexClaw Agent and review the retained source settings; imported model and system settings are not automatically applied")
		case "plugin":
			converted["type"] = "tool"
			config["tool"] = ""
			config["import_requires_review"] = true
			config["import_source"] = node.Raw
			result.issue("warning", "COZE_PLUGIN_MAPPING_REQUIRED", node.ID, "parameters.apiParam", "Select an installed MCP tool and review the source plugin and arguments; Coze plugin identifiers are not executed")
			args := map[string]any{}
			for _, raw := range portableList(parameters["node_inputs"]) {
				input := resourceMap(raw)
				args[stringValue(input["name"])] = cozePortableInput(input["input"], sources, node.ID, result)
			}
			config["args"] = args
		default:
			converted["type"] = node.Type
			result.issue("error", "UNSUPPORTED_COZE_NODE", node.ID, "type", "Unsupported Coze node type: "+node.Type)
		}
	}
	if starts != 1 || ends != 1 {
		result.issue("error", "UNSUPPORTED_COZE_BOUNDARIES", "", "nodes", "A Coze workflow must have exactly one start and one end node")
	}
	if len(source.Edges) != executableCount-1 {
		result.issue("error", "UNSUPPORTED_COZE_GRAPH", "", "edges", "Coze workflow must be one connected linear chain")
	}
	// 来源中的凭据与服务状态同样不进入迁移定义；其余节点原文完整保留供核对。
	portable := portableWorkflow(wf, true)
	result.Workflow = portable.Workflow
	result.Issues = append(result.Issues, portable.Issues...)
}

func validateWorkflowImportReview(wf *WorkflowData) error {
	for _, raw := range wf.Nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		data := workflowNodeData(node)
		if data["import_requires_review"] == true {
			return workflowConfigError("Imported step requires explicit resource mapping and review: %s", stringValue(node["id"]))
		}
		if data["import_source"] != nil && (node["type"] == "agent" && stringValue(data["agent"]) == "" || node["type"] == "tool" && stringValue(data["tool"]) == "") {
			return workflowConfigError("Imported step requires an explicit Agent or tool selection: %s", stringValue(node["id"]))
		}
	}
	return nil
}

func cozePortableOutput(raw any, sources map[string]cozePortableNode, nodeID string, config map[string]any, result *workflowImportResult) {
	input := resourceMap(raw)
	if reference, ok := input["value"].(map[string]any); ok {
		sourceID, field := stringValue(reference["ref_node"]), stringValue(reference["path"])
		source, found := sources[sourceID]
		if !found || field == "" {
			result.issue("error", "INVALID_COZE_REFERENCE", nodeID, "parameters.node_inputs", "Coze output references a missing node or field")
			return
		}
		config["input"] = "{{node." + sourceID + "}}"
		if source.Type == "start" || source.Type == "llm" {
			outputs := resourceMap(source.Parameters["node_outputs"])
			if len(outputs) != 1 || stringValue(resourceMap(outputs[field])["type"]) != "string" {
				result.issue("error", "UNSUPPORTED_COZE_REFERENCE", nodeID, "parameters.node_inputs", "Coze output must reference the single text output of its source")
			}
			config["input_selection"] = map[string]any{"kind": "text", "field": ""}
			return
		}
		if source.Type == "plugin" {
			definition := resourceMap(source.Parameters["node_outputs"])
			parts := strings.Split(field, ".")
			var selected map[string]any
			for index, part := range parts {
				if index == 0 {
					selected = resourceMap(definition[part])
				} else {
					selected = resourceMap(resourceMap(selected["properties"])[part])
				}
			}
			if stringValue(selected["type"]) != "string" {
				result.issue("error", "UNSUPPORTED_COZE_OUTPUT", nodeID, "parameters.node_inputs", "Coze plugin output must select one declared JSON string field")
			}
			config["input_selection"] = map[string]any{"kind": "structured", "field": field}
			return
		}
		result.issue("error", "UNSUPPORTED_COZE_REFERENCE", nodeID, "parameters.node_inputs", "Coze output source cannot be converted")
		return
	}
	config["input"] = cozePortableInput(raw, sources, nodeID, result)
	config["input_selection"] = map[string]any{"kind": "text", "field": ""}
}

func portableEmpty(value any) bool {
	if value == nil || value == false || value == "" {
		return true
	}
	switch typed := value.(type) {
	case int:
		return typed == 0
	case float64:
		return typed == 0
	case string:
		return typed == "0" || typed == "false"
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	}
	return false
}

func portableList(value any) []any {
	values, _ := value.([]any)
	return values
}

func cozeNamedParameters(raw any) map[string]any {
	values := map[string]any{}
	for _, parameter := range portableList(raw) {
		item := resourceMap(parameter)
		values[stringValue(item["name"])] = resourceMap(item["input"])["value"]
	}
	return values
}

func cozePortableInput(raw any, sources map[string]cozePortableNode, nodeID string, result *workflowImportResult) string {
	input := resourceMap(raw)
	value := input["value"]
	if reference, ok := value.(map[string]any); ok {
		sourceID, field := stringValue(reference["ref_node"]), stringValue(reference["path"])
		source, found := sources[sourceID]
		if !found || field == "" {
			result.issue("error", "INVALID_COZE_REFERENCE", nodeID, "parameters.node_inputs", "Coze input references a missing node or field")
			return ""
		}
		outputs := resourceMap(source.Parameters["node_outputs"])
		if source.Type == "start" || source.Type == "llm" {
			if _, ok := outputs[field]; !ok || len(outputs) != 1 {
				result.issue("error", "UNSUPPORTED_COZE_REFERENCE", nodeID, "parameters.node_inputs", "Coze input must reference the single text output of its source")
				return ""
			}
			if source.Type == "start" {
				return "{{input}}"
			}
			return "{{node." + sourceID + "}}"
		}
		if source.Type == "plugin" {
			return "{{node." + sourceID + "." + field + "}}"
		}
		result.issue("error", "UNSUPPORTED_COZE_REFERENCE", nodeID, "parameters.node_inputs", "Coze input source cannot be converted")
		return ""
	}
	if text, ok := value.(string); ok && (input["type"] == nil || stringValue(input["type"]) == "string") {
		return text
	}
	result.issue("error", "UNSUPPORTED_COZE_VALUE", nodeID, "parameters.node_inputs", "Only string constants and supported node references can be converted")
	return ""
}

var cozePortableVariable = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

func cozePortablePrompt(prompt string, variables map[string]string, nodeID string, result *workflowImportResult) string {
	return cozePortableVariable.ReplaceAllStringFunc(prompt, func(token string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(token, "{{"), "}}")
		if value, ok := variables[name]; ok {
			return value
		}
		result.issue("error", "UNSUPPORTED_COZE_TEMPLATE", nodeID, "parameters.llmParam.prompt", "Coze prompt uses an unmapped variable: "+name)
		return token
	})
}
