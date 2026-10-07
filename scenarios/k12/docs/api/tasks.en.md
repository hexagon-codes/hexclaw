# Image tasks, material preparation and creative works API

[中文](tasks.md) · [K12 API overview](../../API.en.md) · [Complete OpenAPI contract](../../../../api/openapi.yaml)

Scope: current mainline and working-tree source. Check the actual service version and release notes for installed builds. The K12 scenario must be mounted with the required runtime dependencies. All paths below use `/api/k12`.

## Identity, format and completion

- Use `Authorization: Bearer <token>` from the connected service. A local Sidecar resolves owner identity from server composition; a cloud service resolves it from authenticated context. Body/query/header owner claims do not define identity.
- Image tasks and assets explicitly authorize owner→agent; remote queries also verify the durable task owner. Per-problem source-actions / answer-feedback derive agent from dispatch/problem: wrong owner is 404, and command permission denial after ownership is established is 403. Materials are scoped by owner/document.
- Direct `creative-works` create/list/get/generate/delete handlers currently address the supplied agent and work-record scope without a separate owner→agent authorization check. Image-backed sends additionally authorize asset ownership. These are the actual handler boundaries, not a claim that direct work endpoints have the same remote ownership checks as the image facade.
- JSON commands normally reject unknown fields and multiple JSON values with a 1 MiB body limit. Material recovery POST is an exception: its current handler decodes only the first JSON value, ignores unknown fields, and applies no dedicated body-size limit or trailing-JSON check. Image upload still limits the JSON envelope to 1 MiB; multipart source images may be up to 10 MiB. Errors use `{"error":"description"}`; do not assume a separate common `code/message` envelope.
- Times are Unix seconds, `timeout_override_ms` is milliseconds, and asset size is bytes. Read current versions, digests and IDs from resource queries rather than deriving them.
- Rare recovery commands require the original known recovery context. Current ordinary dispatch/homework projections do not expose job_version, and correction responses do not expose the unknown response recovery_response_digest. Without that context, do not substitute dispatch.version or invent a digest. This reference does not claim that public APIs expose every internal ledger query.
- The default image flow automatically classifies, recognizes, assesses and advances to an annotated original image or content terminal result. Unreadable content is a content fact, not a technical failure. `routed`, HTTP 200/202 and provider acceptance are not artifact success. The [image-task completion rules](../../API.en.md#image-task-completion) apply.
- Explicit confirmation, corrections, recovery, generation and send endpoints are conditional commands. Do not add mandatory confirmation, replacement photos or recipient selection to the default flow. Query and reconcile unknown outcomes; only an explicit recovery authorization permits a new potentially duplicated execution/charge.

## Route index

| Method | Path | Operation |
| --- | --- | --- |
| GET | `/api/k12/materials/{document_id}/preparation` | [Read material preparation details](#op-get-api-k12-materials-document-id-preparation) |
| GET | `/api/k12/materials/preparations` | [Read preparation overviews](#op-get-api-k12-materials-preparations) |
| GET | `/api/k12/materials/{document_id}/preparation/{task_id}/recovery` | [Read a material recovery plan](#op-get-api-k12-materials-document-id-preparation-task-id-recovery) |
| POST | `/api/k12/materials/{document_id}/preparation/{task_id}/recovery` | [Authorize one material recovery](#op-post-api-k12-materials-document-id-preparation-task-id-recovery) |
| POST | `/api/k12/image-tasks` | [Accept image tasks](#op-post-api-k12-image-tasks) |
| GET | `/api/k12/image-tasks/recoverable` | [Recover renderer session projections](#op-get-api-k12-image-tasks-recoverable) |
| GET | `/api/k12/image-tasks/{id}` | [Read an image-task projection](#op-get-api-k12-image-tasks-id) |
| POST | `/api/k12/image-tasks/{id}/confirm` | [Apply explicit intent or creative commands](#op-post-api-k12-image-tasks-id-confirm) |
| POST | `/api/k12/image-tasks/{id}/retry` | [Retry a proven safe image attempt](#op-post-api-k12-image-tasks-id-retry) |
| POST | `/api/k12/image-tasks/{id}/problems/{problem_id}/final-source-corrections` | [Correct source reading after completion](#op-post-api-k12-image-tasks-id-problems-problem-id-final-source-corrections) |
| POST | `/api/k12/image-tasks/{id}/recognition-recovery-attempts` | [Authorize one unknown recognition recovery](#op-post-api-k12-image-tasks-id-recognition-recovery-attempts) |
| POST | `/api/k12/image-tasks/{id}/grounding-source-recovery-attempts` | [Recover the original textbook source](#op-post-api-k12-image-tasks-id-grounding-source-recovery-attempts) |
| POST | `/api/k12/image-tasks/{id}/reparse` | [Reparse a known classification response](#op-post-api-k12-image-tasks-id-reparse) |
| POST | `/api/k12/image-tasks/{id}/cancel` | [Cancel an image task](#op-post-api-k12-image-tasks-id-cancel) |
| GET | `/api/k12/image-tasks/{id}/result` | [Read image results and receipts](#op-get-api-k12-image-tasks-id-result) |
| POST | `/api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/source-actions` | [Apply a per-problem source action](#op-post-api-k12-image-tasks-dispatch-id-problems-problem-id-source-actions) |
| POST | `/api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/answer-feedback` | [Report an adopted answer-asset error](#op-post-api-k12-image-tasks-dispatch-id-problems-problem-id-answer-feedback) |
| GET | `/api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/answer-feedback` | [Read original and effective assessments](#op-get-api-k12-image-tasks-dispatch-id-problems-problem-id-answer-feedback) |
| GET | `/api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/answer-feedback/{feedback_id}` | [Read answer-feedback progress](#op-get-api-k12-image-tasks-dispatch-id-problems-problem-id-answer-feedback-feedback-id) |
| POST | `/api/k12/creative-works` | [Create a text-only writing work](#op-post-api-k12-creative-works) |
| GET | `/api/k12/creative-works` | [List the addressed child works](#op-get-api-k12-creative-works) |
| GET | `/api/k12/creative-works/{id}` | [Read a work and feedback facts](#op-get-api-k12-creative-works-id) |
| POST | `/api/k12/creative-works/{id}/generate-feedback` | [Generate or recover one feedback generation](#op-post-api-k12-creative-works-id-generate-feedback) |
| POST | `/api/k12/creative-works/{id}/send` | [Send a work and successful feedback to phone](#op-post-api-k12-creative-works-id-send) |
| DELETE | `/api/k12/creative-works/{id}` | [Delete the current work by version](#op-delete-api-k12-creative-works-id) |
| POST | `/api/k12/assets` | [Upload a source image](#op-post-api-k12-assets) |
| GET | `/api/k12/assets/{file}` | [Download a source image](#op-get-api-k12-assets-file) |

<a id="op-get-api-k12-materials-document-id-preparation"></a>

## `GET /api/k12/materials/{document_id}/preparation`

Read material preparation details。

Reads the current owner/document source revision, item states and published answers. Layout decoration is excluded from counts, while an unknown invocation still affects summary availability. This query neither creates nor advances preparation.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `document_id` | Yes | string；Current resource identity |

Success: `200` → [K12TasksMaterialPreparationSummary](#schema-k12tasksmaterialpreparationsummary)。

Errors: `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `500` Persistence, projection or asset infrastructure failure。

<a id="op-get-api-k12-materials-preparations"></a>

## `GET /api/k12/materials/preparations`

Read preparation overviews。

Overviews return items=[] and omit source_observations; use the detail endpoint for item facts.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| query | `document_id` | No | array<string>；Repeat the query parameter; empty/duplicate IDs are removed and missing documents skipped; omission returns an empty array |

Success: `200` → [K12TasksMaterialPreparationList](#schema-k12tasksmaterialpreparationlist)。

Errors: `401` Service token or authenticated principal unavailable; `500` Persistence, projection or asset infrastructure failure。

<a id="op-get-api-k12-materials-document-id-preparation-task-id-recovery"></a>

## `GET /api/k12/materials/{document_id}/preparation/{task_id}/recovery`

Read a material recovery plan。

A plan exists only for an outcome_unknown solve_verify provider call with exactly one unresolved invocation and reusable generation receipts (plus successful visual extraction when required). It freezes source revision, digests, model and fingerprint without calling the model.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `document_id` | Yes | string；Current resource identity |
| path | `task_id` | Yes | string；Current resource identity |

Success: `200` → [K12TasksMaterialRecoveryPlan](#schema-k12tasksmaterialrecoveryplan)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `500` Persistence, projection or asset infrastructure failure。

<a id="op-post-api-k12-materials-document-id-preparation-task-id-recovery"></a>

## `POST /api/k12/materials/{document_id}/preparation/{task_id}/recovery`

Authorize one material recovery。

POST the observed fingerprint and explicitly acknowledge possible duplicate charging. The key is durable within owner scope, must be nonblank, and may contain at most 220 raw UTF-8 bytes; changing document/task/fingerprint for one key returns 409. It requeues the original task for one authorized worker attempt. Neither 202 nor a replacement invocation ID proves that an answer is ready.

This POST currently ignores unknown body fields and content after the first JSON value; it does not inherit the 1 MiB limit of other JSON commands. Callers should still send only the declared command fields in a single JSON object.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `document_id` | Yes | string；Current resource identity |
| path | `task_id` | Yes | string；Current resource identity |
| header | `Idempotency-Key` | Yes | string (minLength=1)；Durable command key; do not reuse it for a different payload |

Body: [K12TasksMaterialRecoveryCommand](#schema-k12tasksmaterialrecoverycommand) (`application/json`)。

Success: `202` → [K12TasksMaterialRecoveryResult](#schema-k12tasksmaterialrecoveryresult)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `500` Persistence, projection or asset infrastructure failure。

<a id="op-post-api-k12-image-tasks"></a>

## `POST /api/k12/image-tasks`

Accept image tasks。

Durable deduplication uses agent + source_kind + source_ref + attempt_generation. Session, images, contextual intent, creative_entry and route selection must match on replay. Automatic processing starts asynchronously. Multiple images are prevalidated and accepted per page in order; a partial error may include accepted tasks and a zero-based failed_index. Preserve accepted identities. Omit creative_entry for automatic intake; an explicit new-work entry uses kind=new_work and writing/artwork/unknown, with its explicit_commit projection.

Body: [K12TasksCreateImageTaskReq](#schema-k12taskscreateimagetaskreq) (`application/json`)。

Success: `200` → [K12TasksImageTaskAccepted](#schema-k12tasksimagetaskaccepted) / [K12TasksImageTaskBatchAccepted](#schema-k12tasksimagetaskbatchaccepted)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `413` Request or image exceeds the size limit; `502` Upstream generation or rendering failure; not an unreadable-content outcome; `503` Required route runtime is unavailable。

<a id="op-get-api-k12-image-tasks-recoverable"></a>

## `GET /api/k12/image-tasks/recoverable`

Recover renderer session projections。

Returns visible session tasks including waiting, failed and terminal facts. projection_ready means routing has ended; terminal may also describe a failure. This read never starts workers, resends a call or creates a new charge.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| query | `agent` | Yes | string；Child TutorAgent name; never an owner identity |
| query | `session` | Yes | string (minLength=1)；The source_session used at creation |

Success: `200` → [K12TasksImageTaskRecoverables](#schema-k12tasksimagetaskrecoverables)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `500` Persistence, projection or asset infrastructure failure; `503` Required route runtime is unavailable。

<a id="op-get-api-k12-image-tasks-id"></a>

## `GET /api/k12/image-tasks/{id}`

Read an image-task projection。

Poll the same dispatch. routed indicates successful classification/routing only. Inspect progress and target_projection, then fetch result for the final artifact.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `id` | Yes | string；Current resource identity |
| query | `agent` | Yes | string；Child TutorAgent name; never an owner identity |

Success: `200` → [K12TasksImageTaskResponse](#schema-k12tasksimagetaskresponse)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `502` Upstream generation or rendering failure; not an unreadable-content outcome; `503` Required route runtime is unavailable。

<a id="op-post-api-k12-image-tasks-id-confirm"></a>

## `POST /api/k12/image-tasks/{id}/confirm`

Apply explicit intent or creative commands。

A conditional explicit command, never a mandatory step in the default image flow. Submit intent only for genuine awaiting_confirmation routing. homework carries explicit corrections; creative is exclusive with homework/intent. freeze_ocr freezes a still-waiting work; segment corrections require the complete canonical_content. commit promotes explicit manual intake and rejects OCR-freeze fields. Submitted writing content must match the frozen text. Feedback does not score, rank or rewrite the work.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `id` | Yes | string；Current resource identity |

Body: [K12TasksConfirmImageTaskReq](#schema-k12tasksconfirmimagetaskreq) (`application/json`)。

Success: `200` → [K12TasksImageTaskResponse](#schema-k12tasksimagetaskresponse)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `413` Request or image exceeds the size limit; `502` Upstream generation or rendering failure; not an unreadable-content outcome; `503` Required route runtime is unavailable。

<a id="op-post-api-k12-image-tasks-id-retry"></a>

## `POST /api/k12/image-tasks/{id}/retry`

Retry a proven safe image attempt。

Retries only a durably retry-safe failed invocation, reusing frozen model/input. Unknown calls cannot be resent through this operation: inspect the original task and receipts first. The returned projection still requires artifact verification.

Omitting `intent` or using an empty value retains ordinary retry behavior. The fixed `known_local_technical` value restores only the original job bound to the current dispatch version, authenticated account and Agent. It requires `failed_terminal`, failed stage `assessing`, a count that has reached the ordinary limit of 3, and a latest physical receipt for the current input and failed generation with `failed/local/provider_response_processed` and no result. Historical failed or successful receipts do not establish eligibility; count 4 with only an old generation 3 failure (`3001`) is still rejected. In-flight, unknown or unproven reconciled receipts, any final artifact, and a successful assessing checkpoint reject recovery. Account scope comes from authentication; callers cannot replace the owner, model or task identity.

The fresh parent window and original job's `queued` state commit in one dual-version CAS transaction before scheduling. An unavailable original runtime or closed scheduler rejects without writes. The full frozen model, budget, input and invocation/assessment history remain unchanged; the current count is retained and new calls use the next generation: count 3→generation 4, or count 4 with a current generation 4 definite failure (`4001`)→generation 5. Ordinary max3 and the general terminal state machine remain unchanged. Repeating the same version returns `409`; query an uncertain result instead of submitting again. `200` means recovery was accepted and still requires the original task's final artifact and delivery receipts.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `id` | Yes | string；Current resource identity |

Body: [K12TasksImageTaskRetryReq](#schema-k12tasksimagetaskretryreq) (`application/json`)。

Success: `200` → [K12TasksImageTaskResponse](#schema-k12tasksimagetaskresponse)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `413` Request or image exceeds the size limit; `502` Upstream generation or rendering failure; not an unreadable-content outcome; `503` Required route runtime is unavailable。

<a id="op-post-api-k12-image-tasks-id-problems-problem-id-final-source-corrections"></a>

## `POST /api/k12/image-tasks/{id}/problems/{problem_id}/final-source-corrections`

Correct source reading after completion。

Independently rereads one original problem only after completion and when artifact and per-problem input identities match; it never invents a new student answer. The body key freezes command identity: new commands return 201 and replay returns 200. A 201 may still contain sent/outcome_unknown/failed; a new final artifact requires status=completed and artifact. Explicit recovery of an unknown correction requires recovery_of, its recovery_response_digest and accept_duplicate_execution=true with the same frozen input; only one recovery child is allowed.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `id` | Yes | string；Current resource identity |
| path | `problem_id` | Yes | string；Current resource identity |

Body: [K12TasksFinalSourceCorrectionInput](#schema-k12tasksfinalsourcecorrectioninput) (`application/json`)。

Success: `201,200` → [K12TasksFinalSourceCorrectionResult](#schema-k12tasksfinalsourcecorrectionresult)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `413` Request or image exceeds the size limit; `502` Upstream generation or rendering failure; not an unreadable-content outcome; `503` Required route runtime is unavailable。

<a id="op-post-api-k12-image-tasks-id-recognition-recovery-attempts"></a>

## `POST /api/k12/image-tasks/{id}/recognition-recovery-attempts`

Authorize one unknown recognition recovery。

For an outcome_unknown job with matching dispatch/job versions, authorizes one unknown primary batch or singleton repair without replacing its model, image or plan. The body key is durable; replay returns 200 without restarting the worker, while a new authorization returns 202. Omitted/zero timeout keeps the original; 180000 is allowed only for an original 120000ms primary batch within the stage budget, never for singleton repair. accept_duplicate_execution must be true. A read or ordinary retry does not provide this authorization.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `id` | Yes | string；Current resource identity |

Body: [K12TasksRecognitionRecoveryInput](#schema-k12tasksrecognitionrecoveryinput) (`application/json`)。

Success: `202,200` → [K12TasksRecognitionRecoveryResult](#schema-k12tasksrecognitionrecoveryresult)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `413` Request or image exceeds the size limit; `503` Required route runtime is unavailable。

<a id="op-post-api-k12-image-tasks-id-grounding-source-recovery-attempts"></a>

## `POST /api/k12/image-tasks/{id}/grounding-source-recovery-attempts`

Recover the original textbook source。

For an eligible failed_retryable assessment recovery, switches the original semantic retrieval to verified_text from the same owner/agent, math subject and verified textbook pages. Requires original semantic receipts, no successful conflicting-source result and no sent/outcome_unknown model call. Old receipts remain intact and unresolved calls are not resent. Body key and versions fence the operation; replay returns 200 and new acceptance 202. Query the final artifact afterward.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `id` | Yes | string；Current resource identity |

Body: [K12TasksGroundingSourceRecoveryInput](#schema-k12tasksgroundingsourcerecoveryinput) (`application/json`)。

Success: `202,200` → [K12TasksGroundingSourceRecoveryResult](#schema-k12tasksgroundingsourcerecoveryresult)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `413` Request or image exceeds the size limit; `503` Required route runtime is unavailable。

<a id="op-post-api-k12-image-tasks-id-reparse"></a>

## `POST /api/k12/image-tasks/{id}/reparse`

Reparse a known classification response。

Reparses a stored raw response only for a current retry-safe failed classification without a target. It does not call a model or independently advance downstream model work. A successful replay preserves the prior parsing fact.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `id` | Yes | string；Current resource identity |

Body: [K12TasksImageTaskVersionReq](#schema-k12tasksimagetaskversionreq) (`application/json`)。

Success: `200` → [K12TasksImageTaskResponse](#schema-k12tasksimagetaskresponse)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `413` Request or image exceeds the size limit; `502` Upstream generation or rendering failure; not an unreadable-content outcome; `503` Required route runtime is unavailable。

<a id="op-post-api-k12-image-tasks-id-cancel"></a>

## `POST /api/k12/image-tasks/{id}/cancel`

Cancel an image task。

Cancels the associated job through the same task with a matching version; an already-cancelled task can replay directly. It does not prove provider-side cancellation or refunds. Illegal transitions return 409.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `id` | Yes | string；Current resource identity |

Body: [K12TasksImageTaskVersionReq](#schema-k12tasksimagetaskversionreq) (`application/json`)。

Success: `200` → [K12TasksImageTaskResponse](#schema-k12tasksimagetaskresponse)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `413` Request or image exceeds the size limit; `503` Required route runtime is unavailable。

<a id="op-get-api-k12-image-tasks-id-result"></a>

## `GET /api/k12/image-tasks/{id}/result`

Read image results and receipts。

Read-only: 200 may contain result=null before readiness, or an unreadable creative-content terminal notice. Completed homework results include the annotated original image at result.payload.annotated_image (mime/data_base64/digest); blank worksheets use parent_teaching_guide. Unreadable content applies only to content evidence, never provider timeouts, unknown calls or infrastructure failures. operation_receipts identifies model/physical execution; source_attachments contains only digests and sizes.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `id` | Yes | string；Current resource identity |
| query | `agent` | Yes | string；Child TutorAgent name; never an owner identity |

Success: `200` → [K12TasksImageTaskResult](#schema-k12tasksimagetaskresult)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `502` Upstream generation or rendering failure; not an unreadable-content outcome; `503` Required route runtime is unavailable。

<a id="op-post-api-k12-image-tasks-dispatch-id-problems-problem-id-source-actions"></a>

## `POST /api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/source-actions`

Apply a per-problem source action。

action is correct_text/select_region/retake/skip/resume. Positive structure_version and expected_input_revision must match current facts. correct_text needs at least one canonical text; select_region uses in-bounds source pixels; retake references a ready asset of the same owner/agent; skip/resume uses {}. Command, version, source facts and frozen replay response are committed together. Replay returns the original JSON. Corrections or replacement photos are voluntary, never a default grading gate.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `dispatch_id` | Yes | string；Current resource identity |
| path | `problem_id` | Yes | string；Current resource identity |
| header | `Idempotency-Key` | Yes | string (minLength=1)；Durable command key; do not reuse it for a different payload |

Body: [K12TasksProblemSourceActionRequest](#schema-k12tasksproblemsourceactionrequest) (`application/json`)。

`payload` fields: `correct_text`: `question_canonical_markdown`, `answer_canonical_markdown`; `select_region`: `page_asset_id`, `region.{x,y,width,height}`; `retake`: `page_asset_id`; `skip` / `resume`: `{}`.

Success: `200` → [K12TasksProblemSourceActionResponse](#schema-k12tasksproblemsourceactionresponse)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `403` Command authorization denied after owner identity was established; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `413` Request or image exceeds the size limit; `422` Invalid domain payload for source action; `500` Persistence, projection or asset infrastructure failure; `503` Required route runtime is unavailable。

<a id="op-post-api-k12-image-tasks-dispatch-id-problems-problem-id-answer-feedback"></a>

## `POST /api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/answer-feedback`

Report an adopted answer-asset error。

Only kind=answer_error for a matching original job/input revision/result digest and an assessment that adopted an asset answer. The reason is feedback, not a new student answer. One transaction saves feedback, archives the matching old asset version and emits the recovery event, without archiving an independently published new version. 202 is acceptance only; query feedback and the effective assessment. Original tasks, artifacts and receipts remain intact.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `dispatch_id` | Yes | string；Current resource identity |
| path | `problem_id` | Yes | string；Current resource identity |
| header | `Idempotency-Key` | Yes | string (minLength=1)；Durable command key; do not reuse it for a different payload |

Body: [K12TasksAnswerFeedbackRequest](#schema-k12tasksanswerfeedbackrequest) (`application/json`)。

Success: `202` → [K12TasksAnswerFeedbackAccepted](#schema-k12tasksanswerfeedbackaccepted)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `403` Command authorization denied after owner identity was established; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `413` Request or image exceeds the size limit; `500` Persistence, projection or asset infrastructure failure; `503` Required route runtime is unavailable。

<a id="op-get-api-k12-image-tasks-dispatch-id-problems-problem-id-answer-feedback"></a>

## `GET /api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/answer-feedback`

Read original and effective assessments。

Derives agent/owner from dispatch/problem rather than caller identity fields. Returns job_id, original input_revision/result_digest and assessment.original/current/correction. Freeze those original identities before submitting feedback.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `dispatch_id` | Yes | string；Current resource identity |
| path | `problem_id` | Yes | string；Current resource identity |

Success: `200` → [K12TasksAnswerFeedbackContext](#schema-k12tasksanswerfeedbackcontext)。

Errors: `401` Service token or authenticated principal unavailable; `403` Command authorization denied after owner identity was established; `404` Resource absent or outside the addressed scope; `503` Required route runtime is unavailable。

<a id="op-get-api-k12-image-tasks-dispatch-id-problems-problem-id-answer-feedback-feedback-id"></a>

## `GET /api/k12/image-tasks/{dispatch_id}/problems/{problem_id}/answer-feedback/{feedback_id}`

Read answer-feedback progress。

Feedback must match the owner and dispatch/problem. status is pending/completed/unresolved/outcome_unknown; per-target outcomes are corrected/unresolved/outcome_unknown. Unresolved or unknown is not a completed correction and never becomes a new student input.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `dispatch_id` | Yes | string；Current resource identity |
| path | `problem_id` | Yes | string；Current resource identity |
| path | `feedback_id` | Yes | string；Current resource identity |

Success: `200` → [K12TasksProblemAssetFeedback](#schema-k12tasksproblemassetfeedback)。

Errors: `401` Service token or authenticated principal unavailable; `403` Command authorization denied after owner identity was established; `404` Resource absent or outside the addressed scope; `500` Persistence, projection or asset infrastructure failure; `503` Required route runtime is unavailable。

<a id="op-post-api-k12-creative-works"></a>

## `POST /api/k12/creative-works`

Create a text-only writing work。

The sole direct-create entry accepts writing and nonempty content_markdown. Image writing/art use image-tasks. The work and initial feedback generation are persisted atomically. The same key/payload replays with created=false; changing payload conflicts. A configured WorkFeedback worker starts asynchronously. 200 does not mean feedback has succeeded.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| header | `Idempotency-Key` | Yes | string (minLength=1)；Durable command key; do not reuse it for a different payload |

Body: [K12TasksCreativeWorkCreateCommand](#schema-k12taskscreativeworkcreatecommand) (`application/json`)。

Success: `200` → [K12TasksCreativeWorkCreateResult](#schema-k12taskscreativeworkcreateresult)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `413` Request or image exceeds the size limit; `502` Upstream generation or rendering failure; not an unreadable-content outcome。

<a id="op-get-api-k12-creative-works"></a>

## `GET /api/k12/creative-works`

List the addressed child works。

Returns works with initial/latest/current feedback generations. latest_generation_at reflects successful feedback and may be null. delivery_batch_id is included only for matching delivery facts.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| query | `agent` | Yes | string；Child TutorAgent name; never an owner identity |
| query | `type` | No | string；Optional writing/art exact filter; omission means all and an unmatched value returns an empty list |

Success: `200` → [K12TasksCreativeWorkList](#schema-k12taskscreativeworklist)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `500` Persistence, projection or asset infrastructure failure。

<a id="op-get-api-k12-creative-works-id"></a>

## `GET /api/k12/creative-works/{id}`

Read a work and feedback facts。

Reads by agent, work record and creative_work collection scope. An in-flight current_feedback must not overwrite already successful latest_feedback facts.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `id` | Yes | string；Current resource identity |
| query | `agent` | Yes | string；Child TutorAgent name; never an owner identity |

Success: `200` → [K12TasksCreativeWorkDTO](#schema-k12taskscreativeworkdto)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `500` Persistence, projection or asset infrastructure failure; `502` Upstream generation or rendering failure; not an unreadable-content outcome。

<a id="op-post-api-k12-creative-works-id-generate-feedback"></a>

## `POST /api/k12/creative-works/{id}/generate-feedback`

Generate or recover one feedback generation。

Durable generations deduplicate by Idempotency-Key, resuming a failed initial generation or appending one generation from frozen source/context. This handler waits for the command result; it is not a generic 202 queue endpoint. Failures persist on the generation without replacing successful latest feedback. Reconcile unknown calls rather than changing the key and resending.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `id` | Yes | string；Current resource identity |
| header | `Idempotency-Key` | Yes | string (minLength=1)；Durable command key; do not reuse it for a different payload |

Body: [K12TasksAgentCommand](#schema-k12tasksagentcommand) (`application/json`)。

Success: `200` → [K12TasksCreativeWorkDTO](#schema-k12taskscreativeworkdto)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `413` Request or image exceeds the size limit; `502` Upstream generation or rendering failure; not an unreadable-content outcome。

<a id="op-post-api-k12-creative-works-id-send"></a>

## `POST /api/k12/creative-works/{id}/send`

Send a work and successful feedback to phone。

Requires successful latest feedback and sends canonical text plus the same real source image for image works. Canonical content/attachment identities freeze the batch; duplicate identity replays the existing batch. One send targets all current effective physical direct-message bindings with deduplication and no recipient/channel selection. Neither 200 nor provider acceptance proves delivered; inspect batch/child receipts and do not blindly resend outcome_unknown.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `id` | Yes | string；Current resource identity |

Body: [K12TasksAgentCommand](#schema-k12tasksagentcommand) (`application/json`)。

Success: `200` → [K12TasksDeliveryBatch](#schema-k12tasksdeliverybatch)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `413` Request or image exceeds the size limit; `500` Persistence, projection or asset infrastructure failure; `501` Delivery capability is not configured; `502` Upstream generation or rendering failure; not an unreadable-content outcome; `503` Required route runtime is unavailable。

<a id="op-delete-api-k12-creative-works-id"></a>

## `DELETE /api/k12/creative-works/{id}`

Delete the current work by version。

Tombstones the current object with a durable command receipt. A stale version or the same key for a different object conflicts with 409. It does not revoke delivered IM messages or refund provider usage.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `id` | Yes | string；Current resource identity |
| query | `agent` | Yes | string；Child TutorAgent name; never an owner identity |
| header | `Idempotency-Key` | Yes | string (minLength=1)；Durable command key; do not reuse it for a different payload |
| header | `If-Match` | Yes | string；Positive row_version, accepted as 1, "1" or W/"1" |

Success: `200` → [K12TasksCreativeWorkDeleteResult](#schema-k12taskscreativeworkdeleteresult)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict。

<a id="op-post-api-k12-assets"></a>

## `POST /api/k12/assets`

Upload a source image。

Accepts multipart/form-data file or JSON standard data_base64, returning content-addressed asset_id and size in bytes. Multipart source images may be up to 10 MiB (10×1024×1024 bytes). The JSON branch uses the common decoder and limits the entire JSON envelope to 1 MiB, including Base64 expansion and other fields; it cannot carry a 10 MiB source image. Unknown JSON fields and extra multipart form fields are currently ignored. Supports fully decodable PNG/JPEG/GIF/WebP with matching magic and format; HEIC is unsupported. Invalid/empty images do not produce ready assets. Stable same-content identities can be reused.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| query | `agent` | No | string；Overrides multipart/JSON agent; at least one source must be nonempty |

Body: [K12TasksAssetUploadReq](#schema-k12tasksassetuploadreq) (`application/json`)。

Alternative body: [K12TasksAssetMultipart](#schema-k12tasksassetmultipart) (`multipart/form-data`)。

Success: `200` → [K12TasksAssetUploadResult](#schema-k12tasksassetuploadresult)。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `409` Version, state, idempotency identity or original evidence conflict; `413` Request or image exceeds the size limit; `415` Unsupported or not fully decodable image format; `500` Persistence, projection or asset infrastructure failure; `503` Required route runtime is unavailable。

<a id="op-get-api-k12-assets-file"></a>

## `GET /api/k12/assets/{file}`

Download a source image。

file is the final segment of asset://<agent>/<file>, never a full URL or host path. Success returns real image bytes with verified Content-Type, actual Content-Length and Cache-Control=private, max-age=86400, immutable. Wrong-owner/absent assets are 404; integrity/storage failures are 500.

| Location | Parameter | Required | Type / contract |
| --- | --- | --- | --- |
| path | `file` | Yes | string；Current resource identity |
| query | `agent` | Yes | string；Child TutorAgent name; never an owner identity |

Success: `200` → binary image bytes。

Errors: `400` Invalid JSON, fields, command identity or format; `401` Service token or authenticated principal unavailable; `404` Resource absent or outside the addressed scope; `500` Persistence, projection or asset infrastructure failure; `503` Required route runtime is unavailable。

## Complete call examples

IDs, digests and generated text below are illustrative. Set `$TOKEN` to the connected service token and `$BASE` to its address, such as `http://127.0.0.1:16060`. Keep real tokens out of repositories. Replace `TutorAgent` with an existing child assistant.

### Upload, accept and read the final artifact

```bash
curl "$BASE/api/k12/assets?agent=TutorAgent" \
  -H "Authorization: Bearer $TOKEN" \
  -F 'file=@homework.png'
```

```json
{"asset_id":"asset://TutorAgent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png","size":204800}
```

Use the actual complete asset_id returned by upload:

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

Final-homework response shape is shown below. data_base64 is an abbreviated placeholder; real responses must contain complete image bytes. routed alone is not completion: verify progress.state=completed in the task projection and the required annotated image.

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

### Explicit per-problem source corrections

Read structure_version / input_revision from the current projection and invoke only for a voluntary source correction:

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

This proves command persistence only; continue reading the final task result. For an adopted answer-asset error, read the original identity and submit feedback rather than changing the student answer:

```bash
curl "$BASE/api/k12/image-tasks/dispatch-demo/problems/problem-1/answer-feedback" -H "Authorization: Bearer $TOKEN"
curl "$BASE/api/k12/image-tasks/dispatch-demo/problems/problem-1/answer-feedback" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -H 'Idempotency-Key: answer-feedback-1' \
  -d '{"kind":"answer_error","job_id":"job-from-context","input_revision":1,"result_digest":"digest-from-context","reason":"原答案与题目不一致"}'
```

The feedback in {"created":true,"feedback":{...}} follows the named field contract below. Save the actual feedback_id, GET .../answer-feedback/{feedback_id} and verify assessment.current. A 202 alone does not close the feedback.

### Material recovery and explicit unknown-call authorization

```bash
curl "$BASE/api/k12/materials/document-1/preparation/task-1/recovery" -H "Authorization: Bearer $TOKEN"
curl "$BASE/api/k12/materials/document-1/preparation/task-1/recovery" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -H 'Idempotency-Key: material-recovery-1' \
  -d '{"fingerprint":"fingerprint-from-plan","allow_possible_duplicate_charge":true}'
```

```json
{"decision_id":"decision-1","task_id":"task-1","state":"queued"}
```

This explicitly acknowledges possible duplicate charging; do not silently provide that acknowledgment. After acceptance, read preparation state and published answers. Exact recognition-recovery body shape:

```json
{"agent":"TutorAgent","version":4,"job_version":7,"source_physical_invocation_id":"physical-from-original-receipt","idempotency_key":"recognition-recovery-1","accept_duplicate_execution":true,"timeout_override_ms":0}
```

Submit to /image-tasks/{id}/recognition-recovery-attempts only when the original recovery context is already available. Ordinary public dispatch/job projections do not expose job_version; do not substitute their version, guess it or increment it yourself. This request shape does not imply that an ordinary client can obtain every field from image-task queries alone. Do not replace the original image/model or treat a timeout as automatic resend permission.

### Text works and explicit manual image works

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

Inspect initial_feedback / current_feedback.status; successful content is in latest_feedback.feedback.projection_markdown. Omit creative_entry for automatic image processing. Only an explicit manual archive entry uses:

```json
{"agent":"TutorAgent","source_kind":"api","source_ref":"manual-art-1","source_asset_refs":["asset://TutorAgent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png"],"attempt_generation":1,"creative_entry":{"kind":"new_work","task_intent":"artwork"}}
```

Read promotion_policy=explicit_commit / commit_required before POSTing to /image-tasks/{id}/confirm:

```json
{"agent":"TutorAgent","version":2,"creative":{"action":"commit","work_title":"窗外的小树","task_requirement":"观察并绘画"}}
```

If explicit manual writing is actually awaiting OCR freeze, use the creative branch {"action":"freeze_ocr","canonical_version":1,"canonical_content":"actual full text"} with the observed version. This is not an extra confirmation for ordinary automatic images. Sending a work requires successful feedback:

```bash
curl "$BASE/api/k12/creative-works/work-1/send" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"agent":"TutorAgent"}'
curl "$BASE/api/k12/creative-works/work-1?agent=TutorAgent" \
  -X DELETE -H "Authorization: Bearer $TOKEN" -H 'If-Match: "1"' -H 'Idempotency-Key: writing-delete-1'
```

The send response follows the full DeliveryBatch field contract with child receipts. Only delivered terminal facts prove delivery. Replace If-Match in the deletion example with the actual current row_version.

## Response and command schema fields

All schema names match the complete OpenAPI document. Response "required" means a serialized key is present, not that a model result has succeeded. Command required fields follow handler validation. Omitted optional response fields may not be present; explicit null is shown in the type. Runtime state, scope and branch constraints above still apply.

<a id="schema-k12tasksagentcommand"></a>

### `K12TasksAgentCommand`

| Field | Type / values | Key required |
| --- | --- | --- |
| `agent` | string (minLength=1) | Yes |

<a id="schema-k12tasksagentinstructionssnapshot"></a>

### `K12TasksAgentInstructionsSnapshot`

| Field | Type / values | Key required |
| --- | --- | --- |
| `content` | string | Yes |
| `digest` | string | Yes |
| `source` | string | Yes |

<a id="schema-k12tasksannotatedimage"></a>

### `K12TasksAnnotatedImage`

| Field | Type / values | Key required |
| --- | --- | --- |
| `mime` | string | Yes |
| `data_base64` | string (base64) | Yes |
| `digest` | string | Yes |

<a id="schema-k12tasksanswerfeedbackaccepted"></a>

### `K12TasksAnswerFeedbackAccepted`

| Field | Type / values | Key required |
| --- | --- | --- |
| `created` | boolean | Yes |
| `feedback` | [K12TasksProblemAssetFeedback](#schema-k12tasksproblemassetfeedback) | Yes |

<a id="schema-k12tasksanswerfeedbackcontext"></a>

### `K12TasksAnswerFeedbackContext`

| Field | Type / values | Key required |
| --- | --- | --- |
| `job_id` | string | Yes |
| `input_revision` | integer | Yes |
| `result_digest` | string | Yes |
| `assessment` | [K12TasksEffectiveGradingAssessment](#schema-k12taskseffectivegradingassessment) | Yes |

<a id="schema-k12tasksanswerfeedbackrequest"></a>

### `K12TasksAnswerFeedbackRequest`

| Field | Type / values | Key required |
| --- | --- | --- |
| `input_revision` | integer (min=1) | Yes |
| `job_id` | string (minLength=1) | Yes |
| `kind` | `answer_error` | Yes |
| `reason` | string (minLength=1) | Yes |
| `result_digest` | string (minLength=1) | Yes |

<a id="schema-k12tasksanswerstate"></a>

### `K12TasksAnswerState`

`blank` / `present` / `unclear`

<a id="schema-k12tasksassetmultipart"></a>

### `K12TasksAssetMultipart`

| Field | Type / values | Key required |
| --- | --- | --- |
| `file` | string | Yes |
| `agent` | string | Optional |

<a id="schema-k12tasksassetuploadreq"></a>

### `K12TasksAssetUploadReq`

| Field | Type / values | Key required |
| --- | --- | --- |
| `agent` | string | Optional |
| `data_base64` | string (base64) | Yes |

<a id="schema-k12tasksassetuploadresult"></a>

### `K12TasksAssetUploadResult`

| Field | Type / values | Key required |
| --- | --- | --- |
| `asset_id` | string | Yes |
| `size` | integer (min=1, max=10485760) | Yes |

<a id="schema-k12tasksbboxdto"></a>

### `K12TasksBboxDTO`

| Field | Type / values | Key required |
| --- | --- | --- |
| `h` | number | Yes |
| `w` | number | Yes |
| `x` | number | Yes |
| `y` | number | Yes |

<a id="schema-k12tasksconfirmimagetaskreq"></a>

### `K12TasksConfirmImageTaskReq`

| Field | Type / values | Key required |
| --- | --- | --- |
| `agent` | string | Yes |
| `creative` | [K12TasksCreativeFreezeCommand](#schema-k12taskscreativefreezecommand) / [K12TasksCreativeCommitCommand](#schema-k12taskscreativecommitcommand) / null | Optional |
| `homework` | object / null | Optional |
| `homework.grade` | string | Optional |
| `homework.question_corrections` | array<[K12TasksGradingQuestionCorrection](#schema-k12tasksgradingquestioncorrection)> / null | Optional |
| `homework.subject` | string | Optional |
| `intent` | `completed_homework` / `blank_worksheet` / `writing` / `artwork` | Optional |
| `version` | integer (min=1) | Yes |

<a id="schema-k12taskscreateimagetaskreq"></a>

### `K12TasksCreateImageTaskReq`

| Field | Type / values | Key required |
| --- | --- | --- |
| `agent` | string (minLength=1) | Yes |
| `attempt_generation` | integer (min=1) | Yes |
| `creative_entry` | object / null | Optional |
| `creative_entry.kind` | `new_work` | Yes |
| `creative_entry.task_intent` | `writing` / `artwork` / `unknown` | Yes |
| `message_intent` | string | Optional |
| `route_request` | [K12TasksImageTaskRouteRequest](#schema-k12tasksimagetaskrouterequest) | Optional |
| `source_asset_refs` | array<string (minLength=1)> (minItems=1) | Yes |
| `source_kind` | [K12TasksImageTaskSourceKind](#schema-k12tasksimagetasksourcekind) | Yes |
| `source_ref` | string (minLength=1) | Yes |
| `source_session` | string | Optional |

<a id="schema-k12taskscreativecommitcommand"></a>

### `K12TasksCreativeCommitCommand`

| Field | Type / values | Key required |
| --- | --- | --- |
| `action` | `commit` | Yes |
| `work_title` | string | Optional |
| `task_requirement` | string | Optional |
| `intent` | string | Optional |
| `content_markdown` | string | Optional |
| `canonical_version` | `0` | Optional |
| `canonical_content` | `` | Optional |
| `segment_corrections` | array<[K12TasksCreativeWorkIntakeOCRCorrection](#schema-k12taskscreativeworkintakeocrcorrection)> (maxItems=0) / null | Optional |

<a id="schema-k12taskscreativefreezecommand"></a>

### `K12TasksCreativeFreezeCommand`

| Field | Type / values | Key required |
| --- | --- | --- |
| `action` | `freeze_ocr` | Yes |
| `canonical_version` | integer (min=1) | Yes |
| `canonical_content` | string | Optional |
| `segment_corrections` | array<[K12TasksCreativeWorkIntakeOCRCorrection](#schema-k12taskscreativeworkintakeocrcorrection)> | Optional |
| `work_title` | `` | Optional |
| `task_requirement` | `` | Optional |
| `intent` | `` | Optional |
| `content_markdown` | `` | Optional |

<a id="schema-k12taskscreativeimagetaskaction"></a>

### `K12TasksCreativeImageTaskAction`

`freeze_ocr` / `commit`

<a id="schema-k12taskscreativeresultfeedback"></a>

### `K12TasksCreativeResultFeedback`

| Field | Type / values | Key required |
| --- | --- | --- |
| `generation_id` | string | Yes |
| `structured_feedback` | [K12TasksWorkFeedback](#schema-k12tasksworkfeedback) | Yes |
| `projection_markdown` | string | Yes |

<a id="schema-k12taskscreativeresultintake"></a>

### `K12TasksCreativeResultIntake`

| Field | Type / values | Key required |
| --- | --- | --- |
| `intake_id` | string | Yes |
| `status` | [K12TasksCreativeWorkIntakeStatus](#schema-k12taskscreativeworkintakestatus) | Yes |

<a id="schema-k12taskscreativeresultpayload"></a>

### `K12TasksCreativeResultPayload`

| Field | Type / values | Key required |
| --- | --- | --- |
| `intake` | [K12TasksCreativeResultIntake](#schema-k12taskscreativeresultintake) | Yes |
| `outcome` | string | Optional |
| `notice` | string | Optional |
| `work` | [K12TasksImageTaskCreativeWorkDTO](#schema-k12tasksimagetaskcreativeworkdto) | Optional |
| `feedback` | [K12TasksCreativeResultFeedback](#schema-k12taskscreativeresultfeedback) | Optional |

<a id="schema-k12taskscreativeresultprojection"></a>

### `K12TasksCreativeResultProjection`

| Field | Type / values | Key required |
| --- | --- | --- |
| `kind` | `writing` / `artwork` | Yes |
| `payload` | [K12TasksCreativeResultPayload](#schema-k12taskscreativeresultpayload) | Yes |

<a id="schema-k12taskscreativeworkcreatecommand"></a>

### `K12TasksCreativeWorkCreateCommand`

| Field | Type / values | Key required |
| --- | --- | --- |
| `agent` | string (minLength=1) | Yes |
| `work_type` | `writing` | Yes |
| `content_markdown` | string (minLength=1) | Yes |

<a id="schema-k12taskscreativeworkcreateresult"></a>

### `K12TasksCreativeWorkCreateResult`

| Field | Type / values | Key required |
| --- | --- | --- |
| `work_id` | string | Yes |
| `created` | boolean | Yes |
| `initial_feedback_generation_id` | string | Yes |

<a id="schema-k12taskscreativeworkdto"></a>

### `K12TasksCreativeWorkDTO`

| Field | Type / values | Key required |
| --- | --- | --- |
| `content_markdown` | string | Optional |
| `created_at` | integer | Yes |
| `current_feedback` | [K12TasksWorkFeedbackGenerationDTO](#schema-k12tasksworkfeedbackgenerationdto) / null | Optional |
| `delivery_batch_id` | string | Optional |
| `display_name` | string | Yes |
| `initial_feedback` | [K12TasksWorkFeedbackGenerationDTO](#schema-k12tasksworkfeedbackgenerationdto) / null | Optional |
| `latest_feedback` | [K12TasksWorkFeedbackGenerationDTO](#schema-k12tasksworkfeedbackgenerationdto) / null | Optional |
| `latest_generation_at` | integer / null | Yes |
| `row_version` | integer | Yes |
| `source_asset_id` | string | Optional |
| `work_id` | string | Yes |
| `work_title` | string | Optional |
| `work_type` | string | Yes |

<a id="schema-k12taskscreativeworkdeleteresult"></a>

### `K12TasksCreativeWorkDeleteResult`

| Field | Type / values | Key required |
| --- | --- | --- |
| `deleted` | boolean | Yes |
| `work_id` | string | Yes |
| `row_version` | integer | Yes |

<a id="schema-k12taskscreativeworkentrykind"></a>

### `K12TasksCreativeWorkEntryKind`

`auto` / `new_work` / `revision`

<a id="schema-k12taskscreativeworkintakeocrcorrection"></a>

### `K12TasksCreativeWorkIntakeOCRCorrection`

| Field | Type / values | Key required |
| --- | --- | --- |
| `canonical_text` | string | Yes |
| `segment_id` | string | Yes |

<a id="schema-k12taskscreativeworkintakestatus"></a>

### `K12TasksCreativeWorkIntakeStatus`

`preparing` / `awaiting_confirmation` / `ready` / `promoted` / `failed` / `cancelled` / `unreadable`

<a id="schema-k12taskscreativeworklist"></a>

### `K12TasksCreativeWorkList`

| Field | Type / values | Key required |
| --- | --- | --- |
| `items` | array<[K12TasksCreativeWorkDTO](#schema-k12taskscreativeworkdto)> | Yes |

<a id="schema-k12taskscreativeworkpromotionpolicy"></a>

### `K12TasksCreativeWorkPromotionPolicy`

`automatic` / `explicit_commit`

<a id="schema-k12tasksdeliverybatch"></a>

### `K12TasksDeliveryBatch`

| Field | Type / values | Key required |
| --- | --- | --- |
| `agent_name` | string | Yes |
| `batch_id` | string | Yes |
| `content_digest` | string | Yes |
| `created_at` | integer | Yes |
| `dedupe_key` | string | Yes |
| `object_id` | string | Yes |
| `object_kind` | string | Yes |
| `receipts` | array<[K12TasksDeliveryReceipt](#schema-k12tasksdeliveryreceipt)> / null | Yes |
| `status` | [K12TasksDeliveryBatchStatus](#schema-k12tasksdeliverybatchstatus) | Yes |
| `updated_at` | integer | Yes |

<a id="schema-k12tasksdeliverybatchstatus"></a>

### `K12TasksDeliveryBatchStatus`

`pending` / `sending` / `delivered` / `failed` / `partial_failed` / `outcome_unknown`

<a id="schema-k12tasksdeliveryreceipt"></a>

### `K12TasksDeliveryReceipt`

| Field | Type / values | Key required |
| --- | --- | --- |
| `agent_name` | string | Yes |
| `attempt` | integer | Yes |
| `batch_id` | string | Optional |
| `batch_ordinal` | integer | Optional |
| `binding_id` | string | Yes |
| `created_at` | integer | Yes |
| `dedupe_key` | string | Yes |
| `delivery_id` | string | Yes |
| `external_message_id` | string | Optional |
| `last_error` | string | Optional |
| `object_id` | string | Yes |
| `object_kind` | string | Yes |
| `part_digest` | string | Yes |
| `part_kind` | [K12TasksPartKind](#schema-k12taskspartkind) | Yes |
| `part_mime` | string | Optional |
| `part_ordinal` | integer | Yes |
| `payload_digest` | string | Yes |
| `payload_json` | string | Yes |
| `render_manifest_json` | string | Yes |
| `status` | [K12TasksDeliveryReceiptStatus](#schema-k12tasksdeliveryreceiptstatus) | Yes |
| `target` | [K12TasksDeliveryTarget](#schema-k12tasksdeliverytarget) | Yes |
| `updated_at` | integer | Yes |

<a id="schema-k12tasksdeliveryreceiptstatus"></a>

### `K12TasksDeliveryReceiptStatus`

`pending` / `sending` / `delivered` / `failed` / `outcome_unknown`

<a id="schema-k12tasksdeliverytarget"></a>

### `K12TasksDeliveryTarget`

| Field | Type / values | Key required |
| --- | --- | --- |
| `chat_id` | string | Yes |
| `instance_id` | string | Optional |
| `label` | string | Optional |
| `platform` | string | Yes |

<a id="schema-k12taskseffectivegradingassessment"></a>

### `K12TasksEffectiveGradingAssessment`

| Field | Type / values | Key required |
| --- | --- | --- |
| `correction` | [K12TasksGradingAssessmentCorrection](#schema-k12tasksgradingassessmentcorrection) / null | Optional |
| `current` | [K12TasksGradingAssessmentItem](#schema-k12tasksgradingassessmentitem) | Yes |
| `original` | [K12TasksGradingAssessmentItem](#schema-k12tasksgradingassessmentitem) | Yes |

<a id="schema-k12tasksfinalsourcecorrectioninput"></a>

### `K12TasksFinalSourceCorrectionInput`

| Field | Type / values | Key required |
| --- | --- | --- |
| `accept_duplicate_execution` | boolean (default=false) | Optional |
| `agent` | string | Yes |
| `artifact_digest` | string | Yes |
| `artifact_id` | string | Yes |
| `idempotency_key` | string | Yes |
| `input_digest` | string | Yes |
| `input_revision` | integer (min=1) | Yes |
| `recovery_of` | string | Optional |
| `recovery_response_digest` | string | Optional |

<a id="schema-k12tasksfinalsourcecorrectionresult"></a>

### `K12TasksFinalSourceCorrectionResult`

| Field | Type / values | Key required |
| --- | --- | --- |
| `artifact` | [K12TasksGradingFinalArtifact](#schema-k12tasksgradingfinalartifact) / null | Optional |
| `correction_id` | string | Yes |
| `replayed` | boolean | Yes |
| `status` | string | Yes |

<a id="schema-k12tasksgraderesp"></a>

### `K12TasksGradeResp`

| Field | Type / values | Key required |
| --- | --- | --- |
| `assessment_status` | string | Optional |
| `badge` | string | Yes |
| `curriculum_unmapped` | array<string> / null | Optional |
| `error_cause` | string | Optional |
| `evidence_type` | string | Yes |
| `final_answer_correct` | boolean / null | Optional |
| `out_of_scope` | boolean | Yes |
| `out_of_scope_kp` | string | Optional |
| `record_created` | boolean | Yes |
| `record_id` | string | Optional |
| `solution` | string | Yes |
| `solve_only` | boolean | Yes |
| `verdict` | string | Yes |
| `wrong_step` | string | Optional |

<a id="schema-k12tasksgradingassessmentcorrection"></a>

### `K12TasksGradingAssessmentCorrection`

| Field | Type / values | Key required |
| --- | --- | --- |
| `assessment` | [K12TasksGradingAssessmentItem](#schema-k12tasksgradingassessmentitem) | Yes |
| `correction_id` | string | Yes |
| `created_at` | integer | Yes |
| `original_answer_source` | [K12TasksProblemAnswerSource](#schema-k12tasksproblemanswersource) / null | Optional |
| `original_result_digest` | string | Yes |
| `previous_correction_id` | string | Yes |
| `reason` | string | Yes |
| `revision` | integer | Yes |

<a id="schema-k12tasksgradingassessmentitem"></a>

### `K12TasksGradingAssessmentItem`

| Field | Type / values | Key required |
| --- | --- | --- |
| `agent_name` | string | Yes |
| `answer_source` | [K12TasksProblemAnswerSource](#schema-k12tasksproblemanswersource) / null | Optional |
| `attempt_id` | string | Yes |
| `confirmed_version` | integer | Yes |
| `created_at` | integer | Yes |
| `current_disposition` | string | Yes |
| `grade_invocation_id` | string | Optional |
| `input_digest` | string | Yes |
| `input_revision` | integer | Yes |
| `job_id` | string | Yes |
| `parent_guide_invocation_id` | string | Optional |
| `problem_id` | string | Yes |
| `projection_created` | boolean | Optional |
| `projection_record_id` | string | Optional |
| `projection_status` | string | Yes |
| `published_revision` | integer | Yes |
| `result_digest` | string | Yes |
| `result_json` | string | Yes |
| `solve_invocation_id` | string | Optional |
| `status` | [K12TasksGradingAssessmentStatus](#schema-k12tasksgradingassessmentstatus) | Yes |
| `structure_version` | integer | Yes |
| `updated_at` | integer | Yes |

<a id="schema-k12tasksgradingassessmentstatus"></a>

### `K12TasksGradingAssessmentStatus`

`correct` / `correct_with_process_issue` / `wrong` / `unanswered` / `answer_unclear` / `blank_solved` / `out_of_scope` / `untrusted`

<a id="schema-k12tasksgradingfinalartifact"></a>

### `K12TasksGradingFinalArtifact`

| Field | Type / values | Key required |
| --- | --- | --- |
| `agent_name` | string | Yes |
| `annotated_asset_id` | string | Optional |
| `annotated_digest` | string | Optional |
| `annotated_mime` | string | Optional |
| `artifact_digest` | string | Yes |
| `artifact_id` | string | Yes |
| `canonical_markdown` | string | Yes |
| `coverage_status` | [K12TasksGradingFinalArtifactCoverageStatus](#schema-k12tasksgradingfinalartifactcoveragestatus) | Yes |
| `created_at` | integer | Yes |
| `job_id` | string | Yes |
| `ordered_current_digests_json` | string | Yes |
| `original_source_digest` | string | Optional |
| `published_count` | integer | Yes |
| `skipped_count` | integer | Yes |
| `structure_version` | integer | Yes |
| `summary_invocation_id` | string | Yes |
| `total_count` | integer | Yes |
| `updated_at` | integer | Yes |

<a id="schema-k12tasksgradingfinalartifactcoveragestatus"></a>

### `K12TasksGradingFinalArtifactCoverageStatus`

`complete` / `with_skips` / `general_guidance`

<a id="schema-k12tasksgradingmodelsnapshot"></a>

### `K12TasksGradingModelSnapshot`

| Field | Type / values | Key required |
| --- | --- | --- |
| `capability` | string | Optional |
| `capability_receipt_digest` | string | Optional |
| `config_fingerprint` | string | Optional |
| `fallback` | string | Optional |
| `model` | string | Yes |
| `parent_instructions` | [K12TasksAgentInstructionsSnapshot](#schema-k12tasksagentinstructionssnapshot) | Optional |
| `probe_policy_version` | string | Optional |
| `provider` | string | Yes |
| `provider_instance_id` | string | Optional |
| `recognizing_request_policy` | [K12TasksModelRequestPolicySnapshot](#schema-k12tasksmodelrequestpolicysnapshot) | Optional |
| `route` | string | Yes |
| `timeout_ms` | integer | Optional |

<a id="schema-k12tasksgradingquestioncorrection"></a>

### `K12TasksGradingQuestionCorrection`

| Field | Type / values | Key required |
| --- | --- | --- |
| `answer_canonical_markdown` | string | Optional |
| `answer_state` | `` / `blank` / `present` / `unclear` | Optional |
| `canonical_markdown` | string | Optional |
| `confirmed` | boolean | Optional |
| `index` | integer (default=0) | Optional |
| `problem_id` | string | Optional |
| `question` | string | Optional |
| `student_answer` | string | Optional |
| `subject` | string | Optional |

<a id="schema-k12tasksgroundingevidencereceipt"></a>

### `K12TasksGroundingEvidenceReceipt`

| Field | Type / values | Key required |
| --- | --- | --- |
| `chunk_id` | string | Yes |
| `citation_digest` | string | Yes |
| `document_generation` | integer | Yes |
| `document_id` | string | Yes |
| `logical_page` | integer | Yes |
| `pdf_page` | integer | Yes |
| `query_digest` | string | Yes |
| `source_digest` | string | Yes |
| `source_mode` | string | Optional |
| `textbook_binding_id` | string | Yes |
| `textbook_manifest_id` | string | Yes |
| `vector_revision_id` | string | Yes |

<a id="schema-k12tasksgroundingsourcerecoveryinput"></a>

### `K12TasksGroundingSourceRecoveryInput`

| Field | Type / values | Key required |
| --- | --- | --- |
| `agent` | string | Yes |
| `idempotency_key` | string | Yes |
| `job_version` | integer (min=1) | Yes |
| `version` | integer (min=1) | Yes |

<a id="schema-k12tasksgroundingsourcerecoveryresult"></a>

### `K12TasksGroundingSourceRecoveryResult`

| Field | Type / values | Key required |
| --- | --- | --- |
| `job_id` | string | Yes |
| `recovery_id` | string | Yes |
| `replayed` | boolean | Yes |
| `source_mode` | string | Yes |

<a id="schema-k12taskshomeworkrecognition"></a>

### `K12TasksHomeworkRecognition`

| Field | Type / values | Key required |
| --- | --- | --- |
| `subject` | string | Yes |
| `questions` | array<[K12TasksRecognizedQuestionDTO](#schema-k12tasksrecognizedquestiondto)> | Yes |

<a id="schema-k12tasksimagetaskaccepted"></a>

### `K12TasksImageTaskAccepted`

| Field | Type / values | Key required |
| --- | --- | --- |
| `created` | boolean | Yes |
| `dispatch` | [K12TasksPublicImageTaskDispatch](#schema-k12taskspublicimagetaskdispatch) | Yes |

<a id="schema-k12tasksimagetaskbatchaccepted"></a>

### `K12TasksImageTaskBatchAccepted`

| Field | Type / values | Key required |
| --- | --- | --- |
| `tasks` | array<[K12TasksImageTaskAccepted](#schema-k12tasksimagetaskaccepted)> | Yes |

<a id="schema-k12tasksimagetaskbatcherror"></a>

### `K12TasksImageTaskBatchError`

| Field | Type / values | Key required |
| --- | --- | --- |
| `error` | string | Yes |
| `failed_index` | integer (min=0) | Yes |
| `tasks` | array<[K12TasksImageTaskAccepted](#schema-k12tasksimagetaskaccepted)> | Yes |

<a id="schema-k12tasksimagetaskcreativeconflictdto"></a>

### `K12TasksImageTaskCreativeConflictDTO`

| Field | Type / values | Key required |
| --- | --- | --- |
| `canonical_text` | string | Optional |
| `raw_text` | string | Optional |
| `reason` | string | Optional |
| `segment_id` | string | Yes |

<a id="schema-k12tasksimagetaskcreativeprojectiondto"></a>

### `K12TasksImageTaskCreativeProjectionDTO`

| Field | Type / values | Key required |
| --- | --- | --- |
| `canonical_content` | string | Optional |
| `canonical_version` | integer | Optional |
| `commit_required` | boolean / null | Optional |
| `commit_state` | string | Optional |
| `conflicts` | array<[K12TasksImageTaskCreativeConflictDTO](#schema-k12tasksimagetaskcreativeconflictdto)> / null | Optional |
| `entry_kind` | [K12TasksCreativeWorkEntryKind](#schema-k12taskscreativeworkentrykind) | Optional |
| `intake_id` | string | Yes |
| `kind` | `creative` | Yes |
| `notice` | string | Optional |
| `outcome` | string | Optional |
| `promoted_generation_id` | string | Optional |
| `promoted_work_id` | string | Optional |
| `promotion_policy` | [K12TasksCreativeWorkPromotionPolicy](#schema-k12taskscreativeworkpromotionpolicy) | Optional |
| `routing_provenance` | [K12TasksImageTaskRoutingProvenance](#schema-k12tasksimagetaskroutingprovenance) | Optional |
| `status` | [K12TasksCreativeWorkIntakeStatus](#schema-k12taskscreativeworkintakestatus) | Yes |
| `work` | [K12TasksImageTaskCreativeWorkDTO](#schema-k12tasksimagetaskcreativeworkdto) / null | Optional |
| `work_type` | string | Yes |

<a id="schema-k12tasksimagetaskcreativeworkdto"></a>

### `K12TasksImageTaskCreativeWorkDTO`

| Field | Type / values | Key required |
| --- | --- | --- |
| `display_name` | string | Yes |
| `work_id` | string | Yes |

<a id="schema-k12tasksimagetaskhomeworkprojectiondto"></a>

### `K12TasksImageTaskHomeworkProjectionDTO`

| Field | Type / values | Key required |
| --- | --- | --- |
| `anchor_state` | string | Yes |
| `completed_at` | integer | Optional |
| `confirmation_state` | string | Yes |
| `final_artifact` | [K12TasksGradingFinalArtifact](#schema-k12tasksgradingfinalartifact) / null | Optional |
| `grounding_evidence_receipts` | array<[K12TasksGroundingEvidenceReceipt](#schema-k12tasksgroundingevidencereceipt)> | Yes |
| `kind` | `homework` | Yes |
| `problem_grounding_receipts` | array<[K12TasksProblemGroundingReceipt](#schema-k12tasksproblemgroundingreceipt)> | Yes |
| `progressive` | [K12TasksImageTaskProgressiveDTO](#schema-k12tasksimagetaskprogressivedto) | Yes |
| `recognition` | [K12TasksHomeworkRecognition](#schema-k12taskshomeworkrecognition) | Optional |
| `stage` | string | Yes |

<a id="schema-k12tasksimagetaskintent"></a>

### `K12TasksImageTaskIntent`

`completed_homework` / `blank_worksheet` / `writing` / `artwork` / `unknown`

<a id="schema-k12tasksimagetaskoperationreceipt"></a>

### `K12TasksImageTaskOperationReceipt`

| Field | Type / values | Key required |
| --- | --- | --- |
| `attempt` | integer | Yes |
| `canonical_input_digest` | string | Yes |
| `execution_kind` | string | Optional |
| `invocation_id` | string | Yes |
| `model` | string | Optional |
| `operation` | string | Yes |
| `parent_invocation_id` | string | Optional |
| `physical_unit` | string | Optional |
| `provider` | string | Optional |
| `request_policy` | [K12TasksModelRequestPolicySnapshot](#schema-k12tasksmodelrequestpolicysnapshot) / null | Optional |
| `request_policy_digest` | string | Optional |
| `result_digest` | string | Yes |
| `status` | string | Yes |

<a id="schema-k12tasksimagetaskproblemprogressdto"></a>

### `K12TasksImageTaskProblemProgressDTO`

| Field | Type / values | Key required |
| --- | --- | --- |
| `current_disposition` | string | Yes |
| `input_revision` | integer | Yes |
| `problem_id` | string | Yes |
| `published_revision` | integer | Yes |
| `status` | string | Yes |

<a id="schema-k12tasksimagetaskprogressdto"></a>

### `K12TasksImageTaskProgressDTO`

| Field | Type / values | Key required |
| --- | --- | --- |
| `operation` | string | Yes |
| `state` | string | Yes |

<a id="schema-k12tasksimagetaskprogressivecoveragedto"></a>

### `K12TasksImageTaskProgressiveCoverageDTO`

| Field | Type / values | Key required |
| --- | --- | --- |
| `awaiting` | integer | Yes |
| `failed` | integer | Yes |
| `projection_revision` | integer | Yes |
| `published` | integer | Yes |
| `skipped` | integer | Yes |
| `status` | string | Yes |
| `total` | integer | Yes |

<a id="schema-k12tasksimagetaskprogressivedto"></a>

### `K12TasksImageTaskProgressiveDTO`

| Field | Type / values | Key required |
| --- | --- | --- |
| `coverage` | [K12TasksImageTaskProgressiveCoverageDTO](#schema-k12tasksimagetaskprogressivecoveragedto) | Yes |
| `problem_progress` | array<[K12TasksImageTaskProblemProgressDTO](#schema-k12tasksimagetaskproblemprogressdto)> / null | Yes |
| `snapshot_revision` | integer | Yes |
| `structure_version` | integer | Yes |

<a id="schema-k12tasksimagetaskrecoverables"></a>

### `K12TasksImageTaskRecoverables`

| Field | Type / values | Key required |
| --- | --- | --- |
| `items` | array<[K12TasksRecoverableImageTask](#schema-k12tasksrecoverableimagetask)> | Yes |

<a id="schema-k12tasksimagetaskresponse"></a>

### `K12TasksImageTaskResponse`

| Field | Type / values | Key required |
| --- | --- | --- |
| `dispatch` | [K12TasksPublicImageTaskDispatch](#schema-k12taskspublicimagetaskdispatch) | Yes |

<a id="schema-k12tasksimagetaskresult"></a>

### `K12TasksImageTaskResult`

| Field | Type / values | Key required |
| --- | --- | --- |
| `dispatch_id` | string | Yes |
| `task_intent` | [K12TasksImageTaskIntent](#schema-k12tasksimagetaskintent) | Yes |
| `status` | [K12TasksImageTaskStatus](#schema-k12tasksimagetaskstatus) | Yes |
| `source_digest` | string | Yes |
| `source_attachments` | array<[K12TasksImageTaskSourceAttachmentReceipt](#schema-k12tasksimagetasksourceattachmentreceipt)> | Yes |
| `operation_receipts` | array<[K12TasksImageTaskOperationReceipt](#schema-k12tasksimagetaskoperationreceipt)> | Yes |
| `result` | [K12TasksPhotoResultProjection](#schema-k12tasksphotoresultprojection) / [K12TasksCreativeResultProjection](#schema-k12taskscreativeresultprojection) / null | Yes |
| `failure_kind` | string | Optional |
| `grounding_evidence_receipts` | array<[K12TasksGroundingEvidenceReceipt](#schema-k12tasksgroundingevidencereceipt)> | Optional |
| `problem_grounding_receipts` | array<[K12TasksProblemGroundingReceipt](#schema-k12tasksproblemgroundingreceipt)> | Optional |

<a id="schema-k12tasksimagetaskrouterequest"></a>

### `K12TasksImageTaskRouteRequest`

| Field | Type / values | Key required |
| --- | --- | --- |
| `model` | string | Optional |
| `provider` | string | Optional |
| `selection_source` | `auto` / `explicit` (default=auto) | Optional |

<a id="schema-k12tasksimagetaskroutingprovenance"></a>

### `K12TasksImageTaskRoutingProvenance`

`model_classified` / `parent_selected`

<a id="schema-k12tasksimagetasksourceattachmentreceipt"></a>

### `K12TasksImageTaskSourceAttachmentReceipt`

| Field | Type / values | Key required |
| --- | --- | --- |
| `digest` | string | Yes |
| `size_bytes` | integer | Yes |

<a id="schema-k12tasksimagetasksourcekind"></a>

### `K12TasksImageTaskSourceKind`

`desktop` / `api` / `im_direct`

<a id="schema-k12tasksimagetaskstatus"></a>

### `K12TasksImageTaskStatus`

`routing` / `awaiting_confirmation` / `routed` / `failed` / `cancelled`

<a id="schema-k12tasksimagetasktargetdto"></a>

### `K12TasksImageTaskTargetDTO`

| Field | Type / values | Key required |
| --- | --- | --- |
| `id` | string | Yes |
| `type` | [K12TasksImageTaskTargetType](#schema-k12tasksimagetasktargettype) | Yes |

<a id="schema-k12tasksimagetasktargettype"></a>

### `K12TasksImageTaskTargetType`

`homework_submission` / `creative_work_intake`

<a id="schema-k12tasksimagetaskretryreq"></a>

### `K12TasksImageTaskRetryReq`

| Field | Type / values | Key required |
| --- | --- | --- |
| `agent` | string; original Agent name | Yes |
| `version` | integer (min=1); current dispatch version | Yes |
| `intent` | string; absent/empty retains ordinary retry, only `known_local_technical` enters controlled recovery | No |

<a id="schema-k12tasksimagetaskversionreq"></a>

### `K12TasksImageTaskVersionReq`

| Field | Type / values | Key required |
| --- | --- | --- |
| `agent` | string | Yes |
| `version` | integer (min=1) | Yes |

<a id="schema-k12tasksmaterialmodelpolicy"></a>

### `K12TasksMaterialModelPolicy`

| Field | Type / values | Key required |
| --- | --- | --- |
| `model` | [K12TasksGradingModelSnapshot](#schema-k12tasksgradingmodelsnapshot) | Yes |
| `version` | string | Yes |

<a id="schema-k12tasksmaterialpreparationitem"></a>

### `K12TasksMaterialPreparationItem`

| Field | Type / values | Key required |
| --- | --- | --- |
| `answer` | string | Optional |
| `asset_id` | string | Optional |
| `asset_version` | integer | Optional |
| `block_id` | string | Yes |
| `candidate_id` | string | Yes |
| `line` | integer | Yes |
| `page` | integer | Optional |
| `reason` | string | Optional |
| `reference_answer` | string | Optional |
| `source_warnings` | array<string> / null | Optional |
| `state` | string | Yes |
| `stem` | string | Yes |

<a id="schema-k12tasksmaterialpreparationlist"></a>

### `K12TasksMaterialPreparationList`

| Field | Type / values | Key required |
| --- | --- | --- |
| `preparations` | array<[K12TasksMaterialPreparationSummary](#schema-k12tasksmaterialpreparationsummary)> | Yes |

<a id="schema-k12tasksmaterialpreparationsummary"></a>

### `K12TasksMaterialPreparationSummary`

| Field | Type / values | Key required |
| --- | --- | --- |
| `counts` | object | Yes |
| `counts.ready` | integer (min=0) | Yes |
| `counts.preparing` | integer (min=0) | Yes |
| `counts.needs_review` | integer (min=0) | Yes |
| `counts.failed` | integer (min=0) | Yes |
| `counts.outcome_unknown` | integer (min=0) | Yes |
| `counts.stopped` | integer (min=0) | Yes |
| `document_id` | string | Yes |
| `extraction_complete` | boolean | Yes |
| `items` | array<[K12TasksMaterialPreparationItem](#schema-k12tasksmaterialpreparationitem)> / null | Yes |
| `source_observations` | array<[K12TasksMaterialReferenceObservation](#schema-k12tasksmaterialreferenceobservation)> / null | Optional |
| `source_revision` | integer | Yes |
| `state` | `not_prepared` / `preparing` / `ready` / `needs_review` / `failed` / `outcome_unknown` / `stopped` | Yes |

<a id="schema-k12tasksmaterialrecoverycommand"></a>

### `K12TasksMaterialRecoveryCommand`

| Field | Type / values | Key required |
| --- | --- | --- |
| `fingerprint` | string (minLength=1) | Yes |
| `allow_possible_duplicate_charge` | `true` | Yes |

<a id="schema-k12tasksmaterialrecoveryplan"></a>

### `K12TasksMaterialRecoveryPlan`

| Field | Type / values | Key required |
| --- | --- | --- |
| `document_id` | string | Yes |
| `fingerprint` | string | Yes |
| `input_digest` | string | Yes |
| `may_duplicate_charge` | boolean | Yes |
| `model_policy` | [K12TasksMaterialModelPolicy](#schema-k12tasksmaterialmodelpolicy) | Yes |
| `policy_digest` | string | Yes |
| `reusable` | array<[K12TasksMaterialRecoveryReceipt](#schema-k12tasksmaterialrecoveryreceipt)> / null | Yes |
| `source_digest` | string | Yes |
| `source_revision` | integer | Yes |
| `task_id` | string | Yes |
| `unknown` | [K12TasksMaterialRecoveryReceipt](#schema-k12tasksmaterialrecoveryreceipt) | Yes |

<a id="schema-k12tasksmaterialrecoveryreceipt"></a>

### `K12TasksMaterialRecoveryReceipt`

| Field | Type / values | Key required |
| --- | --- | --- |
| `attempt` | integer | Yes |
| `invocation_id` | string | Yes |
| `operation` | string | Yes |
| `request_digest` | string | Yes |
| `result_digest` | string | Optional |

<a id="schema-k12tasksmaterialrecoveryresult"></a>

### `K12TasksMaterialRecoveryResult`

| Field | Type / values | Key required |
| --- | --- | --- |
| `decision_id` | string | Yes |
| `replacement_invocation_id` | string | Optional |
| `state` | string | Yes |
| `task_id` | string | Yes |

<a id="schema-k12tasksmaterialreferenceobservation"></a>

### `K12TasksMaterialReferenceObservation`

| Field | Type / values | Key required |
| --- | --- | --- |
| `candidate_id` | string | Optional |
| `locations` | array<[K12TasksMaterialSourceLocation](#schema-k12tasksmaterialsourcelocation)> / null | Yes |
| `question_number` | string | Yes |
| `reason` | string | Optional |
| `review_required` | boolean | Optional |
| `text` | string | Yes |

<a id="schema-k12tasksmaterialsourcelocation"></a>

### `K12TasksMaterialSourceLocation`

| Field | Type / values | Key required |
| --- | --- | --- |
| `block_id` | string | Yes |
| `line` | integer | Yes |

<a id="schema-k12tasksmodelrequestpolicysnapshot"></a>

### `K12TasksModelRequestPolicySnapshot`

| Field | Type / values | Key required |
| --- | --- | --- |
| `policy_version` | string | Yes |
| `reasoning_effort` | string | Yes |
| `stage` | string | Yes |
| `thinking` | string | Yes |

<a id="schema-k12tasksocrriskreason"></a>

### `K12TasksOCRRiskReason`

`fraction` / `decimal_point` / `negative_sign` / `unit` / `erasure` / `evidence_conflict` / `low_confidence` / `unclear_handwriting` / `subject_undetermined` / `canonical_parse_failed`

<a id="schema-k12tasksparentteachingguide"></a>

### `K12TasksParentTeachingGuide`

| Field | Type / values | Key required |
| --- | --- | --- |
| `answer` | string | Yes |
| `checking_method` | string | Yes |
| `follow_up_questions` | array<string> / null | Yes |
| `full_solution_steps` | array<string> / null | Yes |
| `grade_level_method` | string | Yes |
| `likely_mistakes` | array<string> / null | Yes |
| `parent_teaching_sequence` | array<string> / null | Yes |

<a id="schema-k12taskspartkind"></a>

### `K12TasksPartKind`

`markdown` / `text` / `artifact`

<a id="schema-k12tasksphotoitemdto"></a>

### `K12TasksPhotoItemDTO`

| Field | Type / values | Key required |
| --- | --- | --- |
| `answer_source` | [K12TasksProblemAnswerSource](#schema-k12tasksproblemanswersource) / null | Optional |
| `grade` | [K12TasksGradeResp](#schema-k12tasksgraderesp) / null | Optional |
| `parent_guide` | [K12TasksParentTeachingGuide](#schema-k12tasksparentteachingguide) / null | Optional |
| `question` | [K12TasksRecognizedQuestionDTO](#schema-k12tasksrecognizedquestiondto) | Yes |
| `result_kind` | string | Yes |
| `reuse` | [K12TasksProblemAssetReuse](#schema-k12tasksproblemassetreuse) / null | Optional |
| `status` | string | Yes |
| `warning` | string | Optional |

<a id="schema-k12tasksphotoresult"></a>

### `K12TasksPhotoResult`

| Field | Type / values | Key required |
| --- | --- | --- |
| `mode` | `grade` / `solve` | Yes |
| `items` | array<[K12TasksPhotoItemDTO](#schema-k12tasksphotoitemdto)> | Yes |
| `task_intent` | `completed_homework` / `blank_worksheet` | Yes |
| `result_surface` | `annotated_homework` / `parent_teaching_guide` | Yes |
| `markdown` | string | Yes |
| `image_warning` | string | Yes |
| `annotated_image` | [K12TasksAnnotatedImage](#schema-k12tasksannotatedimage) | Optional |

<a id="schema-k12tasksphotoresultprojection"></a>

### `K12TasksPhotoResultProjection`

| Field | Type / values | Key required |
| --- | --- | --- |
| `kind` | `completed_homework` / `blank_worksheet` | Yes |
| `payload` | [K12TasksPhotoResult](#schema-k12tasksphotoresult) | Yes |

<a id="schema-k12tasksproblemanswersource"></a>

### `K12TasksProblemAnswerSource`

| Field | Type / values | Key required |
| --- | --- | --- |
| `adoption_id` | string | Optional |
| `asset_id` | string | Optional |
| `asset_revision` | integer | Optional |
| `asset_version` | integer | Optional |
| `facts_digest` | string | Yes |
| `invocation_id` | string | Optional |
| `kind` | [K12TasksProblemAnswerSourceKind](#schema-k12tasksproblemanswersourcekind) | Yes |

<a id="schema-k12tasksproblemanswersourcekind"></a>

### `K12TasksProblemAnswerSourceKind`

`model` / `deterministic` / `asset`

<a id="schema-k12tasksproblemassetfeedback"></a>

### `K12TasksProblemAssetFeedback`

| Field | Type / values | Key required |
| --- | --- | --- |
| `command` | [K12TasksProblemAssetFeedbackCommand](#schema-k12tasksproblemassetfeedbackcommand) | Yes |
| `created_at` | integer | Yes |
| `feedback_id` | string | Yes |
| `last_error` | string | Optional |
| `outcomes` | array<[K12TasksProblemAssetFeedbackOutcome](#schema-k12tasksproblemassetfeedbackoutcome)> / null | Yes |
| `status` | `pending` / `completed` / `unresolved` / `outcome_unknown` | Yes |
| `targets` | array<[K12TasksProblemAssetFeedbackTarget](#schema-k12tasksproblemassetfeedbacktarget)> / null | Yes |
| `updated_at` | integer | Yes |

<a id="schema-k12tasksproblemassetfeedbackcommand"></a>

### `K12TasksProblemAssetFeedbackCommand`

| Field | Type / values | Key required |
| --- | --- | --- |
| `agent_name` | string | Yes |
| `dispatch_id` | string | Yes |
| `input_revision` | integer | Yes |
| `job_id` | string | Yes |
| `kind` | string | Yes |
| `owner_id` | string | Yes |
| `problem_id` | string | Yes |
| `reason` | string | Yes |
| `request_id` | string | Yes |
| `result_digest` | string | Yes |

<a id="schema-k12tasksproblemassetfeedbackoutcome"></a>

### `K12TasksProblemAssetFeedbackOutcome`

| Field | Type / values | Key required |
| --- | --- | --- |
| `correction_id` | string | Optional |
| `error` | string | Optional |
| `status` | `corrected` / `unresolved` / `outcome_unknown` | Yes |
| `target` | [K12TasksProblemAssetFeedbackTarget](#schema-k12tasksproblemassetfeedbacktarget) | Yes |

<a id="schema-k12tasksproblemassetfeedbacktarget"></a>

### `K12TasksProblemAssetFeedbackTarget`

| Field | Type / values | Key required |
| --- | --- | --- |
| `agent_name` | string | Yes |
| `asset_id` | string | Yes |
| `asset_version` | integer | Yes |
| `input_revision` | integer | Yes |
| `job_id` | string | Yes |
| `problem_id` | string | Yes |
| `result_digest` | string | Yes |

<a id="schema-k12tasksproblemassetreuse"></a>

### `K12TasksProblemAssetReuse`

| Field | Type / values | Key required |
| --- | --- | --- |
| `answer` | string | Yes |
| `document_id` | string | Optional |
| `page` | integer | Optional |
| `source_digest` | string | Optional |
| `source_name` | string | Yes |
| `stem` | string | Yes |

<a id="schema-k12tasksproblemgroundingreceipt"></a>

### `K12TasksProblemGroundingReceipt`

| Field | Type / values | Key required |
| --- | --- | --- |
| `identity_digest` | string | Yes |
| `operation` | string | Yes |
| `problem_id` | string | Yes |
| `chunk_id` | string | Yes |
| `citation_digest` | string | Yes |
| `document_generation` | integer | Yes |
| `document_id` | string | Yes |
| `logical_page` | integer | Yes |
| `pdf_page` | integer | Yes |
| `query_digest` | string | Yes |
| `source_digest` | string | Yes |
| `source_mode` | string | Optional |
| `textbook_binding_id` | string | Yes |
| `textbook_manifest_id` | string | Yes |
| `vector_revision_id` | string | Yes |

<a id="schema-k12tasksproblemkind"></a>

### `K12TasksProblemKind`

`standalone` / `compound_parent` / `subproblem`

<a id="schema-k12tasksproblemsourceactionrequest"></a>

### `K12TasksProblemSourceActionRequest`

Variants: `correct_text` / `select_region` / `retake` / `skip` / `resume`。

<a id="schema-k12tasksproblemsourceactionresponse"></a>

### `K12TasksProblemSourceActionResponse`

| Field | Type / values | Key required |
| --- | --- | --- |
| `action` | string | Yes |
| `command_receipt_id` | string | Yes |
| `dispatch_id` | string | Yes |
| `input_revision` | integer | Yes |
| `problem_id` | string | Yes |
| `progressive_snapshot` | [K12TasksProblemSourceProgressiveSnapshot](#schema-k12tasksproblemsourceprogressivesnapshot) | Yes |
| `structure_version` | integer | Yes |

<a id="schema-k12tasksproblemsourceprogress"></a>

### `K12TasksProblemSourceProgress`

| Field | Type / values | Key required |
| --- | --- | --- |
| `current_disposition` | string | Yes |
| `input_revision` | integer | Yes |
| `page_asset_id` | string | Optional |
| `problem_id` | string | Yes |
| `published_revision` | integer | Yes |
| `source_height` | integer | Optional |
| `source_region` | [K12TasksViewcontractSourcePixelRegion](#schema-k12tasksviewcontractsourcepixelregion) / null | Yes |
| `source_width` | integer | Optional |
| `status` | string | Yes |

<a id="schema-k12tasksproblemsourceprogressivecoverage"></a>

### `K12TasksProblemSourceProgressiveCoverage`

| Field | Type / values | Key required |
| --- | --- | --- |
| `awaiting` | integer | Yes |
| `failed` | integer | Yes |
| `projection_revision` | integer | Yes |
| `published` | integer | Yes |
| `skipped` | integer | Yes |
| `status` | string | Yes |
| `total` | integer | Yes |

<a id="schema-k12tasksproblemsourceprogressivesnapshot"></a>

### `K12TasksProblemSourceProgressiveSnapshot`

| Field | Type / values | Key required |
| --- | --- | --- |
| `coverage` | [K12TasksProblemSourceProgressiveCoverage](#schema-k12tasksproblemsourceprogressivecoverage) | Yes |
| `problem_progress` | array<[K12TasksProblemSourceProgress](#schema-k12tasksproblemsourceprogress)> / null | Yes |
| `snapshot_revision` | integer | Yes |
| `structure_version` | integer | Yes |

<a id="schema-k12taskspublicimagetaskdispatch"></a>

### `K12TasksPublicImageTaskDispatch`

| Field | Type / values | Key required |
| --- | --- | --- |
| `automatic_budget_seconds` | integer | Yes |
| `automatic_deadline_at` | integer | Yes |
| `automatic_remaining_seconds` | integer | Yes |
| `automatic_started_at` | integer | Yes |
| `confirmation_candidates` | array<[K12TasksImageTaskIntent](#schema-k12tasksimagetaskintent)> | Yes |
| `created_at` | integer | Yes |
| `dispatch_id` | string | Yes |
| `failure_kind` | string / null | Optional |
| `intent_confidence` | number | Yes |
| `intent_evidence` | array<string> | Yes |
| `model_id` | string / null | Yes |
| `operation_deadline_at` | integer | Optional |
| `progress` | [K12TasksImageTaskProgressDTO](#schema-k12tasksimagetaskprogressdto) | Yes |
| `provider_display_name` | string / null | Yes |
| `retryable` | boolean | Yes |
| `status` | [K12TasksImageTaskStatus](#schema-k12tasksimagetaskstatus) | Yes |
| `target` | [K12TasksImageTaskTargetDTO](#schema-k12tasksimagetasktargetdto) / null | Optional |
| `target_projection` | [K12TasksImageTaskHomeworkProjectionDTO](#schema-k12tasksimagetaskhomeworkprojectiondto) / [K12TasksImageTaskCreativeProjectionDTO](#schema-k12tasksimagetaskcreativeprojectiondto) | Optional |
| `task_intent` | [K12TasksImageTaskIntent](#schema-k12tasksimagetaskintent) | Yes |
| `updated_at` | integer | Yes |
| `version` | integer | Yes |

<a id="schema-k12tasksrecognitionrecoveryauthorization"></a>

### `K12TasksRecognitionRecoveryAuthorization`

| Field | Type / values | Key required |
| --- | --- | --- |
| `agent` | string | Yes |
| `authorization_id` | string | Yes |
| `candidate_exact_set_digest` | string | Yes |
| `created_at` | integer | Yes |
| `dispatch_id` | string | Yes |
| `idempotency_key` | string | Yes |
| `job_id` | string | Yes |
| `new_parent_invocation_id` | string | Yes |
| `new_physical_invocation_id` | string | Yes |
| `new_request_digest` | string | Yes |
| `page_digest` | string | Yes |
| `request_digest` | string | Yes |
| `source_parent_id` | string | Yes |
| `source_physical_invocation_id` | string | Yes |
| `source_plan_digest` | string | Yes |
| `source_request_digest` | string | Yes |
| `source_timeout_ms` | integer | Optional |
| `timeout_override_ms` | integer | Optional |

<a id="schema-k12tasksrecognitionrecoveryinput"></a>

### `K12TasksRecognitionRecoveryInput`

| Field | Type / values | Key required |
| --- | --- | --- |
| `accept_duplicate_execution` | `true` | Yes |
| `agent` | string | Yes |
| `idempotency_key` | string | Yes |
| `job_version` | integer (min=1) | Yes |
| `source_physical_invocation_id` | string | Yes |
| `timeout_override_ms` | `0` / `180000` (default=0) | Optional |
| `version` | integer (min=1) | Yes |

<a id="schema-k12tasksrecognitionrecoveryresult"></a>

### `K12TasksRecognitionRecoveryResult`

| Field | Type / values | Key required |
| --- | --- | --- |
| `authorization` | [K12TasksRecognitionRecoveryAuthorization](#schema-k12tasksrecognitionrecoveryauthorization) | Yes |
| `replayed` | boolean | Yes |
| `status` | string | Yes |

<a id="schema-k12tasksrecognizedquestiondto"></a>

### `K12TasksRecognizedQuestionDTO`

| Field | Type / values | Key required |
| --- | --- | --- |
| `answer_canonical_markdown` | string | Optional |
| `answer_canonical_valid` | boolean | Yes |
| `answer_raw_transcription` | string | Optional |
| `answer_state` | [K12TasksAnswerState](#schema-k12tasksanswerstate) | Yes |
| `attempt_id` | string | Optional |
| `bbox` | [K12TasksBboxDTO](#schema-k12tasksbboxdto) / null | Optional |
| `canonical_markdown` | string | Yes |
| `canonical_valid` | boolean | Yes |
| `canonical_version` | integer | Yes |
| `confirmation_reasons` | array<[K12TasksOCRRiskReason](#schema-k12tasksocrriskreason)> / null | Optional |
| `confirmation_required` | boolean | Yes |
| `confirmed_version` | integer | Yes |
| `display_label` | string | Yes |
| `input_digest` | string | Optional |
| `knowledge_points` | array<string> / null | Yes |
| `page_asset_id` | string | Yes |
| `parent_problem_id` | string | Optional |
| `problem_id` | string | Yes |
| `problem_kind` | [K12TasksProblemKind](#schema-k12tasksproblemkind) | Yes |
| `question` | string | Yes |
| `raw_transcription` | string | Yes |
| `recognition_confidence` | number / null | Optional |
| `source_height` | integer | Optional |
| `source_number_path` | array<string> / null | Yes |
| `source_region` | [K12TasksSourcePixelRegion](#schema-k12taskssourcepixelregion) / null | Optional |
| `source_section_label` | string | Yes |
| `source_section_path` | array<string> / null | Yes |
| `source_width` | integer | Optional |
| `student_answer` | string | Yes |
| `subject` | string | Optional |
| `subproblem_no` | string | Optional |
| `system_display_label` | string | Yes |
| `system_section_ordinal` | integer | Yes |

<a id="schema-k12tasksrecoverableimagetask"></a>

### `K12TasksRecoverableImageTask`

| Field | Type / values | Key required |
| --- | --- | --- |
| `attempt_generation` | integer | Yes |
| `dispatch_id` | string | Yes |
| `projection_ready` | boolean | Yes |
| `source_message_id` | string | Yes |
| `source_session_id` | string | Yes |
| `stage` | string | Yes |
| `status` | [K12TasksImageTaskStatus](#schema-k12tasksimagetaskstatus) | Yes |
| `terminal` | boolean | Yes |
| `version` | integer | Yes |

<a id="schema-k12taskssourcepixelregion"></a>

### `K12TasksSourcePixelRegion`

| Field | Type / values | Key required |
| --- | --- | --- |
| `height` | integer | Yes |
| `width` | integer | Yes |
| `x` | integer | Yes |
| `y` | integer | Yes |

<a id="schema-k12tasksviewcontractsourcepixelregion"></a>

### `K12TasksViewcontractSourcePixelRegion`

| Field | Type / values | Key required |
| --- | --- | --- |
| `height` | integer | Yes |
| `width` | integer | Yes |
| `x` | integer | Yes |
| `y` | integer | Yes |

<a id="schema-k12tasksworkfeedback"></a>

### `K12TasksWorkFeedback`

| Field | Type / values | Key required |
| --- | --- | --- |
| `affirmation` | string | Optional |
| `evidence_refs` | array<string> / null | Yes |
| `feedback_id` | string | Yes |
| `feedback_type` | string | Yes |
| `limitations` | string | Yes |
| `next_step` | string | Optional |
| `observations` | array<[K12TasksWorkFeedbackObservation](#schema-k12tasksworkfeedbackobservation)> / null | Yes |
| `parent_guidance` | string | Optional |
| `projection_markdown` | string | Yes |
| `source_snapshot` | [K12TasksWorkFeedbackSourceSnapshot](#schema-k12tasksworkfeedbacksourcesnapshot) | Yes |
| `suggestions` | array<string> / null | Yes |
| `version_id` | string | Yes |

<a id="schema-k12tasksworkfeedbackdto"></a>

### `K12TasksWorkFeedbackDTO`

| Field | Type / values | Key required |
| --- | --- | --- |
| `affirmation` | string | Yes |
| `evidence_refs` | array<string> / null | Yes |
| `feedback_id` | string | Yes |
| `feedback_type` | string | Yes |
| `limitations` | string | Optional |
| `next_step` | string | Yes |
| `parent_guidance` | string | Yes |
| `projection_markdown` | string | Optional |
| `source_snapshot` | [K12TasksWorkFeedbackSourceSnapshot](#schema-k12tasksworkfeedbacksourcesnapshot) | Yes |
| `visible_evidence` | array<string> / null | Yes |

<a id="schema-k12tasksworkfeedbackgenerationdto"></a>

### `K12TasksWorkFeedbackGenerationDTO`

| Field | Type / values | Key required |
| --- | --- | --- |
| `failure_message` | string | Optional |
| `feedback` | [K12TasksWorkFeedbackDTO](#schema-k12tasksworkfeedbackdto) / null | Optional |
| `generation_id` | string | Yes |
| `recovery_state` | string | Optional |
| `retry_safe` | boolean | Yes |
| `status` | `queued` / `running` / `succeeded` / `failed` | Yes |

<a id="schema-k12tasksworkfeedbackobservation"></a>

### `K12TasksWorkFeedbackObservation`

| Field | Type / values | Key required |
| --- | --- | --- |
| `dimension` | string | Yes |
| `evidence` | string | Yes |

<a id="schema-k12tasksworkfeedbacksourcesnapshot"></a>

### `K12TasksWorkFeedbackSourceSnapshot`

| Field | Type / values | Key required |
| --- | --- | --- |
| `capability` | string | Yes |
| `method_ref` | string | Yes |
| `source` | string | Yes |
