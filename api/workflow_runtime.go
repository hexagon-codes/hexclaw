package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
)

const (
	nodeStatusPending   = "pending"
	nodeStatusRunning   = "running"
	nodeStatusCompleted = "completed"
	nodeStatusSkipped   = "skipped"
	nodeStatusFailed    = "failed"

	stateKeyInput          = "__workflow_input"
	stateKeyNodeOutputs    = "__workflow_node_outputs"
	stateKeyNodeHandoffs   = "__workflow_node_handoffs"
	stateKeyWorkflowOutput = "__workflow_output"
	stateKeyPrevious       = "__workflow_previous"
	// C6 condition 分支：deadNodes 记录被 condition 分支停用/跳过的节点；deactivatedEdges
	// 记录被 condition 判定为未选中的出边（"source|target"）。节点在拓扑序被求值时，若其
	// 所有入边都不 live（源节点已 dead 或该入边被停用）则整枝跳过——实现按条件的分支路由。
	stateKeyDeadNodes        = "__workflow_dead_nodes"
	stateKeyDeactivatedEdges = "__workflow_deactivated_edges"
)

type RunWorkflowRequest struct {
	Input          string            `json:"input"`
	SessionID      string            `json:"session_id,omitempty"`
	UserID         string            `json:"user_id,omitempty"`
	Platform       string            `json:"platform,omitempty"`
	InstanceID     string            `json:"instance_id,omitempty"`
	ChatID         string            `json:"chat_id,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

type WorkflowNodeRun struct {
	ResourceReceipts    map[string]*WorkflowStepReceipt `json:"resource_receipts,omitempty"`
	NodeID              string                          `json:"node_id"`
	Type                string                          `json:"type"`
	Label               string                          `json:"label,omitempty"`
	Status              string                          `json:"status"`
	Output              string                          `json:"output,omitempty"`
	Error               string                          `json:"error,omitempty"`
	AgentRole           string                          `json:"agent_role,omitempty"`
	HandoffAgent        string                          `json:"handoff_agent,omitempty"`
	StartedAt           time.Time                       `json:"started_at,omitempty"`
	FinishedAt          time.Time                       `json:"finished_at,omitempty"`
	Branches            []WorkflowBranchRun             `json:"branches,omitempty"`
	MessageContent      *messagecontent.MessageContent  `json:"message_content,omitempty"`
	RenderManifest      *messagecontent.RenderManifest  `json:"render_manifest,omitempty"`
	ProviderDisplayName *string                         `json:"provider_display_name,omitempty"`
	ModelID             *string                         `json:"model_id,omitempty"`
	OutcomeUnknown      bool                            `json:"outcome_unknown,omitempty"`
}

// WorkflowBranchRun 保留并行组中每个 Agent 的独立执行事实。
type WorkflowBranchRun struct {
	Agent               string  `json:"agent"`
	Task                string  `json:"task,omitempty"`
	Status              string  `json:"status"`
	Output              string  `json:"output,omitempty"`
	Error               string  `json:"error,omitempty"`
	ProviderDisplayName *string `json:"provider_display_name,omitempty"`
	ModelID             *string `json:"model_id,omitempty"`
	OutcomeUnknown      bool    `json:"outcome_unknown,omitempty"`
}

type workflowNode struct {
	ID    string
	Type  string
	Label string
	Data  map[string]any
}

type workflowEdge struct {
	Source string
	Target string
}

type workflowStage struct {
	ID      string
	NodeIDs []string
}

type workflowExecutor struct {
	server *Server
	wf     *WorkflowData
	req    RunWorkflowRequest

	nodes    map[string]*workflowNode
	edges    []workflowEdge
	incoming map[string][]string
	outgoing map[string][]string
	order    map[string]int
	stages   []workflowStage
	sinks    []string

	mu       sync.Mutex
	nodeRuns map[string]*WorkflowNodeRun
	run      *WorkflowRun

	// routeSnapshotCaptured 表示本次运行已经到达真实 Agent 调用边界。
	// 并行节点可能乱序抵达，因此用 workflow 声明顺序稳定选择首个实际调用的路由。
	routeSnapshotCaptured  bool
	routeSnapshotNodeOrder int
	// routeSnapshotAuthoritative 表示该快照已经被引擎回复确认，而不只是调用前候选。
	routeSnapshotAuthoritative bool

	// resumed：续接（Ph5）已完成节点的缓存输出（nodeID→output）。执行时这些节点跳过重跑，
	// 直接复用上次结果，只重算失败/未达的节点——对齐 OpenClaw 续接语义。
	resumed map[string]string
}

func newWorkflowExecutor(s *Server, wf *WorkflowData, req RunWorkflowRequest) *workflowExecutor {
	return &workflowExecutor{
		server:                 s,
		wf:                     wf,
		req:                    req,
		nodes:                  make(map[string]*workflowNode),
		incoming:               make(map[string][]string),
		outgoing:               make(map[string][]string),
		order:                  make(map[string]int),
		nodeRuns:               make(map[string]*WorkflowNodeRun),
		routeSnapshotNodeOrder: -1,
	}
}

// withResumed 注入续接的已完成节点输出，返回自身便于链式调用。
func (e *workflowExecutor) withResumed(resumed map[string]string) *workflowExecutor {
	e.resumed = resumed
	return e
}

func (e *workflowExecutor) execute(ctx context.Context, run *WorkflowRun) *WorkflowRun {
	e.run = run
	if run != nil && (run.ProviderDisplayName != nil || run.ModelID != nil) {
		// 续接继承上一次真实调用的历史快照，绝不按当前配置重算。
		e.routeSnapshotCaptured = true
		e.routeSnapshotAuthoritative = true
	}
	if err := e.parse(); err != nil {
		return e.failedRun(run, err)
	}
	if err := e.buildStages(); err != nil {
		return e.failedRun(run, err)
	}

	if len(e.nodes) == 0 {
		finished := *run
		finished.Status = "completed"
		finished.FinishedAt = time.Now()
		return &finished
	}

	g, err := e.buildGraph()
	if err != nil {
		return e.failedRun(run, err)
	}

	state := checkpointState(run.Checkpoint, e.req.Input)
	if run.Checkpoint != nil {
		e.resumed = cloneStringMap(run.Checkpoint.Outputs)
		for _, result := range run.NodeResults {
			result := result
			e.nodeRuns[result.NodeID] = &result
		}
	}

	result, err := g.Run(ctx, state)
	if err != nil {
		return e.failedRun(run, err)
	}

	output := firstNonEmpty(stringStateValue(result, stateKeyWorkflowOutput), e.collectFinalOutput(result))
	finished := *run
	finished.Status = "completed"
	finished.Output = output
	finished.MessageContent, finished.RenderManifest = canonicalRenderProjection(
		messagecontent.ProducerWorkflow,
		firstNonEmpty(e.req.Metadata["locale"], "und"),
		output,
		messagecontent.SurfaceHistory,
		"workflow-history-v1",
	)
	finished.NodeResults = e.listNodeRuns()
	for _, result := range finished.NodeResults {
		if result.Output == output && result.MessageContent != nil {
			finished.MessageContent, finished.RenderManifest = result.MessageContent, result.RenderManifest
		}
	}
	finished.FinishedAt = time.Now()
	return &finished
}

func (e *workflowExecutor) failedRun(run *WorkflowRun, err error) *WorkflowRun {
	finished := *run
	finished.Status = "failed"
	if errors.Is(err, context.Canceled) {
		finished.Status = "cancelled"
	}
	if err != nil {
		finished.Error = err.Error()
	}
	finished.NodeResults = e.listNodeRuns()
	finished.FinishedAt = time.Now()
	return &finished
}

func (e *workflowExecutor) parse() error {
	for i, raw := range e.wf.Nodes {
		nodeMap, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := stringValue(nodeMap["id"])
		if id == "" {
			return fmt.Errorf("workflow node[%d] 缺少 id", i)
		}
		if _, exists := e.nodes[id]; exists {
			return fmt.Errorf("workflow node id 重复: %s", id)
		}
		// 节点配置兼容两种字段名：后端原生 "data" 与前端 CanvasNode 的 "config"。
		data, _ := nodeMap["data"].(map[string]any)
		if len(data) == 0 {
			if cfg, ok := nodeMap["config"].(map[string]any); ok {
				data = cfg
			}
		}
		nodeType := strings.ToLower(stringValue(nodeMap["type"]))
		if nodeType == "" {
			nodeType = "noop"
		}
		label := firstNonEmpty(stringValue(nodeMap["label"]), id)
		e.nodes[id] = &workflowNode{
			ID:    id,
			Type:  nodeType,
			Label: label,
			Data:  cloneMap(data),
		}
		e.order[id] = i
	}

	for _, raw := range e.wf.Edges {
		edgeMap, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		// 边端点兼容两种字段名：后端原生 "source"/"target" 与前端 CanvasEdge 的 "from"/"to"。
		source := firstNonEmpty(stringValue(edgeMap["source"]), stringValue(edgeMap["from"]))
		target := firstNonEmpty(stringValue(edgeMap["target"]), stringValue(edgeMap["to"]))
		if source == "" || target == "" {
			continue
		}
		if e.nodes[source] == nil || e.nodes[target] == nil {
			return fmt.Errorf("workflow edge 引用了不存在的节点: %s -> %s", source, target)
		}
		e.edges = append(e.edges, workflowEdge{Source: source, Target: target})
		e.outgoing[source] = append(e.outgoing[source], target)
		e.incoming[target] = append(e.incoming[target], source)
	}

	for id := range e.nodes {
		if len(e.outgoing[id]) == 0 {
			e.sinks = append(e.sinks, id)
		}
	}
	sort.Slice(e.sinks, func(i, j int) bool {
		return e.order[e.sinks[i]] < e.order[e.sinks[j]]
	})
	return nil
}

func (e *workflowExecutor) buildStages() error {
	indegree := make(map[string]int, len(e.nodes))
	for id := range e.nodes {
		indegree[id] = len(e.incoming[id])
	}

	var current []string
	for id, deg := range indegree {
		if deg == 0 {
			current = append(current, id)
		}
	}
	sort.Slice(current, func(i, j int) bool {
		return e.order[current[i]] < e.order[current[j]]
	})

	processed := 0
	stageIndex := 0
	for len(current) > 0 {
		stage := workflowStage{
			ID:      fmt.Sprintf("stage_%02d", stageIndex),
			NodeIDs: append([]string(nil), current...),
		}
		e.stages = append(e.stages, stage)

		nextSet := make(map[string]struct{})
		for _, id := range current {
			processed++
			for _, target := range e.outgoing[id] {
				indegree[target]--
				if indegree[target] == 0 {
					nextSet[target] = struct{}{}
				}
			}
		}

		next := make([]string, 0, len(nextSet))
		for id := range nextSet {
			next = append(next, id)
		}
		sort.Slice(next, func(i, j int) bool {
			return e.order[next[i]] < e.order[next[j]]
		})
		current = next
		stageIndex++
	}

	if processed != len(e.nodes) {
		return fmt.Errorf("workflow DAG 存在环或不可达依赖")
	}
	return nil
}

func (e *workflowExecutor) buildGraph() (*hexagon.Graph[hexagon.MapState], error) {
	builder := hexagon.NewGraph[hexagon.MapState](e.wf.Name)

	for _, stage := range e.stages {
		if len(stage.NodeIDs) == 1 {
			nodeID := stage.NodeIDs[0]
			builder.AddNode(stage.ID, func(ctx context.Context, state hexagon.MapState) (hexagon.MapState, error) {
				return e.executeNode(ctx, state, e.nodes[nodeID])
			})
			continue
		}

		handlers := make([]hexagon.NodeHandler[hexagon.MapState], 0, len(stage.NodeIDs))
		for _, nodeID := range stage.NodeIDs {
			id := nodeID
			handlers = append(handlers, func(ctx context.Context, state hexagon.MapState) (hexagon.MapState, error) {
				return e.executeNode(ctx, state, e.nodes[id])
			})
		}

		builder.AddNodeWithBuilder(hexagon.ParallelNodeWithMerger(
			stage.ID,
			func(original hexagon.MapState, outputs []hexagon.MapState) hexagon.MapState {
				return mergeWorkflowStates(original, outputs)
			},
			handlers...,
		))
	}

	builder.AddEdge(hexagon.START, e.stages[0].ID)
	for i := 0; i < len(e.stages)-1; i++ {
		builder.AddEdge(e.stages[i].ID, e.stages[i+1].ID)
	}
	builder.AddEdge(e.stages[len(e.stages)-1].ID, hexagon.END)
	return builder.Build()
}

func (e *workflowExecutor) executeNode(ctx context.Context, state hexagon.MapState, node *workflowNode) (hexagon.MapState, error) {
	if node == nil {
		return state, nil
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	state = setStringStateValue(state, stateKeyPrevious, e.upstreamOutputs(state, node.ID))

	// 续接（Ph5）：本节点上次已完成 → 跳过重跑，复用缓存输出并回灌 state，只重算失败/未达节点。
	if cached, ok := e.resumed[node.ID]; ok {
		e.markNodeResumed(node, cached)
		state = putStringMapStateValue(state, stateKeyNodeOutputs, node.ID, cached)
		if node.Type == "output" {
			if len(e.outgoing[node.ID]) == 0 {
				state = setStringStateValue(state, stateKeyWorkflowOutput, cached)
			}
		}
		return state, nil
	}

	// C6：若本节点所有入边都因上游 condition 分支未选中而失效，则整枝跳过（不执行、不产出）。
	// 根节点（无入边）与至少有一条 live 入边的节点正常执行。
	if e.nodeDeactivatedByBranch(state, node) {
		e.markNodeSkipped(node, "")
		state = e.markNodeDead(state, node.ID)
		return state, e.publishCheckpoint(state)
	}

	e.markNodeStart(node)
	if err := e.publishCheckpoint(state); err != nil {
		return state, fmt.Errorf("persist workflow checkpoint: %w", err)
	}
	inputText, inputErr := e.resolveNodeInputChecked(state, node)
	if inputErr != nil {
		e.markNodeFailed(node, "", "", inputErr)
		return state, errors.Join(inputErr, e.publishCheckpoint(state))
	}

	var (
		output       string
		agentRole    string
		handoffAgent string
		err          error
	)

	switch node.Type {
	case "input":
		output = firstNonEmpty(renderTemplate(firstNonEmpty(stringValue(node.Data["prompt"]), stringValue(node.Data["value"])), state), stringStateValue(state, stateKeyInput))
		if kind := stringValue(resourceMap(node.Data["input_selection"])["kind"]); kind != "" && kind != "text" {
			output = inputText
		}

	case "agent":
		selected := e.selectedHandoffForNode(state, node)
		agentRole = firstNonEmpty(stringValue(node.Data["agent"]), stringValue(node.Data["role"]), selected)
		if selected != "" && agentRole != "" && agentRole != selected {
			e.markNodeSkipped(node, agentRole)
			return e.markNodeDead(state, node.ID), nil
		}
		output, err = e.executeAgent(ctx, node, inputText, agentRole)

	case "handoff", "agent_handoff":
		handoffAgent, err = e.selectHandoffAgent(ctx, node, inputText)
		output = inputText

	case "parallel", "fanout":
		// 确定性扇出：一个节点并行跑多个角色 Agent，合并输出（对齐 Claude Code Workflow
		// parallel() / OpenClaw fan-out）。复用有界并发上限，避免角色数 = goroutine 数。
		output, err = e.executeParallelRoles(ctx, node, inputText)

	case "tool":
		output, err = e.executeTool(ctx, node, inputText, state)

	case "output":
		output = inputText
		refs := e.workflowSourceArtifacts(node)
		if len(refs) > 0 {
			content, contentErr := messagecontent.New(messagecontent.ProducerWorkflow, firstNonEmpty(e.req.Metadata["locale"], "und"), output, refs)
			if contentErr != nil {
				err = contentErr
			} else {
				e.mu.Lock()
				e.ensureNodeRun(node).MessageContent = &content
				e.mu.Unlock()
			}
		}
		if len(e.outgoing[node.ID]) == 0 {
			state = setStringStateValue(state, stateKeyWorkflowOutput, output)
		}

	case "condition":
		// C6：求值条件表达式，选择激活分支，停用未选中的出边（其下游整枝跳过）。
		// output 透传 inputText 给激活分支；条件配置非法时显式失败而非静默直通。
		state, output, err = e.executeCondition(state, node, inputText)

	default:
		output = inputText
	}

	if err != nil {
		e.markNodeFailed(node, agentRole, handoffAgent, err)
		_ = e.publishCheckpoint(state)
		return state, err
	}

	state = putStringMapStateValue(state, stateKeyNodeOutputs, node.ID, output)
	if handoffAgent != "" {
		state = putStringMapStateValue(state, stateKeyNodeHandoffs, node.ID, handoffAgent)
	}
	e.markNodeCompleted(node, output, agentRole, handoffAgent)
	if err := e.publishCheckpoint(state); err != nil {
		return state, fmt.Errorf("persist workflow checkpoint: %w", err)
	}
	return state, nil
}

func (e *workflowExecutor) executeAgent(ctx context.Context, node *workflowNode, inputText, role string, branchIndex ...int) (string, error) {
	if e.server.engine == nil {
		return "", fmt.Errorf("engine 未初始化")
	}

	metadata := make(map[string]string, len(e.req.Metadata)+4)
	for k, v := range e.req.Metadata {
		metadata[k] = v
	}
	// GO-3：工作流触发请求的 metadata 同为客户端可控——先剥保留派发键，再由本执行器
	// 盖章受信的 source=workflow / workflow_id，避免客户端伪造 cron_job_id 盗 grant。
	engine.StripReservedDispatchMetadata(metadata)
	if provider := stringValue(node.Data["provider"]); provider != "" {
		metadata["provider"] = provider
	}
	if model := stringValue(node.Data["model"]); model != "" {
		metadata["model"] = model
	}
	ctx, inputText, resources, resourceErr := e.prepareStepInvocation(ctx, node, metadata, inputText, branchIndex...)
	if resourceErr != nil {
		return "", resourceErr
	}
	if resources != nil && resources.Options.Agent != nil {
		metadata["pinned_agent"] = resources.Options.Agent.Name
	} else if role != "" && (stringValue(node.Data["agent"]) != "" || len(branchIndex) > 0) {
		if e.server.agentRouter == nil {
			return "", fmt.Errorf("Selected Agent is unavailable: %s", role)
		}
		if _, ok := e.server.agentRouter.GetAgent(role); !ok {
			return "", fmt.Errorf("Selected Agent is unavailable: %s", role)
		}
		metadata["pinned_agent"] = role
	} else if role != "" {
		if e.server.agentRouter != nil {
			if _, ok := e.server.agentRouter.GetAgent(role); ok {
				metadata["pinned_agent"] = role
			} else {
				metadata["role"] = role
			}
		} else {
			metadata["role"] = role
		}
	} else {
		metadata["pinned_agent"] = "default"
	}
	if provider := stringValue(node.Data["provider"]); provider != "" {
		metadata["provider"] = provider
	}
	if model := stringValue(node.Data["model"]); model != "" {
		metadata["model"] = model
	}
	metadata["workflow_id"] = e.wf.ID
	metadata["workflow_node_id"] = node.ID
	metadata["source"] = "workflow"
	metadata["producer_kind"] = string(messagecontent.ProducerWorkflow)
	if strings.TrimSpace(metadata["locale"]) == "" {
		metadata["locale"] = "und"
	}
	e.recordStepReceipt(node, resources, nil, nil, branchIndex...)

	e.freezeRunRouteAtModelBoundary(node)

	reply, err := e.server.engine.Process(ctx, (&agentrouterMessageAdapter{
		UserID:     firstNonEmpty(e.req.UserID, "workflow-"+e.wf.ID),
		Platform:   e.req.Platform,
		InstanceID: e.req.InstanceID,
		ChatID:     e.req.ChatID,
		Content:    inputText,
		Metadata:   metadata,
	}).Message())
	if err != nil {
		e.recordWorkflowInvocationOutcome(node, err, branchIndex...)
		return "", err
	}
	if reply == nil {
		err := fmt.Errorf("Agent returned no response")
		e.recordWorkflowInvocationOutcome(node, err, branchIndex...)
		return "", err
	}
	e.freezeRunRouteFromEngineReply(node, reply)
	artifacts := e.server.freezeWorkflowArtifacts(ctx, reply)
	e.mu.Lock()
	if len(artifacts) > 0 {
		if e.run.ArtifactSnapshots == nil {
			e.run.ArtifactSnapshots = map[string]WorkflowArtifactSnapshot{}
		}
		for id, snapshot := range artifacts {
			e.run.ArtifactSnapshots[id] = snapshot
		}
	}
	result := e.ensureNodeRun(node)
	result.MessageContent, result.RenderManifest = reply.MessageContent, reply.RenderManifest
	if reply.Metadata != nil {
		provider, model := e.server.freezeWorkflowRouteSnapshot(reply.Metadata["provider"], reply.Metadata["model"])
		if reply.Metadata["provider"] != "" || reply.Metadata["model"] != "" {
			result.ProviderDisplayName, result.ModelID = provider, model
			if len(branchIndex) > 0 && branchIndex[0] < len(result.Branches) {
				result.Branches[branchIndex[0]].ProviderDisplayName, result.Branches[branchIndex[0]].ModelID = provider, model
			}
		}
	}
	e.mu.Unlock()
	outputErr := validateWorkflowOutput(resources, reply)
	e.recordStepReceipt(node, resources, reply, outputErr, branchIndex...)
	if outputErr != nil {
		return reply.Content, outputErr
	}
	return reply.Content, nil
}

// freezeRunRouteAtModelBoundary 把首次实际进入 Agent 引擎调用边界的路由落到唯一 run 真相源。
// DAG/解析等前置失败不会触达本方法，因此其 DTO 保持显式 null；resume 则沿用已有历史快照。
func (e *workflowExecutor) freezeRunRouteAtModelBoundary(node *workflowNode) {
	if node == nil {
		return
	}
	e.recordRunRouteSnapshot(
		node,
		strings.TrimSpace(stringValue(node.Data["provider"])),
		strings.TrimSpace(stringValue(node.Data["model"])),
		false,
	)
}

// freezeRunRouteFromEngineReply 以引擎确认的 provider/model 覆盖同一调用边界的候选值。
// 引擎可能在实际运行时完成路由或回退，不能把请求节点配置误当成物理调用事实。
func (e *workflowExecutor) freezeRunRouteFromEngineReply(node *workflowNode, reply *adapter.Reply) {
	if node == nil || reply == nil || reply.Metadata == nil {
		return
	}
	provider := strings.TrimSpace(reply.Metadata["provider"])
	model := strings.TrimSpace(reply.Metadata["model"])
	if provider == "" && model == "" {
		return
	}
	e.recordRunRouteSnapshot(
		node,
		firstNonEmpty(provider, strings.TrimSpace(stringValue(node.Data["provider"]))),
		firstNonEmpty(model, strings.TrimSpace(stringValue(node.Data["model"]))),
		true,
	)
}

func (e *workflowExecutor) recordRunRouteSnapshot(node *workflowNode, requestedProvider, requestedModel string, authoritative bool) {
	if e.server == nil || e.server.workflowStore == nil || e.run == nil || node == nil {
		return
	}

	providerDisplayName, modelID := e.server.freezeWorkflowRouteSnapshot(
		requestedProvider,
		requestedModel,
	)
	candidateOrder := e.order[node.ID]

	e.server.workflowStore.mu.Lock()
	defer e.server.workflowStore.mu.Unlock()
	if e.routeSnapshotCaptured && e.routeSnapshotNodeOrder < 0 {
		return
	}
	if e.routeSnapshotCaptured && e.routeSnapshotNodeOrder < candidateOrder {
		return
	}
	if e.routeSnapshotCaptured && e.routeSnapshotNodeOrder == candidateOrder &&
		(e.routeSnapshotAuthoritative || !authoritative) {
		return
	}

	e.run.ProviderDisplayName = providerDisplayName
	e.run.ModelID = modelID
	e.routeSnapshotCaptured = true
	e.routeSnapshotNodeOrder = candidateOrder
	e.routeSnapshotAuthoritative = authoritative
	// 在物理调用开始时立即持久化，崩溃/重启不能把已发生的调用退化为无路由事实。
	e.server.workflowStore.persistRuns()
}

// maxWorkflowParallel 限制单个 parallel 节点并发跑的角色 Agent 数（与 orchestrate 一致）。
const maxWorkflowParallel = 8

// parseParallelRoles 解析 parallel 节点的角色列表：支持 []any（前端结构化）或逗号/换行分隔的
// 字符串（前端输入框），去空去重保序。
func parseParallelRoles(raw any) []string {
	var parts []string
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			if s := strings.TrimSpace(stringValue(item)); s != "" {
				parts = append(parts, s)
			}
		}
	case []string:
		parts = v
	case string:
		for _, s := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '\n' || r == ';' }) {
			if t := strings.TrimSpace(s); t != "" {
				parts = append(parts, t)
			}
		}
	}
	seen := make(map[string]bool, len(parts))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// executeParallelRoles 让 parallel 节点并行跑多个角色 Agent，按角色名合并输出。复用有界并发
// 上限（maxWorkflowParallel），避免角色数 = goroutine 数。无角色时退化为单 agent。
func (e *workflowExecutor) executeParallelRoles(ctx context.Context, node *workflowNode, inputText string) (string, error) {
	var branches []WorkflowBranchRun
	if configured, ok := node.Data["branches"].([]any); ok {
		for _, raw := range configured {
			branch, ok := raw.(map[string]any)
			if !ok || strings.TrimSpace(stringValue(branch["agent"])) == "" {
				return "", fmt.Errorf("Each parallel branch must select an Agent")
			}
			branches = append(branches, WorkflowBranchRun{Agent: strings.TrimSpace(stringValue(branch["agent"])), Task: stringValue(branch["task"]), Status: nodeStatusPending})
		}
	} else {
		for _, role := range parseParallelRoles(node.Data["roles"]) {
			branches = append(branches, WorkflowBranchRun{Agent: role, Status: nodeStatusPending})
		}
	}
	if len(branches) == 0 {
		return "", fmt.Errorf("Parallel group has no Agent branches")
	}
	e.mu.Lock()
	prior := e.ensureNodeRun(node).Branches
	for i := range branches {
		if i < len(prior) && prior[i].Agent == branches[i].Agent && prior[i].Task == branches[i].Task && prior[i].Status == nodeStatusCompleted {
			branches[i] = prior[i]
		}
	}
	e.ensureNodeRun(node).Branches = branches
	e.mu.Unlock()
	sem := make(chan struct{}, maxWorkflowParallel)
	failures := make([]error, len(branches))
	var wg sync.WaitGroup
	for i := range branches {
		if branches[i].Status == nodeStatusCompleted {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				failures[i] = ctx.Err()
				return
			}
			e.mu.Lock()
			branch := branches[i]
			branch.Status = nodeStatusRunning
			branches[i] = branch
			e.mu.Unlock()
			if err := e.publishCheckpoint(hexagon.MapState{}); err != nil {
				failures[i] = err
				return
			}
			taskInput := inputText
			if branch.Task != "" {
				taskInput = branch.Task + "\n\n" + inputText
			}
			out, err := e.executeAgent(ctx, node, taskInput, branch.Agent, i)
			e.mu.Lock()
			branch.Status, branch.Output = nodeStatusCompleted, out
			branch.ProviderDisplayName, branch.ModelID = branches[i].ProviderDisplayName, branches[i].ModelID
			branch.OutcomeUnknown = branches[i].OutcomeUnknown
			if err != nil {
				branch.Status, branch.Error = nodeStatusFailed, err.Error()
				failures[i] = err
			}
			branches[i] = branch
			e.mu.Unlock()
			if persistErr := e.publishCheckpoint(hexagon.MapState{}); persistErr != nil {
				failures[i] = persistErr
			}
		}(i)
	}
	wg.Wait()
	var output strings.Builder
	for _, branch := range branches {
		if branch.Status == nodeStatusCompleted {
			fmt.Fprintf(&output, "=== %s ===\n%s\n\n", branch.Agent, branch.Output)
		}
	}
	return output.String(), errors.Join(failures...)
}

func (e *workflowExecutor) executeTool(ctx context.Context, node *workflowNode, inputText string, state hexagon.MapState) (string, error) {
	toolName := firstNonEmpty(stringValue(node.Data["tool"]), stringValue(node.Data["name"]))
	if toolName == "" {
		return "", fmt.Errorf("tool 节点缺少 tool 名称")
	}

	// 无人值守连接器授权闸（fail-closed，先于一切副作用）：tool 节点直连 mcpMgr
	// 绕过 PermissionHook，在此补齐与 engine 连接器闸一致的判定，未授权即拦。
	if err := e.server.authorizeWorkflowConnectorTool(e.wf.ID, toolName); err != nil {
		return "", err
	}

	if e.server.mcpMgr == nil {
		return "", fmt.Errorf("mcp manager 未初始化")
	}

	args, valid := node.Data["args"].(map[string]any)
	if node.Data["args"] != nil && !valid {
		return "", fmt.Errorf("Tool arguments must be a JSON object")
	}
	state = setStringStateValue(state, "value", inputText)
	rendered := renderValue(cloneMap(args), state)
	argMap, _ := rendered.(map[string]any)
	if len(argMap) == 0 && inputText != "" {
		argMap = map[string]any{"input": inputText}
	}

	toolStarted := time.Now()
	output, err := e.server.mcpMgr.CallTool(ctx, toolName, argMap)
	if err != nil {
		e.recordWorkflowInvocationOutcome(node, err)
	}
	encodedArgs, _ := json.Marshal(argMap)
	call := adapter.ToolCall{Name: toolName, Arguments: string(encodedArgs), Result: output, Status: "success", DurationMs: time.Since(toolStarted).Milliseconds()}
	if err != nil {
		call.Status = "error"
	}
	e.recordStepReceipt(node, nil, &adapter.Reply{Content: output, ToolCalls: []adapter.ToolCall{call}}, nil)
	return output, err
}

func (e *workflowExecutor) recordWorkflowInvocationOutcome(node *workflowNode, err error, branchIndex ...int) {
	var rejected *llm.ProviderError
	unknown := true
	if errors.As(err, &rejected) && rejected.StatusCode >= 400 && rejected.StatusCode <= 599 {
		unknown = false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	result := e.ensureNodeRun(node)
	result.OutcomeUnknown = result.OutcomeUnknown || unknown
	if len(branchIndex) > 0 && branchIndex[0] < len(result.Branches) {
		result.Branches[branchIndex[0]].OutcomeUnknown = unknown
	}
}

func (e *workflowExecutor) selectHandoffAgent(ctx context.Context, node *workflowNode, inputText string) (string, error) {
	explicit := firstNonEmpty(stringValue(node.Data["to_agent"]), stringValue(node.Data["agent"]), stringValue(node.Data["role"]))
	if explicit != "" {
		return explicit, nil
	}

	candidates := stringSlice(node.Data["candidates"])
	if len(candidates) == 0 {
		candidates = e.successorAgentRoles(node.ID)
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("handoff 节点 %s 缺少候选 agent", node.ID)
	}

	if e.server.agentRouter != nil {
		req := agentrouter.RouteRequest{
			Platform:   e.req.Platform,
			InstanceID: e.req.InstanceID,
			UserID:     e.req.UserID,
			ChatID:     e.req.ChatID,
		}
		result, _ := e.server.agentRouter.RouteWithFallback(ctx, req, inputText)
		if result != nil && slices.Contains(candidates, result.AgentName) {
			return result.AgentName, nil
		}
	}

	return candidates[0], nil
}

func (e *workflowExecutor) successorAgentRoles(nodeID string) []string {
	var roles []string
	for _, target := range e.outgoing[nodeID] {
		role := firstNonEmpty(stringValue(e.nodes[target].Data["role"]), stringValue(e.nodes[target].Data["agent"]))
		if role != "" && !slices.Contains(roles, role) {
			roles = append(roles, role)
		}
	}
	sort.Strings(roles)
	return roles
}

func (e *workflowExecutor) resolveNodeInput(state hexagon.MapState, node *workflowNode) string {
	value, _ := e.resolveNodeInputChecked(state, node)
	return value
}

func (e *workflowExecutor) resolveNodeInputChecked(state hexagon.MapState, node *workflowNode) (string, error) {
	upstream, err := e.workflowSelectedValue(state, node)
	if err != nil {
		return "", err
	}
	if node.Type == "output" || node.Type == "input" && stringValue(resourceMap(node.Data["input_selection"])["kind"]) != "" {
		return upstream, nil
	}
	prompt := stringValue(node.Data["prompt"])

	rendered := renderTemplate(prompt, state)
	if strings.Contains(prompt, "{{") {
		return rendered, nil
	}
	switch {
	case rendered != "" && upstream != "":
		return rendered + "\n\n[上游结果]\n" + upstream, nil
	case rendered != "":
		return rendered, nil
	case upstream != "":
		return upstream, nil
	case stringStateValue(state, stateKeyInput) != "":
		return stringStateValue(state, stateKeyInput), nil
	default:
		return "", nil
	}
}

func (e *workflowExecutor) upstreamOutputs(state hexagon.MapState, nodeID string) string {
	outputs := stringMapStateValue(state, stateKeyNodeOutputs)
	preds := append([]string(nil), e.incoming[nodeID]...)
	sort.Slice(preds, func(i, j int) bool {
		return e.order[preds[i]] < e.order[preds[j]]
	})

	parts := make([]string, 0, len(preds))
	for _, pred := range preds {
		if out := outputs[pred]; out != "" {
			parts = append(parts, out)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (e *workflowExecutor) selectedHandoffForNode(state hexagon.MapState, node *workflowNode) string {
	handoffs := stringMapStateValue(state, stateKeyNodeHandoffs)
	preds := append([]string(nil), e.incoming[node.ID]...)
	sort.Slice(preds, func(i, j int) bool {
		return e.order[preds[i]] < e.order[preds[j]]
	})
	for _, pred := range preds {
		if e.nodes[pred] != nil && (e.nodes[pred].Type == "handoff" || e.nodes[pred].Type == "agent_handoff") {
			if explicit := firstNonEmpty(
				stringValue(e.nodes[pred].Data["to_agent"]),
				stringValue(e.nodes[pred].Data["agent"]),
				stringValue(e.nodes[pred].Data["role"]),
			); explicit != "" {
				return explicit
			}
			if selected := handoffs[pred]; selected != "" {
				return selected
			}
		}
	}
	if len(handoffs) == 1 {
		for _, selected := range handoffs {
			return selected
		}
	}
	return ""
}

func (e *workflowExecutor) collectFinalOutput(state hexagon.MapState) string {
	outputs := stringMapStateValue(state, stateKeyNodeOutputs)
	var selected []string
	for _, sink := range e.sinks {
		if out := outputs[sink]; out != "" {
			selected = append(selected, out)
		}
	}
	return strings.Join(selected, "\n\n")
}

func (e *workflowExecutor) markNodeStart(node *workflowNode) {
	e.mu.Lock()
	defer e.mu.Unlock()
	run := e.ensureNodeRun(node)
	run.Status = nodeStatusRunning
	run.StartedAt = time.Now()
	run.FinishedAt = time.Time{}
	run.Error = ""
	run.OutcomeUnknown = false
}

func (e *workflowExecutor) markNodeCompleted(node *workflowNode, output, role, handoffAgent string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	run := e.ensureNodeRun(node)
	run.Status = nodeStatusCompleted
	run.Output = output
	run.Error = ""
	run.AgentRole = role
	run.HandoffAgent = handoffAgent
	run.FinishedAt = time.Now()
}

// markNodeResumed 把续接复用的节点标记为已完成（直接采用上次缓存输出，不重跑）。
func (e *workflowExecutor) markNodeResumed(node *workflowNode, output string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	run := e.ensureNodeRun(node)
	run.Status = nodeStatusCompleted
	run.Output = output
	now := time.Now()
	if run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	if run.FinishedAt.IsZero() {
		run.FinishedAt = now
	}
}

func (e *workflowExecutor) markNodeSkipped(node *workflowNode, role string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	run := e.ensureNodeRun(node)
	run.Status = nodeStatusSkipped
	run.AgentRole = role
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	run.FinishedAt = time.Now()
}

func (e *workflowExecutor) markNodeFailed(node *workflowNode, role, handoffAgent string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	run := e.ensureNodeRun(node)
	run.Status = nodeStatusFailed
	run.AgentRole = role
	run.HandoffAgent = handoffAgent
	run.Error = err.Error()
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	run.FinishedAt = time.Now()
}

func (e *workflowExecutor) ensureNodeRun(node *workflowNode) *WorkflowNodeRun {
	if run, ok := e.nodeRuns[node.ID]; ok {
		return run
	}
	run := &WorkflowNodeRun{
		NodeID: node.ID,
		Type:   node.Type,
		Label:  node.Label,
		Status: nodeStatusPending,
	}
	e.nodeRuns[node.ID] = run
	return run
}

func (e *workflowExecutor) listNodeRuns() []WorkflowNodeRun {
	e.mu.Lock()
	defer e.mu.Unlock()

	list := make([]WorkflowNodeRun, 0, len(e.nodes))
	for id, node := range e.nodes {
		run := e.nodeRuns[id]
		if run == nil {
			run = &WorkflowNodeRun{
				NodeID: id,
				Type:   node.Type,
				Label:  node.Label,
				Status: nodeStatusPending,
			}
		}
		list = append(list, *run)
	}
	sort.Slice(list, func(i, j int) bool {
		return e.order[list[i].NodeID] < e.order[list[j].NodeID]
	})
	return list
}

func mergeWorkflowStates(original hexagon.MapState, outputs []hexagon.MapState) hexagon.MapState {
	merged := original.Clone().(hexagon.MapState)
	combinedOutputs := stringMapStateValue(merged, stateKeyNodeOutputs)
	combinedHandoffs := stringMapStateValue(merged, stateKeyNodeHandoffs)
	// C6：并行分支各自可能停用不同出边/标记不同死节点，合并时取并集。
	combinedDead := stringMapStateValue(merged, stateKeyDeadNodes)
	combinedDeadEdges := stringMapStateValue(merged, stateKeyDeactivatedEdges)
	var workflowOutputs []string

	for _, out := range outputs {
		for k, v := range stringMapStateValue(out, stateKeyNodeOutputs) {
			combinedOutputs[k] = v
		}
		for k, v := range stringMapStateValue(out, stateKeyNodeHandoffs) {
			combinedHandoffs[k] = v
		}
		for k, v := range stringMapStateValue(out, stateKeyDeadNodes) {
			combinedDead[k] = v
		}
		for k, v := range stringMapStateValue(out, stateKeyDeactivatedEdges) {
			combinedDeadEdges[k] = v
		}
		if v := stringStateValue(out, stateKeyWorkflowOutput); v != "" {
			workflowOutputs = append(workflowOutputs, v)
		}
	}

	merged.Set(stateKeyNodeOutputs, combinedOutputs)
	merged.Set(stateKeyNodeHandoffs, combinedHandoffs)
	merged.Set(stateKeyDeadNodes, combinedDead)
	merged.Set(stateKeyDeactivatedEdges, combinedDeadEdges)
	if len(workflowOutputs) > 0 {
		merged.Set(stateKeyWorkflowOutput, strings.Join(workflowOutputs, "\n\n"))
	}
	return merged
}

func stringMapStateValue(state hexagon.MapState, key string) map[string]string {
	if state == nil {
		return map[string]string{}
	}
	raw, _ := state.Get(key)
	// graph 的 deepCopy 在跨 stage/并行 clone 时把 map[string]string JSON 往返成
	// map[string]any，故两种类型都要容忍（否则跨 stage 后读取恒空——C6 分支状态丢失根因）。
	switch src := raw.(type) {
	case map[string]string:
		dst := make(map[string]string, len(src))
		for k, v := range src {
			dst[k] = v
		}
		return dst
	case map[string]any:
		dst := make(map[string]string, len(src))
		for k, v := range src {
			dst[k] = stringValue(v)
		}
		return dst
	default:
		return map[string]string{}
	}
}

func putStringMapStateValue(state hexagon.MapState, key, entryKey, entryValue string) hexagon.MapState {
	next := state.Clone().(hexagon.MapState)
	values := stringMapStateValue(next, key)
	values[entryKey] = entryValue
	next.Set(key, values)
	return next
}

func stringStateValue(state hexagon.MapState, key string) string {
	if state == nil {
		return ""
	}
	raw, _ := state.Get(key)
	s, _ := raw.(string)
	return s
}

func setStringStateValue(state hexagon.MapState, key, value string) hexagon.MapState {
	next := state.Clone().(hexagon.MapState)
	next.Set(key, value)
	return next
}

func renderTemplate(input string, state hexagon.MapState) string {
	if input == "" {
		return ""
	}
	outputs := stringMapStateValue(state, stateKeyNodeOutputs)
	replacements := []string{
		"{{input}}", stringStateValue(state, stateKeyInput),
		"{{previous}}", stringStateValue(state, stateKeyPrevious),
		"{{handoff_agent}}", "",
	}
	handoffs := stringMapStateValue(state, stateKeyNodeHandoffs)
	if len(handoffs) > 0 {
		var names []string
		for _, v := range handoffs {
			names = append(names, v)
		}
		sort.Strings(names)
		replacements[5] = strings.Join(names, ",")
	}
	input = workflowFieldTemplate.ReplaceAllStringFunc(input, func(token string) string {
		value, ok := workflowFieldValue(token, state)
		if !ok {
			return ""
		}
		if text, ok := value.(string); ok {
			return text
		}
		encoded, _ := json.Marshal(value)
		return string(encoded)
	})
	for id, value := range outputs {
		replacements = append(replacements, "{{node."+id+"}}", value)
	}
	return strings.NewReplacer(replacements...).Replace(input)
}

var workflowFieldTemplate = regexp.MustCompile(`\{\{node\.[^{}]+\.[^{}]+\}\}`)

func workflowFieldValue(token string, state hexagon.MapState) (any, bool) {
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(token, "{{node."), "}}"), ".")
	if len(parts) < 2 {
		return nil, false
	}
	var value any
	if json.Unmarshal([]byte(stringMapStateValue(state, stateKeyNodeOutputs)[parts[0]]), &value) != nil {
		return nil, false
	}
	for _, field := range parts[1:] {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = object[field]
		if !ok {
			return nil, false
		}
	}
	return value, true
}

func renderValue(v any, state hexagon.MapState) any {
	switch x := v.(type) {
	case string:
		if workflowFieldTemplate.FindString(x) == x {
			if value, ok := workflowFieldValue(x, state); ok {
				return value
			}
		}
		return renderTemplate(x, state)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = renderValue(val, state)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = renderValue(val, state)
		}
		return out
	default:
		return v
	}
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func stringValue(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func stringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return append([]string(nil), x...)
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s := stringValue(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if x == "" {
			return nil
		}
		parts := strings.Split(x, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

type agentrouterMessageAdapter struct {
	UserID     string
	Platform   string
	InstanceID string
	ChatID     string
	Content    string
	Metadata   map[string]string
}

func (m *agentrouterMessageAdapter) Message() *adapter.Message {
	return &adapter.Message{
		Platform:   adapter.PlatformAPI,
		UserID:     m.UserID,
		InstanceID: m.InstanceID,
		ChatID:     m.ChatID,
		Content:    m.Content,
		Metadata:   m.Metadata,
		Timestamp:  time.Now(),
	}
}
