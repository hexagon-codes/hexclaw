# K12 错题、练习集、打印与每周练习 API

[English](practice.en.md) · [K12 API 总览](../../API.md)

本页覆盖当前源码（包括未提交改动）注册的全部 52 个方法 / 路径组合。请按实际运行服务的发布版本使用；路径已经包含 `/api/k12` 前缀。需启用 K12 场景和记录存储；模型、渲染、投递操作另需对应已配置依赖。

使用所调用 HexClaw 实例的 Bearer 令牌，JSON 写操作设置 `Content-Type: application/json`。示例 ID 是占位说明，应替换为前一响应中的真实 ID。`agent` 选择当前孩子的 K12 Agent 范围，不改变认证身份。部分严格周练命令按计划 ID 解析 agent，body 中反而不能携带 `agent`。

JSON body 上限 1 MiB，必须只含一个 JSON 值；严格命令拒绝未知字段。业务错误返回 `{"error":"..."}`，不要假设所有接口都有 code/message。400 表示解码或已分类输入错误，404 为范围内资源不存在，409 为已分类版本 / 状态 / 摘要冲突，502 为模型 / 渲染 / 目录依赖失败；未分类错误落到各 handler 的具体默认状态。因此部分普通状态校验也可能使用该默认状态。401 来自服务认证边界。

档案、教材与课程进度规则以 [profile-bundle](../../API.md#profile-bundle) 和 [教材 / 课程进度](../../API.md#textbook-and-curriculum-progress) 为准。

## 接口索引

| Method / path | 操作 |
| --- | --- |
| `GET /api/k12/mistakes` | [查询错题与复习状态](#k12practicemistakes) |
| `DELETE /api/k12/mistakes/{record_id}` | [删除错题](#k12practicedeletemistake) |
| `POST /api/k12/mistakes/{record_id}/archive` | [归档错题](#k12practicearchivemistake) |
| `POST /api/k12/mistakes/{record_id}/restore` | [恢复已归档错题](#k12practicerestoremistake) |
| `POST /api/k12/mistakes/{record_id}/practice-generation` | [启动单题变式生成](#k12practicestartpracticegeneration) |
| `GET /api/k12/mistakes/{record_id}/practice-generation` | [读取单题生成投影](#k12practicegetpracticegeneration) |
| `GET /api/k12/mistakes/{record_id}/practice-generation/receipts` | [读取脱敏生成回执](#k12practicegetpracticegenerationreceipts) |
| `POST /api/k12/mistakes/{record_id}/practice-generation/retry` | [重试单题生成](#k12practiceretrypracticegeneration) |
| `POST /api/k12/mistakes/{record_id}/practice-candidate-selection` | [打开候选题选择](#k12practiceopenpracticecandidateselection) |
| `POST /api/k12/practice-candidate-selections/{id}/batches` | [生成下一批候选题](#k12practicegeneratepracticecandidatebatch) |
| `POST /api/k12/practice-candidate-selections/{id}/commit` | [提交所选候选到练习篮](#k12practicecommitpracticecandidateselection) |
| `POST /api/k12/mistakes/{record_id}/suppress` | [不再安排该错题复习](#k12practicesuppressmistakereview) |
| `POST /api/k12/mistakes/{record_id}/restore-review` | [恢复该错题复习](#k12practicerestoremistakereview) |
| `POST /api/k12/mistakes/{record_id}/defer-this-week` | [本周暂不安排该题](#k12practicedefermistakethisweek) |
| `GET /api/k12/practice-sets` | [查询练习集](#k12practicelistpracticesets) |
| `GET /api/k12/practice-sets/{id}` | [读取练习集详情](#k12practicegetpracticeset) |
| `GET /api/k12/practice-sets/{id}/paper` | [读取题卷或答案卷 Markdown](#k12practicegetpracticepaper) |
| `POST /api/k12/practice-sets/{id}/verify` | [更新草稿练习项验证结果](#k12practiceverifypracticeitem) |
| `POST /api/k12/practice-sets/custom-paper` | [按冻结参数生成复习卷](#k12practicegeneratecustompaper) |
| `POST /api/k12/practice-sets/basket/items` | [将一题加入练习篮](#k12practiceaddtobasket) |
| `POST /api/k12/practice-sets/{id}/items/remove` | [移除待打印题目](#k12practiceremovefrombasket) |
| `POST /api/k12/practice-sets/{id}/finalize` | [固化并出卷或发送](#k12practicefinalizepracticeset) |
| `POST /api/k12/practice-sets/{id}/print-jobs` | [准备练习卷原生打印任务](#k12practicepreparepracticeprintjob) |
| `POST /api/k12/print-jobs` | [准备通用产物打印任务](#k12practicepreparegenericprintjob) |
| `POST /api/k12/print-artifacts` | [冻结可下载 PDF 产物](#k12practiceprepareprintableartifact) |
| `GET /api/k12/print-artifacts/{id}/content` | [下载冻结 PDF 字节](#k12practicegetprintableartifactcontent) |
| `GET /api/k12/print-jobs/{id}` | [读取原生打印任务](#k12practicegetpracticeprintjob) |
| `GET /api/k12/print-jobs/{id}/paper` | [读取打印任务冻结源](#k12practicegetpracticeprintjobpaper) |
| `POST /api/k12/print-jobs/{id}/events` | [记录原生打印事件](#k12practicerecordpracticeprintevent) |
| `POST /api/k12/print-jobs/{id}/commit` | [提交原生打印成功回执](#k12practicecommitpracticeprintreceipt) |
| `POST /api/k12/print-jobs/{id}/retry` | [恢复失败或取消的打印任务](#k12practiceretrypracticeprintjob) |
| `POST /api/k12/practice-sets/{id}/submit` | [提交练习回传照片](#k12practicesubmitpracticeset) |
| `POST /api/k12/practice-sets/{id}/grade` | [手动记录逐题结果](#k12practicegradepracticeset) |
| `POST /api/k12/practice-sets/{id}/close` | [关闭已批改练习集](#k12practiceclosepracticeset) |
| `POST /api/k12/practice-sets/{id}/cancel` | [取消草稿练习集](#k12practicecancelpracticeset) |
| `GET /api/k12/weekly-practice/settings` | [读取每周练习设置](#k12practicegetweeklypracticesettings) |
| `POST /api/k12/weekly-practice/plans` | [建立或读取本周计划](#k12practiceensureweeklypracticeplan) |
| `GET /api/k12/weekly-practice/plans/current` | [读取当前周计划](#k12practicegetcurrentweeklypracticeplan) |
| `GET /api/k12/weekly-practice/plans/history` | [分页读取周练历史](#k12practicelistweeklypracticehistory) |
| `GET /api/k12/weekly-practice/snapshots/{id}` | [读取冻结周练快照](#k12practicegetweeklypracticesnapshot) |
| `POST /api/k12/weekly-practice/plans/{id}/prepare-output` | [冻结周练并准备 PDF](#k12practiceprepareweeklypracticeoutput) |
| `POST /api/k12/weekly-practice/snapshots/{id}/send` | [投递冻结周练 PDF](#k12practicesendweeklypracticesnapshot) |
| `POST /api/k12/weekly-practice/snapshots/{id}/attempts` | [提交冻结周练的单题作答](#k12practicesubmitweeklypracticeattempt) |
| `POST /api/k12/weekly-practice/plans/{id}/arithmetic-batches` | [准备指定题数口算批次](#k12practicecreateweeklyarithmeticbatch) |
| `POST /api/k12/weekly-practice/arithmetic-batches/{id}/start` | [开始已准备口算批次](#k12practicestartweeklyarithmeticbatch) |
| `POST /api/k12/weekly-practice/arithmetic-batches/{id}/retry` | [恢复可重试失败口算批次](#k12practiceretryweeklyarithmeticbatch) |
| `POST /api/k12/weekly-practice/arithmetic-batches/{id}/attempts` | [提交口算批次单题答案](#k12practicesubmitweeklyarithmeticattempt) |
| `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/refresh` | [按当前进度刷新教材巩固轨道](#k12practicerefreshweeklytextbooktrack) |
| `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/prepare` | [按明确题数准备教材巩固](#k12practiceprepareweeklytextbooktrack) |
| `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/recovery-attempts` | [追加授权的教材原题验算恢复](#k12practicerecoverweeklytextbooktrack) |
| `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/reinterpretations` | [重新解释既有教材调用回执](#k12practicereinterpretweeklytextbooktrack) |
| `POST /api/k12/weekly-practice/plans/{id}/save-to-practice-set` | [将冻结周练加入练习篮](#k12practicesaveweeklypracticetopracticeset) |

## 调用准备

```bash
export HEXCLAW_BASE_URL=http://127.0.0.1:16060
export HEXCLAW_TOKEN=YOUR_SERVICE_TOKEN
```

以下命令是调用模板，不表示结果未知时应重发，也不能伪造原生打印成功回执。

<a id="k12practicemistakes"></a>

## `GET /api/k12/mistakes`

查询错题与复习状态

total 是存储查询的记录数，按复习状态过滤后的 items 长度可能更小；没有分页参数。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `agent` | query | string；minLength=1 | 是 | K12 Agent 名称 |
| `status` | query | string | 否 | 省略/all 不筛选；scheduled/deferred_this_week/suppressed/mastered 按 review_state 筛选，其他值按记录 status 筛选 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [MistakeList](#schema-k12practicemistakelist) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 500 | `MessageError` | 未分类存储或依赖错误 |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/mistakes?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

实现依据：[mistakes](../../../../scenarios/k12/apihttp/handler.go)。

<a id="k12practicedeletemistake"></a>

## `DELETE /api/k12/mistakes/{record_id}`

删除错题

永久删除指定 agent 的错题；不同于可恢复归档。不存在或非该 agent 返回 404。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |
| `agent` | query | string；minLength=1 | 是 | K12 Agent 名称 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [OK](#schema-k12practiceok) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |

```bash
curl -fS -X DELETE "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

实现依据：[deleteMistake](../../../../scenarios/k12/apihttp/handler.go)。

<a id="k12practicearchivemistake"></a>

## `POST /api/k12/mistakes/{record_id}/archive`

归档错题

可恢复软归档，保留合法恢复快照，不等于掌握。 相同幂等键保持相同版本和内容。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPMistakeArchiveCommandReq](#schema-k12practicehttpmistakearchivecommandreq)；严格解码，未知字段返回 400。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `version` | integer；minimum=0 | 是 | 当前记录 version，必填且不可为负；使用响应中的新 version 做后续恢复 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

本结构未声明的字段不属于公开契约。

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [HTTPMistakeDTO](#schema-k12practicehttpmistakedto) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/archive" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","version":1,"idempotency_key":"archiveMistake-demo-1"}'
```

实现依据：[archiveMistake](../../../../scenarios/k12/apihttp/handler.go)。

<a id="k12practicerestoremistake"></a>

## `POST /api/k12/mistakes/{record_id}/restore`

恢复已归档错题

Undo 和长期恢复共用 CAS 命令；先读取最新 version，不复用旧版本。 相同幂等键保持相同版本和内容。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPMistakeArchiveCommandReq](#schema-k12practicehttpmistakearchivecommandreq)；严格解码，未知字段返回 400。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `version` | integer；minimum=0 | 是 | 当前记录 version，必填且不可为负；使用响应中的新 version 做后续恢复 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

本结构未声明的字段不属于公开契约。

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [HTTPMistakeDTO](#schema-k12practicehttpmistakedto) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/restore" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","version":1,"idempotency_key":"restoreMistake-demo-1"}'
```

实现依据：[restoreMistake](../../../../scenarios/k12/apihttp/handler.go)。

<a id="k12practicestartpracticegeneration"></a>

## `POST /api/k12/mistakes/{record_id}/practice-generation`

启动单题变式生成

202 仅表示已接纳。pending 后继续 GET，joined 且 practice_set_id/practice_item_id 可读才表示成功入篮；failed/hidden 等其他状态不能作为出题成功。grade/textbook 优先使用显式值，省略从孩子档案补齐；教材最终必须非空；provider/model 必须同时填写或同时省略。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPSinglePracticeGenerationReq](#schema-k12practicehttpsinglepracticegenerationreq)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `grade` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `textbook` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `difficulty` | string (`same`, `easier`, `harder`) | 否 | 省略默认 same |
| `provider` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `model` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_session` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 202 | [SinglePracticeGenerationView](#schema-k12practicesinglepracticegenerationview) | 已接纳；须读取任务终态 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 503 | `MessageError` | 依赖不可用 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/practice-generation" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"variant-demo-1","difficulty":"same"}'
```

实现依据：[startPracticeGeneration](../../../../scenarios/k12/apihttp/handler.go)。

<a id="k12practicegetpracticegeneration"></a>

## `GET /api/k12/mistakes/{record_id}/practice-generation`

读取单题生成投影

只读当前单题任务投影，成功 HTTP 为 200；该 GET 不接收 grade/textbook/provider/model，也不启动或重发生成。pending 时继续查询，joined 且 practice_set_id/practice_item_id 可读才表示成功入篮；failed/hidden 等其他状态不能作为出题成功。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |
| `agent` | query | string；minLength=1 | 是 | K12 Agent 名称 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [SinglePracticeGenerationView](#schema-k12practicesinglepracticegenerationview) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/practice-generation?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

实现依据：[getPracticeGeneration](../../../../scenarios/k12/apihttp/handler.go)。

<a id="k12practicegetpracticegenerationreceipts"></a>

## `GET /api/k12/mistakes/{record_id}/practice-generation/receipts`

读取脱敏生成回执

只读持久回执摘要，不含凭据。云端校验 owner→agent；无权限折叠为 404，无法取得主体为 401。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |
| `agent` | query | string；minLength=1 | 是 | K12 Agent 名称 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [PracticeGenerationReceiptView](#schema-k12practicepracticegenerationreceiptview) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/practice-generation/receipts?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

实现依据：[getPracticeGenerationReceipts](../../../../scenarios/k12/apihttp/handler.go)。

<a id="k12practiceretrypracticegeneration"></a>

## `POST /api/k12/mistakes/{record_id}/practice-generation/retry`

重试单题生成

只对未退休的 failed 原任务按冻结快照重试；若存在 sent/outcome_unknown 物理调用则返回 409，需要先核对。原错题隐藏状态可直接返回 hidden。命令不接受新教材、难度或路由参数，仍须 GET 验证 joined。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPAgentOnlyReq](#schema-k12practicehttpagentonlyreq)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 202 | [SinglePracticeGenerationView](#schema-k12practicesinglepracticegenerationview) | 已接纳；须读取任务终态 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 503 | `MessageError` | 依赖不可用 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/practice-generation/retry" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo"}'
```

实现依据：[retryPracticeGeneration](../../../../scenarios/k12/apihttp/handler.go)。

<a id="k12practiceopenpracticecandidateselection"></a>

## `POST /api/k12/mistakes/{record_id}/practice-candidate-selection`

打开候选题选择

建立 open 选择会话及原题候选，目标为孩子唯一待打印篮；不等于提交装篮。档案默认、教材和 provider/model 约束同单题生成。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPPracticeCandidateOpenReq](#schema-k12practicehttppracticecandidateopenreq)；严格解码，未知字段返回 400。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `grade` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `textbook` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `provider` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `model` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_session` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [PracticeCandidateSelection](#schema-k12practicepracticecandidateselection) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/practice-candidate-selection" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"selection-demo-1"}'
```

实现依据：[openPracticeCandidateSelection](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go)。

<a id="k12practicegeneratepracticecandidatebatch"></a>

## `POST /api/k12/practice-candidate-selections/{id}/batches`

生成下一批候选题

仅 open 选择可生成；revision 必须与选择会话一致，每批最多生成 3 个变式候选。候选保留各自 ready/failed/already_in_set 状态；候选可读不代表已经装篮。provider/model 成对指定。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPPracticeCandidateBatchReq](#schema-k12practicehttppracticecandidatebatchreq)；严格解码，未知字段返回 400。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `revision` | integer；minimum=1 | 是 | 当前聚合修订，用于对应 revision/expected_revision CAS |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `provider` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `model` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [PracticeCandidateSelection](#schema-k12practicepracticecandidateselection) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-candidate-selections/resource-demo-1/batches" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","revision":1,"idempotency_key":"candidate-batch-demo-1"}'
```

实现依据：[generatePracticeCandidateBatch](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go)。

<a id="k12practicecommitpracticecandidateselection"></a>

## `POST /api/k12/practice-candidate-selections/{id}/commit`

提交所选候选到练习篮

candidate_ids 必须非空、无空 ID 和重复 ID，全部属于当前选择；CAS 和装篮去重同一事务。响应 added_count/already_present/replayed 区分新增与重放。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPPracticeCandidateCommitReq](#schema-k12practicehttppracticecandidatecommitreq)；严格解码，未知字段返回 400。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `revision` | integer；minimum=1 | 是 | 当前聚合修订，用于对应 revision/expected_revision CAS |
| `candidate_ids` | array string；minLength=1；minItems=1; uniqueItems=True | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

本结构未声明的字段不属于公开契约。

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [StoragePracticeCandidateCommitReceipt](#schema-k12practicestoragepracticecandidatecommitreceipt) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-candidate-selections/resource-demo-1/commit" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","revision":1,"candidate_ids":["candidate-demo-1"],"idempotency_key":"candidate-commit-demo-1"}'
```

实现依据：[commitPracticeCandidateSelection](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go)。

<a id="k12practicesuppressmistakereview"></a>

## `POST /api/k12/mistakes/{record_id}/suppress`

不再安排该错题复习

设置 suppressed，不删除原错题，也不判为掌握。 version 使用当前错题版本，幂等键绑定同一命令摘要。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPMistakeReviewCommandReq](#schema-k12practicehttpmistakereviewcommandreq)；严格解码，未知字段返回 400。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `version` | integer；minimum=0 | 是 | 当前记录或任务乐观锁版本 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `plan_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `plan_revision` | integer | 否 | 快照所绑定的计划修订或请求的当前计划 CAS |
| `weekly_item_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `iso_year` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `iso_week` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [HTTPMistakeDTO](#schema-k12practicehttpmistakedto) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/suppress" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","version":1,"idempotency_key":"suppressMistakeReview-demo-1"}'
```

实现依据：[suppressMistakeReview](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go)。

<a id="k12practicerestoremistakereview"></a>

## `POST /api/k12/mistakes/{record_id}/restore-review`

恢复该错题复习

恢复复习安排；与恢复已归档记录 /restore 是不同命令。 version 使用当前错题版本，幂等键绑定同一命令摘要。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPMistakeReviewCommandReq](#schema-k12practicehttpmistakereviewcommandreq)；严格解码，未知字段返回 400。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `version` | integer；minimum=0 | 是 | 当前记录或任务乐观锁版本 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `plan_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `plan_revision` | integer | 否 | 快照所绑定的计划修订或请求的当前计划 CAS |
| `weekly_item_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `iso_year` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `iso_week` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [HTTPMistakeDTO](#schema-k12practicehttpmistakedto) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/restore-review" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","version":1,"idempotency_key":"restoreMistakeReview-demo-1"}'
```

实现依据：[restoreMistakeReview](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go)。

<a id="k12practicedefermistakethisweek"></a>

## `POST /api/k12/mistakes/{record_id}/defer-this-week`

本周暂不安排该题

iso_year/iso_week 必填，周为 1–53；仅本周延期。若提供 plan_id，plan_revision 和 weekly_item_id 必须与该周练中的来源题一致。 version 使用当前错题版本，幂等键绑定同一命令摘要。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[DeferMistakeRequest](#schema-k12practicedefermistakerequest)；严格解码，未知字段返回 400。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `version` | integer；minimum=0 | 是 | 当前记录或任务乐观锁版本 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `plan_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `plan_revision` | integer | 否 | 快照所绑定的计划修订或请求的当前计划 CAS |
| `weekly_item_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `iso_year` | integer；minimum=1 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `iso_week` | integer；minimum=1; maximum=53 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [StorageMistakeReviewCommandResult](#schema-k12practicestoragemistakereviewcommandresult) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/defer-this-week" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","version":1,"idempotency_key":"deferMistakeThisWeek-demo-1","iso_year":2026,"iso_week":41}'
```

实现依据：[deferMistakeThisWeek](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go)。

<a id="k12practicelistpracticesets"></a>

## `GET /api/k12/practice-sets`

查询练习集

返回练习集和可见就绪题；生成尚未 ready 的占位项不进入公开 items。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `agent` | query | string；minLength=1 | 是 | K12 Agent 名称 |
| `status` | query | string (`draft`, `confirmed`, `assigned`, `submitted`, `graded`, `closed`, `cancelled`) | 否 | 省略则不筛选 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [SetList](#schema-k12practicesetlist) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 500 | `MessageError` | 未分类存储或依赖错误 |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/practice-sets?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

实现依据：[listPracticeSets](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practicegetpracticeset"></a>

## `GET /api/k12/practice-sets/{id}`

读取练习集详情

返回 item/result_correct、回传照片及复批投影。result_correct 缺省或 null 表示尚无结论，不能按 false 处理。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |
| `agent` | query | string；minLength=1 | 是 | K12 Agent 名称 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

实现依据：[getPracticeSet](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practicegetpracticepaper"></a>

## `GET /api/k12/practice-sets/{id}/paper`

读取题卷或答案卷 Markdown

draft 返回 preview=true、无正式卷号；固化后读正式卷。返回 JSON Markdown，不是 PDF 或已打印回执。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |
| `agent` | query | string；minLength=1 | 是 | K12 Agent 名称 |
| `kind` | query | string (`question`, `answer`) | 否 | 默认 question |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [Paper](#schema-k12practicepaper) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/paper?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

实现依据：[getPracticePaper](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practiceverifypracticeitem"></a>

## `POST /api/k12/practice-sets/{id}/verify`

更新草稿练习项验证结果

只允许 draft；verified 需非空 evidence 且该学科验证器实际支持验证。此接口不是孩子作答批改或掌握确认。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPVerifyItemReq](#schema-k12practicehttpverifyitemreq)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `item_id` | string | 是 | 当前练习项 ID；不得用来源错题 ID 替代 |
| `status` | string (`pending`, `verified`, `needs_review`, `rejected`, `stale`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `evidence` | string | 否 | verified 时必须非空；其他状态可空 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/verify" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","item_id":"item-demo-1","status":"needs_review","evidence":"Pending independent verification"}'
```

实现依据：[verifyPracticeItem](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practicegeneratecustompaper"></a>

## `POST /api/k12/practice-sets/custom-paper`

按冻结参数生成复习卷

后端完成生成、验证、去重和原子装篮。all/5/10 为总题数选择，per_source 为每来源变式 1–3；grade/textbook 可从档案补齐，provider/model 成对。同幂等键改冻结参数返回 400；当前同步成功响应 status=committed。需检查逐题验证、set.items 与 added，不将 job ID 当作可打印产物。

JSON body：[HTTPCustomPaperReq](#schema-k12practicehttpcustompaperreq)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `scope` | string (`week`, `unmastered`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `total` | string (`all`, `5`, `10`) / integer (`5`, `10`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `per_source` | integer；minimum=1; maximum=3 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `difficulty` | string (`same`, `easier`, `harder`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `textbook` | string | 否 | 显式值或档案默认，最终必须非空 |
| `grade` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `provider` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `model` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_session` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [CustomPaperResponse](#schema-k12practicecustompaperresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/custom-paper" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"custom-paper-demo-1","scope":"unmastered","total":5,"per_source":1,"difficulty":"same"}'
```

实现依据：[generateCustomPaper](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practiceaddtobasket"></a>

## `POST /api/k12/practice-sets/basket/items`

将一题加入练习篮

单孩子单 draft 篮，按内容去重；未给 item_id 时服务端生成。subject 只接受数学/语文/英语/科学/信息科技或空，不接受英文别名或美术。仅上述 item 输入字段参与装篮，DTO 的卷序、回传和结果字段不会写入。verification_status 缺省为 pending；调用方提供状态及 verification_evidence，该命令不执行独立验算，verified 受对应学科支持约束。

JSON body：[HTTPAddToBasketReq](#schema-k12practicehttpaddtobasketreq)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `source_session` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `item` | [BasketItemRequest](#schema-k12practicebasketitemrequest) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [BasketAddResponse](#schema-k12practicebasketaddresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/basket/items" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","item":{"question_markdown":"计算 2 + 3","subject":"数学","verification_status":"pending"}}'
```

实现依据：[addToBasket](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practiceremovefrombasket"></a>

## `POST /api/k12/practice-sets/{id}/items/remove`

移除待打印题目

仅可编辑的 draft 篮可移除；固化卷不能通过此命令改题。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPRemoveFromBasketReq](#schema-k12practicehttpremovefrombasketreq)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `item_id` | string | 是 | 当前练习项 ID；不得用来源错题 ID 替代 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/items/remove" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","item_id":"item-demo-1"}'
```

实现依据：[removeFromBasket](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practicefinalizepracticeset"></a>

## `POST /api/k12/practice-sets/{id}/finalize`

固化并出卷或发送

跳过非 verified 项，至少一题可发布才固化为 assigned；via=send 由服务端解析全部有效直连 IM 目标，返回投递 batch 应逐目标核对回执。投递未开通返回 501，无有效直连绑定返回 409。via=print 是出卷命令，HTTP 成功不证明原生打印成功；实际打印优先两阶段 print-jobs→commit 回执链。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPFinalizeReq](#schema-k12practicehttpfinalizereq)；严格解码，未知字段返回 400。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `via` | string (`print`, `send`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [FinalizeResponse](#schema-k12practicefinalizeresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 501 | `MessageError` | 手机投递未开通 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/finalize" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","via":"print"}'
```

实现依据：[finalizePracticeSet](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practicepreparepracticeprintjob"></a>

## `POST /api/k12/practice-sets/{id}/print-jobs`

准备练习卷原生打印任务

第一阶段冻结来源、预占卷号和产物，练习集仍为 draft。原生 printed 回执提交才在同一事务固化练习集。相同命令 replay=true；取消/失败不算 printed。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPPreparePracticePrintReq](#schema-k12practicehttppreparepracticeprintreq)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `artifact_kind` | string (`question`, `answer`) | 否 | 省略默认 question |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 201 | [PrintJobPrepareResponse](#schema-k12practiceprintjobprepareresponse) | 首次创建 |
| 200 | [PrintJobPrepareResponse](#schema-k12practiceprintjobprepareresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/print-jobs" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"print-demo-1","artifact_kind":"question"}'
```

实现依据：[preparePracticePrintJob](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practicepreparegenericprintjob"></a>

## `POST /api/k12/print-jobs`

准备通用产物打印任务

artifact_id 与 canonical 输入严格二选一：artifact_id 引用既有冻结产物；canonical 需 source_kind/source_ref/title/canonical_markdown。二者均需 agent/idempotency_key；不直接改变源业务状态。gprint- ID 通过同一 /print-jobs 读取/事件/commit/retry。

JSON body：[GenericPrintRequest](#schema-k12practicegenericprintrequest)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `idempotency_key` | string；minLength=1 | 是 | 命令键，UTF-8 字节数不超过 512 |
| `artifact_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_kind` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_ref` | string | 否 | 源引用，UTF-8 字节数不超过 512 |
| `title` | string | 否 | 标题，UTF-8 字节数不超过 256 |
| `canonical_markdown` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

分支与条件约束：

```json
{
  "oneOf": [
    {
      "required": [
        "artifact_id"
      ],
      "properties": {
        "artifact_id": {
          "minLength": 1
        },
        "source_kind": {
          "const": ""
        },
        "source_ref": {
          "const": ""
        },
        "title": {
          "const": ""
        },
        "canonical_markdown": {
          "const": ""
        }
      }
    },
    {
      "required": [
        "source_kind",
        "source_ref",
        "title",
        "canonical_markdown"
      ],
      "properties": {
        "artifact_id": {
          "const": ""
        },
        "source_kind": {
          "type": "string",
          "enum": [
            "tutoring_tips",
            "creative_observation_card",
            "practice_question",
            "practice_answer",
            "grading_final_artifact",
            "weekly_practice_snapshot",
            "learning_archive"
          ]
        },
        "source_ref": {
          "type": "string",
          "minLength": 1,
          "description": "源引用，UTF-8 字节数不超过 512 / Source reference, at most 512 UTF-8 bytes"
        },
        "title": {
          "type": "string",
          "minLength": 1,
          "description": "标题，UTF-8 字节数不超过 256 / Title, at most 256 UTF-8 bytes"
        },
        "canonical_markdown": {
          "type": "string",
          "minLength": 1
        }
      }
    }
  ]
}
```

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 201 | [PrintJobPrepareResponse](#schema-k12practiceprintjobprepareresponse) | 首次创建 |
| 200 | [PrintJobPrepareResponse](#schema-k12practiceprintjobprepareresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/print-jobs" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"generic-print-demo-1","artifact_id":"artifact-demo-1"}'
```

实现依据：[prepareGenericPrintJob](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practiceprepareprintableartifact"></a>

## `POST /api/k12/print-artifacts`

冻结可下载 PDF 产物

canonical 输入与 final_artifact_id+final_artifact_digest 二选一；最终批注产物模式还需 title，摘要必须匹配当前冻结产物。无需 idempotency_key，产物按内容身份去重。响应是 PDF 元信息；随后 content 获取实际字节。JSON HTTP 请求体上限 1 MiB，即使渲染层文本边界更大。

JSON body：[PrintableArtifactRequest](#schema-k12practiceprintableartifactrequest)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_kind` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_ref` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `title` | string；minLength=1 | 是 | 标题，UTF-8 字节数不超过 256 |
| `canonical_markdown` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `final_artifact_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `final_artifact_digest` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

分支与条件约束：

```json
{
  "oneOf": [
    {
      "required": [
        "source_kind",
        "source_ref",
        "canonical_markdown"
      ],
      "properties": {
        "final_artifact_id": {
          "const": ""
        },
        "final_artifact_digest": {
          "const": ""
        },
        "source_kind": {
          "type": "string",
          "enum": [
            "tutoring_tips",
            "creative_observation_card",
            "practice_question",
            "practice_answer",
            "grading_final_artifact",
            "weekly_practice_snapshot",
            "learning_archive"
          ]
        },
        "source_ref": {
          "type": "string",
          "minLength": 1,
          "description": "源引用，UTF-8 字节数不超过 512 / Source reference, at most 512 UTF-8 bytes"
        },
        "canonical_markdown": {
          "type": "string",
          "minLength": 1
        }
      }
    },
    {
      "required": [
        "final_artifact_id",
        "final_artifact_digest"
      ],
      "properties": {
        "source_kind": {
          "const": ""
        },
        "source_ref": {
          "const": ""
        },
        "canonical_markdown": {
          "const": ""
        },
        "final_artifact_id": {
          "type": "string",
          "minLength": 1
        },
        "final_artifact_digest": {
          "type": "string",
          "minLength": 1
        }
      }
    }
  ]
}
```

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 201 | [ArtifactPrepareResponse](#schema-k12practiceartifactprepareresponse) | 首次创建 |
| 200 | [ArtifactPrepareResponse](#schema-k12practiceartifactprepareresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/print-artifacts" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","source_kind":"tutoring_tips","source_ref":"tips-demo-1","title":"辅导要点","canonical_markdown":"# 辅导要点\n\n先核对已知条件。"}'
```

实现依据：[preparePrintableArtifact](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practicegetprintableartifactcontent"></a>

## `GET /api/k12/print-artifacts/{id}/content`

下载冻结 PDF 字节

Content-Type 为冻结渲染类型（PDF）；X-Content-SHA256 为字节摘要，ETag 为带引号摘要。不接受 kind，也不创建打印任务；handler 未实现条件 GET/304。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |
| `agent` | query | string；minLength=1 | 是 | K12 Agent 名称 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | string | 冻结 PDF 字节；响应头带字节 SHA-256 和 ETag |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/print-artifacts/resource-demo-1/content?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -o frozen-print-demo.pdf
```

实现依据：[getPrintableArtifactContent](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practicegetpracticeprintjob"></a>

## `GET /api/k12/print-jobs/{id}`

读取原生打印任务

按 ID 前缀读取练习卷或 gprint- 通用任务；status=printed 且有效 native receipt 才代表成功，submitted 不等于 printed。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |
| `agent` | query | string；minLength=1 | 是 | K12 Agent 名称 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [PrintJobResponse](#schema-k12practiceprintjobresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/print-jobs/resource-demo-1?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

实现依据：[getPracticePrintJob](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practicegetpracticeprintjobpaper"></a>

## `GET /api/k12/print-jobs/{id}/paper`

读取打印任务冻结源

返回来源摘要和 Markdown，与准备阶段冻结源一致；练习卷请求可按冻结来源惰性补建对应 question/answer PDF。通用任务响应含 source_kind/source_ref，不含练习卷 kind/paper_no。PDF 下载另用 print-artifacts content。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |
| `agent` | query | string；minLength=1 | 是 | K12 Agent 名称 |
| `kind` | query | string (`question`, `answer`) | 否 | 仅练习卷生效，省略为 question；通用任务忽略 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [PrintPaper](#schema-k12practiceprintpaper) / [GenericPrintPaper](#schema-k12practicegenericprintpaper) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/print-jobs/resource-demo-1/paper?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

实现依据：[getPracticePrintJobPaper](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practicerecordpracticeprintevent"></a>

## `POST /api/k12/print-jobs/{id}/events`

记录原生打印事件

dialog_open/submitted/cancelled/failed/outcome_unknown 持久化状态；failed/outcome_unknown 必须给 failure_kind。printed 需 native_job_id/native_receipt_id 和非空对象 printer_snapshot，并走同一事务成功边界。结果未知不能重试打印。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPPracticePrintEventReq](#schema-k12practicehttppracticeprinteventreq)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `status` | string (`dialog_open`, `submitted`, `printed`, `cancelled`, `failed`, `outcome_unknown`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `native_job_id` | string | 否 | 系统打印任务身份；结果未知时用原身份核对 |
| `native_receipt_id` | string | 否 | 系统打印成功回执身份，禁止以接纳/对话框状态代替 |
| `printer_snapshot` | object | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `failure_kind` | string | 否 | 失败分类；失败或结果未知事件必需 |
| `failure_detail` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [PrintJobResponse](#schema-k12practiceprintjobresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/print-jobs/resource-demo-1/events" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","status":"dialog_open"}'
```

实现依据：[recordPracticePrintEvent](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practicecommitpracticeprintreceipt"></a>

## `POST /api/k12/print-jobs/{id}/commit`

提交原生打印成功回执

唯一 printed 成功边界；完全相同 native receipt 可幂等重放，练习篮固化与任务 printed 同 SQLite 事务。不同 receipt/来源版本冲突不得当作成功；outcome_unknown 只允许同 native_job_id 的明确核对回执。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPPracticePrintCommitReq](#schema-k12practicehttppracticeprintcommitreq)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `native_job_id` | string | 是 | 系统打印任务身份；结果未知时用原身份核对 |
| `native_receipt_id` | string | 是 | 系统打印成功回执身份，禁止以接纳/对话框状态代替 |
| `printer_snapshot` | object；minProperties=1 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [PrintJobResponse](#schema-k12practiceprintjobresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/print-jobs/resource-demo-1/commit" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","native_job_id":"native-job-demo-1","native_receipt_id":"native-receipt-demo-1","printer_snapshot":{"name":"Demo printer"}}'
```

实现依据：[commitPracticePrintReceipt](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practiceretrypracticeprintjob"></a>

## `POST /api/k12/print-jobs/{id}/retry`

恢复失败或取消的打印任务

仅 cancelled/failed 可普通重试，保留原冻结内容和卷号；outcome_unknown 必须核对而非重发。练习任务要求篮仍为原 draft 和原版本；通用任务最多 3 次尝试。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPAgentOnlyReq](#schema-k12practicehttpagentonlyreq)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [PrintJobResponse](#schema-k12practiceprintjobresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/print-jobs/resource-demo-1/retry" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo"}'
```

实现依据：[retryPracticePrintJob](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practicesubmitpracticeset"></a>

## `POST /api/k12/practice-sets/{id}/submit`

提交练习回传照片

return_id 为该照片回传幂等身份；同一 asset 使用现有素材 ID。auto_match=true 与非空 item_ids 严格二选一，空覆盖不代表整卷。HTTP 200 表示接纳，异步复批需继续读取 return_assets 中终态、annotated_asset_id/result_markdown。只对照片实际覆盖且可可靠判定的题积累一次复习证据，重放不重复推进。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPSubmitReturnReq](#schema-k12practicehttpsubmitreturnreq)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `return_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `asset_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `item_ids` | array string；minLength=1 | 否 | 本次覆盖的练习项 ID；空不代表整卷 |
| `auto_match` | boolean；default=False | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

分支与条件约束：

```json
{
  "oneOf": [
    {
      "properties": {
        "auto_match": {
          "const": true
        },
        "item_ids": {
          "maxItems": 0
        }
      },
      "required": [
        "auto_match"
      ]
    },
    {
      "properties": {
        "auto_match": {
          "const": false
        },
        "item_ids": {
          "minItems": 1
        }
      },
      "required": [
        "item_ids"
      ]
    }
  ]
}
```

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/submit" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","return_id":"return-demo-1","asset_id":"asset-demo-1","auto_match":true}'
```

实现依据：[submitPracticeSet](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practicegradepracticeset"></a>

## `POST /api/k12/practice-sets/{id}/grade`

手动记录逐题结果

results=[{item_id,correct}] 只写 human_confirmed，可推进复习间隔但不构成系统掌握证据。省略或空 results 保留旧整卷推进行为，不联动错题；新调用应始终提供逐题 results。逐项 correct 为布尔值，当前解码省略它时取 false，不代表“尚无结论”，调用方应始终明确提交。照片系统复批由内部协调器处理。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPGradeResultsReq](#schema-k12practicehttpgraderesultsreq)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `results` | array / null object | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/grade" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","results":[{"item_id":"item-demo-1","correct":true}]}'
```

实现依据：[gradePracticeSet](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practiceclosepracticeset"></a>

## `POST /api/k12/practice-sets/{id}/close`

关闭已批改练习集

仅 graded→closed；reason 是 query 参数，不是 body 字段。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |
| `reason` | query | string (`manual`, `semester`) | 否 | 默认 manual |

JSON body：[HTTPAgentOnlyReq](#schema-k12practicehttpagentonlyreq)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/close" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo"}'
```

实现依据：[closePracticeSet](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practicecancelpracticeset"></a>

## `POST /api/k12/practice-sets/{id}/cancel`

取消草稿练习集

仅 draft/confirmed→cancelled，不删除已发布或已作答卷。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPAgentOnlyReq](#schema-k12practicehttpagentonlyreq)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/cancel" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo"}'
```

实现依据：[cancelPracticeSet](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="k12practicegetweeklypracticesettings"></a>

## `GET /api/k12/weekly-practice/settings`

读取每周练习设置

默认 Asia/Shanghai、due_review_enabled=true、textbook_consolidation_enabled=false、tier=standard、arithmetic_warmup_enabled=false、arithmetic_minutes=2。该读接口不修改设置；修改通过 profile-bundle 原子命令。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `agent` | query | string；minLength=1 | 是 | K12 Agent 名称 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [WeeklyPracticeSettings](#schema-k12practiceweeklypracticesettings) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/weekly-practice/settings?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

实现依据：[getWeeklyPracticeSettings](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="k12practiceensureweeklypracticeplan"></a>

## `POST /api/k12/weekly-practice/plans`

建立或读取本周计划

使用服务端当前时间和设置时区计算 ISO 周，命令不接受日期或题数。相同 agent/周/时区读取既有计划；完成边界归档。冻结计划不会因为新建议或课程进度自动重新生成；补充轨道使用显式 prepare。

JSON body：[HTTPWeeklyPlanCommandRequest](#schema-k12practicehttpweeklyplancommandrequest)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 201 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | 首次创建 |
| 200 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"weekly-plan-demo-1"}'
```

实现依据：[ensureWeeklyPracticePlan](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="k12practicegetcurrentweeklypracticeplan"></a>

## `GET /api/k12/weekly-practice/plans/current`

读取当前周计划

无当前计划返回 200 {"plan":null}，不隐式创建新题；读操作会按服务端时间协调周边界状态。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `agent` | query | string；minLength=1 | 是 | K12 Agent 名称 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [CurrentPlanResponse](#schema-k12practicecurrentplanresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/current?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

实现依据：[getCurrentWeeklyPracticePlan](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="k12practicelistweeklypracticehistory"></a>

## `GET /api/k12/weekly-practice/plans/history`

分页读取周练历史

只返回归档摘要，题目和冻结内容通过 snapshot 读取；不要自行构造分页游标。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `agent` | query | string；minLength=1 | 是 | K12 Agent 名称 |
| `limit` | query | integer；default=20; minimum=1; maximum=100 | 否 | 默认 20，范围 1–100 |
| `cursor` | query | string | 否 | 原样使用前一页 next_cursor；null 表示无下一页 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [WeeklyHistoryResponse](#schema-k12practiceweeklyhistoryresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/history?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

实现依据：[listWeeklyPracticeHistory](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="k12practicegetweeklypracticesnapshot"></a>

## `GET /api/k12/weekly-practice/snapshots/{id}`

读取冻结周练快照

快照记录 plan_revision、题目、来源/验证证据及摘要，历史快照不会随当前教材建议变动。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |
| `agent` | query | string；minLength=1 | 是 | K12 Agent 名称 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [WeeklyPracticeSnapshot](#schema-k12practiceweeklypracticesnapshot) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/weekly-practice/snapshots/resource-demo-1?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

实现依据：[getWeeklyPracticeSnapshot](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="k12practiceprepareweeklypracticeoutput"></a>

## `POST /api/k12/weekly-practice/plans/{id}/prepare-output`

冻结周练并准备 PDF

CAS 校验计划 revision，将 draft 冻结为快照并返回 PDF 元信息；已冻结的相同版本可重放既有产物，不重生成题目。下载实际 PDF 使用 artifact_id 的 content；HTTP 成功不等于打印或投递。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPWeeklyExpectedRevisionRequest](#schema-k12practicehttpweeklyexpectedrevisionrequest)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `expected_revision` | integer；minimum=1 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 201 | [WeeklyOutputResponse](#schema-k12practiceweeklyoutputresponse) | 首次创建 |
| 200 | [WeeklyOutputResponse](#schema-k12practiceweeklyoutputresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/resource-demo-1/prepare-output" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","expected_revision":1,"idempotency_key":"weekly-output-demo-1"}'
```

实现依据：[prepareWeeklyPracticeOutput](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="k12practicesendweeklypracticesnapshot"></a>

## `POST /api/k12/weekly-practice/snapshots/{id}/send`

投递冻结周练 PDF

向该 agent 当前有效直连绑定投递既有 PDF，不接受目标选择或新题参数。幂等命令及内容批次去重，返回 batch 仍须检查各目标投递状态/回执；未知投递不盲重发。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPWeeklySendRequest](#schema-k12practicehttpweeklysendrequest)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [DeliveryBatch](#schema-k12practicedeliverybatch) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/snapshots/resource-demo-1/send" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"weekly-send-demo-1"}'
```

实现依据：[sendWeeklyPracticeSnapshot](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="k12practicesubmitweeklypracticeattempt"></a>

## `POST /api/k12/weekly-practice/snapshots/{id}/attempts`

提交冻结周练的单题作答

按冻结题目/答案验证单题，结果 correct/wrong/needs_review。相同幂等键与答案重放同一持久评估，内容变更冲突；未知模型回执先协调，不再调用。仅真实覆盖且可判定的系统作答按规则积累证据，不能把 needs_review 计为掌握。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPWeeklyAttemptRequest](#schema-k12practicehttpweeklyattemptrequest)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `item_id` | string | 是 | 当前练习项 ID；不得用来源错题 ID 替代 |
| `student_answer` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 201 | [WeeklyAttemptResponse](#schema-k12practiceweeklyattemptresponse) | 首次创建 |
| 200 | [WeeklyAttemptResponse](#schema-k12practiceweeklyattemptresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/snapshots/resource-demo-1/attempts" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","item_id":"weekly-item-demo-1","student_answer":"5","idempotency_key":"weekly-answer-demo-1"}'
```

实现依据：[submitWeeklyPracticeAttempt](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="k12practicecreateweeklyarithmeticbatch"></a>

## `POST /api/k12/weekly-practice/plans/{id}/arithmetic-batches`

准备指定题数口算批次

严格 body 不接受 agent；服务端由 plan ID 解析所属孩子。plan_revision 需最新，item_count 1–20，不自动套用建议题数。返回公开 batch 投影，准备状态与完成状态分别判断。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPWeeklyManualArithmeticRequest](#schema-k12practicehttpweeklymanualarithmeticrequest)；严格解码，未知字段返回 400。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `plan_revision` | integer；minimum=1 | 是 | 快照所绑定的计划修订或请求的当前计划 CAS |
| `item_count` | integer；minimum=1; maximum=20 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

本结构未声明的字段不属于公开契约。

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 201 | [ArithmeticBatchResponse](#schema-k12practicearithmeticbatchresponse) | 首次创建 |
| 200 | [ArithmeticBatchResponse](#schema-k12practicearithmeticbatchresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/resource-demo-1/arithmetic-batches" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"plan_revision":1,"item_count":10,"idempotency_key":"arithmetic-batch-demo-1"}'
```

实现依据：[createWeeklyArithmeticBatch](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="k12practicestartweeklyarithmeticbatch"></a>

## `POST /api/k12/weekly-practice/arithmetic-batches/{id}/start`

开始已准备口算批次

使 ready 批次进入 in_progress，不新建或重新生成题目；幂等键绑定该批次命令。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPWeeklySendRequest](#schema-k12practicehttpweeklysendrequest)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [ArithmeticBatchResponse](#schema-k12practicearithmeticbatchresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/arithmetic-batches/resource-demo-1/start" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"arithmetic-start-demo-1"}'
```

实现依据：[startWeeklyArithmeticBatch](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="k12practiceretryweeklyarithmeticbatch"></a>

## `POST /api/k12/weekly-practice/arithmetic-batches/{id}/retry`

恢复可重试失败口算批次

仅合法可重试失败按原 checkpoint 恢复；ready/in_progress/completed 不作为重新出题入口，未知模型调用不盲重发。返回 batch.retryable 和 state，而非推断成功。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPWeeklySendRequest](#schema-k12practicehttpweeklysendrequest)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [ArithmeticBatchResponse](#schema-k12practicearithmeticbatchresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/arithmetic-batches/resource-demo-1/retry" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"arithmetic-retry-demo-1"}'
```

实现依据：[retryWeeklyArithmeticBatch](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="k12practicesubmitweeklyarithmeticattempt"></a>

## `POST /api/k12/weekly-practice/arithmetic-batches/{id}/attempts`

提交口算批次单题答案

使用该批次冻结 item_id 和答案，持久单题评估并返回 correct/wrong/needs_review；相同键相同答案重放不重复推进，错误可安排错题复习。不得用整批未覆盖作答冒充完成。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPWeeklyAttemptRequest](#schema-k12practicehttpweeklyattemptrequest)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `item_id` | string | 是 | 当前练习项 ID；不得用来源错题 ID 替代 |
| `student_answer` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 201 | [ArithmeticAttemptResponse](#schema-k12practicearithmeticattemptresponse) | 首次创建 |
| 200 | [ArithmeticAttemptResponse](#schema-k12practicearithmeticattemptresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 500 | `MessageError` | 未分类存储或依赖错误 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/arithmetic-batches/resource-demo-1/attempts" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","item_id":"arithmetic-item-demo-1","student_answer":"5","idempotency_key":"arithmetic-answer-demo-1"}'
```

实现依据：[submitWeeklyArithmeticAttempt](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="k12practicerefreshweeklytextbooktrack"></a>

## `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/refresh`

按当前进度刷新教材巩固轨道

只允许 draft，轨道必须已启用且进度变新或原生成失败；CAS 校验 expected_revision。进度过期时建立新 revision，返回 201；失败恢复或重放返回 200。只替换该计划教材分区，既有冻结卷不会覆盖或自动再生。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPWeeklyExpectedRevisionRequest](#schema-k12practicehttpweeklyexpectedrevisionrequest)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `expected_revision` | integer；minimum=1 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 200 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | 成功或相同命令重放 |
| 201 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | 首次创建 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/resource-demo-1/tracks/textbook_consolidation/refresh" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","expected_revision":1,"idempotency_key":"textbook-refresh-demo-1"}'
```

实现依据：[refreshWeeklyTextbookTrack](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="k12practiceprepareweeklytextbooktrack"></a>

## `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/prepare`

按明确题数准备教材巩固

严格 body 不接受 agent，按 plan ID 解析孩子；plan_revision CAS、item_count 1–10。需合法教材绑定/进度及源证据；建议题数只是建议，显式命令才生成，既有冻结内容不重写。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPWeeklyManualTextbookRequest](#schema-k12practicehttpweeklymanualtextbookrequest)；严格解码，未知字段返回 400。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `plan_revision` | integer；minimum=1 | 是 | 快照所绑定的计划修订或请求的当前计划 CAS |
| `item_count` | integer；minimum=1; maximum=10 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

本结构未声明的字段不属于公开契约。

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 201 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | 首次创建 |
| 200 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/resource-demo-1/tracks/textbook_consolidation/prepare" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"plan_revision":1,"item_count":5,"idempotency_key":"textbook-prepare-demo-1"}'
```

实现依据：[prepareWeeklyTextbookTrack](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="k12practicerecoverweeklytextbooktrack"></a>

## `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/recovery-attempts`

追加授权的教材原题验算恢复

恢复命令允许原题追加物理验算，因此 accept_duplicate_execution 必须显式 true。 严格 body 不接受 agent。source_command_key、checkpoint_sha256 和零基 item_index 精确绑定原 checkpoint。旧 plan_revision>0 可同时作为源/期望版本；分离版本时 source_plan_revision>=1 且 expected_plan_revision>=source，若同时给旧字段须等于源版本。源档案/教材/进度变更返回冲突；保留原回执和未知结果。

此命令需要已经取得的原恢复上下文：`source_command_key` 是原教材 prepare/refresh 命令键；`checkpoint_sha256` 是持久 checkpoint 原始 JSON 字节的实际 SHA-256，`item_index` 是该 checkpoint 内 `generation.items` 的零基索引。当前公开计划/快照查询不返回原始 checkpoint 或它的摘要/索引，不能由 `snapshot_digest`、公开题目数组或当前 revision 推导。示例中的全零摘要只是请求形状占位，缺少真实上下文时不能直接调用。底层解码省略 `item_index` 时取 0，建议始终显式填写目标索引。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[WeeklyTextbookRecoveryRequest](#schema-k12practiceweeklytextbookrecoveryrequest)；严格解码，未知字段返回 400。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `source_command_key` | string；minLength=1 | 是 | 原教材 prepare/refresh 命令的幂等键，须来自已有恢复上下文。 |
| `plan_revision` | integer；minimum=1 | 否 | 快照所绑定的计划修订或请求的当前计划 CAS |
| `checkpoint_sha256` | string；pattern=^[a-fA-F0-9]{64}$ | 是 | 原持久 checkpoint JSON 字节的真实 SHA-256；公开计划/快照查询不提供，使用实际小写摘要。 |
| `item_index` | integer；minimum=0; default=0 | 否 | 原 checkpoint 的 generation.items 零基索引；省略取首项，建议显式提交。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `accept_duplicate_execution` | boolean (`true`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_plan_revision` | integer；minimum=1 | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `expected_plan_revision` | integer；minimum=1 | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

分支与条件约束：

```json
{
  "anyOf": [
    {
      "required": [
        "plan_revision"
      ]
    },
    {
      "required": [
        "source_plan_revision",
        "expected_plan_revision"
      ]
    }
  ]
}
```

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 201 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | 首次创建 |
| 200 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/resource-demo-1/tracks/textbook_consolidation/recovery-attempts" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"source_command_key":"textbook-prepare-demo-1","plan_revision":1,"checkpoint_sha256":"0000000000000000000000000000000000000000000000000000000000000000","item_index":0,"idempotency_key":"recoverWeeklyTextbookTrack-demo-1","accept_duplicate_execution":true}'
```

实现依据：[recoverWeeklyTextbookTrack](../../../../scenarios/k12/apihttp/weekly_recovery_handler.go)。

<a id="k12practicereinterpretweeklytextbooktrack"></a>

## `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/reinterpretations`

重新解释既有教材调用回执

只允许读取和重新解释成功的既有物理回执，不允许新增模型调用。 严格 body 不接受 agent。source_command_key、checkpoint_sha256 和零基 item_index 精确绑定原 checkpoint。旧 plan_revision>0 可同时作为源/期望版本；分离版本时 source_plan_revision>=1 且 expected_plan_revision>=source，若同时给旧字段须等于源版本。源档案/教材/进度变更返回冲突；保留原回执和未知结果。

与恢复命令一样，这需要原持久 checkpoint 的实际字节摘要及 `generation.items` 索引。公开计划/快照查询不暴露这些字段；不能使用 `snapshot_digest` 或公开题目顺序替代。示例仅展示请求形状，已有真实恢复上下文时才能提交。省略 `item_index` 的底层默认值为 0，建议显式指定。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[WeeklyTextbookReinterpretationRequest](#schema-k12practiceweeklytextbookreinterpretationrequest)；严格解码，未知字段返回 400。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `source_command_key` | string；minLength=1 | 是 | 原教材 prepare/refresh 命令的幂等键，须来自已有恢复上下文。 |
| `plan_revision` | integer；minimum=1 | 否 | 快照所绑定的计划修订或请求的当前计划 CAS |
| `checkpoint_sha256` | string；pattern=^[a-fA-F0-9]{64}$ | 是 | 原持久 checkpoint JSON 字节的真实 SHA-256；公开计划/快照查询不提供，使用实际小写摘要。 |
| `item_index` | integer；minimum=0; default=0 | 否 | 原 checkpoint 的 generation.items 零基索引；省略取首项，建议显式提交。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `source_plan_revision` | integer；minimum=1 | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `expected_plan_revision` | integer；minimum=1 | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

分支与条件约束：

```json
{
  "anyOf": [
    {
      "required": [
        "plan_revision"
      ]
    },
    {
      "required": [
        "source_plan_revision",
        "expected_plan_revision"
      ]
    }
  ]
}
```

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 201 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | 首次创建 |
| 200 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/resource-demo-1/tracks/textbook_consolidation/reinterpretations" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"source_command_key":"textbook-prepare-demo-1","plan_revision":1,"checkpoint_sha256":"0000000000000000000000000000000000000000000000000000000000000000","item_index":0,"idempotency_key":"reinterpretWeeklyTextbookTrack-demo-1"}'
```

实现依据：[reinterpretWeeklyTextbookTrack](../../../../scenarios/k12/apihttp/weekly_recovery_handler.go)。

<a id="k12practicesaveweeklypracticetopracticeset"></a>

## `POST /api/k12/weekly-practice/plans/{id}/save-to-practice-set`

将冻结周练加入练习篮

仅 frozen 计划且 expected_revision 与冻结版本一致；从快照中取 verified 项，按内容去重装入孩子 draft 篮并持久化收据，重放不重复加入。该命令不等于已打印、已回传或已掌握。

| 参数 | 位置 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | 是 | 资源 ID；使用前一响应中的实际 ID |

JSON body：[HTTPWeeklyExpectedRevisionRequest](#schema-k12practicehttpweeklyexpectedrevisionrequest)；未知字段不作为本操作输入使用。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `expected_revision` | integer；minimum=1 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

| HTTP | 返回体 | 说明 |
| --- | --- | --- |
| 401 | `MessageError` | Bearer 令牌缺失或无效。 |
| 201 | [WeeklySaveResponse](#schema-k12practiceweeklysaveresponse) | 首次创建 |
| 200 | [WeeklySaveResponse](#schema-k12practiceweeklysaveresponse) | 成功或相同命令重放 |
| 400 | `MessageError` | JSON、参数或已分类领域输入无效 |
| 404 | `MessageError` | 资源不存在或不属于指定 agent；部分入口将范围拒绝折叠为 404 |
| 409 | `MessageError` | 已分类版本、命令摘要或状态冲突；结果未知须先核对原调用 |
| 502 | `MessageError` | 模型、渲染或目录依赖执行失败 |
| 413 | `MessageError` | JSON 请求体超过 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/resource-demo-1/save-to-practice-set" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","expected_revision":1,"idempotency_key":"weekly-save-demo-1"}'
```

实现依据：[saveWeeklyPracticeToPracticeSet](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

## 返回体与嵌套 JSON 结构

本节“必填”表示 Go DTO 未使用 `omitempty`，应作为响应字段序列化；nullable 字段仍可为 null，不是额外请求要求。`_at` 整数时间戳通常为 Unix 秒，显式 format 另有定义的从其定义。

<a id="schema-k12practiceok"></a>

### OK

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `ok` | boolean (`true`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicehttpmistakedto"></a>

### HTTPMistakeDTO

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `record_id` | string | 是 | 记录 ID；作为对应记录路由的路径参数 |
| `question` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `knowledge_point` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `error_cause` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `status` | string (`new`, `explained`, `retried`, `mastered`, `archived`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `review_state` | string (`scheduled`, `deferred_this_week`, `suppressed`, `mastered`) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `version` | integer | 是 | 当前记录或任务乐观锁版本 |
| `due_at` | integer / null | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `subject` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `review_kind` | string (`verify`, `verbatim`) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `spot_check_state` | string (`none`, `scheduled`, `passed`, `failed`) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `parent_confirmed_at` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `archived_reason` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `archived_at` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `archive_restored_at` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `restorable` | boolean | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `created_at` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `entry_source` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/apihttp/handler.go](../../../../scenarios/k12/apihttp/handler.go)。

<a id="schema-k12practicesinglepracticegenerationview"></a>

### SinglePracticeGenerationView

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `state` | string (`available`, `pending`, `joined`, `failed`, `re_add`, `hidden`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_mistake_id` | string | 是 | 来源错题 ID |
| `generation_job_id` | string | 否 | 持久生成任务 ID；本身不证明产物完成 |
| `practice_set_id` | string | 否 | 已加入的练习集 ID |
| `practice_item_id` | string | 否 | 已加入练习集的题目 ID |
| `failure_reason` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_mistake_summary` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `item` | [PracticeItem](#schema-k12practicepracticeitem) / null | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `parent_confirmed` | boolean | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `evidence_mastered` | boolean | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/usecase/single_practice_generation.go](../../../../scenarios/k12/usecase/single_practice_generation.go)。

<a id="schema-k12practicesinglepracticegenerationstate"></a>

### SinglePracticeGenerationState

`{"type": "string", "enum": ["available", "pending", "joined", "failed", "re_add", "hidden"]}`

源码：[scenarios/k12/usecase/single_practice_generation.go](../../../../scenarios/k12/usecase/single_practice_generation.go)。

<a id="schema-k12practicepracticeitem"></a>

### PracticeItem

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `asset_source` | [PracticeAssetSource](#schema-k12practicepracticeassetsource) / null | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `item_id` | string | 是 | 当前练习项 ID；不得用来源错题 ID 替代 |
| `source_problem_id` | string | 否 | 来源题 ID，用于关联原错题；手工题可无来源 |
| `source_mistake_summary` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `subject` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `added_via` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `generation_status` | string (`queued`, `generating`, `validating`, `ready`, `failed`) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `question_markdown` | string | 是 | 规范题面 Markdown |
| `expected_answer_markdown` | string | 是 | 已采用的答案 Markdown，供答案卷或核对使用 |
| `verification_status` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `verification_evidence` | string | 否 | 本次验证方式或证据引用 |
| `blocked_reason` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `paper_seq` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `returned` | boolean | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `practice_problem_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `generation_job_id` | string | 否 | 持久生成任务 ID；本身不证明产物完成 |
| `variant_index` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `requested_difficulty` | string (`same`, `easier`, `harder`) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `actual_difficulty` | string (`same`, `easier`, `harder`) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `normalized_content_hash` | string | 否 | 服务端题目规范化摘要，用于内容去重 |
| `result_correct` | boolean / null | 否 | 缺省或 null 为无结论；false 为已判错 |
| `result_evidence` | string | 否 | system_verified 或 human_confirmed；人工标记不形成系统掌握证据 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/practiceset.go](../../../../scenarios/k12/practiceset.go)。

<a id="schema-k12practicepracticeassetsource"></a>

### PracticeAssetSource

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `owner_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `asset_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `asset_version` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `asset_revision` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `facts_digest` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `grade_term` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `knowledge_point` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `original_review` | boolean | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/practice_asset.go](../../../../scenarios/k12/practice_asset.go)。

<a id="schema-k12practicepracticecandidateselection"></a>

### PracticeCandidateSelection

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `selection_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_mistake_id` | string | 是 | 来源错题 ID |
| `target_set_record_id` | string | 是 | 当前选择会话的目标 draft 练习篮 ID |
| `state` | string (`open`, `committed`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `next_batch_ordinal` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `revision` | integer | 是 | 当前聚合修订，用于对应 revision/expected_revision CAS |
| `candidates` | array / null [PracticeCandidate](#schema-k12practicepracticecandidate) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/practice_candidate_review.go](../../../../scenarios/k12/practice_candidate_review.go)。

<a id="schema-k12practicepracticecandidate"></a>

### PracticeCandidate

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `candidate_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `candidate_kind` | string (`original`, `variant`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `batch_ordinal` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `candidate_ordinal` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `normalized_content_hash` | string | 是 | 服务端题目规范化摘要，用于内容去重 |
| `state` | string (`generating`, `ready`, `failed`, `already_in_set`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `question_markdown` | string | 是 | 规范题面 Markdown |
| `expected_answer_markdown` | string | 否 | 已采用的答案 Markdown，供答案卷或核对使用 |
| `failure_message` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/practice_candidate_review.go](../../../../scenarios/k12/practice_candidate_review.go)。

<a id="schema-k12practicehttppracticesetdto"></a>

### HTTPPracticeSetDTO

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `record_id` | string | 是 | 记录 ID；作为对应记录路由的路径参数 |
| `title` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_kind` | string (`weekly`, `custom`, `single_variant`, `manual`, `mixed`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `status` | string (`draft`, `confirmed`, `assigned`, `submitted`, `graded`, `closed`, `cancelled`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `status_label` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `publishable` | boolean | 是 | 是否包含可发布的已验证项；不表示已打印/投递 |
| `question_artifact_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `answer_artifact_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `delivery_status` | string (`not_sent`, `pending`, `sending`, `delivered`, `failed`, `partial_failed`, `outcome_unknown`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `delivery_batch_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `skipped_blocked_count` | integer | 否 | 固化时跳过的未达发布条件题数 |
| `paper_no` | string | 否 | 预占或正式卷号；是否已打印以原生回执为准 |
| `finalized_at` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `finalized_via` | string (`print`, `send`) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `items` | array / null [HTTPPracticeItemDTO](#schema-k12practicehttppracticeitemdto) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `return_assets` | array / null [HTTPPracticeReturnAssetDTO](#schema-k12practicehttppracticereturnassetdto) | 是 | 回传照片及各自的异步复批状态 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="schema-k12practicehttppracticeitemdto"></a>

### HTTPPracticeItemDTO

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `item_id` | string | 是 | 当前练习项 ID；不得用来源错题 ID 替代 |
| `source_problem_id` | string | 否 | 来源题 ID，用于关联原错题；手工题可无来源 |
| `subject` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `added_via` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `question_markdown` | string | 是 | 规范题面 Markdown |
| `expected_answer_markdown` | string | 否 | 已采用的答案 Markdown，供答案卷或核对使用 |
| `verification_status` | string (`pending`, `verified`, `needs_review`, `rejected`, `stale`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `verification_evidence` | string | 否 | 本次验证方式或证据引用 |
| `blocked_reason` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `paper_seq` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `returned` | boolean | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `return_ids` | array / null string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `generation_job_id` | string | 否 | 持久生成任务 ID；本身不证明产物完成 |
| `variant_index` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `requested_difficulty` | string (`same`, `easier`, `harder`) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `actual_difficulty` | string (`same`, `easier`, `harder`) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `result_correct` | boolean / null | 否 | 缺省或 null 为无结论；false 为已判错 |
| `result_evidence` | string | 否 | system_verified 或 human_confirmed；人工标记不形成系统掌握证据 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="schema-k12practicehttppracticereturnassetdto"></a>

### HTTPPracticeReturnAssetDTO

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `return_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `asset_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `item_ids` | array / null string | 是 | 本次覆盖的练习项 ID；空不代表整卷 |
| `auto_match` | boolean | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `candidate_item_ids` | array / null string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `returned_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `regrade_job_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `regrade_status` | string (`queued`, `running`, `needs_review`, `completed`, `failed_retryable`, `failed_terminal`, `outcome_unknown`) | 否 | 回传照片的持久复批状态 |
| `route_snapshot` | [GradingModelSnapshot](#schema-k12practicegradingmodelsnapshot) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `annotated_asset_id` | string | 否 | 批注原图产物 ID；任务完成还需核对可读取产物 |
| `result_markdown` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `unresolved_item_ids` | array / null string | 否 | 本次未能可靠判定的题目，不积累掌握证据 |
| `regrade_updated_at` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="schema-k12practicegradingmodelsnapshot"></a>

### GradingModelSnapshot

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `parent_instructions` | [ConfigAgentInstructionsSnapshot](#schema-k12practiceconfigagentinstructionssnapshot) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `provider` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `model` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `provider_instance_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `config_fingerprint` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `capability_receipt_digest` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `probe_policy_version` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `route` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `capability` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `timeout_ms` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `fallback` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `recognizing_request_policy` | [ModelRequestPolicySnapshot](#schema-k12practicemodelrequestpolicysnapshot) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/gradingjob.go](../../../../scenarios/k12/gradingjob.go)。

<a id="schema-k12practiceconfigagentinstructionssnapshot"></a>

### ConfigAgentInstructionsSnapshot

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `content` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `digest` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[config/agent_instructions.go](../../../../config/agent_instructions.go)。

<a id="schema-k12practicemodelrequestpolicysnapshot"></a>

### ModelRequestPolicySnapshot

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `policy_version` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `stage` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `thinking` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `reasoning_effort` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/model_request_policy.go](../../../../scenarios/k12/model_request_policy.go)。

<a id="schema-k12practicehttppracticeprintjobdto"></a>

### HTTPPracticePrintJobDTO

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `print_job_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `practice_set_id` | string | 否 | 已加入的练习集 ID |
| `idempotency_key` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `status` | string (`preparing`, `dialog_open`, `submitted`, `printed`, `cancelled`, `failed`, `outcome_unknown`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `paper_no` | string | 否 | 预占或正式卷号；是否已打印以原生回执为准 |
| `artifact_kind` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `artifact_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `question_artifact_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `answer_artifact_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_kind` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_ref` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `title` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_digest` | string | 是 | 冻结来源内容摘要，用于确认读取同一版本 |
| `attempt_count` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `native_job_id` | string | 否 | 系统打印任务身份；结果未知时用原身份核对 |
| `native_receipt_id` | string | 否 | 系统打印成功回执身份，禁止以接纳/对话框状态代替 |
| `printer_snapshot` | object / null | 否 | 原生打印机事实快照 |
| `failure_kind` | string | 否 | 失败分类；失败或结果未知事件必需 |
| `failure_detail` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `prepared_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `printed_at` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `updated_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `version` | integer | 是 | 当前记录或任务乐观锁版本 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="schema-k12practiceweeklypracticeplan"></a>

### WeeklyPracticePlan

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `plan_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `agent` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `revision` | integer | 是 | 当前聚合修订，用于对应 revision/expected_revision CAS |
| `iso_week_year` | integer | 是 | 该时区 ISO 周年，跨年时可与日历年不同 |
| `iso_week_number` | integer | 是 | ISO 周数 1–53 |
| `timezone` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `week_start` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `week_end` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `local_start_date` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `local_end_date` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `status` | string (`draft`, `frozen`, `archived`, `expired_unused`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `settings_revision` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `curriculum_progress_revision` | integer / null | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `tracks` | array / null [WeeklyPracticeTrack](#schema-k12practiceweeklypracticetrack) | 是 | 到期复习、教材巩固、口算热身分区及逐题证据 |
| `manual_track_recommendations` | [WeeklyManualTrackRecommendations](#schema-k12practiceweeklymanualtrackrecommendations) | 是 | 当前可用性与建议数量；不会自动替代显式 item_count |
| `created_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `updated_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go)。

<a id="schema-k12practiceweeklypracticetrack"></a>

### WeeklyPracticeTrack

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `plan_section` | string (`due_review`, `textbook_consolidation`, `arithmetic_warmup`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `status` | string (`ready`, `disabled`, `stale`, `failed`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `failure_message` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `items` | array / null [WeeklyPracticeItem](#schema-k12practiceweeklypracticeitem) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `arithmetic_batch` | [WeeklyArithmeticBatch](#schema-k12practiceweeklyarithmeticbatch) / null | 是 | 该轨道当前口算批次投影，可为 null |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go)。

<a id="schema-k12practiceweeklypracticeitem"></a>

### WeeklyPracticeItem

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `item_id` | string | 是 | 当前练习项 ID；不得用来源错题 ID 替代 |
| `position` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `plan_section` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_kind` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `generation_method` | string (`original`, `ai_variant`, `ai_generated`, `rule_generated`, `asset_reuse`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_ref` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `subject` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `knowledge_point` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `mastery_status` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `verification` | [WeeklyPracticeVerification](#schema-k12practiceweeklypracticeverification) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `prompt_markdown` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `asset_source` | [PracticeAssetSource](#schema-k12practicepracticeassetsource) / null | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_question` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go)。

<a id="schema-k12practiceweeklypracticeverification"></a>

### WeeklyPracticeVerification

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `status` | string (`verified`, `failed`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `evidence_refs` | array / null string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `textbook_binding_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `unit_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `lesson_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `verified_page_from` | integer / null | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `verified_page_to` | integer / null | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go)。

<a id="schema-k12practiceweeklyarithmeticbatch"></a>

### WeeklyArithmeticBatch

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `batch_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `state` | string (`preparing`, `ready`, `in_progress`, `completed`, `failed_retryable`, `failed_terminal`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `item_count` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `content_digest` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `retryable` | boolean | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `failure_message` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `created_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `updated_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `completed_at` | integer / null | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/weekly_arithmetic.go](../../../../scenarios/k12/weekly_arithmetic.go)。

<a id="schema-k12practiceweeklymanualtrackrecommendations"></a>

### WeeklyManualTrackRecommendations

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `textbook_consolidation` | [WeeklyManualTrackRecommendation](#schema-k12practiceweeklymanualtrackrecommendation) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `arithmetic_warmup` | [WeeklyManualTrackRecommendation](#schema-k12practiceweeklymanualtrackrecommendation) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go)。

<a id="schema-k12practiceweeklymanualtrackrecommendation"></a>

### WeeklyManualTrackRecommendation

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `availability` | string (`available`, `setup_required`, `processing`, `failed_retryable`, `failed_terminal`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `selected_item_count` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `recommended_item_count` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `min_item_count` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `max_item_count` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go)。

<a id="schema-k12practiceweeklypracticesnapshot"></a>

### WeeklyPracticeSnapshot

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `snapshot_id` | string | 是 | 冻结周练快照 ID，历史内容不可变 |
| `artifact_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `plan_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `plan_revision` | integer | 是 | 快照所绑定的计划修订或请求的当前计划 CAS |
| `agent` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `iso_week_year` | integer | 是 | 该时区 ISO 周年，跨年时可与日历年不同 |
| `iso_week_number` | integer | 是 | ISO 周数 1–53 |
| `timezone` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `week_start` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `week_end` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `local_start_date` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `local_end_date` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `settings_revision` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `curriculum_progress_revision` | integer / null | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `tracks` | array / null [WeeklyPracticeTrack](#schema-k12practiceweeklypracticetrack) | 是 | 到期复习、教材巩固、口算热身分区及逐题证据 |
| `render_version` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `snapshot_digest` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `created_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go)。

<a id="schema-k12practicedeliverybatch"></a>

### DeliveryBatch

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `batch_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `agent_name` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `object_kind` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `object_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `dedupe_key` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `content_digest` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `status` | [DeliveryBatchStatus](#schema-k12practicedeliverybatchstatus) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `receipts` | array / null [DeliveryReceipt](#schema-k12practicedeliveryreceipt) | 是 | 逐目标/消息部分投递回执；全部必要部分 delivered 才完成 |
| `created_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `updated_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/delivery_receipt.go](../../../../scenarios/k12/delivery_receipt.go)。

<a id="schema-k12practicedeliverybatchstatus"></a>

### DeliveryBatchStatus

`{"type": "string", "enum": ["pending", "sending", "delivered", "failed", "partial_failed", "outcome_unknown"]}`

源码：[scenarios/k12/delivery_receipt.go](../../../../scenarios/k12/delivery_receipt.go)。

<a id="schema-k12practicedeliveryreceipt"></a>

### DeliveryReceipt

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `delivery_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `batch_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `batch_ordinal` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `part_kind` | [MessagePartKind](#schema-k12practicemessagepartkind) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `part_mime` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `part_ordinal` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `part_digest` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `agent_name` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `object_kind` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `object_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `binding_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `target` | [DeliveryTarget](#schema-k12practicedeliverytarget) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `status` | [DeliveryReceiptStatus](#schema-k12practicedeliveryreceiptstatus) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `dedupe_key` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `payload_digest` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `payload_json` | string | 是 | 冻结投递载荷的 JSON 字符串 |
| `render_manifest_json` | string | 是 | 冻结投递呈现协议的 JSON 字符串 |
| `external_message_id` | string | 否 | Provider 接纳关联 ID，本身不代表 delivered |
| `attempt` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `last_error` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `created_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `updated_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/delivery_receipt.go](../../../../scenarios/k12/delivery_receipt.go)。

<a id="schema-k12practicemessagepartkind"></a>

### MessagePartKind

`{"type": "string", "enum": ["markdown", "text", "artifact"]}`

源码：[messagecontent/messagecontent.go](../../../../messagecontent/messagecontent.go)。

<a id="schema-k12practicedeliverytarget"></a>

### DeliveryTarget

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `platform` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `instance_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `chat_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `label` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/delivery_receipt.go](../../../../scenarios/k12/delivery_receipt.go)。

<a id="schema-k12practicedeliveryreceiptstatus"></a>

### DeliveryReceiptStatus

`{"type": "string", "enum": ["pending", "sending", "delivered", "failed", "outcome_unknown"]}`

源码：[scenarios/k12/delivery_receipt.go](../../../../scenarios/k12/delivery_receipt.go)。

<a id="schema-k12practiceprintjobresponse"></a>

### PrintJobResponse

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `print_job` | [HTTPPracticePrintJobDTO](#schema-k12practicehttppracticeprintjobdto) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practiceprintjobprepareresponse"></a>

### PrintJobPrepareResponse

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `print_job` | [HTTPPracticePrintJobDTO](#schema-k12practicehttppracticeprintjobdto) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `replayed` | boolean | 是 | 同一冻结命令的持久结果重放，未重复新增副作用 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practiceplanresponse"></a>

### PlanResponse

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `plan` | [WeeklyPracticePlan](#schema-k12practiceweeklypracticeplan) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practiceplanreplayresponse"></a>

### PlanReplayResponse

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `plan` | [WeeklyPracticePlan](#schema-k12practiceweeklypracticeplan) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `replayed` | boolean | 是 | 同一冻结命令的持久结果重放，未重复新增副作用 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicearithmeticbatchresponse"></a>

### ArithmeticBatchResponse

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `batch` | [WeeklyArithmeticBatch](#schema-k12practiceweeklyarithmeticbatch) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `replayed` | boolean | 是 | 同一冻结命令的持久结果重放，未重复新增副作用 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practiceprintableartifact"></a>

### PrintableArtifact

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `artifact_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_kind` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_ref` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `title` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_digest` | string | 是 | 冻结来源内容摘要，用于确认读取同一版本 |
| `format` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `render_contract_version` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `content_type` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `byte_digest` | string | 是 | 冻结 PDF 字节摘要，与下载 X-Content-SHA256 一致 |
| `byte_size` | integer | 是 | PDF 实际字节数 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practiceartifactprepareresponse"></a>

### ArtifactPrepareResponse

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `artifact` | [PrintableArtifact](#schema-k12practiceprintableartifact) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `replayed` | boolean | 是 | 同一冻结命令的持久结果重放，未重复新增副作用 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicehttpagentonlyreq"></a>

### HTTPAgentOnlyReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |

源码：[scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="schema-k12practicemistakelist"></a>

### MistakeList

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | array [HTTPMistakeDTO](#schema-k12practicehttpmistakedto) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `total` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicehttpmistakearchivecommandreq"></a>

### HTTPMistakeArchiveCommandReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `version` | integer；minimum=0 | 是 | 当前记录 version，必填且不可为负；使用响应中的新 version 做后续恢复 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/apihttp/handler.go](../../../../scenarios/k12/apihttp/handler.go)。

<a id="schema-k12practicehttpsinglepracticegenerationreq"></a>

### HTTPSinglePracticeGenerationReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `grade` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `textbook` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `difficulty` | string (`same`, `easier`, `harder`) | 否 | 省略默认 same |
| `provider` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `model` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_session` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

源码：[scenarios/k12/apihttp/handler.go](../../../../scenarios/k12/apihttp/handler.go)。

<a id="schema-k12practicepracticegenerationreceiptview"></a>

### PracticeGenerationReceiptView

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `schema_version` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_kind` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `generation_job_id_digest` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `generation_status` | string (`queued`, `generating`, `validating`, `committed`, `failed`, `cancelled`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `receipt_exact_set_digest` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `receipts` | array / null [PracticeGenerationInvocationReceipt](#schema-k12practicepracticegenerationinvocationreceipt) | 是 | 逐目标/消息部分投递回执；全部必要部分 delivered 才完成 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/usecase/single_practice_generation.go](../../../../scenarios/k12/usecase/single_practice_generation.go)。

<a id="schema-k12practicepracticegenerationinvocationreceipt"></a>

### PracticeGenerationInvocationReceipt

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `stage` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `attempt` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `status` | [ModelInvocationStatus](#schema-k12practicemodelinvocationstatus) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `provider` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `model` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `route` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `provider_instance_id_digest` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `config_fingerprint` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `capability_receipt_digest` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `probe_policy_version` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `request_digest` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `result_digest` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `external_request_id_digest` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `failure_kind` | string | 否 | 失败分类；失败或结果未知事件必需 |
| `created_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `updated_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `receipt_digest` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/usecase/single_practice_generation.go](../../../../scenarios/k12/usecase/single_practice_generation.go)。

<a id="schema-k12practicemodelinvocationstatus"></a>

### ModelInvocationStatus

`{"type": "string", "enum": ["prepared", "sent", "succeeded", "failed", "outcome_unknown", "reconciled"]}`

源码：[scenarios/k12/model_invocation.go](../../../../scenarios/k12/model_invocation.go)。

<a id="schema-k12practicehttppracticecandidateopenreq"></a>

### HTTPPracticeCandidateOpenReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `grade` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `textbook` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `provider` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `model` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_session` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/apihttp/practice_candidate_review_handler.go](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go)。

<a id="schema-k12practicehttppracticecandidatebatchreq"></a>

### HTTPPracticeCandidateBatchReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `revision` | integer；minimum=1 | 是 | 当前聚合修订，用于对应 revision/expected_revision CAS |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `provider` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `model` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/apihttp/practice_candidate_review_handler.go](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go)。

<a id="schema-k12practicehttppracticecandidatecommitreq"></a>

### HTTPPracticeCandidateCommitReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `revision` | integer；minimum=1 | 是 | 当前聚合修订，用于对应 revision/expected_revision CAS |
| `candidate_ids` | array string；minLength=1；minItems=1; uniqueItems=True | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/apihttp/practice_candidate_review_handler.go](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go)。

<a id="schema-k12practicestoragepracticecandidatecommitreceipt"></a>

### StoragePracticeCandidateCommitReceipt

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `selection` | [PracticeCandidateSelection](#schema-k12practicepracticecandidateselection) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `added_count` | integer | 是 | 本次事务实际新增题数 |
| `already_present` | array / null string | 是 | 提交时已在目标篮内的候选题 ID |
| `replayed` | boolean | 是 | 同一冻结命令的持久结果重放，未重复新增副作用 |

本结构未声明的字段不属于公开契约。


源码：[scenarios/k12/storage/practice_candidate_review.go](../../../../scenarios/k12/storage/practice_candidate_review.go)。

<a id="schema-k12practicehttpmistakereviewcommandreq"></a>

### HTTPMistakeReviewCommandReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `version` | integer；minimum=0 | 是 | 当前记录或任务乐观锁版本 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `plan_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `plan_revision` | integer | 否 | 快照所绑定的计划修订或请求的当前计划 CAS |
| `weekly_item_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `iso_year` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `iso_week` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/apihttp/practice_candidate_review_handler.go](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go)。

<a id="schema-k12practicestoragemistakereviewcommandresult"></a>

### StorageMistakeReviewCommandResult

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `state` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `mistake_version` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `replayed` | boolean | 是 | 同一冻结命令的持久结果重放，未重复新增副作用 |
| `review` | [MistakeReviewState](#schema-k12practicemistakereviewstate) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/storage/practice_candidate_review.go](../../../../scenarios/k12/storage/practice_candidate_review.go)。

<a id="schema-k12practicemistakereviewstate"></a>

### MistakeReviewState

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `mistake_record_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `state` | string (`scheduled`, `deferred_this_week`, `suppressed`, `mastered`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `deferred_iso_year` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `deferred_iso_week` | integer | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `revision` | integer | 是 | 当前聚合修订，用于对应 revision/expected_revision CAS |
| `updated_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/practice_candidate_review.go](../../../../scenarios/k12/practice_candidate_review.go)。

<a id="schema-k12practicedefermistakerequest"></a>

### DeferMistakeRequest

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `version` | integer；minimum=0 | 是 | 当前记录或任务乐观锁版本 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `plan_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `plan_revision` | integer | 否 | 快照所绑定的计划修订或请求的当前计划 CAS |
| `weekly_item_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `iso_year` | integer；minimum=1 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `iso_week` | integer；minimum=1; maximum=53 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicesetlist"></a>

### SetList

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | array [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicepaper"></a>

### Paper

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `kind` | string (`question`, `answer`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `title` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `paper_no` | string | 是 | 预占或正式卷号；是否已打印以原生回执为准 |
| `markdown` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `preview` | boolean | 是 | true 为草稿预览，无正式卷面号 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicehttpverifyitemreq"></a>

### HTTPVerifyItemReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `item_id` | string | 是 | 当前练习项 ID；不得用来源错题 ID 替代 |
| `status` | string (`pending`, `verified`, `needs_review`, `rejected`, `stale`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `evidence` | string | 否 | verified 时必须非空；其他状态可空 |

源码：[scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="schema-k12practicehttpcustompaperreq"></a>

### HTTPCustomPaperReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `scope` | string (`week`, `unmastered`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `total` | string (`all`, `5`, `10`) / integer (`5`, `10`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `per_source` | integer；minimum=1; maximum=3 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `difficulty` | string (`same`, `easier`, `harder`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `textbook` | string | 否 | 显式值或档案默认，最终必须非空 |
| `grade` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `provider` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `model` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_session` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

源码：[scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="schema-k12practicecustompaperitemresult"></a>

### CustomPaperItemResult

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `item_id` | string | 是 | 当前练习项 ID；不得用来源错题 ID 替代 |
| `source_problem_id` | string | 是 | 来源题 ID，用于关联原错题；手工题可无来源 |
| `variant_index` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `actual_difficulty` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `verification_status` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `verification_evidence` | string | 否 | 本次验证方式或证据引用 |
| `blocked_reason` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `question_markdown` | string | 是 | 规范题面 Markdown |
| `expected_answer_markdown` | string | 否 | 已采用的答案 Markdown，供答案卷或核对使用 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/usecase/custom_paper.go](../../../../scenarios/k12/usecase/custom_paper.go)。

<a id="schema-k12practicecustompaperresponse"></a>

### CustomPaperResponse

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `generation_job_id` | string | 是 | 持久生成任务 ID；本身不证明产物完成 |
| `status` | string (`committed`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `set` | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `items` | array / null [CustomPaperItemResult](#schema-k12practicecustompaperitemresult) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `added` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `deduplicated` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicebasketitemrequest"></a>

### BasketItemRequest

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `item_id` | string | 否 | 当前练习项 ID；不得用来源错题 ID 替代 |
| `source_problem_id` | string | 否 | 来源题 ID，用于关联原错题；手工题可无来源 |
| `subject` | string (``, `数学`, `语文`, `英语`, `科学`, `信息科技`) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `added_via` | string (``, `weekly`, `custom`, `single_variant`, `manual`, `accumulation`, `spot_check`) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `question_markdown` | string；minLength=1 | 是 | 规范题面 Markdown |
| `expected_answer_markdown` | string | 否 | 已采用的答案 Markdown，供答案卷或核对使用 |
| `verification_status` | string (`pending`, `verified`, `needs_review`, `rejected`, `stale`) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `verification_evidence` | string | 否 | 本次验证方式或证据引用 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicehttpaddtobasketreq"></a>

### HTTPAddToBasketReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `source_session` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `item` | [BasketItemRequest](#schema-k12practicebasketitemrequest) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

源码：[scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="schema-k12practicebasketaddresponse"></a>

### BasketAddResponse

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `record_id` | string | 是 | 记录 ID；作为对应记录路由的路径参数 |
| `added` | boolean | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicehttpremovefrombasketreq"></a>

### HTTPRemoveFromBasketReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `item_id` | string | 是 | 当前练习项 ID；不得用来源错题 ID 替代 |

源码：[scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="schema-k12practicehttpfinalizereq"></a>

### HTTPFinalizeReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `via` | string (`print`, `send`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="schema-k12practicefinalizeresponse"></a>

### FinalizeResponse

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `set` | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `skipped_blocked_count` | integer | 是 | 固化时跳过的未达发布条件题数 |
| `delivery_batch` | [DeliveryBatch](#schema-k12practicedeliverybatch) | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicehttppreparepracticeprintreq"></a>

### HTTPPreparePracticePrintReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `artifact_kind` | string (`question`, `answer`) | 否 | 省略默认 question |

源码：[scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="schema-k12practicegenericprintrequest"></a>

### GenericPrintRequest

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `idempotency_key` | string；minLength=1 | 是 | 命令键，UTF-8 字节数不超过 512 |
| `artifact_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_kind` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_ref` | string | 否 | 源引用，UTF-8 字节数不超过 512 |
| `title` | string | 否 | 标题，UTF-8 字节数不超过 256 |
| `canonical_markdown` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

分支与条件约束：

```json
{
  "oneOf": [
    {
      "required": [
        "artifact_id"
      ],
      "properties": {
        "artifact_id": {
          "minLength": 1
        },
        "source_kind": {
          "const": ""
        },
        "source_ref": {
          "const": ""
        },
        "title": {
          "const": ""
        },
        "canonical_markdown": {
          "const": ""
        }
      }
    },
    {
      "required": [
        "source_kind",
        "source_ref",
        "title",
        "canonical_markdown"
      ],
      "properties": {
        "artifact_id": {
          "const": ""
        },
        "source_kind": {
          "type": "string",
          "enum": [
            "tutoring_tips",
            "creative_observation_card",
            "practice_question",
            "practice_answer",
            "grading_final_artifact",
            "weekly_practice_snapshot",
            "learning_archive"
          ]
        },
        "source_ref": {
          "type": "string",
          "minLength": 1,
          "description": "源引用，UTF-8 字节数不超过 512 / Source reference, at most 512 UTF-8 bytes"
        },
        "title": {
          "type": "string",
          "minLength": 1,
          "description": "标题，UTF-8 字节数不超过 256 / Title, at most 256 UTF-8 bytes"
        },
        "canonical_markdown": {
          "type": "string",
          "minLength": 1
        }
      }
    }
  ]
}
```

<a id="schema-k12practiceprintableartifactrequest"></a>

### PrintableArtifactRequest

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_kind` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_ref` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `title` | string；minLength=1 | 是 | 标题，UTF-8 字节数不超过 256 |
| `canonical_markdown` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `final_artifact_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `final_artifact_digest` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

分支与条件约束：

```json
{
  "oneOf": [
    {
      "required": [
        "source_kind",
        "source_ref",
        "canonical_markdown"
      ],
      "properties": {
        "final_artifact_id": {
          "const": ""
        },
        "final_artifact_digest": {
          "const": ""
        },
        "source_kind": {
          "type": "string",
          "enum": [
            "tutoring_tips",
            "creative_observation_card",
            "practice_question",
            "practice_answer",
            "grading_final_artifact",
            "weekly_practice_snapshot",
            "learning_archive"
          ]
        },
        "source_ref": {
          "type": "string",
          "minLength": 1,
          "description": "源引用，UTF-8 字节数不超过 512 / Source reference, at most 512 UTF-8 bytes"
        },
        "canonical_markdown": {
          "type": "string",
          "minLength": 1
        }
      }
    },
    {
      "required": [
        "final_artifact_id",
        "final_artifact_digest"
      ],
      "properties": {
        "source_kind": {
          "const": ""
        },
        "source_ref": {
          "const": ""
        },
        "canonical_markdown": {
          "const": ""
        },
        "final_artifact_id": {
          "type": "string",
          "minLength": 1
        },
        "final_artifact_digest": {
          "type": "string",
          "minLength": 1
        }
      }
    }
  ]
}
```

<a id="schema-k12practiceprintpaper"></a>

### PrintPaper

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `print_job_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `kind` | string (`question`, `answer`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `title` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `paper_no` | string | 是 | 预占或正式卷号；是否已打印以原生回执为准 |
| `source_digest` | string | 是 | 冻结来源内容摘要，用于确认读取同一版本 |
| `artifact_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `markdown` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicegenericprintpaper"></a>

### GenericPrintPaper

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `print_job_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `artifact_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_kind` | string (`tutoring_tips`, `creative_observation_card`, `practice_question`, `practice_answer`, `grading_final_artifact`, `weekly_practice_snapshot`, `learning_archive`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_ref` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `title` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_digest` | string | 是 | 冻结来源内容摘要，用于确认读取同一版本 |
| `markdown` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicehttppracticeprinteventreq"></a>

### HTTPPracticePrintEventReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `status` | string (`dialog_open`, `submitted`, `printed`, `cancelled`, `failed`, `outcome_unknown`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `native_job_id` | string | 否 | 系统打印任务身份；结果未知时用原身份核对 |
| `native_receipt_id` | string | 否 | 系统打印成功回执身份，禁止以接纳/对话框状态代替 |
| `printer_snapshot` | object | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `failure_kind` | string | 否 | 失败分类；失败或结果未知事件必需 |
| `failure_detail` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

源码：[scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="schema-k12practicehttppracticeprintcommitreq"></a>

### HTTPPracticePrintCommitReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `native_job_id` | string | 是 | 系统打印任务身份；结果未知时用原身份核对 |
| `native_receipt_id` | string | 是 | 系统打印成功回执身份，禁止以接纳/对话框状态代替 |
| `printer_snapshot` | object；minProperties=1 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

源码：[scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="schema-k12practicehttpsubmitreturnreq"></a>

### HTTPSubmitReturnReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `return_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `asset_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `item_ids` | array string；minLength=1 | 否 | 本次覆盖的练习项 ID；空不代表整卷 |
| `auto_match` | boolean；default=False | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

分支与条件约束：

```json
{
  "oneOf": [
    {
      "properties": {
        "auto_match": {
          "const": true
        },
        "item_ids": {
          "maxItems": 0
        }
      },
      "required": [
        "auto_match"
      ]
    },
    {
      "properties": {
        "auto_match": {
          "const": false
        },
        "item_ids": {
          "minItems": 1
        }
      },
      "required": [
        "item_ids"
      ]
    }
  ]
}
```

源码：[scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="schema-k12practicehttpgraderesultsreq"></a>

### HTTPGradeResultsReq

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `results` | array / null object | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

源码：[scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go)。

<a id="schema-k12practiceweeklypracticesettings"></a>

### WeeklyPracticeSettings

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `revision` | integer | 是 | 当前聚合修订，用于对应 revision/expected_revision CAS |
| `timezone` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `due_review_enabled` | boolean | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `textbook_consolidation_enabled` | boolean | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `textbook_consolidation_tier` | string (`less`, `standard`, `more`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `arithmetic_warmup_enabled` | boolean | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `arithmetic_minutes` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `created_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `updated_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go)。

<a id="schema-k12practicehttpweeklyplancommandrequest"></a>

### HTTPWeeklyPlanCommandRequest

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

源码：[scenarios/k12/apihttp/weekly_practice_handler.go](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="schema-k12practicecurrentplanresponse"></a>

### CurrentPlanResponse

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `plan` | [WeeklyPracticePlan](#schema-k12practiceweeklypracticeplan) / null | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practiceweeklypracticehistorysummary"></a>

### WeeklyPracticeHistorySummary

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `snapshot_id` | string | 是 | 冻结周练快照 ID，历史内容不可变 |
| `artifact_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `plan_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `iso_week_year` | integer | 是 | 该时区 ISO 周年，跨年时可与日历年不同 |
| `iso_week_number` | integer | 是 | ISO 周数 1–53 |
| `timezone` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `local_start_date` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `local_end_date` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `item_count` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `correct_count` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `wrong_count` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `needs_review_count` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `archived_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go)。

<a id="schema-k12practiceweeklyhistoryresponse"></a>

### WeeklyHistoryResponse

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | array / null [WeeklyPracticeHistorySummary](#schema-k12practiceweeklypracticehistorysummary) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `next_cursor` | string / null | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicehttpweeklyexpectedrevisionrequest"></a>

### HTTPWeeklyExpectedRevisionRequest

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `expected_revision` | integer；minimum=1 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

源码：[scenarios/k12/apihttp/weekly_practice_handler.go](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="schema-k12practiceweeklyoutputresponse"></a>

### WeeklyOutputResponse

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `snapshot` | [WeeklyPracticeSnapshot](#schema-k12practiceweeklypracticesnapshot) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `artifact` | [PrintableArtifact](#schema-k12practiceprintableartifact) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicehttpweeklysendrequest"></a>

### HTTPWeeklySendRequest

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

源码：[scenarios/k12/apihttp/weekly_practice_handler.go](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="schema-k12practicehttpweeklyattemptrequest"></a>

### HTTPWeeklyAttemptRequest

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | 是 | K12 Agent 名称；限定本次孩子的记录范围 |
| `item_id` | string | 是 | 当前练习项 ID；不得用来源错题 ID 替代 |
| `student_answer` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

源码：[scenarios/k12/apihttp/weekly_practice_handler.go](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="schema-k12practiceweeklypracticeattempt"></a>

### WeeklyPracticeAttempt

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `attempt_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `snapshot_id` | string | 是 | 冻结周练快照 ID，历史内容不可变 |
| `item_id` | string | 是 | 当前练习项 ID；不得用来源错题 ID 替代 |
| `assessment_id` | string | 是 | 单题持久评估 ID，用于关联原结果 |
| `result` | string (`correct`, `wrong`, `needs_review`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `verification_evidence` | string | 是 | 本次验证方式或证据引用 |
| `mistake_record_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `review_scheduled` | boolean | 是 | 本次错误是否已安排复习，不等于已掌握 |
| `created_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go)。

<a id="schema-k12practiceweeklyattemptresponse"></a>

### WeeklyAttemptResponse

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `attempt` | [WeeklyPracticeAttempt](#schema-k12practiceweeklypracticeattempt) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `replayed` | boolean | 是 | 同一冻结命令的持久结果重放，未重复新增副作用 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicehttpweeklymanualarithmeticrequest"></a>

### HTTPWeeklyManualArithmeticRequest

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `plan_revision` | integer；minimum=1 | 是 | 快照所绑定的计划修订或请求的当前计划 CAS |
| `item_count` | integer；minimum=1; maximum=20 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/apihttp/weekly_practice_handler.go](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="schema-k12practiceweeklyarithmeticattempt"></a>

### WeeklyArithmeticAttempt

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `attempt_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `batch_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `item_id` | string | 是 | 当前练习项 ID；不得用来源错题 ID 替代 |
| `assessment_id` | string | 是 | 单题持久评估 ID，用于关联原结果 |
| `result` | string (`correct`, `wrong`, `needs_review`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `verification_evidence` | string | 是 | 本次验证方式或证据引用 |
| `mistake_record_id` | string | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `review_scheduled` | boolean | 是 | 本次错误是否已安排复习，不等于已掌握 |
| `created_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/weekly_arithmetic.go](../../../../scenarios/k12/weekly_arithmetic.go)。

<a id="schema-k12practicearithmeticattemptresponse"></a>

### ArithmeticAttemptResponse

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `attempt` | [WeeklyArithmeticAttempt](#schema-k12practiceweeklyarithmeticattempt) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `replayed` | boolean | 是 | 同一冻结命令的持久结果重放，未重复新增副作用 |

本结构未声明的字段不属于公开契约。

<a id="schema-k12practicehttpweeklymanualtextbookrequest"></a>

### HTTPWeeklyManualTextbookRequest

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `plan_revision` | integer；minimum=1 | 是 | 快照所绑定的计划修订或请求的当前计划 CAS |
| `item_count` | integer；minimum=1; maximum=10 | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/apihttp/weekly_practice_handler.go](../../../../scenarios/k12/apihttp/weekly_practice_handler.go)。

<a id="schema-k12practiceweeklytextbookrecoveryrequest"></a>

### WeeklyTextbookRecoveryRequest

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `source_command_key` | string；minLength=1 | 是 | 原教材 prepare/refresh 命令的幂等键，须来自已有恢复上下文。 |
| `plan_revision` | integer；minimum=1 | 否 | 快照所绑定的计划修订或请求的当前计划 CAS |
| `checkpoint_sha256` | string；pattern=^[a-fA-F0-9]{64}$ | 是 | 原持久 checkpoint JSON 字节的真实 SHA-256；公开计划/快照查询不提供，使用实际小写摘要。 |
| `item_index` | integer；minimum=0; default=0 | 否 | 原 checkpoint 的 generation.items 零基索引；省略取首项，建议显式提交。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `accept_duplicate_execution` | boolean (`true`) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `source_plan_revision` | integer；minimum=1 | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `expected_plan_revision` | integer；minimum=1 | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

分支与条件约束：

```json
{
  "anyOf": [
    {
      "required": [
        "plan_revision"
      ]
    },
    {
      "required": [
        "source_plan_revision",
        "expected_plan_revision"
      ]
    }
  ]
}
```

源码：[scenarios/k12/usecase/weekly_recovery.go](../../../../scenarios/k12/usecase/weekly_recovery.go)。

<a id="schema-k12practiceweeklytextbookreinterpretationrequest"></a>

### WeeklyTextbookReinterpretationRequest

请求结构；参数也在各接口正文列出。

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `source_command_key` | string；minLength=1 | 是 | 原教材 prepare/refresh 命令的幂等键，须来自已有恢复上下文。 |
| `plan_revision` | integer；minimum=1 | 否 | 快照所绑定的计划修订或请求的当前计划 CAS |
| `checkpoint_sha256` | string；pattern=^[a-fA-F0-9]{64}$ | 是 | 原持久 checkpoint JSON 字节的真实 SHA-256；公开计划/快照查询不提供，使用实际小写摘要。 |
| `item_index` | integer；minimum=0; default=0 | 否 | 原 checkpoint 的 generation.items 零基索引；省略取首项，建议显式提交。 |
| `idempotency_key` | string；minLength=1 | 是 | 本操作唯一命令键；相同键应保持相同冻结参数，不代表未知调用可盲目重发 |
| `source_plan_revision` | integer；minimum=1 | 否 | JSON 投影字段；可选字段在无值时可能省略。 |
| `expected_plan_revision` | integer；minimum=1 | 否 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

分支与条件约束：

```json
{
  "anyOf": [
    {
      "required": [
        "plan_revision"
      ]
    },
    {
      "required": [
        "source_plan_revision",
        "expected_plan_revision"
      ]
    }
  ]
}
```

源码：[scenarios/k12/usecase/weekly_reinterpretation.go](../../../../scenarios/k12/usecase/weekly_reinterpretation.go)。

<a id="schema-k12practiceweeklypracticesavereceipt"></a>

### WeeklyPracticeSaveReceipt

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `save_receipt_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `plan_id` | string | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `plan_revision` | integer | 是 | 快照所绑定的计划修订或请求的当前计划 CAS |
| `snapshot_id` | string | 是 | 冻结周练快照 ID，历史内容不可变 |
| `practice_set_id` | string | 是 | 已加入的练习集 ID |
| `created_at` | integer | 是 | JSON 投影字段；可选字段在无值时可能省略。 |

本结构未声明的字段不属于公开契约。

源码：[scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go)。

<a id="schema-k12practiceweeklysaveresponse"></a>

### WeeklySaveResponse

| 字段 | 类型 / 约束 | 必填 | 说明 |
| --- | --- | --- | --- |
| `receipt` | [WeeklyPracticeSaveReceipt](#schema-k12practiceweeklypracticesavereceipt) | 是 | JSON 投影字段；可选字段在无值时可能省略。 |
| `replayed` | boolean | 是 | 同一冻结命令的持久结果重放，未重复新增副作用 |

本结构未声明的字段不属于公开契约。
