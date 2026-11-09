package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coretool "github.com/hexagon-codes/ai-core/tool"
	"github.com/hexagon-codes/hexagon/browserruntime"
	"github.com/hexagon-codes/hexclaw/internal/externaltools"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	hexmcp "github.com/hexagon-codes/hexclaw/mcp"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type browserSettlementResult struct {
	result coretool.Result
	err    error
}

// 真实 SDK 处理器只执行一次受控等待；通道由用例决定原调用何时完成。
type browserSettlementFixture struct {
	runtime    *browserToolRuntime
	db         *sql.DB
	appCancel  context.CancelFunc
	started    chan struct{}
	release    chan struct{}
	physical   chan context.Context
	finished   chan struct{}
	releaseOne sync.Once
	calls      atomic.Int32
}

func newBrowserSettlementFixture(t *testing.T, browserLike ...bool) *browserSettlementFixture {
	t.Helper()
	appCtx, appCancel := context.WithCancel(context.Background())
	f := &browserSettlementFixture{appCancel: appCancel, started: make(chan struct{}), release: make(chan struct{}), physical: make(chan context.Context, 1)}
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "settlement.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(appCtx); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	f.db = store.DB()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "browser-settlement", Version: "1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "hold", Description: "Wait for the controlled original response"}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, _ struct{}) (*sdkmcp.CallToolResult, struct{}, error) {
		if f.calls.Add(1) == 1 {
			close(f.started)
		}
		select {
		case <-ctx.Done():
			return nil, struct{}{}, ctx.Err()
		case <-f.release:
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "original-response-settled"}}}, struct{}{}, nil
		}
	})
	if len(browserLike) > 0 && browserLike[0] {
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "browser_run_code_unsafe", Description: "Browser identity for a blocked dispatch"}, func(context.Context, *sdkmcp.CallToolRequest, struct{}) (*sdkmcp.CallToolResult, struct{}, error) {
			f.calls.Add(1)
			return &sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "unexpected physical dispatch"}}}, struct{}{}, nil
		})
	}
	endpoint := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil))
	manager := hexmcp.NewManager()
	if err := manager.AddServer(appCtx, hexmcp.ServerConfig{Name: "settlement", Transport: "streamable", Endpoint: endpoint.URL, Enabled: true}); err != nil {
		endpoint.Close()
		_ = store.Close()
		appCancel()
		t.Fatal(err)
	}
	profile := &browserToolProfile{ref: browserruntime.SessionRef{ServerName: "settlement", ProfileID: "test-profile", Generation: 1}, outputDir: t.TempDir()}
	profile.generation.Store(1)
	f.runtime = &browserToolRuntime{ctx: appCtx, manager: manager, executions: externaltools.New(appCtx, store.ExternalToolExecutionRepository(), nil), profiles: map[string]*browserToolProfile{"settlement": profile}, ordinals: map[string]map[string]uint64{}}
	manager.SetToolDispatchHook("settlement", func(ctx context.Context, _, _ string, _ map[string]any, execute func(context.Context) (coretool.Result, error)) (coretool.Result, error) {
		f.physical <- ctx
		return execute(ctx)
	})
	t.Cleanup(func() {
		f.releaseOne.Do(func() { close(f.release) })
		appCancel()
		if f.finished != nil {
			select {
			case <-f.finished:
			case <-time.After(10 * time.Second):
				t.Error("original MCP call did not settle during cleanup")
			}
		}
		manager.Close()
		endpoint.Close()
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func (f *browserSettlementFixture) start(t *testing.T, ctx context.Context) (<-chan browserSettlementResult, context.Context) {
	t.Helper()
	done := make(chan browserSettlementResult, 1)
	f.finished = make(chan struct{})
	go func() {
		defer close(f.finished)
		result, err := f.runtime.callServer(ctx, "settlement", "hold", map[string]any{})
		done <- browserSettlementResult{result: result, err: err}
	}()
	select {
	case <-f.started:
	case <-time.After(10 * time.Second):
		t.Fatal("actual SDK handler was not dispatched")
	}
	return done, <-f.physical
}

func browserSettlementContext() context.Context {
	ctx := skill.WithInvocationContext(context.Background(), skill.InvocationContext{AuthenticatedUserID: "test-owner", RequestID: "test-request", ToolCallID: "test-tool"})
	return skill.WithExecutionPolicySource(ctx, func() skill.ExecutionPolicy { return skill.ExecutionPolicy{Profile: "full_access", Revision: 1} })
}

func browserAwaitSettlement(t *testing.T, done <-chan browserSettlementResult) browserSettlementResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(10 * time.Second):
		t.Fatal("original SDK call did not return")
		return browserSettlementResult{}
	}
}

func (f *browserSettlementFixture) assertReceipt(t *testing.T, phase string, received bool, generation uint64) {
	t.Helper()
	var actualPhase string
	var actualGeneration uint64
	var raw []byte
	if err := f.db.QueryRow(`SELECT phase,browser_generation,result_manifest_json FROM external_tool_executions WHERE request_id='test-request' AND kind='browser_mcp_request'`).Scan(&actualPhase, &actualGeneration, &raw); err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Result struct {
			ResponseReceived bool `json:"response_received"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if actualPhase != phase || actualGeneration != generation || manifest.Result.ResponseReceived != received {
		t.Fatalf("receipt phase=%s generation=%d received=%v; want %s/%d/%v", actualPhase, actualGeneration, manifest.Result.ResponseReceived, phase, generation, received)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("physical calls=%d, want one original call", f.calls.Load())
	}
}

func TestBrowserMCPPhysicalCallRetainsResultAfterTaskCancellation(t *testing.T) {
	f := newBrowserSettlementFixture(t)
	deadline := time.Now().Add(time.Minute)
	budget, stopBudget := context.WithDeadline(browserSettlementContext(), deadline)
	defer stopBudget()
	task, cancelTask := context.WithCancel(budget)
	defer cancelTask()
	done, physical := f.start(t, task)
	cancelTask()
	if err := physical.Err(); err != nil {
		t.Fatalf("user cancellation discarded the dispatched physical call: %v", err)
	}
	if actual, ok := physical.Deadline(); !ok || !actual.Equal(deadline) {
		t.Fatalf("physical deadline=%v/%v, want original %v", actual, ok, deadline)
	}
	if skill.CurrentInvocation(physical).RequestID != "test-request" {
		t.Fatal("physical call lost its invocation identity")
	}
	f.releaseOne.Do(func() { close(f.release) })
	result := browserAwaitSettlement(t, done)
	if result.err != nil || !result.result.Success {
		t.Fatalf("original actual response was discarded: result=%+v err=%v", result.result, result.err)
	}
	f.assertReceipt(t, storage.ExternalExecutionObserved, true, 1)
}

func TestBrowserMCPPhysicalCallKeepsDeadlineAndApplicationShutdown(t *testing.T) {
	for _, mode := range []string{"deadline", "application_shutdown"} {
		t.Run(mode, func(t *testing.T) {
			f := newBrowserSettlementFixture(t)
			ctx := browserSettlementContext()
			if mode == "deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
			}
			done, _ := f.start(t, ctx)
			if mode == "application_shutdown" {
				f.appCancel()
			}
			result := browserAwaitSettlement(t, done)
			want := context.DeadlineExceeded
			if mode == "application_shutdown" {
				want = context.Canceled
			}
			if !errors.Is(result.err, want) {
				t.Fatalf("physical call err=%v, want %v", result.err, want)
			}
			f.assertReceipt(t, storage.ExternalExecutionUnknown, false, 1)
		})
	}
}

func TestBrowserMCPPhysicalLateResultKeepsOriginalGeneration(t *testing.T) {
	f := newBrowserSettlementFixture(t)
	done, _ := f.start(t, browserSettlementContext())
	f.runtime.profiles["settlement"].generation.Store(2)
	f.releaseOne.Do(func() { close(f.release) })
	result := browserAwaitSettlement(t, done)
	if result.err != nil || !result.result.Success {
		t.Fatalf("original result=%+v err=%v", result.result, result.err)
	}
	if got := f.runtime.profiles["settlement"].generation.Load(); got != 2 {
		t.Fatalf("late result changed the newer generation to %d", got)
	}
	f.assertReceipt(t, storage.ExternalExecutionObserved, true, 1)
}

func TestBrowserMCPPhysicalCancelledWithoutDeadlineIsBounded(t *testing.T) {
	f := newBrowserSettlementFixture(t)
	f.runtime.cancelSettlementWait = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(browserSettlementContext())
	defer cancel()
	done, physical := f.start(t, ctx)
	if _, ok := physical.Deadline(); ok {
		t.Fatal("uncancelled task gained a new deadline")
	}
	cancel()
	result := browserAwaitSettlement(t, done)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("bounded cancelled call err=%v, want context.Canceled", result.err)
	}
	f.assertReceipt(t, storage.ExternalExecutionUnknown, false, 1)
}

type blockedBrowserDriver struct{ binds, actions, inspections atomic.Int32 }

func (d *blockedBrowserDriver) Bind(context.Context, string, string, bool) (browserruntime.Observation, error) {
	d.binds.Add(1)
	return browserruntime.Observation{Page: browserruntime.Page{ID: "owned-page"}, Pages: []browserruntime.Page{{ID: "owned-page"}}, Width: 100, Height: 100}, nil
}
func (d *blockedBrowserDriver) Execute(context.Context, browserruntime.SessionRef, browserruntime.Action) (browserruntime.ActionResult, error) {
	d.actions.Add(1)
	return browserruntime.ActionResult{Outcome: "unknown"}, &browserruntime.UnknownCallError{Cause: errors.New("original response unavailable")}
}
func (*blockedBrowserDriver) Quiesce(context.Context, string) error {
	return browserruntime.ErrNotQuiescent
}
func (*blockedBrowserDriver) ClosePage(context.Context, string, string) (coretool.Result, error) {
	return coretool.NewResult(nil), nil
}
func (d *blockedBrowserDriver) Inspect(context.Context, string) (browserruntime.Observation, error) {
	d.inspections.Add(1)
	return browserruntime.Observation{}, errors.New("unexpected inspection")
}

func TestBrowserBlockedProfileIsFailureBeforeDispatch(t *testing.T) {
	for _, entry := range []string{"observe", "inspect", "direct_mcp"} {
		t.Run(entry, func(t *testing.T) {
			f := newBrowserSettlementFixture(t, true)
			driver := &blockedBrowserDriver{}
			coordinator := browserruntime.New(driver, browserruntime.Options{})
			ref := browserruntime.SessionRef{ID: "settlement", ServerName: "settlement", ProfileID: "test-profile", Generation: 1}
			if err := coordinator.RegisterSession(ref); err != nil {
				t.Fatal(err)
			}
			f.runtime.browser = coordinator
			f.runtime.profiles["settlement"].ref = ref
			f.runtime.runs = map[string]*browserToolRun{}
			f.runtime.leases = map[*browserruntime.Lease]struct{}{}
			first, err := coordinator.Acquire(t.Context(), browserruntime.TaskRef{ID: "first", OwnerID: "test-owner"}, browserruntime.Target{ServerName: "settlement"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = coordinator.CloseSession(context.Background(), "test-profile", func(context.Context, browserruntime.SessionRef) error { return nil })
			})
			_, err = first.Execute(t.Context(), browserruntime.Action{Type: "mcp", ToolName: "write"})
			var originalUnknown *browserruntime.UnknownCallError
			if !errors.As(err, &originalUnknown) {
				t.Fatalf("first action did not become unknown: %v", err)
			}
			beforeBinds, beforeActions := driver.binds.Load(), driver.actions.Load()
			callCtx, cancel := context.WithTimeout(browserSettlementContext(), time.Second)
			defer cancel()
			if entry == "direct_mcp" {
				_, err = f.runtime.dispatch(callCtx, "settlement", "hold", map[string]any{})
			} else {
				_, err = (&browserSessionSkill{runtime: f.runtime}).Execute(callCtx, map[string]any{"action": entry, "server": "settlement"})
			}
			var failure *skill.ExecutionError
			if !errors.Is(err, browserruntime.ErrNotQuiescent) || !errors.As(err, &failure) || failure.Detail.Outcome != "failure" || failure.Detail.Reason != "profile_not_quiescent" {
				t.Fatalf("undispatched rejection=%+v err=%v, want explicit profile failure", failure, err)
			}
			if driver.binds.Load() != beforeBinds || driver.actions.Load() != beforeActions || driver.inspections.Load() != 0 || f.calls.Load() != 0 {
				t.Fatal("rejected task performed a driver or physical SDK action")
			}
			holderError := browserExecutionError(first.Release(t.Context(), "verify-held"))
			var held *skill.ExecutionError
			if !errors.As(holderError, &held) || held.Detail.Outcome != "unknown" {
				t.Fatalf("original holder uncertainty was weakened: %v", holderError)
			}
		})
	}
}

func TestBrowserExplicitServerRejectsConflictingResolvedProfile(t *testing.T) {
	f := newBrowserSettlementFixture(t)
	config, ok := f.runtime.manager.ConfiguredServerConfig("settlement")
	if !ok {
		t.Fatal("local MCP fixture config missing")
	}
	config.Name = "second"
	if err := f.runtime.manager.AddServer(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	driver := &blockedBrowserDriver{}
	coordinator := browserruntime.New(driver, browserruntime.Options{})
	firstRef := browserruntime.SessionRef{ID: "settlement", ServerName: "settlement", ProfileID: "external:settlement", Generation: 1}
	secondRef := browserruntime.SessionRef{ID: "second", ServerName: "second", ProfileID: "external:second", Generation: 1}
	for _, ref := range []browserruntime.SessionRef{firstRef, secondRef} {
		if err := coordinator.RegisterSession(ref); err != nil {
			t.Fatal(err)
		}
	}
	f.runtime.browser = coordinator
	f.runtime.profiles["settlement"].ref = firstRef
	f.runtime.profiles["settlement"].owner = "test-owner"
	second := &browserToolProfile{ref: secondRef, owner: "test-owner"}
	second.generation.Store(1)
	f.runtime.profiles["second"] = second
	f.runtime.runs = map[string]*browserToolRun{}
	f.runtime.leases = map[*browserruntime.Lease]struct{}{}
	t.Cleanup(func() {
		for _, ref := range []browserruntime.SessionRef{firstRef, secondRef} {
			_ = coordinator.CloseSession(context.Background(), ref.ProfileID, func(context.Context, browserruntime.SessionRef) error { return nil })
		}
	})
	ctx := browserSettlementContext()
	lease, err := f.runtime.borrow(ctx, browserruntime.TaskRef{}, browserruntime.Target{ServerName: "settlement", ProfileID: "default"})
	if err != nil || lease == nil || lease.Ref().ServerName != "settlement" {
		t.Fatalf("explicit server with existing logical label no longer works: lease=%v error=%v", lease, err)
	}
	selected, resolveErr := f.runtime.resolveProfile(ctx, browserruntime.Target{ServerName: "settlement", ProfileID: "Default"})
	if resolveErr != nil || selected != f.runtime.profiles["settlement"] {
		t.Fatalf("explicit server with legacy logical label changed: profile=%v err=%v", selected, resolveErr)
	}
	if err := f.runtime.finalizeBrowserRun(ctx, nil); err != nil {
		t.Fatal(err)
	}
	beforeBinds := driver.binds.Load()
	lease, err = f.runtime.borrow(ctx, browserruntime.TaskRef{}, browserruntime.Target{ServerName: "settlement", ProfileID: secondRef.ProfileID})
	if err == nil || lease != nil || driver.binds.Load() != beforeBinds {
		t.Fatalf("conflicting server/profile reached a browser: lease=%v err=%v binds=%d/%d", lease, err, driver.binds.Load(), beforeBinds)
	}
	if !errors.Is(err, browserruntime.ErrTargetRequired) {
		t.Fatalf("conflicting target err=%v, want ErrTargetRequired", err)
	}
	lease, err = f.runtime.borrow(ctx, browserruntime.TaskRef{}, browserruntime.Target{ServerName: "missing-server", ProfileID: secondRef.ProfileID})
	if !errors.Is(err, browserruntime.ErrTargetRequired) || lease != nil || driver.binds.Load() != beforeBinds {
		t.Fatalf("explicit missing server fell back to another profile: lease=%v err=%v binds=%d/%d", lease, err, driver.binds.Load(), beforeBinds)
	}
	if driver.actions.Load() != 0 || f.calls.Load() != 0 {
		t.Fatal("target consistency check performed a page action or SDK tool call")
	}
}
