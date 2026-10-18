package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
)

func portabilityUpload(t *testing.T, endpoint, name string, content []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, endpoint, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func portabilityIssue(result workflowImportResult, code string) *workflowImportIssue {
	for _, issue := range result.Issues {
		if issue.Code == code {
			return &issue
		}
	}
	return nil
}

func TestWorkflowPortabilityNativeRoundTripCreatesIndependentDraft(t *testing.T) {
	s := newWorkflowTestServer()
	s.engine = nil
	s.workflowStore.filePath = filepath.Join(t.TempDir(), "workflows.json")
	wf := contractWorkflow(
		contractNode("input", "input", map[string]any{"value": "{{input}}"}),
		contractNode("analyze", "agent", map[string]any{"agent": "missing-analyst", "prompt": "Summarize {{node.input}}", "resources": map[string]any{"prompts": map[string]any{"mode": "override", "value": []string{"prompt-example"}}}}),
		contractNode("output", "output", map[string]any{"input": "{{node.analyze}}", "delivery": map[string]any{"session": map[string]any{"mode": "workflow"}, "dingtalk": map[string]any{"enabled": true, "instance_id": "instance-example", "chat_id": "chat-example"}}}),
	)
	wf.Data = map[string]any{"run_settings": map[string]any{"timeout": map[string]any{"mode": "custom", "minutes": 7}}, "trigger_bindings": []string{"existing-trigger"}, "credentials": map[string]any{"api_key": "test-service-secret"}}
	s.workflowStore.workflows[wf.ID] = wf
	before, err := json.Marshal(wf)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/canvas/workflows/wf-contract/export", nil)
	request.SetPathValue("id", wf.ID)
	s.handleExportWorkflow(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("export failed: %d %s", recorder.Code, recorder.Body.String())
	}
	packageBytes := append([]byte(nil), recorder.Body.Bytes()...)
	var envelope workflowPortableEnvelope
	if err := json.Unmarshal(packageBytes, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Format != "hexclaw-workflow" || envelope.SchemaVersion != "1.0.0" || envelope.Workflow.ID != "" || envelope.Workflow.Data["trigger_bindings"] != nil || bytes.Contains(packageBytes, []byte("test-service-secret")) {
		t.Fatalf("invalid portable definition: %s", packageBytes)
	}
	output := workflowNodeData(envelope.Workflow.Nodes[2].(map[string]any))
	if resourceMap(resourceMap(output["delivery"])["dingtalk"])["enabled"] != true {
		t.Fatal("export changed the authored delivery configuration")
	}
	preview := httptest.NewRecorder()
	s.handlePreviewWorkflowImport(preview, portabilityUpload(t, "/api/v1/canvas/workflows/import/preview", "contract.json", packageBytes))
	var inspected workflowImportResult
	if err := json.Unmarshal(preview.Body.Bytes(), &inspected); err != nil {
		t.Fatal(err)
	}
	if preview.Code != 200 || !inspected.CanImport || portabilityIssue(inspected, "DELIVERY_DISABLED") == nil || portabilityIssue(inspected, "RESOURCE_UNAVAILABLE") == nil {
		t.Fatalf("preview did not describe migration: %d %s", preview.Code, preview.Body.String())
	}
	if len(s.workflowStore.workflows) != 1 || len(s.workflowStore.runs) != 0 {
		t.Fatal("preview wrote or ran a workflow")
	}
	var importedIDs []string
	for range 2 {
		recorder = httptest.NewRecorder()
		s.handleImportWorkflow(recorder, portabilityUpload(t, "/api/v1/canvas/workflows/import", "contract.json", packageBytes))
		var imported workflowImportResult
		if err := json.Unmarshal(recorder.Body.Bytes(), &imported); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusCreated || imported.Workflow.ID == "" || imported.Workflow.ID == wf.ID || imported.Workflow.CreatedAt.IsZero() || imported.Workflow.Revision == "" {
			t.Fatalf("import did not create a new definition: %d %s", recorder.Code, recorder.Body.String())
		}
		importedIDs = append(importedIDs, imported.Workflow.ID)
		if !reflect.DeepEqual(imported.Workflow.Edges, envelope.Workflow.Edges) || !reflect.DeepEqual(workflowNodeData(imported.Workflow.Nodes[1].(map[string]any)), workflowNodeData(envelope.Workflow.Nodes[1].(map[string]any))) {
			t.Fatal("import changed graph references or Agent resources")
		}
		settings := workflowNodeData(imported.Workflow.Nodes[2].(map[string]any))
		delivery := resourceMap(settings["delivery"])
		dingtalk := resourceMap(delivery["dingtalk"])
		if resourceMap(delivery["session"])["mode"] != "none" || dingtalk["enabled"] != false || dingtalk["instance_id"] != "instance-example" || dingtalk["chat_id"] != "chat-example" {
			t.Fatal("import must disable delivery while preserving destination references")
		}
	}
	if importedIDs[0] == importedIDs[1] || len(s.workflowStore.workflows) != 3 || len(s.workflowStore.runs) != 0 {
		t.Fatal("re-import overwrote a definition or created execution")
	}
	after, _ := json.Marshal(wf)
	if !bytes.Equal(before, after) {
		t.Fatal("export or import mutated the original definition")
	}
	reloaded := &WorkflowStore{filePath: s.workflowStore.filePath}
	reloaded.loadFromFile()
	if len(reloaded.workflows) != 3 || reloaded.workflows[importedIDs[0]] == nil || reloaded.workflows[wf.ID] == nil {
		t.Fatal("import was not durably stored")
	}
}

func TestWorkflowPortabilityUnsavedExportAndPersistenceFailure(t *testing.T) {
	s := newWorkflowTestServer()
	s.engine = nil
	definition := contractWorkflow(contractNode("input", "input", map[string]any{"value": "draft content"}), contractNode("output", "output", nil))
	definition.Name = "unsaved definition"
	definition.Nodes[0].(map[string]any)["config"].(map[string]any)["headers"] = map[string]any{"Authorization": "test-bearer", "Accept": "application/json"}
	body, err := json.Marshal(map[string]any{"workflow": definition})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	s.handleExportWorkflowDefinition(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/canvas/workflows/export", bytes.NewReader(body)))
	if recorder.Code != 200 || !bytes.Contains(recorder.Body.Bytes(), []byte("unsaved definition")) || bytes.Contains(recorder.Body.Bytes(), []byte("test-bearer")) || len(s.workflowStore.workflows) != 0 {
		t.Fatalf("unsaved export persisted or exposed credentials: %d %s", recorder.Code, recorder.Body.String())
	}
	packageBytes := append([]byte(nil), recorder.Body.Bytes()...)
	// 原子替换的目标为目录，真实文件系统失败不能留下虚假的导入成功。
	s.workflowStore.filePath = t.TempDir()
	recorder = httptest.NewRecorder()
	s.handleImportWorkflow(recorder, portabilityUpload(t, "/api/v1/canvas/workflows/import", "draft.json", packageBytes))
	if recorder.Code != http.StatusInternalServerError || len(s.workflowStore.workflows) != 0 || len(s.workflowStore.runs) != 0 {
		t.Fatalf("failed persistence retained an import: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestWorkflowPortabilityDraftExportIgnoresLifecycleDatesAndReportsDistinctErrors(t *testing.T) {
	s := newWorkflowTestServer()
	s.engine = nil
	s.workflowStore.filePath = filepath.Join(t.TempDir(), "definitions.json")
	definition := contractWorkflow(contractNode("input", "input", map[string]any{"value": "{{input}}"}), contractNode("output", "output", nil))
	definition.ID, definition.Name = "", "Unsaved report"
	encoded, _ := json.Marshal(definition)
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	raw["created_at"], raw["updated_at"], raw["revision"] = "", "", "obsolete-revision"
	body, _ := json.Marshal(map[string]any{"workflow": raw})
	result := httptest.NewRecorder()
	s.handleExportWorkflowDefinition(result, httptest.NewRequest(http.MethodPost, "/export", bytes.NewReader(body)))
	var exported workflowPortableEnvelope
	if result.Code != 200 || json.Unmarshal(result.Body.Bytes(), &exported) != nil || exported.Format != "hexclaw-workflow" || exported.SchemaVersion != "1.0.0" || exported.Workflow == nil || len(exported.Workflow.Nodes) != 2 || exported.Workflow.Name != "Unsaved report" || exported.Workflow.Revision != "" {
		t.Fatalf("empty lifecycle dates rejected a valid unsaved definition: %d %s", result.Code, result.Body.String())
	}
	if len(s.workflowStore.workflows) != 0 || len(s.workflowStore.runs) != 0 {
		t.Fatal("draft export created persistent definitions or runs")
	}
	if _, err := os.Stat(s.workflowStore.filePath); !os.IsNotExist(err) {
		t.Fatal("draft export wrote a definitions file")
	}
	for _, test := range []struct {
		name, body, code string
		status           int
	}{
		{"bad JSON", `{"workflow":`, "WORKFLOW_EXPORT_INVALID_JSON", 400},
		{"missing definition", `{}`, "WORKFLOW_DEFINITION_REQUIRED", 400},
		{"invalid fields", `{"workflow":{"name":false,"nodes":[]}}`, "WORKFLOW_DEFINITION_INVALID", 400},
		{"no steps", `{"workflow":{"name":"Empty draft","nodes":[],"edges":[]}}`, "WORKFLOW_NODES_REQUIRED", 422},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			s.handleExportWorkflowDefinition(response, httptest.NewRequest(http.MethodPost, "/export", strings.NewReader(test.body)))
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) && !strings.Contains(response.Body.String(), `"code": "`+test.code+`"`) {
				t.Fatalf("wrong draft export error: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestWorkflowPortabilityImportDisablesNewDeliveryTargetsWithoutLosingOutputContract(t *testing.T) {
	wf := contractWorkflow(contractNode("input", "input", nil), contractNode("out", "output", map[string]any{
		"outputs":                map[string]any{"formats": []string{"pdf", "md", "html"}},
		"import_requires_review": true,
		"delivery": map[string]any{
			"session":   map[string]any{"mode": "workflow"},
			"dingtalk":  map[string]any{"enabled": true, "instance_id": "ding-id", "chat_id": "person"},
			"email":     map[string]any{"enabled": true, "instance_id": "mail-id", "to": "reader@example.test", "subject": "Report"},
			"directory": map[string]any{"enabled": true, "location": "desktop", "client_id": "desktop-id", "directory_ref": "folder-ref"},
		},
	}))
	before, _ := json.Marshal(wf)
	exported := portableWorkflow(wf, false)
	settings := workflowNodeData(exported.Workflow.Nodes[1].(map[string]any))
	for _, kind := range []string{"dingtalk", "email", "directory"} {
		if resourceMap(resourceMap(settings["delivery"])[kind])["enabled"] != true {
			t.Fatalf("export changed authored %s setting", kind)
		}
	}
	imported := portableWorkflow(exported.Workflow, true)
	settings = workflowNodeData(imported.Workflow.Nodes[1].(map[string]any))
	delivery := resourceMap(settings["delivery"])
	for _, kind := range []string{"dingtalk", "email", "directory"} {
		if resourceMap(delivery[kind])["enabled"] != false {
			t.Fatalf("import retained active %s delivery", kind)
		}
	}
	email, directory := resourceMap(delivery["email"]), resourceMap(delivery["directory"])
	if resourceMap(delivery["session"])["mode"] != "none" || email["instance_id"] != "mail-id" || email["to"] != "reader@example.test" || email["subject"] != "Report" || directory["client_id"] != "desktop-id" || directory["directory_ref"] != "folder-ref" || directory["location"] != "desktop" {
		t.Fatal("import disabled delivery by discarding configured destinations")
	}
	if !reflect.DeepEqual(resourceMap(settings["outputs"])["formats"], []any{"pdf", "md", "html"}) || settings["import_requires_review"] != true {
		t.Fatal("import discarded output formats or bypassed resource review")
	}
	found := false
	for _, issue := range imported.Issues {
		found = found || issue.Code == "DELIVERY_DISABLED"
	}
	if !found {
		t.Fatal("import did not report that delivery requires review")
	}
	after, _ := json.Marshal(wf)
	if !bytes.Equal(before, after) {
		t.Fatal("portable conversion mutated the original workflow")
	}
}

const portabilityCozeSample = `schema_version: 1.0.0
name: coze-text
description: text output example
mode: workflow
icon: plugin_icon/workflow.png
nodes:
  - id: "100001"
    type: start
    title: Start
    parameters:
      node_outputs:
        input:
          type: string
          required: true
  - id: "900001"
    type: end
    title: End
    parameters:
      terminatePlan: returnVariables
      node_inputs:
        - name: result
          input:
            type: string
            value:
              ref_node: "100001"
              path: input
edges:
  - source_node: "100001"
    target_node: "900001"
`

func TestWorkflowPortabilityCozeZIPAndYAMLPreservePreflightIssues(t *testing.T) {
	s := newWorkflowTestServer()
	s.engine = nil
	plain := s.parseWorkflowImport("coze.yaml", []byte(portabilityCozeSample))
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range map[string]string{"MANIFEST.yml": "name: coze-text\n", "workflow/coze-text.yaml": portabilityCozeSample} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(t.TempDir(), "coze.zip")
	if err := os.WriteFile(zipPath, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	archived := s.parseWorkflowImport("coze.zip", zipBytes)
	if plain.Format != "coze-yaml" || archived.Format != "coze-zip" || !plain.CanImport || !archived.CanImport {
		t.Fatalf("supported single string output was not converted: %+v %+v", plain, archived)
	}
	for _, result := range []workflowImportResult{plain, archived} {
		output := workflowNodeData(result.Workflow.Nodes[1].(map[string]any))
		if result.Workflow == nil || len(result.Workflow.Nodes) != 2 || len(result.Workflow.Edges) != 1 || resourceMap(output["output_object"])["result"] != "{{value}}" || output["input"] != "{{node.100001}}" {
			t.Fatalf("single output lost its variable name or source: %+v", result)
		}
	}
	if !reflect.DeepEqual(plain.Issues, archived.Issues) || !reflect.DeepEqual(plain.Workflow.Nodes, archived.Workflow.Nodes) || !reflect.DeepEqual(plain.Workflow.Edges, archived.Workflow.Edges) {
		t.Fatal("ZIP and YAML produced different executable definitions or issues")
	}
	recorder := httptest.NewRecorder()
	s.handleImportWorkflow(recorder, portabilityUpload(t, "/api/v1/canvas/workflows/import", "coze.zip", zipBytes))
	if recorder.Code != http.StatusCreated || len(s.workflowStore.workflows) != 1 || len(s.workflowStore.runs) != 0 {
		t.Fatal("import did not create only a definition")
	}
	var imported workflowImportResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	text := "line one: \"quoted\"\\path\n中文 second line"
	run, err := s.launchWorkflow(imported.Workflow, RunWorkflowRequest{Input: text}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunCompletion(t, s, run.ID)
	finished := getRunSnapshot(t, s, run.ID)
	var output map[string]string
	if err := json.Unmarshal([]byte(finished.Output), &output); err != nil || finished.Status != "completed" || len(output) != 1 || output["result"] != text {
		t.Fatalf("returnVariables did not preserve JSON escaping: %+v %v", finished, err)
	}
}

const portabilityCozeAgent = `schema_version: 1.0.0
name: migrated-agent
description: source settings need review
mode: workflow
nodes:
  - id: "100001"
    type: start
    title: Start
    parameters:
      node_outputs:
        input:
          type: string
  - id: "200001"
    type: llm
    title: Summarize
    parameters:
      llmParam:
        - name: prompt
          input: {type: string, value: 'Read {{input}}'}
        - name: systemPrompt
          input: {type: string, value: 'Original system instructions'}
        - name: temperature
          input: {type: float, value: 0}
        - name: modelName
          input: {type: string, value: 'Source model'}
      node_inputs:
        - name: input
          input:
            value: {ref_node: "100001", path: input}
      node_outputs:
        answer: {type: string}
  - id: "900001"
    type: end
    title: End
    parameters:
      terminatePlan: returnVariables
      node_inputs:
        - name: result
          input:
            type: string
            value: {ref_node: "200001", path: answer}
edges:
  - {source_node: "100001", target_node: "200001"}
  - {source_node: "200001", target_node: "900001"}
`

func TestWorkflowPortabilityCozeDraftRequiresExplicitAdaptation(t *testing.T) {
	s := newWorkflowTestServer()
	s.workflowStore.filePath = filepath.Join(t.TempDir(), "workflows.json")
	var calls int
	s.engine = &workflowContractEngine{process: func(_ context.Context, message *adapter.Message) (*adapter.Reply, error) {
		calls++
		if message.Metadata["pinned_agent"] != "reviewed-analyst" {
			t.Fatalf("wrong Agent binding: %+v", message.Metadata)
		}
		return &adapter.Reply{Content: "reviewed \"result\"\n中文"}, nil
	}}
	recorder := httptest.NewRecorder()
	s.handleImportWorkflow(recorder, portabilityUpload(t, "/api/v1/canvas/workflows/import", "agent.yaml", []byte(portabilityCozeAgent)))
	var imported workflowImportResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusCreated || !imported.CanImport || portabilityIssue(imported, "COZE_AGENT_MAPPING_REQUIRED") == nil || len(s.workflowStore.runs) != 0 || calls != 0 {
		t.Fatalf("source was not retained as an unexecuted draft: %d %s", recorder.Code, recorder.Body.String())
	}
	config := workflowNodeData(imported.Workflow.Nodes[1].(map[string]any))
	if config["import_requires_review"] != true || config["agent"] != "" || resourceMap(config["import_source"])["type"] != "llm" || !strings.Contains(stringValue(config["prompt"]), "Original system instructions") {
		t.Fatalf("source settings were dropped or silently activated: %+v", config)
	}
	if _, err := s.launchWorkflow(imported.Workflow, RunWorkflowRequest{Input: "input"}, nil); err == nil || len(s.workflowStore.runs) != 0 || calls != 0 {
		t.Fatal("an unreviewed migration reached execution or created a run")
	}
	config["import_requires_review"] = false
	if _, err := s.launchWorkflow(imported.Workflow, RunWorkflowRequest{Input: "input"}, nil); err == nil || len(s.workflowStore.runs) != 0 || calls != 0 {
		t.Fatal("clearing the flag without selecting an Agent was accepted")
	}
	s.agentRouter = agentrouter.New()
	if err := s.agentRouter.Register(agentrouter.AgentConfig{Name: "reviewed-analyst"}); err != nil {
		t.Fatal(err)
	}
	config["agent"] = "reviewed-analyst"
	run, err := s.launchWorkflow(imported.Workflow, RunWorkflowRequest{Input: "input"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunCompletion(t, s, run.ID)
	finished := getRunSnapshot(t, s, run.ID)
	var output map[string]string
	if err := json.Unmarshal([]byte(finished.Output), &output); err != nil || finished.Status != "completed" || output["result"] != "reviewed \"result\"\n中文" || calls != 1 {
		t.Fatalf("explicitly adapted step did not return its named JSON output: %+v %v", finished, err)
	}
}

func TestWorkflowPortabilityRejectsUnknownSemanticsAndPreservesMissingReferences(t *testing.T) {
	s := newWorkflowTestServer()
	s.engine = nil
	for _, scenario := range []struct {
		name, yaml, code string
	}{
		{"unknown node", strings.Replace(portabilityCozeSample, "type: start", "type: loop", 1), "UNSUPPORTED_COZE_NODE"},
		{"unknown schema", strings.Replace(portabilityCozeSample, "1.0.0", "9.0.0", 1), "UNSUPPORTED_COZE_SCHEMA"},
		{"branch port", portabilityCozeSample + "    source_port: true\n", "UNSUPPORTED_COZE_BRANCH"},
		{"node retry", strings.Replace(portabilityCozeSample, "terminatePlan: returnVariables", "settingOnError:\n        retryTimes: 1", 1), "UNSUPPORTED_COZE_ERROR_POLICY"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			result := s.parseWorkflowImport("workflow.yaml", []byte(scenario.yaml))
			if result.CanImport || portabilityIssue(result, scenario.code) == nil {
				t.Fatalf("unsupported semantics were accepted: %+v", result)
			}
		})
	}
	wf := contractWorkflow(contractNode("input", "input", nil), contractNode("agent", "agent", map[string]any{"agent": "not-installed", "resources": map[string]any{
		"skills": map[string]any{"mode": "override", "value": []string{"skill-unknown"}}, "knowledge": map[string]any{"mode": "override", "value": []string{"document-unknown"}}, "tools": map[string]any{"mode": "override", "value": []string{"tool-unknown"}},
	}}), contractNode("output", "output", nil))
	packageBytes, err := json.Marshal(portableWorkflow(wf, false))
	if err != nil {
		t.Fatal(err)
	}
	result := s.parseWorkflowImport("native.json", packageBytes)
	if !result.CanImport {
		t.Fatalf("missing references should remain editable in an imported draft: %+v", result)
	}
	if len(result.Issues) != 4 || workflowNodeData(result.Workflow.Nodes[1].(map[string]any))["agent"] != "not-installed" || len(s.workflowStore.workflows) != 0 || len(s.workflowStore.runs) != 0 {
		t.Fatalf("missing references were hidden, replaced, or executed: %+v", result)
	}
	if _, err := s.prepareWorkflowResources(result.Workflow); err == nil {
		t.Fatal("missing referenced resources unexpectedly passed the existing runtime preparation")
	}
}
