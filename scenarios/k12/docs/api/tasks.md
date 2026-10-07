# 图片任务、资料准备与作品 API

[English](tasks.en.md) · [K12 API 总览](../../API.md) · [OpenAPI 完整契约](../../../../api/openapi.yaml)

适用范围：当前主线及工作区源码。安装包是否具备这些接口，以实际服务版本和发布说明为准；K12 场景必须已挂载且对应运行时已注入。以下路径前缀统一为 `/api/k12`。

## 身份、格式与完成标准

- 业务请求使用所连接服务的 `Authorization: Bearer <token>`。本机 Sidecar 的 owner 由服务组合状态解析；云端 owner 由已认证上下文解析，不能从 body/query/header 自报 owner。
- 图片任务与资产显式校验 owner→agent；云端查询已有图片任务还核验持久 owner。逐题 source-actions / answer-feedback 从 dispatch/problem 推导 agent，越 owner 404，已归属后的命令权限拒绝 403。资料准备以 owner/document 为边界。
- `creative-works` 的直接创建、列表、详情、生成、删除 handler 按请求 agent 和作品记录作用域操作，当前没有独立 owner→agent 授权检查；带图片的发送分支另校验资产 owner。这里描述实际 handler 边界，不承诺这些直接入口具有与图片 facade 相同的云端归属检查。
- JSON 命令通常严格拒绝未知字段及多个 JSON 值，体积上限 1 MiB。资料恢复 POST 是例外：当前 handler 只解码首个 JSON 值，忽略未知字段，也没有设置专门请求体上限或检查尾随 JSON。资产上传的 JSON 分支仍有 1 MiB 信封上限；multipart 原图上限为 10 MiB。错误体为 `{"error":"说明"}`，不能假定另有统一 `code/message`。
- 时间字段为 Unix 秒，`timeout_override_ms` 为毫秒，图片 `size` 为字节。版本/摘要/ID应从当前查询结果读取，不自行推导。
- 低频恢复命令需要原已知恢复上下文。当前普通 dispatch/作业投影未公开 `job_version`，来源纠正响应也未公开其未知回复的 `recovery_response_digest`；缺少这些上下文时不能用 dispatch.version 替代或猜测摘要。这里不承诺公开 API 已提供原始内部账本的全部查询。
- 图片默认自动分类、识别、判定和推进，返回批注原图或相应内容终态；明确不可辨认只属于内容事实，不代表技术失败。`routed`、HTTP 200/202、模型接纳都不是最终产物成功。[图片任务终态规则](../../API.md#image-task-completion)仍适用。
- 显式 `confirm`、来源纠正、恢复、再次生成和发送接口是条件命令；不应给默认流程额外加确认、补拍或收件人选择。结果未知先查询和对账，只有具体恢复命令中的明确重复执行/计费授权允许其新尝试。

## 路由索引

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/api/k12/materials/{document_id}/preparation` | [读取资料准备明细](#op-get-api-k12-materials-document-id-preparation) |
| GET | `/api/k12/materials/preparations` | [批量读取资料准备概况](#op-get-api-k12-materials-preparations) |
| GET | `/api/k12/materials/{document_id}/preparation/{task_id}/recovery` | [读取一次资料恢复计划](#op-get-api-k12-materials-document-id-preparation-task-id-recovery) |
| POST | `/api/k12/materials/{document_id}/preparation/{task_id}/recovery` | [授权一次资料恢复](#op-post-api-k12-materials-document-id-preparation-task-id-recovery) |
| POST | `/api/k12/image-tasks` | [接纳图片任务](#op-post-api-k12-image-tasks) |
| GET | `/api/k12/image-tasks/recoverable` | [恢复会话任务投影](#op-get-api-k12-image-tasks-recoverable) |
| GET | `/api/k12/image-tasks/{id}` | [读取图片任务投影](#op-get-api-k12-image-tasks-id) |
| POST | `/api/k12/image-tasks/{id}/confirm` | [执行显式意图或作品命令](#op-post-api-k12-image-tasks-id-confirm) |
| POST | `/api/k12/image-tasks/{id}/retry` | [安全重试图片任务](#op-post-api-k12-image-tasks-id-retry) |
| POST | `/api/k12/image-tasks/{id}/problems/{problem_id}/final-source-corrections` | [纠正已完成任务的原题来源](#op-post-api-k12-image-tasks-id-problems-problem-id-final-source-corrections) |
| POST | `/api/k12/image-tasks/{id}/recognition-recovery-attempts` | [授权一次未知识别调用恢复](#op-post-api-k12-image-tasks-id-recognition-recovery-attempts) |
| POST | `/api/k12/image-tasks/{id}/grounding-source-recovery-attempts` | [恢复原任务教材来源](#op-post-api-k12-image-tasks-id-grounding-source-recovery-attempts) |
| POST | `/api/k12/image-tasks/{id}/reparse` | [重新解析已知分类回复](#op-post-api-k12-image-tasks-id-reparse) |
| POST | `/api/k12/image-tasks/{id}/cancel` | [取消图片任务](#op-post-api-k12-image-tasks-id-cancel) |
| GET | `/api/k12/image-tasks/{id}/result` | [读取图片结果和执行回执](#op-get-api-k12-image-tasks-id-result) |
| POST | `/api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/source-actions` | [修正逐题输入来源](#op-post-api-k12-image-tasks-dispatch-id-problems-problem-id-source-actions) |
| POST | `/api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/answer-feedback` | [反馈采用答案资产的错误](#op-post-api-k12-image-tasks-dispatch-id-problems-problem-id-answer-feedback) |
| GET | `/api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/answer-feedback` | [读取原评估与有效纠正](#op-get-api-k12-image-tasks-dispatch-id-problems-problem-id-answer-feedback) |
| GET | `/api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/answer-feedback/{feedback_id}` | [读取答案反馈处理终态](#op-get-api-k12-image-tasks-dispatch-id-problems-problem-id-answer-feedback-feedback-id) |
| POST | `/api/k12/creative-works` | [直接创建纯文字写作作品](#op-post-api-k12-creative-works) |
| GET | `/api/k12/creative-works` | [列出当前孩子作品](#op-get-api-k12-creative-works) |
| GET | `/api/k12/creative-works/{id}` | [读取作品与点评事实](#op-get-api-k12-creative-works-id) |
| POST | `/api/k12/creative-works/{id}/generate-feedback` | [生成或恢复一次作品点评](#op-post-api-k12-creative-works-id-generate-feedback) |
| POST | `/api/k12/creative-works/{id}/send` | [发送作品及成功点评到手机](#op-post-api-k12-creative-works-id-send) |
| DELETE | `/api/k12/creative-works/{id}` | [按版本删除当前作品](#op-delete-api-k12-creative-works-id) |
| POST | `/api/k12/assets` | [上传原图资产](#op-post-api-k12-assets) |
| GET | `/api/k12/assets/{file}` | [下载原图资产](#op-get-api-k12-assets-file) |

<a id="op-get-api-k12-materials-document-id-preparation"></a>

## `GET /api/k12/materials/{document_id}/preparation`

读取资料准备明细。

只读当前 owner/document 的来源修订、题目状态和已发布答案。题目排版装饰不计入数量，但真实结果未知仍影响摘要。该接口不创建或推进准备任务。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `document_id` | 是 | string；当前资源身份 |

成功响应：`200` → [K12TasksMaterialPreparationSummary](#schema-k12tasksmaterialpreparationsummary)。

错误：`401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `500` 持久化、投影或资产基础设施失败。

<a id="op-get-api-k12-materials-preparations"></a>

## `GET /api/k12/materials/preparations`

批量读取资料准备概况。

概况中 items=[]，不返回 source_observations；不是详细题目明细。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| query | `document_id` | 否 | array<string>；重复 query 参数；去空、去重，缺失文档跳过；省略返回空数组 |

成功响应：`200` → [K12TasksMaterialPreparationList](#schema-k12tasksmaterialpreparationlist)。

错误：`401` 当前服务令牌/已认证 principal 不可用; `500` 持久化、投影或资产基础设施失败。

<a id="op-get-api-k12-materials-document-id-preparation-task-id-recovery"></a>

## `GET /api/k12/materials/{document_id}/preparation/{task_id}/recovery`

读取一次资料恢复计划。

仅原 outcome_unknown 的 solve_verify Provider 调用可形成计划：必须恰好一个未决调用、存在可复用独立生成回执（需要视觉证据时还须已成功提取）。计划冻结来源修订、摘要、模型与fingerprint；不会发送模型请求。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `document_id` | 是 | string；当前资源身份 |
| path | `task_id` | 是 | string；当前资源身份 |

成功响应：`200` → [K12TasksMaterialRecoveryPlan](#schema-k12tasksmaterialrecoveryplan)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `500` 持久化、投影或资产基础设施失败。

<a id="op-post-api-k12-materials-document-id-preparation-task-id-recovery"></a>

## `POST /api/k12/materials/{document_id}/preparation/{task_id}/recovery`

授权一次资料恢复。

POST 使用刚读取的 fingerprint，显式接受可能重复计费。Idempotency-Key 原始 UTF-8 最长 220 字节，不能为空白，按 owner 持久复用；同键改变 document/task/fingerprint 为 409。仅将原任务重新排队，原 worker 消费一次授权；202 和 replacement_invocation_id 均不是答案就绪证明。

该 POST 当前忽略未知 body 字段及首个 JSON 值之后的内容；没有套用其他 JSON 命令的 1 MiB 限制。调用方仍应只提交所列命令字段和单个 JSON 对象。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `document_id` | 是 | string；当前资源身份 |
| path | `task_id` | 是 | string；当前资源身份 |
| header | `Idempotency-Key` | 是 | string (minLength=1)；持久命令幂等键；同一键不得改载荷 |

请求体：[K12TasksMaterialRecoveryCommand](#schema-k12tasksmaterialrecoverycommand) (`application/json`)。

成功响应：`202` → [K12TasksMaterialRecoveryResult](#schema-k12tasksmaterialrecoveryresult)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `500` 持久化、投影或资产基础设施失败。

<a id="op-post-api-k12-image-tasks"></a>

## `POST /api/k12/image-tasks`

接纳图片任务。

稳定去重身份为agent + source_kind + source_ref + attempt_generation；同身份必须保持会话、图片、意图上下文、creative_entry和模型选择一致。自动分类/处理异步启动。多图整组预校验后按输入顺序逐页接纳；中途失败可带tasks与零起始failed_index，已接纳页保留，不盲重发。默认省略creative_entry；档案手工新建作品可用kind=new_work，task_intent=writing/artwork/unknown，并按explicit_commit投影操作。

请求体：[K12TasksCreateImageTaskReq](#schema-k12taskscreateimagetaskreq) (`application/json`)。

成功响应：`200` → [K12TasksImageTaskAccepted](#schema-k12tasksimagetaskaccepted) / [K12TasksImageTaskBatchAccepted](#schema-k12tasksimagetaskbatchaccepted)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `413` 超过请求/图片大小上限; `502` 下游生成或渲染失败；不是内容无法识别; `503` 该路由依赖的运行时不可用。

<a id="op-get-api-k12-image-tasks-recoverable"></a>

## `GET /api/k12/image-tasks/recoverable`

恢复会话任务投影。

返回会话下可见任务，包含等待、失败与已终态事实。projection_ready表示已离开routing；terminal仅是投影终态，失败也可以为true。读取不会启动worker、重发或重新计费。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| query | `agent` | 是 | string；孩子 TutorAgent 名称；不作为 owner 身份 |
| query | `session` | 是 | string (minLength=1)；创建时的 source_session |

成功响应：`200` → [K12TasksImageTaskRecoverables](#schema-k12tasksimagetaskrecoverables)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `500` 持久化、投影或资产基础设施失败; `503` 该路由依赖的运行时不可用。

<a id="op-get-api-k12-image-tasks-id"></a>

## `GET /api/k12/image-tasks/{id}`

读取图片任务投影。

轮询同一dispatch。routed仅表示分类路由成立；实际处理需看progress以及target_projection，终稿需另取result。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `id` | 是 | string；当前资源身份 |
| query | `agent` | 是 | string；孩子 TutorAgent 名称；不作为 owner 身份 |

成功响应：`200` → [K12TasksImageTaskResponse](#schema-k12tasksimagetaskresponse)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `502` 下游生成或渲染失败；不是内容无法识别; `503` 该路由依赖的运行时不可用。

<a id="op-post-api-k12-image-tasks-id-confirm"></a>

## `POST /api/k12/image-tasks/{id}/confirm`

执行显式意图或作品命令。

这是条件式显式命令，不是图片默认流程必经确认。分类确有awaiting_confirmation时才提交intent；homework分支用于显式题目修正；creative与homework/intent互斥。freeze_ocr仅对仍等待的作品冻结全文/片段，segment_corrections需完整canonical_content；commit仅用于手工作品入库，不能带OCR冻结字段。写作commit的content_markdown若提交必须等于已冻结正文；作品不会打分、排名或代写。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `id` | 是 | string；当前资源身份 |

请求体：[K12TasksConfirmImageTaskReq](#schema-k12tasksconfirmimagetaskreq) (`application/json`)。

成功响应：`200` → [K12TasksImageTaskResponse](#schema-k12tasksimagetaskresponse)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `413` 超过请求/图片大小上限; `502` 下游生成或渲染失败；不是内容无法识别; `503` 该路由依赖的运行时不可用。

<a id="op-post-api-k12-image-tasks-id-retry"></a>

## `POST /api/k12/image-tasks/{id}/retry`

安全重试图片任务。

仅重试有持久安全重试依据的失败调用，复用冻结模型/输入。未知调用不可用此命令重发；先查询原任务/回执。返回投影仍须继续核对最终产物。

`intent` 省略或为空时保留普通重试语义。固定 `known_local_technical` 只恢复同账户、同实例、同 dispatch 当前版本绑定的原 job：必须为 `failed_terminal`、失败阶段 `assessing`、阶段计数 3，当前输入最新代次的物理调用为 `failed/local/provider_response_processed` 且无结果。旧历史失败或成功回执不能代替当前资格；任何未知、在途或无依据对账回执、已有终稿、成功 assessing 检查点均拒绝。服务从认证上下文派生账户，不接受客户端账户、模型或新任务身份。

父任务新窗口与原 job `queued` 在一个双版本 CAS 事务提交后才启动；运行对象不可恢复或调度生命周期已关闭时零写入。保持完整冻结模型、预算、输入、历史调用和 assessment；计数 3 不清零，新调用从代次 4 开始。普通 max3 和终态状态机保持不变。同版本重复请求返回 `409`；结果未知时只查询，不能再次提交恢复。`200` 只表示恢复已接纳，仍需原任务最终产物与交付回执。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `id` | 是 | string；当前资源身份 |

请求体：[K12TasksImageTaskRetryReq](#schema-k12tasksimagetaskretryreq) (`application/json`)。

成功响应：`200` → [K12TasksImageTaskResponse](#schema-k12tasksimagetaskresponse)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `413` 超过请求/图片大小上限; `502` 下游生成或渲染失败；不是内容无法识别; `503` 该路由依赖的运行时不可用。

<a id="op-post-api-k12-image-tasks-id-problems-problem-id-final-source-corrections"></a>

## `POST /api/k12/image-tasks/{id}/problems/{problem_id}/final-source-corrections`

纠正已完成任务的原题来源。

仅原任务已completed且artifact_id/digest、逐题input_revision/digest都匹配时，独立复核同一道原题；不重新生成学生作答。body idempotency_key冻结本次身份，同键重放200，新命令201；201可能是sent/outcome_unknown/failed，只有status=completed且artifact存在才是新终稿。显式恢复未知纠正须同时提供recovery_of、其recovery_response_digest、accept_duplicate_execution=true，并保持原冻结输入；只允许一个恢复子命令。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `id` | 是 | string；当前资源身份 |
| path | `problem_id` | 是 | string；当前资源身份 |

请求体：[K12TasksFinalSourceCorrectionInput](#schema-k12tasksfinalsourcecorrectioninput) (`application/json`)。

成功响应：`201,200` → [K12TasksFinalSourceCorrectionResult](#schema-k12tasksfinalsourcecorrectionresult)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `413` 超过请求/图片大小上限; `502` 下游生成或渲染失败；不是内容无法识别; `503` 该路由依赖的运行时不可用。

<a id="op-post-api-k12-image-tasks-id-recognition-recovery-attempts"></a>

## `POST /api/k12/image-tasks/{id}/recognition-recovery-attempts`

授权一次未知识别调用恢复。

仅原作业outcome_unknown且dispatch/job版本匹配，授权一个未知主批次或单项复核；不替换模型、原图或计划。body idempotency_key为持久身份，重放200不再次StartAsync，新授权202启动原作业。timeout_override_ms省略/0保持原值；180000仅适用于原120000ms主批次且阶段预算足够，单项复核不允许覆盖。accept_duplicate_execution必须true；只读请求或普通retry不等价于该授权。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `id` | 是 | string；当前资源身份 |

请求体：[K12TasksRecognitionRecoveryInput](#schema-k12tasksrecognitionrecoveryinput) (`application/json`)。

成功响应：`202,200` → [K12TasksRecognitionRecoveryResult](#schema-k12tasksrecognitionrecoveryresult)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `413` 超过请求/图片大小上限; `503` 该路由依赖的运行时不可用。

<a id="op-post-api-k12-image-tasks-id-grounding-source-recovery-attempts"></a>

## `POST /api/k12/image-tasks/{id}/grounding-source-recovery-attempts`

恢复原任务教材来源。

仅符合failed_retryable/评估恢复条件的原任务，从原语义检索切换为同owner、agent、学科math、当前已核验教材页的verified_text。必须有原semantic检索回执，没有成功的冲突来源结果或sent/outcome_unknown模型请求；不改旧回执、不重放未决模型请求。body key及版本围栏，重放200，新接纳202启动原作业；仍须查询终稿。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `id` | 是 | string；当前资源身份 |

请求体：[K12TasksGroundingSourceRecoveryInput](#schema-k12tasksgroundingsourcerecoveryinput) (`application/json`)。

成功响应：`202,200` → [K12TasksGroundingSourceRecoveryResult](#schema-k12tasksgroundingsourcerecoveryresult)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `413` 超过请求/图片大小上限; `503` 该路由依赖的运行时不可用。

<a id="op-post-api-k12-image-tasks-id-reparse"></a>

## `POST /api/k12/image-tasks/{id}/reparse`

重新解析已知分类回复。

仅当前失败且retry-safe、尚无目标的分类调用，重新解析已保存的原始回复。不会再请求模型，也不会自行推进下游模型任务；成功重放复用原解析事实。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `id` | 是 | string；当前资源身份 |

请求体：[K12TasksImageTaskVersionReq](#schema-k12tasksimagetaskversionreq) (`application/json`)。

成功响应：`200` → [K12TasksImageTaskResponse](#schema-k12tasksimagetaskresponse)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `413` 超过请求/图片大小上限; `502` 下游生成或渲染失败；不是内容无法识别; `503` 该路由依赖的运行时不可用。

<a id="op-post-api-k12-image-tasks-id-cancel"></a>

## `POST /api/k12/image-tasks/{id}/cancel`

取消图片任务。

沿同一任务取消关联作业，版本须匹配；已cancelled可直接返回。不能据此推断已发往Provider的请求取消或退款；当前阶段不允许取消时409。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `id` | 是 | string；当前资源身份 |

请求体：[K12TasksImageTaskVersionReq](#schema-k12tasksimagetaskversionreq) (`application/json`)。

成功响应：`200` → [K12TasksImageTaskResponse](#schema-k12tasksimagetaskresponse)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `413` 超过请求/图片大小上限; `503` 该路由依赖的运行时不可用。

<a id="op-get-api-k12-image-tasks-id-result"></a>

## `GET /api/k12/image-tasks/{id}/result`

读取图片结果和执行回执。

查询是只读，200可以result=null（未就绪），也可以只有作品不可识别终态说明。完成作业结果包含result.payload.annotated_image（mime/data_base64/digest）批注原图；空白题走parent_teaching_guide。真正无法辨认仅标对应内容，不把Provider超时/未知/故障伪装为无法识别。operation_receipts保存模型/物理执行身份；source_attachments只返回摘要/大小。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `id` | 是 | string；当前资源身份 |
| query | `agent` | 是 | string；孩子 TutorAgent 名称；不作为 owner 身份 |

成功响应：`200` → [K12TasksImageTaskResult](#schema-k12tasksimagetaskresult)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `502` 下游生成或渲染失败；不是内容无法识别; `503` 该路由依赖的运行时不可用。

<a id="op-post-api-k12-image-tasks-dispatch-id-problems-problem-id-source-actions"></a>

## `POST /api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/source-actions`

修正逐题输入来源。

action=correct_text/select_region/retake/skip/resume，structure_version和expected_input_revision必须正数且匹配当前事实。correct_text至少一项canonical文本；select_region为原图像素且在边界内；retake引用本owner/agent已ready资产；skip/resume传{}。命令与版本、source facts、幂等冻结响应同事务提交；重放返回当时原JSON。纠正/补图均为用户自愿操作，不是默认批改门槛。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `dispatch_id` | 是 | string；当前资源身份 |
| path | `problem_id` | 是 | string；当前资源身份 |
| header | `Idempotency-Key` | 是 | string (minLength=1)；持久命令幂等键；同一键不得改载荷 |

请求体：[K12TasksProblemSourceActionRequest](#schema-k12tasksproblemsourceactionrequest) (`application/json`)。

`payload`字段：`correct_text`: `question_canonical_markdown`, `answer_canonical_markdown`; `select_region`: `page_asset_id`, `region.{x,y,width,height}`; `retake`: `page_asset_id`; `skip` / `resume`: `{}`.

成功响应：`200` → [K12TasksProblemSourceActionResponse](#schema-k12tasksproblemsourceactionresponse)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `403` 已证明归属后的命令权限被拒绝; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `413` 超过请求/图片大小上限; `422` 来源 action 的领域载荷无效; `500` 持久化、投影或资产基础设施失败; `503` 该路由依赖的运行时不可用。

<a id="op-post-api-k12-image-tasks-dispatch-id-problems-problem-id-answer-feedback"></a>

## `POST /api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/answer-feedback`

反馈采用答案资产的错误。

仅kind=answer_error，原job_id/input_revision/result_digest与题目必须匹配且原评估采用asset答案。原因是错误反馈，不作为孩子新作答。事务内保存反馈、停用匹配的旧资产版本并发出恢复事件；不误停用独立发布的新版本。202仅接纳，可查询feedback及有效assessment；原任务/终稿和历史回执保留。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `dispatch_id` | 是 | string；当前资源身份 |
| path | `problem_id` | 是 | string；当前资源身份 |
| header | `Idempotency-Key` | 是 | string (minLength=1)；持久命令幂等键；同一键不得改载荷 |

请求体：[K12TasksAnswerFeedbackRequest](#schema-k12tasksanswerfeedbackrequest) (`application/json`)。

成功响应：`202` → [K12TasksAnswerFeedbackAccepted](#schema-k12tasksanswerfeedbackaccepted)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `403` 已证明归属后的命令权限被拒绝; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `413` 超过请求/图片大小上限; `500` 持久化、投影或资产基础设施失败; `503` 该路由依赖的运行时不可用。

<a id="op-get-api-k12-image-tasks-dispatch-id-problems-problem-id-answer-feedback"></a>

## `GET /api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/answer-feedback`

读取原评估与有效纠正。

从dispatch/problem推导agent/owner，不接受自报身份。返回job_id、原input_revision/result_digest及assessment.original/current/correction；提交反馈前应冻结这些原始身份。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `dispatch_id` | 是 | string；当前资源身份 |
| path | `problem_id` | 是 | string；当前资源身份 |

成功响应：`200` → [K12TasksAnswerFeedbackContext](#schema-k12tasksanswerfeedbackcontext)。

错误：`401` 当前服务令牌/已认证 principal 不可用; `403` 已证明归属后的命令权限被拒绝; `404` 目标不存在或不属于当前作用域; `503` 该路由依赖的运行时不可用。

<a id="op-get-api-k12-image-tasks-dispatch-id-problems-problem-id-answer-feedback-feedback-id"></a>

## `GET /api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/answer-feedback/{feedback_id}`

读取答案反馈处理终态。

反馈必须属于同owner、dispatch/problem。status=pending/completed/unresolved/outcome_unknown，逐target的outcomes为corrected/unresolved/outcome_unknown。未解决或未知不等于已纠正；同一反馈不会变成新的学生输入。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `dispatch_id` | 是 | string；当前资源身份 |
| path | `problem_id` | 是 | string；当前资源身份 |
| path | `feedback_id` | 是 | string；当前资源身份 |

成功响应：`200` → [K12TasksProblemAssetFeedback](#schema-k12tasksproblemassetfeedback)。

错误：`401` 当前服务令牌/已认证 principal 不可用; `403` 已证明归属后的命令权限被拒绝; `404` 目标不存在或不属于当前作用域; `500` 持久化、投影或资产基础设施失败; `503` 该路由依赖的运行时不可用。

<a id="op-post-api-k12-creative-works"></a>

## `POST /api/k12/creative-works`

直接创建纯文字写作作品。

唯一直接创建路径只接受writing和非空content_markdown。图片写作/美术走image-tasks。作品与首个点评generation原子持久化；重复key+相同载荷返回created=false，改变载荷冲突。若WorkFeedback worker已注入会异步启动；200不是点评已成功。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| header | `Idempotency-Key` | 是 | string (minLength=1)；持久命令幂等键；同一键不得改载荷 |

请求体：[K12TasksCreativeWorkCreateCommand](#schema-k12taskscreativeworkcreatecommand) (`application/json`)。

成功响应：`200` → [K12TasksCreativeWorkCreateResult](#schema-k12taskscreativeworkcreateresult)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `413` 超过请求/图片大小上限; `502` 下游生成或渲染失败；不是内容无法识别。

<a id="op-get-api-k12-creative-works"></a>

## `GET /api/k12/creative-works`

列出当前孩子作品。

返回作品及initial/latest/current点评generation；latest_generation_at仅成功点评时间，可为null；有实际匹配投递事实时带delivery_batch_id。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| query | `agent` | 是 | string；孩子 TutorAgent 名称；不作为 owner 身份 |
| query | `type` | 否 | string；可选writing/art筛选，省略全部；未匹配字符串返回空列表 |

成功响应：`200` → [K12TasksCreativeWorkList](#schema-k12taskscreativeworklist)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `500` 持久化、投影或资产基础设施失败。

<a id="op-get-api-k12-creative-works-id"></a>

## `GET /api/k12/creative-works/{id}`

读取作品与点评事实。

按agent、作品record和creative_work集合归属读取；不要把current_feedback进行态覆盖latest_feedback已成功事实。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `id` | 是 | string；当前资源身份 |
| query | `agent` | 是 | string；孩子 TutorAgent 名称；不作为 owner 身份 |

成功响应：`200` → [K12TasksCreativeWorkDTO](#schema-k12taskscreativeworkdto)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `500` 持久化、投影或资产基础设施失败; `502` 下游生成或渲染失败；不是内容无法识别。

<a id="op-post-api-k12-creative-works-id-generate-feedback"></a>

## `POST /api/k12/creative-works/{id}/generate-feedback`

生成或恢复一次作品点评。

持久generation按Idempotency-Key去重，恢复失败首点评或增加一次新点评，复用冻结原文/原图和模型上下文；该handler等待本次命令返回（不是统一202队列接口）。失败保留在generation且不替换成功latest；未知调用须对账，不能换key盲重发。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `id` | 是 | string；当前资源身份 |
| header | `Idempotency-Key` | 是 | string (minLength=1)；持久命令幂等键；同一键不得改载荷 |

请求体：[K12TasksAgentCommand](#schema-k12tasksagentcommand) (`application/json`)。

成功响应：`200` → [K12TasksCreativeWorkDTO](#schema-k12taskscreativeworkdto)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `413` 超过请求/图片大小上限; `502` 下游生成或渲染失败；不是内容无法识别。

<a id="op-post-api-k12-creative-works-id-send"></a>

## `POST /api/k12/creative-works/{id}/send`

发送作品及成功点评到手机。

仅有成功latest点评时可发送规范正文，图片作品附同一真实源图。按规范内容+附件身份冻结批次，重复相同投递复用现有批次；一次发送覆盖当前有效物理私聊目标并去重，不选渠道/收件人。200/provider接纳不等于delivered，查询批次及子回执终态，outcome_unknown不可盲重发。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `id` | 是 | string；当前资源身份 |

请求体：[K12TasksAgentCommand](#schema-k12tasksagentcommand) (`application/json`)。

成功响应：`200` → [K12TasksDeliveryBatch](#schema-k12tasksdeliverybatch)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `413` 超过请求/图片大小上限; `500` 持久化、投影或资产基础设施失败; `501` 投递能力未注入; `502` 下游生成或渲染失败；不是内容无法识别; `503` 该路由依赖的运行时不可用。

<a id="op-delete-api-k12-creative-works-id"></a>

## `DELETE /api/k12/creative-works/{id}`

按版本删除当前作品。

软删除当前对象并提交持久命令回执；版本不符或同key不同对象冲突409。不会以此撤回已送达IM消息或退款。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `id` | 是 | string；当前资源身份 |
| query | `agent` | 是 | string；孩子 TutorAgent 名称；不作为 owner 身份 |
| header | `Idempotency-Key` | 是 | string (minLength=1)；持久命令幂等键；同一键不得改载荷 |
| header | `If-Match` | 是 | string；正整数row_version，可写1、"1"或W/"1" |

成功响应：`200` → [K12TasksCreativeWorkDeleteResult](#schema-k12taskscreativeworkdeleteresult)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突。

<a id="op-post-api-k12-assets"></a>

## `POST /api/k12/assets`

上传原图资产。

支持multipart/form-data的file字段或JSON标准data_base64，返回内容寻址asset_id及字节size。multipart 原图上限为 10 MiB（10×1024×1024 字节）；JSON 分支经过通用 decode，整个 JSON 信封上限为 1 MiB，包含 Base64 膨胀及其他字段，不能用它上传 10 MiB 原图。JSON 未知字段和 multipart 额外表单字段当前被忽略。支持 PNG/JPEG/GIF/WebP 且魔数与完整解码格式一致；HEIC 不支持。空图/格式非法不生成 ready 资产；相同内容引用可用于稳定去重。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| query | `agent` | 否 | string；优先于multipart字段/JSON agent；三处至少一处非空 |

请求体：[K12TasksAssetUploadReq](#schema-k12tasksassetuploadreq) (`application/json`)。

另一种请求体：[K12TasksAssetMultipart](#schema-k12tasksassetmultipart) (`multipart/form-data`)。

成功响应：`200` → [K12TasksAssetUploadResult](#schema-k12tasksassetuploadresult)。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `409` 版本、状态、幂等身份或原始证据冲突; `413` 超过请求/图片大小上限; `415` 不是可完整解码的支持图片格式; `500` 持久化、投影或资产基础设施失败; `503` 该路由依赖的运行时不可用。

<a id="op-get-api-k12-assets-file"></a>

## `GET /api/k12/assets/{file}`

下载原图资产。

file是asset://<agent>/<file>的末段，不是完整URL/本机路径。成功为真实图片字节，Content-Type为已核验mime、Content-Length为实际大小，Cache-Control=private, max-age=86400, immutable；同agent不同owner或越界引用不存在为404，完整性/存储失败500。

| 位置 | 参数 | 必填 | 类型 / 契约 |
| --- | --- | --- | --- |
| path | `file` | 是 | string；当前资源身份 |
| query | `agent` | 是 | string；孩子 TutorAgent 名称；不作为 owner 身份 |

成功响应：`200` → 图片二进制字节。

错误：`400` 请求 JSON、字段、身份或格式无效; `401` 当前服务令牌/已认证 principal 不可用; `404` 目标不存在或不属于当前作用域; `500` 持久化、投影或资产基础设施失败; `503` 该路由依赖的运行时不可用。

## 完整调用示例

下面的 ID、摘要和输出文本均是示例。将 `$TOKEN` 设置为当前服务令牌；`$BASE` 是当前所连服务地址，例如 `http://127.0.0.1:16060`。不要把真实令牌写入仓库。`TutorAgent` 替换为已经创建的孩子助手。

### 上传、接纳与读取终稿

```bash
curl "$BASE/api/k12/assets?agent=TutorAgent" \
  -H "Authorization: Bearer $TOKEN" \
  -F 'file=@homework.png'
```

```json
{"asset_id":"asset://TutorAgent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png","size":204800}
```

使用实际返回的完整 asset_id：

```bash
curl "$BASE/api/k12/image-tasks" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"agent":"TutorAgent","source_kind":"api","source_ref":"homework-message-1","source_session":"demo-session","source_asset_refs":["asset://TutorAgent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png"],"attempt_generation":1}'
```

```json
{
  "created":true,
  "dispatch":{
    "dispatch_id":"dispatch-demo","task_intent":"unknown","status":"routing",
    "provider_display_name":"Configured provider","model_id":"configured-model",
    "retryable":false,"intent_evidence":[],"intent_confidence":0,
    "confirmation_candidates":[],"progress":{"operation":"classification","state":"routing"},
    "version":1,"created_at":1780000000,"updated_at":1780000000,
    "automatic_budget_seconds":300,"automatic_started_at":1780000000,
    "automatic_deadline_at":1780000300,"automatic_remaining_seconds":300
  }
}
```

```bash
curl "$BASE/api/k12/image-tasks/dispatch-demo?agent=TutorAgent" -H "Authorization: Bearer $TOKEN"
curl "$BASE/api/k12/image-tasks/dispatch-demo/result?agent=TutorAgent" -H "Authorization: Bearer $TOKEN"
curl "$BASE/api/k12/image-tasks/recoverable?agent=TutorAgent&session=demo-session" -H "Authorization: Bearer $TOKEN"
curl "$BASE/api/k12/assets/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png?agent=TutorAgent" -H "Authorization: Bearer $TOKEN" -o downloaded-homework.png
```

作业终稿响应形状如下；`data_base64`示例是缩略占位，实际值须是完整图片字节。`routed`仍不是完成依据，应用须核对任务投影 `progress.state=completed` 与必要批注图片：

```json
{
  "dispatch_id":"dispatch-demo","task_intent":"completed_homework","status":"routed",
  "source_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","source_attachments":[{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size_bytes":204800}],
  "operation_receipts":[],"grounding_evidence_receipts":[],"problem_grounding_receipts":[],
  "result":{"kind":"completed_homework","payload":{
    "mode":"grade","items":[],"task_intent":"completed_homework","result_surface":"annotated_homework",
    "markdown":"本次作业批改结果","image_warning":"",
    "annotated_image":{"mime":"image/png","data_base64":"REPLACE_WITH_COMPLETE_BASE64_IMAGE","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
  }}
}
```

### 显式逐题来源纠正

从当前投影读取 `structure_version` / `input_revision` 后，仅在用户自愿修正时调用：

```bash
curl "$BASE/api/k12/image-tasks/dispatch-demo/problems/problem-1/source-actions" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -H 'Idempotency-Key: source-correction-1' \
  -d '{"action":"correct_text","structure_version":1,"expected_input_revision":1,"payload":{"question_canonical_markdown":"计算 1 + 1","answer_canonical_markdown":"2"}}'
```

```json
{
  "command_receipt_id":"source-receipt-1","dispatch_id":"dispatch-demo","problem_id":"problem-1",
  "action":"correct_text","structure_version":1,"input_revision":2,
  "progressive_snapshot":{"structure_version":1,"snapshot_revision":2,
    "problem_progress":[{"problem_id":"problem-1","status":"processing","input_revision":2,"published_revision":0,"current_disposition":"current","source_region":null}],
    "coverage":{"total":1,"published":0,"skipped":0,"awaiting":1,"failed":0,"status":"in_progress","projection_revision":2}}
}
```

该响应只证明修正命令已提交，继续读取任务终稿。对采用答案资产的错误，先读取原身份，再反馈，而不是更改学生作答：

```bash
curl "$BASE/api/k12/image-tasks/dispatch-demo/problems/problem-1/answer-feedback" -H "Authorization: Bearer $TOKEN"
curl "$BASE/api/k12/image-tasks/dispatch-demo/problems/problem-1/answer-feedback" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -H 'Idempotency-Key: answer-feedback-1' \
  -d '{"kind":"answer_error","job_id":"job-from-context","input_revision":1,"result_digest":"digest-from-context","reason":"原答案与题目不一致"}'
```

响应 `{"created":true,"feedback":{...}}` 中 feedback 使用本页具名字段契约；保存实际 `feedback_id` 并GET对应 `.../answer-feedback/{feedback_id}`，同时核对 `assessment.current`，不能以202关闭反馈。

### 资料恢复与未知调用授权

```bash
curl "$BASE/api/k12/materials/document-1/preparation/task-1/recovery" -H "Authorization: Bearer $TOKEN"
curl "$BASE/api/k12/materials/document-1/preparation/task-1/recovery" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -H 'Idempotency-Key: material-recovery-1' \
  -d '{"fingerprint":"fingerprint-from-plan","allow_possible_duplicate_charge":true}'
```

```json
{"decision_id":"decision-1","task_id":"task-1","state":"queued"}
```

这个示例体现明确的重复计费授权，不能自动为用户勾选；成功后读取准备状态和发布答案。识别未知的精确授权请求形状：

```json
{"agent":"TutorAgent","version":4,"job_version":7,"source_physical_invocation_id":"physical-from-original-receipt","idempotency_key":"recognition-recovery-1","accept_duplicate_execution":true,"timeout_override_ms":0}
```

仅在已经取得原任务恢复上下文时提交给 `/image-tasks/{id}/recognition-recovery-attempts`。普通公开 dispatch/作业投影没有 `job_version`，不能把其 `version` 代入、猜测或自行递增；这个请求形状不表示普通客户端可仅靠图片任务查询补齐所有字段。不允许更换原图/模型，也不把请求超时当作可自动重复执行。

### 纯文字作品与显式手工图片作品

```bash
curl "$BASE/api/k12/creative-works" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -H 'Idempotency-Key: writing-create-1' \
  -d '{"agent":"TutorAgent","work_type":"writing","content_markdown":"今天我观察了窗外的小树。"}'
```

```json
{"work_id":"work-1","created":true,"initial_feedback_generation_id":"generation-1"}
```

```bash
curl "$BASE/api/k12/creative-works/work-1?agent=TutorAgent" -H "Authorization: Bearer $TOKEN"
```

核对 `initial_feedback` / `current_feedback.status`，已成功内容在 `latest_feedback.feedback.projection_markdown`。图片默认省略creative_entry以自动处理；只有明确手工档案入口才用：

```json
{"agent":"TutorAgent","source_kind":"api","source_ref":"manual-art-1","source_asset_refs":["asset://TutorAgent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png"],"attempt_generation":1,"creative_entry":{"kind":"new_work","task_intent":"artwork"}}
```

读取投影 `promotion_policy=explicit_commit` / `commit_required` 后提交 `/image-tasks/{id}/confirm`：

```json
{"agent":"TutorAgent","version":2,"creative":{"action":"commit","work_title":"窗外的小树","task_requirement":"观察并绘画"}}
```

手工写作若真正处于OCR待冻结状态，先以相同版本命令的creative分支 `{"action":"freeze_ocr","canonical_version":1,"canonical_content":"实际全文"}` 冻结；这不是普通图片自动流程的额外确认。发送作品前必须已有成功点评：

```bash
curl "$BASE/api/k12/creative-works/work-1/send" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"agent":"TutorAgent"}'
curl "$BASE/api/k12/creative-works/work-1?agent=TutorAgent" \
  -X DELETE -H "Authorization: Bearer $TOKEN" -H 'If-Match: "1"' -H 'Idempotency-Key: writing-delete-1'
```

发送响应按 `DeliveryBatch` 全字段契约返回批次及子回执；只有实际投递终态 `delivered` 才证明送达。删除示例中的 If-Match 应替换为当前真实row_version。

## 响应与命令字段参考

以下具名结构与完整 OpenAPI 一致。响应“必含”只表示序列化键存在，不表示模型结果成功；命令必填按 handler 校验。可选响应字段可能省略，允许 null 的字段在类型中标注。运行态、归属和互斥条件仍按对应操作说明执行。

<a id="schema-k12tasksagentcommand"></a>

### `K12TasksAgentCommand`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `agent` | string (minLength=1) | 是 |

<a id="schema-k12tasksagentinstructionssnapshot"></a>

### `K12TasksAgentInstructionsSnapshot`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `content` | string | 是 |
| `digest` | string | 是 |
| `source` | string | 是 |

<a id="schema-k12tasksannotatedimage"></a>

### `K12TasksAnnotatedImage`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `mime` | string | 是 |
| `data_base64` | string (base64) | 是 |
| `digest` | string | 是 |

<a id="schema-k12tasksanswerfeedbackaccepted"></a>

### `K12TasksAnswerFeedbackAccepted`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `created` | boolean | 是 |
| `feedback` | [K12TasksProblemAssetFeedback](#schema-k12tasksproblemassetfeedback) | 是 |

<a id="schema-k12tasksanswerfeedbackcontext"></a>

### `K12TasksAnswerFeedbackContext`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `job_id` | string | 是 |
| `input_revision` | integer | 是 |
| `result_digest` | string | 是 |
| `assessment` | [K12TasksEffectiveGradingAssessment](#schema-k12taskseffectivegradingassessment) | 是 |

<a id="schema-k12tasksanswerfeedbackrequest"></a>

### `K12TasksAnswerFeedbackRequest`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `input_revision` | integer (min=1) | 是 |
| `job_id` | string (minLength=1) | 是 |
| `kind` | `answer_error` | 是 |
| `reason` | string (minLength=1) | 是 |
| `result_digest` | string (minLength=1) | 是 |

<a id="schema-k12tasksanswerstate"></a>

### `K12TasksAnswerState`

`blank` / `present` / `unclear`

<a id="schema-k12tasksassetmultipart"></a>

### `K12TasksAssetMultipart`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `file` | string | 是 |
| `agent` | string | 可选 |

<a id="schema-k12tasksassetuploadreq"></a>

### `K12TasksAssetUploadReq`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `agent` | string | 可选 |
| `data_base64` | string (base64) | 是 |

<a id="schema-k12tasksassetuploadresult"></a>

### `K12TasksAssetUploadResult`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `asset_id` | string | 是 |
| `size` | integer (min=1, max=10485760) | 是 |

<a id="schema-k12tasksbboxdto"></a>

### `K12TasksBboxDTO`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `h` | number | 是 |
| `w` | number | 是 |
| `x` | number | 是 |
| `y` | number | 是 |

<a id="schema-k12tasksconfirmimagetaskreq"></a>

### `K12TasksConfirmImageTaskReq`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `agent` | string | 是 |
| `creative` | [K12TasksCreativeFreezeCommand](#schema-k12taskscreativefreezecommand) / [K12TasksCreativeCommitCommand](#schema-k12taskscreativecommitcommand) / null | 可选 |
| `homework` | object / null | 可选 |
| `homework.grade` | string | 可选 |
| `homework.question_corrections` | array<[K12TasksGradingQuestionCorrection](#schema-k12tasksgradingquestioncorrection)> / null | 可选 |
| `homework.subject` | string | 可选 |
| `intent` | `completed_homework` / `blank_worksheet` / `writing` / `artwork` | 可选 |
| `version` | integer (min=1) | 是 |

<a id="schema-k12taskscreateimagetaskreq"></a>

### `K12TasksCreateImageTaskReq`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `agent` | string (minLength=1) | 是 |
| `attempt_generation` | integer (min=1) | 是 |
| `creative_entry` | object / null | 可选 |
| `creative_entry.kind` | `new_work` | 是 |
| `creative_entry.task_intent` | `writing` / `artwork` / `unknown` | 是 |
| `message_intent` | string | 可选 |
| `route_request` | [K12TasksImageTaskRouteRequest](#schema-k12tasksimagetaskrouterequest) | 可选 |
| `source_asset_refs` | array<string (minLength=1)> (minItems=1) | 是 |
| `source_kind` | [K12TasksImageTaskSourceKind](#schema-k12tasksimagetasksourcekind) | 是 |
| `source_ref` | string (minLength=1) | 是 |
| `source_session` | string | 可选 |

<a id="schema-k12taskscreativecommitcommand"></a>

### `K12TasksCreativeCommitCommand`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `action` | `commit` | 是 |
| `work_title` | string | 可选 |
| `task_requirement` | string | 可选 |
| `intent` | string | 可选 |
| `content_markdown` | string | 可选 |
| `canonical_version` | `0` | 可选 |
| `canonical_content` | `` | 可选 |
| `segment_corrections` | array<[K12TasksCreativeWorkIntakeOCRCorrection](#schema-k12taskscreativeworkintakeocrcorrection)> (maxItems=0) / null | 可选 |

<a id="schema-k12taskscreativefreezecommand"></a>

### `K12TasksCreativeFreezeCommand`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `action` | `freeze_ocr` | 是 |
| `canonical_version` | integer (min=1) | 是 |
| `canonical_content` | string | 可选 |
| `segment_corrections` | array<[K12TasksCreativeWorkIntakeOCRCorrection](#schema-k12taskscreativeworkintakeocrcorrection)> | 可选 |
| `work_title` | `` | 可选 |
| `task_requirement` | `` | 可选 |
| `intent` | `` | 可选 |
| `content_markdown` | `` | 可选 |

<a id="schema-k12taskscreativeimagetaskaction"></a>

### `K12TasksCreativeImageTaskAction`

`freeze_ocr` / `commit`

<a id="schema-k12taskscreativeresultfeedback"></a>

### `K12TasksCreativeResultFeedback`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `generation_id` | string | 是 |
| `structured_feedback` | [K12TasksWorkFeedback](#schema-k12tasksworkfeedback) | 是 |
| `projection_markdown` | string | 是 |

<a id="schema-k12taskscreativeresultintake"></a>

### `K12TasksCreativeResultIntake`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `intake_id` | string | 是 |
| `status` | [K12TasksCreativeWorkIntakeStatus](#schema-k12taskscreativeworkintakestatus) | 是 |

<a id="schema-k12taskscreativeresultpayload"></a>

### `K12TasksCreativeResultPayload`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `intake` | [K12TasksCreativeResultIntake](#schema-k12taskscreativeresultintake) | 是 |
| `outcome` | string | 可选 |
| `notice` | string | 可选 |
| `work` | [K12TasksImageTaskCreativeWorkDTO](#schema-k12tasksimagetaskcreativeworkdto) | 可选 |
| `feedback` | [K12TasksCreativeResultFeedback](#schema-k12taskscreativeresultfeedback) | 可选 |

<a id="schema-k12taskscreativeresultprojection"></a>

### `K12TasksCreativeResultProjection`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `kind` | `writing` / `artwork` | 是 |
| `payload` | [K12TasksCreativeResultPayload](#schema-k12taskscreativeresultpayload) | 是 |

<a id="schema-k12taskscreativeworkcreatecommand"></a>

### `K12TasksCreativeWorkCreateCommand`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `agent` | string (minLength=1) | 是 |
| `work_type` | `writing` | 是 |
| `content_markdown` | string (minLength=1) | 是 |

<a id="schema-k12taskscreativeworkcreateresult"></a>

### `K12TasksCreativeWorkCreateResult`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `work_id` | string | 是 |
| `created` | boolean | 是 |
| `initial_feedback_generation_id` | string | 是 |

<a id="schema-k12taskscreativeworkdto"></a>

### `K12TasksCreativeWorkDTO`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `content_markdown` | string | 可选 |
| `created_at` | integer | 是 |
| `current_feedback` | [K12TasksWorkFeedbackGenerationDTO](#schema-k12tasksworkfeedbackgenerationdto) / null | 可选 |
| `delivery_batch_id` | string | 可选 |
| `display_name` | string | 是 |
| `initial_feedback` | [K12TasksWorkFeedbackGenerationDTO](#schema-k12tasksworkfeedbackgenerationdto) / null | 可选 |
| `latest_feedback` | [K12TasksWorkFeedbackGenerationDTO](#schema-k12tasksworkfeedbackgenerationdto) / null | 可选 |
| `latest_generation_at` | integer / null | 是 |
| `row_version` | integer | 是 |
| `source_asset_id` | string | 可选 |
| `work_id` | string | 是 |
| `work_title` | string | 可选 |
| `work_type` | string | 是 |

<a id="schema-k12taskscreativeworkdeleteresult"></a>

### `K12TasksCreativeWorkDeleteResult`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `deleted` | boolean | 是 |
| `work_id` | string | 是 |
| `row_version` | integer | 是 |

<a id="schema-k12taskscreativeworkentrykind"></a>

### `K12TasksCreativeWorkEntryKind`

`auto` / `new_work` / `revision`

<a id="schema-k12taskscreativeworkintakeocrcorrection"></a>

### `K12TasksCreativeWorkIntakeOCRCorrection`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `canonical_text` | string | 是 |
| `segment_id` | string | 是 |

<a id="schema-k12taskscreativeworkintakestatus"></a>

### `K12TasksCreativeWorkIntakeStatus`

`preparing` / `awaiting_confirmation` / `ready` / `promoted` / `failed` / `cancelled` / `unreadable`

<a id="schema-k12taskscreativeworklist"></a>

### `K12TasksCreativeWorkList`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `items` | array<[K12TasksCreativeWorkDTO](#schema-k12taskscreativeworkdto)> | 是 |

<a id="schema-k12taskscreativeworkpromotionpolicy"></a>

### `K12TasksCreativeWorkPromotionPolicy`

`automatic` / `explicit_commit`

<a id="schema-k12tasksdeliverybatch"></a>

### `K12TasksDeliveryBatch`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `agent_name` | string | 是 |
| `batch_id` | string | 是 |
| `content_digest` | string | 是 |
| `created_at` | integer | 是 |
| `dedupe_key` | string | 是 |
| `object_id` | string | 是 |
| `object_kind` | string | 是 |
| `receipts` | array<[K12TasksDeliveryReceipt](#schema-k12tasksdeliveryreceipt)> / null | 是 |
| `status` | [K12TasksDeliveryBatchStatus](#schema-k12tasksdeliverybatchstatus) | 是 |
| `updated_at` | integer | 是 |

<a id="schema-k12tasksdeliverybatchstatus"></a>

### `K12TasksDeliveryBatchStatus`

`pending` / `sending` / `delivered` / `failed` / `partial_failed` / `outcome_unknown`

<a id="schema-k12tasksdeliveryreceipt"></a>

### `K12TasksDeliveryReceipt`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `agent_name` | string | 是 |
| `attempt` | integer | 是 |
| `batch_id` | string | 可选 |
| `batch_ordinal` | integer | 可选 |
| `binding_id` | string | 是 |
| `created_at` | integer | 是 |
| `dedupe_key` | string | 是 |
| `delivery_id` | string | 是 |
| `external_message_id` | string | 可选 |
| `last_error` | string | 可选 |
| `object_id` | string | 是 |
| `object_kind` | string | 是 |
| `part_digest` | string | 是 |
| `part_kind` | [K12TasksPartKind](#schema-k12taskspartkind) | 是 |
| `part_mime` | string | 可选 |
| `part_ordinal` | integer | 是 |
| `payload_digest` | string | 是 |
| `payload_json` | string | 是 |
| `render_manifest_json` | string | 是 |
| `status` | [K12TasksDeliveryReceiptStatus](#schema-k12tasksdeliveryreceiptstatus) | 是 |
| `target` | [K12TasksDeliveryTarget](#schema-k12tasksdeliverytarget) | 是 |
| `updated_at` | integer | 是 |

<a id="schema-k12tasksdeliveryreceiptstatus"></a>

### `K12TasksDeliveryReceiptStatus`

`pending` / `sending` / `delivered` / `failed` / `outcome_unknown`

<a id="schema-k12tasksdeliverytarget"></a>

### `K12TasksDeliveryTarget`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `chat_id` | string | 是 |
| `instance_id` | string | 可选 |
| `label` | string | 可选 |
| `platform` | string | 是 |

<a id="schema-k12taskseffectivegradingassessment"></a>

### `K12TasksEffectiveGradingAssessment`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `correction` | [K12TasksGradingAssessmentCorrection](#schema-k12tasksgradingassessmentcorrection) / null | 可选 |
| `current` | [K12TasksGradingAssessmentItem](#schema-k12tasksgradingassessmentitem) | 是 |
| `original` | [K12TasksGradingAssessmentItem](#schema-k12tasksgradingassessmentitem) | 是 |

<a id="schema-k12tasksfinalsourcecorrectioninput"></a>

### `K12TasksFinalSourceCorrectionInput`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `accept_duplicate_execution` | boolean (default=false) | 可选 |
| `agent` | string | 是 |
| `artifact_digest` | string | 是 |
| `artifact_id` | string | 是 |
| `idempotency_key` | string | 是 |
| `input_digest` | string | 是 |
| `input_revision` | integer (min=1) | 是 |
| `recovery_of` | string | 可选 |
| `recovery_response_digest` | string | 可选 |

<a id="schema-k12tasksfinalsourcecorrectionresult"></a>

### `K12TasksFinalSourceCorrectionResult`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `artifact` | [K12TasksGradingFinalArtifact](#schema-k12tasksgradingfinalartifact) / null | 可选 |
| `correction_id` | string | 是 |
| `replayed` | boolean | 是 |
| `status` | string | 是 |

<a id="schema-k12tasksgraderesp"></a>

### `K12TasksGradeResp`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `assessment_status` | string | 可选 |
| `badge` | string | 是 |
| `curriculum_unmapped` | array<string> / null | 可选 |
| `error_cause` | string | 可选 |
| `evidence_type` | string | 是 |
| `final_answer_correct` | boolean / null | 可选 |
| `out_of_scope` | boolean | 是 |
| `out_of_scope_kp` | string | 可选 |
| `record_created` | boolean | 是 |
| `record_id` | string | 可选 |
| `solution` | string | 是 |
| `solve_only` | boolean | 是 |
| `verdict` | string | 是 |
| `wrong_step` | string | 可选 |

<a id="schema-k12tasksgradingassessmentcorrection"></a>

### `K12TasksGradingAssessmentCorrection`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `assessment` | [K12TasksGradingAssessmentItem](#schema-k12tasksgradingassessmentitem) | 是 |
| `correction_id` | string | 是 |
| `created_at` | integer | 是 |
| `original_answer_source` | [K12TasksProblemAnswerSource](#schema-k12tasksproblemanswersource) / null | 可选 |
| `original_result_digest` | string | 是 |
| `previous_correction_id` | string | 是 |
| `reason` | string | 是 |
| `revision` | integer | 是 |

<a id="schema-k12tasksgradingassessmentitem"></a>

### `K12TasksGradingAssessmentItem`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `agent_name` | string | 是 |
| `answer_source` | [K12TasksProblemAnswerSource](#schema-k12tasksproblemanswersource) / null | 可选 |
| `attempt_id` | string | 是 |
| `confirmed_version` | integer | 是 |
| `created_at` | integer | 是 |
| `current_disposition` | string | 是 |
| `grade_invocation_id` | string | 可选 |
| `input_digest` | string | 是 |
| `input_revision` | integer | 是 |
| `job_id` | string | 是 |
| `parent_guide_invocation_id` | string | 可选 |
| `problem_id` | string | 是 |
| `projection_created` | boolean | 可选 |
| `projection_record_id` | string | 可选 |
| `projection_status` | string | 是 |
| `published_revision` | integer | 是 |
| `result_digest` | string | 是 |
| `result_json` | string | 是 |
| `solve_invocation_id` | string | 可选 |
| `status` | [K12TasksGradingAssessmentStatus](#schema-k12tasksgradingassessmentstatus) | 是 |
| `structure_version` | integer | 是 |
| `updated_at` | integer | 是 |

<a id="schema-k12tasksgradingassessmentstatus"></a>

### `K12TasksGradingAssessmentStatus`

`correct` / `correct_with_process_issue` / `wrong` / `unanswered` / `answer_unclear` / `blank_solved` / `out_of_scope` / `untrusted`

<a id="schema-k12tasksgradingfinalartifact"></a>

### `K12TasksGradingFinalArtifact`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `agent_name` | string | 是 |
| `annotated_asset_id` | string | 可选 |
| `annotated_digest` | string | 可选 |
| `annotated_mime` | string | 可选 |
| `artifact_digest` | string | 是 |
| `artifact_id` | string | 是 |
| `canonical_markdown` | string | 是 |
| `coverage_status` | [K12TasksGradingFinalArtifactCoverageStatus](#schema-k12tasksgradingfinalartifactcoveragestatus) | 是 |
| `created_at` | integer | 是 |
| `job_id` | string | 是 |
| `ordered_current_digests_json` | string | 是 |
| `original_source_digest` | string | 可选 |
| `published_count` | integer | 是 |
| `skipped_count` | integer | 是 |
| `structure_version` | integer | 是 |
| `summary_invocation_id` | string | 是 |
| `total_count` | integer | 是 |
| `updated_at` | integer | 是 |

<a id="schema-k12tasksgradingfinalartifactcoveragestatus"></a>

### `K12TasksGradingFinalArtifactCoverageStatus`

`complete` / `with_skips` / `general_guidance`

<a id="schema-k12tasksgradingmodelsnapshot"></a>

### `K12TasksGradingModelSnapshot`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `capability` | string | 可选 |
| `capability_receipt_digest` | string | 可选 |
| `config_fingerprint` | string | 可选 |
| `fallback` | string | 可选 |
| `model` | string | 是 |
| `parent_instructions` | [K12TasksAgentInstructionsSnapshot](#schema-k12tasksagentinstructionssnapshot) | 可选 |
| `probe_policy_version` | string | 可选 |
| `provider` | string | 是 |
| `provider_instance_id` | string | 可选 |
| `recognizing_request_policy` | [K12TasksModelRequestPolicySnapshot](#schema-k12tasksmodelrequestpolicysnapshot) | 可选 |
| `route` | string | 是 |
| `timeout_ms` | integer | 可选 |

<a id="schema-k12tasksgradingquestioncorrection"></a>

### `K12TasksGradingQuestionCorrection`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `answer_canonical_markdown` | string | 可选 |
| `answer_state` | `` / `blank` / `present` / `unclear` | 可选 |
| `canonical_markdown` | string | 可选 |
| `confirmed` | boolean | 可选 |
| `index` | integer (default=0) | 可选 |
| `problem_id` | string | 可选 |
| `question` | string | 可选 |
| `student_answer` | string | 可选 |
| `subject` | string | 可选 |

<a id="schema-k12tasksgroundingevidencereceipt"></a>

### `K12TasksGroundingEvidenceReceipt`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `chunk_id` | string | 是 |
| `citation_digest` | string | 是 |
| `document_generation` | integer | 是 |
| `document_id` | string | 是 |
| `logical_page` | integer | 是 |
| `pdf_page` | integer | 是 |
| `query_digest` | string | 是 |
| `source_digest` | string | 是 |
| `source_mode` | string | 可选 |
| `textbook_binding_id` | string | 是 |
| `textbook_manifest_id` | string | 是 |
| `vector_revision_id` | string | 是 |

<a id="schema-k12tasksgroundingsourcerecoveryinput"></a>

### `K12TasksGroundingSourceRecoveryInput`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `agent` | string | 是 |
| `idempotency_key` | string | 是 |
| `job_version` | integer (min=1) | 是 |
| `version` | integer (min=1) | 是 |

<a id="schema-k12tasksgroundingsourcerecoveryresult"></a>

### `K12TasksGroundingSourceRecoveryResult`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `job_id` | string | 是 |
| `recovery_id` | string | 是 |
| `replayed` | boolean | 是 |
| `source_mode` | string | 是 |

<a id="schema-k12taskshomeworkrecognition"></a>

### `K12TasksHomeworkRecognition`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `subject` | string | 是 |
| `questions` | array<[K12TasksRecognizedQuestionDTO](#schema-k12tasksrecognizedquestiondto)> | 是 |

<a id="schema-k12tasksimagetaskaccepted"></a>

### `K12TasksImageTaskAccepted`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `created` | boolean | 是 |
| `dispatch` | [K12TasksPublicImageTaskDispatch](#schema-k12taskspublicimagetaskdispatch) | 是 |

<a id="schema-k12tasksimagetaskbatchaccepted"></a>

### `K12TasksImageTaskBatchAccepted`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `tasks` | array<[K12TasksImageTaskAccepted](#schema-k12tasksimagetaskaccepted)> | 是 |

<a id="schema-k12tasksimagetaskbatcherror"></a>

### `K12TasksImageTaskBatchError`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `error` | string | 是 |
| `failed_index` | integer (min=0) | 是 |
| `tasks` | array<[K12TasksImageTaskAccepted](#schema-k12tasksimagetaskaccepted)> | 是 |

<a id="schema-k12tasksimagetaskcreativeconflictdto"></a>

### `K12TasksImageTaskCreativeConflictDTO`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `canonical_text` | string | 可选 |
| `raw_text` | string | 可选 |
| `reason` | string | 可选 |
| `segment_id` | string | 是 |

<a id="schema-k12tasksimagetaskcreativeprojectiondto"></a>

### `K12TasksImageTaskCreativeProjectionDTO`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `canonical_content` | string | 可选 |
| `canonical_version` | integer | 可选 |
| `commit_required` | boolean / null | 可选 |
| `commit_state` | string | 可选 |
| `conflicts` | array<[K12TasksImageTaskCreativeConflictDTO](#schema-k12tasksimagetaskcreativeconflictdto)> / null | 可选 |
| `entry_kind` | [K12TasksCreativeWorkEntryKind](#schema-k12taskscreativeworkentrykind) | 可选 |
| `intake_id` | string | 是 |
| `kind` | `creative` | 是 |
| `notice` | string | 可选 |
| `outcome` | string | 可选 |
| `promoted_generation_id` | string | 可选 |
| `promoted_work_id` | string | 可选 |
| `promotion_policy` | [K12TasksCreativeWorkPromotionPolicy](#schema-k12taskscreativeworkpromotionpolicy) | 可选 |
| `routing_provenance` | [K12TasksImageTaskRoutingProvenance](#schema-k12tasksimagetaskroutingprovenance) | 可选 |
| `status` | [K12TasksCreativeWorkIntakeStatus](#schema-k12taskscreativeworkintakestatus) | 是 |
| `work` | [K12TasksImageTaskCreativeWorkDTO](#schema-k12tasksimagetaskcreativeworkdto) / null | 可选 |
| `work_type` | string | 是 |

<a id="schema-k12tasksimagetaskcreativeworkdto"></a>

### `K12TasksImageTaskCreativeWorkDTO`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `display_name` | string | 是 |
| `work_id` | string | 是 |

<a id="schema-k12tasksimagetaskhomeworkprojectiondto"></a>

### `K12TasksImageTaskHomeworkProjectionDTO`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `anchor_state` | string | 是 |
| `completed_at` | integer | 可选 |
| `confirmation_state` | string | 是 |
| `final_artifact` | [K12TasksGradingFinalArtifact](#schema-k12tasksgradingfinalartifact) / null | 可选 |
| `grounding_evidence_receipts` | array<[K12TasksGroundingEvidenceReceipt](#schema-k12tasksgroundingevidencereceipt)> | 是 |
| `kind` | `homework` | 是 |
| `problem_grounding_receipts` | array<[K12TasksProblemGroundingReceipt](#schema-k12tasksproblemgroundingreceipt)> | 是 |
| `progressive` | [K12TasksImageTaskProgressiveDTO](#schema-k12tasksimagetaskprogressivedto) | 是 |
| `recognition` | [K12TasksHomeworkRecognition](#schema-k12taskshomeworkrecognition) | 可选 |
| `stage` | string | 是 |

<a id="schema-k12tasksimagetaskintent"></a>

### `K12TasksImageTaskIntent`

`completed_homework` / `blank_worksheet` / `writing` / `artwork` / `unknown`

<a id="schema-k12tasksimagetaskoperationreceipt"></a>

### `K12TasksImageTaskOperationReceipt`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `attempt` | integer | 是 |
| `canonical_input_digest` | string | 是 |
| `execution_kind` | string | 可选 |
| `invocation_id` | string | 是 |
| `model` | string | 可选 |
| `operation` | string | 是 |
| `parent_invocation_id` | string | 可选 |
| `physical_unit` | string | 可选 |
| `provider` | string | 可选 |
| `request_policy` | [K12TasksModelRequestPolicySnapshot](#schema-k12tasksmodelrequestpolicysnapshot) / null | 可选 |
| `request_policy_digest` | string | 可选 |
| `result_digest` | string | 是 |
| `status` | string | 是 |

<a id="schema-k12tasksimagetaskproblemprogressdto"></a>

### `K12TasksImageTaskProblemProgressDTO`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `current_disposition` | string | 是 |
| `input_revision` | integer | 是 |
| `problem_id` | string | 是 |
| `published_revision` | integer | 是 |
| `status` | string | 是 |

<a id="schema-k12tasksimagetaskprogressdto"></a>

### `K12TasksImageTaskProgressDTO`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `operation` | string | 是 |
| `state` | string | 是 |

<a id="schema-k12tasksimagetaskprogressivecoveragedto"></a>

### `K12TasksImageTaskProgressiveCoverageDTO`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `awaiting` | integer | 是 |
| `failed` | integer | 是 |
| `projection_revision` | integer | 是 |
| `published` | integer | 是 |
| `skipped` | integer | 是 |
| `status` | string | 是 |
| `total` | integer | 是 |

<a id="schema-k12tasksimagetaskprogressivedto"></a>

### `K12TasksImageTaskProgressiveDTO`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `coverage` | [K12TasksImageTaskProgressiveCoverageDTO](#schema-k12tasksimagetaskprogressivecoveragedto) | 是 |
| `problem_progress` | array<[K12TasksImageTaskProblemProgressDTO](#schema-k12tasksimagetaskproblemprogressdto)> / null | 是 |
| `snapshot_revision` | integer | 是 |
| `structure_version` | integer | 是 |

<a id="schema-k12tasksimagetaskrecoverables"></a>

### `K12TasksImageTaskRecoverables`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `items` | array<[K12TasksRecoverableImageTask](#schema-k12tasksrecoverableimagetask)> | 是 |

<a id="schema-k12tasksimagetaskresponse"></a>

### `K12TasksImageTaskResponse`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `dispatch` | [K12TasksPublicImageTaskDispatch](#schema-k12taskspublicimagetaskdispatch) | 是 |

<a id="schema-k12tasksimagetaskresult"></a>

### `K12TasksImageTaskResult`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `dispatch_id` | string | 是 |
| `task_intent` | [K12TasksImageTaskIntent](#schema-k12tasksimagetaskintent) | 是 |
| `status` | [K12TasksImageTaskStatus](#schema-k12tasksimagetaskstatus) | 是 |
| `source_digest` | string | 是 |
| `source_attachments` | array<[K12TasksImageTaskSourceAttachmentReceipt](#schema-k12tasksimagetasksourceattachmentreceipt)> | 是 |
| `operation_receipts` | array<[K12TasksImageTaskOperationReceipt](#schema-k12tasksimagetaskoperationreceipt)> | 是 |
| `result` | [K12TasksPhotoResultProjection](#schema-k12tasksphotoresultprojection) / [K12TasksCreativeResultProjection](#schema-k12taskscreativeresultprojection) / null | 是 |
| `failure_kind` | string | 可选 |
| `grounding_evidence_receipts` | array<[K12TasksGroundingEvidenceReceipt](#schema-k12tasksgroundingevidencereceipt)> | 可选 |
| `problem_grounding_receipts` | array<[K12TasksProblemGroundingReceipt](#schema-k12tasksproblemgroundingreceipt)> | 可选 |

<a id="schema-k12tasksimagetaskrouterequest"></a>

### `K12TasksImageTaskRouteRequest`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `model` | string | 可选 |
| `provider` | string | 可选 |
| `selection_source` | `auto` / `explicit` (default=auto) | 可选 |

<a id="schema-k12tasksimagetaskroutingprovenance"></a>

### `K12TasksImageTaskRoutingProvenance`

`model_classified` / `parent_selected`

<a id="schema-k12tasksimagetasksourceattachmentreceipt"></a>

### `K12TasksImageTaskSourceAttachmentReceipt`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `digest` | string | 是 |
| `size_bytes` | integer | 是 |

<a id="schema-k12tasksimagetasksourcekind"></a>

### `K12TasksImageTaskSourceKind`

`desktop` / `api` / `im_direct`

<a id="schema-k12tasksimagetaskstatus"></a>

### `K12TasksImageTaskStatus`

`routing` / `awaiting_confirmation` / `routed` / `failed` / `cancelled`

<a id="schema-k12tasksimagetasktargetdto"></a>

### `K12TasksImageTaskTargetDTO`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `id` | string | 是 |
| `type` | [K12TasksImageTaskTargetType](#schema-k12tasksimagetasktargettype) | 是 |

<a id="schema-k12tasksimagetasktargettype"></a>

### `K12TasksImageTaskTargetType`

`homework_submission` / `creative_work_intake`

<a id="schema-k12tasksimagetaskretryreq"></a>

### `K12TasksImageTaskRetryReq`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `agent` | string；原实例 name | 是 |
| `version` | integer (min=1)；当前 dispatch 版本 | 是 |
| `intent` | string；省略或空为普通重试，仅 `known_local_technical` 可进入受控恢复 | 否 |

<a id="schema-k12tasksimagetaskversionreq"></a>

### `K12TasksImageTaskVersionReq`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `agent` | string | 是 |
| `version` | integer (min=1) | 是 |

<a id="schema-k12tasksmaterialmodelpolicy"></a>

### `K12TasksMaterialModelPolicy`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `model` | [K12TasksGradingModelSnapshot](#schema-k12tasksgradingmodelsnapshot) | 是 |
| `version` | string | 是 |

<a id="schema-k12tasksmaterialpreparationitem"></a>

### `K12TasksMaterialPreparationItem`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `answer` | string | 可选 |
| `asset_id` | string | 可选 |
| `asset_version` | integer | 可选 |
| `block_id` | string | 是 |
| `candidate_id` | string | 是 |
| `line` | integer | 是 |
| `page` | integer | 可选 |
| `reason` | string | 可选 |
| `reference_answer` | string | 可选 |
| `source_warnings` | array<string> / null | 可选 |
| `state` | string | 是 |
| `stem` | string | 是 |

<a id="schema-k12tasksmaterialpreparationlist"></a>

### `K12TasksMaterialPreparationList`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `preparations` | array<[K12TasksMaterialPreparationSummary](#schema-k12tasksmaterialpreparationsummary)> | 是 |

<a id="schema-k12tasksmaterialpreparationsummary"></a>

### `K12TasksMaterialPreparationSummary`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `counts` | object | 是 |
| `counts.ready` | integer (min=0) | 是 |
| `counts.preparing` | integer (min=0) | 是 |
| `counts.needs_review` | integer (min=0) | 是 |
| `counts.failed` | integer (min=0) | 是 |
| `counts.outcome_unknown` | integer (min=0) | 是 |
| `counts.stopped` | integer (min=0) | 是 |
| `document_id` | string | 是 |
| `extraction_complete` | boolean | 是 |
| `items` | array<[K12TasksMaterialPreparationItem](#schema-k12tasksmaterialpreparationitem)> / null | 是 |
| `source_observations` | array<[K12TasksMaterialReferenceObservation](#schema-k12tasksmaterialreferenceobservation)> / null | 可选 |
| `source_revision` | integer | 是 |
| `state` | `not_prepared` / `preparing` / `ready` / `needs_review` / `failed` / `outcome_unknown` / `stopped` | 是 |

<a id="schema-k12tasksmaterialrecoverycommand"></a>

### `K12TasksMaterialRecoveryCommand`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `fingerprint` | string (minLength=1) | 是 |
| `allow_possible_duplicate_charge` | `true` | 是 |

<a id="schema-k12tasksmaterialrecoveryplan"></a>

### `K12TasksMaterialRecoveryPlan`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `document_id` | string | 是 |
| `fingerprint` | string | 是 |
| `input_digest` | string | 是 |
| `may_duplicate_charge` | boolean | 是 |
| `model_policy` | [K12TasksMaterialModelPolicy](#schema-k12tasksmaterialmodelpolicy) | 是 |
| `policy_digest` | string | 是 |
| `reusable` | array<[K12TasksMaterialRecoveryReceipt](#schema-k12tasksmaterialrecoveryreceipt)> / null | 是 |
| `source_digest` | string | 是 |
| `source_revision` | integer | 是 |
| `task_id` | string | 是 |
| `unknown` | [K12TasksMaterialRecoveryReceipt](#schema-k12tasksmaterialrecoveryreceipt) | 是 |

<a id="schema-k12tasksmaterialrecoveryreceipt"></a>

### `K12TasksMaterialRecoveryReceipt`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `attempt` | integer | 是 |
| `invocation_id` | string | 是 |
| `operation` | string | 是 |
| `request_digest` | string | 是 |
| `result_digest` | string | 可选 |

<a id="schema-k12tasksmaterialrecoveryresult"></a>

### `K12TasksMaterialRecoveryResult`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `decision_id` | string | 是 |
| `replacement_invocation_id` | string | 可选 |
| `state` | string | 是 |
| `task_id` | string | 是 |

<a id="schema-k12tasksmaterialreferenceobservation"></a>

### `K12TasksMaterialReferenceObservation`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `candidate_id` | string | 可选 |
| `locations` | array<[K12TasksMaterialSourceLocation](#schema-k12tasksmaterialsourcelocation)> / null | 是 |
| `question_number` | string | 是 |
| `reason` | string | 可选 |
| `review_required` | boolean | 可选 |
| `text` | string | 是 |

<a id="schema-k12tasksmaterialsourcelocation"></a>

### `K12TasksMaterialSourceLocation`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `block_id` | string | 是 |
| `line` | integer | 是 |

<a id="schema-k12tasksmodelrequestpolicysnapshot"></a>

### `K12TasksModelRequestPolicySnapshot`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `policy_version` | string | 是 |
| `reasoning_effort` | string | 是 |
| `stage` | string | 是 |
| `thinking` | string | 是 |

<a id="schema-k12tasksocrriskreason"></a>

### `K12TasksOCRRiskReason`

`fraction` / `decimal_point` / `negative_sign` / `unit` / `erasure` / `evidence_conflict` / `low_confidence` / `unclear_handwriting` / `subject_undetermined` / `canonical_parse_failed`

<a id="schema-k12tasksparentteachingguide"></a>

### `K12TasksParentTeachingGuide`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `answer` | string | 是 |
| `checking_method` | string | 是 |
| `follow_up_questions` | array<string> / null | 是 |
| `full_solution_steps` | array<string> / null | 是 |
| `grade_level_method` | string | 是 |
| `likely_mistakes` | array<string> / null | 是 |
| `parent_teaching_sequence` | array<string> / null | 是 |

<a id="schema-k12taskspartkind"></a>

### `K12TasksPartKind`

`markdown` / `text` / `artifact`

<a id="schema-k12tasksphotoitemdto"></a>

### `K12TasksPhotoItemDTO`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `answer_source` | [K12TasksProblemAnswerSource](#schema-k12tasksproblemanswersource) / null | 可选 |
| `grade` | [K12TasksGradeResp](#schema-k12tasksgraderesp) / null | 可选 |
| `parent_guide` | [K12TasksParentTeachingGuide](#schema-k12tasksparentteachingguide) / null | 可选 |
| `question` | [K12TasksRecognizedQuestionDTO](#schema-k12tasksrecognizedquestiondto) | 是 |
| `result_kind` | string | 是 |
| `reuse` | [K12TasksProblemAssetReuse](#schema-k12tasksproblemassetreuse) / null | 可选 |
| `status` | string | 是 |
| `warning` | string | 可选 |

<a id="schema-k12tasksphotoresult"></a>

### `K12TasksPhotoResult`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `mode` | `grade` / `solve` | 是 |
| `items` | array<[K12TasksPhotoItemDTO](#schema-k12tasksphotoitemdto)> | 是 |
| `task_intent` | `completed_homework` / `blank_worksheet` | 是 |
| `result_surface` | `annotated_homework` / `parent_teaching_guide` | 是 |
| `markdown` | string | 是 |
| `image_warning` | string | 是 |
| `annotated_image` | [K12TasksAnnotatedImage](#schema-k12tasksannotatedimage) | 可选 |

<a id="schema-k12tasksphotoresultprojection"></a>

### `K12TasksPhotoResultProjection`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `kind` | `completed_homework` / `blank_worksheet` | 是 |
| `payload` | [K12TasksPhotoResult](#schema-k12tasksphotoresult) | 是 |

<a id="schema-k12tasksproblemanswersource"></a>

### `K12TasksProblemAnswerSource`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `adoption_id` | string | 可选 |
| `asset_id` | string | 可选 |
| `asset_revision` | integer | 可选 |
| `asset_version` | integer | 可选 |
| `facts_digest` | string | 是 |
| `invocation_id` | string | 可选 |
| `kind` | [K12TasksProblemAnswerSourceKind](#schema-k12tasksproblemanswersourcekind) | 是 |

<a id="schema-k12tasksproblemanswersourcekind"></a>

### `K12TasksProblemAnswerSourceKind`

`model` / `deterministic` / `asset`

<a id="schema-k12tasksproblemassetfeedback"></a>

### `K12TasksProblemAssetFeedback`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `command` | [K12TasksProblemAssetFeedbackCommand](#schema-k12tasksproblemassetfeedbackcommand) | 是 |
| `created_at` | integer | 是 |
| `feedback_id` | string | 是 |
| `last_error` | string | 可选 |
| `outcomes` | array<[K12TasksProblemAssetFeedbackOutcome](#schema-k12tasksproblemassetfeedbackoutcome)> / null | 是 |
| `status` | `pending` / `completed` / `unresolved` / `outcome_unknown` | 是 |
| `targets` | array<[K12TasksProblemAssetFeedbackTarget](#schema-k12tasksproblemassetfeedbacktarget)> / null | 是 |
| `updated_at` | integer | 是 |

<a id="schema-k12tasksproblemassetfeedbackcommand"></a>

### `K12TasksProblemAssetFeedbackCommand`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `agent_name` | string | 是 |
| `dispatch_id` | string | 是 |
| `input_revision` | integer | 是 |
| `job_id` | string | 是 |
| `kind` | string | 是 |
| `owner_id` | string | 是 |
| `problem_id` | string | 是 |
| `reason` | string | 是 |
| `request_id` | string | 是 |
| `result_digest` | string | 是 |

<a id="schema-k12tasksproblemassetfeedbackoutcome"></a>

### `K12TasksProblemAssetFeedbackOutcome`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `correction_id` | string | 可选 |
| `error` | string | 可选 |
| `status` | `corrected` / `unresolved` / `outcome_unknown` | 是 |
| `target` | [K12TasksProblemAssetFeedbackTarget](#schema-k12tasksproblemassetfeedbacktarget) | 是 |

<a id="schema-k12tasksproblemassetfeedbacktarget"></a>

### `K12TasksProblemAssetFeedbackTarget`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `agent_name` | string | 是 |
| `asset_id` | string | 是 |
| `asset_version` | integer | 是 |
| `input_revision` | integer | 是 |
| `job_id` | string | 是 |
| `problem_id` | string | 是 |
| `result_digest` | string | 是 |

<a id="schema-k12tasksproblemassetreuse"></a>

### `K12TasksProblemAssetReuse`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `answer` | string | 是 |
| `document_id` | string | 可选 |
| `page` | integer | 可选 |
| `source_digest` | string | 可选 |
| `source_name` | string | 是 |
| `stem` | string | 是 |

<a id="schema-k12tasksproblemgroundingreceipt"></a>

### `K12TasksProblemGroundingReceipt`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `identity_digest` | string | 是 |
| `operation` | string | 是 |
| `problem_id` | string | 是 |
| `chunk_id` | string | 是 |
| `citation_digest` | string | 是 |
| `document_generation` | integer | 是 |
| `document_id` | string | 是 |
| `logical_page` | integer | 是 |
| `pdf_page` | integer | 是 |
| `query_digest` | string | 是 |
| `source_digest` | string | 是 |
| `source_mode` | string | 可选 |
| `textbook_binding_id` | string | 是 |
| `textbook_manifest_id` | string | 是 |
| `vector_revision_id` | string | 是 |

<a id="schema-k12tasksproblemkind"></a>

### `K12TasksProblemKind`

`standalone` / `compound_parent` / `subproblem`

<a id="schema-k12tasksproblemsourceactionrequest"></a>

### `K12TasksProblemSourceActionRequest`

分支：`correct_text` / `select_region` / `retake` / `skip` / `resume`。

<a id="schema-k12tasksproblemsourceactionresponse"></a>

### `K12TasksProblemSourceActionResponse`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `action` | string | 是 |
| `command_receipt_id` | string | 是 |
| `dispatch_id` | string | 是 |
| `input_revision` | integer | 是 |
| `problem_id` | string | 是 |
| `progressive_snapshot` | [K12TasksProblemSourceProgressiveSnapshot](#schema-k12tasksproblemsourceprogressivesnapshot) | 是 |
| `structure_version` | integer | 是 |

<a id="schema-k12tasksproblemsourceprogress"></a>

### `K12TasksProblemSourceProgress`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `current_disposition` | string | 是 |
| `input_revision` | integer | 是 |
| `page_asset_id` | string | 可选 |
| `problem_id` | string | 是 |
| `published_revision` | integer | 是 |
| `source_height` | integer | 可选 |
| `source_region` | [K12TasksViewcontractSourcePixelRegion](#schema-k12tasksviewcontractsourcepixelregion) / null | 是 |
| `source_width` | integer | 可选 |
| `status` | string | 是 |

<a id="schema-k12tasksproblemsourceprogressivecoverage"></a>

### `K12TasksProblemSourceProgressiveCoverage`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `awaiting` | integer | 是 |
| `failed` | integer | 是 |
| `projection_revision` | integer | 是 |
| `published` | integer | 是 |
| `skipped` | integer | 是 |
| `status` | string | 是 |
| `total` | integer | 是 |

<a id="schema-k12tasksproblemsourceprogressivesnapshot"></a>

### `K12TasksProblemSourceProgressiveSnapshot`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `coverage` | [K12TasksProblemSourceProgressiveCoverage](#schema-k12tasksproblemsourceprogressivecoverage) | 是 |
| `problem_progress` | array<[K12TasksProblemSourceProgress](#schema-k12tasksproblemsourceprogress)> / null | 是 |
| `snapshot_revision` | integer | 是 |
| `structure_version` | integer | 是 |

<a id="schema-k12taskspublicimagetaskdispatch"></a>

### `K12TasksPublicImageTaskDispatch`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `automatic_budget_seconds` | integer | 是 |
| `automatic_deadline_at` | integer | 是 |
| `automatic_remaining_seconds` | integer | 是 |
| `automatic_started_at` | integer | 是 |
| `confirmation_candidates` | array<[K12TasksImageTaskIntent](#schema-k12tasksimagetaskintent)> | 是 |
| `created_at` | integer | 是 |
| `dispatch_id` | string | 是 |
| `failure_kind` | string / null | 可选 |
| `intent_confidence` | number | 是 |
| `intent_evidence` | array<string> | 是 |
| `model_id` | string / null | 是 |
| `operation_deadline_at` | integer | 可选 |
| `progress` | [K12TasksImageTaskProgressDTO](#schema-k12tasksimagetaskprogressdto) | 是 |
| `provider_display_name` | string / null | 是 |
| `retryable` | boolean | 是 |
| `status` | [K12TasksImageTaskStatus](#schema-k12tasksimagetaskstatus) | 是 |
| `target` | [K12TasksImageTaskTargetDTO](#schema-k12tasksimagetasktargetdto) / null | 可选 |
| `target_projection` | [K12TasksImageTaskHomeworkProjectionDTO](#schema-k12tasksimagetaskhomeworkprojectiondto) / [K12TasksImageTaskCreativeProjectionDTO](#schema-k12tasksimagetaskcreativeprojectiondto) | 可选 |
| `task_intent` | [K12TasksImageTaskIntent](#schema-k12tasksimagetaskintent) | 是 |
| `updated_at` | integer | 是 |
| `version` | integer | 是 |

<a id="schema-k12tasksrecognitionrecoveryauthorization"></a>

### `K12TasksRecognitionRecoveryAuthorization`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `agent` | string | 是 |
| `authorization_id` | string | 是 |
| `candidate_exact_set_digest` | string | 是 |
| `created_at` | integer | 是 |
| `dispatch_id` | string | 是 |
| `idempotency_key` | string | 是 |
| `job_id` | string | 是 |
| `new_parent_invocation_id` | string | 是 |
| `new_physical_invocation_id` | string | 是 |
| `new_request_digest` | string | 是 |
| `page_digest` | string | 是 |
| `request_digest` | string | 是 |
| `source_parent_id` | string | 是 |
| `source_physical_invocation_id` | string | 是 |
| `source_plan_digest` | string | 是 |
| `source_request_digest` | string | 是 |
| `source_timeout_ms` | integer | 可选 |
| `timeout_override_ms` | integer | 可选 |

<a id="schema-k12tasksrecognitionrecoveryinput"></a>

### `K12TasksRecognitionRecoveryInput`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `accept_duplicate_execution` | `true` | 是 |
| `agent` | string | 是 |
| `idempotency_key` | string | 是 |
| `job_version` | integer (min=1) | 是 |
| `source_physical_invocation_id` | string | 是 |
| `timeout_override_ms` | `0` / `180000` (default=0) | 可选 |
| `version` | integer (min=1) | 是 |

<a id="schema-k12tasksrecognitionrecoveryresult"></a>

### `K12TasksRecognitionRecoveryResult`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `authorization` | [K12TasksRecognitionRecoveryAuthorization](#schema-k12tasksrecognitionrecoveryauthorization) | 是 |
| `replayed` | boolean | 是 |
| `status` | string | 是 |

<a id="schema-k12tasksrecognizedquestiondto"></a>

### `K12TasksRecognizedQuestionDTO`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `answer_canonical_markdown` | string | 可选 |
| `answer_canonical_valid` | boolean | 是 |
| `answer_raw_transcription` | string | 可选 |
| `answer_state` | [K12TasksAnswerState](#schema-k12tasksanswerstate) | 是 |
| `attempt_id` | string | 可选 |
| `bbox` | [K12TasksBboxDTO](#schema-k12tasksbboxdto) / null | 可选 |
| `canonical_markdown` | string | 是 |
| `canonical_valid` | boolean | 是 |
| `canonical_version` | integer | 是 |
| `confirmation_reasons` | array<[K12TasksOCRRiskReason](#schema-k12tasksocrriskreason)> / null | 可选 |
| `confirmation_required` | boolean | 是 |
| `confirmed_version` | integer | 是 |
| `display_label` | string | 是 |
| `input_digest` | string | 可选 |
| `knowledge_points` | array<string> / null | 是 |
| `page_asset_id` | string | 是 |
| `parent_problem_id` | string | 可选 |
| `problem_id` | string | 是 |
| `problem_kind` | [K12TasksProblemKind](#schema-k12tasksproblemkind) | 是 |
| `question` | string | 是 |
| `raw_transcription` | string | 是 |
| `recognition_confidence` | number / null | 可选 |
| `source_height` | integer | 可选 |
| `source_number_path` | array<string> / null | 是 |
| `source_region` | [K12TasksSourcePixelRegion](#schema-k12taskssourcepixelregion) / null | 可选 |
| `source_section_label` | string | 是 |
| `source_section_path` | array<string> / null | 是 |
| `source_width` | integer | 可选 |
| `student_answer` | string | 是 |
| `subject` | string | 可选 |
| `subproblem_no` | string | 可选 |
| `system_display_label` | string | 是 |
| `system_section_ordinal` | integer | 是 |

<a id="schema-k12tasksrecoverableimagetask"></a>

### `K12TasksRecoverableImageTask`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `attempt_generation` | integer | 是 |
| `dispatch_id` | string | 是 |
| `projection_ready` | boolean | 是 |
| `source_message_id` | string | 是 |
| `source_session_id` | string | 是 |
| `stage` | string | 是 |
| `status` | [K12TasksImageTaskStatus](#schema-k12tasksimagetaskstatus) | 是 |
| `terminal` | boolean | 是 |
| `version` | integer | 是 |

<a id="schema-k12taskssourcepixelregion"></a>

### `K12TasksSourcePixelRegion`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `height` | integer | 是 |
| `width` | integer | 是 |
| `x` | integer | 是 |
| `y` | integer | 是 |

<a id="schema-k12tasksviewcontractsourcepixelregion"></a>

### `K12TasksViewcontractSourcePixelRegion`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `height` | integer | 是 |
| `width` | integer | 是 |
| `x` | integer | 是 |
| `y` | integer | 是 |

<a id="schema-k12tasksworkfeedback"></a>

### `K12TasksWorkFeedback`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `affirmation` | string | 可选 |
| `evidence_refs` | array<string> / null | 是 |
| `feedback_id` | string | 是 |
| `feedback_type` | string | 是 |
| `limitations` | string | 是 |
| `next_step` | string | 可选 |
| `observations` | array<[K12TasksWorkFeedbackObservation](#schema-k12tasksworkfeedbackobservation)> / null | 是 |
| `parent_guidance` | string | 可选 |
| `projection_markdown` | string | 是 |
| `source_snapshot` | [K12TasksWorkFeedbackSourceSnapshot](#schema-k12tasksworkfeedbacksourcesnapshot) | 是 |
| `suggestions` | array<string> / null | 是 |
| `version_id` | string | 是 |

<a id="schema-k12tasksworkfeedbackdto"></a>

### `K12TasksWorkFeedbackDTO`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `affirmation` | string | 是 |
| `evidence_refs` | array<string> / null | 是 |
| `feedback_id` | string | 是 |
| `feedback_type` | string | 是 |
| `limitations` | string | 可选 |
| `next_step` | string | 是 |
| `parent_guidance` | string | 是 |
| `projection_markdown` | string | 可选 |
| `source_snapshot` | [K12TasksWorkFeedbackSourceSnapshot](#schema-k12tasksworkfeedbacksourcesnapshot) | 是 |
| `visible_evidence` | array<string> / null | 是 |

<a id="schema-k12tasksworkfeedbackgenerationdto"></a>

### `K12TasksWorkFeedbackGenerationDTO`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `failure_message` | string | 可选 |
| `feedback` | [K12TasksWorkFeedbackDTO](#schema-k12tasksworkfeedbackdto) / null | 可选 |
| `generation_id` | string | 是 |
| `recovery_state` | string | 可选 |
| `retry_safe` | boolean | 是 |
| `status` | `queued` / `running` / `succeeded` / `failed` | 是 |

<a id="schema-k12tasksworkfeedbackobservation"></a>

### `K12TasksWorkFeedbackObservation`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `dimension` | string | 是 |
| `evidence` | string | 是 |

<a id="schema-k12tasksworkfeedbacksourcesnapshot"></a>

### `K12TasksWorkFeedbackSourceSnapshot`

| 字段 | 类型 / 取值 | 必含键 |
| --- | --- | --- |
| `capability` | string | 是 |
| `method_ref` | string | 是 |
| `source` | string | 是 |
