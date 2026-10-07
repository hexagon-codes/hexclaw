# HexClaw 公共 API

[English](api.en.md) · [返回 README](../README.md)

本文是当前源码的公共 API 入口。模块参考覆盖主服务及桌面集成的显式路由；K12 场景由独立参考覆盖。对应机器可读描述见 [OpenAPI](../api/openapi.yaml)。已发布版本以该版本的代码和文档为准；功能开关、依赖注入和运行环境决定某个部署实际提供的接口。

## 按模块查接口

| 模块 | 完整参考 |
| --- | --- |
| 对话、附件、会话、消息、流恢复与记忆 | [对话与会话](api/chat-sessions-memory.md) |
| 知识库、导入、恢复、Embedding、文档提取与渲染 | [知识与文档](api/knowledge-documents.md) |
| 模型配置、探测、Ollama、媒体、日志、系统与桌面桥接 | [模型与系统](api/models-system-media.md) |
| Agent、路由、Prompt、MCP、Skill、市场与工具观测 | [智能体与集成](api/agents-integrations.md) |
| Cron、Webhook、自治、连接、IM、工作流与团队 | [自动化与连接](api/automation-connections.md) |
| K12 档案、教材、图片任务、练习与打印 | [K12 API](../scenarios/k12/API.md) |

每个模块说明请求字段、成功与失败响应、挂载条件及实际副作用。下方保留聊天和 Cron 的最小调用示例；README 仅提供常用入口。

## 连接与身份

默认服务地址为 `http://127.0.0.1:16060`；云端使用实际服务地址，包含代理路径前缀。业务请求携带 `Authorization: Bearer <token>`，JSON 请求携带 `Content-Type: application/json`。

`hexclaw init` 在 `~/.hexclaw/hexclaw.yaml` 中生成 `server.api_token`；直接启动独立服务时也会补齐缺少的令牌，保留已有值。自定义配置使用对应文件中的令牌。Desktop 本机连接由原生层管理自己的令牌。两者不能混用。

聊天请求体的 `user_id`、`platform` 仅保留解码兼容；身份取自鉴权上下文，不能靠这两个字段切换用户。领域接口的 `agent`、记录 ID 等用于选择业务对象，校验与缺失行为见对应模块，不代表可以选择任意所有者。

以下命令假设已经启动服务，并把地址与当前服务令牌设置为：

```bash
export HEXCLAW_API_BASE="http://127.0.0.1:16060"
export HEXCLAW_API_TOKEN="当前服务的访问令牌"
```

以下入口不使用普通业务 Bearer 校验：`GET /health`、`GET /api/v1/version`、`POST /api/v1/webhooks/{name}`，以及 `GET/POST /api/v1/platforms/hooks/{provider}/{name}`。回调仍遵守已配置的 Secret 或平台签名机制，见[自动化与连接](api/automation-connections.md)。健康响应不能证明模型、任务执行或结果投递可用。

`/api/internal/desktop/*` 使用原生 Sidecar capability 并要求回环来源，普通业务令牌不能代替；这些接口列在[模型与系统](api/models-system-media.md)的内部桥接章节，不作为第三方稳定集成入口。

模块未启用时，路由可能没有挂载并返回普通 `404`，也可能保留空投影或错误提示；各模块分别说明。不要把所有 `200` 都解释为业务成功。

## 通用调用约定

- **ID 与范围**：ID 按响应中的字符串使用，不假设所有 ID 都是 UUID。路径、查询和请求体中的实例名各有用途，以对应操作为准。
- **请求格式**：JSON、multipart、二进制和 WebSocket 升级不能互换。上传元数据的字段顺序、文件限额及流式帧在对应模块说明。
- **异步任务**：接纳、运行、结果冻结和投递是不同状态。使用响应中的操作或任务 ID 读取状态与产物；结果未知时先查询，不盲目重发。
- **错误**：多数 JSON 错误有 `error`；部分操作另含 `code/message/retryable` 或业务状态。SSE 开流后的错误出现在帧中；二进制下载和 JSON 降级结果按实际 Content-Type 区分。
- **版本与幂等**：`version/revision`、`If-Match`、`Idempotency-Key` 和请求体幂等键不是统一机制；必填性、作用范围及重放行为按操作说明。
- **浏览器与原生调用**：当前 CORS 仅响应既有本地开发与 Tauri Origin，`Access-Control-Allow-Methods` 为 `GET, POST, PUT, DELETE, OPTIONS`，未列 `PATCH`；浏览器跨域能力不能仅由接口已注册推断。`OPTIONS` 预检返回 `204`。GET 路由接受 Go ServeMux 的 HEAD 匹配，但鉴权仍按实际请求方法判断，例如 `HEAD /api/v1/version` 需要业务令牌；它们不另列为业务操作。
- **扩展路由**：运行时挂载的第三方场景或插件维护自己的契约；本参考覆盖仓库内置主服务、桌面集成和 K12，不预先枚举外部扩展。

<a id="chat"></a>
## 聊天

### 请求

`POST /api/v1/chat`。至少提供非空 `message` 或有效 `attachments`；默认返回完整 JSON。此 handler 的 JSON 请求体上限为 20 MiB。

| 字段 | 契约 |
| --- | --- |
| `message` | 文本输入；有有效附件时可空 |
| `session_id` | 省略或空值创建会话；继续会话时使用上次响应值 |
| `provider`、`model` | 可选显式选择；必须属于当前可用配置 |
| `role` | 可选 Agent 角色 |
| `temperature`、`max_tokens` | 可选采样参数；未传时跟随模型/Agent 设置 |
| `metadata` | 字符串键值；内部派发保留字段由服务处理 |
| `request_id` | 可选关联与流式恢复标识；不保证未知结果可安全重发 |
| `attachments` | 图片附件数组；暂存引用使用 `attachment_id`，或使用类型、MIME、Base64 内容/URL；以服务附件校验为准 |

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/chat" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message":"你好，介绍一下你能做什么"}'
```

### 响应

成功为 `200 application/json`。下面仅示意主要字段，回复文本由模型决定：

```json
{
  "reply": "模型生成的回答",
  "session_id": "服务返回的会话 ID",
  "reasoning_disclosure": {}
}
```

响应还可包含 `message_content`、`render_manifest`、`usage`、`tool_calls`、`blocks`、`knowledge_hits`、`memory_hits`、消息 ID、序列号和运行事件。结构化消息与产物字段用于实际展示与交付，不能只根据 HTTP 状态判断文件或投递已经完成。

继续同一会话，将实际返回的 ID 代入：

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/chat" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message":"继续刚才的话题","session_id":"上次响应的 session_id"}'
```

### 流式响应

请求添加 `Accept: text/event-stream` 并使用 `curl -N`。同一路径返回 `data:` 帧，每帧是回复 chunk JSON；成功流结束标记为 `data: [DONE]`。

错误帧包含 `error`、`done: true`，以及可选 `code`、`retryable`。SSE 开流后 HTTP 已是 `200`，客户端仍须消费成功或错误终态；`retryable` 是错误分类提示，不代表结果未知时可以自动重放模型请求。

### 失败响应

聊天通常返回 `{"error":"..."}`；已分类模型错误另带 `code`、`retryable`。网关拒绝可另带 `code`、`layer`。

| HTTP | 原因 |
| --- | --- |
| `400` | 非法 JSON、空输入、无效附件、显式 Provider/模型或采样参数无效 |
| `401` | 缺少或无效的当前服务令牌 |
| `403` | 当前网关策略拒绝请求 |
| `404` | 附件暂存引用不可用 |
| `422` | `MODEL_CAPABILITY_MISMATCH` |
| `429` | `UPSTREAM_RATE_LIMITED` |
| `500` | 未分类的引擎或应用错误 |
| `503` | 引擎未就绪、`UPSTREAM_UNAVAILABLE` 或 `UPSTREAM_POOL_EXHAUSTED` |

<a id="cronjob"></a>
## 定时任务

### 统一入口

`POST /api/v1/cronjob` 使用 `action` 指定操作；成功均返回 `200 application/json`。此路径不根据 `Accept` 切换 SSE，JSON 请求体上限为 1 MiB。

| action | 输入 | 成功响应字段 |
| --- | --- | --- |
| `create` | `draft` | `action`、`job`、`quota`、`ok: true` |
| `update` | `job_id`、完整 `draft` | `action`、替换后的 `job`、`quota`、`ok: true` |
| `list` | 可选 `include_paused`，默认 `false` | `action`、`jobs`、`total`、`quota` |
| `pause`、`resume`、`remove` | `job_id` | `action`、`ok: true` |
| `run` | `job_id` | `action`、`ok: true`；仅接纳触发 |

空 `jobs` 和 `total: 0` 可能省略；客户端按空数组和零处理。当前活跃任务上限为 `30`。暂停任务不能直接 `run`，先使用 `resume`。

写操作可传 `idempotency_key`。它是当前 owner 范围内、进程内、五分钟的成功响应缓存，命中带 `X-Idempotent-Replay: 1`。各写 action 共用 owner/key，不同操作使用不同 key；失败不缓存，重启不保留，不是持久或并发接纳幂等保证。

### draft

`create` 与 `update` 要求非空 `name`、`schedule`、`prompt`。更新是完整替换，未提交的字段不自动继承。

| 字段 | 契约 |
| --- | --- |
| `schedule` | Cron 表达式或服务支持的 `@daily`、`@every` 等形式 |
| `script`、`runtime` | 非空 script 直接接纳预编写脚本，跳过模型编译；runtime 默认 `starlark`，脚本须调用 `emit({...})` 或 `wake_agent(...)` 产生结果；也支持环境可用的 `python3` |
| 无 `script` | 服务按 prompt 选择脚本编译或 Agent 模式；编译需要可用模型 |
| `continuous` | 无 script 的持续任务使用 Agent 模式 |
| `deliver`、`chat_id` | 已配置投递目标；IM 投递需要目标会话 ID |
| `timeout_s` | `0` 使用运行模式默认值 |
| `paused` | 创建后的初始暂停状态 |

`no_agent`、`enabled_toolsets` 虽可出现在解析层 draft 中，当前统一 create/update 不消费这两个字段；不能用它们保证执行模式或工具范围。

创建预编写脚本示例，初始暂停：

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/cronjob" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"action":"create","idempotency_key":"daily-status-create-1","draft":{"name":"每日状态示例","schedule":"0 9 * * *","prompt":"每天输出状态","runtime":"starlark","script":"emit({\"status\":\"success\",\"data\":{\"message\":\"ready\"}})","paused":true}}'
```

响应摘录：

```json
{
  "action": "create",
  "job": {"id": "实际任务 ID", "status": "paused", "spec": {"runtime": "starlark"}},
  "quota": {"used": 1, "limit": 30},
  "ok": true
}
```

查询任务可发送 `{"action":"list","include_paused":true}`。用实际 `job.id` 发送 `{"action":"resume","job_id":"...","idempotency_key":"resume-1"}`，再发送 `{"action":"run","job_id":"...","idempotency_key":"run-1"}`。

`run` 接纳响应不是执行或投递成功。读取 `GET /api/v1/cron/jobs/{id}/history?limit=20`，检查实际 `status`、`result`、`error`、`stdout`、`stderr`、`exit_code` 等。历史响应为 `{"history":[...],"total":...}`，按新到旧排列，历史 `id` 使用 JSON 字符串编码；缺省 limit 为 `50`。

流式创建保留独立 `POST /api/v1/cron/jobs/stream`。旧集合创建/列表，以及 `/pause`、`/resume`、`/trigger`、DELETE 等分散路径不作为当前公共操作入口。

### 失败响应

Cron handler 错误为 `{"code":"...","message":"...","error":"..."}`，兼容 `error` 等于 `message`；鉴权失败仍使用 `{"error":"..."}`。

| HTTP | code / 原因 |
| --- | --- |
| `400` | `BAD_REQUEST`、`CRON_UNKNOWN_ACTION`、`CRON_INVALID_SCHEDULE`、`CRON_COMPILE_FAILED`、`CRON_VALIDATE_FAILED` |
| `401` | 当前服务令牌无效 |
| `404` | 目标不属于当前 owner 或不存在；update 使用 `BAD_REQUEST`，生命周期操作使用 `CRON_JOB_NOT_FOUND` |
| `409` | `CRON_JOB_PAUSED` |
| `429` | `CRON_QUOTA_EXCEEDED` |
| `500` | `INTERNAL_ERROR`，包括未分类的编译、替换或持久化失败 |
| `503` | `CRON_EXECUTOR_UNAVAILABLE`；未注入调度器的 handler 可返回 `CRON_DISABLED`，普通关闭模块时路由不挂载 |

## 实现参考

[聊天与路由](../api/server.go) · [Cron 统一操作](../api/handler_cronjob_unified.go) · [Cron 错误](../api/errors.go)。这些链接用于理解实现，公共调用以本文和 OpenAPI 契约为入口。
