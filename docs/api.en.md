# HexClaw public API

[中文](api.md) · [Back to README](../README.en.md)

This is the public API entry point for the current source. Module references cover explicit main-service and desktop-integration routes; K12 has its own reference. See [OpenAPI](../api/openapi.yaml) for the machine-readable description. Releases use their matching code and documentation. Feature switches, injected dependencies, and the runtime environment determine the endpoints available in a deployment.

## Browse by module

| Module | Complete reference |
| --- | --- |
| Chat, attachments, sessions, messages, stream recovery, and memory | [Chat and sessions](api/chat-sessions-memory.en.md) |
| Knowledge, ingestion, recovery, Embedding, document extraction, and rendering | [Knowledge and documents](api/knowledge-documents.en.md) |
| Model settings, probes, Ollama, media, logs, system, and desktop bridging | [Models and system](api/models-system-media.en.md) |
| Agents, routing, Prompts, MCP, Skills, marketplace, and tool observation | [Agents and integrations](api/agents-integrations.en.md) |
| Cron, Webhooks, autonomy, connections, IM, workflows, and teams | [Automation and connections](api/automation-connections.en.md) |
| K12 profiles, textbooks, image tasks, practice, and printing | [K12 API](../scenarios/k12/API.en.md) |

Each module documents request fields, success and error responses, availability, and actual side effects. The examples below cover a first chat and Cron call; the README keeps only common entry points.

## Connection and identity

The default service address is `http://127.0.0.1:16060`. For a cloud backend, use its actual address, including any proxy path prefix. Business requests require `Authorization: Bearer <token>`; JSON requests use `Content-Type: application/json`.

`hexclaw init` generates `server.api_token` in `~/.hexclaw/hexclaw.yaml`. Starting the standalone service directly also fills a missing token while preserving an existing value. With a custom configuration, use the token in that file. Desktop's native layer manages its own local connection token. These tokens are not interchangeable.

Chat body fields `user_id` and `platform` remain decode-compatible; they do not switch the authenticated identity. Domain fields such as `agent` and record IDs select business objects. Their validation and missing-object behavior are operation-specific and do not let callers select arbitrary owners.

The commands below assume a running service and these environment variables:

```bash
export HEXCLAW_API_BASE="http://127.0.0.1:16060"
export HEXCLAW_API_TOKEN="the current service token"
```

These endpoints bypass the ordinary business Bearer check: `GET /health`, `GET /api/v1/version`, `POST /api/v1/webhooks/{name}`, and `GET/POST /api/v1/platforms/hooks/{provider}/{name}`. Callbacks still use their configured Secret or platform signature mechanism; see [automation and connections](api/automation-connections.en.md). Health does not prove model, execution, or delivery availability.

`/api/internal/desktop/*` requires a native Sidecar capability and a loopback source; an ordinary business token is insufficient. These endpoints appear in the internal bridging section of [models and system](api/models-system-media.en.md) and are not stable third-party integration entry points.

Disabled modules may omit routes with `404`, retain empty projections, or return an error indicator. Each module documents its behavior. HTTP `200` does not always mean business success.

## Common calling conventions

- **IDs and scope:** use IDs as returned strings; not every ID is a UUID. Instance names in paths, queries, and bodies have operation-specific roles.
- **Request formats:** JSON, multipart, binary uploads, and WebSocket upgrades are distinct. Modules document metadata ordering, file limits, and streaming frames.
- **Asynchronous tasks:** acceptance, running, frozen results, and delivery are separate states. Read status and outputs using returned operation/task IDs; query an unknown outcome before replaying a request.
- **Errors:** most JSON errors contain `error`; some include `code/message/retryable` or domain state. Errors after SSE opens appear in frames. Distinguish binary downloads from JSON fallback responses by their actual Content-Type.
- **Revisions and idempotency:** `version/revision`, `If-Match`, `Idempotency-Key`, and body keys are not one shared mechanism. Required fields, scope, and replay behavior depend on the operation.
- **Browser and native clients:** current CORS responses cover existing local development and Tauri Origins, advertising `GET, POST, PUT, DELETE, OPTIONS` without `PATCH`. Registered endpoints alone do not establish browser cross-origin support. OPTIONS preflight returns `204`. GET routes receive Go ServeMux HEAD matching, but authentication still checks the actual request method: for example, `HEAD /api/v1/version` requires a business token. These are not separately listed business operations.
- **Extensions:** dynamically mounted third-party scenarios and plugins maintain their own contracts. This reference covers the repository's main service, desktop integration, and K12, rather than enumerating external extensions in advance.

<a id="chat"></a>
## Chat

### Request

`POST /api/v1/chat` requires a non-empty `message` or valid `attachments`. The default response is complete JSON. This handler limits the JSON body to 20 MiB.

| Field | Contract |
| --- | --- |
| `message` | Text input; may be empty with valid attachments |
| `session_id` | Omit or leave empty to create a session; reuse the returned ID to continue it |
| `provider`, `model` | Optional explicit selections from the current available configuration |
| `role` | Optional Agent role |
| `temperature`, `max_tokens` | Optional sampling overrides; omitted values follow model/Agent settings |
| `metadata` | String key-value pairs; the service handles reserved internal dispatch fields |
| `request_id` | Optional correlation and streaming recovery ID, not a safe replay guarantee |
| `attachments` | Image array; use staged `attachment_id` references or type, MIME, Base64 data/URL, subject to service validation |

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/chat" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message":"Hello, tell me what you can do"}'
```

### Response

Success is `200 application/json`. This illustrates primary fields, not fixed model wording:

```json
{
  "reply": "The model's answer",
  "session_id": "the returned session ID",
  "reasoning_disclosure": {}
}
```

Optional fields include `message_content`, `render_manifest`, `usage`, `tool_calls`, `blocks`, `knowledge_hits`, `memory_hits`, message IDs, sequence numbers, and runtime events. Use structured messages and artifact fields for presentation and delivery; HTTP success alone does not prove a file or delivery completed.

Continue the same conversation with the actual returned ID:

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/chat" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message":"Continue the previous topic","session_id":"the previous session_id"}'
```

### Streaming

Add `Accept: text/event-stream` and use `curl -N`. The same endpoint emits `data:` frames containing reply chunk JSON, ending successful streams with `data: [DONE]`.

Error frames contain `error`, `done: true`, and optional `code` and `retryable`. Opening SSE commits HTTP `200`, so clients must consume the success or error terminal state. `retryable` classifies an error; it does not authorize automatic replay when the model call outcome is unknown.

### Errors

Chat generally returns `{"error":"..."}`. Classified model errors also contain `code` and `retryable`; gateway rejection may include `code` and `layer`.

| HTTP | Cause |
| --- | --- |
| `400` | Invalid JSON, empty input, invalid attachment, explicit Provider/model, or sampling parameters |
| `401` | Missing or invalid current service token |
| `403` | Rejection by the configured gateway policy |
| `404` | Unavailable staged attachment reference |
| `422` | `MODEL_CAPABILITY_MISMATCH` |
| `429` | `UPSTREAM_RATE_LIMITED` |
| `500` | Unclassified engine or application error |
| `503` | Engine not ready, `UPSTREAM_UNAVAILABLE`, or `UPSTREAM_POOL_EXHAUSTED` |

<a id="cronjob"></a>
## Scheduled tasks

### Unified endpoint

`POST /api/v1/cronjob` uses `action` to select an operation. Successful operations return `200 application/json`; this endpoint does not switch to SSE based on `Accept`, and the JSON body limit is 1 MiB.

| action | Input | Success fields |
| --- | --- | --- |
| `create` | `draft` | `action`, `job`, `quota`, `ok: true` |
| `update` | `job_id` and a complete `draft` | `action`, replacement `job`, `quota`, `ok: true` |
| `list` | Optional `include_paused`, default `false` | `action`, `jobs`, `total`, `quota` |
| `pause`, `resume`, `remove` | `job_id` | `action`, `ok: true` |
| `run` | `job_id` | `action`, `ok: true`; trigger accepted only |

Empty `jobs` and `total: 0` may be omitted; clients should interpret them as an empty array and zero. The current active-job limit is `30`. Resume paused jobs before using `run`.

Mutations may supply `idempotency_key`. This is an owner-scoped, process-local, five-minute successful-response cache. A hit returns `X-Idempotent-Replay: 1`. All mutation actions share owner/key, so use different keys for different operations. Errors are not cached; restarts clear the cache. It is not durable or concurrent-admission idempotency.

### draft

`create` and `update` require non-empty `name`, `schedule`, and `prompt`. Update replaces the complete definition; omitted fields are not inherited automatically.

| Field | Contract |
| --- | --- |
| `schedule` | Cron expression or supported forms such as `@daily` and `@every` |
| `script`, `runtime` | A non-empty script admits pre-authored code without model compilation; runtime defaults to `starlark`, whose output contract requires `emit({...})` or `wake_agent(...)`; `python3` is available when supported by the environment |
| No `script` | The service selects script compilation or Agent mode by prompt; compilation needs a usable model |
| `continuous` | Continuous tasks without a script use Agent mode |
| `deliver`, `chat_id` | Configured delivery destinations; IM delivery requires a target conversation ID |
| `timeout_s` | `0` selects the runtime's default |
| `paused` | Initial paused state |

Although parsing-layer drafts can contain `no_agent` and `enabled_toolsets`, the unified create/update handler currently does not consume them. They cannot guarantee the execution mode or tool scope.

Create a pre-authored script, initially paused:

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/v1/cronjob" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"action":"create","idempotency_key":"daily-status-create-1","draft":{"name":"Daily status example","schedule":"0 9 * * *","prompt":"Print the daily status","runtime":"starlark","script":"emit({\"status\":\"success\",\"data\":{\"message\":\"ready\"}})","paused":true}}'
```

Response excerpt:

```json
{
  "action": "create",
  "job": {"id": "the actual job ID", "status": "paused", "spec": {"runtime": "starlark"}},
  "quota": {"used": 1, "limit": 30},
  "ok": true
}
```

List jobs with `{"action":"list","include_paused":true}`. Using the actual `job.id`, send `{"action":"resume","job_id":"...","idempotency_key":"resume-1"}`, then `{"action":"run","job_id":"...","idempotency_key":"run-1"}`.

A successful `run` response means acceptance, not completed execution or delivery. Read `GET /api/v1/cron/jobs/{id}/history?limit=20` and inspect actual `status`, `result`, `error`, `stdout`, `stderr`, and `exit_code`. History returns `{"history":[...],"total":...}`, newest first; history `id` is encoded as a JSON string. The default limit is `50`.

Streaming creation retains its separate `POST /api/v1/cron/jobs/stream` path. The old collection create/list, per-job `/pause`, `/resume`, `/trigger`, and DELETE routes are not current public operation entry points.

### Errors

Cron handler errors return `{"code":"...","message":"...","error":"..."}`, with compatibility `error` equal to `message`. Authentication failures still use `{"error":"..."}`.

| HTTP | code / Cause |
| --- | --- |
| `400` | `BAD_REQUEST`, `CRON_UNKNOWN_ACTION`, `CRON_INVALID_SCHEDULE`, `CRON_COMPILE_FAILED`, `CRON_VALIDATE_FAILED` |
| `401` | Invalid current service token |
| `404` | Job missing or outside the current owner: update uses `BAD_REQUEST`, lifecycle actions use `CRON_JOB_NOT_FOUND` |
| `409` | `CRON_JOB_PAUSED` |
| `429` | `CRON_QUOTA_EXCEEDED` |
| `500` | `INTERNAL_ERROR`, including unclassified compilation, replacement, or storage failures |
| `503` | `CRON_EXECUTOR_UNAVAILABLE`; a handler without an injected scheduler can return `CRON_DISABLED`, while ordinary disabled modules have no mounted route |

## Implementation references

[Chat and routes](../api/server.go) · [Unified Cron operations](../api/handler_cronjob_unified.go) · [Cron errors](../api/errors.go). These explain the implementation; use this document and OpenAPI as the public integration entry points.
