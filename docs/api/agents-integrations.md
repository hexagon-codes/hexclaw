[English](agents-integrations.en.md) | **中文**

# Agent、集成与工具 API

本文覆盖当前源码的 33 个方法/路径组合。基于实际 handler 与 DTO；已发布安装包可能使用不同源码版本。完整机器可读规范由 [OpenAPI](../../api/openapi.yaml)提供。

## 认证与公共约定

全部接口要求当前服务的 `Authorization: Bearer <token>`；使用本机或云端各自的持久访问令牌，缺失/无效返回 401。本文不展示实际凭据。JSON 请求推荐 `Content-Type: application/json`；handler 按 JSON 解码，不把这个头作为额外业务字段。无单项声明时没有其他 path/query/header/body 字段。

通常错误体为 `{"error":"..."}`。模块未挂载时可能由 ServeMux 返回 404/405 文本；这与 handler 的 JSON 错误不同。下文逐项列出真实可用性与返回。MCP 调用和工具指标的 HTTP 200 也可能带 `error`，必须读正文。写入、工具调用或模型调用结果未知时，不能把这些 API 当作天然幂等重发入口。

K12 实例创建 metadata、profile-bundle 与初始 `curriculum_progress` 特殊契约见 [K12 API](../../scenarios/k12/API.md)，不在这里复制场景进度规则。

## 接口索引

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/api/v1/roles` | 查询角色模板 |
| `GET` | `/api/v1/prompts` | 查询已启用 Prompt |
| `GET` | `/api/v1/prompts/all` | 查询全部 Prompt |
| `POST` | `/api/v1/prompts` | 创建或整体更新 Prompt |
| `DELETE` | `/api/v1/prompts/{id}` | 删除 Prompt |
| `GET` | `/api/v1/mcp/tools` | 查询发现的 MCP 工具 |
| `GET` | `/api/v1/mcp/servers` | 查询 MCP 服务器摘要 |
| `POST` | `/api/v1/mcp/servers` | 登记或替换 MCP 服务器 |
| `DELETE` | `/api/v1/mcp/servers/{name}` | 移除 MCP 服务器 |
| `POST` | `/api/v1/mcp/servers/{name}/restart` | 重建指定 MCP 连接 |
| `POST` | `/api/v1/mcp/tools/call` | 调用 MCP 工具 |
| `GET` | `/api/v1/mcp/status` | 查询 MCP 运行状态 |
| `GET` | `/api/v1/skills` | 列出已安装技能及生效状态 |
| `GET` | `/api/v1/skills/{name}/content` | 读取已安装 SKILL.md |
| `PUT` | `/api/v1/skills/{name}/status` | 保存技能启用状态 |
| `POST` | `/api/v1/skills/install` | 安装技能或 Hub MCP 条目 |
| `POST` | `/api/v1/skills/generate` | 生成 Skill 草稿 |
| `DELETE` | `/api/v1/skills/{name}` | 卸载技能 |
| `GET` | `/api/v1/agents` | 列出实例、规则与默认值 |
| `POST` | `/api/v1/agents` | 注册 Agent 实例 |
| `PUT` | `/api/v1/agents/{name}` | 局部更新 Agent 配置 |
| `DELETE` | `/api/v1/agents/{name}` | 注销 Agent 实例 |
| `POST` | `/api/v1/agents/default` | 设置或清除默认 Agent |
| `GET` | `/api/v1/agents/rules` | 查询路由规则 |
| `POST` | `/api/v1/agents/rules` | 添加路由规则 |
| `POST` | `/api/v1/agents/rules/test` | 解释路由选择 |
| `DELETE` | `/api/v1/agents/rules/{id}` | 删除路由规则 |
| `GET` | `/api/v1/subagents/runs` | 查询子 Agent 执行记录 |
| `GET` | `/api/v1/clawhub/search` | 搜索 Hub 技能与 MCP 条目 |
| `GET` | `/api/v1/clawhub/skills/{name}/content` | 预览未安装的 Hub SKILL.md |
| `GET` | `/api/v1/tools/cache/stats` | 查询工具缓存统计 |
| `GET` | `/api/v1/tools/metrics` | 查询工具调用指标 |
| `GET` | `/api/v1/tools/permissions` | 查询当前工具权限规则 |

## 操作参考

### `GET /api/v1/roles`

查询角色模板.

认证：Bearer；成功 HTTP 200。

只有 ReActEngine 通过 AgentFactory 返回角色；其他引擎返回 roles=[]。不创建实例、不调用模型。

可用性：{"mounted_when": "always", "disabled_projection": {"status": 200, "body": {"roles": []}}}.

请求体：无。

成功体：`AgentsRoleList`（字段见数据结构）。

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleListRoles`.

### `GET /api/v1/prompts`

查询已启用 Prompt.

认证：Bearer；成功 HTTP 200。

只返回 enabled=true 的条目。

可用性：{"mounted_when": "s.promptStore != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

请求体：无。

成功体：`AgentsPromptList`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `500` | 存储查询失败。 |

实现：[api/handler_library.go](../../api/handler_library.go) · `handleListPrompts`.

### `GET /api/v1/prompts/all`

查询全部 Prompt.

认证：Bearer；成功 HTTP 200。

包含启用与停用条目。

可用性：{"mounted_when": "s.promptStore != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

请求体：无。

成功体：`AgentsPromptList`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `500` | 存储查询失败。 |

实现：[api/handler_library.go](../../api/handler_library.go) · `handleListAllPrompts`.

### `POST /api/v1/prompts`

创建或整体更新 Prompt.

认证：Bearer；成功 HTTP 200。

按 id 整条 upsert，不是字段 patch；省略 enabled 会停用，省略字符串字段写空值。updated_at 由存储当前时间生成，不运行该 Prompt。

可用性：{"mounted_when": "s.promptStore != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

请求体：`AgentsPromptWrite`; 最大读取 1048576 bytes. 字段详见下方结构表；“必填”以 handler 实际拒绝条件为准。

成功体：`AgentsPromptID`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | JSON、title 或长度无效。 |
| `500` | 读取旧值或保存失败。 |

实现：[api/handler_library.go](../../api/handler_library.go) · `handleUpsertPrompt`.

### `DELETE /api/v1/prompts/{id}`

删除 Prompt.

认证：Bearer；成功 HTTP 200。

删除指定 id；不存在的 id 也返回 200，不以此证明原来存在。

可用性：{"mounted_when": "s.promptStore != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| 位置 / 字段 | 必填 | 类型 | 合同 |
| --- | --- | --- | --- |
| `path.id` | 是 | `string` | Prompt ID。  |

请求体：无。

成功体：`AgentsPromptDeleted`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `500` | 删除存储失败。 |

实现：[api/handler_library.go](../../api/handler_library.go) · `handleDeletePrompt`.

### `GET /api/v1/mcp/tools`

查询发现的 MCP 工具.

认证：Bearer；成功 HTTP 200。

name 为公开可调用名，server_name 标明归属，input_schema 为工具参数 Schema；原始内部 OriginalName 不在响应中。关闭时返回 tools=[]、total=0。

可用性：{"mounted_when": "s.mcpMgr != nil", "disabled_projection": {"status": 200, "body": {"tools": [], "total": 0}}}.

请求体：无。

成功体：`AgentsMCPTools`（字段见数据结构）。

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleListMCPTools`.

### `GET /api/v1/mcp/servers`

查询 MCP 服务器摘要.

认证：Bearer；成功 HTTP 200。

返回已配置服务器的连接摘要，未连接项仍保留；不返回 command、args、env、endpoint 或秘密值。关闭时 servers=[]、total=0。

可用性：{"mounted_when": "s.mcpMgr != nil", "disabled_projection": {"status": 200, "body": {"servers": [], "total": 0}}}.

请求体：无。

成功体：`AgentsMCPServers`（字段见数据结构）。

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleListMCPServers`.

### `POST /api/v1/mcp/servers`

登记或替换 MCP 服务器.

认证：Bearer；成功 HTTP 200。

先保存配置，再用最多 10 秒的即时连接窗口登记运行配置。connected=false 表示配置已登记，后续后台连接；不是失败回滚保证。可启动/替换 stdio 子进程或访问网络端点。秘密变更须有配置 Writer，同一 index/key 不可重复，replace 值非空且最多 64 KiB；参见秘密变更 DTO。

可用性：{"mounted_when": "s.mcpMgr != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

请求体：`AgentsMCPAddRequest`; 最大读取 65536 bytes. 字段详见下方结构表；“必填”以 handler 实际拒绝条件为准。

成功体：`AgentsMCPAdded`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | 格式、名称、传输、命令/端点、秘密变更或 Manager 状态无效。配置可能已先保存。 |
| `503` | 配置或秘密持久化不可用/失败。 |

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleAddMCPServer`.

### `DELETE /api/v1/mcp/servers/{name}`

移除 MCP 服务器.

认证：Bearer；成功 HTTP 200。

先删持久配置，再关闭运行连接/子进程与工具注册；运行移除返回 404 时，持久配置可能已移除。

可用性：{"mounted_when": "s.mcpMgr != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| 位置 / 字段 | 必填 | 类型 | 合同 |
| --- | --- | --- | --- |
| `path.name` | 是 | `string` | 服务器名称。  |

请求体：无。

成功体：`AgentsMessage`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | name 为空。 |
| `404` | 运行服务器不存在。 |
| `503` | 配置移除失败或 MCP 未启用。 |

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleRemoveMCPServer`.

### `POST /api/v1/mcp/servers/{name}/restart`

重建指定 MCP 连接.

认证：Bearer；成功 HTTP 200。

20 秒连接窗口；新连接成功才替换旧连接，失败保留旧状态。不会修改服务器配置。

可用性：{"mounted_when": "s.mcpMgr != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| 位置 / 字段 | 必填 | 类型 | 合同 |
| --- | --- | --- | --- |
| `path.name` | 是 | `string` | 服务器名称。  |

请求体：无。

成功体：`AgentsMessage`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | name 为空。 |
| `404` | 未配置或已禁用。 |
| `502` | 连接失败。 |
| `503` | MCP 未启用。 |

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleRestartMCPServer`.

### `POST /api/v1/mcp/tools/call`

调用 MCP 工具.

认证：Bearer；成功 HTTP 200。

成功是 {result:string}；工具不存在、名称歧义、传输/执行失败返回 HTTP 200 的 {error:string}。提供 server_name 时 name 为该服务器上游原名；省略 server_name 可直接使用 GET tools 返回的公开 name。关闭分支也返回 200 {error:"MCP 模块未启用"}，且不解析请求体。工具本身可以产生外部副作用；HTTP 200 不是调用成功判据。

可用性：{"mounted_when": "s.mcpMgr != nil", "disabled_projection": {"status": 200, "body": {"error": "MCP 模块未启用"}}}.

请求体：`AgentsMCPCallRequest`; 最大读取 1048576 bytes. 字段详见下方结构表；“必填”以 handler 实际拒绝条件为准。

成功体：`AgentsMCPCallResponse`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | 启用时请求 JSON 或 name 无效。 |
| `503` | 仅 handler 直接遇到 nil Manager 的防御分支；正常关闭路由为 200 error。 |

实现：[api/handler_extended.go](../../api/handler_extended.go) · `handleCallMCPTool`.

### `GET /api/v1/mcp/status`

查询 MCP 运行状态.

认证：Bearer；成功 HTTP 200。

逐服务器 connected、tool_count 和重连诊断；关闭时 servers=[]、total=0，不代表连接成功。

可用性：{"mounted_when": "s.mcpMgr != nil", "disabled_projection": {"status": 200, "body": {"servers": [], "total": 0}}}.

请求体：无。

成功体：`AgentsMCPStatus`（字段见数据结构）。

实现：[api/handler_extended.go](../../api/handler_extended.go) · `handleMCPStatus`.

### `GET /api/v1/skills`

列出已安装技能及生效状态.

认证：Bearer；成功 HTTP 200。

enabled 是保存状态，effective_enabled 是当前运行时状态；requires_restart 表示未生效/不支持探测。列表 dir 是服务端路径。

可用性：{"mounted_when": "s.mp != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

请求体：无。

成功体：`AgentsSkillList`（字段见数据结构）。

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleListSkills`.

### `GET /api/v1/skills/{name}/content`

读取已安装 SKILL.md.

认证：Bearer；成功 HTTP 200。

只读文件，不安装或修改技能。name 必须是单个文件名且不能含 ..；path 是服务端路径。

可用性：{"mounted_when": "s.mp != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| 位置 / 字段 | 必填 | 类型 | 合同 |
| --- | --- | --- | --- |
| `path.name` | 是 | `string` | 已安装技能名称。  |

请求体：无。

成功体：`AgentsSkillContent`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | 非法名称。 |
| `404` | 技能未安装。 |
| `500` | 读取文件失败。 |

实现：[api/handler_skill_content.go](../../api/handler_skill_content.go) · `handleSkillContent`.

### `PUT /api/v1/skills/{name}/status`

保存技能启用状态.

认证：Bearer；成功 HTTP 200。

请求省略 enabled 等同 false，不保留旧状态。先持久化再尝试热更新；热更新失败仍返回 200，并由 effective_enabled/requires_restart/message 说明。

可用性：{"mounted_when": "s.mp != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| 位置 / 字段 | 必填 | 类型 | 合同 |
| --- | --- | --- | --- |
| `path.name` | 是 | `string` | 单个技能名称，不可含 ..。  |

请求体：`AgentsSkillStatusRequest`; 最大读取 1024 bytes. 字段详见下方结构表；“必填”以 handler 实际拒绝条件为准。

成功体：`AgentsSkillStatus`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | 名称或 JSON 无效。 |
| `404` | 技能未安装。 |
| `500` | 状态保存失败。 |

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleSkillStatus`.

### `POST /api/v1/skills/install`

安装技能或 Hub MCP 条目.

认证：Bearer；成功 HTTP 200。

file 读取执行服务所在机器的路径，可为 .md 文件或技能目录，不能含 ..；单文件上限 1 MiB。url 必须 HTTPS，下载窗口 30 秒，内容上限 1 MiB 且具有 frontmatter。content 要求闭合 frontmatter 与合法 name。clawhub 解析目录名称，普通技能下载并落盘；MCP 条目需通过既有 pinned artifact 校验，保存配置并最佳努力连接。会写入技能/配置并同步运行引擎，实际启用状态以 GET skills/MCP 状态为准。

可用性：{"mounted_when": "s.mp != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

请求体：`AgentsSkillInstallRequest`; 最大读取 1048576 bytes. 字段详见下方结构表；“必填”以 handler 实际拒绝条件为准。

成功体：`AgentsSkillInstalled`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | JSON/来源/类型/格式/大小/Hub MCP 目录校验无效。 |
| `404` | Hub 条目不存在。 |
| `500` | 安装、解析或临时文件处理失败。 |
| `502` | URL 下载失败或远端状态不是 200。 |
| `503` | 所需 Hub/MCP 或配置持久化不可用。 |

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleInstallSkill`.

### `POST /api/v1/skills/generate`

生成 Skill 草稿.

认证：Bearer；成功 HTTP 200。

使用当前 ReActEngine 模型生成最多请求 1500 输出 token 的 SKILL.md；会调用模型，返回 content，不落盘、不安装。只检查生成内容以 frontmatter 开始，不代表所有安装校验已通过；安装另调用 POST skills/install，type 在 JSON 请求体中。

可用性：{"mounted_when": "s.mp != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

请求体：`AgentsGenerateSkillRequest`; 最大读取 65536 bytes. 字段详见下方结构表；“必填”以 handler 实际拒绝条件为准。

成功体：`AgentsGeneratedSkill`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | JSON、空白描述或修剪后超过 2000 字符。 |
| `502` | 模型失败或产物缺少 frontmatter。 |
| `503` | 不是支持技能生成的 ReActEngine。 |

实现：[api/handler_skill_generate.go](../../api/handler_skill_generate.go) · `handleGenerateSkill`.

### `DELETE /api/v1/skills/{name}`

卸载技能.

认证：Bearer；成功 HTTP 200。

删除市场安装内容并同步运行引擎。

可用性：{"mounted_when": "s.mp != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| 位置 / 字段 | 必填 | 类型 | 合同 |
| --- | --- | --- | --- |
| `path.name` | 是 | `string` | 安装的技能名称。  |

请求体：无。

成功体：`AgentsMessage`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `404` | 技能未安装。 |
| `500` | 其他卸载失败。 |

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleUninstallSkill`.

### `GET /api/v1/agents`

列出实例、规则与默认值.

认证：Bearer；成功 HTTP 200。

返回实例完整配置与规则；关闭路由器时 agents=[]、rules=[]、total=0、default=""。

可用性：{"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": 200, "body": {"agents": [], "rules": [], "total": 0, "default": ""}}}.

请求体：无。

成功体：`AgentsAgentList`（字段见数据结构）。

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleListAgents`.

### `POST /api/v1/agents`

注册 Agent 实例.

认证：Bearer；成功 HTTP 200。

持久化成功后才发布运行时实例；无 Store 时只保存当前运行态。当前没有默认实例时，新注册实例会成为默认实例，包括之前主动清除默认值后的注册。provider/model 对要么均空跟随全局，要么选择当前可用配置；reasoning_policy 省略/null 为 inherit。K12 metadata、档案、初始进度的特殊事务规则见 K12 API，不将普通 Agent 注册理解为任意进度写入口。

可用性：{"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

请求体：`AgentsRegisterAgentRequest`; 最大读取 1048576 bytes. 字段详见下方结构表；“必填”以 handler 实际拒绝条件为准。

成功体：`AgentsAgentMessage`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | JSON、空名称、名称长度、描述/元数据、模型对、思考策略、温度或场景字段无效。 |
| `404` | 场景初始事务引用不存在。 |
| `409` | 重名、路由器注册失败（含纯空白 name）或初始场景版本冲突。 |
| `500` | 持久化或场景事务其他失败。 |
| `503` | 提交非空初始进度但场景持久化器未挂载。 |

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleRegisterAgent`.

### `PUT /api/v1/agents/{name}`

局部更新 Agent 配置.

认证：Bearer；成功 HTTP 200。

虽使用 PUT，实际按字段 patch：普通指针字段省略/null 保留，数组/metadata 提交时整体替换；temperature 省略保留/null 清除/数值设置。只有实际改变 provider/model 才重新验证模型，允许编辑其他字段时保留失效旧模型。已存在 K12 实例的档案字段必须通过 profile-bundle，不允许在此更新显示名、描述、提示词、模型、技能或所属教材字段。

可用性：{"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| 位置 / 字段 | 必填 | 类型 | 合同 |
| --- | --- | --- | --- |
| `path.name` | 是 | `string` | 现有稳定标识，URL 标识不通过 body 更名。  |

请求体：`AgentsUpdateAgentRequest`; 最大读取 1048576 bytes. 字段详见下方结构表；“必填”以 handler 实际拒绝条件为准。

成功体：`AgentsAgentMessage`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | JSON、温度、实际更名/长度、模型或元数据无效。 |
| `404` | 实例不存在或更新期间已不存在。 |
| `409` | 尝试修改 K12 profile-bundle 所有字段。 |
| `500` | 持久化失败。 |

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleUpdateAgent`.

### `DELETE /api/v1/agents/{name}`

注销 Agent 实例.

认证：Bearer；成功 HTTP 200。

删除实例与关联内存路由，默认实例需要原子重分配；调用已注入的归属资源清理器。持久化/资源清理失败会保留实例并尝试补偿，不承诺删除所有场景历史数据。

可用性：{"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| 位置 / 字段 | 必填 | 类型 | 合同 |
| --- | --- | --- | --- |
| `path.name` | 是 | `string` | 实例稳定标识。  |

请求体：无。

成功体：`AgentsMessage`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `404` | 实例未注册。 |
| `500` | 归属资源或持久化失败，可能含回滚失败信息。 |

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleUnregisterAgent`.

### `POST /api/v1/agents/default`

设置或清除默认 Agent.

认证：Bearer；成功 HTTP 200。

非空 name 设为默认实例；省略/空串清除默认值，成功 name=""。先持久化再发布默认配置。

可用性：{"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

请求体：`AgentsDefaultAgentRequest`; 最大读取 1048576 bytes. 字段详见下方结构表；“必填”以 handler 实际拒绝条件为准。

成功体：`AgentsAgentMessage`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | JSON 无效。 |
| `404` | 指定实例不存在。 |
| `500` | 持久化默认值失败。 |

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleSetDefaultAgent`.

### `GET /api/v1/agents/rules`

查询路由规则.

认证：Bearer；成功 HTTP 200。

关闭路由器仍返回 rules=[]、total=0。

可用性：{"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": 200, "body": {"rules": [], "total": 0}}}.

请求体：无。

成功体：`AgentsRuleList`（字段见数据结构）。

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleListRules`.

### `POST /api/v1/agents/rules`

添加路由规则.

认证：Bearer；成功 HTTP 200。

可组合 platform/instance_id/user_id/chat_id，空值为不限，最终目标必须是注册 Agent。持久化 SaveRule 先于运行时 AddRule；因此运行时拒绝返回 400 时，不应假定持久层没有写入。

可用性：{"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

请求体：`AgentsAddRuleRequest`; 最大读取 1048576 bytes. 字段详见下方结构表；“必填”以 handler 实际拒绝条件为准。

成功体：`AgentsRuleAdded`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | JSON、空 agent_name 或目标未注册。 |
| `500` | 持久化失败。 |

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleAddRule`.

### `POST /api/v1/agents/rules/test`

解释路由选择.

认证：Bearer；成功 HTTP 200。

返回显式匹配候选与来源，也可能经语义路由调用 LLM 或回退默认；不执行选中 Agent 的任务，不新增规则。body user_id 仅为模拟匹配值，不改变 HTTP 身份。

可用性：{"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

请求体：`AgentsRouteTestRequest`; 最大读取 1048576 bytes. 字段详见下方结构表；“必填”以 handler 实际拒绝条件为准。

成功体：`AgentsRouteTestResponse`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | JSON 无效。 |

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleTestRoute`.

### `DELETE /api/v1/agents/rules/{id}`

删除路由规则.

认证：Bearer；成功 HTTP 200。

按规则整数 ID 删除，持久删除成功后更新运行规则。

可用性：{"mounted_when": "s.agentRouter != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

| 位置 / 字段 | 必填 | 类型 | 合同 |
| --- | --- | --- | --- |
| `path.id` | 是 | `integer` | 整数规则 ID。  |

请求体：无。

成功体：`AgentsMessage`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | ID 无法按整数解析。 |
| `404` | 规则不存在。 |
| `500` | 持久化删除失败。 |

实现：[api/handler_misc.go](../../api/handler_misc.go) · `handleDeleteRule`.

### `GET /api/v1/subagents/runs`

查询子 Agent 执行记录.

认证：Bearer；成功 HTTP 200。

按 started_at 倒序；仅观测，不创建、取消或续接执行。注册表未挂载返回 runs=[]。limit 无效、非正值或省略均使用 200，不额外设置上限。

可用性：{"mounted_when": "always", "disabled_projection": {"status": 200, "body": {"runs": []}}, "disabled_when": "registry/Hub is not injected"}.

| 位置 / 字段 | 必填 | 类型 | 合同 |
| --- | --- | --- | --- |
| `query.limit` | 否 | `integer` | 正整数有效；无效/非正值使用 200。 默认 200. |

请求体：无。

成功体：`AgentsSubAgentList`（字段见数据结构）。

实现：[api/server.go](../../api/server.go) · `handleListSubAgentRuns`.

### `GET /api/v1/clawhub/search`

搜索 Hub 技能与 MCP 条目.

认证：Bearer；成功 HTTP 200。

确保目录种子/缓存并可能后台刷新；无需安装任何条目。q 非空做搜索，category/type 首尾修剪并转小写，空串或 all 不过滤；未知过滤值返回空结果，不是 400。Hub 未挂载返回 skills=[]、total=0、source=clawhub。

可用性：{"mounted_when": "always", "disabled_projection": {"status": 200, "body": {"skills": [], "total": 0, "source": "clawhub"}}, "disabled_when": "registry/Hub is not injected"}.

| 位置 / 字段 | 必填 | 类型 | 合同 |
| --- | --- | --- | --- |
| `query.q` | 否 | `string` | 可选关键字；验证长度后再修剪。  |
| `query.category` | 否 | `string` | 可选分类；空/all 不过滤。  |
| `query.type` | 否 | `string` | 常用 skill/mcp/all；未知值允许但无匹配。  |

请求体：无。

成功体：`AgentsHubSearch`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | 原始 q 超过 512 Unicode 字符。 |

实现：[api/handler_extended.go](../../api/handler_extended.go) · `handleClawHubSearch`.

### `GET /api/v1/clawhub/skills/{name}/content`

预览未安装的 Hub SKILL.md.

认证：Bearer；成功 HTTP 200。

获取目录原文，可能访问网络，不安装或写入安装目录；MCP 条目没有 SKILL.md 预览语义。

可用性：{"mounted_when": "always", "disabled_projection": {"status": 503, "body": {"error": "ClawHub 未启用"}}}.

| 位置 / 字段 | 必填 | 类型 | 合同 |
| --- | --- | --- | --- |
| `path.name` | 是 | `string` | 技能目录标识，单个文件名且不含 ..。  |

请求体：无。

成功体：`AgentsHubContent`（字段见数据结构）。

| 状态 | 实际错误 / 副作用边界 |
| --- | --- |
| `400` | 非法名称或条目类型为 MCP。 |
| `404` | 目录条目不存在。 |
| `502` | 获取原文失败。 |
| `503` | Hub 未挂载。 |

实现：[api/handler_skill_content.go](../../api/handler_skill_content.go) · `handleClawHubSkillContent`.

### `GET /api/v1/tools/cache/stats`

查询工具缓存统计.

认证：Bearer；成功 HTTP 200。

只读缓存条目、命中/未命中与百分比；无 query/body 参数。

可用性：{"mounted_when": "s.toolCache != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

请求体：无。

成功体：`AgentsToolCacheStats`（字段见数据结构）。

实现：[api/handler_tools.go](../../api/handler_tools.go) · `handleToolCacheStats`.

### `GET /api/v1/tools/metrics`

查询工具调用指标.

认证：Bearer；成功 HTTP 200。

固定最多 20 工具，按调用次数倒序；不接受 limit。文件不存在 tools 可为 null；读取失败仍 200 tools=[] 并带 error。

可用性：{"mounted_when": "s.toolMetrics != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

请求体：无。

成功体：`AgentsToolMetrics`（字段见数据结构）。

实现：[api/handler_tools.go](../../api/handler_tools.go) · `handleToolMetrics`.

### `GET /api/v1/tools/permissions`

查询当前工具权限规则.

认证：Bearer；成功 HTTP 200。

返回 allow 后 deny 的模式规则，不能通过此 GET 修改、启停或授权工具。

可用性：{"mounted_when": "s.toolPerms != nil", "disabled_projection": {"status": [404, 405], "description": "Route is not mounted; ServeMux returns 405 when another method remains registered for the same path, otherwise 404."}}.

请求体：无。

成功体：`AgentsToolPermissions`（字段见数据结构）。

实现：[api/handler_tools.go](../../api/handler_tools.go) · `handleToolPermissions`.

## 数据结构

请求的省略、null 与默认值可能不同；每个字段说明与上方操作语义一起使用。返回结构中“必有”来自非 omitempty 字段或固定 map。旧值原样保留的长度例外不应被 SDK 误改为无条件截断。

### `AgentsMessage`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `message` | 是 | `string` | 操作说明，保留服务实际文案。 |

### `AgentsAgentMessage`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `message` | 是 | `string` | 操作说明。 |
| `name` | 是 | `string` | 实例稳定标识。 |

### `AgentsRole`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `name` | 是 | `string` | 角色标识。 |
| `title` | 是 | `string` | 角色标题。 |
| `goal` | 是 | `string` | 角色目标。 |
| `backstory` | 是 | `string` | 背景设定。 |
| `expertise` | 是 | `array / null<string>` | 专业领域。 |
| `tools` | 是 | `array / null<string>` | 角色工具。 |
| `constraints` | 是 | `array / null<string>` | 角色约束。 |

### `AgentsRoleList`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `roles` | 是 | `array<AgentsRole>` | 角色数组；非 ReActEngine 返回空数组。 |

### `AgentsReasoningPolicy`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `mode` | 否 | `string` | auto / inherit / on / off / effort；对象内省略时为 auto。 枚举：`auto`, `inherit`, `on`, `off`, `effort`. 默认："auto". |
| `effort` | 否 | `string` | 仅 mode=effort 时必填，其他模式不得设置。 枚举：`low`, `medium`, `high`, `xhigh`, `max`. |

### `AgentsAgentConfig`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `name` | 是 | `string` | 稳定标识；创建要求非空，纯空白会被路由器拒绝。 |
| `display_name` | 是 | `string` | 显示名；新建或实际更名最多 64 个 Unicode 字符；更名时修剪首尾空白。 |
| `description` | 是 | `string` | 描述；新增或改变值最多 2000 个 Unicode 字符，旧值原样回传保留。 |
| `model` | 是 | `string` | 模型；与 provider 的最终值同时为空或同时非空，非空时验证当前可用文本模型。 |
| `provider` | 是 | `string` | 当前服务的 Provider 配置键；可大小写匹配后归一为实际键。空模型对跟随全局。 |
| `system_prompt` | 是 | `string` | 系统提示词；本接口不运行聊天。 |
| `skills` | 是 | `array / null<string>` | 技能标识数组；创建省略时为 null。 |
| `max_tokens` | 是 | `integer` | 0 表示未设，跟随模型默认；handler 不额外验证数值范围。 默认：0. |
| `reasoning_policy` | 是 | `AgentsReasoningPolicy / null` | 创建省略或 null 归一为 inherit；更新省略或 null 不改变。 |
| `temperature` | 否 | `number / null` | 0–2；创建省略/null 未设；更新省略保留、null 清除、0 为显式零。 |
| `metadata` | 是 | `object / null` | 场景元数据；K12 特殊合同见 ../../scenarios/k12/API.md；创建/更改 k12.child_name 上限 40 字符。 |

### `AgentsRegisterAgentRequest`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `name` | 是 | `string` | 稳定标识；创建要求非空，纯空白会被路由器拒绝。 |
| `display_name` | 否 | `string` | 显示名；新建或实际更名最多 64 个 Unicode 字符；更名时修剪首尾空白。 |
| `description` | 否 | `string` | 描述；新增或改变值最多 2000 个 Unicode 字符，旧值原样回传保留。 |
| `model` | 否 | `string` | 模型；与 provider 的最终值同时为空或同时非空，非空时验证当前可用文本模型。 |
| `provider` | 否 | `string` | 当前服务的 Provider 配置键；可大小写匹配后归一为实际键。空模型对跟随全局。 |
| `system_prompt` | 否 | `string` | 系统提示词；本接口不运行聊天。 |
| `skills` | 否 | `array / null<string>` | 技能标识数组；创建省略时为 null。 |
| `max_tokens` | 否 | `integer` | 0 表示未设，跟随模型默认；handler 不额外验证数值范围。 默认：0. |
| `reasoning_policy` | 否 | `AgentsReasoningPolicy / null` | 创建省略或 null 归一为 inherit；更新省略或 null 不改变。 |
| `temperature` | 否 | `number / null` | 0–2；创建省略/null 未设；更新省略保留、null 清除、0 为显式零。 |
| `metadata` | 否 | `object / null` | 场景元数据；K12 特殊合同见 ../../scenarios/k12/API.md；创建/更改 k12.child_name 上限 40 字符。 |
| `curriculum_progress` | 否 | `object / null` | 可省略/null；非空交给场景事务与实例原子保存，具体字段见 K12 API。 |

### `AgentsUpdateAgentRequest`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `display_name` | 否 | `string / null` | 显示名；新建或实际更名最多 64 个 Unicode 字符；更名时修剪首尾空白。 |
| `description` | 否 | `string / null` | 描述；新增或改变值最多 2000 个 Unicode 字符，旧值原样回传保留。 |
| `model` | 否 | `string / null` | 模型；与 provider 的最终值同时为空或同时非空，非空时验证当前可用文本模型。 |
| `provider` | 否 | `string / null` | 当前服务的 Provider 配置键；可大小写匹配后归一为实际键。空模型对跟随全局。 |
| `system_prompt` | 否 | `string / null` | 系统提示词；本接口不运行聊天。 |
| `skills` | 否 | `array / null<string>` | 技能标识数组；创建省略时为 null。 |
| `max_tokens` | 否 | `integer / null` | 0 表示未设，跟随模型默认；handler 不额外验证数值范围。 默认：0. |
| `reasoning_policy` | 否 | `AgentsReasoningPolicy / null` | 创建省略或 null 归一为 inherit；更新省略或 null 不改变。 |
| `temperature` | 否 | `number / null` | 0–2；创建省略/null 未设；更新省略保留、null 清除、0 为显式零。 |
| `metadata` | 否 | `object / null` | 场景元数据；K12 特殊合同见 ../../scenarios/k12/API.md；创建/更改 k12.child_name 上限 40 字符。 |

### `AgentsDefaultAgentRequest`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `name` | 否 | `string` | 可省略或空串，用于清除默认 Agent；非空必须已注册。 默认："". |

### `AgentsRule`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `id` | 是 | `integer` | 持久化规则 ID；无持久层时可为 0。 |
| `platform` | 是 | `string` | 匹配的平台，空串表示不限定。 默认："". |
| `instance_id` | 是 | `string` | 匹配的平台实例，空串表示不限定。 默认："". |
| `user_id` | 是 | `string` | 匹配消息用户，不改变 HTTP 鉴权身份。 默认："". |
| `chat_id` | 是 | `string` | 匹配会话，空串表示不限定。 默认："". |
| `agent_name` | 是 | `string` | 目标实例稳定标识。 默认："". |
| `priority` | 是 | `integer` | 默认 0；越大越优先，仍结合匹配范围判定。 默认：0. |

### `AgentsAddRuleRequest`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `platform` | 否 | `string` | 匹配的平台，空串表示不限定。 默认："". |
| `instance_id` | 否 | `string` | 匹配的平台实例，空串表示不限定。 默认："". |
| `user_id` | 否 | `string` | 匹配消息用户，不改变 HTTP 鉴权身份。 默认："". |
| `chat_id` | 否 | `string` | 匹配会话，空串表示不限定。 默认："". |
| `agent_name` | 是 | `string` | 目标实例稳定标识。 默认："". |
| `priority` | 否 | `integer` | 默认 0；越大越优先，仍结合匹配范围判定。 默认：0. |

### `AgentsRuleList`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `rules` | 是 | `array<AgentsRule>` | 路由规则。 |
| `total` | 是 | `integer` | 规则数量。 |

### `AgentsRuleAdded`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `message` | 是 | `string` | 操作说明。 |
| `id` | 是 | `integer` | 保存后的规则 ID。 |

### `AgentsAgentList`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `agents` | 是 | `array<AgentsAgentConfig>` | 已注册实例。 |
| `rules` | 是 | `array<AgentsRule>` | 路由规则。 |
| `total` | 是 | `integer` | 实例数量。 |
| `default` | 是 | `string` | 默认实例名，无默认时为空串。 |

### `AgentsRouteTestRequest`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `platform` | 否 | `string` | 匹配的平台，空串表示不限定。 默认："". |
| `instance_id` | 否 | `string` | 匹配的平台实例，空串表示不限定。 默认："". |
| `user_id` | 否 | `string` | 匹配消息用户，不改变 HTTP 鉴权身份。 默认："". |
| `chat_id` | 否 | `string` | 匹配会话，空串表示不限定。 默认："". |
| `message` | 否 | `string` | 可选，用于语义 fallback；可能触发模型调用。 默认："". |

### `AgentsRuleMatch`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `rule` | 是 | `AgentsRule` |  |
| `score` | 是 | `integer` | 匹配分数。 |

### `AgentsRouteTestResponse`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `matched` | 是 | `boolean` | 是否匹配到路由结果。 |
| `agent_name` | 是 | `string` | 最终实例名，未命中时为空串。 |
| `source` | 是 | `string` | 路由来源。 枚举：`rule`, `llm`, `default`, `none`. |
| `rule` | 是 | `AgentsRule / null` | 显式命中规则，其他路径可能为 null。 |
| `score` | 是 | `integer` | 显式规则得分。 |
| `matches` | 是 | `array / null<AgentsRuleMatch>` | 候选规则，可为 null。 |
| `message` | 是 | `string` | 解释文本。 |

### `AgentsPrompt`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `id` | 是 | `string` | 空串/省略生成 pr- 前缀 ID；已有 ID 做整条 upsert。 |
| `type` | 是 | `string` | 空白/省略为 prompt；prompt/command 为常用值，handler 不验证枚举。 默认："prompt". |
| `title` | 是 | `string` | 必填非空；新增/改变最多 200 字符，不修剪后再判空。 |
| `body_md` | 是 | `string` | Markdown 正文；省略写空串。 |
| `args_json` | 是 | `string` | 参数声明字符串，不是嵌套 JSON 对象。 |
| `tool_scope` | 是 | `string` | 逗号分隔的工具标识；新增标识最多 256 字符。 |
| `model` | 是 | `string` | 建议模型字符串；新增/改变最多 256 字符，此接口不验证 Provider 目录。 |
| `category` | 是 | `string` | 分类；新增/改变最多 256 字符。 |
| `enabled` | 是 | `boolean` | 默认 false；省略会保存为停用，更新不会保留旧 enabled。 默认：false. |
| `updated_at` | 是 | `string` | 响应为服务保存时间；请求提供时须可解码为 RFC3339，但存储使用当前时间。 |

### `AgentsPromptWrite`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `id` | 否 | `string` | 空串/省略生成 pr- 前缀 ID；已有 ID 做整条 upsert。 |
| `type` | 否 | `string` | 空白/省略为 prompt；prompt/command 为常用值，handler 不验证枚举。 默认："prompt". |
| `title` | 是 | `string` | 必填非空；新增/改变最多 200 字符，不修剪后再判空。 |
| `body_md` | 否 | `string` | Markdown 正文；省略写空串。 |
| `args_json` | 否 | `string` | 参数声明字符串，不是嵌套 JSON 对象。 |
| `tool_scope` | 否 | `string` | 逗号分隔的工具标识；新增标识最多 256 字符。 |
| `model` | 否 | `string` | 建议模型字符串；新增/改变最多 256 字符，此接口不验证 Provider 目录。 |
| `category` | 否 | `string` | 分类；新增/改变最多 256 字符。 |
| `enabled` | 否 | `boolean` | 默认 false；省略会保存为停用，更新不会保留旧 enabled。 默认：false. |
| `updated_at` | 否 | `string` | 响应为服务保存时间；请求提供时须可解码为 RFC3339，但存储使用当前时间。 |

### `AgentsPromptList`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `prompts` | 是 | `array / null<AgentsPrompt>` | Prompt 数组，可为 null。 |
| `total` | 是 | `integer` | 条目数量。 |

### `AgentsPromptID`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `id` | 是 | `string` | 保存后的 ID。 |

### `AgentsPromptDeleted`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `deleted` | 是 | `string` | 请求删除的 ID；不存在也可成功。 |

### `AgentsMCPServerSummary`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `name` | 是 | `string` | 服务器名。 |
| `description` | 是 | `string` | 服务器用途摘要。 |
| `status` | 是 | `string` | 连接状态。 枚举：`connected`, `disconnected`. |
| `transport` | 是 | `string` | 已配置传输投影；http 别名当前可能投影为 unknown。 枚举：`stdio`, `sse`, `streamable`, `unknown`. |
| `tool_count` | 是 | `integer` | 已发现工具数量。 |
| `last_error` | 否 | `string` | 最近连接错误，可省略。 |
| `retryable` | 否 | `boolean` | 可选重试分类，不是副作用重放授权。 |
| `retry_state` | 否 | `string` | 连接重试状态，以实际值为准。 |
| `retry_count` | 否 | `integer` | 连接重试计数，可省略。 |
| `next_retry_at` | 否 | `string` | 下次连接重试时间，可省略。 |

### `AgentsMCPServers`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `servers` | 是 | `array<AgentsMCPServerSummary>` | 配置与连接状态投影，不包含 command/args/env/endpoint。 |
| `total` | 是 | `integer` | 服务器数量。 |

### `AgentsMCPServerStatus`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `name` | 是 | `string` | 服务器名。 |
| `kind` | 否 | `string` | 推断的服务器类型，可省略。 |
| `connected` | 是 | `boolean` | 当前是否已连接。 |
| `tool_count` | 是 | `integer` | 工具数量。 |
| `last_error` | 否 | `string` | 最近连接错误，可省略。 |
| `retryable` | 否 | `boolean` | 可选重试分类，不是副作用重放授权。 |
| `retry_state` | 否 | `string` | 连接重试状态，以实际值为准。 |
| `retry_count` | 否 | `integer` | 连接重试计数，可省略。 |
| `next_retry_at` | 否 | `string` | 下次连接重试时间，可省略。 |

### `AgentsMCPStatus`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `servers` | 是 | `array<AgentsMCPServerStatus>` | 逐服务器运行状态。 |
| `total` | 是 | `integer` | 服务器数量。 |

### `AgentsMCPTool`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `name` | 是 | `string` | 公开工具名；无 server_name 调用时可原样使用。 |
| `description` | 是 | `string` | 工具说明。 |
| `server_name` | 是 | `string` | 服务器归属。 |
| `input_schema` | 否 | `any JSON` | 工具参数的 JSON Schema。 |

### `AgentsMCPTools`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `tools` | 是 | `array<AgentsMCPTool>` | 工具列表。 |
| `total` | 是 | `integer` | 工具数量。 |

### `AgentsMCPSecretArg`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `index` | 否 | `integer` | args 中的索引，省略为 0，必须落在本次 args 范围内。 默认：0. |
| `mode` | 是 | `string` | preserve 恢复旧值；replace 使用本次值；clear 清除秘密引用，保留本次普通参数值。 枚举：`preserve`, `replace`, `clear`. |
| `credential_ref` | 否 | `string` | replace 必须提供 sidecar-connection:v1: 非空引用；preserve 可继承已有引用，clear 忽略。 |

### `AgentsMCPSecretEnv`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `key` | 是 | `string` | 非空 env 键。 |
| `mode` | 是 | `string` | preserve/replace/clear；clear 值为空时移除 env 键，否则保留普通值并清除引用。 枚举：`preserve`, `replace`, `clear`. |
| `credential_ref` | 否 | `string` | replace 必须提供 sidecar-connection:v1: 非空引用；preserve 可继承已有引用，clear 忽略。 |

### `AgentsMCPAddRequest`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `name` | 是 | `string` | 必填非空服务器名；新增/变更上限 64 Unicode 字符，旧值原样保留。 |
| `transport` | 否 | `string` | 省略或空字符串：有 endpoint 则 sse，否则 stdio。http 为 Streamable HTTP 别名。 枚举：`""`, `stdio`, `sse`, `streamable`, `http`. |
| `command` | 否 | `string` | stdio 必填；仅拒绝空值与换行/回车/NUL，支持自定义可执行文件。 |
| `args` | 否 | `array / null<string>` | stdio 参数；不得含换行/回车/NUL。 |
| `env` | 否 | `object / null` | stdio 子进程环境，省略/null 为未提供。 |
| `endpoint` | 否 | `string` | 网络传输必填，http/https 且有 host；新增/变更最多 8192 UTF-8 字节。 |
| `secret_args` | 否 | `array / null<AgentsMCPSecretArg>` | 可选秘密参数变更；索引不可重复。 |
| `secret_env` | 否 | `array / null<AgentsMCPSecretEnv>` | 可选秘密环境变更；键不可重复。 |

### `AgentsMCPAdded`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `message` | 是 | `string` | 登记结果说明。 |
| `connected` | 是 | `boolean` | 即时连接是否成功；false 表示已登记并后台连接。 |

### `AgentsMCPCallRequest`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `server_name` | 否 | `string` | 可省略；提供时 name 应为该服务器的上游原名。 |
| `name` | 是 | `string` | 必填非空；未提供 server_name 时支持 GET tools 返回的公开名。 |
| `arguments` | 否 | `object / null` | 按发现的 input_schema 准备；省略时为 null。 |

### `AgentsMCPCallResponse`

`{"result":"..."}` 或 `{"error":"..."}`；两者都可能 HTTP 200。

### `AgentsSkillStatus`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `name` | 否 | `string` | 技能名称，列表中存在；状态更新响应可省略。 |
| `description` | 否 | `string` | 说明。 |
| `author` | 否 | `string` | 作者。 |
| `version` | 否 | `string` | 版本。 |
| `triggers` | 否 | `array<string>` | 触发词。 |
| `tags` | 否 | `array<string>` | 标签。 |
| `icon` | 否 | `string` | 图标。 |
| `enabled` | 是 | `boolean` | 持久化的期望状态。 |
| `effective_enabled` | 是 | `boolean` | 当前运行时实际状态。 |
| `requires_restart` | 是 | `boolean` | 持久化与运行时未对齐时可能为 true。 |
| `message` | 否 | `string` | 状态说明。 |

### `AgentsSkillList`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `skills` | 是 | `array<AgentsSkillStatus>` | 已安装技能。 |
| `total` | 是 | `integer` | 技能数量。 |
| `dir` | 是 | `string` | 服务端技能目录，不是客户端文件路径。 |

### `AgentsSkillStatusRequest`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `enabled` | 否 | `boolean` | 省略等同 false，将禁用技能；不是保留原值。 默认：false. |

### `AgentsSkillInstallRequest`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `source` | 否 | `string` | 除 type=content 外必填；服务端文件/目录、HTTPS URL 或 Hub 名称。 |
| `type` | 否 | `string` | 省略或空字符串按 source 推断：clawhub://→clawhub，https://→url，其余→file。 枚举：`""`, `file`, `url`, `clawhub`, `content`. |
| `content` | 否 | `string` | type=content 必填非空完整 SKILL.md；首段闭合 frontmatter，name 为 [a-z0-9_-]+；上限 1 MiB UTF-8 字节。 |

### `AgentsSkillInstalled`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `name` | 是 | `string` | 安装项名称。 |
| `description` | 否 | `string` | 技能描述，部分来源省略。 |
| `version` | 否 | `string` | 技能版本，部分来源省略。 |
| `type` | 否 | `string` | Hub MCP 安装为 mcp，普通技能可省略。 |
| `message` | 是 | `string` | 结果说明。 |
| `requires_restart` | 是 | `boolean` | 安装 handler 返回 false；实际启用状态另看 GET skills。 |
| `runtime_registered` | 是 | `boolean` | 普通技能返回 true；Hub MCP 为即时连接结果，不替代状态查询。 |
| `config_hint` | 否 | `string` | Hub MCP 配置提示，可省略。 |
| `artifact` | 否 | `AgentsHubArtifact` |  |

### `AgentsGenerateSkillRequest`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `description` | 是 | `string` | 必填，先修剪首尾空白后要求非空且最多 2000 Unicode 字符。 |

### `AgentsGeneratedSkill`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `content` | 是 | `string` | 模型生成并移除围栏后的 SKILL.md 草稿，尚未安装。 |

### `AgentsSkillContent`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `name` | 是 | `string` | 技能名。 |
| `path` | 是 | `string` | 服务端安装文件路径。 |
| `content` | 是 | `string` | 原始 SKILL.md 文本。 |

### `AgentsHubArtifact`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `ecosystem` | 是 | `string` | npm / pypi。 |
| `package` | 是 | `string` | 发行包名称。 |
| `version` | 是 | `string` | 精确发行版。 |
| `integrity` | 是 | `string` | 目录声明的完整性元数据，不等于实际运行包的安全承诺。 |
| `source_registry` | 否 | `string` | 来源仓库。 |
| `resolved_at` | 否 | `string` | 解析时间标记。 |

### `AgentsHubSkillMeta`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `name` | 是 | `string` | 条目标识。 |
| `display_name` | 是 | `string` | 显示名称。 |
| `description` | 是 | `string` | 说明。 |
| `version` | 是 | `string` | 版本。 |
| `author` | 是 | `string` | 作者。 |
| `category` | 是 | `string` | 分类。 |
| `type` | 否 | `string` | skill 或 mcp；省略按 skill 解释。 |
| `url` | 是 | `string` | 文件来源 URL。 |
| `command` | 否 | `string` | MCP 命令，可省略。 |
| `config_hint` | 否 | `string` | 配置提示，可省略。 |
| `source` | 否 | `string` | 目录来源标识。 |
| `status` | 否 | `string` | 如 pinned / quarantined，以目录实际值为准。 |
| `quarantine_reason` | 否 | `string` | 隔离说明，可省略。 |
| `file` | 否 | `string` | 文件标识，可省略。 |
| `sha256` | 否 | `string` | 摘要，可省略。 |
| `trust` | 否 | `string` | 信任元数据，可省略。 |
| `min_engine_version` | 否 | `string` | 声明的最低引擎版本。 |
| `license` | 否 | `string` | 许可证。 |
| `schema_version` | 否 | `string` | 目录 Schema 版本。 |
| `eval` | 否 | `string` | 评估说明。 |
| `acceptance` | 否 | `string` | 验收说明。 |
| `tags` | 是 | `array / null<string>` | 标签。 |
| `dependencies` | 否 | `array / null<string>` | 依赖。 |
| `args` | 否 | `array / null<string>` | MCP 参数。 |
| `requires` | 否 | `array / null<string>` | 能力前提。 |
| `outputs` | 否 | `array / null<string>` | 声明产物。 |
| `env` | 否 | `object / null` | MCP 环境声明。 |
| `artifact` | 否 | `AgentsHubArtifact / null` | MCP 发行元数据。 |
| `size` | 否 | `integer` | 文件字节数。 |
| `downloads` | 是 | `integer` | 目录声明的下载计数。 |
| `rating` | 是 | `number` | 目录评分。 |

### `AgentsHubSearch`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `skills` | 是 | `array<AgentsHubSkillMeta>` | 匹配条目。 |
| `total` | 是 | `integer` | 本次返回数量，无分页。 |
| `source` | 是 | `string` | 固定 clawhub。 枚举：`clawhub`. |

### `AgentsHubContent`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `name` | 是 | `string` | 条目名。 |
| `content` | 是 | `string` | 原始 SKILL.md 文本。 |

### `AgentsSubAgentRun`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `id` | 是 | `string` | 执行 ID。 |
| `parent_id` | 否 | `string` | 父执行 ID，可省略。 |
| `agent` | 是 | `string` | 角色名。 |
| `role` | 是 | `string` | main / orchestrator / leaf。 |
| `mode` | 是 | `string` | run / session。 |
| `status` | 是 | `string` | running / ok / error / timeout。 |
| `session_id` | 否 | `string` | 子会话 ID，可省略。 |
| `task` | 否 | `string` | 任务，可省略。 |
| `output` | 否 | `string` | 结果，可省略。 |
| `error` | 否 | `string` | 执行错误，可省略。 |
| `depth` | 是 | `integer` | 派生深度。 |
| `tool_allow` | 否 | `array<string>` | 继承的允许工具。 |
| `tool_deny` | 否 | `array<string>` | 继承的拒绝工具。 |
| `started_at` | 是 | `string` | 开始时间。 |
| `ended_at` | 否 | `string` | 结束时间，运行中可为零值时间；Go time.Time 的 omitempty 不保证省略。 |

### `AgentsSubAgentList`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `runs` | 是 | `array<AgentsSubAgentRun>` | 按 started_at 倒序的执行记录；未启用注册表为空数组。 |

### `AgentsToolCacheStats`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `entries` | 是 | `integer` | 当前缓存条目数。 |
| `hits` | 是 | `integer` | 缓存命中次数。 |
| `misses` | 是 | `integer` | 未命中次数。 |
| `hit_rate` | 是 | `number` | 命中率百分数 0–100，无访问时为 0。 |

### `AgentsToolMetric`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `tool` | 是 | `string` | 工具名。 |
| `call_count` | 是 | `integer` | 调用次数。 |
| `success_rate` | 是 | `number` | 成功百分比 0–100。 |
| `avg_latency_ms` | 是 | `number` | 平均延迟，毫秒。 |
| `cached_count` | 是 | `integer` | 缓存调用数量。 |

### `AgentsToolMetrics`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `tools` | 是 | `array / null<AgentsToolMetric>` | 最多 20 项，按 call_count 降序；无文件时可为 null，读取错误时为空数组。 |
| `error` | 否 | `string` | 读取失败仍 HTTP 200，并带此字段。 |

### `AgentsToolPermission`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `pattern` | 是 | `string` | 规则模式。 |
| `action` | 是 | `string` | 规则动作。 枚举：`allow`, `deny`. |

### `AgentsToolPermissions`

| 字段 | 必有 / 必填 | 类型 | 说明 |
| --- | --- | --- | --- |
| `rules` | 是 | `array<AgentsToolPermission>` | 先 allow 后 deny；仅查询，不修改配置。 |

## 请求与响应示例

以下为静态合同示例，不代表已调用服务。先将变量设为当前服务与自己的令牌；示例标识/路径应替换为该服务真实可用值。

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

MCP：先查询工具 Schema；无服务器归属时使用发现的公开 name，有 server_name 时使用该服务器的上游原名。以下假设服务已配置 files/read_file，返回值是字符串。

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

模块关闭时的真实错误投影，同样是 HTTP 200：

```json
{
  "error": "MCP 模块未启用"
}
```

Prompt 的完整 upsert 请求（enabled 明确为 true）；省略 enabled 不会保留旧状态：

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

技能草稿须经过安装接口另行落盘；请求类型是 JSON 字段，不是 ?type=content 查询参数：

```json
{
  "type": "content",
  "content": "---\nname: demo-review\ndescription: Review a change\nversion: \"1.0.0\"\n---\n# Review\nCheck contracts and actual outputs.\n"
}
```

## 来源与版本边界

路由挂载以 [api/server.go](../../api/server.go) 为准；请求/响应以逐项 handler 和本文 DTO 为准。本文与 OpenAPI 是当前源码参考，不能代替已发布服务版本、模型执行或外部工具副作用的真实验证。
