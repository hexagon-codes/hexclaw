# 聊天、会话、运行流与记忆 API

[English](chat-sessions-memory.en.md) · [公共 API 总览](../api.md) · [OpenAPI](../../api/openapi.yaml)

本文覆盖当前源码的 33 个方法/路径组合。安装包与发布标签以各自版本为准。每个端点都使用当前服务的 Bearer 令牌；声明为兼容字段的 user_id 不改变鉴权身份。模块未挂载通常是普通 HTTP 404，不能当作 handler 结构化错误。

## 通用调用约定

所有端点：`Authorization: Bearer $HEXCLAW_API_TOKEN`，认证失败 `401 {"error":"Valid access token required"}`。JSON body 使用 `Content-Type: application/json`；附件暂存例外为 multipart。没有列出的 query/header 不作为功能契约；除明确字段外，不从客户端 body 读取身份。时间对象使用 RFC 3339；MemoryEntry 的时间字段是存储字符串，不保证完整时间戳。

## 端点索引

| 方法 | 路径 | 用途 | 挂载条件 |
| --- | --- | --- | --- |
| `POST` | `/api/v1/chat` | [发起或继续聊天](#operation-chatsessionchat) | 始终挂载 |
| `POST` | `/api/v1/attachments` | [暂存图片附件](#operation-chatsessionstageattachment) | 始终挂载 |
| `POST` | `/api/v1/sessions` | [创建会话](#operation-chatsessioncreatesession) | 持久化存储可用时挂载（s.store != nil） |
| `GET` | `/api/v1/sessions` | [列出会话](#operation-chatsessionlistsessions) | 持久化存储可用时挂载（s.store != nil） |
| `GET` | `/api/v1/sessions/{id}` | [获取会话](#operation-chatsessiongetsession) | 持久化存储可用时挂载（s.store != nil） |
| `PATCH` | `/api/v1/sessions/{id}` | [修改标题](#operation-chatsessionupdatesession) | 持久化存储可用时挂载（s.store != nil） |
| `POST` | `/api/v1/sessions/{id}/suggest-title` | [生成建议标题](#operation-chatsessionsuggestsessiontitle) | 持久化存储可用时挂载（s.store != nil） |
| `DELETE` | `/api/v1/sessions/{id}` | [删除会话](#operation-chatsessiondeletesession) | 持久化存储可用时挂载（s.store != nil） |
| `GET` | `/api/v1/sessions/{id}/messages` | [读取消息历史](#operation-chatsessionlistmessages) | 持久化存储可用时挂载（s.store != nil） |
| `POST` | `/api/v1/sessions/{id}/messages` | [追加持久消息](#operation-chatsessionappendmessage) | 持久化存储可用时挂载（s.store != nil） |
| `POST` | `/api/v1/sessions/{id}/messages/batch` | [批量追加持久消息](#operation-chatsessionbatchappendmessages) | 持久化存储可用时挂载（s.store != nil） |
| `GET` | `/api/v1/sessions/{id}/branches` | [列出分支](#operation-chatsessionlistbranches) | 持久化存储可用时挂载（s.store != nil） |
| `POST` | `/api/v1/sessions/{id}/fork` | [创建对话分支](#operation-chatsessionforksession) | 持久化存储可用时挂载（s.store != nil） |
| `GET` | `/api/v1/messages/search` | [全文搜索消息](#operation-chatsessionsearchmessages) | 持久化存储可用时挂载（s.store != nil） |
| `DELETE` | `/api/v1/messages/{id}` | [删除单条消息](#operation-chatsessiondeletemessage) | 持久化存储可用时挂载（s.store != nil） |
| `PUT` | `/api/v1/messages/{id}/feedback` | [更新消息反馈](#operation-chatsessionupdatemessagefeedback) | 持久化存储可用时挂载（s.store != nil） |
| `GET` | `/api/v1/streams/active` | [列出活跃运行流](#operation-chatsessionlistactivestreams) | 运行流状态管理器可用时挂载（s.streamStates != nil） |
| `GET` | `/api/v1/streams/{request_id}` | [读取运行流快照](#operation-chatsessiongetstreamsnapshot) | 运行流状态管理器可用时挂载（s.streamStates != nil） |
| `GET` | `/api/v1/memory` | [读取文件记忆](#operation-chatsessiongetmemory) | 始终挂载；文件记忆启用时读取，关闭时返回空投影 |
| `POST` | `/api/v1/memory` | [创建记忆条目](#operation-chatsessionsavememory) | 文件记忆启用时挂载（s.fileMem != nil） |
| `PUT` | `/api/v1/memory` | [替换全部活跃记忆文本](#operation-chatsessionupdatememorywhole) | 文件记忆启用时挂载（s.fileMem != nil） |
| `PUT` | `/api/v1/memory/{id}` | [更新单条记忆](#operation-chatsessionupdatememory) | 文件记忆启用时挂载（s.fileMem != nil） |
| `POST` | `/api/v1/memory/{id}/archive` | [归档记忆](#operation-chatsessionarchivememoryitem) | 文件记忆启用时挂载（s.fileMem != nil） |
| `POST` | `/api/v1/memory/{id}/restore` | [恢复记忆](#operation-chatsessionrestorememoryitem) | 文件记忆启用时挂载（s.fileMem != nil） |
| `POST` | `/api/v1/memory/{id}/pin` | [置顶记忆](#operation-chatsessionpinmemoryitem) | 文件记忆启用时挂载（s.fileMem != nil） |
| `POST` | `/api/v1/memory/{id}/unpin` | [取消置顶](#operation-chatsessionunpinmemoryitem) | 文件记忆启用时挂载（s.fileMem != nil） |
| `DELETE` | `/api/v1/memory/{id}` | [删除记忆条目](#operation-chatsessiondeletememoryitem) | 文件记忆启用时挂载（s.fileMem != nil） |
| `DELETE` | `/api/v1/memory` | [清空文件记忆](#operation-chatsessiondeletememory) | 始终挂载；文件记忆启用时执行，关闭时返回未启用提示 |
| `GET` | `/api/v1/memory/search` | [搜索记忆](#operation-chatsessionsearchmemory) | 文件记忆启用时挂载（s.fileMem != nil） |
| `POST` | `/api/v1/memory/profile/refresh` | [刷新记忆画像](#operation-chatsessionrefreshmemoryprofile) | 文件记忆启用时挂载（s.fileMem != nil） |
| `PUT` | `/api/v1/memory/profile` | [编辑画像及源记忆](#operation-chatsessioneditmemoryprofile) | 文件记忆启用时挂载（s.fileMem != nil） |
| `GET` | `/api/v1/sessions/{id}/checkpoints` | [读取最新检查点](#operation-chatsessionlistcheckpoints) | 检查点管理器可用时挂载（s.checkpointMgr != nil） |
| `GET` | `/ws` | [建立 WebSocket 会话](#operation-chatsessionws) | WebSocket 处理器启用时挂载（s.wsHandler != nil）；此处帧契约对应 adapter/web.WebAdapter |

## 可复制示例

以下沿用 [连接与身份](../api.md#连接与身份) 中的 `HEXCLAW_API_BASE` / `HEXCLAW_API_TOKEN`。标识均为示例；请求返回的 ID、修订号和暂存引用应替换为真实值。示例没有在此文档工作中发起业务调用。

### 会话与原子消息写入

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/sessions" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"id":"session-api-demo","title":"API example"}'
```

`201` 示例：

```json
{"id":"session-api-demo","title":"API example","created_at":"2026-10-07T08:00:00Z"}
```

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/sessions/session-api-demo/messages/batch" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"messages":[{"id":"message-api-demo-user","role":"user","content":"Hello"},{"id":"message-api-demo-assistant","role":"assistant","content":"Hello!"}]}'
```

`201` 示例；这些是调用方已取得的内容，此接口不生成模型回复：

```json
{"ids":["message-api-demo-user","message-api-demo-assistant"],"session_id":"session-api-demo"}
```

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/sessions/session-api-demo/messages?limit=50&offset=0" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN"
```

### 图片暂存后聊天

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/attachments" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Idempotency-Key: attachment-demo-1" \
  -F 'file=@./example.png;type=image/png'
```

`201` 示例：

```json
{"attachment_id":"att_v1_0123456789abcdef0123456789abcdef","digest":"sha256:example","size":1024,"media_type":"image/png","display_name":"example.png","expires_at":"2026-10-07T08:15:00Z"}
```

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/chat" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message":"Describe this image","attachments":[{"attachment_id":"REPLACE_WITH_RETURNED_ATTACHMENT_ID"}]}'
```

引用对象只传 `attachment_id`，不附带名称、MIME 或原始内容；过期、另一个 owner 或无效引用均不可使用。完整聊天与 SSE 例子见 [聊天](../api.md#chat)。

### 记忆列表与写入

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/memory?view=active&limit=50" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN"
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/memory" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"content":"I prefer concise answers.","type":"preference","source":"manual"}'
```

空列表示例（已启用模块）：

```json
{"entries":[],"summary":"","capacity":{"used":0,"max":100},"total":0,"next_cursor":"","has_more":false}
```

`capacity.max` 来自实例配置，不保证为示例值。创建响应示例：

```json
{"id":"e0123456789abcdef","content":"I prefer concise answers.","type":"preference","source":"manual","created_at":"2026-10-07","updated_at":"2026-10-07","hit_count":0,"status":"active"}
```

### SSE 终态与运行身份

成功片段示意（实际可含下文响应模型中的更多字段）：

```text
data: {"content":"Hello","done":false,"sequence":1,"reasoning_disclosure":{"visibility":"not_exposed","source":"","dialect":"","provider":"","model":""}}

data: {"content":"","done":true,"sequence":2,"reasoning_disclosure":{"visibility":"not_exposed","source":"","dialect":"","provider":"","model":""}}

data: [DONE]

```

开流后的错误示例：

```text
data: {"error":"Upstream unavailable","done":true,"code":"UPSTREAM_UNAVAILABLE","retryable":true}

```

错误路径不追加 `[DONE]`。增量 `content` 按序累加；终态 `message_content` 表达完整 Markdown/TeX，不能再次当增量拼接。`sequence` / 消息 ID / `runtime_event` 用来关联实际执行；`reasoning_disclosure.visibility=not_exposed` 不证明模型没有推理。`reasoning_receipt` 固定为 version / reasoning_request / reasoning_support / reasoning_execution 四字段，不含内部证据。

### WebSocket 握手与帧

握手必须使用能设置 `Authorization` 的客户端。以下 Python 示例依赖已安装的 `websockets`，它发起一条真实聊天请求；请先设置当前实例令牌。

```python
import asyncio
import json
import os
from websockets.asyncio.client import connect

async def main():
    base = os.environ["HEXCLAW_API_BASE"]
    uri = base.replace("https://", "wss://", 1).replace("http://", "ws://", 1) + "/ws"
    headers = {"Authorization": "Bearer " + os.environ["HEXCLAW_API_TOKEN"]}
    async with connect(uri, additional_headers=headers) as socket:
        await socket.send(json.dumps({"type": "message", "content": "Hello", "request_id": "ws-example-1"}))
        async for payload in socket:
            frame = json.loads(payload)
            print(frame)
            if frame["type"] in ("reply", "error") or frame.get("done"):
                break

asyncio.run(main())
```

入站控制帧：每行表示单独发送的一条 JSON 消息，不是一个合并的请求体。

```jsonl
{"type":"ping"}
{"type":"resume","request_id":"ORIGINAL_REQUEST_ID"}
{"type":"cancel","request_id":"ORIGINAL_REQUEST_ID"}
```

| 帧 | 所需字段 / 行为 |
| --- | --- |
| `message` | 非空白 `content` 或有效 `attachments`；可带 session_id/request_id/provider/model/role/temperature/max_tokens/metadata。空 request_id 由服务生成；user_id 不改变 owner |
| `ping` | 返回 `pong`，是应用层 JSON 心跳 |
| `resume` | request_id；先返回累计 `stream_snapshot`，未结束则订阅原请求后续 chunk；不是重发 message |
| `cancel` | request_id；为空时可按已归属的 session_id 找当前请求。没有独立取消 ACK，读取保留快照判定状态 |
| `reply` | 同步完整回复，content/message_content 及可选 usage/tool_calls/blocks/命中字段 |
| `chunk` | 增量 content/reasoning，done=true 表终态；可携消息 ID/sequence/runtime_event/公共推理回执 |
| `error` | content 为公开错误；可带 code/retryable/session_id/request_id。升级成功或收到 chunk 不等于最终成功 |
| `stream_snapshot` | 累计 content/reasoning、done、sequence/last_sequence、runtime_events；没有 Snapshot REST 模型的 status/started_at/updated_at 字段 |

未知帧类型及无有效输入的 message 当前会忽略。运行帧单次处理期限为 10 分钟；全部订阅连接离开后默认给 5 秒重连宽限，再取消原运行；默认注册器保留终态 2 分钟。恢复窗口是当前实现的进程内行为，不是持久请求保证。恢复快照应替换本地累计投影，并用 sequence 去重后续事件。

### WebSocket 工具审批

审批走原 WebSocket，不存在平行 REST 决策入口。收到 `tool_approval_request` 时保留其 request_id、owner_id、session_id、invocation_id、arguments_digest、security_scope_digest、scope_schema_version、deadline_at；还可含 tool_name、arguments、content 与 risk 元数据。

回应 `tool_approval_response`（兼容类型 `tool_permission_response`）必须带完整原身份，加非空 `decision_id`、`idempotency_key` 和 `decision`：`approved_once` / `approved_remember` / `denied`。顶层与 metadata 同时提供的值须一致；metadata 可用 approval_request_id/request_id。owner_id 必须匹配当前认证身份；旧 approved/remember 两布尔字段不足以提交决策。

```json
{"type":"tool_approval_response","request_id":"ORIGINAL_APPROVAL_ID","owner_id":"ORIGINAL_OWNER_ID","session_id":"ORIGINAL_SESSION_ID","invocation_id":"ORIGINAL_INVOCATION_ID","arguments_digest":"ORIGINAL_ARGUMENTS_DIGEST","security_scope_digest":"ORIGINAL_SCOPE_DIGEST","scope_schema_version":1,"deadline_at":"ORIGINAL_DEADLINE_RFC3339","decision_id":"decision-example-1","idempotency_key":"approval-example-1","decision":"denied"}
```

以上原身份全部从当前审批请求复制，不自行构造。`tool_approval_ack` 回显身份与决策，status 可为 accepted / already_accepted / expired / rejected，终态结果位于 metadata.terminal_result。重连时 `tool_approval_reconcile` 仅提交完整原身份，不带新决策；会返回原 pending 请求、已有 durable ACK 或 `tool_approval_terminal`（expired/fenced）。审批 ACK 只证明决策接纳，不等于工具执行或产物交付成功。


## 逐端点契约

<a id="operation-chatsessionchat"></a>
### `POST /api/v1/chat`

发起或继续聊天

认证：Bearer. 挂载：始终挂载.

默认完整 JSON；Accept 包含 text/event-stream 时切换 SSE。处理可能创建/继续会话、持久化消息、消耗模型额度及调用工具。request_id 不保证未知结果可安全重发。完整示例见上级聊天文档。

请求体上限：20971520 bytes.

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| header | `Accept` | 否 | Use text/event-stream for SSE; otherwise complete JSON — |

JSON body（字段如下；嵌套结构见模型）：

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `message` | string | 否 | — |
| `session_id` | string | 否 | Omitted/empty starts a session; reuse the returned ID to continue. |
| `user_id` | string | 否 | compatibility field |
| `role` | string | 否 | Optional Agent role; not a hardcoded enum at this API handler. |
| `provider` | string | 否 | Optional explicit currently configured provider; validated before processing. |
| `model` | string | 否 | Optional explicit completion model present in the active provider configuration. |
| `platform` | string | 否 | compatibility field |
| `attachments` | [ChatSessionAttachment](#schema-chatsessionattachment)[] | 否 | maxItems=20 |
| `metadata` | object | 否 | String map. Reserved internal dispatch/sampling keys are stripped. Explicit role/provider/model fields override corresponding metadata. |
| `request_id` | string | 否 | Correlation and recovery identity, not a durable admission/idempotency guarantee. |
| `temperature` | number / null | 否 | minimum=0; maximum=2; Omitted/null follows the Agent/model; zero remains an explicit value. |
| `max_tokens` | integer / null | 否 | minimum=1; maximum=1000000; Omitted/null follows the Agent/model. |

成功：`200` [ChatSessionChatResponse](#schema-chatsessionchatresponse).

错误：401 认证失败；400: Invalid JSON, input, attachment, provider/model or sampling fields; 403: Gateway policy rejection; code/layer may be present; 404: Attachment reference is unavailable, conflicting or over the count limit during resolution; 422: MODEL_CAPABILITY_MISMATCH; 429: UPSTREAM_RATE_LIMITED; 500: Unclassified engine/application error or streaming unavailable; 503: Engine not ready, UPSTREAM_UNAVAILABLE or UPSTREAM_POOL_EXHAUSTED

源码：[api/server.go](../../api/server.go) — `handleChat`.

<a id="operation-chatsessionstageattachment"></a>
### `POST /api/v1/attachments`

暂存图片附件

认证：Bearer. 挂载：始终挂载.

只接受一个名为 file 且带文件名的 multipart part，无额外表单字段。单文件 200 MiB；以实际内容识别图片，声明 MIME 如存在须匹配。暂存有效期 15 分钟，绑定当前 owner，进程关闭后不保留；201 创建，200 幂等命中。

请求体上限：210763776 bytes.

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| header | `Idempotency-Key` | 是 | Required, trimmed; owner-scoped; same key+digest+size+media type+display name replays until expiry pattern=^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$ |

Body: `multipart/form-data`, 必填单个 `file` binary part，带文件名；无其他 part。

成功：`201` [ChatSessionAttachmentReceipt](#schema-chatsessionattachmentreceipt).

错误：401 认证失败；400: Missing/invalid Idempotency-Key or not exactly one multipart file part; 409: Key conflicts with different content or metadata; 413: File exceeds 200 MiB; 422: Empty file, invalid filename, non-image or mismatched media type; 500: Staging failed; 503: 256-entry / 512-MiB staging capacity exhausted

200 为相同附件的幂等命中，响应形状相同。

源码：[api/attachment_staging.go](../../api/attachment_staging.go) — `handleStageAttachment`.

<a id="operation-chatsessioncreatesession"></a>
### `POST /api/v1/sessions`

创建会话

认证：Bearer. 挂载：持久化存储可用时挂载（s.store != nil）.

id 由客户端提供且不能为空；title 可空，创建接口没有改名接口的 200 字符校验。owner 取自令牌，platform=web、status=1。重复 ID 为存储失败，不保证幂等。

请求体上限：1048576 bytes.

没有 path/query/额外 header 参数。

JSON body（字段如下；嵌套结构见模型）：

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `id` | string | 是 | minLength=1; Client-supplied session ID; no whitespace normalization at this handler. |
| `title` | string | 否 | Optional; create does not apply the rename nonblank/200-rune validation. |
| `user_id` | string | 否 | compatibility field |

成功：`201` [ChatSessionSessionCreated](#schema-chatsessionsessioncreated).

错误：401 认证失败；400: Invalid JSON or request fields; 500: Storage operation failed

源码：[api/handler_session.go](../../api/handler_session.go) — `handleCreateSession`.

<a id="operation-chatsessionlistsessions"></a>
### `GET /api/v1/sessions`

列出会话

认证：Bearer. 挂载：持久化存储可用时挂载（s.store != nil）.

只列当前 owner，按更新时间倒序。total=len(本页)+offset，不是真实总数；has_more 仅表示本页数量等于 limit。

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| query | `limit` | 否 | Missing, invalid or <=0 uses 20; handler has no upper clamp. default=20 |
| query | `offset` | 否 | Missing/invalid parses as 0; handler does not clamp negative values. default=0 |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionSessionList](#schema-chatsessionsessionlist).

错误：401 认证失败；500: Session listing failed

源码：[api/handler_session.go](../../api/handler_session.go) — `handleListSessions`.

<a id="operation-chatsessiongetsession"></a>
### `GET /api/v1/sessions/{id}`

获取会话

认证：Bearer. 挂载：持久化存储可用时挂载（s.store != nil）.



| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Session ID — |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionSession](#schema-chatsessionsession).

错误：401 认证失败；404: Session missing or not owned by the current principal; 500: Storage operation failed

源码：[api/handler_session.go](../../api/handler_session.go) — `handleGetSession`.

<a id="operation-chatsessionupdatesession"></a>
### `PATCH /api/v1/sessions/{id}`

修改标题

认证：Bearer. 挂载：持久化存储可用时挂载（s.store != nil）.

title 修剪后不能为空；修改后的标题最多 200 Unicode 字符。同值历史标题不受新增长度限制。更新后返回 id/title/updated_at。

请求体上限：1048576 bytes.

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Session ID — |

JSON body（字段如下；嵌套结构见模型）：

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `title` | string | 是 | Trimmed, nonblank; changed titles are limited to 200 Unicode code points. An unchanged historical title is preserved. |

成功：`200` [ChatSessionSessionUpdated](#schema-chatsessionsessionupdated).

错误：401 认证失败；400: Invalid JSON or request fields; 404: Session missing or not owned by the current principal; 500: Storage operation failed

源码：[api/handler_session.go](../../api/handler_session.go) — `handleUpdateSession`.

<a id="operation-chatsessionsuggestsessiontitle"></a>
### `POST /api/v1/sessions/{id}/suggest-title`

生成建议标题

认证：Bearer. 挂载：持久化存储可用时挂载（s.store != nil）.

可不带请求体；当前 handler 仅 ContentLength>0 时解码 body，因此 chunked/未知长度 body 不读取。expected_title 非空且与当前标题不一致时不覆盖。读取最多 6 条现有消息，必要时调用模型并写标题；无消息、不支持生成、生成失败/无变化均 200 updated=false。

请求体上限：1048576 bytes.

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Session ID — |

JSON body（字段如下；嵌套结构见模型）：

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `expected_title` | string | 否 | — |

成功：`200` [ChatSessionTitleSuggested](#schema-chatsessiontitlesuggested).

错误：401 认证失败；400: Invalid JSON or request fields; 404: Session missing or not owned by the current principal; 500: Storage operation failed

源码：[api/handler_session.go](../../api/handler_session.go) — `handleSuggestSessionTitle`.

<a id="operation-chatsessiondeletesession"></a>
### `DELETE /api/v1/sessions/{id}`

删除会话

认证：Bearer. 挂载：持久化存储可用时挂载（s.store != nil）.

删除会话及其消息，成功后调用已配置的 sessionDeletedHook。200 {message:"会话已删除"}。

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Session ID — |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

错误：401 认证失败；404: Session missing or not owned by the current principal; 500: Storage operation failed

源码：[api/handler_session.go](../../api/handler_session.go) — `handleDeleteSession`.

<a id="operation-chatsessionlistmessages"></a>
### `GET /api/v1/sessions/{id}/messages`

读取消息历史

认证：Bearer. 挂载：持久化存储可用时挂载（s.store != nil）.

按创建时间正序；total 优先 CountMessages，计数失败才回退 len(本页)+offset。message_content 为读取投影；metadata/meta 是 JSON 字符串。

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Session ID — |
| query | `limit` | 否 | Missing, invalid, <=0 or >200 uses 50. default=50 |
| query | `offset` | 否 | Missing/invalid uses 0; negative values clamp to 0. default=0 |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionMessageList](#schema-chatsessionmessagelist).

错误：401 认证失败；404: Session missing or not owned by the current principal; 500: Storage operation failed

源码：[api/handler_session.go](../../api/handler_session.go) — `handleListMessages`.

<a id="operation-chatsessionappendmessage"></a>
### `POST /api/v1/sessions/{id}/messages`

追加持久消息

认证：Bearer. 挂载：持久化存储可用时挂载（s.store != nil）.

直接写历史，不触发聊天/模型。content 可空；role 仅 user/assistant/system。空 id 自动生成；重复 id 不保证 UPSERT，可能 500。content_type 缺省随 metadata 是否存在取 multimodal_json/text。

请求体上限：8388608 bytes.

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Session ID — |

JSON body（字段如下；嵌套结构见模型）：

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `id` | string | 否 | Trimmed client ID; blank generates msg-*. Duplicate IDs are not a guaranteed idempotent replay and may fail with 500. |
| `role` | string | 是 | enum="user", "assistant", "system" |
| `content` | string | 否 | — |
| `content_type` | string | 否 | When omitted: multimodal_json if metadata is present, otherwise text. Nonempty values are not enum-validated. |
| `metadata` | JSON | 否 | Raw JSON stored as a JSON string. Attachments in an object envelope retain the complete envelope on reads; no fixed metadata schema is enforced. |
| `model_name` | string | 否 | — |
| `prompt_tokens` | integer | 否 | — |
| `completion_tokens` | integer | 否 | — |
| `finish_reason` | string | 否 | — |
| `parent_id` | string | 否 | — |
| `request_id` | string | 否 | — |

成功：`201` [ChatSessionMessageCreated](#schema-chatsessionmessagecreated).

错误：401 认证失败；400: Invalid JSON or request fields; 404: Session missing or not owned by the current principal; 500: Storage operation failed

源码：[api/handler_session.go](../../api/handler_session.go) — `handleAppendMessage`.

<a id="operation-chatsessionbatchappendmessages"></a>
### `POST /api/v1/sessions/{id}/messages/batch`

批量追加持久消息

认证：Bearer. 挂载：持久化存储可用时挂载（s.store != nil）.

messages 必填且 1–50 条，逐项字段与单条写入相同。全部写入同一事务，任一失败全部回滚；不触发模型。

请求体上限：16777216 bytes.

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Session ID — |

JSON body（字段如下；嵌套结构见模型）：

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `messages` | [ChatSessionAppendMessageRequest](#schema-chatsessionappendmessagerequest)[] | 是 | minItems=1; maxItems=50 |

成功：`201` [ChatSessionBatchMessageCreated](#schema-chatsessionbatchmessagecreated).

错误：401 认证失败；400: Invalid JSON or request fields; 404: Session missing or not owned by the current principal; 500: Storage operation failed

源码：[api/handler_session.go](../../api/handler_session.go) — `handleBatchAppendMessages`.

<a id="operation-chatsessionlistbranches"></a>
### `GET /api/v1/sessions/{id}/branches`

列出分支

认证：Bearer. 挂载：持久化存储可用时挂载（s.store != nil）.



| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Parent session ID — |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionBranchList](#schema-chatsessionbranchlist).

错误：401 认证失败；404: Session missing or not owned by the current principal; 500: Storage operation failed

源码：[api/handler_session.go](../../api/handler_session.go) — `handleListBranches`.

<a id="operation-chatsessionforksession"></a>
### `POST /api/v1/sessions/{id}/fork`

创建对话分支

认证：Bearer. 挂载：持久化存储可用时挂载（s.store != nil）.

message_id 必填。include_message 省略或显式 null 时默认 true，复制源会话截至该消息的前缀；false 排除边界消息。创建新会话和复制历史，不生成模型回答；边界消息不存在等底层失败目前返回 500。

请求体上限：1048576 bytes.

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Source session ID — |

JSON body（字段如下；嵌套结构见模型）：

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `message_id` | string | 是 | minLength=1 |
| `user_id` | string | 否 | compatibility field |
| `include_message` | boolean / null | 否 | default=true; Omitted/null includes message_id; false copies the prefix before it. |

成功：`200` [ChatSessionForkResult](#schema-chatsessionforkresult).

错误：401 认证失败；400: Invalid JSON or request fields; 404: Session missing or not owned by the current principal; 500: Storage operation failed

源码：[api/handler_session.go](../../api/handler_session.go) — `handleForkSession`.

<a id="operation-chatsessionsearchmessages"></a>
### `GET /api/v1/messages/search`

全文搜索消息

认证：Bearer. 挂载：持久化存储可用时挂载（s.store != nil）.

只检索当前 owner 的会话。返回 results[].message/session_title/rank，total 为搜索计数，query 回显原查询。

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| query | `q` | 是 | Required, nonempty; maximum 512 Unicode code points. maxLength=512 |
| query | `limit` | 否 | Missing, invalid or <=0 uses 20; no upper clamp. default=20 |
| query | `offset` | 否 | Missing/invalid parses as 0; no negative clamp. default=0 |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionSearchMessageList](#schema-chatsessionsearchmessagelist).

错误：401 认证失败；400: Empty q or q exceeds 512 characters; 500: Search failed

源码：[api/handler_session.go](../../api/handler_session.go) — `handleSearchMessages`.

<a id="operation-chatsessiondeletemessage"></a>
### `DELETE /api/v1/messages/{id}`

删除单条消息

认证：Bearer. 挂载：持久化存储可用时挂载（s.store != nil）.

删除单条持久消息。与会话端点不同，消息归属检查失败返回 403；成功 200 {message:"消息已删除"}。

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Message ID — |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

错误：401 认证失败；400: Empty message ID; 403: Message session ownership check failed; 404: Message is missing or lookup failed; 500: Delete failed

源码：[api/handler_session.go](../../api/handler_session.go) — `handleDeleteMessage`.

<a id="operation-chatsessionupdatemessagefeedback"></a>
### `PUT /api/v1/messages/{id}/feedback`

更新消息反馈

认证：Bearer. 挂载：持久化存储可用时挂载（s.store != nil）.

feedback 可为 like/dislike/空字符串；省略或空字符串清除反馈。200 {message:"反馈已更新"}。

请求体上限：1048576 bytes.

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Message ID — |

JSON body（字段如下；嵌套结构见模型）：

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `feedback` | string | 否 | default=""; enum="", "like", "dislike"; Empty or omitted clears feedback. |

成功：`200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

错误：401 认证失败；400: Invalid JSON, empty ID or feedback outside empty/like/dislike; 403: Message session ownership check failed; 404: Message is missing or lookup failed; 500: Feedback update failed

源码：[api/handler_session.go](../../api/handler_session.go) — `handleUpdateMessageFeedback`.

<a id="operation-chatsessionlistactivestreams"></a>
### `GET /api/v1/streams/active`

列出活跃运行流

认证：Bearer. 挂载：运行流状态管理器可用时挂载（s.streamStates != nil）.

仅当前 owner、未终止的 pending/streaming 快照，按更新时间倒序。只读状态，不触发或恢复模型。

没有 path/query/额外 header 参数。

Body: 不读取 / 不需要。

成功：`200` [ChatSessionStreamList](#schema-chatsessionstreamlist).

错误：401 认证失败；没有其他 handler 错误码。

源码：[api/handler_streams.go](../../api/handler_streams.go) — `handleListActiveStreams`.

<a id="operation-chatsessiongetstreamsnapshot"></a>
### `GET /api/v1/streams/{request_id}`

读取运行流快照

认证：Bearer. 挂载：运行流状态管理器可用时挂载（s.streamStates != nil）.

返回累计内容、reasoning、status/done、消息身份、runtime_events 和 last_sequence。进程内恢复窗口由注册器配置，默认终态保留 2 分钟；不承诺跨重启持久恢复。

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `request_id` | 是 | Original stream request ID — |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionSnapshot](#schema-chatsessionsnapshot).

错误：401 认证失败；400: Empty request_id; 404: No retained snapshot for this owner/request

源码：[api/handler_streams.go](../../api/handler_streams.go) — `handleGetStreamSnapshot`.

<a id="operation-chatsessiongetmemory"></a>
### `GET /api/v1/memory`

读取文件记忆

认证：Bearer. 挂载：始终挂载；文件记忆启用时读取，关闭时返回空投影.

返回 _global 条目、上下文 summary、capacity 和分页；type/source/exclude_id 在分页前过滤。未启用 FileMemory 时忽略查询并固定返回 entries=[]、summary=""、capacity={used:0,max:0}，不带分页字段。

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| query | `view` | 否 | Default active; choose active/archived/all. default="active"; enum="active", "archived", "all" |
| query | `limit` | 否 | Noninteger returns 400; <=0 uses 50; >200 clamps to 200. default=50 |
| query | `cursor` | 否 | Nonnegative decimal offset; invalid or negative returns 400. — |
| query | `type` | 否 | Exact type filter; no enum validation. — |
| query | `source` | 否 | Exact source filter; no enum validation. — |
| query | `exclude_id` | 否 | Exclude exactly one entry ID. — |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionMemoryList](#schema-chatsessionmemorylist).

错误：401 认证失败；400: Invalid limit, cursor or view; only when FileMemory is enabled

源码：[api/handler_misc.go](../../api/handler_misc.go) — `handleGetMemory`.

<a id="operation-chatsessionsavememory"></a>
### `POST /api/v1/memory`

创建记忆条目

认证：Bearer. 挂载：文件记忆启用时挂载（s.fileMem != nil）.

写入 _global 活跃记忆并请求画像刷新；可能执行容量淘汰。type 默认 fact，source 默认 manual，均不限制枚举。成功返回当前最后一条解析记忆；若没有可解析条目返回 {message:"记忆已保存"}。纯空白内容会被底层忽略，不应拿它创建条目。

请求体上限：1048576 bytes.

没有 path/query/额外 header 参数。

JSON body（字段如下；嵌套结构见模型）：

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `content` | string | 是 | minLength=1; Nonempty at HTTP handler; FileMemory trims it. Supply nonblank text to create an entry. |
| `type` | string | 否 | default="fact"; Common values identity/preference/fact/instruction/context/rule; no enum validation. |
| `source` | string | 否 | default="manual"; Common values manual/chat_explicit/chat_extract/system; no enum validation. |

成功：`200` [ChatSessionMemoryEntry](#schema-chatsessionmemoryentry).

错误：401 认证失败；400: Invalid JSON or empty content; 500: Memory save failed

成功也可返回 MessageText；详见说明。

源码：[api/handler_misc.go](../../api/handler_misc.go) — `handleSaveMemory`.

<a id="operation-chatsessionupdatememorywhole"></a>
### `PUT /api/v1/memory`

替换全部活跃记忆文本

认证：Bearer. 挂载：文件记忆启用时挂载（s.fileMem != nil）.

完整替换 _global/MEMORY.md，不是按条目合并；content 可为空或省略，此时写空文件。请求画像刷新；200 {message:"记忆已更新"}。

请求体上限：1048576 bytes.

没有 path/query/额外 header 参数。

JSON body（字段如下；嵌套结构见模型）：

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `content` | string | 否 | Whole active MEMORY.md replacement without id; a single entry update with id. |

成功：`200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

错误：401 认证失败；400: Invalid JSON; 500: Memory replacement failed

源码：[api/handler_extended.go](../../api/handler_extended.go) — `handleUpdateMemory`.

<a id="operation-chatsessionupdatememory"></a>
### `PUT /api/v1/memory/{id}`

更新单条记忆

认证：Bearer. 挂载：文件记忆启用时挂载（s.fileMem != nil）.

content 必须非空，按实际条目 ID 更新正文并请求画像刷新；200 {message:"记忆已更新"}。底层更新错误统一映射 404。

请求体上限：1048576 bytes.

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Existing memory entry ID — |

JSON body（字段如下；嵌套结构见模型）：

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `content` | string | 是 | minLength=1; New entry text; empty/omitted is rejected. |

content 必须非空；省略返回 400。

成功：`200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

错误：401 认证失败；400: Invalid JSON or empty content; 404: Entry update failed, including missing ID or file error

源码：[api/handler_extended.go](../../api/handler_extended.go) — `handleUpdateMemory`.

<a id="operation-chatsessionarchivememoryitem"></a>
### `POST /api/v1/memory/{id}/archive`

归档记忆

认证：Bearer. 挂载：文件记忆启用时挂载（s.fileMem != nil）.

只允许活跃记忆，移动到归档并请求画像刷新。 请求体不读取；200 返回 message。

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Memory entry ID — |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

错误：401 认证失败；400: Empty memory ID; 404: Entry/state/file operation failed

源码：[api/handler_extended.go](../../api/handler_extended.go) — `handleArchiveMemoryItem`.

<a id="operation-chatsessionrestorememoryitem"></a>
### `POST /api/v1/memory/{id}/restore`

恢复记忆

认证：Bearer. 挂载：文件记忆启用时挂载（s.fileMem != nil）.

只允许归档记忆，恢复到活跃文件，必要时执行容量淘汰并请求画像刷新。 请求体不读取；200 返回 message。

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Memory entry ID — |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

错误：401 认证失败；400: Empty memory ID; 404: Entry/state/file operation failed

源码：[api/handler_extended.go](../../api/handler_extended.go) — `handleRestoreMemoryItem`.

<a id="operation-chatsessionpinmemoryitem"></a>
### `POST /api/v1/memory/{id}/pin`

置顶记忆

认证：Bearer. 挂载：文件记忆启用时挂载（s.fileMem != nil）.

原位更新 pinned=true，正文不变。 请求体不读取；200 返回 message。

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Memory entry ID — |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

错误：401 认证失败；400: Empty memory ID; 404: Entry/state/file operation failed

源码：[api/handler_extended.go](../../api/handler_extended.go) — `handlePinMemoryItem`.

<a id="operation-chatsessionunpinmemoryitem"></a>
### `POST /api/v1/memory/{id}/unpin`

取消置顶

认证：Bearer. 挂载：文件记忆启用时挂载（s.fileMem != nil）.

原位更新 pinned=false，正文不变。 请求体不读取；200 返回 message。

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Memory entry ID — |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

错误：401 认证失败；400: Empty memory ID; 404: Entry/state/file operation failed

源码：[api/handler_extended.go](../../api/handler_extended.go) — `handleUnpinMemoryItem`.

<a id="operation-chatsessiondeletememoryitem"></a>
### `DELETE /api/v1/memory/{id}`

删除记忆条目

认证：Bearer. 挂载：文件记忆启用时挂载（s.fileMem != nil）.

删除指定条目并请求画像刷新；200 {message:"记忆已删除"}。

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Memory entry ID — |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

错误：401 认证失败；400: Empty memory ID; 404: Entry/file delete failed

源码：[api/handler_extended.go](../../api/handler_extended.go) — `handleDeleteMemoryItem`.

<a id="operation-chatsessiondeletememory"></a>
### `DELETE /api/v1/memory`

清空文件记忆

认证：Bearer. 挂载：始终挂载；文件记忆启用时执行，关闭时返回未启用提示.

启用时清除当前 FileMemory 目录下所有 Markdown 文件，包含全局、角色和归档，并请求画像刷新；不是清空向量库或只删当前页。未启用时 200 {message:"记忆模块未启用"}。请求体不读取。

没有 path/query/额外 header 参数。

Body: 不读取 / 不需要。

成功：`200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

错误：401 认证失败；500: ClearAll failed; only when FileMemory is enabled

源码：[api/handler_extended.go](../../api/handler_extended.go) — `handleDeleteMemory`.

<a id="operation-chatsessionsearchmemory"></a>
### `GET /api/v1/memory/search`

搜索记忆

认证：Bearer. 挂载：文件记忆启用时挂载（s.fileMem != nil）.

FileMemory 按记忆目录及一级角色目录中的 Markdown 关键词检索，包含归档，最多 100 条；可选 VectorMemory 语义检索最多 5 条，向量检索失败被忽略。exclude_id 只作用于文件检索；两类无结果均可为 null。results 包含 file/line/content/score，total 为两类返回结果数量之和。

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| query | `q` | 是 | Required nonempty query; no handler length limit. — |
| query | `exclude_id` | 否 | Exclude this ID from file keyword results only. — |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionMemorySearch](#schema-chatsessionmemorysearch).

错误：401 认证失败；400: Empty q

源码：[api/handler_misc.go](../../api/handler_misc.go) — `handleSearchMemory`.

<a id="operation-chatsessionrefreshmemoryprofile"></a>
### `POST /api/v1/memory/profile/refresh`

刷新记忆画像

认证：Bearer. 挂载：文件记忆启用时挂载（s.fileMem != nil）.

不读取请求体；需要引擎的画像维护能力。可能调用模型、更新 Pinned identity 画像；action=insert/update/skip。结果未知返回 409，不自动重发原请求。

没有 path/query/额外 header 参数。

Body: 不读取 / 不需要。

成功：`200` [ChatSessionProfileRefreshResult](#schema-chatsessionprofilerefreshresult).

错误：401 认证失败；409: Profile became stale or the previous outcome is unknown; 422: Profile maintenance/generation failed; 503: Engine does not implement profile maintenance

源码：[api/handler_memory_profile.go](../../api/handler_memory_profile.go) — `handleRefreshMemoryProfile`.

<a id="operation-chatsessioneditmemoryprofile"></a>
### `PUT /api/v1/memory/profile`

编辑画像及源记忆

认证：Bearer. 挂载：文件记忆启用时挂载（s.fileMem != nil）.

revision 使用 GET memory 中画像条目的 profile_revision；content 必须非空白。通过模型将修正关联到源记忆后原子更新来源与画像；无法可靠关联不保存草稿。相同修正已有持久 applied 记录时返回成功。

请求体上限：1048576 bytes.

没有 path/query/额外 header 参数。

JSON body（字段如下；嵌套结构见模型）：

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `revision` | string | 是 | minLength=1; Current profile_revision from GET /api/v1/memory |
| `content` | string | 是 | minLength=1; Nonblank revised profile; applied to the profile and reliably matched source memories. |

成功：`200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

错误：401 认证失败；400: Invalid JSON; 409: Revision is stale or prior outcome is unknown; 422: Blank revision/content, rejected/unreliably linked changes or other maintenance failure; 503: Engine does not implement profile maintenance

源码：[api/handler_memory_profile.go](../../api/handler_memory_profile.go) — `handleEditMemoryProfile`.

<a id="operation-chatsessionlistcheckpoints"></a>
### `GET /api/v1/sessions/{id}/checkpoints`

读取最新检查点

认证：Bearer. 挂载：检查点管理器可用时挂载（s.checkpointMgr != nil）.

当前 handler 调用 Load 而非 List，返回 checkpoints=[] 或单个 CheckpointData，不是全量列表/CheckpointSummary。任何加载错误均 200 空数组。缺少 id 的 handler 分支为 400 text/plain；没有 body/query。此 handler 没有调用 getOwnedSession，不能宣称逐会话归属校验。

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| path | `id` | 是 | Session ID — |

Body: 不读取 / 不需要。

成功：`200` [ChatSessionCheckpointList](#schema-chatsessioncheckpointlist).

错误：401 认证失败；没有其他 handler 错误码。

源码：[api/handler_tools.go](../../api/handler_tools.go) — `handleListCheckpoints`.

<a id="operation-chatsessionws"></a>
### `GET /ws`

建立 WebSocket 会话

认证：Bearer. 挂载：WebSocket 处理器启用时挂载（s.wsHandler != nil）；此处帧契约对应 adapter/web.WebAdapter.

以当前 Bearer 认证的 HTTP Upgrade 请求握手；没有 query token 契约。原生客户端可不带 Origin；浏览器 Origin 遵循现有本地/Tauri规则。升级后使用 JSON 文本帧，单帧读取上限 20 MiB。客户端标准浏览器 WebSocket API 不能自行添加 Authorization，需现有原生/代理通道。

| 位置 | 字段 | 必填 | 类型 / 规则 |
| --- | --- | --- | --- |
| header | `Origin` | 否 | Optional for native clients. When present: existing local HTTP or Tauri origin rules apply; remote/https origins are rejected. — |

Body: 不读取 / 不需要。

成功：`101` [ChatSessionWebSocketFrame](#schema-chatsessionwebsocketframe).

错误：401 认证失败；403: Origin rejected (text/plain before upgrade); other WebSocket handshake errors follow the transport library

101 之后是 WebSocket JSON 帧；HTTP 成功响应不带普通 JSON body。403 为 text/plain。

源码：[adapter/web/web.go](../../adapter/web/web.go) — `handleWS`.

## 请求与响应模型

以下表格中的“必填”对响应表示服务序列化时始终下发，对请求表示有效输入所需字段。Go `omitempty` 字段可能省略；部分 slice 可为 null。Raw JSON 与工具 arguments 保持真实动态契约，不凭文档新增字段限制。

<a id="schema-chatsessionusage"></a>
### `ChatSessionUsage`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `input_tokens` | integer | 是 | — |
| `output_tokens` | integer | 是 | — |
| `total_tokens` | integer | 是 | — |
| `provider` | string | 是 | — |
| `model` | string | 是 | — |
| `cost` | number | 否 | — |

<a id="schema-chatsessiontoolcall"></a>
### `ChatSessionToolCall`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `origin` | [ChatSessionToolOrigin](#schema-chatsessiontoolorigin) | 否 | — |
| `execution` | [ChatSessionSandboxExecution](#schema-chatsessionsandboxexecution) | 否 | — |
| `id` | string | 是 | — |
| `name` | string | 是 | — |
| `arguments` | string | 是 | — |
| `result` | string | 否 | — |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | 否 | — |
| `status` | string | 否 | — |
| `duration_ms` | integer | 否 | — |

<a id="schema-chatsessionblock"></a>
### `ChatSessionBlock`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `retrieval` | [ChatSessionRetrievalActivity](#schema-chatsessionretrievalactivity) | 否 | — |
| `thinking` | string | 否 | — |
| `type` | string | 是 | enum="text", "thinking", "tool_use", "tool_result", "retrieval" |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | 否 | — |
| `text` | string | 否 | — |
| `id` | string | 否 | — |
| `name` | string | 否 | — |
| `input` | string | 否 | — |
| `toolUseId` | string | 否 | — |
| `toolName` | string | 否 | — |
| `output` | string | 否 | — |
| `isError` | boolean | 否 | — |

<a id="schema-chatsessionknowledgehit"></a>
### `ChatSessionKnowledgeHit`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `doc_id` | string | 否 | — |
| `document_generation` | integer | 否 | — |
| `source_digest` | string | 否 | — |
| `page_start` | integer | 否 | — |
| `page_end` | integer | 否 | — |
| `doc_title` | string | 否 | — |
| `source` | string | 否 | — |
| `content` | string | 否 | — |
| `score` | number | 否 | — |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | 否 | — |

<a id="schema-chatsessionmemoryhit"></a>
### `ChatSessionMemoryHit`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `id` | string | 否 | — |
| `content` | string | 否 | — |
| `source` | string | 否 | — |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | 否 | — |

<a id="schema-chatsessionreplychunk"></a>
### `ChatSessionReplyChunk`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `content` | string | 是 | — |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | 否 | — |
| `render_manifest` | [ChatSessionRenderManifest](#schema-chatsessionrendermanifest) | 否 | — |
| `reasoning` | string | 否 | — |
| `done` | boolean | 是 | — |
| `error` | string | 否 | Transport error messages are serialized as strings by the error-frame handler. |
| `metadata` | object | 否 | — |
| `usage` | [ChatSessionUsage](#schema-chatsessionusage) | 否 | — |
| `tool_calls` | [ChatSessionToolCall](#schema-chatsessiontoolcall)[] | 否 | — |
| `blocks` | [ChatSessionBlock](#schema-chatsessionblock)[] | 否 | — |
| `interactive` | [ChatSessionInteractivePayload](#schema-chatsessioninteractivepayload) | 否 | — |
| `knowledge_hits` | [ChatSessionKnowledgeHit](#schema-chatsessionknowledgehit)[] | 否 | — |
| `memory_hits` | [ChatSessionMemoryHit](#schema-chatsessionmemoryhit)[] | 否 | — |
| `assistant_message_id` | string | 否 | — |
| `backend_message_id` | string | 否 | — |
| `message_id` | string | 否 | — |
| `sequence` | integer | 否 | minimum=0 |
| `reasoning_disclosure` | [ChatSessionReasoningDisclosure](#schema-chatsessionreasoningdisclosure) | 是 | — |
| `reasoning_receipt` | [ChatSessionReasoningReceipt](#schema-chatsessionreasoningreceipt) | 否 | — |
| `runtime_event` | [ChatSessionRuntimeEvent](#schema-chatsessionruntimeevent) | 否 | — |

<a id="schema-chatsessionsandboxexecution"></a>
### `ChatSessionSandboxExecution`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `run_id` | string | 是 | — |
| `status` | string | 是 | — |
| `language` | string | 否 | — |
| `command` | string[] | 否 | — |
| `exit_code` | integer | 是 | — |
| `timeout` | boolean | 是 | — |
| `error` | string | 否 | — |
| `artifacts` | [ChatSessionSandboxArtifact](#schema-chatsessionsandboxartifact)[] | 否 | — |

<a id="schema-chatsessionsandboxartifact"></a>
### `ChatSessionSandboxArtifact`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `id` | string | 是 | — |
| `name` | string | 是 | — |
| `size` | integer | 是 | — |
| `mime` | string | 否 | — |

<a id="schema-chatsessionretrievalactivity"></a>
### `ChatSessionRetrievalActivity`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `kind` | string | 是 | — |
| `status` | string | 是 | — |
| `source` | string | 否 | — |
| `knowledge_hits` | [ChatSessionKnowledgeHit](#schema-chatsessionknowledgehit)[] | 否 | — |
| `memory_hits` | [ChatSessionMemoryHit](#schema-chatsessionmemoryhit)[] | 否 | — |
| `resident_hits` | [ChatSessionMemoryHit](#schema-chatsessionmemoryhit)[] | 否 | — |

<a id="schema-chatsessiontoolorigin"></a>
### `ChatSessionToolOrigin`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `kind` | string | 是 | — |
| `name` | string | 是 | — |
| `server_name` | string | 否 | — |

<a id="schema-chatsessionreasoningdisclosure"></a>
### `ChatSessionReasoningDisclosure`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `visibility` | string | 是 | enum="visible", "not_exposed" |
| `source` | string | 是 | — |
| `dialect` | string | 是 | — |
| `provider` | string | 是 | — |
| `model` | string | 是 | — |

<a id="schema-chatsessionruntimeevent"></a>
### `ChatSessionRuntimeEvent`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `version` | integer | 是 | minimum=0 |
| `event_id` | string | 是 | — |
| `kind` | string | 是 | enum="tool_started", "tool_completed", "tool_failed", "terminal" |
| `tool_call_id` | string | 否 | — |
| `tool_name` | string | 否 | — |
| `terminal_status` | string | 否 | enum="completed", "failed", "cancelled" |

<a id="schema-chatsessionsequencedruntimeevent"></a>
### `ChatSessionSequencedRuntimeEvent`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `sequence` | integer | 是 | minimum=0 |
| `event` | [ChatSessionRuntimeEvent](#schema-chatsessionruntimeevent) | 是 | — |

<a id="schema-chatsessionreasoningreceipt"></a>
### `ChatSessionReasoningReceipt`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `version` | integer | 是 | enum=1 |
| `reasoning_request` | string | 是 | enum="on", "off" |
| `reasoning_support` | string | 是 | enum="supported", "unsupported", "unknown" |
| `reasoning_execution` | string | 是 | enum="applied", "ignored", "rejected", "unknown" |

<a id="schema-chatsessioninteractivepayload"></a>
### `ChatSessionInteractivePayload`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `type` | string | 是 | enum="buttons", "select", "approval", "card" |
| `prompt` | string | 否 | — |
| `buttons` | [ChatSessionInteractiveButton](#schema-chatsessioninteractivebutton)[] | 否 | — |
| `options` | [ChatSessionInteractiveOption](#schema-chatsessioninteractiveoption)[] | 否 | — |
| `approval` | [ChatSessionInteractiveApproval](#schema-chatsessioninteractiveapproval) | 否 | — |
| `card` | [ChatSessionInteractiveCard](#schema-chatsessioninteractivecard) | 否 | — |
| `resolved` | [ChatSessionInteractiveResolved](#schema-chatsessioninteractiveresolved) | 否 | — |

<a id="schema-chatsessioninteractivebutton"></a>
### `ChatSessionInteractiveButton`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `label` | string | 是 | — |
| `action` | string | 是 | — |
| `variant` | string | 否 | enum="primary", "secondary", "danger" |
| `payload` | string | 否 | — |

<a id="schema-chatsessioninteractiveoption"></a>
### `ChatSessionInteractiveOption`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `label` | string | 是 | — |
| `value` | string | 是 | — |
| `description` | string | 否 | — |

<a id="schema-chatsessioninteractiveapproval"></a>
### `ChatSessionInteractiveApproval`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `subject` | string | 是 | — |
| `summary` | string | 否 | — |
| `approve_label` | string | 否 | — |
| `reject_label` | string | 否 | — |
| `approve_action` | string | 否 | — |
| `reject_action` | string | 否 | — |

<a id="schema-chatsessioninteractivecard"></a>
### `ChatSessionInteractiveCard`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `title` | string | 是 | — |
| `fields` | [ChatSessionCardField](#schema-chatsessioncardfield)[] | 否 | — |
| `buttons` | [ChatSessionInteractiveButton](#schema-chatsessioninteractivebutton)[] | 否 | — |
| `image` | string | 否 | — |
| `footer` | string | 否 | — |

<a id="schema-chatsessioncardfield"></a>
### `ChatSessionCardField`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `label` | string | 是 | — |
| `value` | string | 是 | — |
| `short` | boolean | 否 | — |

<a id="schema-chatsessioninteractiveresolved"></a>
### `ChatSessionInteractiveResolved`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `action` | string | 是 | — |
| `label` | string | 否 | — |
| `value` | string | 否 | — |
| `approved` | boolean | 否 | — |
| `timestamp` | string | 否 | — |

<a id="schema-chatsessionmessagecontent"></a>
### `ChatSessionMessageContent`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `content_id` | string | 是 | — |
| `content_version` | string | 是 | — |
| `producer_kind` | string | 是 | enum="chat", "quick_chat", "k12", "skill", "tool", "rag", "report", "cron", "webhook", "workflow" |
| `markdown` | string | 是 | — |
| `source_digest` | string | 是 | — |
| `locale` | string | 是 | — |
| `attachments` | [ChatSessionAttachmentRef](#schema-chatsessionattachmentref)[] | 否 | — |

<a id="schema-chatsessionattachmentref"></a>
### `ChatSessionAttachmentRef`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `asset_id` | string | 是 | — |
| `name` | string | 否 | — |
| `mime` | string | 是 | — |
| `digest` | string | 是 | — |
| `alt_text` | string | 否 | — |

<a id="schema-chatsessionrendermanifest"></a>
### `ChatSessionRenderManifest`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `render_id` | string | 是 | — |
| `content_id` | string | 是 | — |
| `surface` | string | 是 | enum="desktop", "quick_chat", "history", "k12", "channel", "export" |
| `capability_snapshot` | [ChatSessionCapabilitySnapshot](#schema-chatsessioncapabilitysnapshot) | 是 | — |
| `renderer_version` | string | 是 | — |
| `source_digest` | string | 是 | — |
| `parts` | [ChatSessionRenderPart](#schema-chatsessionrenderpart)[] | 是 | — |
| `fallback_reason` | string | 否 | — |
| `receipt_ref` | string | 否 | — |

<a id="schema-chatsessioncapabilitysnapshot"></a>
### `ChatSessionCapabilitySnapshot`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `markdown` | boolean | 是 | — |
| `tex_math` | boolean | 是 | — |
| `mathml` | boolean | 否 | — |
| `unicode_math` | boolean | 否 | — |
| `attachments` | boolean | 否 | — |
| `max_runes` | integer | 否 | — |

<a id="schema-chatsessionrenderpart"></a>
### `ChatSessionRenderPart`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `kind` | string | 是 | enum="markdown", "text", "artifact" |
| `text` | string | 否 | — |
| `artifact_ref` | string | 否 | — |
| `artifact_digest` | string | 否 | — |
| `alt_text` | string | 否 | — |

<a id="schema-chatsessionsession"></a>
### `ChatSessionSession`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `id` | string | 是 | — |
| `user_id` | string | 是 | — |
| `platform` | string | 是 | — |
| `instance_id` | string | 是 | — |
| `chat_id` | string | 是 | — |
| `title` | string | 是 | — |
| `parent_session_id` | string | 是 | — |
| `branch_message_id` | string | 是 | — |
| `status` | integer | 是 | enum=1, 0, -1 |
| `message_count` | integer | 是 | — |
| `total_prompt_tokens` | integer | 是 | — |
| `total_completion_tokens` | integer | 是 | — |
| `last_message_preview` | string | 是 | — |
| `meta` | string | 是 | Additional stored JSON encoded as a string. |
| `created_at` | string | 是 | format=date-time |
| `updated_at` | string | 是 | format=date-time |

<a id="schema-chatsessionmessagerecord"></a>
### `ChatSessionMessageRecord`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `id` | string | 是 | — |
| `session_id` | string | 是 | — |
| `parent_id` | string | 是 | — |
| `role` | string | 是 | Stored role; may include checkpoint/tool as well as user/assistant/system. |
| `content` | string | 是 | — |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | 否 | — |
| `render_manifest` | [ChatSessionRenderManifest](#schema-chatsessionrendermanifest) | 否 | — |
| `content_type` | string | 是 | — |
| `metadata` | string | 是 | JSON encoded as a string, with attachments merged on reads; not a JSON object. |
| `feedback` | string | 是 | — |
| `model_name` | string | 是 | — |
| `prompt_tokens` | integer | 是 | — |
| `completion_tokens` | integer | 是 | — |
| `finish_reason` | string | 是 | — |
| `latency_ms` | integer | 是 | — |
| `request_id` | string | 是 | — |
| `meta` | string | 是 | Additional stored JSON encoded as a string. |
| `created_at` | string | 是 | format=date-time |
| `assistant_message_id` | string | 否 | — |
| `backend_message_id` | string | 否 | — |
| `message_id` | string | 否 | — |
| `reasoning_disclosure` | [ChatSessionReasoningDisclosure](#schema-chatsessionreasoningdisclosure) | 是 | — |
| `runtime_events` | [ChatSessionSequencedRuntimeEvent](#schema-chatsessionsequencedruntimeevent)[] / null | 是 | — |
| `last_sequence` | integer | 是 | minimum=0 |

<a id="schema-chatsessionsearchresult"></a>
### `ChatSessionSearchResult`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `message` | [ChatSessionMessageRecord](#schema-chatsessionmessagerecord) | 是 | — |
| `session_title` | string | 是 | — |
| `rank` | number | 是 | — |

<a id="schema-chatsessionsnapshot"></a>
### `ChatSessionSnapshot`

Owner-scoped process-local accumulated stream state; not a durable request ledger.

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `request_id` | string | 是 | — |
| `session_id` | string | 是 | — |
| `user_id` | string | 否 | — |
| `content` | string | 否 | — |
| `reasoning` | string | 否 | — |
| `done` | boolean | 是 | — |
| `status` | string | 是 | enum="pending", "streaming", "completed", "errored", "cancelled" |
| `metadata` | object | 否 | — |
| `usage` | [ChatSessionUsage](#schema-chatsessionusage) | 否 | — |
| `tool_calls` | [ChatSessionToolCall](#schema-chatsessiontoolcall)[] | 否 | — |
| `blocks` | [ChatSessionBlock](#schema-chatsessionblock)[] | 否 | — |
| `started_at` | string | 是 | format=date-time |
| `updated_at` | string | 是 | format=date-time |
| `assistant_message_id` | string | 否 | — |
| `backend_message_id` | string | 否 | — |
| `message_id` | string | 否 | — |
| `reasoning_disclosure` | [ChatSessionReasoningDisclosure](#schema-chatsessionreasoningdisclosure) | 是 | — |
| `runtime_events` | [ChatSessionSequencedRuntimeEvent](#schema-chatsessionsequencedruntimeevent)[] / null | 是 | — |
| `last_sequence` | integer | 是 | minimum=0 |

<a id="schema-chatsessioncheckpointdata"></a>
### `ChatSessionCheckpointData`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `session_id` | string | 是 | — |
| `agent_name` | string | 否 | — |
| `turn` | integer | 是 | — |
| `messages` | JSON | 是 | Raw JSON preserved by this API; no fixed nested validation is applied here. |
| `budget` | JSON | 否 | Raw JSON preserved by this API; no fixed nested validation is applied here. |

<a id="schema-chatsessionmemoryentry"></a>
### `ChatSessionMemoryEntry`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `profile_digest` | string | 否 | — |
| `profile_revision` | string | 否 | — |
| `manual_correction` | boolean | 否 | — |
| `id` | string | 是 | — |
| `content` | string | 是 | — |
| `type` | string | 是 | Common values: identity/preference/fact/instruction/context/rule; this API does not enforce an enum. |
| `source` | string | 是 | Examples: manual/chat_explicit/chat_extract/system/reflect_profile; this API does not enforce an enum. |
| `created_at` | string | 是 | — |
| `updated_at` | string | 是 | — |
| `hit_count` | integer | 是 | — |
| `status` | string | 是 | enum="active", "archived" |
| `archived_at` | string | 否 | — |
| `pinned` | boolean | 否 | — |
| `subject` | string | 否 | — |
| `valid_from` | string | 否 | — |
| `valid_to` | string | 否 | — |
| `supersedes` | string | 否 | — |
| `confidence` | number | 否 | — |

<a id="schema-chatsessionmemorycapacity"></a>
### `ChatSessionMemoryCapacity`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `used` | integer | 是 | — |
| `max` | integer | 是 | — |
| `archived` | integer | 否 | — |

<a id="schema-chatsessionattachmentreceipt"></a>
### `ChatSessionAttachmentReceipt`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `attachment_id` | string | 是 | — |
| `digest` | string | 是 | — |
| `size` | integer | 是 | — |
| `media_type` | string | 是 | — |
| `display_name` | string | 是 | — |
| `expires_at` | string | 是 | format=date-time |

<a id="schema-chatsessionchatresponse"></a>
### `ChatSessionChatResponse`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `reply` | string | 是 | — |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | 否 | — |
| `render_manifest` | [ChatSessionRenderManifest](#schema-chatsessionrendermanifest) | 否 | — |
| `session_id` | string | 是 | — |
| `metadata` | object | 否 | — |
| `usage` | [ChatSessionUsage](#schema-chatsessionusage) | 否 | — |
| `tool_calls` | [ChatSessionToolCall](#schema-chatsessiontoolcall)[] | 否 | — |
| `blocks` | [ChatSessionBlock](#schema-chatsessionblock)[] | 否 | — |
| `knowledge_hits` | [ChatSessionKnowledgeHit](#schema-chatsessionknowledgehit)[] | 否 | — |
| `memory_hits` | [ChatSessionMemoryHit](#schema-chatsessionmemoryhit)[] | 否 | — |
| `assistant_message_id` | string | 否 | — |
| `backend_message_id` | string | 否 | — |
| `message_id` | string | 否 | — |
| `last_sequence` | integer | 否 | minimum=0 |
| `reasoning_disclosure` | [ChatSessionReasoningDisclosure](#schema-chatsessionreasoningdisclosure) | 是 | — |
| `runtime_events` | [ChatSessionSequencedRuntimeEvent](#schema-chatsessionsequencedruntimeevent)[] | 否 | — |

<a id="schema-chatsessionchatrequest"></a>
### `ChatSessionChatRequest`

At least nonblank message or valid attachments; JSON body maximum 20 MiB. user_id/platform are compatibility fields and cannot change the authenticated principal.

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `message` | string | 否 | — |
| `session_id` | string | 否 | Omitted/empty starts a session; reuse the returned ID to continue. |
| `user_id` | string | 否 | compatibility field |
| `role` | string | 否 | Optional Agent role; not a hardcoded enum at this API handler. |
| `provider` | string | 否 | Optional explicit currently configured provider; validated before processing. |
| `model` | string | 否 | Optional explicit completion model present in the active provider configuration. |
| `platform` | string | 否 | compatibility field |
| `attachments` | [ChatSessionAttachment](#schema-chatsessionattachment)[] | 否 | maxItems=20 |
| `metadata` | object | 否 | String map. Reserved internal dispatch/sampling keys are stripped. Explicit role/provider/model fields override corresponding metadata. |
| `request_id` | string | 否 | Correlation and recovery identity, not a durable admission/idempotency guarantee. |
| `temperature` | number / null | 否 | minimum=0; maximum=2; Omitted/null follows the Agent/model; zero remains an explicit value. |
| `max_tokens` | integer / null | 否 | minimum=1; maximum=1000000; Omitted/null follows the Agent/model. |

<a id="schema-chatsessioncreatesessionrequest"></a>
### `ChatSessionCreateSessionRequest`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `id` | string | 是 | minLength=1; Client-supplied session ID; no whitespace normalization at this handler. |
| `title` | string | 否 | Optional; create does not apply the rename nonblank/200-rune validation. |
| `user_id` | string | 否 | compatibility field |

<a id="schema-chatsessionupdatesessionrequest"></a>
### `ChatSessionUpdateSessionRequest`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `title` | string | 是 | Trimmed, nonblank; changed titles are limited to 200 Unicode code points. An unchanged historical title is preserved. |

<a id="schema-chatsessionsuggesttitlerequest"></a>
### `ChatSessionSuggestTitleRequest`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `expected_title` | string | 否 | — |

<a id="schema-chatsessionfeedbackrequest"></a>
### `ChatSessionFeedbackRequest`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `feedback` | string | 否 | default=""; enum="", "like", "dislike"; Empty or omitted clears feedback. |

<a id="schema-chatsessionappendmessagerequest"></a>
### `ChatSessionAppendMessageRequest`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `id` | string | 否 | Trimmed client ID; blank generates msg-*. Duplicate IDs are not a guaranteed idempotent replay and may fail with 500. |
| `role` | string | 是 | enum="user", "assistant", "system" |
| `content` | string | 否 | — |
| `content_type` | string | 否 | When omitted: multimodal_json if metadata is present, otherwise text. Nonempty values are not enum-validated. |
| `metadata` | JSON | 否 | Raw JSON stored as a JSON string. Attachments in an object envelope retain the complete envelope on reads; no fixed metadata schema is enforced. |
| `model_name` | string | 否 | — |
| `prompt_tokens` | integer | 否 | — |
| `completion_tokens` | integer | 否 | — |
| `finish_reason` | string | 否 | — |
| `parent_id` | string | 否 | — |
| `request_id` | string | 否 | — |

<a id="schema-chatsessionforksessionrequest"></a>
### `ChatSessionForkSessionRequest`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `message_id` | string | 是 | minLength=1 |
| `user_id` | string | 否 | compatibility field |
| `include_message` | boolean / null | 否 | default=true; Omitted/null includes message_id; false copies the prefix before it. |

<a id="schema-chatsessionsavememoryrequest"></a>
### `ChatSessionSaveMemoryRequest`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `content` | string | 是 | minLength=1; Nonempty at HTTP handler; FileMemory trims it. Supply nonblank text to create an entry. |
| `type` | string | 否 | default="fact"; Common values identity/preference/fact/instruction/context/rule; no enum validation. |
| `source` | string | 否 | default="manual"; Common values manual/chat_explicit/chat_extract/system; no enum validation. |

<a id="schema-chatsessionattachment"></a>
### `ChatSessionAttachment`

object / JSON / JSON

Opaque staged reference. Do not send type/name/mime/data/url alongside attachment_id.

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `attachment_id` | string | 是 | minLength=1 |

Legacy image input. type must equal image case-insensitively OR mime must begin with image/. Provide exactly one of data/url.

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `type` | string | 否 | — |
| `name` | string | 否 | — |
| `mime` | string | 否 | — |
| `data` | string | 否 | minLength=1; Base64 content |
| `url` | string | 否 | minLength=1; Image URL |

<a id="schema-chatsessionmessagetext"></a>
### `ChatSessionMessageText`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `message` | string | 是 | — |

<a id="schema-chatsessionerror"></a>
### `ChatSessionError`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `error` | string | 是 | — |
| `code` | string | 否 | — |
| `layer` | string | 否 | — |
| `retryable` | boolean | 否 | — |
| `done` | boolean | 否 | — |

<a id="schema-chatsessionsseerror"></a>
### `ChatSessionSSEError`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `error` | string | 是 | — |
| `done` | boolean | 是 | enum=true |
| `code` | string | 否 | — |
| `retryable` | boolean | 否 | — |
| `assistant_message_id` | string | 否 | — |
| `backend_message_id` | string | 否 | — |
| `message_id` | string | 否 | — |
| `sequence` | integer | 否 | minimum=0 |
| `reasoning_disclosure` | [ChatSessionReasoningDisclosure](#schema-chatsessionreasoningdisclosure) | 否 | — |
| `runtime_event` | [ChatSessionRuntimeEvent](#schema-chatsessionruntimeevent) / null | 否 | — |

<a id="schema-chatsessionsessioncreated"></a>
### `ChatSessionSessionCreated`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `id` | string | 是 | — |
| `title` | string | 是 | — |
| `created_at` | string | 是 | format=date-time |

<a id="schema-chatsessionsessionupdated"></a>
### `ChatSessionSessionUpdated`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `id` | string | 是 | — |
| `title` | string | 是 | — |
| `updated_at` | string | 是 | format=date-time |

<a id="schema-chatsessiontitlesuggested"></a>
### `ChatSessionTitleSuggested`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `id` | string | 是 | — |
| `title` | string | 是 | — |
| `updated` | boolean | 是 | — |
| `updated_at` | string | 是 | format=date-time |

<a id="schema-chatsessionsessionlist"></a>
### `ChatSessionSessionList`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `sessions` | [ChatSessionSession](#schema-chatsessionsession)[] | 是 | — |
| `total` | integer | 是 | len(page) + offset; not the total row count |
| `has_more` | boolean | 是 | True when len(page) == limit; may be true on the final full page. |

<a id="schema-chatsessionmessagelist"></a>
### `ChatSessionMessageList`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `messages` | [ChatSessionMessageRecord](#schema-chatsessionmessagerecord)[] | 是 | — |
| `total` | integer | 是 | CountMessages when available, otherwise len(page) + offset |

<a id="schema-chatsessionsearchmessagelist"></a>
### `ChatSessionSearchMessageList`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `results` | [ChatSessionSearchResult](#schema-chatsessionsearchresult)[] | 是 | — |
| `total` | integer | 是 | — |
| `query` | string | 是 | — |

<a id="schema-chatsessionbranchlist"></a>
### `ChatSessionBranchList`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `branches` | [ChatSessionSession](#schema-chatsessionsession)[] | 是 | — |
| `total` | integer | 是 | — |

<a id="schema-chatsessionforkresult"></a>
### `ChatSessionForkResult`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `session` | [ChatSessionSession](#schema-chatsessionsession) | 是 | — |
| `message` | string | 是 | — |

<a id="schema-chatsessionmessagecreated"></a>
### `ChatSessionMessageCreated`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `id` | string | 是 | — |
| `session_id` | string | 是 | — |

<a id="schema-chatsessionbatchmessagecreated"></a>
### `ChatSessionBatchMessageCreated`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `ids` | string[] | 是 | — |
| `session_id` | string | 是 | — |

<a id="schema-chatsessionbatchappendrequest"></a>
### `ChatSessionBatchAppendRequest`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `messages` | [ChatSessionAppendMessageRequest](#schema-chatsessionappendmessagerequest)[] | 是 | minItems=1; maxItems=50 |

<a id="schema-chatsessionstreamlist"></a>
### `ChatSessionStreamList`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `streams` | [ChatSessionSnapshot](#schema-chatsessionsnapshot)[] | 是 | — |
| `total` | integer | 是 | — |

<a id="schema-chatsessioncheckpointlist"></a>
### `ChatSessionCheckpointList`

The mounted handler calls Load and returns at most one latest checkpoint; load errors are represented by an empty array.

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `checkpoints` | [ChatSessionCheckpointData](#schema-chatsessioncheckpointdata)[] | 是 | maxItems=1 |

<a id="schema-chatsessionmemorylist"></a>
### `ChatSessionMemoryList`

With FileMemory enabled, total/next_cursor/has_more are also always emitted. Disabled: entries=[], summary="", capacity={used:0,max:0}.

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `entries` | [ChatSessionMemoryEntry](#schema-chatsessionmemoryentry)[] | 是 | — |
| `summary` | string | 是 | — |
| `capacity` | [ChatSessionMemoryCapacity](#schema-chatsessionmemorycapacity) | 是 | — |
| `total` | integer | 否 | — |
| `next_cursor` | string | 否 | — |
| `has_more` | boolean | 否 | — |

<a id="schema-chatsessionmemorysearchresult"></a>
### `ChatSessionMemorySearchResult`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `file` | string | 是 | — |
| `line` | integer | 是 | — |
| `content` | string | 是 | — |
| `score` | number | 是 | — |

<a id="schema-chatsessionvectormemorysearchresult"></a>
### `ChatSessionVectorMemorySearchResult`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `content` | string | 是 | — |
| `score` | number | 是 | — |
| `source` | string | 是 | enum="vector" |

<a id="schema-chatsessionmemorysearch"></a>
### `ChatSessionMemorySearch`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `results` | [ChatSessionMemorySearchResult](#schema-chatsessionmemorysearchresult)[] / null | 是 | — |
| `vector_results` | [ChatSessionVectorMemorySearchResult](#schema-chatsessionvectormemorysearchresult)[] / null | 是 | — |
| `total` | integer | 是 | — |

<a id="schema-chatsessionupdatememoryrequest"></a>
### `ChatSessionUpdateMemoryRequest`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `content` | string | 否 | Whole active MEMORY.md replacement without id; a single entry update with id. |

<a id="schema-chatsessionupdatememoryentryrequest"></a>
### `ChatSessionUpdateMemoryEntryRequest`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `content` | string | 是 | minLength=1; New entry text; empty/omitted is rejected. |

<a id="schema-chatsessioneditprofilerequest"></a>
### `ChatSessionEditProfileRequest`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `revision` | string | 是 | minLength=1; Current profile_revision from GET /api/v1/memory |
| `content` | string | 是 | minLength=1; Nonblank revised profile; applied to the profile and reliably matched source memories. |

<a id="schema-chatsessionprofilerefreshresult"></a>
### `ChatSessionProfileRefreshResult`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `action` | string | 是 | enum="insert", "update", "skip" |

<a id="schema-chatsessionwebsocketframe"></a>
### `ChatSessionWebSocketFrame`

Union transport envelope; required fields depend on frame type. See x-websocket-client-frames and x-websocket-server-frames on GET /ws.

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `type` | string | 是 | enum="message", "reply", "chunk", "error", "ping", "pong", "cancel", "resume", "stream_snapshot", "tool_approval_request", "tool_approval_response", "tool_permission_response", "tool_approval_ack", "tool_approval_terminal", "tool_approval_reconcile" |
| `content` | string | 是 | — |
| `code` | string | 否 | — |
| `retryable` | boolean | 否 | — |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | 否 | — |
| `render_manifest` | [ChatSessionRenderManifest](#schema-chatsessionrendermanifest) | 否 | — |
| `reasoning` | string | 否 | — |
| `session_id` | string | 否 | — |
| `request_id` | string | 否 | — |
| `decision_id` | string | 否 | — |
| `decision` | string | 否 | enum="approved_once", "approved_remember", "denied" |
| `idempotency_key` | string | 否 | — |
| `status` | string | 否 | Approval ACK status: accepted/already_accepted/expired/rejected; not a stream status in stream_snapshot frames. |
| `owner_id` | string | 否 | — |
| `tool_name` | string | 否 | — |
| `user_id` | string | 否 | compatibility field |
| `provider` | string | 否 | — |
| `model` | string | 否 | — |
| `role` | string | 否 | — |
| `temperature` | number / null | 否 | minimum=0; maximum=2; Omitted/null follows the Agent/model; zero remains an explicit value. |
| `max_tokens` | integer / null | 否 | minimum=1; maximum=1000000; Omitted/null follows the Agent/model. |
| `done` | boolean | 否 | — |
| `metadata` | object | 否 | — |
| `arguments` | object | 否 | Free-form JSON fields defined by the selected tool or producer. |
| `invocation_id` | string | 否 | — |
| `arguments_digest` | string | 否 | — |
| `security_scope_digest` | string | 否 | — |
| `scope_schema_version` | integer | 否 | minimum=1 |
| `deadline_at` | string | 否 | format=date-time |
| `terminal_result` | string | 否 | — |
| `usage` | [ChatSessionUsage](#schema-chatsessionusage) | 否 | — |
| `tool_calls` | [ChatSessionToolCall](#schema-chatsessiontoolcall)[] | 否 | — |
| `blocks` | [ChatSessionBlock](#schema-chatsessionblock)[] | 否 | — |
| `attachments` | [ChatSessionAttachment](#schema-chatsessionattachment)[] | 否 | — |
| `knowledge_hits` | [ChatSessionKnowledgeHit](#schema-chatsessionknowledgehit)[] | 否 | — |
| `memory_hits` | [ChatSessionMemoryHit](#schema-chatsessionmemoryhit)[] | 否 | — |
| `assistant_message_id` | string | 否 | — |
| `backend_message_id` | string | 否 | — |
| `message_id` | string | 否 | — |
| `sequence` | integer | 否 | minimum=0 |
| `reasoning_disclosure` | [ChatSessionReasoningDisclosure](#schema-chatsessionreasoningdisclosure) | 是 | — |
| `reasoning_receipt` | [ChatSessionReasoningReceipt](#schema-chatsessionreasoningreceipt) | 否 | — |
| `runtime_event` | [ChatSessionRuntimeEvent](#schema-chatsessionruntimeevent) | 否 | — |
| `runtime_events` | [ChatSessionSequencedRuntimeEvent](#schema-chatsessionsequencedruntimeevent)[] | 否 | — |
| `last_sequence` | integer | 否 | minimum=0 |

<a id="schema-chatsessionwebsocketclientmessage"></a>
### `ChatSessionWebSocketClientMessage`

At least nonblank content or valid attachments. Empty request_id generates req-*; an existing request_id is not a deduplication guarantee.

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `type` | string | 是 | enum="message" |
| `content` | string | 否 | — |
| `session_id` | string | 否 | — |
| `request_id` | string | 否 | — |
| `provider` | string | 否 | — |
| `model` | string | 否 | — |
| `role` | string | 否 | — |
| `temperature` | number / null | 否 | minimum=0; maximum=2; Omitted/null follows the Agent/model; zero remains an explicit value. |
| `max_tokens` | integer / null | 否 | minimum=1; maximum=1000000; Omitted/null follows the Agent/model. |
| `metadata` | object | 否 | String map. Reserved internal dispatch/sampling keys are stripped. Explicit role/provider/model fields override corresponding metadata. |
| `attachments` | [ChatSessionAttachment](#schema-chatsessionattachment)[] | 否 | maxItems=20 |
| `user_id` | string | 否 | compatibility field; Ignored for authenticated identity |

<a id="schema-chatsessionwebsocketresume"></a>
### `ChatSessionWebSocketResume`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `type` | string | 是 | enum="resume" |
| `request_id` | string | 是 | minLength=1 |

<a id="schema-chatsessionwebsocketcancel"></a>
### `ChatSessionWebSocketCancel`

Use request_id, or the current owned session_id binding when request_id is empty. No cancellation ACK is emitted.

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `type` | string | 是 | enum="cancel" |
| `request_id` | string | 否 | — |
| `session_id` | string | 否 | — |

<a id="schema-chatsessionwebsocketping"></a>
### `ChatSessionWebSocketPing`

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `type` | string | 是 | enum="ping" |

<a id="schema-chatsessionwebsocketapprovalreconcile"></a>
### `ChatSessionWebSocketApprovalReconcile`

Identity fields can alternatively be supplied in metadata; simultaneous top-level/metadata values must match. Metadata approval_request_id/request_id aliases are accepted.

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `type` | string | 是 | enum="tool_approval_reconcile" |
| `request_id` | string | 是 | — |
| `owner_id` | string | 是 | — |
| `session_id` | string | 是 | — |
| `invocation_id` | string | 是 | — |
| `arguments_digest` | string | 是 | — |
| `security_scope_digest` | string | 是 | — |
| `scope_schema_version` | integer | 是 | minimum=1 |
| `deadline_at` | string | 是 | format=date-time |

<a id="schema-chatsessionwebsocketapprovaldecision"></a>
### `ChatSessionWebSocketApprovalDecision`

Repeat the complete identity from the original approval request. owner_id must match the authenticated principal; no legacy approved/remember-only decision is accepted.

| 字段 | 类型 | 必填 | 规则 / 缺省 |
| --- | --- | --- | --- |
| `type` | string | 是 | enum="tool_approval_response", "tool_permission_response" |
| `request_id` | string | 是 | — |
| `owner_id` | string | 是 | — |
| `session_id` | string | 是 | — |
| `invocation_id` | string | 是 | — |
| `arguments_digest` | string | 是 | — |
| `security_scope_digest` | string | 是 | — |
| `scope_schema_version` | integer | 是 | minimum=1 |
| `deadline_at` | string | 是 | format=date-time |
| `decision_id` | string | 是 | minLength=1 |
| `decision` | string | 是 | enum="approved_once", "approved_remember", "denied" |
| `idempotency_key` | string | 是 | minLength=1 |
