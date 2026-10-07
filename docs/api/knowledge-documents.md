# 知识库、文档、渲染与恢复 API

[中文 / English](knowledge-documents.en.md) · [OpenAPI](../../api/openapi.yaml)

本参考覆盖当前 main 源码与工作区未提交行为，共 26 个唯一方法/路径；不据此宣称已包含在公开安装包中。全部端点继承所选服务的 Bearer 访问令牌鉴权。

业务归属取鉴权上下文及服务绑定，不由 body/query 的 user_id 指定。公开知识库目前只支持 `default`。未装配模块时其路由不挂载（依其他已注册方法可能为 404 或 405），但 KB 关闭时 `GET documents` 仍返回空投影；异步摄取、原文件及投影能力的额外前提逐项注明。

## 生命周期与命令边界

文本索引（`pending/building/ready/failed`）、向量索引（`disabled/pending/building/retry_wait/ready/failed/cancelled`）、上传操作与执行 Job 是不同耐久事实。正文可读、HTTP 成功、上传完成，都不能替代向量就绪或实际产物完整。

| 命令 | 实际作用 |
| --- | --- |
| `ack` | 确认客户端收到操作回执，不确认索引完成。 |
| `dismiss` | 只结束接纳前失败提醒，保留审计历史。 |
| `retry` | 从保留原文件/检查点重试明确失败的文本或向量任务。 |
| `recovery` | 显式提交指纹，建立未知 OCR 页的替代决定与任务。 |
| `reindex` | 用已有正文重建索引，不从原文件再次 OCR。 |
| `reparse` | 建立解析候选代次，正式正文在候选成功前保持可读。 |

OCR/embedding 结果未知时保留已完成事实，不能盲目重发。恢复命令代表具体替代决定，不是自动重试授权。

## 接口清单

| 方法与路径 | 用途 | 挂载条件 |
| --- | --- | --- |
| `POST /api/v1/render` | 渲染Markdown并下载产物 | s.renderSvc != nil |
| `POST /api/v1/knowledge/documents` | 新增文本或异步上传原文件 | s.kb != nil |
| `GET /api/v1/knowledge/documents` | 列表、分页与来源计数 | KB启用返回分页列表；KB未启用仍注册并返回documents=[]、total=0空投影 |
| `GET /api/v1/knowledge/documents/{id}` | 读取正文及异步状态 | s.kb != nil |
| `GET /api/v1/knowledge/documents/{id}/source` | 读取保留的原文件 | s.kb != nil |
| `DELETE /api/v1/knowledge/documents/{id}` | 删除文档 | s.kb != nil |
| `POST /api/v1/knowledge/documents/{id}/reindex` | 用已有正文重建索引 | s.kb != nil |
| `POST /api/v1/knowledge/search` | 检索知识库 | s.kb != nil |
| `GET /api/v1/knowledge/metrics` | 读取进程检索聚合指标 | s.kb != nil |
| `GET /api/v1/knowledge/config` | 读取检索配置 | s.kb != nil |
| `GET /api/v1/knowledge/embedding-status` | 读取嵌入接线与就绪提示 | s.kb != nil |
| `PUT /api/v1/knowledge/config` | 全量保存检索配置 | s.kb != nil |
| `GET /api/v1/knowledge/operations` | 读取耐久上传恢复投影 | s.semanticIndex != nil |
| `POST /api/v1/knowledge/operations/{operation_id}/ack` | 确认收到上传回执 | s.semanticIndex != nil |
| `POST /api/v1/knowledge/operations/{operation_id}/dismiss` | 结束接纳前失败提醒 | s.semanticIndex != nil |
| `POST /api/v1/knowledge/documents/{id}/retry` | 重试明确失败的索引任务 | s.semanticIndex != nil |
| `GET /api/v1/knowledge/documents/{id}/recovery` | 读取未知OCR恢复快照 | s.semanticIndex != nil |
| `POST /api/v1/knowledge/documents/{id}/recovery` | 显式建立指纹绑定的OCR替代任务 | s.semanticIndex != nil |
| `POST /api/v1/knowledge/documents/{id}/reparse` | 从原文件建立新解析候选代次 | s.semanticIndex != nil |
| `GET /api/v1/knowledge/corpora/{corpus_id}/embedding-policy` | 读取嵌入策略与可用目录 | s.semanticIndex != nil |
| `POST /api/v1/knowledge/corpora/{corpus_id}/embedding-policy:apply` | 比较版本后应用嵌入选择 | s.semanticIndex != nil |
| `GET /api/v1/knowledge/jobs/{job_id}` | 读取知识任务进度与终态 | s.semanticIndex != nil |
| `POST /api/v1/knowledge/jobs/{job_id}/cancel` | 取消知识任务 | s.semanticIndex != nil |
| `POST /api/v1/documents/extract` | 无状态提取文档文本 | 始终注册 |
| `POST /api/v1/documents/preview` | 暂存原文件并取得预览令牌 | 始终注册 |
| `GET /api/v1/documents/preview/{token}` | 读取或下载临时预览文件 | 始终注册 |

## 可复制调用示例

以下以已经运行、已启用知识库的服务为前提。`BASE_URL` 指向当前本机或自行部署的云端服务，`HEXCLAW_API_TOKEN` 使用该服务的访问令牌；`lesson.pdf` 是调用者自己的文件。响应 JSON 只展示字段结构，不是本次运行记录。

```bash
BASE_URL="http://localhost:16060"
export HEXCLAW_API_TOKEN="replace-with-your-service-token"

# JSON 文本同步入库
curl --fail-with-body "$BASE_URL/api/v1/knowledge/documents" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"title":"Lesson","content":"A short lesson.","source":"manual:lesson"}'

# 原文件异步接纳：元数据在 file 前，重放同一文件保持同一键
curl --fail-with-body "$BASE_URL/api/v1/knowledge/documents" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H 'Idempotency-Key: lesson-upload-001' \
  -F 'corpus_id=default' -F 'subject=English' -F 'grade=3' \
  -F 'file=@lesson.pdf;type=application/pdf'
```

异步上传响应示例（HTTP 202）：

```json
{"operation_id":"upload_example","document_id":"doc_example","job_id":"job_example","text_index_state":"pending","vector_index_state":"pending"}
```

客户端收到身份后显式 `ack`，再读取真正的执行与产物；不要把 202、上传字节到 100% 或 `ack` 当成已建索引。

```bash
OPERATION_ID="replace-with-returned-operation-id"
DOCUMENT_ID="replace-with-returned-document-id"
JOB_ID="replace-with-returned-job-id"
curl --fail-with-body -X POST "$BASE_URL/api/v1/knowledge/operations/$OPERATION_ID/ack" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN"
curl --fail-with-body "$BASE_URL/api/v1/knowledge/jobs/$JOB_ID" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN"
curl --fail-with-body "$BASE_URL/api/v1/knowledge/documents/$DOCUMENT_ID" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN"
curl --fail-with-body "$BASE_URL/api/v1/knowledge/documents/$DOCUMENT_ID/source" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -o original.pdf
curl --fail-with-body "$BASE_URL/api/v1/knowledge/search" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"lesson","top_k":3,"source_types":["upload"]}'
```

`jobs/{job_id}` 的页与块进度在未知时为 `null`；例如失败可以携带 `failure.code="vision_model_required"` 和 `action_code="configure_default_vision_model"`。搜索响应始终使用 `results`、`total`、`query_receipts`，没有结果可以是：

```json
{"results":[],"total":0,"query_receipts":null}
```

已知失败后的普通重试、未知 OCR 的恢复、重新解析是三个不同命令。只有确定需要替代未知页时，先 GET 计划并原样提交指纹；不要自动循环重发。

```bash
# 普通明确失败重试，不带正文或原文件
curl --fail-with-body -X POST "$BASE_URL/api/v1/knowledge/documents/$DOCUMENT_ID/retry" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -H 'Idempotency-Key: lesson-retry-001'

# 只读计划；确认决定后，将其 fingerprint 填入下一条请求
curl --fail-with-body "$BASE_URL/api/v1/knowledge/documents/$DOCUMENT_ID/recovery" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN"
RECOVERY_FINGERPRINT="replace-with-current-plan-fingerprint"
curl --fail-with-body -X POST "$BASE_URL/api/v1/knowledge/documents/$DOCUMENT_ID/recovery" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -H 'Idempotency-Key: lesson-recovery-001' \
  -H 'Content-Type: application/json' -d "{\"fingerprint\":\"$RECOVERY_FINGERPRINT\"}"

# 已发布文档的解析升级：expected_generation 必须来自当前详情
EXPECTED_GENERATION=1
curl --fail-with-body -X POST "$BASE_URL/api/v1/knowledge/documents/$DOCUMENT_ID/reparse" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -H 'Idempotency-Key: lesson-reparse-001' \
  -H 'Content-Type: application/json' -d "{\"expected_generation\":$EXPECTED_GENERATION}"

# 渲染成功是二进制/文本文件；以 .pdf 实际内容核对产物
curl --fail-with-body "$BASE_URL/api/v1/render" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"content":"# Lesson\n\nA short lesson.","format":"pdf","title":"lesson","options":{"Locale":"en"}}' \
  -o lesson-rendered.pdf

# extract 只回文本；preview 只回临时 token，两者不创建知识文档
curl --fail-with-body "$BASE_URL/api/v1/documents/extract" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -F 'file=@lesson.pdf'
curl --fail-with-body "$BASE_URL/api/v1/documents/preview" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -F 'file=@lesson.pdf'
PREVIEW_TOKEN="replace-with-returned-preview-token"
curl --fail-with-body "$BASE_URL/api/v1/documents/preview/$PREVIEW_TOKEN?dl=1" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -o lesson-preview.pdf
```

其他只读入口：`GET /api/v1/knowledge/config`、`embedding-status`、`metrics`、`operations`、`corpora/default/embedding-policy`。策略修改先读取版本，再提交当前 `expected_policy_version`；`PUT config` 是六字段全量替换，示例见对应 OpenAPI 请求体。


## 1. `POST /api/v1/render`

同步确定性渲染，成功响应是目标格式文件，不是JSON。只在renderSvc已装配时注册。

实现：[`handleRender`](../../api/handler_render.go)

格式为md/html/docx/pdf/epub/odt/rtf/txt。单进程最多3个渲染；普通Pandoc超时30秒，PDF60秒。options字段采用Go公开拼法，未知字段400；相对图片路径只保留alt，绝对路径/file://会被拒绝，HTTP(S)图片经当前下载器处理，建议使用自包含data URL以保留图片。Render服务未装配时该路由不注册。

完整JSON请求体上限50 MiB（52428800字节，含data URL的Base64开销），超限返回400、`error.code=INPUT_TOO_LARGE`；输出文件上限100 MiB与该输入限制不同。成功文件携带`Content-Disposition: attachment`（title为空时使用artifact）和`X-Render-Cache-Hit: true|false`。语言优先级是Markdown frontmatter的`lang`、`options.Locale`、服务默认语言（默认zh-CN），不能仅凭请求中的Locale推断成果语言。

剥离Base64 data URL后的Markdown正文上限5 MiB（5242880 UTF-8字节）；单张远程下载图片上限10 MiB（10485760原始字节），下载默认超时5秒。两种超限同样返回400、`INPUT_TOO_LARGE`。这些不是字符数或输出文件大小限制，内联data URL仍计入完整JSON请求体预算。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | text/markdown: string, text/html: string, application/vnd.openxmlformats-officedocument.wordprocessingml.document: string, application/pdf: string, application/epub+zip: string, application/vnd.oasis.opendocument.text: string, application/rtf: string, text/plain: string; 完整文件；最多100 MiB |
| `400` | application/json: KnowledgeRenderErrorResponse; FORMAT_UNSUPPORTED / INPUT_TOO_LARGE / INVALID_INPUT（输入过大是400） |
| `413` | application/json: KnowledgeRenderErrorResponse; OUTPUT_TOO_LARGE |
| `429` | application/json: KnowledgeRenderErrorResponse; CONCURRENCY_REJECTED |
| `500` | application/json: KnowledgeRenderErrorResponse; RENDER_FAILED |
| `503` | application/json: KnowledgeRenderErrorResponse; ENGINE_MISSING |
| `504` | application/json: KnowledgeRenderErrorResponse; TIMEOUT |

### `KnowledgeRenderRequest`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `content` | string | 可省略; default="" |
| `format` | string: md, html, docx, pdf, epub, odt, rtf, txt | 必有 |
| `title` | string | 可省略; default="artifact" |
| `options` | KnowledgeRenderOptions | 可省略 |

### `KnowledgeRenderOptions`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `Locale` | string | 可省略; default="zh-CN" |
| `AllowRawHTML` | boolean | 可省略; default=false |
| `AllowRawTeX` | boolean | 可省略; default=false |

### `KnowledgeRenderErrorResponse`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `error` | KnowledgeRenderError | 必有 |

### `KnowledgeRenderError`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `code` | string: ENGINE_MISSING, FORMAT_UNSUPPORTED, INPUT_TOO_LARGE, INVALID_INPUT, OUTPUT_TOO_LARGE, TIMEOUT, RENDER_FAILED, CONCURRENCY_REJECTED | 必有 |
| `format` | string: md, html, docx, pdf, epub, odt, rtf, txt | 可省略 |
| `engine` | string | 可省略 |
| `detail` | string | 可省略 |


## 2. `POST /api/v1/knowledge/documents`

Content-Type选择两条真实契约：JSON文本同步入库返回200；multipart原文件持久接纳返回202，不在HTTP请求内解析/OCR/分块。

实现：[`handleAddDocument`](../../api/handler_knowledge.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `Idempotency-Key` | header | 否 | string; multipart必填，修剪后最多256 UTF-8字节；JSON分支不使用此字段。 |
| `corpus_id` | query | 否 | string; 仅multipart使用：file前form值优先，空时query回退，再默认default；非default返回422。JSON分支忽略此参数。 |

文件扩展名：.txt/.md/.csv/.json/.jsonl/.hexbank/.doc/.docx/.pptx/.pdf/.png/.jpg/.jpeg/.webp/.gif。200 MiB=209715200字节；multipart含额外开销最多202 MiB。所有元数据必须放file之前；后置字段不会被读取。重复相同键与相同文件可返回原身份；同键不同文件409，仍在receiving的并发重放也409。202后读取operations/job/detail，再显式ack已收到的operation_id；写出HTTP响应不等于客户端确认。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeAddedDocument; JSON文本已入库 |
| `202` | application/json: KnowledgeCreateDocumentResult; 原文件及操作/任务身份已持久接纳，尚未证明解析/索引完成 |
| `400` | application/json: KnowledgeError; 无效JSON/title/content/文件名/扩展名/元数据/幂等键 |
| `409` | application/json: KnowledgeError; knowledge_idempotency_conflict或knowledge_document_retry_requires_reupload |
| `413` | application/json: KnowledgeError; knowledge_document_too_large或multipart总大小超限 |
| `422` | application/json: KnowledgeError; knowledge_scope_unsupported |
| `500` | application/json: KnowledgeError; 文本入库失败或knowledge_ingest_internal |
| `503` | application/json: KnowledgeError; KB虽启用但异步摄取能力缺失：knowledge_ingest_unavailable |

### `KnowledgeAddTextRequest`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `title` | string | 必有; minLength=1; maxLength=200 |
| `content` | string | 必有; minLength=1 |
| `source` | string | 可省略; default="" |

### `KnowledgeUploadRequest`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `file` | string | 必有; format="binary" |
| `corpus_id` | string: default | 可省略; default="default" |
| `agent_id` | string | 可省略 |
| `learner_id` | string | 可省略 |
| `subject` | string | 可省略 |
| `grade` | string | 可省略 |

### `KnowledgeAddedDocument`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `id` | string | 必有 |
| `title` | string | 必有 |
| `source` | string | 可省略 |
| `chunk_count` | integer | 必有 |
| `created_at` | string | 必有; format="date-time" |
| `updated_at` | string | 可省略; format="date-time" |
| `status` | string | 必有 |
| `error_message` | string | 可省略 |
| `source_type` | string | 可省略 |
| `warnings` | array <string> | 可省略 |

### `KnowledgeCreateDocumentResult`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `operation_id` | string | 必有 |
| `document_id` | string | 必有 |
| `job_id` | string | 必有 |
| `text_index_state` | string: pending, building, ready, failed | 必有 |
| `vector_index_state` | string: disabled, pending, building, retry_wait, ready, failed, cancelled | 必有 |


## 3. `GET /api/v1/knowledge/documents`

KB未启用时只有documents=[]与total=0；启用时返回完整分页包。

实现：[`handleListDocuments`](../../api/handler_knowledge.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `source` | query | 否 | string; 修剪后按source精确过滤 |
| `limit` | query | 否 | integer; 缺失/非法/负数均按0，不分页；正数分页 |
| `offset` | query | 否 | integer; 缺失/非法/负数均按0；超过总数夹到total |

total是source过滤后且分页前的数量；sources按未过滤全集聚合，不是本页计数。source_type/vector_*等可选字段以当前文档投影为准。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeDocumentList / KnowledgeDisabledDocumentList; 启用或禁用投影 |
| `500` | application/json: KnowledgeError; 列表/向量投影读取失败 |

### `KnowledgeDocumentList`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `documents` | array <KnowledgeDocument> | 必有 |
| `total` | integer | 必有 |
| `limit` | integer | 必有 |
| `offset` | integer | 必有 |
| `sources` | array <KnowledgeSourceCount> | 必有 |

### `KnowledgeDocument`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `id` | string | 必有 |
| `title` | string | 必有 |
| `content` | string | 可省略 |
| `source` | string | 必有 |
| `chunk_count` | integer | 必有 |
| `created_at` | string | 必有; format="date-time" |
| `updated_at` | string | 可省略; format="date-time" |
| `status` | string | 可省略 |
| `error_message` | string | 可省略 |
| `source_type` | string | 可省略 |
| `vector_index_state` | string: disabled, pending, building, retry_wait, ready, failed, cancelled | 可省略 |
| `vector_job_id` | string | 可省略 |
| `vector_job_state` | string: queued, running, retry_wait, succeeded, failed, cancelled | 可省略 |
| `vector_job_stage` | string: extracting, ocr, chunking, text_indexing, embedding, publishing, gc | 可省略 |
| `vector_chunks_done` | integer / null | 可省略 |
| `vector_chunks_total` | integer / null | 可省略 |
| `vector_error` | string | 可省略 |
| `vector_outcome_unknown` | boolean | 可省略 |
| `text_outcome_unknown` | boolean | 可省略 |

### `KnowledgeSourceCount`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `source` | string | 必有 |
| `count` | integer | 必有 |

### `KnowledgeDisabledDocumentList`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `documents` | array <KnowledgeDocument> | 必有 |
| `total` | integer = 0 | 必有 |


## 4. `GET /api/v1/knowledge/documents/{id}`

优先返回耐久文档投影，旧文档返回Document DTO；不是只有id/title的摘要。

实现：[`handleGetDocument`](../../api/handler_knowledge.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string;  |

Markdown源图可投影为保留图的URI。text_index_state和vector_index_state分开；page/chunk统计未知时可省略或为null。旧数据page/offset缺失表示不能提供精确坐标，不可伪造。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeDocument / KnowledgeDocumentDetail; 正文与当前generation的生命周期 |
| `400` | application/json: KnowledgeError; 文档ID空 |
| `404` | application/json: KnowledgeError; 文档不存在或跨归属不可见 |
| `500` | application/json: KnowledgeError; 读取正文、源图或向量投影失败 |

`KnowledgeDocument` 字段表见前文同名 DTO。

### `KnowledgeDocumentDetail`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `id` | string | 必有 |
| `title` | string | 可省略 |
| `content` | string | 可省略 |
| `source` | string | 可省略 |
| `chunk_count` | integer | 可省略 |
| `created_at` | string | 可省略; format="date-time" |
| `updated_at` | string | 可省略; format="date-time" |
| `status` | string | 可省略 |
| `error_message` | string | 可省略 |
| `source_type` | string | 可省略 |
| `vector_index_state` | string: disabled, pending, building, retry_wait, ready, failed, cancelled | 可省略 |
| `vector_job_id` | string | 可省略 |
| `vector_job_state` | string: queued, running, retry_wait, succeeded, failed, cancelled | 可省略 |
| `vector_job_stage` | string: extracting, ocr, chunking, text_indexing, embedding, publishing, gc | 可省略 |
| `vector_chunks_done` | integer / null | 可省略 |
| `vector_chunks_total` | integer / null | 可省略 |
| `vector_error` | string | 可省略 |
| `vector_outcome_unknown` | boolean | 可省略 |
| `text_outcome_unknown` | boolean | 可省略 |
| `document_id` | string | 必有 |
| `owner_id` | string | 必有 |
| `corpus_id` | string | 必有 |
| `filename` | string | 必有 |
| `media_type` | string | 必有 |
| `sha256` | string | 必有 |
| `source_digest` | string | 必有 |
| `agent_id` | string | 必有 |
| `learner_id` | string | 必有 |
| `subject` | string | 必有 |
| `grade` | string | 必有 |
| `document_generation` | integer | 必有 |
| `size_bytes` | integer | 必有 |
| `text_index_state` | string: pending, building, ready, failed | 必有 |
| `warnings` | array / null <string> | 必有 |
| `source_spans` | array / null <KnowledgeSourceSpan> | 必有 |
| `ocr_page_route_receipts` | array / null <KnowledgeOCRPageRouteReceipt> | 必有 |
| `page_count` | integer | 可省略 |
| `pages_total` | integer | 可省略 |
| `pages_done` | integer | 可省略 |
| `chunks_total` | integer | 可省略 |
| `chunks_done` | integer | 可省略 |

### `KnowledgeSourceSpan`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `page_start` | integer | 可省略 |
| `page_end` | integer | 可省略 |
| `source_digest` | string | 可省略 |
| `source_offset_start` | integer | 可省略 |
| `source_offset_end` | integer | 可省略 |

### `KnowledgeOCRPageRouteReceipt`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `page_number` | integer | 必有 |
| `pages_total` | integer | 必有 |
| `source_digest` | string | 必有 |
| `content_digest` | string | 必有 |
| `provider` | string | 必有 |
| `model` | string | 必有 |
| `operation` | string | 必有 |
| `status` | string | 必有 |
| `fake` | boolean | 必有 |


## 5. `GET /api/v1/knowledge/documents/{id}/source`

按当前鉴权归属读取原字节，可用source_digest核对历史引用。

实现：[`handleKnowledgeDocumentSource`](../../api/handler_knowledge_source.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string;  |
| `source_digest` | query | 否 | string; 可选，非空且不等于当前SHA256时409 |
| `Range` | header | 否 | string; 标准HTTP字节范围 |
| `If-None-Match` | header | 否 | string; 标准ETag条件读取 |

实际Content-Type取保留的media_type；inline文件名经mime.FormatMediaType编码，ETag为引号包裹的SHA256。原文件删除后是410，不是可重试的下载任务。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/octet-stream: string; 原文件 |
| `206` | application/octet-stream: string; 所请求的字节范围 |
| `304` | 无响应体; 条件读取未修改，无响应体 |
| `404` | application/json: KnowledgeError; Document source not found |
| `409` | application/json: KnowledgeError; Document source has changed |
| `410` | application/json: KnowledgeError; knowledge_document_deleted |
| `416` | text/plain: string; 不满足的字节范围 |
| `500` | application/json: KnowledgeError; 源文件读取失败 |
| `503` | application/json: KnowledgeError; Document source unavailable |


## 6. `DELETE /api/v1/knowledge/documents/{id}`

删除当前归属文档并返回message。

实现：[`handleDeleteDocument`](../../api/handler_knowledge.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string;  |

删除不等于cancel；已取消/已删除文档的retry要求重新上传。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeDeleteResponse; 文档已删除 |
| `400` | application/json: KnowledgeError; 文档ID空 |
| `404` | application/json: KnowledgeError; 文档不存在 |
| `500` | application/json: KnowledgeError; 删除失败 |

### `KnowledgeDeleteResponse`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `message` | string | 必有 |


## 7. `POST /api/v1/knowledge/documents/{id}/reindex`

不接收请求体；使用已有正文重建分块，可能关联异步向量子任务。

实现：[`handleReindexDocument`](../../api/handler_knowledge.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string;  |

不是失败摄取retry，不会从原文件重新OCR；也不是保留旧代次的reparse。失败包为status/message，不是通用error字段。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeReindexResponse; 现有正文索引已重建 |
| `202` | application/json: KnowledgeReindexResponse; 已重建正文并返回向量任务job_id/job_state |
| `400` | application/json: KnowledgeError; 文档ID空 |
| `404` | application/json: KnowledgeReindexError; 文档不存在 |
| `500` | application/json: KnowledgeReindexError; 重建失败或正文为空 |

### `KnowledgeReindexResponse`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `status` | string | 必有 |
| `message` | string | 必有 |
| `id` | string | 必有 |
| `chunk_count` | integer | 必有 |
| `updated_at` | string | 必有; format="date-time" |
| `job_id` | string | 可省略 |
| `job_state` | string: queued, running, retry_wait, succeeded, failed, cancelled | 可省略 |
| `vector_index_state` | string: disabled, pending, building, retry_wait, ready, failed, cancelled | 可省略 |
| `vector_job_id` | string | 可省略 |
| `vector_job_state` | string: queued, running, retry_wait, succeeded, failed, cancelled | 可省略 |
| `vector_job_stage` | string: extracting, ocr, chunking, text_indexing, embedding, publishing, gc | 可省略 |
| `vector_chunks_done` | integer / null | 可省略 |
| `vector_chunks_total` | integer / null | 可省略 |
| `vector_error` | string | 可省略 |
| `vector_outcome_unknown` | boolean | 可省略 |
| `text_outcome_unknown` | boolean | 可省略 |

### `KnowledgeReindexError`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `status` | string = failed | 必有 |
| `message` | string | 必有 |


## 8. `POST /api/v1/knowledge/search`

显式检索返回匹配片段和实际完成的查询嵌入回执。

实现：[`handleSearchKnowledge`](../../api/handler_knowledge.go)

不会返回单数result。纯文本回退不合成向量回执；total仅本次返回数量。显式检索并不代表自动知识注入已就绪。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeSearchResponse; 可为空的results与真实query_receipts |
| `400` | application/json: KnowledgeError; JSON/query/top_k/日期格式非法 |
| `500` | application/json: KnowledgeError; 检索失败 |

### `KnowledgeSearchRequest`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `query` | string | 必有; minLength=1; maxLength=4096 |
| `top_k` | integer | 可省略; default=3; max=50 |
| `sources` | array <string> | 可省略 |
| `source_types` | array <string> | 可省略 |
| `created_after` | string | 可省略 |
| `created_before` | string | 可省略 |

### `KnowledgeSearchResponse`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `results` | array <KnowledgeSearchHit> | 必有 |
| `total` | integer | 必有 |
| `query_receipts` | array / null <KnowledgeQueryEmbeddingReceipt> | 必有 |

### `KnowledgeSearchHit`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `doc_id` | string | 必有 |
| `document_generation` | integer | 可省略 |
| `revision_id` | string | 可省略 |
| `doc_title` | string | 必有 |
| `source` | string | 可省略 |
| `chunk_id` | string | 必有 |
| `chunk_index` | integer | 必有 |
| `chunk_count` | integer | 必有 |
| `content` | string | 必有 |
| `score` | number | 必有 |
| `created_at` | string | 可省略; format="date-time" |
| `metadata` | object / null | 可省略 |
| `page_start` | integer | 可省略 |
| `page_end` | integer | 可省略 |
| `citation_digest` | string | 可省略 |
| `source_digest` | string | 可省略 |
| `source_offset_start` | integer | 可省略 |
| `source_offset_end` | integer | 可省略 |

### `KnowledgeQueryEmbeddingReceipt`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `operation` | string | 必有 |
| `status` | string | 必有 |
| `provider_id` | string | 必有 |
| `provider_name` | string | 可省略 |
| `model` | string | 必有 |
| `profile_id` | string | 必有 |
| `profile_config_hash` | string | 必有 |
| `dimension` | integer | 必有 |
| `revision_id` | string | 必有 |
| `query_digest` | string | 必有 |


## 9. `GET /api/v1/knowledge/metrics`

只读vector/fts/like、rerank与local_inference聚合，不返回查询正文。

实现：[`handleKnowledgeRetrievalMetrics`](../../api/handler_knowledge.go)

total_latency_ms和推理时长是毫秒；rate是比例。model_load_available当前不能由总耗时推断。重启后计数重置。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeRetrievalMetrics; 进程生命周期聚合快照 |
| `503` | application/json: KnowledgeError; 知识库未启用（防御性handler分支；正常KB关闭时路由不注册） |

### `KnowledgeRetrievalMetrics`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `vector` | KnowledgeRetrievalLaneMetrics | 必有 |
| `fts` | KnowledgeRetrievalLaneMetrics | 必有 |
| `like` | KnowledgeRetrievalLaneMetrics | 必有 |
| `rerank` | KnowledgeRerankMetrics | 必有 |
| `local_inference` | KnowledgeLocalInferenceMetrics | 必有 |

### `KnowledgeRetrievalLaneMetrics`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `calls` | integer | 必有 |
| `hits` | integer | 必有 |
| `empty` | integer | 必有 |
| `errors` | integer | 必有 |
| `fallbacks` | integer | 必有 |
| `total_latency_ms` | number | 必有 |
| `hit_rate` | number | 必有 |
| `fallback_rate` | number | 必有 |

### `KnowledgeRerankMetrics`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `configured` | integer | 必有 |
| `eligible` | integer | 必有 |
| `executed` | integer | 必有 |
| `succeeded` | integer | 必有 |
| `failed` | integer | 必有 |
| `skipped` | object / null | 必有 |

### `KnowledgeLocalInferenceMetrics`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `model_load_available` | boolean | 必有 |
| `operations` | object / null | 必有 |

### `KnowledgeLocalInferenceOperationMetrics`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `attempts` | integer | 必有 |
| `admitted` | integer | 必有 |
| `completed` | integer | 必有 |
| `failed` | integer | 必有 |
| `cancelled` | integer | 必有 |
| `queue_wait_total_ms` | number | 必有 |
| `queue_wait_max_ms` | number | 必有 |
| `first_output_count` | integer | 必有 |
| `first_output_total_ms` | number | 必有 |
| `first_output_max_ms` | number | 必有 |
| `generation_count` | integer | 必有 |
| `generation_total_ms` | number | 必有 |
| `generation_max_ms` | number | 必有 |
| `total_duration_ms` | number | 必有 |
| `total_duration_max_ms` | number | 必有 |


## 10. `GET /api/v1/knowledge/config`

运行字段取当前Manager；rerank_model取最近保存值，可是待重启的目标。

实现：[`handleGetKnowledgeConfig`](../../api/handler_knowledge.go)

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeConfig; 六个检索字段 |
| `503` | application/json: KnowledgeError; KB未启用的防御性分支 |

### `KnowledgeConfig`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `rerank` | boolean | 必有 |
| `rerank_model` | string | 必有 |
| `query_expand` | boolean | 必有 |
| `contextual` | boolean | 必有 |
| `min_score` | number | 必有; min=0; max=1 |
| `candidate_k` | integer | 必有; min=1; max=100 |


## 11. `GET /api/v1/knowledge/embedding-status`

返回enabled/configured/provider/model/local/ready/pulling；不是任务完成状态。

实现：[`handleKnowledgeEmbeddingStatus`](../../api/handler_knowledge_embedding.go)

本地模型通过Ollama已安装探测；非本地已配置时ready=true只是配置判断，并未真实调用Provider验证。ready=false时自动注入休眠，显式检索仍可用；pulling仅进程安装标志，不是某个文档进度。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeEmbeddingStatus; 接线状态 |

### `KnowledgeEmbeddingStatus`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `enabled` | boolean | 必有 |
| `configured` | boolean | 必有 |
| `local` | boolean | 必有 |
| `ready` | boolean | 必有 |
| `pulling` | boolean | 必有 |
| `provider` | string | 可省略 |
| `model` | string | 可省略 |


## 12. `PUT /api/v1/knowledge/config`

校验后先持久保存再替换运行配置；省略字段使用Go零值，不是部分更新。

实现：[`handlePutKnowledgeConfig`](../../api/handler_knowledge.go)

candidate_k为1..100；min_score为0..1。rerank/query_expand/contextual/min_score/candidate_k即时生效；rerank_model变更需重启，响应给rerank_model_restart_required。没有cfgWriter的嵌入式装配仅更新运行态，不能据200推断已写磁盘。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeConfigResponse; 生效值与重排器重启提示 |
| `400` | application/json: KnowledgeError; JSON/min_score/candidate_k非法 |
| `500` | application/json: KnowledgeError; 持久保存失败，运行配置保持原值 |
| `503` | application/json: KnowledgeError; KB未启用的防御性分支 |

### `KnowledgeConfigRequest`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `rerank` | boolean | 可省略; default=false |
| `rerank_model` | string | 可省略; default="" |
| `query_expand` | boolean | 可省略; default=false |
| `contextual` | boolean | 可省略; default=false |
| `min_score` | number | 可省略; default=0; min=0; max=1 |
| `candidate_k` | integer | 必有; min=1; max=100 |

### `KnowledgeConfigResponse`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `rerank` | boolean | 必有 |
| `rerank_model` | string | 必有 |
| `query_expand` | boolean | 必有 |
| `contextual` | boolean | 必有 |
| `min_score` | number | 必有; min=0; max=1 |
| `candidate_k` | integer | 必有; min=1; max=100 |
| `rerank_model_restart_required` | boolean | 必有 |


## 13. `GET /api/v1/knowledge/operations`

只读取当前身份/default知识库的持久操作，不自动重试或重新入库。

实现：[`handleKnowledgeOperations`](../../api/handler_knowledge.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `corpus_id` | query | 否 | string: default; 省略/空值为default；其他值422 knowledge_scope_unsupported。 |
| `include_history` | query | 否 | string; 只有字面值true启用历史；包含已dismiss/删除记录，不恢复旧任务 |

receiving可能没有document_id/job_id（字段仍为空字符串）；pending_response需要客户端ack。默认过滤已dismiss及已删除文档操作，include_history=true保留审计记录。上传操作和执行Job是不同身份。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeOperationsResponse; operations数组 |
| `422` | application/json: KnowledgeError; knowledge_scope_unsupported |
| `500` | application/json: KnowledgeError; 投影读取失败 |
| `503` | application/json: KnowledgeError; knowledge recovery/history unavailable |

### `KnowledgeOperationsResponse`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `operations` | array <KnowledgeOperation> | 必有 |

### `KnowledgeOperation`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `operation_id` | string | 必有 |
| `idempotency_key` | string | 可省略 |
| `document_deleted` | boolean | 可省略 |
| `job_id` | string | 必有 |
| `document_id` | string | 必有 |
| `title` | string | 必有 |
| `display_name` | string | 必有 |
| `content_digest` | string | 可省略 |
| `state` | string: receiving, pending_response, queued, running, retry_wait, succeeded, failed, cancelled | 必有 |
| `stage` | string | 必有 |
| `terminal` | boolean | 必有 |
| `error` | string | 可省略 |
| `created_at` | string | 必有; format="date-time" |
| `updated_at` | string | 必有; format="date-time" |


## 14. `POST /api/v1/knowledge/operations/{operation_id}/ack`

确认客户端已拿到操作身份；不确认解析/向量完成，不创建新任务。

实现：[`handleAcknowledgeKnowledgeOperation`](../../api/handler_knowledge.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `operation_id` | path | 是 | string;  |
| `corpus_id` | query | 否 | string: default; 省略/空值为default；其他值422 knowledge_scope_unsupported。 |

将已绑定的pending_response推进为queued；已进入其他状态的同归属操作ack不回退状态。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `204` | 无响应体; 无响应体；现有操作重复ack安全 |
| `400` | application/json: KnowledgeError; operation_id空 |
| `404` | application/json: KnowledgeError; 操作不存在 |
| `422` | application/json: KnowledgeError; knowledge_scope_unsupported |
| `500` | application/json: KnowledgeError; 确认失败 |
| `503` | application/json: KnowledgeError; acknowledgement capability unavailable |


## 15. `POST /api/v1/knowledge/operations/{operation_id}/dismiss`

仅允许failed且尚无document/job绑定的上传提醒；不删文档或取消已接纳任务。

实现：[`handleDismissKnowledgeOperation`](../../api/handler_knowledge.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `operation_id` | path | 是 | string;  |
| `corpus_id` | query | 否 | string: default; 省略/空值为default；其他值422 knowledge_scope_unsupported。 |

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `204` | 无响应体; 无响应体，保留历史审计 |
| `404` | application/json: KnowledgeError; 操作不存在 |
| `409` | application/json: KnowledgeError; knowledge_upload_dismiss_not_allowed |
| `422` | application/json: KnowledgeError; knowledge_scope_unsupported |
| `500` | application/json: KnowledgeError; dismiss失败 |
| `503` | application/json: KnowledgeError; knowledge recovery unavailable |


## 16. `POST /api/v1/knowledge/documents/{id}/retry`

不接收正文/原文件，复用保留来源及已成功页检查点；创建新的审计Job。

实现：[`handleRetryKnowledgeDocument`](../../api/handler_knowledge_semantic_index.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string;  |
| `Idempotency-Key` | header | 是 | string; 非空，修剪后最多242 UTF-8字节；同一动作重放必须保持键和document一致。 |

文本失败建立新ingest根任务；仅向量失败建立embedding子任务，不再次OCR。取消/删除/缺失来源要求重新上传。unknown OCR 409 knowledge_document_ocr_outcome_unknown，不能用新幂等键盲重发。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `202` | application/json: KnowledgeCreateDocumentResult; 重试任务已接纳，operation_id可为空 |
| `400` | application/json: KnowledgeError; 缺少/非法Idempotency-Key、fingerprint、expected_generation或JSON |
| `404` | application/json: KnowledgeError; semantic_index_not_found |
| `409` | application/json: KnowledgeError; knowledge_idempotency_conflict / knowledge_document_retry_requires_reupload / knowledge_document_retry_not_allowed / knowledge_document_ocr_outcome_unknown；reparse另含knowledge_document_embedding_outcome_unknown |
| `500` | application/json: KnowledgeError; knowledge_document_retry_internal |
| `503` | application/json: KnowledgeError; knowledge_ingest_unavailable |

`KnowledgeCreateDocumentResult` 字段表见前文同名 DTO。


## 17. `GET /api/v1/knowledge/documents/{id}/recovery`

只读失败任务、原文件摘要、已完成页与待替代调用的指纹绑定快照。

实现：[`handleKnowledgeDocumentRecovery`](../../api/handler_knowledge_source.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string;  |

仅存在明确可恢复的未知OCR页时返回计划；无该类页不是空计划成功。此GET不发送Provider请求。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeRecoveryPlan; 恢复计划 |
| `404` | application/json: KnowledgeError; 作用域/文档不存在 |
| `409` | application/json: KnowledgeError; 没有可恢复未知页、仍有活跃任务或状态冲突 |
| `500` | application/json: KnowledgeError; 计划读取失败 |
| `503` | application/json: KnowledgeError; knowledge_ingest_unavailable |

### `KnowledgeRecoveryPlan`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `document_id` | string | 必有 |
| `failed_job_id` | string | 必有 |
| `generation` | integer | 必有 |
| `source_digest` | string | 必有 |
| `completed_pages` | integer | 必有 |
| `pages_total` | integer | 必有 |
| `pages` | array / null <KnowledgeRecoveryPage> | 必有 |
| `fingerprint` | string | 必有 |

### `KnowledgeRecoveryPage`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `invocation_id` | string | 必有 |
| `page_number` | integer | 必有 |


## 18. `POST /api/v1/knowledge/documents/{id}/recovery`

提交最近GET计划的原样fingerprint，持久记录替代决定；不抹掉原未知调用。

实现：[`handleKnowledgeDocumentRecovery`](../../api/handler_knowledge_source.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string;  |
| `Idempotency-Key` | header | 是 | string; 非空，修剪后最多220 UTF-8字节 |

需要明确决定再次处理列出的未知页，可能产生新的Provider费用；已成功页继续复用。计划、任务、来源或待替代页改变会使旧指纹409；同key/同指纹重放返回已有Job，同key不同指纹409。替代调用自身未知仍会阻塞，不能循环自动恢复。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `202` | application/json: KnowledgeCreateDocumentResult; 恢复Job已接纳 |
| `400` | application/json: KnowledgeError; 缺少/非法Idempotency-Key、fingerprint、expected_generation或JSON |
| `404` | application/json: KnowledgeError; semantic_index_not_found |
| `409` | application/json: KnowledgeError; knowledge_idempotency_conflict / knowledge_document_retry_requires_reupload / knowledge_document_retry_not_allowed / knowledge_document_ocr_outcome_unknown；reparse另含knowledge_document_embedding_outcome_unknown |
| `500` | application/json: KnowledgeError; knowledge_document_retry_internal |
| `503` | application/json: KnowledgeError; knowledge_ingest_unavailable |

### `KnowledgeRecoveryRequest`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `fingerprint` | string | 必有; minLength=64; maxLength=64 |

`KnowledgeCreateDocumentResult` 字段表见前文同名 DTO。


## 19. `POST /api/v1/knowledge/documents/{id}/reparse`

提交当前expected_generation，保留正式正文/原文件/索引直到候选解析成功。

实现：[`handleReparseKnowledgeDocument`](../../api/handler_knowledge_semantic_index.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string;  |
| `Idempotency-Key` | header | 是 | string; 非空；当前reparse未施加upload/retry同样的长度上限 |

只对已indexed、原文件保留且当前generation匹配的正式文档建立候选；不是reindex。响应job_id追踪候选，详情继续显示正式代次；未知OCR或embedding阻止建立候选（409），失败不将旧正文降为处理中。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `202` | application/json: KnowledgeCreateDocumentResult; 候选Job已接纳，响应text/vector状态仍描述当前正式代次 |
| `400` | application/json: KnowledgeError; 缺少/非法Idempotency-Key、fingerprint、expected_generation或JSON |
| `404` | application/json: KnowledgeError; semantic_index_not_found |
| `409` | application/json: KnowledgeError; knowledge_idempotency_conflict / knowledge_document_retry_requires_reupload / knowledge_document_retry_not_allowed / knowledge_document_ocr_outcome_unknown；reparse另含knowledge_document_embedding_outcome_unknown |
| `500` | application/json: KnowledgeError; knowledge_document_retry_internal |
| `503` | application/json: KnowledgeError; knowledge_ingest_unavailable |

### `KnowledgeReparseRequest`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `expected_generation` | integer | 必有; min=1 |

`KnowledgeCreateDocumentResult` 字段表见前文同名 DTO。


## 20. `GET /api/v1/knowledge/corpora/{corpus_id}/embedding-policy`

当前HTTP只支持default知识库；返回策略版本、active/desired修订、进度、目录与推荐。

实现：[`handleGetKnowledgeEmbeddingPolicy`](../../api/handler_knowledge_semantic_index.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `corpus_id` | path | 是 | string;  |

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeEmbeddingPolicy; 策略读模型 |
| `400` | application/json: KnowledgeError; corpus_id空 |
| `404` | application/json: KnowledgeError; semantic_index_not_found |
| `409` | application/json: KnowledgeError; semantic_index_version_conflict |
| `422` | application/json: KnowledgeError; semantic_index_invalid_profile或knowledge_scope_unsupported |
| `500` | application/json: KnowledgeError; semantic_index_internal |

### `KnowledgeEmbeddingPolicy`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `policy_version` | integer | 必有 |
| `selection` | KnowledgeEmbeddingSelection | 必有 |
| `active_revision` | KnowledgeEmbeddingRevision / null | 必有 |
| `desired_revision` | KnowledgeEmbeddingRevision / null | 必有 |
| `indexing_activity` | KnowledgeIndexingActivity | 必有 |
| `available_profiles` | array / null <KnowledgeEmbeddingProfile> | 必有 |
| `recommendation` | KnowledgeEmbeddingRecommendation / null | 必有 |
| `catalog_version` | integer | 必有 |

### `KnowledgeEmbeddingSelection`

| kind | profile_id |
| --- | --- |
| `auto` / `disabled` | 省略或空白；非空值被拒绝。 |
| `profile` | 必填，非空白，使用目录提供的 profile ID。 |

### `KnowledgeEmbeddingProfile`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `profile_id` | string | 必有 |
| `model_name` | string | 必有 |
| `provider_id` | string | 必有 |
| `provider_name` | string | 必有 |
| `location` | string: local, cloud | 必有 |
| `capability` | string | 必有 |
| `dimension` | integer | 必有 |
| `availability` | string: installed, downloadable, downloading, connected, unavailable | 必有 |
| `display_order` | integer | 必有 |

### `KnowledgeEmbeddingRevision`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `revision_id` | string | 必有 |
| `state` | string: disabled, pending, building, retry_wait, ready, failed, cancelled | 必有 |
| `profile_config_hash` | string | 必有 |
| `profile` | KnowledgeEmbeddingProfile | 必有 |
| `chunks_done` | integer / null | 可省略 |
| `chunks_total` | integer / null | 可省略 |
| `job_id` | string / null | 可省略 |

### `KnowledgeIndexingActivity`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `state` | string: idle, building, retry_wait, failed | 必有 |
| `processing_documents` | integer | 必有 |
| `chunks_done` | integer / null | 必有 |
| `chunks_total` | integer / null | 必有 |

### `KnowledgeEmbeddingRecommendation`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `profile_id` | string / null | 必有 |
| `reason_code` | string | 必有 |
| `reason_text` | string | 必有 |


## 21. `POST /api/v1/knowledge/corpora/{corpus_id}/embedding-policy:apply`

必须提交expected_policy_version和严格selection，可能返回job_id；200不是重建完成。

实现：[`handleApplyKnowledgeEmbeddingPolicy`](../../api/handler_knowledge_semantic_index.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `corpus_id` | path | 是 | string;  |

selection是auto/profile/disabled；profile必须profile_id，其他kind不能带非空profile_id。先GET版本再apply，409 version_conflict后重新GET。仅接纳当前可执行installed/connected profile；不会自动下载模型，downloadable/downloading/unavailable会422。分支branch属于内部字段，响应JSON不含branch。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeApplyPolicyResult; 策略已接受或同选择no-op，job_id可能省略 |
| `400` | application/json: KnowledgeError; JSON/expected_policy_version非法 |
| `404` | application/json: KnowledgeError; semantic_index_not_found |
| `409` | application/json: KnowledgeError; semantic_index_version_conflict |
| `422` | application/json: KnowledgeError; semantic_index_invalid_profile或knowledge_scope_unsupported |
| `500` | application/json: KnowledgeError; semantic_index_internal |

### `KnowledgeApplyPolicyRequest`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `expected_policy_version` | integer | 必有; min=0 |
| `selection` | KnowledgeEmbeddingSelection | 必有 |

### `KnowledgeApplyPolicyResult`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `policy_version` | integer | 必有 |
| `selection` | KnowledgeEmbeddingSelection | 必有 |
| `active_revision_id` | string / null | 必有 |
| `desired_revision_id` | string / null | 必有 |
| `job_id` | string / null | 可省略 |


## 22. `GET /api/v1/knowledge/jobs/{job_id}`

只读当前归属/default知识库Job；真实页/块进度未知时是null。

实现：[`handleGetKnowledgeJob`](../../api/handler_knowledge_semantic_index.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `job_id` | path | 是 | string;  |

state: queued/running/retry_wait/succeeded/failed/cancelled。ingest根任务与embed_document子任务分别检查；正文ready不能代替向量ready。failure可能要求configure_default_vision_model；actual OCR回执提供provider/model/operation/page与摘要，内部lease/owner/request ID不出JSON。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeJob; 任务DTO |
| `400` | application/json: KnowledgeError; job_id空 |
| `404` | application/json: KnowledgeError; 不存在或跨范围 |
| `500` | application/json: KnowledgeError; 读取失败 |

### `KnowledgeJob`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `job_id` | string | 必有 |
| `parent_job_id` | string | 可省略 |
| `kind` | string: ingest, download_model, rebuild_revision, embed_document, gc | 必有 |
| `document_id` | string | 可省略 |
| `target_revision_id` | string | 可省略 |
| `state` | string: queued, running, retry_wait, succeeded, failed, cancelled | 必有 |
| `stage` | string: extracting, ocr, chunking, text_indexing, embedding, publishing, gc | 必有 |
| `pages_done` | integer / null | 必有 |
| `pages_total` | integer / null | 必有 |
| `chunks_done` | integer / null | 必有 |
| `chunks_total` | integer / null | 必有 |
| `attempt` | integer | 必有 |
| `next_attempt_at` | string / null | 可省略; format="date-time" |
| `cancel_requested` | boolean | 必有 |
| `last_error` | string | 可省略 |
| `failure` | KnowledgeJobFailure / null | 可省略 |
| `ocr_page_route_receipts` | array <KnowledgeOCRPageRouteReceipt> | 必有 |
| `created_at` | string | 必有; format="date-time" |
| `updated_at` | string | 必有; format="date-time" |

### `KnowledgeJobFailure`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `code` | string | 必有 |
| `message` | string | 必有 |
| `affected_pages` | array / null <integer> | 可省略 |
| `provider_display_name` | string | 可省略 |
| `model` | string | 可省略 |
| `action_code` | string | 可省略 |

`KnowledgeOCRPageRouteReceipt` 字段表见前文同名 DTO。


## 23. `POST /api/v1/knowledge/jobs/{job_id}/cancel`

不接收请求体；停止当前Job及适用子Job的后续执行，返回持久任务事实。

实现：[`handleCancelKnowledgeJob`](../../api/handler_knowledge_semantic_index.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `job_id` | path | 是 | string;  |

活跃queued/running/retry_wait转cancelled；已failed或已完成ingest可能保留原state并标cancel_requested。不等于删除原文件，不撤销已发生的Provider费用，也不允许以取消后重试绕过未知结果。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeJob; 任务状态及cancel_requested |
| `400` | application/json: KnowledgeError; job_id空 |
| `404` | application/json: KnowledgeError; 不存在或跨范围 |
| `500` | application/json: KnowledgeError; 取消失败 |

`KnowledgeJob` 字段表见前文同名 DTO。


## 24. `POST /api/v1/documents/extract`

multipart file输入，成功返回文本，供调用者注入上下文；不创建知识文档或任务。

实现：[`handleExtractDocument`](../../api/handler_documents.go)

文件最多200 MiB；完整multipart请求最多201 MiB，解析错误仍是400。支持.pdf/.doc/.docx/.pptx/.txt/.md/.csv/.json；.xlsx/.xls不走此端点，iWork私有格式须先自行导出支持格式。视觉内容提取依赖当前服务VLM能力，422不是成功空文本。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgeExtractResponse; 提取文本 |
| `400` | application/json: KnowledgeError; multipart/格式/文件超200MiB/解析失败 |
| `422` | application/json: KnowledgeError; 无法提取可读文本，可能附warnings |
| `500` | application/json: KnowledgeError; 读取上传失败 |

### `KnowledgeExtractResponse`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `text` | string | 必有 |
| `file_name` | string | 必有 |
| `page_count` | integer | 可省略 |
| `warnings` | array / null <string> | 可省略 |


## 25. `POST /api/v1/documents/preview`

multipart file；成功返回token，不返回解析文本、不入知识库。

实现：[`handleDocumentPreviewUpload`](../../api/handler_doc_preview.go)

最多保留最近16份，服务重启或被后续上传淘汰后token失效，无固定TTL。当前读取上限100 MiB+1但未检查超限，超大文件可能被截断后仍200，因此应传100 MiB以内并核对下载字节；规范不虚构413保证。Content-Type优先multipart header，再按扩展名推断。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/json: KnowledgePreviewToken; 内存预览token |
| `400` | application/json: KnowledgeError; multipart失败或缺少file |
| `500` | application/json: KnowledgeError; 读取失败 |

### `KnowledgePreviewUploadRequest`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `file` | string | 必有; format="binary" |

### `KnowledgePreviewToken`

| 字段 | 类型 | 是否出现 / 约束 |
| --- | --- | --- |
| `token` | string | 必有 |


## 26. `GET /api/v1/documents/preview/{token}`

继承Bearer鉴权；有效token提供原始缓存字节。

实现：[`handleDocumentPreviewGet`](../../api/handler_doc_preview.go)

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `token` | path | 是 | string;  |
| `dl` | query | 否 | string: 1; 只有字面值1切为attachment；其他/省略均inline |

不提供Range读取；不返回JSON封套；缺token是Go http.NotFound纯文本。token属于所选服务进程，切换服务或重启不能沿用。

| HTTP | 响应契约 |
| --- | --- |
| `401` | 鉴权失败：MessageError |
| `200` | application/octet-stream: string; 缓存文件字节 |
| `404` | text/plain: string; token不存在/已淘汰/服务重启，text/plain而非JSON |


## 通用错误解读

除 render 的嵌套 error 对象、reindex 的 status/message 失败包、preview GET 的纯文本 404 外，普通失败使用 `{"error":"..."}`，可附 code 和 warnings。不能将文件成功响应当作 JSON，也不能以成功状态替代索引/产物完整性。

| code | HTTP | 含义 |
| --- | --- | --- |
| `knowledge_document_invalid` | 400 | 上传字段/格式非法 |
| `knowledge_document_too_large` | 413 | 原文件字节超限 |
| `knowledge_idempotency_conflict` | 409 | 幂等键、原文件、版本或计划冲突 |
| `knowledge_scope_unsupported` | 422 | 仅支持default知识库 |
| `knowledge_ingest_unavailable` | 503 | 缺少所需摄取能力 |
| `knowledge_document_retry_invalid` | 400 | 重试/恢复/解析参数非法 |
| `knowledge_document_retry_requires_reupload` | 409 | 取消/删除/原文件缺失需重新上传 |
| `knowledge_document_retry_not_allowed` | 409 | 没有符合条件的失败任务或恢复页 |
| `knowledge_document_ocr_outcome_unknown` | 409 | 普通重试/解析不能重放未知OCR |
| `knowledge_document_embedding_outcome_unknown` | 409 | reparse不能替代未决embedding调用 |
| `knowledge_upload_dismiss_not_allowed` | 409 | 仅接纳前失败提醒可dismiss |
| `semantic_index_not_found` | 404 | 当前鉴权范围内不存在 |
| `semantic_index_invalid_profile` | 422 | profile/selection非法或不可执行 |
| `semantic_index_version_conflict` | 409 | 读取当前策略版本后再修改 |
| `semantic_index_internal` | 500 | 语义索引暂时不可用 |
| `knowledge_ingest_internal` | 500 | 文件接纳失败 |
| `knowledge_document_retry_internal` | 500 | 重试/恢复/解析失败 |

并非每个 handler 错误都有稳定 code；按 HTTP 状态与实际错误包处理，不补造缺失字段。semantic helper 解码的 JSON 命令使用 64 KiB reader，拒绝未知字段及多个/尾随 JSON 值；其他 JSON 分支使用各自请求体上限，可能忽略未知字段。
