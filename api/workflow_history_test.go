package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkflowHistoryDeletePersistsAndKeepsIdempotency(t *testing.T) {
	s := newWorkflowTestServer()
	dir := t.TempDir()
	ws := s.workflowStore
	ws.filePath, ws.runsFilePath = filepath.Join(dir, "workflows.json"), filepath.Join(dir, "runs.json")
	wf := contractWorkflow(contractNode("input", "input", nil), contractNode("output", "output", nil))
	ws.workflows[wf.ID] = wf
	identity := sha256.Sum256([]byte(`["wf-contract","owner","","","","event-1"]`))
	run := &WorkflowRun{ID: "run-ended", WorkflowID: wf.ID, Status: "completed", Output: "confirmed-result", TriggerKey: hex.EncodeToString(identity[:]), StartedAt: time.Now(), FinishedAt: time.Now(), Definition: wf}
	if err := ws.persistToFile(); err != nil {
		t.Fatal(err)
	}
	if err := ws.addRun(run); err != nil {
		t.Fatal(err)
	}
	definition, _ := os.ReadFile(ws.filePath)
	artifact := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(artifact, []byte("existing-artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/v1/canvas/runs/{id}", s.handleDeleteWorkflowRun)
	mux.HandleFunc("GET /api/v1/canvas/runs/{id}", s.handleGetWorkflowRun)
	mux.HandleFunc("GET /api/v1/canvas/runs", s.handleListWorkflowRuns)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/v1/canvas/runs/"+run.ID, nil)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatalf("delete status: %d", response.StatusCode)
	}
	response, err = server.Client().Get(server.URL + "/api/v1/canvas/runs/" + run.ID)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatalf("deleted detail visible: %d", response.StatusCode)
	}
	response, err = server.Client().Get(server.URL + "/api/v1/canvas/runs")
	if err != nil {
		t.Fatal(err)
	}
	var history struct {
		Runs []WorkflowRun `json:"runs"`
	}
	err = json.NewDecoder(response.Body).Decode(&history)
	response.Body.Close()
	if err != nil || len(history.Runs) != 0 {
		t.Fatalf("deleted history visible: %+v %v", history, err)
	}
	restarted := newWorkflowTestServer()
	restarted.workflowStore.filePath, restarted.workflowStore.runsFilePath = ws.filePath, ws.runsFilePath
	restarted.workflowStore.loadFromFile()
	restarted.workflowStore.loadRunsFromFile()
	if !restarted.workflowStore.runs[run.ID].HistoryDeleted {
		t.Fatal("deletion did not survive store restart")
	}
	listed := httptest.NewRecorder()
	restarted.handleListWorkflowRuns(listed, httptest.NewRequest(http.MethodGet, "/api/v1/canvas/runs", nil))
	if err = json.Unmarshal(listed.Body.Bytes(), &history); err != nil || len(history.Runs) != 0 {
		t.Fatalf("restart restored deleted history: %s", listed.Body.String())
	}
	duplicate, err := restarted.launchWorkflow(wf, RunWorkflowRequest{UserID: "owner", IdempotencyKey: "event-1"}, nil)
	if err != nil || duplicate.ID != run.ID || duplicate.Output != "confirmed-result" || len(restarted.workflowStore.runs) != 1 {
		t.Fatalf("deleted trigger replayed: %+v %v", duplicate, err)
	}
	after, _ := os.ReadFile(ws.filePath)
	file, _ := os.ReadFile(artifact)
	if string(after) != string(definition) || string(file) != "existing-artifact" {
		t.Fatal("deleting history changed definition or artifact")
	}
	for _, operation := range []struct {
		method  string
		handler http.HandlerFunc
	}{{http.MethodDelete, restarted.handleDeleteWorkflowRun}, {http.MethodPost, restarted.handleResumeWorkflowRun}} {
		req := httptest.NewRequest(operation.method, "/api/v1/canvas/runs/"+run.ID, nil)
		req.SetPathValue("id", run.ID)
		w := httptest.NewRecorder()
		operation.handler(w, req)
		if w.Code != 404 {
			t.Fatalf("deleted record operation: %d", w.Code)
		}
	}
}

func TestWorkflowHistoryDeleteRejectsActiveAndUnknown(t *testing.T) {
	for _, test := range []struct {
		name, status string
		node         WorkflowNodeRun
		active       bool
	}{
		{name: "running", status: "running"}, {name: "pending", status: "pending"}, {name: "unknown", status: "outcome_unknown"},
		{name: "unconfirmed-node", status: "failed", node: WorkflowNodeRun{OutcomeUnknown: true}},
		{name: "unconfirmed-branch", status: "cancelled", node: WorkflowNodeRun{Branches: []WorkflowBranchRun{{OutcomeUnknown: true}}}},
		{name: "still-finishing", status: "completed", active: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newWorkflowTestServer()
			s.workflowStore.runsFilePath = filepath.Join(t.TempDir(), "runs.json")
			run := &WorkflowRun{ID: "run-1", Status: test.status, NodeResults: []WorkflowNodeRun{test.node}}
			if err := s.workflowStore.addRun(run); err != nil {
				t.Fatal(err)
			}
			if test.active {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				_ = ctx
				s.workflowStore.cancels = map[string]context.CancelFunc{run.ID: cancel}
			}
			req := httptest.NewRequest(http.MethodDelete, "/runs/run-1", nil)
			req.SetPathValue("id", run.ID)
			w := httptest.NewRecorder()
			s.handleDeleteWorkflowRun(w, req)
			if w.Code != 409 || run.HistoryDeleted {
				t.Fatalf("active or unknown record deleted: %d %+v", w.Code, run)
			}
		})
	}
}

func TestWorkflowHistoryDeleteWriteFailureKeepsRecord(t *testing.T) {
	s := newWorkflowTestServer()
	dir := t.TempDir()
	ws := s.workflowStore
	ws.runsFilePath = filepath.Join(dir, "runs.json")
	run := &WorkflowRun{ID: "run-1", Status: "failed", Error: "confirmed failure"}
	if err := ws.addRun(run); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(ws.runsFilePath)
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0o750); err != nil {
		t.Fatal(err)
	}
	ws.runsFilePath = blocked
	req := httptest.NewRequest(http.MethodDelete, "/runs/run-1", nil)
	req.SetPathValue("id", run.ID)
	w := httptest.NewRecorder()
	s.handleDeleteWorkflowRun(w, req)
	after, _ := os.ReadFile(filepath.Join(dir, "runs.json"))
	if w.Code != 500 || run.HistoryDeleted || ws.runs[run.ID].Error != "confirmed failure" || string(after) != string(before) {
		t.Fatalf("write failure lost record: %d %+v", w.Code, run)
	}
}
