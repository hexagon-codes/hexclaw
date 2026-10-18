package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/library"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

func TestWorkflowResourcesFreezeAndResume(t *testing.T) {
	db, err := sqlitefixture.Memory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = migrate.Run(t.Context(), db, migrate.All); err != nil {
		t.Fatal(err)
	}
	s := newWorkflowTestServer()
	s.workflowStore.runsFilePath = filepath.Join(t.TempDir(), "runs.json")
	s.promptStore = library.NewPromptStore(db)
	prompt := &library.Prompt{ID: "template", Title: "summary", BodyMD: "FROZEN_TEMPLATE $ARGUMENTS", Enabled: true}
	if _, err = s.promptStore.Upsert(t.Context(), prompt); err != nil {
		t.Fatal(err)
	}
	s.agentRouter = agentrouter.New()
	if err = s.agentRouter.Register(agentrouter.AgentConfig{Name: "analyst", Model: "model-v1", SystemPrompt: "frozen base", Metadata: map[string]string{"workflow_prompts": `["template"]`}}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var messages []adapter.Message
	fail := true
	s.engine = &workflowContractEngine{process: func(_ context.Context, msg *adapter.Message) (*adapter.Reply, error) {
		mu.Lock()
		defer mu.Unlock()
		messages = append(messages, *msg)
		if strings.Contains(msg.Content, "second") && fail {
			fail = false
			return nil, &llm.ProviderError{StatusCode: 400}
		}
		return &adapter.Reply{Content: "result"}, nil
	}}
	wf := contractWorkflow(contractNode("first", "agent", map[string]any{"agent": "analyst", "prompt": "first"}), contractNode("second", "agent", map[string]any{"agent": "analyst", "prompt": "second"}))
	s.workflowStore.workflows[wf.ID] = wf
	revision := workflowRevision(wf)
	run, err := s.launchWorkflow(wf, RunWorkflowRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunCompletion(t, s, run.ID)
	prior := getRunSnapshot(t, s, run.ID)
	if prior.Status != "failed" || prior.Definition.Revision != revision || workflowRevision(wf) != revision || len(prior.ResourceSnapshots) != 2 {
		t.Fatalf("definition or snapshot drift: %+v", prior)
	}
	prompt.BodyMD = "CHANGED_TEMPLATE"
	if _, err = s.promptStore.Upsert(t.Context(), prompt); err != nil {
		t.Fatal(err)
	}
	if err = s.agentRouter.Unregister("analyst"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/resume", nil)
	req.SetPathValue("id", run.ID)
	w := httptest.NewRecorder()
	s.handleResumeWorkflowRun(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", w.Code, w.Body.String())
	}
	var resumed WorkflowRun
	if err = json.Unmarshal(w.Body.Bytes(), &resumed); err != nil {
		t.Fatal(err)
	}
	waitForRunCompletion(t, s, resumed.ID)
	final := getRunSnapshot(t, s, resumed.ID)
	reloaded := &WorkflowStore{runs: map[string]*WorkflowRun{}, runsFilePath: s.workflowStore.runsFilePath}
	reloaded.loadRunsFromFile()
	if saved := reloaded.runs[resumed.ID]; saved == nil || saved.ResourceSnapshots["second"]["main"] == nil || saved.Definition.Revision != revision {
		t.Fatal("resource snapshot or original definition lost after durable reload")
	}
	mu.Lock()
	defer mu.Unlock()
	if final.Status != "completed" || len(messages) != 3 || !strings.Contains(messages[2].Content, "FROZEN_TEMPLATE") || strings.Contains(messages[2].Content, "CHANGED_TEMPLATE") || messages[2].Metadata["model"] != "model-v1" {
		t.Fatalf("resources were re-resolved: %+v / %+v", final, messages)
	}
	for _, policy := range []struct {
		mode  string
		count int
	}{{"inherit", 1}, {"override", 0}, {"none", 0}} {
		if err = s.agentRouter.Register(agentrouter.AgentConfig{Name: "agent-" + policy.mode, Metadata: map[string]string{"workflow_prompts": `["template"]`}}); err != nil {
			t.Fatal(err)
		}
		resolved, err := s.resolveWorkflowResources(t.Context(), map[string]any{"resources": map[string]any{"prompts": map[string]any{"mode": policy.mode}}}, "agent-"+policy.mode)
		if err != nil || len(resolved.Prompts) != policy.count {
			t.Fatalf("prompt %s: %+v %v", policy.mode, resolved, err)
		}
	}
}

func TestWorkflowResourcesHTTPPersistAndReadback(t *testing.T) {
	s := newWorkflowTestServer()
	s.workflowStore.filePath = filepath.Join(t.TempDir(), "workflows.json")
	s.engine = &workflowContractEngine{process: func(context.Context, *adapter.Message) (*adapter.Reply, error) {
		return &adapter.Reply{Content: `{"summary":"verified"}`}, nil
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/canvas/workflows", s.handleSaveWorkflow)
	mux.HandleFunc("GET /api/v1/canvas/workflows", s.handleListWorkflows)
	mux.HandleFunc("POST /api/v1/canvas/workflows/{id}/run", s.handleRunWorkflow)
	mux.HandleFunc("GET /api/v1/canvas/runs/{id}", s.handleGetWorkflowRun)
	server := httptest.NewServer(mux)
	defer server.Close()
	body := `{"id":"wf-http-resources","name":"Resource contract","data":{"run_settings":{"timeout":{"mode":"custom","minutes":1}}},"nodes":[{"id":"agent","type":"agent","config":{"prompt":"Summarize","resources":{"prompts":{"mode":"none","value":[]},"tools":{"mode":"none","value":[]}},"memory":{"retrieval":{"mode":"none"},"writeback":{"mode":"suggest"}},"output":{"kind":"structured","fields":"summary: text"}}}],"edges":[]}`
	response, err := http.Post(server.URL+"/api/v1/canvas/workflows", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var saved WorkflowData
	err = json.NewDecoder(response.Body).Decode(&saved)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("save: %d %v", response.StatusCode, err)
	}
	reloaded := &WorkflowStore{workflows: map[string]*WorkflowData{}, filePath: s.workflowStore.filePath}
	reloaded.loadFromFile()
	if stored := reloaded.workflows[saved.ID]; stored == nil || workflowRevision(stored) != saved.Revision || stored.Data == nil {
		t.Fatal("definition resources lost in durable storage")
	}
	bad := strings.Replace(body, `"minutes":1`, `"minutes":-1`, 1)
	badResponse, badErr := http.Post(server.URL+"/api/v1/canvas/workflows", "application/json", strings.NewReader(bad))
	if badErr != nil {
		t.Fatal(badErr)
	}
	badResponse.Body.Close()
	if badResponse.StatusCode != 400 || s.workflowStore.workflows[saved.ID].Revision != saved.Revision {
		t.Fatal("invalid timeout overwrote the saved definition")
	}
	response, err = http.Post(server.URL+"/api/v1/canvas/workflows/"+saved.ID+"/run", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var run WorkflowRun
	err = json.NewDecoder(response.Body).Decode(&run)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("run: %d %v", response.StatusCode, err)
	}
	waitForRunCompletion(t, s, run.ID)
	response, err = http.Get(server.URL + "/api/v1/canvas/runs/" + run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var final WorkflowRun
	if err = json.NewDecoder(response.Body).Decode(&final); err != nil {
		t.Fatal(err)
	}
	receipt := nodeStatusOf(final, "agent").ResourceReceipts["main"]
	if final.Status != "completed" || receipt == nil || receipt.Effective.Options.ToolsMode != "none" || receipt.Effective.Options.MemoryWriteback != "suggest" || len(receipt.ToolCalls) != 0 || final.ResourceSnapshots["agent"]["main"] == nil {
		t.Fatalf("readback omitted resources or invented execution: %+v", final)
	}
}

func TestWorkflowResourcesOutputContract(t *testing.T) {
	content, err := messagecontent.New(messagecontent.ProducerWorkflow, "en", "report", []messagecontent.AttachmentRef{{AssetID: "existing-report", MIME: "application/pdf", Digest: "sha256:fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, kind, fields, body string
		artifact                 bool
		want                     string
	}{
		{"structured-valid", "structured", "summary: text\nitems: list", `{"summary":"ok","items":[]}`, false, "completed"},
		{"structured-missing", "structured", "summary: text", `{}`, false, "failed"},
		{"structured-wrong-type", "structured", "items: list", `{"items":"wrong"}`, false, "failed"},
		{"artifact-invented-text", "artifact", "", "File created at fake.pdf", false, "failed"},
		{"artifact-existing-reference", "artifact", "", "report", true, "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newWorkflowTestServer()
			s.engine = &workflowContractEngine{process: func(context.Context, *adapter.Message) (*adapter.Reply, error) {
				reply := &adapter.Reply{Content: tc.body}
				if tc.artifact {
					reply.MessageContent = &content
				}
				return reply, nil
			}}
			mapping := "{{value}}"
			if tc.artifact {
				mapping = "Mapped: {{value}}"
			}
			wf := contractWorkflow(contractNode("agent", "agent", map[string]any{"prompt": "report", "output": map[string]any{"kind": tc.kind, "fields": tc.fields, "artifactKinds": []string{"file"}}}), contractNode("out", "output", map[string]any{"output_template": mapping}))
			run, err := s.launchWorkflow(wf, RunWorkflowRequest{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			waitForRunCompletion(t, s, run.ID)
			final := getRunSnapshot(t, s, run.ID)
			if final.Status != tc.want {
				t.Fatalf("output status: %+v", final)
			}
			if tc.artifact && (final.MessageContent == nil || len(final.MessageContent.Attachments) != 1 || final.MessageContent.Attachments[0].AssetID != "existing-report") {
				t.Fatal("output mapping dropped or duplicated the selected artifact")
			}
			receipt := nodeStatusOf(final, "agent").ResourceReceipts["main"]
			if receipt == nil || (tc.want == "failed") != (receipt.OutputError != "") || (tc.artifact && len(receipt.Artifacts) != 1) {
				t.Fatalf("receipt: %+v", receipt)
			}
		})
	}
}

func TestWorkflowResourcesSelectedInputAndTimeout(t *testing.T) {
	s := newWorkflowTestServer()
	wf := contractWorkflow(contractNode("input", "input", map[string]any{"value": "{{input}}", "input_selection": map[string]any{"kind": "structured", "field": "items.0.name"}}), contractNode("out", "output", map[string]any{"output_template": "Selected: {{value}}"}))
	run, err := s.launchWorkflow(wf, RunWorkflowRequest{Input: `{"items":[{"name":"chosen"}],"name":"wrong"}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunCompletion(t, s, run.ID)
	if final := getRunSnapshot(t, s, run.ID); final.Status != "completed" || final.Output != "Selected: chosen" {
		t.Fatalf("selected mapping: %+v", final)
	}
	artifactWorkflow := contractWorkflow(contractNode("input", "input", map[string]any{"value": "{{input}}", "input_selection": map[string]any{"kind": "artifact"}}), contractNode("out", "output", map[string]any{"output_template": "Mapped: {{value}}"}))
	artifactRun, err := s.launchWorkflow(artifactWorkflow, RunWorkflowRequest{Input: `[{"asset_id":"existing-image","mime":"image/png","digest":"sha256:fixture"}]`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunCompletion(t, s, artifactRun.ID)
	artifactResult := getRunSnapshot(t, s, artifactRun.ID)
	if artifactResult.Status != "completed" || artifactResult.MessageContent == nil || len(artifactResult.MessageContent.Attachments) != 1 || artifactResult.MessageContent.Attachments[0].AssetID != "existing-image" {
		t.Fatal("input artifact reference lost across output mapping")
	}
	wf.Data = map[string]any{"run_settings": map[string]any{"timeout": map[string]any{"mode": "custom", "minutes": -1}}}
	if _, err := s.launchWorkflow(wf, RunWorkflowRequest{}, nil); err == nil {
		t.Fatal("invalid timeout accepted")
	}
	deadlineObserved := make(chan time.Duration, 1)
	s.engine = &workflowContractEngine{process: func(ctx context.Context, _ *adapter.Message) (*adapter.Reply, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return nil, context.DeadlineExceeded
		}
		deadlineObserved <- time.Until(deadline)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	wf = contractWorkflow(contractNode("agent", "agent", map[string]any{"prompt": "wait"}))
	wf.Data = map[string]any{"run_settings": map[string]any{"timeout": map[string]any{"mode": "custom", "minutes": 0.001}}}
	run, err = s.launchWorkflow(wf, RunWorkflowRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunCompletion(t, s, run.ID)
	select {
	case remaining := <-deadlineObserved:
		if remaining <= 0 || remaining > time.Second {
			t.Fatalf("timeout not applied: %s", remaining)
		}
	default:
		t.Fatal("engine did not receive a deadline")
	}
	if getRunSnapshot(t, s, run.ID).Status == "completed" {
		t.Fatal("timed-out invocation completed")
	}
}
