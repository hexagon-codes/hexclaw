# Knowledge, documents, rendering, and recovery API

[中文 / English](knowledge-documents.md) · [OpenAPI](../../api/openapi.yaml)

This reference covers the current main source plus uncommitted working-tree behavior. It documents 26 unique method/path contracts; it does not imply those changes are already included in a published installer. All endpoints inherit the selected service’s Bearer authentication.

Owner scope comes from authentication/service binding, never body/query user_id. The public Knowledge corpus currently only supports `default`. A missing module means its routes are not mounted (404 or 405 depending on other registered methods), except GET documents which returns an empty projection when KB is disabled. Additional ingest/source/projection capabilities are stated per endpoint.

## Lifecycle and command boundaries

Text indexing (`pending/building/ready/failed`), vector indexing (`disabled/pending/building/retry_wait/ready/failed/cancelled`), upload operations, and execution jobs are separate durable facts. Text readiness, HTTP success, or a finished upload does not prove vector readiness or an intact artifact.

| Command | Effect |
| --- | --- |
| `ack` | Confirms client receipt; does not confirm completed indexing. |
| `dismiss` | Hides only a failed pre-acceptance notice; keeps audit history. |
| `retry` | Retries a known failed text/vector task using retained source/checkpoints. |
| `recovery` | Explicit fingerprint-bound replacement of unknown OCR page calls. |
| `reindex` | Rebuilds from existing extracted text, without source OCR. |
| `reparse` | Creates a parsing candidate generation while published content remains readable. |

Unknown OCR/embedding outcomes are preserved and must not be blindly resent. The recovery command is an explicit replacement decision, not a blanket automatic retry permission.

## Endpoints

| Method and path | Operation | Availability |
| --- | --- | --- |
| `POST /api/v1/render` | Render Markdown and download the artifact | Render service configured |
| `POST /api/v1/knowledge/documents` | Add text or accept an asynchronous source upload | KB configured |
| `GET /api/v1/knowledge/documents` | List, paginate, and count document sources | KB configured; empty projection if disabled |
| `GET /api/v1/knowledge/documents/{id}` | Read content and asynchronous document state | KB configured |
| `GET /api/v1/knowledge/documents/{id}/source` | Read the retained original source | KB configured |
| `DELETE /api/v1/knowledge/documents/{id}` | Delete a document | KB configured |
| `POST /api/v1/knowledge/documents/{id}/reindex` | Reindex existing extracted text | KB configured |
| `POST /api/v1/knowledge/search` | Search the knowledge base | KB configured |
| `GET /api/v1/knowledge/metrics` | Read process-lifetime retrieval metrics | KB configured |
| `GET /api/v1/knowledge/config` | Read retrieval settings | KB configured |
| `GET /api/v1/knowledge/embedding-status` | Read embedding wiring and readiness | KB configured |
| `PUT /api/v1/knowledge/config` | Replace retrieval settings | KB configured |
| `GET /api/v1/knowledge/operations` | Read durable upload operation projections | SemanticIndex configured |
| `POST /api/v1/knowledge/operations/{operation_id}/ack` | Acknowledge receipt of an upload response | SemanticIndex configured |
| `POST /api/v1/knowledge/operations/{operation_id}/dismiss` | Dismiss a failed pre-acceptance upload notice | SemanticIndex configured |
| `POST /api/v1/knowledge/documents/{id}/retry` | Retry a known failed indexing job | SemanticIndex configured |
| `GET /api/v1/knowledge/documents/{id}/recovery` | Read a recovery plan for unknown OCR outcomes | SemanticIndex configured |
| `POST /api/v1/knowledge/documents/{id}/recovery` | Explicitly enqueue fingerprint-bound replacement OCR work | SemanticIndex configured |
| `POST /api/v1/knowledge/documents/{id}/reparse` | Create a new parsing candidate from retained source | SemanticIndex configured |
| `GET /api/v1/knowledge/corpora/{corpus_id}/embedding-policy` | Read embedding policy and the available profile catalog | SemanticIndex configured |
| `POST /api/v1/knowledge/corpora/{corpus_id}/embedding-policy:apply` | Apply an embedding selection with optimistic version checking | SemanticIndex configured |
| `GET /api/v1/knowledge/jobs/{job_id}` | Read knowledge job progress and outcome | SemanticIndex configured |
| `POST /api/v1/knowledge/jobs/{job_id}/cancel` | Cancel a knowledge job | SemanticIndex configured |
| `POST /api/v1/documents/extract` | Extract document text without persistence | Always registered |
| `POST /api/v1/documents/preview` | Temporarily cache a source file and obtain its preview token | Always registered |
| `GET /api/v1/documents/preview/{token}` | Read or download a cached preview file | Always registered |

## Copyable request examples

Use a running service with Knowledge enabled. `BASE_URL` points to your local or self-hosted cloud service; `HEXCLAW_API_TOKEN` is that service’s access token, and `lesson.pdf` is your own file. JSON responses illustrate structure rather than recording an execution in this documentation task.

```bash
BASE_URL="http://localhost:16060"
export HEXCLAW_API_TOKEN="replace-with-your-service-token"

# Synchronous JSON text ingestion
curl --fail-with-body "$BASE_URL/api/v1/knowledge/documents" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"title":"Lesson","content":"A short lesson.","source":"manual:lesson"}'

# Asynchronous file acceptance: metadata precedes file; replay keeps the same key
curl --fail-with-body "$BASE_URL/api/v1/knowledge/documents" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H 'Idempotency-Key: lesson-upload-001' \
  -F 'corpus_id=default' -F 'subject=English' -F 'grade=3' \
  -F 'file=@lesson.pdf;type=application/pdf'
```

Example asynchronous upload response (HTTP 202):

```json
{"operation_id":"upload_example","document_id":"doc_example","job_id":"job_example","text_index_state":"pending","vector_index_state":"pending"}
```

Explicitly acknowledge received identities, then read execution state and actual artifacts. Neither 202, 100% uploaded bytes, nor ack proves completed indexing.

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

Unknown page/chunk progress is null. A job failure may carry failure.code="vision_model_required" and action_code="configure_default_vision_model". Search always returns results, total, and query_receipts; an empty result can be:

```json
{"results":[],"total":0,"query_receipts":null}
```

Retrying a known failure, recovering unknown OCR, and reparsing are distinct commands. Only after deciding to replace unknown-page work should you read a recovery plan and submit its exact fingerprint; do not retry it in an automatic loop.

```bash
# Retry a known failure without content or original file
curl --fail-with-body -X POST "$BASE_URL/api/v1/knowledge/documents/$DOCUMENT_ID/retry" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -H 'Idempotency-Key: lesson-retry-001'

# Read the plan; after an explicit decision, use its fingerprint in the next command
curl --fail-with-body "$BASE_URL/api/v1/knowledge/documents/$DOCUMENT_ID/recovery" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN"
RECOVERY_FINGERPRINT="replace-with-current-plan-fingerprint"
curl --fail-with-body -X POST "$BASE_URL/api/v1/knowledge/documents/$DOCUMENT_ID/recovery" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -H 'Idempotency-Key: lesson-recovery-001' \
  -H 'Content-Type: application/json' -d "{\"fingerprint\":\"$RECOVERY_FINGERPRINT\"}"

# Upgrade published parsing: expected_generation must come from current detail
EXPECTED_GENERATION=1
curl --fail-with-body -X POST "$BASE_URL/api/v1/knowledge/documents/$DOCUMENT_ID/reparse" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -H 'Idempotency-Key: lesson-reparse-001' \
  -H 'Content-Type: application/json' -d "{\"expected_generation\":$EXPECTED_GENERATION}"

# Render success is a file; verify the actual PDF bytes
curl --fail-with-body "$BASE_URL/api/v1/render" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"content":"# Lesson\n\nA short lesson.","format":"pdf","title":"lesson","options":{"Locale":"en"}}' \
  -o lesson-rendered.pdf

# extract returns text; preview returns a temporary token; neither ingests knowledge
curl --fail-with-body "$BASE_URL/api/v1/documents/extract" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -F 'file=@lesson.pdf'
curl --fail-with-body "$BASE_URL/api/v1/documents/preview" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -F 'file=@lesson.pdf'
PREVIEW_TOKEN="replace-with-returned-preview-token"
curl --fail-with-body "$BASE_URL/api/v1/documents/preview/$PREVIEW_TOKEN?dl=1" \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" -o lesson-preview.pdf
```

Other read-only endpoints: GET /api/v1/knowledge/config, embedding-status, metrics, operations, and corpora/default/embedding-policy. Read policy version before applying expected_policy_version. PUT config replaces all six settings; see the corresponding OpenAPI request example.


## 1. `POST /api/v1/render`

Synchronous deterministic rendering. Success returns the requested file, not JSON. Mounted only when renderSvc is configured.

Implementation: [`handleRender`](../../api/handler_render.go)

Formats: md/html/docx/pdf/epub/odt/rtf/txt. Up to three renders per process; normal Pandoc timeout is 30 seconds and PDF is 60 seconds. Options use the exported Go field names; unknown fields produce 400. Relative image references become alt text, absolute/file:// references are rejected, and HTTP(S) images use the existing downloader. Self-contained data URLs preserve image content. The route is absent without a render service.

The complete JSON body is limited to 50 MiB (52428800 bytes), including Base64 data-URL overhead. Oversize input returns 400 with `error.code=INPUT_TOO_LARGE`; the separate output-file limit is 100 MiB. File responses include `Content-Disposition: attachment` (artifact when title is empty) and `X-Render-Cache-Hit: true|false`. Locale precedence is Markdown frontmatter `lang`, `options.Locale`, then the service default (zh-CN by default); the requested Locale alone does not determine the artifact language.

Markdown text after removing Base64 data URLs is limited to 5 MiB (5242880 UTF-8 bytes). Each remotely downloaded image is limited to 10 MiB (10485760 original bytes), with a default 5-second download timeout. These oversize errors also return 400 with `INPUT_TOO_LARGE`. They are byte limits rather than character/output-file limits; inline data URLs still consume the complete JSON-body budget.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | text/markdown: string, text/html: string, application/vnd.openxmlformats-officedocument.wordprocessingml.document: string, application/pdf: string, application/epub+zip: string, application/vnd.oasis.opendocument.text: string, application/rtf: string, text/plain: string; Successful payload/file; read the command-specific completion semantics. |
| `400` | application/json: KnowledgeRenderErrorResponse; Invalid JSON, fields, upload, or input limits described above. |
| `413` | application/json: KnowledgeRenderErrorResponse; Original-upload or rendered-output byte limit exceeded. |
| `429` | application/json: KnowledgeRenderErrorResponse; Render concurrency rejected. |
| `500` | application/json: KnowledgeRenderErrorResponse; Processing, storage, or projection failed; inspect the documented failure shape. |
| `503` | application/json: KnowledgeRenderErrorResponse; Required module/capability/engine unavailable. |
| `504` | application/json: KnowledgeRenderErrorResponse; Render timeout. |

### `KnowledgeRenderRequest`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `content` | string | Optional; default="" |
| `format` | string: md, html, docx, pdf, epub, odt, rtf, txt | Required |
| `title` | string | Optional; default="artifact" |
| `options` | KnowledgeRenderOptions | Optional |

### `KnowledgeRenderOptions`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `Locale` | string | Optional; default="zh-CN" |
| `AllowRawHTML` | boolean | Optional; default=false |
| `AllowRawTeX` | boolean | Optional; default=false |

### `KnowledgeRenderErrorResponse`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `error` | KnowledgeRenderError | Required |

### `KnowledgeRenderError`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `code` | string: ENGINE_MISSING, FORMAT_UNSUPPORTED, INPUT_TOO_LARGE, INVALID_INPUT, OUTPUT_TOO_LARGE, TIMEOUT, RENDER_FAILED, CONCURRENCY_REJECTED | Required |
| `format` | string: md, html, docx, pdf, epub, odt, rtf, txt | Optional |
| `engine` | string | Optional |
| `detail` | string | Optional |


## 2. `POST /api/v1/knowledge/documents`

Content-Type selects two contracts: synchronous JSON text returns 200; durable multipart source acceptance returns 202 without parsing/OCR/chunking in the HTTP request.

Implementation: [`handleAddDocument`](../../api/handler_knowledge.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `Idempotency-Key` | header | No | string; Required for multipart only; max 256 UTF-8 bytes after trimming. |
| `corpus_id` | query | No | string; Multipart only: a form value before file wins, followed by query fallback, then default. Other corpus aliases produce 422. JSON ignores this query parameter. |

Extensions: .txt/.md/.csv/.json/.jsonl/.hexbank/.doc/.docx/.pptx/.pdf/.png/.jpg/.jpeg/.webp/.gif. File bytes are limited to 200 MiB (209715200); the multipart request allows 202 MiB with overhead. Put every metadata field before file; later parts are not read. The same key/file can return original identities; a different payload or a concurrent receiving replay returns 409. After 202, read operations/job/detail and explicitly acknowledge the received operation_id. Writing an HTTP response does not itself acknowledge client receipt.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeAddedDocument; Successful payload/file; read the command-specific completion semantics. |
| `202` | application/json: KnowledgeCreateDocumentResult; Accepted asynchronous work; does not prove indexing completion. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `409` | application/json: KnowledgeError; State, idempotency, version, source-digest, or unknown-outcome conflict; see command notes and error codes. |
| `413` | application/json: KnowledgeError; Original-upload or rendered-output byte limit exceeded. |
| `422` | application/json: KnowledgeError; Unsupported corpus/profile, invalid selection, or empty extracted text, as applicable. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |
| `503` | application/json: KnowledgeError; Required module/capability/engine unavailable. |

### `KnowledgeAddTextRequest`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `title` | string | Required; minLength=1; maxLength=200 |
| `content` | string | Required; minLength=1 |
| `source` | string | Optional; default="" |

### `KnowledgeUploadRequest`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `file` | string | Required; format="binary" |
| `corpus_id` | string: default | Optional; default="default" |
| `agent_id` | string | Optional |
| `learner_id` | string | Optional |
| `subject` | string | Optional |
| `grade` | string | Optional |

### `KnowledgeAddedDocument`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `id` | string | Required |
| `title` | string | Required |
| `source` | string | Optional |
| `chunk_count` | integer | Required |
| `created_at` | string | Required; format="date-time" |
| `updated_at` | string | Optional; format="date-time" |
| `status` | string | Required |
| `error_message` | string | Optional |
| `source_type` | string | Optional |
| `warnings` | array <string> | Optional |

### `KnowledgeCreateDocumentResult`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `operation_id` | string | Required |
| `document_id` | string | Required |
| `job_id` | string | Required |
| `text_index_state` | string: pending, building, ready, failed | Required |
| `vector_index_state` | string: disabled, pending, building, retry_wait, ready, failed, cancelled | Required |


## 3. `GET /api/v1/knowledge/documents`

When KB is disabled, only documents=[] and total=0 are returned; an enabled KB returns the full paging envelope.

Implementation: [`handleListDocuments`](../../api/handler_knowledge.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `source` | query | No | string; Trimmed exact source match. |
| `limit` | query | No | integer; Missing/invalid/negative means 0 (no paging); positive means page size. |
| `offset` | query | No | integer; Missing/invalid/negative means 0; capped at the filtered total. |

total counts the source-filtered set before paging. sources aggregates the unfiltered set, not the current page. Optional source_type/vector_* fields follow the current document projection.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeDocumentList / KnowledgeDisabledDocumentList; Successful payload/file; read the command-specific completion semantics. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |

### `KnowledgeDocumentList`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `documents` | array <KnowledgeDocument> | Required |
| `total` | integer | Required |
| `limit` | integer | Required |
| `offset` | integer | Required |
| `sources` | array <KnowledgeSourceCount> | Required |

### `KnowledgeDocument`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `id` | string | Required |
| `title` | string | Required |
| `content` | string | Optional |
| `source` | string | Required |
| `chunk_count` | integer | Required |
| `created_at` | string | Required; format="date-time" |
| `updated_at` | string | Optional; format="date-time" |
| `status` | string | Optional |
| `error_message` | string | Optional |
| `source_type` | string | Optional |
| `vector_index_state` | string: disabled, pending, building, retry_wait, ready, failed, cancelled | Optional |
| `vector_job_id` | string | Optional |
| `vector_job_state` | string: queued, running, retry_wait, succeeded, failed, cancelled | Optional |
| `vector_job_stage` | string: extracting, ocr, chunking, text_indexing, embedding, publishing, gc | Optional |
| `vector_chunks_done` | integer / null | Optional |
| `vector_chunks_total` | integer / null | Optional |
| `vector_error` | string | Optional |
| `vector_outcome_unknown` | boolean | Optional |
| `text_outcome_unknown` | boolean | Optional |

### `KnowledgeSourceCount`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `source` | string | Required |
| `count` | integer | Required |

### `KnowledgeDisabledDocumentList`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `documents` | array <KnowledgeDocument> | Required |
| `total` | integer = 0 | Required |


## 4. `GET /api/v1/knowledge/documents/{id}`

Returns a durable document projection when available, or the legacy Document DTO; content is included when available.

Implementation: [`handleGetDocument`](../../api/handler_knowledge.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `id` | path | Yes | string; Scoped resource ID from the corresponding service response. |

Markdown source images may be projected to retained image URIs. Text and vector states are separate. Unknown page/chunk progress can be omitted or null. Missing legacy page/offset coordinates do not authorize invented citations.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeDocument / KnowledgeDocumentDetail; Successful payload/file; read the command-specific completion semantics. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `404` | application/json: KnowledgeError; Resource/token absent or inaccessible in the current scope. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |

`KnowledgeDocument` uses the DTO table already defined above.

### `KnowledgeDocumentDetail`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `id` | string | Required |
| `title` | string | Optional |
| `content` | string | Optional |
| `source` | string | Optional |
| `chunk_count` | integer | Optional |
| `created_at` | string | Optional; format="date-time" |
| `updated_at` | string | Optional; format="date-time" |
| `status` | string | Optional |
| `error_message` | string | Optional |
| `source_type` | string | Optional |
| `vector_index_state` | string: disabled, pending, building, retry_wait, ready, failed, cancelled | Optional |
| `vector_job_id` | string | Optional |
| `vector_job_state` | string: queued, running, retry_wait, succeeded, failed, cancelled | Optional |
| `vector_job_stage` | string: extracting, ocr, chunking, text_indexing, embedding, publishing, gc | Optional |
| `vector_chunks_done` | integer / null | Optional |
| `vector_chunks_total` | integer / null | Optional |
| `vector_error` | string | Optional |
| `vector_outcome_unknown` | boolean | Optional |
| `text_outcome_unknown` | boolean | Optional |
| `document_id` | string | Required |
| `owner_id` | string | Required |
| `corpus_id` | string | Required |
| `filename` | string | Required |
| `media_type` | string | Required |
| `sha256` | string | Required |
| `source_digest` | string | Required |
| `agent_id` | string | Required |
| `learner_id` | string | Required |
| `subject` | string | Required |
| `grade` | string | Required |
| `document_generation` | integer | Required |
| `size_bytes` | integer | Required |
| `text_index_state` | string: pending, building, ready, failed | Required |
| `warnings` | array / null <string> | Required |
| `source_spans` | array / null <KnowledgeSourceSpan> | Required |
| `ocr_page_route_receipts` | array / null <KnowledgeOCRPageRouteReceipt> | Required |
| `page_count` | integer | Optional |
| `pages_total` | integer | Optional |
| `pages_done` | integer | Optional |
| `chunks_total` | integer | Optional |
| `chunks_done` | integer | Optional |

### `KnowledgeSourceSpan`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `page_start` | integer | Optional |
| `page_end` | integer | Optional |
| `source_digest` | string | Optional |
| `source_offset_start` | integer | Optional |
| `source_offset_end` | integer | Optional |

### `KnowledgeOCRPageRouteReceipt`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `page_number` | integer | Required |
| `pages_total` | integer | Required |
| `source_digest` | string | Required |
| `content_digest` | string | Required |
| `provider` | string | Required |
| `model` | string | Required |
| `operation` | string | Required |
| `status` | string | Required |
| `fake` | boolean | Required |


## 5. `GET /api/v1/knowledge/documents/{id}/source`

Reads original bytes in the authenticated owner scope; source_digest can verify an earlier citation.

Implementation: [`handleKnowledgeDocumentSource`](../../api/handler_knowledge_source.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `id` | path | Yes | string; Scoped resource ID from the corresponding service response. |
| `source_digest` | query | No | string; Nonempty expected SHA256; mismatch gives 409. |
| `Range` | header | No | string; Standard HTTP byte range. |
| `If-None-Match` | header | No | string; Standard conditional ETag read. |

Actual Content-Type is the retained media_type. Content-Disposition is inline with an encoded filename; ETag is the quoted SHA256. Deleted originals produce 410 and are not a retryable download job.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/octet-stream: string; Successful payload/file; read the command-specific completion semantics. |
| `206` | application/octet-stream: string; Requested byte range. |
| `304` | No body; Unmodified conditional file read; no body. |
| `404` | application/json: KnowledgeError; Resource/token absent or inaccessible in the current scope. |
| `409` | application/json: KnowledgeError; State, idempotency, version, source-digest, or unknown-outcome conflict; see command notes and error codes. |
| `410` | application/json: KnowledgeError; Original source was deleted. |
| `416` | text/plain: string; Unsatisfiable byte range. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |
| `503` | application/json: KnowledgeError; Required module/capability/engine unavailable. |


## 6. `DELETE /api/v1/knowledge/documents/{id}`

Deletes the scoped document and returns a message.

Implementation: [`handleDeleteDocument`](../../api/handler_knowledge.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `id` | path | Yes | string; Scoped resource ID from the corresponding service response. |

Deletion differs from cancellation. Retrying a cancelled/deleted document requires a new upload.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeDeleteResponse; Successful payload/file; read the command-specific completion semantics. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `404` | application/json: KnowledgeError; Resource/token absent or inaccessible in the current scope. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |

### `KnowledgeDeleteResponse`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `message` | string | Required |


## 7. `POST /api/v1/knowledge/documents/{id}/reindex`

Takes no request body. Rebuilds chunks from retained extracted content and may attach an asynchronous vector child job.

Implementation: [`handleReindexDocument`](../../api/handler_knowledge.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `id` | path | Yes | string; Scoped resource ID from the corresponding service response. |

This is not failed-ingest retry and does not repeat source OCR. It also differs from generation-preserving reparse. Failures use status/message rather than the normal error field.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeReindexResponse; Successful payload/file; read the command-specific completion semantics. |
| `202` | application/json: KnowledgeReindexResponse; Accepted asynchronous work; does not prove indexing completion. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `404` | application/json: KnowledgeReindexError; Resource/token absent or inaccessible in the current scope. |
| `500` | application/json: KnowledgeReindexError; Processing, storage, or projection failed; inspect the documented failure shape. |

### `KnowledgeReindexResponse`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `status` | string | Required |
| `message` | string | Required |
| `id` | string | Required |
| `chunk_count` | integer | Required |
| `updated_at` | string | Required; format="date-time" |
| `job_id` | string | Optional |
| `job_state` | string: queued, running, retry_wait, succeeded, failed, cancelled | Optional |
| `vector_index_state` | string: disabled, pending, building, retry_wait, ready, failed, cancelled | Optional |
| `vector_job_id` | string | Optional |
| `vector_job_state` | string: queued, running, retry_wait, succeeded, failed, cancelled | Optional |
| `vector_job_stage` | string: extracting, ocr, chunking, text_indexing, embedding, publishing, gc | Optional |
| `vector_chunks_done` | integer / null | Optional |
| `vector_chunks_total` | integer / null | Optional |
| `vector_error` | string | Optional |
| `vector_outcome_unknown` | boolean | Optional |
| `text_outcome_unknown` | boolean | Optional |

### `KnowledgeReindexError`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `status` | string = failed | Required |
| `message` | string | Required |


## 8. `POST /api/v1/knowledge/search`

Explicit search returns matching chunks and receipts for query embeddings that actually completed.

Implementation: [`handleSearchKnowledge`](../../api/handler_knowledge.go)

There is no singular result property. Lexical fallback does not synthesize a vector receipt. total is the returned hit count. Explicit search success does not prove automatic knowledge injection readiness.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeSearchResponse; Successful payload/file; read the command-specific completion semantics. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |

### `KnowledgeSearchRequest`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `query` | string | Required; minLength=1; maxLength=4096 |
| `top_k` | integer | Optional; default=3; max=50 |
| `sources` | array <string> | Optional |
| `source_types` | array <string> | Optional |
| `created_after` | string | Optional |
| `created_before` | string | Optional |

### `KnowledgeSearchResponse`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `results` | array <KnowledgeSearchHit> | Required |
| `total` | integer | Required |
| `query_receipts` | array / null <KnowledgeQueryEmbeddingReceipt> | Required |

### `KnowledgeSearchHit`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `doc_id` | string | Required |
| `document_generation` | integer | Optional |
| `revision_id` | string | Optional |
| `doc_title` | string | Required |
| `source` | string | Optional |
| `chunk_id` | string | Required |
| `chunk_index` | integer | Required |
| `chunk_count` | integer | Required |
| `content` | string | Required |
| `score` | number | Required |
| `created_at` | string | Optional; format="date-time" |
| `metadata` | object / null | Optional |
| `page_start` | integer | Optional |
| `page_end` | integer | Optional |
| `citation_digest` | string | Optional |
| `source_digest` | string | Optional |
| `source_offset_start` | integer | Optional |
| `source_offset_end` | integer | Optional |

### `KnowledgeQueryEmbeddingReceipt`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `operation` | string | Required |
| `status` | string | Required |
| `provider_id` | string | Required |
| `provider_name` | string | Optional |
| `model` | string | Required |
| `profile_id` | string | Required |
| `profile_config_hash` | string | Required |
| `dimension` | integer | Required |
| `revision_id` | string | Required |
| `query_digest` | string | Required |


## 9. `GET /api/v1/knowledge/metrics`

Read-only vector/fts/like, rerank, and local_inference aggregates without query content.

Implementation: [`handleKnowledgeRetrievalMetrics`](../../api/handler_knowledge.go)

Latency fields are milliseconds and rates are ratios. model_load_available cannot be inferred from total duration. Counters reset when the service process restarts.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeRetrievalMetrics; Successful payload/file; read the command-specific completion semantics. |
| `503` | application/json: KnowledgeError; Required module/capability/engine unavailable. |

### `KnowledgeRetrievalMetrics`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `vector` | KnowledgeRetrievalLaneMetrics | Required |
| `fts` | KnowledgeRetrievalLaneMetrics | Required |
| `like` | KnowledgeRetrievalLaneMetrics | Required |
| `rerank` | KnowledgeRerankMetrics | Required |
| `local_inference` | KnowledgeLocalInferenceMetrics | Required |

### `KnowledgeRetrievalLaneMetrics`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `calls` | integer | Required |
| `hits` | integer | Required |
| `empty` | integer | Required |
| `errors` | integer | Required |
| `fallbacks` | integer | Required |
| `total_latency_ms` | number | Required |
| `hit_rate` | number | Required |
| `fallback_rate` | number | Required |

### `KnowledgeRerankMetrics`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `configured` | integer | Required |
| `eligible` | integer | Required |
| `executed` | integer | Required |
| `succeeded` | integer | Required |
| `failed` | integer | Required |
| `skipped` | object / null | Required |

### `KnowledgeLocalInferenceMetrics`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `model_load_available` | boolean | Required |
| `operations` | object / null | Required |

### `KnowledgeLocalInferenceOperationMetrics`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `attempts` | integer | Required |
| `admitted` | integer | Required |
| `completed` | integer | Required |
| `failed` | integer | Required |
| `cancelled` | integer | Required |
| `queue_wait_total_ms` | number | Required |
| `queue_wait_max_ms` | number | Required |
| `first_output_count` | integer | Required |
| `first_output_total_ms` | number | Required |
| `first_output_max_ms` | number | Required |
| `generation_count` | integer | Required |
| `generation_total_ms` | number | Required |
| `generation_max_ms` | number | Required |
| `total_duration_ms` | number | Required |
| `total_duration_max_ms` | number | Required |


## 10. `GET /api/v1/knowledge/config`

Runtime fields come from the active manager. rerank_model is the most recently saved value and may await restart.

Implementation: [`handleGetKnowledgeConfig`](../../api/handler_knowledge.go)

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeConfig; Successful payload/file; read the command-specific completion semantics. |
| `503` | application/json: KnowledgeError; Required module/capability/engine unavailable. |

### `KnowledgeConfig`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `rerank` | boolean | Required |
| `rerank_model` | string | Required |
| `query_expand` | boolean | Required |
| `contextual` | boolean | Required |
| `min_score` | number | Required; min=0; max=1 |
| `candidate_k` | integer | Required; min=1; max=100 |


## 11. `GET /api/v1/knowledge/embedding-status`

Returns enabled/configured/provider/model/local/ready/pulling; it is not document/job completion.

Implementation: [`handleKnowledgeEmbeddingStatus`](../../api/handler_knowledge_embedding.go)

Local readiness probes whether the Ollama model is installed. A configured nonlocal provider yields ready=true as a configuration judgment without a live provider call. Automatic injection sleeps when ready=false; explicit retrieval can remain available. pulling is a process installation flag, not document progress.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeEmbeddingStatus; Successful payload/file; read the command-specific completion semantics. |

### `KnowledgeEmbeddingStatus`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `enabled` | boolean | Required |
| `configured` | boolean | Required |
| `local` | boolean | Required |
| `ready` | boolean | Required |
| `pulling` | boolean | Required |
| `provider` | string | Optional |
| `model` | string | Optional |


## 12. `PUT /api/v1/knowledge/config`

Validates, persists, then updates runtime settings. Omitted fields use Go zero values; this is not a partial update.

Implementation: [`handlePutKnowledgeConfig`](../../api/handler_knowledge.go)

candidate_k is 1..100 and min_score is 0..1. The switches, floor, and candidate pool change immediately; changing rerank_model requires restart and is reported by rerank_model_restart_required. An embedded runtime without cfgWriter only changes memory; 200 alone does not prove disk persistence.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeConfigResponse; Successful payload/file; read the command-specific completion semantics. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |
| `503` | application/json: KnowledgeError; Required module/capability/engine unavailable. |

### `KnowledgeConfigRequest`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `rerank` | boolean | Optional; default=false |
| `rerank_model` | string | Optional; default="" |
| `query_expand` | boolean | Optional; default=false |
| `contextual` | boolean | Optional; default=false |
| `min_score` | number | Optional; default=0; min=0; max=1 |
| `candidate_k` | integer | Required; min=1; max=100 |

### `KnowledgeConfigResponse`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `rerank` | boolean | Required |
| `rerank_model` | string | Required |
| `query_expand` | boolean | Required |
| `contextual` | boolean | Required |
| `min_score` | number | Required; min=0; max=1 |
| `candidate_k` | integer | Required; min=1; max=100 |
| `rerank_model_restart_required` | boolean | Required |


## 13. `GET /api/v1/knowledge/operations`

Reads durable operations for the authenticated owner/default corpus without retrying or re-ingesting.

Implementation: [`handleKnowledgeOperations`](../../api/handler_knowledge.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `corpus_id` | query | No | string: default; Only default is supported; empty/missing query value means default; other values produce 422. |
| `include_history` | query | No | string; Only literal true includes dismissed/deleted history; does not resume old work. |

receiving may have empty document_id/job_id strings. pending_response needs a client acknowledgement. Default reads exclude dismissed and deleted-document operations; include_history=true preserves audit entries. An upload operation and an execution job have different identities.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeOperationsResponse; Successful payload/file; read the command-specific completion semantics. |
| `422` | application/json: KnowledgeError; Unsupported corpus/profile, invalid selection, or empty extracted text, as applicable. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |
| `503` | application/json: KnowledgeError; Required module/capability/engine unavailable. |

### `KnowledgeOperationsResponse`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `operations` | array <KnowledgeOperation> | Required |

### `KnowledgeOperation`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `operation_id` | string | Required |
| `idempotency_key` | string | Optional |
| `document_deleted` | boolean | Optional |
| `job_id` | string | Required |
| `document_id` | string | Required |
| `title` | string | Required |
| `display_name` | string | Required |
| `content_digest` | string | Optional |
| `state` | string: receiving, pending_response, queued, running, retry_wait, succeeded, failed, cancelled | Required |
| `stage` | string | Required |
| `terminal` | boolean | Required |
| `error` | string | Optional |
| `created_at` | string | Required; format="date-time" |
| `updated_at` | string | Required; format="date-time" |


## 14. `POST /api/v1/knowledge/operations/{operation_id}/ack`

Confirms that the client received the operation identity; it neither confirms indexing completion nor starts a new job.

Implementation: [`handleAcknowledgeKnowledgeOperation`](../../api/handler_knowledge.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `operation_id` | path | Yes | string; Scoped resource ID from the corresponding service response. |
| `corpus_id` | query | No | string: default; Only default is supported; empty/missing query value means default; other values produce 422. |

Moves a bound pending_response operation to queued. Acknowledging an existing operation in another state does not move it backwards.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `204` | No body; Successful command with an empty response body. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `404` | application/json: KnowledgeError; Resource/token absent or inaccessible in the current scope. |
| `422` | application/json: KnowledgeError; Unsupported corpus/profile, invalid selection, or empty extracted text, as applicable. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |
| `503` | application/json: KnowledgeError; Required module/capability/engine unavailable. |


## 15. `POST /api/v1/knowledge/operations/{operation_id}/dismiss`

Only a failed upload without document/job bindings may be dismissed. This does not delete a document or cancel an accepted job.

Implementation: [`handleDismissKnowledgeOperation`](../../api/handler_knowledge.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `operation_id` | path | Yes | string; Scoped resource ID from the corresponding service response. |
| `corpus_id` | query | No | string: default; Only default is supported; empty/missing query value means default; other values produce 422. |

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `204` | No body; Successful command with an empty response body. |
| `404` | application/json: KnowledgeError; Resource/token absent or inaccessible in the current scope. |
| `409` | application/json: KnowledgeError; State, idempotency, version, source-digest, or unknown-outcome conflict; see command notes and error codes. |
| `422` | application/json: KnowledgeError; Unsupported corpus/profile, invalid selection, or empty extracted text, as applicable. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |
| `503` | application/json: KnowledgeError; Required module/capability/engine unavailable. |


## 16. `POST /api/v1/knowledge/documents/{id}/retry`

Takes no body or source file. Reuses retained source and completed page checkpoints while creating a new auditable job.

Implementation: [`handleRetryKnowledgeDocument`](../../api/handler_knowledge_semantic_index.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `id` | path | Yes | string; Scoped resource ID from the corresponding service response. |
| `Idempotency-Key` | header | Yes | string; Required; max 242 UTF-8 bytes after trimming. |

Text failures create a new ingest root; embedding-only failures create a child without repeating OCR. Cancelled/deleted/missing sources require upload again. Unknown OCR returns 409 knowledge_document_ocr_outcome_unknown; a fresh key does not make blind replay safe.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `202` | application/json: KnowledgeCreateDocumentResult; Accepted asynchronous work; does not prove indexing completion. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `404` | application/json: KnowledgeError; Resource/token absent or inaccessible in the current scope. |
| `409` | application/json: KnowledgeError; State, idempotency, version, source-digest, or unknown-outcome conflict; see command notes and error codes. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |
| `503` | application/json: KnowledgeError; Required module/capability/engine unavailable. |

`KnowledgeCreateDocumentResult` uses the DTO table already defined above.


## 17. `GET /api/v1/knowledge/documents/{id}/recovery`

Read-only snapshot of a failed job, source digest, completed pages, unknown page calls, and a bound fingerprint.

Implementation: [`handleKnowledgeDocumentRecovery`](../../api/handler_knowledge_source.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `id` | path | Yes | string; Scoped resource ID from the corresponding service response. |

Returns a plan only when eligible unknown OCR pages exist; no such pages is not an empty successful plan. GET sends no provider request.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeRecoveryPlan; Successful payload/file; read the command-specific completion semantics. |
| `404` | application/json: KnowledgeError; Resource/token absent or inaccessible in the current scope. |
| `409` | application/json: KnowledgeError; State, idempotency, version, source-digest, or unknown-outcome conflict; see command notes and error codes. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |
| `503` | application/json: KnowledgeError; Required module/capability/engine unavailable. |

### `KnowledgeRecoveryPlan`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `document_id` | string | Required |
| `failed_job_id` | string | Required |
| `generation` | integer | Required |
| `source_digest` | string | Required |
| `completed_pages` | integer | Required |
| `pages_total` | integer | Required |
| `pages` | array / null <KnowledgeRecoveryPage> | Required |
| `fingerprint` | string | Required |

### `KnowledgeRecoveryPage`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `invocation_id` | string | Required |
| `page_number` | integer | Required |


## 18. `POST /api/v1/knowledge/documents/{id}/recovery`

Submits the fingerprint from the latest GET plan and durably records replacement decisions without erasing the original unknown invocation.

Implementation: [`handleKnowledgeDocumentRecovery`](../../api/handler_knowledge_source.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `id` | path | Yes | string; Scoped resource ID from the corresponding service response. |
| `Idempotency-Key` | header | Yes | string; Required; max 220 UTF-8 bytes after trimming. |

This is an explicit decision to process the listed unknown pages again and may incur another provider charge. Completed pages are reused. A changed plan/job/source/page set causes a stale fingerprint to return 409. Same key/fingerprint replays the existing job; a changed fingerprint conflicts. If the replacement outcome is unknown, it remains blocked rather than being automatically recovered in a loop.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `202` | application/json: KnowledgeCreateDocumentResult; Accepted asynchronous work; does not prove indexing completion. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `404` | application/json: KnowledgeError; Resource/token absent or inaccessible in the current scope. |
| `409` | application/json: KnowledgeError; State, idempotency, version, source-digest, or unknown-outcome conflict; see command notes and error codes. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |
| `503` | application/json: KnowledgeError; Required module/capability/engine unavailable. |

### `KnowledgeRecoveryRequest`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `fingerprint` | string | Required; minLength=64; maxLength=64 |

`KnowledgeCreateDocumentResult` uses the DTO table already defined above.


## 19. `POST /api/v1/knowledge/documents/{id}/reparse`

Submits current expected_generation while keeping published content/source/indexes readable until the new parsing candidate succeeds.

Implementation: [`handleReparseKnowledgeDocument`](../../api/handler_knowledge_semantic_index.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `id` | path | Yes | string; Scoped resource ID from the corresponding service response. |
| `Idempotency-Key` | header | Yes | string; Required and nonblank; reparse does not currently enforce the upload/retry length limit. |

Requires an indexed source-retaining document whose current generation matches. It differs from reindex. Track the candidate by response job_id; detail continues showing the published generation. Unresolved OCR/embedding blocks the command with 409; a failed candidate does not turn old content into processing state.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `202` | application/json: KnowledgeCreateDocumentResult; Accepted asynchronous work; does not prove indexing completion. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `404` | application/json: KnowledgeError; Resource/token absent or inaccessible in the current scope. |
| `409` | application/json: KnowledgeError; State, idempotency, version, source-digest, or unknown-outcome conflict; see command notes and error codes. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |
| `503` | application/json: KnowledgeError; Required module/capability/engine unavailable. |

### `KnowledgeReparseRequest`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `expected_generation` | integer | Required; min=1 |

`KnowledgeCreateDocumentResult` uses the DTO table already defined above.


## 20. `GET /api/v1/knowledge/corpora/{corpus_id}/embedding-policy`

The current HTTP contract only supports the default corpus. Returns policy version, active/desired revisions, activity, profiles, and recommendation.

Implementation: [`handleGetKnowledgeEmbeddingPolicy`](../../api/handler_knowledge_semantic_index.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `corpus_id` | path | Yes | string; Only default is supported; empty/missing query value means default; other values produce 422. |

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeEmbeddingPolicy; Successful payload/file; read the command-specific completion semantics. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `404` | application/json: KnowledgeError; Resource/token absent or inaccessible in the current scope. |
| `409` | application/json: KnowledgeError; State, idempotency, version, source-digest, or unknown-outcome conflict; see command notes and error codes. |
| `422` | application/json: KnowledgeError; Unsupported corpus/profile, invalid selection, or empty extracted text, as applicable. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |

### `KnowledgeEmbeddingPolicy`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `policy_version` | integer | Required |
| `selection` | KnowledgeEmbeddingSelection | Required |
| `active_revision` | KnowledgeEmbeddingRevision / null | Required |
| `desired_revision` | KnowledgeEmbeddingRevision / null | Required |
| `indexing_activity` | KnowledgeIndexingActivity | Required |
| `available_profiles` | array / null <KnowledgeEmbeddingProfile> | Required |
| `recommendation` | KnowledgeEmbeddingRecommendation / null | Required |
| `catalog_version` | integer | Required |

### `KnowledgeEmbeddingSelection`

| kind | profile_id |
| --- | --- |
| `auto` / `disabled` | Omitted or empty; a nonempty value is rejected. |
| `profile` | Required nonblank profile ID from the catalog. |

### `KnowledgeEmbeddingProfile`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `profile_id` | string | Required |
| `model_name` | string | Required |
| `provider_id` | string | Required |
| `provider_name` | string | Required |
| `location` | string: local, cloud | Required |
| `capability` | string | Required |
| `dimension` | integer | Required |
| `availability` | string: installed, downloadable, downloading, connected, unavailable | Required |
| `display_order` | integer | Required |

### `KnowledgeEmbeddingRevision`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `revision_id` | string | Required |
| `state` | string: disabled, pending, building, retry_wait, ready, failed, cancelled | Required |
| `profile_config_hash` | string | Required |
| `profile` | KnowledgeEmbeddingProfile | Required |
| `chunks_done` | integer / null | Optional |
| `chunks_total` | integer / null | Optional |
| `job_id` | string / null | Optional |

### `KnowledgeIndexingActivity`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `state` | string: idle, building, retry_wait, failed | Required |
| `processing_documents` | integer | Required |
| `chunks_done` | integer / null | Required |
| `chunks_total` | integer / null | Required |

### `KnowledgeEmbeddingRecommendation`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `profile_id` | string / null | Required |
| `reason_code` | string | Required |
| `reason_text` | string | Required |


## 21. `POST /api/v1/knowledge/corpora/{corpus_id}/embedding-policy:apply`

Requires expected_policy_version and a strict selection union. May return job_id; 200 does not mean rebuild completion.

Implementation: [`handleApplyKnowledgeEmbeddingPolicy`](../../api/handler_knowledge_semantic_index.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `corpus_id` | path | Yes | string; Only default is supported; empty/missing query value means default; other values produce 422. |

selection is auto/profile/disabled. profile requires profile_id; other kinds cannot carry a nonempty profile_id. Read the version before apply, and read again after a version conflict. Only currently executable installed/connected profiles are accepted; this route does not download models. downloadable/downloading/unavailable profiles produce 422. branch is internal and absent from JSON.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeApplyPolicyResult; Successful payload/file; read the command-specific completion semantics. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `404` | application/json: KnowledgeError; Resource/token absent or inaccessible in the current scope. |
| `409` | application/json: KnowledgeError; State, idempotency, version, source-digest, or unknown-outcome conflict; see command notes and error codes. |
| `422` | application/json: KnowledgeError; Unsupported corpus/profile, invalid selection, or empty extracted text, as applicable. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |

### `KnowledgeApplyPolicyRequest`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `expected_policy_version` | integer | Required; min=0 |
| `selection` | KnowledgeEmbeddingSelection | Required |

### `KnowledgeApplyPolicyResult`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `policy_version` | integer | Required |
| `selection` | KnowledgeEmbeddingSelection | Required |
| `active_revision_id` | string / null | Required |
| `desired_revision_id` | string / null | Required |
| `job_id` | string / null | Optional |


## 22. `GET /api/v1/knowledge/jobs/{job_id}`

Reads a job in the authenticated owner/default corpus; unknown page/chunk counts are null.

Implementation: [`handleGetKnowledgeJob`](../../api/handler_knowledge_semantic_index.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `job_id` | path | Yes | string; Scoped resource ID from the corresponding service response. |

States are queued/running/retry_wait/succeeded/failed/cancelled. Check ingest roots and embed_document children separately; text ready is not vector ready. A failure may require configure_default_vision_model. Actual OCR receipts contain provider/model/operation/page/digests, while lease/owner/external request IDs stay outside JSON.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeJob; Successful payload/file; read the command-specific completion semantics. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `404` | application/json: KnowledgeError; Resource/token absent or inaccessible in the current scope. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |

### `KnowledgeJob`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `job_id` | string | Required |
| `parent_job_id` | string | Optional |
| `kind` | string: ingest, download_model, rebuild_revision, embed_document, gc | Required |
| `document_id` | string | Optional |
| `target_revision_id` | string | Optional |
| `state` | string: queued, running, retry_wait, succeeded, failed, cancelled | Required |
| `stage` | string: extracting, ocr, chunking, text_indexing, embedding, publishing, gc | Required |
| `pages_done` | integer / null | Required |
| `pages_total` | integer / null | Required |
| `chunks_done` | integer / null | Required |
| `chunks_total` | integer / null | Required |
| `attempt` | integer | Required |
| `next_attempt_at` | string / null | Optional; format="date-time" |
| `cancel_requested` | boolean | Required |
| `last_error` | string | Optional |
| `failure` | KnowledgeJobFailure / null | Optional |
| `ocr_page_route_receipts` | array <KnowledgeOCRPageRouteReceipt> | Required |
| `created_at` | string | Required; format="date-time" |
| `updated_at` | string | Required; format="date-time" |

### `KnowledgeJobFailure`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `code` | string | Required |
| `message` | string | Required |
| `affected_pages` | array / null <integer> | Optional |
| `provider_display_name` | string | Optional |
| `model` | string | Optional |
| `action_code` | string | Optional |

`KnowledgeOCRPageRouteReceipt` uses the DTO table already defined above.


## 23. `POST /api/v1/knowledge/jobs/{job_id}/cancel`

Takes no body. Stops further execution of the job and applicable children, returning durable job facts.

Implementation: [`handleCancelKnowledgeJob`](../../api/handler_knowledge_semantic_index.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `job_id` | path | Yes | string; Scoped resource ID from the corresponding service response. |

Active queued/running/retry_wait jobs become cancelled. An already failed job or completed ingest can retain its state with cancel_requested set. This does not delete source files, reverse provider charges, or make unknown outcomes safe to replay.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeJob; Successful payload/file; read the command-specific completion semantics. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `404` | application/json: KnowledgeError; Resource/token absent or inaccessible in the current scope. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |

`KnowledgeJob` uses the DTO table already defined above.


## 24. `POST /api/v1/documents/extract`

Accepts a multipart file and returns text for the caller to use as context. It creates no knowledge document/job.

Implementation: [`handleExtractDocument`](../../api/handler_documents.go)

File limit is 200 MiB and multipart body limit is 201 MiB; parse/size errors use 400. Supports .pdf/.doc/.docx/.pptx/.txt/.md/.csv/.json. .xlsx/.xls are not handled here, and iWork formats must first be exported to a supported format. Visual extraction depends on the current service VLM; 422 is not a successful empty text result.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgeExtractResponse; Successful payload/file; read the command-specific completion semantics. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `422` | application/json: KnowledgeError; Unsupported corpus/profile, invalid selection, or empty extracted text, as applicable. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |

### `KnowledgeExtractResponse`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `text` | string | Required |
| `file_name` | string | Required |
| `page_count` | integer | Optional |
| `warnings` | array / null <string> | Optional |


## 25. `POST /api/v1/documents/preview`

Accepts a multipart file and returns a token without extracting text or ingesting knowledge.

Implementation: [`handleDocumentPreviewUpload`](../../api/handler_doc_preview.go)

Retains the latest 16 files; restart or later uploads can evict a token, with no fixed TTL. Current code reads at most 100 MiB+1 bytes without an explicit oversize check: a larger file may be truncated while returning 200. Keep files within 100 MiB and verify downloaded bytes; the contract does not promise 413. Content-Type uses the part header, then extension inference.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/json: KnowledgePreviewToken; Successful payload/file; read the command-specific completion semantics. |
| `400` | application/json: KnowledgeError; Invalid JSON, fields, upload, or input limits described above. |
| `500` | application/json: KnowledgeError; Processing, storage, or projection failed; inspect the documented failure shape. |

### `KnowledgePreviewUploadRequest`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `file` | string | Required; format="binary" |

### `KnowledgePreviewToken`

| Field | Type | Presence / constraints |
| --- | --- | --- |
| `token` | string | Required |


## 26. `GET /api/v1/documents/preview/{token}`

Inherits Bearer authentication and returns cached source bytes for a valid token.

Implementation: [`handleDocumentPreviewGet`](../../api/handler_doc_preview.go)

| Parameter | Location | Required | Type / constraints |
| --- | --- | --- | --- |
| `token` | path | Yes | string; Scoped resource ID from the corresponding service response. |
| `dl` | query | No | string: 1; Only literal 1 means attachment; other/missing values mean inline. |

No Range handling and no JSON envelope. A missing token uses Go http.NotFound plain text. A token belongs to the selected service process and cannot be reused across service switches or restarts.

| HTTP | Response contract |
| --- | --- |
| `401` | Unauthorized: MessageError |
| `200` | application/octet-stream: string; Successful payload/file; read the command-specific completion semantics. |
| `404` | text/plain: string; Resource/token absent or inaccessible in the current scope. |


## Shared error interpretation

Except render’s nested error object, reindex’s status/message failure, and preview GET’s plain-text 404, ordinary failures use `{"error":"..."}` with an optional `code` and optional warnings. Never parse a binary success as JSON or use a successful status as proof of indexing/artifact completeness.

| Code | HTTP | Meaning |
| --- | --- | --- |
| `knowledge_document_invalid` | 400 | Malformed upload |
| `knowledge_document_too_large` | 413 | Original bytes exceed upload limit |
| `knowledge_idempotency_conflict` | 409 | Key/payload/version/plan conflict |
| `knowledge_scope_unsupported` | 422 | Only default corpus is supported |
| `knowledge_ingest_unavailable` | 503 | Required ingest capability is absent |
| `knowledge_document_retry_invalid` | 400 | Invalid retry/recovery/reparse arguments |
| `knowledge_document_retry_requires_reupload` | 409 | Cancelled/deleted/missing source requires upload |
| `knowledge_document_retry_not_allowed` | 409 | No eligible failed job or unknown recovery pages |
| `knowledge_document_ocr_outcome_unknown` | 409 | Ordinary retry/reparse cannot replay unknown OCR |
| `knowledge_document_embedding_outcome_unknown` | 409 | Reparse cannot replace an unresolved embedding call |
| `knowledge_upload_dismiss_not_allowed` | 409 | Only failed unaccepted notices can be dismissed |
| `semantic_index_not_found` | 404 | Not found in the authenticated scope |
| `semantic_index_invalid_profile` | 422 | Malformed/non-executable profile or selection |
| `semantic_index_version_conflict` | 409 | Read current policy version before applying |
| `semantic_index_internal` | 500 | Semantic index temporarily unavailable |
| `knowledge_ingest_internal` | 500 | File acceptance failed |
| `knowledge_document_retry_internal` | 500 | Retry/recovery/reparse failed |

Handlers do not attach a stable code to every error. Use HTTP status and the documented error shape; do not invent missing fields. JSON commands decoded by the semantic helper use a 64 KiB reader, reject unknown fields and trailing/multiple JSON values; other JSON branches use their documented body limits and may ignore unknown fields.
