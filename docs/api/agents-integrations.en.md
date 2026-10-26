**English** | [中文](agents-integrations.md)

# Agents, integrations, and tools API

This reference covers 38 method/path combinations in the current source, based on actual handlers and DTOs. Published installers may use a different revision. See the complete [OpenAPI specification](../../api/openapi.yaml).

## Authentication and common conventions

All endpoints require the active service's `Authorization: Bearer <token>`, using that local or cloud service's persistent token; missing/invalid credentials return 401. No real credentials are shown. JSON requests should use `Content-Type: application/json`; handlers decode JSON rather than treating that header as another business field. No other path/query/header/body fields are defined unless listed.

Typical handler errors use `{"error":"..."}`. Unmounted modules can produce ServeMux text responses with 404/405, distinct from handler JSON errors. Availability and outcomes are listed per operation. MCP calls and tool metrics can include `error` with HTTP 200, so inspect the body. Unknown write/tool/model outcomes do not make these APIs inherently safe to replay.

See the [K12 API](../../scenarios/k12/API.md) for instance metadata, profile-bundle, and initial `curriculum_progress` contracts; scenario progress rules are not duplicated here.

## Endpoint index

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/roles` | List role templates |
| `GET` | `/api/v1/prompts` | List enabled Prompts |
| `GET` | `/api/v1/prompts/all` | List all Prompts |
| `POST` | `/api/v1/prompts` | Create or fully upsert a Prompt |
| `DELETE` | `/api/v1/prompts/{id}` | Delete a Prompt |
| `GET` | `/api/v1/mcp/tools` | List discovered MCP tools |
| `GET` | `/api/v1/mcp/servers` | List MCP server summaries |
| `POST` | `/api/v1/mcp/servers` | Register or replace an MCP server |
| `DELETE` | `/api/v1/mcp/servers/{name}` | Remove an MCP server |
| `POST` | `/api/v1/mcp/servers/{name}/restart` | Reconnect an MCP server |
| `PUT` | `/api/v1/mcp/servers/{name}/status` | Persist MCP enabled state and update the connection |
| `POST` | `/api/v1/mcp/tools/call` | Call an MCP tool |
| `GET` | `/api/v1/mcp/status` | Read MCP runtime status |
| `GET` | `/api/v1/skills` | List installed skills and effective state |
| `GET` | `/api/v1/skills/{name}/content` | Read an installed SKILL.md |
| `POST` | `/api/v1/skills/refresh` | Rediscover and hot-load installed skills |
| `GET` | `/api/v1/skills/{name}/resources` | List installed package resources |
| `GET` | `/api/v1/skills/{name}/resources/{path...}` | Read exact declared resource bytes |
| `PUT` | `/api/v1/skills/{name}/status` | Persist skill enabled state |
| `POST` | `/api/v1/skills/install` | Install a skill or Hub MCP entry |
| `POST` | `/api/v1/skills/generate` | Generate a Skill draft |
| `DELETE` | `/api/v1/skills/{name}` | Uninstall a skill |
| `GET` | `/api/v1/agents` | List instances, rules, and default |
| `GET` | `/api/v1/agents/templates` | Read generic Agent templates and exact snapshots |
| `POST` | `/api/v1/agents` | Register an Agent instance |
| `PUT` | `/api/v1/agents/{name}` | Partially update an Agent |
| `DELETE` | `/api/v1/agents/{name}` | Unregister an Agent |
| `POST` | `/api/v1/agents/default` | Set or clear the default Agent |
| `GET` | `/api/v1/agents/rules` | List routing rules |
| `POST` | `/api/v1/agents/rules` | Add a routing rule |
| `POST` | `/api/v1/agents/rules/test` | Explain a routing decision |
| `DELETE` | `/api/v1/agents/rules/{id}` | Delete a routing rule |
| `GET` | `/api/v1/subagents/runs` | List sub-Agent run records |
| `GET` | `/api/v1/clawhub/search` | Search Hub skills and MCP entries |
| `GET` | `/api/v1/clawhub/skills/{name}/content` | Preview a Hub SKILL.md without installing |
| `GET` | `/api/v1/tools/cache/stats` | Read tool cache statistics |
| `GET` | `/api/v1/tools/metrics` | Read tool call metrics |
| `GET` | `/api/v1/tools/permissions` | Read current tool permission rules |

## Operation reference

### `GET /api/v1/roles`

List role templates.

Authentication: Bearer. Successful HTTP status: 200.

Only ReActEngine returns roles from AgentFactory; other engines return roles=[]. This does not create instances or call a model.

Availability: {"mounted_when": "always", "disabled_projection": {"status": 200, "body": {"roles": []}}}.

Request body: none.

Success body: `AgentsRoleList` (see data structures).

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleListRoles`.

### `GET /api/v1/prompts`

List enabled Prompts.

Authentication: Bearer. Successful HTTP status: 200.

Returns only enabled=true records.

Availability: {"mounted_when": "s.promptStore != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

Request body: none.

Success body: `AgentsPromptList` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `500` | Storage query failed. |

Implementation: [api/handler_library.go](../../api/handler_library.go) · `handleListPrompts`.

### `GET /api/v1/prompts/all`

List all Prompts.

Authentication: Bearer. Successful HTTP status: 200.

Includes both enabled and disabled records.

Availability: {"mounted_when": "s.promptStore != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

Request body: none.

Success body: `AgentsPromptList` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `500` | Storage query failed. |

Implementation: [api/handler_library.go](../../api/handler_library.go) · `handleListAllPrompts`.

### `POST /api/v1/prompts`

Create or fully upsert a Prompt.

Authentication: Bearer. Successful HTTP status: 200.

Whole-record upsert by id, not a field patch; omitted enabled disables it and omitted strings become empty. Storage writes current updated_at and does not execute the Prompt.

Availability: {"mounted_when": "s.promptStore != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

Request body: `AgentsPromptWrite`; maximum read 1048576 bytes. Fields are listed below; required status follows actual handler rejection conditions.

Success body: `AgentsPromptID` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Invalid JSON, title, or length. |
| `500` | Reading the previous record or saving failed. |

Implementation: [api/handler_library.go](../../api/handler_library.go) · `handleUpsertPrompt`.

### `DELETE /api/v1/prompts/{id}`

Delete a Prompt.

Authentication: Bearer. Successful HTTP status: 200.

Deletes the id; nonexistent ids also return 200, which does not establish prior existence.

Availability: {"mounted_when": "s.promptStore != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| Location / field | Required | Type | Contract |
| --- | --- | --- | --- |
| `path.id` | true | `string` | Prompt ID.  |

Request body: none.

Success body: `AgentsPromptDeleted` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `500` | Storage deletion failed. |

Implementation: [api/handler_library.go](../../api/handler_library.go) · `handleDeletePrompt`.

### `GET /api/v1/mcp/tools`

List discovered MCP tools.

Authentication: Bearer. Successful HTTP status: 200.

name is the public callable name, server_name identifies ownership, and input_schema describes arguments. Internal OriginalName is not returned. Disabled MCP returns tools=[] and total=0.

Availability: {"mounted_when": "s.mcpMgr != nil", "disabled_projection": {"status": 200, "body": {"tools": [], "total": 0}}}.

Request body: none.

Success body: `AgentsMCPTools` (see data structures).

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleListMCPTools`.

### `GET /api/v1/mcp/servers`

List MCP server summaries.

Authentication: Bearer. Successful HTTP status: 200.

Lists configured server connection summaries, retaining disconnected entries; does not return command, args, env, endpoint, or secret values. An unavailable global MCP module returns servers=[] and total=0.

Availability: {"mounted_when": "s.mcpMgr != nil", "disabled_projection": {"status": 200, "body": {"servers": [], "total": 0}}}.

Request body: none.

Success body: `AgentsMCPServers` (see data structures).

The list includes saved enabled and disabled servers, merged with runtime-only connections. Each item's `enabled` reflects the saved setting, while `status` reflects the actual connection. Disabled entries remain listed, normally with `enabled:false`, `status:"disconnected"`, and `tool_count:0`. Disabling does not uninstall the server or expose launch arguments or credentials.

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleListMCPServers`.

### `POST /api/v1/mcp/servers`

Register or replace an MCP server.

Authentication: Bearer. Successful HTTP status: 200.

Persists configuration before registration with an immediate connection window of up to 10 seconds. connected=false means registered with background connection pending, not rollback. This can start/replace a stdio subprocess or access a network endpoint. Secret mutations require a configuration Writer, unique index/key, and a nonempty replacement of at most 64 KiB; see the secret mutation DTOs.

Add, replace, enable, disable, remove, and Hub installation share a short configuration commit phase that binds persisted state to the runtime generation. Network connection and resource closing occur outside its coordination lock. A newer command for the same name can supersede an older connection attempt. After a 409, query current configuration and runtime state rather than blindly replaying old configuration.

Availability: {"mounted_when": "s.mcpMgr != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

Request body: `AgentsMCPAddRequest`; maximum read 65536 bytes. Fields are listed below; required status follows actual handler rejection conditions.

Success body: `AgentsMCPAdded` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Invalid format, name, transport, command/endpoint, secret mutation, or Manager state. Configuration may already have been saved. |
| `409` | A newer command superseded the in-flight connection; body: `{"error":"MCP server change was superseded"}`. |
| `503` | Configuration or secret persistence unavailable/failed. |

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleAddMCPServer`.

### `DELETE /api/v1/mcp/servers/{name}`

Remove an MCP server.

Authentication: Bearer. Successful HTTP status: 200.

Uses the same removal use case as the conversation tool: removes persisted configuration and invalidates the runtime generation before closing the connection/subprocess and unregistering tools. A configured server that is disabled or not yet connected returns 200; an absent runtime entry does not make deletion fail. An unconfigured server returns 404. Persistence failures still return 503 and are not reported as success.

Availability: {"mounted_when": "s.mcpMgr != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| Location / field | Required | Type | Contract |
| --- | --- | --- | --- |
| `path.name` | true | `string` | Server name.  |

Request body: none.

Success body: `AgentsMessage` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Empty name. |
| `404` | Server is not configured. |
| `503` | Configuration removal failed or MCP unavailable. |

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleRemoveMCPServer`.

### `POST /api/v1/mcp/servers/{name}/restart`

Reconnect an MCP server.

Authentication: Bearer. Successful HTTP status: 200.

A 20-second connection window; replaces the old connection only after successful new connection, preserving old state on failure. It does not change server configuration.

When interleaved with configuration replacement, restart reads the latest committed configuration and takes over its connection generation; a late result from an older attempt cannot overwrite it. Background reconnection does not duplicate a same-name connection for the current in-flight generation.

Availability: {"mounted_when": "s.mcpMgr != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| Location / field | Required | Type | Contract |
| --- | --- | --- | --- |
| `path.name` | true | `string` | Server name.  |

Request body: none.

Success body: `AgentsMessage` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Empty name. |
| `404` | Not configured or disabled. |
| `502` | Connection failed. |
| `503` | MCP unavailable. |

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleRestartMCPServer`.

### `PUT /api/v1/mcp/servers/{name}/status`

Persist an existing MCP server's enabled state and update its actual connection. Authentication: Bearer; success: HTTP 200.

Mounted only when `s.mcpMgr != nil`; otherwise 404/405. Skill enablement and Hub availability do not gate this operation. Configuration is saved before connecting or closing the connection/subprocess and removing its tools. Enabling has an immediate connection window of up to 10 seconds; connected=false can mean background connection is pending. Explicit disablement survives process restart. Failure does not guarantee rollback of an earlier configuration write.

| Location / field | Required | Type | Contract |
| --- | --- | --- | --- |
| `path.name` | true | `string` | Saved server name. |
| `body.enabled` | true | `boolean` | Explicit true or false; omission/null is invalid. Maximum read: 1024 bytes. |

Success body: `AgentsMCPLifecycleState`. It contains no command, args, env, endpoint, or secret values.

Persistence and runtime-generation reservation share a short commit phase; network connection and resource closing occur outside it. Disabling or removing invalidates an in-flight connection for the same name. An older enable request superseded by a newer operation returns 409; query saved and runtime state instead of blindly enabling again.

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Invalid JSON/enabled, unconfigured/unsaved server, persistence unavailable/failed, or connection/runtime update failed. Configuration may already be saved. |
| `409` | A newer command superseded the in-flight connection; body: `{"error":"MCP server change was superseded"}`. |

Implementation: [api/resource_mcp_lifecycle.go](../../api/resource_mcp_lifecycle.go) · `handleMCPResourceStatus` / `ManageMCP`.

### `POST /api/v1/mcp/tools/call`

Call an MCP tool.

Authentication: Bearer. Successful HTTP status: 200.

Success is {result:string}; missing/ambiguous tools and transport/execution failures return HTTP 200 with {error:string}. With server_name, name is the upstream original tool name; without it, use the public name from GET tools. The disabled branch also returns 200 {error:"MCP 模块未启用"} without parsing the body. Tools can cause external side effects; HTTP 200 is not a success criterion.

Availability: {"mounted_when": "s.mcpMgr != nil", "disabled_projection": {"status": 200, "body": {"error": "MCP 模块未启用"}}}.

Request body: `AgentsMCPCallRequest`; maximum read 1048576 bytes. Fields are listed below; required status follows actual handler rejection conditions.

Success body: `AgentsMCPCallResponse` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Invalid JSON/name when enabled. |
| `503` | Defensive handler branch for a nil Manager; normal disabled registration returns 200 error. |

Implementation: [api/handler_extended.go](../../api/handler_extended.go) · `handleCallMCPTool`.

### `GET /api/v1/mcp/status`

Read MCP runtime status.

Authentication: Bearer. Successful HTTP status: 200.

Per-server connected, tool_count, and reconnect diagnostics; disabled MCP returns servers=[] and total=0, not a successful connection.

Availability: {"mounted_when": "s.mcpMgr != nil", "disabled_projection": {"status": 200, "body": {"servers": [], "total": 0}}}.

Request body: none.

Success body: `AgentsMCPStatus` (see data structures).

Implementation: [api/handler_extended.go](../../api/handler_extended.go) · `handleMCPStatus`.

### `GET /api/v1/skills`

List installed skills and effective state.

Authentication: Bearer. Successful HTTP status: 200.

enabled is persisted state and effective_enabled is current runtime state. requires_restart means the host cannot apply an enabled skill through hot refresh, rather than every state mismatch. With AutoLoad=false, an installed but unregistered skill has effective_enabled=false and requires_restart=false; explicit refresh can activate it, while the next restart still respects disabled automatic loading. dir is a server-side path.

Availability: {"mounted_when": "s.mp != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

Request body: none.

Success body: `AgentsSkillList` (see data structures).

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleListSkills`.

### `GET /api/v1/skills/{name}/content`

Read an installed SKILL.md.

Authentication: Bearer. Successful HTTP status: 200.

Read-only file access without installing or modifying a skill. name must be a single filename without ..; path is server-side.

The conversation tool `patch_skill` submits a snippet edit against the digest of the instructions it read. If another write replaced those instructions meanwhile, it returns the tool error `skill patch conflict: installed content changed`, preserving already committed content. The caller should reread and match the snippet again. Whole-content `create_skill` writes retain explicit replacement semantics. This is a conversation tool error, not an HTTP 409 from this read endpoint.

Availability: {"mounted_when": "s.mp != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| Location / field | Required | Type | Contract |
| --- | --- | --- | --- |
| `path.name` | true | `string` | Installed skill name.  |

Request body: none.

Success body: `AgentsSkillContent` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Invalid name. |
| `404` | Skill not installed. |
| `500` | File read failed. |

Implementation: [api/handler_skill_content.go](../../api/handler_skill_content.go) · `handleSkillContent`.

### `POST /api/v1/skills/refresh`

Rescan installed files and hot-update runtime Markdown skills. Authentication: Bearer; success: HTTP 200.

Uses the same lifecycle service as install, enablement, and uninstall: replaces changed instructions, unregisters removed entries, and preserves saved disabled state. It does not download catalog entries or call a model. Explicit refresh can register skills with AutoLoad=false. Mounted only when `s.mp != nil`; otherwise 404/405.

The body is not read; omit it or send `{}`. Success body: `{"skills":[AgentsSkillRuntimeState...]}`, without total or dir.

| Status | Error / side-effect boundary |
| --- | --- |
| `500` | Discovery or runtime refresh failed; existing disk changes are not guaranteed to roll back. |
| `503` | Marketplace is mounted but the host has not injected a lifecycle service. |

Implementation: [api/handler_skill_lifecycle.go](../../api/handler_skill_lifecycle.go) · `handleRefreshSkills`.

### `GET /api/v1/skills/{name}/resources`

List declared resources in the current installed package. Authentication: Bearer; success: HTTP 200. Mounted only when `s.mp != nil`; otherwise 404/405.

`path.name` is the installed skill name; no request body. Success body: `{"name":"...","resources":[AgentsSkillResource...]}`, sorted by path. Resource content is neither returned nor injected into model context. Legacy single-file skills list only SKILL.md. For a complete package, the manifest and all declared files are checked for identity, size, and digest; missing resources cannot be reported as a complete success.

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Invalid/incomplete package, manifest, or files; digest mismatch; or another resource read failure. |
| `404` | Skill is not installed. |
| `503` | Lifecycle service is not injected. |

Implementation: [api/handler_skill_lifecycle.go](../../api/handler_skill_lifecycle.go) · `handleSkillResources`.

### `GET /api/v1/skills/{name}/resources/{path...}`

Read exact resource bytes from the current installed package. Authentication: Bearer; success: HTTP 200. Mounted only when `s.mp != nil`; otherwise 404/405.

`path.name` identifies the skill; `path.path` is a package-relative path, such as assets/template.typ, SKILL.md, or bundle.json. Reads only declared files and the manifest itself. It does not execute scripts, generate media, or change enabled state. Legacy single-file skills support only SKILL.md. There is no historical-version parameter: callers must freeze resource versions, digests, and bytes rather than assume the installed package stays unchanged during recovery.

The success body is raw bytes, without a JSON wrapper. Content-Type is inferred from the extension, falling back to application/octet-stream. Content-Length is the byte count; ETag is the quoted file SHA256; X-Skill-Version is the current package version; X-Content-SHA256 is the file SHA256. The handler does not implement conditional requests/304. The raw bundle.json file SHA differs from its canonical semantic manifest digest.

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Undeclared/invalid path, absent bundle, changed package identity requiring refresh, missing/mismatched files, or read failure. |
| `404` | Skill is not installed; a missing package file instead returns 400. |
| `503` | Lifecycle service is not injected. |

Implementation: [api/handler_skill_lifecycle.go](../../api/handler_skill_lifecycle.go) · `handleSkillResource`.

### `PUT /api/v1/skills/{name}/status`

Persist skill enabled state.

Authentication: Bearer. Successful HTTP status: 200.

Omitted enabled means false rather than preserving the previous value. The standard host persists state and then rescans/hot-updates through the shared lifecycle, returning actual enabled/effective_enabled/requires_restart values. A failure returns 500 and may follow a completed state write. Legacy hosts without the lifecycle retain the previous compatibility branch: hot-update failure can return 200 with state fields/message. This operation does not delete resources or previously frozen artifacts.

Availability: {"mounted_when": "s.mp != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| Location / field | Required | Type | Contract |
| --- | --- | --- | --- |
| `path.name` | true | `string` | Single skill name without ..  |

Request body: `AgentsSkillStatusRequest`; maximum read 1024 bytes. Fields are listed below; required status follows actual handler rejection conditions.

Success body: `AgentsSkillStatus` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Invalid name or JSON. |
| `404` | Skill not installed. |
| `500` | Saving state or lifecycle refresh failed; an earlier state write is not guaranteed to roll back. |

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleSkillStatus`.

### `POST /api/v1/skills/install`

Install a skill or Hub MCP entry.

Authentication: Bearer. Successful HTTP status: 200.

file reads a path on the executing server, accepting an .md file or skill directory without ..; a single file is limited to 1 MiB. url requires HTTPS, a 30-second download window, at most 1 MiB, and frontmatter. content requires closed frontmatter and a valid name. When a directory or Hub entry declares bundle.json, all declared resources are validated and installed, not only SKILL.md. Updating instructions within an existing complete package preserves other resources and updates instruction/manifest digests.

Ordinary skill installation, enablement, uninstall, and refresh use the shared lifecycle. Reinstall preserves saved disabled state; explicit install clears that skill's uninstall marker. clawhub resolves the same catalog to Hub resources or compatible read-only bundled resources with matching version/digests, returning source=hub/bundled. A different version or digest declared by the Hub does not silently fall back to an old bundle. A successful installation does not imply enablement: inspect enabled, runtime_registered, and effective_enabled. MCP entries retain pinned-artifact validation, persisted configuration, and best-effort connection. Reinstalling identical saved MCP configuration preserves enabled, including explicit false. A write/refresh failure may follow completed disk changes; do not blindly replay.

Availability: {"mounted_when": "s.mp != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

Request body: `AgentsSkillInstallRequest`; maximum read 1048576 bytes. Fields are listed below; required status follows actual handler rejection conditions.

Success body: `AgentsSkillInstalled` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Invalid JSON/source/type/format/size or Hub MCP catalog validation. |
| `404` | Hub entry not found. |
| `500` | Installation/parsing/temporary-file processing failed. |
| `502` | URL download failed or returned a status other than 200. |
| `503` | Required Hub/MCP or configuration persistence unavailable. |

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleInstallSkill`.

### `POST /api/v1/skills/generate`

Generate a Skill draft.

Authentication: Bearer. Successful HTTP status: 200.

Uses the current ReActEngine model to request up to 1500 output tokens for SKILL.md; calls a model and returns content without saving/installing it. Checking a frontmatter prefix does not establish all installation validation passed; install through POST skills/install with type in the JSON body.

Availability: {"mounted_when": "s.mp != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

Request body: `AgentsGenerateSkillRequest`; maximum read 65536 bytes. Fields are listed below; required status follows actual handler rejection conditions.

Success body: `AgentsGeneratedSkill` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Invalid JSON, blank description, or more than 2000 characters after trimming. |
| `502` | Model failure or output without frontmatter. |
| `503` | Engine is not a ReActEngine supporting generation. |

Implementation: [api/handler_skill_generate.go](../../api/handler_skill_generate.go) · `handleGenerateSkill`.

### `DELETE /api/v1/skills/{name}`

Uninstall a skill.

Authentication: Bearer. Successful HTTP status: 200.

Deletes marketplace-installed content and synchronizes the runtime engine. The shared lifecycle records an internal uninstall marker so refresh or bundled seeding does not resurrect it; the marker is not exposed as a user skill. Previously frozen artifacts are not deleted with the skill.

Availability: {"mounted_when": "s.mp != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| Location / field | Required | Type | Contract |
| --- | --- | --- | --- |
| `path.name` | true | `string` | Installed skill name.  |

Request body: none.

Success body: `AgentsMessage` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `404` | Skill not installed. |
| `500` | Other uninstall failure. |

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleUninstallSkill`.

### `GET /api/v1/agents`

List instances, rules, and default.

Authentication: Bearer. Successful HTTP status: 200.

Returns complete instance configurations and rules; default="" means no named default is selected, even when other instances are registered. A disabled router returns agents=[], rules=[], total=0, and default="".

Availability: {"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": 200, "body": {"agents": [], "rules": [], "total": 0, "default": ""}}}.

Request body: none.

Success body: `AgentsAgentList` (see data structures).

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleListAgents`.

### `GET /api/v1/agents/templates`

Read the backend source of generic Agent templates and exact snapshots. Authentication: Bearer; success: HTTP 200. Always mounted, independently of Agent router enablement. It does not create instances, call a model, or change templates/existing Agents. K12-specific templates retain their scenario contract.

| Location / field | Required | Type | Contract |
| --- | --- | --- | --- |
| `query.locale` | false | `string` | Omitted/empty or zh selects zh-CN; supported values: zh-CN, en, ug-CN. |
| `query.key` | false | `string` | Nonempty selects one template_key; omitted/empty lists all templates. |

No request body. List success body: `AgentsTemplateCatalog`. With key, the response is directly `AgentsTemplateSnapshot`, without a templates wrapper. The existing Agent metadata can store catalog_source, catalog_version, template_key, template_version, template_digest, template_locale, and template_snapshot (a serialized snapshot string). Creation or explicit replacement of a backend snapshot validates the current catalog. Ordinary edits preserving the original source fields retain the frozen source across catalog upgrades and do not overwrite edited persona fields.

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | List request uses an unavailable locale. |
| `404` | Requested key or locale is unavailable; single-item catalog read errors also map to 404. |
| `500` | Catalog read failed in the list branch. |

Implementation: [api/handler_agent_templates.go](../../api/handler_agent_templates.go) · `handleAgentTemplates`.

### `POST /api/v1/agents`

Register an Agent instance.

Authentication: Bearer. Successful HTTP status: 200.

Publishes the runtime instance only after persistence succeeds; without a Store it is runtime-only. Registration preserves the explicit default; an unset or cleared default does not automatically select the new instance. provider/model are both empty for global configuration or select an available configured pair; reasoning_policy omission/null is inherit. K12 metadata/profile/initial-progress transaction rules are in the K12 API; ordinary registration is not an arbitrary progress-write endpoint.

Availability: {"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

Request body: `AgentsRegisterAgentRequest`; maximum read 1048576 bytes. Fields are listed below; required status follows actual handler rejection conditions.

Success body: `AgentsAgentMessage` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Invalid JSON/empty name/display length/description/metadata/model pair/reasoning/temperature/scenario fields. |
| `404` | Scenario initial-transaction reference not found. |
| `409` | Duplicate name/router registration failure including whitespace-only name, or initial scenario version conflict. |
| `500` | Persistence or other scenario transaction failure. |
| `503` | Nonempty initial progress submitted without a scenario persister. |

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleRegisterAgent`.

### `PUT /api/v1/agents/{name}`

Partially update an Agent.

Authentication: Bearer. Successful HTTP status: 200.

Despite PUT, this is a field patch: omitted/null ordinary pointer fields are preserved, while submitted arrays/metadata replace their values; temperature omission preserves/null clears/numbers set. Model validation runs only when provider/model actually change, allowing other edits despite stale models. Existing K12 profile fields use profile-bundle rather than this endpoint for display name, description, prompt, model, skills, or owned textbook metadata.

Availability: {"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| Location / field | Required | Type | Contract |
| --- | --- | --- | --- |
| `path.name` | true | `string` | Existing stable identifier; the body does not rename this URL identifier.  |

Request body: `AgentsUpdateAgentRequest`; maximum read 1048576 bytes. Fields are listed below; required status follows actual handler rejection conditions.

Success body: `AgentsAgentMessage` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Invalid JSON/temperature/actual rename/length/model/metadata. |
| `404` | Instance not found, including deletion during update. |
| `409` | Attempt to change K12 profile-bundle-owned fields. |
| `500` | Persistence failed. |

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleUpdateAgent`.

### `DELETE /api/v1/agents/{name}`

Unregister an Agent.

Authentication: Bearer. Successful HTTP status: 200.

Removes the instance and associated in-memory rules; deleting the explicit default clears it in the same transaction without selecting another instance. Invokes an attached resource cleaner. Persistence/resource failure preserves the instance and attempts compensation; this does not promise deletion of all scenario history.

Availability: {"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| Location / field | Required | Type | Contract |
| --- | --- | --- | --- |
| `path.name` | true | `string` | Stable instance identifier.  |

Request body: none.

Success body: `AgentsMessage` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `404` | Instance not registered. |
| `500` | Owned-resource or persistence failure, possibly with rollback-failure information. |

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleUnregisterAgent`.

### `POST /api/v1/agents/default`

Set or clear the default Agent.

Authentication: Bearer. Successful HTTP status: 200.

Nonempty name selects a registered default; omission/empty clears it and returns name="". Persistence precedes runtime publication. A cleared default remains empty after restart; when no explicit rule or other routing selection applies, the built-in assistant handles the request without selecting an instance by registration order or name.

Availability: {"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

Request body: `AgentsDefaultAgentRequest`; maximum read 1048576 bytes. Fields are listed below; required status follows actual handler rejection conditions.

Success body: `AgentsAgentMessage` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Invalid JSON. |
| `404` | Specified instance not found. |
| `500` | Default persistence failed. |

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleSetDefaultAgent`.

### `GET /api/v1/agents/rules`

List routing rules.

Authentication: Bearer. Successful HTTP status: 200.

A disabled router still returns rules=[] and total=0.

Availability: {"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": 200, "body": {"rules": [], "total": 0}}}.

Request body: none.

Success body: `AgentsRuleList` (see data structures).

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleListRules`.

### `POST /api/v1/agents/rules`

Add a routing rule.

Authentication: Bearer. Successful HTTP status: 200.

Combines platform/instance_id/user_id/chat_id, with empty values unrestricted; the target must be registered. SaveRule persistence precedes runtime AddRule, so a runtime 400 does not establish no persistent write occurred.

Availability: {"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

Request body: `AgentsAddRuleRequest`; maximum read 1048576 bytes. Fields are listed below; required status follows actual handler rejection conditions.

Success body: `AgentsRuleAdded` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Invalid JSON/empty agent_name/unregistered target. |
| `500` | Persistence failed. |

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleAddRule`.

### `POST /api/v1/agents/rules/test`

Explain a routing decision.

Authentication: Bearer. Successful HTTP status: 200.

Returns explicit-match candidates and source, and can call an LLM for semantic routing or fall back to the default. It does not execute the selected Agent task or add rules. Body user_id is only a simulated match value, not HTTP identity.

When no explicit rule matches DingTalk, the built-in default assistant handles the request without semantic classification or selection of a registered default Agent. The response has `source=default`, `matched=false`, and an empty `agent_name`. Explicit matches and existing fallback behavior on other platforms are preserved.

Availability: {"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

Request body: `AgentsRouteTestRequest`; maximum read 1048576 bytes. Fields are listed below; required status follows actual handler rejection conditions.

Success body: `AgentsRouteTestResponse` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Invalid JSON. |

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleTestRoute`.

### `DELETE /api/v1/agents/rules/{id}`

Delete a routing rule.

Authentication: Bearer. Successful HTTP status: 200.

Deletes by integer rule ID and updates runtime rules after persistent deletion succeeds.

Availability: {"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| Location / field | Required | Type | Contract |
| --- | --- | --- | --- |
| `path.id` | true | `integer` | Integer rule ID.  |

Request body: none.

Success body: `AgentsMessage` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | ID cannot be parsed as an integer. |
| `404` | Rule not found. |
| `500` | Persistent deletion failed. |

Implementation: [api/handler_misc.go](../../api/handler_misc.go) · `handleDeleteRule`.

### `GET /api/v1/subagents/runs`

List sub-Agent run records.

Authentication: Bearer. Successful HTTP status: 200.

Ordered by started_at descending; observation only, without creating/canceling/resuming runs. An absent registry returns runs=[]. Invalid, nonpositive, or omitted limit uses 200, without a further cap.

Availability: {"mounted_when": "always", "disabled_projection": {"status": 200, "body": {"runs": []}}, "disabled_when": "registry/Hub is not injected"}.

| Location / field | Required | Type | Contract |
| --- | --- | --- | --- |
| `query.limit` | false | `integer` | Positive integers apply; invalid/nonpositive values use 200. Default 200. |

Request body: none.

Success body: `AgentsSubAgentList` (see data structures).

Implementation: [api/server.go](../../api/server.go) · `handleListSubAgentRuns`.

### `GET /api/v1/clawhub/search`

Search Hub skills and MCP entries.

Authentication: Bearer. Successful HTTP status: 200.

Ensures catalog seed/cache and can refresh in the background without installing entries. A nonempty q searches; category/type are trimmed and lowercased, with empty/all unfiltered. Unknown filters yield empty results rather than 400. An absent Hub returns skills=[], total=0, source=clawhub.

Availability: {"mounted_when": "always", "disabled_projection": {"status": 200, "body": {"skills": [], "total": 0, "source": "clawhub"}}, "disabled_when": "registry/Hub is not injected"}.

| Location / field | Required | Type | Contract |
| --- | --- | --- | --- |
| `query.q` | false | `string` | Optional keyword; length is checked before trimming.  |
| `query.category` | false | `string` | Optional category; empty/all is unfiltered.  |
| `query.type` | false | `string` | Common values skill/mcp/all; unknown values are accepted but yield no matches.  |

Request body: none.

Success body: `AgentsHubSearch` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Raw q exceeds 512 Unicode characters. |

Implementation: [api/handler_extended.go](../../api/handler_extended.go) · `handleClawHubSearch`.

### `GET /api/v1/clawhub/skills/{name}/content`

Preview a Hub SKILL.md without installing.

Authentication: Bearer. Successful HTTP status: 200.

Reads instructions from the same resolved source as catalog installation: compatible read-only bundled resources or Hub content, potentially over the network. The response identifies source=hub/bundled. It does not install, enable, or write the install directory; MCP entries have no SKILL.md preview.

Availability: {"mounted_when": "always", "disabled_projection": {"status": 503, "body": {"error": "ClawHub 未启用"}}}.

| Location / field | Required | Type | Contract |
| --- | --- | --- | --- |
| `path.name` | true | `string` | Catalog skill identifier, a single filename without ..  |

Request body: none.

Success body: `AgentsHubContent` (see data structures).

| Status | Error / side-effect boundary |
| --- | --- |
| `400` | Invalid name or MCP entry type. |
| `404` | Catalog entry not found. |
| `502` | Fetching source text failed. |
| `503` | Hub unavailable. |

Implementation: [api/handler_skill_content.go](../../api/handler_skill_content.go) · `handleClawHubSkillContent`.

### `GET /api/v1/tools/cache/stats`

Read tool cache statistics.

Authentication: Bearer. Successful HTTP status: 200.

Read-only entry/hit/miss counts and percentage; no query/body parameters.

Availability: {"mounted_when": "s.toolCache != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

Request body: none.

Success body: `AgentsToolCacheStats` (see data structures).

Implementation: [api/handler_tools.go](../../api/handler_tools.go) · `handleToolCacheStats`.

### `GET /api/v1/tools/metrics`

Read tool call metrics.

Authentication: Bearer. Successful HTTP status: 200.

Fixed maximum of 20 tools ordered by call count descending; no limit parameter. Missing files can produce tools=null; read errors still return 200 with tools=[] and error.

Availability: {"mounted_when": "s.toolMetrics != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

Request body: none.

Success body: `AgentsToolMetrics` (see data structures).

Implementation: [api/handler_tools.go](../../api/handler_tools.go) · `handleToolMetrics`.

### `GET /api/v1/tools/permissions`

Read current tool permission rules.

Authentication: Bearer. Successful HTTP status: 200.

Returns pattern rules with allow before deny; this GET does not change, enable/disable, or grant tools.

Availability: {"mounted_when": "s.toolPerms != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

Request body: none.

Success body: `AgentsToolPermissions` (see data structures).

Implementation: [api/handler_tools.go](../../api/handler_tools.go) · `handleToolPermissions`.

## Data structures

Omission, null, and defaults can differ; combine each field description with operation semantics. Required response fields come from non-omitempty fields or fixed maps. Legacy unchanged-value length exceptions are not authorization for SDK truncation.

### `AgentsMessage`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `message` | true | `string` | Operation message returned by the service. |

### `AgentsAgentMessage`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `message` | true | `string` | Operation message. |
| `name` | true | `string` | Stable instance identifier. |

### `AgentsRole`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | true | `string` | Role identifier. |
| `title` | true | `string` | Role title. |
| `goal` | true | `string` | Role goal. |
| `backstory` | true | `string` | Role backstory. |
| `expertise` | true | `array / null<string>` | Expertise. |
| `tools` | true | `array / null<string>` | Role tools. |
| `constraints` | true | `array / null<string>` | Role constraints. |

### `AgentsRoleList`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `roles` | true | `array<AgentsRole>` | Role array; empty for engines other than ReActEngine. |

### `AgentsReasoningPolicy`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `mode` | false | `string` | auto / inherit / on / off / effort; omitted within an object means auto. Enum: `auto`, `inherit`, `on`, `off`, `effort`. Default: "auto". |
| `effort` | false | `string` | Required only for mode=effort and rejected for other modes. Enum: `low`, `medium`, `high`, `xhigh`, `max`. |

### `AgentsAgentConfig`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | true | `string` | Stable identifier; creation requires a nonempty name, and the router rejects whitespace-only names. |
| `display_name` | true | `string` | Display name; at most 64 Unicode characters for creation or an actual rename; surrounding whitespace is trimmed on rename. |
| `description` | true | `string` | Description; new or changed values are limited to 2000 Unicode characters; unchanged legacy values are preserved. |
| `model` | true | `string` | Model; the final model/provider pair is either both empty or both nonempty; nonempty values must select a configured available text model. |
| `provider` | true | `string` | Provider configuration key on the active service; case-insensitive matching normalizes to the actual key. An empty model/provider pair follows global configuration. |
| `system_prompt` | true | `string` | System prompt; this endpoint does not execute a chat task. |
| `skills` | true | `array / null<string>` | Skill identifiers; omitted on creation is null. |
| `max_tokens` | true | `integer` | 0 means unset and follows the model default; the handler imposes no additional numeric range. Default: 0. |
| `reasoning_policy` | true | `AgentsReasoningPolicy / null` | Creation omission/null normalizes to inherit; update omission/null leaves it unchanged. |
| `temperature` | false | `number / null` | 0–2; creation omission/null is unset; update omission preserves, null clears, and 0 explicitly selects zero. |
| `metadata` | true | `object / null` | Scenario metadata; K12 contracts are in ../../scenarios/k12/API.md; new/changed k12.child_name is limited to 40 characters. |

### `AgentsRegisterAgentRequest`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | true | `string` | Stable identifier; creation requires a nonempty name, and the router rejects whitespace-only names. |
| `display_name` | false | `string` | Display name; at most 64 Unicode characters for creation or an actual rename; surrounding whitespace is trimmed on rename. |
| `description` | false | `string` | Description; new or changed values are limited to 2000 Unicode characters; unchanged legacy values are preserved. |
| `model` | false | `string` | Model; the final model/provider pair is either both empty or both nonempty; nonempty values must select a configured available text model. |
| `provider` | false | `string` | Provider configuration key on the active service; case-insensitive matching normalizes to the actual key. An empty model/provider pair follows global configuration. |
| `system_prompt` | false | `string` | System prompt; this endpoint does not execute a chat task. |
| `skills` | false | `array / null<string>` | Skill identifiers; omitted on creation is null. |
| `max_tokens` | false | `integer` | 0 means unset and follows the model default; the handler imposes no additional numeric range. Default: 0. |
| `reasoning_policy` | false | `AgentsReasoningPolicy / null` | Creation omission/null normalizes to inherit; update omission/null leaves it unchanged. |
| `temperature` | false | `number / null` | 0–2; creation omission/null is unset; update omission preserves, null clears, and 0 explicitly selects zero. |
| `metadata` | false | `object / null` | Scenario metadata; K12 contracts are in ../../scenarios/k12/API.md; new/changed k12.child_name is limited to 40 characters. |
| `curriculum_progress` | false | `object / null` | Optional/null; a nonnull value is persisted atomically with the instance by the scenario transaction; see the K12 API for fields. |

### `AgentsUpdateAgentRequest`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `display_name` | false | `string / null` | Display name; at most 64 Unicode characters for creation or an actual rename; surrounding whitespace is trimmed on rename. |
| `description` | false | `string / null` | Description; new or changed values are limited to 2000 Unicode characters; unchanged legacy values are preserved. |
| `model` | false | `string / null` | Model; the final model/provider pair is either both empty or both nonempty; nonempty values must select a configured available text model. |
| `provider` | false | `string / null` | Provider configuration key on the active service; case-insensitive matching normalizes to the actual key. An empty model/provider pair follows global configuration. |
| `system_prompt` | false | `string / null` | System prompt; this endpoint does not execute a chat task. |
| `skills` | false | `array / null<string>` | Skill identifiers; omitted on creation is null. |
| `max_tokens` | false | `integer / null` | 0 means unset and follows the model default; the handler imposes no additional numeric range. Default: 0. |
| `reasoning_policy` | false | `AgentsReasoningPolicy / null` | Creation omission/null normalizes to inherit; update omission/null leaves it unchanged. |
| `temperature` | false | `number / null` | 0–2; creation omission/null is unset; update omission preserves, null clears, and 0 explicitly selects zero. |
| `metadata` | false | `object / null` | Scenario metadata; K12 contracts are in ../../scenarios/k12/API.md; new/changed k12.child_name is limited to 40 characters. |

### `AgentsDefaultAgentRequest`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | false | `string` | Omitted or empty clears the default Agent; a nonempty value must identify a registered Agent. Default: "". |

### `AgentsRule`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | true | `integer` | Persistent rule ID; can be 0 without persistence. |
| `platform` | true | `string` | Platform match; empty is unrestricted. Default: "". |
| `instance_id` | true | `string` | Platform instance match; empty is unrestricted. Default: "". |
| `user_id` | true | `string` | Message-user match; this does not change HTTP authentication identity. Default: "". |
| `chat_id` | true | `string` | Chat match; empty is unrestricted. Default: "". |
| `agent_name` | true | `string` | Target instance identifier. Default: "". |
| `priority` | true | `integer` | Defaults to 0; higher takes precedence together with match specificity. Default: 0. |

### `AgentsAddRuleRequest`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `platform` | false | `string` | Platform match; empty is unrestricted. Default: "". |
| `instance_id` | false | `string` | Platform instance match; empty is unrestricted. Default: "". |
| `user_id` | false | `string` | Message-user match; this does not change HTTP authentication identity. Default: "". |
| `chat_id` | false | `string` | Chat match; empty is unrestricted. Default: "". |
| `agent_name` | true | `string` | Target instance identifier. Default: "". |
| `priority` | false | `integer` | Defaults to 0; higher takes precedence together with match specificity. Default: 0. |

### `AgentsRuleList`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `rules` | true | `array<AgentsRule>` | Routing rules. |
| `total` | true | `integer` | Rule count. |

### `AgentsRuleAdded`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `message` | true | `string` | Operation message. |
| `id` | true | `integer` | Rule ID after saving. |

### `AgentsAgentList`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `agents` | true | `array<AgentsAgentConfig>` | Registered instances. |
| `rules` | true | `array<AgentsRule>` | Routing rules. |
| `total` | true | `integer` | Instance count. |
| `default` | true | `string` | Default instance name; empty when unset. |

### `AgentsRouteTestRequest`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `platform` | false | `string` | Platform match; empty is unrestricted. Default: "". |
| `instance_id` | false | `string` | Platform instance match; empty is unrestricted. Default: "". |
| `user_id` | false | `string` | Message-user match; this does not change HTTP authentication identity. Default: "". |
| `chat_id` | false | `string` | Chat match; empty is unrestricted. Default: "". |
| `message` | false | `string` | Optional text for semantic fallback; can trigger a model call. Default: "". |

### `AgentsRuleMatch`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `rule` | true | `AgentsRule` |  |
| `score` | true | `integer` | Match score. |

### `AgentsRouteTestResponse`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `matched` | true | `boolean` | Whether routing produced a match. |
| `agent_name` | true | `string` | Selected instance; empty for no match. |
| `source` | true | `string` | Routing source. Enum: `rule`, `llm`, `default`, `none`. |
| `rule` | true | `AgentsRule / null` | Matched explicit rule; otherwise may be null. |
| `score` | true | `integer` | Explicit-rule score. |
| `matches` | true | `array / null<AgentsRuleMatch>` | Candidate rules; may be null. |
| `message` | true | `string` | Explanation text. |

### `AgentsPrompt`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | true | `string` | Empty/omitted generates a pr- ID; an existing ID upserts the entire record. |
| `type` | true | `string` | Blank/omitted defaults to prompt; prompt/command are conventional values but are not enforced as an enum. Default: "prompt". |
| `title` | true | `string` | Required and nonempty; new/changed values are limited to 200 characters; emptiness is checked before trimming. |
| `body_md` | true | `string` | Markdown body; omission writes an empty string. |
| `args_json` | true | `string` | Parameter declaration string, not a nested JSON object. |
| `tool_scope` | true | `string` | Comma-separated tool identifiers; each new identifier is limited to 256 characters. |
| `model` | true | `string` | Suggested model string; new/changed values are limited to 256 characters; this endpoint does not validate a Provider catalog. |
| `category` | true | `string` | Category; new/changed values are limited to 256 characters. |
| `enabled` | true | `boolean` | Defaults to false; omission saves a disabled record rather than preserving previous enabled state. Default: false. |
| `updated_at` | true | `string` | Server save time in responses; if supplied in a request it must decode as RFC3339, but storage writes the current time. |

### `AgentsPromptWrite`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | false | `string` | Empty/omitted generates a pr- ID; an existing ID upserts the entire record. |
| `type` | false | `string` | Blank/omitted defaults to prompt; prompt/command are conventional values but are not enforced as an enum. Default: "prompt". |
| `title` | true | `string` | Required and nonempty; new/changed values are limited to 200 characters; emptiness is checked before trimming. |
| `body_md` | false | `string` | Markdown body; omission writes an empty string. |
| `args_json` | false | `string` | Parameter declaration string, not a nested JSON object. |
| `tool_scope` | false | `string` | Comma-separated tool identifiers; each new identifier is limited to 256 characters. |
| `model` | false | `string` | Suggested model string; new/changed values are limited to 256 characters; this endpoint does not validate a Provider catalog. |
| `category` | false | `string` | Category; new/changed values are limited to 256 characters. |
| `enabled` | false | `boolean` | Defaults to false; omission saves a disabled record rather than preserving previous enabled state. Default: false. |
| `updated_at` | false | `string` | Server save time in responses; if supplied in a request it must decode as RFC3339, but storage writes the current time. |

### `AgentsPromptList`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `prompts` | true | `array / null<AgentsPrompt>` | Prompt array; may be null. |
| `total` | true | `integer` | Entry count. |

### `AgentsPromptID`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | true | `string` | Saved ID. |

### `AgentsPromptDeleted`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `deleted` | true | `string` | Requested deletion ID; nonexistent IDs can succeed. |

### `AgentsMCPServerSummary`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | true | `string` | Server name. |
| `enabled` | true | `boolean` | Saved enabled setting; runtime-only entries are true and disabled saved entries remain listed. |
| `description` | true | `string` | Server-purpose summary. |
| `status` | true | `string` | Connection state. Enum: `connected`, `disconnected`. |
| `transport` | true | `string` | Configured transport projection; the http alias may currently project as unknown. Enum: `stdio`, `sse`, `streamable`, `unknown`. |
| `tool_count` | true | `integer` | Discovered tool count. |
| `last_error` | false | `string` | Latest connection error; optional. |
| `retryable` | false | `boolean` | Optional retry classification; not authorization to replay side effects. |
| `retry_state` | false | `string` | Connection retry state; use the returned value. |
| `retry_count` | false | `integer` | Connection retry count; optional. |
| `next_retry_at` | false | `string` | Next connection retry time; optional. |

### `AgentsMCPServers`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `servers` | true | `array<AgentsMCPServerSummary>` | Configuration and connection projection without command/args/env/endpoint. |
| `total` | true | `integer` | Server count. |

### `AgentsMCPLifecycleState`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | true | `string` | Server name. |
| `configured` | true | `boolean` | Saved or runtime configuration exists; not proof of connection. |
| `persisted` | true | `boolean` | Configuration Writer confirms the server is saved. |
| `enabled` | true | `boolean` | Configured desired enabled state. |
| `connected` | true | `boolean` | Actual current connection state. |
| `tool_count` | true | `integer` | Tools discovered on the current connection; zero after disablement. |

### `AgentsMCPServerStatus`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | true | `string` | Server name. |
| `kind` | false | `string` | Inferred server kind; optional. |
| `connected` | true | `boolean` | Whether connected now. |
| `tool_count` | true | `integer` | Tool count. |
| `last_error` | false | `string` | Latest connection error; optional. |
| `retryable` | false | `boolean` | Optional retry classification; not authorization to replay side effects. |
| `retry_state` | false | `string` | Connection retry state; use the returned value. |
| `retry_count` | false | `integer` | Connection retry count; optional. |
| `next_retry_at` | false | `string` | Next connection retry time; optional. |

### `AgentsMCPStatus`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `servers` | true | `array<AgentsMCPServerStatus>` | Per-server runtime status. |
| `total` | true | `integer` | Server count. |

### `AgentsMCPTool`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | true | `string` | Public tool name; usable verbatim when calling without server_name. |
| `description` | true | `string` | Tool description. |
| `server_name` | true | `string` | Owning server. |
| `input_schema` | false | `any JSON` | JSON Schema for tool arguments. |

### `AgentsMCPTools`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `tools` | true | `array<AgentsMCPTool>` | Tool list. |
| `total` | true | `integer` | Tool count. |

### `AgentsMCPSecretArg`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `index` | false | `integer` | Index into args, default 0; must be within the submitted args. Default: 0. |
| `mode` | true | `string` | preserve restores an existing value; replace uses the new value; clear removes the secret reference and retains the submitted ordinary argument. Enum: `preserve`, `replace`, `clear`. |
| `credential_ref` | false | `string` | replace requires a nonempty sidecar-connection:v1: reference; preserve may inherit an existing reference; clear ignores it. |

### `AgentsMCPSecretEnv`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `key` | true | `string` | Nonempty env key. |
| `mode` | true | `string` | preserve/replace/clear; clear removes an empty env value, otherwise retaining the ordinary value and removing its reference. Enum: `preserve`, `replace`, `clear`. |
| `credential_ref` | false | `string` | replace requires a nonempty sidecar-connection:v1: reference; preserve may inherit an existing reference; clear ignores it. |

### `AgentsMCPAddRequest`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | true | `string` | Required nonempty server name; new/changed values are limited to 64 Unicode characters, with unchanged legacy names preserved. |
| `transport` | false | `string` | Omitted or empty: sse with endpoint, otherwise stdio. http aliases Streamable HTTP. Enum: `""`, `stdio`, `sse`, `streamable`, `http`. |
| `command` | false | `string` | Required for stdio; only empty/control-character values are rejected, and custom executables are supported. |
| `args` | false | `array / null<string>` | stdio arguments; newline/carriage return/NUL are rejected. |
| `env` | false | `object / null` | Environment for the stdio subprocess; omitted/null means not provided. |
| `endpoint` | false | `string` | Required for network transport, with http/https and host; new/changed values are limited to 8192 UTF-8 bytes. |
| `secret_args` | false | `array / null<AgentsMCPSecretArg>` | Optional secret argument mutations; indices must be unique. |
| `secret_env` | false | `array / null<AgentsMCPSecretEnv>` | Optional secret environment mutations; keys must be unique. |

### `AgentsMCPAdded`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `message` | true | `string` | Registration result message. |
| `connected` | true | `boolean` | Whether immediate connection succeeded; false means registered with background connection pending. |

### `AgentsMCPCallRequest`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `server_name` | false | `string` | Optional; when supplied, name is the upstream original tool name for that server. |
| `name` | true | `string` | Required nonempty name; without server_name it accepts public names returned by GET tools. |
| `arguments` | false | `object / null` | Follow the discovered input_schema; omission is null. |

### `AgentsMCPCallResponse`

`{"result":"..."}` or `{"error":"..."}`; both can use HTTP 200.

### `AgentsTemplateCatalog`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `schema_version` | true | `integer` | Catalog structure version, currently 1. |
| `catalog_source` | true | `string` | Always backend for this API. |
| `catalog_version` | true | `string` | Backend source catalog version. |
| `templates` | true | `array<AgentsTemplateSnapshot>` | Generic template snapshots in the selected locale. |
| `total` | true | `integer` | Template count. |

### `AgentsTemplateSnapshot`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `catalog_source` | true | `string` | Always backend for this API. |
| `catalog_version` | true | `string` | Source catalog version. |
| `template_key` | true | `string` | Stable template identifier for query.key. |
| `template_version` | true | `string` | Template version. |
| `template_digest` | true | `string` | Deterministic SHA256 hex digest of version, locale, template identity, and content. |
| `locale` | true | `string` | Actual locale: zh-CN, en, or ug-CN. |
| `slug` | true | `string` | Original template's suggested instance identifier. |
| `icon_key` | true | `string` | Original template icon key. |
| `name` | true | `string` | Localized name. |
| `description` | true | `string` | Localized description. |
| `system_prompt` | true | `string` | Original template persona instructions. |
| `expertise` | true | `array<string>` | Original template expertise entries. |

### `AgentsSkillRuntimeState`

Entries returned by skills/refresh use this structure, rather than the existing display-oriented `AgentsSkillStatus`.

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | true | `string` | Installed skill name. |
| `version` | true | `string` | Current metadata version. |
| `description` | true | `string` | Current description. |
| `enabled` | true | `boolean` | Saved enabled state. |
| `runtime_registered` | true | `boolean` | Actual presence in the runtime registry. |
| `effective_enabled` | true | `boolean` | Actual enabled state in the runtime registry. |
| `requires_restart` | true | `boolean` | True when enabled and the host lacks hot refresh; AutoLoad=false alone does not require restart. |

### `AgentsSkillResource`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `path` | true | `string` | Package-relative path. |
| `role` | true | `string` | Resource role declared by the manifest. |
| `size` | true | `integer` | Original resource byte count. |
| `sha256` | true | `string` | File SHA256 hex digest. |

The resource list has no Content, skill_id, or version fields. Obtain version from X-Skill-Version on the content response, skill metadata, or bundle.json. The manifest itself is readable but not included in its own files list.

### `AgentsSkillStatus`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | false | `string` | Skill name, present in listings; may be omitted from status updates. |
| `description` | false | `string` | Description. |
| `author` | false | `string` | Author. |
| `version` | false | `string` | Version. |
| `triggers` | false | `array<string>` | Triggers. |
| `tags` | false | `array<string>` | Tags. |
| `is_persona` | Yes | `boolean` | Derived from the declared persona tag; identifies an ordinary conversation persona. Task skills remain per-turn. |
| `icon` | false | `string` | Icon. |
| `enabled` | true | `boolean` | Persisted desired state. |
| `effective_enabled` | true | `boolean` | Current effective runtime state. |
| `requires_restart` | true | `boolean` | In the standard host, true when enabled without hot-refresh support; explicit refreshability does not require restart. Legacy hosts report their actual runtime capability. |
| `message` | false | `string` | Status message. |
### `AgentsSkillList`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `skills` | true | `array<AgentsSkillStatus>` | Installed skills. |
| `total` | true | `integer` | Skill count. |
| `dir` | true | `string` | Server-side skill directory, not a client path. |

### `AgentsSkillStatusRequest`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `enabled` | false | `boolean` | Omission means false and disables the skill; it does not preserve the existing value. Default: false. |

### `AgentsSkillInstallRequest`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `source` | false | `string` | Required unless type=content; server-side file/directory, HTTPS URL, or Hub name. |
| `type` | false | `string` | Inferred when omitted or empty: clawhub://→clawhub, https://→url, otherwise→file. Enum: `""`, `file`, `url`, `clawhub`, `content`. |
| `content` | false | `string` | Required nonempty complete SKILL.md for type=content; closed initial frontmatter and name matching [a-z0-9_-]+; at most 1 MiB UTF-8 bytes. |

### `AgentsSkillInstalled`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | true | `string` | Installed entry name. |
| `description` | false | `string` | Skill description, omitted for some sources. |
| `version` | false | `string` | Skill version, omitted for some sources. |
| `type` | false | `string` | mcp for Hub MCP installations; may be omitted for ordinary skills. |
| `message` | true | `string` | Result message. |
| `requires_restart` | true | `boolean` | Ordinary skills reflect the host's actual hot-load capability; Hub MCP returns false. |
| `runtime_registered` | true | `boolean` | Actual ordinary-skill registration; for Hub MCP, immediate connection result rather than saved/enabled state. |
| `enabled` | true | `boolean` | Saved enabled state; reinstall preserves explicit disablement. |
| `effective_enabled` | false | `boolean` | Current runtime enablement for ordinary skills; absent from the MCP response. |
| `source` | false | `string` | hub or bundled for ordinary clawhub skills; may be absent for other install types. |
| `connected` | false | `boolean` | Actual Hub MCP connection state. |
| `persisted` | false | `boolean` | Whether Hub MCP configuration is confirmed saved. |
| `config_hint` | false | `string` | Optional Hub MCP configuration hint. |
| `artifact` | false | `AgentsHubArtifact` |  |

### `AgentsGenerateSkillRequest`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `description` | true | `string` | Required, nonempty after trimming, at most 2000 Unicode characters after trimming. |

### `AgentsGeneratedSkill`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `content` | true | `string` | Generated SKILL.md draft with code fences removed; not installed yet. |

### `AgentsSkillContent`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | true | `string` | Skill name. |
| `path` | true | `string` | Server-side installed file path. |
| `content` | true | `string` | Raw SKILL.md text. |

### `AgentsHubArtifact`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `ecosystem` | true | `string` | npm / pypi. |
| `package` | true | `string` | Package name. |
| `version` | true | `string` | Exact release version. |
| `integrity` | true | `string` | Catalog-declared integrity metadata, not a safety guarantee about the executed package. |
| `source_registry` | false | `string` | Source registry. |
| `resolved_at` | false | `string` | Resolution-time marker. |

### `AgentsHubSkillMeta`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | true | `string` | Entry identifier. |
| `display_name` | true | `string` | Display name. |
| `description` | true | `string` | Description. |
| `version` | true | `string` | Version. |
| `author` | true | `string` | Author. |
| `category` | true | `string` | Category. |
| `type` | false | `string` | skill or mcp; omission is treated as skill. |
| `url` | true | `string` | File source URL. |
| `command` | false | `string` | Optional MCP command. |
| `config_hint` | false | `string` | Optional configuration hint. |
| `source` | false | `string` | Catalog source identifier; bundled denotes a read-only factory candidate, not an installed skill. |
| `status` | false | `string` | For example pinned / quarantined; use the catalog value. |
| `quarantine_reason` | false | `string` | Optional quarantine reason. |
| `file` | false | `string` | Optional file identifier. |
| `sha256` | false | `string` | Optional digest. |
| `bundle_sha256` | false | `string` | Canonical semantic digest of the full resource manifest, not the raw bundle.json file SHA. |
| `trust` | false | `string` | Optional trust metadata. |
| `min_engine_version` | false | `string` | Declared minimum engine version. |
| `license` | false | `string` | License. |
| `schema_version` | false | `string` | Catalog schema version. |
| `eval` | false | `string` | Evaluation notes. |
| `acceptance` | false | `string` | Acceptance notes. |
| `tags` | true | `array / null<string>` | Tags. |
| `dependencies` | false | `array / null<string>` | Dependencies. |
| `args` | false | `array / null<string>` | MCP arguments. |
| `requires` | false | `array / null<string>` | Capability prerequisites. |
| `outputs` | false | `array / null<string>` | Declared outputs. |
| `env` | false | `object / null` | MCP environment declaration. |
| `artifact` | false | `AgentsHubArtifact / null` | MCP release metadata. |
| `size` | false | `integer` | File size in bytes. |
| `downloads` | true | `integer` | Catalog-declared download count. |
| `rating` | true | `number` | Catalog rating. |

### `AgentsHubSearch`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `skills` | true | `array<AgentsHubSkillMeta>` | Matching entries. |
| `total` | true | `integer` | Returned count; no pagination. |
| `source` | true | `string` | Always clawhub. Enum: `clawhub`. |

### `AgentsHubContent`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `name` | true | `string` | Entry name. |
| `content` | true | `string` | Raw SKILL.md text. |
| `source` | true | `string` | Actual preview source: hub or bundled. Preview does not install or enable the skill. |

### `AgentsSubAgentRun`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `id` | true | `string` | Run ID. |
| `parent_id` | false | `string` | Optional parent run ID. |
| `agent` | true | `string` | Agent role. |
| `role` | true | `string` | main / orchestrator / leaf. |
| `mode` | true | `string` | run / session. |
| `status` | true | `string` | running / ok / error / timeout. |
| `session_id` | false | `string` | Optional child session ID. |
| `task` | false | `string` | Optional task. |
| `output` | false | `string` | Optional output. |
| `error` | false | `string` | Optional execution error. |
| `depth` | true | `integer` | Spawn depth. |
| `tool_allow` | false | `array<string>` | Inherited allowed tools. |
| `tool_deny` | false | `array<string>` | Inherited denied tools. |
| `started_at` | true | `string` | Start time. |
| `ended_at` | false | `string` | End time; running entries may contain the zero timestamp, and Go time.Time omitempty does not guarantee omission. |

### `AgentsSubAgentList`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `runs` | true | `array<AgentsSubAgentRun>` | Run records ordered by started_at descending; empty when no registry is attached. |

### `AgentsToolCacheStats`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `entries` | true | `integer` | Current cache entry count. |
| `hits` | true | `integer` | Cache hits. |
| `misses` | true | `integer` | Cache misses. |
| `hit_rate` | true | `number` | Hit percentage from 0 to 100, or 0 without accesses. |

### `AgentsToolMetric`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `tool` | true | `string` | Tool name. |
| `call_count` | true | `integer` | Call count. |
| `success_rate` | true | `number` | Success percentage from 0 to 100. |
| `avg_latency_ms` | true | `number` | Average latency in milliseconds. |
| `cached_count` | true | `integer` | Cached call count. |

### `AgentsToolMetrics`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `tools` | true | `array / null<AgentsToolMetric>` | At most 20 entries ordered by call_count descending; may be null with no file, or empty on read errors. |
| `error` | false | `string` | Read failures still use HTTP 200 and include this field. |

### `AgentsToolPermission`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `pattern` | true | `string` | Rule pattern. |
| `action` | true | `string` | Rule action. Enum: `allow`, `deny`. |

### `AgentsToolPermissions`

| Field | Required | Type | Description |
| --- | --- | --- | --- |
| `rules` | true | `array<AgentsToolPermission>` | allow entries followed by deny entries; read-only projection. |

## Request and response examples

These are static contract examples, not evidence of executed calls. Set variables to your active service and token; replace example identifiers/paths with values available on that service.

```bash
HEXCLAW_API_BASE=http://127.0.0.1:16060
HEXCLAW_API_TOKEN='replace-with-your-service-token'

curl -sS "$HEXCLAW_API_BASE/api/v1/agents" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN"

curl -sS "$HEXCLAW_API_BASE/api/v1/agents" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"demo-research","display_name":"Research assistant","system_prompt":"Summarize evidence clearly.","reasoning_policy":{"mode":"inherit"}}'
```

```json
{
  "message": "Agent 已注册",
  "name": "demo-research"
}
```

MCP: discover the tool schema first. Without server ownership, use its public name; with server_name, use that server's upstream original name. The example assumes files/read_file is configured and returns a string.

```bash
curl -sS "$HEXCLAW_API_BASE/api/v1/mcp/tools" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN"

curl -sS "$HEXCLAW_API_BASE/api/v1/mcp/tools/call" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"server_name":"files","name":"read_file","arguments":{"path":"/data/example.txt"}}'
```

```json
{
  "result": "Example file contents"
}
```

Actual disabled-module error projection, also HTTP 200:

```json
{
  "error": "MCP 模块未启用"
}
```

A complete Prompt upsert with explicit enabled=true; omitting enabled does not preserve prior state:

```json
{
  "id": "pr-demo",
  "type": "prompt",
  "title": "Review checklist",
  "body_md": "Check scope, contracts, and results.",
  "args_json": "",
  "tool_scope": "",
  "model": "",
  "category": "review",
  "enabled": true
}
```

Skill drafts are persisted separately by the install endpoint; type is a JSON field, not a ?type=content query parameter:

```json
{
  "type": "content",
  "content": "---\nname: demo-review\ndescription: Review a change\nversion: \"1.0.0\"\n---\n# Review\nCheck contracts and actual outputs.\n"
}
```

## Sources and revision boundary

Route mounting follows [api/server.go](../../api/server.go); requests and responses follow the per-operation handlers and DTOs. This document and OpenAPI describe current source and do not establish released service behavior, model execution, or external tool side effects.
