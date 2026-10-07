# HexClaw 插件开发指南

**[English](plugin-dev.en.md) | 中文**

## 概述

HexClaw 的 Go 插件 SDK 基于 [Hexagon](https://github.com/hexagon-codes/hexagon) 的公共 `plugin` 子包，扩展了 HexClaw 专属接口。示例使用 [go.mod](../go.mod) 当前锁定的 Hexagon `v0.5.14`，保留上游推荐的直接子包导入。HexClaw 内部的 `hexagon.PluginPlugin` 等根包 facade 是兼容别名，不代表公共子包已过时。

**当前这是嵌入式 Go Host 的编程接口。** [CLI](../cmd/hexclaw/main.go) 未创建第三方插件 Manager，也没有自动发现、注册或加载 Go 插件的流程；[Config](../config/config.go) 没有顶层 `plugins` 字段。开发者需要在自己的 Host 中显式注册、启动并接入能力。

## 插件类型

| 类型 | 接口 | 说明 |
|------|------|------|
| **SkillPlugin** | `hcplugin.SkillPlugin` | 提供额外技能 |
| **AdapterPlugin** | `hcplugin.AdapterPlugin` | 提供新平台适配器 |
| **HookPlugin** | `hcplugin.HookPlugin` | 消息/回复处理钩子 |

示例中 `plugin` 指 Hexagon 公共子包，`hcplugin` 指 HexClaw 的扩展插件包。Manager 不自动把基础 Provider、Tool 或 Memory 插件接入应用服务，Host 自行装配。

基础插件类型继承自 Hexagon:
- `ProviderPlugin` — LLM Provider 插件
- `ToolPlugin` — 工具插件
- `MemoryPlugin` — 记忆插件

## 生命周期

```
Register → Init(config) → Start() → [运行中] → Stop()
```

所有插件实现 Hexagon 的 `plugin.Plugin` 接口。下面是接口摘要，`PluginInfo` 和 `HealthStatus` 来自 Hexagon 的 `plugin` 包：

```go
type Plugin interface {
    Info() PluginInfo
    Init(ctx context.Context, config map[string]any) error
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    Health() HealthStatus
}
```

`Manager.StartAll` 按注册顺序逐个初始化、启动，遇错立即返回，不自动停止此前已启动的插件；Host 负责失败清理。`StopAll` 按注册逆序停止，并记录停止错误。

Hexagon Registry 的事件回调在 Registry 自身写锁释放后触发；这不代表可以重入 `hcplugin.Manager`。Manager 在注册、生命周期和钩子调用时仍持有自己的锁，插件回调应避免同步调用同一个 Manager。

## ExtensionPlugin Manifest v1

`ExtensionPlugin` 在基础插件能力之外增加 `Manifest()` 声明。可为下方 `MyPlugin` 添加这个方法，保持 `Manifest.Name` 与注册名称一致。校验和上下文交付是两个不同的 Host 责任。

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
        Description: "示例插件",
    }
}
```

`MinHostVersion` 应按插件实际依赖填写：只依赖 v1 manifest/capability 协议时可保持 `0.4.0`；如果调用 v0.5 之后才新增的 Host API，应提升到对应最低版本。`Dependencies` 使用其他插件的 `Manifest.Name`，Host 需要先成功加载这些依赖。

已定义能力：
- `skills.read`：读取 Host 的 Skill registry
- `events.emit`：投递结构化事件
- `network`：发起网络请求
- `fs.read`：读取 Host 沙箱目录文件
- `fs.write`：写入 `.pending` 风格的待审核文件

实际边界见 [manager.go](../plugin/manager.go) 和 [extension.go](../plugin/extension.go)：

- `NewManager()` 不注入 Host 版本或 flags；Host 调用 `SetHostContext(hostVersion, flags)` 后，只有插件实现 `ExtensionPlugin` 且 `plugin.extension.v1` 启用时，`Register` 才校验 Manifest。该 flag 不要求所有普通插件都实现 Manifest。
- `Manifest.Dependencies` 是声明；Manager 不自动检查、排序或加载这些依赖，由 Host 自行处理。
- Manager 不自动构造或注入 `ExtensionContext`；Host 可以调用 `BuildExtensionContext`，提供能力实现，再通过自行约定的接口交给插件。`ExtensionPlugin` 本身只有 `Manifest()` 方法。
- 未声明或 Host 未提供的能力字段可为 nil；待审核文件写入语义由 Host 提供的 `FSWritePending` 实现承担。

## 快速开始

### 1. 创建 Skill 插件

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
            Description: "示例 Skill 插件",
            Author:      "your-name",
        }),
    }
}

// Skills 返回该插件提供的 Skill 列表
func (p *MyPlugin) Skills() []skill.Skill {
    return []skill.Skill{
        &MySkill{},
    }
}

// MySkill 自定义技能
type MySkill struct{}

func (s *MySkill) Name() string        { return "my-skill" }
func (s *MySkill) Description() string  { return "我的自定义技能" }
func (s *MySkill) Match(content string) bool { return false } // 仅通过 LLM Tool Use 调用

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

`ToolDefinition()` 是必需方法；只实现 `Name/Description/Match/Execute` 不满足当前 `skill.Skill`。后续 Hook、Adapter 和 Host 片段延续同一示例包，使用前补充各片段需要的 import。

### 2. 创建 Hook 插件

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

Host 需要在自己的处理链中调用 `RunMessageHooks`/`RunReplyHooks` 并处理错误。钩子返回 `nil, nil` 时保持当前消息或回复，不表示取消投递。

### 3. 创建 Adapter 插件

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

此例只包装调用者已经实现的适配器，不提供 Matrix 协议实现。Host 将收集到的适配器接入启动、停止和消息路由；`StartAll` 只调用插件生命周期，不自动启动 Adapter。

## 注册插件

在应用启动时注册：

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

registry 必须是引擎实际使用的注册表。示例用于 Host 初始化阶段；注册失败应中止启动。`StopAll` 不会自动撤销外部 Registry 中已注册的技能，Host 自行处理这些记录，并在退出时调用返回 Manager 的 `StopAll`。

## 配置与 CLI 边界

`StartAll` 接受 `map[string]map[string]any`，外层键是 `Info().Name`，配置由各插件 `Init` 解读。上例使用空配置；BasePlugin 默认保存它，但不自动应用 timeout、API Key 等自定义字段。

当前 CLI 没有第三方 Go 插件的 `plugins:` YAML 入口，也不自动加载 `.so` 或扫描插件目录。在 `hexclaw.yaml` 中写入 `plugins:` 不会注册 Go 插件。Markdown Skill、MCP 和 Go 插件是不同入口；现有可安装技能见 [HexClaw Hub](https://github.com/hexagon-codes/hexclaw-hub)，管理接口见 [技能与集成 API](api/agents-integrations.md)。

## 最佳实践

1. **继承 BasePlugin** — 使用 `plugin.BasePlugin` 获得默认的生命周期实现
2. **健康检查** — 重写 `Health()` 方法返回真实的健康状态
3. **优雅停止** — 在 `Stop()` 中释放所有资源，关闭连接
4. **配置验证** — 在 `Init()` 中验证必需配置项
5. **错误处理** — Skill 执行失败时返回明确的错误信息
6. **超时控制** — 使用 `ctx` 上下文控制超时，避免阻塞
7. **回调边界** — 区分 Registry 与 Manager 的锁，不从生命周期或钩子同步重入同一个 Manager。
8. **Host 装配** — 检查注册、启动与技能接入错误；显式处理依赖、ExtensionContext 注入和失败清理。

## 参考

- [Hexagon Plugin 包](https://github.com/hexagon-codes/hexagon/tree/v0.5.14/plugin) — 基础插件接口和注册表
- [HexClaw Skill 接口](../skill/skill.go) — Skill 接口定义
- [HexClaw Adapter 接口](../adapter/adapter.go) — 适配器接口定义
- [GitHub Issues](https://github.com/hexagon-codes/hexclaw/issues) — 问题反馈
