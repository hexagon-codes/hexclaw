# 自动化、连接与工作流 API

[English](automation-connections.en.md) · [API 总览](../api.md) · [OpenAPI](../../api/openapi.yaml)

本参考覆盖当前服务路由的57个方法/路径组合。路径均以 `/api/v1` 开头；以下字段和行为来自当前工作区实现。示例只说明格式，没有执行任务、发送消息或修改权限设置。

除明确列出的公共回调外，管理接口沿用总览中的 Bearer 认证。`POST /api/v1/webhooks/{name}` 与 `GET/POST /api/v1/platforms/hooks/{provider}/{name}` 不走 API Bearer，而是使用对应Webhook或平台的签名/挑战机制。**签名校验并非免认证。**

`x-availability` 记录启动时的注册条件：缺少scheduler、webhookMgr、instanceMgr、connectorStore或canvasSvc时，相应条件路由不会注册，通常表现为404；handler内503并不意味着未启用时一定能访问该路径。Workflow和Team路由无此注册条件。

除Team写入为64 KiB外，本模块JSON写入通常使用1 MiB body上限；解码超限在管理接口按400返回，公共Webhook接收显式使用413。大多数管理错误为 `{"error":"…"}`，Cron使用带code/message/error的APIError；公共平台回调错误可能是文本。HTTP 200不等于探测成功、任务完成或外部送达。

输入表中的required只代表必填字段；其余字段按各handler的零值、默认值和条件规则处理。时间字段通常为RFC3339；实例保存响应的updated_at是HTTP-date。长度约束主要适用于新增/改变字段，原样保存的旧值可能沿用既有合同。

## 端点

### `POST /api/v1/connections/test` — 验证未保存的连接

仅瞬态探测，不保存凭据、不发送消息。总超时10秒；邮件先SMTP AUTH，可选IMAP登录；其他平台调用适配器ValidateConfig，可能访问平台认证API。HTTP 200中的ok=false表示探测失败。

实现: [`handleConnectionsTest`](../../api/handler_connections.go#L98). 注册条件: `Always registered.`.

请求体（必需）: `application/json`: [AutomationConnectionTestRequest](#automationconnectiontestrequest).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Validate an unsaved connection. Inspect business status in the response. `application/json` [AutomationConnectionTestResponse](#automationconnectiontestresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Invalid JSON, missing type/config, or unsupported type. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "type": "telegram",
  "config": {
    "token": "example-token"
  }
}
```

### `GET /api/v1/connectors` — 列出数据连接器

按创建时间列出脱敏摘要，不返回token。

实现: [`handleListConnectors`](../../api/handler_connectors.go#L19). 注册条件: `s.connectorStore != nil`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | List data connectors. Inspect business status in the response. `application/json` [AutomationConnectors](#automationconnectors) |
| 401 | Bearer authentication failed; see API overview. |

### `POST /api/v1/connectors` — 新增数据连接器

支持github/notion。先真实验证token，再保存；name缺省为provider。请求超时15秒；返回脱敏摘要。保存失败也映射400，不能将所有400当作字段错误。

实现: [`handleCreateConnector`](../../api/handler_connectors.go#L31). 注册条件: `s.connectorStore != nil`.

请求体（必需）: `application/json`: [AutomationConnectorCreateRequest](#automationconnectorcreaterequest).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Create a data connector. Inspect business status in the response. `application/json` [AutomationConnectorSummary](#automationconnectorsummary) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed request, invalid provider/token, length validation, provider probe or persistence failure. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "provider": "github",
  "name": "Source repository",
  "token": "example-token"
}
```

### `DELETE /api/v1/connectors/{id}` — 删除数据连接器

删除本地连接器记录；不撤销第三方token。所有Store.Delete错误（含持久化失败）均映射404。

实现: [`handleDeleteConnector`](../../api/handler_connectors.go#L56). 注册条件: `s.connectorStore != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Delete a data connector. Inspect business status in the response. `application/json` [AutomationMessage](#automationmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Connector missing or deletion/persistence failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/connectors/test` — 验证数据连接器token

真实请求GitHub/Notion认证API，不保存。HTTP 200仍须检查ok；探测失败detail给出原因。

实现: [`handleTestConnector`](../../api/handler_connectors.go#L70). 注册条件: `s.connectorStore != nil`.

请求体（必需）: `application/json`: [AutomationConnectorTestRequest](#automationconnectortestrequest).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Probe a connector token. Inspect business status in the response. `application/json` [AutomationConnectionTestResponse](#automationconnectiontestresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON or token exceeds 65536 UTF-8 bytes. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "provider": "notion",
  "token": "example-token"
}
```

### `GET /api/v1/connectors/{id}/resources` — 读取连接器资源

使用已存token访问第三方只读资源；不是本地缓存清单。15秒超时；找不到连接器和上游失败均为502。

实现: [`handleConnectorResources`](../../api/handler_connectors.go#L91). 注册条件: `s.connectorStore != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | List connector resources. Inspect business status in the response. `application/json` [AutomationConnectorResources](#automationconnectorresources) |
| 401 | Bearer authentication failed; see API overview. |
| 502 | Missing connector or provider resource request failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/webhooks/{name}` — 接收Webhook回调

公共回调不使用API Bearer。按Webhook类型验证签名后才处理。generic/github/gitlab响应accepted仅代表接纳，不代表业务完成；测试事件不派发。K12使用独立签名、nonce和event_id协议，返回202 receipt，后续查询回执判断终态。

实现: [`Manager.Handler`](../../webhook/webhook.go#L358). 注册条件: `s.webhookMgr != nil`.

认证：平台/Webhook原生认证，不要求API Bearer。

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |
| query | `test` | no | `string`; enum: `1` Generic/GitHub/GitLab test event: verify and parse without dispatch. |
| header | `X-HexClaw-Webhook-Test` | no | `string`; — Any nonempty value marks a generic/GitHub/GitLab test event. |
| header | `X-Webhook-Signature` | no | `string`; — Generic HMAC-SHA256 hex of raw body; optional sha256= prefix. |
| header | `X-Signature` | no | `string`; — Fallback generic signature header. |
| header | `X-Hub-Signature-256` | no | `string`; — GitHub HMAC-SHA256 hex of raw body, convention sha256=<hex>. |
| header | `X-Gitlab-Token` | no | `string`; — GitLab configured secret. |
| header | `X-GitHub-Event` | no | `string`; — GitHub event name; ping is a test event. |
| header | `X-Event-Type` | no | `string`; — Generic event name; defaults to generic. GitLab reads payload.object_kind. |
| header | `X-HexClaw-Timestamp` | no | `string`; — K12 RFC3339 or Unix-seconds timestamp in the permitted five-minute window. |
| header | `X-HexClaw-Nonce` | no | `string`; maxLength: 128 K12 fresh nonblank nonce; consumed once. |
| header | `X-HexClaw-Signature` | no | `string`; — K12 sha256=<hex HMAC(secret, timestamp \|\| nonce \|\| raw-body)>; no separators. |

请求体（必需）: `application/json`: object (map) / [AutomationK12Envelope](#automationk12envelope); `text/plain`: `string`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Generic/GitHub/GitLab的accepted正文为JSON，但未显式设置JSON Content-Type，Go HTTP可将其标记为text/plain；test响应使用application/json。`application/json` / `text/plain` [AutomationWebhookAccepted](#automationwebhookaccepted) |
| 400 | Body read/JSON/event/payload/Content-Type validation failed. `application/json` [AutomationWebhookRejection](#automationwebhookrejection); `text/plain` `string` |
| 401 | Missing/invalid signature, timestamp or nonce. `application/json` [AutomationWebhookRejection](#automationwebhookrejection); `text/plain` `string` |
| 403 | K12 event is outside the binding allowlist. `application/json` [AutomationWebhookRejection](#automationwebhookrejection); `text/plain` `string` |
| 404 | Webhook or binding not found. `application/json` [AutomationWebhookRejection](#automationwebhookrejection); `text/plain` `string` |
| 409 | K12 nonce replay or same event_id with different payload. `application/json` [AutomationWebhookRejection](#automationwebhookrejection); `text/plain` `string` |
| 413 | Body exceeds 1 MiB. `application/json` [AutomationWebhookRejection](#automationwebhookrejection); `text/plain` `string` |
| 423 | Webhook/binding disabled. `application/json` [AutomationWebhookAccepted](#automationwebhookaccepted) / [AutomationWebhookRejection](#automationwebhookrejection); `text/plain` `string` |
| 429 | K12 receiver rate limit. `application/json` [AutomationWebhookRejection](#automationwebhookrejection); `text/plain` `string` |
| 500 | Binding/receipt storage error. `application/json` [AutomationWebhookRejection](#automationwebhookrejection); `text/plain` `string` |
| 202 | K12 admission or idempotent event replay; receipt is not necessarily terminal. `application/json` [AutomationReceiptResponse](#automationreceiptresponse) |

请求示例:

```json
{
  "event_type": "push",
  "message": "Example event"
}
```

### `GET /api/v1/webhooks` — 列出Webhook或K12回执

缺省按可信owner列出普通Webhook；agent_id非空时追加该Agent的K12绑定。receipt_id优先于binding_name，两种回执查询都要求agent_id；binding_name历史最多50条。

实现: [`handleListWebhooks`](../../api/handler_webhook.go#L19). 注册条件: `s.webhookMgr != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| query | `agent_id` | no | `string`; — Required for receipt_id/binding_name queries; adds K12 bindings otherwise. |
| query | `receipt_id` | no | `string`; — Single receipt; takes precedence over binding_name. |
| query | `binding_name` | no | `string`; — Receipt history, at most 50 entries. |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | List webhooks or K12 receipts. Inspect business status in the response. `application/json` [AutomationWebhooks](#automationwebhooks) / [AutomationReceiptResponse](#automationreceiptresponse) / [AutomationReceipts](#automationreceipts) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Receipt query missing agent_id. `application/json` [AutomationError](#automationerror) |
| 404 | Owned binding/receipt not found. `application/json` [AutomationError](#automationerror) |
| 500 | Storage query failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/webhooks` — 注册Webhook

普通Webhook要求name和prompt或job_id，默认enabled=false、type=generic；自动生成secret只返回一次。K12要求name/agent_id/learner_id/allowed_events，禁止自带secret；返回binding和一次性secret。绑定cron必须属于当前owner。

实现: [`handleRegisterWebhook`](../../api/handler_webhook.go#L109). 注册条件: `s.webhookMgr != nil`.

请求体（必需）: `application/json`: [AutomationRegisterWebhookRequest](#automationregisterwebhookrequest).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Register a webhook. Inspect business status in the response. `application/json` [AutomationWebhookCreated](#automationwebhookcreated) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON, length limit, invalid K12 fields/binding. `application/json` [AutomationError](#automationerror) |
| 404 | Bound Cron job not found for owner. `application/json` [AutomationError](#automationerror) |
| 409 | Webhook name already exists. `application/json` [AutomationError](#automationerror) |
| 500 | Job validation, registration or storage failed. `application/json` [AutomationError](#automationerror) |
| 503 | Webhook manager or required Cron scheduler unavailable. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "name": "repo-events",
  "type": "github",
  "prompt": "Summarize repository events",
  "enabled": false
}
```

### `PATCH /api/v1/webhooks/{name}` — 更新Webhook或重试K12回执

普通Webhook只更新enabled。K12使用query agent_id校验owner；支持enabled、allowed_events（同时更新allowed_workflows）、rotate_secret。retry_receipt_id必须独立提交，仅可重试已有安全证据的failed回执；保持事件身份并可能再次派发实际业务。多个配置修改按顺序执行，后一步失败可能保留前一步变更。

实现: [`handleUpdateWebhook`](../../api/handler_webhook.go#L257). 注册条件: `s.webhookMgr != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |
| query | `agent_id` | no | `string`; — Required to resolve owned K12 binding; not used by generic webhooks. |

请求体（必需）: `application/json`: [AutomationUpdateWebhookRequest](#automationupdatewebhookrequest).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Update a webhook or retry a K12 receipt. Inspect business status in the response. `application/json` [AutomationWebhookUpdated](#automationwebhookupdated) / [AutomationReceiptResponse](#automationreceiptresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON, missing operation, invalid events or conflicting retry fields. `application/json` [AutomationError](#automationerror) |
| 404 | Owned webhook/receipt not found. `application/json` [AutomationError](#automationerror) |
| 409 | Receipt is not safely retryable. `application/json` [AutomationError](#automationerror) |
| 500 | Binding read/update, rotation or retry failed. `application/json` [AutomationError](#automationerror) |
| 503 | Webhook manager unavailable. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "enabled": true
}
```

### `DELETE /api/v1/webhooks/{name}` — 删除Webhook

按可信owner删除；K12还需agent_id。删除后回收对应任务授权；K12仍有执行中回执时409。

实现: [`handleDeleteWebhook`](../../api/handler_webhook.go#L352). 注册条件: `s.webhookMgr != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |
| query | `agent_id` | no | `string`; — Required to resolve owned K12 binding; not used by generic webhooks. |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Delete a webhook. Inspect business status in the response. `application/json` [AutomationMessage](#automationmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Owned webhook not found. `application/json` [AutomationError](#automationerror) |
| 409 | K12 binding has active receipts. `application/json` [AutomationError](#automationerror) |
| 500 | Storage read/delete failed. `application/json` [AutomationError](#automationerror) |
| 503 | Webhook manager unavailable. `application/json` [AutomationError](#automationerror) |

### `GET /api/v1/automation/status` — 读取自动化组件状态

enabled为配置开关，state为disabled/unavailable/ready。ready由实际组件是否已注入决定；不能用空任务列表判断组件不可用。

实现: [`handleAutomationStatus`](../../api/handler_automation_status.go#L11). 注册条件: `Always registered.`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Read automation availability. Inspect business status in the response. `application/json` [AutomationStatus](#automationstatus) |
| 401 | Bearer authentication failed; see API overview. |

### `GET /api/v1/autonomy/profile` — 读取自动化权限档位

返回当前profile、四种可选profiles及实际matrix快照；只读。

实现: [`handleGetAutonomyProfile`](../../api/handler_autonomy.go#L104). 注册条件: `Always registered.`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Read the autonomy profile. Inspect business status in the response. `application/json` [AutomationProfile](#automationprofile) |
| 401 | Bearer authentication failed; see API overview. |

### `PUT /api/v1/autonomy/profile` — 更新自动化权限档位

profile先trim和转小写。先保存配置，成功后更新运行中的策略，无需重启；保存失败不改变运行态。本参考仅记录既有行为。

实现: [`handleUpdateAutonomyProfile`](../../api/handler_autonomy.go#L122). 注册条件: `Always registered.`.

请求体（必需）: `application/json`: [AutomationProfileUpdate](#automationprofileupdate).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Update the autonomy profile. Inspect business status in the response. `application/json` [AutomationProfile](#automationprofile) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON or unknown profile. `application/json` [AutomationError](#automationerror) |
| 503 | Configuration is not ready. `application/json` [AutomationError](#automationerror) |
| 500 | Configuration persistence failed. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "profile": "balanced"
}
```

### `POST /api/v1/autonomy/preflight` — 预检自动化所需能力

source非空。workflow_id可补静态工具；cron_job_id可在prompt为空时补已有任务prompt和投递事实。仅评估，不执行任务、不授予权限；all_clear仅覆盖预估范围，basis=heuristic可能漏判。缺失的workflow/job不会由此接口自动报404。

实现: [`handleAutonomyPreflight`](../../api/handler_autonomy.go#L188). 注册条件: `Always registered.`.

请求体（必需）: `application/json`: [AutomationPreflightRequest](#automationpreflightrequest).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Preflight automation capabilities. Inspect business status in the response. `application/json` [AutomationPreflightResult](#automationpreflightresult) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON or blank source. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "source": "workflow",
  "workflow_id": "wf-example"
}
```

### `GET /api/v1/autonomy/summary` — 读取自动化治理总览

按user_id（缺省api-user）汇总Cron与Webhook，并包含已存Workflow。组合预检与最近未解决阻断；counts.grants为全部活跃授权数。部分依赖查询失败被跳过或记录日志，200不是完整性保证。

实现: [`handleAutonomySummary`](../../api/handler_autonomy.go#L277). 注册条件: `Always registered.`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| query | `user_id` | no | `string`; default: `"api-user"`  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Read the autonomy summary. Inspect business status in the response. `application/json` [AutomationSummary](#automationsummary) |
| 401 | Bearer authentication failed; see API overview. |

### `GET /api/v1/autonomy/decisions` — 查询权限决策

支持source/task_ref/decision过滤。按at、id降序；limit<=0、无效或>500使用100。治理存储未注入时返回空列表。

实现: [`handleListAutonomyDecisions`](../../api/handler_autonomy.go#L419). 注册条件: `Always registered.`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| query | `source` | no | `string`; —  |
| query | `task_ref` | no | `string`; —  |
| query | `decision` | no | `string`; enum: `allow`, `pending`, `deny`  |
| query | `limit` | no | `integer`; default: `100` Nonpositive, nonnumeric or >500 falls back to 100. |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | List autonomy decisions. Inspect business status in the response. `application/json` [AutomationDecisions](#automationdecisions) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Decision store query failed. `application/json` [AutomationError](#automationerror) |

### `GET /api/v1/autonomy/grants` — 查询活跃任务授权

task_ref可选；省略列出全部活跃授权。未注入授权存储时返回空列表。

实现: [`handleListAutonomyGrants`](../../api/handler_autonomy.go#L443). 注册条件: `Always registered.`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| query | `task_ref` | no | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | List active task grants. Inspect business status in the response. `application/json` [AutomationGrants](#automationgrants) |
| 401 | Bearer authentication failed; see API overview. |

### `POST /api/v1/autonomy/grants` — 创建任务授权

task_ref与至少一条有效entries必填。owner只从可信上下文取得；entries修剪、去重并丢弃空项和裸*，source转小写。相同活跃task_ref/source/entries集合返回已有授权；该幂等判断不比较note或security_scope_digest。只创建授权，不自动恢复任务。

实现: [`handleCreateAutonomyGrant`](../../api/handler_autonomy.go#L469). 注册条件: `Always registered.`.

请求体（必需）: `application/json`: [AutomationCreateGrantRequest](#automationcreategrantrequest).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Create a task grant. Inspect business status in the response. `application/json` object {`grant`: [AutomationGrant](#automationgrant)} |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON, invalid grant, or storage create/reload failure. `application/json` [AutomationError](#automationerror) |
| 503 | Grant storage is unavailable. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "task_ref": "workflow:wf-example",
  "source": "workflow",
  "entries": [
    "web_search"
  ],
  "note": "Task-specific scope"
}
```

### `DELETE /api/v1/autonomy/grants/{id}` — 撤销任务授权

撤销持久化授权并刷新活跃镜像。

实现: [`handleRevokeAutonomyGrant`](../../api/handler_autonomy.go#L494). 注册条件: `Always registered.`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Revoke a task grant. Inspect business status in the response. `application/json` [AutomationMessage](#automationmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Grant revoke failed or grant not found. `application/json` [AutomationError](#automationerror) |
| 503 | Grant storage is unavailable. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/cronjob` — 统一Cron操作

七种action、完整字段、幂等边界和示例见上级Cron参考；本路径不切换SSE。

实现: [`handleCronjobUnified`](../../api/handler_cronjob_unified.go#L120). 注册条件: `s.scheduler != nil`.

请求体（必需）: `application/json`: `CronJobRequest`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | action 对应响应；缓存命中时带 X-Idempotent-Replay: 1 `application/json` `CronJobResponse` |
| 400 | BAD_REQUEST / CRON_UNKNOWN_ACTION / CRON_INVALID_SCHEDULE / CRON_COMPILE_FAILED / CRON_VALIDATE_FAILED `application/json` `APIError` |
| 401 | Bearer authentication failed; see API overview. |
| 404 | 目标不存在或不属于当前 owner；update 为 BAD_REQUEST，其他生命周期操作为 CRON_JOB_NOT_FOUND `application/json` `APIError` |
| 409 | CRON_JOB_PAUSED（暂停任务不能 run） `application/json` `APIError` |
| 429 | CRON_QUOTA_EXCEEDED；当前活跃任务上限为 30 `application/json` `APIError` |
| 500 | INTERNAL_ERROR；包括未分类的创建、替换或存储错误 `application/json` `APIError` |
| 503 | CRON_EXECUTOR_UNAVAILABLE；handler 内部的 CRON_DISABLED 仅在调度器未注入时出现，普通未启用实例不会挂载本路径 `application/json` `APIError` |

### `POST /api/v1/cron/parse` — 解析自然语言定时任务

只解析草稿，不创建任务。hints.locale当前未参与解析；可能调用已配置模型。成功解析或模型未就绪/调用失败/输出不完整均返回200，后者needs_clarification=true；tier固定2。

实现: [`handleCronParse`](../../api/handler_cron_parse.go#L62). 注册条件: `s.scheduler != nil`.

请求体（必需）: `application/json`: [AutomationCronParseRequest](#automationcronparserequest).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Parse a natural-language scheduled task. Inspect business status in the response. `application/json` [AutomationCronParseResponse](#automationcronparseresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON or blank text; APIError shape. `application/json` `APIError` |

请求示例:

```json
{
  "text": "每天早上9点生成摘要",
  "hints": {
    "locale": "zh-CN"
  }
}
```

### `POST /api/v1/cron/jobs/stream` — 流式创建定时任务

先校验请求与30个活跃任务/用户配额，再开始SSE。progress报告编译阶段；done带job/spec_preview；error带error/stage。流内error仍是HTTP 200。断开请求会取消上下文；在结果未知时先查任务列表，不能把流关闭当创建失败后盲重发。

实现: [`handleAddCronJobSSE`](../../api/handler_cron.go#L123). 注册条件: `s.scheduler != nil`.

请求体（必需）: `application/json`: [AutomationAddCronJobRequest](#automationaddcronjobrequest).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | SSE: progress -> done, or progress -> error. error remains HTTP 200 after headers. JSON frame schemas: AutomationSSEProgress, AutomationSSEDone, AutomationSSEError. `text/event-stream` `string` |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed request, missing fields, invalid lengths, or unsupported once type; APIError. `application/json` `APIError` |
| 429 | Active job quota reached; APIError CRON_QUOTA_EXCEEDED. `application/json` `APIError` |
| 500 | 配额查询失败返回APIError（code=INTERNAL_ERROR）；SSE初始化失败返回仅error的形状。`application/json` `APIError` / [AutomationError](#automationerror) |
| 503 | Scheduler missing inside handler; normally this route is not registered. `application/json` `APIError` |

请求示例:

```json
{
  "name": "Daily summary",
  "schedule": "0 9 * * *",
  "prompt": "Summarize recent notes",
  "paused": true
}
```

### `GET /api/v1/cron/jobs/{id}/history` — 查询Cron执行历史

读取实际执行与投递历史；limit默认50，成功接纳不代表实际完成。

实现: [`handleCronJobHistory`](../../api/handler_extended.go#L48). 注册条件: `s.scheduler != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |
| query | `limit` | no | `integer`; default: `50`; minimum: 1 正整数；缺省或无效值使用服务默认 50 |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | 从新到旧的历史记录；检查执行状态与实际结果，而非仅检查 run 接纳 `application/json` object {`history`: array&lt;`JobHistory`&gt;, `total`: `integer`} |
| 401 | Bearer authentication failed; see API overview. |
| 404 | 任务不存在或历史查询失败 `application/json` `MessageError` |

### `GET /api/v1/connections` — 列出连接摘要

返回全部实例的脱敏摘要，不含config或凭据。receive/send能力由provider推导；摘要不代表实际送达证明。

实现: [`handleListConnections`](../../api/handler_connections.go#L55). 注册条件: `s.instanceMgr != nil`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | List connection summaries. Inspect business status in the response. `application/json` [AutomationConnections](#automationconnections) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Instance store read failed. `application/json` [AutomationError](#automationerror) |

### `GET /api/v1/platforms/instances` — 列出平台实例

先核对运行中适配器的实时连接健康，再返回实例列表；敏感config值掩码。不是单纯读取静态配置。

实现: [`handleListInstances`](../../api/handler_instances.go#L191). 注册条件: `s.instanceMgr != nil`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | List platform instances. Inspect business status in the response. `application/json` [AutomationInstances](#automationinstances) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Instance read/reconciliation failed. `application/json` [AutomationError](#automationerror) |

### `GET /api/v1/platforms/instances/health` — 读取全部实例健康

执行实际实例健康检查，返回逐实例健康报告；healthy与status需分别读取。

实现: [`handleListInstanceHealth`](../../api/handler_instances.go#L440). 注册条件: `s.instanceMgr != nil`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Read health for all instances. Inspect business status in the response. `application/json` [AutomationInstanceHealth](#automationinstancehealth) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Health enumeration failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/platforms/instances` — 按名称保存平台实例

POST使用body.name，PUT使用path.name覆盖body.name。provider/name必填；id字段忽略。未给config时存空对象，未给enabled时为false；掩码凭据回传可保留已有secret，不能用掩码新建凭据。保存后enabled=true会停止并启动实例，false会停止。启动失败500可能发生在保存已成功之后。

实现: [`handleUpsertInstance`](../../api/handler_instances.go#L210). 注册条件: `s.instanceMgr != nil`.

请求体（必需）: `application/json`: [AutomationUpsertInstanceRequest](#automationupsertinstancerequest) + required: `provider`, `name`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Upsert a platform instance by name. Inspect business status in the response. `application/json` [AutomationInstanceResponse](#automationinstanceresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed request, missing provider/name, unsupported provider, invalid config/mask, or length/write validation. `application/json` [AutomationError](#automationerror) |
| 500 | Store read or adapter startup failed. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "provider": "telegram",
  "name": "example-bot",
  "enabled": false,
  "config": {
    "token": "example-token"
  }
}
```

### `PUT /api/v1/platforms/instances/by-id/{id}` — 按ID更新平台实例

path.id定位现有实例；空provider/name沿用旧值，省略config沿用旧配置，enabled省略仍是false。先停止旧实例，再写入并按enabled启动；允许更名。响应updated_at是HTTP-date，列表时间是RFC3339。

实现: [`handleUpdateInstanceByID`](../../api/handler_instances.go#L267). 注册条件: `s.instanceMgr != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

请求体（必需）: `application/json`: [AutomationUpsertInstanceRequest](#automationupsertinstancerequest).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Update a platform instance by ID. Inspect business status in the response. `application/json` [AutomationInstanceResponse](#automationinstanceresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON, invalid id/config/mask/length or write validation. `application/json` [AutomationError](#automationerror) |
| 404 | Instance id not found. `application/json` [AutomationError](#automationerror) |
| 500 | Store read or adapter startup failed. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "name": "renamed-bot",
  "enabled": false
}
```

### `DELETE /api/v1/platforms/instances/by-id/{id}` — 按ID删除实例

与按名称删除的级联规则相同，但未知ID返回404。

实现: [`handleDeleteInstanceByID`](../../api/handler_instances.go#L366). 注册条件: `s.instanceMgr != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Delete an instance by ID. Inspect business status in the response. `application/json` [AutomationNamedMessage](#automationnamedmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Blank id. `application/json` [AutomationError](#automationerror) |
| 404 | Instance id not found. `application/json` [AutomationError](#automationerror) |
| 500 | Read, delete or routing cleanup failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/platforms/instances/by-id/{id}/test` — 按ID检查已保存实例

先按ID找实例，再复用按名称的运行态健康检查；不发送消息。

实现: [`handleTestInstanceByID`](../../api/handler_instances.go#L496). 注册条件: `s.instanceMgr != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Check a saved instance by ID. Inspect business status in the response. `application/json` [AutomationInstanceTestResponse](#automationinstancetestresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Instance id not found. `application/json` [AutomationError](#automationerror) |
| 500 | Instance lookup/health query failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/platforms/instances/by-id/{id}/send-test` — 向外部会话发送测试消息

此接口确有外发副作用。实例须enabled。request_id非空：根据现有有效绑定冻结目标并去重，30秒内发送，重放只读原回执；不健康或无绑定时不发，accepted不证明送达，未知结果不自动重发。request_id为空：target/content必填，启动实例后向指定target发送；发送失败也可返回200 success=false。示例仅供阅读。

实现: [`handleSendTestInstanceByID`](../../api/handler_instances.go#L511). 注册条件: `s.instanceMgr != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

请求体（必需）: `application/json`: [AutomationSendTestRequest](#automationsendtestrequest).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Send a test message to external conversations. Inspect business status in the response. `application/json` [AutomationTestDeliveryResult](#automationtestdeliveryresult) / [AutomationDirectSendResult](#automationdirectsendresult) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Disabled instance, malformed JSON, or missing direct target/content. `application/json` [AutomationError](#automationerror) / [AutomationDirectSendResult](#automationdirectsendresult) |
| 404 | Instance id not found. `application/json` [AutomationError](#automationerror) / [AutomationDirectSendResult](#automationdirectsendresult) |
| 500 | Instance lookup/start, frozen-delivery read or send processing failed; pending may be true. `application/json` [AutomationError](#automationerror) / [AutomationDirectSendResult](#automationdirectsendresult) |

请求示例:

```json
{
  "request_id": "connection-check-example",
  "content": "Connection check"
}
```

### `PUT /api/v1/platforms/instances/{name}` — 按名称保存平台实例

POST使用body.name，PUT使用path.name覆盖body.name。provider/name必填；id字段忽略。未给config时存空对象，未给enabled时为false；掩码凭据回传可保留已有secret，不能用掩码新建凭据。保存后enabled=true会停止并启动实例，false会停止。启动失败500可能发生在保存已成功之后。

实现: [`handleUpsertInstance`](../../api/handler_instances.go#L210). 注册条件: `s.instanceMgr != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |

请求体（必需）: `application/json`: [AutomationUpsertInstanceRequest](#automationupsertinstancerequest) + required: `provider`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Upsert a platform instance by name. Inspect business status in the response. `application/json` [AutomationInstanceResponse](#automationinstanceresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed request, missing provider/name, unsupported provider, invalid config/mask, or length/write validation. `application/json` [AutomationError](#automationerror) |
| 500 | Store read or adapter startup failed. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "provider": "telegram",
  "name": "example-bot",
  "enabled": false,
  "config": {
    "token": "example-token"
  }
}
```

### `DELETE /api/v1/platforms/instances/{name}` — 按名称删除实例

停止并删除实例，清理该实例路由；同平台已无其他实例时清理平台遗留规则。不存在名称仍200。清理失败500可能意味着实例已删除。

实现: [`handleDeleteInstance`](../../api/handler_instances.go#L342). 注册条件: `s.instanceMgr != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Delete an instance by name. Inspect business status in the response. `application/json` [AutomationNamedMessage](#automationnamedmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Read, delete or routing cleanup failed. `application/json` [AutomationError](#automationerror) |

### `GET /api/v1/platforms/instances/{name}/health` — 读取指定实例健康

按实例名称进行运行态健康检查。未知名称也由当前handler映射500。

实现: [`handleGetInstanceHealth`](../../api/handler_instances.go#L452). 注册条件: `s.instanceMgr != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Read instance health. Inspect business status in the response. `application/json` [AutomationHealthReport](#automationhealthreport) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Instance lookup/health query failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/platforms/instances/{name}/test` — 检查已保存实例

执行运行态健康检查，不发送测试消息。HTTP 200 success=false是健康检查未通过。

实现: [`handleTestInstance`](../../api/handler_instances.go#L471). 注册条件: `s.instanceMgr != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Check a saved instance. Inspect business status in the response. `application/json` [AutomationInstanceTestResponse](#automationinstancetestresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Instance lookup/health query failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/platforms/instances/{name}/start` — 启动实例

启动适配器及其连接/轮询，可能建立外部网络连接；无请求体。

实现: [`handleStartInstance`](../../api/handler_instances.go#L431). 注册条件: `s.instanceMgr != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Start an instance. Inspect business status in the response. `application/json` [AutomationNamedMessage](#automationnamedmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Instance startup failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/platforms/instances/{name}/stop` — 停止实例

停止正在运行的适配器及连接；无请求体。

实现: [`handleStopInstance`](../../api/handler_instances.go#L462). 注册条件: `s.instanceMgr != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Stop an instance. Inspect business status in the response. `application/json` [AutomationNamedMessage](#automationnamedmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Instance stop failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/im/channels/{provider}/test` — 验证平台配置

请求体直接为平台config，不包type/config。构造适配器并调用ValidateConfig，可能访问平台认证API；不保存、不启动、不发送消息。验证失败返回200 success=false。

实现: [`handleTestChannelConfig`](../../api/handler_instances.go#L573). 注册条件: `s.instanceMgr != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `provider` | yes | `string`; —  |

请求体（必需）: `application/json`: [AutomationProviderConfig](#automationproviderconfig).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Validate platform configuration. Inspect business status in the response. `application/json` [AutomationChannelTestResponse](#automationchanneltestresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON, missing provider/body. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "token": "example-token"
}
```

### `GET /api/v1/channels/wecom/guide` — 读取企业微信配置引导

返回steps、required_fields、callback_url。当前guide的callback_url仍输出旧/api/v1/webhook/wecom；实际实例回调使用/api/v1/platforms/hooks/wecom/{name}。实例config真实字段是token/aes_key，guide展示的callback_token/callback_aes_key不是实例字段名。

实现: [`handleWecomGuide`](../../api/handler_wechat.go#L11). 注册条件: `s.instanceMgr != nil`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Read the WeCom setup guide. Inspect business status in the response. `application/json` [AutomationWecomGuide](#automationwecomguide) |
| 401 | Bearer authentication failed; see API overview. |

### `GET /api/v1/platforms/hooks/{provider}/{name}` — 接收平台实例回调

公共回调不使用API Bearer。先匹配当前已启动实例的provider/name及已挂载入站handler；否则404文本。请求和响应是各平台原生挑战/事件协议，按实例配置验签、解密，可能进入真实消息处理与回复链。

实现: [`handlePlatformHook`](../../api/handler_instances.go#L625). 注册条件: `s.instanceMgr != nil`.

认证：平台/Webhook原生认证，不要求API Bearer。

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `provider` | yes | `string`; —  |
| path | `name` | yes | `string`; —  |
| query | `signature` | no | `string`; — WeChat SHA-1 callback signature. |
| query | `msg_signature` | no | `string`; — WeCom/encrypted-WeChat signature. |
| query | `timestamp` | no | `string`; — WeChat/WeCom timestamp. |
| query | `nonce` | no | `string`; — WeChat/WeCom nonce. |
| query | `echostr` | no | `string`; — WeChat/WeCom challenge. |
| query | `hub.mode` | no | `string`; — WhatsApp verification mode subscribe. |
| query | `hub.verify_token` | no | `string`; — WhatsApp configured verify_token. |
| query | `hub.challenge` | no | `string`; — WhatsApp challenge echoed on valid GET. |
| header | `X-Slack-Request-Timestamp` | no | `string`; — Slack request timestamp. |
| header | `X-Slack-Signature` | no | `string`; — Slack v0 HMAC-SHA256 signature. |
| header | `X-Line-Signature` | no | `string`; — LINE base64 HMAC-SHA256 signature. |
| header | `X-Hub-Signature-256` | no | `string`; — WhatsApp HMAC-SHA256 body signature. |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Receive a platform instance callback. Inspect business status in the response. `text/plain` `string`; `application/json` object {`challenge`: `string`}; `application/xml` `string` |
| 400 | Native payload parsing failed. `text/plain` `string` |
| 401 | Native signature verification failed (provider-specific). `text/plain` `string` |
| 403 | Invalid challenge/signature/replay-window (provider-specific). `text/plain` `string` |
| 404 | Started instance/inbound handler missing or provider mismatch. `text/plain` `string` |
| 405 | Method unsupported by the provider. `text/plain` `string` |
| 413 | Provider body limit exceeded. `text/plain` `string` |
| 500 | Provider decryption/processing failure. `text/plain` `string` |
| 503 | Provider worker unavailable. `text/plain` `string` |

### `POST /api/v1/platforms/hooks/{provider}/{name}` — 接收平台实例回调

公共回调不使用API Bearer。先匹配当前已启动实例的provider/name及已挂载入站handler；否则404文本。请求和响应是各平台原生挑战/事件协议，按实例配置验签、解密，可能进入真实消息处理与回复链。

实现: [`handlePlatformHook`](../../api/handler_instances.go#L625). 注册条件: `s.instanceMgr != nil`.

认证：平台/Webhook原生认证，不要求API Bearer。

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `provider` | yes | `string`; —  |
| path | `name` | yes | `string`; —  |
| query | `signature` | no | `string`; — WeChat SHA-1 callback signature. |
| query | `msg_signature` | no | `string`; — WeCom/encrypted-WeChat signature. |
| query | `timestamp` | no | `string`; — WeChat/WeCom timestamp. |
| query | `nonce` | no | `string`; — WeChat/WeCom nonce. |
| query | `echostr` | no | `string`; — WeChat/WeCom challenge. |
| query | `hub.mode` | no | `string`; — WhatsApp verification mode subscribe. |
| query | `hub.verify_token` | no | `string`; — WhatsApp configured verify_token. |
| query | `hub.challenge` | no | `string`; — WhatsApp challenge echoed on valid GET. |
| header | `X-Slack-Request-Timestamp` | no | `string`; — Slack request timestamp. |
| header | `X-Slack-Signature` | no | `string`; — Slack v0 HMAC-SHA256 signature. |
| header | `X-Line-Signature` | no | `string`; — LINE base64 HMAC-SHA256 signature. |
| header | `X-Hub-Signature-256` | no | `string`; — WhatsApp HMAC-SHA256 body signature. |

请求体（必需）: `application/json`: [AutomationPlatformCallback](#automationplatformcallback); `application/xml`: `string`; `text/xml`: `string`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Receive a platform instance callback. Inspect business status in the response. `text/plain` `string`; `application/json` object {`challenge`: `string`}; `application/xml` `string` |
| 400 | Native payload parsing failed. `text/plain` `string` |
| 401 | Native signature verification failed (provider-specific). `text/plain` `string` |
| 403 | Invalid challenge/signature/replay-window (provider-specific). `text/plain` `string` |
| 404 | Started instance/inbound handler missing or provider mismatch. `text/plain` `string` |
| 405 | Method unsupported by the provider. `text/plain` `string` |
| 413 | Provider body limit exceeded. `text/plain` `string` |
| 500 | Provider decryption/processing failure. `text/plain` `string` |
| 503 | Provider worker unavailable. `text/plain` `string` |

### `GET /api/v1/canvas/panels` — 列出Canvas面板

只返回id/title/component_count/version；无面板时panels可为null。

实现: [`handleListPanels`](../../api/handler_misc.go#L1936). 注册条件: `s.canvasSvc != nil`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | List Canvas panels. Inspect business status in the response. `application/json` [AutomationPanels](#automationpanels) |
| 401 | Bearer authentication failed; see API overview. |

### `GET /api/v1/canvas/panels/{id}` — 读取Canvas面板

返回完整组件树，props按组件类型解释。

实现: [`handleGetPanel`](../../api/handler_misc.go#L1956). 注册条件: `s.canvasSvc != nil`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Read a Canvas panel. Inspect business status in the response. `application/json` [AutomationPanel](#automationpanel) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Panel not found. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/canvas/events` — 提交Canvas组件事件

将panel_id/component_id/action/data交给面板注册的处理器。返回新Panel，或无结果时message。不存在处理器也返回200 message，不能据此证明交互已执行。

实现: [`handleCanvasEvent`](../../api/handler_misc.go#L1975). 注册条件: `s.canvasSvc != nil`.

请求体（必需）: `application/json`: [AutomationCanvasEventRequest](#automationcanvaseventrequest).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Submit a Canvas component event. Inspect business status in the response. `application/json` [AutomationPanel](#automationpanel) / [AutomationMessage](#automationmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON. `application/json` [AutomationError](#automationerror) |
| 500 | Registered event handler failed. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "panel_id": "panel-example",
  "component_id": "submit",
  "action": "submit",
  "data": {
    "answer": "Example"
  }
}
```

### `GET /api/v1/canvas/workflows` — 列出已保存工作流

返回完整定义；空列表可为null。定义写workflows.json，运行记录写workflow_runs.json并在超过1000条时淘汰旧记录；不是仅内存存储。

实现: [`handleListWorkflows`](../../api/handler_extended.go#L795). 注册条件: `Always registered.`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | List saved workflows. Inspect business status in the response. `application/json` [AutomationWorkflows](#automationworkflows) |
| 401 | Bearer authentication failed; see API overview. |

### `POST /api/v1/canvas/workflows` — 创建或替换工作流

name必填；缺省ID生成wf-，已有ID替换定义，created_at保留，updated_at刷新。保存只检查输入与长度；节点ID、引用、环等在执行时才校验。实际执行按依赖拓扑分阶段，同阶段顺序稳定；condition选择分支。文件落盘错误当前仅日志记录，因此200不单独证明持久化成功。

实现: [`handleSaveWorkflow`](../../api/handler_extended.go#L805). 注册条件: `Always registered.`.

请求体（必需）: `application/json`: [AutomationWorkflowData](#automationworkflowdata).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Create or replace a workflow. Inspect business status in the response. `application/json` [AutomationWorkflowData](#automationworkflowdata) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON, blank name or input length violation. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "name": "Pass-through example",
  "nodes": [
    {
      "id": "start",
      "type": "input"
    },
    {
      "id": "end",
      "type": "output"
    }
  ],
  "edges": [
    {
      "source": "start",
      "target": "end"
    }
  ]
}
```

### `DELETE /api/v1/canvas/workflows/{id}` — 删除工作流定义

删除定义并回收workflow:<id>任务授权；不等于清除已有运行记录。

实现: [`handleDeleteWorkflow`](../../api/handler_extended.go#L839). 注册条件: `Always registered.`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Delete a workflow definition. Inspect business status in the response. `application/json` [AutomationMessage](#automationmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Workflow not found. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/canvas/workflows/{id}/run` — 启动工作流执行

可省略请求体。立即返回200 running快照，后台最长10分钟；断开HTTP不取消后台运行。轮询GET runs/{id}确认completed/failed和实际输出；可能调用模型、MCP及其他工具并产生真实副作用。

实现: [`handleRunWorkflow`](../../api/handler_extended.go#L855). 注册条件: `Always registered.`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

请求体 (optional): `application/json`: [AutomationRunWorkflowRequest](#automationrunworkflowrequest).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Start a workflow run. Inspect business status in the response. `application/json` [AutomationWorkflowRun](#automationworkflowrun) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed nonempty body. `application/json` [AutomationError](#automationerror) |
| 404 | Workflow not found. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "input": "Example input",
  "metadata": {
    "locale": "en"
  }
}
```

### `GET /api/v1/canvas/runs/{id}` — 读取工作流运行状态

读取快照与node_results。provider_display_name/model_id为真实进入Agent调用边界后冻结的信息；未调用时可为null。

实现: [`handleGetWorkflowRun`](../../api/handler_extended.go#L1268). 注册条件: `Always registered.`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Read a workflow run. Inspect business status in the response. `application/json` [AutomationWorkflowRun](#automationworkflowrun) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Run not found. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/canvas/runs/{id}/resume` — 从已完成节点续接

无请求体。要求原运行和定义存在、至少一个completed节点；新建run并复用这些节点输出，其余节点重新执行，使用原input。当前handler不校验原run必须failed，也不恢复原请求的user/platform/chat/metadata；不能假定所有副作用都具备全局幂等。

实现: [`handleResumeWorkflowRun`](../../api/handler_extended.go#L970). 注册条件: `Always registered.`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Resume from completed nodes. Inspect business status in the response. `application/json` [AutomationWorkflowRun](#automationworkflowrun) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | No completed nodes to reuse. `application/json` [AutomationError](#automationerror) |
| 404 | Prior run or source workflow not found. `application/json` [AutomationError](#automationerror) |

### `GET /api/v1/team/agents` — 列出共享Agent记录

读取本机TeamStore中的共享配置记录，不是远端市场查询。

实现: [`handleListSharedAgents`](../../api/handler_team.go#L169). 注册条件: `Always registered.`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | List shared Agent records. Inspect business status in the response. `application/json` [AutomationSharedAgents](#automationsharedagents) |
| 401 | Bearer authentication failed; see API overview. |

### `POST /api/v1/team/agents` — 新增共享Agent记录

name必填，visibility默认private；接受其余SharedAgent字段，缺省ID/updated_at由服务生成。追加本机记录；不代表向远程发布。文件写失败只记录日志，200不是落盘证明。

实现: [`handleShareAgent`](../../api/handler_team.go#L192). 注册条件: `Always registered.`.

请求体（必需）: `application/json`: [AutomationSharedAgent](#automationsharedagent).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Add a shared Agent record. Inspect business status in the response. `application/json` [AutomationSharedAgent](#automationsharedagent) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed body (64 KiB limit), blank name or invalid visibility. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "name": "Research helper",
  "visibility": "private",
  "description": "Example shared configuration",
  "config": {}
}
```

### `DELETE /api/v1/team/agents/{id}` — 删除共享Agent记录

从本机TeamStore移除记录。

实现: [`handleDeleteSharedAgent`](../../api/handler_team.go#L213). 注册条件: `Always registered.`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Delete a shared Agent record. Inspect business status in the response. `application/json` [AutomationMessage](#automationmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Shared Agent not found. `application/json` [AutomationError](#automationerror) |

### `GET /api/v1/team/members` — 列出团队成员记录

读取本机成员记录；不调用外部身份服务。

实现: [`handleListTeamMembers`](../../api/handler_team.go#L222). 注册条件: `Always registered.`.

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | List team member records. Inspect business status in the response. `application/json` [AutomationTeamMembers](#automationteammembers) |
| 401 | Bearer authentication failed; see API overview. |

### `POST /api/v1/team/members` — 新增团队成员记录

email非空即可；未做邮箱格式验证。name缺省邮箱@前部分，role默认member；缺省ID/last_active自动生成。只追加本机成员记录，不发送邀请邮件、不建立远端账号。

实现: [`handleInviteTeamMember`](../../api/handler_team.go#L227). 注册条件: `Always registered.`.

请求体（必需）: `application/json`: [AutomationTeamMember](#automationteammember).

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Add a team member record. Inspect business status in the response. `application/json` [AutomationTeamMember](#automationteammember) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed body (64 KiB limit), blank email or invalid role. `application/json` [AutomationError](#automationerror) |

请求示例:

```json
{
  "email": "member@example.com",
  "role": "member"
}
```

### `DELETE /api/v1/team/members/{id}` — 删除团队成员记录

移除本机成员记录，不撤销外部账号。

实现: [`handleRemoveTeamMember`](../../api/handler_team.go#L252). 注册条件: `Always registered.`.

| 位置 | 参数 | 必填 | 类型 / 说明 |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | 响应 / 条件 |
| --- | --- |
| 200 | Delete a team member record. Inspect business status in the response. `application/json` [AutomationMessage](#automationmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Member not found. `application/json` [AutomationError](#automationerror) |

## 公共回调协议

| 类型 | 输入与验证 | 成功与副作用 |
| --- | --- | --- |
| generic | JSON对象或原始文本（无法解析为对象时保留payload.raw）；`X-Webhook-Signature`或`X-Signature`为原始body的HMAC-SHA256十六进制，允许`sha256=`前缀 | `200 {"status":"accepted"}`；异步调用处理器，可能触发绑定Cron或Agent；不是终态 |
| github | `X-Hub-Signature-256`同上；`X-GitHub-Event`为事件类型 | `ping`或显式test标记只验签/解析，不派发、不计入统计 |
| gitlab | `X-Gitlab-Token`匹配配置secret；payload.object_kind为事件类型 | 同普通Webhook接纳行为 |
| k12 | JSON `event_id`（或等值`delivery_id`）、`event_type`、`payload`；未知顶层字段拒绝；专用三header | `202 {"receipt":…}`；同event_id/同载荷返回原回执，新nonce仍必需；载荷冲突409 |
| wecom | GET查询`msg_signature/timestamp/nonce/echostr`；POST为加密XML，查询前三项；按token与aes_key验签解密 | GET解密挑战；POST确认接收后处理消息；实例路由必须已挂载 |
| wechat | GET查询`signature/timestamp/nonce/echostr`；POST原生XML或加密XML，按需要含`msg_signature` | 原生挑战、success或被动回复；按token及配置AES模式验证 |
| slack | 原生JSON `type/challenge/event`；`X-Slack-Request-Timestamp`及`X-Slack-Signature` | URL验证返回challenge；event_callback确认后异步处理 |
| line | 原生JSON `destination/events`；`X-Line-Signature`，由LINE SDK验签 | POST确认，逐事件进入消息处理；GET不支持 |
| whatsapp | GET `hub.mode=subscribe/hub.verify_token/hub.challenge`；POST `entry[].changes[].value`、`X-Hub-Signature-256` | 验证时回显challenge；消息入队处理，worker不可用503 |

K12签名为 `sha256=` 加 `HMAC-SHA256(secret, timestamp + nonce + raw_body)` 的十六进制，无分隔符；时间窗±5分钟，nonce非空且最长128字节。timestamp接受RFC3339或Unix秒，必须使用实际发送时刻。接收器按现有绑定、事件和owner限流，可返回429与Retry-After。不能复用相同nonce重放；`event_id`才是业务幂等身份。

K12 payload禁止自报可信owner/Agent/learner字段，禁止远程URL；媒体先上传，再传同owner的asset_ref。三种事件的具体payload见对应schema。回执`accepted/processing`不是完成，`outcome_unknown`不得盲重发。Receipt查询和显式安全重试复用本页GET collection / PATCH item路由。

## Cron与工作流调用

完整Cron action、draft、JobSpec和历史字段见[既有Cron参考](../api.md#cronjob)。统一入口与SSE创建并行存在；统一入口不会依据Accept自动变成流。

```text
event: progress
data: {"stage":"analyzing","message":"Analyzing task"}

event: error
data: {"error":"Example compilation failure","stage":"validating"}
```

SSE progress可能重复，done和error为互斥终态。done数据为`{job,spec_preview}`，各字段复用Cron参考。工作流先保存定义，再run并轮询运行记录；结果须检查节点与实际输出。图节点类型和条件分支字段见WorkflowNodeData。Resume只缓存已完成节点输出，不保证任意工具未知结果都可安全重试。

## 字段模型

下表与OpenAPI同源。开放对象仅用于实际开放的工具参数、配置扩展、事件data等；固定请求与响应均列出实际字段。响应中的required表示该schema必须出现的字段，不把成功HTTP码等同业务成功。

### AutomationConnectionSummary

来源: [api/handler_connections.go](../../api/handler_connections.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `provider` | `string` | yes | — |
| `name` | `string` | yes | — |
| `capabilities` | array&lt;`string`&gt; | yes | — |
| `status` | `string` | yes | — |
| `enabled` | `boolean` | yes | — |

### AutomationConnectionTestRequest

来源: [api/handler_connections.go](../../api/handler_connections.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `type` | `string` | yes | enum: `feishu`, `dingtalk`, `discord`, `telegram`, `wecom`, `wechat`, `slack`, `line`, `whatsapp`, `matrix`, `email`; Trimmed and converted to lowercase. |
| `config` | [AutomationFeishuConfig](#automationfeishuconfig) / [AutomationDingtalkConfig](#automationdingtalkconfig) / [AutomationWecomConfig](#automationwecomconfig) / [AutomationWechatConfig](#automationwechatconfig) / [AutomationSlackConfig](#automationslackconfig) / [AutomationTelegramConfig](#automationtelegramconfig) / [AutomationDiscordConfig](#automationdiscordconfig) / [AutomationLINEConfig](#automationlineconfig) / [AutomationWhatsAppConfig](#automationwhatsappconfig) / [AutomationMatrixConfig](#automationmatrixconfig) / [AutomationEmailTestConfig](#automationemailtestconfig) | yes | — |

### AutomationConnectionTestResponse

来源: [api/handler_connections.go](../../api/handler_connections.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `ok` | `boolean` | yes | — |
| `detail` | `string` | yes | — |

### AutomationConnectorCreateRequest

来源: [api/handler_connectors.go](../../api/handler_connectors.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `provider` | `string` | yes | enum: `github`, `notion` |
| `name` | `string` | no | maxLength: 64; Optional; trimmed; defaults to provider. |
| `token` | `string` | yes | Required nonblank token; at most 65536 UTF-8 bytes. Used for a real provider credential probe. |

### AutomationConnectorTestRequest

来源: [api/handler_connectors.go](../../api/handler_connectors.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `provider` | `string` | yes | enum: `github`, `notion` |
| `token` | `string` | yes | Required nonblank token; at most 65536 UTF-8 bytes. Used for a real provider credential probe. |

### AutomationCronParseRequest

来源: [api/handler_cron_parse.go](../../api/handler_cron_parse.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `text` | `string` | yes | minLength: 1; Nonblank natural-language schedule and task. |
| `hints` | object {`locale`: `string`} | no | — |

### AutomationCronParseResponse

来源: [api/handler_cron_parse.go](../../api/handler_cron_parse.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `draft` | `CronJobDraft` | no | — |
| `needs_clarification` | `boolean` | no | — |
| `suggestion` | `string` | no | — |
| `tier` | `integer` | yes | enum: `2` |

### AutomationAddCronJobRequest

来源: [api/handler_cron.go](../../api/handler_cron.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `name` | `string` | yes | maxLength: 64 |
| `schedule` | `string` | yes | maxLength: 256 |
| `prompt` | `string` | yes | — |
| `user_id` | `string` | no | default: `"api-user"` |
| `type` | `string` | no | Only once is explicitly rejected; omitted or cron follows recurring creation. This route does not accept a draft/script. |
| `platform` | `string` | no | — |
| `chat_id` | `string` | no | — |
| `deliver` | array&lt;`string`&gt; | no | Nonempty overrides delivery targets; empty lets compilation infer targets, default chat. |
| `continuous` | `boolean` | no | default: `false`; Cross-tick progress; forces Agent mode. |
| `paused` | `boolean` | no | default: `false`; Create in paused state when true. |

### AutomationTaskStatus

来源: [api/handler_autonomy.go](../../api/handler_autonomy.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `task_ref` | `string` | yes | — |
| `kind` | `string` | yes | — |
| `name` | `string` | yes | — |
| `enabled` | `boolean` | yes | — |
| `status` | `string` | no | — |
| `needs_decision` | array&lt;`string`&gt; | no | — |
| `all_clear` | `boolean` | yes | — |
| `last_block` | [AutomationDecision](#automationdecision) / `null` | no | — |
| `preflight` | [AutomationPreflightResult](#automationpreflightresult) / `null` | no | — |

### AutomationCreateGrantRequest

来源: [api/handler_autonomy.go](../../api/handler_autonomy.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `task_ref` | `string` | yes | minLength: 1; Trimmed nonblank task reference, convention cron:<id>, webhook:<id>, workflow:<id>. |
| `source` | `string` | no | Trimmed lowercase; empty means any source. |
| `entries` | array&lt;`string`&gt; | yes | minItems: 1; Category/tool/glob entries; trimmed and deduplicated. Blank entries and bare * are discarded; at least one entry must remain. |
| `security_scope_digest` | `string` | no | Optional exact parameter-scope digest. Owner comes from trusted request context, never the body. |
| `note` | `string` | no | — |

### AutomationPreflightRequest

来源: [autonomy/preflight.go](../../autonomy/preflight.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `source` | `string` | yes | — |
| `task_ref` | `string` | no | — |
| `prompt` | `string` | no | — |
| `deliver` | `boolean` | no | — |
| `tools` | array&lt;`string`&gt; | no | — |
| `workflow_id` | `string` | no | Adds tools from the saved workflow when available; fills task_ref if absent. |
| `cron_job_id` | `string` | no | Loads source prompt and delivery only if prompt is blank and scheduler/job exists. |

### AutomationPreflightResult

来源: [autonomy/preflight.go](../../autonomy/preflight.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `source` | `string` | yes | — |
| `profile` | `string` | yes | — |
| `capabilities` | array&lt;[AutomationCapabilityVerdict](#automationcapabilityverdict)&gt; | yes | — |
| `extra_tools` | array&lt;[AutomationToolVerdict](#automationtoolverdict)&gt; | no | — |
| `estimated` | array&lt;`string`&gt; | yes | — |
| `needs_decision` | array&lt;`string`&gt; | yes | — |
| `all_clear` | `boolean` | yes | No approval identified in the estimated scope; not a runtime guarantee. |
| `basis` | `string` | yes | enum: `static`, `heuristic` |

### AutomationCapabilityVerdict

来源: [autonomy/preflight.go](../../autonomy/preflight.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `category` | `string` | yes | — |
| `tools` | array&lt;`string`&gt; | yes | — |
| `state` | `string` | yes | enum: `auto`, `granted`, `approval` |

### AutomationToolVerdict

来源: [autonomy/preflight.go](../../autonomy/preflight.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `tool` | `string` | yes | — |
| `state` | `string` | yes | enum: `auto`, `granted`, `approval` |

### AutomationMatrixCell

来源: [autonomy/preflight.go](../../autonomy/preflight.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `category` | `string` | yes | — |
| `state` | `string` | yes | enum: `auto`, `approval` |

### AutomationMatrixRow

来源: [autonomy/preflight.go](../../autonomy/preflight.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `source` | `string` | yes | — |
| `cells` | array&lt;[AutomationMatrixCell](#automationmatrixcell)&gt; | yes | — |

### AutomationMatrixView

来源: [autonomy/preflight.go](../../autonomy/preflight.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `profile` | `string` | yes | — |
| `categories` | array&lt;`string`&gt; | yes | — |
| `rows` | array&lt;[AutomationMatrixRow](#automationmatrixrow)&gt; | yes | — |

### AutomationGrant

来源: [autonomy/grants.go](../../autonomy/grants.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `task_ref` | `string` | yes | — |
| `source` | `string` | yes | — |
| `entries` | array&lt;`string`&gt; | yes | — |
| `owner_id` | `string` | no | — |
| `security_scope_digest` | `string` | no | — |
| `note` | `string` | no | — |
| `created_at` | `string` | yes | format: date-time |
| `revoked_at` | `string` / `null` | no | — |

### AutomationDecision

来源: [autonomy/decisions.go](../../autonomy/decisions.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `at` | `string` | yes | format: date-time |
| `source` | `string` | yes | — |
| `task_ref` | `string` | no | — |
| `tool` | `string` | yes | — |
| `capability` | `string` | no | — |
| `profile` | `string` | yes | — |
| `decision` | `string` | yes | enum: `allow`, `pending`, `deny` |
| `via` | `string` | yes | Decision origin, e.g. matrix, task_grant, solve_grant, policy. |
| `reason` | `string` | no | — |

### AutomationUpsertInstanceRequest

来源: [api/handler_instances.go](../../api/handler_instances.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | no | Accepted but ignored by name-based upsert; by-id update uses path id. |
| `provider` | `string` | no | enum: ``, `feishu`, `dingtalk`, `discord`, `telegram`, `wecom`, `wechat`, `slack`, `line`, `whatsapp`, `matrix`, `email`; Required nonblank for name-based upsert; blank retains the old value for PUT by id. |
| `name` | `string` | no | maxLength: 64 |
| `enabled` | `boolean` | no | default: `false`; Omission means false, including PUT by id; it is not a partial boolean update. |
| `config` | [AutomationProviderConfig](#automationproviderconfig) | no | — |

### AutomationSendTestRequest

来源: [api/handler_instances.go](../../api/handler_instances.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `request_id` | `string` | no | Nonblank chooses bound-target mode; same instance/id replays the frozen result without resend. target is ignored in this mode. |
| `target` | `string` | no | Required with content only when request_id is blank; explicit external recipient/chat. |
| `content` | `string` | no | Required nonblank in direct mode; bound mode supplies a default test message when empty. |

### AutomationInstanceResponse

来源: [api/handler_instances.go](../../api/handler_instances.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `provider` | `string` | yes | enum: `feishu`, `dingtalk`, `discord`, `telegram`, `wecom`, `wechat`, `slack`, `line`, `whatsapp`, `matrix`, `email` |
| `name` | `string` | yes | — |
| `enabled` | `boolean` | yes | — |
| `status` | `string` | yes | enum: `stopped`, `running`, `error` |
| `last_error` | `string` | no | — |
| `config` | [AutomationProviderConfig](#automationproviderconfig) | no | — |
| `updated_at` | `string` | no | HTTP-date (Go http.TimeFormat), unlike RFC3339 timestamps in list responses. |
| `message` | `string` | no | — |

### AutomationInstance

来源: [instances/manager.go](../../instances/manager.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `provider` | `string` | yes | enum: `feishu`, `dingtalk`, `discord`, `telegram`, `wecom`, `wechat`, `slack`, `line`, `whatsapp`, `matrix`, `email` |
| `name` | `string` | yes | — |
| `enabled` | `boolean` | yes | — |
| `mode` | `string` | yes | enum: `runtime`, `webhook` |
| `status` | `string` | yes | enum: `stopped`, `running`, `error` |
| `config` | [AutomationProviderConfig](#automationproviderconfig) | yes | — |
| `last_event_at` | `string` | no | format: date-time |
| `last_error` | `string` | no | — |
| `created_at` | `string` | yes | format: date-time |
| `updated_at` | `string` | yes | format: date-time |

### AutomationHealthReport

来源: [instances/manager.go](../../instances/manager.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `name` | `string` | yes | — |
| `provider` | `string` | yes | — |
| `mode` | `string` | yes | enum: `runtime`, `webhook` |
| `status` | `string` | yes | enum: `stopped`, `running`, `error` |
| `healthy` | `boolean` | yes | — |
| `connection_state` | `string` | no | — |
| `last_event_at` | `string` | no | format: date-time |
| `last_error` | `string` | no | — |
| `checked_at` | `string` | yes | format: date-time |

### AutomationTestDelivery

来源: [instances/test_delivery.go](../../instances/test_delivery.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `chat_id` | `string` | yes | — |
| `status` | `string` | yes | enum: `not_sent`, `sending`, `delivered`, `accepted`, `failed`, `outcome_unknown` |
| `external_message_id` | `string` | no | — |

### AutomationTestDeliveryResult

来源: [instances/test_delivery.go](../../instances/test_delivery.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `request_id` | `string` | yes | — |
| `success` | `boolean` | yes | — |
| `message` | `string` | yes | — |
| `pending` | `boolean` | yes | — |
| `deliveries` | array&lt;[AutomationTestDelivery](#automationtestdelivery)&gt; | yes | — |

### AutomationConnectorSummary

来源: [connector/connector.go](../../connector/connector.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `provider` | `string` | yes | enum: `github`, `notion` |
| `name` | `string` | yes | — |
| `created_at` | `string` | yes | format: date-time |

### AutomationConnectorResource

来源: [connector/connector.go](../../connector/connector.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `title` | `string` | yes | — |
| `url` | `string` | no | — |
| `kind` | `string` | no | — |
| `desc` | `string` | no | — |

### AutomationWebhook

来源: [webhook/webhook.go](../../webhook/webhook.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `name` | `string` | yes | — |
| `type` | `string` | yes | — |
| `has_secret` | `boolean` | yes | — |
| `prompt` | `string` | yes | — |
| `job_id` | `string` | no | — |
| `user_id` | `string` | yes | — |
| `enabled` | `boolean` | yes | — |
| `last_event_at` | `string` | yes | format: date-time |
| `event_count` | `integer` | yes | — |
| `created_at` | `string` | yes | format: date-time |

### AutomationK12Binding

来源: [webhook/k12.go](../../webhook/k12.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `binding_id` | `string` | yes | — |
| `name` | `string` | yes | — |
| `agent_id` | `string` | yes | — |
| `learner_id` | `string` | yes | — |
| `scope` | `string` | yes | enum: `direct` |
| `allowed_events` | array&lt;`string`&gt; | yes | — |
| `allowed_workflows` | array&lt;`string`&gt; | no | — |
| `has_secret` | `boolean` | yes | — |
| `secret_version` | `integer` | yes | — |
| `status` | `string` | yes | enum: `enabled`, `disabled` |
| `created_by` | `string` | yes | — |
| `rotated_at` | `string` | no | format: date-time |
| `created_at` | `string` | yes | format: date-time |
| `updated_at` | `string` | yes | format: date-time |

### AutomationK12Receipt

来源: [webhook/k12.go](../../webhook/k12.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `receipt_id` | `string` | yes | — |
| `binding_id` | `string` | yes | — |
| `event_id` | `string` | no | — |
| `event_type` | `string` | no | enum: `k12.submission.requested.v1`, `k12.practice_return.requested.v1`, `k12.workflow_run.requested.v1` |
| `payload_digest` | `string` | yes | — |
| `status` | `string` | yes | enum: `accepted`, `processing`, `succeeded`, `failed`, `outcome_unknown`, `rejected` |
| `job_or_execution_ref` | `string` | no | — |
| `failure_kind` | `string` | no | — |
| `retryable` | `boolean` | yes | — |
| `attempt_count` | `integer` | yes | — |
| `created_at` | `string` | yes | format: date-time |
| `updated_at` | `string` | yes | format: date-time |

### AutomationK12Envelope

来源: [webhook/k12.go](../../webhook/k12.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `event_id` | `string` | no | maxLength: 200; Stable nonblank event id, or delivery_id as alias; both must match if provided. |
| `delivery_id` | `string` | no | Alias for event_id; same value required when both are present. |
| `event_type` | `string` | yes | enum: `k12.submission.requested.v1`, `k12.practice_return.requested.v1`, `k12.workflow_run.requested.v1` |
| `payload` | [AutomationK12SubmissionPayload](#automationk12submissionpayload) / [AutomationK12PracticePayload](#automationk12practicepayload) / [AutomationK12WorkflowPayload](#automationk12workflowpayload) | yes | Shape selected by event_type. Remote URLs and recursively supplied agent_id/learner_id/owner_id/user_id/job_id/execution_id/conversation_scope/scope are rejected. |

### AutomationRegisterWebhookRequest

来源: [api/handler_webhook.go](../../api/handler_webhook.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `name` | `string` | yes | minLength: 1; maxLength: 64; Required. K12: ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$. |
| `type` | `string` | no | default: `"generic"`; Known values generic/github/gitlab/k12; unknown non-k12 strings use generic signature/parser behavior. |
| `secret` | `string` | no | Generic: optional, generated when blank and returned once only when generated. K12: caller-supplied nonblank secret is rejected. |
| `prompt` | `string` | no | Required for non-k12 requests when job_id is empty. |
| `job_id` | `string` | no | When nonblank, owned Cron job is triggered instead of prompt; missing scheduler 503, unowned/missing job 404. |
| `user_id` | `string` | no | Authenticated owner takes priority; direct unauthenticated handler compatibility may use this field. |
| `enabled` | `boolean` | no | default: `false` |
| `agent_id` | `string` | no | Required for k12 binding. |
| `learner_id` | `string` | no | Required for k12 binding. |
| `allowed_events` | array&lt;`string`&gt; | no | Required nonempty for k12 binding. |
| `allowed_workflows` | array&lt;`string`&gt; | no | For workflow events, allowlisted workflow_id@workflow_version strings. |

### AutomationUpdateWebhookRequest

来源: [api/handler_webhook.go](../../api/handler_webhook.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `enabled` | `boolean` / `null` | no | — |
| `rotate_secret` | `boolean` | no | — |
| `allowed_events` | array&lt;`string`&gt; | no | — |
| `allowed_workflows` | array&lt;`string`&gt; | no | Applied together with allowed_events; alone is not an accepted update. |
| `retry_receipt_id` | `string` | no | K12 only; exclusive with enabled, allowed_events, rotate_secret and nonempty allowed_workflows. Only failed receipts with retryable=true can retry. |

### AutomationComponent

来源: [canvas/canvas.go](../../canvas/canvas.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `type` | `string` | yes | enum: `markdown`, `chart`, `form`, `table`, `kanban`, `buttons`, `progress`, `image` |
| `props` | `string` / [AutomationChartProps](#automationchartprops) / [AutomationTableProps](#automationtableprops) / [AutomationButtonsProps](#automationbuttonsprops) / [AutomationFormProps](#automationformprops) / [AutomationProgressProps](#automationprogressprops) / [AutomationImageProps](#automationimageprops) / object (map) | yes | Type-specific props; markdown can be text; custom/kanban props remain producer-defined. |
| `events` | array&lt;`string`&gt; | no | — |
| `children` | array&lt;[AutomationComponent](#automationcomponent) / `null`&gt; | no | — |

### AutomationPanel

来源: [canvas/canvas.go](../../canvas/canvas.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `title` | `string` | yes | — |
| `components` | array&lt;[AutomationComponent](#automationcomponent) / `null`&gt; / `null` | yes | 刚创建的空面板可返回null。 |
| `version` | `integer` | yes | — |

### AutomationChartProps

来源: [canvas/canvas.go](../../canvas/canvas.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `chart_type` | `string` | yes | — |
| `title` | `string` | yes | — |
| `labels` | array&lt;`string`&gt; | yes | — |
| `datasets` | array&lt;[AutomationDataset](#automationdataset)&gt; | yes | — |

### AutomationDataset

来源: [canvas/canvas.go](../../canvas/canvas.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `label` | `string` | yes | — |
| `data` | array&lt;`number`&gt; | yes | — |
| `color` | `string` | no | — |

### AutomationTableProps

来源: [canvas/canvas.go](../../canvas/canvas.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `headers` | array&lt;`string`&gt; | yes | — |
| `rows` | array&lt;array&lt;`string`&gt;&gt; | yes | — |

### AutomationButtonsProps

来源: [canvas/canvas.go](../../canvas/canvas.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `buttons` | array&lt;[AutomationButtonDef](#automationbuttondef)&gt; | yes | — |

### AutomationButtonDef

来源: [canvas/canvas.go](../../canvas/canvas.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `label` | `string` | yes | — |
| `action` | `string` | yes | — |
| `style` | `string` | no | — |

### AutomationFormField

来源: [canvas/canvas.go](../../canvas/canvas.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `name` | `string` | yes | — |
| `label` | `string` | yes | — |
| `type` | `string` | yes | — |
| `placeholder` | `string` | no | — |
| `required` | `boolean` | no | — |
| `options` | array&lt;`string`&gt; | no | — |
| `default` | `string` | no | — |

### AutomationFormProps

来源: [canvas/canvas.go](../../canvas/canvas.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `fields` | array&lt;[AutomationFormField](#automationformfield)&gt; | yes | — |
| `submit_text` | `string` | yes | — |

### AutomationProgressProps

来源: [canvas/canvas.go](../../canvas/canvas.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `value` | `number` | yes | — |
| `label` | `string` | yes | — |
| `color` | `string` | yes | — |

### AutomationImageProps

来源: [canvas/canvas.go](../../canvas/canvas.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `url` | `string` | yes | — |
| `alt` | `string` | no | — |
| `width` | `integer` | no | — |
| `height` | `integer` | no | — |

### AutomationCanvasEventRequest

来源: [api/handler_misc.go](../../api/handler_misc.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `panel_id` | `string` | no | — |
| `component_id` | `string` | no | — |
| `action` | `string` | no | — |
| `data` | object (map) | no | — |

### AutomationWorkflowData

来源: [api/handler_extended.go](../../api/handler_extended.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | no | Omit to create; existing id replaces the saved definition. Server sets created_at/updated_at. |
| `name` | `string` | yes | maxLength: 64 |
| `description` | `string` | no | maxLength: 2000 |
| `nodes` | array&lt;[AutomationWorkflowNode](#automationworkflownode)&gt; / `null` | no | 省略/null可保存为空定义，响应可为null。 |
| `edges` | array&lt;[AutomationWorkflowEdge](#automationworkflowedge)&gt; / `null` | no | 省略/null时响应可为null。 |
| `data` | object (map) | no | — |
| `created_at` | `string` | no | format: date-time |
| `updated_at` | `string` | no | format: date-time |

### AutomationWorkflowRun

来源: [api/handler_extended.go](../../api/handler_extended.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `workflow_id` | `string` | yes | — |
| `status` | `string` | yes | enum: `running`, `completed`, `failed` |
| `provider_display_name` | `string` / `null` | yes | Nullable actual model-call route fact, not prefilled from settings. |
| `model_id` | `string` / `null` | yes | Nullable actual model-call route fact; resume preserves prior route facts. |
| `input` | `string` | no | — |
| `output` | `string` | no | — |
| `message_content` | [AutomationMessageContent](#automationmessagecontent) / `null` | no | — |
| `render_manifest` | [AutomationRenderManifest](#automationrendermanifest) / `null` | no | — |
| `error` | `string` | no | — |
| `node_results` | array&lt;[AutomationWorkflowNodeRun](#automationworkflownoderun)&gt; | no | — |
| `trigger_key` | `string` | no | — |
| `prior_run_id` | `string` | no | — |
| `retry_safe` | `boolean` | no | — |
| `started_at` | `string` | yes | format: date-time |
| `finished_at` | `string` | no | format: date-time |

### AutomationRunWorkflowRequest

来源: [api/workflow_runtime.go](../../api/workflow_runtime.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `input` | `string` | no | — |
| `user_id` | `string` | no | Agent nodes default to workflow-<workflow_id> when empty. |
| `platform` | `string` | no | — |
| `instance_id` | `string` | no | — |
| `chat_id` | `string` | no | — |
| `metadata` | object (map) | no | String map. Reserved dispatch keys are removed; source and workflow ids are server-stamped. locale defaults to und. |

### AutomationWorkflowNodeRun

来源: [api/workflow_runtime.go](../../api/workflow_runtime.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `node_id` | `string` | yes | — |
| `type` | `string` | yes | — |
| `label` | `string` | no | — |
| `status` | `string` | yes | enum: `pending`, `running`, `completed`, `skipped`, `failed` |
| `output` | `string` | no | — |
| `error` | `string` | no | — |
| `agent_role` | `string` | no | — |
| `handoff_agent` | `string` | no | — |
| `started_at` | `string` | no | format: date-time |
| `finished_at` | `string` | no | format: date-time |

### AutomationSharedAgent

来源: [api/handler_team.go](../../api/handler_team.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | no | Generated sa-... when omitted. |
| `name` | `string` | yes | — |
| `author` | `string` | no | — |
| `description` | `string` | no | — |
| `downloads` | `integer` | no | — |
| `visibility` | `string` | no | enum: `public`, `team`, `private`; default: `"private"` |
| `updated_at` | `string` | no | Generated RFC3339 UTC timestamp when omitted. |
| `config` | object (map) | no | — |

### AutomationTeamMember

来源: [api/handler_team.go](../../api/handler_team.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | no | Generated tm-... when omitted. |
| `name` | `string` | no | Defaults to email prefix before @. |
| `email` | `string` | yes | — |
| `role` | `string` | no | enum: `admin`, `member`, `viewer`; default: `"member"` |
| `avatar` | `string` | no | — |
| `last_active` | `string` | no | Generated RFC3339 UTC timestamp when omitted. |

### AutomationAttachmentRef

来源: [messagecontent/messagecontent.go](../../messagecontent/messagecontent.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `asset_id` | `string` | yes | — |
| `name` | `string` | no | — |
| `mime` | `string` | yes | — |
| `digest` | `string` | yes | — |
| `alt_text` | `string` | no | — |

### AutomationMessageContent

来源: [messagecontent/messagecontent.go](../../messagecontent/messagecontent.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `content_id` | `string` | yes | — |
| `content_version` | `string` | yes | — |
| `producer_kind` | `string` | yes | — |
| `markdown` | `string` | yes | — |
| `source_digest` | `string` | yes | — |
| `locale` | `string` | yes | — |
| `attachments` | array&lt;[AutomationAttachmentRef](#automationattachmentref)&gt; | no | — |

### AutomationCapabilitySnapshot

来源: [messagecontent/messagecontent.go](../../messagecontent/messagecontent.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `markdown` | `boolean` | yes | — |
| `tex_math` | `boolean` | yes | — |
| `mathml` | `boolean` | no | — |
| `unicode_math` | `boolean` | no | — |
| `attachments` | `boolean` | no | — |
| `max_runes` | `integer` | no | — |

### AutomationRenderPart

来源: [messagecontent/messagecontent.go](../../messagecontent/messagecontent.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `kind` | `string` | yes | — |
| `text` | `string` | no | — |
| `artifact_ref` | `string` | no | — |
| `artifact_digest` | `string` | no | — |
| `alt_text` | `string` | no | — |

### AutomationRenderManifest

来源: [messagecontent/messagecontent.go](../../messagecontent/messagecontent.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `render_id` | `string` | yes | — |
| `content_id` | `string` | yes | — |
| `surface` | `string` | yes | — |
| `capability_snapshot` | [AutomationCapabilitySnapshot](#automationcapabilitysnapshot) | yes | — |
| `renderer_version` | `string` | yes | — |
| `source_digest` | `string` | yes | — |
| `parts` | array&lt;[AutomationRenderPart](#automationrenderpart)&gt; | yes | — |
| `fallback_reason` | `string` | no | — |
| `receipt_ref` | `string` | no | — |

### AutomationTelegramConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: token. name is overridden by the instance name; outer enabled controls lifecycle.

来源: [config/config.go](../../config/config.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |

### AutomationDiscordConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: token. name is overridden by the instance name; outer enabled controls lifecycle.

来源: [config/config.go](../../config/config.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |

### AutomationSlackConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: token. name is overridden by the instance name; outer enabled controls lifecycle.

来源: [config/config.go](../../config/config.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `webhook_port` | `integer` | no | — |
| `token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `signing_secret` | `string` | no |  Credential; list/update responses mask stored values. |

### AutomationFeishuConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: app_id, app_secret. name is overridden by the instance name; outer enabled controls lifecycle.

来源: [config/config.go](../../config/config.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `webhook_port` | `integer` | no | — |
| `app_id` | `string` | no | Required for adapter credential validation/start. |
| `app_secret` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `verification_token` | `string` | no |  Credential; list/update responses mask stored values. |

### AutomationDingtalkConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: app_key, app_secret, robot_code. name is overridden by the instance name; outer enabled controls lifecycle.

来源: [config/config.go](../../config/config.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `webhook_port` | `integer` | no | — |
| `app_key` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `app_secret` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `robot_code` | `string` | no | Required for adapter credential validation/start. |

### AutomationWechatConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: app_id, app_secret, token. name is overridden by the instance name; outer enabled controls lifecycle.

来源: [config/config.go](../../config/config.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `app_id` | `string` | no | Required for adapter credential validation/start. |
| `app_secret` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `aes_key` | `string` | no |  Credential; list/update responses mask stored values. |

### AutomationWecomConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: corp_id, agent_id, secret, token, aes_key. name is overridden by the instance name; outer enabled controls lifecycle.

来源: [config/config.go](../../config/config.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `webhook_port` | `integer` | no | — |
| `corp_id` | `string` | no | Required for adapter credential validation/start. |
| `agent_id` | `string` | no | Required for adapter credential validation/start. |
| `secret` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `aes_key` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |

### AutomationWhatsAppConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: token, phone_id, verify_token, app_secret. name is overridden by the instance name; outer enabled controls lifecycle.

来源: [config/config.go](../../config/config.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `phone_id` | `string` | no | Required for adapter credential validation/start. |
| `verify_token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `app_secret` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |

### AutomationLINEConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: channel_secret, channel_token. name is overridden by the instance name; outer enabled controls lifecycle.

来源: [config/config.go](../../config/config.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `channel_secret` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `channel_token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |

### AutomationMatrixConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: homeserver_url, access_token, user_id. name is overridden by the instance name; outer enabled controls lifecycle.

来源: [config/config.go](../../config/config.go).

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `homeserver_url` | `string` | no | Required for adapter credential validation/start. |
| `access_token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `user_id` | `string` | no | Required for adapter credential validation/start. |

### AutomationSSEProgress

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `stage` | `string` | yes | enum: `analyzing`, `calling_llm`, `validating`, `persisting` |
| `message` | `string` | yes | — |

### AutomationSSEDone

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `job` | `CronJob` | yes | — |
| `spec_preview` | `JobSpec` | yes | — |

### AutomationSSEError

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `error` | `string` | yes | — |
| `stage` | `string` | yes | Failed compile stage, or empty when unknown. |

### AutomationProfile

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `profile` | `string` | yes | enum: `function_first`, `balanced`, `strict`, `full_access` |
| `profiles` | array&lt;`string`&gt; | no | — |
| `matrix` | [AutomationMatrixView](#automationmatrixview) | yes | — |

### AutomationProfileUpdate

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `profile` | `string` | yes | enum: `function_first`, `balanced`, `strict`, `full_access` |

### AutomationCapabilityStatus

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `enabled` | `boolean` | yes | — |
| `state` | `string` | yes | enum: `disabled`, `unavailable`, `ready` |

### AutomationStatus

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `cron` | [AutomationCapabilityStatus](#automationcapabilitystatus) | yes | — |
| `webhook` | [AutomationCapabilityStatus](#automationcapabilitystatus) | yes | — |

### AutomationSummaryCounts

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `tasks` | `integer` | yes | — |
| `ready` | `integer` | yes | — |
| `pending` | `integer` | yes | — |
| `grants` | `integer` | yes | — |

### AutomationSummary

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `profile` | `string` | yes | enum: `function_first`, `balanced`, `strict`, `full_access` |
| `counts` | [AutomationSummaryCounts](#automationsummarycounts) | yes | — |
| `pending` | array&lt;[AutomationTaskStatus](#automationtaskstatus)&gt; | yes | — |
| `tasks` | array&lt;[AutomationTaskStatus](#automationtaskstatus)&gt; | yes | — |

### AutomationSMTPConfig

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `host` | `string` | no | — |
| `port` | `integer` | no | default: `587` |
| `username` | `string` | no | — |
| `password` | `string` | no | — |
| `from` | `string` | no | — |

### AutomationIMAPConfig

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `host` | `string` | no | — |
| `port` | `integer` | no | default: `993` |
| `username` | `string` | no | — |
| `password` | `string` | no | — |
| `tls` | `boolean` | no | Connection test defaults true; nested saved EmailConfig retains the supplied boolean. |
| `folder` | `string` | no | default: `"INBOX"` |

### AutomationEmailConfig

Use nested smtp/imap, or flat email/password/smtp_host/smtp_port/imap_host/imap_port form. Flat fallback applies only when both nested hosts are empty; ports in flat form are strings. Flat form sets IMAP TLS true.

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `smtp` | [AutomationSMTPConfig](#automationsmtpconfig) | no | — |
| `imap` | [AutomationIMAPConfig](#automationimapconfig) | no | — |
| `PollInterval` | `integer` | no | default: `60`; Nested Go EmailConfig JSON field name; nonpositive means 60 seconds. |
| `MaxFetch` | `integer` | no | default: `10` |
| `email` | `string` | no | — |
| `password` | `string` | no | — |
| `from` | `string` | no | — |
| `smtp_host` | `string` | no | — |
| `smtp_port` | `string` | no | — |
| `imap_host` | `string` | no | — |
| `imap_port` | `string` | no | — |
| `poll_interval` | `integer` | no | default: `60` |
| `max_fetch` | `integer` | no | default: `10` |

### AutomationEmailTestConfig

connections/test accepts this nested email form. SMTP host may be inferred as smtp.<email-domain>; IMAP host may be inferred from imap.username. Missing IMAP host skips receiving validation. SMTP probe does not send mail.

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `smtp` | [AutomationSMTPConfig](#automationsmtpconfig) | yes | — |
| `imap` | [AutomationIMAPConfig](#automationimapconfig) | no | — |

### AutomationProviderConfig

Choose fields by outer provider. Config may be empty for a disabled stored instance. Credential-validation requirements are stated in the matching provider schema.

[AutomationFeishuConfig](#automationfeishuconfig) / [AutomationDingtalkConfig](#automationdingtalkconfig) / [AutomationWecomConfig](#automationwecomconfig) / [AutomationWechatConfig](#automationwechatconfig) / [AutomationSlackConfig](#automationslackconfig) / [AutomationTelegramConfig](#automationtelegramconfig) / [AutomationDiscordConfig](#automationdiscordconfig) / [AutomationLINEConfig](#automationlineconfig) / [AutomationWhatsAppConfig](#automationwhatsappconfig) / [AutomationMatrixConfig](#automationmatrixconfig) / [AutomationEmailConfig](#automationemailconfig)

### AutomationInstanceTestResponse

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `success` | `boolean` | yes | — |
| `message` | `string` | yes | — |
| `name` | `string` | yes | — |
| `provider` | `string` | yes | — |
| `status` | `string` | yes | — |
| `last_error` | `string` | yes | — |

### AutomationChannelTestResponse

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `success` | `boolean` | yes | — |
| `provider` | `string` | yes | — |
| `message` | `string` | yes | — |

### AutomationDirectSendResult

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `success` | `boolean` | yes | — |
| `message` | `string` | no | — |
| `id` | `string` | no | — |
| `name` | `string` | no | — |
| `pending` | `boolean` | no | — |
| `error` | `string` | no | — |

### AutomationWecomGuide

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `steps` | array&lt;object {`step`: `string`, `title`: `string`, `description`: `string`}&gt; | yes | — |
| `required_fields` | array&lt;object {`field`: `string`, `label`: `string`, `placeholder`: `string`}&gt; | yes | — |
| `callback_url` | `string` | yes | — |

### AutomationK12SubmissionPayload

Requires nonblank text or a nonempty asset_refs array. Asset refs must be owner-scoped uploads.

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `text` | `string` | no | — |
| `asset_refs` | array&lt;`string`&gt; | no | — |

### AutomationK12ReturnAsset

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `asset_ref` | `string` | yes | minLength: 1 |
| `item_ids` | array&lt;`string`&gt; | yes | — |

### AutomationK12PracticePayload

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `paper_no` | `string` | yes | minLength: 1 |
| `return_assets` | array&lt;[AutomationK12ReturnAsset](#automationk12returnasset)&gt; | yes | — |

### AutomationK12WorkflowPayload

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `workflow_id` | `string` | yes | minLength: 1 |
| `workflow_version` | `string` | yes | minLength: 1 |
| `input` | `string` | no | — |

### AutomationWebhookCreated

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `name` | `string` | yes | — |
| `url` | `string` | yes | — |
| `enabled` | `boolean` | yes | — |
| `secret` | `string` | no | One-time secret when server-generated. |
| `binding` | [AutomationK12Binding](#automationk12binding) | no | — |
| `binding_id` | `string` | no | — |
| `type` | `string` | no | — |

### AutomationWebhookUpdated

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `name` | `string` | yes | — |
| `enabled` | `boolean` | yes | — |
| `binding` | [AutomationK12Binding](#automationk12binding) | no | — |
| `secret` | `string` | no | — |

### AutomationReceiptResponse

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `receipt` | [AutomationK12Receipt](#automationk12receipt) | yes | — |

### AutomationWebhookAccepted

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `status` | `string` | yes | enum: `accepted`, `test`, `disabled` |
| `signature` | `string` | no | — |
| `event_type` | `string` | no | — |
| `summary` | `string` | no | — |
| `dispatched` | `boolean` | no | — |
| `detail` | `string` | no | — |

### AutomationWebhookRejection

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `error` | `string` | yes | — |
| `receipt` | [AutomationK12Receipt](#automationk12receipt) | no | — |
| `status` | `string` | no | enum: `rejected` |
| `failure_kind` | `string` | no | — |

### AutomationPanelSummary

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `title` | `string` | yes | — |
| `component_count` | `integer` | yes | — |
| `version` | `integer` | yes | — |

### AutomationConditionRule

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `op` | `string` | yes | enum: `eq`, `ne`, `contains`, `not_contains`, `gt`, `lt`, `gte`, `lte`, `regex`, `empty`, `not_empty` |
| `value` | `string` | no | — |
| `target` | `string` | yes | — |

### AutomationWorkflowNodeData

input uses prompt/value; agent uses role/agent and provider/model; tool uses tool/name plus args; handoff uses to_agent/agent/role or candidates; parallel uses roles; condition uses source/conditions/default. Unknown keys are retained.

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `prompt` | `string` | no | — |
| `value` | `string` | no | — |
| `role` | `string` | no | maxLength: 256 |
| `agent` | `string` | no | — |
| `provider` | `string` | no | — |
| `model` | `string` | no | maxLength: 256 |
| `tool` | `string` | no | maxLength: 256 |
| `name` | `string` | no | — |
| `args` | object (map) | no | — |
| `roles` | `string` / array&lt;`string`&gt; | no | — |
| `to_agent` | `string` | no | — |
| `candidates` | array&lt;`string`&gt; | no | — |
| `source` | `string` | no | — |
| `conditions` | array&lt;[AutomationConditionRule](#automationconditionrule)&gt; | no | — |
| `default` | `string` | no | — |

### AutomationWorkflowNode

data takes priority; config is used when data is empty. id must be nonempty and unique when run.

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `type` | `string` | no | input/agent/handoff/agent_handoff/parallel/fanout/tool/output/condition. Missing type becomes noop; other types pass input through. |
| `label` | `string` | no | maxLength: 64 |
| `data` | [AutomationWorkflowNodeData](#automationworkflownodedata) | no | — |
| `config` | [AutomationWorkflowNodeData](#automationworkflownodedata) | no | — |
| `position` | object {`x`: `number`, `y`: `number`} | no | — |

### AutomationWorkflowEdge

source/target take priority over aliases from/to. Missing endpoints are skipped; unknown node references fail at execution.

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | no | — |
| `source` | `string` | no | — |
| `target` | `string` | no | — |
| `from` | `string` | no | — |
| `to` | `string` | no | — |

### AutomationMessage

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `message` | `string` | yes | — |

### AutomationNamedMessage

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `message` | `string` | yes | — |
| `name` | `string` | no | — |
| `id` | `string` | no | — |

### AutomationError

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `error` | `string` | yes | — |

### AutomationConnections

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `connections` | array&lt;[AutomationConnectionSummary](#automationconnectionsummary)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationConnectors

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `connectors` | array&lt;[AutomationConnectorSummary](#automationconnectorsummary)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationConnectorResources

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `resources` | array&lt;[AutomationConnectorResource](#automationconnectorresource)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationInstances

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `instances` | array&lt;[AutomationInstance](#automationinstance)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationInstanceHealth

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `instances` | array&lt;[AutomationHealthReport](#automationhealthreport)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationDecisions

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `decisions` | array&lt;[AutomationDecision](#automationdecision)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationGrants

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `grants` | array&lt;[AutomationGrant](#automationgrant)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationReceipts

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `receipts` | array&lt;[AutomationK12Receipt](#automationk12receipt)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationPanels

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `panels` | array&lt;[AutomationPanelSummary](#automationpanelsummary)&gt; / `null` | yes | — |
| `total` | `integer` | yes | — |

### AutomationWorkflows

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `workflows` | array&lt;[AutomationWorkflowData](#automationworkflowdata)&gt; / `null` | yes | — |
| `total` | `integer` | yes | — |

### AutomationSharedAgents

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `agents` | array&lt;[AutomationSharedAgent](#automationsharedagent)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationTeamMembers

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `members` | array&lt;[AutomationTeamMember](#automationteammember)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationWebhooks

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `webhooks` | array&lt;[AutomationWebhook](#automationwebhook) / [AutomationK12Binding](#automationk12binding)&gt; | yes | — |
| `k12_bindings` | array&lt;[AutomationK12Binding](#automationk12binding)&gt; / `null` | no | — |
| `total` | `integer` | yes | — |

### AutomationSlackCallback

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `type` | `string` | no | url_verification or event_callback |
| `challenge` | `string` | no | — |
| `event` | object {`type`: `string`, `user`: `string`, `text`: `string`, `channel`: `string`, `ts`: `string`, `bot_id`: `string`} | no | — |

### AutomationLINECallback

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `destination` | `string` | no | — |
| `events` | array&lt;object {`type`: `string`, `replyToken`: `string`, `timestamp`: `integer`, `source`: object {`type`: `string`, `userId`: `string`, `groupId`: `string`, `roomId`: `string`}, `message`: object {`id`: `string`, `type`: `string`, `text`: `string`}}&gt; | no | — |

### AutomationWhatsAppMessage

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `id` | `string` | no | — |
| `from` | `string` | no | — |
| `type` | `string` | no | — |
| `text` | object {`body`: `string`} | no | — |

### AutomationWhatsAppCallback

| 字段 | 类型 | 必填 | 约束 / 默认 / 说明 |
| --- | --- | --- | --- |
| `entry` | array&lt;object {`id`: `string`, `changes`: array&lt;object {`value`: object {`messages`: array&lt;[AutomationWhatsAppMessage](#automationwhatsappmessage)&gt;, `contacts`: array&lt;object {`wa_id`: `string`, `profile`: object {`name`: `string`}}&gt;}}&gt;}&gt; | no | — |

### AutomationPlatformCallback

Selected by provider; WeCom/WeChat instead use native XML callbacks. Native protocol extension fields are retained/ignored according to the adapter.

[AutomationSlackCallback](#automationslackcallback) / [AutomationLINECallback](#automationlinecallback) / [AutomationWhatsAppCallback](#automationwhatsappcallback)
