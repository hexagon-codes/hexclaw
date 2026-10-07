# 模型、系统与媒体 API 参考

[API 总览](../api.md) · [English](models-system-media.en.md) · [OpenAPI](../../api/openapi.yaml)

本页覆盖当前源码的 53 个方法/路径组合，包括条件模块及原生内部端点。发布二进制可能不同，应使用对应版本的文档。下文给出调用契约，源码链接仅作实现参考。

## 调用前提

- 除 `GET /health`、`GET /api/v1/version` 外，业务端点使用 `Authorization: Bearer ${HEXCLAW_API_TOKEN}`。使用本服务配置生成的令牌，不把令牌放入 URL。原生内部端点另需专用 capability 和回环来源。
- JSON 请求使用 `Content-Type: application/json`。音频上传/响应、生成文件下载、SSE、WebSocket 和纯文本 handler 错误按各操作的 Content-Type 处理。
- “必填 / 必有”分别表示请求必填或响应必有。仅明确注明的值有默认行为；允许 null 的 patch 按对应操作保留原值。下方 Schema 列出全部声明字段及嵌套对象；任意 map 仅用于实际的 Provider 参数或日志数据。
- 能力缓存、STT/TTS、预算和桌面宿主路由仅在对应服务注入后挂载；未注入时无匹配路由。其他媒体状态路由始终存在，未配置时返回 `enabled:false`，生成请求返回 503。
- health 表示进程健康，不代表模型就绪。探测/生成会调用上游并可能计费；HTTP 200 或提交任务 ID 不代表完整产物已交付。

```bash
export HEXCLAW_API="http://127.0.0.1:16060"
export HEXCLAW_API_TOKEN="REPLACE_WITH_YOUR_SERVICE_TOKEN"
```

示例标识与响应为契约示意。将 `REPLACE_WITH_*` 替换为本服务实际返回的值和 Provider 支持的模型。会改变状态的示例均说明副作用；上游结果未知时不要盲目重放。

## 目录

- [健康、版本与服务生命周期](#system)
- [LLM 配置与能力](#llm)
- [记忆与默认助理](#memory-soul)
- [语音、图片、视频与生成文件](#media)
- [日志与配置投影](#logs-config)
- [Ollama 目标与模型管理](#ollama)
- [桌面宿主能力](#desktop)
- [原生内部凭据协调](#native)
- [数据 Schema](#data-schemas)

<a id="system"></a>

## 健康、版本与服务生命周期

| 方法 | 路径 | 操作 |
| --- | --- | --- |
| GET | `/health` | [公开服务健康](#op-get-health) |
| POST | `/api/v1/service/restart` | [受理托管服务重启](#op-post-api-v1-service-restart) |
| GET | `/api/v1/stats` | [读取进程统计](#op-get-api-v1-stats) |
| GET | `/api/v1/version` | [公开版本信息](#op-get-api-v1-version) |
| GET | `/api/v1/budget/status` | [读取当前任务预算](#op-get-api-v1-budget-status) |

<a id="op-get-health"></a>

### GET `/health` — 公开服务健康

认证: 公开，无需令牌。

挂载条件: `始终挂载`.

不要求令牌；process_instance_id每次进程重建改变；健康不证明特定模型任务已完成。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsHealth](#schema-modelshealth) | 成功响应 |
| 503 | `application/json` | [ModelsHealth](#schema-modelshealth) | 引擎健康检查失败，status=unhealthy |

实现参考: [`handleHealth`](../../api/server.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl "${HEXCLAW_API}/health"
```

```json
{
  "status": "healthy",
  "process_instance_id": "example-process-generation",
  "restart_supported": false
}
```

<a id="op-post-api-v1-service-restart"></a>

### POST `/api/v1/service/restart` — 受理托管服务重启

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

JSON上限4 KiB。读取health后提交对应进程代际和非空request_id；202仅接纳，需观察新代际健康。相同代际同ID重放只返回接纳，不重复退出。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：

- `application/json`: [ModelsRestartRequest](#schema-modelsrestartrequest); 整体请求体上限 4 KiB.

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `expected_process_instance_id` | string | 是 | 最短: 1 | 先从 /health 读取当前进程代际 |
| `request_id` | string | 是 | 最短: 1 | 当前进程代际的稳定标识 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 202 | `application/json` | [ModelsRestartAccepted](#schema-modelsrestartaccepted) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 409 | `application/json` | [MessageError](#schema-messageerror) | 进程代际改变、无托管重启能力或另一重启在途 |

实现参考: [`handleRestartService`](../../api/service_lifecycle.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X POST "${HEXCLAW_API}/api/v1/service/restart" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{"expected_process_instance_id":"REPLACE_WITH_HEALTH_PROCESS_ID","request_id":"restart-example-1"}'
```

```json
{
  "status": "restarting",
  "process_instance_id": "REPLACE_WITH_HEALTH_PROCESS_ID",
  "request_id": "restart-example-1"
}
```

<a id="op-get-api-v1-stats"></a>

### GET `/api/v1/stats` — 读取进程统计

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

统计可缓存250ms；只反映本后端进程内存、GC、goroutine及运行时长，不代表模型或任务吞吐验收。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsStats](#schema-modelsstats) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleStats`](../../api/handler_extended.go).

<a id="op-get-api-v1-version"></a>

### GET `/api/v1/version` — 公开版本信息

认证: 公开，无需令牌。

挂载条件: `始终挂载`.

无需令牌；engine字段固定返回Hexagon，具体Agent任务运行方式由服务决定。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsVersion](#schema-modelsversion) | 成功响应 |

实现参考: [`handleVersion`](../../api/handler_extended.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl "${HEXCLAW_API}/api/v1/version"
```

```json
{
  "version": "example-build-version",
  "engine": "Hexagon",
  "engine_version": "example-engine-version"
}
```

<a id="op-get-api-v1-budget-status"></a>

### GET `/api/v1/budget/status` — 读取当前任务预算

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `s.budgetCtrl != nil`.

仅budgetCtrl存在时挂载，否则404。金额、token与时长是控制器状态，不是全系统已结算账单。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsBudget](#schema-modelsbudget) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleBudgetStatus`](../../api/handler_tools.go).

<a id="llm"></a>

## LLM 配置与能力

| 方法 | 路径 | 操作 |
| --- | --- | --- |
| GET | `/api/v1/config/llm` | [读取脱敏LLM配置](#op-get-api-v1-config-llm) |
| POST | `/api/v1/config/llm/providers/{provider_instance_id}/reveal-key` | [显式读取Provider Key](#op-post-api-v1-config-llm-providers-provider-instance-id-reveal-key) |
| GET | `/api/v1/config/mutations/{request_id}` | [查询已提交配置证明](#op-get-api-v1-config-mutations-request-id) |
| PUT | `/api/v1/config/llm` | [替换或更新LLM配置](#op-put-api-v1-config-llm) |
| POST | `/api/v1/config/llm/test` | [执行Provider连接测试](#op-post-api-v1-config-llm-test) |
| POST | `/api/v1/config/llm/probe` | [执行精确模型能力探测](#op-post-api-v1-config-llm-probe) |
| POST | `/api/v1/config/llm/models` | [发现Provider模型目录](#op-post-api-v1-config-llm-models) |
| GET | `/api/v1/llm/capabilities` | [读取工具调用能力缓存](#op-get-api-v1-llm-capabilities) |
| POST | `/api/v1/llm/capabilities/probe` | [触发工具调用探测](#op-post-api-v1-llm-capabilities-probe) |

<a id="op-get-api-v1-config-llm"></a>

### GET `/api/v1/config/llm` — 读取脱敏LLM配置

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

Provider map key用于路由，provider_instance_id是稳定身份。api_key为脱敏值；effective_models、probe_receipt和配置revision/digest是只读投影。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsLLMRead](#schema-modelsllmread) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [APIError](#schema-apierror) | 配置摘要不可用 |

实现参考: [`handleGetLLMConfig`](../../api/handler_config.go).

<a id="op-post-api-v1-config-llm-providers-provider-instance-id-reveal-key"></a>

### POST `/api/v1/config/llm/providers/{provider_instance_id}/reveal-key` — 显式读取Provider Key

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

无body。仅匹配稳定provider_instance_id；响应Cache-Control:no-store，api_key为当前明文或空值，不应当作普通配置列表读取。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `provider_instance_id` | path | 是 | string | — | 实际资源标识；使用服务响应值 |

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsKeyReveal](#schema-modelskeyreveal) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 404 | `application/json` | [MessageError](#schema-messageerror) | 资源不存在 |

实现参考: [`handleRevealProviderKey`](../../api/handler_config_proof.go).

<a id="op-get-api-v1-config-mutations-request-id"></a>

### GET `/api/v1/config/mutations/{request_id}` — 查询已提交配置证明

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

query operation_kind必填llm或ollama_target。仅查询提交证明；404不是未提交的证明，不能据此盲目重发。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `request_id` | path | 是 | string | — | 实际资源标识；使用服务响应值 |
| `operation_kind` | query | 是 | string | 枚举：`"llm"`, `"ollama_target"` | 提交操作类型 |

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsMutationReceipt](#schema-modelsmutationreceipt) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 404 | `application/json` | [MessageError](#schema-messageerror) | 资源不存在 |
| 409 | `application/json` | [MessageError](#schema-messageerror) | 操作类型与已提交证明不符 |

实现参考: [`handleGetConfigMutation`](../../api/handler_config_proof.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl "${HEXCLAW_API}/api/v1/config/mutations/llm-example-1?operation_kind=llm" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}"
```

```json
{
  "status": "ok",
  "operation_kind": "llm",
  "request_id": "llm-example-1",
  "config_revision": 3,
  "config_digest": "example-config-digest",
  "target_revision": 0,
  "target_digest": "",
  "committed_at": 1791292800000,
  "replayed": true
}
```

<a id="op-put-api-v1-config-llm"></a>

### PUT `/api/v1/config/llm` — 替换或更新LLM配置

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

1 MiB JSON。providers省略/null保留，提供map则整体替换；model_specs省略/null保留既有声明模式，[]为显式未分类。default空保留；默认模型必须含text。expected_config_revision/digest必须一起提供。api_key与api_key_mutation互斥；preserve需已有Provider，replace仅引用已注入的原生凭据，delete清除。仅****脱敏Key保留旧Key；普通空api_key不会自动保留。成功持久化并热更新；同Idempotency-Key内容冲突409；结果未知先查提交证明。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `Idempotency-Key` | header | 否 | string | 格式：`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$` | 类型化凭据mutation必填，其他提交可选 |

请求体：

- `application/json`: [ModelsLLMWrite](#schema-modelsllmwrite); 整体请求体上限 1 MiB.

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `default` | string | 否 | — | 非空切换默认 Provider；空值保留 |
| `default_reasoning_policy` | [ModelsReasoningPolicyWrite](#schema-modelsreasoningpolicywrite) 或 null | 否 | — | 全局默认推理策略 |
| `providers` | map&lt;string, [ModelsProviderWrite](#schema-modelsproviderwrite)&gt; 或 null | 否 | — | Provider map |
| `routing` | [ModelsRoutingWrite](#schema-modelsroutingwrite) 或 null | 否 | — | 模型路由 |
| `cache` | [ModelsCacheWrite](#schema-modelscachewrite) 或 null | 否 | — | 响应缓存 |
| `reasoning_provider` | string / null | 否 | — | 省略/null 保留；空字符串清除选择 |
| `reasoning_model` | string / null | 否 | — | 省略/null 保留；空字符串清除选择 |
| `expected_config_revision` | integer 或 null | 否 | 最小: 0 | 预期配置版本 |
| `expected_config_digest` | string 或 null | 否 | — | 预期配置摘要 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsMutationReceipt](#schema-modelsmutationreceipt) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 409 | `application/json` | [ModelsConfigStaleError](#schema-modelsconfigstaleerror) 或 [MessageError](#schema-messageerror) | 条件版本过期或幂等内容冲突 |
| 422 | `application/json` | [MessageError](#schema-messageerror) | api_key_mutation 领域校验失败；仅未注入引用分支另含 code=credential_ref_not_hydrated |
| 428 | `application/json` | [MessageError](#schema-messageerror) | 类型化凭据变更缺Idempotency-Key |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 存储、运行时或处理失败 |

实现参考: [`handleUpdateLLMConfig`](../../api/handler_config.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X PUT "${HEXCLAW_API}/api/v1/config/llm" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Idempotency-Key: llm-example-1' \
  -H 'Content-Type: application/json' \
  --data '{"default_reasoning_policy":{"mode":"auto"}}'
```

```json
{
  "status": "ok",
  "config_revision": 3,
  "config_digest": "example-config-digest",
  "replayed": false
}
```

<a id="op-post-api-v1-config-llm-test"></a>

### POST `/api/v1/config/llm/test` — 执行Provider连接测试

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

64 KiB JSON；type/model必填，api_key除ollama外必需。测试会调用上游，可能计费；HTTP200仍须读ok。只有稳定ID与已保存执行配置匹配时持久化显式测试回执，其他候选测试不改配置。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：

- `application/json`: [ModelsConnectionRequest](#schema-modelsconnectionrequest); 整体请求体上限 64 KiB.

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider` | [ModelsConnectionProvider](#schema-modelsconnectionprovider) | 是 | — | Provider 名称或请求描述 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsConnectionResult](#schema-modelsconnectionresult) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleTestLLMConfig`](../../api/handler_config.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X POST "${HEXCLAW_API}/api/v1/config/llm/test" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{"provider":{"type":"ollama","model":"REPLACE_WITH_INSTALLED_MODEL","base_url":"http://127.0.0.1:11434"}}'
```

```json
{
  "ok": false,
  "message": "Example connection failure",
  "persisted": false,
  "tested_at": 1791292800000,
  "probe_started_at": 1791292799000
}
```

<a id="op-post-api-v1-config-llm-probe"></a>

### POST `/api/v1/config/llm/probe` — 执行精确模型能力探测

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

64 KiB JSON；只接受已保存稳定Provider ID及精确model，不接收凭据/端点。kind大小写归一、去重后1..4项，五种候选catalog/text/vision/tools/embedding。逐项串行调用并持久回执；200结果可为failed，不等于全通过。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：

- `application/json`: [ModelsModelProbeRequest](#schema-modelsmodelproberequest); 整体请求体上限 64 KiB.

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider_instance_id` | string | 是 | — | 稳定 Provider 身份 |
| `model` | string | 是 | — | 精确模型 ID |
| `kinds` | string[] | 是 | 最少条目: 1；最多条目: 4；条目枚举：`"catalog"`, `"text"`, `"vision"`, `"tools"`, `"embedding"` | 大小写归一并去重后，要求 1..4 个不同 kind |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsModelProbeResponse](#schema-modelsmodelproberesponse) | 成功响应 |
| 400 | `application/json` | [APIError](#schema-apierror) | 模型身份或kind无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 409 | `application/json` | [ModelsProbeStaleError](#schema-modelsprobestaleerror) | 探测期间配置变更 |
| 503 | `application/json` | [APIError](#schema-apierror) | 回执存储不可用或保存失败 |

实现参考: [`handleProbeModelCapability`](../../api/model_capability_probe.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X POST "${HEXCLAW_API}/api/v1/config/llm/probe" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{"provider_instance_id":"REPLACE_WITH_SAVED_PROVIDER_ID","model":"REPLACE_WITH_EXACT_MODEL_ID","kinds":["text"]}'
```

```json
{
  "provider_instance_id": "REPLACE_WITH_SAVED_PROVIDER_ID",
  "model": "REPLACE_WITH_EXACT_MODEL_ID",
  "results": [
    {
      "probe_kind": "text",
      "outcome": "passed",
      "probe_policy_version": "example-policy-version",
      "tested_at": 1791292800000,
      "probe_started_at": 1791292799000,
      "latency_ms": 1000,
      "persisted": true
    }
  ]
}
```

<a id="op-post-api-v1-config-llm-models"></a>

### POST `/api/v1/config/llm/models` — 发现Provider模型目录

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

16 KiB JSON。有稳定ID时使用保存的URL/Key/授权，客户端候选字段被替换；否则base_url必填。访问上游/models，已选Ollama目标走/v1/models；Google兼容目录使用原生模型目录。上游失败、不可解析或空目录通常返回200+models:[]+error，不能当作成功发现。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：

- `application/json`: [ModelsCatalogRequest](#schema-modelscatalogrequest); 整体请求体上限 16 KiB.

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider_instance_id` | string | 否 | — | 提供稳定 ID 时，以保存的描述替换客户端 URL/Key/授权字段 |
| `base_url` | string | 否 | — | 未提供已保存的 provider_instance_id 时必填 |
| `api_key` | string | 否 | 仅写入 | API Key |
| `locality` | string | 否 | 枚举：`""`, `"auto"`, `"local"`, `"cloud"` | 端点归属 |
| `private_network_access` | [ModelsPrivateNetworkAccess](#schema-modelsprivatenetworkaccess) | 否 | — | 私有网络授权 |
| `http_authorization` | [ModelsHTTPAuthorization](#schema-modelshttpauthorization) | 否 | — | HTTP 授权 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsCatalogResult](#schema-modelscatalogresult) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleFetchProviderModels`](../../api/handler_config.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X POST "${HEXCLAW_API}/api/v1/config/llm/models" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{"provider_instance_id":"REPLACE_WITH_SAVED_PROVIDER_ID"}'
```

```json
{
  "models": [],
  "error": "Example upstream catalog error"
}
```

<a id="op-get-api-v1-llm-capabilities"></a>

### GET `/api/v1/llm/capabilities` — 读取工具调用能力缓存

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `s.capabilities != nil`.

仅能力服务存在时挂载；未启用时路由404。成功顶层数组，空为[]；这是历史tool_call可靠性，不是所有模态声明。handler错误为text/plain。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsToolCapability](#schema-modelstoolcapability)[] | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `text/plain` | string | 能力缓存查询失败 |
| 503 | `text/plain` | string | handler缺能力服务 |

实现参考: [`handleListCapabilities`](../../api/handler_capabilities.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl "${HEXCLAW_API}/api/v1/llm/capabilities" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}"
```

```json
[]
```

<a id="op-post-api-v1-llm-capabilities-probe"></a>

### POST `/api/v1/llm/capabilities/probe` — 触发工具调用探测

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `s.capabilities != nil`.

query provider使用配置map key，model为精确文本模型，两者必填；body不使用。调用模型并upsert能力缓存，可能计费。关闭模块路由404；handler错误text/plain。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `provider` | query | 是 | string | — | 配置providers的map key |
| `model` | query | 是 | string | — | 精确已配置文本模型ID |

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsToolCapability](#schema-modelstoolcapability) | 成功响应 |
| 400 | `text/plain` | string | provider/model缺少或不属于可用文本模型 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `text/plain` | string | 探测失败 |
| 503 | `text/plain` | string | handler缺能力服务 |

实现参考: [`handleProbeCapability`](../../api/handler_capabilities.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X POST "${HEXCLAW_API}/api/v1/llm/capabilities/probe?provider=REPLACE_WITH_PROVIDER_MAP_KEY&model=REPLACE_WITH_EXACT_TEXT_MODEL" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}"
```

```json
{
  "provider_name": "REPLACE_WITH_PROVIDER_MAP_KEY",
  "model_name": "REPLACE_WITH_EXACT_TEXT_MODEL",
  "tool_call": 1,
  "tool_call_text": "good",
  "last_probe": "2026-10-07T00:00:00Z"
}
```

<a id="memory-soul"></a>

## 记忆与默认助理

| 方法 | 路径 | 操作 |
| --- | --- | --- |
| GET | `/api/v1/config/memory` | [读取当前记忆配置](#op-get-api-v1-config-memory) |
| PUT | `/api/v1/config/memory` | [修改记忆行为](#op-put-api-v1-config-memory) |
| GET | `/api/v1/assistant/soul` | [读取默认助理SOUL](#op-get-api-v1-assistant-soul) |
| PUT | `/api/v1/assistant/soul` | [修改默认助理SOUL](#op-put-api-v1-assistant-soul) |

<a id="op-get-api-v1-config-memory"></a>

### GET `/api/v1/config/memory` — 读取当前记忆配置

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

优先返回引擎当前配置；enabled/profile_interval_mins只读。未配置active_recall默认有效值true；未知auto_memory投影为inline。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsMemoryConfig](#schema-modelsmemoryconfig) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleGetMemoryConfig`](../../api/handler_config.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl "${HEXCLAW_API}/api/v1/config/memory" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}"
```

```json
{
  "enabled": true,
  "auto_memory": "inline",
  "recall_min_score": 0.3,
  "active_recall": true,
  "profile": false,
  "profile_interval_mins": 60
}
```

<a id="op-put-api-v1-config-memory"></a>

### PUT `/api/v1/config/memory` — 修改记忆行为

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

64 KiB JSON，字段patch；省略/null保留。auto_memory大小写归一后inline/extract/off；recall_min_score 0..1。auto_memory、recall_min_score、active_recall热生效，profile变化列入restart_required，重启后后台周期生效。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：

- `application/json`: [ModelsMemoryWrite](#schema-modelsmemorywrite); 整体请求体上限 64 KiB.

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `auto_memory` | string 或 null | 否 | 枚举：`"inline"`, `"extract"`, `"off"` | 自动记忆模式 |
| `recall_min_score` | number 或 null | 否 | 最小: 0；最大: 1 | 召回最低分数 |
| `active_recall` | boolean 或 null | 否 | — | 是否主动召回 |
| `profile` | boolean 或 null | 否 | — | 是否后台整理画像 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsMemoryWritten](#schema-modelsmemorywritten) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 存储、运行时或处理失败 |

实现参考: [`handleUpdateMemoryConfig`](../../api/handler_config.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X PUT "${HEXCLAW_API}/api/v1/config/memory" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{"auto_memory":"inline","active_recall":true}'
```

```json
{
  "status": "ok",
  "config": {
    "enabled": true,
    "auto_memory": "inline",
    "recall_min_score": 0.3,
    "active_recall": true,
    "profile": false,
    "profile_interval_mins": 60
  },
  "restart_required": []
}
```

<a id="op-get-api-v1-assistant-soul"></a>

### GET `/api/v1/assistant/soul` — 读取默认助理SOUL

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

返回自定义文本、是否自定义及内置默认，不依赖模型调用。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsSoul](#schema-modelssoul) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleGetAssistantSoul`](../../api/handler_assistant.go).

<a id="op-put-api-v1-assistant-soul"></a>

### PUT `/api/v1/assistant/soul` — 修改默认助理SOUL

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

1 MiB JSON；空/省略system_prompt删除自定义文件，恢复内置默认。每轮读取，下一轮生效。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：

- `application/json`: [ModelsSoulWrite](#schema-modelssoulwrite); 整体请求体上限 1 MiB.

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `system_prompt` | string | 否 | — | 空值/省略删除自定义 SOUL 文件并恢复默认 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsSoul](#schema-modelssoul) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 存储、运行时或处理失败 |

实现参考: [`handleUpdateAssistantSoul`](../../api/handler_assistant.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X PUT "${HEXCLAW_API}/api/v1/assistant/soul" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{"system_prompt":"You are a helpful assistant."}'
```

<a id="media"></a>

## 语音、图片、视频与生成文件

| 方法 | 路径 | 操作 |
| --- | --- | --- |
| GET | `/api/v1/voice/status` | [读取STT/TTS状态](#op-get-api-v1-voice-status) |
| POST | `/api/v1/voice/transcribe` | [语音转写](#op-post-api-v1-voice-transcribe) |
| POST | `/api/v1/voice/synthesize` | [文本转语音](#op-post-api-v1-voice-synthesize) |
| GET | `/api/v1/images/status` | [读取图片生成状态](#op-get-api-v1-images-status) |
| POST | `/api/v1/images/generate` | [生成图片](#op-post-api-v1-images-generate) |
| GET | `/api/v1/videos/status` | [读取视频生成状态](#op-get-api-v1-videos-status) |
| POST | `/api/v1/videos/generate` | [提交视频生成](#op-post-api-v1-videos-generate) |
| GET | `/api/v1/videos/tasks/{id}` | [查询视频任务](#op-get-api-v1-videos-tasks-id) |
| GET | `/api/v1/voicechat/status` | [读取语音对话状态](#op-get-api-v1-voicechat-status) |
| POST | `/api/v1/voicechat/chat` | [语音对话](#op-post-api-v1-voicechat-chat) |
| GET | `/api/v1/files/generated/{path}` | [下载生成产物](#op-get-api-v1-files-generated-path) |

<a id="op-get-api-v1-voice-status"></a>

### GET `/api/v1/voice/status` — 读取STT/TTS状态

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `s.voiceSvc != nil`.

仅voiceSvc注入时挂载，否则404；STT/TTS可各自未配置，分别看enabled/provider。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsVoiceStatus](#schema-modelsvoicestatus) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleVoiceStatus`](../../api/handler_misc.go).

<a id="op-post-api-v1-voice-transcribe"></a>

### POST `/api/v1/voice/transcribe` — 语音转写

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `s.voiceSvc != nil`.

音频整体上限10 MiB；multipart audio文件或raw body。query language可省，format默认wav，传给Provider。调用上游并可能计费；STT未配置503，关闭voiceSvc时未挂载404。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `language` | query | 否 | string | — | 语言代码，省略自动检测 |
| `format` | query | 否 | string | 默认：`"wav"` | 音频格式，由Provider支持范围决定 |

请求体：

- `multipart/form-data`: object（字段见下）; 整体请求体上限 10 MiB.

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `audio` | string (binary) | 是 | — | Base64 音频 |

- `application/octet-stream`: string (binary); 整体请求体上限 10 MiB.

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsTranscript](#schema-modelstranscript) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 处理或运行时失败 |
| 503 | `application/json` | [MessageError](#schema-messageerror) | 对应服务未配置 |

实现参考: [`handleVoiceTranscribe`](../../api/handler_misc.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X POST "${HEXCLAW_API}/api/v1/voice/transcribe" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -F 'audio=@./sample.wav'
```

```json
{
  "text": "Example transcript",
  "language": "en",
  "duration": 2.5
}
```

<a id="op-post-api-v1-voice-synthesize"></a>

### POST `/api/v1/voice/synthesize` — 文本转语音

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `s.voiceSvc != nil`.

1 MiB JSON，text非空白。成功直接返回音频二进制，格式由配置的TTS结果决定；voice为可选音色。调用上游可能计费。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：

- `application/json`: [ModelsSpeechRequest](#schema-modelsspeechrequest); 整体请求体上限 1 MiB.

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `text` | string | 是 | 最短: 1 | 文本 |
| `voice` | string | 否 | — | 可选音色标识，由 Provider 支持 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `audio/mpeg` | string (binary) | 成功响应 |
| 200 | `audio/wav` | string (binary) | 成功响应 |
| 200 | `audio/ogg` | string (binary) | 成功响应 |
| 200 | `audio/flac` | string (binary) | 成功响应 |
| 200 | `audio/pcm` | string (binary) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 处理或运行时失败 |
| 503 | `application/json` | [MessageError](#schema-messageerror) | 对应服务未配置 |

实现参考: [`handleVoiceSynthesize`](../../api/handler_misc.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X POST "${HEXCLAW_API}/api/v1/voice/synthesize" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{"text":"Hello from HexClaw."}' \
  --output 'speech.audio'
```

<a id="op-get-api-v1-images-status"></a>

### GET `/api/v1/images/status` — 读取图片生成状态

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

路由始终挂载；服务未配置返回200且enabled=false，providers/models均为空数组。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsMediaStatus](#schema-modelsmediastatus) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleImageGenStatus`](../../api/handler_imagegen.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl "${HEXCLAW_API}/api/v1/images/status" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}"
```

```json
{
  "enabled": false,
  "providers": [],
  "models": []
}
```

<a id="op-post-api-v1-images-generate"></a>

### POST `/api/v1/images/generate` — 生成图片

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

64 KiB JSON，prompt不能是空字符串。Provider选择依次显式名称、model索引、默认；无法路由500。body idempotency_key仅在上游支持时透传；两个header只是日志关联，不代表本服务去重。调用可能计费。genStore存在则落盘并回填file_path，成功后b64清空；落盘失败仍可200且保留URL/B64，必须检查实际图片产物。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `Idempotency-Key` | header | 否 | string | — | 仅用作关联日志的后备ID，不自动注入body幂等键 |
| `X-Request-ID` | header | 否 | string | — | 次级日志关联后备ID |

请求体：

- `application/json`: [ModelsImageRequest](#schema-modelsimagerequest); 整体请求体上限 64 KiB.

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider` | string | 否 | — | 可选；依次显式 Provider、model 路由、配置默认 |
| `model` | string | 否 | — | 精确模型 ID |
| `prompt` | string | 是 | — | 生成提示词 |
| `negative` | string | 否 | — | 负向提示词 |
| `image_url` | string | 否 | — | 输入图片 URL |
| `size` | string | 否 | — | 大小/尺寸，见所属对象 |
| `ratio` | string | 否 | — | 宽高比 |
| `seed` | integer | 否 | — | 0 表示未指定 |
| `idempotency_key` | string | 否 | — | 仅上游 Provider 支持时透传 |
| `callback_url` | string | 否 | — | Provider 专有能力；不注册 HexClaw 回调 |
| `extra` | object（实际附加字段） | 否 | — | Provider 专有参数 |
| `n` | integer | 否 | — | 由 Provider 决定，通常默认 1 |
| `style` | string | 否 | — | Provider 专有，例如 vivid/natural |
| `quality` | string | 否 | — | Provider 专有，例如 standard/hd |
| `user` | string | 否 | — | 用户关联值 |
| `response_format` | string | 否 | — | Provider 专有，通常为 url/b64_json |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsImageResult](#schema-modelsimageresult) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 处理或运行时失败 |
| 503 | `application/json` | [MessageError](#schema-messageerror) | 对应服务未配置 |

实现参考: [`handleImageGenGenerate`](../../api/handler_imagegen.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X POST "${HEXCLAW_API}/api/v1/images/generate" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{"model":"REPLACE_WITH_CONFIGURED_IMAGE_MODEL","prompt":"A small crab on a white background","n":1}'
```

```json
{
  "provider": "example-provider",
  "model": "REPLACE_WITH_CONFIGURED_IMAGE_MODEL",
  "created": 1791292800,
  "images": [
    {
      "file_path": "images/example.png"
    }
  ],
  "usage_ms": 1500
}
```

<a id="op-get-api-v1-videos-status"></a>

### GET `/api/v1/videos/status` — 读取视频生成状态

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

路由始终挂载；服务未配置返回200且enabled=false，providers/models均为空数组。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsMediaStatus](#schema-modelsmediastatus) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleVideoGenStatus`](../../api/handler_videogen.go).

<a id="op-post-api-v1-videos-generate"></a>

### POST `/api/v1/videos/generate` — 提交视频生成

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

64 KiB JSON，prompt或image_url至少一个非空白值；领域校验错误由handler包装500。body幂等键由上游能力决定，header仅日志关联。200返回provider::raw-task-id表示提交，不是视频完成；保存该task_id供查询，结果未知不盲重发。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `Idempotency-Key` | header | 否 | string | — | 仅用作关联日志的后备ID，不自动注入body幂等键 |
| `X-Request-ID` | header | 否 | string | — | 次级日志关联后备ID |

请求体：

- `application/json`: [ModelsVideoRequest](#schema-modelsvideorequest); 整体请求体上限 64 KiB.

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider` | string | 否 | — | 可选；依次显式 Provider、model 路由、配置默认 |
| `model` | string | 否 | — | 精确模型 ID |
| `prompt` | string | 否 | — | 生成提示词 |
| `negative` | string | 否 | — | 负向提示词 |
| `image_url` | string | 否 | — | 输入图片 URL |
| `size` | string | 否 | — | 大小/尺寸，见所属对象 |
| `ratio` | string | 否 | — | 宽高比 |
| `seed` | integer | 否 | — | 0 表示未指定 |
| `idempotency_key` | string | 否 | — | 仅上游 Provider 支持时透传 |
| `callback_url` | string | 否 | — | Provider 专有能力；不注册 HexClaw 回调 |
| `extra` | object（实际附加字段） | 否 | — | Provider 专有参数 |
| `end_image_url` | string | 否 | — | 尾帧图片 URL |
| `quality` | string | 否 | — | Provider 专有，例如 speed/quality |
| `with_audio` | boolean | 否 | — | 是否生成音频 |
| `duration` | integer | 否 | — | 秒；由 Provider 支持范围决定 |
| `fps` | integer | 否 | — | 帧率 |
| `user_id` | string | 否 | — | 上游用户关联值 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsVideoTask](#schema-modelsvideotask) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 处理或运行时失败 |
| 503 | `application/json` | [MessageError](#schema-messageerror) | 对应服务未配置 |

实现参考: [`handleVideoGenSubmit`](../../api/handler_videogen.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X POST "${HEXCLAW_API}/api/v1/videos/generate" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{"model":"REPLACE_WITH_CONFIGURED_VIDEO_MODEL","prompt":"A small crab walking along a beach","duration":5}'
```

```json
{
  "task_id": "example-provider::example-task"
}
```

<a id="op-get-api-v1-videos-tasks-id"></a>

### GET `/api/v1/videos/tasks/{id}` — 查询视频任务

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

id使用完整provider::task标识，URL编码。查询调用上游；done时可能下载视频/封面到genStore。HTTP200/status=success不保证本地文件持久化成功；读取error与video_file_path，下载失败可保留临时URL。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `id` | path | 是 | string | — | 实际资源标识；使用服务响应值 |

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsVideoStatus](#schema-modelsvideostatus) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 处理或运行时失败 |
| 503 | `application/json` | [MessageError](#schema-messageerror) | 对应服务未配置 |

实现参考: [`handleVideoGenPoll`](../../api/handler_videogen.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl "${HEXCLAW_API}/api/v1/videos/tasks/example-provider%3A%3Aexample-task" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}"
```

```json
{
  "task_id": "example-provider::example-task",
  "provider": "example-provider",
  "model": "example-model",
  "status": "success",
  "state": "succeeded",
  "done": true,
  "video_file_path": "videos/example.mp4"
}
```

<a id="op-get-api-v1-voicechat-status"></a>

### GET `/api/v1/voicechat/status` — 读取语音对话状态

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

路由始终挂载；服务未配置返回200且enabled=false，providers/models均为空数组。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsMediaStatus](#schema-modelsmediastatus) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleVoiceChatStatus`](../../api/handler_voicechat.go).

<a id="op-post-api-v1-voicechat-chat"></a>

### POST `/api/v1/voicechat/chat` — 语音对话

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

16 MiB JSON。audio为Base64输入或text兜底，Provider决定格式/模型/音色；调用可能计费。genStore落盘成功返回audio_file_path并清空audio；落盘失败仍200且可能保留audio，不能伪称文件已生成。 audio与text至少一个非空；两者为空的领域校验失败当前返回500。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：

- `application/json`: [ModelsVoiceChatRequest](#schema-modelsvoicechatrequest); 整体请求体上限 16 MiB.

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider` | string | 否 | — | Provider 名称或请求描述 |
| `model` | string | 否 | — | 精确模型 ID |
| `audio` | string | 否 | — | 用户音频的 Base64；也可提供 text |
| `text` | string | 否 | — | 文本 |
| `voice` | string | 否 | — | 可选音色标识，由 Provider 支持 |
| `format` | string | 否 | 默认：`"wav"` | Provider 音频格式，默认 wav |
| `system` | string | 否 | — | 系统提示词 |
| `history` | [ModelsVoiceTurn](#schema-modelsvoiceturn)[] | 否 | — | 历史对话 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsVoiceChatResult](#schema-modelsvoicechatresult) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 处理或运行时失败 |
| 503 | `application/json` | [MessageError](#schema-messageerror) | 对应服务未配置 |

实现参考: [`handleVoiceChat`](../../api/handler_voicechat.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X POST "${HEXCLAW_API}/api/v1/voicechat/chat" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{"model":"REPLACE_WITH_CONFIGURED_VOICECHAT_MODEL","text":"Hello."}'
```

```json
{
  "provider": "example-provider",
  "model": "example-model",
  "transcript": "Hello.",
  "format": "wav",
  "audio_file_path": "voicechat/example.wav"
}
```

<a id="op-get-api-v1-files-generated-path"></a>

### GET `/api/v1/files/generated/{path}` — 下载生成产物

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

实际Go路由为{path...}，path可含目录斜线，使用生成响应中的相对file_path。query download非空触发attachment；1/true使用原名，其他值为下载名。Cache-Control public,max-age=86400；支持标准范围/条件请求。handler错误text/plain，鉴权错误仍JSON。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `path` | path | 是 | string | — | 响应file_path给出的相对路径；可含目录分隔符 |
| `download` | query | 否 | string | — | 可选下载名或1/true |
| `Range` | header | 否 | string | — | 标准HTTP字节范围请求 |
| `If-Range` | header | 否 | string | — | 范围请求的ETag或Last-Modified条件 |
| `If-Modified-Since` | header | 否 | string | — | 标准HTTP修改时间条件 |
| `If-Unmodified-Since` | header | 否 | string | — | 标准HTTP未修改时间条件 |
| `If-Match` | header | 否 | string | — | 标准HTTP ETag匹配条件 |
| `If-None-Match` | header | 否 | string | — | 标准HTTP ETag不匹配条件 |

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/octet-stream` | string (binary) | 成功响应 |
| 200 | `image/png` | string (binary) | 成功响应 |
| 200 | `image/jpeg` | string (binary) | 成功响应 |
| 200 | `image/webp` | string (binary) | 成功响应 |
| 200 | `image/gif` | string (binary) | 成功响应 |
| 200 | `video/mp4` | string (binary) | 成功响应 |
| 200 | `video/webm` | string (binary) | 成功响应 |
| 206 | `application/octet-stream` | string (binary) | Partial binary content |
| 304 | — | 无响应体 | Not modified |
| 400 | `text/plain` | string | 缺少path |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 404 | `text/plain` | string | 文件未找到或不可打开 |
| 412 | — | 无响应体 | Conditional request failed |
| 416 | `text/plain` | string | Range不可满足 |
| 500 | `text/plain` | string | 读取文件状态失败 |
| 503 | `text/plain` | string | genStore未注入 |

实现参考: [`handleGeneratedFile`](../../api/server.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl "${HEXCLAW_API}/api/v1/files/generated/images/example.png" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  --output 'example.png'
```

<a id="logs-config"></a>

## 日志与配置投影

| 方法 | 路径 | 操作 |
| --- | --- | --- |
| GET | `/api/v1/logs` | [查询运行日志](#op-get-api-v1-logs) |
| GET | `/api/v1/logs/stats` | [读取日志统计](#op-get-api-v1-logs-stats) |
| GET | `/api/v1/logs/stream` | [订阅日志WebSocket](#op-get-api-v1-logs-stream) |
| GET | `/api/v1/config` | [读取脱敏整体配置](#op-get-api-v1-config) |
| PUT | `/api/v1/config` | [修改整体配置或Ollama目标](#op-put-api-v1-config) |
| GET | `/api/v1/models` | [读取当前配置模型索引](#op-get-api-v1-models) |

<a id="op-get-api-v1-logs"></a>

### GET `/api/v1/logs` — 查询运行日志

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

默认查内存日志；history=true或start/end出现时读落盘历史。start不得晚于end；keyword受输入长度规则约束。过滤值按实际日志字段，不是枚举校验。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `level` | query | 否 | string | — | 匹配实际level；不校验枚举 |
| `source` | query | 否 | string | — | 匹配实际source；不校验枚举 |
| `domain` | query | 否 | string | — | 匹配实际domain；不校验枚举 |
| `keyword` | query | 否 | string | 最长: 512 | 关键词过滤，最多512个Unicode字符 |
| `limit` | query | 否 | integer | 默认：`100`；最大: 8000 | 正整数；无效回默认，大于8000截断 |
| `offset` | query | 否 | integer | 默认：`0`；最小: 0 | 非负整数；无效值回到0 |
| `history` | query | 否 | string | — | 仅true开启落盘历史 |
| `start` | query | 否 | string | — | RFC3339Nano或空值；该参数存在即查询历史 |
| `end` | query | 否 | string | — | RFC3339Nano或空值；该参数存在即查询历史 |

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsLogs](#schema-modelslogs) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 处理或运行时失败 |
| 503 | `application/json` | [MessageError](#schema-messageerror) | 历史日志sink未注入 |

实现参考: [`handleGetLogs`](../../api/handler_logs.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl "${HEXCLAW_API}/api/v1/logs?limit=20&offset=0" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}"
```

```json
{
  "logs": [],
  "total": 0
}
```

<a id="op-get-api-v1-logs-stats"></a>

### GET `/api/v1/logs/stats` — 读取日志统计

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

只读当前收集器统计。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsLogStats](#schema-modelslogstats) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleGetLogStats`](../../api/handler_logs.go).

<a id="op-get-api-v1-logs-stream"></a>

### GET `/api/v1/logs/stream` — 订阅日志WebSocket

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

GET须标准WebSocket升级握手并携带Bearer。成功101后文本帧为LogEntry JSON，不是SSE或JSON数组；客户端断开退订。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `Upgrade` | header | 是 | string | 固定值：`"websocket"` | 固定websocket |
| `Connection` | header | 是 | string | — | 须包含Upgrade token |
| `Sec-WebSocket-Key` | header | 是 | string | — | 标准随机16字节的Base64握手key |
| `Sec-WebSocket-Version` | header | 是 | string | 固定值：`"13"` | 固定13 |

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 101 | — | 无响应体 | 成功响应 |
| 400 | `text/plain` | string | WebSocket握手无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 403 | `text/plain` | string | WebSocket握手被拒绝 |
| 426 | `text/plain` | string | WebSocket版本需升级 |
| 503 | `application/json` | [MessageError](#schema-messageerror) | 日志订阅者上限已满 |

每个 WebSocket 文本帧为 [ModelsLogEntry](#schema-modelslogentry).

实现参考: [`handleLogStream`](../../api/handler_logs.go).

<a id="op-get-api-v1-config"></a>

### GET `/api/v1/config` — 读取脱敏整体配置

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

只读投影；无API Key明文。Provider switchable是enabled/key/local条件推导，不是实时连通性；Ollama包括目标身份、revision/digest及管理能力。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsFullConfig](#schema-modelsfullconfig) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleGetFullConfig`](../../api/handler_extended.go).

<a id="op-put-api-v1-config"></a>

### PUT `/api/v1/config` — 修改整体配置或Ollama目标

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

security/sandbox为字段patch，落盘成功后发布运行时；allowed_paths提供数组为整体替换，[]清空。network_enabled=true当前拒绝400。ollama必须单独提交，不可混security/sandbox：四个expected revision/digest、非空Idempotency-Key及associated_provider_instance_ids数组必需；更新关联Provider端点并返回提交证明。普通patch返回message。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `Idempotency-Key` | header | 否 | string | 格式：`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$` | 提交ollama目标必填 |

请求体：

- `application/json`: [ModelsFullWrite](#schema-modelsfullwrite).

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `security` | [ModelsSecurity](#schema-modelssecurity) 或 null | 否 | — | 安全模块配置投影 |
| `sandbox` | [ModelsSandbox](#schema-modelssandbox) 或 null | 否 | — | 代码执行策略 |
| `ollama` | [ModelsOllamaTargetWrite](#schema-modelsollamatargetwrite) 或 null | 否 | — | Ollama 目标 |
| `expected_config_revision` | integer 或 null | 否 | 最小: 0 | 预期配置版本 |
| `expected_config_digest` | string 或 null | 否 | — | 预期配置摘要 |
| `expected_target_revision` | integer 或 null | 否 | 最小: 0 | 预期目标版本 |
| `expected_target_digest` | string 或 null | 否 | — | 预期目标摘要 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsMessage](#schema-modelsmessage) 或 [ModelsMutationReceipt](#schema-modelsmutationreceipt) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 409 | `application/json` | [MessageError](#schema-messageerror) 或 [ModelsConfigStaleError](#schema-modelsconfigstaleerror) | Ollama/LLM条件冲突或幂等冲突 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 处理或运行时失败 |

实现参考: [`handleUpdateFullConfig`](../../api/handler_extended.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X PUT "${HEXCLAW_API}/api/v1/config" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{"security":{"rate_limit_rpm":120}}'
```

```json
{
  "message": "Configuration updated"
}
```

<a id="op-get-api-v1-models"></a>

### GET `/api/v1/models` — 读取当前配置模型索引

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

仅列每个Provider当前非空model，不是远端目录或完整model_specs；空集合models可能为null，total=0。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsConfiguredModels](#schema-modelsconfiguredmodels) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleListModels`](../../api/handler_extended.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl "${HEXCLAW_API}/api/v1/models" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}"
```

```json
{
  "models": null,
  "total": 0
}
```

<a id="ollama"></a>

## Ollama 目标与模型管理

| 方法 | 路径 | 操作 |
| --- | --- | --- |
| GET | `/api/v1/ollama/status` | [探测当前Ollama目标](#op-get-api-v1-ollama-status) |
| POST | `/api/v1/ollama/pull` | [下载Ollama模型并订阅进度](#op-post-api-v1-ollama-pull) |
| GET | `/api/v1/ollama/pulls/{operation_id}` | [读取Ollama下载快照](#op-get-api-v1-ollama-pulls-operation-id) |
| GET | `/api/v1/ollama/pulls/{operation_id}/events` | [重连Ollama下载事件](#op-get-api-v1-ollama-pulls-operation-id-events) |
| GET | `/api/v1/ollama/running` | [读取Ollama内存模型](#op-get-api-v1-ollama-running) |
| POST | `/api/v1/ollama/load` | [预热Ollama模型](#op-post-api-v1-ollama-load) |
| POST | `/api/v1/ollama/unload` | [卸载Ollama内存模型](#op-post-api-v1-ollama-unload) |
| DELETE | `/api/v1/ollama/models/{name}` | [删除Ollama模型](#op-delete-api-v1-ollama-models-name) |
| POST | `/api/v1/ollama/restart` | [管理Ollama进程重启](#op-post-api-v1-ollama-restart) |

<a id="op-get-api-v1-ollama-status"></a>

### GET `/api/v1/ollama/status` — 探测当前Ollama目标

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

请求当前冻结目标tags及version；连通失败仍200，reachable/running=false且error说明。models未必出现；关联状态是配置事实。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsOllamaStatus](#schema-modelsollamastatus) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleOllamaStatus`](../../api/handler_ollama.go).

<a id="op-post-api-v1-ollama-pull"></a>

### POST `/api/v1/ollama/pull` — 下载Ollama模型并订阅进度

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

1 MiB JSON。expected_target_id/revision需同时给；可省略。响应始终SSE data快照，state非running即终态，无[DONE]。同ID同请求只订阅既有操作，不再发物理下载；同ID异内容409。后台操作由服务生命周期持有，客户端断开不取消；仅内存，durable=false，重启后未知操作404。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `Idempotency-Key` | header | 否 | string | 格式：`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$` | 可选；省略由服务生成，响应快照给operation_id |

请求体：

- `application/json`: [ModelsPullRequest](#schema-modelspullrequest); 整体请求体上限 1 MiB.

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `model` | string | 是 | 最短: 1 | 精确模型 ID |
| `expected_target_id` | string | 否 | — | 预期目标标识 |
| `expected_target_revision` | integer | 否 | 最小: 0 | 预期目标版本 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `text/event-stream` | string | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 409 | `application/json` | [MessageError](#schema-messageerror) | 目标条件变化或相同ID不同请求 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 处理或运行时失败 |

每个 SSE `data:` 值为 [ModelsPullSnapshot](#schema-modelspullsnapshot).

实现参考: [`handleOllamaPull`](../../api/ollama_pull.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -N -X POST "${HEXCLAW_API}/api/v1/ollama/pull" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Idempotency-Key: ollama-pull-example-1' \
  -H 'Content-Type: application/json' \
  --data '{"model":"REPLACE_WITH_OLLAMA_MODEL"}'
```

```text
data: {"operation_id":"ollama-pull-example-1","target_id":"example-target-id","target_revision":1,"model":"REPLACE_WITH_OLLAMA_MODEL","state":"running","status":"pulling manifest","durable":false}

```

<a id="op-get-api-v1-ollama-pulls-operation-id"></a>

### GET `/api/v1/ollama/pulls/{operation_id}` — 读取Ollama下载快照

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

仅当前进程保留的operation_id；没有持久下载账本，404不证明此前没有发送物理下载。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `operation_id` | path | 是 | string | — | 实际资源标识；使用服务响应值 |

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsPullSnapshot](#schema-modelspullsnapshot) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 404 | `application/json` | [MessageError](#schema-messageerror) | 资源不存在 |

实现参考: [`handleGetOllamaPull`](../../api/ollama_pull.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl "${HEXCLAW_API}/api/v1/ollama/pulls/ollama-pull-example-1" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}"
```

```json
{
  "operation_id": "ollama-pull-example-1",
  "target_id": "example-target-id",
  "target_revision": 1,
  "model": "REPLACE_WITH_OLLAMA_MODEL",
  "state": "succeeded",
  "status": "success",
  "durable": false
}
```

<a id="op-get-api-v1-ollama-pulls-operation-id-events"></a>

### GET `/api/v1/ollama/pulls/{operation_id}/events` — 重连Ollama下载事件

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

只订阅已存在操作，SSE data为PullSnapshot；终态state非running后结束。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `operation_id` | path | 是 | string | — | 实际资源标识；使用服务响应值 |

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `text/event-stream` | string | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 404 | `application/json` | [MessageError](#schema-messageerror) | 资源不存在 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 处理或运行时失败 |

每个 SSE `data:` 值为 [ModelsPullSnapshot](#schema-modelspullsnapshot).

实现参考: [`handleOllamaPullEvents`](../../api/ollama_pull.go).

<a id="op-get-api-v1-ollama-running"></a>

### GET `/api/v1/ollama/running` — 读取Ollama内存模型

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

读当前目标/api/ps；transport、非200、无效JSON或缺models返回502。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsRunningModels](#schema-modelsrunningmodels) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 502 | `application/json` | [MessageError](#schema-messageerror) | 上游连接或协议失败 |

实现参考: [`handleOllamaRunning`](../../api/handler_ollama.go).

<a id="op-post-api-v1-ollama-load"></a>

### POST `/api/v1/ollama/load` — 预热Ollama模型

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

model必填，num_ctx正值可选，默认8192；关联Provider对同model的num_ctx/keep_alive优先，keep_alive默认30m。上游必须2xx且done=true无error才返回loaded。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：

- `application/json`: [ModelsLoad](#schema-modelsload).

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `model` | string | 是 | 最短: 1 | 精确模型 ID |
| `num_ctx` | integer | 否 | — | 正值；同模型关联 Provider 覆盖值优先，默认 8192 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsStatus](#schema-modelsstatus) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 502 | `application/json` | [MessageError](#schema-messageerror) | 上游连接或协议失败 |

实现参考: [`handleOllamaLoad`](../../api/handler_ollama.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X POST "${HEXCLAW_API}/api/v1/ollama/load" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{"model":"REPLACE_WITH_OLLAMA_MODEL","num_ctx":8192}'
```

```json
{
  "status": "loaded"
}
```

<a id="op-post-api-v1-ollama-unload"></a>

### POST `/api/v1/ollama/unload` — 卸载Ollama内存模型

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

发送keep_alive=0到当前目标。上游非2xx状态码原样返回JSON error，不能只列固定502。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：

- `application/json`: [ModelsUnload](#schema-modelsunload).

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `model` | string | 是 | 最短: 1 | 精确模型 ID |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsStatus](#schema-modelsstatus) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 502 | `application/json` | [MessageError](#schema-messageerror) | 上游连接或协议失败 |
| default | `application/json` | [MessageError](#schema-messageerror) | 上游非2xx状态转发 |

实现参考: [`handleOllamaUnload`](../../api/handler_ollama.go).

<a id="op-delete-api-v1-ollama-models-name"></a>

### DELETE `/api/v1/ollama/models/{name}` — 删除Ollama模型

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

删除前关闭该目标模型的Embedding自动安装设置并持久化；上游失败时会查询目录确认模型是否已缺失，缺失可返回deleted。否则上游非2xx原样返回。name可能含命名空间斜线，作为单一路径参数URL编码。

| 参数 | 位置 | 必填 | 类型 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- | --- |
| `name` | path | 是 | string | — | 实际资源标识；使用服务响应值 |

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsStatus](#schema-modelsstatus) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 处理或运行时失败 |
| 502 | `application/json` | [MessageError](#schema-messageerror) | 上游连接或协议失败 |
| default | `application/json` | [MessageError](#schema-messageerror) | 上游非2xx状态转发 |

实现参考: [`handleOllamaDelete`](../../api/handler_ollama.go).

<a id="op-post-api-v1-ollama-restart"></a>

### POST `/api/v1/ollama/restart` — 管理Ollama进程重启

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `始终挂载`.

无body。仅can_restart目标可用；产生进程副作用。200的running/restarting/starting是当前观察，starting不代表已就绪，后续查status。 can_restart仅在本后端托管、mode=default且精确loopback:11434目标时为true。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsStatus](#schema-modelsstatus) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 409 | `application/json` | [MessageError](#schema-messageerror) | 目标不是本后端托管进程 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 处理或运行时失败 |

实现参考: [`handleOllamaRestart`](../../api/handler_ollama.go).

<a id="desktop"></a>

## 桌面宿主能力

| 方法 | 路径 | 操作 |
| --- | --- | --- |
| GET | `/api/v1/desktop/info` | [读取桌面宿主信息](#op-get-api-v1-desktop-info) |
| GET | `/api/v1/desktop/notifications` | [读取最近桌面通知](#op-get-api-v1-desktop-notifications) |
| POST | `/api/v1/desktop/notifications` | [发送桌面通知](#op-post-api-v1-desktop-notifications) |
| DELETE | `/api/v1/desktop/notifications` | [清空桌面通知队列](#op-delete-api-v1-desktop-notifications) |
| GET | `/api/v1/desktop/clipboard` | [读取宿主剪贴板](#op-get-api-v1-desktop-clipboard) |
| POST | `/api/v1/desktop/clipboard` | [写入宿主剪贴板](#op-post-api-v1-desktop-clipboard) |

这些端点操作运行 HexClaw 的宿主，供本地桌面集成使用，不会取得远端客户端的剪贴板或通知系统。

<a id="op-get-api-v1-desktop-info"></a>

### GET `/api/v1/desktop/info` — 读取桌面宿主信息

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `s.desktopSvc != nil（由 api/server.go 挂载）`.

仅注入desktopSvc时挂载；此HTTP服务所在宿主的OS/arch/hostname/version，不是远端客户端硬件。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsDesktopInfo](#schema-modelsdesktopinfo) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleInfo`](../../desktop/desktop.go).

<a id="op-get-api-v1-desktop-notifications"></a>

### GET `/api/v1/desktop/notifications` — 读取最近桌面通知

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `s.desktopSvc != nil（由 api/server.go 挂载）`.

无分页参数；返回当前内存队列最近最多50条，total为本次返回条数；空队列返回 notifications=[]。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsNotifications](#schema-modelsnotifications) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleGetNotifications`](../../desktop/desktop.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl "${HEXCLAW_API}/api/v1/desktop/notifications" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}"
```

```json
{
  "notifications": [],
  "total": 0
}
```

<a id="op-post-api-v1-desktop-notifications"></a>

### POST `/api/v1/desktop/notifications` — 发送桌面通知

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `s.desktopSvc != nil（由 api/server.go 挂载）`.

title非空，body可空，type省略默认info；已知类型info/warning/error/success但handler不拒绝其他字符串。加入内存通知并经宿主通知能力发送；200提示不是远端系统可见性的独立证明。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：

- `application/json`: [ModelsNotifyRequest](#schema-modelsnotifyrequest).

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `title` | string | 是 | 最短: 1 | 通知标题 |
| `body` | string | 否 | — | 通知正文 |
| `type` | string | 否 | 默认：`"info"` | 已知值 info/warning/error/success；不拒绝其他字符串 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsMessage](#schema-modelsmessage) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handlePostNotification`](../../desktop/desktop.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X POST "${HEXCLAW_API}/api/v1/desktop/notifications" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{"title":"HexClaw example","body":"Local host notification","type":"info"}'
```

```json
{
  "message": "通知已发送"
}
```

<a id="op-delete-api-v1-desktop-notifications"></a>

### DELETE `/api/v1/desktop/notifications` — 清空桌面通知队列

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `s.desktopSvc != nil（由 api/server.go 挂载）`.

仅清空服务当前内存通知队列；无body字段。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsMessage](#schema-modelsmessage) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |

实现参考: [`handleClearNotifications`](../../desktop/desktop.go).

<a id="op-get-api-v1-desktop-clipboard"></a>

### GET `/api/v1/desktop/clipboard` — 读取宿主剪贴板

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `s.desktopSvc != nil（由 api/server.go 挂载）`.

操作后端所在宿主剪贴板，依赖平台命令/能力；不会读取云端连接客户端的剪贴板。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsClipboardRead](#schema-modelsclipboardread) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 处理或运行时失败 |

实现参考: [`handleGetClipboard`](../../desktop/desktop.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl "${HEXCLAW_API}/api/v1/desktop/clipboard" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}"
```

```json
{
  "content": "Example clipboard text"
}
```

<a id="op-post-api-v1-desktop-clipboard"></a>

### POST `/api/v1/desktop/clipboard` — 写入宿主剪贴板

认证: `Authorization: Bearer ${HEXCLAW_API_TOKEN}`.

挂载条件: `s.desktopSvc != nil（由 api/server.go 挂载）`.

content可空或省略（写空），操作后端所在宿主剪贴板，依赖平台能力。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：

- `application/json`: [ModelsClipboard](#schema-modelsclipboard).

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `content` | string | 否 | — | POST 省略/空 content 会写空剪贴板 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsMessage](#schema-modelsmessage) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 处理或运行时失败 |

实现参考: [`handleSetClipboard`](../../desktop/desktop.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X POST "${HEXCLAW_API}/api/v1/desktop/clipboard" \
  -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{"content":"Example clipboard text"}'
```

```json
{
  "message": "已复制到剪贴板"
}
```

<a id="native"></a>

## 原生内部凭据协调

| 方法 | 路径 | 操作 |
| --- | --- | --- |
| POST | `/api/internal/desktop/credentials/hydrate` | [原生凭据注入](#op-post-api-internal-desktop-credentials-hydrate) |
| POST | `/api/internal/desktop/credentials/dehydrate` | [原生凭据撤出](#op-post-api-internal-desktop-credentials-dehydrate) |
| POST | `/api/internal/desktop/provider-credentials/reserve` | [预留原生Provider身份](#op-post-api-internal-desktop-provider-credentials-reserve) |

以下三个 `/api/internal/desktop/*` 端点是原生内部实现能力，不推荐第三方集成。授权同时要求回环来源和专用 sidecar capability token；普通业务令牌会被拒绝。

<a id="op-post-api-internal-desktop-credentials-hydrate"></a>

### POST `/api/internal/desktop/credentials/hydrate` — 原生凭据注入

认证: 回环 + 专用 `Authorization: Bearer ${HEXCLAW_NATIVE_CAPABILITY}`；拒绝普通业务令牌。

挂载条件: `始终挂载`.

仅本机原生层：回环请求与专用sidecar capability token，两者缺一返回401；普通业务令牌不可替代。1..64条、引用不能重复、secret非空且最多64 KiB字节，总体4 MiB，未知字段拒绝。仅更新内存解析器并激活运行时，失败恢复此前状态，不建立第二持久化凭据路径。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：

- `application/json`: [ModelsHydrateRequest](#schema-modelshydraterequest); 整体请求体上限 4 MiB.

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `entries` | [ModelsSecretEntry](#schema-modelssecretentry)[] | 是 | 最少条目: 1；最多条目: 64；按字段唯一：credential_ref | 凭据条目 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsHydrateResponse](#schema-modelshydrateresponse) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 存储、运行时或处理失败 |

实现参考: [`handleHydrateDesktopCredentials`](../../api/provider_credentials.go).

<a id="op-post-api-internal-desktop-credentials-dehydrate"></a>

### POST `/api/internal/desktop/credentials/dehydrate` — 原生凭据撤出

认证: 回环 + 专用 `Authorization: Bearer ${HEXCLAW_NATIVE_CAPABILITY}`；拒绝普通业务令牌。

挂载条件: `始终挂载`.

仅原生回环能力令牌。1..64个合法、唯一引用，64 KiB JSON，未知字段拒绝。移除内存解析器引用并重新应用运行时，失败恢复；不会因引用未命中清空YAML内API Key。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：

- `application/json`: [ModelsDehydrateRequest](#schema-modelsdehydraterequest); 整体请求体上限 64 KiB.

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `credential_refs` | string[] | 是 | 最少条目: 1；最多条目: 64；条目唯一 | 凭据引用列表 |

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 200 | `application/json` | [ModelsDehydrateResponse](#schema-modelsdehydrateresponse) | 成功响应 |
| 400 | `application/json` | [MessageError](#schema-messageerror) | 请求/参数无效 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 存储、运行时或处理失败 |

实现参考: [`handleDehydrateDesktopCredentials`](../../api/provider_credentials.go).

<a id="op-post-api-internal-desktop-provider-credentials-reserve"></a>

### POST `/api/internal/desktop/provider-credentials/reserve` — 预留原生Provider身份

认证: 回环 + 专用 `Authorization: Bearer ${HEXCLAW_NATIVE_CAPABILITY}`；拒绝普通业务令牌。

挂载条件: `始终挂载`.

仅原生回环能力令牌；无body字段。201返回服务端生成的稳定provider_instance_id及其唯一credential_ref；不写配置，后续PUT再校验唯一性。

路径/query/额外请求头：无。认证和 Content-Type 按上文。

请求体：无定义字段，不需要发送 JSON 请求体。

响应：

| HTTP 状态 | Content-Type | 响应契约 | 含义 |
| --- | --- | --- | --- |
| 201 | `application/json` | [ModelsReservedIdentity](#schema-modelsreservedidentity) | 成功响应 |
| 401 | `application/json` | [MessageError](#schema-messageerror) | 访问令牌无效 |
| 500 | `application/json` | [MessageError](#schema-messageerror) | 存储、运行时或处理失败 |

实现参考: [`handleReserveProviderCredentialIdentity`](../../api/provider_credentials.go).

示例（响应为契约示意，不是实测记录）：

```bash
curl -X POST "${HEXCLAW_API}/api/internal/desktop/provider-credentials/reserve" \
  -H "Authorization: Bearer ${HEXCLAW_NATIVE_CAPABILITY}"
```

```json
{
  "provider_instance_id": "pvd_v1_0123456789abcdef0123456789abcdef",
  "credential_ref": "llm_provider/pvd_v1_0123456789abcdef0123456789abcdef/api_key"
}
```

<a id="data-schemas"></a>

## 数据 Schema

以下同名 Schema 与 OpenAPI 一致。操作中的引用直接跳转到完整字段契约。对象子字段要求在父对象提供时适用；未注明默认值的字段不额外承诺默认行为。

<a id="schema-modelshealth"></a>

### ModelsHealth

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `status` | string | 是 | 枚举：`"healthy"`, `"unhealthy"` | 状态 |
| `error` | string | 否 | — | 错误信息 |
| `process_instance_id` | string | 是 | — | 进程代际标识 |
| `restart_supported` | boolean | 是 | — | 服务是否支持托管重启 |

<a id="schema-modelsrestartrequest"></a>

### ModelsRestartRequest

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `expected_process_instance_id` | string | 是 | 最短: 1 | 先从 /health 读取当前进程代际 |
| `request_id` | string | 是 | 最短: 1 | 当前进程代际的稳定标识 |

<a id="schema-modelsrestartaccepted"></a>

### ModelsRestartAccepted

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `status` | string | 是 | 固定值：`"restarting"` | 状态 |
| `process_instance_id` | string | 是 | — | 进程代际标识 |
| `request_id` | string | 是 | — | 请求关联标识 |

<a id="schema-modelsversion"></a>

### ModelsVersion

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `version` | string | 是 | — | 版本标识 |
| `engine` | string | 是 | 固定值：`"Hexagon"` | 响应报告框架标识；实际任务运行方式由服务决定 |
| `engine_version` | string | 是 | — | 框架版本 |

<a id="schema-modelsstats"></a>

### ModelsStats

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `uptime_seconds` | number | 是 | — | 进程运行秒数 |
| `goroutines` | integer | 是 | — | goroutine 数量 |
| `memory_alloc_mb` | number | 是 | — | 已分配内存 MB |
| `memory_sys_mb` | number | 是 | — | 系统申请内存 MB |
| `gc_cycles` | integer | 是 | — | GC 次数 |
| `log_entries` | integer | 是 | — | 日志条数 |

<a id="schema-modelsstatus"></a>

### ModelsStatus

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `status` | string | 是 | — | 状态 |

<a id="schema-modelsmessage"></a>

### ModelsMessage

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `message` | string | 是 | — | 说明信息 |

<a id="schema-modelssecretentry"></a>

### ModelsSecretEntry

拒绝未知字段。

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `credential_ref` | string | 是 | — | 服务端签发的 llm_provider/{provider_instance_id}/api_key |
| `secret` | string | 是 | 仅写入 | 非空凭据，最多 64 KiB UTF-8 字节 |

<a id="schema-modelshydraterequest"></a>

### ModelsHydrateRequest

拒绝未知字段。

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `entries` | [ModelsSecretEntry](#schema-modelssecretentry)[] | 是 | 最少条目: 1；最多条目: 64；按字段唯一：credential_ref | 凭据条目 |

<a id="schema-modelsdehydraterequest"></a>

### ModelsDehydrateRequest

拒绝未知字段。

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `credential_refs` | string[] | 是 | 最少条目: 1；最多条目: 64；条目唯一 | 凭据引用列表 |

<a id="schema-modelshydrateresponse"></a>

### ModelsHydrateResponse

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `hydrated_count` | integer | 是 | — | 注入条数 |
| `credential_refs` | string[] | 是 | — | 凭据引用列表 |

<a id="schema-modelsdehydrateresponse"></a>

### ModelsDehydrateResponse

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `dehydrated_count` | integer | 是 | — | 撤出条数 |
| `credential_refs` | string[] | 是 | — | 凭据引用列表 |

<a id="schema-modelsreservedidentity"></a>

### ModelsReservedIdentity

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider_instance_id` | string | 是 | — | pvd_v1_ 加 32 位小写十六进制字符 |
| `credential_ref` | string | 是 | — | 对应稳定 Provider 身份的唯一 Key 引用 |

<a id="schema-modelskeyreveal"></a>

### ModelsKeyReveal

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `api_key` | string | 是 | 只读 | 当前 Key 明文，响应 Cache-Control: no-store |

<a id="schema-modelskeymutation"></a>

### ModelsKeyMutation

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `mode` | string | 是 | 枚举：`"preserve"`, `"replace"`, `"delete"` | 模式 |
| `credential_ref` | string | 否 | — | replace 时必填；必须已通过原生层注入 |

<a id="schema-modelsprivatenetworkaccess"></a>

### ModelsPrivateNetworkAccess

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `host` | string | 是 | — | 授权绑定到规范化的端点 host |
| `allowed` | boolean | 是 | — | 是否允许 |

<a id="schema-modelshttpauthorization"></a>

### ModelsHTTPAuthorization

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `base_url` | string | 是 | — | 授权绑定到该精确 HTTP base URL |
| `allowed` | boolean | 是 | — | 是否允许 |

<a id="schema-modelsreasoningpolicy"></a>

### ModelsReasoningPolicy

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `mode` | string | 是 | 枚举：`""`, `"auto"`, `"on"`, `"off"`, `"effort"` | 空值/auto 使用原生默认；全局策略不接受 inherit |
| `effort` | string | 否 | 枚举：`"low"`, `"medium"`, `"high"`, `"xhigh"`, `"max"` | 仅 mode=effort 时使用 |

<a id="schema-modelsreasoningcontrol"></a>

### ModelsReasoningControl

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `dialect` | string | 是 | 枚举：`"reasoning_effort"`, `"enable_thinking"`, `"think"`, `"thinking"` | 上游推理协议 |
| `on` | JSON 值 | 是 | — | 启用时传给上游的精确 JSON 值，可为标量、对象或数组 |
| `off` | JSON 值 | 是 | — | 禁用时传给上游的精确 JSON 值，可为标量、对象或数组 |
| `allowed_efforts` | string[] | 否 | 条目枚举：`"low"`, `"medium"`, `"high"`, `"xhigh"`, `"max"` | 允许推理等级 |

<a id="schema-modelsembeddingspec"></a>

### ModelsEmbeddingSpec

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `protocol` | string | 是 | 枚举：`"openai_embeddings"`, `"ollama_embeddings"` | Embedding 协议 |
| `dimension` | integer | 是 | 最小: 1；最大: 65536 | 向量维数 |
| `normalization` | string | 否 | — | 向量归一方式 |

<a id="schema-modelsmodelspec"></a>

### ModelsModelSpec

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `id` | string | 是 | 最短: 1 | 标识 |
| `display_name` | string | 否 | — | 展示名称 |
| `is_custom` | boolean | 否 | — | 是否自定义 |
| `capabilities` | string[] | 是 | 条目枚举：`"text"`, `"vision"`, `"video"`, `"audio"`, `"code"`, `"image_generation"`, `"video_generation"`, `"embedding"` | 模态能力声明 |
| `reasoning_support` | string | 否 | 枚举：`"supported"`, `"unsupported"`, `"unknown"` | 推理支持状态 |
| `reasoning_control` | [ModelsReasoningControl](#schema-modelsreasoningcontrol) | 否 | — | 推理协议声明 |
| `embedding` | [ModelsEmbeddingSpec](#schema-modelsembeddingspec) | 否 | — | Embedding 声明 |

<a id="schema-modelsprobereceipt"></a>

### ModelsProbeReceipt

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider_instance_id` | string | 是 | — | 稳定 Provider 身份 |
| `outcome` | string | 是 | 枚举：`"passed"`, `"failed"` | 探测结论 |
| `tested_at` | integer | 是 | — | Unix 毫秒 |
| `probe_started_at` | integer | 是 | — | Unix 毫秒 |
| `latency_ms` | integer | 是 | — | 耗时毫秒 |
| `locality` | string | 是 | — | 端点归属 |
| `message` | string | 是 | — | 说明信息 |

<a id="schema-modelsmodelprobereceipt"></a>

### ModelsModelProbeReceipt

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `probe_kind` | string | 是 | 枚举：`"catalog"`, `"text"`, `"vision"`, `"tools"`, `"embedding"` | 探测类型 |
| `outcome` | string | 是 | 枚举：`"passed"`, `"failed"` | 探测结论 |
| `failure_code` | string | 否 | — | 失败分类 |
| `probe_policy_version` | string | 是 | — | 探测策略版本 |
| `tested_at` | integer | 是 | — | Unix 毫秒 |
| `probe_started_at` | integer | 是 | — | Unix 毫秒 |
| `latency_ms` | integer | 是 | — | 耗时毫秒 |

<a id="schema-modelseffectivemodel"></a>

### ModelsEffectiveModel

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `id` | string | 是 | — | 标识 |
| `display_name` | string | 否 | — | 展示名称 |
| `capabilities` | string[] | 是 | — | 模态能力声明 |
| `capability_states` | map&lt;string, string&gt; | 是 | — | 各能力的证据状态 |
| `route_eligible` | boolean | 是 | — | 是否符合当前路由条件 |
| `availability` | string | 是 | 枚举：`"unknown"`, `"ready"`, `"rate_limited"`, `"pool_exhausted"`, `"provider_down"` | 可用状态 |
| `probe_receipts` | [ModelsModelProbeReceipt](#schema-modelsmodelprobereceipt)[] | 是 | — | 探测证据 |

<a id="schema-modelsproviderwrite"></a>

### ModelsProviderWrite

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider_instance_id` | string | 否 | — | 稳定服务端身份；重命名 Provider 时保留 |
| `display_name` | string | 否 | — | 展示名称 |
| `base_url` | string | 否 | — | 服务端点 |
| `model` | string | 否 | — | 精确的当前模型 ID |
| `models` | string[] | 否 | — | 模型列表 |
| `model_specs` | [ModelsModelSpecWrite](#schema-modelsmodelspecwrite)[] 或 null | 否 | — | 模型声明 |
| `compatible` | string | 否 | — | Provider 协议选择 |
| `locality` | string | 否 | 枚举：`""`, `"auto"`, `"local"`, `"cloud"` | 端点归属 |
| `locality_source` | string | 否 | 枚举：`""`, `"system"`, `"user"` | 归属来源 |
| `confirmed_endpoint_host` | string | 否 | — | 已确认端点 host |
| `private_network_access` | [ModelsPrivateNetworkAccess](#schema-modelsprivatenetworkaccess) | 否 | — | 私有网络授权 |
| `http_authorization` | [ModelsHTTPAuthorization](#schema-modelshttpauthorization) | 否 | — | HTTP 授权 |
| `tools_enabled` | boolean 或 null | 否 | — | 是否启用工具 |
| `max_tools` | integer | 否 | — | 0 表示不限制工具数量 |
| `enabled` | boolean 或 null | 否 | — | 是否启用 |
| `keep_alive` | string | 否 | — | Go duration、整数秒、0 或 -1 |
| `num_ctx` | integer | 否 | — | Ollama 上下文窗口覆盖值 |
| `api_key` | string | 否 | 仅写入 | 兼容直接 Key；**** 脱敏值保留旧 Key，与 api_key_mutation 互斥 |
| `api_key_mutation` | [ModelsKeyMutation](#schema-modelskeymutation) | 否 | — | 显式凭据变更 |

<a id="schema-modelsproviderread"></a>

### ModelsProviderRead

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider_instance_id` | string | 是 | — | 稳定服务端身份；重命名 Provider 时保留 |
| `display_name` | string | 否 | — | 展示名称 |
| `base_url` | string | 是 | — | 服务端点 |
| `model` | string | 是 | — | 精确的当前模型 ID |
| `models` | string[] / null | 是 | — | 模型列表 |
| `model_specs` | [ModelsModelSpec](#schema-modelsmodelspec)[] | 是 | — | 只读模型分类声明投影 |
| `compatible` | string | 是 | — | Provider 协议选择 |
| `locality` | string | 否 | 枚举：`""`, `"auto"`, `"local"`, `"cloud"` | 端点归属 |
| `locality_source` | string | 否 | 枚举：`""`, `"system"`, `"user"` | 归属来源 |
| `confirmed_endpoint_host` | string | 否 | — | 已确认端点 host |
| `private_network_access` | [ModelsPrivateNetworkAccess](#schema-modelsprivatenetworkaccess) | 否 | — | 私有网络授权 |
| `http_authorization` | [ModelsHTTPAuthorization](#schema-modelshttpauthorization) | 否 | — | HTTP 授权 |
| `tools_enabled` | boolean | 否 | — | 是否启用工具 |
| `max_tools` | integer | 否 | — | 0 表示不限制工具数量 |
| `enabled` | boolean | 否 | — | 当前生效的 Provider 启用标志 |
| `keep_alive` | string | 否 | — | Go duration、整数秒、0 或 -1 |
| `num_ctx` | integer | 否 | — | Ollama 上下文窗口覆盖值 |
| `api_key` | string | 是 | — | 脱敏 Key |
| `credential_ref` | string | 否 | — | 凭据引用 |
| `credential_present` | boolean | 是 | — | 是否存在可解析凭据 |
| `api_key_length` | integer | 否 | — | Key 长度投影 |
| `model_specs_mode` | string | 是 | 枚举：`"legacy"`, `"explicit"` | 模型声明模式 |
| `probe_receipt` | [ModelsProbeReceipt](#schema-modelsprobereceipt) | 否 | — | 显式连接测试证据 |
| `effective_models` | [ModelsEffectiveModel](#schema-modelseffectivemodel)[] | 否 | — | 有效模型投影 |

<a id="schema-modelsrouting"></a>

### ModelsRouting

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `enabled` | boolean | 是 | — | 是否启用 |
| `strategy` | string | 是 | — | cost-aware / quality-first / latency-first |

<a id="schema-modelscache"></a>

### ModelsCache

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `enabled` | boolean | 是 | — | 是否启用 |
| `similarity` | number | 是 | — | 缓存相似度阈值 |
| `ttl` | string | 是 | — | Go duration 字符串 |
| `max_entries` | integer | 是 | — | 缓存最大条数 |

<a id="schema-modelsllmread"></a>

### ModelsLLMRead

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `default` | string | 是 | — | 默认 Provider |
| `default_reasoning_policy` | [ModelsReasoningPolicy](#schema-modelsreasoningpolicy) | 是 | — | 全局默认推理策略 |
| `providers` | map&lt;string, [ModelsProviderRead](#schema-modelsproviderread)&gt; | 是 | — | Provider map |
| `routing` | [ModelsRouting](#schema-modelsrouting) | 是 | — | 模型路由 |
| `cache` | [ModelsCache](#schema-modelscache) | 是 | — | 响应缓存 |
| `reasoning_provider` | string | 否 | — | 推理 Provider |
| `reasoning_model` | string | 否 | — | 推理模型 |
| `config_revision` | integer | 是 | 最小: 0 | 非负 uint64 |
| `config_digest` | string | 是 | — | 配置摘要 |

<a id="schema-modelsllmwrite"></a>

### ModelsLLMWrite

providers 省略/null 保留；提供 map 则整体替换。两项配置条件须同时提供。

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `default` | string | 否 | — | 非空切换默认 Provider；空值保留 |
| `default_reasoning_policy` | [ModelsReasoningPolicyWrite](#schema-modelsreasoningpolicywrite) 或 null | 否 | — | 全局默认推理策略 |
| `providers` | map&lt;string, [ModelsProviderWrite](#schema-modelsproviderwrite)&gt; 或 null | 否 | — | Provider map |
| `routing` | [ModelsRoutingWrite](#schema-modelsroutingwrite) 或 null | 否 | — | 模型路由 |
| `cache` | [ModelsCacheWrite](#schema-modelscachewrite) 或 null | 否 | — | 响应缓存 |
| `reasoning_provider` | string / null | 否 | — | 省略/null 保留；空字符串清除选择 |
| `reasoning_model` | string / null | 否 | — | 省略/null 保留；空字符串清除选择 |
| `expected_config_revision` | integer 或 null | 否 | 最小: 0 | 预期配置版本 |
| `expected_config_digest` | string 或 null | 否 | — | 预期配置摘要 |

<a id="schema-modelsmutationreceipt"></a>

### ModelsMutationReceipt

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `status` | string | 是 | 固定值：`"ok"` | 状态 |
| `operation_kind` | string | 否 | 枚举：`"llm"`, `"ollama_target"` | 配置提交类别 |
| `request_id` | string | 否 | — | 请求关联标识 |
| `config_revision` | integer | 是 | 最小: 0 | 配置版本 |
| `config_digest` | string | 是 | — | 配置摘要 |
| `target_revision` | integer | 否 | 最小: 0 | 目标版本 |
| `target_digest` | string | 否 | — | 目标摘要 |
| `committed_at` | integer | 否 | — | Unix 毫秒 |
| `replayed` | boolean | 是 | — | 是否重放提交证明 |

<a id="schema-modelsconfigstaleerror"></a>

### ModelsConfigStaleError

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `code` | string | 是 | 固定值：`"LLM_CONFIG_STALE"` | 错误分类 |
| `message` | string | 是 | — | 说明信息 |
| `error` | string | 是 | — | 错误信息 |
| `config_revision` | integer | 是 | 最小: 0 | 配置版本 |
| `config_digest` | string | 是 | — | 配置摘要 |

<a id="schema-modelsprobestaleerror"></a>

### ModelsProbeStaleError

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `code` | string | 是 | 固定值：`"PROBE_CONFIG_STALE"` | 错误分类 |
| `message` | string | 是 | — | 说明信息 |
| `error` | string | 是 | — | 错误信息 |

<a id="schema-modelsconnectionprovider"></a>

### ModelsConnectionProvider

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider_instance_id` | string | 否 | — | 稳定 Provider 身份 |
| `type` | string | 是 | — | Provider 类型/配置 map 标识 |
| `base_url` | string | 否 | — | 服务端点 |
| `api_key` | string | 否 | 仅写入 | type 不是 ollama 时必填；匹配已保存描述时可解析当前凭据 |
| `model` | string | 是 | 最短: 1 | 精确模型 ID |
| `locality` | string | 否 | 枚举：`""`, `"auto"`, `"local"`, `"cloud"` | 端点归属 |
| `private_network_access` | [ModelsPrivateNetworkAccess](#schema-modelsprivatenetworkaccess) | 否 | — | 私有网络授权 |
| `http_authorization` | [ModelsHTTPAuthorization](#schema-modelshttpauthorization) | 否 | — | HTTP 授权 |

<a id="schema-modelsconnectionrequest"></a>

### ModelsConnectionRequest

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider` | [ModelsConnectionProvider](#schema-modelsconnectionprovider) | 是 | — | Provider 名称或请求描述 |

<a id="schema-modelsconnectionresult"></a>

### ModelsConnectionResult

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `ok` | boolean | 是 | — | 测试是否通过 |
| `message` | string | 是 | — | 说明信息 |
| `provider` | string | 否 | — | Provider 名称或请求描述 |
| `model` | string | 否 | — | 精确模型 ID |
| `latency_ms` | integer | 否 | — | 耗时毫秒 |
| `persisted` | boolean | 是 | — | 是否持久化证据 |
| `tested_at` | integer | 是 | — | Unix 毫秒 |
| `probe_started_at` | integer | 是 | — | Unix 毫秒 |

<a id="schema-modelscatalogrequest"></a>

### ModelsCatalogRequest

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider_instance_id` | string | 否 | — | 提供稳定 ID 时，以保存的描述替换客户端 URL/Key/授权字段 |
| `base_url` | string | 否 | — | 未提供已保存的 provider_instance_id 时必填 |
| `api_key` | string | 否 | 仅写入 | API Key |
| `locality` | string | 否 | 枚举：`""`, `"auto"`, `"local"`, `"cloud"` | 端点归属 |
| `private_network_access` | [ModelsPrivateNetworkAccess](#schema-modelsprivatenetworkaccess) | 否 | — | 私有网络授权 |
| `http_authorization` | [ModelsHTTPAuthorization](#schema-modelshttpauthorization) | 否 | — | HTTP 授权 |

<a id="schema-modelscatalogmodel"></a>

### ModelsCatalogModel

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `id` | string | 是 | — | 标识 |
| `name` | string | 否 | — | 名称 |
| `context_length` | integer | 否 | — | 上下文长度 |
| `prompt_price` | string | 否 | — | 输入价格声明 |
| `completion_price` | string | 否 | — | 输出价格声明 |
| `input_modalities` | string[] | 否 | — | 输入模态 |
| `supports_tools` | boolean | 否 | — | 是否声明支持工具 |
| `capabilities` | string[] | 否 | — | 模态能力声明 |
| `reasoning_support` | string | 否 | — | 推理支持状态 |
| `reasoning_control` | [ModelsReasoningControl](#schema-modelsreasoningcontrol) | 否 | — | 推理协议声明 |

<a id="schema-modelscatalogresult"></a>

### ModelsCatalogResult

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `models` | [ModelsCatalogModel](#schema-modelscatalogmodel)[] | 是 | — | 模型列表 |
| `error` | string | 否 | — | HTTP 200 且 models=[] 时仍可能有错误 |

<a id="schema-modelsmodelproberequest"></a>

### ModelsModelProbeRequest

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider_instance_id` | string | 是 | — | 稳定 Provider 身份 |
| `model` | string | 是 | — | 精确模型 ID |
| `kinds` | string[] | 是 | 最少条目: 1；最多条目: 4；条目枚举：`"catalog"`, `"text"`, `"vision"`, `"tools"`, `"embedding"` | 大小写归一并去重后，要求 1..4 个不同 kind |

<a id="schema-modelsmodelproberesult"></a>

### ModelsModelProbeResult

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `probe_kind` | string | 是 | 枚举：`"catalog"`, `"text"`, `"vision"`, `"tools"`, `"embedding"` | 探测类型 |
| `outcome` | string | 是 | 枚举：`"passed"`, `"failed"` | 探测结论 |
| `failure_code` | string | 否 | — | 失败分类 |
| `probe_policy_version` | string | 是 | — | 探测策略版本 |
| `tested_at` | integer | 是 | — | Unix 毫秒 |
| `probe_started_at` | integer | 是 | — | Unix 毫秒 |
| `latency_ms` | integer | 是 | — | 耗时毫秒 |
| `persisted` | boolean | 是 | — | 是否持久化证据 |

<a id="schema-modelsmodelproberesponse"></a>

### ModelsModelProbeResponse

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider_instance_id` | string | 是 | — | 稳定 Provider 身份 |
| `model` | string | 是 | — | 精确模型 ID |
| `results` | [ModelsModelProbeResult](#schema-modelsmodelproberesult)[] | 是 | — | 逐项探测结果 |

<a id="schema-modelsmemoryconfig"></a>

### ModelsMemoryConfig

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `enabled` | boolean | 是 | — | 只读；修改配置中的总开关后重启 |
| `auto_memory` | string | 是 | 枚举：`"inline"`, `"extract"`, `"off"` | 自动记忆模式 |
| `recall_min_score` | number | 是 | 最小: 0；最大: 1 | 召回最低分数 |
| `active_recall` | boolean | 是 | — | 是否主动召回 |
| `profile` | boolean | 是 | — | 是否后台整理画像 |
| `profile_interval_mins` | integer | 是 | — | 只读 |

<a id="schema-modelsmemorywrite"></a>

### ModelsMemoryWrite

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `auto_memory` | string 或 null | 否 | 枚举：`"inline"`, `"extract"`, `"off"` | 自动记忆模式 |
| `recall_min_score` | number 或 null | 否 | 最小: 0；最大: 1 | 召回最低分数 |
| `active_recall` | boolean 或 null | 否 | — | 是否主动召回 |
| `profile` | boolean 或 null | 否 | — | 是否后台整理画像 |

<a id="schema-modelsmemorywritten"></a>

### ModelsMemoryWritten

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `status` | string | 是 | 固定值：`"ok"` | 状态 |
| `config` | [ModelsMemoryConfig](#schema-modelsmemoryconfig) | 是 | — | 配置投影 |
| `restart_required` | string[] | 是 | 条目枚举：`"profile"` | 需重启的字段 |

<a id="schema-modelstoolcapability"></a>

### ModelsToolCapability

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider_name` | string | 是 | — | Provider map key |
| `model_name` | string | 是 | — | 模型 ID |
| `tool_call` | integer | 是 | 枚举：`0`, `1`, `2`, `3` | 工具调用可靠性编号 |
| `tool_call_text` | string | 是 | 枚举：`"unknown"`, `"good"`, `"partial"`, `"bad"` | 工具调用可靠性文本 |
| `last_probe` | string (date-time) | 是 | — | 最后探测时间 |
| `probe_error` | string | 否 | — | 探测错误 |

<a id="schema-modelssoul"></a>

### ModelsSoul

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `system_prompt` | string | 是 | — | 默认助理系统提示词 |
| `is_custom` | boolean | 是 | — | 是否自定义 |
| `default_prompt` | string | 是 | — | 内置默认提示词 |

<a id="schema-modelssoulwrite"></a>

### ModelsSoulWrite

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `system_prompt` | string | 否 | — | 空值/省略删除自定义 SOUL 文件并恢复默认 |

<a id="schema-modelsollamatarget"></a>

### ModelsOllamaTarget

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `mode` | string | 是 | 枚举：`"default"`, `"custom"`；默认：`"default"` | 模式 |
| `custom_base_url` | string | 是 | — | custom 模式必填；有效 http/https 服务根 URL |
| `target_revision` | integer | 是 | 最小: 0 | 目标版本 |
| `associated_provider_instance_ids` | string[] | 是 | — | 关联稳定 Provider 身份 |
| `resolved_base_url` | string | 是 | — | 解析后的服务根 URL |
| `target_id` | string | 是 | — | 目标标识 |
| `target_digest` | string | 是 | — | 目标摘要 |
| `can_restart` | boolean | 是 | — | 当前目标能否由本服务管理重启 |

<a id="schema-modelsollamatargetwrite"></a>

### ModelsOllamaTargetWrite

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `mode` | string | 否 | 枚举：`"default"`, `"custom"` | 模式 |
| `custom_base_url` | string | 否 | — | 自定义目标服务根 URL |
| `target_revision` | integer | 否 | 最小: 0 | 目标版本 |
| `associated_provider_instance_ids` | string[] | 是 | 条目唯一 | 关联稳定 Provider 身份 |

<a id="schema-modelssecurity"></a>

### ModelsSecurity

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `gateway_enabled` | boolean 或 null | 否 | — | 是否启用网关认证 |
| `injection_detection` | boolean 或 null | 否 | — | 是否启用注入检测 |
| `pii_filter` | boolean 或 null | 否 | — | 是否启用 PII 处理 |
| `content_filter` | boolean 或 null | 否 | — | 是否启用内容过滤 |
| `rate_limit_rpm` | integer 或 null | 否 | — | 每分钟请求数配置 |

<a id="schema-modelssandbox"></a>

### ModelsSandbox

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `network_enabled` | boolean 或 null | 否 | — | 代码执行宿主网络开关 |
| `allowed_paths` | string[] 或 null | 否 | — | 代码执行可读路径 |

<a id="schema-modelsfullconfig"></a>

### ModelsFullConfig

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `backend_id` | string | 是 | — | 后端标识 |
| `ollama` | [ModelsOllamaTarget](#schema-modelsollamatarget) | 是 | — | Ollama 目标 |
| `server` | object（字段见下） | 是 | — | 服务监听配置 |
| `server.host` | string | 是 | — | 端点 host |
| `server.port` | integer | 是 | — | port |
| `server.mode` | string | 是 | — | 模式 |
| `llm` | object（字段见下） | 是 | — | LLM 配置投影 |
| `llm.default` | string | 是 | — | 默认 Provider |
| `llm.providers` | map&lt;string, object（字段见下）&gt; | 是 | — | Provider map |
| `llm.providers.{key}.model` | string | 父对象存在时 | — | 精确模型 ID |
| `llm.providers.{key}.base_url` | string | 父对象存在时 | — | 服务端点 |
| `llm.providers.{key}.has_key` | boolean | 父对象存在时 | — | has_key |
| `llm.providers.{key}.enabled` | boolean | 父对象存在时 | — | 是否启用 |
| `llm.providers.{key}.local` | boolean | 父对象存在时 | — | local |
| `llm.providers.{key}.switchable` | boolean | 父对象存在时 | — | switchable |
| `llm.providers.{key}.switch_disabled_reason` | string | 父对象存在时 | — | switch_disabled_reason |
| `knowledge` | object（字段见下） | 是 | — | 知识库开关 |
| `knowledge.enabled` | boolean | 是 | — | 是否启用 |
| `mcp` | object（字段见下） | 是 | — | MCP 开关 |
| `mcp.enabled` | boolean | 是 | — | 是否启用 |
| `cron` | object（字段见下） | 是 | — | 定时任务开关 |
| `cron.enabled` | boolean | 是 | — | 是否启用 |
| `webhook` | object（字段见下） | 是 | — | Webhook 开关 |
| `webhook.enabled` | boolean | 是 | — | 是否启用 |
| `canvas` | object（字段见下） | 是 | — | Canvas 开关 |
| `canvas.enabled` | boolean | 是 | — | 是否启用 |
| `voice` | object（字段见下） | 是 | — | 语音开关 |
| `voice.enabled` | boolean | 是 | — | 是否启用 |
| `security` | [ModelsSecurityRead](#schema-modelssecurityread) | 是 | — | 安全模块配置投影 |
| `sandbox` | [ModelsSandboxRead](#schema-modelssandboxread) | 是 | — | 代码执行策略 |

<a id="schema-modelsfullwrite"></a>

### ModelsFullWrite

security/sandbox 为字段 patch；Ollama 单独提交并提供四项条件及 Idempotency-Key。

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `security` | [ModelsSecurity](#schema-modelssecurity) 或 null | 否 | — | 安全模块配置投影 |
| `sandbox` | [ModelsSandbox](#schema-modelssandbox) 或 null | 否 | — | 代码执行策略 |
| `ollama` | [ModelsOllamaTargetWrite](#schema-modelsollamatargetwrite) 或 null | 否 | — | Ollama 目标 |
| `expected_config_revision` | integer 或 null | 否 | 最小: 0 | 预期配置版本 |
| `expected_config_digest` | string 或 null | 否 | — | 预期配置摘要 |
| `expected_target_revision` | integer 或 null | 否 | 最小: 0 | 预期目标版本 |
| `expected_target_digest` | string 或 null | 否 | — | 预期目标摘要 |

<a id="schema-modelsconfiguredmodels"></a>

### ModelsConfiguredModels

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `models` | object（字段见下）[] / null | 是 | — | 模型列表 |
| `models[].id` | string | 父对象存在时 | — | 标识 |
| `models[].name` | string | 父对象存在时 | — | 名称 |
| `models[].provider` | string | 父对象存在时 | — | Provider 名称或请求描述 |
| `total` | integer | 是 | — | 总数/返回条数，见操作语义 |

<a id="schema-modelsollamamodel"></a>

### ModelsOllamaModel

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `name` | string | 是 | — | 名称 |
| `size` | integer | 是 | — | 大小/尺寸，见所属对象 |
| `modified` | string | 是 | — | 修改时间 |
| `family` | string | 否 | — | 模型家族 |
| `parameter_size` | string | 否 | — | 参数量说明 |
| `quantization_level` | string | 否 | — | 量化等级 |
| `capabilities` | string[] | 否 | — | 模态能力声明 |

<a id="schema-modelsollamastatus"></a>

### ModelsOllamaStatus

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `target_id` | string | 是 | — | 目标标识 |
| `target_revision` | integer | 是 | — | 目标版本 |
| `resolved_base_url` | string | 是 | — | 解析后的服务根 URL |
| `can_restart` | boolean | 是 | — | 当前目标能否由本服务管理重启 |
| `reachable` | boolean | 是 | — | 目标是否可达 |
| `error` | string | 否 | — | 错误信息 |
| `running` | boolean | 是 | — | 目标是否运行 |
| `version` | string | 否 | — | 版本标识 |
| `models` | [ModelsOllamaModel](#schema-modelsollamamodel)[] | 否 | — | 模型列表 |
| `associated` | boolean | 是 | — | 是否有配置关联 |
| `model_count` | integer | 是 | — | 磁盘模型数量 |

<a id="schema-modelsrunningmodels"></a>

### ModelsRunningModels

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `models` | object（字段见下）[] | 是 | — | 模型列表 |
| `models[].name` | string | 父对象存在时 | — | 名称 |
| `models[].size` | integer | 父对象存在时 | — | 大小/尺寸，见所属对象 |
| `models[].size_vram` | integer | 父对象存在时 | — | 显存字节数 |
| `models[].expires_at` | string | 父对象存在时 | — | 内存模型过期时间 |
| `models[].parameter_size` | string | 否 | — | 参数量说明 |
| `models[].quantization_level` | string | 否 | — | 量化等级 |
| `models[].context_length` | integer | 父对象存在时 | — | 上下文长度 |
| `target_id` | string | 是 | — | 目标标识 |
| `target_revision` | integer | 是 | — | 目标版本 |

<a id="schema-modelspullrequest"></a>

### ModelsPullRequest

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `model` | string | 是 | 最短: 1 | 精确模型 ID |
| `expected_target_id` | string | 否 | — | 预期目标标识 |
| `expected_target_revision` | integer | 否 | 最小: 0 | 预期目标版本 |

<a id="schema-modelspullsnapshot"></a>

### ModelsPullSnapshot

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `operation_id` | string | 是 | — | 下载操作标识 |
| `target_id` | string | 是 | — | 目标标识 |
| `target_revision` | integer | 是 | — | 目标版本 |
| `model` | string | 是 | — | 精确模型 ID |
| `state` | string | 是 | 枚举：`"running"`, `"succeeded"`, `"failed"`, `"outcome_unknown"` | 归一化状态 |
| `status` | string | 是 | — | 状态 |
| `digest` | string | 否 | — | 下载层摘要 |
| `completed` | integer | 否 | — | 已下载字节数 |
| `total` | integer | 否 | — | 总数/返回条数，见操作语义 |
| `error` | string | 否 | — | 错误信息 |
| `durable` | boolean | 是 | 固定值：`false` | 操作账本是否持久化 |

<a id="schema-modelsload"></a>

### ModelsLoad

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `model` | string | 是 | 最短: 1 | 精确模型 ID |
| `num_ctx` | integer | 否 | — | 正值；同模型关联 Provider 覆盖值优先，默认 8192 |

<a id="schema-modelsunload"></a>

### ModelsUnload

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `model` | string | 是 | 最短: 1 | 精确模型 ID |

<a id="schema-modelsmediastatus"></a>

### ModelsMediaStatus

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `enabled` | boolean | 是 | — | 是否启用 |
| `providers` | string[] | 是 | — | Provider map |
| `models` | string[] | 是 | — | 模型列表 |

<a id="schema-modelsvoicestatus"></a>

### ModelsVoiceStatus

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `stt_enabled` | boolean | 是 | — | 是否启用 STT |
| `tts_enabled` | boolean | 是 | — | 是否启用 TTS |
| `stt_provider` | string | 是 | — | STT Provider |
| `tts_provider` | string | 是 | — | TTS Provider |

<a id="schema-modelstranscript"></a>

### ModelsTranscript

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `text` | string | 是 | — | 文本 |
| `language` | string | 否 | — | 语言 |
| `duration` | number | 否 | — | 秒 |
| `confidence` | number | 否 | — | Provider 置信度 |
| `request_id` | string | 否 | — | 请求关联标识 |

<a id="schema-modelsspeechrequest"></a>

### ModelsSpeechRequest

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `text` | string | 是 | 最短: 1 | 文本 |
| `voice` | string | 否 | — | 可选音色标识，由 Provider 支持 |

<a id="schema-modelsimagerequest"></a>

### ModelsImageRequest

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider` | string | 否 | — | 可选；依次显式 Provider、model 路由、配置默认 |
| `model` | string | 否 | — | 精确模型 ID |
| `prompt` | string | 是 | — | 生成提示词 |
| `negative` | string | 否 | — | 负向提示词 |
| `image_url` | string | 否 | — | 输入图片 URL |
| `size` | string | 否 | — | 大小/尺寸，见所属对象 |
| `ratio` | string | 否 | — | 宽高比 |
| `seed` | integer | 否 | — | 0 表示未指定 |
| `idempotency_key` | string | 否 | — | 仅上游 Provider 支持时透传 |
| `callback_url` | string | 否 | — | Provider 专有能力；不注册 HexClaw 回调 |
| `extra` | object（实际附加字段） | 否 | — | Provider 专有参数 |
| `n` | integer | 否 | — | 由 Provider 决定，通常默认 1 |
| `style` | string | 否 | — | Provider 专有，例如 vivid/natural |
| `quality` | string | 否 | — | Provider 专有，例如 standard/hd |
| `user` | string | 否 | — | 用户关联值 |
| `response_format` | string | 否 | — | Provider 专有，通常为 url/b64_json |

<a id="schema-modelsimage"></a>

### ModelsImage

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `url` | string | 否 | — | 图片 URL |
| `b64_json` | string | 否 | — | Base64 图片 |
| `file_path` | string | 否 | — | 成功持久化后的生成文件相对路径 |
| `revised_prompt` | string | 否 | — | 上游修订提示词 |

<a id="schema-modelsimageresult"></a>

### ModelsImageResult

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider` | string | 是 | — | Provider 名称或请求描述 |
| `model` | string | 是 | — | 精确模型 ID |
| `created` | integer | 是 | — | Unix 秒 |
| `images` | [ModelsImage](#schema-modelsimage)[] / null | 是 | — | 生成图片列表 |
| `request_id` | string | 否 | — | 请求关联标识 |
| `billed` | boolean | 否 | — | 上游计费标记 |
| `usage_ms` | integer | 否 | — | 生成耗时毫秒 |

<a id="schema-modelsvideorequest"></a>

### ModelsVideoRequest

prompt 或 image_url 至少一个非空白；此 handler 当前将领域校验错误包装为 HTTP 500。

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider` | string | 否 | — | 可选；依次显式 Provider、model 路由、配置默认 |
| `model` | string | 否 | — | 精确模型 ID |
| `prompt` | string | 否 | — | 生成提示词 |
| `negative` | string | 否 | — | 负向提示词 |
| `image_url` | string | 否 | — | 输入图片 URL |
| `size` | string | 否 | — | 大小/尺寸，见所属对象 |
| `ratio` | string | 否 | — | 宽高比 |
| `seed` | integer | 否 | — | 0 表示未指定 |
| `idempotency_key` | string | 否 | — | 仅上游 Provider 支持时透传 |
| `callback_url` | string | 否 | — | Provider 专有能力；不注册 HexClaw 回调 |
| `extra` | object（实际附加字段） | 否 | — | Provider 专有参数 |
| `end_image_url` | string | 否 | — | 尾帧图片 URL |
| `quality` | string | 否 | — | Provider 专有，例如 speed/quality |
| `with_audio` | boolean | 否 | — | 是否生成音频 |
| `duration` | integer | 否 | — | 秒；由 Provider 支持范围决定 |
| `fps` | integer | 否 | — | 帧率 |
| `user_id` | string | 否 | — | 上游用户关联值 |

<a id="schema-modelsvideotask"></a>

### ModelsVideoTask

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `task_id` | string | 是 | — | 视频任务标识 |

<a id="schema-modelsvideostatus"></a>

### ModelsVideoStatus

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `task_id` | string | 是 | — | 视频任务标识 |
| `provider` | string | 是 | — | Provider 名称或请求描述 |
| `model` | string | 是 | — | 精确模型 ID |
| `status` | string | 是 | — | Provider 原始任务状态，如 queueing/running/success/failed |
| `state` | string | 否 | — | 归一化 queued/running/succeeded/failed/cancelled |
| `done` | boolean | 是 | — | 是否终态 |
| `video_url` | string | 否 | — | 视频 URL |
| `video_file_path` | string | 否 | — | 本地视频相对路径 |
| `cover_url` | string | 否 | — | 封面 URL |
| `cover_file_path` | string | 否 | — | 本地封面相对路径 |
| `error` | string | 否 | — | 错误信息 |
| `request_id` | string | 否 | — | 请求关联标识 |
| `billed` | boolean | 否 | — | 上游计费标记 |
| `progress` | number | 否 | — | 上游进度 |
| `usage_ms` | integer | 否 | — | 生成耗时毫秒 |
| `raw_status` | string | 否 | — | 上游原始状态 |

<a id="schema-modelsvoiceturn"></a>

### ModelsVoiceTurn

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `role` | string | 是 | — | 通常为 user/assistant |
| `text` | string | 否 | — | 文本 |
| `audio_id` | string | 否 | — | 历史音频标识 |

<a id="schema-modelsvoicechatrequest"></a>

### ModelsVoiceChatRequest

audio 或 text 至少一个非空；此 handler 当前将领域校验失败包装为 HTTP 500。

至少满足一个分支；handler 实际错误状态见对应操作。

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider` | string | 否 | — | Provider 名称或请求描述 |
| `model` | string | 否 | — | 精确模型 ID |
| `audio` | string | 否 | — | 用户音频的 Base64；也可提供 text |
| `text` | string | 否 | — | 文本 |
| `voice` | string | 否 | — | 可选音色标识，由 Provider 支持 |
| `format` | string | 否 | 默认：`"wav"` | Provider 音频格式，默认 wav |
| `system` | string | 否 | — | 系统提示词 |
| `history` | [ModelsVoiceTurn](#schema-modelsvoiceturn)[] | 否 | — | 历史对话 |

<a id="schema-modelsvoicechatresult"></a>

### ModelsVoiceChatResult

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `provider` | string | 是 | — | Provider 名称或请求描述 |
| `model` | string | 是 | — | 精确模型 ID |
| `audio` | string | 否 | — | 持久化未完成时保留的 Base64 |
| `transcript` | string | 否 | — | 回复文字转录 |
| `user_text` | string | 否 | — | 识别的用户文本 |
| `format` | string | 否 | — | 音频格式 |
| `usage_ms` | integer | 否 | — | 生成耗时毫秒 |
| `request_id` | string | 否 | — | 请求关联标识 |
| `audio_file_path` | string | 否 | — | 生成音频相对路径 |

<a id="schema-modelslogentry"></a>

### ModelsLogEntry

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `id` | string | 是 | — | 标识 |
| `timestamp` | string | 是 | — | 时间戳 |
| `level` | string | 是 | — | 日志级别 |
| `source` | string | 是 | — | 来源 |
| `domain` | string | 否 | — | 领域 |
| `message` | string | 是 | — | 说明信息 |
| `fields` | object（实际附加字段） | 否 | — | 实际结构化日志字段 |
| `trace_id` | string | 否 | — | 链路标识 |

<a id="schema-modelslogs"></a>

### ModelsLogs

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `logs` | [ModelsLogEntry](#schema-modelslogentry)[] | 是 | — | 日志列表 |
| `total` | integer | 是 | — | 总数/返回条数，见操作语义 |

<a id="schema-modelslogstats"></a>

### ModelsLogStats

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `total` | integer | 是 | — | 总数/返回条数，见操作语义 |
| `by_level` | map&lt;string, integer&gt; | 是 | — | 按级别计数 |
| `by_source` | map&lt;string, integer&gt; | 是 | — | 按来源计数 |
| `requests_per_minute` | number | 是 | — | 日志统计中的每分钟请求数 |

<a id="schema-modelsbudget"></a>

### ModelsBudget

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `tokens_used` | integer | 是 | — | 已用 token |
| `tokens_max` | integer | 是 | — | token 上限 |
| `tokens_remaining` | integer | 是 | — | 剩余 token |
| `cost_used` | number | 是 | — | 已用成本 |
| `cost_max` | number | 是 | — | 成本上限 |
| `cost_remaining` | number | 是 | — | 剩余成本 |
| `duration_used` | string | 是 | — | 已用时长 |
| `duration_max` | string | 是 | — | 时长上限 |
| `duration_remaining` | string | 是 | — | 剩余时长 |
| `exhausted` | boolean | 是 | — | 是否耗尽预算 |

<a id="schema-modelsdesktopinfo"></a>

### ModelsDesktopInfo

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `os` | string | 是 | — | 宿主操作系统 |
| `arch` | string | 是 | — | 宿主架构 |
| `hostname` | string | 是 | — | 宿主名称 |
| `version` | string | 是 | — | 版本标识 |

<a id="schema-modelsnotification"></a>

### ModelsNotification

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `id` | string | 是 | — | 标识 |
| `title` | string | 是 | — | 通知标题 |
| `body` | string | 是 | — | 通知正文 |
| `type` | string | 是 | — | 已知展示类型；API 不拒绝其他字符串 |
| `timestamp` | string (date-time) | 是 | — | 时间戳 |
| `source` | string | 否 | — | 来源 |

<a id="schema-modelsnotifications"></a>

### ModelsNotifications

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `notifications` | [ModelsNotification](#schema-modelsnotification)[] | 是 | — | 通知队列，空队列为 [] |
| `total` | integer | 是 | — | 总数/返回条数，见操作语义 |

<a id="schema-modelsnotifyrequest"></a>

### ModelsNotifyRequest

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `title` | string | 是 | 最短: 1 | 通知标题 |
| `body` | string | 否 | — | 通知正文 |
| `type` | string | 否 | 默认：`"info"` | 已知值 info/warning/error/success；不拒绝其他字符串 |

<a id="schema-modelsclipboard"></a>

### ModelsClipboard

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `content` | string | 否 | — | POST 省略/空 content 会写空剪贴板 |

<a id="schema-modelsreasoningpolicywrite"></a>

### ModelsReasoningPolicyWrite

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `mode` | string | 否 | 枚举：`""`, `"auto"`, `"on"`, `"off"`, `"effort"` | 空值/auto 使用原生默认；全局策略不接受 inherit |
| `effort` | string | 否 | 枚举：`"low"`, `"medium"`, `"high"`, `"xhigh"`, `"max"` | 仅 mode=effort 时使用 |

<a id="schema-modelsmodelspecwrite"></a>

### ModelsModelSpecWrite

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `id` | string | 是 | 最短: 1 | 标识 |
| `display_name` | string | 否 | — | 展示名称 |
| `is_custom` | boolean | 否 | — | 是否自定义 |
| `capabilities` | string[] 或 null | 否 | 条目枚举：`"text"`, `"vision"`, `"video"`, `"audio"`, `"code"`, `"image_generation"`, `"video_generation"`, `"embedding"` | 模态能力声明 |
| `reasoning_support` | string | 否 | 枚举：`"supported"`, `"unsupported"`, `"unknown"` | 推理支持状态 |
| `reasoning_control` | [ModelsReasoningControl](#schema-modelsreasoningcontrol) | 否 | — | 推理协议声明 |
| `embedding` | [ModelsEmbeddingSpec](#schema-modelsembeddingspec) | 否 | — | Embedding 声明 |

<a id="schema-modelsroutingwrite"></a>

### ModelsRoutingWrite

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `enabled` | boolean | 否 | — | 是否启用 |
| `strategy` | string | 否 | — | cost-aware / quality-first / latency-first |

<a id="schema-modelscachewrite"></a>

### ModelsCacheWrite

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `enabled` | boolean | 否 | — | 是否启用 |
| `similarity` | number | 否 | — | 缓存相似度阈值 |
| `ttl` | string | 否 | — | Go duration 字符串 |
| `max_entries` | integer | 否 | — | 缓存最大条数 |

<a id="schema-modelsclipboardread"></a>

### ModelsClipboardRead

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `content` | string | 是 | — | POST 省略/空 content 会写空剪贴板 |

<a id="schema-modelssecurityread"></a>

### ModelsSecurityRead

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `gateway_enabled` | boolean | 是 | — | 是否启用网关认证 |
| `injection_detection` | boolean | 是 | — | 是否启用注入检测 |
| `pii_filter` | boolean | 是 | — | 是否启用 PII 处理 |
| `content_filter` | boolean | 是 | — | 是否启用内容过滤 |
| `rate_limit_rpm` | integer | 是 | — | 每分钟请求数配置 |

<a id="schema-modelssandboxread"></a>

### ModelsSandboxRead

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `network_enabled` | boolean | 是 | — | 代码执行宿主网络开关 |
| `allowed_paths` | string[] / null | 是 | — | 代码执行可读路径 |

<a id="schema-messageerror"></a>

### MessageError

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `error` | string | 是 | — | 错误信息 |

<a id="schema-apierror"></a>

### APIError

| 字段 | 类型 / 契约 | 必填 / 必有 | 默认 / 限制 | 含义 |
| --- | --- | --- | --- | --- |
| `code` | string | 是 | 枚举：`"BAD_REQUEST"`, `"INTERNAL_ERROR"`, `"NOT_FOUND"`, `"UNAUTHORIZED"`, `"RATE_LIMITED"`, `"SERVICE_UNAVAILABLE"`, `"CRON_DISABLED"`, `"CRON_INVALID_SCHEDULE"`, `"CRON_COMPILE_FAILED"`, `"CRON_VALIDATE_FAILED"`, `"CRON_NOT_SUPPORTED"`, `"CRON_QUOTA_EXCEEDED"`, `"CRON_JOB_NOT_FOUND"`, `"CRON_JOB_PAUSED"`, `"CRON_EXECUTOR_UNAVAILABLE"`, `"CRON_UNKNOWN_ACTION"` | 错误分类 |
| `message` | string | 是 | — | 说明信息 |
| `error` | string | 是 | — | 兼容字段，等于 message |
| `details` | JSON 值 | 否 | — | 实际错误附加信息 |
| `docs_url` | string | 否 | — | 错误文档链接 |
