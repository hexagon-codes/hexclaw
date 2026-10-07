# HexClaw Plugin Development Guide

**English | [中文](plugin-dev.md)**

## Overview

HexClaw's Go plugin SDK builds on the [Hexagon](https://github.com/hexagon-codes/hexagon) public `plugin` subpackage and adds HexClaw-specific interfaces. The examples use Hexagon `v0.5.14`, currently pinned in [go.mod](../go.mod), and retain the upstream-recommended direct subpackage import. HexClaw's internal root-package facades such as `hexagon.PluginPlugin` are compatible aliases; they do not make the public subpackage obsolete.

**This is currently an embedded Go host API.** [The CLI](../cmd/hexclaw/main.go) does not instantiate the third-party plugin Manager or automatically discover/register/load Go plugins. [Config](../config/config.go) has no top-level `plugins` field. Your own host must register, start, and connect plugin capabilities explicitly.

## Plugin Types

| Type | Interface | Description |
|------|-----------|-------------|
| **SkillPlugin** | `hcplugin.SkillPlugin` | Provides additional skills |
| **AdapterPlugin** | `hcplugin.AdapterPlugin` | Provides new platform adapters |
| **HookPlugin** | `hcplugin.HookPlugin` | Message/reply processing hooks |

The examples use `plugin` for Hexagon's public subpackage and `hcplugin` for HexClaw's extension package. The Manager does not connect base provider, tool, or memory plugins to application services automatically; the host performs that wiring.

Base plugin types inherited from Hexagon:
- `ProviderPlugin` — LLM provider plugin
- `ToolPlugin` — Tool plugin
- `MemoryPlugin` — Memory plugin

## Lifecycle

```
Register → Init(config) → Start() → [Running] → Stop()
```

All plugins implement Hexagon's `plugin.Plugin` interface. The following is an interface excerpt; `PluginInfo` and `HealthStatus` belong to the Hexagon `plugin` package:

```go
type Plugin interface {
    Info() PluginInfo
    Init(ctx context.Context, config map[string]any) error
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    Health() HealthStatus
}
```

`Manager.StartAll` initializes and starts plugins in registration order, returning at the first error without automatically stopping earlier plugins. The host performs cleanup; `StopAll` stops in reverse registration order and logs stop errors.

Hexagon Registry callbacks run after releasing the Registry's own write lock. This does not make `hcplugin.Manager` reentrant: it retains its own lock during registration, lifecycle, and hook calls. Avoid synchronous callbacks into the same Manager.

## ExtensionPlugin Manifest v1

`ExtensionPlugin` adds a `Manifest()` declaration alongside the base capability interfaces. Add the following method to the `MyPlugin` example below, keeping `Manifest.Name` equal to its registration name. Validation and context delivery are separate host responsibilities.

```go
func (p *MyPlugin) Manifest() hcplugin.Manifest {
    return hcplugin.Manifest{
        Name:           "my-skill-plugin",
        Version:        "1.0.0",
        MinHostVersion: "0.4.0",
        Capabilities: []hcplugin.Capability{
            hcplugin.CapReadSkills,
            hcplugin.CapEmitEvents,
        },
        Dependencies: []string{},
        Description: "Example plugin",
    }
}
```

Set `MinHostVersion` to the real minimum your plugin needs. If it only depends on the v1 manifest/capability protocol, `0.4.0` is still valid; if it calls Host APIs added after v0.5, raise it to that minimum version. `Dependencies` uses other plugins' `Manifest.Name`; the Host must load them successfully first.

Defined capabilities:
- `skills.read`: read the host Skill registry
- `events.emit`: emit structured events
- `network`: make network requests
- `fs.read`: read files inside the host sandbox
- `fs.write`: write `.pending`-style files for later review

Implemented boundaries in [manager.go](../plugin/manager.go) and [extension.go](../plugin/extension.go):

- `NewManager()` does not inject a host version or flags. After `SetHostContext(hostVersion, flags)`, `Register` validates the Manifest only when the plugin implements `ExtensionPlugin` and `plugin.extension.v1` is enabled. The flag does not require every ordinary plugin to implement Manifest.
- `Manifest.Dependencies` is a declaration. The Manager does not automatically check, sort, or load these dependencies; the host does so.
- The Manager does not construct or inject `ExtensionContext`. A host can call `BuildExtensionContext`, supply capability implementations, and deliver the result through its own agreed interface. `ExtensionPlugin` itself only declares `Manifest()`.
- Undeclared or unprovided capability fields can be nil. Pending/reviewed file-write behavior depends on the host's `FSWritePending` implementation.

## Quick Start

### 1. Create a Skill Plugin

```go
package myplugin

import (
    "context"

    "github.com/hexagon-codes/ai-core/llm"
    "github.com/hexagon-codes/hexagon/plugin"
    hcplugin "github.com/hexagon-codes/hexclaw/plugin"
    "github.com/hexagon-codes/hexclaw/skill"
)

type MyPlugin struct {
    plugin.BasePlugin // 继承基础实现
}

func New() *MyPlugin {
    return &MyPlugin{
        BasePlugin: *plugin.NewBasePlugin(plugin.PluginInfo{
            Name:        "my-skill-plugin",
            Version:     "1.0.0",
            Type:        hcplugin.TypeSkill,
            Description: "Example Skill Plugin",
            Author:      "your-name",
        }),
    }
}

// 返回插件提供的技能列表
func (p *MyPlugin) Skills() []skill.Skill {
    return []skill.Skill{
        &MySkill{},
    }
}

// 自定义技能
type MySkill struct{}

func (s *MySkill) Name() string        { return "my-skill" }
func (s *MySkill) Description() string  { return "My custom skill" }
func (s *MySkill) Match(content string) bool { return false } // 仅通过工具调用执行

func (s *MySkill) ToolDefinition() llm.ToolDefinition {
    return llm.NewToolDefinition(s.Name(), s.Description(), &llm.Schema{
        Type: "object",
        Properties: map[string]*llm.Schema{
            "query": {Type: "string", Description: "Text to process"},
        },
        Required: []string{"query"},
    })
}

func (s *MySkill) Execute(ctx context.Context, args map[string]any) (*skill.Result, error) {
    query, _ := args["query"].(string)
    return &skill.Result{
        Content: "Result: " + query,
    }, nil
}

var _ hcplugin.SkillPlugin = (*MyPlugin)(nil)
var _ skill.Skill = (*MySkill)(nil)
```

`ToolDefinition()` is a required method. Implementing only `Name/Description/Match/Execute` does not satisfy the current `skill.Skill` interface. The following hook, adapter, and host snippets continue the example in the same package; add the imports shown where needed.

### 2. Create a Hook Plugin

```go
// 另需导入 context、log、HexClaw adapter 和 plugin 别名 hcplugin
type LoggingHook struct {
    plugin.BasePlugin
}

func NewLoggingHook() *LoggingHook {
    return &LoggingHook{BasePlugin: *plugin.NewBasePlugin(plugin.PluginInfo{
        Name: "logging-hook", Version: "1.0.0", Type: hcplugin.TypeHook,
    })}
}

func (h *LoggingHook) OnMessage(ctx context.Context, msg *adapter.Message) (*adapter.Message, error) {
    log.Printf("[inbound] message=%s", msg.ID)
    return msg, nil // 返回原消息或修改后的消息
}

func (h *LoggingHook) OnReply(ctx context.Context, reply *adapter.Reply) (*adapter.Reply, error) {
    log.Printf("[outbound] bytes=%d", len(reply.Content))
    return reply, nil
}

var _ hcplugin.HookPlugin = (*LoggingHook)(nil)
```

The host must invoke `RunMessageHooks`/`RunReplyHooks` in its processing chain and handle errors. Returning `nil, nil` keeps the current message/reply; it does not cancel delivery.

### 3. Create an Adapter Plugin

```go
// 另需导入 HexClaw adapter 和 plugin 别名 hcplugin
type MatrixPlugin struct {
    plugin.BasePlugin
    instance adapter.Adapter
}

func NewMatrixPlugin(instance adapter.Adapter) *MatrixPlugin {
    return &MatrixPlugin{
        BasePlugin: *plugin.NewBasePlugin(plugin.PluginInfo{
            Name: "matrix-adapter", Version: "1.0.0", Type: hcplugin.TypeAdapter,
        }),
        instance: instance,
    }
}

func (p *MatrixPlugin) Adapter() adapter.Adapter { return p.instance }

var _ hcplugin.AdapterPlugin = (*MatrixPlugin)(nil)
```

This wraps an adapter implemented by the caller; it does not implement Matrix. The host wires the collected adapters into startup, shutdown, and message routing. `StartAll` starts plugin lifecycle methods, not collected adapters automatically.

## Registering Plugins

Register at application startup:

```go
// 另需导入 HexClaw featureflag；registry 是 Host 交给引擎使用的注册表
func StartHostPlugins(ctx context.Context, hostVersion string, flags featureflag.Flags, registry skill.Registry) (*hcplugin.Manager, error) {
    mgr := hcplugin.NewManager()
    mgr.SetHostContext(hostVersion, flags)
    if err := mgr.Register(New()); err != nil {
        return nil, err
    }
    if err := mgr.Register(NewLoggingHook()); err != nil {
        return nil, err
    }
    configs := map[string]map[string]any{
        "my-skill-plugin": {},
        "logging-hook":    {},
    }
    if err := mgr.StartAll(ctx, configs); err != nil {
        mgr.StopAll(ctx)
        return nil, err
    }
    for _, provided := range mgr.Skills() {
        if err := registry.Register(provided); err != nil {
            mgr.StopAll(ctx)
            return nil, err
        }
    }
    return mgr, nil
}
```

The Registry must be the one actually used by the engine. This example wires a host that is still initializing; a registration failure aborts startup. `StopAll` does not remove skills already added to an external Registry. The host handles those entries and calls the returned Manager's `StopAll` on shutdown.

## Configuration and CLI Boundaries

`StartAll` receives `map[string]map[string]any`, keyed by `Info().Name`. Each plugin interprets its own settings in `Init`. The example above uses empty settings; BasePlugin saves them but does not automatically apply a timeout or API key field.

The current CLI has no third-party Go plugin `plugins:` YAML entry point and does not automatically load `.so` files or scan plugin directories. Writing `plugins:` into `hexclaw.yaml` does not register a Go plugin. Markdown Skills, MCP, and Go plugins have different integration paths. See [HexClaw Hub](https://github.com/hexagon-codes/hexclaw-hub) for installable skills and the [Skills and Integrations API](api/agents-integrations.en.md) for management endpoints.

## Best Practices

1. **Embed BasePlugin** — Use `plugin.BasePlugin` for default lifecycle implementation
2. **Health checks** — Override `Health()` to return real health status
3. **Graceful shutdown** — Release all resources and close connections in `Stop()`
4. **Config validation** — Validate required config values in `Init()`
5. **Error handling** — Return clear error messages when skill execution fails
6. **Timeout control** — Use `ctx` context to control timeouts and avoid blocking
7. **Callback boundaries** — Distinguish Registry and Manager locks; avoid synchronous reentry into the same Manager.
8. **Host integration** — Handle registration, startup, and Skill Registry errors; explicitly manage dependencies, ExtensionContext delivery, and failure cleanup.

## References

- [Hexagon Plugin Package](https://github.com/hexagon-codes/hexagon/tree/v0.5.14/plugin) — Base plugin interfaces and registry
- [HexClaw Skill Interface](../skill/skill.go) — Skill interface definition
- [HexClaw Adapter Interface](../adapter/adapter.go) — Adapter interface definition
- [GitHub Issues](https://github.com/hexagon-codes/hexclaw/issues) — Bug reports and feedback
