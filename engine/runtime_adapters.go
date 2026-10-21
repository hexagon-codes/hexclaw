package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/ai-core/template"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexagon/observe/trace"
	hruntime "github.com/hexagon-codes/hexagon/runtime"
	"github.com/hexagon-codes/hexagon/runtime/middleware"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/messagecontent"
)

// runRuntimeAttempt 只对模型失败续接运行状态；目标模型输入仍由应用按现有配置重建。
func runRuntimeAttempt(ctx context.Context, runner *hruntime.DefaultRunner, req hruntime.Request, previous error, sink hruntime.EventSink) (*hruntime.Result, error) {
	if sink == nil {
		req.StreamMode = hruntime.StreamModeOff
	}
	var interrupted *hruntime.ProviderCallError
	if errors.As(previous, &interrupted) && interrupted.State != nil {
		copy := *interrupted
		copy.State = interrupted.State.Snapshot()
		copy.State.Attributes["hexclaw.current_input_user_ordinal"] = countContextUserMessages(req.Messages)
		for _, record := range copy.State.ToolCalls {
			labelToolResultEgress(ctx, record.Name)
		}
		return runner.Continue(ctx, req, &copy, sink)
	}
	if sink == nil {
		return runner.Run(ctx, req)
	}
	return runner.Stream(ctx, req, sink)
}

type runtimeProviderSelector struct {
	router interface {
		Fallback(exclude ...string) (hexagon.Provider, string, error)
	}
	// markUnhealthy 在回退时短期熔断失败 provider（BUG-20260711）。此前 runner 内建的
	// 单跳回退能换 provider 让本次对话成功，但**从不熔断**失败 provider——于是每条新消息
	// 都先打一次挂掉的默认 provider（429）再回退，"额度耗尽即云端整体变慢/不可用"的观感由此
	// 而来。仅对 isProviderUnavailableError（429/5xx/超时/连接失败）熔断，不误伤"工具不支持"
	// （那条走去工具降级、不换 provider）。nil 时不熔断（保持旧行为）。
	markUnhealthy   func(name, reason string)
	initialProvider hexagon.Provider
	initialName     string
	initialModel    string
	// initialSameProviderFallback 是首选模型硬失败时，同一 provider 内可用的默认模型。
	// 典型场景：K12 优先 glm-4.5 推理，但该模型 429；同一智谱的 glm-4v-flash 仍健康。
	initialSameProviderFallback string
	usedSameProviderFallback    bool
	explicitProvider            bool
	modelForProvider            func(string) string
	wrapProvider                func(hexagon.Provider, string, string) hexagon.Provider
	currentProvider             hexagon.Provider
	currentName                 string
	currentModel                string
	// tried 累积本次运行已尝试过的 provider 名，供多跳回退用 exclude 集合遍历所有健康
	// provider 一轮、防止回到已失败的 provider 造成死循环（BUG-20260711 Gap-2）。
	tried map[string]bool
}

func (s *runtimeProviderSelector) markTried(name string) {
	if name == "" {
		return
	}
	if s.tried == nil {
		s.tried = make(map[string]bool)
	}
	s.tried[name] = true
}

func (s *runtimeProviderSelector) excludeList() []string {
	names := make([]string, 0, len(s.tried))
	for n := range s.tried {
		names = append(names, n)
	}
	return names
}

func (s *runtimeProviderSelector) setCurrent(p hexagon.Provider, name, model string) {
	s.currentProvider = s.wrap(p, name, model)
	s.currentName = name
	s.currentModel = model
}

func (s *runtimeProviderSelector) Select(ctx context.Context, _ hruntime.Request) (hruntime.ProviderSelection, error) {
	// 首次进入用 initial；再次进入（engine 多跳回退重跑 runner）返回已 advance 的 current。
	if s.currentProvider == nil {
		if s.initialProvider == nil {
			return hruntime.ProviderSelection{}, hruntime.ErrNoProvider
		}
		s.setCurrent(s.initialProvider, s.initialName, s.initialModel)
	}
	s.markTried(s.currentName)
	selectedProvider := s.currentProvider
	if replyMetaSinkFrom(ctx) != nil {
		selectedProvider = preserveContextTokenCounter(&skillContextConsumedProvider{Provider: selectedProvider}, selectedProvider)
	}
	return hruntime.ProviderSelection{
		Provider: selectedProvider,
		Name:     s.currentName,
		Model:    s.currentModel,
	}, nil
}

// tripBreaker 对"provider 级不可用"错误短期熔断给定 provider（工具不支持不熔断）。
func (s *runtimeProviderSelector) tripBreaker(name string, cause error) {
	if s.markUnhealthy != nil && isProviderUnavailableError(cause) {
		s.markUnhealthy(name, causeReason(cause))
	}
}

// Fallback 现恒返回 ErrNoFallback：runner 的**内部单跳跨 provider fallback 已禁用**，多跳
// failover 完全交给 engine 外层循环（completeWithTools / processStreamRuntime 里的
// failoverAdvance 循环）。
//
// 修 BUG-20260712（option A）：runner 内部单跳换 provider 时用的是**传入的 messages**——即为
// 初始（本地）provider 构建、带 <memory-context> 的请求，无法在 runner 内按目标 provider 的
// locality 重建。于是本地超时后 runner 内部换到云端、复用带 memory 的请求 → 直接撞云 egress 拦截，
// 且该 egress 错误不算 provider 不可用 → 外层不再回退 → 整条链断。把跨 provider 回退上移到 engine
// 外层后，那里能用 rebuildRequestForFailover 起干净 egress 信封 + 盖目标 locality（云端不带 memory），
// 从根因上不让带 memory 的请求出云。熔断/exclude 记账由外层 failoverAdvance 承担。
func (s *runtimeProviderSelector) Fallback(context.Context, hruntime.ProviderSelection, error) (hruntime.ProviderSelection, error) {
	return hruntime.ProviderSelection{}, hruntime.ErrNoFallback
}

// failoverAdvance 由 engine 在一次 runner.Run/Stream 返回"provider 级不可用"错误后调用，
// 把当前 provider 熔断 + 遍历到下一个尚未尝试的健康 provider（多跳回退，Gap-2）。返回是否
// 成功推进到一个新 provider——true 时调用方用同一 runner+selector 重跑（Select 会返回新 current）。
// 显式 pin / 无 router / 无更多健康 provider 时返回 false。
func (s *runtimeProviderSelector) failoverAdvance(cause error) bool {
	if s.explicitProvider || s.router == nil {
		return false
	}
	// 模型级失败不等于整个 provider 失败。首选推理模型不可用时，先用同 provider 的
	// 配置默认模型兜底；只有默认模型也失败，才熔断 provider 并跨 provider 切换。
	if !s.usedSameProviderFallback &&
		s.currentName == s.initialName &&
		s.initialProvider != nil &&
		s.initialSameProviderFallback != "" &&
		s.initialSameProviderFallback != s.currentModel {
		s.usedSameProviderFallback = true
		s.setCurrent(s.initialProvider, s.initialName, s.initialSameProviderFallback)
		return true
	}
	s.tripBreaker(s.currentName, cause)
	s.markTried(s.currentName)
	p, name, err := s.router.Fallback(s.excludeList()...)
	if err != nil || p == nil || name == "" || s.tried[name] {
		return false
	}
	model := name
	if s.modelForProvider != nil {
		model = s.modelForProvider(name)
	}
	s.setCurrent(p, name, model)
	return true
}

func (s *runtimeProviderSelector) wrap(provider hexagon.Provider, name, model string) hexagon.Provider {
	if s.wrapProvider == nil || provider == nil {
		return provider
	}
	return s.wrapProvider(provider, name, model)
}

// 只观察现有 Provider 请求，不改变正文、路由、次数或错误分类。
type skillContextConsumedProvider struct{ hexagon.Provider }

func (p *skillContextConsumedProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	response, err := p.Provider.Complete(ctx, req)
	if err == nil && response != nil {
		confirmConsumedToolSkillContexts(ctx, req.Messages)
	}
	return response, err
}

func (p *skillContextConsumedProvider) Stream(ctx context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	stream, err := p.Provider.Stream(ctx, req)
	if err == nil && stream != nil {
		confirmConsumedToolSkillContexts(ctx, req.Messages)
	}
	return stream, err
}

func (s *runtimeProviderSelector) Current() (hexagon.Provider, string, string) {
	if s.currentProvider == nil {
		return s.initialProvider, s.initialName, s.initialModel
	}
	return s.currentProvider, s.currentName, s.currentModel
}

type thinkingRecoveryTracker struct {
	timeout   atomic.Bool
	recovered atomic.Bool
}

type thinkingBoundProvider struct {
	engine       *ReActEngine
	provider     hexagon.Provider
	providerName string
	modelName    string
	tracker      *thinkingRecoveryTracker
}

func (p *thinkingBoundProvider) Name() string { return p.provider.Name() }

func (p *thinkingBoundProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	resp, thinkingTimedOut, err := p.engine.completeWithThinkingTimeout(ctx, p.provider, p.providerName, p.modelName, req)
	if err == nil || !thinkingTimedOut {
		return resp, err
	}
	if p.tracker != nil {
		p.tracker.timeout.Store(true)
	}
	if recovered, ok := p.engine.recoverReasoningOnly(ctx, p.provider, req); ok {
		if p.tracker != nil {
			p.tracker.recovered.Store(true)
		}
		return &llm.CompletionResponse{Content: recovered}, nil
	}
	return resp, err
}

func (p *thinkingBoundProvider) Stream(ctx context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	return p.provider.Stream(ctx, req)
}

func (p *thinkingBoundProvider) Models() []llm.ModelInfo {
	return p.provider.Models()
}

func (p *thinkingBoundProvider) CountTokens(messages []llm.Message) (int, error) {
	return p.provider.CountTokens(messages)
}

// maxIdenticalToolCalls is how many times the exact same (tool, arguments)
// call is allowed within one run before the guard short-circuits it. Weak
// models (e.g. glm-4-flash) otherwise loop on an identical browser fetch until
// the token budget is exhausted instead of using the result they already have
// (BUG-20260613).
const maxIdenticalToolCalls = 2

const repeatToolCallBlockedError = "repeat_tool_call_blocked"

func maxIdenticalToolCallsFor(toolName string) int {
	if runtimeToolSideEffect(toolName) == hruntime.SideEffectUnsafe {
		return 1
	}
	return maxIdenticalToolCalls
}

func runtimeToolSideEffect(toolName string) hruntime.ToolSideEffect {
	switch toolName {
	case "app_query", "browser", "knowledge_search", "list_agents", "search", "session_search", "weather", "web_search":
		return hruntime.SideEffectReadOnly
	default:
		return hruntime.SideEffectUnsafe
	}
}

// newRuntimeToolExecutor builds a per-run executor with a fresh repeat guard.
func newRuntimeToolExecutor(executor *ToolExecutor) *runtimeToolExecutor {
	return &runtimeToolExecutor{executor: executor, callCounts: make(map[string]int), unknownActions: make(map[string]hruntime.ToolResult)}
}

type runtimeToolExecutor struct {
	executor *ToolExecutor
	// 流式请求用现有工具心跳续传活动帧；非流式调用不绑定回调。
	onActivity func(context.Context) error

	mu             sync.Mutex
	callCounts     map[string]int // (tool+args) signature → times seen this run
	unknownActions map[string]hruntime.ToolResult
}

func (e *runtimeToolExecutor) Execute(ctx context.Context, call llm.ToolCall) (out hruntime.ToolResult, runErr error) {
	replayed := false
	defer func() { recordRuntimeToolReceipt(ctx, call, string(out.Status), out.Raw, replayed) }()
	started := time.Now()
	argumentsForLog := call.Arguments
	if strings.Contains(strings.ToLower(argumentsForLog), "base64") {
		argumentsForLog = "[omitted: contains base64 media]"
	}
	trace.L(ctx).Info("runtime tool call started", "stage", "tool_execute", "tool", call.Name, "tool_call_id", call.ID, "arguments", argumentsForLog)
	var args map[string]any
	if call.Arguments != "" {
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			trace.L(ctx).Warn("runtime tool call failed", "stage", "tool_execute", "tool", call.Name, "tool_call_id", call.ID, "arguments", argumentsForLog, "reason", "invalid_arguments", "err", err, "elapsed_ms", time.Since(started).Milliseconds())
			msg := fmt.Sprintf("Error: invalid arguments for tool %q: %s", call.Name, err.Error())
			return hruntime.ToolResult{Content: msg, Error: err.Error(), Status: hruntime.ToolStatusError, DurationMs: time.Since(started).Milliseconds()}, nil
		}
	}
	if e.executor == nil {
		trace.L(ctx).Warn("runtime tool call failed", "stage", "tool_execute", "tool", call.Name, "tool_call_id", call.ID, "arguments", argumentsForLog, "reason", "executor_unavailable", "err", "tool executor not available", "elapsed_ms", time.Since(started).Milliseconds())
		return hruntime.ToolResult{Content: "Error: tool executor not available", Error: "tool executor not available", Status: hruntime.ToolStatusError, DurationMs: time.Since(started).Milliseconds()}, nil
	}
	canonicalArgs, _ := json.Marshal(args)
	unknownKey := call.Name + "\x00" + string(canonicalArgs)
	e.mu.Lock()
	previous, unknown := e.unknownActions[unknownKey]
	e.mu.Unlock()
	if unknown {
		replayed = true
		previous.Content += "\nThe original POST outcome is unknown. Do not resend it automatically; verify the original operation first."
		return previous, nil
	}

	// Loop breaker: if the model repeats the exact same call, stop executing
	// it and nudge it to answer from the result it already has.
	sig := call.Name + "\x00" + call.Arguments
	e.mu.Lock()
	e.callCounts[sig]++
	count := e.callCounts[sig]
	e.mu.Unlock()
	if count > maxIdenticalToolCallsFor(call.Name) {
		trace.L(ctx).Warn("tool-loop repeat guard tripped",
			"stage", "tool_execute", "tool", call.Name, "tool_call_id", call.ID, "arguments", argumentsForLog,
			"reason", "repeat_guard", "err", repeatToolCallBlockedError, "identical_calls", count, "elapsed_ms", time.Since(started).Milliseconds())
		msg := fmt.Sprintf("You have already called %q with these exact arguments %d times and received the same result above. Do NOT call it again. Produce your final answer now using the information you already have; if it is insufficient, explain what is missing and stop.", call.Name, count-1)
		return hruntime.ToolResult{Content: msg, Raw: repeatToolCallBlockedError, Status: hruntime.ToolStatusError}, nil
	}

	// 已进入真实执行阶段即续传一次活动，不能等第一个周期心跳才续期。
	if e.onActivity != nil {
		if err := e.onActivity(ctx); err != nil {
			return hruntime.ToolResult{}, err
		}
	}
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	heartbeatStopped := make(chan struct{})
	var stopHeartbeatOnce sync.Once
	finishHeartbeat := func() {
		stopHeartbeatOnce.Do(func() {
			stopHeartbeat()
			select {
			case <-heartbeatStopped:
			case <-time.After(time.Second):
				trace.L(ctx).Warn("runtime tool heartbeat stop timed out", "stage", "tool_execute", "tool", call.Name, "tool_call_id", call.ID, "err", "heartbeat goroutine did not stop within timeout", "timeout_ms", int64(time.Second/time.Millisecond))
			}
		})
	}
	defer finishHeartbeat()
	go func() {
		defer close(heartbeatStopped)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				trace.L(ctx).Info("runtime tool call heartbeat", "stage", "tool_execute", "tool", call.Name, "tool_call_id", call.ID, "elapsed_ms", time.Since(started).Milliseconds())
				if e.onActivity != nil {
					if err := e.onActivity(heartbeatCtx); err != nil {
						return
					}
				}
			}
		}
	}()
	ctx = withRuntimeInvocationSelection(ctx)
	result, raw, err := e.executor.ExecuteResult(ctx, call.Name, args)
	finishHeartbeat()
	if err != nil {
		trace.L(ctx).Warn("runtime tool call failed", "stage", "tool_execute", "tool", call.Name, "tool_call_id", call.ID, "arguments", argumentsForLog, "reason", "execution_error", "err", err, "elapsed_ms", time.Since(started).Milliseconds())
		msg := fmt.Sprintf("Error executing tool %q: %s", call.Name, err.Error())
		if result != "" {
			msg += "\n" + result
		}
		msg = toolExecutionExplanation(msg, raw)
		outcome := hruntime.ToolResult{Content: msg, Raw: raw, Error: err.Error(), Status: hruntime.ToolStatusError, DurationMs: time.Since(started).Milliseconds()}
		if detail := toolExecutionDetail(raw); detail != nil && detail.Action == "post" && detail.Outcome == "unknown" {
			e.mu.Lock()
			e.unknownActions[unknownKey] = outcome
			e.mu.Unlock()
		}
		return outcome, nil
	}
	trace.L(ctx).Info("runtime tool call completed", "stage", "tool_execute", "tool", call.Name, "tool_call_id", call.ID, "elapsed_ms", time.Since(started).Milliseconds())
	return hruntime.ToolResult{Content: toolExecutionExplanation(result, raw), Raw: raw, Status: hruntime.ToolStatusSuccess, DurationMs: time.Since(started).Milliseconds()}, nil
}

func (e *runtimeToolExecutor) SideEffectOf(call llm.ToolCall) hruntime.ToolSideEffect {
	return runtimeToolSideEffect(call.Name)
}

type runtimeRepeatGuardStopMiddleware struct{}

func (runtimeRepeatGuardStopMiddleware) BeforeLLM(context.Context, *hruntime.State) error {
	return nil
}

func (runtimeRepeatGuardStopMiddleware) AfterLLM(context.Context, *hruntime.State, *llm.CompletionResponse) error {
	return nil
}

func (runtimeRepeatGuardStopMiddleware) BeforeTool(context.Context, *hruntime.State, llm.ToolCall) error {
	return nil
}

func (runtimeRepeatGuardStopMiddleware) AfterTool(_ context.Context, state *hruntime.State, _ llm.ToolCall, result hruntime.ToolResult) error {
	if state != nil && result.Raw == repeatToolCallBlockedError {
		state.Final = true
		state.FinalText = result.Content
	}
	return nil
}

func (runtimeRepeatGuardStopMiddleware) Finalize(context.Context, *hruntime.State) error {
	return nil
}

type runtimeBudgetMiddleware struct {
	budget       *BudgetController
	providerName string
	modelName    string
}

func (m runtimeBudgetMiddleware) BeforeLLM(ctx context.Context, state *hruntime.State) error {
	if m.budget == nil {
		return nil
	}
	if err := m.budget.Check(); err != nil {
		trace.L(ctx).Warn("预算耗尽", "turn", state.Turn, "err", err)
		return err
	}
	return nil
}

func (m runtimeBudgetMiddleware) AfterLLM(_ context.Context, state *hruntime.State, resp *llm.CompletionResponse) error {
	if m.budget == nil || resp == nil || resp.Usage.TotalTokens <= 0 {
		return nil
	}
	providerName := m.providerName
	modelName := m.modelName
	if state != nil {
		if v, ok := state.Attributes["provider"].(string); ok && v != "" {
			providerName = v
		}
		if v, ok := state.Attributes["model"].(string); ok && v != "" {
			modelName = v
		}
	}
	m.budget.RecordTokens(resp.Usage.TotalTokens)
	if cost := EstimateCost(providerName, modelName, resp.Usage.PromptTokens, resp.Usage.CompletionTokens); cost > 0 {
		m.budget.RecordCost(cost)
	}
	return nil
}

func (runtimeBudgetMiddleware) BeforeTool(context.Context, *hruntime.State, llm.ToolCall) error {
	return nil
}

func (runtimeBudgetMiddleware) AfterTool(context.Context, *hruntime.State, llm.ToolCall, hruntime.ToolResult) error {
	return nil
}

func (runtimeBudgetMiddleware) Finalize(context.Context, *hruntime.State) error { return nil }

// runtimeCompaction 将应用触发条件和模型策略注入共享压缩中间件。
func runtimeCompaction(provider hexagon.Provider, sessionID string, currentProvider func() hexagon.Provider) hruntime.Middleware {
	return hruntime.MiddlewareFuncSet{BeforeLLMFunc: func(ctx context.Context, state *hruntime.State) error {
		if state == nil {
			return nil
		}
		if state.Attributes == nil {
			state.Attributes = map[string]any{}
		}
		const inputOrdinalKey = "hexclaw.current_input_user_ordinal"
		ordinal, initialized := state.Attributes[inputOrdinalKey].(int)
		if !initialized {
			// 身份来自工具执行前的原始请求，不能把后续工具图片误当当前输入。
			ordinal = countContextUserMessages(state.Request.Messages)
		}
		inputIndex := contextUserMessageIndex(state.Messages, ordinal)
		compaction := middleware.Compaction{
			ShouldCompact: func(messages []llm.Message) bool {
				return contextMessageBytes(messages) >= contextCompressCharThreshold || middleware.HasCompactableToolMedia(messages, 3)
			},
			Count: EstimateMessagesTokens,
			Compact: func(ctx context.Context, messages []llm.Message) []llm.Message {
				selected := provider
				if currentProvider != nil {
					if current := currentProvider(); current != nil {
						selected = current
					}
				}
				compressed, nextInput := compressContextWithInput(ctx, messages, selected, sessionID, inputIndex)
				inputIndex = nextInput
				return compressed
			},
		}
		err := compaction.BeforeLLM(ctx, state)
		ordinal = 0
		if inputIndex >= 0 {
			ordinal = countContextUserMessages(state.Messages[:inputIndex+1])
		}
		state.Attributes[inputOrdinalKey] = ordinal
		return err
	}}
}

func runtimeToolCallsToAdapter(calls []hruntime.ToolCallRecord, origins ...map[string]*adapter.ToolOrigin) []adapter.ToolCall {
	result := make([]adapter.ToolCall, 0, len(calls))
	for _, c := range calls {
		var origin *adapter.ToolOrigin
		if len(origins) > 0 {
			origin = origins[0][c.Name]
		}
		execution := sandboxExecutionForDisplay(c.Name, c.Result.Content)
		if origin != nil && origin.Kind == "mcp" {
			execution = nil
		}
		displayContent := c.Result.Content
		if execution != nil {
			displayContent = displayContent[:strings.LastIndex(displayContent, "\n[hexclaw_sandbox_result]\n")]
		}
		displayResult := truncateToolResultForDisplay(c.Name, toolExecutionDisplayContent(displayContent, c.Result.Raw))
		status := string(c.Result.Status)
		// 兼容执行前拦截回执：结束事件不等于工具成功，沿用既有错误信封契约。
		if strings.HasPrefix(strings.TrimSpace(c.Result.Content), "Error executing tool \"") {
			status = "error"
		}
		if execution != nil && (execution.Status != "success" || execution.ExitCode != 0 || execution.Timeout) {
			status = "error"
		}
		if execution != nil && execution.Status == "success" && status == "" {
			status = "success"
		}
		result = append(result, adapter.ToolCall{
			Detail:    toolExecutionDetail(c.Result.Raw),
			ID:        c.ID,
			Name:      c.Name,
			Origin:    origin,
			Arguments: c.Arguments,
			// 多 Agent 工具(orchestrate/spawn)放宽展示上限以保全尾部 hexclaw-subagents 哨兵块。
			Result:         displayResult,
			MessageContent: canonicalProducerContent(messagecontent.ProducerTool, displayResult, "und"),
			// 透传 hexagon 框架在执行点产出的执行真相（状态/耗时），客户端免去正文嗅探。
			Status:     status,
			Execution:  execution,
			DurationMs: c.Result.DurationMs,
		})
	}
	return result
}

// runtimeBlocksToAdapter 把 hexagon 产出的有序内容块流（template.Blocks）转成 wire 形态。
// 块只承载**顺序**（text 片段 + tool_use 位置）；富数据（status/duration）仍走扁平 ToolCalls，
// 前端按块序渲染、在 tool_use 处用 id 取完整数据。tool_result 的 toolName 由同 id 的 tool_use 回填。
func runtimeBlocksToAdapter(blocks template.Blocks, projectedCalls ...[]adapter.ToolCall) []adapter.Block {
	if len(blocks) == 0 {
		return nil
	}
	names := make(map[string]string, len(blocks))
	outputs := make(map[string]string)
	for _, calls := range projectedCalls {
		for _, call := range calls {
			outputs[call.ID] = call.Result
		}
	}
	for _, b := range blocks {
		if b.Type == template.BlockToolUse {
			names[b.ID] = b.Name
		}
	}
	out := make([]adapter.Block, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case template.BlockText:
			out = append(out, adapter.Block{
				Type:           "text",
				Text:           b.Text,
				MessageContent: canonicalProducerContent(messagecontent.ProducerChat, b.Text, "und"),
			})
		case template.BlockToolUse:
			out = append(out, adapter.Block{Type: "tool_use", ID: b.ID, Name: b.Name, Input: b.Input})
		case template.BlockToolResult:
			output := truncateToolResultForDisplay(names[b.ToolUseID], b.Output)
			if projected, ok := outputs[b.ToolUseID]; ok {
				output = projected
			}
			out = append(out, adapter.Block{
				Type:           "tool_result",
				MessageContent: canonicalProducerContent(messagecontent.ProducerTool, output, "und"),
				ToolUseID:      b.ToolUseID,
				ToolName:       names[b.ToolUseID],
				Output:         output,
				IsError:        b.IsError,
			})
		}
	}
	return out
}
