package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"mime"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/ai-core/llm/openai"
	coretool "github.com/hexagon-codes/ai-core/tool"
	"github.com/hexagon-codes/hexagon/browserruntime"
	hexagonmcp "github.com/hexagon-codes/hexagon/mcp"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	browserresource "github.com/hexagon-codes/hexclaw/internal/browserruntime"
	"github.com/hexagon-codes/hexclaw/internal/externaltools"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	hexmcp "github.com/hexagon-codes/hexclaw/mcp"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
	"github.com/hexagon-codes/hexclaw/storage"
	"github.com/hexagon-codes/toolkit/util/idgen"
	"github.com/hexagon-codes/toolkit/util/logger"
)

type browserToolProfile struct {
	mu         sync.Mutex
	ref        browserruntime.SessionRef
	config     hexmcp.ServerConfig
	outputDir  string
	managed    bool
	owner      string
	generation atomic.Uint64
	connecting *browserProfileConnection
	socketDir  string
}

type browserProfileConnection struct {
	done chan struct{}
	err  error
}

type browserToolRun struct {
	mu    sync.Mutex
	lease *browserruntime.Lease
	task  browserruntime.TaskRef
}

type browserDispatchToolKey struct{}
type browserScreenshotDeliveryKey struct{}
type browserPhysicalCallKey struct{}
type browserManualCallKey struct{}

// browserMCPOutput 只投影同次持久回执引用，保留原 MCP content 和图像字节。
type browserMCPOutput struct {
	original    any
	executionID string
	artifacts   []map[string]any
	generation  uint64
}

func (p browserMCPOutput) MarshalJSON() ([]byte, error) {
	var output map[string]any
	if text, ok := p.original.(string); ok {
		output = map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}
	} else {
		data, err := json.Marshal(p.original)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &output); err != nil || output == nil {
			return nil, errors.New("browser MCP result has no structured content")
		}
	}
	metadata, _ := output["_meta"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["hexclaw"] = map[string]any{"execution_id": p.executionID, "browser_generation": p.generation, "artifacts": p.artifacts}
	output["_meta"] = metadata
	return json.Marshal(output)
}

func (p browserMCPOutput) ToolText() string { return browserResultText(coretool.NewResult(p.original)) }

// ToolArtifactExecutionID 只由应用派发后构造的 Go 投影提供，不认可上游 JSON 自报身份。
func (p browserMCPOutput) ToolArtifactExecutionID() string { return p.executionID }

func (p browserMCPOutput) ToolArtifactBrowserGeneration() uint64 { return p.generation }

func (p browserMCPOutput) ToolMessageContent() []llm.ContentPart {
	if original, ok := p.original.(interface{ ToolMessageContent() []llm.ContentPart }); ok {
		return original.ToolMessageContent()
	}
	return nil
}

type browserToolRuntime struct {
	ctx         context.Context
	manager     *hexmcp.Manager
	permissions *engine.PermissionHook
	executor    *engine.ToolExecutor
	files       *builtin.FileAccessBroker
	executions  *externaltools.Service
	driver      *browserruntime.MCPDriver
	browser     *browserruntime.Coordinator
	root        string
	workspace   string
	resourceDir string
	mu          sync.Mutex
	profiles    map[string]*browserToolProfile
	runs        map[string]*browserToolRun
	leases      map[*browserruntime.Lease]struct{}
	ordinals    map[string]map[string]uint64
}

// configureBrowserTools 只装配现有执行链；资源与账号未就绪只影响实际调用，不阻止应用启动。
func configureBrowserTools(ctx context.Context, cfg *config.Config, manager *hexmcp.Manager, registry *skill.DefaultRegistry, eng *engine.ReActEngine, executor *engine.ToolExecutor, permissions *engine.PermissionHook, executions *externaltools.Service, router *llmrouter.Selector, store storage.Store, files *builtin.FileAccessBroker, workspace string) error {
	_ = eng
	w := &browserToolRuntime{ctx: ctx, manager: manager, permissions: permissions, executor: executor, files: files, executions: executions, root: filepath.Join(filepath.Dir(cfg.Storage.SQLite.Path), "browser"), workspace: workspace, resourceDir: os.Getenv("HEXCLAW_RESOURCE_DIR"), profiles: map[string]*browserToolProfile{}, runs: map[string]*browserToolRun{}, leases: map[*browserruntime.Lease]struct{}{}, ordinals: map[string]map[string]uint64{}}
	w.driver = &browserruntime.MCPDriver{ListTools: w.listTools, CallTool: w.callServer, WaitIdle: func(context.Context, string) error {
		// MCP 取消通知没有远端停止确认，不能用本地请求已返回冒充动作收口。
		return browserruntime.ErrNotQuiescent
	}}
	w.browser = browserruntime.New(w.driver, browserruntime.Options{Authorize: w.authorize})
	go w.closeBrowserRuntime()
	executor.SetMCPDispatch(w.dispatch)
	executor.SetRunFinalizer(w.finishRun)
	if err := registry.Register(&browserSessionSkill{runtime: w}); err != nil {
		return err
	}
	controller := engine.NewComputerUseController(engine.ComputerUseOptions{
		Router: router, Browser: w.browser, Executions: executions, Budget: engine.CurrentToolBudget, BorrowBrowser: w.borrow,
		AuthorizeModel: func(ctx context.Context, args map[string]any) (context.Context, error) {
			return w.authorizeTool(ctx, "computer_use", args)
		},
		ObserveUsage: func(usageCtx context.Context, usage engine.ComputerUseUsage) error {
			if usage.Usage == nil || store == nil {
				return nil
			}
			inv := skill.CurrentInvocation(usageCtx)
			metadata, err := json.Marshal(map[string]any{"kind": "computer_use", "execution_id": usage.ExecutionID, "model_exchange_id": usage.ModelExchangeID, "response_id": usage.ResponseID, "request_id": usage.RequestID, "exchange_ordinal": usage.ExchangeOrdinal})
			if err != nil {
				return err
			}
			return store.SaveCost(usageCtx, &storage.CostRecord{ID: "cost-" + idgen.ShortID(), UserID: inv.AuthenticatedUserID, SessionID: inv.SessionID, Provider: usage.Provider, Model: usage.Model, PromptTokens: usage.Usage.PromptTokens, CompletionTokens: usage.Usage.CompletionTokens, TotalTokens: usage.Usage.TotalTokens, Cost: usage.Cost, Meta: string(metadata), CreatedAt: time.Now()})
		},
		AcknowledgeSafetyChecks: func(checkCtx context.Context, checks []openai.ComputerSafetyCheck) error {
			if permissions == nil {
				return errors.New("computer use permission runtime is unavailable")
			}
			return permissions.BeforeToolCall(checkCtx, &engine.ToolCallInfo{Name: "computer_use", Source: "skill", Arguments: map[string]any{"pending_safety_checks": checks}})
		},
	})
	return registry.Register(builtin.NewComputerUseSkill(func(ctx context.Context, args map[string]any) (*skill.Result, error) {
		return controller.Execute(context.WithValue(ctx, browserDispatchToolKey{}, "computer_use"), args)
	}))
}

func browserTask(ctx context.Context) (browserruntime.TaskRef, error) {
	inv := skill.CurrentInvocation(ctx)
	request := inv.RequestID
	if request == "" {
		request = skill.SystemDispatchTaskRef(ctx)
	}
	if inv.AuthenticatedUserID == "" || request == "" {
		return browserruntime.TaskRef{}, errors.New("browser execution requires a trusted owner and request identity")
	}
	return browserruntime.TaskRef{ID: request, OwnerID: inv.AuthenticatedUserID, SessionID: inv.SessionID, RequestID: inv.RequestID, ToolCallID: inv.ToolCallID}, nil
}

func browserIdentity(parts ...string) string {
	data, _ := json.Marshal(parts)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:12])
}

func (w *browserToolRuntime) listTools(_ context.Context, server string) ([]browserruntime.ToolSchema, error) {
	if w.manager == nil {
		return nil, errors.New("MCP runtime is unavailable")
	}
	var out []browserruntime.ToolSchema
	for _, tool := range w.manager.ListToolInfos() {
		if tool.ServerName == server {
			out = append(out, browserruntime.ToolSchema{Name: tool.OriginalName, Schema: tool.InputSchema})
		}
	}
	return out, nil
}

func (w *browserToolRuntime) callServer(ctx context.Context, server, name string, args map[string]any) (coretool.Result, error) {
	if w.manager == nil || w.executions == nil {
		return coretool.Result{}, errors.New("browser execution storage or MCP runtime is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return coretool.Result{}, err
	}
	inv := skill.CurrentInvocation(ctx)
	runKey := browserIdentity(inv.AuthenticatedUserID, inv.RequestID)
	key := browserIdentity(inv.AuthenticatedUserID, inv.RequestID, inv.ToolCallID, skill.ExternalExecutionID(ctx))
	w.mu.Lock()
	if w.ordinals[runKey] == nil {
		w.ordinals[runKey] = map[string]uint64{}
	}
	w.ordinals[runKey][key]++
	ordinal := w.ordinals[runKey][key]
	profile := w.profiles[server]
	w.mu.Unlock()
	generation := uint64(0)
	if profile != nil {
		generation = profile.generation.Load()
	}
	before, err := browserOutputSnapshot(profile)
	if err != nil {
		return coretool.Result{}, err
	}
	scope, _ := ctx.Value(browserDispatchToolKey{}).(string)
	policy, authorized := skill.ToolDispatchPolicy(ctx, scope)
	if authorized && policy != skill.CurrentExecutionPolicy(ctx) {
		return coretool.Result{}, externaltools.ErrPolicyChanged
	}
	ctx, attempt, err := w.executions.Begin(ctx, externaltools.Intent{Kind: "browser_mcp_request", ToolName: name, ServerName: server, LogicalSuffix: key + "/" + strconv.FormatUint(ordinal, 10), BrowserGeneration: generation, Input: args}, false)
	if err != nil {
		return coretool.Result{}, err
	}
	if authorized && (attempt.Record.PolicyProfile != policy.Profile || attempt.Record.PolicyRevision != policy.Revision) {
		return coretool.Result{}, errors.Join(externaltools.ErrPolicyChanged, w.executions.Finish(attempt, storage.ExternalExecutionObserved, "failed", "policy_changed_before_dispatch", nil, false))
	}
	if err = w.executions.Dispatch(ctx, attempt); err != nil {
		return coretool.Result{}, err
	}
	physical := context.WithValue(ctx, browserPhysicalCallKey{}, server)
	result, callErr := w.manager.CallServerToolResult(physical, server, name, args)
	responseReceived := callErr == nil
	if hexagonmcp.ResultIsError(result) {
		responseReceived = true
		callErr = &browserruntime.ObservedToolError{Message: result.Error}
	}
	userScreenshot, _ := ctx.Value(browserScreenshotDeliveryKey{}).(bool)
	var screenshotDigests map[string]bool
	if name == "browser_take_screenshot" && !userScreenshot {
		screenshotDigests = browserInlineImageDigests(result.Output)
	}
	snapshotFiles := browserSnapshotFiles(result.Output)
	observationFiles := snapshotFiles
	explicitFile := ""
	if callErr == nil && result.Success {
		filename, _ := args["filename"].(string)
		if name == "browser_snapshot" && filename != "" {
			observationFiles = nil
			for path := range snapshotFiles {
				explicitFile = path
				break
			}
			if explicitFile == "" {
				explicitFile = browserMCPFilePath(filename)
			}
		}
		if name == "browser_take_screenshot" && filename != "" {
			links := browserReturnedFiles(result.Output, "Result", "Screenshot of")
			for path := range links {
				explicitFile = path
				break
			}
			if explicitFile == "" {
				explicitFile = browserMCPFilePath(filename)
			}
		}
	}
	artifacts, artifactErr := w.captureOutputs(ctx, profile, before, callErr != nil || !result.Success, screenshotDigests, observationFiles, explicitFile)
	if result.Output != nil {
		result.Output = browserMCPOutput{original: result.Output, executionID: attempt.Record.ID, artifacts: artifacts, generation: generation}
	}
	phase, outcome, reason := storage.ExternalExecutionObserved, "success", "completed"
	if callErr != nil && !responseReceived {
		phase, outcome, reason = storage.ExternalExecutionUnknown, "unknown", "mcp_response_unconfirmed"
	} else if !result.Success {
		outcome, reason = "failure", "mcp_tool_failed"
	} else if artifactErr != nil {
		outcome, reason = "failure", "artifact_persistence_failed"
	}
	finishErr := w.executions.Finish(attempt, phase, outcome, reason, map[string]any{"success": result.Success, "response_received": responseReceived}, false)
	return result, errors.Join(callErr, artifactErr, finishErr)
}

func (w *browserToolRuntime) authorize(ctx context.Context, ref browserruntime.SessionRef, action browserruntime.Action) error {
	if action.Type == "cleanup_page" {
		return nil
	}
	if w.permissions == nil {
		return errors.New("browser permission runtime is unavailable")
	}
	name := "browser_session"
	direct, _ := ctx.Value(browserDispatchToolKey{}).(string)
	if direct != "" {
		name = direct
	}
	hostCode := action.Type == "mcp" && (action.ToolName == "browser_run_code_unsafe" || action.ToolName == "browser_run_code")
	if hostCode {
		name = "shell"
	}
	authorized := skill.ToolDispatchAuthorized(ctx, "browser_session") || skill.ToolDispatchAuthorized(ctx, "computer_use") || direct != "" && skill.ToolDispatchAuthorized(ctx, direct)
	if manual, _ := ctx.Value(browserManualCallKey{}).(bool); manual {
		// 手工测试是认证用户提交的精确操作，只刷新该单次操作的有效策略，不另加审批。
		ctx = skill.WithToolDispatchGrant(ctx, direct, skill.CurrentExecutionPolicy(ctx))
		if hostCode {
			ctx = skill.WithToolDispatchGrant(ctx, "shell", skill.CurrentExecutionPolicy(ctx))
		}
		authorized = true
	}
	if hostCode || !authorized {
		arguments := map[string]any{"server": ref.ServerName, "action": action.Type, "tool": action.ToolName}
		if hostCode {
			arguments["code"] = action.Arguments["code"]
		}
		if _, err := w.authorizeTool(ctx, name, arguments); err != nil {
			return err
		}
	}
	paths := action.Paths
	if action.Type == "mcp" && action.ToolName == "browser_file_upload" {
		paths = browserStrings(action.Arguments["paths"])
	}
	for _, path := range paths {
		if _, err := builtin.ResolveToolPath(ctx, w.workspace, path); err != nil {
			if _, err := w.files.AuthorizeExistingContext(ctx, path); err != nil {
				return err
			}
		}
	}
	if action.Type == "mcp" && (action.ToolName == "browser_take_screenshot" || action.ToolName == "browser_snapshot") && !skill.FullAccess(ctx) {
		filename, _ := action.Arguments["filename"].(string)
		if filename != "" {
			w.mu.Lock()
			profile := w.profiles[ref.ServerName]
			w.mu.Unlock()
			outputRoot := ""
			if profile != nil {
				outputRoot = profile.outputDir
			}
			if outputRoot == "" {
				outputRoot = w.workspace
			}
			path := browserMCPFilePath(filename)
			if _, err := builtin.ResolveToolPath(ctx, outputRoot, path); err != nil {
				if _, err := builtin.ResolveToolPath(ctx, w.workspace, path); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (w *browserToolRuntime) authorizeTool(ctx context.Context, name string, arguments map[string]any) (context.Context, error) {
	if skill.ToolDispatchAuthorized(ctx, name) {
		return ctx, nil
	}
	source := "skill"
	server, _ := arguments["server"].(string)
	if server != "" && name != "browser_session" && name != "computer_use" && name != "shell" {
		source = "mcp"
	}
	return w.executor.AuthorizeToolDispatch(ctx, &engine.ToolCallInfo{Name: name, Source: source, ServerName: server, Arguments: arguments})
}

func (w *browserToolRuntime) connected(server string) bool {
	if w.manager == nil {
		return false
	}
	for _, status := range w.manager.ServerStatuses() {
		if status.Name == server {
			return status.Connected
		}
	}
	return false
}

func (w *browserToolRuntime) resolveProfile(ctx context.Context, target browserruntime.Target) (*browserToolProfile, error) {
	if w.manager == nil {
		return nil, errors.New("MCP runtime is unavailable")
	}
	task, err := browserTask(ctx)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, profile := range w.profiles {
		if target.ServerName != "" && profile.ref.ServerName == target.ServerName || target.ProfileID != "" && profile.ref.ProfileID == target.ProfileID {
			if profile.owner != "" && profile.owner != task.OwnerID {
				return nil, errors.New("the browser profile belongs to a different application owner")
			}
			return profile, nil
		}
	}
	if target.ServerName != "" {
		cfg, _ := w.manager.ConfiguredServerConfig(target.ServerName)
		if !w.connected(target.ServerName) {
			return nil, errors.New("the selected external browser MCP service is not connected")
		}
		tools, toolsErr := w.listTools(ctx, target.ServerName)
		if toolsErr != nil {
			return nil, toolsErr
		}
		foundTabs, foundCode := false, false
		for _, tool := range tools {
			foundTabs = foundTabs || tool.Name == "browser_tabs"
			foundCode = foundCode || tool.Name == "browser_run_code" || tool.Name == "browser_run_code_unsafe"
		}
		if !foundTabs || !foundCode {
			return nil, errors.New("the selected MCP service does not provide same-page browser session operations")
		}
		extension := false
		for _, argument := range cfg.Args {
			extension = extension || argument == "--extension"
		}
		outputDir := browserFlag(cfg.Args, "--output-dir")
		if outputDir != "" {
			outputDir, err = filepath.Abs(outputDir)
			if err != nil {
				return nil, err
			}
		}
		profile := &browserToolProfile{ref: browserruntime.SessionRef{ID: target.ServerName, ServerName: target.ServerName, ProfileID: "external:" + target.ServerName, Extension: extension}, config: cfg, outputDir: outputDir}
		profile.generation.Store(1)
		ref := profile.ref
		ref.Generation = 1
		if err := w.browser.RegisterSession(ref); err != nil {
			return nil, err
		}
		w.profiles[ref.ServerName] = profile
		w.installReplacement(profile)
		return profile, nil
	}
	label := target.ProfileID
	if label == "" {
		label = "default"
	}
	mode := "managed"
	if target.RequireExistingSession {
		mode = "extension"
	}
	identity := browserIdentity(task.OwnerID, mode, label)
	server := "hexclaw-browser-" + identity
	if profile := w.profiles[server]; profile != nil {
		if profile.owner != task.OwnerID {
			return nil, errors.New("the browser profile belongs to a different application owner")
		}
		return profile, nil
	}
	if _, conflict := w.manager.ConfiguredServerConfig(server); conflict {
		return nil, errors.New("bundled browser service identity conflicts with an existing MCP configuration")
	}
	output := filepath.Join(w.root, identity, "output")
	options := browserresource.LaunchOptions{ProfileDirectory: filepath.Join(w.root, identity, "profile"), OutputDirectory: output, Extension: mode == "extension", Headless: true, UnrestrictedFiles: true}
	if mode == "managed" {
		options.ViewportWidth, options.ViewportHeight = 1440, 900
	}
	launch, err := browserresource.ManagedLaunch(w.resourceDir, options)
	if err != nil {
		return nil, err
	}
	for _, directory := range []string{filepath.Dir(output), output} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return nil, fmt.Errorf("prepare bundled browser directories: %w", err)
		}
	}
	profile := &browserToolProfile{ref: browserruntime.SessionRef{ID: server, ServerName: server, ProfileID: mode + ":" + identity, OwnsBrowser: mode == "managed", Extension: mode == "extension"}, config: hexmcp.ServerConfig{Name: server, Transport: "stdio", Command: launch.Command, Args: launch.Args, Env: launch.Env, Enabled: true}, outputDir: output, managed: true, owner: task.OwnerID}
	if err := w.browser.PrepareProfile(profile.ref); err != nil {
		return nil, err
	}
	w.profiles[server] = profile
	w.installReplacement(profile)
	w.installPrivateDispatch(profile)
	return profile, nil
}

func (w *browserToolRuntime) installPrivateDispatch(profile *browserToolProfile) {
	if !profile.managed || profile.owner == "" {
		return
	}
	w.manager.SetToolDispatchHook(profile.ref.ServerName, func(ctx context.Context, server, name string, args map[string]any, execute func(context.Context) (coretool.Result, error)) (coretool.Result, error) {
		if physical, _ := ctx.Value(browserPhysicalCallKey{}).(string); physical == server {
			return execute(ctx)
		}
		inv := skill.CurrentInvocation(ctx)
		if inv.AuthenticatedUserID == "" || inv.AuthenticatedUserID != profile.owner {
			return coretool.Result{}, errors.New("the browser profile belongs to a different application owner")
		}
		if inv.RequestID != "" && inv.ToolCallID != "" {
			return w.dispatch(ctx, server, name, args)
		}
		return w.manualDispatch(ctx, server, name, args)
	})
}

func (w *browserToolRuntime) manualDispatch(ctx context.Context, server, name string, args map[string]any) (result coretool.Result, err error) {
	inv := skill.CurrentInvocation(ctx)
	inv.RequestID = "mcp-manual-" + idgen.ShortID()
	inv.ToolCallID = "manual:" + idgen.ShortID()
	inv.SessionID, inv.SourceMessageID, inv.RoutedAgentName, inv.OperationOrdinal = "", "", "", ""
	ctx = skill.WithInvocationContext(ctx, inv)
	ctx = skill.WithExecutionPolicySource(ctx, w.permissions.EffectiveExecutionPolicy)
	ctx = context.WithValue(ctx, browserManualCallKey{}, true)
	ctx = skill.WithToolDispatchGrant(ctx, name, skill.CurrentExecutionPolicy(ctx))
	if name == "browser_run_code" || name == "browser_run_code_unsafe" {
		ctx = skill.WithToolDispatchGrant(ctx, "shell", skill.CurrentExecutionPolicy(ctx))
	}
	defer func() {
		if err == nil && result.Success {
			task, _ := browserTask(ctx)
			w.mu.Lock()
			run := w.runs[browserIdentity(task.OwnerID, task.ID)]
			w.mu.Unlock()
			if run != nil {
				run.mu.Lock()
				if run.lease != nil {
					w.browser.RetainPage(task, run.lease.Ref().PageID)
				}
				run.mu.Unlock()
			}
		}
		err = errors.Join(err, w.finalizeBrowserRun(ctx, nil))
		err = browserExecutionError(err)
	}()
	return w.dispatch(ctx, server, name, args)
}

func (w *browserToolRuntime) installReplacement(profile *browserToolProfile) {
	w.manager.SetBeforeReplace(profile.ref.ServerName, func(ctx context.Context, _ string, closeOld func() error) (func(bool) error, error) {
		if profile.owner != "" {
			caller := skill.CurrentInvocation(ctx).AuthenticatedUserID
			if caller != "" && caller != profile.owner {
				return nil, errors.New("the browser profile belongs to a different application owner")
			}
			if caller == "" && ctx != w.ctx && !(hexmcp.IsBackgroundLifecycle(ctx) && profile.generation.Load() > 0) {
				return nil, errors.New("private browser lifecycle requires its application owner")
			}
		}
		if !profile.ref.OwnsBrowser {
			// 外部浏览器不归本进程终止；未知动作不能用断开 SDK 连接冒充已停止。
			w.mu.Lock()
			leases := make([]*browserruntime.Lease, 0, len(w.leases))
			for lease := range w.leases {
				leases = append(leases, lease)
			}
			w.mu.Unlock()
			for _, lease := range leases {
				if lease != nil && lease.Ref().ProfileID == profile.ref.ProfileID {
					if err := lease.Release(ctx, "browser_connection_replacement"); err != nil {
						return nil, err
					}
				}
			}
		}
		finish, err := w.browser.BeginReplacement(ctx, profile.ref.ProfileID, closeOld)
		if err != nil {
			return nil, err
		}
		next := profile.ref
		next.Generation = profile.generation.Add(1)
		return func(connected bool) error { return finish(next, connected) }, nil
	})
}

func (w *browserToolRuntime) ensureProfile(ctx context.Context, profile *browserToolProfile) error {
	profile.mu.Lock()
	if w.connected(profile.ref.ServerName) {
		for _, session := range w.browser.Sessions() {
			if session.ProfileID == profile.ref.ProfileID {
				profile.mu.Unlock()
				return nil
			}
		}
		// browser_close 已确认原浏览器停止；新任务才登记下一代，实际浏览器仍按需启动。
		next := profile.ref
		next.Generation = profile.generation.Add(1)
		err := w.browser.RegisterSession(next)
		profile.mu.Unlock()
		return err
	}
	if !profile.managed {
		profile.mu.Unlock()
		return errors.New("the selected external browser MCP service is disconnected")
	}
	connection := profile.connecting
	if connection == nil {
		if profile.socketDir == "" {
			base := os.TempDir()
			if runtime.GOOS != "windows" {
				base = "/tmp"
			}
			socketDir, err := os.MkdirTemp(base, "hc-pw-")
			if err != nil {
				profile.mu.Unlock()
				return fmt.Errorf("prepare browser IPC directory: %w", err)
			}
			profile.socketDir = socketDir
			if profile.config.Env == nil {
				profile.config.Env = map[string]string{}
			}
			profile.config.Env["PWTEST_SOCKETS_DIR"] = socketDir
		}
		connection = &browserProfileConnection{done: make(chan struct{})}
		profile.connecting = connection
		// 连接归属应用生命周期，等待归属本次调用；前端退出不终止共享服务。
		go func() {
			connection.err = w.manager.AddServer(w.ctx, profile.config)
			profile.mu.Lock()
			profile.connecting = nil
			close(connection.done)
			profile.mu.Unlock()
		}()
	}
	profile.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-connection.done:
		if connection.err != nil {
			return fmt.Errorf("connect bundled browser MCP service: %w", connection.err)
		}
		return nil
	}
}

func (w *browserToolRuntime) closeBrowserRuntime() {
	<-w.ctx.Done()
	if w.manager != nil {
		w.manager.Close()
	}
	w.mu.Lock()
	profiles := make([]*browserToolProfile, 0, len(w.profiles))
	for _, profile := range w.profiles {
		profiles = append(profiles, profile)
	}
	w.mu.Unlock()
	for _, profile := range profiles {
		profile.mu.Lock()
		directory := profile.socketDir
		profile.socketDir = ""
		profile.mu.Unlock()
		if directory != "" {
			if err := os.RemoveAll(directory); err != nil {
				logger.Warn("remove browser IPC directory failed", "err", err)
			}
		}
	}
}

func (w *browserToolRuntime) borrow(ctx context.Context, _ browserruntime.TaskRef, target browserruntime.Target) (*browserruntime.Lease, error) {
	task, err := browserTask(ctx)
	if err != nil {
		return nil, err
	}
	key := browserIdentity(task.OwnerID, task.ID)
	w.mu.Lock()
	run := w.runs[key]
	if run == nil {
		run = &browserToolRun{task: task}
		w.runs[key] = run
	}
	w.mu.Unlock()
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.lease != nil {
		ref := run.lease.Ref()
		current := false
		for _, session := range w.browser.Sessions() {
			current = current || session.ServerName == ref.ServerName && session.ProfileID == ref.ProfileID && session.Generation == ref.Generation
		}
		if !current || !w.connected(ref.ServerName) {
			return nil, browserruntime.ErrStaleGeneration
		}
	}
	if run.lease != nil && target.ServerName == "" && target.ProfileID == "" && (!target.RequireExistingSession || run.lease.Ref().Extension) {
		if target.PageID != "" && target.PageID != run.lease.Ref().PageID {
			if _, err := run.lease.Execute(ctx, browserruntime.Action{Type: "select_page", PageID: target.PageID}); err != nil {
				return nil, err
			}
		}
		return run.lease, nil
	}
	profile, err := w.resolveProfile(ctx, target)
	if err != nil {
		return nil, err
	}
	if err = w.ensureProfile(ctx, profile); err != nil {
		return nil, err
	}
	if run.lease != nil && run.lease.Ref().ProfileID == profile.ref.ProfileID {
		if target.PageID != "" && target.PageID != run.lease.Ref().PageID {
			if _, err := run.lease.Execute(ctx, browserruntime.Action{Type: "select_page", PageID: target.PageID}); err != nil {
				return nil, err
			}
		}
		return run.lease, nil
	}
	if run.lease != nil {
		cleanup, cancel := context.WithTimeout(w.ctx, 10*time.Second)
		defer cancel()
		cleanup = skill.WithInvocationContext(cleanup, skill.CurrentInvocation(ctx))
		cleanup = skill.WithExternalExecution(cleanup, skill.ExternalExecutionID(ctx))
		if w.permissions != nil {
			cleanup = skill.WithExecutionPolicySource(cleanup, w.permissions.EffectiveExecutionPolicy)
		}
		if err := run.lease.FinishTask(cleanup); err != nil {
			return nil, err
		}
		if err := run.lease.Release(cleanup, "browser_target_changed"); err != nil {
			return nil, err
		}
		w.mu.Lock()
		delete(w.leases, run.lease)
		w.mu.Unlock()
		run.lease = nil
	}
	target.ServerName, target.ProfileID = profile.ref.ServerName, profile.ref.ProfileID
	lease, err := w.browser.Acquire(ctx, task, target)
	if err == nil {
		run.lease = lease
		w.mu.Lock()
		w.leases[lease] = struct{}{}
		w.mu.Unlock()
	}
	return lease, err
}

func (w *browserToolRuntime) dispatch(ctx context.Context, server, name string, args map[string]any) (output coretool.Result, err error) {
	defer func() { err = browserExecutionError(err) }()
	ctx = context.WithValue(ctx, browserDispatchToolKey{}, name)
	if name == "browser_take_screenshot" {
		ctx = context.WithValue(ctx, browserScreenshotDeliveryKey{}, true)
	}
	tools, err := w.listTools(ctx, server)
	if err != nil {
		return coretool.Result{}, err
	}
	browserServer := false
	for _, tool := range tools {
		browserServer = browserServer || tool.Name == "browser_run_code" || tool.Name == "browser_run_code_unsafe"
	}
	if !browserServer {
		return w.manager.CallServerToolResult(ctx, server, name, args)
	}
	manual, _ := ctx.Value(browserManualCallKey{}).(bool)
	lease, err := w.borrow(ctx, browserruntime.TaskRef{}, browserruntime.Target{ServerName: server, UseCurrentPage: manual})
	if err != nil {
		return coretool.Result{}, err
	}
	result, err := lease.Execute(ctx, browserruntime.Action{Type: "mcp", ToolName: name, Arguments: args})
	for index, image := range result.Images {
		if browserImageArtifact(result.Result, image.Data) != nil {
			continue
		}
		_, saveErr := w.executions.SaveArtifact(ctx, browserImageName(index+1, image.MIME), image.MIME, bytes.NewReader(image.Data))
		err = errors.Join(err, saveErr)
	}
	return result.Result, err
}

func (w *browserToolRuntime) finishRun(ctx context.Context, message *adapter.Message) {
	if err := w.finalizeBrowserRun(ctx, message); err != nil {
		logger.Warn("browser task cleanup is unresolved", "err", err)
	}
}

func (w *browserToolRuntime) finalizeBrowserRun(ctx context.Context, message *adapter.Message) error {
	inv := skill.CurrentInvocation(ctx)
	if inv.AuthenticatedUserID == "" && message != nil {
		inv.AuthenticatedUserID = message.UserID
	}
	if inv.RequestID == "" && message != nil {
		inv.RequestID = message.Metadata["request_id"]
	}
	taskID := inv.RequestID
	if taskID == "" {
		taskID = skill.SystemDispatchTaskRef(ctx)
	}
	key := browserIdentity(inv.AuthenticatedUserID, taskID)
	w.mu.Lock()
	run := w.runs[key]
	w.mu.Unlock()
	if run == nil {
		return nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	cleanup, cancel := context.WithTimeout(w.ctx, 10*time.Second)
	defer cancel()
	inv.ToolCallID = "browser-finalize:" + taskID
	cleanup = skill.WithInvocationContext(cleanup, inv)
	if w.permissions != nil {
		cleanup = skill.WithExecutionPolicySource(cleanup, w.permissions.EffectiveExecutionPolicy)
	}
	if run.lease != nil {
		finishErr := run.lease.FinishTask(cleanup)
		releaseErr := run.lease.Release(cleanup, "run_finished")
		if err := errors.Join(finishErr, releaseErr); err != nil {
			return err
		}
	}
	w.mu.Lock()
	delete(w.leases, run.lease)
	delete(w.runs, key)
	delete(w.ordinals, browserIdentity(inv.AuthenticatedUserID, inv.RequestID))
	w.mu.Unlock()
	return nil
}

type browserOutputFingerprint struct {
	size int64
	mod  time.Time
}

func browserOutputSnapshot(profile *browserToolProfile) (map[string]browserOutputFingerprint, error) {
	files := map[string]browserOutputFingerprint{}
	if profile == nil || profile.outputDir == "" {
		return files, nil
	}
	root, err := filepath.EvalSymlinks(profile.outputDir)
	if errors.Is(err, os.ErrNotExist) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files[path] = browserOutputFingerprint{size: info.Size(), mod: info.ModTime()}
		}
		return nil
	})
	return files, err
}

func (w *browserToolRuntime) captureOutputs(ctx context.Context, profile *browserToolProfile, before map[string]browserOutputFingerprint, observation bool, screenshotDigests, observationFiles map[string]bool, explicitFile string) ([]map[string]any, error) {
	cleanup, cancel := context.WithTimeout(w.ctx, 10*time.Second)
	defer cancel()
	cleanup = skill.WithInvocationContext(cleanup, skill.CurrentInvocation(ctx))
	cleanup = skill.WithExternalExecution(cleanup, skill.ExternalExecutionID(ctx))
	after, err := browserOutputSnapshot(profile)
	if err != nil {
		return nil, err
	}
	if explicitFile != "" {
		explicitFile, err = filepath.EvalSymlinks(explicitFile)
		if err != nil {
			return nil, fmt.Errorf("requested browser output file is unavailable: %w", err)
		}
		info, err := os.Stat(explicitFile)
		if err != nil {
			return nil, fmt.Errorf("requested browser output file is unavailable: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("requested browser output is not a regular file")
		}
		after[explicitFile] = browserOutputFingerprint{size: info.Size(), mod: info.ModTime()}
	}
	var artifacts []map[string]any
	for path, current := range after {
		if previous, exists := before[path]; exists && current == previous && path != explicitFile {
			continue
		}
		name := filepath.Base(path)
		if strings.HasSuffix(name, ".crdownload") || strings.HasSuffix(name, ".part") {
			continue
		}
		if strings.HasPrefix(name, "console-") && strings.HasSuffix(name, ".log") {
			continue
		}
		// 文件选择模态允许自动快照为空；原模态文本仍保留在工具响应中。
		if current.size == 0 && observationFiles[path] && path != explicitFile {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			return artifacts, err
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			_ = file.Close()
			return artifacts, err
		}
		digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			_ = file.Close()
			return artifacts, err
		}
		deliver := !observation && !screenshotDigests[digest] && !observationFiles[path]
		var artifact map[string]any
		var saveErr error
		if deliver {
			artifact, saveErr = w.executions.SaveArtifactFromPath(cleanup, path, name, mime.TypeByExtension(filepath.Ext(name)), file)
		} else {
			artifact, saveErr = w.executions.SaveObservation(cleanup, name, mime.TypeByExtension(filepath.Ext(name)), file)
		}
		closeErr := file.Close()
		if err := errors.Join(saveErr, closeErr); err != nil {
			return artifacts, err
		}
		artifact["deliver"] = deliver
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}

type browserSessionSkill struct{ runtime *browserToolRuntime }

func (*browserSessionSkill) Name() string { return "browser_session" }
func (*browserSessionSkill) Description() string {
	return "Operate the application's browser session and return actual web results"
}
func (*browserSessionSkill) Match(string) bool { return false }
func (*browserSessionSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("browser_session", "Connect and operate the bundled browser through official Playwright MCP. Use action=tools for the tool name and description index, then action=tools with tool_name for that tool's complete schema before action=call with tool_name and arguments. Use stable page identities from observations, keep the specified account/session, and inspect actual final results. existing_session=true connects the official browser extension only when the task needs the user's existing browser login; do not replace it with an anonymous browser. Browser resources, extensions and accounts must really be ready.", &llm.Schema{Type: "object", Properties: map[string]*llm.Schema{
		"action":           {Type: "string", Description: "status, tools, list_tabs, connect, observe, call, navigate, screenshot, upload, select_page, new_page, wait_for_login, or read_artifact"},
		"server":           {Type: "string", Description: "Explicit existing browser MCP server identity"},
		"profile":          {Type: "string", Description: "Stable browser profile identity or managed profile label"},
		"page":             {Type: "string", Description: "Stable target page identity from the same session"},
		"existing_session": {Type: "boolean", Description: "Use the official extension and existing Chrome/Edge session when required"},
		"tool_name":        {Type: "string", Description: "Optional exact MCP tool name for action=tools to retrieve its complete schema; required for action=call"},
		"arguments":        {Type: "object", Description: "Exact MCP tool arguments according to its discovered schema"},
		"url":              {Type: "string", Description: "URL for action=navigate"},
		"paths":            {Type: "array", Items: &llm.Schema{Type: "string"}, Description: "Actual local upload files"},
		"path":             {Type: "string", Description: "Actual task result file to attach with action=read_artifact"},
		"ready":            {Type: "object", Description: "For wait_for_login, choose a verifiable condition from the actual site. All supplied conditions must match; the user only completes login, never supplies technical selectors.", Properties: map[string]*llm.Schema{"url": {Type: "string", Description: "Exact expected page URL"}, "text": {Type: "string", Description: "Expected visible page text"}, "selector": {Type: "string", Description: "Expected visible element selector"}}},
		"timeout_seconds":  {Type: "number", Description: "Optional explicit wait_for_login timeout in seconds; otherwise follow the current request context and budget"},
	}, Required: []string{"action"}})
}

func (s *browserSessionSkill) Execute(ctx context.Context, args map[string]any) (output *skill.Result, err error) {
	defer func() { err = browserExecutionError(err) }()
	ctx = context.WithValue(ctx, browserDispatchToolKey{}, "browser_session")
	w := s.runtime
	text := func(key string) string { value, _ := args[key].(string); return strings.TrimSpace(value) }
	action := text("action")
	if action == "status" {
		_, readyErr := browserresource.ManagedLaunch(w.resourceDir, browserresource.LaunchOptions{ProfileDirectory: filepath.Join(w.root, "readiness-profile"), Headless: true})
		owner := skill.CurrentInvocation(ctx).AuthenticatedUserID
		count := 0
		for _, session := range w.browser.Sessions() {
			w.mu.Lock()
			profile := w.profiles[session.ServerName]
			w.mu.Unlock()
			if profile != nil && (profile.owner == "" || profile.owner == owner) {
				count++
			}
		}
		data := map[string]any{"bundled_runtime_available": readyErr == nil, "mcp_available": w.manager != nil, "registered_mcp_sessions": count}
		if readyErr != nil {
			data["reason"] = readyErr.Error()
		}
		body, _ := json.Marshal(data)
		return &skill.Result{Content: string(body), Data: data}, nil
	}
	if w.executions == nil {
		return nil, errors.New("browser execution storage is unavailable")
	}
	if action == "read_artifact" {
		path, err := builtin.ResolveToolPath(ctx, w.workspace, text("path"))
		if err != nil {
			path, err = w.files.AuthorizeExistingContext(ctx, text("path"))
			if err != nil {
				return nil, err
			}
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		name, contentType := filepath.Base(path), mime.TypeByExtension(filepath.Ext(path))
		artifact, found, err := w.executions.ReferenceArtifactFromPath(ctx, path, name, contentType, file)
		if err == nil && !found {
			artifact, err = w.executions.SaveArtifactFromPath(ctx, path, name, contentType, file)
		}
		if err != nil {
			return nil, err
		}
		body, _ := json.Marshal(artifact)
		return &skill.Result{Content: string(body), Data: artifact}, nil
	}
	existing, _ := args["existing_session"].(bool)
	target := browserruntime.Target{ServerName: text("server"), ProfileID: text("profile"), PageID: text("page"), RequireExistingSession: existing}
	if action == "tools" || action == "list_tabs" || action == "inspect" {
		target = w.currentTarget(ctx, target)
	}
	if action == "tools" {
		profile, err := w.resolveProfile(ctx, target)
		if err != nil {
			return nil, err
		}
		if err = w.ensureProfile(ctx, profile); err != nil {
			return nil, err
		}
		if w.manager == nil {
			return nil, errors.New("MCP runtime is unavailable")
		}
		requested := text("tool_name")
		tools := make([]map[string]any, 0)
		for _, item := range w.manager.ListToolInfos() {
			if item.ServerName != profile.ref.ServerName || (requested != "" && item.OriginalName != requested) {
				continue
			}
			metadata := map[string]any{"Name": item.OriginalName}
			if requested == "" {
				metadata["Description"] = item.Description
			} else {
				metadata["Schema"] = item.InputSchema
			}
			tools = append(tools, metadata)
		}
		if requested != "" && len(tools) == 0 {
			return nil, fmt.Errorf("browser MCP tool not found: %s", requested)
		}
		ref := profile.ref
		for _, session := range w.browser.Sessions() {
			if session.ProfileID == ref.ProfileID {
				ref = session
				break
			}
		}
		data := map[string]any{"session": ref, "tools": tools}
		body, _ := json.Marshal(data)
		return &skill.Result{Content: string(body), Data: data}, nil
	}
	if action == "list_tabs" || action == "inspect" {
		profile, err := w.resolveProfile(ctx, target)
		if err != nil {
			return nil, err
		}
		if err = w.ensureProfile(ctx, profile); err != nil {
			return nil, err
		}
		task, err := browserTask(ctx)
		if err != nil {
			return nil, err
		}
		target.ServerName, target.ProfileID = profile.ref.ServerName, profile.ref.ProfileID
		observation, err := w.browser.Inspect(ctx, task, target)
		if err != nil {
			return nil, err
		}
		data := map[string]any{"server": profile.ref.ServerName, "profile": profile.ref.ProfileID, "observation": observation}
		body, _ := json.Marshal(data)
		return &skill.Result{Content: string(body), Data: data}, nil
	}
	lease, err := w.borrow(ctx, browserruntime.TaskRef{}, target)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"session": lease.Ref()}
	if action == "wait_for_login" {
		observation, err := w.waitForLogin(ctx, lease, args)
		if err != nil {
			return nil, err
		}
		data["observation"], data["state"] = observation, "ready_condition_satisfied"
		body, _ := json.Marshal(data)
		return &skill.Result{Content: string(body), Data: data}, nil
	}
	if action == "observe" || action == "connect" {
		observation, err := lease.Observe(ctx)
		if err != nil {
			return nil, err
		}
		data["observation"] = observation
		body, _ := json.Marshal(data)
		return &skill.Result{Content: string(body), Data: data}, nil
	}
	operation := browserruntime.Action{Type: action, URL: text("url"), PageID: text("page"), Paths: browserStrings(args["paths"])}
	if action == "call" {
		operation.Type, operation.ToolName = "mcp", text("tool_name")
		operation.Arguments, _ = args["arguments"].(map[string]any)
		if operation.ToolName == "" {
			return nil, errors.New("browser MCP tool_name is required")
		}
	}
	if action == "screenshot" || operation.ToolName == "browser_take_screenshot" {
		ctx = context.WithValue(ctx, browserScreenshotDeliveryKey{}, true)
	}
	result, err := lease.Execute(ctx, operation)
	data["session"], data["observation"], data["result"] = lease.Ref(), result.Observation, result.Result
	var artifacts []map[string]any
	for index, image := range result.Images {
		if existing := browserImageArtifact(result.Result, image.Data); existing != nil {
			artifacts = append(artifacts, existing)
			continue
		}
		artifact, saveErr := w.executions.SaveArtifact(ctx, browserImageName(index+1, image.MIME), image.MIME, bytes.NewReader(image.Data))
		if saveErr != nil {
			err = errors.Join(err, saveErr)
			break
		}
		artifacts = append(artifacts, artifact)
	}
	if projection, ok := result.Result.Output.(browserMCPOutput); ok {
		seen := map[string]bool{}
		for _, artifact := range artifacts {
			seen[fmt.Sprint(artifact["artifact_id"])] = true
		}
		for _, artifact := range projection.artifacts {
			if deliver, _ := artifact["deliver"].(bool); deliver && !seen[fmt.Sprint(artifact["artifact_id"])] {
				artifacts = append(artifacts, artifact)
			}
		}
	}
	data["artifacts"] = artifacts
	content, _ := json.Marshal(map[string]any{"session": lease.Ref(), "observation": result.Observation, "artifacts": artifacts, "text": browserResultText(result.Result)})
	output = &skill.Result{Content: string(content), Data: data}
	if err != nil {
		outcome, reason := "failure", "browser_action_failed"
		var unknown *browserruntime.UnknownCallError
		if errors.As(err, &unknown) || result.Outcome == "unknown" || errors.Is(err, browserruntime.ErrNotQuiescent) {
			outcome, reason = "unknown", "browser_action_unconfirmed"
		}
		output.Detail = &skill.ToolExecutionDetail{Phase: "browser", Outcome: outcome, Reason: reason}
		return output, &skill.ExecutionError{Detail: *output.Detail, Err: err}
	}
	return output, nil
}

func (w *browserToolRuntime) currentTarget(ctx context.Context, target browserruntime.Target) browserruntime.Target {
	if target.ServerName != "" || target.ProfileID != "" {
		return target
	}
	task, err := browserTask(ctx)
	if err != nil {
		return target
	}
	w.mu.Lock()
	run := w.runs[browserIdentity(task.OwnerID, task.ID)]
	w.mu.Unlock()
	if run == nil {
		return target
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.lease != nil {
		ref := run.lease.Ref()
		if !target.RequireExistingSession || ref.Extension {
			target.ServerName, target.ProfileID = ref.ServerName, ref.ProfileID
		}
	}
	return target
}

// waitForLogin 保留原工具调用和页面租约，直到只读条件满足或原请求结束。
func (w *browserToolRuntime) waitForLogin(ctx context.Context, lease *browserruntime.Lease, args map[string]any) (browserruntime.Observation, error) {
	failure := func(reason string, cause error) error {
		return &skill.ExecutionError{Detail: skill.ToolExecutionDetail{Phase: "wait_for_login", Outcome: "failure", Reason: reason}, Err: cause}
	}
	if !lease.Ref().Extension {
		return browserruntime.Observation{}, failure("existing_session_required", errors.New("manual login requires the user's browser session through the official extension"))
	}
	raw, ok := args["ready"].(map[string]any)
	if !ok || len(raw) == 0 {
		return browserruntime.Observation{}, failure("login_condition_required", errors.New("wait_for_login requires a verifiable ready condition chosen from the actual site"))
	}
	condition := make(map[string]string, len(raw))
	for key, value := range raw {
		if key != "url" && key != "text" && key != "selector" {
			return browserruntime.Observation{}, failure("invalid_login_condition", fmt.Errorf("unsupported login ready condition %q", key))
		}
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return browserruntime.Observation{}, failure("invalid_login_condition", errors.New("login ready conditions must be non-empty strings"))
		}
		condition[key] = text
	}
	waitCtx := ctx
	if rawTimeout, present := args["timeout_seconds"]; present {
		var seconds float64
		switch value := rawTimeout.(type) {
		case float64:
			seconds = value
		case int:
			seconds = float64(value)
		case json.Number:
			var err error
			seconds, err = value.Float64()
			if err != nil {
				return browserruntime.Observation{}, failure("invalid_wait_timeout", errors.New("wait timeout must be a positive finite number of seconds"))
			}
		default:
			return browserruntime.Observation{}, failure("invalid_wait_timeout", errors.New("wait timeout must be a positive finite number of seconds"))
		}
		if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds > float64(math.MaxInt64)/float64(time.Second) {
			return browserruntime.Observation{}, failure("invalid_wait_timeout", errors.New("wait timeout must be a positive finite number of seconds"))
		}
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, time.Duration(seconds*float64(time.Second)))
		defer cancel()
	}
	task, err := browserTask(ctx)
	if err != nil {
		return browserruntime.Observation{}, err
	}
	w.browser.RetainPage(task, lease.Ref().PageID)
	for {
		if err := waitCtx.Err(); err != nil {
			return browserruntime.Observation{}, failure("login_wait_ended", err)
		}
		if budget := engine.CurrentToolBudget(ctx); budget != nil {
			if err := budget.Check(); err != nil {
				return browserruntime.Observation{}, failure("request_budget_exhausted", err)
			}
		}
		result, err := lease.Execute(waitCtx, browserruntime.Action{Type: "login_ready", Arguments: map[string]any{"ready": condition}})
		if err != nil {
			if errors.Is(err, browserruntime.ErrNotQuiescent) {
				return browserruntime.Observation{}, err
			}
			if waitCtx.Err() != nil {
				return browserruntime.Observation{}, failure("login_wait_ended", waitCtx.Err())
			}
			return browserruntime.Observation{}, failure("login_observation_failed", err)
		}
		if result.Ready == nil || result.Observation == nil {
			return browserruntime.Observation{}, failure("login_observation_missing", errors.New("login wait returned no verified page observation"))
		}
		if *result.Ready {
			return *result.Observation, nil
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return browserruntime.Observation{}, failure("login_wait_ended", waitCtx.Err())
		case <-timer.C:
		}
	}
}

func browserExecutionError(err error) error {
	if err == nil {
		return nil
	}
	var executionErr *skill.ExecutionError
	if errors.As(err, &executionErr) {
		return err
	}
	outcome, reason := "failure", "browser_action_failed"
	if errors.Is(err, externaltools.ErrPolicyChanged) {
		reason = "policy_changed_before_dispatch"
	}
	var unknown *browserruntime.UnknownCallError
	if errors.As(err, &unknown) || errors.Is(err, browserruntime.ErrNotQuiescent) {
		outcome, reason = "unknown", "browser_action_unconfirmed"
	}
	var observed *browserruntime.ObservedToolError
	return &skill.ExecutionError{Detail: skill.ToolExecutionDetail{Phase: "browser", Outcome: outcome, Reason: reason, ResponseReceived: errors.As(err, &observed)}, Err: err}
}

func browserStrings(value any) []string {
	if strings, ok := value.([]string); ok {
		return strings
	}
	var out []string
	if values, ok := value.([]any); ok {
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
	}
	return out
}

func browserFlag(arguments []string, flag string) string {
	for index, argument := range arguments {
		if argument == flag && index+1 < len(arguments) {
			return arguments[index+1]
		}
		if value, ok := strings.CutPrefix(argument, flag+"="); ok {
			return value
		}
	}
	return ""
}

func browserImageName(index int, contentType string) string {
	extension := ".png"
	switch contentType {
	case "image/jpeg":
		extension = ".jpg"
	case "image/webp":
		extension = ".webp"
	}
	return fmt.Sprintf("browser-screenshot-%d%s", index, extension)
}

// browserMCPFilePath 解析官方返回的文件系统路径；相对位置基于服务继承的进程工作目录。
func browserMCPFilePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	if canonical, err := filepath.EvalSymlinks(absolute); err == nil {
		return canonical
	}
	return filepath.Clean(absolute)
}

// browserReturnedFiles 只关联官方结果节的明确文件链接，不按扩展名推断文件用途。
func browserReturnedFiles(output any, section, labelPrefix string) map[string]bool {
	files := map[string]bool{}
	text := browserResultText(coretool.NewResult(output))
	var payload struct {
		Snapshot struct {
			File string `json:"file"`
		} `json:"snapshot"`
		Result string `json:"result"`
	}
	if json.Unmarshal([]byte(text), &payload) == nil {
		if section == "Snapshot" && payload.Snapshot.File != "" {
			if path := browserMCPFilePath(payload.Snapshot.File); path != "" {
				files[path] = true
			}
		}
		if section == "Result" && payload.Result != "" {
			text = "### Result\n" + payload.Result
		}
	}
	current, fenced := "", false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		if heading, ok := strings.CutPrefix(line, "### "); ok {
			current = heading
			continue
		}
		if current != section {
			continue
		}
		link := strings.TrimSpace(line)
		if !strings.HasPrefix(link, "- [") || !strings.HasSuffix(link, ")") {
			continue
		}
		label, location, ok := strings.Cut(strings.TrimPrefix(link, "- ["), "](")
		if !ok || !strings.HasPrefix(label, labelPrefix) {
			continue
		}
		if section == "Snapshot" && label != "Snapshot" {
			continue
		}
		if path := browserMCPFilePath(strings.TrimSuffix(location, ")")); path != "" {
			files[path] = true
		}
	}
	return files
}

func browserSnapshotFiles(output any) map[string]bool {
	return browserReturnedFiles(output, "Snapshot", "Snapshot")
}

func browserInlineImageDigests(output any) map[string]bool {
	digests := map[string]bool{}
	contents, err := hexagonmcp.ToolImageContents(output)
	if err != nil {
		return digests
	}
	for _, content := range contents {
		digest := sha256.Sum256(content.Data)
		digests["sha256:"+hex.EncodeToString(digest[:])] = true
	}
	return digests
}

func browserImageArtifact(result coretool.Result, image []byte) map[string]any {
	projection, ok := result.Output.(browserMCPOutput)
	if !ok {
		return nil
	}
	digest := sha256.Sum256(image)
	want := "sha256:" + hex.EncodeToString(digest[:])
	for _, artifact := range projection.artifacts {
		if deliver, _ := artifact["deliver"].(bool); deliver && artifact["digest"] == want {
			return artifact
		}
	}
	return nil
}

func browserResultText(result coretool.Result) string {
	if text, ok := result.Output.(string); ok {
		return text
	}
	data, _ := json.Marshal(result.Output)
	var response struct{ Content []struct{ Type, Text string } }
	if json.Unmarshal(data, &response) == nil && len(response.Content) > 0 {
		var texts []string
		for _, block := range response.Content {
			if block.Type == "text" {
				texts = append(texts, block.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	return result.Error
}
