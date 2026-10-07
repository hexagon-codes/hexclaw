# Automation, connections and workflow API

[中文](automation-connections.md) · [API overview](../api.en.md) · [OpenAPI](../../api/openapi.yaml)

This reference covers 57 current method/path combinations. All paths begin with `/api/v1`; contracts reflect the current working tree. Examples describe formats only: no tasks were executed, messages sent, or permission settings changed.

Management endpoints follow the overview's Bearer authentication, except the explicitly public callbacks. `POST /api/v1/webhooks/{name}` and `GET/POST /api/v1/platforms/hooks/{provider}/{name}` do not use API Bearer; they use their configured webhook/platform signature or challenge mechanisms. **Signature verification is a separate authentication boundary.**

`x-availability` records startup registration conditions. Without scheduler, webhookMgr, instanceMgr, connectorStore or canvasSvc, the corresponding conditional routes are absent, normally yielding 404. A handler's internal 503 does not mean its route is available when disabled. Workflow and Team routes have no such registration condition.

JSON writes generally use a 1 MiB body limit; Team writes use 64 KiB. Management decode-limit failures map to 400; the public webhook receiver explicitly uses 413. Most management errors are `{"error":"…"}`; Cron uses APIError with code/message/error; platform callback errors can be plain text. HTTP 200 does not prove probe success, task completion or external delivery.

Required fields in input schemas express actual mandatory presence; other fields follow handler zero values, defaults and conditional rules. Timestamps usually use RFC3339; the instance-save response uses HTTP-date for updated_at. Length limits generally apply to new/changed fields; unchanged stored values may retain their prior contract.

## Endpoints

### `POST /api/v1/connections/test` — Validate an unsaved connection

Transient credential probe, without persistence or message delivery. Total timeout is 10 seconds. Email performs SMTP AUTH and optional IMAP login; other providers use adapter ValidateConfig and may call provider authentication APIs. HTTP 200 with ok=false is a failed probe.

Implementation: [`handleConnectionsTest`](../../api/handler_connections.go#L98). Registration: `Always registered.`.

Request body (required): `application/json`: [AutomationConnectionTestRequest](#automationconnectiontestrequest).

| HTTP | Response / condition |
| --- | --- |
| 200 | Validate an unsaved connection. Inspect business status in the response. `application/json` [AutomationConnectionTestResponse](#automationconnectiontestresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Invalid JSON, missing type/config, or unsupported type. `application/json` [AutomationError](#automationerror) |

Request example:

```json
{
  "type": "telegram",
  "config": {
    "token": "example-token"
  }
}
```

### `GET /api/v1/connectors` — List data connectors

Returns redacted summaries ordered by creation time; tokens are omitted.

Implementation: [`handleListConnectors`](../../api/handler_connectors.go#L19). Registration: `s.connectorStore != nil`.

| HTTP | Response / condition |
| --- | --- |
| 200 | List data connectors. Inspect business status in the response. `application/json` [AutomationConnectors](#automationconnectors) |
| 401 | Bearer authentication failed; see API overview. |

### `POST /api/v1/connectors` — Create a data connector

Supports github/notion. Validates the token against the provider before saving; name defaults to provider. The request timeout is 15 seconds. Returns a redacted summary. Persistence errors also map to 400, so 400 is not exclusively field validation.

Implementation: [`handleCreateConnector`](../../api/handler_connectors.go#L31). Registration: `s.connectorStore != nil`.

Request body (required): `application/json`: [AutomationConnectorCreateRequest](#automationconnectorcreaterequest).

| HTTP | Response / condition |
| --- | --- |
| 200 | Create a data connector. Inspect business status in the response. `application/json` [AutomationConnectorSummary](#automationconnectorsummary) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed request, invalid provider/token, length validation, provider probe or persistence failure. `application/json` [AutomationError](#automationerror) |

Request example:

```json
{
  "provider": "github",
  "name": "Source repository",
  "token": "example-token"
}
```

### `DELETE /api/v1/connectors/{id}` — Delete a data connector

Deletes the local connector record; it does not revoke the third-party token. All Store.Delete errors, including persistence errors, map to 404.

Implementation: [`handleDeleteConnector`](../../api/handler_connectors.go#L56). Registration: `s.connectorStore != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Delete a data connector. Inspect business status in the response. `application/json` [AutomationMessage](#automationmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Connector missing or deletion/persistence failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/connectors/test` — Probe a connector token

Calls the GitHub/Notion authentication API without saving. Check ok even on HTTP 200; detail describes probe failure.

Implementation: [`handleTestConnector`](../../api/handler_connectors.go#L70). Registration: `s.connectorStore != nil`.

Request body (required): `application/json`: [AutomationConnectorTestRequest](#automationconnectortestrequest).

| HTTP | Response / condition |
| --- | --- |
| 200 | Probe a connector token. Inspect business status in the response. `application/json` [AutomationConnectionTestResponse](#automationconnectiontestresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON or token exceeds 65536 UTF-8 bytes. `application/json` [AutomationError](#automationerror) |

Request example:

```json
{
  "provider": "notion",
  "token": "example-token"
}
```

### `GET /api/v1/connectors/{id}/resources` — List connector resources

Uses the saved token to fetch read-only provider resources, rather than a local inventory. Timeout is 15 seconds; missing connector and upstream failures both map to 502.

Implementation: [`handleConnectorResources`](../../api/handler_connectors.go#L91). Registration: `s.connectorStore != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | List connector resources. Inspect business status in the response. `application/json` [AutomationConnectorResources](#automationconnectorresources) |
| 401 | Bearer authentication failed; see API overview. |
| 502 | Missing connector or provider resource request failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/webhooks/{name}` — Receive a webhook callback

This public callback does not use API Bearer authentication. Signature verification follows webhook type. accepted for generic/github/gitlab means admission, not business completion; test events do not dispatch. K12 uses its separate signature, nonce, and event_id protocol, returns a 202 receipt, and requires receipt queries to observe completion.

Implementation: [`Manager.Handler`](../../webhook/webhook.go#L358). Registration: `s.webhookMgr != nil`.

Authentication: native platform/webhook mechanism, no API Bearer.

| Location | Parameter | Required | Type / notes |
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

Request body (required): `application/json`: object (map) / [AutomationK12Envelope](#automationk12envelope); `text/plain`: `string`.

| HTTP | Response / condition |
| --- | --- |
| 200 | Generic/GitHub/GitLab accepted has a JSON body but no explicit JSON Content-Type, so Go HTTP may label it text/plain; test responses use application/json. `application/json` / `text/plain` [AutomationWebhookAccepted](#automationwebhookaccepted) |
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

Request example:

```json
{
  "event_type": "push",
  "message": "Example event"
}
```

### `GET /api/v1/webhooks` — List webhooks or K12 receipts

Defaults to ordinary webhooks for the trusted owner; agent_id adds that Agent's K12 bindings. receipt_id takes precedence over binding_name; both receipt queries require agent_id. binding_name history is capped at 50.

Implementation: [`handleListWebhooks`](../../api/handler_webhook.go#L19). Registration: `s.webhookMgr != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| query | `agent_id` | no | `string`; — Required for receipt_id/binding_name queries; adds K12 bindings otherwise. |
| query | `receipt_id` | no | `string`; — Single receipt; takes precedence over binding_name. |
| query | `binding_name` | no | `string`; — Receipt history, at most 50 entries. |

| HTTP | Response / condition |
| --- | --- |
| 200 | List webhooks or K12 receipts. Inspect business status in the response. `application/json` [AutomationWebhooks](#automationwebhooks) / [AutomationReceiptResponse](#automationreceiptresponse) / [AutomationReceipts](#automationreceipts) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Receipt query missing agent_id. `application/json` [AutomationError](#automationerror) |
| 404 | Owned binding/receipt not found. `application/json` [AutomationError](#automationerror) |
| 500 | Storage query failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/webhooks` — Register a webhook

Ordinary webhooks require name and either prompt or job_id, default enabled=false and type=generic; generated secrets are returned once. K12 requires name/agent_id/learner_id/allowed_events and rejects caller-supplied secrets; returns the binding and a one-time secret. Bound Cron jobs must belong to the current owner.

Implementation: [`handleRegisterWebhook`](../../api/handler_webhook.go#L109). Registration: `s.webhookMgr != nil`.

Request body (required): `application/json`: [AutomationRegisterWebhookRequest](#automationregisterwebhookrequest).

| HTTP | Response / condition |
| --- | --- |
| 200 | Register a webhook. Inspect business status in the response. `application/json` [AutomationWebhookCreated](#automationwebhookcreated) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON, length limit, invalid K12 fields/binding. `application/json` [AutomationError](#automationerror) |
| 404 | Bound Cron job not found for owner. `application/json` [AutomationError](#automationerror) |
| 409 | Webhook name already exists. `application/json` [AutomationError](#automationerror) |
| 500 | Job validation, registration or storage failed. `application/json` [AutomationError](#automationerror) |
| 503 | Webhook manager or required Cron scheduler unavailable. `application/json` [AutomationError](#automationerror) |

Request example:

```json
{
  "name": "repo-events",
  "type": "github",
  "prompt": "Summarize repository events",
  "enabled": false
}
```

### `PATCH /api/v1/webhooks/{name}` — Update a webhook or retry a K12 receipt

Ordinary webhooks update enabled only. K12 uses query agent_id for owner checks and accepts enabled, allowed_events (with allowed_workflows), and rotate_secret. retry_receipt_id must be submitted alone and only retries failed receipts with explicit retry-safe evidence; it preserves event identity and can redispatch real work. Multiple configuration changes execute sequentially; a later failure may leave earlier changes applied.

Implementation: [`handleUpdateWebhook`](../../api/handler_webhook.go#L257). Registration: `s.webhookMgr != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |
| query | `agent_id` | no | `string`; — Required to resolve owned K12 binding; not used by generic webhooks. |

Request body (required): `application/json`: [AutomationUpdateWebhookRequest](#automationupdatewebhookrequest).

| HTTP | Response / condition |
| --- | --- |
| 200 | Update a webhook or retry a K12 receipt. Inspect business status in the response. `application/json` [AutomationWebhookUpdated](#automationwebhookupdated) / [AutomationReceiptResponse](#automationreceiptresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON, missing operation, invalid events or conflicting retry fields. `application/json` [AutomationError](#automationerror) |
| 404 | Owned webhook/receipt not found. `application/json` [AutomationError](#automationerror) |
| 409 | Receipt is not safely retryable. `application/json` [AutomationError](#automationerror) |
| 500 | Binding read/update, rotation or retry failed. `application/json` [AutomationError](#automationerror) |
| 503 | Webhook manager unavailable. `application/json` [AutomationError](#automationerror) |

Request example:

```json
{
  "enabled": true
}
```

### `DELETE /api/v1/webhooks/{name}` — Delete a webhook

Deletes for the trusted owner; K12 also needs agent_id. Revokes grants for the deleted webhook task. K12 returns 409 while receipts are still executing.

Implementation: [`handleDeleteWebhook`](../../api/handler_webhook.go#L352). Registration: `s.webhookMgr != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |
| query | `agent_id` | no | `string`; — Required to resolve owned K12 binding; not used by generic webhooks. |

| HTTP | Response / condition |
| --- | --- |
| 200 | Delete a webhook. Inspect business status in the response. `application/json` [AutomationMessage](#automationmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Owned webhook not found. `application/json` [AutomationError](#automationerror) |
| 409 | K12 binding has active receipts. `application/json` [AutomationError](#automationerror) |
| 500 | Storage read/delete failed. `application/json` [AutomationError](#automationerror) |
| 503 | Webhook manager unavailable. `application/json` [AutomationError](#automationerror) |

### `GET /api/v1/automation/status` — Read automation availability

enabled reports the configuration flag. state is disabled/unavailable/ready; ready reflects the actual injected component. An empty job list does not mean the component is unavailable.

Implementation: [`handleAutomationStatus`](../../api/handler_automation_status.go#L11). Registration: `Always registered.`.

| HTTP | Response / condition |
| --- | --- |
| 200 | Read automation availability. Inspect business status in the response. `application/json` [AutomationStatus](#automationstatus) |
| 401 | Bearer authentication failed; see API overview. |

### `GET /api/v1/autonomy/profile` — Read the autonomy profile

Returns the current profile, the four selectable profiles, and the effective matrix snapshot; read-only.

Implementation: [`handleGetAutonomyProfile`](../../api/handler_autonomy.go#L104). Registration: `Always registered.`.

| HTTP | Response / condition |
| --- | --- |
| 200 | Read the autonomy profile. Inspect business status in the response. `application/json` [AutomationProfile](#automationprofile) |
| 401 | Bearer authentication failed; see API overview. |

### `PUT /api/v1/autonomy/profile` — Update the autonomy profile

Trims and lowercases profile. Saves configuration before replacing the runtime policy; no restart is required. A persistence failure leaves runtime policy unchanged. This reference describes existing behavior only.

Implementation: [`handleUpdateAutonomyProfile`](../../api/handler_autonomy.go#L122). Registration: `Always registered.`.

Request body (required): `application/json`: [AutomationProfileUpdate](#automationprofileupdate).

| HTTP | Response / condition |
| --- | --- |
| 200 | Update the autonomy profile. Inspect business status in the response. `application/json` [AutomationProfile](#automationprofile) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON or unknown profile. `application/json` [AutomationError](#automationerror) |
| 503 | Configuration is not ready. `application/json` [AutomationError](#automationerror) |
| 500 | Configuration persistence failed. `application/json` [AutomationError](#automationerror) |

Request example:

```json
{
  "profile": "balanced"
}
```

### `POST /api/v1/autonomy/preflight` — Preflight automation capabilities

source must be nonblank. workflow_id may add statically known tools; cron_job_id may supply an existing job prompt and delivery facts when prompt is blank. This only estimates capabilities: it neither runs tasks nor grants access. all_clear covers the estimated scope only; heuristic analysis may miss capabilities. Missing referenced workflows/jobs do not automatically produce 404 here.

Implementation: [`handleAutonomyPreflight`](../../api/handler_autonomy.go#L188). Registration: `Always registered.`.

Request body (required): `application/json`: [AutomationPreflightRequest](#automationpreflightrequest).

| HTTP | Response / condition |
| --- | --- |
| 200 | Preflight automation capabilities. Inspect business status in the response. `application/json` [AutomationPreflightResult](#automationpreflightresult) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON or blank source. `application/json` [AutomationError](#automationerror) |

Request example:

```json
{
  "source": "workflow",
  "workflow_id": "wf-example"
}
```

### `GET /api/v1/autonomy/summary` — Read the autonomy summary

Summarizes Cron/Webhooks for user_id (default api-user) and saved workflows. Combines preflight estimates with unresolved runtime blocks; counts.grants counts all active grants. Some dependency read failures are skipped or logged, so 200 is not a completeness guarantee.

Implementation: [`handleAutonomySummary`](../../api/handler_autonomy.go#L277). Registration: `Always registered.`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| query | `user_id` | no | `string`; default: `"api-user"`  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Read the autonomy summary. Inspect business status in the response. `application/json` [AutomationSummary](#automationsummary) |
| 401 | Bearer authentication failed; see API overview. |

### `GET /api/v1/autonomy/decisions` — List autonomy decisions

Filters by source/task_ref/decision; ordered by descending at and id. Nonpositive, invalid, or >500 limit uses 100. Returns an empty list when decision storage is absent.

Implementation: [`handleListAutonomyDecisions`](../../api/handler_autonomy.go#L419). Registration: `Always registered.`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| query | `source` | no | `string`; —  |
| query | `task_ref` | no | `string`; —  |
| query | `decision` | no | `string`; enum: `allow`, `pending`, `deny`  |
| query | `limit` | no | `integer`; default: `100` Nonpositive, nonnumeric or >500 falls back to 100. |

| HTTP | Response / condition |
| --- | --- |
| 200 | List autonomy decisions. Inspect business status in the response. `application/json` [AutomationDecisions](#automationdecisions) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Decision store query failed. `application/json` [AutomationError](#automationerror) |

### `GET /api/v1/autonomy/grants` — List active task grants

Optional task_ref filter; omitted means all active grants. Returns an empty list if grant storage is absent.

Implementation: [`handleListAutonomyGrants`](../../api/handler_autonomy.go#L443). Registration: `Always registered.`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| query | `task_ref` | no | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | List active task grants. Inspect business status in the response. `application/json` [AutomationGrants](#automationgrants) |
| 401 | Bearer authentication failed; see API overview. |

### `POST /api/v1/autonomy/grants` — Create a task grant

Requires task_ref and at least one nonblank entries item. Owner is taken only from trusted context; entries are trimmed/deduplicated, blank entries and bare * discarded, and source lowercased. An existing active task_ref/source/entries set is returned; this idempotency check does not compare note or security_scope_digest. Creating a grant does not automatically resume a task.

Implementation: [`handleCreateAutonomyGrant`](../../api/handler_autonomy.go#L469). Registration: `Always registered.`.

Request body (required): `application/json`: [AutomationCreateGrantRequest](#automationcreategrantrequest).

| HTTP | Response / condition |
| --- | --- |
| 200 | Create a task grant. Inspect business status in the response. `application/json` object {`grant`: [AutomationGrant](#automationgrant)} |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON, invalid grant, or storage create/reload failure. `application/json` [AutomationError](#automationerror) |
| 503 | Grant storage is unavailable. `application/json` [AutomationError](#automationerror) |

Request example:

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

### `DELETE /api/v1/autonomy/grants/{id}` — Revoke a task grant

Revokes the persisted grant and refreshes the active grant snapshot.

Implementation: [`handleRevokeAutonomyGrant`](../../api/handler_autonomy.go#L494). Registration: `Always registered.`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Revoke a task grant. Inspect business status in the response. `application/json` [AutomationMessage](#automationmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Grant revoke failed or grant not found. `application/json` [AutomationError](#automationerror) |
| 503 | Grant storage is unavailable. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/cronjob` — Unified Cron operations

See the parent Cron reference for all seven actions, fields, idempotency limits and examples; this route does not switch to SSE.

Implementation: [`handleCronjobUnified`](../../api/handler_cronjob_unified.go#L120). Registration: `s.scheduler != nil`.

Request body (required): `application/json`: `CronJobRequest`.

| HTTP | Response / condition |
| --- | --- |
| 200 | action 对应响应；缓存命中时带 X-Idempotent-Replay: 1 `application/json` `CronJobResponse` |
| 400 | BAD_REQUEST / CRON_UNKNOWN_ACTION / CRON_INVALID_SCHEDULE / CRON_COMPILE_FAILED / CRON_VALIDATE_FAILED `application/json` `APIError` |
| 401 | Bearer authentication failed; see API overview. |
| 404 | 目标不存在或不属于当前 owner；update 为 BAD_REQUEST，其他生命周期操作为 CRON_JOB_NOT_FOUND `application/json` `APIError` |
| 409 | CRON_JOB_PAUSED（暂停任务不能 run） `application/json` `APIError` |
| 429 | CRON_QUOTA_EXCEEDED；当前活跃任务上限为 30 `application/json` `APIError` |
| 500 | INTERNAL_ERROR；包括未分类的创建、替换或存储错误 `application/json` `APIError` |
| 503 | CRON_EXECUTOR_UNAVAILABLE；handler 内部的 CRON_DISABLED 仅在调度器未注入时出现，普通未启用实例不会挂载本路径 `application/json` `APIError` |

### `POST /api/v1/cron/parse` — Parse a natural-language scheduled task

Parses a draft without creating a job. hints.locale is currently unused. May call the configured model. Parsed drafts and unavailable/failed/incomplete model results all use 200; the latter return needs_clarification=true. tier is always 2.

Implementation: [`handleCronParse`](../../api/handler_cron_parse.go#L62). Registration: `s.scheduler != nil`.

Request body (required): `application/json`: [AutomationCronParseRequest](#automationcronparserequest).

| HTTP | Response / condition |
| --- | --- |
| 200 | Parse a natural-language scheduled task. Inspect business status in the response. `application/json` [AutomationCronParseResponse](#automationcronparseresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON or blank text; APIError shape. `application/json` `APIError` |

Request example:

```json
{
  "text": "每天早上9点生成摘要",
  "hints": {
    "locale": "zh-CN"
  }
}
```

### `POST /api/v1/cron/jobs/stream` — Create a scheduled task over SSE

Validates the request and quota of 30 active jobs per user before opening SSE. progress reports compilation stages; done carries job/spec_preview; error carries error/stage. Stream errors retain HTTP 200. Disconnect cancels the request context; check the task list before retrying an unknown creation outcome.

Implementation: [`handleAddCronJobSSE`](../../api/handler_cron.go#L123). Registration: `s.scheduler != nil`.

Request body (required): `application/json`: [AutomationAddCronJobRequest](#automationaddcronjobrequest).

| HTTP | Response / condition |
| --- | --- |
| 200 | SSE: progress -> done, or progress -> error. error remains HTTP 200 after headers. JSON frame schemas: AutomationSSEProgress, AutomationSSEDone, AutomationSSEError. `text/event-stream` `string` |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed request, missing fields, invalid lengths, or unsupported once type; APIError. `application/json` `APIError` |
| 429 | Active job quota reached; APIError CRON_QUOTA_EXCEEDED. `application/json` `APIError` |
| 500 | Quota check failure returns APIError (code=INTERNAL_ERROR); SSE initialization failure returns the error-only shape. `application/json` `APIError` / [AutomationError](#automationerror) |
| 503 | Scheduler missing inside handler; normally this route is not registered. `application/json` `APIError` |

Request example:

```json
{
  "name": "Daily summary",
  "schedule": "0 9 * * *",
  "prompt": "Summarize recent notes",
  "paused": true
}
```

### `GET /api/v1/cron/jobs/{id}/history` — Read Cron execution history

Read actual execution/delivery history; limit defaults to 50. Accepted triggering does not prove completion.

Implementation: [`handleCronJobHistory`](../../api/handler_extended.go#L48). Registration: `s.scheduler != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |
| query | `limit` | no | `integer`; default: `50`; minimum: 1 正整数；缺省或无效值使用服务默认 50 |

| HTTP | Response / condition |
| --- | --- |
| 200 | 从新到旧的历史记录；检查执行状态与实际结果，而非仅检查 run 接纳 `application/json` object {`history`: array&lt;`JobHistory`&gt;, `total`: `integer`} |
| 401 | Bearer authentication failed; see API overview. |
| 404 | 任务不存在或历史查询失败 `application/json` `MessageError` |

### `GET /api/v1/connections` — List connection summaries

Returns redacted summaries of all instances without config or credentials. receive/send capabilities are derived from provider; the summary does not prove actual delivery.

Implementation: [`handleListConnections`](../../api/handler_connections.go#L55). Registration: `s.instanceMgr != nil`.

| HTTP | Response / condition |
| --- | --- |
| 200 | List connection summaries. Inspect business status in the response. `application/json` [AutomationConnections](#automationconnections) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Instance store read failed. `application/json` [AutomationError](#automationerror) |

### `GET /api/v1/platforms/instances` — List platform instances

Reconciles started adapters with live connection health before returning the list; sensitive config values are masked. This is more than a static configuration read.

Implementation: [`handleListInstances`](../../api/handler_instances.go#L191). Registration: `s.instanceMgr != nil`.

| HTTP | Response / condition |
| --- | --- |
| 200 | List platform instances. Inspect business status in the response. `application/json` [AutomationInstances](#automationinstances) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Instance read/reconciliation failed. `application/json` [AutomationError](#automationerror) |

### `GET /api/v1/platforms/instances/health` — Read health for all instances

Performs actual instance health checks and returns per-instance reports; read healthy separately from status.

Implementation: [`handleListInstanceHealth`](../../api/handler_instances.go#L440). Registration: `s.instanceMgr != nil`.

| HTTP | Response / condition |
| --- | --- |
| 200 | Read health for all instances. Inspect business status in the response. `application/json` [AutomationInstanceHealth](#automationinstancehealth) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Health enumeration failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/platforms/instances` — Upsert a platform instance by name

POST uses body.name; PUT overrides it with path.name. provider/name are required and id is ignored. Omitted config saves an empty object; omitted enabled is false. Returning masked credentials can preserve existing secrets but cannot create a new secret from a mask. enabled=true stops/starts the instance after saving; false stops it. A startup 500 may occur after persistence has succeeded.

Implementation: [`handleUpsertInstance`](../../api/handler_instances.go#L210). Registration: `s.instanceMgr != nil`.

Request body (required): `application/json`: [AutomationUpsertInstanceRequest](#automationupsertinstancerequest) + required: `provider`, `name`.

| HTTP | Response / condition |
| --- | --- |
| 200 | Upsert a platform instance by name. Inspect business status in the response. `application/json` [AutomationInstanceResponse](#automationinstanceresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed request, missing provider/name, unsupported provider, invalid config/mask, or length/write validation. `application/json` [AutomationError](#automationerror) |
| 500 | Store read or adapter startup failed. `application/json` [AutomationError](#automationerror) |

Request example:

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

### `PUT /api/v1/platforms/instances/by-id/{id}` — Update a platform instance by ID

Locates the existing instance by path.id. Blank provider/name retain old values; omitted config retains the old config, but omitted enabled is still false. Stops the old instance before writing and optionally starting it; rename is allowed. Response updated_at uses HTTP-date, whereas list timestamps use RFC3339.

Implementation: [`handleUpdateInstanceByID`](../../api/handler_instances.go#L267). Registration: `s.instanceMgr != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

Request body (required): `application/json`: [AutomationUpsertInstanceRequest](#automationupsertinstancerequest).

| HTTP | Response / condition |
| --- | --- |
| 200 | Update a platform instance by ID. Inspect business status in the response. `application/json` [AutomationInstanceResponse](#automationinstanceresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON, invalid id/config/mask/length or write validation. `application/json` [AutomationError](#automationerror) |
| 404 | Instance id not found. `application/json` [AutomationError](#automationerror) |
| 500 | Store read or adapter startup failed. `application/json` [AutomationError](#automationerror) |

Request example:

```json
{
  "name": "renamed-bot",
  "enabled": false
}
```

### `DELETE /api/v1/platforms/instances/by-id/{id}` — Delete an instance by ID

Uses the same routing cleanup as deletion by name, but unknown IDs return 404.

Implementation: [`handleDeleteInstanceByID`](../../api/handler_instances.go#L366). Registration: `s.instanceMgr != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Delete an instance by ID. Inspect business status in the response. `application/json` [AutomationNamedMessage](#automationnamedmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Blank id. `application/json` [AutomationError](#automationerror) |
| 404 | Instance id not found. `application/json` [AutomationError](#automationerror) |
| 500 | Read, delete or routing cleanup failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/platforms/instances/by-id/{id}/test` — Check a saved instance by ID

Resolves the ID and reuses the named runtime health check; no message is sent.

Implementation: [`handleTestInstanceByID`](../../api/handler_instances.go#L496). Registration: `s.instanceMgr != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Check a saved instance by ID. Inspect business status in the response. `application/json` [AutomationInstanceTestResponse](#automationinstancetestresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Instance id not found. `application/json` [AutomationError](#automationerror) |
| 500 | Instance lookup/health query failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/platforms/instances/by-id/{id}/send-test` — Send a test message to external conversations

This endpoint has real outbound effects and requires an enabled instance. With request_id, it freezes/deduplicates existing bound targets, sends within 30 seconds, and replays only the original receipt; unhealthy or unbound instances send nothing. accepted is not confirmed delivery and unknown results are not automatically resent. Without request_id, target/content are required; the instance is started and sends to target. Send failure can still be HTTP 200 success=false. Examples are documentation only.

Implementation: [`handleSendTestInstanceByID`](../../api/handler_instances.go#L511). Registration: `s.instanceMgr != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

Request body (required): `application/json`: [AutomationSendTestRequest](#automationsendtestrequest).

| HTTP | Response / condition |
| --- | --- |
| 200 | Send a test message to external conversations. Inspect business status in the response. `application/json` [AutomationTestDeliveryResult](#automationtestdeliveryresult) / [AutomationDirectSendResult](#automationdirectsendresult) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Disabled instance, malformed JSON, or missing direct target/content. `application/json` [AutomationError](#automationerror) / [AutomationDirectSendResult](#automationdirectsendresult) |
| 404 | Instance id not found. `application/json` [AutomationError](#automationerror) / [AutomationDirectSendResult](#automationdirectsendresult) |
| 500 | Instance lookup/start, frozen-delivery read or send processing failed; pending may be true. `application/json` [AutomationError](#automationerror) / [AutomationDirectSendResult](#automationdirectsendresult) |

Request example:

```json
{
  "request_id": "connection-check-example",
  "content": "Connection check"
}
```

### `PUT /api/v1/platforms/instances/{name}` — Upsert a platform instance by name

POST uses body.name; PUT overrides it with path.name. provider/name are required and id is ignored. Omitted config saves an empty object; omitted enabled is false. Returning masked credentials can preserve existing secrets but cannot create a new secret from a mask. enabled=true stops/starts the instance after saving; false stops it. A startup 500 may occur after persistence has succeeded.

Implementation: [`handleUpsertInstance`](../../api/handler_instances.go#L210). Registration: `s.instanceMgr != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |

Request body (required): `application/json`: [AutomationUpsertInstanceRequest](#automationupsertinstancerequest) + required: `provider`.

| HTTP | Response / condition |
| --- | --- |
| 200 | Upsert a platform instance by name. Inspect business status in the response. `application/json` [AutomationInstanceResponse](#automationinstanceresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed request, missing provider/name, unsupported provider, invalid config/mask, or length/write validation. `application/json` [AutomationError](#automationerror) |
| 500 | Store read or adapter startup failed. `application/json` [AutomationError](#automationerror) |

Request example:

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

### `DELETE /api/v1/platforms/instances/{name}` — Delete an instance by name

Stops/deletes the instance and removes its routing rules; when no instances remain for the provider, legacy provider rules are also removed. Missing names still return 200. A cleanup 500 can mean the instance itself was already deleted.

Implementation: [`handleDeleteInstance`](../../api/handler_instances.go#L342). Registration: `s.instanceMgr != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Delete an instance by name. Inspect business status in the response. `application/json` [AutomationNamedMessage](#automationnamedmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Read, delete or routing cleanup failed. `application/json` [AutomationError](#automationerror) |

### `GET /api/v1/platforms/instances/{name}/health` — Read instance health

Checks runtime health by instance name. The current handler also maps an unknown name to 500.

Implementation: [`handleGetInstanceHealth`](../../api/handler_instances.go#L452). Registration: `s.instanceMgr != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Read instance health. Inspect business status in the response. `application/json` [AutomationHealthReport](#automationhealthreport) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Instance lookup/health query failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/platforms/instances/{name}/test` — Check a saved instance

Checks runtime health without sending a test message. HTTP 200 with success=false is an unhealthy result.

Implementation: [`handleTestInstance`](../../api/handler_instances.go#L471). Registration: `s.instanceMgr != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Check a saved instance. Inspect business status in the response. `application/json` [AutomationInstanceTestResponse](#automationinstancetestresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Instance lookup/health query failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/platforms/instances/{name}/start` — Start an instance

Starts the adapter and its connection/polling lifecycle; may establish external network connections. No request body.

Implementation: [`handleStartInstance`](../../api/handler_instances.go#L431). Registration: `s.instanceMgr != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Start an instance. Inspect business status in the response. `application/json` [AutomationNamedMessage](#automationnamedmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Instance startup failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/platforms/instances/{name}/stop` — Stop an instance

Stops the running adapter and its connections; no request body.

Implementation: [`handleStopInstance`](../../api/handler_instances.go#L462). Registration: `s.instanceMgr != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `name` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Stop an instance. Inspect business status in the response. `application/json` [AutomationNamedMessage](#automationnamedmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 500 | Instance stop failed. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/im/channels/{provider}/test` — Validate platform configuration

The body is the provider config itself, without a type/config wrapper. Builds the adapter and invokes ValidateConfig, which may access provider authentication APIs. Does not save, start, or send messages. Validation failure is HTTP 200 success=false.

Implementation: [`handleTestChannelConfig`](../../api/handler_instances.go#L573). Registration: `s.instanceMgr != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `provider` | yes | `string`; —  |

Request body (required): `application/json`: [AutomationProviderConfig](#automationproviderconfig).

| HTTP | Response / condition |
| --- | --- |
| 200 | Validate platform configuration. Inspect business status in the response. `application/json` [AutomationChannelTestResponse](#automationchanneltestresponse) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON, missing provider/body. `application/json` [AutomationError](#automationerror) |

Request example:

```json
{
  "token": "example-token"
}
```

### `GET /api/v1/channels/wecom/guide` — Read the WeCom setup guide

Returns steps, required_fields, and callback_url. The current guide still emits the legacy /api/v1/webhook/wecom URL; actual instance callbacks use /api/v1/platforms/hooks/wecom/{name}. Actual instance config fields are token/aes_key; guide labels callback_token/callback_aes_key are not instance field names.

Implementation: [`handleWecomGuide`](../../api/handler_wechat.go#L11). Registration: `s.instanceMgr != nil`.

| HTTP | Response / condition |
| --- | --- |
| 200 | Read the WeCom setup guide. Inspect business status in the response. `application/json` [AutomationWecomGuide](#automationwecomguide) |
| 401 | Bearer authentication failed; see API overview. |

### `GET /api/v1/platforms/hooks/{provider}/{name}` — Receive a platform instance callback

Public callback without API Bearer. Matches provider/name against the started instance and its registered inbound handler; otherwise returns a text 404. Request/response follow each provider's native challenge/event protocol, using configured signature verification/decryption, and can enter real message handling and replies.

Implementation: [`handlePlatformHook`](../../api/handler_instances.go#L625). Registration: `s.instanceMgr != nil`.

Authentication: native platform/webhook mechanism, no API Bearer.

| Location | Parameter | Required | Type / notes |
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

| HTTP | Response / condition |
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

### `POST /api/v1/platforms/hooks/{provider}/{name}` — Receive a platform instance callback

Public callback without API Bearer. Matches provider/name against the started instance and its registered inbound handler; otherwise returns a text 404. Request/response follow each provider's native challenge/event protocol, using configured signature verification/decryption, and can enter real message handling and replies.

Implementation: [`handlePlatformHook`](../../api/handler_instances.go#L625). Registration: `s.instanceMgr != nil`.

Authentication: native platform/webhook mechanism, no API Bearer.

| Location | Parameter | Required | Type / notes |
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

Request body (required): `application/json`: [AutomationPlatformCallback](#automationplatformcallback); `application/xml`: `string`; `text/xml`: `string`.

| HTTP | Response / condition |
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

### `GET /api/v1/canvas/panels` — List Canvas panels

Returns id/title/component_count/version only; panels can be null when empty.

Implementation: [`handleListPanels`](../../api/handler_misc.go#L1936). Registration: `s.canvasSvc != nil`.

| HTTP | Response / condition |
| --- | --- |
| 200 | List Canvas panels. Inspect business status in the response. `application/json` [AutomationPanels](#automationpanels) |
| 401 | Bearer authentication failed; see API overview. |

### `GET /api/v1/canvas/panels/{id}` — Read a Canvas panel

Returns the full component tree; props are interpreted by component type.

Implementation: [`handleGetPanel`](../../api/handler_misc.go#L1956). Registration: `s.canvasSvc != nil`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Read a Canvas panel. Inspect business status in the response. `application/json` [AutomationPanel](#automationpanel) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Panel not found. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/canvas/events` — Submit a Canvas component event

Passes panel_id/component_id/action/data to the panel's registered handler. Returns a new Panel or a message when no result is produced. A missing handler also returns 200 message, which does not prove that an interaction executed.

Implementation: [`handleCanvasEvent`](../../api/handler_misc.go#L1975). Registration: `s.canvasSvc != nil`.

Request body (required): `application/json`: [AutomationCanvasEventRequest](#automationcanvaseventrequest).

| HTTP | Response / condition |
| --- | --- |
| 200 | Submit a Canvas component event. Inspect business status in the response. `application/json` [AutomationPanel](#automationpanel) / [AutomationMessage](#automationmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON. `application/json` [AutomationError](#automationerror) |
| 500 | Registered event handler failed. `application/json` [AutomationError](#automationerror) |

Request example:

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

### `GET /api/v1/canvas/workflows` — List saved workflows

Returns complete definitions; an empty list may be null. Definitions persist to workflows.json; runs persist to workflow_runs.json, evicting older records above 1000 runs. They are not memory-only.

Implementation: [`handleListWorkflows`](../../api/handler_extended.go#L795). Registration: `Always registered.`.

| HTTP | Response / condition |
| --- | --- |
| 200 | List saved workflows. Inspect business status in the response. `application/json` [AutomationWorkflows](#automationworkflows) |
| 401 | Bearer authentication failed; see API overview. |

### `POST /api/v1/canvas/workflows` — Create or replace a workflow

name is required. An omitted ID generates wf-...; an existing ID replaces the definition, preserves created_at and refreshes updated_at. Save validates input/lengths; node IDs, references and cycles are validated on execution. Execution uses dependency stages with stable declaration order; condition selects branches. File persistence errors are currently logged rather than returned, so 200 alone does not prove durable persistence.

Implementation: [`handleSaveWorkflow`](../../api/handler_extended.go#L805). Registration: `Always registered.`.

Request body (required): `application/json`: [AutomationWorkflowData](#automationworkflowdata).

| HTTP | Response / condition |
| --- | --- |
| 200 | Create or replace a workflow. Inspect business status in the response. `application/json` [AutomationWorkflowData](#automationworkflowdata) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed JSON, blank name or input length violation. `application/json` [AutomationError](#automationerror) |

Request example:

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

### `DELETE /api/v1/canvas/workflows/{id}` — Delete a workflow definition

Deletes the definition and revokes workflow:<id> task grants; it does not mean that existing run records are removed.

Implementation: [`handleDeleteWorkflow`](../../api/handler_extended.go#L839). Registration: `Always registered.`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Delete a workflow definition. Inspect business status in the response. `application/json` [AutomationMessage](#automationmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Workflow not found. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/canvas/workflows/{id}/run` — Start a workflow run

Body is optional. Immediately returns a 200 running snapshot while execution continues in the background for up to 10 minutes; HTTP disconnect does not cancel it. Poll GET runs/{id} for completed/failed and actual output. May call models, MCP tools and other capabilities with real side effects.

Implementation: [`handleRunWorkflow`](../../api/handler_extended.go#L855). Registration: `Always registered.`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

Request body (optional): `application/json`: [AutomationRunWorkflowRequest](#automationrunworkflowrequest).

| HTTP | Response / condition |
| --- | --- |
| 200 | Start a workflow run. Inspect business status in the response. `application/json` [AutomationWorkflowRun](#automationworkflowrun) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed nonempty body. `application/json` [AutomationError](#automationerror) |
| 404 | Workflow not found. `application/json` [AutomationError](#automationerror) |

Request example:

```json
{
  "input": "Example input",
  "metadata": {
    "locale": "en"
  }
}
```

### `GET /api/v1/canvas/runs/{id}` — Read a workflow run

Reads the run snapshot and node_results. provider_display_name/model_id are frozen at an actual Agent-call boundary and can be null if no model call occurred.

Implementation: [`handleGetWorkflowRun`](../../api/handler_extended.go#L1268). Registration: `Always registered.`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Read a workflow run. Inspect business status in the response. `application/json` [AutomationWorkflowRun](#automationworkflowrun) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Run not found. `application/json` [AutomationError](#automationerror) |

### `POST /api/v1/canvas/runs/{id}/resume` — Resume from completed nodes

No body. Requires the prior run, its definition, and at least one completed node. Creates a new run reusing those outputs and reruns other nodes with the prior input. The current handler does not require a failed prior status and does not restore the original user/platform/chat/metadata request fields; it does not guarantee global idempotency of every side effect.

Implementation: [`handleResumeWorkflowRun`](../../api/handler_extended.go#L970). Registration: `Always registered.`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Resume from completed nodes. Inspect business status in the response. `application/json` [AutomationWorkflowRun](#automationworkflowrun) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | No completed nodes to reuse. `application/json` [AutomationError](#automationerror) |
| 404 | Prior run or source workflow not found. `application/json` [AutomationError](#automationerror) |

### `GET /api/v1/team/agents` — List shared Agent records

Reads shared configuration records from the local TeamStore; this is not a remote marketplace query.

Implementation: [`handleListSharedAgents`](../../api/handler_team.go#L169). Registration: `Always registered.`.

| HTTP | Response / condition |
| --- | --- |
| 200 | List shared Agent records. Inspect business status in the response. `application/json` [AutomationSharedAgents](#automationsharedagents) |
| 401 | Bearer authentication failed; see API overview. |

### `POST /api/v1/team/agents` — Add a shared Agent record

name is required; visibility defaults to private. Accepts other SharedAgent fields, generating ID/updated_at when omitted. Appends a local record, not a remote publication. File write failures are logged, so 200 does not prove persistence.

Implementation: [`handleShareAgent`](../../api/handler_team.go#L192). Registration: `Always registered.`.

Request body (required): `application/json`: [AutomationSharedAgent](#automationsharedagent).

| HTTP | Response / condition |
| --- | --- |
| 200 | Add a shared Agent record. Inspect business status in the response. `application/json` [AutomationSharedAgent](#automationsharedagent) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed body (64 KiB limit), blank name or invalid visibility. `application/json` [AutomationError](#automationerror) |

Request example:

```json
{
  "name": "Research helper",
  "visibility": "private",
  "description": "Example shared configuration",
  "config": {}
}
```

### `DELETE /api/v1/team/agents/{id}` — Delete a shared Agent record

Removes the record from the local TeamStore.

Implementation: [`handleDeleteSharedAgent`](../../api/handler_team.go#L213). Registration: `Always registered.`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Delete a shared Agent record. Inspect business status in the response. `application/json` [AutomationMessage](#automationmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Shared Agent not found. `application/json` [AutomationError](#automationerror) |

### `GET /api/v1/team/members` — List team member records

Reads local member records without calling an external identity service.

Implementation: [`handleListTeamMembers`](../../api/handler_team.go#L222). Registration: `Always registered.`.

| HTTP | Response / condition |
| --- | --- |
| 200 | List team member records. Inspect business status in the response. `application/json` [AutomationTeamMembers](#automationteammembers) |
| 401 | Bearer authentication failed; see API overview. |

### `POST /api/v1/team/members` — Add a team member record

Only nonempty email is required; email format is not validated. name defaults to the part before @ and role to member; ID/last_active are generated when absent. Appends a local member record without sending invitation email or creating a remote account.

Implementation: [`handleInviteTeamMember`](../../api/handler_team.go#L227). Registration: `Always registered.`.

Request body (required): `application/json`: [AutomationTeamMember](#automationteammember).

| HTTP | Response / condition |
| --- | --- |
| 200 | Add a team member record. Inspect business status in the response. `application/json` [AutomationTeamMember](#automationteammember) |
| 401 | Bearer authentication failed; see API overview. |
| 400 | Malformed body (64 KiB limit), blank email or invalid role. `application/json` [AutomationError](#automationerror) |

Request example:

```json
{
  "email": "member@example.com",
  "role": "member"
}
```

### `DELETE /api/v1/team/members/{id}` — Delete a team member record

Removes the local member record; does not revoke an external account.

Implementation: [`handleRemoveTeamMember`](../../api/handler_team.go#L252). Registration: `Always registered.`.

| Location | Parameter | Required | Type / notes |
| --- | --- | --- | --- |
| path | `id` | yes | `string`; —  |

| HTTP | Response / condition |
| --- | --- |
| 200 | Delete a team member record. Inspect business status in the response. `application/json` [AutomationMessage](#automationmessage) |
| 401 | Bearer authentication failed; see API overview. |
| 404 | Member not found. `application/json` [AutomationError](#automationerror) |

## Public callback protocols

| Type | Input and verification | Success and effects |
| --- | --- | --- |
| generic | JSON object or raw text (object decode failure is retained in payload.raw); `X-Webhook-Signature` or `X-Signature` contains raw-body HMAC-SHA256 hex, with optional `sha256=` prefix | `200 {"status":"accepted"}`; asynchronous handler may trigger a bound Cron job or Agent; not a terminal result |
| github | `X-Hub-Signature-256` as above; event name in `X-GitHub-Event` | `ping` or explicit test flag verifies/parses only, without dispatch or event statistics |
| gitlab | `X-Gitlab-Token` matches configured secret; payload.object_kind carries event type | Same ordinary webhook admission behavior |
| k12 | JSON `event_id` (or equal `delivery_id`), `event_type`, `payload`; unknown top-level fields rejected; three dedicated headers | `202 {"receipt":…}`; same event id/payload returns the prior receipt but requires a fresh nonce; conflicting payload returns 409 |
| wecom | GET query `msg_signature/timestamp/nonce/echostr`; POST encrypted XML with the first three query values; token/AES verification | Decrypted challenge on GET; POST acknowledgment then message handling; requires registered instance handler |
| wechat | GET query `signature/timestamp/nonce/echostr`; POST native/encrypted XML with `msg_signature` when applicable | Native challenge, success, or passive reply; verified using token and configured AES mode |
| slack | Native JSON `type/challenge/event`; `X-Slack-Request-Timestamp` and `X-Slack-Signature` | URL verification returns challenge; event_callback is acknowledged then processed asynchronously |
| line | Native JSON `destination/events`; `X-Line-Signature`, checked by the LINE SDK | POST acknowledgment then message processing; GET unsupported |
| whatsapp | GET `hub.mode=subscribe/hub.verify_token/hub.challenge`; POST `entry[].changes[].value` plus `X-Hub-Signature-256` | Echoes verification challenge; messages go to workers; unavailable worker returns 503 |

K12 signatures are `sha256=` plus the hex digest of `HMAC-SHA256(secret, timestamp + nonce + raw_body)`, without separators. The time window is ±5 minutes; nonce must be nonblank and at most 128 bytes. timestamp accepts RFC3339 or Unix seconds; use the actual transmission time. Existing binding/event/owner limits can return 429 with Retry-After. Never replay a nonce; `event_id` is the business idempotency identity.

K12 payloads cannot claim trusted owner/Agent/learner fields or contain remote URLs. Upload media first and pass asset_ref values scoped to the same owner. See the three payload schemas. Receipt states accepted/processing are not completion; do not blindly resend outcome_unknown. Receipt queries and explicit safe retries reuse this page's GET collection / PATCH item routes.

## Cron and workflow calls

See the [existing Cron reference](../api.en.md#cronjob) for complete action, draft, JobSpec and history fields. The unified endpoint and SSE creation endpoint coexist; Accept does not turn the unified endpoint into a stream.

```text
event: progress
data: {"stage":"analyzing","message":"Analyzing task"}

event: error
data: {"error":"Example compilation failure","stage":"validating"}
```

progress may repeat; done and error are mutually exclusive terminal events. done data is `{job,spec_preview}` using the Cron models. Save a workflow definition, run it, and poll its run record; inspect nodes and actual output. WorkflowNodeData describes node types and condition branches. Resume only reuses completed node outputs; it does not make every unknown tool outcome safe to retry.

## Field models

These tables share the OpenAPI schema source. Open objects are limited to genuinely extensible tool args, config extensions and event data; fixed requests/responses list their actual fields. Required output fields describe schema presence, not business success.

### AutomationConnectionSummary

Source: [api/handler_connections.go](../../api/handler_connections.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `provider` | `string` | yes | — |
| `name` | `string` | yes | — |
| `capabilities` | array&lt;`string`&gt; | yes | — |
| `status` | `string` | yes | — |
| `enabled` | `boolean` | yes | — |

### AutomationConnectionTestRequest

Source: [api/handler_connections.go](../../api/handler_connections.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `type` | `string` | yes | enum: `feishu`, `dingtalk`, `discord`, `telegram`, `wecom`, `wechat`, `slack`, `line`, `whatsapp`, `matrix`, `email`; Trimmed and converted to lowercase. |
| `config` | [AutomationFeishuConfig](#automationfeishuconfig) / [AutomationDingtalkConfig](#automationdingtalkconfig) / [AutomationWecomConfig](#automationwecomconfig) / [AutomationWechatConfig](#automationwechatconfig) / [AutomationSlackConfig](#automationslackconfig) / [AutomationTelegramConfig](#automationtelegramconfig) / [AutomationDiscordConfig](#automationdiscordconfig) / [AutomationLINEConfig](#automationlineconfig) / [AutomationWhatsAppConfig](#automationwhatsappconfig) / [AutomationMatrixConfig](#automationmatrixconfig) / [AutomationEmailTestConfig](#automationemailtestconfig) | yes | — |

### AutomationConnectionTestResponse

Source: [api/handler_connections.go](../../api/handler_connections.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `ok` | `boolean` | yes | — |
| `detail` | `string` | yes | — |

### AutomationConnectorCreateRequest

Source: [api/handler_connectors.go](../../api/handler_connectors.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `provider` | `string` | yes | enum: `github`, `notion` |
| `name` | `string` | no | maxLength: 64; Optional; trimmed; defaults to provider. |
| `token` | `string` | yes | Required nonblank token; at most 65536 UTF-8 bytes. Used for a real provider credential probe. |

### AutomationConnectorTestRequest

Source: [api/handler_connectors.go](../../api/handler_connectors.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `provider` | `string` | yes | enum: `github`, `notion` |
| `token` | `string` | yes | Required nonblank token; at most 65536 UTF-8 bytes. Used for a real provider credential probe. |

### AutomationCronParseRequest

Source: [api/handler_cron_parse.go](../../api/handler_cron_parse.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `text` | `string` | yes | minLength: 1; Nonblank natural-language schedule and task. |
| `hints` | object {`locale`: `string`} | no | — |

### AutomationCronParseResponse

Source: [api/handler_cron_parse.go](../../api/handler_cron_parse.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `draft` | `CronJobDraft` | no | — |
| `needs_clarification` | `boolean` | no | — |
| `suggestion` | `string` | no | — |
| `tier` | `integer` | yes | enum: `2` |

### AutomationAddCronJobRequest

Source: [api/handler_cron.go](../../api/handler_cron.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [api/handler_autonomy.go](../../api/handler_autonomy.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [api/handler_autonomy.go](../../api/handler_autonomy.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `task_ref` | `string` | yes | minLength: 1; Trimmed nonblank task reference, convention cron:<id>, webhook:<id>, workflow:<id>. |
| `source` | `string` | no | Trimmed lowercase; empty means any source. |
| `entries` | array&lt;`string`&gt; | yes | minItems: 1; Category/tool/glob entries; trimmed and deduplicated. Blank entries and bare * are discarded; at least one entry must remain. |
| `security_scope_digest` | `string` | no | Optional exact parameter-scope digest. Owner comes from trusted request context, never the body. |
| `note` | `string` | no | — |

### AutomationPreflightRequest

Source: [autonomy/preflight.go](../../autonomy/preflight.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `source` | `string` | yes | — |
| `task_ref` | `string` | no | — |
| `prompt` | `string` | no | — |
| `deliver` | `boolean` | no | — |
| `tools` | array&lt;`string`&gt; | no | — |
| `workflow_id` | `string` | no | Adds tools from the saved workflow when available; fills task_ref if absent. |
| `cron_job_id` | `string` | no | Loads source prompt and delivery only if prompt is blank and scheduler/job exists. |

### AutomationPreflightResult

Source: [autonomy/preflight.go](../../autonomy/preflight.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [autonomy/preflight.go](../../autonomy/preflight.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `category` | `string` | yes | — |
| `tools` | array&lt;`string`&gt; | yes | — |
| `state` | `string` | yes | enum: `auto`, `granted`, `approval` |

### AutomationToolVerdict

Source: [autonomy/preflight.go](../../autonomy/preflight.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `tool` | `string` | yes | — |
| `state` | `string` | yes | enum: `auto`, `granted`, `approval` |

### AutomationMatrixCell

Source: [autonomy/preflight.go](../../autonomy/preflight.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `category` | `string` | yes | — |
| `state` | `string` | yes | enum: `auto`, `approval` |

### AutomationMatrixRow

Source: [autonomy/preflight.go](../../autonomy/preflight.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `source` | `string` | yes | — |
| `cells` | array&lt;[AutomationMatrixCell](#automationmatrixcell)&gt; | yes | — |

### AutomationMatrixView

Source: [autonomy/preflight.go](../../autonomy/preflight.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `profile` | `string` | yes | — |
| `categories` | array&lt;`string`&gt; | yes | — |
| `rows` | array&lt;[AutomationMatrixRow](#automationmatrixrow)&gt; | yes | — |

### AutomationGrant

Source: [autonomy/grants.go](../../autonomy/grants.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [autonomy/decisions.go](../../autonomy/decisions.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [api/handler_instances.go](../../api/handler_instances.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `id` | `string` | no | Accepted but ignored by name-based upsert; by-id update uses path id. |
| `provider` | `string` | no | enum: ``, `feishu`, `dingtalk`, `discord`, `telegram`, `wecom`, `wechat`, `slack`, `line`, `whatsapp`, `matrix`, `email`; Required nonblank for name-based upsert; blank retains the old value for PUT by id. |
| `name` | `string` | no | maxLength: 64 |
| `enabled` | `boolean` | no | default: `false`; Omission means false, including PUT by id; it is not a partial boolean update. |
| `config` | [AutomationProviderConfig](#automationproviderconfig) | no | — |

### AutomationSendTestRequest

Source: [api/handler_instances.go](../../api/handler_instances.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `request_id` | `string` | no | Nonblank chooses bound-target mode; same instance/id replays the frozen result without resend. target is ignored in this mode. |
| `target` | `string` | no | Required with content only when request_id is blank; explicit external recipient/chat. |
| `content` | `string` | no | Required nonblank in direct mode; bound mode supplies a default test message when empty. |

### AutomationInstanceResponse

Source: [api/handler_instances.go](../../api/handler_instances.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [instances/manager.go](../../instances/manager.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [instances/manager.go](../../instances/manager.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [instances/test_delivery.go](../../instances/test_delivery.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `chat_id` | `string` | yes | — |
| `status` | `string` | yes | enum: `not_sent`, `sending`, `delivered`, `accepted`, `failed`, `outcome_unknown` |
| `external_message_id` | `string` | no | — |

### AutomationTestDeliveryResult

Source: [instances/test_delivery.go](../../instances/test_delivery.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `request_id` | `string` | yes | — |
| `success` | `boolean` | yes | — |
| `message` | `string` | yes | — |
| `pending` | `boolean` | yes | — |
| `deliveries` | array&lt;[AutomationTestDelivery](#automationtestdelivery)&gt; | yes | — |

### AutomationConnectorSummary

Source: [connector/connector.go](../../connector/connector.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `provider` | `string` | yes | enum: `github`, `notion` |
| `name` | `string` | yes | — |
| `created_at` | `string` | yes | format: date-time |

### AutomationConnectorResource

Source: [connector/connector.go](../../connector/connector.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `title` | `string` | yes | — |
| `url` | `string` | no | — |
| `kind` | `string` | no | — |
| `desc` | `string` | no | — |

### AutomationWebhook

Source: [webhook/webhook.go](../../webhook/webhook.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [webhook/k12.go](../../webhook/k12.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [webhook/k12.go](../../webhook/k12.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [webhook/k12.go](../../webhook/k12.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `event_id` | `string` | no | maxLength: 200; Stable nonblank event id, or delivery_id as alias; both must match if provided. |
| `delivery_id` | `string` | no | Alias for event_id; same value required when both are present. |
| `event_type` | `string` | yes | enum: `k12.submission.requested.v1`, `k12.practice_return.requested.v1`, `k12.workflow_run.requested.v1` |
| `payload` | [AutomationK12SubmissionPayload](#automationk12submissionpayload) / [AutomationK12PracticePayload](#automationk12practicepayload) / [AutomationK12WorkflowPayload](#automationk12workflowpayload) | yes | Shape selected by event_type. Remote URLs and recursively supplied agent_id/learner_id/owner_id/user_id/job_id/execution_id/conversation_scope/scope are rejected. |

### AutomationRegisterWebhookRequest

Source: [api/handler_webhook.go](../../api/handler_webhook.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [api/handler_webhook.go](../../api/handler_webhook.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `enabled` | `boolean` / `null` | no | — |
| `rotate_secret` | `boolean` | no | — |
| `allowed_events` | array&lt;`string`&gt; | no | — |
| `allowed_workflows` | array&lt;`string`&gt; | no | Applied together with allowed_events; alone is not an accepted update. |
| `retry_receipt_id` | `string` | no | K12 only; exclusive with enabled, allowed_events, rotate_secret and nonempty allowed_workflows. Only failed receipts with retryable=true can retry. |

### AutomationComponent

Source: [canvas/canvas.go](../../canvas/canvas.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `type` | `string` | yes | enum: `markdown`, `chart`, `form`, `table`, `kanban`, `buttons`, `progress`, `image` |
| `props` | `string` / [AutomationChartProps](#automationchartprops) / [AutomationTableProps](#automationtableprops) / [AutomationButtonsProps](#automationbuttonsprops) / [AutomationFormProps](#automationformprops) / [AutomationProgressProps](#automationprogressprops) / [AutomationImageProps](#automationimageprops) / object (map) | yes | Type-specific props; markdown can be text; custom/kanban props remain producer-defined. |
| `events` | array&lt;`string`&gt; | no | — |
| `children` | array&lt;[AutomationComponent](#automationcomponent) / `null`&gt; | no | — |

### AutomationPanel

Source: [canvas/canvas.go](../../canvas/canvas.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `title` | `string` | yes | — |
| `components` | array&lt;[AutomationComponent](#automationcomponent) / `null`&gt; / `null` | yes | A newly created empty panel can return null. |
| `version` | `integer` | yes | — |

### AutomationChartProps

Source: [canvas/canvas.go](../../canvas/canvas.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `chart_type` | `string` | yes | — |
| `title` | `string` | yes | — |
| `labels` | array&lt;`string`&gt; | yes | — |
| `datasets` | array&lt;[AutomationDataset](#automationdataset)&gt; | yes | — |

### AutomationDataset

Source: [canvas/canvas.go](../../canvas/canvas.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `label` | `string` | yes | — |
| `data` | array&lt;`number`&gt; | yes | — |
| `color` | `string` | no | — |

### AutomationTableProps

Source: [canvas/canvas.go](../../canvas/canvas.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `headers` | array&lt;`string`&gt; | yes | — |
| `rows` | array&lt;array&lt;`string`&gt;&gt; | yes | — |

### AutomationButtonsProps

Source: [canvas/canvas.go](../../canvas/canvas.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `buttons` | array&lt;[AutomationButtonDef](#automationbuttondef)&gt; | yes | — |

### AutomationButtonDef

Source: [canvas/canvas.go](../../canvas/canvas.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `label` | `string` | yes | — |
| `action` | `string` | yes | — |
| `style` | `string` | no | — |

### AutomationFormField

Source: [canvas/canvas.go](../../canvas/canvas.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `name` | `string` | yes | — |
| `label` | `string` | yes | — |
| `type` | `string` | yes | — |
| `placeholder` | `string` | no | — |
| `required` | `boolean` | no | — |
| `options` | array&lt;`string`&gt; | no | — |
| `default` | `string` | no | — |

### AutomationFormProps

Source: [canvas/canvas.go](../../canvas/canvas.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `fields` | array&lt;[AutomationFormField](#automationformfield)&gt; | yes | — |
| `submit_text` | `string` | yes | — |

### AutomationProgressProps

Source: [canvas/canvas.go](../../canvas/canvas.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `value` | `number` | yes | — |
| `label` | `string` | yes | — |
| `color` | `string` | yes | — |

### AutomationImageProps

Source: [canvas/canvas.go](../../canvas/canvas.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `url` | `string` | yes | — |
| `alt` | `string` | no | — |
| `width` | `integer` | no | — |
| `height` | `integer` | no | — |

### AutomationCanvasEventRequest

Source: [api/handler_misc.go](../../api/handler_misc.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `panel_id` | `string` | no | — |
| `component_id` | `string` | no | — |
| `action` | `string` | no | — |
| `data` | object (map) | no | — |

### AutomationWorkflowData

Source: [api/handler_extended.go](../../api/handler_extended.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `id` | `string` | no | Omit to create; existing id replaces the saved definition. Server sets created_at/updated_at. |
| `name` | `string` | yes | maxLength: 64 |
| `description` | `string` | no | maxLength: 2000 |
| `nodes` | array&lt;[AutomationWorkflowNode](#automationworkflownode)&gt; / `null` | no | Omitted/null is retained as an empty definition and can be returned as null. |
| `edges` | array&lt;[AutomationWorkflowEdge](#automationworkflowedge)&gt; / `null` | no | Omitted/null can be returned as null. |
| `data` | object (map) | no | — |
| `created_at` | `string` | no | format: date-time |
| `updated_at` | `string` | no | format: date-time |

### AutomationWorkflowRun

Source: [api/handler_extended.go](../../api/handler_extended.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [api/workflow_runtime.go](../../api/workflow_runtime.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `input` | `string` | no | — |
| `user_id` | `string` | no | Agent nodes default to workflow-<workflow_id> when empty. |
| `platform` | `string` | no | — |
| `instance_id` | `string` | no | — |
| `chat_id` | `string` | no | — |
| `metadata` | object (map) | no | String map. Reserved dispatch keys are removed; source and workflow ids are server-stamped. locale defaults to und. |

### AutomationWorkflowNodeRun

Source: [api/workflow_runtime.go](../../api/workflow_runtime.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [api/handler_team.go](../../api/handler_team.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [api/handler_team.go](../../api/handler_team.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `id` | `string` | no | Generated tm-... when omitted. |
| `name` | `string` | no | Defaults to email prefix before @. |
| `email` | `string` | yes | — |
| `role` | `string` | no | enum: `admin`, `member`, `viewer`; default: `"member"` |
| `avatar` | `string` | no | — |
| `last_active` | `string` | no | Generated RFC3339 UTC timestamp when omitted. |

### AutomationAttachmentRef

Source: [messagecontent/messagecontent.go](../../messagecontent/messagecontent.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `asset_id` | `string` | yes | — |
| `name` | `string` | no | — |
| `mime` | `string` | yes | — |
| `digest` | `string` | yes | — |
| `alt_text` | `string` | no | — |

### AutomationMessageContent

Source: [messagecontent/messagecontent.go](../../messagecontent/messagecontent.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `content_id` | `string` | yes | — |
| `content_version` | `string` | yes | — |
| `producer_kind` | `string` | yes | — |
| `markdown` | `string` | yes | — |
| `source_digest` | `string` | yes | — |
| `locale` | `string` | yes | — |
| `attachments` | array&lt;[AutomationAttachmentRef](#automationattachmentref)&gt; | no | — |

### AutomationCapabilitySnapshot

Source: [messagecontent/messagecontent.go](../../messagecontent/messagecontent.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `markdown` | `boolean` | yes | — |
| `tex_math` | `boolean` | yes | — |
| `mathml` | `boolean` | no | — |
| `unicode_math` | `boolean` | no | — |
| `attachments` | `boolean` | no | — |
| `max_runes` | `integer` | no | — |

### AutomationRenderPart

Source: [messagecontent/messagecontent.go](../../messagecontent/messagecontent.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `kind` | `string` | yes | — |
| `text` | `string` | no | — |
| `artifact_ref` | `string` | no | — |
| `artifact_digest` | `string` | no | — |
| `alt_text` | `string` | no | — |

### AutomationRenderManifest

Source: [messagecontent/messagecontent.go](../../messagecontent/messagecontent.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [config/config.go](../../config/config.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |

### AutomationDiscordConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: token. name is overridden by the instance name; outer enabled controls lifecycle.

Source: [config/config.go](../../config/config.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |

### AutomationSlackConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: token. name is overridden by the instance name; outer enabled controls lifecycle.

Source: [config/config.go](../../config/config.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `webhook_port` | `integer` | no | — |
| `token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `signing_secret` | `string` | no |  Credential; list/update responses mask stored values. |

### AutomationFeishuConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: app_id, app_secret. name is overridden by the instance name; outer enabled controls lifecycle.

Source: [config/config.go](../../config/config.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `webhook_port` | `integer` | no | — |
| `app_id` | `string` | no | Required for adapter credential validation/start. |
| `app_secret` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `verification_token` | `string` | no |  Credential; list/update responses mask stored values. |

### AutomationDingtalkConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: app_key, app_secret, robot_code. name is overridden by the instance name; outer enabled controls lifecycle.

Source: [config/config.go](../../config/config.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `webhook_port` | `integer` | no | — |
| `app_key` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `app_secret` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `robot_code` | `string` | no | Required for adapter credential validation/start. |

### AutomationWechatConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: app_id, app_secret, token. name is overridden by the instance name; outer enabled controls lifecycle.

Source: [config/config.go](../../config/config.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `app_id` | `string` | no | Required for adapter credential validation/start. |
| `app_secret` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `aes_key` | `string` | no |  Credential; list/update responses mask stored values. |

### AutomationWecomConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: corp_id, agent_id, secret, token, aes_key. name is overridden by the instance name; outer enabled controls lifecycle.

Source: [config/config.go](../../config/config.go).

| Field | Type | Required | Constraints / defaults / notes |
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

Source: [config/config.go](../../config/config.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `phone_id` | `string` | no | Required for adapter credential validation/start. |
| `verify_token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `app_secret` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |

### AutomationLINEConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: channel_secret, channel_token. name is overridden by the instance name; outer enabled controls lifecycle.

Source: [config/config.go](../../config/config.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `channel_secret` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `channel_token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |

### AutomationMatrixConfig

Fields marked required below are required for adapter credential validation/start, not for saving a disabled instance: homeserver_url, access_token, user_id. name is overridden by the instance name; outer enabled controls lifecycle.

Source: [config/config.go](../../config/config.go).

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `name` | `string` | no | — |
| `enabled` | `boolean` | no | — |
| `homeserver_url` | `string` | no | Required for adapter credential validation/start. |
| `access_token` | `string` | no | Required for adapter credential validation/start. Credential; list/update responses mask stored values. |
| `user_id` | `string` | no | Required for adapter credential validation/start. |

### AutomationSSEProgress

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `stage` | `string` | yes | enum: `analyzing`, `calling_llm`, `validating`, `persisting` |
| `message` | `string` | yes | — |

### AutomationSSEDone

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `job` | `CronJob` | yes | — |
| `spec_preview` | `JobSpec` | yes | — |

### AutomationSSEError

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `error` | `string` | yes | — |
| `stage` | `string` | yes | Failed compile stage, or empty when unknown. |

### AutomationProfile

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `profile` | `string` | yes | enum: `function_first`, `balanced`, `strict`, `full_access` |
| `profiles` | array&lt;`string`&gt; | no | — |
| `matrix` | [AutomationMatrixView](#automationmatrixview) | yes | — |

### AutomationProfileUpdate

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `profile` | `string` | yes | enum: `function_first`, `balanced`, `strict`, `full_access` |

### AutomationCapabilityStatus

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `enabled` | `boolean` | yes | — |
| `state` | `string` | yes | enum: `disabled`, `unavailable`, `ready` |

### AutomationStatus

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `cron` | [AutomationCapabilityStatus](#automationcapabilitystatus) | yes | — |
| `webhook` | [AutomationCapabilityStatus](#automationcapabilitystatus) | yes | — |

### AutomationSummaryCounts

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `tasks` | `integer` | yes | — |
| `ready` | `integer` | yes | — |
| `pending` | `integer` | yes | — |
| `grants` | `integer` | yes | — |

### AutomationSummary

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `profile` | `string` | yes | enum: `function_first`, `balanced`, `strict`, `full_access` |
| `counts` | [AutomationSummaryCounts](#automationsummarycounts) | yes | — |
| `pending` | array&lt;[AutomationTaskStatus](#automationtaskstatus)&gt; | yes | — |
| `tasks` | array&lt;[AutomationTaskStatus](#automationtaskstatus)&gt; | yes | — |

### AutomationSMTPConfig

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `host` | `string` | no | — |
| `port` | `integer` | no | default: `587` |
| `username` | `string` | no | — |
| `password` | `string` | no | — |
| `from` | `string` | no | — |

### AutomationIMAPConfig

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `host` | `string` | no | — |
| `port` | `integer` | no | default: `993` |
| `username` | `string` | no | — |
| `password` | `string` | no | — |
| `tls` | `boolean` | no | Connection test defaults true; nested saved EmailConfig retains the supplied boolean. |
| `folder` | `string` | no | default: `"INBOX"` |

### AutomationEmailConfig

Use nested smtp/imap, or flat email/password/smtp_host/smtp_port/imap_host/imap_port form. Flat fallback applies only when both nested hosts are empty; ports in flat form are strings. Flat form sets IMAP TLS true.

| Field | Type | Required | Constraints / defaults / notes |
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

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `smtp` | [AutomationSMTPConfig](#automationsmtpconfig) | yes | — |
| `imap` | [AutomationIMAPConfig](#automationimapconfig) | no | — |

### AutomationProviderConfig

Choose fields by outer provider. Config may be empty for a disabled stored instance. Credential-validation requirements are stated in the matching provider schema.

[AutomationFeishuConfig](#automationfeishuconfig) / [AutomationDingtalkConfig](#automationdingtalkconfig) / [AutomationWecomConfig](#automationwecomconfig) / [AutomationWechatConfig](#automationwechatconfig) / [AutomationSlackConfig](#automationslackconfig) / [AutomationTelegramConfig](#automationtelegramconfig) / [AutomationDiscordConfig](#automationdiscordconfig) / [AutomationLINEConfig](#automationlineconfig) / [AutomationWhatsAppConfig](#automationwhatsappconfig) / [AutomationMatrixConfig](#automationmatrixconfig) / [AutomationEmailConfig](#automationemailconfig)

### AutomationInstanceTestResponse

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `success` | `boolean` | yes | — |
| `message` | `string` | yes | — |
| `name` | `string` | yes | — |
| `provider` | `string` | yes | — |
| `status` | `string` | yes | — |
| `last_error` | `string` | yes | — |

### AutomationChannelTestResponse

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `success` | `boolean` | yes | — |
| `provider` | `string` | yes | — |
| `message` | `string` | yes | — |

### AutomationDirectSendResult

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `success` | `boolean` | yes | — |
| `message` | `string` | no | — |
| `id` | `string` | no | — |
| `name` | `string` | no | — |
| `pending` | `boolean` | no | — |
| `error` | `string` | no | — |

### AutomationWecomGuide

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `steps` | array&lt;object {`step`: `string`, `title`: `string`, `description`: `string`}&gt; | yes | — |
| `required_fields` | array&lt;object {`field`: `string`, `label`: `string`, `placeholder`: `string`}&gt; | yes | — |
| `callback_url` | `string` | yes | — |

### AutomationK12SubmissionPayload

Requires nonblank text or a nonempty asset_refs array. Asset refs must be owner-scoped uploads.

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `text` | `string` | no | — |
| `asset_refs` | array&lt;`string`&gt; | no | — |

### AutomationK12ReturnAsset

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `asset_ref` | `string` | yes | minLength: 1 |
| `item_ids` | array&lt;`string`&gt; | yes | — |

### AutomationK12PracticePayload

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `paper_no` | `string` | yes | minLength: 1 |
| `return_assets` | array&lt;[AutomationK12ReturnAsset](#automationk12returnasset)&gt; | yes | — |

### AutomationK12WorkflowPayload

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `workflow_id` | `string` | yes | minLength: 1 |
| `workflow_version` | `string` | yes | minLength: 1 |
| `input` | `string` | no | — |

### AutomationWebhookCreated

| Field | Type | Required | Constraints / defaults / notes |
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

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `name` | `string` | yes | — |
| `enabled` | `boolean` | yes | — |
| `binding` | [AutomationK12Binding](#automationk12binding) | no | — |
| `secret` | `string` | no | — |

### AutomationReceiptResponse

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `receipt` | [AutomationK12Receipt](#automationk12receipt) | yes | — |

### AutomationWebhookAccepted

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `status` | `string` | yes | enum: `accepted`, `test`, `disabled` |
| `signature` | `string` | no | — |
| `event_type` | `string` | no | — |
| `summary` | `string` | no | — |
| `dispatched` | `boolean` | no | — |
| `detail` | `string` | no | — |

### AutomationWebhookRejection

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `error` | `string` | yes | — |
| `receipt` | [AutomationK12Receipt](#automationk12receipt) | no | — |
| `status` | `string` | no | enum: `rejected` |
| `failure_kind` | `string` | no | — |

### AutomationPanelSummary

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `title` | `string` | yes | — |
| `component_count` | `integer` | yes | — |
| `version` | `integer` | yes | — |

### AutomationConditionRule

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `op` | `string` | yes | enum: `eq`, `ne`, `contains`, `not_contains`, `gt`, `lt`, `gte`, `lte`, `regex`, `empty`, `not_empty` |
| `value` | `string` | no | — |
| `target` | `string` | yes | — |

### AutomationWorkflowNodeData

input uses prompt/value; agent uses role/agent and provider/model; tool uses tool/name plus args; handoff uses to_agent/agent/role or candidates; parallel uses roles; condition uses source/conditions/default. Unknown keys are retained.

| Field | Type | Required | Constraints / defaults / notes |
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

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `id` | `string` | yes | — |
| `type` | `string` | no | input/agent/handoff/agent_handoff/parallel/fanout/tool/output/condition. Missing type becomes noop; other types pass input through. |
| `label` | `string` | no | maxLength: 64 |
| `data` | [AutomationWorkflowNodeData](#automationworkflownodedata) | no | — |
| `config` | [AutomationWorkflowNodeData](#automationworkflownodedata) | no | — |
| `position` | object {`x`: `number`, `y`: `number`} | no | — |

### AutomationWorkflowEdge

source/target take priority over aliases from/to. Missing endpoints are skipped; unknown node references fail at execution.

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `id` | `string` | no | — |
| `source` | `string` | no | — |
| `target` | `string` | no | — |
| `from` | `string` | no | — |
| `to` | `string` | no | — |

### AutomationMessage

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `message` | `string` | yes | — |

### AutomationNamedMessage

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `message` | `string` | yes | — |
| `name` | `string` | no | — |
| `id` | `string` | no | — |

### AutomationError

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `error` | `string` | yes | — |

### AutomationConnections

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `connections` | array&lt;[AutomationConnectionSummary](#automationconnectionsummary)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationConnectors

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `connectors` | array&lt;[AutomationConnectorSummary](#automationconnectorsummary)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationConnectorResources

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `resources` | array&lt;[AutomationConnectorResource](#automationconnectorresource)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationInstances

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `instances` | array&lt;[AutomationInstance](#automationinstance)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationInstanceHealth

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `instances` | array&lt;[AutomationHealthReport](#automationhealthreport)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationDecisions

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `decisions` | array&lt;[AutomationDecision](#automationdecision)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationGrants

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `grants` | array&lt;[AutomationGrant](#automationgrant)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationReceipts

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `receipts` | array&lt;[AutomationK12Receipt](#automationk12receipt)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationPanels

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `panels` | array&lt;[AutomationPanelSummary](#automationpanelsummary)&gt; / `null` | yes | — |
| `total` | `integer` | yes | — |

### AutomationWorkflows

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `workflows` | array&lt;[AutomationWorkflowData](#automationworkflowdata)&gt; / `null` | yes | — |
| `total` | `integer` | yes | — |

### AutomationSharedAgents

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `agents` | array&lt;[AutomationSharedAgent](#automationsharedagent)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationTeamMembers

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `members` | array&lt;[AutomationTeamMember](#automationteammember)&gt; | yes | — |
| `total` | `integer` | yes | — |

### AutomationWebhooks

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `webhooks` | array&lt;[AutomationWebhook](#automationwebhook) / [AutomationK12Binding](#automationk12binding)&gt; | yes | — |
| `k12_bindings` | array&lt;[AutomationK12Binding](#automationk12binding)&gt; / `null` | no | — |
| `total` | `integer` | yes | — |

### AutomationSlackCallback

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `type` | `string` | no | url_verification or event_callback |
| `challenge` | `string` | no | — |
| `event` | object {`type`: `string`, `user`: `string`, `text`: `string`, `channel`: `string`, `ts`: `string`, `bot_id`: `string`} | no | — |

### AutomationLINECallback

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `destination` | `string` | no | — |
| `events` | array&lt;object {`type`: `string`, `replyToken`: `string`, `timestamp`: `integer`, `source`: object {`type`: `string`, `userId`: `string`, `groupId`: `string`, `roomId`: `string`}, `message`: object {`id`: `string`, `type`: `string`, `text`: `string`}}&gt; | no | — |

### AutomationWhatsAppMessage

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `id` | `string` | no | — |
| `from` | `string` | no | — |
| `type` | `string` | no | — |
| `text` | object {`body`: `string`} | no | — |

### AutomationWhatsAppCallback

| Field | Type | Required | Constraints / defaults / notes |
| --- | --- | --- | --- |
| `entry` | array&lt;object {`id`: `string`, `changes`: array&lt;object {`value`: object {`messages`: array&lt;[AutomationWhatsAppMessage](#automationwhatsappmessage)&gt;, `contacts`: array&lt;object {`wa_id`: `string`, `profile`: object {`name`: `string`}}&gt;}}&gt;}&gt; | no | — |

### AutomationPlatformCallback

Selected by provider; WeCom/WeChat instead use native XML callbacks. Native protocol extension fields are retained/ignored according to the adapter.

[AutomationSlackCallback](#automationslackcallback) / [AutomationLINECallback](#automationlinecallback) / [AutomationWhatsAppCallback](#automationwhatsappcallback)
