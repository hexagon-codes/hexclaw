# K12 mistakes, practice, printing and weekly practice API

[简体中文](practice.md) · [K12 API overview](../../API.en.md)

This reference covers all 52 registered method/path pairs in the current source, including uncommitted changes. Match the service version to the release you run. Paths include the `/api/k12` prefix. The K12 scenario and record store must be enabled; model, rendering and delivery operations also require their corresponding configured dependencies.

Use the serving HexClaw instance's Bearer token. Set `Content-Type: application/json` on JSON writes. IDs below are illustrative: replace them with actual response IDs. `agent` selects the current learner/K12 agent; it is not an authentication identity. Some strict weekly commands intentionally resolve agent from the plan ID and reject a body `agent` field.

JSON bodies are limited to 1 MiB and must contain exactly one JSON value. Strict commands reject unknown fields. Business errors use `{"error":"..."}`; do not assume a universal code/message envelope. 400 identifies decoded/typed input errors, 404 missing scoped resources, 409 typed revision/state/digest conflicts, and 502 model/render/catalog failures. Unclassified errors follow the specific handler fallback shown below. Some plain state-validation errors therefore also use that fallback. HTTP 401 comes from the service authentication boundary.

Profile, textbook and curriculum-progress constraints are maintained in [profile-bundle](../../API.en.md#profile-bundle) and [textbook / progress](../../API.en.md#textbook-and-curriculum-progress).

## Contents

| Method / path | Operation |
| --- | --- |
| `GET /api/k12/mistakes` | [List mistakes and review states](#k12practicemistakes) |
| `DELETE /api/k12/mistakes/{record_id}` | [Delete a mistake](#k12practicedeletemistake) |
| `POST /api/k12/mistakes/{record_id}/archive` | [Archive a mistake](#k12practicearchivemistake) |
| `POST /api/k12/mistakes/{record_id}/restore` | [Restore an archived mistake](#k12practicerestoremistake) |
| `POST /api/k12/mistakes/{record_id}/practice-generation` | [Start single-mistake variant generation](#k12practicestartpracticegeneration) |
| `GET /api/k12/mistakes/{record_id}/practice-generation` | [Read single-mistake generation state](#k12practicegetpracticegeneration) |
| `GET /api/k12/mistakes/{record_id}/practice-generation/receipts` | [Read redacted invocation receipts](#k12practicegetpracticegenerationreceipts) |
| `POST /api/k12/mistakes/{record_id}/practice-generation/retry` | [Retry single-mistake generation](#k12practiceretrypracticegeneration) |
| `POST /api/k12/mistakes/{record_id}/practice-candidate-selection` | [Open a candidate selection](#k12practiceopenpracticecandidateselection) |
| `POST /api/k12/practice-candidate-selections/{id}/batches` | [Generate the next candidate batch](#k12practicegeneratepracticecandidatebatch) |
| `POST /api/k12/practice-candidate-selections/{id}/commit` | [Commit selected candidates to the basket](#k12practicecommitpracticecandidateselection) |
| `POST /api/k12/mistakes/{record_id}/suppress` | [Suppress mistake review](#k12practicesuppressmistakereview) |
| `POST /api/k12/mistakes/{record_id}/restore-review` | [Restore mistake review](#k12practicerestoremistakereview) |
| `POST /api/k12/mistakes/{record_id}/defer-this-week` | [Defer a mistake for one ISO week](#k12practicedefermistakethisweek) |
| `GET /api/k12/practice-sets` | [List practice sets](#k12practicelistpracticesets) |
| `GET /api/k12/practice-sets/{id}` | [Read a practice set](#k12practicegetpracticeset) |
| `GET /api/k12/practice-sets/{id}/paper` | [Read question or answer paper Markdown](#k12practicegetpracticepaper) |
| `POST /api/k12/practice-sets/{id}/verify` | [Update draft item verification](#k12practiceverifypracticeitem) |
| `POST /api/k12/practice-sets/custom-paper` | [Generate a custom paper with frozen parameters](#k12practicegeneratecustompaper) |
| `POST /api/k12/practice-sets/basket/items` | [Add one item to the practice basket](#k12practiceaddtobasket) |
| `POST /api/k12/practice-sets/{id}/items/remove` | [Remove a draft basket item](#k12practiceremovefrombasket) |
| `POST /api/k12/practice-sets/{id}/finalize` | [Finalize and publish or send a practice set](#k12practicefinalizepracticeset) |
| `POST /api/k12/practice-sets/{id}/print-jobs` | [Prepare a practice print job](#k12practicepreparepracticeprintjob) |
| `POST /api/k12/print-jobs` | [Prepare a generic artifact print job](#k12practicepreparegenericprintjob) |
| `POST /api/k12/print-artifacts` | [Freeze a downloadable PDF artifact](#k12practiceprepareprintableartifact) |
| `GET /api/k12/print-artifacts/{id}/content` | [Download frozen PDF bytes](#k12practicegetprintableartifactcontent) |
| `GET /api/k12/print-jobs/{id}` | [Read a native print job](#k12practicegetpracticeprintjob) |
| `GET /api/k12/print-jobs/{id}/paper` | [Read a print job frozen source](#k12practicegetpracticeprintjobpaper) |
| `POST /api/k12/print-jobs/{id}/events` | [Record a native print event](#k12practicerecordpracticeprintevent) |
| `POST /api/k12/print-jobs/{id}/commit` | [Commit a successful native print receipt](#k12practicecommitpracticeprintreceipt) |
| `POST /api/k12/print-jobs/{id}/retry` | [Retry a failed or cancelled print job](#k12practiceretrypracticeprintjob) |
| `POST /api/k12/practice-sets/{id}/submit` | [Submit a returned practice photo](#k12practicesubmitpracticeset) |
| `POST /api/k12/practice-sets/{id}/grade` | [Record manual item results](#k12practicegradepracticeset) |
| `POST /api/k12/practice-sets/{id}/close` | [Close a graded practice set](#k12practiceclosepracticeset) |
| `POST /api/k12/practice-sets/{id}/cancel` | [Cancel a draft practice set](#k12practicecancelpracticeset) |
| `GET /api/k12/weekly-practice/settings` | [Read weekly practice settings](#k12practicegetweeklypracticesettings) |
| `POST /api/k12/weekly-practice/plans` | [Ensure the current weekly plan](#k12practiceensureweeklypracticeplan) |
| `GET /api/k12/weekly-practice/plans/current` | [Read the current weekly plan](#k12practicegetcurrentweeklypracticeplan) |
| `GET /api/k12/weekly-practice/plans/history` | [Page through weekly practice history](#k12practicelistweeklypracticehistory) |
| `GET /api/k12/weekly-practice/snapshots/{id}` | [Read an immutable weekly snapshot](#k12practicegetweeklypracticesnapshot) |
| `POST /api/k12/weekly-practice/plans/{id}/prepare-output` | [Freeze weekly practice and prepare PDF](#k12practiceprepareweeklypracticeoutput) |
| `POST /api/k12/weekly-practice/snapshots/{id}/send` | [Send a frozen weekly PDF snapshot](#k12practicesendweeklypracticesnapshot) |
| `POST /api/k12/weekly-practice/snapshots/{id}/attempts` | [Submit one answer against a frozen snapshot](#k12practicesubmitweeklypracticeattempt) |
| `POST /api/k12/weekly-practice/plans/{id}/arithmetic-batches` | [Prepare an arithmetic batch with an explicit count](#k12practicecreateweeklyarithmeticbatch) |
| `POST /api/k12/weekly-practice/arithmetic-batches/{id}/start` | [Start a prepared arithmetic batch](#k12practicestartweeklyarithmeticbatch) |
| `POST /api/k12/weekly-practice/arithmetic-batches/{id}/retry` | [Retry a retryable failed arithmetic batch](#k12practiceretryweeklyarithmeticbatch) |
| `POST /api/k12/weekly-practice/arithmetic-batches/{id}/attempts` | [Submit one arithmetic batch answer](#k12practicesubmitweeklyarithmeticattempt) |
| `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/refresh` | [Refresh the textbook track against current progress](#k12practicerefreshweeklytextbooktrack) |
| `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/prepare` | [Prepare textbook consolidation with an explicit count](#k12practiceprepareweeklytextbooktrack) |
| `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/recovery-attempts` | [Authorize a textbook item solve recovery](#k12practicerecoverweeklytextbooktrack) |
| `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/reinterpretations` | [Reinterpret existing textbook receipts](#k12practicereinterpretweeklytextbooktrack) |
| `POST /api/k12/weekly-practice/plans/{id}/save-to-practice-set` | [Save frozen weekly practice to the basket](#k12practicesaveweeklypracticetopracticeset) |

## Call setup

```bash
export HEXCLAW_BASE_URL=http://127.0.0.1:16060
export HEXCLAW_TOKEN=YOUR_SERVICE_TOKEN
```

These commands are request templates, not instructions to resubmit unknown outcomes or manufacture native receipts.

<a id="k12practicemistakes"></a>

## `GET /api/k12/mistakes`

List mistakes and review states

total is the stored-record count before the review-state projection filter; it can exceed items length. There are no pagination parameters.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `agent` | query | string；minLength=1 | Yes | K12 agent name |
| `status` | query | string | No | Omit or all for no filter; review states filter review_state, other values filter record status |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [MistakeList](#schema-k12practicemistakelist) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 500 | `MessageError` | Unclassified store or dependency error |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/mistakes?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

Implementation: [mistakes](../../../../scenarios/k12/apihttp/handler.go).

<a id="k12practicedeletemistake"></a>

## `DELETE /api/k12/mistakes/{record_id}`

Delete a mistake

Permanently deletes a mistake in the agent scope, unlike reversible archive. Missing or differently scoped records return 404.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |
| `agent` | query | string；minLength=1 | Yes | K12 agent name |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [OK](#schema-k12practiceok) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |

```bash
curl -fS -X DELETE "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

Implementation: [deleteMistake](../../../../scenarios/k12/apihttp/handler.go).

<a id="k12practicearchivemistake"></a>

## `POST /api/k12/mistakes/{record_id}/archive`

Archive a mistake

Reversible soft archive retains its valid restoration snapshot; it does not establish mastery. Replay retains the same command key, version and payload.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPMistakeArchiveCommandReq](#schema-k12practicehttpmistakearchivecommandreq)；strict, rejects unknown fields.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `version` | integer；minimum=0 | Yes | Current record version, required and non-negative; restore using the returned newer version |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

Unknown fields are not part of this schema.

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [HTTPMistakeDTO](#schema-k12practicehttpmistakedto) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/archive" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","version":1,"idempotency_key":"archiveMistake-demo-1"}'
```

Implementation: [archiveMistake](../../../../scenarios/k12/apihttp/handler.go).

<a id="k12practicerestoremistake"></a>

## `POST /api/k12/mistakes/{record_id}/restore`

Restore an archived mistake

Undo and later restoration share the CAS command; read the latest version rather than reusing a stale one. Replay retains the same command key, version and payload.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPMistakeArchiveCommandReq](#schema-k12practicehttpmistakearchivecommandreq)；strict, rejects unknown fields.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `version` | integer；minimum=0 | Yes | Current record version, required and non-negative; restore using the returned newer version |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

Unknown fields are not part of this schema.

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [HTTPMistakeDTO](#schema-k12practicehttpmistakedto) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/restore" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","version":1,"idempotency_key":"restoreMistake-demo-1"}'
```

Implementation: [restoreMistake](../../../../scenarios/k12/apihttp/handler.go).

<a id="k12practicestartpracticegeneration"></a>

## `POST /api/k12/mistakes/{record_id}/practice-generation`

Start single-mistake variant generation

202 only accepts the command. Poll GET while pending; joined with readable practice_set_id/practice_item_id establishes basket insertion. Other states such as failed/hidden do not establish generated output. Explicit grade/textbook override profile defaults; textbook must resolve to a non-empty value. Supply provider/model together or omit both.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPSinglePracticeGenerationReq](#schema-k12practicehttpsinglepracticegenerationreq)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `grade` | string | No | JSON projection field; omitted when absent if optional. |
| `textbook` | string | No | JSON projection field; omitted when absent if optional. |
| `difficulty` | string (`same`, `easier`, `harder`) | No | Defaults to same when omitted |
| `provider` | string | No | JSON projection field; omitted when absent if optional. |
| `model` | string | No | JSON projection field; omitted when absent if optional. |
| `source_session` | string | No | JSON projection field; omitted when absent if optional. |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 202 | [SinglePracticeGenerationView](#schema-k12practicesinglepracticegenerationview) | Accepted; inspect the task terminal state |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 503 | `MessageError` | Dependency unavailable |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/practice-generation" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"variant-demo-1","difficulty":"same"}'
```

Implementation: [startPracticeGeneration](../../../../scenarios/k12/apihttp/handler.go).

<a id="k12practicegetpracticegeneration"></a>

## `GET /api/k12/mistakes/{record_id}/practice-generation`

Read single-mistake generation state

Reads the current single-question task projection with HTTP 200. This GET accepts no grade/textbook/provider/model fields and does not start or resend generation. Poll while pending; joined with readable practice_set_id/practice_item_id establishes basket insertion. Other states such as failed/hidden do not establish generated output.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |
| `agent` | query | string；minLength=1 | Yes | K12 agent name |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [SinglePracticeGenerationView](#schema-k12practicesinglepracticegenerationview) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/practice-generation?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

Implementation: [getPracticeGeneration](../../../../scenarios/k12/apihttp/handler.go).

<a id="k12practicegetpracticegenerationreceipts"></a>

## `GET /api/k12/mistakes/{record_id}/practice-generation/receipts`

Read redacted invocation receipts

Read-only durable receipt digests contain no credentials. Remote owner-to-agent authorization failures are collapsed to 404; an unresolved principal returns 401.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |
| `agent` | query | string；minLength=1 | Yes | K12 agent name |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [PracticeGenerationReceiptView](#schema-k12practicepracticegenerationreceiptview) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/practice-generation/receipts?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

Implementation: [getPracticeGenerationReceipts](../../../../scenarios/k12/apihttp/handler.go).

<a id="k12practiceretrypracticegeneration"></a>

## `POST /api/k12/mistakes/{record_id}/practice-generation/retry`

Retry single-mistake generation

Retries an unretired failed original task using its frozen snapshot. A sent/outcome_unknown physical invocation conflicts with 409 and requires reconciliation first. Hidden source mistakes may return hidden directly. No new textbook, difficulty or route parameters are accepted; poll GET to verify joined.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPAgentOnlyReq](#schema-k12practicehttpagentonlyreq)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 202 | [SinglePracticeGenerationView](#schema-k12practicesinglepracticegenerationview) | Accepted; inspect the task terminal state |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 503 | `MessageError` | Dependency unavailable |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/practice-generation/retry" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo"}'
```

Implementation: [retryPracticeGeneration](../../../../scenarios/k12/apihttp/handler.go).

<a id="k12practiceopenpracticecandidateselection"></a>

## `POST /api/k12/mistakes/{record_id}/practice-candidate-selection`

Open a candidate selection

Opens an open selection with an original-question candidate targeting the learner draft basket; this does not commit basket items. Profile defaults, textbook and paired provider/model rules match single generation.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPPracticeCandidateOpenReq](#schema-k12practicehttppracticecandidateopenreq)；strict, rejects unknown fields.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `grade` | string | No | JSON projection field; omitted when absent if optional. |
| `textbook` | string | No | JSON projection field; omitted when absent if optional. |
| `provider` | string | No | JSON projection field; omitted when absent if optional. |
| `model` | string | No | JSON projection field; omitted when absent if optional. |
| `source_session` | string | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [PracticeCandidateSelection](#schema-k12practicepracticecandidateselection) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/practice-candidate-selection" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"selection-demo-1"}'
```

Implementation: [openPracticeCandidateSelection](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go).

<a id="k12practicegeneratepracticecandidatebatch"></a>

## `POST /api/k12/practice-candidate-selections/{id}/batches`

Generate the next candidate batch

Only an open selection can generate against its current revision, with up to three variant candidates per batch. Each candidate retains ready/failed/already_in_set state; readable candidates are not committed basket items. Supply provider/model together.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPPracticeCandidateBatchReq](#schema-k12practicehttppracticecandidatebatchreq)；strict, rejects unknown fields.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `revision` | integer；minimum=1 | Yes | Current aggregate revision used by the matching revision/expected_revision CAS |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `provider` | string | No | JSON projection field; omitted when absent if optional. |
| `model` | string | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [PracticeCandidateSelection](#schema-k12practicepracticecandidateselection) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-candidate-selections/resource-demo-1/batches" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","revision":1,"idempotency_key":"candidate-batch-demo-1"}'
```

Implementation: [generatePracticeCandidateBatch](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go).

<a id="k12practicecommitpracticecandidateselection"></a>

## `POST /api/k12/practice-candidate-selections/{id}/commit`

Commit selected candidates to the basket

candidate_ids must be non-empty, distinct and belong to this selection. CAS and deduplicated basket insertion are transactional. Inspect added_count/already_present/replayed.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPPracticeCandidateCommitReq](#schema-k12practicehttppracticecandidatecommitreq)；strict, rejects unknown fields.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `revision` | integer；minimum=1 | Yes | Current aggregate revision used by the matching revision/expected_revision CAS |
| `candidate_ids` | array string；minLength=1；minItems=1; uniqueItems=True | Yes | JSON projection field; omitted when absent if optional. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

Unknown fields are not part of this schema.

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [StoragePracticeCandidateCommitReceipt](#schema-k12practicestoragepracticecandidatecommitreceipt) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-candidate-selections/resource-demo-1/commit" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","revision":1,"candidate_ids":["candidate-demo-1"],"idempotency_key":"candidate-commit-demo-1"}'
```

Implementation: [commitPracticeCandidateSelection](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go).

<a id="k12practicesuppressmistakereview"></a>

## `POST /api/k12/mistakes/{record_id}/suppress`

Suppress mistake review

Sets suppressed without deleting the mistake or establishing mastery. Use the current mistake version; the command key binds the same command digest.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPMistakeReviewCommandReq](#schema-k12practicehttpmistakereviewcommandreq)；strict, rejects unknown fields.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `version` | integer；minimum=0 | Yes | Current record or job optimistic-lock version |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `plan_id` | string | No | JSON projection field; omitted when absent if optional. |
| `plan_revision` | integer | No | Plan revision bound to the snapshot or used by request CAS |
| `weekly_item_id` | string | No | JSON projection field; omitted when absent if optional. |
| `iso_year` | integer | No | JSON projection field; omitted when absent if optional. |
| `iso_week` | integer | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [HTTPMistakeDTO](#schema-k12practicehttpmistakedto) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/suppress" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","version":1,"idempotency_key":"suppressMistakeReview-demo-1"}'
```

Implementation: [suppressMistakeReview](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go).

<a id="k12practicerestoremistakereview"></a>

## `POST /api/k12/mistakes/{record_id}/restore-review`

Restore mistake review

Restores review scheduling; this is distinct from restoring an archived record through /restore. Use the current mistake version; the command key binds the same command digest.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPMistakeReviewCommandReq](#schema-k12practicehttpmistakereviewcommandreq)；strict, rejects unknown fields.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `version` | integer；minimum=0 | Yes | Current record or job optimistic-lock version |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `plan_id` | string | No | JSON projection field; omitted when absent if optional. |
| `plan_revision` | integer | No | Plan revision bound to the snapshot or used by request CAS |
| `weekly_item_id` | string | No | JSON projection field; omitted when absent if optional. |
| `iso_year` | integer | No | JSON projection field; omitted when absent if optional. |
| `iso_week` | integer | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [HTTPMistakeDTO](#schema-k12practicehttpmistakedto) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/restore-review" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","version":1,"idempotency_key":"restoreMistakeReview-demo-1"}'
```

Implementation: [restoreMistakeReview](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go).

<a id="k12practicedefermistakethisweek"></a>

## `POST /api/k12/mistakes/{record_id}/defer-this-week`

Defer a mistake for one ISO week

iso_year and iso_week (1–53) are required; deferral applies to that ISO week only. With plan_id, plan_revision and weekly_item_id must identify the matching source item in that plan. Use the current mistake version; the command key binds the same command digest.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `record_id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [DeferMistakeRequest](#schema-k12practicedefermistakerequest)；strict, rejects unknown fields.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `version` | integer；minimum=0 | Yes | Current record or job optimistic-lock version |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `plan_id` | string | No | JSON projection field; omitted when absent if optional. |
| `plan_revision` | integer | No | Plan revision bound to the snapshot or used by request CAS |
| `weekly_item_id` | string | No | JSON projection field; omitted when absent if optional. |
| `iso_year` | integer；minimum=1 | Yes | JSON projection field; omitted when absent if optional. |
| `iso_week` | integer；minimum=1; maximum=53 | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [StorageMistakeReviewCommandResult](#schema-k12practicestoragemistakereviewcommandresult) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/mistakes/mistake-demo-1/defer-this-week" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","version":1,"idempotency_key":"deferMistakeThisWeek-demo-1","iso_year":2026,"iso_week":41}'
```

Implementation: [deferMistakeThisWeek](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go).

<a id="k12practicelistpracticesets"></a>

## `GET /api/k12/practice-sets`

List practice sets

Returns practice sets and visible ready items; generation placeholders not yet ready are omitted from public items.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `agent` | query | string；minLength=1 | Yes | K12 agent name |
| `status` | query | string (`draft`, `confirmed`, `assigned`, `submitted`, `graded`, `closed`, `cancelled`) | No | Omit to include all statuses |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [SetList](#schema-k12practicesetlist) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 500 | `MessageError` | Unclassified store or dependency error |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/practice-sets?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

Implementation: [listPracticeSets](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practicegetpracticeset"></a>

## `GET /api/k12/practice-sets/{id}`

Read a practice set

Includes items/result_correct and returned-photo/regrade projections. Absent or null result_correct means no conclusion, not false.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |
| `agent` | query | string；minLength=1 | Yes | K12 agent name |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

Implementation: [getPracticeSet](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practicegetpracticepaper"></a>

## `GET /api/k12/practice-sets/{id}/paper`

Read question or answer paper Markdown

Draft returns preview=true without a formal paper number; finalized sets return the formal paper. JSON Markdown is neither PDF bytes nor a printed receipt.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |
| `agent` | query | string；minLength=1 | Yes | K12 agent name |
| `kind` | query | string (`question`, `answer`) | No | Defaults to question |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [Paper](#schema-k12practicepaper) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/paper?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

Implementation: [getPracticePaper](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practiceverifypracticeitem"></a>

## `POST /api/k12/practice-sets/{id}/verify`

Update draft item verification

Draft only; verified requires evidence and actual subject verifier support. This endpoint does not grade learner answers or confirm mastery.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPVerifyItemReq](#schema-k12practicehttpverifyitemreq)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `item_id` | string | Yes | Current practice-item ID, distinct from its source mistake ID |
| `status` | string (`pending`, `verified`, `needs_review`, `rejected`, `stale`) | Yes | JSON projection field; omitted when absent if optional. |
| `evidence` | string | No | Required and non-empty for verified; may be empty for other states |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/verify" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","item_id":"item-demo-1","status":"needs_review","evidence":"Pending independent verification"}'
```

Implementation: [verifyPracticeItem](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practicegeneratecustompaper"></a>

## `POST /api/k12/practice-sets/custom-paper`

Generate a custom paper with frozen parameters

The backend generates, verifies, deduplicates and atomically inserts items. total is all/5/10 and per_source is 1–3 variants per source. Profile may supply grade/textbook; provider/model are paired. Changing frozen parameters under the same command key returns 400; current synchronous success has status=committed. Inspect per-item verification, set.items and added; a job ID alone is not printable output.

JSON body: [HTTPCustomPaperReq](#schema-k12practicehttpcustompaperreq)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `scope` | string (`week`, `unmastered`) | Yes | JSON projection field; omitted when absent if optional. |
| `total` | string (`all`, `5`, `10`) / integer (`5`, `10`) | Yes | JSON projection field; omitted when absent if optional. |
| `per_source` | integer；minimum=1; maximum=3 | Yes | JSON projection field; omitted when absent if optional. |
| `difficulty` | string (`same`, `easier`, `harder`) | Yes | JSON projection field; omitted when absent if optional. |
| `textbook` | string | No | Explicit value or profile default; must resolve to non-empty |
| `grade` | string | No | JSON projection field; omitted when absent if optional. |
| `provider` | string | No | JSON projection field; omitted when absent if optional. |
| `model` | string | No | JSON projection field; omitted when absent if optional. |
| `source_session` | string | No | JSON projection field; omitted when absent if optional. |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [CustomPaperResponse](#schema-k12practicecustompaperresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/custom-paper" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"custom-paper-demo-1","scope":"unmastered","total":5,"per_source":1,"difficulty":"same"}'
```

Implementation: [generateCustomPaper](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practiceaddtobasket"></a>

## `POST /api/k12/practice-sets/basket/items`

Add one item to the practice basket

One draft basket per learner, with content deduplication. The service assigns item_id if omitted. subject allows 数学/语文/英语/科学/信息科技 or empty; English aliases and art are not allowed. Only the documented item input fields are used; paper sequence, return and result projection fields are not written. verification_status defaults to pending. The caller supplies status and verification_evidence; this command does not perform independent verification, and verified is constrained by subject support.

JSON body: [HTTPAddToBasketReq](#schema-k12practicehttpaddtobasketreq)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `source_session` | string | No | JSON projection field; omitted when absent if optional. |
| `item` | [BasketItemRequest](#schema-k12practicebasketitemrequest) | Yes | JSON projection field; omitted when absent if optional. |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [BasketAddResponse](#schema-k12practicebasketaddresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/basket/items" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","item":{"question_markdown":"计算 2 + 3","subject":"数学","verification_status":"pending"}}'
```

Implementation: [addToBasket](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practiceremovefrombasket"></a>

## `POST /api/k12/practice-sets/{id}/items/remove`

Remove a draft basket item

Only an editable draft basket can remove items; this command cannot edit a frozen paper.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPRemoveFromBasketReq](#schema-k12practicehttpremovefrombasketreq)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `item_id` | string | Yes | Current practice-item ID, distinct from its source mistake ID |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/items/remove" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","item_id":"item-demo-1"}'
```

Implementation: [removeFromBasket](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practicefinalizepracticeset"></a>

## `POST /api/k12/practice-sets/{id}/finalize`

Finalize and publish or send a practice set

Skips non-verified items and requires at least one publishable item before assigned. via=send resolves all active direct IM targets server-side; inspect delivery batch receipts per target. Unconfigured delivery returns 501; no active direct binding returns 409. via=print publishes a paper but HTTP success does not establish native printing; use the two-phase print-jobs→commit receipt workflow.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPFinalizeReq](#schema-k12practicehttpfinalizereq)；strict, rejects unknown fields.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `via` | string (`print`, `send`) | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [FinalizeResponse](#schema-k12practicefinalizeresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 501 | `MessageError` | Phone delivery is not configured |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/finalize" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","via":"print"}'
```

Implementation: [finalizePracticeSet](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practicepreparepracticeprintjob"></a>

## `POST /api/k12/practice-sets/{id}/print-jobs`

Prepare a practice print job

Phase one freezes the source, reserves paper number/artifacts and leaves the set draft. Only committing a native printed receipt finalizes the set in the same transaction. Matching commands return replayed=true; cancellation/failure is not printed.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPPreparePracticePrintReq](#schema-k12practicehttppreparepracticeprintreq)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `artifact_kind` | string (`question`, `answer`) | No | Defaults to question |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 201 | [PrintJobPrepareResponse](#schema-k12practiceprintjobprepareresponse) | Created |
| 200 | [PrintJobPrepareResponse](#schema-k12practiceprintjobprepareresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/print-jobs" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"print-demo-1","artifact_kind":"question"}'
```

Implementation: [preparePracticePrintJob](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practicepreparegenericprintjob"></a>

## `POST /api/k12/print-jobs`

Prepare a generic artifact print job

Choose exactly one input variant: artifact_id references an existing frozen artifact, or canonical input requires source_kind/source_ref/title/canonical_markdown. Both require agent/idempotency_key. The source business state is not mutated. gprint- IDs use the shared /print-jobs read/event/commit/retry routes.

JSON body: [GenericPrintRequest](#schema-k12practicegenericprintrequest)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | JSON projection field; omitted when absent if optional. |
| `idempotency_key` | string；minLength=1 | Yes | Command key, at most 512 UTF-8 bytes |
| `artifact_id` | string | No | JSON projection field; omitted when absent if optional. |
| `source_kind` | string | No | JSON projection field; omitted when absent if optional. |
| `source_ref` | string | No | Source reference, at most 512 UTF-8 bytes |
| `title` | string | No | Title, at most 256 UTF-8 bytes |
| `canonical_markdown` | string | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Variant and conditional requirements:

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

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 201 | [PrintJobPrepareResponse](#schema-k12practiceprintjobprepareresponse) | Created |
| 200 | [PrintJobPrepareResponse](#schema-k12practiceprintjobprepareresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/print-jobs" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"generic-print-demo-1","artifact_id":"artifact-demo-1"}'
```

Implementation: [prepareGenericPrintJob](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practiceprepareprintableartifact"></a>

## `POST /api/k12/print-artifacts`

Freeze a downloadable PDF artifact

Choose canonical input or final_artifact_id+final_artifact_digest; the grading final-artifact variant also requires title and an exact frozen digest. No idempotency_key is required; artifacts deduplicate by content identity. The response is PDF metadata; fetch content for actual bytes. HTTP JSON bodies remain limited to 1 MiB.

JSON body: [PrintableArtifactRequest](#schema-k12practiceprintableartifactrequest)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | JSON projection field; omitted when absent if optional. |
| `source_kind` | string | No | JSON projection field; omitted when absent if optional. |
| `source_ref` | string | No | JSON projection field; omitted when absent if optional. |
| `title` | string；minLength=1 | Yes | Title, at most 256 UTF-8 bytes |
| `canonical_markdown` | string | No | JSON projection field; omitted when absent if optional. |
| `final_artifact_id` | string | No | JSON projection field; omitted when absent if optional. |
| `final_artifact_digest` | string | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Variant and conditional requirements:

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

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 201 | [ArtifactPrepareResponse](#schema-k12practiceartifactprepareresponse) | Created |
| 200 | [ArtifactPrepareResponse](#schema-k12practiceartifactprepareresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/print-artifacts" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","source_kind":"tutoring_tips","source_ref":"tips-demo-1","title":"辅导要点","canonical_markdown":"# 辅导要点\n\n先核对已知条件。"}'
```

Implementation: [preparePrintableArtifact](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practicegetprintableartifactcontent"></a>

## `GET /api/k12/print-artifacts/{id}/content`

Download frozen PDF bytes

Content-Type is the frozen rendered type (PDF); X-Content-SHA256 is the byte digest and ETag is the quoted digest. No kind is accepted and no print job is created. This handler does not implement conditional GET/304.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |
| `agent` | query | string；minLength=1 | Yes | K12 agent name |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | string | Frozen PDF bytes with digest and ETag |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/print-artifacts/resource-demo-1/content?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -o frozen-print-demo.pdf
```

Implementation: [getPrintableArtifactContent](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practicegetpracticeprintjob"></a>

## `GET /api/k12/print-jobs/{id}`

Read a native print job

Reads either a practice job or a gprint- generic job by ID prefix. printed plus a valid native receipt establishes success; submitted does not.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |
| `agent` | query | string；minLength=1 | Yes | K12 agent name |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [PrintJobResponse](#schema-k12practiceprintjobresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/print-jobs/resource-demo-1?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

Implementation: [getPracticePrintJob](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practicegetpracticeprintjobpaper"></a>

## `GET /api/k12/print-jobs/{id}/paper`

Read a print job frozen source

Returns the prepared immutable source digest/Markdown; practice requests can lazily freeze the requested question/answer PDF from that source. Generic responses include source_kind/source_ref rather than practice kind/paper_no. Download PDF through print-artifacts content.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |
| `agent` | query | string；minLength=1 | Yes | K12 agent name |
| `kind` | query | string (`question`, `answer`) | No | Practice jobs only; defaults to question. Ignored for generic jobs |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [PrintPaper](#schema-k12practiceprintpaper) / [GenericPrintPaper](#schema-k12practicegenericprintpaper) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/print-jobs/resource-demo-1/paper?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

Implementation: [getPracticePrintJobPaper](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practicerecordpracticeprintevent"></a>

## `POST /api/k12/print-jobs/{id}/events`

Record a native print event

Persists dialog_open/submitted/cancelled/failed/outcome_unknown; failed/outcome_unknown require failure_kind. printed requires native_job_id/native_receipt_id and non-empty object printer_snapshot, using the same transactional success boundary. Do not retry an unknown print outcome.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPPracticePrintEventReq](#schema-k12practicehttppracticeprinteventreq)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `status` | string (`dialog_open`, `submitted`, `printed`, `cancelled`, `failed`, `outcome_unknown`) | Yes | JSON projection field; omitted when absent if optional. |
| `native_job_id` | string | No | Native print job identity used to reconcile unknown outcomes |
| `native_receipt_id` | string | No | Native success receipt identity, distinct from acceptance or a dialog state |
| `printer_snapshot` | object | No | JSON projection field; omitted when absent if optional. |
| `failure_kind` | string | No | Failure classification, required for failed or outcome_unknown events |
| `failure_detail` | string | No | JSON projection field; omitted when absent if optional. |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [PrintJobResponse](#schema-k12practiceprintjobresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/print-jobs/resource-demo-1/events" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","status":"dialog_open"}'
```

Implementation: [recordPracticePrintEvent](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practicecommitpracticeprintreceipt"></a>

## `POST /api/k12/print-jobs/{id}/commit`

Commit a successful native print receipt

The printed success boundary accepts identical native receipts idempotently. Practice finalization and printed state commit in one SQLite transaction. Different receipts/source revisions conflict; outcome_unknown requires an explicit reconciliation receipt for the same native_job_id.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPPracticePrintCommitReq](#schema-k12practicehttppracticeprintcommitreq)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `native_job_id` | string | Yes | Native print job identity used to reconcile unknown outcomes |
| `native_receipt_id` | string | Yes | Native success receipt identity, distinct from acceptance or a dialog state |
| `printer_snapshot` | object；minProperties=1 | Yes | JSON projection field; omitted when absent if optional. |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [PrintJobResponse](#schema-k12practiceprintjobresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/print-jobs/resource-demo-1/commit" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","native_job_id":"native-job-demo-1","native_receipt_id":"native-receipt-demo-1","printer_snapshot":{"name":"Demo printer"}}'
```

Implementation: [commitPracticePrintReceipt](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practiceretrypracticeprintjob"></a>

## `POST /api/k12/print-jobs/{id}/retry`

Retry a failed or cancelled print job

Only cancelled/failed jobs allow ordinary retry, retaining the frozen source and paper number. Reconcile outcome_unknown instead of resending. Practice jobs require the unchanged draft/version; generic jobs allow at most three attempts.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPAgentOnlyReq](#schema-k12practicehttpagentonlyreq)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [PrintJobResponse](#schema-k12practiceprintjobresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/print-jobs/resource-demo-1/retry" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo"}'
```

Implementation: [retryPracticePrintJob](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practicesubmitpracticeset"></a>

## `POST /api/k12/practice-sets/{id}/submit`

Submit a returned practice photo

return_id identifies an idempotent photo return, using an existing asset ID. Choose auto_match=true or non-empty item_ids, never both; empty coverage does not mean the whole paper. HTTP 200 accepts the photo; read return_assets terminal regrade state and annotated_asset_id/result_markdown. Only actually covered, reliably assessed answers accumulate review evidence once; replay does not advance it again.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPSubmitReturnReq](#schema-k12practicehttpsubmitreturnreq)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `return_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `asset_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `item_ids` | array string；minLength=1 | No | Practice-item IDs covered by this operation; empty does not mean the entire paper |
| `auto_match` | boolean；default=False | No | JSON projection field; omitted when absent if optional. |

Variant and conditional requirements:

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

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/submit" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","return_id":"return-demo-1","asset_id":"asset-demo-1","auto_match":true}'
```

Implementation: [submitPracticeSet](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practicegradepracticeset"></a>

## `POST /api/k12/practice-sets/{id}/grade`

Record manual item results

results=[{item_id,correct}] records human_confirmed and can advance review intervals without establishing system mastery evidence. Missing/empty results retain legacy whole-set progression without mistake linkage; new callers should always supply item results. Each correct value is boolean; the current decoder defaults an omitted value to false, which does not mean “no conclusion.” Callers should always supply it explicitly. Photo-based system regrading is coordinated internally.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPGradeResultsReq](#schema-k12practicehttpgraderesultsreq)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `results` | array / null object | No | JSON projection field; omitted when absent if optional. |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/grade" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","results":[{"item_id":"item-demo-1","correct":true}]}'
```

Implementation: [gradePracticeSet](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practiceclosepracticeset"></a>

## `POST /api/k12/practice-sets/{id}/close`

Close a graded practice set

Only graded→closed; reason is a query parameter, not a request-body field.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |
| `reason` | query | string (`manual`, `semester`) | No | Defaults to manual |

JSON body: [HTTPAgentOnlyReq](#schema-k12practicehttpagentonlyreq)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/close" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo"}'
```

Implementation: [closePracticeSet](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practicecancelpracticeset"></a>

## `POST /api/k12/practice-sets/{id}/cancel`

Cancel a draft practice set

Only draft/confirmed→cancelled; does not delete published or answered papers.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPAgentOnlyReq](#schema-k12practicehttpagentonlyreq)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/practice-sets/resource-demo-1/cancel" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo"}'
```

Implementation: [cancelPracticeSet](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="k12practicegetweeklypracticesettings"></a>

## `GET /api/k12/weekly-practice/settings`

Read weekly practice settings

Defaults: Asia/Shanghai; due_review_enabled=true; textbook_consolidation_enabled=false; tier=standard; arithmetic_warmup_enabled=false; arithmetic_minutes=2. This read does not update settings; use the atomic profile-bundle command.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `agent` | query | string；minLength=1 | Yes | K12 agent name |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [WeeklyPracticeSettings](#schema-k12practiceweeklypracticesettings) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/weekly-practice/settings?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

Implementation: [getWeeklyPracticeSettings](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="k12practiceensureweeklypracticeplan"></a>

## `POST /api/k12/weekly-practice/plans`

Ensure the current weekly plan

Uses server time and the settings timezone to compute the ISO week; no client date or item count is accepted. Reuses the existing agent/week/timezone plan and reconciles week boundaries. Frozen plans are not regenerated by new recommendations or progress; supplementary tracks use explicit prepare.

JSON body: [HTTPWeeklyPlanCommandRequest](#schema-k12practicehttpweeklyplancommandrequest)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 201 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | Created |
| 200 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"weekly-plan-demo-1"}'
```

Implementation: [ensureWeeklyPracticePlan](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="k12practicegetcurrentweeklypracticeplan"></a>

## `GET /api/k12/weekly-practice/plans/current`

Read the current weekly plan

Returns 200 {"plan":null} when no current plan exists; does not implicitly create questions. Reads reconcile week-boundary state using server time.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `agent` | query | string；minLength=1 | Yes | K12 agent name |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [CurrentPlanResponse](#schema-k12practicecurrentplanresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/current?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

Implementation: [getCurrentWeeklyPracticePlan](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="k12practicelistweeklypracticehistory"></a>

## `GET /api/k12/weekly-practice/plans/history`

Page through weekly practice history

Returns archived summaries; read snapshot for frozen questions/content. Do not construct pagination cursors.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `agent` | query | string；minLength=1 | Yes | K12 agent name |
| `limit` | query | integer；default=20; minimum=1; maximum=100 | No | Defaults to 20; range 1–100 |
| `cursor` | query | string | No | Pass previous next_cursor unchanged; null means no next page |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [WeeklyHistoryResponse](#schema-k12practiceweeklyhistoryresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/history?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

Implementation: [listWeeklyPracticeHistory](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="k12practicegetweeklypracticesnapshot"></a>

## `GET /api/k12/weekly-practice/snapshots/{id}`

Read an immutable weekly snapshot

The snapshot preserves plan_revision, questions, source/verification evidence and digests; current textbook recommendations do not rewrite history.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |
| `agent` | query | string；minLength=1 | Yes | K12 agent name |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [WeeklyPracticeSnapshot](#schema-k12practiceweeklypracticesnapshot) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |

```bash
curl -fS "$HEXCLAW_BASE_URL/api/k12/weekly-practice/snapshots/resource-demo-1?agent=tutor-demo" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN"
```

Implementation: [getWeeklyPracticeSnapshot](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="k12practiceprepareweeklypracticeoutput"></a>

## `POST /api/k12/weekly-practice/plans/{id}/prepare-output`

Freeze weekly practice and prepare PDF

CAS checks plan revision, freezes draft into a snapshot and returns PDF metadata. The same frozen revision replays existing output without regenerating questions. Download actual PDF through artifact_id/content; HTTP success does not establish printing or delivery.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPWeeklyExpectedRevisionRequest](#schema-k12practicehttpweeklyexpectedrevisionrequest)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `expected_revision` | integer；minimum=1 | Yes | JSON projection field; omitted when absent if optional. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 201 | [WeeklyOutputResponse](#schema-k12practiceweeklyoutputresponse) | Created |
| 200 | [WeeklyOutputResponse](#schema-k12practiceweeklyoutputresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/resource-demo-1/prepare-output" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","expected_revision":1,"idempotency_key":"weekly-output-demo-1"}'
```

Implementation: [prepareWeeklyPracticeOutput](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="k12practicesendweeklypracticesnapshot"></a>

## `POST /api/k12/weekly-practice/snapshots/{id}/send`

Send a frozen weekly PDF snapshot

Sends the existing PDF to the agent active direct bindings; accepts no target selection or new-question parameters. Command and content-batch deduplication apply. Inspect per-target delivery state/receipt in the returned batch; do not blindly resend unknown outcomes.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPWeeklySendRequest](#schema-k12practicehttpweeklysendrequest)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [DeliveryBatch](#schema-k12practicedeliverybatch) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/snapshots/resource-demo-1/send" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"weekly-send-demo-1"}'
```

Implementation: [sendWeeklyPracticeSnapshot](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="k12practicesubmitweeklypracticeattempt"></a>

## `POST /api/k12/weekly-practice/snapshots/{id}/attempts`

Submit one answer against a frozen snapshot

Assesses one item using frozen question/answer keys with correct/wrong/needs_review result. Identical command key and answer replay the same durable assessment; changed content conflicts. Reconcile unknown model receipts without another invocation. Only reliably assessed system answers accumulate evidence; needs_review does not establish mastery.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPWeeklyAttemptRequest](#schema-k12practicehttpweeklyattemptrequest)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `item_id` | string | Yes | Current practice-item ID, distinct from its source mistake ID |
| `student_answer` | string | Yes | JSON projection field; omitted when absent if optional. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 201 | [WeeklyAttemptResponse](#schema-k12practiceweeklyattemptresponse) | Created |
| 200 | [WeeklyAttemptResponse](#schema-k12practiceweeklyattemptresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/snapshots/resource-demo-1/attempts" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","item_id":"weekly-item-demo-1","student_answer":"5","idempotency_key":"weekly-answer-demo-1"}'
```

Implementation: [submitWeeklyPracticeAttempt](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="k12practicecreateweeklyarithmeticbatch"></a>

## `POST /api/k12/weekly-practice/plans/{id}/arithmetic-batches`

Prepare an arithmetic batch with an explicit count

Strict body rejects agent; the service resolves the learner from plan ID. Use the latest plan_revision and explicit item_count 1–20; recommendations are not substituted. Inspect the public batch projection to distinguish preparation from completion.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPWeeklyManualArithmeticRequest](#schema-k12practicehttpweeklymanualarithmeticrequest)；strict, rejects unknown fields.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `plan_revision` | integer；minimum=1 | Yes | Plan revision bound to the snapshot or used by request CAS |
| `item_count` | integer；minimum=1; maximum=20 | Yes | JSON projection field; omitted when absent if optional. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

Unknown fields are not part of this schema.

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 201 | [ArithmeticBatchResponse](#schema-k12practicearithmeticbatchresponse) | Created |
| 200 | [ArithmeticBatchResponse](#schema-k12practicearithmeticbatchresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/resource-demo-1/arithmetic-batches" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"plan_revision":1,"item_count":10,"idempotency_key":"arithmetic-batch-demo-1"}'
```

Implementation: [createWeeklyArithmeticBatch](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="k12practicestartweeklyarithmeticbatch"></a>

## `POST /api/k12/weekly-practice/arithmetic-batches/{id}/start`

Start a prepared arithmetic batch

Transitions a ready batch to in_progress without creating/regenerating questions; the command key binds this batch.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPWeeklySendRequest](#schema-k12practicehttpweeklysendrequest)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [ArithmeticBatchResponse](#schema-k12practicearithmeticbatchresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/arithmetic-batches/resource-demo-1/start" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"arithmetic-start-demo-1"}'
```

Implementation: [startWeeklyArithmeticBatch](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="k12practiceretryweeklyarithmeticbatch"></a>

## `POST /api/k12/weekly-practice/arithmetic-batches/{id}/retry`

Retry a retryable failed arithmetic batch

Resumes an eligible retryable failure from its original checkpoint. ready/in_progress/completed are not regeneration entry points; unknown model calls are not blindly repeated. Inspect batch.retryable/state rather than inferring success.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPWeeklySendRequest](#schema-k12practicehttpweeklysendrequest)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [ArithmeticBatchResponse](#schema-k12practicearithmeticbatchresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/arithmetic-batches/resource-demo-1/retry" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","idempotency_key":"arithmetic-retry-demo-1"}'
```

Implementation: [retryWeeklyArithmeticBatch](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="k12practicesubmitweeklyarithmeticattempt"></a>

## `POST /api/k12/weekly-practice/arithmetic-batches/{id}/attempts`

Submit one arithmetic batch answer

Uses frozen batch item_id/answer keys, persists a one-item assessment and returns correct/wrong/needs_review. Matching key/answer replay does not advance evidence again; incorrect work can schedule mistake review. Uncovered batch items do not count as answered.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPWeeklyAttemptRequest](#schema-k12practicehttpweeklyattemptrequest)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `item_id` | string | Yes | Current practice-item ID, distinct from its source mistake ID |
| `student_answer` | string | Yes | JSON projection field; omitted when absent if optional. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 201 | [ArithmeticAttemptResponse](#schema-k12practicearithmeticattemptresponse) | Created |
| 200 | [ArithmeticAttemptResponse](#schema-k12practicearithmeticattemptresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 500 | `MessageError` | Unclassified store or dependency error |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/arithmetic-batches/resource-demo-1/attempts" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","item_id":"arithmetic-item-demo-1","student_answer":"5","idempotency_key":"arithmetic-answer-demo-1"}'
```

Implementation: [submitWeeklyArithmeticAttempt](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="k12practicerefreshweeklytextbooktrack"></a>

## `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/refresh`

Refresh the textbook track against current progress

Draft only: the track must be enabled and either have newer progress or a prior generation failure; expected_revision uses CAS. Stale progress creates a newer revision with 201; failure recovery or replay returns 200. Only the textbook section is replaced; frozen papers are never overwritten or automatically regenerated.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPWeeklyExpectedRevisionRequest](#schema-k12practicehttpweeklyexpectedrevisionrequest)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `expected_revision` | integer；minimum=1 | Yes | JSON projection field; omitted when absent if optional. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 200 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | Success or matching command replay |
| 201 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | Created |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/resource-demo-1/tracks/textbook_consolidation/refresh" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","expected_revision":1,"idempotency_key":"textbook-refresh-demo-1"}'
```

Implementation: [refreshWeeklyTextbookTrack](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="k12practiceprepareweeklytextbooktrack"></a>

## `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/prepare`

Prepare textbook consolidation with an explicit count

Strict body rejects agent and resolves the learner from plan ID. plan_revision uses CAS and item_count is 1–10. Requires valid textbook binding/progress/source evidence. Recommended counts remain recommendations; only explicit commands generate, without rewriting existing frozen content.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPWeeklyManualTextbookRequest](#schema-k12practicehttpweeklymanualtextbookrequest)；strict, rejects unknown fields.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `plan_revision` | integer；minimum=1 | Yes | Plan revision bound to the snapshot or used by request CAS |
| `item_count` | integer；minimum=1; maximum=10 | Yes | JSON projection field; omitted when absent if optional. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

Unknown fields are not part of this schema.

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 201 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | Created |
| 200 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/resource-demo-1/tracks/textbook_consolidation/prepare" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"plan_revision":1,"item_count":5,"idempotency_key":"textbook-prepare-demo-1"}'
```

Implementation: [prepareWeeklyTextbookTrack](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="k12practicerecoverweeklytextbooktrack"></a>

## `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/recovery-attempts`

Authorize a textbook item solve recovery

This recovery may append a physical solve, so accept_duplicate_execution must explicitly be true. Strict body rejects agent. source_command_key/checkpoint_sha256/zero-based item_index bind the exact original checkpoint. Legacy plan_revision>0 supplies both source/expected revisions; separated source_plan_revision must be >=1 and expected_plan_revision >= source; if legacy is also supplied it must equal source. Changed source profile/textbook/progress conflicts; original receipts and unknown outcomes are retained.

This command requires an already-known original recovery context: source_command_key is the original textbook prepare/refresh command key; checkpoint_sha256 is the actual SHA-256 of the persisted checkpoint's raw JSON bytes, and item_index indexes generation.items in that checkpoint. Current public plan/snapshot queries do not expose the raw checkpoint, its digest or these indexes. Do not derive them from snapshot_digest, the public item array or the current revision. The all-zero digest in the example is only a request-shape placeholder; the command is not directly usable without real recovery context. Omitting item_index decodes to 0; callers should always specify the intended index explicitly.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [WeeklyTextbookRecoveryRequest](#schema-k12practiceweeklytextbookrecoveryrequest)；strict, rejects unknown fields.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `source_command_key` | string；minLength=1 | Yes | Original textbook prepare/refresh command key from existing recovery context. |
| `plan_revision` | integer；minimum=1 | No | Plan revision bound to the snapshot or used by request CAS |
| `checkpoint_sha256` | string；pattern=^[a-fA-F0-9]{64}$ | Yes | Actual raw-byte SHA-256 of the original persisted checkpoint, unavailable from public plan/snapshot queries; use the actual lowercase digest. |
| `item_index` | integer；minimum=0; default=0 | No | Zero-based generation.items index in the original checkpoint; omission selects the first item, so specify it explicitly. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `accept_duplicate_execution` | boolean (`true`) | Yes | JSON projection field; omitted when absent if optional. |
| `source_plan_revision` | integer；minimum=1 | No | JSON projection field; omitted when absent if optional. |
| `expected_plan_revision` | integer；minimum=1 | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Variant and conditional requirements:

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

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 201 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | Created |
| 200 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/resource-demo-1/tracks/textbook_consolidation/recovery-attempts" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"source_command_key":"textbook-prepare-demo-1","plan_revision":1,"checkpoint_sha256":"0000000000000000000000000000000000000000000000000000000000000000","item_index":0,"idempotency_key":"recoverWeeklyTextbookTrack-demo-1","accept_duplicate_execution":true}'
```

Implementation: [recoverWeeklyTextbookTrack](../../../../scenarios/k12/apihttp/weekly_recovery_handler.go).

<a id="k12practicereinterpretweeklytextbooktrack"></a>

## `POST /api/k12/weekly-practice/plans/{id}/tracks/textbook_consolidation/reinterpretations`

Reinterpret existing textbook receipts

Reads and reinterprets successful existing physical receipts only; no new model invocation is authorized. Strict body rejects agent. source_command_key/checkpoint_sha256/zero-based item_index bind the exact original checkpoint. Legacy plan_revision>0 supplies both source/expected revisions; separated source_plan_revision must be >=1 and expected_plan_revision >= source; if legacy is also supplied it must equal source. Changed source profile/textbook/progress conflicts; original receipts and unknown outcomes are retained.

As with recovery, this requires the actual raw-byte digest of the original persisted checkpoint and its generation.items index. Public plan/snapshot queries do not expose those fields; snapshot_digest or the public item order cannot substitute for them. The example shows the request shape and requires real existing recovery context before submission. Omitting item_index defaults to 0 in the decoder; specify it explicitly.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [WeeklyTextbookReinterpretationRequest](#schema-k12practiceweeklytextbookreinterpretationrequest)；strict, rejects unknown fields.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `source_command_key` | string；minLength=1 | Yes | Original textbook prepare/refresh command key from existing recovery context. |
| `plan_revision` | integer；minimum=1 | No | Plan revision bound to the snapshot or used by request CAS |
| `checkpoint_sha256` | string；pattern=^[a-fA-F0-9]{64}$ | Yes | Actual raw-byte SHA-256 of the original persisted checkpoint, unavailable from public plan/snapshot queries; use the actual lowercase digest. |
| `item_index` | integer；minimum=0; default=0 | No | Zero-based generation.items index in the original checkpoint; omission selects the first item, so specify it explicitly. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `source_plan_revision` | integer；minimum=1 | No | JSON projection field; omitted when absent if optional. |
| `expected_plan_revision` | integer；minimum=1 | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Variant and conditional requirements:

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

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 201 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | Created |
| 200 | [PlanReplayResponse](#schema-k12practiceplanreplayresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/resource-demo-1/tracks/textbook_consolidation/reinterpretations" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"source_command_key":"textbook-prepare-demo-1","plan_revision":1,"checkpoint_sha256":"0000000000000000000000000000000000000000000000000000000000000000","item_index":0,"idempotency_key":"reinterpretWeeklyTextbookTrack-demo-1"}'
```

Implementation: [reinterpretWeeklyTextbookTrack](../../../../scenarios/k12/apihttp/weekly_recovery_handler.go).

<a id="k12practicesaveweeklypracticetopracticeset"></a>

## `POST /api/k12/weekly-practice/plans/{id}/save-to-practice-set`

Save frozen weekly practice to the basket

Requires a frozen plan matching expected_revision. Inserts verified snapshot items into the learner draft basket with content deduplication and a durable receipt; replay does not add duplicates. Does not establish printing, returned work or mastery.

| Parameter | Location | Type / constraints | Required | Meaning |
| --- | --- | --- | --- | --- |
| `id` | path | string；minLength=1 | Yes | Resource ID from a preceding response |

JSON body: [HTTPWeeklyExpectedRevisionRequest](#schema-k12practicehttpweeklyexpectedrevisionrequest)；unknown fields are not used by this operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `expected_revision` | integer；minimum=1 | Yes | JSON projection field; omitted when absent if optional. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

| HTTP | Body | Meaning |
| --- | --- | --- |
| 401 | `MessageError` | Missing or invalid Bearer token. |
| 201 | [WeeklySaveResponse](#schema-k12practiceweeklysaveresponse) | Created |
| 200 | [WeeklySaveResponse](#schema-k12practiceweeklysaveresponse) | Success or matching command replay |
| 400 | `MessageError` | Invalid JSON, parameters or typed domain input |
| 404 | `MessageError` | Resource absent or outside the requested agent scope |
| 409 | `MessageError` | Typed revision, command digest or state conflict; reconcile unknown outcomes |
| 502 | `MessageError` | Model, render or curriculum dependency failure |
| 413 | `MessageError` | JSON request body exceeds 1 MiB |

```bash
curl -fS -X POST "$HEXCLAW_BASE_URL/api/k12/weekly-practice/plans/resource-demo-1/save-to-practice-set" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"agent":"tutor-demo","expected_revision":1,"idempotency_key":"weekly-save-demo-1"}'
```

Implementation: [saveWeeklyPracticeToPracticeSet](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

## Response and nested JSON schemas

Required in this section means serialized by the Go DTO without `omitempty`; a nullable field may still be present as null. It is not an additional input requirement. Integers ending in `_at` are Unix seconds unless an explicit format says otherwise.

<a id="schema-k12practiceok"></a>

### OK

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `ok` | boolean (`true`) | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

<a id="schema-k12practicehttpmistakedto"></a>

### HTTPMistakeDTO

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `record_id` | string | Yes | Record ID used by the corresponding resource routes |
| `question` | string | Yes | JSON projection field; omitted when absent if optional. |
| `knowledge_point` | string | Yes | JSON projection field; omitted when absent if optional. |
| `error_cause` | string | Yes | JSON projection field; omitted when absent if optional. |
| `status` | string (`new`, `explained`, `retried`, `mastered`, `archived`) | Yes | JSON projection field; omitted when absent if optional. |
| `review_state` | string (`scheduled`, `deferred_this_week`, `suppressed`, `mastered`) | No | JSON projection field; omitted when absent if optional. |
| `version` | integer | Yes | Current record or job optimistic-lock version |
| `due_at` | integer / null | No | JSON projection field; omitted when absent if optional. |
| `subject` | string | No | JSON projection field; omitted when absent if optional. |
| `review_kind` | string (`verify`, `verbatim`) | No | JSON projection field; omitted when absent if optional. |
| `spot_check_state` | string (`none`, `scheduled`, `passed`, `failed`) | No | JSON projection field; omitted when absent if optional. |
| `parent_confirmed_at` | integer | No | JSON projection field; omitted when absent if optional. |
| `archived_reason` | string | No | JSON projection field; omitted when absent if optional. |
| `archived_at` | integer | No | JSON projection field; omitted when absent if optional. |
| `archive_restored_at` | integer | No | JSON projection field; omitted when absent if optional. |
| `restorable` | boolean | Yes | JSON projection field; omitted when absent if optional. |
| `created_at` | integer | No | JSON projection field; omitted when absent if optional. |
| `entry_source` | string | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/apihttp/handler.go](../../../../scenarios/k12/apihttp/handler.go).

<a id="schema-k12practicesinglepracticegenerationview"></a>

### SinglePracticeGenerationView

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `state` | string (`available`, `pending`, `joined`, `failed`, `re_add`, `hidden`) | Yes | JSON projection field; omitted when absent if optional. |
| `source_mistake_id` | string | Yes | Source mistake record ID |
| `generation_job_id` | string | No | Durable generation job ID; not evidence that output is complete |
| `practice_set_id` | string | No | Practice-set ID containing the joined item |
| `practice_item_id` | string | No | ID of the item joined to the practice set |
| `failure_reason` | string | No | JSON projection field; omitted when absent if optional. |
| `source_mistake_summary` | string | No | JSON projection field; omitted when absent if optional. |
| `item` | [PracticeItem](#schema-k12practicepracticeitem) / null | No | JSON projection field; omitted when absent if optional. |
| `parent_confirmed` | boolean | No | JSON projection field; omitted when absent if optional. |
| `evidence_mastered` | boolean | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/usecase/single_practice_generation.go](../../../../scenarios/k12/usecase/single_practice_generation.go).

<a id="schema-k12practicesinglepracticegenerationstate"></a>

### SinglePracticeGenerationState

`{"type": "string", "enum": ["available", "pending", "joined", "failed", "re_add", "hidden"]}`

Source: [scenarios/k12/usecase/single_practice_generation.go](../../../../scenarios/k12/usecase/single_practice_generation.go).

<a id="schema-k12practicepracticeitem"></a>

### PracticeItem

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `asset_source` | [PracticeAssetSource](#schema-k12practicepracticeassetsource) / null | No | JSON projection field; omitted when absent if optional. |
| `item_id` | string | Yes | Current practice-item ID, distinct from its source mistake ID |
| `source_problem_id` | string | No | Source problem reference used for mistake linkage; manual items may have none |
| `source_mistake_summary` | string | No | JSON projection field; omitted when absent if optional. |
| `subject` | string | Yes | JSON projection field; omitted when absent if optional. |
| `added_via` | string | No | JSON projection field; omitted when absent if optional. |
| `generation_status` | string (`queued`, `generating`, `validating`, `ready`, `failed`) | No | JSON projection field; omitted when absent if optional. |
| `question_markdown` | string | Yes | Canonical question Markdown |
| `expected_answer_markdown` | string | Yes | Adopted answer Markdown for answer sheets or verification |
| `verification_status` | string | Yes | JSON projection field; omitted when absent if optional. |
| `verification_evidence` | string | No | Verification method or evidence reference |
| `blocked_reason` | string | No | JSON projection field; omitted when absent if optional. |
| `paper_seq` | integer | No | JSON projection field; omitted when absent if optional. |
| `returned` | boolean | No | JSON projection field; omitted when absent if optional. |
| `practice_problem_id` | string | No | JSON projection field; omitted when absent if optional. |
| `generation_job_id` | string | No | Durable generation job ID; not evidence that output is complete |
| `variant_index` | integer | No | JSON projection field; omitted when absent if optional. |
| `requested_difficulty` | string (`same`, `easier`, `harder`) | No | JSON projection field; omitted when absent if optional. |
| `actual_difficulty` | string (`same`, `easier`, `harder`) | No | JSON projection field; omitted when absent if optional. |
| `normalized_content_hash` | string | No | Server-normalized question digest used for content deduplication |
| `result_correct` | boolean / null | No | Absent/null means no conclusion; false is an assessed incorrect result |
| `result_evidence` | string | No | system_verified or human_confirmed; manual confirmation does not establish system mastery evidence |

Unknown fields are not part of this schema.

Source: [scenarios/k12/practiceset.go](../../../../scenarios/k12/practiceset.go).

<a id="schema-k12practicepracticeassetsource"></a>

### PracticeAssetSource

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `owner_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `asset_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `asset_version` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `asset_revision` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `facts_digest` | string | Yes | JSON projection field; omitted when absent if optional. |
| `grade_term` | string | Yes | JSON projection field; omitted when absent if optional. |
| `knowledge_point` | string | Yes | JSON projection field; omitted when absent if optional. |
| `original_review` | boolean | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/practice_asset.go](../../../../scenarios/k12/practice_asset.go).

<a id="schema-k12practicepracticecandidateselection"></a>

### PracticeCandidateSelection

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `selection_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `source_mistake_id` | string | Yes | Source mistake record ID |
| `target_set_record_id` | string | Yes | Target draft basket ID for this selection |
| `state` | string (`open`, `committed`) | Yes | JSON projection field; omitted when absent if optional. |
| `next_batch_ordinal` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `revision` | integer | Yes | Current aggregate revision used by the matching revision/expected_revision CAS |
| `candidates` | array / null [PracticeCandidate](#schema-k12practicepracticecandidate) | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/practice_candidate_review.go](../../../../scenarios/k12/practice_candidate_review.go).

<a id="schema-k12practicepracticecandidate"></a>

### PracticeCandidate

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `candidate_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `candidate_kind` | string (`original`, `variant`) | Yes | JSON projection field; omitted when absent if optional. |
| `batch_ordinal` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `candidate_ordinal` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `normalized_content_hash` | string | Yes | Server-normalized question digest used for content deduplication |
| `state` | string (`generating`, `ready`, `failed`, `already_in_set`) | Yes | JSON projection field; omitted when absent if optional. |
| `question_markdown` | string | Yes | Canonical question Markdown |
| `expected_answer_markdown` | string | No | Adopted answer Markdown for answer sheets or verification |
| `failure_message` | string | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/practice_candidate_review.go](../../../../scenarios/k12/practice_candidate_review.go).

<a id="schema-k12practicehttppracticesetdto"></a>

### HTTPPracticeSetDTO

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `record_id` | string | Yes | Record ID used by the corresponding resource routes |
| `title` | string | Yes | JSON projection field; omitted when absent if optional. |
| `source_kind` | string (`weekly`, `custom`, `single_variant`, `manual`, `mixed`) | Yes | JSON projection field; omitted when absent if optional. |
| `status` | string (`draft`, `confirmed`, `assigned`, `submitted`, `graded`, `closed`, `cancelled`) | Yes | JSON projection field; omitted when absent if optional. |
| `status_label` | string | Yes | JSON projection field; omitted when absent if optional. |
| `publishable` | boolean | Yes | Whether verified publishable items exist; does not establish printing or delivery |
| `question_artifact_id` | string | No | JSON projection field; omitted when absent if optional. |
| `answer_artifact_id` | string | No | JSON projection field; omitted when absent if optional. |
| `delivery_status` | string (`not_sent`, `pending`, `sending`, `delivered`, `failed`, `partial_failed`, `outcome_unknown`) | Yes | JSON projection field; omitted when absent if optional. |
| `delivery_batch_id` | string | No | JSON projection field; omitted when absent if optional. |
| `skipped_blocked_count` | integer | No | Count of non-publishable items skipped during finalization |
| `paper_no` | string | No | Reserved/formal paper number; native receipts determine whether printing completed |
| `finalized_at` | integer | No | JSON projection field; omitted when absent if optional. |
| `finalized_via` | string (`print`, `send`) | No | JSON projection field; omitted when absent if optional. |
| `items` | array / null [HTTPPracticeItemDTO](#schema-k12practicehttppracticeitemdto) | Yes | JSON projection field; omitted when absent if optional. |
| `return_assets` | array / null [HTTPPracticeReturnAssetDTO](#schema-k12practicehttppracticereturnassetdto) | Yes | Returned photos with their asynchronous regrade projections |

Unknown fields are not part of this schema.

Source: [scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="schema-k12practicehttppracticeitemdto"></a>

### HTTPPracticeItemDTO

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `item_id` | string | Yes | Current practice-item ID, distinct from its source mistake ID |
| `source_problem_id` | string | No | Source problem reference used for mistake linkage; manual items may have none |
| `subject` | string | No | JSON projection field; omitted when absent if optional. |
| `added_via` | string | No | JSON projection field; omitted when absent if optional. |
| `question_markdown` | string | Yes | Canonical question Markdown |
| `expected_answer_markdown` | string | No | Adopted answer Markdown for answer sheets or verification |
| `verification_status` | string (`pending`, `verified`, `needs_review`, `rejected`, `stale`) | Yes | JSON projection field; omitted when absent if optional. |
| `verification_evidence` | string | No | Verification method or evidence reference |
| `blocked_reason` | string | No | JSON projection field; omitted when absent if optional. |
| `paper_seq` | integer | No | JSON projection field; omitted when absent if optional. |
| `returned` | boolean | No | JSON projection field; omitted when absent if optional. |
| `return_ids` | array / null string | No | JSON projection field; omitted when absent if optional. |
| `generation_job_id` | string | No | Durable generation job ID; not evidence that output is complete |
| `variant_index` | integer | No | JSON projection field; omitted when absent if optional. |
| `requested_difficulty` | string (`same`, `easier`, `harder`) | No | JSON projection field; omitted when absent if optional. |
| `actual_difficulty` | string (`same`, `easier`, `harder`) | No | JSON projection field; omitted when absent if optional. |
| `result_correct` | boolean / null | No | Absent/null means no conclusion; false is an assessed incorrect result |
| `result_evidence` | string | No | system_verified or human_confirmed; manual confirmation does not establish system mastery evidence |

Unknown fields are not part of this schema.

Source: [scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="schema-k12practicehttppracticereturnassetdto"></a>

### HTTPPracticeReturnAssetDTO

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `return_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `asset_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `item_ids` | array / null string | Yes | Practice-item IDs covered by this operation; empty does not mean the entire paper |
| `auto_match` | boolean | No | JSON projection field; omitted when absent if optional. |
| `candidate_item_ids` | array / null string | No | JSON projection field; omitted when absent if optional. |
| `returned_at` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `regrade_job_id` | string | No | JSON projection field; omitted when absent if optional. |
| `regrade_status` | string (`queued`, `running`, `needs_review`, `completed`, `failed_retryable`, `failed_terminal`, `outcome_unknown`) | No | Durable regrade state for the returned photo |
| `route_snapshot` | [GradingModelSnapshot](#schema-k12practicegradingmodelsnapshot) | No | JSON projection field; omitted when absent if optional. |
| `annotated_asset_id` | string | No | Annotated-original-image asset ID; completion also requires readable output |
| `result_markdown` | string | No | JSON projection field; omitted when absent if optional. |
| `unresolved_item_ids` | array / null string | No | Items not reliably assessed in this return; no mastery evidence is accumulated |
| `regrade_updated_at` | integer | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="schema-k12practicegradingmodelsnapshot"></a>

### GradingModelSnapshot

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `parent_instructions` | [ConfigAgentInstructionsSnapshot](#schema-k12practiceconfigagentinstructionssnapshot) | No | JSON projection field; omitted when absent if optional. |
| `provider` | string | Yes | JSON projection field; omitted when absent if optional. |
| `model` | string | Yes | JSON projection field; omitted when absent if optional. |
| `provider_instance_id` | string | No | JSON projection field; omitted when absent if optional. |
| `config_fingerprint` | string | No | JSON projection field; omitted when absent if optional. |
| `capability_receipt_digest` | string | No | JSON projection field; omitted when absent if optional. |
| `probe_policy_version` | string | No | JSON projection field; omitted when absent if optional. |
| `route` | string | Yes | JSON projection field; omitted when absent if optional. |
| `capability` | string | No | JSON projection field; omitted when absent if optional. |
| `timeout_ms` | integer | No | JSON projection field; omitted when absent if optional. |
| `fallback` | string | No | JSON projection field; omitted when absent if optional. |
| `recognizing_request_policy` | [ModelRequestPolicySnapshot](#schema-k12practicemodelrequestpolicysnapshot) | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/gradingjob.go](../../../../scenarios/k12/gradingjob.go).

<a id="schema-k12practiceconfigagentinstructionssnapshot"></a>

### ConfigAgentInstructionsSnapshot

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `content` | string | Yes | JSON projection field; omitted when absent if optional. |
| `digest` | string | Yes | JSON projection field; omitted when absent if optional. |
| `source` | string | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [config/agent_instructions.go](../../../../config/agent_instructions.go).

<a id="schema-k12practicemodelrequestpolicysnapshot"></a>

### ModelRequestPolicySnapshot

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `policy_version` | string | Yes | JSON projection field; omitted when absent if optional. |
| `stage` | string | Yes | JSON projection field; omitted when absent if optional. |
| `thinking` | string | Yes | JSON projection field; omitted when absent if optional. |
| `reasoning_effort` | string | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/model_request_policy.go](../../../../scenarios/k12/model_request_policy.go).

<a id="schema-k12practicehttppracticeprintjobdto"></a>

### HTTPPracticePrintJobDTO

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `print_job_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `practice_set_id` | string | No | Practice-set ID containing the joined item |
| `idempotency_key` | string | Yes | JSON projection field; omitted when absent if optional. |
| `status` | string (`preparing`, `dialog_open`, `submitted`, `printed`, `cancelled`, `failed`, `outcome_unknown`) | Yes | JSON projection field; omitted when absent if optional. |
| `paper_no` | string | No | Reserved/formal paper number; native receipts determine whether printing completed |
| `artifact_kind` | string | Yes | JSON projection field; omitted when absent if optional. |
| `artifact_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `question_artifact_id` | string | No | JSON projection field; omitted when absent if optional. |
| `answer_artifact_id` | string | No | JSON projection field; omitted when absent if optional. |
| `source_kind` | string | No | JSON projection field; omitted when absent if optional. |
| `source_ref` | string | No | JSON projection field; omitted when absent if optional. |
| `title` | string | No | JSON projection field; omitted when absent if optional. |
| `source_digest` | string | Yes | Digest of the frozen source used to verify the same content revision |
| `attempt_count` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `native_job_id` | string | No | Native print job identity used to reconcile unknown outcomes |
| `native_receipt_id` | string | No | Native success receipt identity, distinct from acceptance or a dialog state |
| `printer_snapshot` | object / null | No | Native printer fact snapshot |
| `failure_kind` | string | No | Failure classification, required for failed or outcome_unknown events |
| `failure_detail` | string | No | JSON projection field; omitted when absent if optional. |
| `prepared_at` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `printed_at` | integer | No | JSON projection field; omitted when absent if optional. |
| `updated_at` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `version` | integer | Yes | Current record or job optimistic-lock version |

Unknown fields are not part of this schema.

Source: [scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="schema-k12practiceweeklypracticeplan"></a>

### WeeklyPracticePlan

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `plan_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `agent` | string | Yes | JSON projection field; omitted when absent if optional. |
| `revision` | integer | Yes | Current aggregate revision used by the matching revision/expected_revision CAS |
| `iso_week_year` | integer | Yes | Timezone-specific ISO week year, which may differ from the calendar year |
| `iso_week_number` | integer | Yes | ISO week number 1–53 |
| `timezone` | string | Yes | JSON projection field; omitted when absent if optional. |
| `week_start` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `week_end` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `local_start_date` | string | Yes | JSON projection field; omitted when absent if optional. |
| `local_end_date` | string | Yes | JSON projection field; omitted when absent if optional. |
| `status` | string (`draft`, `frozen`, `archived`, `expired_unused`) | Yes | JSON projection field; omitted when absent if optional. |
| `settings_revision` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `curriculum_progress_revision` | integer / null | Yes | JSON projection field; omitted when absent if optional. |
| `tracks` | array / null [WeeklyPracticeTrack](#schema-k12practiceweeklypracticetrack) | Yes | Due-review, textbook and arithmetic sections with per-item evidence |
| `manual_track_recommendations` | [WeeklyManualTrackRecommendations](#schema-k12practiceweeklymanualtrackrecommendations) | Yes | Current availability and recommended counts; never substitutes an explicit item_count |
| `created_at` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `updated_at` | integer | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go).

<a id="schema-k12practiceweeklypracticetrack"></a>

### WeeklyPracticeTrack

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `plan_section` | string (`due_review`, `textbook_consolidation`, `arithmetic_warmup`) | Yes | JSON projection field; omitted when absent if optional. |
| `status` | string (`ready`, `disabled`, `stale`, `failed`) | Yes | JSON projection field; omitted when absent if optional. |
| `failure_message` | string | No | JSON projection field; omitted when absent if optional. |
| `items` | array / null [WeeklyPracticeItem](#schema-k12practiceweeklypracticeitem) | Yes | JSON projection field; omitted when absent if optional. |
| `arithmetic_batch` | [WeeklyArithmeticBatch](#schema-k12practiceweeklyarithmeticbatch) / null | Yes | Current arithmetic batch projection for the track; may be null |

Unknown fields are not part of this schema.

Source: [scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go).

<a id="schema-k12practiceweeklypracticeitem"></a>

### WeeklyPracticeItem

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `item_id` | string | Yes | Current practice-item ID, distinct from its source mistake ID |
| `position` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `plan_section` | string | Yes | JSON projection field; omitted when absent if optional. |
| `source_kind` | string | Yes | JSON projection field; omitted when absent if optional. |
| `generation_method` | string (`original`, `ai_variant`, `ai_generated`, `rule_generated`, `asset_reuse`) | Yes | JSON projection field; omitted when absent if optional. |
| `source_ref` | string | Yes | JSON projection field; omitted when absent if optional. |
| `subject` | string | No | JSON projection field; omitted when absent if optional. |
| `knowledge_point` | string | No | JSON projection field; omitted when absent if optional. |
| `mastery_status` | string | No | JSON projection field; omitted when absent if optional. |
| `verification` | [WeeklyPracticeVerification](#schema-k12practiceweeklypracticeverification) | Yes | JSON projection field; omitted when absent if optional. |
| `prompt_markdown` | string | Yes | JSON projection field; omitted when absent if optional. |
| `asset_source` | [PracticeAssetSource](#schema-k12practicepracticeassetsource) / null | No | JSON projection field; omitted when absent if optional. |
| `source_question` | string | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go).

<a id="schema-k12practiceweeklypracticeverification"></a>

### WeeklyPracticeVerification

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `status` | string (`verified`, `failed`) | Yes | JSON projection field; omitted when absent if optional. |
| `evidence_refs` | array / null string | Yes | JSON projection field; omitted when absent if optional. |
| `textbook_binding_id` | string | No | JSON projection field; omitted when absent if optional. |
| `unit_id` | string | No | JSON projection field; omitted when absent if optional. |
| `lesson_id` | string | No | JSON projection field; omitted when absent if optional. |
| `verified_page_from` | integer / null | No | JSON projection field; omitted when absent if optional. |
| `verified_page_to` | integer / null | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go).

<a id="schema-k12practiceweeklyarithmeticbatch"></a>

### WeeklyArithmeticBatch

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `batch_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `state` | string (`preparing`, `ready`, `in_progress`, `completed`, `failed_retryable`, `failed_terminal`) | Yes | JSON projection field; omitted when absent if optional. |
| `item_count` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `content_digest` | string | Yes | JSON projection field; omitted when absent if optional. |
| `retryable` | boolean | Yes | JSON projection field; omitted when absent if optional. |
| `failure_message` | string | Yes | JSON projection field; omitted when absent if optional. |
| `created_at` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `updated_at` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `completed_at` | integer / null | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/weekly_arithmetic.go](../../../../scenarios/k12/weekly_arithmetic.go).

<a id="schema-k12practiceweeklymanualtrackrecommendations"></a>

### WeeklyManualTrackRecommendations

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `textbook_consolidation` | [WeeklyManualTrackRecommendation](#schema-k12practiceweeklymanualtrackrecommendation) | Yes | JSON projection field; omitted when absent if optional. |
| `arithmetic_warmup` | [WeeklyManualTrackRecommendation](#schema-k12practiceweeklymanualtrackrecommendation) | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go).

<a id="schema-k12practiceweeklymanualtrackrecommendation"></a>

### WeeklyManualTrackRecommendation

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `availability` | string (`available`, `setup_required`, `processing`, `failed_retryable`, `failed_terminal`) | Yes | JSON projection field; omitted when absent if optional. |
| `selected_item_count` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `recommended_item_count` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `min_item_count` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `max_item_count` | integer | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go).

<a id="schema-k12practiceweeklypracticesnapshot"></a>

### WeeklyPracticeSnapshot

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `snapshot_id` | string | Yes | Immutable weekly snapshot ID |
| `artifact_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `plan_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `plan_revision` | integer | Yes | Plan revision bound to the snapshot or used by request CAS |
| `agent` | string | Yes | JSON projection field; omitted when absent if optional. |
| `iso_week_year` | integer | Yes | Timezone-specific ISO week year, which may differ from the calendar year |
| `iso_week_number` | integer | Yes | ISO week number 1–53 |
| `timezone` | string | Yes | JSON projection field; omitted when absent if optional. |
| `week_start` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `week_end` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `local_start_date` | string | Yes | JSON projection field; omitted when absent if optional. |
| `local_end_date` | string | Yes | JSON projection field; omitted when absent if optional. |
| `settings_revision` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `curriculum_progress_revision` | integer / null | No | JSON projection field; omitted when absent if optional. |
| `tracks` | array / null [WeeklyPracticeTrack](#schema-k12practiceweeklypracticetrack) | Yes | Due-review, textbook and arithmetic sections with per-item evidence |
| `render_version` | string | Yes | JSON projection field; omitted when absent if optional. |
| `snapshot_digest` | string | Yes | JSON projection field; omitted when absent if optional. |
| `created_at` | integer | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go).

<a id="schema-k12practicedeliverybatch"></a>

### DeliveryBatch

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `batch_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `agent_name` | string | Yes | JSON projection field; omitted when absent if optional. |
| `object_kind` | string | Yes | JSON projection field; omitted when absent if optional. |
| `object_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `dedupe_key` | string | Yes | JSON projection field; omitted when absent if optional. |
| `content_digest` | string | Yes | JSON projection field; omitted when absent if optional. |
| `status` | [DeliveryBatchStatus](#schema-k12practicedeliverybatchstatus) | Yes | JSON projection field; omitted when absent if optional. |
| `receipts` | array / null [DeliveryReceipt](#schema-k12practicedeliveryreceipt) | Yes | Per-target/message-part receipts; all required parts must be delivered for completion |
| `created_at` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `updated_at` | integer | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/delivery_receipt.go](../../../../scenarios/k12/delivery_receipt.go).

<a id="schema-k12practicedeliverybatchstatus"></a>

### DeliveryBatchStatus

`{"type": "string", "enum": ["pending", "sending", "delivered", "failed", "partial_failed", "outcome_unknown"]}`

Source: [scenarios/k12/delivery_receipt.go](../../../../scenarios/k12/delivery_receipt.go).

<a id="schema-k12practicedeliveryreceipt"></a>

### DeliveryReceipt

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `delivery_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `batch_id` | string | No | JSON projection field; omitted when absent if optional. |
| `batch_ordinal` | integer | No | JSON projection field; omitted when absent if optional. |
| `part_kind` | [MessagePartKind](#schema-k12practicemessagepartkind) | Yes | JSON projection field; omitted when absent if optional. |
| `part_mime` | string | No | JSON projection field; omitted when absent if optional. |
| `part_ordinal` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `part_digest` | string | Yes | JSON projection field; omitted when absent if optional. |
| `agent_name` | string | Yes | JSON projection field; omitted when absent if optional. |
| `object_kind` | string | Yes | JSON projection field; omitted when absent if optional. |
| `object_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `binding_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `target` | [DeliveryTarget](#schema-k12practicedeliverytarget) | Yes | JSON projection field; omitted when absent if optional. |
| `status` | [DeliveryReceiptStatus](#schema-k12practicedeliveryreceiptstatus) | Yes | JSON projection field; omitted when absent if optional. |
| `dedupe_key` | string | Yes | JSON projection field; omitted when absent if optional. |
| `payload_digest` | string | Yes | JSON projection field; omitted when absent if optional. |
| `payload_json` | string | Yes | JSON string of the frozen delivery payload |
| `render_manifest_json` | string | Yes | JSON string of the frozen delivery render manifest |
| `external_message_id` | string | No | Provider acceptance correlation ID; not proof of delivered state |
| `attempt` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `last_error` | string | No | JSON projection field; omitted when absent if optional. |
| `created_at` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `updated_at` | integer | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/delivery_receipt.go](../../../../scenarios/k12/delivery_receipt.go).

<a id="schema-k12practicemessagepartkind"></a>

### MessagePartKind

`{"type": "string", "enum": ["markdown", "text", "artifact"]}`

Source: [messagecontent/messagecontent.go](../../../../messagecontent/messagecontent.go).

<a id="schema-k12practicedeliverytarget"></a>

### DeliveryTarget

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `platform` | string | Yes | JSON projection field; omitted when absent if optional. |
| `instance_id` | string | No | JSON projection field; omitted when absent if optional. |
| `chat_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `label` | string | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/delivery_receipt.go](../../../../scenarios/k12/delivery_receipt.go).

<a id="schema-k12practicedeliveryreceiptstatus"></a>

### DeliveryReceiptStatus

`{"type": "string", "enum": ["pending", "sending", "delivered", "failed", "outcome_unknown"]}`

Source: [scenarios/k12/delivery_receipt.go](../../../../scenarios/k12/delivery_receipt.go).

<a id="schema-k12practiceprintjobresponse"></a>

### PrintJobResponse

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `print_job` | [HTTPPracticePrintJobDTO](#schema-k12practicehttppracticeprintjobdto) | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

<a id="schema-k12practiceprintjobprepareresponse"></a>

### PrintJobPrepareResponse

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `print_job` | [HTTPPracticePrintJobDTO](#schema-k12practicehttppracticeprintjobdto) | Yes | JSON projection field; omitted when absent if optional. |
| `replayed` | boolean | Yes | Replay of a durable result for the same frozen command without repeating new domain effects |

Unknown fields are not part of this schema.

<a id="schema-k12practiceplanresponse"></a>

### PlanResponse

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `plan` | [WeeklyPracticePlan](#schema-k12practiceweeklypracticeplan) | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

<a id="schema-k12practiceplanreplayresponse"></a>

### PlanReplayResponse

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `plan` | [WeeklyPracticePlan](#schema-k12practiceweeklypracticeplan) | Yes | JSON projection field; omitted when absent if optional. |
| `replayed` | boolean | Yes | Replay of a durable result for the same frozen command without repeating new domain effects |

Unknown fields are not part of this schema.

<a id="schema-k12practicearithmeticbatchresponse"></a>

### ArithmeticBatchResponse

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `batch` | [WeeklyArithmeticBatch](#schema-k12practiceweeklyarithmeticbatch) | Yes | JSON projection field; omitted when absent if optional. |
| `replayed` | boolean | Yes | Replay of a durable result for the same frozen command without repeating new domain effects |

Unknown fields are not part of this schema.

<a id="schema-k12practiceprintableartifact"></a>

### PrintableArtifact

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `artifact_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `source_kind` | string | Yes | JSON projection field; omitted when absent if optional. |
| `source_ref` | string | Yes | JSON projection field; omitted when absent if optional. |
| `title` | string | Yes | JSON projection field; omitted when absent if optional. |
| `source_digest` | string | Yes | Digest of the frozen source used to verify the same content revision |
| `format` | string | Yes | JSON projection field; omitted when absent if optional. |
| `render_contract_version` | string | Yes | JSON projection field; omitted when absent if optional. |
| `content_type` | string | Yes | JSON projection field; omitted when absent if optional. |
| `byte_digest` | string | Yes | Frozen PDF byte digest matching downloaded X-Content-SHA256 |
| `byte_size` | integer | Yes | Actual PDF byte count |

Unknown fields are not part of this schema.

<a id="schema-k12practiceartifactprepareresponse"></a>

### ArtifactPrepareResponse

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `artifact` | [PrintableArtifact](#schema-k12practiceprintableartifact) | Yes | JSON projection field; omitted when absent if optional. |
| `replayed` | boolean | Yes | Replay of a durable result for the same frozen command without repeating new domain effects |

Unknown fields are not part of this schema.

<a id="schema-k12practicehttpagentonlyreq"></a>

### HTTPAgentOnlyReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |

Source: [scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="schema-k12practicemistakelist"></a>

### MistakeList

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `items` | array [HTTPMistakeDTO](#schema-k12practicehttpmistakedto) | Yes | JSON projection field; omitted when absent if optional. |
| `total` | integer | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

<a id="schema-k12practicehttpmistakearchivecommandreq"></a>

### HTTPMistakeArchiveCommandReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `version` | integer；minimum=0 | Yes | Current record version, required and non-negative; restore using the returned newer version |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

Unknown fields are not part of this schema.

Source: [scenarios/k12/apihttp/handler.go](../../../../scenarios/k12/apihttp/handler.go).

<a id="schema-k12practicehttpsinglepracticegenerationreq"></a>

### HTTPSinglePracticeGenerationReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `grade` | string | No | JSON projection field; omitted when absent if optional. |
| `textbook` | string | No | JSON projection field; omitted when absent if optional. |
| `difficulty` | string (`same`, `easier`, `harder`) | No | Defaults to same when omitted |
| `provider` | string | No | JSON projection field; omitted when absent if optional. |
| `model` | string | No | JSON projection field; omitted when absent if optional. |
| `source_session` | string | No | JSON projection field; omitted when absent if optional. |

Source: [scenarios/k12/apihttp/handler.go](../../../../scenarios/k12/apihttp/handler.go).

<a id="schema-k12practicepracticegenerationreceiptview"></a>

### PracticeGenerationReceiptView

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `source_kind` | string | Yes | JSON projection field; omitted when absent if optional. |
| `generation_job_id_digest` | string | Yes | JSON projection field; omitted when absent if optional. |
| `generation_status` | string (`queued`, `generating`, `validating`, `committed`, `failed`, `cancelled`) | Yes | JSON projection field; omitted when absent if optional. |
| `receipt_exact_set_digest` | string | Yes | JSON projection field; omitted when absent if optional. |
| `receipts` | array / null [PracticeGenerationInvocationReceipt](#schema-k12practicepracticegenerationinvocationreceipt) | Yes | Per-target/message-part receipts; all required parts must be delivered for completion |

Unknown fields are not part of this schema.

Source: [scenarios/k12/usecase/single_practice_generation.go](../../../../scenarios/k12/usecase/single_practice_generation.go).

<a id="schema-k12practicepracticegenerationinvocationreceipt"></a>

### PracticeGenerationInvocationReceipt

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `stage` | string | Yes | JSON projection field; omitted when absent if optional. |
| `attempt` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `status` | [ModelInvocationStatus](#schema-k12practicemodelinvocationstatus) | Yes | JSON projection field; omitted when absent if optional. |
| `provider` | string | Yes | JSON projection field; omitted when absent if optional. |
| `model` | string | Yes | JSON projection field; omitted when absent if optional. |
| `route` | string | Yes | JSON projection field; omitted when absent if optional. |
| `provider_instance_id_digest` | string | Yes | JSON projection field; omitted when absent if optional. |
| `config_fingerprint` | string | Yes | JSON projection field; omitted when absent if optional. |
| `capability_receipt_digest` | string | Yes | JSON projection field; omitted when absent if optional. |
| `probe_policy_version` | string | Yes | JSON projection field; omitted when absent if optional. |
| `request_digest` | string | Yes | JSON projection field; omitted when absent if optional. |
| `result_digest` | string | Yes | JSON projection field; omitted when absent if optional. |
| `external_request_id_digest` | string | No | JSON projection field; omitted when absent if optional. |
| `failure_kind` | string | No | Failure classification, required for failed or outcome_unknown events |
| `created_at` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `updated_at` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `receipt_digest` | string | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/usecase/single_practice_generation.go](../../../../scenarios/k12/usecase/single_practice_generation.go).

<a id="schema-k12practicemodelinvocationstatus"></a>

### ModelInvocationStatus

`{"type": "string", "enum": ["prepared", "sent", "succeeded", "failed", "outcome_unknown", "reconciled"]}`

Source: [scenarios/k12/model_invocation.go](../../../../scenarios/k12/model_invocation.go).

<a id="schema-k12practicehttppracticecandidateopenreq"></a>

### HTTPPracticeCandidateOpenReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `grade` | string | No | JSON projection field; omitted when absent if optional. |
| `textbook` | string | No | JSON projection field; omitted when absent if optional. |
| `provider` | string | No | JSON projection field; omitted when absent if optional. |
| `model` | string | No | JSON projection field; omitted when absent if optional. |
| `source_session` | string | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/apihttp/practice_candidate_review_handler.go](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go).

<a id="schema-k12practicehttppracticecandidatebatchreq"></a>

### HTTPPracticeCandidateBatchReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `revision` | integer；minimum=1 | Yes | Current aggregate revision used by the matching revision/expected_revision CAS |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `provider` | string | No | JSON projection field; omitted when absent if optional. |
| `model` | string | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/apihttp/practice_candidate_review_handler.go](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go).

<a id="schema-k12practicehttppracticecandidatecommitreq"></a>

### HTTPPracticeCandidateCommitReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `revision` | integer；minimum=1 | Yes | Current aggregate revision used by the matching revision/expected_revision CAS |
| `candidate_ids` | array string；minLength=1；minItems=1; uniqueItems=True | Yes | JSON projection field; omitted when absent if optional. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

Unknown fields are not part of this schema.

Source: [scenarios/k12/apihttp/practice_candidate_review_handler.go](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go).

<a id="schema-k12practicestoragepracticecandidatecommitreceipt"></a>

### StoragePracticeCandidateCommitReceipt

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `selection` | [PracticeCandidateSelection](#schema-k12practicepracticecandidateselection) | Yes | JSON projection field; omitted when absent if optional. |
| `added_count` | integer | Yes | Number of items actually inserted by this transaction |
| `already_present` | array / null string | Yes | Candidate IDs already present in the target basket at commit time |
| `replayed` | boolean | Yes | Replay of a durable result for the same frozen command without repeating new domain effects |

Unknown fields are not part of this schema.


Source: [scenarios/k12/storage/practice_candidate_review.go](../../../../scenarios/k12/storage/practice_candidate_review.go).

<a id="schema-k12practicehttpmistakereviewcommandreq"></a>

### HTTPMistakeReviewCommandReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `version` | integer；minimum=0 | Yes | Current record or job optimistic-lock version |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `plan_id` | string | No | JSON projection field; omitted when absent if optional. |
| `plan_revision` | integer | No | Plan revision bound to the snapshot or used by request CAS |
| `weekly_item_id` | string | No | JSON projection field; omitted when absent if optional. |
| `iso_year` | integer | No | JSON projection field; omitted when absent if optional. |
| `iso_week` | integer | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/apihttp/practice_candidate_review_handler.go](../../../../scenarios/k12/apihttp/practice_candidate_review_handler.go).

<a id="schema-k12practicestoragemistakereviewcommandresult"></a>

### StorageMistakeReviewCommandResult

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `state` | string | Yes | JSON projection field; omitted when absent if optional. |
| `mistake_version` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `replayed` | boolean | Yes | Replay of a durable result for the same frozen command without repeating new domain effects |
| `review` | [MistakeReviewState](#schema-k12practicemistakereviewstate) | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/storage/practice_candidate_review.go](../../../../scenarios/k12/storage/practice_candidate_review.go).

<a id="schema-k12practicemistakereviewstate"></a>

### MistakeReviewState

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string | Yes | JSON projection field; omitted when absent if optional. |
| `mistake_record_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `state` | string (`scheduled`, `deferred_this_week`, `suppressed`, `mastered`) | Yes | JSON projection field; omitted when absent if optional. |
| `deferred_iso_year` | integer | No | JSON projection field; omitted when absent if optional. |
| `deferred_iso_week` | integer | No | JSON projection field; omitted when absent if optional. |
| `revision` | integer | Yes | Current aggregate revision used by the matching revision/expected_revision CAS |
| `updated_at` | integer | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/practice_candidate_review.go](../../../../scenarios/k12/practice_candidate_review.go).

<a id="schema-k12practicedefermistakerequest"></a>

### DeferMistakeRequest

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `version` | integer；minimum=0 | Yes | Current record or job optimistic-lock version |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `plan_id` | string | No | JSON projection field; omitted when absent if optional. |
| `plan_revision` | integer | No | Plan revision bound to the snapshot or used by request CAS |
| `weekly_item_id` | string | No | JSON projection field; omitted when absent if optional. |
| `iso_year` | integer；minimum=1 | Yes | JSON projection field; omitted when absent if optional. |
| `iso_week` | integer；minimum=1; maximum=53 | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

<a id="schema-k12practicesetlist"></a>

### SetList

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `items` | array [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

<a id="schema-k12practicepaper"></a>

### Paper

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `kind` | string (`question`, `answer`) | Yes | JSON projection field; omitted when absent if optional. |
| `title` | string | Yes | JSON projection field; omitted when absent if optional. |
| `paper_no` | string | Yes | Reserved/formal paper number; native receipts determine whether printing completed |
| `markdown` | string | Yes | JSON projection field; omitted when absent if optional. |
| `preview` | boolean | Yes | true identifies a draft preview without a formal paper number |

Unknown fields are not part of this schema.

<a id="schema-k12practicehttpverifyitemreq"></a>

### HTTPVerifyItemReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `item_id` | string | Yes | Current practice-item ID, distinct from its source mistake ID |
| `status` | string (`pending`, `verified`, `needs_review`, `rejected`, `stale`) | Yes | JSON projection field; omitted when absent if optional. |
| `evidence` | string | No | Required and non-empty for verified; may be empty for other states |

Source: [scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="schema-k12practicehttpcustompaperreq"></a>

### HTTPCustomPaperReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `scope` | string (`week`, `unmastered`) | Yes | JSON projection field; omitted when absent if optional. |
| `total` | string (`all`, `5`, `10`) / integer (`5`, `10`) | Yes | JSON projection field; omitted when absent if optional. |
| `per_source` | integer；minimum=1; maximum=3 | Yes | JSON projection field; omitted when absent if optional. |
| `difficulty` | string (`same`, `easier`, `harder`) | Yes | JSON projection field; omitted when absent if optional. |
| `textbook` | string | No | Explicit value or profile default; must resolve to non-empty |
| `grade` | string | No | JSON projection field; omitted when absent if optional. |
| `provider` | string | No | JSON projection field; omitted when absent if optional. |
| `model` | string | No | JSON projection field; omitted when absent if optional. |
| `source_session` | string | No | JSON projection field; omitted when absent if optional. |

Source: [scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="schema-k12practicecustompaperitemresult"></a>

### CustomPaperItemResult

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `item_id` | string | Yes | Current practice-item ID, distinct from its source mistake ID |
| `source_problem_id` | string | Yes | Source problem reference used for mistake linkage; manual items may have none |
| `variant_index` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `actual_difficulty` | string | Yes | JSON projection field; omitted when absent if optional. |
| `verification_status` | string | Yes | JSON projection field; omitted when absent if optional. |
| `verification_evidence` | string | No | Verification method or evidence reference |
| `blocked_reason` | string | No | JSON projection field; omitted when absent if optional. |
| `question_markdown` | string | Yes | Canonical question Markdown |
| `expected_answer_markdown` | string | No | Adopted answer Markdown for answer sheets or verification |

Unknown fields are not part of this schema.

Source: [scenarios/k12/usecase/custom_paper.go](../../../../scenarios/k12/usecase/custom_paper.go).

<a id="schema-k12practicecustompaperresponse"></a>

### CustomPaperResponse

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `generation_job_id` | string | Yes | Durable generation job ID; not evidence that output is complete |
| `status` | string (`committed`) | Yes | JSON projection field; omitted when absent if optional. |
| `set` | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | Yes | JSON projection field; omitted when absent if optional. |
| `items` | array / null [CustomPaperItemResult](#schema-k12practicecustompaperitemresult) | Yes | JSON projection field; omitted when absent if optional. |
| `added` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `deduplicated` | integer | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

<a id="schema-k12practicebasketitemrequest"></a>

### BasketItemRequest

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `item_id` | string | No | Current practice-item ID, distinct from its source mistake ID |
| `source_problem_id` | string | No | Source problem reference used for mistake linkage; manual items may have none |
| `subject` | string (``, `数学`, `语文`, `英语`, `科学`, `信息科技`) | No | JSON projection field; omitted when absent if optional. |
| `added_via` | string (``, `weekly`, `custom`, `single_variant`, `manual`, `accumulation`, `spot_check`) | No | JSON projection field; omitted when absent if optional. |
| `question_markdown` | string；minLength=1 | Yes | Canonical question Markdown |
| `expected_answer_markdown` | string | No | Adopted answer Markdown for answer sheets or verification |
| `verification_status` | string (`pending`, `verified`, `needs_review`, `rejected`, `stale`) | No | JSON projection field; omitted when absent if optional. |
| `verification_evidence` | string | No | Verification method or evidence reference |

Unknown fields are not part of this schema.

<a id="schema-k12practicehttpaddtobasketreq"></a>

### HTTPAddToBasketReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `source_session` | string | No | JSON projection field; omitted when absent if optional. |
| `item` | [BasketItemRequest](#schema-k12practicebasketitemrequest) | Yes | JSON projection field; omitted when absent if optional. |

Source: [scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="schema-k12practicebasketaddresponse"></a>

### BasketAddResponse

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `record_id` | string | Yes | Record ID used by the corresponding resource routes |
| `added` | boolean | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

<a id="schema-k12practicehttpremovefrombasketreq"></a>

### HTTPRemoveFromBasketReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `item_id` | string | Yes | Current practice-item ID, distinct from its source mistake ID |

Source: [scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="schema-k12practicehttpfinalizereq"></a>

### HTTPFinalizeReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `via` | string (`print`, `send`) | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="schema-k12practicefinalizeresponse"></a>

### FinalizeResponse

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `set` | [HTTPPracticeSetDTO](#schema-k12practicehttppracticesetdto) | Yes | JSON projection field; omitted when absent if optional. |
| `skipped_blocked_count` | integer | Yes | Count of non-publishable items skipped during finalization |
| `delivery_batch` | [DeliveryBatch](#schema-k12practicedeliverybatch) | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

<a id="schema-k12practicehttppreparepracticeprintreq"></a>

### HTTPPreparePracticePrintReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `artifact_kind` | string (`question`, `answer`) | No | Defaults to question |

Source: [scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="schema-k12practicegenericprintrequest"></a>

### GenericPrintRequest

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | JSON projection field; omitted when absent if optional. |
| `idempotency_key` | string；minLength=1 | Yes | Command key, at most 512 UTF-8 bytes |
| `artifact_id` | string | No | JSON projection field; omitted when absent if optional. |
| `source_kind` | string | No | JSON projection field; omitted when absent if optional. |
| `source_ref` | string | No | Source reference, at most 512 UTF-8 bytes |
| `title` | string | No | Title, at most 256 UTF-8 bytes |
| `canonical_markdown` | string | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Variant and conditional requirements:

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

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | JSON projection field; omitted when absent if optional. |
| `source_kind` | string | No | JSON projection field; omitted when absent if optional. |
| `source_ref` | string | No | JSON projection field; omitted when absent if optional. |
| `title` | string；minLength=1 | Yes | Title, at most 256 UTF-8 bytes |
| `canonical_markdown` | string | No | JSON projection field; omitted when absent if optional. |
| `final_artifact_id` | string | No | JSON projection field; omitted when absent if optional. |
| `final_artifact_digest` | string | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Variant and conditional requirements:

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

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `print_job_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `kind` | string (`question`, `answer`) | Yes | JSON projection field; omitted when absent if optional. |
| `title` | string | Yes | JSON projection field; omitted when absent if optional. |
| `paper_no` | string | Yes | Reserved/formal paper number; native receipts determine whether printing completed |
| `source_digest` | string | Yes | Digest of the frozen source used to verify the same content revision |
| `artifact_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `markdown` | string | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

<a id="schema-k12practicegenericprintpaper"></a>

### GenericPrintPaper

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `print_job_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `artifact_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `source_kind` | string (`tutoring_tips`, `creative_observation_card`, `practice_question`, `practice_answer`, `grading_final_artifact`, `weekly_practice_snapshot`, `learning_archive`) | Yes | JSON projection field; omitted when absent if optional. |
| `source_ref` | string | Yes | JSON projection field; omitted when absent if optional. |
| `title` | string | Yes | JSON projection field; omitted when absent if optional. |
| `source_digest` | string | Yes | Digest of the frozen source used to verify the same content revision |
| `markdown` | string | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

<a id="schema-k12practicehttppracticeprinteventreq"></a>

### HTTPPracticePrintEventReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `status` | string (`dialog_open`, `submitted`, `printed`, `cancelled`, `failed`, `outcome_unknown`) | Yes | JSON projection field; omitted when absent if optional. |
| `native_job_id` | string | No | Native print job identity used to reconcile unknown outcomes |
| `native_receipt_id` | string | No | Native success receipt identity, distinct from acceptance or a dialog state |
| `printer_snapshot` | object | No | JSON projection field; omitted when absent if optional. |
| `failure_kind` | string | No | Failure classification, required for failed or outcome_unknown events |
| `failure_detail` | string | No | JSON projection field; omitted when absent if optional. |

Source: [scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="schema-k12practicehttppracticeprintcommitreq"></a>

### HTTPPracticePrintCommitReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `native_job_id` | string | Yes | Native print job identity used to reconcile unknown outcomes |
| `native_receipt_id` | string | Yes | Native success receipt identity, distinct from acceptance or a dialog state |
| `printer_snapshot` | object；minProperties=1 | Yes | JSON projection field; omitted when absent if optional. |

Source: [scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="schema-k12practicehttpsubmitreturnreq"></a>

### HTTPSubmitReturnReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `return_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `asset_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `item_ids` | array string；minLength=1 | No | Practice-item IDs covered by this operation; empty does not mean the entire paper |
| `auto_match` | boolean；default=False | No | JSON projection field; omitted when absent if optional. |

Variant and conditional requirements:

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

Source: [scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="schema-k12practicehttpgraderesultsreq"></a>

### HTTPGradeResultsReq

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `results` | array / null object | No | JSON projection field; omitted when absent if optional. |

Source: [scenarios/k12/apihttp/practiceset_handler.go](../../../../scenarios/k12/apihttp/practiceset_handler.go).

<a id="schema-k12practiceweeklypracticesettings"></a>

### WeeklyPracticeSettings

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string | Yes | JSON projection field; omitted when absent if optional. |
| `revision` | integer | Yes | Current aggregate revision used by the matching revision/expected_revision CAS |
| `timezone` | string | Yes | JSON projection field; omitted when absent if optional. |
| `due_review_enabled` | boolean | Yes | JSON projection field; omitted when absent if optional. |
| `textbook_consolidation_enabled` | boolean | Yes | JSON projection field; omitted when absent if optional. |
| `textbook_consolidation_tier` | string (`less`, `standard`, `more`) | Yes | JSON projection field; omitted when absent if optional. |
| `arithmetic_warmup_enabled` | boolean | Yes | JSON projection field; omitted when absent if optional. |
| `arithmetic_minutes` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `created_at` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `updated_at` | integer | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go).

<a id="schema-k12practicehttpweeklyplancommandrequest"></a>

### HTTPWeeklyPlanCommandRequest

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

Source: [scenarios/k12/apihttp/weekly_practice_handler.go](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="schema-k12practicecurrentplanresponse"></a>

### CurrentPlanResponse

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `plan` | [WeeklyPracticePlan](#schema-k12practiceweeklypracticeplan) / null | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

<a id="schema-k12practiceweeklypracticehistorysummary"></a>

### WeeklyPracticeHistorySummary

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `snapshot_id` | string | Yes | Immutable weekly snapshot ID |
| `artifact_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `plan_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `iso_week_year` | integer | Yes | Timezone-specific ISO week year, which may differ from the calendar year |
| `iso_week_number` | integer | Yes | ISO week number 1–53 |
| `timezone` | string | Yes | JSON projection field; omitted when absent if optional. |
| `local_start_date` | string | Yes | JSON projection field; omitted when absent if optional. |
| `local_end_date` | string | Yes | JSON projection field; omitted when absent if optional. |
| `item_count` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `correct_count` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `wrong_count` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `needs_review_count` | integer | Yes | JSON projection field; omitted when absent if optional. |
| `archived_at` | integer | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go).

<a id="schema-k12practiceweeklyhistoryresponse"></a>

### WeeklyHistoryResponse

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `items` | array / null [WeeklyPracticeHistorySummary](#schema-k12practiceweeklypracticehistorysummary) | Yes | JSON projection field; omitted when absent if optional. |
| `next_cursor` | string / null | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

<a id="schema-k12practicehttpweeklyexpectedrevisionrequest"></a>

### HTTPWeeklyExpectedRevisionRequest

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `expected_revision` | integer；minimum=1 | Yes | JSON projection field; omitted when absent if optional. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

Source: [scenarios/k12/apihttp/weekly_practice_handler.go](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="schema-k12practiceweeklyoutputresponse"></a>

### WeeklyOutputResponse

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `snapshot` | [WeeklyPracticeSnapshot](#schema-k12practiceweeklypracticesnapshot) | Yes | JSON projection field; omitted when absent if optional. |
| `artifact` | [PrintableArtifact](#schema-k12practiceprintableartifact) | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

<a id="schema-k12practicehttpweeklysendrequest"></a>

### HTTPWeeklySendRequest

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

Source: [scenarios/k12/apihttp/weekly_practice_handler.go](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="schema-k12practicehttpweeklyattemptrequest"></a>

### HTTPWeeklyAttemptRequest

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `agent` | string；minLength=1 | Yes | K12 agent name; selects this learner record scope |
| `item_id` | string | Yes | Current practice-item ID, distinct from its source mistake ID |
| `student_answer` | string | Yes | JSON projection field; omitted when absent if optional. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

Source: [scenarios/k12/apihttp/weekly_practice_handler.go](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="schema-k12practiceweeklypracticeattempt"></a>

### WeeklyPracticeAttempt

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `attempt_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `snapshot_id` | string | Yes | Immutable weekly snapshot ID |
| `item_id` | string | Yes | Current practice-item ID, distinct from its source mistake ID |
| `assessment_id` | string | Yes | Durable one-item assessment ID linking the original result |
| `result` | string (`correct`, `wrong`, `needs_review`) | Yes | JSON projection field; omitted when absent if optional. |
| `verification_evidence` | string | Yes | Verification method or evidence reference |
| `mistake_record_id` | string | No | JSON projection field; omitted when absent if optional. |
| `review_scheduled` | boolean | Yes | Whether this error scheduled review; does not establish mastery |
| `created_at` | integer | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go).

<a id="schema-k12practiceweeklyattemptresponse"></a>

### WeeklyAttemptResponse

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `attempt` | [WeeklyPracticeAttempt](#schema-k12practiceweeklypracticeattempt) | Yes | JSON projection field; omitted when absent if optional. |
| `replayed` | boolean | Yes | Replay of a durable result for the same frozen command without repeating new domain effects |

Unknown fields are not part of this schema.

<a id="schema-k12practicehttpweeklymanualarithmeticrequest"></a>

### HTTPWeeklyManualArithmeticRequest

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `plan_revision` | integer；minimum=1 | Yes | Plan revision bound to the snapshot or used by request CAS |
| `item_count` | integer；minimum=1; maximum=20 | Yes | JSON projection field; omitted when absent if optional. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

Unknown fields are not part of this schema.

Source: [scenarios/k12/apihttp/weekly_practice_handler.go](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="schema-k12practiceweeklyarithmeticattempt"></a>

### WeeklyArithmeticAttempt

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `attempt_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `batch_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `item_id` | string | Yes | Current practice-item ID, distinct from its source mistake ID |
| `assessment_id` | string | Yes | Durable one-item assessment ID linking the original result |
| `result` | string (`correct`, `wrong`, `needs_review`) | Yes | JSON projection field; omitted when absent if optional. |
| `verification_evidence` | string | Yes | Verification method or evidence reference |
| `mistake_record_id` | string | No | JSON projection field; omitted when absent if optional. |
| `review_scheduled` | boolean | Yes | Whether this error scheduled review; does not establish mastery |
| `created_at` | integer | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/weekly_arithmetic.go](../../../../scenarios/k12/weekly_arithmetic.go).

<a id="schema-k12practicearithmeticattemptresponse"></a>

### ArithmeticAttemptResponse

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `attempt` | [WeeklyArithmeticAttempt](#schema-k12practiceweeklyarithmeticattempt) | Yes | JSON projection field; omitted when absent if optional. |
| `replayed` | boolean | Yes | Replay of a durable result for the same frozen command without repeating new domain effects |

Unknown fields are not part of this schema.

<a id="schema-k12practicehttpweeklymanualtextbookrequest"></a>

### HTTPWeeklyManualTextbookRequest

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `plan_revision` | integer；minimum=1 | Yes | Plan revision bound to the snapshot or used by request CAS |
| `item_count` | integer；minimum=1; maximum=10 | Yes | JSON projection field; omitted when absent if optional. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |

Unknown fields are not part of this schema.

Source: [scenarios/k12/apihttp/weekly_practice_handler.go](../../../../scenarios/k12/apihttp/weekly_practice_handler.go).

<a id="schema-k12practiceweeklytextbookrecoveryrequest"></a>

### WeeklyTextbookRecoveryRequest

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `source_command_key` | string；minLength=1 | Yes | Original textbook prepare/refresh command key from existing recovery context. |
| `plan_revision` | integer；minimum=1 | No | Plan revision bound to the snapshot or used by request CAS |
| `checkpoint_sha256` | string；pattern=^[a-fA-F0-9]{64}$ | Yes | Actual raw-byte SHA-256 of the original persisted checkpoint, unavailable from public plan/snapshot queries; use the actual lowercase digest. |
| `item_index` | integer；minimum=0; default=0 | No | Zero-based generation.items index in the original checkpoint; omission selects the first item, so specify it explicitly. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `accept_duplicate_execution` | boolean (`true`) | Yes | JSON projection field; omitted when absent if optional. |
| `source_plan_revision` | integer；minimum=1 | No | JSON projection field; omitted when absent if optional. |
| `expected_plan_revision` | integer；minimum=1 | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Variant and conditional requirements:

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

Source: [scenarios/k12/usecase/weekly_recovery.go](../../../../scenarios/k12/usecase/weekly_recovery.go).

<a id="schema-k12practiceweeklytextbookreinterpretationrequest"></a>

### WeeklyTextbookReinterpretationRequest

Request schema; parameters are also shown at each operation.

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `source_command_key` | string；minLength=1 | Yes | Original textbook prepare/refresh command key from existing recovery context. |
| `plan_revision` | integer；minimum=1 | No | Plan revision bound to the snapshot or used by request CAS |
| `checkpoint_sha256` | string；pattern=^[a-fA-F0-9]{64}$ | Yes | Actual raw-byte SHA-256 of the original persisted checkpoint, unavailable from public plan/snapshot queries; use the actual lowercase digest. |
| `item_index` | integer；minimum=0; default=0 | No | Zero-based generation.items index in the original checkpoint; omission selects the first item, so specify it explicitly. |
| `idempotency_key` | string；minLength=1 | Yes | Unique command key; retain identical frozen parameters on replay, not permission for blind retry |
| `source_plan_revision` | integer；minimum=1 | No | JSON projection field; omitted when absent if optional. |
| `expected_plan_revision` | integer；minimum=1 | No | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Variant and conditional requirements:

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

Source: [scenarios/k12/usecase/weekly_reinterpretation.go](../../../../scenarios/k12/usecase/weekly_reinterpretation.go).

<a id="schema-k12practiceweeklypracticesavereceipt"></a>

### WeeklyPracticeSaveReceipt

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `save_receipt_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `plan_id` | string | Yes | JSON projection field; omitted when absent if optional. |
| `plan_revision` | integer | Yes | Plan revision bound to the snapshot or used by request CAS |
| `snapshot_id` | string | Yes | Immutable weekly snapshot ID |
| `practice_set_id` | string | Yes | Practice-set ID containing the joined item |
| `created_at` | integer | Yes | JSON projection field; omitted when absent if optional. |

Unknown fields are not part of this schema.

Source: [scenarios/k12/weekly_practice.go](../../../../scenarios/k12/weekly_practice.go).

<a id="schema-k12practiceweeklysaveresponse"></a>

### WeeklySaveResponse

| Field | Type / constraints | Required | Meaning |
| --- | --- | --- | --- |
| `receipt` | [WeeklyPracticeSaveReceipt](#schema-k12practiceweeklypracticesavereceipt) | Yes | JSON projection field; omitted when absent if optional. |
| `replayed` | boolean | Yes | Replay of a durable result for the same frozen command without repeating new domain effects |

Unknown fields are not part of this schema.
