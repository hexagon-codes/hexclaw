package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/cron"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
	"github.com/hexagon-codes/hexclaw/webhook"
)

// 仅替换不可控模型边界；请求、执行图、持久化和查询均使用真实实现。
type workflowContractEngine struct {
	workflowTestEngine
	process func(context.Context, *adapter.Message) (*adapter.Reply, error)
}

type workflowOpaqueDeadline struct{}

func (workflowOpaqueDeadline) Error() string { return "Model response unavailable" }
func (workflowOpaqueDeadline) Unwrap() error { return context.DeadlineExceeded }

func (e *workflowContractEngine) Process(ctx context.Context, msg *adapter.Message) (*adapter.Reply, error) {
	return e.process(ctx, msg)
}

func contractWorkflow(nodes ...map[string]any) *WorkflowData {
	wf := &WorkflowData{ID: "wf-contract", Name: "contract"}
	for i, node := range nodes {
		wf.Nodes = append(wf.Nodes, node)
		if i > 0 {
			wf.Edges = append(wf.Edges, map[string]any{"from": nodes[i-1]["id"], "to": node["id"]})
		}
	}
	return wf
}
func contractNode(id, kind string, config map[string]any) map[string]any {
	return map[string]any{"id": id, "type": kind, "label": id, "config": config}
}

func TestWorkflowContractDirectPredecessorAndFinalOutput(t *testing.T) {
	s := newWorkflowTestServer()
	var inputs []string
	s.engine = &workflowContractEngine{process: func(_ context.Context, msg *adapter.Message) (*adapter.Reply, error) {
		inputs = append(inputs, msg.Content)
		if len(inputs) == 1 {
			return &adapter.Reply{Content: `{"answer":"B"}`}, nil
		}
		return &adapter.Reply{Content: "final-result"}, nil
	}}
	wf := contractWorkflow(contractNode("start", "input", map[string]any{"value": "A"}), contractNode("first", "agent", map[string]any{"prompt": "Read {{previous}}"}), contractNode("early", "output", nil), contractNode("last", "agent", map[string]any{"prompt": "Use {{node.first.answer}}; previous={{previous}}"}))
	run, err := s.launchWorkflow(wf, RunWorkflowRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunCompletion(t, s, run.ID)
	final := getRunSnapshot(t, s, run.ID)
	if final.Status != "completed" || final.Output != "final-result" {
		t.Fatalf("final output: %+v", final)
	}
	if len(inputs) != 2 || inputs[0] != "Read A" || inputs[1] != `Use B; previous={"answer":"B"}` {
		t.Fatalf("explicit mapping duplicated or mixed history: %#v", inputs)
	}
}

func TestWorkflowContractParallelFailureHasBranchReceipts(t *testing.T) {
	s := newWorkflowTestServer()
	s.agentRouter = agentrouter.New()
	for _, name := range []string{"good", "broken"} {
		if err := s.agentRouter.Register(agentrouter.AgentConfig{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	s.engine = &workflowContractEngine{process: func(_ context.Context, msg *adapter.Message) (*adapter.Reply, error) {
		if msg.Metadata["pinned_agent"] == "broken" {
			return nil, &llm.ProviderError{StatusCode: 400}
		}
		return &adapter.Reply{Content: "valid-result"}, nil
	}}
	wf := contractWorkflow(contractNode("start", "input", map[string]any{"value": "data"}), contractNode("group", "parallel", map[string]any{"branches": []any{map[string]any{"agent": "good", "task": "Analyze"}, map[string]any{"agent": "broken", "task": "Review"}}}), contractNode("out", "output", nil))
	run, err := s.launchWorkflow(wf, RunWorkflowRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunCompletion(t, s, run.ID)
	final := getRunSnapshot(t, s, run.ID)
	group := nodeStatusOf(final, "group")
	if final.Status != "failed" || group == nil || group.Status != "failed" || len(group.Branches) != 2 {
		t.Fatalf("parallel failure: %+v", final)
	}
	if group.Branches[0].Status != "completed" || group.Branches[0].Output != "valid-result" || group.Branches[1].Status != "failed" {
		t.Fatalf("branch receipts: %+v", group.Branches)
	}
	if nodeStatusOf(final, "out").Status == "completed" {
		t.Fatal("downstream output ran after required branch failure")
	}
}

func TestWorkflowContractSaveFailurePreservesDefinition(t *testing.T) {
	s := newWorkflowTestServer()
	wf := contractWorkflow(contractNode("start", "input", nil))
	s.workflowStore.workflows[wf.ID] = wf
	// 目标为目录使原子替换真实失败，不依赖宿主权限或 mock 文件系统。
	s.workflowStore.filePath = t.TempDir()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/canvas/workflows", strings.NewReader(`{"id":"wf-contract","name":"changed","nodes":[],"edges":[]}`))
	w := httptest.NewRecorder()
	s.handleSaveWorkflow(w, req)
	if w.Code != 500 || s.workflowStore.workflows[wf.ID].Name != "contract" {
		t.Fatalf("false save or changed memory: %d %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/canvas/workflows/wf-contract", nil)
	req.SetPathValue("id", wf.ID)
	w = httptest.NewRecorder()
	s.handleDeleteWorkflow(w, req)
	if w.Code != 500 || s.workflowStore.workflows[wf.ID] == nil {
		t.Fatalf("false delete: %d %s", w.Code, w.Body.String())
	}
}

func TestWorkflowContractResumeFreezesDefinitionAndContext(t *testing.T) {
	s := newWorkflowTestServer()
	var mu sync.Mutex
	var calls []adapter.Message
	fail := true
	s.engine = &workflowContractEngine{process: func(_ context.Context, msg *adapter.Message) (*adapter.Reply, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, *msg)
		if strings.HasPrefix(msg.Content, "second-v1") && fail {
			fail = false
			return nil, &llm.ProviderError{StatusCode: 400}
		}
		return &adapter.Reply{Content: "confirmed-result"}, nil
	}}
	wf := contractWorkflow(contractNode("start", "input", nil), contractNode("first", "agent", map[string]any{"prompt": "first-v1"}), contractNode("second", "agent", map[string]any{"prompt": "second-v1"}), contractNode("out", "output", nil))
	s.workflowStore.workflows[wf.ID] = wf
	run, err := s.launchWorkflow(wf, RunWorkflowRequest{Input: "original", UserID: "owner", Platform: "desktop", ChatID: "chat", Metadata: map[string]string{"locale": "zh-CN"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunCompletion(t, s, run.ID)
	// 新定义不能改变旧运行或它的续接。
	s.workflowStore.workflows[wf.ID] = contractWorkflow(contractNode("start", "input", nil), contractNode("second", "agent", map[string]any{"prompt": "second-v2"}))
	req := httptest.NewRequest(http.MethodPost, "/resume", nil)
	req.SetPathValue("id", run.ID)
	w := httptest.NewRecorder()
	s.handleResumeWorkflowRun(w, req)
	if w.Code != 200 {
		t.Fatalf("resume: %d %s", w.Code, w.Body.String())
	}
	var resumed WorkflowRun
	if err = json.Unmarshal(w.Body.Bytes(), &resumed); err != nil {
		t.Fatal(err)
	}
	waitForRunCompletion(t, s, resumed.ID)
	final := getRunSnapshot(t, s, resumed.ID)
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 || !strings.HasPrefix(calls[2].Content, "second-v1") || calls[2].UserID != "owner" || calls[2].ChatID != "chat" || calls[2].Metadata["locale"] != "zh-CN" {
		t.Fatalf("replayed completed work or lost context: %+v", calls)
	}
	if final.Status != "completed" || final.Definition.Revision != run.Definition.Revision {
		t.Fatalf("mixed definition: %+v", final)
	}
}

func TestWorkflowContractCheckpointCancellationAndRestart(t *testing.T) {
	s := newWorkflowTestServer()
	s.workflowStore.runsFilePath = filepath.Join(t.TempDir(), "runs.json")
	entered := make(chan struct{})
	s.engine = &workflowContractEngine{process: func(ctx context.Context, _ *adapter.Message) (*adapter.Reply, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	wf := contractWorkflow(contractNode("start", "input", map[string]any{"value": "saved-input"}), contractNode("agent", "agent", map[string]any{"prompt": "wait"}))
	run, err := s.launchWorkflow(wf, RunWorkflowRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Agent never started")
	}
	// 独立读盘证明开始外部调用前已持久化输入、运行上下文与节点状态。
	bytes, err := os.ReadFile(s.workflowStore.runsFilePath)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]*WorkflowRun
	if err = json.Unmarshal(bytes, &saved); err != nil {
		t.Fatal(err)
	}
	if saved[run.ID].Checkpoint.Outputs["start"] != "saved-input" || nodeStatusOf(*saved[run.ID], "agent").Status != "running" {
		t.Fatalf("missing durable checkpoint: %+v", saved[run.ID])
	}
	restarted := &WorkflowStore{runs: saved}
	restarted.reconcileInterruptedRuns()
	if restarted.runs[run.ID].Status != "outcome_unknown" {
		t.Fatal("restart allowed blind external replay")
	}
	req := httptest.NewRequest(http.MethodPost, "/cancel", nil)
	req.SetPathValue("id", run.ID)
	w := httptest.NewRecorder()
	s.handleCancelWorkflowRun(w, req)
	if w.Code != 200 {
		t.Fatalf("cancel: %d", w.Code)
	}
	waitForRunCompletion(t, s, run.ID)
	if getRunSnapshot(t, s, run.ID).Status != "outcome_unknown" {
		t.Fatal("unconfirmed external result was hidden")
	}
}

func TestWorkflowContractIdempotentHTTPRunAndHistory(t *testing.T) {
	s := newWorkflowTestServer()
	wf := contractWorkflow(contractNode("start", "input", map[string]any{"value": "answer"}), contractNode("out", "output", nil))
	s.workflowStore.workflows[wf.ID] = wf
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/canvas/workflows/{id}/run", s.handleRunWorkflow)
	mux.HandleFunc("GET /api/v1/canvas/workflows/{id}/runs", s.handleListWorkflowRuns)
	server := httptest.NewServer(mux)
	defer server.Close()
	var ids []string
	for range 2 {
		response, err := http.Post(server.URL+"/api/v1/canvas/workflows/"+wf.ID+"/run", "application/json", strings.NewReader(`{"user_id":"owner","idempotency_key":"event-1"}`))
		if err != nil {
			t.Fatal(err)
		}
		var run WorkflowRun
		err = json.NewDecoder(response.Body).Decode(&run)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
	}
	if ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("duplicate event created new execution: %v", ids)
	}
	waitForRunCompletion(t, s, ids[0])
	response, err := http.Get(server.URL + "/api/v1/canvas/workflows/" + wf.ID + "/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var history struct {
		Runs []WorkflowRun `json:"runs"`
	}
	if err = json.NewDecoder(response.Body).Decode(&history); err != nil {
		t.Fatal(err)
	}
	if len(history.Runs) != 1 || history.Runs[0].Status != "completed" || history.Runs[0].Output != "answer" {
		t.Fatalf("actual history/result: %+v", history)
	}
}

func TestWorkflowContractTriggerBindingsUsePublicRoutesAndDurableWorkflow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
	mgr := webhook.NewManager(db)
	if err = mgr.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.SetWebhookManager(mgr)
	scheduler := cron.NewScheduler(db, nil, nil)
	if err = scheduler.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.SetCronScheduler(scheduler)
	s.SetDesktopAPIToken("workflow-contract-fixture")
	wf := contractWorkflow(contractNode("start", "input", map[string]any{"value": "{{input}}"}), contractNode("out", "output", nil))
	s.workflowStore.workflows[wf.ID] = wf
	server := httptest.NewServer(s.routes())
	defer server.Close()
	request := func(method, path, body string, headers map[string]string, expected ...int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer workflow-contract-fixture")
		req.Header.Set("Content-Type", "application/json")
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var value json.RawMessage
		if err = json.NewDecoder(response.Body).Decode(&value); err != nil {
			t.Fatal(err)
		}
		status := 200
		if len(expected) > 0 {
			status = expected[0]
		}
		if response.StatusCode != status {
			t.Fatalf("%s %s: %d %s", method, path, response.StatusCode, value)
		}
		return value
	}
	base := "/api/v1/canvas/workflows/" + wf.ID
	var job cron.Job
	if err = json.Unmarshal(request("POST", base+"/triggers", `{"kind":"cron","name":"scheduled","schedule":"0 0 1 1 *","user_id":"owner","input":"scheduled-input"}`, nil), &job); err != nil {
		t.Fatal(err)
	}
	if err = scheduler.TriggerJobForOwner(t.Context(), job.ID, job.UserID); err != nil {
		t.Fatal(err)
	}
	waitCount := func(count int) {
		t.Helper()
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			s.workflowStore.mu.RLock()
			complete := 0
			for _, run := range s.workflowStore.runs {
				if run.Status == "completed" {
					complete++
				}
			}
			s.workflowStore.mu.RUnlock()
			if complete == count {
				return
			}
			select {
			case <-deadline.C:
				t.Fatalf("wanted %d completed runs, got %d", count, complete)
			case <-ticker.C:
			}
		}
	}
	waitCount(1)
	const secret = "workflow-signing-fixture"
	request("POST", base+"/triggers", `{"kind":"webhook","name":"workflow-event","user_id":"owner","secret":"`+secret+`"}`, nil)
	wh, ok := mgr.Get("workflow-event")
	if !ok || wh.WorkflowID != wf.ID || wh.Enabled {
		t.Fatalf("binding bypassed normal setup: %+v", wh)
	}
	request("PATCH", "/api/v1/webhooks/workflow-event", `{"enabled":true,"user_id":"owner"}`, nil)
	body := `{"source":"event-input"}`
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	headers := map[string]string{"X-Webhook-Signature": "sha256=" + hex.EncodeToString(mac.Sum(nil)), "X-HexClaw-Event-ID": "one-event"}
	for range 2 {
		request("POST", "/api/v1/webhooks/workflow-event", body, headers)
	}
	waitCount(2)
	// 同一事件同步核对终态，证明重复投递只复用已有 Run。
	if err = s.runWorkflowWebhook(t.Context(), &webhook.Event{WebhookID: wh.ID, WorkflowID: wf.ID, UserID: wh.UserID, EventID: "one-event", Payload: map[string]any{"source": "event-input"}}, ""); err != nil {
		t.Fatal(err)
	}
	var history struct {
		Runs []WorkflowRun `json:"runs"`
	}
	json.Unmarshal(request("GET", base+"/runs", "", nil), &history)
	if len(history.Runs) != 2 {
		t.Fatalf("duplicate trigger executions: %+v", history.Runs)
	}
	outputs := map[string]bool{}
	for _, run := range history.Runs {
		outputs[run.Output] = true
		if run.Definition == nil || run.Definition.Revision == "" {
			t.Fatal("trigger omitted frozen definition")
		}
	}
	if !outputs["scheduled-input"] || !outputs[body] {
		t.Fatalf("trigger input/result lost: %+v", outputs)
	}
	// 同名普通任务不能被绑定吸收；重复保存更新输入，不创建第二个绑定。
	legacy := &cron.Job{ID: "ordinary-job", Name: "ordinary", UserID: job.UserID, Schedule: "0 0 1 1 *", Spec: &cron.JobSpec{Runtime: "agent"}}
	if err = scheduler.AddJob(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	var updated cron.Job
	request("POST", base+"/triggers", `{"kind":"cron","name":"ordinary","schedule":"0 0 1 1 *","user_id":"owner","input":"wrong-input"}`, nil, 400)
	json.Unmarshal(request("POST", base+"/triggers", `{"kind":"cron","name":"scheduled","schedule":"0 0 1 1 *","user_id":"owner","input":"updated-input"}`, nil), &updated)
	if updated.ID != job.ID || updated.Spec.Inputs["input"] != "updated-input" {
		t.Fatalf("binding update lost identity/input: %+v", updated)
	}
	if err = scheduler.PauseJobForOwner(t.Context(), job.ID, job.UserID); err != nil {
		t.Fatal(err)
	}
	scheduler = cron.NewScheduler(db, nil, nil)
	if err = scheduler.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.SetCronScheduler(scheduler)
	json.Unmarshal(request("POST", base+"/triggers", `{"kind":"cron","name":"scheduled","schedule":"0 0 1 1 *","user_id":"owner","input":"updated-input"}`, nil), &updated)
	if updated.Status != cron.StatusPaused {
		t.Fatal("repeated binding resumed a paused job")
	}
	request("DELETE", base+"/triggers/cron/"+job.ID, "", nil)
	json.Unmarshal(request("POST", base+"/triggers", `{"kind":"cron","name":"scheduled","schedule":"0 0 1 1 *","user_id":"owner","input":"updated-input"}`, nil), &updated)
	if err = scheduler.PauseJobForOwner(t.Context(), updated.ID, updated.UserID); err != nil {
		t.Fatal(err)
	}
	scheduler = cron.NewScheduler(db, nil, nil)
	if err = scheduler.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.SetCronScheduler(scheduler)
	reloaded := webhook.NewManager(db)
	if err = reloaded.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	if binding, ok := reloaded.Get("workflow-event"); !ok || binding.WorkflowID != wf.ID {
		t.Fatal("webhook binding lost after reload")
	}
	previousPath := s.workflowStore.filePath
	s.workflowStore.filePath = t.TempDir()
	request("DELETE", base, "", nil, 500)
	if _, ok = mgr.Get("workflow-event"); !ok {
		t.Fatal("failed definition write removed webhook binding")
	}
	if jobs, err := scheduler.ListJobs(t.Context(), job.UserID); err != nil || len(jobs) != 2 {
		t.Fatal("failed definition write removed scheduled binding")
	}
	s.workflowStore.filePath = previousPath
	request("DELETE", base, "", nil)
	if endpoint, exists := mgr.Get("workflow-event"); !exists || endpoint.WorkflowID != "" || endpoint.Secret != secret {
		t.Fatal("workflow deletion did not detach and preserve the existing endpoint")
	}
	if _, ok = scheduler.GetJob(t.Context(), job.ID); ok {
		t.Fatal("workflow deletion left scheduled binding")
	}
	jobs, err := scheduler.ListJobs(t.Context(), job.UserID)
	if err != nil || len(jobs) != 1 || jobs[0].ID != legacy.ID {
		t.Fatalf("persistent cleanup changed unrelated job or left orphan: %+v %v", jobs, err)
	}
	json.Unmarshal(request("GET", "/api/v1/canvas/runs", "", nil), &history)
	if len(history.Runs) != 2 || history.Runs[0].Definition.Name != wf.Name {
		t.Fatal("global history lost deleted definition snapshot")
	}
	json.Unmarshal(request("GET", "/api/v1/canvas/runs?workflow_id=missing", "", nil), &history)
	if len(history.Runs) != 0 {
		t.Fatal("history filter included another workflow")
	}
}

func TestWorkflowContractResumePreservesConditionSelection(t *testing.T) {
	s := newWorkflowTestServer()
	var mu sync.Mutex
	counts := map[string]int{}
	s.engine = &workflowContractEngine{process: func(_ context.Context, msg *adapter.Message) (*adapter.Reply, error) {
		mu.Lock()
		defer mu.Unlock()
		role := msg.Metadata["role"]
		counts[role]++
		if counts[role] == 1 {
			return nil, &llm.ProviderError{StatusCode: 400}
		}
		return &adapter.Reply{Content: "selected-result"}, nil
	}}
	wf := branchingWorkflow()
	for _, raw := range wf.Nodes {
		node := raw.(map[string]any)
		if node["id"] == "branchA" {
			node["type"] = "agent"
			node["data"] = map[string]any{"role": "researcher", "prompt": "selected"}
		}
		if node["id"] == "branchB" {
			node["type"] = "agent"
			node["data"] = map[string]any{"role": "writer", "prompt": "unselected"}
		}
	}
	run, err := s.launchWorkflow(wf, RunWorkflowRequest{Input: "go"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunCompletion(t, s, run.ID)
	prior := getRunSnapshot(t, s, run.ID)
	if prior.Status != "failed" || len(prior.Checkpoint.DeactivatedEdges) == 0 {
		t.Fatalf("missing failed control checkpoint: %+v", prior)
	}
	req := httptest.NewRequest(http.MethodPost, "/resume", nil)
	req.SetPathValue("id", run.ID)
	w := httptest.NewRecorder()
	s.handleResumeWorkflowRun(w, req)
	if w.Code != 200 {
		t.Fatalf("resume: %d %s", w.Code, w.Body.String())
	}
	var next WorkflowRun
	json.Unmarshal(w.Body.Bytes(), &next)
	waitForRunCompletion(t, s, next.ID)
	final := getRunSnapshot(t, s, next.ID)
	mu.Lock()
	defer mu.Unlock()
	if final.Status != "completed" || counts["writer"] != 0 || counts["researcher"] != 2 {
		t.Fatalf("control selection changed after resume: %+v %+v", counts, final)
	}
}

func TestWorkflowContractJSONFieldsKeepTheirTypes(t *testing.T) {
	state := hexagon.MapState{stateKeyNodeOutputs: map[string]string{"source": `{"n":3,"s":"3","flag":true}`}}
	result := renderValue(map[string]any{"number": "{{node.source.n}}", "text": "{{node.source.s}}", "flag": "{{node.source.flag}}"}, state).(map[string]any)
	if result["number"] != float64(3) || result["text"] != "3" || result["flag"] != true {
		t.Fatalf("structured input type changed: %#v", result)
	}
}

func TestWorkflowContractOpaqueTimeoutIsNotBlindlyResumed(t *testing.T) {
	s := newWorkflowTestServer()
	calls := 0
	s.engine = &workflowContractEngine{process: func(_ context.Context, _ *adapter.Message) (*adapter.Reply, error) {
		calls++
		return nil, workflowOpaqueDeadline{}
	}}
	wf := contractWorkflow(contractNode("agent", "agent", map[string]any{"prompt": "task"}))
	run, err := s.launchWorkflow(wf, RunWorkflowRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunCompletion(t, s, run.ID)
	final := getRunSnapshot(t, s, run.ID)
	if final.Status != "outcome_unknown" || !final.NodeResults[0].OutcomeUnknown {
		t.Fatalf("friendly text hid unknown invocation: %+v", final)
	}
	req := httptest.NewRequest(http.MethodPost, "/resume", nil)
	req.SetPathValue("id", run.ID)
	w := httptest.NewRecorder()
	s.handleResumeWorkflowRun(w, req)
	if w.Code != 409 || calls != 1 {
		t.Fatalf("unknown invocation was resent: HTTP%d calls=%d", w.Code, calls)
	}
}
