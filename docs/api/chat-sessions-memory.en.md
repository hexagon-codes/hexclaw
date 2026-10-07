# Chat, sessions, streams and memory API

[中文](chat-sessions-memory.md) · [Public API overview](../api.en.md) · [OpenAPI](../../api/openapi.yaml)

This reference covers 33 method/path combinations in the current source. Released packages and tags follow their own versions. Every endpoint requires the current service Bearer token. Body/query user_id is compatibility-only and cannot change identity. An unmounted module normally returns a plain HTTP 404, rather than a structured handler error.

## Common request rules

All endpoints: Authorization: Bearer $HEXCLAW_API_TOKEN. Authentication failure is 401 {"error":"Valid access token required"}. JSON bodies use Content-Type: application/json; attachment staging uses multipart. Unlisted query/header fields are not part of the functional contract; client body fields do not grant identity. Time objects use RFC 3339; MemoryEntry timestamps are stored strings and need not be full timestamps.

## Endpoint index

| Method | Path | Purpose | Availability |
| --- | --- | --- | --- |
| `POST` | `/api/v1/chat` | [Start or continue a chat](#operation-chatsessionchat) | Always mounted |
| `POST` | `/api/v1/attachments` | [Stage an image attachment](#operation-chatsessionstageattachment) | Always mounted |
| `POST` | `/api/v1/sessions` | [Create a session](#operation-chatsessioncreatesession) | Mounted only when s.store != nil |
| `GET` | `/api/v1/sessions` | [List sessions](#operation-chatsessionlistsessions) | Mounted only when s.store != nil |
| `GET` | `/api/v1/sessions/{id}` | [Get a session](#operation-chatsessiongetsession) | Mounted only when s.store != nil |
| `PATCH` | `/api/v1/sessions/{id}` | [Rename a session](#operation-chatsessionupdatesession) | Mounted only when s.store != nil |
| `POST` | `/api/v1/sessions/{id}/suggest-title` | [Suggest a session title](#operation-chatsessionsuggestsessiontitle) | Mounted only when s.store != nil |
| `DELETE` | `/api/v1/sessions/{id}` | [Delete a session](#operation-chatsessiondeletesession) | Mounted only when s.store != nil |
| `GET` | `/api/v1/sessions/{id}/messages` | [List message history](#operation-chatsessionlistmessages) | Mounted only when s.store != nil |
| `POST` | `/api/v1/sessions/{id}/messages` | [Append a stored message](#operation-chatsessionappendmessage) | Mounted only when s.store != nil |
| `POST` | `/api/v1/sessions/{id}/messages/batch` | [Append stored messages atomically](#operation-chatsessionbatchappendmessages) | Mounted only when s.store != nil |
| `GET` | `/api/v1/sessions/{id}/branches` | [List session branches](#operation-chatsessionlistbranches) | Mounted only when s.store != nil |
| `POST` | `/api/v1/sessions/{id}/fork` | [Fork a conversation](#operation-chatsessionforksession) | Mounted only when s.store != nil |
| `GET` | `/api/v1/messages/search` | [Search message contents](#operation-chatsessionsearchmessages) | Mounted only when s.store != nil |
| `DELETE` | `/api/v1/messages/{id}` | [Delete a stored message](#operation-chatsessiondeletemessage) | Mounted only when s.store != nil |
| `PUT` | `/api/v1/messages/{id}/feedback` | [Update message feedback](#operation-chatsessionupdatemessagefeedback) | Mounted only when s.store != nil |
| `GET` | `/api/v1/streams/active` | [List active streams](#operation-chatsessionlistactivestreams) | Mounted only when s.streamStates != nil |
| `GET` | `/api/v1/streams/{request_id}` | [Get a stream snapshot](#operation-chatsessiongetstreamsnapshot) | Mounted only when s.streamStates != nil |
| `GET` | `/api/v1/memory` | [List file memory](#operation-chatsessiongetmemory) | Always mounted; FileMemory-enabled handler or disabled empty response |
| `POST` | `/api/v1/memory` | [Create a memory entry](#operation-chatsessionsavememory) | Mounted only when s.fileMem != nil |
| `PUT` | `/api/v1/memory` | [Replace the active memory file](#operation-chatsessionupdatememorywhole) | Mounted only when s.fileMem != nil |
| `PUT` | `/api/v1/memory/{id}` | [Update one memory entry](#operation-chatsessionupdatememory) | Mounted only when s.fileMem != nil |
| `POST` | `/api/v1/memory/{id}/archive` | [Archive a memory entry](#operation-chatsessionarchivememoryitem) | Mounted only when s.fileMem != nil |
| `POST` | `/api/v1/memory/{id}/restore` | [Restore an archived memory entry](#operation-chatsessionrestorememoryitem) | Mounted only when s.fileMem != nil |
| `POST` | `/api/v1/memory/{id}/pin` | [Pin a memory entry](#operation-chatsessionpinmemoryitem) | Mounted only when s.fileMem != nil |
| `POST` | `/api/v1/memory/{id}/unpin` | [Unpin a memory entry](#operation-chatsessionunpinmemoryitem) | Mounted only when s.fileMem != nil |
| `DELETE` | `/api/v1/memory/{id}` | [Delete one memory entry](#operation-chatsessiondeletememoryitem) | Mounted only when s.fileMem != nil |
| `DELETE` | `/api/v1/memory` | [Clear file memory](#operation-chatsessiondeletememory) | Always mounted; FileMemory-enabled handler or disabled no-op response |
| `GET` | `/api/v1/memory/search` | [Search memory](#operation-chatsessionsearchmemory) | Mounted only when s.fileMem != nil |
| `POST` | `/api/v1/memory/profile/refresh` | [Refresh the memory profile](#operation-chatsessionrefreshmemoryprofile) | Mounted only when s.fileMem != nil |
| `PUT` | `/api/v1/memory/profile` | [Edit the profile and source memories](#operation-chatsessioneditmemoryprofile) | Mounted only when s.fileMem != nil |
| `GET` | `/api/v1/sessions/{id}/checkpoints` | [Get the latest retained checkpoint](#operation-chatsessionlistcheckpoints) | Mounted only when s.checkpointMgr != nil |
| `GET` | `/ws` | [Upgrade to the WebAdapter WebSocket](#operation-chatsessionws) | Mounted only when s.wsHandler != nil; frames here describe adapter/web.WebAdapter |

## Copyable examples

Use `HEXCLAW_API_BASE` / `HEXCLAW_API_TOKEN` from [Connection and identity](../api.en.md#connection-and-identity). All IDs, revisions and timestamps below are illustrative; use real values returned by your instance. No business requests were run as part of authoring this reference.

### Sessions and atomic message writes

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/sessions" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"id":"session-api-demo","title":"API example"}'
```

Example `201`:

```json
{"id":"session-api-demo","title":"API example","created_at":"2026-10-07T08:00:00Z"}
```

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/sessions/session-api-demo/messages/batch" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"messages":[{"id":"message-api-demo-user","role":"user","content":"Hello"},{"id":"message-api-demo-assistant","role":"assistant","content":"Hello!"}]}'
```

Example `201`; this stores content already obtained by the caller, without generating a model response:

```json
{"ids":["message-api-demo-user","message-api-demo-assistant"],"session_id":"session-api-demo"}
```

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/sessions/session-api-demo/messages?limit=50&offset=0" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN"
```

### Stage an image before chatting

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/attachments" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Idempotency-Key: attachment-demo-1" \
  -F 'file=@./example.png;type=image/png'
```

Example `201`:

```json
{"attachment_id":"att_v1_0123456789abcdef0123456789abcdef","digest":"sha256:example","size":1024,"media_type":"image/png","display_name":"example.png","expires_at":"2026-10-07T08:15:00Z"}
```

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/chat" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message":"Describe this image","attachments":[{"attachment_id":"REPLACE_WITH_RETURNED_ATTACHMENT_ID"}]}'
```

A reference object sends only attachment_id, with no filename, MIME or raw content. Expired, other-owner and invalid references cannot be used. Complete chat/SSE examples are in [Chat](../api.en.md#chat).

### Read and write memory

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/memory?view=active&limit=50" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN"
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/memory" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"content":"I prefer concise answers.","type":"preference","source":"manual"}'
```

Example enabled-module empty list:

```json
{"entries":[],"summary":"","capacity":{"used":0,"max":100},"total":0,"next_cursor":"","has_more":false}
```

capacity.max comes from instance configuration and need not equal the sample. Example creation response:

```json
{"id":"e0123456789abcdef","content":"I prefer concise answers.","type":"preference","source":"manual","created_at":"2026-10-07","updated_at":"2026-10-07","hit_count":0,"status":"active"}
```

### SSE terminal frames and execution identity

Illustrative success frames; actual frames may contain the response fields documented below:

```text
data: {"content":"Hello","done":false,"sequence":1,"reasoning_disclosure":{"visibility":"not_exposed","source":"","dialect":"","provider":"","model":""}}

data: {"content":"","done":true,"sequence":2,"reasoning_disclosure":{"visibility":"not_exposed","source":"","dialect":"","provider":"","model":""}}

data: [DONE]

```

Example error after the stream is open:

```text
data: {"error":"Upstream unavailable","done":true,"code":"UPSTREAM_UNAVAILABLE","retryable":true}

```

Error paths do not append [DONE]. Append incremental content in order; terminal message_content contains the complete canonical Markdown/TeX and must not be appended as another delta. sequence/message IDs/runtime_event correlate actual execution. reasoning_disclosure.visibility=not_exposed does not prove absence of model reasoning. reasoning_receipt contains exactly version/reasoning_request/reasoning_support/reasoning_execution, without internal evidence.

### WebSocket handshake and frames

Use a client able to set Authorization on the HTTP Upgrade request. This Python example requires an installed websockets package and initiates one real chat request; set the current instance token first.

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

Incoming control frames: each line is a separate JSON message, not a combined request body.

```jsonl
{"type":"ping"}
{"type":"resume","request_id":"ORIGINAL_REQUEST_ID"}
{"type":"cancel","request_id":"ORIGINAL_REQUEST_ID"}
```

| Frame | Required fields / behavior |
| --- | --- |
| message | Nonblank content or valid attachments; optional session_id/request_id/provider/model/role/temperature/max_tokens/metadata. A blank request_id is generated; user_id cannot change owner |
| ping | Returns pong; an application-level JSON heartbeat |
| resume | request_id; returns an accumulated stream_snapshot, then subscribes to original future chunks if still active. Does not resend message |
| cancel | request_id, or an owned session_id binding when request_id is empty. No dedicated cancellation ACK; inspect a retained snapshot |
| reply | Complete synchronous content/message_content and optional usage/tool_calls/blocks/retrieval hits |
| chunk | Incremental content/reasoning; done=true is terminal. May carry message IDs, sequence, runtime_event and the public reasoning receipt |
| error | Public error in content, with optional code/retryable/session_id/request_id. Successful upgrade or a received chunk is not terminal success |
| stream_snapshot | Accumulated content/reasoning, done, sequence/last_sequence and runtime_events; does not contain the REST snapshot status/started_at/updated_at fields |

Unknown frame types and message frames without valid input are currently ignored. Each message operation has a ten-minute processing deadline. When all subscribers disconnect, the default reconnect grace is five seconds before cancellation. The default registry retains terminal snapshots for two minutes. These are process-local recovery semantics, not durable admission guarantees. A snapshot replaces the local accumulated projection; use sequence to deduplicate subsequent events.

### WebSocket tool approvals

Approvals use the original WebSocket; there is no parallel REST decision endpoint. Preserve request_id, owner_id, session_id, invocation_id, arguments_digest, security_scope_digest, scope_schema_version and deadline_at from tool_approval_request. It may also carry tool_name, arguments, content and risk metadata.

Respond with tool_approval_response (tool_permission_response is the compatible alias), the complete original identity, a nonempty decision_id/idempotency_key, and decision=approved_once/approved_remember/denied. Simultaneous top-level and metadata identities must agree; metadata accepts approval_request_id/request_id aliases. owner_id must match the authenticated principal. Legacy approved/remember booleans alone are not a valid decision.

```json
{"type":"tool_approval_response","request_id":"ORIGINAL_APPROVAL_ID","owner_id":"ORIGINAL_OWNER_ID","session_id":"ORIGINAL_SESSION_ID","invocation_id":"ORIGINAL_INVOCATION_ID","arguments_digest":"ORIGINAL_ARGUMENTS_DIGEST","security_scope_digest":"ORIGINAL_SCOPE_DIGEST","scope_schema_version":1,"deadline_at":"ORIGINAL_DEADLINE_RFC3339","decision_id":"decision-example-1","idempotency_key":"approval-example-1","decision":"denied"}
```

Copy all original identity fields from the active request. tool_approval_ack echoes identity/decision with status=accepted/already_accepted/expired/rejected; the terminal result is in metadata.terminal_result. After reconnecting, tool_approval_reconcile sends only the complete original identity, without a new decision; the response is the original pending request, an existing durable ACK, or tool_approval_terminal (expired/fenced). An approval ACK confirms acceptance of a decision, not tool execution or artifact delivery.


## Endpoint contracts

<a id="operation-chatsessionchat"></a>
### `POST /api/v1/chat`

Start or continue a chat

Authentication: Bearer. Availability: Always mounted.

Returns complete JSON by default, or SSE when Accept contains text/event-stream. Processing may create/continue a session, persist messages, consume model quota and invoke tools. request_id does not make unknown outcomes safe to replay.

Request body maximum: 20971520 bytes.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| header | `Accept` | No | Use text/event-stream for SSE; otherwise complete JSON — |

JSON body (nested structures are documented under models):

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `message` | string | No | — |
| `session_id` | string | No | Omitted/empty starts a session; reuse the returned ID to continue. |
| `user_id` | string | No | compatibility field |
| `role` | string | No | Optional Agent role; not a hardcoded enum at this API handler. |
| `provider` | string | No | Optional explicit currently configured provider; validated before processing. |
| `model` | string | No | Optional explicit completion model present in the active provider configuration. |
| `platform` | string | No | compatibility field |
| `attachments` | [ChatSessionAttachment](#schema-chatsessionattachment)[] | No | maxItems=20 |
| `metadata` | object | No | String map. Reserved internal dispatch/sampling keys are stripped. Explicit role/provider/model fields override corresponding metadata. |
| `request_id` | string | No | Correlation and recovery identity, not a durable admission/idempotency guarantee. |
| `temperature` | number / null | No | minimum=0; maximum=2; Omitted/null follows the Agent/model; zero remains an explicit value. |
| `max_tokens` | integer / null | No | minimum=1; maximum=1000000; Omitted/null follows the Agent/model. |

Success: `200` [ChatSessionChatResponse](#schema-chatsessionchatresponse).

Errors: 401 authentication failure; 400: Invalid JSON, input, attachment, provider/model or sampling fields; 403: Gateway policy rejection; code/layer may be present; 404: Attachment reference is unavailable, conflicting or over the count limit during resolution; 422: MODEL_CAPABILITY_MISMATCH; 429: UPSTREAM_RATE_LIMITED; 500: Unclassified engine/application error or streaming unavailable; 503: Engine not ready, UPSTREAM_UNAVAILABLE or UPSTREAM_POOL_EXHAUSTED

Source: [api/server.go](../../api/server.go) — `handleChat`.

<a id="operation-chatsessionstageattachment"></a>
### `POST /api/v1/attachments`

Stage an image attachment

Authentication: Bearer. Availability: Always mounted.

Exactly one multipart part named file with a filename; no extra fields. Maximum file size 200 MiB; detected content must be image data and any claimed MIME must match. Owner-bound, process-local staging lasts 15 minutes. 201 creates a receipt; 200 replays an identical receipt.

Request body maximum: 210763776 bytes.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| header | `Idempotency-Key` | Yes | Required, trimmed; owner-scoped; same key+digest+size+media type+display name replays until expiry pattern=^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$ |

Body: `multipart/form-data`, exactly one required binary file part with a filename; no extra parts.

Success: `201` [ChatSessionAttachmentReceipt](#schema-chatsessionattachmentreceipt).

Errors: 401 authentication failure; 400: Missing/invalid Idempotency-Key or not exactly one multipart file part; 409: Key conflicts with different content or metadata; 413: File exceeds 200 MiB; 422: Empty file, invalid filename, non-image or mismatched media type; 500: Staging failed; 503: 256-entry / 512-MiB staging capacity exhausted

200 indicates an identical attachment replay, with the same receipt shape.

Source: [api/attachment_staging.go](../../api/attachment_staging.go) — `handleStageAttachment`.

<a id="operation-chatsessioncreatesession"></a>
### `POST /api/v1/sessions`

Create a session

Authentication: Bearer. Availability: Mounted only when s.store != nil.

The client supplies a nonempty id. title may be empty; create does not use the rename 200-character validation. Owner comes from the token; platform=web/status=1. Duplicate IDs can fail storage; creation is not an idempotent replay.

Request body maximum: 1048576 bytes.

No path/query/additional header parameters.

JSON body (nested structures are documented under models):

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `id` | string | Yes | minLength=1; Client-supplied session ID; no whitespace normalization at this handler. |
| `title` | string | No | Optional; create does not apply the rename nonblank/200-rune validation. |
| `user_id` | string | No | compatibility field |

Success: `201` [ChatSessionSessionCreated](#schema-chatsessionsessioncreated).

Errors: 401 authentication failure; 400: Invalid JSON or request fields; 500: Storage operation failed

Source: [api/handler_session.go](../../api/handler_session.go) — `handleCreateSession`.

<a id="operation-chatsessionlistsessions"></a>
### `GET /api/v1/sessions`

List sessions

Authentication: Bearer. Availability: Mounted only when s.store != nil.

Lists the current owner by updated time descending. total=len(page)+offset is approximate; has_more only means the page filled limit.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| query | `limit` | No | Missing, invalid or <=0 uses 20; handler has no upper clamp. default=20 |
| query | `offset` | No | Missing/invalid parses as 0; handler does not clamp negative values. default=0 |

Body: not read / not required.

Success: `200` [ChatSessionSessionList](#schema-chatsessionsessionlist).

Errors: 401 authentication failure; 500: Session listing failed

Source: [api/handler_session.go](../../api/handler_session.go) — `handleListSessions`.

<a id="operation-chatsessiongetsession"></a>
### `GET /api/v1/sessions/{id}`

Get a session

Authentication: Bearer. Availability: Mounted only when s.store != nil.



| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Session ID — |

Body: not read / not required.

Success: `200` [ChatSessionSession](#schema-chatsessionsession).

Errors: 401 authentication failure; 404: Session missing or not owned by the current principal; 500: Storage operation failed

Source: [api/handler_session.go](../../api/handler_session.go) — `handleGetSession`.

<a id="operation-chatsessionupdatesession"></a>
### `PATCH /api/v1/sessions/{id}`

Rename a session

Authentication: Bearer. Availability: Mounted only when s.store != nil.

title is trimmed and must be nonblank. Changed titles allow at most 200 Unicode code points; an unchanged historical value is preserved. Returns id/title/updated_at.

Request body maximum: 1048576 bytes.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Session ID — |

JSON body (nested structures are documented under models):

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `title` | string | Yes | Trimmed, nonblank; changed titles are limited to 200 Unicode code points. An unchanged historical title is preserved. |

Success: `200` [ChatSessionSessionUpdated](#schema-chatsessionsessionupdated).

Errors: 401 authentication failure; 400: Invalid JSON or request fields; 404: Session missing or not owned by the current principal; 500: Storage operation failed

Source: [api/handler_session.go](../../api/handler_session.go) — `handleUpdateSession`.

<a id="operation-chatsessionsuggestsessiontitle"></a>
### `POST /api/v1/sessions/{id}/suggest-title`

Suggest a session title

Authentication: Bearer. Availability: Mounted only when s.store != nil.

Body is optional. The current handler decodes it only when ContentLength>0; chunked/unknown-length bodies are not read. A nonblank expected_title that differs from the current title prevents overwriting. Loads up to six existing messages, may invoke a model and persist the title. No messages, unsupported generation, failed/unchanged generation return 200 with updated=false.

Request body maximum: 1048576 bytes.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Session ID — |

JSON body (nested structures are documented under models):

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `expected_title` | string | No | — |

Success: `200` [ChatSessionTitleSuggested](#schema-chatsessiontitlesuggested).

Errors: 401 authentication failure; 400: Invalid JSON or request fields; 404: Session missing or not owned by the current principal; 500: Storage operation failed

Source: [api/handler_session.go](../../api/handler_session.go) — `handleSuggestSessionTitle`.

<a id="operation-chatsessiondeletesession"></a>
### `DELETE /api/v1/sessions/{id}`

Delete a session

Authentication: Bearer. Availability: Mounted only when s.store != nil.

Deletes the session and its messages; invokes the configured sessionDeletedHook on success. 200 {message:"会话已删除"}.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Session ID — |

Body: not read / not required.

Success: `200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

Errors: 401 authentication failure; 404: Session missing or not owned by the current principal; 500: Storage operation failed

Source: [api/handler_session.go](../../api/handler_session.go) — `handleDeleteSession`.

<a id="operation-chatsessionlistmessages"></a>
### `GET /api/v1/sessions/{id}/messages`

List message history

Authentication: Bearer. Availability: Mounted only when s.store != nil.

Messages are ordered by creation time ascending. total uses CountMessages when available, otherwise len(page)+offset. message_content is an egress projection; metadata/meta are JSON-encoded strings.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Session ID — |
| query | `limit` | No | Missing, invalid, <=0 or >200 uses 50. default=50 |
| query | `offset` | No | Missing/invalid uses 0; negative values clamp to 0. default=0 |

Body: not read / not required.

Success: `200` [ChatSessionMessageList](#schema-chatsessionmessagelist).

Errors: 401 authentication failure; 404: Session missing or not owned by the current principal; 500: Storage operation failed

Source: [api/handler_session.go](../../api/handler_session.go) — `handleListMessages`.

<a id="operation-chatsessionappendmessage"></a>
### `POST /api/v1/sessions/{id}/messages`

Append a stored message

Authentication: Bearer. Availability: Mounted only when s.store != nil.

Persists history without running chat/a model. content may be empty; role is user/assistant/system. Blank id is generated; duplicates are not guaranteed UPSERTs and may return 500. Missing content_type uses multimodal_json when metadata is present, otherwise text.

Request body maximum: 8388608 bytes.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Session ID — |

JSON body (nested structures are documented under models):

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `id` | string | No | Trimmed client ID; blank generates msg-*. Duplicate IDs are not a guaranteed idempotent replay and may fail with 500. |
| `role` | string | Yes | enum="user", "assistant", "system" |
| `content` | string | No | — |
| `content_type` | string | No | When omitted: multimodal_json if metadata is present, otherwise text. Nonempty values are not enum-validated. |
| `metadata` | JSON | No | Raw JSON stored as a JSON string. Attachments in an object envelope retain the complete envelope on reads; no fixed metadata schema is enforced. |
| `model_name` | string | No | — |
| `prompt_tokens` | integer | No | — |
| `completion_tokens` | integer | No | — |
| `finish_reason` | string | No | — |
| `parent_id` | string | No | — |
| `request_id` | string | No | — |

Success: `201` [ChatSessionMessageCreated](#schema-chatsessionmessagecreated).

Errors: 401 authentication failure; 400: Invalid JSON or request fields; 404: Session missing or not owned by the current principal; 500: Storage operation failed

Source: [api/handler_session.go](../../api/handler_session.go) — `handleAppendMessage`.

<a id="operation-chatsessionbatchappendmessages"></a>
### `POST /api/v1/sessions/{id}/messages/batch`

Append stored messages atomically

Authentication: Bearer. Availability: Mounted only when s.store != nil.

messages contains 1–50 entries with the same fields as single append. One transaction writes all or rolls back all; no model call is made.

Request body maximum: 16777216 bytes.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Session ID — |

JSON body (nested structures are documented under models):

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `messages` | [ChatSessionAppendMessageRequest](#schema-chatsessionappendmessagerequest)[] | Yes | minItems=1; maxItems=50 |

Success: `201` [ChatSessionBatchMessageCreated](#schema-chatsessionbatchmessagecreated).

Errors: 401 authentication failure; 400: Invalid JSON or request fields; 404: Session missing or not owned by the current principal; 500: Storage operation failed

Source: [api/handler_session.go](../../api/handler_session.go) — `handleBatchAppendMessages`.

<a id="operation-chatsessionlistbranches"></a>
### `GET /api/v1/sessions/{id}/branches`

List session branches

Authentication: Bearer. Availability: Mounted only when s.store != nil.



| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Parent session ID — |

Body: not read / not required.

Success: `200` [ChatSessionBranchList](#schema-chatsessionbranchlist).

Errors: 401 authentication failure; 404: Session missing or not owned by the current principal; 500: Storage operation failed

Source: [api/handler_session.go](../../api/handler_session.go) — `handleListBranches`.

<a id="operation-chatsessionforksession"></a>
### `POST /api/v1/sessions/{id}/fork`

Fork a conversation

Authentication: Bearer. Availability: Mounted only when s.store != nil.

message_id is required. Omitted or null include_message defaults to true and copies the prefix through that message; false excludes the boundary. Creates a new session and copies history without a model response. Missing boundary messages and other storage failures currently return 500.

Request body maximum: 1048576 bytes.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Source session ID — |

JSON body (nested structures are documented under models):

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `message_id` | string | Yes | minLength=1 |
| `user_id` | string | No | compatibility field |
| `include_message` | boolean / null | No | default=true; Omitted/null includes message_id; false copies the prefix before it. |

Success: `200` [ChatSessionForkResult](#schema-chatsessionforkresult).

Errors: 401 authentication failure; 400: Invalid JSON or request fields; 404: Session missing or not owned by the current principal; 500: Storage operation failed

Source: [api/handler_session.go](../../api/handler_session.go) — `handleForkSession`.

<a id="operation-chatsessionsearchmessages"></a>
### `GET /api/v1/messages/search`

Search message contents

Authentication: Bearer. Availability: Mounted only when s.store != nil.

Searches only the current owner’s sessions. results items contain message/session_title/rank; total is the matching count, query echoes the submitted value.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| query | `q` | Yes | Required, nonempty; maximum 512 Unicode code points. maxLength=512 |
| query | `limit` | No | Missing, invalid or <=0 uses 20; no upper clamp. default=20 |
| query | `offset` | No | Missing/invalid parses as 0; no negative clamp. default=0 |

Body: not read / not required.

Success: `200` [ChatSessionSearchMessageList](#schema-chatsessionsearchmessagelist).

Errors: 401 authentication failure; 400: Empty q or q exceeds 512 characters; 500: Search failed

Source: [api/handler_session.go](../../api/handler_session.go) — `handleSearchMessages`.

<a id="operation-chatsessiondeletemessage"></a>
### `DELETE /api/v1/messages/{id}`

Delete a stored message

Authentication: Bearer. Availability: Mounted only when s.store != nil.

Deletes one stored message. Unlike session lookups, a failed session ownership check returns 403. Success: 200 {message:"消息已删除"}.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Message ID — |

Body: not read / not required.

Success: `200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

Errors: 401 authentication failure; 400: Empty message ID; 403: Message session ownership check failed; 404: Message is missing or lookup failed; 500: Delete failed

Source: [api/handler_session.go](../../api/handler_session.go) — `handleDeleteMessage`.

<a id="operation-chatsessionupdatemessagefeedback"></a>
### `PUT /api/v1/messages/{id}/feedback`

Update message feedback

Authentication: Bearer. Availability: Mounted only when s.store != nil.

feedback is like, dislike or an empty string. Omitted/empty clears feedback. 200 {message:"反馈已更新"}.

Request body maximum: 1048576 bytes.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Message ID — |

JSON body (nested structures are documented under models):

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `feedback` | string | No | default=""; enum="", "like", "dislike"; Empty or omitted clears feedback. |

Success: `200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

Errors: 401 authentication failure; 400: Invalid JSON, empty ID or feedback outside empty/like/dislike; 403: Message session ownership check failed; 404: Message is missing or lookup failed; 500: Feedback update failed

Source: [api/handler_session.go](../../api/handler_session.go) — `handleUpdateMessageFeedback`.

<a id="operation-chatsessionlistactivestreams"></a>
### `GET /api/v1/streams/active`

List active streams

Authentication: Bearer. Availability: Mounted only when s.streamStates != nil.

Owner-scoped, nonterminal pending/streaming snapshots ordered by updated time descending. Reads state without triggering/resending a model operation.

No path/query/additional header parameters.

Body: not read / not required.

Success: `200` [ChatSessionStreamList](#schema-chatsessionstreamlist).

Errors: 401 authentication failure; no other JSON handler error status.

Source: [api/handler_streams.go](../../api/handler_streams.go) — `handleListActiveStreams`.

<a id="operation-chatsessiongetstreamsnapshot"></a>
### `GET /api/v1/streams/{request_id}`

Get a stream snapshot

Authentication: Bearer. Availability: Mounted only when s.streamStates != nil.

Returns accumulated content/reasoning, status/done, message identity, runtime_events and last_sequence. Retention is process-local and registry-configured; the default keeps terminal snapshots for two minutes, without restart durability.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `request_id` | Yes | Original stream request ID — |

Body: not read / not required.

Success: `200` [ChatSessionSnapshot](#schema-chatsessionsnapshot).

Errors: 401 authentication failure; 400: Empty request_id; 404: No retained snapshot for this owner/request

Source: [api/handler_streams.go](../../api/handler_streams.go) — `handleGetStreamSnapshot`.

<a id="operation-chatsessiongetmemory"></a>
### `GET /api/v1/memory`

List file memory

Authentication: Bearer. Availability: Always mounted; FileMemory-enabled handler or disabled empty response.

Lists _global entries with context summary/capacity/pagination. type/source/exclude_id filter before pagination. With FileMemory disabled, queries are ignored and the response is entries=[], summary="", capacity={used:0,max:0}, without pagination fields.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| query | `view` | No | Default active; choose active/archived/all. default="active"; enum="active", "archived", "all" |
| query | `limit` | No | Noninteger returns 400; <=0 uses 50; >200 clamps to 200. default=50 |
| query | `cursor` | No | Nonnegative decimal offset; invalid or negative returns 400. — |
| query | `type` | No | Exact type filter; no enum validation. — |
| query | `source` | No | Exact source filter; no enum validation. — |
| query | `exclude_id` | No | Exclude exactly one entry ID. — |

Body: not read / not required.

Success: `200` [ChatSessionMemoryList](#schema-chatsessionmemorylist).

Errors: 401 authentication failure; 400: Invalid limit, cursor or view; only when FileMemory is enabled

Source: [api/handler_misc.go](../../api/handler_misc.go) — `handleGetMemory`.

<a id="operation-chatsessionsavememory"></a>
### `POST /api/v1/memory`

Create a memory entry

Authentication: Bearer. Availability: Mounted only when s.fileMem != nil.

Writes active _global memory and requests a profile refresh; capacity eviction may run. type defaults to fact and source to manual; neither is enum-validated. Returns the last parsed entry, or {message:"记忆已保存"} when no entry is available. Whitespace-only content is ignored by storage and should not be used to create an entry.

Request body maximum: 1048576 bytes.

No path/query/additional header parameters.

JSON body (nested structures are documented under models):

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `content` | string | Yes | minLength=1; Nonempty at HTTP handler; FileMemory trims it. Supply nonblank text to create an entry. |
| `type` | string | No | default="fact"; Common values identity/preference/fact/instruction/context/rule; no enum validation. |
| `source` | string | No | default="manual"; Common values manual/chat_explicit/chat_extract/system; no enum validation. |

Success: `200` [ChatSessionMemoryEntry](#schema-chatsessionmemoryentry).

Errors: 401 authentication failure; 400: Invalid JSON or empty content; 500: Memory save failed

Success may also return MessageText; see the behavior above.

Source: [api/handler_misc.go](../../api/handler_misc.go) — `handleSaveMemory`.

<a id="operation-chatsessionupdatememorywhole"></a>
### `PUT /api/v1/memory`

Replace the active memory file

Authentication: Bearer. Availability: Mounted only when s.fileMem != nil.

Fully replaces _global/MEMORY.md, rather than merging entries. Empty/omitted content writes an empty file. Requests profile refresh; 200 {message:"记忆已更新"}.

Request body maximum: 1048576 bytes.

No path/query/additional header parameters.

JSON body (nested structures are documented under models):

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `content` | string | No | Whole active MEMORY.md replacement without id; a single entry update with id. |

Success: `200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

Errors: 401 authentication failure; 400: Invalid JSON; 500: Memory replacement failed

Source: [api/handler_extended.go](../../api/handler_extended.go) — `handleUpdateMemory`.

<a id="operation-chatsessionupdatememory"></a>
### `PUT /api/v1/memory/{id}`

Update one memory entry

Authentication: Bearer. Availability: Mounted only when s.fileMem != nil.

content must be nonempty. Updates the identified entry text and requests a profile refresh. 200 {message:"记忆已更新"}; underlying update errors all map to 404.

Request body maximum: 1048576 bytes.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Existing memory entry ID — |

JSON body (nested structures are documented under models):

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `content` | string | Yes | minLength=1; New entry text; empty/omitted is rejected. |

content must be nonempty; omission returns 400.

Success: `200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

Errors: 401 authentication failure; 400: Invalid JSON or empty content; 404: Entry update failed, including missing ID or file error

Source: [api/handler_extended.go](../../api/handler_extended.go) — `handleUpdateMemory`.

<a id="operation-chatsessionarchivememoryitem"></a>
### `POST /api/v1/memory/{id}/archive`

Archive a memory entry

Authentication: Bearer. Availability: Mounted only when s.fileMem != nil.

Only active entries can be archived; moves them to the archive and requests a profile refresh. The body is not read; 200 returns message.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Memory entry ID — |

Body: not read / not required.

Success: `200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

Errors: 401 authentication failure; 400: Empty memory ID; 404: Entry/state/file operation failed

Source: [api/handler_extended.go](../../api/handler_extended.go) — `handleArchiveMemoryItem`.

<a id="operation-chatsessionrestorememoryitem"></a>
### `POST /api/v1/memory/{id}/restore`

Restore an archived memory entry

Authentication: Bearer. Availability: Mounted only when s.fileMem != nil.

Only archived entries can be restored; moves them to the active file, may evict for capacity and requests profile refresh. The body is not read; 200 returns message.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Memory entry ID — |

Body: not read / not required.

Success: `200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

Errors: 401 authentication failure; 400: Empty memory ID; 404: Entry/state/file operation failed

Source: [api/handler_extended.go](../../api/handler_extended.go) — `handleRestoreMemoryItem`.

<a id="operation-chatsessionpinmemoryitem"></a>
### `POST /api/v1/memory/{id}/pin`

Pin a memory entry

Authentication: Bearer. Availability: Mounted only when s.fileMem != nil.

Updates pinned=true in place, retaining the content. The body is not read; 200 returns message.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Memory entry ID — |

Body: not read / not required.

Success: `200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

Errors: 401 authentication failure; 400: Empty memory ID; 404: Entry/state/file operation failed

Source: [api/handler_extended.go](../../api/handler_extended.go) — `handlePinMemoryItem`.

<a id="operation-chatsessionunpinmemoryitem"></a>
### `POST /api/v1/memory/{id}/unpin`

Unpin a memory entry

Authentication: Bearer. Availability: Mounted only when s.fileMem != nil.

Updates pinned=false in place, retaining the content. The body is not read; 200 returns message.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Memory entry ID — |

Body: not read / not required.

Success: `200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

Errors: 401 authentication failure; 400: Empty memory ID; 404: Entry/state/file operation failed

Source: [api/handler_extended.go](../../api/handler_extended.go) — `handleUnpinMemoryItem`.

<a id="operation-chatsessiondeletememoryitem"></a>
### `DELETE /api/v1/memory/{id}`

Delete one memory entry

Authentication: Bearer. Availability: Mounted only when s.fileMem != nil.

Deletes the entry and requests profile refresh; 200 {message:"记忆已删除"}.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Memory entry ID — |

Body: not read / not required.

Success: `200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

Errors: 401 authentication failure; 400: Empty memory ID; 404: Entry/file delete failed

Source: [api/handler_extended.go](../../api/handler_extended.go) — `handleDeleteMemoryItem`.

<a id="operation-chatsessiondeletememory"></a>
### `DELETE /api/v1/memory`

Clear file memory

Authentication: Bearer. Availability: Always mounted; FileMemory-enabled handler or disabled no-op response.

When enabled, removes every Markdown file under the current FileMemory directory, including global, role-specific and archived files, then requests profile refresh. Does not clear VectorMemory or only the visible page. Disabled: 200 {message:"记忆模块未启用"}. Body is not read.

No path/query/additional header parameters.

Body: not read / not required.

Success: `200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

Errors: 401 authentication failure; 500: ClearAll failed; only when FileMemory is enabled

Source: [api/handler_extended.go](../../api/handler_extended.go) — `handleDeleteMemory`.

<a id="operation-chatsessionsearchmemory"></a>
### `GET /api/v1/memory/search`

Search memory

Authentication: Bearer. Availability: Mounted only when s.fileMem != nil.

File keyword search scans Markdown in the memory directory and first-level role directories, including archives, and returns at most 100 matches. Optional VectorMemory search returns up to five; vector errors are ignored. exclude_id applies only to file results. Either empty result list may serialize as null. File items contain file/line/content/score; total is the sum of returned file/vector results.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| query | `q` | Yes | Required nonempty query; no handler length limit. — |
| query | `exclude_id` | No | Exclude this ID from file keyword results only. — |

Body: not read / not required.

Success: `200` [ChatSessionMemorySearch](#schema-chatsessionmemorysearch).

Errors: 401 authentication failure; 400: Empty q

Source: [api/handler_misc.go](../../api/handler_misc.go) — `handleSearchMemory`.

<a id="operation-chatsessionrefreshmemoryprofile"></a>
### `POST /api/v1/memory/profile/refresh`

Refresh the memory profile

Authentication: Bearer. Availability: Mounted only when s.fileMem != nil.

No request body is read. Requires the engine profile-maintenance interface. May invoke a model and update the pinned identity profile; action=insert/update/skip. An unknown prior outcome returns 409 and the original request is not resent.

No path/query/additional header parameters.

Body: not read / not required.

Success: `200` [ChatSessionProfileRefreshResult](#schema-chatsessionprofilerefreshresult).

Errors: 401 authentication failure; 409: Profile became stale or the previous outcome is unknown; 422: Profile maintenance/generation failed; 503: Engine does not implement profile maintenance

Source: [api/handler_memory_profile.go](../../api/handler_memory_profile.go) — `handleRefreshMemoryProfile`.

<a id="operation-chatsessioneditmemoryprofile"></a>
### `PUT /api/v1/memory/profile`

Edit the profile and source memories

Authentication: Bearer. Availability: Mounted only when s.fileMem != nil.

Use the profile entry’s profile_revision from GET memory. content must be nonblank. A model links the correction to source memories before an atomic source/profile update; changes that cannot be reliably linked are not saved. An already-applied identical correction returns success.

Request body maximum: 1048576 bytes.

No path/query/additional header parameters.

JSON body (nested structures are documented under models):

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `revision` | string | Yes | minLength=1; Current profile_revision from GET /api/v1/memory |
| `content` | string | Yes | minLength=1; Nonblank revised profile; applied to the profile and reliably matched source memories. |

Success: `200` [ChatSessionMessageText](#schema-chatsessionmessagetext).

Errors: 401 authentication failure; 400: Invalid JSON; 409: Revision is stale or prior outcome is unknown; 422: Blank revision/content, rejected/unreliably linked changes or other maintenance failure; 503: Engine does not implement profile maintenance

Source: [api/handler_memory_profile.go](../../api/handler_memory_profile.go) — `handleEditMemoryProfile`.

<a id="operation-chatsessionlistcheckpoints"></a>
### `GET /api/v1/sessions/{id}/checkpoints`

Get the latest retained checkpoint

Authentication: Bearer. Availability: Mounted only when s.checkpointMgr != nil.

The handler calls Load, not List: checkpoints is empty or contains one CheckpointData, not a full list/CheckpointSummary. All load errors become 200 with an empty array. The empty-id handler branch returns 400 text/plain. No body/query fields. This handler does not call getOwnedSession; per-session ownership validation must not be inferred.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| path | `id` | Yes | Session ID — |

Body: not read / not required.

Success: `200` [ChatSessionCheckpointList](#schema-chatsessioncheckpointlist).

Errors: 401 authentication failure; no other JSON handler error status.

Source: [api/handler_tools.go](../../api/handler_tools.go) — `handleListCheckpoints`.

<a id="operation-chatsessionws"></a>
### `GET /ws`

Upgrade to the WebAdapter WebSocket

Authentication: Bearer. Availability: Mounted only when s.wsHandler != nil; frames here describe adapter/web.WebAdapter.

Authenticate the HTTP Upgrade request with the current Bearer token; query-token authentication is not supported. Native clients may omit Origin; browser origins follow existing local/Tauri rules. JSON text frames have a 20-MiB read limit. The standard browser WebSocket API cannot set Authorization itself; use an existing native/proxy transport.

| Location | Field | Required | Type / rules |
| --- | --- | --- | --- |
| header | `Origin` | No | Optional for native clients. When present: existing local HTTP or Tauri origin rules apply; remote/https origins are rejected. — |

Body: not read / not required.

Success: `101` [ChatSessionWebSocketFrame](#schema-chatsessionwebsocketframe).

Errors: 401 authentication failure; 403: Origin rejected (text/plain before upgrade); other WebSocket handshake errors follow the transport library

After 101, messages are WebSocket JSON frames; the successful HTTP response has no JSON body. 403 is text/plain.

Source: [adapter/web/web.go](../../adapter/web/web.go) — `handleWS`.

## Request and response models

Required means always serialized for responses, or needed for a valid request. Go omitempty fields may be absent; some slices may be null. Raw JSON and tool arguments retain the actual dynamic contract, without new field restrictions.

<a id="schema-chatsessionusage"></a>
### `ChatSessionUsage`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `input_tokens` | integer | Yes | — |
| `output_tokens` | integer | Yes | — |
| `total_tokens` | integer | Yes | — |
| `provider` | string | Yes | — |
| `model` | string | Yes | — |
| `cost` | number | No | — |

<a id="schema-chatsessiontoolcall"></a>
### `ChatSessionToolCall`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `origin` | [ChatSessionToolOrigin](#schema-chatsessiontoolorigin) | No | — |
| `execution` | [ChatSessionSandboxExecution](#schema-chatsessionsandboxexecution) | No | — |
| `id` | string | Yes | — |
| `name` | string | Yes | — |
| `arguments` | string | Yes | — |
| `result` | string | No | — |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | No | — |
| `status` | string | No | — |
| `duration_ms` | integer | No | — |

<a id="schema-chatsessionblock"></a>
### `ChatSessionBlock`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `retrieval` | [ChatSessionRetrievalActivity](#schema-chatsessionretrievalactivity) | No | — |
| `thinking` | string | No | — |
| `type` | string | Yes | enum="text", "thinking", "tool_use", "tool_result", "retrieval" |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | No | — |
| `text` | string | No | — |
| `id` | string | No | — |
| `name` | string | No | — |
| `input` | string | No | — |
| `toolUseId` | string | No | — |
| `toolName` | string | No | — |
| `output` | string | No | — |
| `isError` | boolean | No | — |

<a id="schema-chatsessionknowledgehit"></a>
### `ChatSessionKnowledgeHit`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `doc_id` | string | No | — |
| `document_generation` | integer | No | — |
| `source_digest` | string | No | — |
| `page_start` | integer | No | — |
| `page_end` | integer | No | — |
| `doc_title` | string | No | — |
| `source` | string | No | — |
| `content` | string | No | — |
| `score` | number | No | — |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | No | — |

<a id="schema-chatsessionmemoryhit"></a>
### `ChatSessionMemoryHit`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `id` | string | No | — |
| `content` | string | No | — |
| `source` | string | No | — |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | No | — |

<a id="schema-chatsessionreplychunk"></a>
### `ChatSessionReplyChunk`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `content` | string | Yes | — |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | No | — |
| `render_manifest` | [ChatSessionRenderManifest](#schema-chatsessionrendermanifest) | No | — |
| `reasoning` | string | No | — |
| `done` | boolean | Yes | — |
| `error` | string | No | Transport error messages are serialized as strings by the error-frame handler. |
| `metadata` | object | No | — |
| `usage` | [ChatSessionUsage](#schema-chatsessionusage) | No | — |
| `tool_calls` | [ChatSessionToolCall](#schema-chatsessiontoolcall)[] | No | — |
| `blocks` | [ChatSessionBlock](#schema-chatsessionblock)[] | No | — |
| `interactive` | [ChatSessionInteractivePayload](#schema-chatsessioninteractivepayload) | No | — |
| `knowledge_hits` | [ChatSessionKnowledgeHit](#schema-chatsessionknowledgehit)[] | No | — |
| `memory_hits` | [ChatSessionMemoryHit](#schema-chatsessionmemoryhit)[] | No | — |
| `assistant_message_id` | string | No | — |
| `backend_message_id` | string | No | — |
| `message_id` | string | No | — |
| `sequence` | integer | No | minimum=0 |
| `reasoning_disclosure` | [ChatSessionReasoningDisclosure](#schema-chatsessionreasoningdisclosure) | Yes | — |
| `reasoning_receipt` | [ChatSessionReasoningReceipt](#schema-chatsessionreasoningreceipt) | No | — |
| `runtime_event` | [ChatSessionRuntimeEvent](#schema-chatsessionruntimeevent) | No | — |

<a id="schema-chatsessionsandboxexecution"></a>
### `ChatSessionSandboxExecution`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `run_id` | string | Yes | — |
| `status` | string | Yes | — |
| `language` | string | No | — |
| `command` | string[] | No | — |
| `exit_code` | integer | Yes | — |
| `timeout` | boolean | Yes | — |
| `error` | string | No | — |
| `artifacts` | [ChatSessionSandboxArtifact](#schema-chatsessionsandboxartifact)[] | No | — |

<a id="schema-chatsessionsandboxartifact"></a>
### `ChatSessionSandboxArtifact`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `id` | string | Yes | — |
| `name` | string | Yes | — |
| `size` | integer | Yes | — |
| `mime` | string | No | — |

<a id="schema-chatsessionretrievalactivity"></a>
### `ChatSessionRetrievalActivity`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `kind` | string | Yes | — |
| `status` | string | Yes | — |
| `source` | string | No | — |
| `knowledge_hits` | [ChatSessionKnowledgeHit](#schema-chatsessionknowledgehit)[] | No | — |
| `memory_hits` | [ChatSessionMemoryHit](#schema-chatsessionmemoryhit)[] | No | — |
| `resident_hits` | [ChatSessionMemoryHit](#schema-chatsessionmemoryhit)[] | No | — |

<a id="schema-chatsessiontoolorigin"></a>
### `ChatSessionToolOrigin`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `kind` | string | Yes | — |
| `name` | string | Yes | — |
| `server_name` | string | No | — |

<a id="schema-chatsessionreasoningdisclosure"></a>
### `ChatSessionReasoningDisclosure`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `visibility` | string | Yes | enum="visible", "not_exposed" |
| `source` | string | Yes | — |
| `dialect` | string | Yes | — |
| `provider` | string | Yes | — |
| `model` | string | Yes | — |

<a id="schema-chatsessionruntimeevent"></a>
### `ChatSessionRuntimeEvent`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `version` | integer | Yes | minimum=0 |
| `event_id` | string | Yes | — |
| `kind` | string | Yes | enum="tool_started", "tool_completed", "tool_failed", "terminal" |
| `tool_call_id` | string | No | — |
| `tool_name` | string | No | — |
| `terminal_status` | string | No | enum="completed", "failed", "cancelled" |

<a id="schema-chatsessionsequencedruntimeevent"></a>
### `ChatSessionSequencedRuntimeEvent`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `sequence` | integer | Yes | minimum=0 |
| `event` | [ChatSessionRuntimeEvent](#schema-chatsessionruntimeevent) | Yes | — |

<a id="schema-chatsessionreasoningreceipt"></a>
### `ChatSessionReasoningReceipt`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `version` | integer | Yes | enum=1 |
| `reasoning_request` | string | Yes | enum="on", "off" |
| `reasoning_support` | string | Yes | enum="supported", "unsupported", "unknown" |
| `reasoning_execution` | string | Yes | enum="applied", "ignored", "rejected", "unknown" |

<a id="schema-chatsessioninteractivepayload"></a>
### `ChatSessionInteractivePayload`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `type` | string | Yes | enum="buttons", "select", "approval", "card" |
| `prompt` | string | No | — |
| `buttons` | [ChatSessionInteractiveButton](#schema-chatsessioninteractivebutton)[] | No | — |
| `options` | [ChatSessionInteractiveOption](#schema-chatsessioninteractiveoption)[] | No | — |
| `approval` | [ChatSessionInteractiveApproval](#schema-chatsessioninteractiveapproval) | No | — |
| `card` | [ChatSessionInteractiveCard](#schema-chatsessioninteractivecard) | No | — |
| `resolved` | [ChatSessionInteractiveResolved](#schema-chatsessioninteractiveresolved) | No | — |

<a id="schema-chatsessioninteractivebutton"></a>
### `ChatSessionInteractiveButton`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `label` | string | Yes | — |
| `action` | string | Yes | — |
| `variant` | string | No | enum="primary", "secondary", "danger" |
| `payload` | string | No | — |

<a id="schema-chatsessioninteractiveoption"></a>
### `ChatSessionInteractiveOption`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `label` | string | Yes | — |
| `value` | string | Yes | — |
| `description` | string | No | — |

<a id="schema-chatsessioninteractiveapproval"></a>
### `ChatSessionInteractiveApproval`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `subject` | string | Yes | — |
| `summary` | string | No | — |
| `approve_label` | string | No | — |
| `reject_label` | string | No | — |
| `approve_action` | string | No | — |
| `reject_action` | string | No | — |

<a id="schema-chatsessioninteractivecard"></a>
### `ChatSessionInteractiveCard`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `title` | string | Yes | — |
| `fields` | [ChatSessionCardField](#schema-chatsessioncardfield)[] | No | — |
| `buttons` | [ChatSessionInteractiveButton](#schema-chatsessioninteractivebutton)[] | No | — |
| `image` | string | No | — |
| `footer` | string | No | — |

<a id="schema-chatsessioncardfield"></a>
### `ChatSessionCardField`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `label` | string | Yes | — |
| `value` | string | Yes | — |
| `short` | boolean | No | — |

<a id="schema-chatsessioninteractiveresolved"></a>
### `ChatSessionInteractiveResolved`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `action` | string | Yes | — |
| `label` | string | No | — |
| `value` | string | No | — |
| `approved` | boolean | No | — |
| `timestamp` | string | No | — |

<a id="schema-chatsessionmessagecontent"></a>
### `ChatSessionMessageContent`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `content_id` | string | Yes | — |
| `content_version` | string | Yes | — |
| `producer_kind` | string | Yes | enum="chat", "quick_chat", "k12", "skill", "tool", "rag", "report", "cron", "webhook", "workflow" |
| `markdown` | string | Yes | — |
| `source_digest` | string | Yes | — |
| `locale` | string | Yes | — |
| `attachments` | [ChatSessionAttachmentRef](#schema-chatsessionattachmentref)[] | No | — |

<a id="schema-chatsessionattachmentref"></a>
### `ChatSessionAttachmentRef`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `asset_id` | string | Yes | — |
| `name` | string | No | — |
| `mime` | string | Yes | — |
| `digest` | string | Yes | — |
| `alt_text` | string | No | — |

<a id="schema-chatsessionrendermanifest"></a>
### `ChatSessionRenderManifest`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `render_id` | string | Yes | — |
| `content_id` | string | Yes | — |
| `surface` | string | Yes | enum="desktop", "quick_chat", "history", "k12", "channel", "export" |
| `capability_snapshot` | [ChatSessionCapabilitySnapshot](#schema-chatsessioncapabilitysnapshot) | Yes | — |
| `renderer_version` | string | Yes | — |
| `source_digest` | string | Yes | — |
| `parts` | [ChatSessionRenderPart](#schema-chatsessionrenderpart)[] | Yes | — |
| `fallback_reason` | string | No | — |
| `receipt_ref` | string | No | — |

<a id="schema-chatsessioncapabilitysnapshot"></a>
### `ChatSessionCapabilitySnapshot`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `markdown` | boolean | Yes | — |
| `tex_math` | boolean | Yes | — |
| `mathml` | boolean | No | — |
| `unicode_math` | boolean | No | — |
| `attachments` | boolean | No | — |
| `max_runes` | integer | No | — |

<a id="schema-chatsessionrenderpart"></a>
### `ChatSessionRenderPart`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `kind` | string | Yes | enum="markdown", "text", "artifact" |
| `text` | string | No | — |
| `artifact_ref` | string | No | — |
| `artifact_digest` | string | No | — |
| `alt_text` | string | No | — |

<a id="schema-chatsessionsession"></a>
### `ChatSessionSession`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `id` | string | Yes | — |
| `user_id` | string | Yes | — |
| `platform` | string | Yes | — |
| `instance_id` | string | Yes | — |
| `chat_id` | string | Yes | — |
| `title` | string | Yes | — |
| `parent_session_id` | string | Yes | — |
| `branch_message_id` | string | Yes | — |
| `status` | integer | Yes | enum=1, 0, -1 |
| `message_count` | integer | Yes | — |
| `total_prompt_tokens` | integer | Yes | — |
| `total_completion_tokens` | integer | Yes | — |
| `last_message_preview` | string | Yes | — |
| `meta` | string | Yes | Additional stored JSON encoded as a string. |
| `created_at` | string | Yes | format=date-time |
| `updated_at` | string | Yes | format=date-time |

<a id="schema-chatsessionmessagerecord"></a>
### `ChatSessionMessageRecord`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `id` | string | Yes | — |
| `session_id` | string | Yes | — |
| `parent_id` | string | Yes | — |
| `role` | string | Yes | Stored role; may include checkpoint/tool as well as user/assistant/system. |
| `content` | string | Yes | — |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | No | — |
| `render_manifest` | [ChatSessionRenderManifest](#schema-chatsessionrendermanifest) | No | — |
| `content_type` | string | Yes | — |
| `metadata` | string | Yes | JSON encoded as a string, with attachments merged on reads; not a JSON object. |
| `feedback` | string | Yes | — |
| `model_name` | string | Yes | — |
| `prompt_tokens` | integer | Yes | — |
| `completion_tokens` | integer | Yes | — |
| `finish_reason` | string | Yes | — |
| `latency_ms` | integer | Yes | — |
| `request_id` | string | Yes | — |
| `meta` | string | Yes | Additional stored JSON encoded as a string. |
| `created_at` | string | Yes | format=date-time |
| `assistant_message_id` | string | No | — |
| `backend_message_id` | string | No | — |
| `message_id` | string | No | — |
| `reasoning_disclosure` | [ChatSessionReasoningDisclosure](#schema-chatsessionreasoningdisclosure) | Yes | — |
| `runtime_events` | [ChatSessionSequencedRuntimeEvent](#schema-chatsessionsequencedruntimeevent)[] / null | Yes | — |
| `last_sequence` | integer | Yes | minimum=0 |

<a id="schema-chatsessionsearchresult"></a>
### `ChatSessionSearchResult`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `message` | [ChatSessionMessageRecord](#schema-chatsessionmessagerecord) | Yes | — |
| `session_title` | string | Yes | — |
| `rank` | number | Yes | — |

<a id="schema-chatsessionsnapshot"></a>
### `ChatSessionSnapshot`

Owner-scoped process-local accumulated stream state; not a durable request ledger.

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `request_id` | string | Yes | — |
| `session_id` | string | Yes | — |
| `user_id` | string | No | — |
| `content` | string | No | — |
| `reasoning` | string | No | — |
| `done` | boolean | Yes | — |
| `status` | string | Yes | enum="pending", "streaming", "completed", "errored", "cancelled" |
| `metadata` | object | No | — |
| `usage` | [ChatSessionUsage](#schema-chatsessionusage) | No | — |
| `tool_calls` | [ChatSessionToolCall](#schema-chatsessiontoolcall)[] | No | — |
| `blocks` | [ChatSessionBlock](#schema-chatsessionblock)[] | No | — |
| `started_at` | string | Yes | format=date-time |
| `updated_at` | string | Yes | format=date-time |
| `assistant_message_id` | string | No | — |
| `backend_message_id` | string | No | — |
| `message_id` | string | No | — |
| `reasoning_disclosure` | [ChatSessionReasoningDisclosure](#schema-chatsessionreasoningdisclosure) | Yes | — |
| `runtime_events` | [ChatSessionSequencedRuntimeEvent](#schema-chatsessionsequencedruntimeevent)[] / null | Yes | — |
| `last_sequence` | integer | Yes | minimum=0 |

<a id="schema-chatsessioncheckpointdata"></a>
### `ChatSessionCheckpointData`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `session_id` | string | Yes | — |
| `agent_name` | string | No | — |
| `turn` | integer | Yes | — |
| `messages` | JSON | Yes | Raw JSON preserved by this API; no fixed nested validation is applied here. |
| `budget` | JSON | No | Raw JSON preserved by this API; no fixed nested validation is applied here. |

<a id="schema-chatsessionmemoryentry"></a>
### `ChatSessionMemoryEntry`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `profile_digest` | string | No | — |
| `profile_revision` | string | No | — |
| `manual_correction` | boolean | No | — |
| `id` | string | Yes | — |
| `content` | string | Yes | — |
| `type` | string | Yes | Common values: identity/preference/fact/instruction/context/rule; this API does not enforce an enum. |
| `source` | string | Yes | Examples: manual/chat_explicit/chat_extract/system/reflect_profile; this API does not enforce an enum. |
| `created_at` | string | Yes | — |
| `updated_at` | string | Yes | — |
| `hit_count` | integer | Yes | — |
| `status` | string | Yes | enum="active", "archived" |
| `archived_at` | string | No | — |
| `pinned` | boolean | No | — |
| `subject` | string | No | — |
| `valid_from` | string | No | — |
| `valid_to` | string | No | — |
| `supersedes` | string | No | — |
| `confidence` | number | No | — |

<a id="schema-chatsessionmemorycapacity"></a>
### `ChatSessionMemoryCapacity`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `used` | integer | Yes | — |
| `max` | integer | Yes | — |
| `archived` | integer | No | — |

<a id="schema-chatsessionattachmentreceipt"></a>
### `ChatSessionAttachmentReceipt`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `attachment_id` | string | Yes | — |
| `digest` | string | Yes | — |
| `size` | integer | Yes | — |
| `media_type` | string | Yes | — |
| `display_name` | string | Yes | — |
| `expires_at` | string | Yes | format=date-time |

<a id="schema-chatsessionchatresponse"></a>
### `ChatSessionChatResponse`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `reply` | string | Yes | — |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | No | — |
| `render_manifest` | [ChatSessionRenderManifest](#schema-chatsessionrendermanifest) | No | — |
| `session_id` | string | Yes | — |
| `metadata` | object | No | — |
| `usage` | [ChatSessionUsage](#schema-chatsessionusage) | No | — |
| `tool_calls` | [ChatSessionToolCall](#schema-chatsessiontoolcall)[] | No | — |
| `blocks` | [ChatSessionBlock](#schema-chatsessionblock)[] | No | — |
| `knowledge_hits` | [ChatSessionKnowledgeHit](#schema-chatsessionknowledgehit)[] | No | — |
| `memory_hits` | [ChatSessionMemoryHit](#schema-chatsessionmemoryhit)[] | No | — |
| `assistant_message_id` | string | No | — |
| `backend_message_id` | string | No | — |
| `message_id` | string | No | — |
| `last_sequence` | integer | No | minimum=0 |
| `reasoning_disclosure` | [ChatSessionReasoningDisclosure](#schema-chatsessionreasoningdisclosure) | Yes | — |
| `runtime_events` | [ChatSessionSequencedRuntimeEvent](#schema-chatsessionsequencedruntimeevent)[] | No | — |

<a id="schema-chatsessionchatrequest"></a>
### `ChatSessionChatRequest`

At least nonblank message or valid attachments; JSON body maximum 20 MiB. user_id/platform are compatibility fields and cannot change the authenticated principal.

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `message` | string | No | — |
| `session_id` | string | No | Omitted/empty starts a session; reuse the returned ID to continue. |
| `user_id` | string | No | compatibility field |
| `role` | string | No | Optional Agent role; not a hardcoded enum at this API handler. |
| `provider` | string | No | Optional explicit currently configured provider; validated before processing. |
| `model` | string | No | Optional explicit completion model present in the active provider configuration. |
| `platform` | string | No | compatibility field |
| `attachments` | [ChatSessionAttachment](#schema-chatsessionattachment)[] | No | maxItems=20 |
| `metadata` | object | No | String map. Reserved internal dispatch/sampling keys are stripped. Explicit role/provider/model fields override corresponding metadata. |
| `request_id` | string | No | Correlation and recovery identity, not a durable admission/idempotency guarantee. |
| `temperature` | number / null | No | minimum=0; maximum=2; Omitted/null follows the Agent/model; zero remains an explicit value. |
| `max_tokens` | integer / null | No | minimum=1; maximum=1000000; Omitted/null follows the Agent/model. |

<a id="schema-chatsessioncreatesessionrequest"></a>
### `ChatSessionCreateSessionRequest`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `id` | string | Yes | minLength=1; Client-supplied session ID; no whitespace normalization at this handler. |
| `title` | string | No | Optional; create does not apply the rename nonblank/200-rune validation. |
| `user_id` | string | No | compatibility field |

<a id="schema-chatsessionupdatesessionrequest"></a>
### `ChatSessionUpdateSessionRequest`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `title` | string | Yes | Trimmed, nonblank; changed titles are limited to 200 Unicode code points. An unchanged historical title is preserved. |

<a id="schema-chatsessionsuggesttitlerequest"></a>
### `ChatSessionSuggestTitleRequest`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `expected_title` | string | No | — |

<a id="schema-chatsessionfeedbackrequest"></a>
### `ChatSessionFeedbackRequest`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `feedback` | string | No | default=""; enum="", "like", "dislike"; Empty or omitted clears feedback. |

<a id="schema-chatsessionappendmessagerequest"></a>
### `ChatSessionAppendMessageRequest`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `id` | string | No | Trimmed client ID; blank generates msg-*. Duplicate IDs are not a guaranteed idempotent replay and may fail with 500. |
| `role` | string | Yes | enum="user", "assistant", "system" |
| `content` | string | No | — |
| `content_type` | string | No | When omitted: multimodal_json if metadata is present, otherwise text. Nonempty values are not enum-validated. |
| `metadata` | JSON | No | Raw JSON stored as a JSON string. Attachments in an object envelope retain the complete envelope on reads; no fixed metadata schema is enforced. |
| `model_name` | string | No | — |
| `prompt_tokens` | integer | No | — |
| `completion_tokens` | integer | No | — |
| `finish_reason` | string | No | — |
| `parent_id` | string | No | — |
| `request_id` | string | No | — |

<a id="schema-chatsessionforksessionrequest"></a>
### `ChatSessionForkSessionRequest`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `message_id` | string | Yes | minLength=1 |
| `user_id` | string | No | compatibility field |
| `include_message` | boolean / null | No | default=true; Omitted/null includes message_id; false copies the prefix before it. |

<a id="schema-chatsessionsavememoryrequest"></a>
### `ChatSessionSaveMemoryRequest`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `content` | string | Yes | minLength=1; Nonempty at HTTP handler; FileMemory trims it. Supply nonblank text to create an entry. |
| `type` | string | No | default="fact"; Common values identity/preference/fact/instruction/context/rule; no enum validation. |
| `source` | string | No | default="manual"; Common values manual/chat_explicit/chat_extract/system; no enum validation. |

<a id="schema-chatsessionattachment"></a>
### `ChatSessionAttachment`

object / JSON / JSON

Opaque staged reference. Do not send type/name/mime/data/url alongside attachment_id.

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `attachment_id` | string | Yes | minLength=1 |

Legacy image input. type must equal image case-insensitively OR mime must begin with image/. Provide exactly one of data/url.

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `type` | string | No | — |
| `name` | string | No | — |
| `mime` | string | No | — |
| `data` | string | No | minLength=1; Base64 content |
| `url` | string | No | minLength=1; Image URL |

<a id="schema-chatsessionmessagetext"></a>
### `ChatSessionMessageText`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `message` | string | Yes | — |

<a id="schema-chatsessionerror"></a>
### `ChatSessionError`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `error` | string | Yes | — |
| `code` | string | No | — |
| `layer` | string | No | — |
| `retryable` | boolean | No | — |
| `done` | boolean | No | — |

<a id="schema-chatsessionsseerror"></a>
### `ChatSessionSSEError`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `error` | string | Yes | — |
| `done` | boolean | Yes | enum=true |
| `code` | string | No | — |
| `retryable` | boolean | No | — |
| `assistant_message_id` | string | No | — |
| `backend_message_id` | string | No | — |
| `message_id` | string | No | — |
| `sequence` | integer | No | minimum=0 |
| `reasoning_disclosure` | [ChatSessionReasoningDisclosure](#schema-chatsessionreasoningdisclosure) | No | — |
| `runtime_event` | [ChatSessionRuntimeEvent](#schema-chatsessionruntimeevent) / null | No | — |

<a id="schema-chatsessionsessioncreated"></a>
### `ChatSessionSessionCreated`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `id` | string | Yes | — |
| `title` | string | Yes | — |
| `created_at` | string | Yes | format=date-time |

<a id="schema-chatsessionsessionupdated"></a>
### `ChatSessionSessionUpdated`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `id` | string | Yes | — |
| `title` | string | Yes | — |
| `updated_at` | string | Yes | format=date-time |

<a id="schema-chatsessiontitlesuggested"></a>
### `ChatSessionTitleSuggested`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `id` | string | Yes | — |
| `title` | string | Yes | — |
| `updated` | boolean | Yes | — |
| `updated_at` | string | Yes | format=date-time |

<a id="schema-chatsessionsessionlist"></a>
### `ChatSessionSessionList`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `sessions` | [ChatSessionSession](#schema-chatsessionsession)[] | Yes | — |
| `total` | integer | Yes | len(page) + offset; not the total row count |
| `has_more` | boolean | Yes | True when len(page) == limit; may be true on the final full page. |

<a id="schema-chatsessionmessagelist"></a>
### `ChatSessionMessageList`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `messages` | [ChatSessionMessageRecord](#schema-chatsessionmessagerecord)[] | Yes | — |
| `total` | integer | Yes | CountMessages when available, otherwise len(page) + offset |

<a id="schema-chatsessionsearchmessagelist"></a>
### `ChatSessionSearchMessageList`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `results` | [ChatSessionSearchResult](#schema-chatsessionsearchresult)[] | Yes | — |
| `total` | integer | Yes | — |
| `query` | string | Yes | — |

<a id="schema-chatsessionbranchlist"></a>
### `ChatSessionBranchList`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `branches` | [ChatSessionSession](#schema-chatsessionsession)[] | Yes | — |
| `total` | integer | Yes | — |

<a id="schema-chatsessionforkresult"></a>
### `ChatSessionForkResult`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `session` | [ChatSessionSession](#schema-chatsessionsession) | Yes | — |
| `message` | string | Yes | — |

<a id="schema-chatsessionmessagecreated"></a>
### `ChatSessionMessageCreated`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `id` | string | Yes | — |
| `session_id` | string | Yes | — |

<a id="schema-chatsessionbatchmessagecreated"></a>
### `ChatSessionBatchMessageCreated`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `ids` | string[] | Yes | — |
| `session_id` | string | Yes | — |

<a id="schema-chatsessionbatchappendrequest"></a>
### `ChatSessionBatchAppendRequest`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `messages` | [ChatSessionAppendMessageRequest](#schema-chatsessionappendmessagerequest)[] | Yes | minItems=1; maxItems=50 |

<a id="schema-chatsessionstreamlist"></a>
### `ChatSessionStreamList`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `streams` | [ChatSessionSnapshot](#schema-chatsessionsnapshot)[] | Yes | — |
| `total` | integer | Yes | — |

<a id="schema-chatsessioncheckpointlist"></a>
### `ChatSessionCheckpointList`

The mounted handler calls Load and returns at most one latest checkpoint; load errors are represented by an empty array.

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `checkpoints` | [ChatSessionCheckpointData](#schema-chatsessioncheckpointdata)[] | Yes | maxItems=1 |

<a id="schema-chatsessionmemorylist"></a>
### `ChatSessionMemoryList`

With FileMemory enabled, total/next_cursor/has_more are also always emitted. Disabled: entries=[], summary="", capacity={used:0,max:0}.

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `entries` | [ChatSessionMemoryEntry](#schema-chatsessionmemoryentry)[] | Yes | — |
| `summary` | string | Yes | — |
| `capacity` | [ChatSessionMemoryCapacity](#schema-chatsessionmemorycapacity) | Yes | — |
| `total` | integer | No | — |
| `next_cursor` | string | No | — |
| `has_more` | boolean | No | — |

<a id="schema-chatsessionmemorysearchresult"></a>
### `ChatSessionMemorySearchResult`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `file` | string | Yes | — |
| `line` | integer | Yes | — |
| `content` | string | Yes | — |
| `score` | number | Yes | — |

<a id="schema-chatsessionvectormemorysearchresult"></a>
### `ChatSessionVectorMemorySearchResult`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `content` | string | Yes | — |
| `score` | number | Yes | — |
| `source` | string | Yes | enum="vector" |

<a id="schema-chatsessionmemorysearch"></a>
### `ChatSessionMemorySearch`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `results` | [ChatSessionMemorySearchResult](#schema-chatsessionmemorysearchresult)[] / null | Yes | — |
| `vector_results` | [ChatSessionVectorMemorySearchResult](#schema-chatsessionvectormemorysearchresult)[] / null | Yes | — |
| `total` | integer | Yes | — |

<a id="schema-chatsessionupdatememoryrequest"></a>
### `ChatSessionUpdateMemoryRequest`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `content` | string | No | Whole active MEMORY.md replacement without id; a single entry update with id. |

<a id="schema-chatsessionupdatememoryentryrequest"></a>
### `ChatSessionUpdateMemoryEntryRequest`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `content` | string | Yes | minLength=1; New entry text; empty/omitted is rejected. |

<a id="schema-chatsessioneditprofilerequest"></a>
### `ChatSessionEditProfileRequest`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `revision` | string | Yes | minLength=1; Current profile_revision from GET /api/v1/memory |
| `content` | string | Yes | minLength=1; Nonblank revised profile; applied to the profile and reliably matched source memories. |

<a id="schema-chatsessionprofilerefreshresult"></a>
### `ChatSessionProfileRefreshResult`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `action` | string | Yes | enum="insert", "update", "skip" |

<a id="schema-chatsessionwebsocketframe"></a>
### `ChatSessionWebSocketFrame`

Union transport envelope; required fields depend on frame type. See x-websocket-client-frames and x-websocket-server-frames on GET /ws.

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `type` | string | Yes | enum="message", "reply", "chunk", "error", "ping", "pong", "cancel", "resume", "stream_snapshot", "tool_approval_request", "tool_approval_response", "tool_permission_response", "tool_approval_ack", "tool_approval_terminal", "tool_approval_reconcile" |
| `content` | string | Yes | — |
| `code` | string | No | — |
| `retryable` | boolean | No | — |
| `message_content` | [ChatSessionMessageContent](#schema-chatsessionmessagecontent) | No | — |
| `render_manifest` | [ChatSessionRenderManifest](#schema-chatsessionrendermanifest) | No | — |
| `reasoning` | string | No | — |
| `session_id` | string | No | — |
| `request_id` | string | No | — |
| `decision_id` | string | No | — |
| `decision` | string | No | enum="approved_once", "approved_remember", "denied" |
| `idempotency_key` | string | No | — |
| `status` | string | No | Approval ACK status: accepted/already_accepted/expired/rejected; not a stream status in stream_snapshot frames. |
| `owner_id` | string | No | — |
| `tool_name` | string | No | — |
| `user_id` | string | No | compatibility field |
| `provider` | string | No | — |
| `model` | string | No | — |
| `role` | string | No | — |
| `temperature` | number / null | No | minimum=0; maximum=2; Omitted/null follows the Agent/model; zero remains an explicit value. |
| `max_tokens` | integer / null | No | minimum=1; maximum=1000000; Omitted/null follows the Agent/model. |
| `done` | boolean | No | — |
| `metadata` | object | No | — |
| `arguments` | object | No | Free-form JSON fields defined by the selected tool or producer. |
| `invocation_id` | string | No | — |
| `arguments_digest` | string | No | — |
| `security_scope_digest` | string | No | — |
| `scope_schema_version` | integer | No | minimum=1 |
| `deadline_at` | string | No | format=date-time |
| `terminal_result` | string | No | — |
| `usage` | [ChatSessionUsage](#schema-chatsessionusage) | No | — |
| `tool_calls` | [ChatSessionToolCall](#schema-chatsessiontoolcall)[] | No | — |
| `blocks` | [ChatSessionBlock](#schema-chatsessionblock)[] | No | — |
| `attachments` | [ChatSessionAttachment](#schema-chatsessionattachment)[] | No | — |
| `knowledge_hits` | [ChatSessionKnowledgeHit](#schema-chatsessionknowledgehit)[] | No | — |
| `memory_hits` | [ChatSessionMemoryHit](#schema-chatsessionmemoryhit)[] | No | — |
| `assistant_message_id` | string | No | — |
| `backend_message_id` | string | No | — |
| `message_id` | string | No | — |
| `sequence` | integer | No | minimum=0 |
| `reasoning_disclosure` | [ChatSessionReasoningDisclosure](#schema-chatsessionreasoningdisclosure) | Yes | — |
| `reasoning_receipt` | [ChatSessionReasoningReceipt](#schema-chatsessionreasoningreceipt) | No | — |
| `runtime_event` | [ChatSessionRuntimeEvent](#schema-chatsessionruntimeevent) | No | — |
| `runtime_events` | [ChatSessionSequencedRuntimeEvent](#schema-chatsessionsequencedruntimeevent)[] | No | — |
| `last_sequence` | integer | No | minimum=0 |

<a id="schema-chatsessionwebsocketclientmessage"></a>
### `ChatSessionWebSocketClientMessage`

At least nonblank content or valid attachments. Empty request_id generates req-*; an existing request_id is not a deduplication guarantee.

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `type` | string | Yes | enum="message" |
| `content` | string | No | — |
| `session_id` | string | No | — |
| `request_id` | string | No | — |
| `provider` | string | No | — |
| `model` | string | No | — |
| `role` | string | No | — |
| `temperature` | number / null | No | minimum=0; maximum=2; Omitted/null follows the Agent/model; zero remains an explicit value. |
| `max_tokens` | integer / null | No | minimum=1; maximum=1000000; Omitted/null follows the Agent/model. |
| `metadata` | object | No | String map. Reserved internal dispatch/sampling keys are stripped. Explicit role/provider/model fields override corresponding metadata. |
| `attachments` | [ChatSessionAttachment](#schema-chatsessionattachment)[] | No | maxItems=20 |
| `user_id` | string | No | compatibility field; Ignored for authenticated identity |

<a id="schema-chatsessionwebsocketresume"></a>
### `ChatSessionWebSocketResume`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `type` | string | Yes | enum="resume" |
| `request_id` | string | Yes | minLength=1 |

<a id="schema-chatsessionwebsocketcancel"></a>
### `ChatSessionWebSocketCancel`

Use request_id, or the current owned session_id binding when request_id is empty. No cancellation ACK is emitted.

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `type` | string | Yes | enum="cancel" |
| `request_id` | string | No | — |
| `session_id` | string | No | — |

<a id="schema-chatsessionwebsocketping"></a>
### `ChatSessionWebSocketPing`

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `type` | string | Yes | enum="ping" |

<a id="schema-chatsessionwebsocketapprovalreconcile"></a>
### `ChatSessionWebSocketApprovalReconcile`

Identity fields can alternatively be supplied in metadata; simultaneous top-level/metadata values must match. Metadata approval_request_id/request_id aliases are accepted.

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `type` | string | Yes | enum="tool_approval_reconcile" |
| `request_id` | string | Yes | — |
| `owner_id` | string | Yes | — |
| `session_id` | string | Yes | — |
| `invocation_id` | string | Yes | — |
| `arguments_digest` | string | Yes | — |
| `security_scope_digest` | string | Yes | — |
| `scope_schema_version` | integer | Yes | minimum=1 |
| `deadline_at` | string | Yes | format=date-time |

<a id="schema-chatsessionwebsocketapprovaldecision"></a>
### `ChatSessionWebSocketApprovalDecision`

Repeat the complete identity from the original approval request. owner_id must match the authenticated principal; no legacy approved/remember-only decision is accepted.

| Field | Type | Required | Rules / defaults |
| --- | --- | --- | --- |
| `type` | string | Yes | enum="tool_approval_response", "tool_permission_response" |
| `request_id` | string | Yes | — |
| `owner_id` | string | Yes | — |
| `session_id` | string | Yes | — |
| `invocation_id` | string | Yes | — |
| `arguments_digest` | string | Yes | — |
| `security_scope_digest` | string | Yes | — |
| `scope_schema_version` | integer | Yes | minimum=1 |
| `deadline_at` | string | Yes | format=date-time |
| `decision_id` | string | Yes | minLength=1 |
| `decision` | string | Yes | enum="approved_once", "approved_remember", "denied" |
| `idempotency_key` | string | Yes | minLength=1 |
