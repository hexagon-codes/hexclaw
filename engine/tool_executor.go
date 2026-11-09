package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"encoding/json"

	coretool "github.com/hexagon-codes/ai-core/tool"
	"github.com/hexagon-codes/hexagon/observe/events"
	"github.com/hexagon-codes/hexagon/observe/trace"
	hruntime "github.com/hexagon-codes/hexagon/runtime"
	"github.com/hexagon-codes/toolkit/util/idgen"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/featureflag"
	"github.com/hexagon-codes/hexclaw/internal/externaltools"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/knowledge"
	"github.com/hexagon-codes/hexclaw/mcp"
	"github.com/hexagon-codes/hexclaw/skill"
)

// ToolExecutor executes tool calls with hook chain support.
//
// Resolution order: Skill registry → MCP Manager.
// Hook chain: beforeHooks → execute → afterHooks.
type ToolExecutor struct {
	skills          *skill.DefaultRegistry
	mcpMgr          *mcp.Manager
	beforeHooks     []BeforeToolHook
	afterHooks      []AfterToolHook
	executionPolicy skill.ExecutionPolicySource
	externalTools   *externaltools.Service
	mcpDispatch     func(context.Context, string, string, map[string]any) (coretool.Result, error)
	runFinalizer    func(context.Context, *adapter.Message)
}

func (e *ToolExecutor) SetExecutionPolicySource(source skill.ExecutionPolicySource) {
	e.executionPolicy = source
}
func (e *ToolExecutor) SetExternalTools(service *externaltools.Service) { e.externalTools = service }
func (e *ToolExecutor) SetMCPDispatch(dispatch func(context.Context, string, string, map[string]any) (coretool.Result, error)) {
	e.mcpDispatch = dispatch
}
func (e *ToolExecutor) SetRunFinalizer(finalize func(context.Context, *adapter.Message)) {
	e.runFinalizer = finalize
}

// NewToolExecutor creates a new executor.
func NewToolExecutor(skills *skill.DefaultRegistry, mcpMgr *mcp.Manager) *ToolExecutor {
	return &ToolExecutor{skills: skills, mcpMgr: mcpMgr}
}

// AddHook adds a hook that implements BeforeToolHook and/or AfterToolHook.
func (e *ToolExecutor) AddHook(h any) {
	if permission, ok := h.(*PermissionHook); ok && e.executionPolicy == nil {
		e.executionPolicy = permission.EffectiveExecutionPolicy
	}
	if bh, ok := h.(BeforeToolHook); ok {
		e.beforeHooks = append(e.beforeHooks, bh)
	}
	if ah, ok := h.(AfterToolHook); ok {
		e.afterHooks = append(e.afterHooks, ah)
	}
}

// Execute runs a tool call through the hook chain.
//
//  1. Find the tool (Skill first, then MCP)
//  2. Run beforeHooks (any can abort by returning error)
//  3. Execute the tool
//  4. Run afterHooks (can modify result, e.g. truncation)
type toolRawResultContextKey struct{}
type toolRawResultCapture struct{ value any }

// ExecuteResult 复用同一执行与钩子链，单独保留未截断的结构化结果。
func (e *ToolExecutor) ExecuteResult(ctx context.Context, toolName string, args map[string]any) (string, any, error) {
	capture := &toolRawResultCapture{}
	content, err := e.Execute(context.WithValue(ctx, toolRawResultContextKey{}, capture), toolName, args)
	var executionErr *skill.ExecutionError
	if errors.As(err, &executionErr) {
		if result, ok := capture.value.(*skill.Result); ok && result != nil {
			result.Detail = &executionErr.Detail
		} else {
			capture.value = &skill.Result{Content: content, Data: capture.value, Detail: &executionErr.Detail}
		}
	}
	if capture.value == nil {
		capture.value = content
	}
	return content, capture.value, err
}

func (e *ToolExecutor) Execute(ctx context.Context, toolName string, args map[string]any) (result string, err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if _, ok := ctx.Value(toolRawResultContextKey{}).(*toolRawResultCapture); !ok {
		ctx = context.WithValue(ctx, toolRawResultContextKey{}, &toolRawResultCapture{})
	}
	if e.executionPolicy != nil {
		ctx = skill.WithExecutionPolicySource(ctx, e.executionPolicy)
	}
	if err := workflowToolAllowed(workflowStepOptions(ctx), toolName, args); err != nil {
		return "", err
	}
	// 排查用：逐个工具调用打点（开始 + 完成/失败 + 耗时 + 结果规模）。只记 arg 的 key，不记 value，
	// 避免把敏感/超长参数写进日志。工具循环里模型到底调了哪些工具、成没成、多慢，一目了然。
	toolStart := time.Now()
	trace.L(ctx).Info("工具调用开始", "tool", toolName, "arg_keys", argKeysForLog(args))
	defer func() {
		trace.L(ctx).Info("工具调用完成", "tool", toolName,
			"ok", err == nil, "result_bytes", len(result),
			"elapsed_ms", time.Since(toolStart).Milliseconds(), "err", errStringForLog(err))
	}()
	call := &ToolCallInfo{
		Name:      toolName,
		Arguments: args,
	}

	// 1. Try Skill registry — direct name first, then derived slug for skills
	//    whose original name isn't a valid LLM identifier (e.g. Chinese names).
	if e.skills != nil {
		s, ok := e.skills.Get(toolName)
		if !ok {
			s, ok = e.resolveSkillBySlug(toolName)
		}
		if ok {
			call.Source = "skill"
			skillName := s.Name()
			trace.L(ctx).Info("Skill execution resolved", "tool", toolName, "skill", skillName, "routed_agent", skill.CurrentInvocation(ctx).RoutedAgentName,
				"pipeline_enabled", featureflag.Enabled(ctx, skill.FlagSkillPipelineV1))
			// v0.4.0 G2：flag skill.pipeline.v1 ON 时 Skill 走 7 阶段 pipeline，
			// 触发 Loading / Verification / Persistence / Improvement 钩子；
			// flag OFF 退化到直接 Execute（与 v0.3 行为一致）。
			if featureflag.Enabled(ctx, skill.FlagSkillPipelineV1) {
				return e.executeWithHooks(ctx, call, func(ctx context.Context) (string, error) {
					return e.runSkillViaPipeline(ctx, skillName, call.Arguments)
				})
			}
			return e.executeWithHooks(ctx, call, func(ctx context.Context) (string, error) {
				result, err := s.Execute(ctx, call.Arguments)
				captureSkillRawResult(ctx, result)
				captureCodeExecutionReceipt(ctx, skillName, result)
				if err != nil {
					if result != nil {
						if skillName == "media_generate" {
							// 媒体部分成功仍需交付已登记资产，错误状态继续向上传递。
							stampToolReplyMeta(ctx, result.Metadata)
						}
						return result.Content, err
					}
					return "", err
				}
				captureSkillRawResult(ctx, result)
				if skillName == "knowledge_search" {
					if hits, ok := result.Data.([]knowledge.SearchHit); ok {
						recordKnowledgeHits(ctx, hits)
					}
				}
				// BUG-1：skill 结构化 reply-safe 元数据（如 record chip）经 ctx sink 带到 reply。
				stampToolReplyMeta(ctx, result.Metadata)
				return result.Content, nil
			})
		}
	}

	// MCP 内部调用标识只用于路由，既有钩子继续按上游原名识别功能。
	if e.mcpMgr != nil {
		for _, info := range e.mcpMgr.ListToolInfos() {
			if info.Name == toolName {
				call.Name = info.OriginalName
				call.ServerName = info.ServerName
				break
			}
		}
	}

	// 2. Prevent MCP tools from shadowing builtin skill names
	if e.skills != nil && e.isBuiltinSkillName(call.Name) {
		return "", fmt.Errorf("tool %q is a reserved builtin skill name", toolName)
	}

	// 3. Try MCP Manager
	if e.mcpMgr != nil {
		call.Source = "mcp"
		if call.ServerName == "" {
			return "", fmt.Errorf("tool %q has no ready MCP server", toolName)
		}
		return e.executeWithHooks(ctx, call, func(ctx context.Context) (string, error) {
			var result coretool.Result
			var owner string
			var err error
			if e.mcpDispatch != nil && call.ServerName != "" {
				owner = call.ServerName
				result, err = e.mcpDispatch(ctx, owner, call.Name, call.Arguments)
			} else {
				result, owner, err = e.mcpMgr.CallToolResultWithOwner(ctx, toolName, call.Arguments)
			}
			call.ServerName = owner
			var existingExecutionErr *skill.ExecutionError
			if err != nil && result.Error != "" && !errors.As(err, &existingExecutionErr) {
				err = &skill.ExecutionError{Detail: skill.ToolExecutionDetail{Phase: "response", Outcome: "failure", Reason: "upstream_tool_error", ResponseReceived: true}, Err: err}
			}
			if capture, ok := ctx.Value(toolRawResultContextKey{}).(*toolRawResultCapture); ok {
				capture.value = result.Output
			}
			if projected, ok := result.Output.(interface{ ToolText() string }); ok {
				return projected.ToolText(), err
			}
			return result.String(), err
		})
	}

	return "", fmt.Errorf("tool '%s' not found in skills or MCP servers", toolName)
}

// argKeysForLog 返回工具参数的 key 列表（不含 value，防敏感/超长内容进日志）。
func argKeysForLog(args map[string]any) []string {
	if len(args) == 0 {
		return nil
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	return keys
}

// errStringForLog nil-safe 错误转字符串（成功时为空串，日志字段不显噪声）。
func errStringForLog(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// resolveSkillBySlug finds the skill whose derived LLM tool slug matches name,
// so a non-ASCII-named skill exposed under a slug still routes on tool calls.
func (e *ToolExecutor) resolveSkillBySlug(name string) (skill.Skill, bool) {
	if e.skills == nil {
		return nil, false
	}
	for _, s := range e.skills.All() {
		if llmToolNameSlug(s.Name()) == name {
			return s, true
		}
	}
	return nil, false
}

// HasTool checks whether a tool exists in any source.
func (e *ToolExecutor) HasTool(toolName string) bool {
	if e.skills != nil {
		if _, ok := e.skills.Get(toolName); ok {
			return true
		}
		if _, ok := e.resolveSkillBySlug(toolName); ok {
			return true
		}
	}
	if e.mcpMgr != nil {
		for _, info := range e.mcpMgr.ListToolInfos() {
			if info.Name == toolName {
				return true
			}
		}
	}
	return false
}

// isBuiltinSkillName checks whether the given tool name is registered as a builtin skill.
func (e *ToolExecutor) isBuiltinSkillName(name string) bool {
	_, ok := e.skills.Get(name)
	return ok
}

// AuthorizeToolDispatch 为后续物理派发复用同一完整钩子链。
func (e *ToolExecutor) AuthorizeToolDispatch(ctx context.Context, call *ToolCallInfo) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return ctx, err
	}
	if e.executionPolicy != nil {
		ctx = skill.WithExecutionPolicySource(ctx, e.executionPolicy)
	}
	for {
		policy := skill.CurrentExecutionPolicy(ctx)
		for _, hook := range sortBeforeHooks(e.beforeHooks) {
			if err := hook.BeforeToolCall(ctx, call); err != nil {
				return ctx, fmt.Errorf("tool call blocked by hook: %w", err)
			}
		}
		if skill.CurrentExecutionPolicy(ctx) == policy {
			return skill.WithToolDispatchGrant(ctx, call.Name, policy), nil
		}
		if ctx.Err() != nil {
			return ctx, ctx.Err()
		}
	}
}

func (e *ToolExecutor) executeWithHooks(ctx context.Context, call *ToolCallInfo, exec func(context.Context) (string, error)) (string, error) {
	if e.executionPolicy != nil {
		ctx = skill.WithExecutionPolicySource(ctx, e.executionPolicy)
	}
	v2 := featureflag.Enabled(ctx, FlagToolLifecycleV2)

	afterHooks := e.afterHooks
	if v2 {
		afterHooks = sortAfterHooks(afterHooks)
	}

	// 审批可能等待用户，保存新档位后必须重新按新版本求值。
	authorize := func() (skill.ExecutionPolicy, error) {
		var err error
		ctx, err = e.AuthorizeToolDispatch(ctx, call)
		policy, _ := skill.ToolDispatchPolicy(ctx, call.Name)
		return policy, err
	}
	authorizedPolicy, gateErr := authorize()
	if gateErr != nil {
		return "", gateErr
	}

	// 外部执行记录在权限处理后、真实派发前提交。HTTP内部重试另有逐次回执。
	ctx = skill.WithToolDispatchGrant(ctx, call.Name, authorizedPolicy)
	var attempt *externaltools.Attempt
	if e.externalTools != nil && (call.Source == "mcp" || call.Name == "shell" || call.Name == "code_exec" || call.Name == "browser" || call.Name == "computer_use" || call.Name == "browser_session" || call.Name == "mcp_resources" || call.Name == "mcp_prompts" || (call.Name == "file_ops" && call.Arguments["action"] == "attach")) {
		if retainErr := mediaflow.RetainRequest(ctx); retainErr != nil {
			return "", retainErr
		}
		inv := skill.CurrentInvocation(ctx)
		if inv.ToolCallID == "" {
			inv.ToolCallID = "direct:" + idgen.ShortID()
			ctx = skill.WithInvocationContext(ctx, inv)
		}
		var prepareErr error
		for policyAttempt := 0; policyAttempt < 2; policyAttempt++ {
			ctx, attempt, prepareErr = e.externalTools.Begin(ctx, externaltools.Intent{Kind: "tool_call", ToolName: call.Name, ServerName: call.ServerName, Input: call.Arguments}, false)
			if prepareErr != nil {
				return "", prepareErr
			}
			if attempt.Record.Phase != "prepared" {
				return replayExternalReceipt(ctx, attempt)
			}
			if attempt.Record.PolicyProfile != authorizedPolicy.Profile || attempt.Record.PolicyRevision != authorizedPolicy.Revision {
				prepareErr = errors.Join(externaltools.ErrPolicyChanged, e.externalTools.Finish(attempt, "observed", "failed", "policy_changed_before_dispatch", nil, false))
			} else {
				prepareErr = e.externalTools.Dispatch(ctx, attempt)
			}
			if !errors.Is(prepareErr, externaltools.ErrPolicyChanged) {
				break
			}
			authorizedPolicy, gateErr = authorize()
			if gateErr != nil {
				return "", gateErr
			}
			ctx = skill.WithToolDispatchGrant(ctx, call.Name, authorizedPolicy)
		}
		if prepareErr != nil {
			return "", prepareErr
		}
	}
	// Execute
	startedAt := time.Now()
	content, err := exec(ctx)
	// 观察字节与归属在本次回执封存前登记，终态记录保持不可覆盖。
	if capture, ok := ctx.Value(toolRawResultContextKey{}).(*toolRawResultCapture); ok {
		capture.value, err = e.captureToolObservations(ctx, call.Name, capture.value, err)
	}
	if attempt != nil {
		phase, outcome, reason := "observed", "success", ""
		if err != nil {
			outcome = "failed"
			reason = "execution_failed"
		}
		var detail *skill.ToolExecutionDetail
		var executionErr *skill.ExecutionError
		if errors.As(err, &executionErr) {
			detail = &executionErr.Detail
		}
		if capture, ok := ctx.Value(toolRawResultContextKey{}).(*toolRawResultCapture); ok {
			if raw, ok := capture.value.(*skill.Result); ok && raw != nil && raw.Detail != nil {
				detail = raw.Detail
			}
		}
		metadata := map[string]any{"content_bytes": len(content)}
		if capture, ok := ctx.Value(toolRawResultContextKey{}).(*toolRawResultCapture); ok {
			if raw, ok := capture.value.(*skill.Result); ok && raw != nil {
				if facts, ok := raw.Data.(interface{ ExternalExecutionFacts() map[string]any }); ok {
					metadata["execution"] = facts.ExternalExecutionFacts()
				}
				if data, ok := raw.Data.(map[string]any); ok {
					facts := map[string]any{}
					for _, key := range []string{"method", "status", "bytes_read", "response_complete", "artifact", "pending_artifact", "transport_replay"} {
						if value, ok := data[key]; ok {
							facts[key] = value
						}
					}
					if len(facts) > 0 {
						metadata["execution"] = facts
					}
				}
			}
		}
		var pendingArtifact *externaltools.ArtifactRegistrationError
		if errors.As(err, &pendingArtifact) {
			metadata["pending_artifacts"] = []externaltools.Artifact{pendingArtifact.Artifact}
		}
		if detail != nil {
			metadata["detail"] = detail
			outcome = detail.Outcome
			reason = detail.Reason
		}
		if detail == nil && err != nil && call.Source == "mcp" {
			phase = "unknown"
			outcome = "unknown"
			reason = "mcp_result_unconfirmed"
		}
		if outcome == "unknown" || (detail == nil && errors.Is(err, context.Canceled) && runtimeToolSideEffect(call.Name) != hruntime.SideEffectReadOnly) {
			phase = "unknown"
			outcome = "unknown"
		}
		if finishErr := e.externalTools.Finish(attempt, phase, outcome, reason, metadata, false); finishErr != nil {
			err = errors.Join(err, fmt.Errorf("persist external execution receipt: %w", finishErr))
		}
		if detail == nil {
			publicOutcome := outcome
			if publicOutcome == "failed" {
				publicOutcome = "failure"
			}
			detail = &skill.ToolExecutionDetail{Outcome: publicOutcome, Phase: phase, Reason: reason}
		}
		detail.ExecutionID = attempt.Record.ID
		detail.PolicyProfile = attempt.Record.PolicyProfile
		detail.PolicyRevision = attempt.Record.PolicyRevision
		if executionErr != nil {
			executionErr.Detail = *detail
		}
		if capture, ok := ctx.Value(toolRawResultContextKey{}).(*toolRawResultCapture); ok {
			if raw, ok := capture.value.(*skill.Result); ok && raw != nil {
				raw.Detail = detail
			} else {
				capture.value = &skill.Result{Content: content, Data: capture.value, Detail: detail}
			}
		}
	}
	result := &ToolCallResult{Content: content, Error: err}
	if v2 {
		result.StartedAt = startedAt
		result.Duration = time.Since(startedAt)
	}

	// After hooks (run even on error for audit)
	for _, h := range afterHooks {
		runAfterHook(ctx, h, call, result, v2)
	}
	// 只有实际返回的业务内容进入后续模型上下文；无内容的执行错误不代表读取了记录。
	if strings.TrimSpace(result.Content) != "" {
		labelToolResultEgress(ctx, call.Name)
	}

	// v0.4.0 H6：投递结构化事件；是否落盘/外发由注入的 events.Sink 决定。
	severity := events.SeverityInfo
	if result.Error != nil {
		severity = events.SeverityError
	}
	_ = events.Emit(ctx, events.New("tool.call.completed", severity).
		WithSource("engine.tool_executor").
		With("tool", call.Name).
		With("source", call.Source).
		With("ok", result.Error == nil).
		With("duration_ms", result.Duration.Milliseconds()).
		With("content_len", len(result.Content)))

	return result.Content, result.Error
}

func replayExternalReceipt(ctx context.Context, attempt *externaltools.Attempt) (string, error) {
	r := attempt.Record
	outcome := r.Outcome
	if r.Phase == "dispatched" || r.Phase == "unknown" {
		outcome = "unknown"
	}
	if outcome == "failed" {
		outcome = "failure"
	}
	detail := &skill.ToolExecutionDetail{ExecutionID: r.ID, PolicyProfile: r.PolicyProfile, PolicyRevision: r.PolicyRevision, Phase: r.Phase, Outcome: outcome, Reason: r.Reason, Replayed: true}
	data := map[string]any{"execution_id": r.ID, "phase": r.Phase, "outcome": outcome, "original_receipt": r.ResultManifestJSON}
	encoded, _ := json.Marshal(data)
	result := &skill.Result{Content: string(encoded), Data: data, Detail: detail}
	captureSkillRawResult(ctx, result)
	if outcome != "success" && outcome != "succeeded" && outcome != "completed" {
		return result.Content, &skill.ExecutionError{Detail: *detail, Err: fmt.Errorf("original external execution %s has outcome %s; reconcile its receipt before retrying", r.ID, outcome)}
	}
	return result.Content, nil
}

// runAfterHook 在 lifecycle.v2 启用时用 panic recover 隔离单个 hook 失败，避免
// Audit / metrics 类 hook 抛出 panic 把整个调用链炸掉。flag 关闭时退化成直接调用，
// 与 v0.3 行为完全一致。
func runAfterHook(ctx context.Context, h AfterToolHook, call *ToolCallInfo, result *ToolCallResult, v2 bool) {
	if !v2 {
		h.AfterToolCall(ctx, call, result)
		return
	}
	defer func() {
		if r := recover(); r != nil {
			trace.L(ctx).Error("after-tool hook panicked",
				"tool", call.Name,
				"hook", fmt.Sprintf("%T", h),
				"panic", r,
			)
		}
	}()
	h.AfterToolCall(ctx, call, result)
}

// runSkillViaPipeline 通过 7 阶段 skill.RunPipeline 执行命名 Skill。
//
// 与 RunSkillByPipeline（按 query 召回）不同：这里调用方已经知道精确 toolName，
// 用 toolName 同时作为 Query —— 名称完全匹配在 SelectByContext 中得分 +100，
// Discovery 阶段几乎必然命中。
//
// 防御：若 Discovery 召回的 skill 名称与请求的 toolName 不一致（例如被 when/not_when
// 过滤后命中了同义 skill），返回 error 让调用方决定是否退化；不静默路由到错误工具。
//
// flag OFF 时由调用方退化到 s.Execute；本函数假设 flag 已 ON。
func (e *ToolExecutor) runSkillViaPipeline(ctx context.Context, toolName string, args map[string]any) (string, error) {
	captureCodeExecutionReceipt(ctx, toolName, nil)
	if e.skills == nil {
		return "", fmt.Errorf("runSkillViaPipeline: nil skill registry")
	}
	res, err := skill.RunPipeline(ctx, e.skills, skill.PipelineOptions{
		Query: toolName,
		Args:  args,
		TopK:  1,
	})
	if res != nil && res.Result != nil {
		captureSkillRawResult(ctx, res.Result)
		// 失败执行仍有真实回执；不能因流水线返回错误而丢失编译诊断。
		if res.Skill != nil && res.Skill.Name() == toolName {
			captureCodeExecutionReceipt(ctx, toolName, res.Result)
		}
	}
	if err != nil {
		if res != nil && res.Result != nil {
			if toolName == "media_generate" && res.Skill != nil && res.Skill.Name() == toolName {
				stampToolReplyMeta(ctx, res.Result.Metadata)
			}
			return res.Result.Content, err
		}
		return "", err
	}
	if res == nil || res.Result == nil {
		return "", fmt.Errorf("runSkillViaPipeline: %q produced no result", toolName)
	}
	if res.Skill != nil && res.Skill.Name() != toolName {
		return "", fmt.Errorf("runSkillViaPipeline: pipeline routed %q → %q (refusing to execute mismatched skill)", toolName, res.Skill.Name())
	}
	captureSkillRawResult(ctx, res.Result)
	if toolName == "knowledge_search" {
		if hits, ok := res.Result.Data.([]knowledge.SearchHit); ok {
			recordKnowledgeHits(ctx, hits)
		}
	}
	// BUG-1：pipeline 路径同样透传结构化 reply-safe 元数据（record chip 等）。
	stampToolReplyMeta(ctx, res.Result.Metadata)
	return res.Result.Content, nil
}

// 类型化读取与原执行共用钩子和流水线，不为读取元数据再次执行工具。
func captureSkillRawResult(ctx context.Context, result *skill.Result) {
	if result == nil {
		return
	}
	if capture, ok := ctx.Value(toolRawResultContextKey{}).(*toolRawResultCapture); ok {
		capture.value = result
	}
}

// InitLifecycle 调用所有实现 LifecycleTool 的 Skill 的 Init —— 仅在 lifecycle.v2
// flag 启用时生效；flag 关闭时返回 nil（no-op）。任一 Init 返回 error 立即终止并上抛。
//
// 用法（cmd/hexclaw/main.go 启动阶段）：
//
//	if err := toolExec.InitLifecycle(ctx); err != nil {
//	    return fmt.Errorf("tool lifecycle init failed: %w", err)
//	}
func (e *ToolExecutor) InitLifecycle(ctx context.Context) error {
	if !featureflag.Enabled(ctx, FlagToolLifecycleV2) {
		return nil
	}
	if e.skills == nil {
		return nil
	}
	for _, s := range e.skills.All() {
		lt, ok := s.(LifecycleTool)
		if !ok {
			continue
		}
		if err := lt.Init(ctx); err != nil {
			return fmt.Errorf("init lifecycle tool %q: %w", s.Name(), err)
		}
	}
	return nil
}

// ShutdownLifecycle 调用所有实现 LifecycleTool 的 Skill 的 Shutdown —— 仅在
// lifecycle.v2 flag 启用时生效。Shutdown error 仅记日志，继续清理后续 tool（避免
// 一个工具关闭失败把整个 graceful shutdown 卡住）。
func (e *ToolExecutor) ShutdownLifecycle(ctx context.Context) {
	if !featureflag.Enabled(ctx, FlagToolLifecycleV2) {
		return
	}
	if e.skills == nil {
		return
	}
	for _, s := range e.skills.All() {
		lt, ok := s.(LifecycleTool)
		if !ok {
			continue
		}
		if err := lt.Shutdown(ctx); err != nil {
			trace.L(ctx).Warn("lifecycle tool shutdown failed",
				"tool", s.Name(),
				"err", err,
			)
		}
	}
}
