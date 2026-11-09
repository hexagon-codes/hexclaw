package engine

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	coretool "github.com/hexagon-codes/ai-core/tool"
	"github.com/hexagon-codes/hexagon/browserruntime"
	hruntime "github.com/hexagon-codes/hexagon/runtime"
	"github.com/hexagon-codes/hexclaw/skill"
)

type runtimeBrowserSelector struct {
	server, profile, page string
	existing              bool
}

type runtimeBrowserCall struct {
	selector runtimeBrowserSelector
	kind     string
	server   string
}

type runtimeBrowserObservation struct {
	ref      browserruntime.SessionRef
	selector runtimeBrowserSelector
	source   string
}

type runtimeBrowserRepeatSegment struct {
	guard *hruntime.ToolRepeatGuard
	last  string
}

func (e *runtimeToolExecutor) browserCall(name string, args map[string]any) runtimeBrowserCall {
	text := func(key string) string { value, _ := args[key].(string); return value }
	if name == "browser_session" && e.executor.skills != nil {
		if _, exists := e.executor.skills.Get(name); exists {
			existing, _ := args["existing_session"].(bool)
			call := runtimeBrowserCall{selector: runtimeBrowserSelector{server: text("server"), profile: text("profile"), page: text("page"), existing: existing}}
			switch text("action") {
			case "navigate", "upload":
				call.kind = "page"
			case "select_page", "new_page":
				call.kind = "selection"
			case "call":
				input, _ := args["arguments"].(map[string]any)
				call.kind = browserMCPInteraction(text("tool_name"), input)
			}
			return call
		}
	}
	if e.executor.skills != nil {
		if _, exists := e.executor.skills.Get(name); exists {
			return runtimeBrowserCall{}
		}
	}
	if e.executor.mcpMgr != nil {
		for _, info := range e.executor.mcpMgr.ListToolInfos() {
			if info.Name == name {
				return runtimeBrowserCall{selector: runtimeBrowserSelector{server: info.ServerName}, kind: browserMCPInteraction(info.OriginalName, args), server: info.ServerName}
			}
		}
	}
	return runtimeBrowserCall{}
}

func browserMCPInteraction(name string, args map[string]any) string {
	switch name {
	case "browser_navigate", "browser_navigate_back", "browser_click", "browser_type", "browser_fill_form", "browser_press_key", "browser_drag", "browser_select_option", "browser_file_upload", "browser_handle_dialog", "browser_resize":
		return "page"
	case "browser_tabs":
		action, _ := args["action"].(string)
		if action == "select" || action == "new" || action == "close" {
			return "selection"
		}
	}
	return ""
}

// 浏览器角色只来自应用盖章的 Go 回执，并绑定同一运行内的服务配置与声明。
func (e *runtimeToolExecutor) browserSourceIdentity(server string) string {
	if e.executor.mcpMgr == nil || server == "" {
		return ""
	}
	config, exists := e.executor.mcpMgr.ConfiguredServerConfig(server)
	if !exists {
		return ""
	}
	type declaration struct {
		Name   string
		Schema any
	}
	tools := []declaration{}
	for _, info := range e.executor.mcpMgr.ListToolInfos() {
		if info.ServerName == server {
			tools = append(tools, declaration{Name: info.OriginalName, Schema: info.InputSchema})
		}
	}
	if len(tools) == 0 {
		return ""
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	encoded, err := json.Marshal(struct {
		Config any
		Tools  []declaration
	}{Config: config, Tools: tools})
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func (e *runtimeToolExecutor) knownBrowserSource(server string) bool {
	identity := e.browserSourceIdentity(server)
	if identity == "" {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, observation := range e.browserObservations {
		if observation.ref.ServerName == server && observation.source == identity {
			return true
		}
	}
	return false
}

// 只读取当前运行已观察到的引用；真实目标绑定仍由浏览器执行器完成。
func (e *runtimeToolExecutor) browserReference(call runtimeBrowserCall, validate bool) (browserruntime.SessionRef, bool) {
	e.mu.Lock()
	observations := append([]runtimeBrowserObservation(nil), e.browserObservations...)
	e.mu.Unlock()
	current := func(observed runtimeBrowserObservation) bool {
		return !validate || e.executor.mcpMgr == nil || observed.source != "" && observed.source == e.browserSourceIdentity(observed.ref.ServerName)
	}
	target := call.selector
	server, profile := target.server, ""
	if server == "" && target.profile != "" {
		for index := len(observations) - 1; index >= 0; index-- {
			observed := observations[index]
			if !current(observed) {
				continue
			}
			if target.profile == observed.ref.ProfileID || observed.selector.server == "" && target.profile == observed.selector.profile {
				server, profile = observed.ref.ServerName, observed.ref.ProfileID
				break
			}
		}
		if server == "" {
			return browserruntime.SessionRef{}, false
		}
	}
	if server == "" {
		for index := len(observations) - 1; index >= 0; index-- {
			observed := observations[index]
			if observed.ref.PageID != "" && current(observed) {
				server, profile = observed.ref.ServerName, observed.ref.ProfileID
				break
			}
		}
	}
	for index := len(observations) - 1; index >= 0; index-- {
		observed := observations[index]
		ref := observed.ref
		if ref.ServerName != server || profile != "" && profile != ref.ProfileID || target.existing && !ref.Extension || !current(observed) {
			continue
		}
		if call.kind == "page" && (ref.PageID == "" || target.page != "" && target.page != ref.PageID) {
			continue
		}
		return ref, true
	}
	return browserruntime.SessionRef{}, false
}

func browserRepeatScope(call runtimeBrowserCall, ref browserruntime.SessionRef) string {
	if call.kind == "" || ref.ServerName == "" || ref.ProfileID == "" || ref.Generation == 0 {
		return ""
	}
	page := ref.PageID
	if call.kind == "selection" {
		page = ""
	} else if page == "" {
		return ""
	}
	encoded, _ := json.Marshal([]any{call.kind, ref.ServerName, ref.ProfileID, page, ref.Generation})
	return string(encoded)
}

func (e *runtimeToolExecutor) observeBrowserRepeat(scope, name string, args map[string]any) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.browserRepeatSegment(scope).guard.Observe(name, args)
}

// 调用方持有当前运行的锁，分段不跨请求、服务或页面复用。
func (e *runtimeToolExecutor) browserRepeatSegment(scope string) *runtimeBrowserRepeatSegment {
	if e.browserRepeats == nil {
		e.browserRepeats = map[string]*runtimeBrowserRepeatSegment{}
	}
	segment := e.browserRepeats[scope]
	if segment == nil {
		segment = &runtimeBrowserRepeatSegment{guard: &hruntime.ToolRepeatGuard{}}
		e.browserRepeats[scope] = segment
	}
	return segment
}

func trustedBrowserReference(raw any, builtin bool) (browserruntime.SessionRef, bool) {
	if value, ok := raw.(interface {
		ToolBrowserSession() browserruntime.SessionRef
	}); ok {
		ref := value.ToolBrowserSession()
		return ref, ref.ServerName != "" && ref.ProfileID != "" && ref.Generation != 0
	}
	switch value := raw.(type) {
	case *skill.Result:
		if value != nil {
			return trustedBrowserReference(value.Data, builtin)
		}
	case coretool.Result:
		return trustedBrowserReference(value.Output, builtin)
	case *coretool.Result:
		if value != nil {
			return trustedBrowserReference(value.Output, builtin)
		}
	case map[string]any:
		if builtin {
			if ref, ok := value["session"].(browserruntime.SessionRef); ok {
				return ref, ref.ServerName != "" && ref.ProfileID != "" && ref.Generation != 0
			}
		}
		if result, ok := value["result"].(coretool.Result); ok {
			return trustedBrowserReference(result.Output, false)
		}
	}
	return browserruntime.SessionRef{}, false
}

// 只把已确认完成的不同交互作为进展；新分段保留当前动作的一次计数。
func (e *runtimeToolExecutor) recordBrowserProgress(call runtimeBrowserCall, beforeScope, sourceServer, sourceIdentity, name string, args map[string]any, raw any, executionErr error) {
	if name != "browser_session" && call.server == "" {
		return
	}
	ref, trusted := trustedBrowserReference(raw, name == "browser_session")
	if !trusted || call.server != "" && call.server != ref.ServerName {
		return
	}
	detail := toolExecutionDetail(raw)
	if detail != nil && detail.Outcome == "unknown" {
		return
	}
	identity := ""
	if sourceServer == ref.ServerName && sourceIdentity != "" && sourceIdentity == e.browserSourceIdentity(ref.ServerName) {
		identity = sourceIdentity
	}
	knownSuccess := executionErr == nil && (detail == nil || detail.Outcome == "success")
	scope := browserRepeatScope(call, ref)
	canonical, _ := json.Marshal(args)
	key := name + "\x00" + string(canonical)
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, observed := range e.browserObservations {
		if observed.ref.ServerName == ref.ServerName && observed.ref.ProfileID == ref.ProfileID && observed.ref.Generation > ref.Generation {
			return
		}
	}
	current := e.browserObservations[:0]
	for _, observed := range e.browserObservations {
		if observed.ref.ServerName == ref.ServerName && observed.ref.ProfileID == ref.ProfileID && observed.ref.Generation < ref.Generation {
			continue
		}
		current = append(current, observed)
	}
	e.browserObservations = append(current, runtimeBrowserObservation{ref: ref, selector: call.selector, source: identity})
	if scope == "" {
		return
	}
	segment := e.browserRepeatSegment(scope)
	if knownSuccess && segment.last != key {
		segment.guard = &hruntime.ToolRepeatGuard{}
		_, _ = segment.guard.Observe(name, args)
		segment.last = key
	} else if beforeScope != scope {
		_, _ = segment.guard.Observe(name, args)
	}
}
