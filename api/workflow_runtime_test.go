package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/messagecontent"
)

type workflowTestEngine struct {
	mu      sync.Mutex
	lastMsg *adapter.Message
}

func (e *workflowTestEngine) Start(context.Context) error  { return nil }
func (e *workflowTestEngine) Stop(context.Context) error   { return nil }
func (e *workflowTestEngine) Health(context.Context) error { return nil }

func (e *workflowTestEngine) Process(_ context.Context, msg *adapter.Message) (*adapter.Reply, error) {
	e.mu.Lock()
	e.lastMsg = msg
	e.mu.Unlock()
	role := ""
	if msg.Metadata != nil {
		role = msg.Metadata["role"]
	}
	return &adapter.Reply{Content: "[" + role + "] " + msg.Content}, nil
}

func (e *workflowTestEngine) ProcessStream(context.Context, *adapter.Message) (<-chan *adapter.ReplyChunk, error) {
	return nil, nil
}

func newWorkflowTestServer() *Server {
	return &Server{
		cfg:           config.DefaultConfig(),
		engine:        &workflowTestEngine{},
		logCollector:  NewLogCollector(10),
		workflowStore: &WorkflowStore{workflows: make(map[string]*WorkflowData), runs: make(map[string]*WorkflowRun), maxRuns: 100},
	}
}

func TestWorkflowRun_ParallelStage(t *testing.T) {
	s := newWorkflowTestServer()
	s.workflowStore.workflows["wf-parallel"] = &WorkflowData{
		ID:   "wf-parallel",
		Name: "parallel",
		Nodes: []any{
			map[string]any{"id": "input", "type": "input", "data": map[string]any{"value": "{{input}}"}},
			map[string]any{"id": "a", "type": "agent", "data": map[string]any{"role": "researcher", "prompt": "A: {{previous}}"}},
			map[string]any{"id": "b", "type": "agent", "data": map[string]any{"role": "writer", "prompt": "B: {{previous}}"}},
			map[string]any{"id": "out", "type": "output"},
		},
		Edges: []any{
			map[string]any{"source": "input", "target": "a"},
			map[string]any{"source": "input", "target": "b"},
			map[string]any{"source": "a", "target": "out"},
			map[string]any{"source": "b", "target": "out"},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/canvas/workflows/wf-parallel/run", strings.NewReader(`{"input":"hello"}`))
	req.SetPathValue("id", "wf-parallel")
	w := httptest.NewRecorder()

	s.handleRunWorkflow(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", w.Code, w.Body.String())
	}

	var run WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatalf("解析运行响应失败: %v", err)
	}

	waitForRunCompletion(t, s, run.ID)
	got := getRunSnapshot(t, s, run.ID)
	if got.Status != "completed" {
		t.Fatalf("期望 completed，实际 %s: %+v", got.Status, got)
	}
	if !strings.Contains(got.Output, "[researcher]") || !strings.Contains(got.Output, "[writer]") {
		t.Fatalf("并行阶段输出不完整: %q", got.Output)
	}
	if len(got.NodeResults) != 4 {
		t.Fatalf("期望 4 个节点结果，实际 %d", len(got.NodeResults))
	}
	if got.MessageContent == nil || got.MessageContent.ProducerKind != "workflow" {
		t.Fatalf("workflow output omitted canonical content: %#v", got.MessageContent)
	}
	if got.RenderManifest == nil || got.RenderManifest.SourceDigest != got.MessageContent.SourceDigest {
		t.Fatalf("workflow output omitted same-source render evidence: content=%#v manifest=%#v", got.MessageContent, got.RenderManifest)
	}
	if last := s.engine.(*workflowTestEngine).lastMsg; last == nil || last.Metadata["producer_kind"] != "workflow" {
		t.Fatalf("workflow engine dispatch omitted producer stamp: %#v", last)
	}
}

// TestWorkflowRun_FrontendFieldNames 验证 parse 兼容前端 CanvasNode/CanvasEdge 的字段名：
// 节点用 "config"（而非后端原生 "data"）、边用 "from"/"to"（而非 "source"/"target"）。
// 桌面端 WorkflowPanel 直接保存这种形状，必须能被图执行器正确读取并链接。
func TestWorkflowRun_FrontendFieldNames(t *testing.T) {
	s := newWorkflowTestServer()
	s.workflowStore.workflows["wf-fe"] = &WorkflowData{
		ID:   "wf-fe",
		Name: "frontend-shape",
		Nodes: []any{
			map[string]any{"id": "n1", "type": "input", "config": map[string]any{"value": "{{input}}"}},
			map[string]any{"id": "n2", "type": "agent", "config": map[string]any{"role": "researcher", "prompt": "分析：{{previous}}"}},
			map[string]any{"id": "n3", "type": "output"},
		},
		Edges: []any{
			map[string]any{"id": "e1", "from": "n1", "to": "n2"},
			map[string]any{"id": "e2", "from": "n2", "to": "n3"},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/canvas/workflows/wf-fe/run", strings.NewReader(`{"input":"市场调研"}`))
	req.SetPathValue("id", "wf-fe")
	w := httptest.NewRecorder()
	s.handleRunWorkflow(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var run WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatalf("解析运行响应失败: %v", err)
	}
	waitForRunCompletion(t, s, run.ID)
	got := getRunSnapshot(t, s, run.ID)
	if got.Status != "completed" {
		t.Fatalf("期望 completed，实际 %s: %+v", got.Status, got)
	}
	// config.role 被读取（前端字段兼容生效）→ 输出带 [researcher] 前缀
	if !strings.Contains(got.Output, "[researcher]") {
		t.Fatalf("config.role 未生效（前端字段兼容失败）: %q", got.Output)
	}
	// from/to 边被链接 → 3 个节点全部执行（非孤立）
	if len(got.NodeResults) != 3 {
		t.Fatalf("期望 3 个节点结果（from/to 边已链接），实际 %d: %+v", len(got.NodeResults), got.NodeResults)
	}
	eng := s.engine.(*workflowTestEngine)
	eng.mu.Lock()
	lastMsg := eng.lastMsg
	eng.mu.Unlock()
	if lastMsg == nil {
		t.Fatal("workflow engine 未收到消息")
	}
	if lastMsg.Metadata["source"] != "workflow" {
		t.Fatalf("workflow agent message must stamp source=workflow, got metadata=%v", lastMsg.Metadata)
	}
}

func TestWorkflowRun_AgentHandoff(t *testing.T) {
	s := newWorkflowTestServer()
	s.workflowStore.workflows["wf-handoff"] = &WorkflowData{
		ID:   "wf-handoff",
		Name: "handoff",
		Nodes: []any{
			map[string]any{"id": "input", "type": "input", "data": map[string]any{"value": "{{input}}"}},
			map[string]any{"id": "handoff", "type": "handoff", "data": map[string]any{"to_agent": "writer"}},
			map[string]any{"id": "research", "type": "agent", "data": map[string]any{"role": "researcher", "prompt": "R: {{previous}}"}},
			map[string]any{"id": "writer", "type": "agent", "data": map[string]any{"role": "writer", "prompt": "W: {{previous}}"}},
			map[string]any{"id": "out", "type": "output"},
		},
		Edges: []any{
			map[string]any{"source": "input", "target": "handoff"},
			map[string]any{"source": "handoff", "target": "research"},
			map[string]any{"source": "handoff", "target": "writer"},
			map[string]any{"source": "research", "target": "out"},
			map[string]any{"source": "writer", "target": "out"},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/canvas/workflows/wf-handoff/run", strings.NewReader(`{"input":"hello"}`))
	req.SetPathValue("id", "wf-handoff")
	w := httptest.NewRecorder()

	s.handleRunWorkflow(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", w.Code, w.Body.String())
	}

	var run WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatalf("解析运行响应失败: %v", err)
	}

	waitForRunCompletion(t, s, run.ID)
	got := getRunSnapshot(t, s, run.ID)
	if got.Status != "completed" {
		t.Fatalf("期望 completed，实际 %s: %+v", got.Status, got)
	}
	if !strings.Contains(got.Output, "[writer]") {
		t.Fatalf("handoff 后应由 writer 处理，实际输出: %q", got.Output)
	}

	results := make(map[string]WorkflowNodeRun)
	for _, item := range got.NodeResults {
		results[item.NodeID] = item
	}
	if results["research"].Status != nodeStatusSkipped {
		t.Fatalf("research 节点应被跳过，实际 %+v", results["research"])
	}
	if results["writer"].AgentRole != "writer" || results["writer"].Status != nodeStatusCompleted {
		t.Fatalf("writer 节点状态不正确: %+v", results["writer"])
	}
	if results["handoff"].HandoffAgent != "writer" {
		t.Fatalf("handoff 节点未记录目标 agent: %+v", results["handoff"])
	}
}

func TestWorkflowRun_CycleFails(t *testing.T) {
	s := newWorkflowTestServer()
	s.workflowStore.workflows["wf-cycle"] = &WorkflowData{
		ID:   "wf-cycle",
		Name: "cycle",
		Nodes: []any{
			map[string]any{"id": "a", "type": "noop"},
			map[string]any{"id": "b", "type": "noop"},
		},
		Edges: []any{
			map[string]any{"source": "a", "target": "b"},
			map[string]any{"source": "b", "target": "a"},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/canvas/workflows/wf-cycle/run", nil)
	req.SetPathValue("id", "wf-cycle")
	w := httptest.NewRecorder()

	s.handleRunWorkflow(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", w.Code, w.Body.String())
	}

	var run WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatalf("解析运行响应失败: %v", err)
	}

	waitForRunCompletion(t, s, run.ID)
	got := getRunSnapshot(t, s, run.ID)
	if got.Status != "failed" {
		t.Fatalf("期望 failed，实际 %s: %+v", got.Status, got)
	}
	if !strings.Contains(got.Error, "DAG") {
		t.Fatalf("期望 DAG 错误，实际 %q", got.Error)
	}
}

func TestWorkflowRun_PassesExplicitProviderAndModelToEngine(t *testing.T) {
	s := newWorkflowTestServer()
	s.workflowStore.workflows["wf-model"] = &WorkflowData{
		ID:   "wf-model",
		Name: "model",
		Nodes: []any{
			map[string]any{"id": "input", "type": "input", "data": map[string]any{"value": "{{input}}"}},
			map[string]any{"id": "agent", "type": "agent", "data": map[string]any{"provider": "智谱", "model": "glm-5", "prompt": "{{previous}}"}},
			map[string]any{"id": "out", "type": "output"},
		},
		Edges: []any{
			map[string]any{"source": "input", "target": "agent"},
			map[string]any{"source": "agent", "target": "out"},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/canvas/workflows/wf-model/run", strings.NewReader(`{"input":"hello"}`))
	req.SetPathValue("id", "wf-model")
	w := httptest.NewRecorder()

	s.handleRunWorkflow(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d: %s", w.Code, w.Body.String())
	}

	var run WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatalf("解析运行响应失败: %v", err)
	}

	waitForRunCompletion(t, s, run.ID)

	eng := s.engine.(*workflowTestEngine)
	if eng.lastMsg == nil {
		t.Fatal("workflow engine 未收到消息")
	}
	if got := eng.lastMsg.Metadata["provider"]; got != "智谱" {
		t.Fatalf("provider 未透传，实际 %q", got)
	}
	if got := eng.lastMsg.Metadata["model"]; got != "glm-5" {
		t.Fatalf("model 未透传，实际 %q", got)
	}
}

func waitForRunCompletion(t *testing.T, s *Server, runID string) {
	t.Helper()
	for range 50 {
		run := getRunSnapshot(t, s, runID)
		if run.Status != "running" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %s 超时未完成", runID)
}

func getRunSnapshot(t *testing.T, s *Server, runID string) WorkflowRun {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/canvas/runs/"+runID, nil)
	req.SetPathValue("id", runID)
	s.handleGetWorkflowRun(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("查询 run 失败: %d %s", w.Code, w.Body.String())
	}
	var run WorkflowRun
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatalf("解析 run 响应失败: %v", err)
	}
	return run
}

// 失败回复的已观察附件必须随运行快照保留，原失败分类与下游停止语义不变。
func TestWorkflowRunFailurePreservesObservedArtifact(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cause   error
		status  string
		unknown bool
	}{
		{"provider_rejected", &llm.ProviderError{StatusCode: 503, Body: "controlled upstream failure"}, "failed", false},
		{"outcome_unknown", errors.New("controlled unknown invocation"), "outcome_unknown", true},
		{"cancelled", context.Canceled, "outcome_unknown", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := workflowDeliveryServer(t)
			raw := []byte("workflow-partial-artifact-unique-marker\n")
			ref, attachment := workflowArtifactFixture(t, "partial.txt", "text/plain", raw)
			storedPath, err := s.genStore.SaveBytes(raw, "txt")
			if err != nil {
				t.Fatal(err)
			}
			attachment.Data = ""
			attachment.URL = "/api/v1/files/generated/" + storedPath
			canonical, err := messagecontent.New(messagecontent.ProducerWorkflow, "en", "observed partial", []messagecontent.AttachmentRef{ref})
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := messagecontent.BuildManifest(canonical, messagecontent.RenderRequest{Surface: messagecontent.SurfaceHistory, RendererVersion: "workflow-partial-test-v1", Capabilities: messagecontent.CapabilitySnapshot{Markdown: true, Attachments: true}, Parts: []messagecontent.RenderPart{{Kind: messagecontent.PartMarkdown, Text: canonical.Markdown}, {Kind: messagecontent.PartArtifact, ArtifactRef: ref.AssetID, ArtifactDigest: ref.Digest, AltText: ref.AltText}}})
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			s.engine = &workflowContractEngine{process: func(_ context.Context, _ *adapter.Message) (*adapter.Reply, error) {
				calls.Add(1)
				return &adapter.Reply{Content: canonical.Markdown, MessageContent: &canonical, RenderManifest: &manifest, Attachments: []adapter.Attachment{attachment}, ToolCalls: []adapter.ToolCall{{ID: "observed-get", Name: "browser", Arguments: `{"action":"request","method":"GET","url":"http://127.0.0.1/synthetic"}`, Status: "success", Result: "observed synthetic download", Detail: &adapter.ToolExecutionDetail{Phase: "observed", Outcome: "success"}}}}, tc.cause
			}}
			wf := contractWorkflow(contractNode("agent", "agent", nil), contractNode("after", "output", nil))
			s.workflowStore.workflows[wf.ID] = wf
			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/v1/canvas/workflows/{id}/run", s.handleRunWorkflow)
			mux.HandleFunc("GET /api/v1/canvas/runs/{id}", s.handleGetWorkflowRun)
			server := httptest.NewServer(mux)
			defer server.Close()
			response, err := server.Client().Post(server.URL+"/api/v1/canvas/workflows/"+wf.ID+"/run", "application/json", strings.NewReader(`{"input":"synthetic-only","user_id":"workflow-test-owner"}`))
			if err != nil {
				t.Fatal(err)
			}
			var launched WorkflowRun
			decodeErr := json.NewDecoder(response.Body).Decode(&launched)
			response.Body.Close()
			if response.StatusCode != 200 || decodeErr != nil || launched.ID == "" {
				t.Fatalf("run start: %d %v", response.StatusCode, decodeErr)
			}
			var got WorkflowRun
			for range 100 {
				response, err = server.Client().Get(server.URL + "/api/v1/canvas/runs/" + launched.ID)
				if err != nil {
					t.Fatal(err)
				}
				decodeErr = json.NewDecoder(response.Body).Decode(&got)
				response.Body.Close()
				if response.StatusCode != 200 || decodeErr != nil {
					t.Fatalf("run read: %d %v", response.StatusCode, decodeErr)
				}
				if got.Status != "running" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if got.Status != tc.status || !strings.Contains(got.Error, tc.cause.Error()) || calls.Load() != 1 {
				t.Fatalf("original failure changed: status=%s error=%q calls=%d", got.Status, got.Error, calls.Load())
			}
			check := func(label string, run *WorkflowRun) {
				t.Helper()
				snapshot, found := run.ArtifactSnapshots[ref.AssetID]
				var node *WorkflowNodeRun
				for i := range run.NodeResults {
					item := &run.NodeResults[i]
					if item.NodeID == "agent" {
						node = item
					}
					if item.NodeID == "after" && item.Status == nodeStatusCompleted {
						t.Fatal("failed invocation advanced downstream")
					}
				}
				canonicalOK := node != nil && node.MessageContent != nil && node.MessageContent.SourceDigest == canonical.SourceDigest && node.RenderManifest != nil && node.RenderManifest.RenderID == manifest.RenderID
				t.Logf("%s status=%s artifact=%t path=%t canonical=%t unknown=%t calls=%d", label, run.Status, found, snapshot.FilePath != "", canonicalOK, node != nil && node.OutcomeUnknown, calls.Load())
				if node == nil || node.Status != nodeStatusFailed || node.OutcomeUnknown != tc.unknown || !found || snapshot.Error != "" || snapshot.FilePath == "" || !canonicalOK {
					t.Fatalf("%s lost failed observed artifact/canonical: node=%+v artifact=%+v", label, node, snapshot)
				}
				file, err := s.genStore.Open(snapshot.FilePath)
				if err != nil {
					t.Fatal(err)
				}
				actual, err := io.ReadAll(file)
				file.Close()
				if err != nil || !bytes.Equal(actual, raw) {
					t.Fatalf("artifact bytes changed: %v", err)
				}
			}
			check("http", &got)
			reloaded := &WorkflowStore{runs: map[string]*WorkflowRun{}, runsFilePath: s.workflowStore.runsFilePath}
			reloaded.loadRunsFromFile()
			persisted := reloaded.runs[launched.ID]
			if persisted == nil {
				t.Fatal("run was not durably recorded")
			}
			check("reloaded", persisted)
		})
	}
}
