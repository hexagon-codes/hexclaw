# K12 records, textbooks and delivery API

New text-solve, practice-generation, and accumulation-metadata tasks automatically prepare missing or stale text-capability receipts for the exact selected model; no manual probe is required. Preparation failures retain their original errors instead of admitting a permanently empty model snapshot. Frozen tasks retain their original route and receipt, and current failed probes or unknown outcomes are not automatically resent. First-use preparation calls the actual Provider; declarations or model self-reports do not replace a successful receipt.

When text grading has a genuine settlement for the same Agent, a persisted assessment reference, and strong verification evidence, new mistakes use the existing `entry_source: verified` rather than claiming a photo source. Image grading and legacy direct calls retain `photo`; manual entry retains `manual`. Existing records are not rewritten by this projection rule.

When a verified solution has an explicit answer section, a parent guide's `answer` preserves all requested quantities, units and negative conditions. The use case restores incomplete explanatory candidates from that source without another model call. Legacy solutions without an explicit answer section retain their existing validation; original student answers and assessments remain unchanged.

New dedicated teaching and work-feedback calls honor current Skill enablement. Explicitly disabled or uninstalled methods do not fall back to an embedded Skill of the same name; required application facts and solving/output contracts remain. Existing frozen results retain their original identity and are not regenerated when the catalog or settings change.

For photo mistakes, `question` is the problem statement. Recognized versioned solver-scope wrappers and complete internal scope suffixes remain model-request details; list and review responses project the original statement. Unknown formats remain unchanged. This read projection preserves historical rows, IDs, versions, states and frozen artifacts; new mistakes store the same statement representation.

This reference covers the 51 method/path registrations in the current source, including worktree changes, for a HexClaw service with the K12 scenario enabled. Use the documentation and installer at the relevant Tag for a released version; a mainline route is not a promise that an older installer contains it.

- [Scenario API and shared rules](../../API.en.md) · [中文](records.md) · [Complete OpenAPI](../../../../api/openapi.yaml)
- The scenario reference owns [atomic profile updates](../../API.en.md#profile-bundle) and the [ten shared textbook/progress rules](../../API.en.md#textbook-and-curriculum-progress). This page documents each operation's fields, responses and side effects.

All paths require the current service `Authorization: Bearer <token>`. Local Sidecar identity is composed by the service; remote owner identity comes from authenticated context. `agent` is a Tutor/learner scope, not an identity credential. Textbook operations, profile transactions and Cron provisioning additionally verify owner→agent scope. Do not submit an owner in a body, query or custom header. Routes are absent when K12 is disabled; missing delivery/Cron adapters return 501, and missing image/practice coordinators can return 503.

JSON commands must contain exactly one JSON value and are limited to 1 MiB, except restore/restore-as (128 MiB). Operations marked strict reject unknown fields. Scenario errors are `{"error":"..."}`, without a universal code/message envelope. Error tables reflect handler mappings; Bearer middleware can additionally return 401. 400 usually means invalid input, 404 means absent/invisible scope, 409 means version/state/idempotency conflict or an invocation requiring reconciliation, and 502 means downstream failure. Do not disguise technical failures as unreadable source content. Unix timestamps use seconds.

For sends, follow server-owned batch/receipt state: `pending → sending → delivered`. Eligible failed/partial_failed children can be retried; query outcome_unknown before any resend. GET reads stored state; POST/query reconciles and persists Provider results. HTTP 200 is only a successful command response. A batch is delivered only after every required child receipt is delivered.

## Operation index

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | [`/api/k12/view-descriptor`](#viewdescriptor) | Read the tutor view descriptor |
| `POST` | [`/api/k12/grade`](#grade) | Grade one problem |
| `POST` | [`/api/k12/record-mistake`](#recordmistake) | Record a known mistake |
| `POST` | [`/api/k12/solve`](#solve) | Solve one problem |
| `GET` | [`/api/k12/text-solves/{solve_run_id}`](#gettextsolve) | Read a text-solve ledger and finalization |
| `POST` | [`/api/k12/text-solves/{solve_run_id}/resume-publication`](#resumetextsolvepublication) | Recover the original text finalization and answer publication |
| `GET` | [`/api/k12/print-replies/{message_id}`](#print-replies) | Read original-turn PDF delivery facts |
| `POST` | [`/api/k12/print-replies/{message_id}/resume`](#print-replies) | Resume the original-turn PDF batch |
| `GET` | [`/api/k12/review-queue`](#reviewqueue) | Read the due review queue |
| `POST` | [`/api/k12/review-source-groups`](#recordreviewsourcegroup) | Record reviewed equivalent question sources |
| `GET` | [`/api/k12/insight-report`](#insightreport) | Read an insight snapshot |
| `POST` | [`/api/k12/mark-mastered`](#markmastered) | Record parent confirmation and schedule a spot check |
| `POST` | [`/api/k12/tutoring-tips`](#tutoringtips) | Build tutoring tips for completed homework |
| `POST` | [`/api/k12/tutoring-tips/send`](#sendtutoringtips) | Send the frozen tutoring artifact |
| `GET` | [`/api/k12/delivery-receipts/{id}`](#getdeliveryreceipt) | Get delivery receipt |
| `POST` | [`/api/k12/delivery-receipts/{id}/retry`](#retrydeliveryreceipt) | Retry delivery receipt |
| `POST` | [`/api/k12/delivery-receipts/{id}/query`](#querydeliveryreceipt) | Query delivery receipt |
| `GET` | [`/api/k12/delivery-batches/{id}`](#getdeliverybatch) | Get delivery batch |
| `POST` | [`/api/k12/delivery-batches/{id}/retry`](#retrydeliverybatch) | Retry delivery batch |
| `POST` | [`/api/k12/delivery-batches/{id}/query`](#querydeliverybatch) | Query delivery batch |
| `POST` | [`/api/k12/grounding`](#addgrounding) | Add textbook grounding |
| `POST` | [`/api/k12/accumulation`](#addaccumulation) | Create an accumulation entry |
| `GET` | [`/api/k12/accumulation`](#listaccumulation) | List accumulation entries |
| `GET` | [`/api/k12/accumulation/{id}`](#getaccumulation) | Read an accumulation entry |
| `POST` | [`/api/k12/accumulation/{id}/send`](#sendaccumulation) | Send stored accumulation content |
| `POST` | [`/api/k12/accumulation/{id}/dictation-to-basket`](#accumdictationtobasket) | Accept accumulation dictation generation |
| `DELETE` | [`/api/k12/accumulation/{id}`](#deleteaccumulation) | Delete an accumulation entry |
| `GET` | [`/api/k12/backup`](#backup) | Read a complete learner backup |
| `POST` | [`/api/k12/restore`](#restore) | Merge a backup into the same Tutor |
| `POST` | [`/api/k12/restore-as`](#restoreas) | Migrate a backup to another Tutor |
| `POST` | [`/api/k12/restore-as/{migration_id}/rollback`](#rollbackrestoreas) | Roll back a cross-Tutor migration |
| `GET` | [`/api/k12/export`](#export) | Export the current-term learning archive |
| `GET` | [`/api/k12/mistake-sheet`](#mistakesheet) | Build the due mistake sheet |
| `GET` | [`/api/k12/profile`](#getprofile) | Read the learner profile and revision |
| `PUT` | [`/api/k12/profile`](#updateprofile) | Disabled legacy profile update |
| `GET` | [`/api/k12/textbook-binding-options`](#listtextbookbindingoptions) | Read textbook manifest candidates |
| `GET` | [`/api/k12/curriculum-catalog`](#getcurriculumcatalog) | Read the authoritative math catalog |
| `GET` | [`/api/k12/curriculum-progress`](#getcurriculumprogress) | Read curriculum progress or preview an estimate |
| `PUT` | [`/api/k12/profile-bundle`](#updateprofilebundle) | Atomically update profile, progress and weekly settings |
| `POST` | [`/api/k12/cold-start`](#coldstart) | Preview or confirm a cold-start profile |
| `POST` | [`/api/k12/tutor-turn`](#tutorturn) | Generate parent tutoring guidance |
| `GET` | [`/api/k12/cron/mistake-sheet`](#cronmistakesheet) | Weekly due-mistake sheet |
| `POST` | [`/api/k12/cron/fill-basket`](#cronfillbasket) | Legacy no-op fill-basket endpoint |
| `GET` | [`/api/k12/cron/daily-reminder`](#crondailyreminder) | Daily review reminder |
| `GET` | [`/api/k12/cron/return-reminder`](#cronreturnreminder) | Practice-return reminder |
| `GET` | [`/api/k12/cron/monthly-report`](#cronmonthlyreport) | Monthly insight report |
| `GET` | [`/api/k12/cron/semester-check`](#cronsemestercheck) | Term-transition reminder |
| `GET` | [`/api/k12/cron/year-archive`](#cronyeararchive) | Year-end archive suggestion |
| `POST` | [`/api/k12/cron/provision`](#cronprovision) | Replace the four default automation jobs |
| `POST` | [`/api/k12/cron/reconcile-defaults`](#cronreconciledefaults) | Fill only missing default jobs |
| `POST` | [`/api/k12/bind-im`](#bindim) | Bind a Tutor to an IM direct conversation |

<a id="viewdescriptor"></a>
## GET /api/k12/view-descriptor

Read the tutor view descriptor.

Read-only. Omitted or empty slot uses tutor. Returns registered tabs, badges, composer hints, panels and schema_version.

Availability: K12 module and ViewExtensionRegistry.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `slot` | query | No | string  |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `actions` | array<string> / null | Yes |  |
| `composer_chips` | array<string> / null | Yes |  |
| `composer_placeholder` | string | Yes |  |
| `header_tabs` | array<string> / null | Yes |  |
| `i18n_keys` | array<string> / null | Yes |  |
| `message_badges` | array<string> / null | Yes |  |
| `record_collections` | array<string> / null | Yes |  |
| `schema_version` | integer | Yes |  |
| `side_panels` | array<string> / null | Yes |  |


| Status | Error meaning |
| --- | --- |
| `404` | Unknown view slot. |

[Implementation](../../apihttp/handler.go)


<a id="grade"></a>
## POST /api/k12/grade

Grade one problem.

Retained direct calibration endpoint, not the image-task entry point. agent/problem are required. An empty student_answer selects solve-only: solve_only=true, no grading or mistake creation. Grading may persist mistakes/review changes; inspect record_created and record_id rather than infer persistence from verdict. out_of_scope and curriculum_unmapped are separate facts.

The authenticated server wiring determines the trusted owner. Tutor profiles retain their owner-to-tutor checks. Ordinary and default assistants can solve or grade without creating a learner profile and do not write student mistakes, review state or learning records. Direct HTTP requests do not need fabricated tool request/session/message identities. A supplied source session still requires actual access.

When a submitted student answer is successfully graded, `grade_report_ref` can bind the quick answers and deep explanation to the actual grading receipt for the same text run. The original `finalization` remains an immutable solve result. To prepare this graded report as a PDF, pass the reference with the text finalization identity to [print-artifacts](practice.en.md#k12practiceprepareprintableartifact). Historical sources without a reference always retain their original ungraded projection; reading them does not look up the latest grade to backfill history.

Availability: K12 record store, Solver/Grader and optional curriculum/insight adapters.

JSON body:

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. minLength=1 |
| `grade` | `` / `一年级上` / `一年级下` / `二年级上` / `二年级下` / `三年级上` / `三年级下` / `四年级上` / `四年级下` / `五年级上` / `五年级下` / `六年级上` / `六年级下` / `初一上` / `初一下` / `初二上` / `初二下` / `初三上` / `初三下` | No | When omitted or empty, use the saved profile term if available; otherwise no term constraint is injected. |
| `knowledge_points` | array<string> / null | No | Knowledge-point names. |
| `problem` | string | Yes | Problem text. minLength=1 |
| `source_session` | string | No | Optional originating session reference. |
| `student_answer` | string | No | Learner answer, not a canonical solution. |
| `subject` | `` / `数学` / `语文` / `英语` / `物理` / `化学` | No | Empty preserves the default route; these direct-calibration endpoints use Chinese subject names. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `assessment_status` | `correct` / `correct_with_process_issue` / `wrong` / `unanswered` / `answer_unclear` / `blank_solved` / `out_of_scope` / `untrusted` | No |  |
| `badge` | string | Yes | Verification badge value, such as verified-strong. |
| `curriculum_unmapped` | array<string> / null | No | Knowledge points missing from curriculum mapping; separate from out-of-scope. |
| `error_cause` | string | No |  |
| `evidence_type` | `numeric_exec` / `symbolic_exec` / `heterogeneous_model` / `heuristic` / `verbatim` / `none` | Yes |  |
| `final_answer_correct` | boolean / null | No |  |
| `out_of_scope` | boolean | Yes | Whether the term boundary was triggered. |
| `out_of_scope_kp` | string | No | Knowledge point triggering the scope boundary; optional. |
| `record_created` | boolean | Yes | Whether this operation created a mistake record. |
| `record_id` | string | No | Server-issued record identity. |
| `finalization` | object | No | Original immutable text-solve finalization, including `solve_run_id`, `finalization_id`, and `result_digest` for printing; grading does not rewrite it. |
| `answer_source` | object | No | Actual generated, deterministic, or adopted answer source from the original solve, separate from the student grading receipt. |
| `grade_report_ref` | [TextGradeReportReference](#text-grade-report-reference) | No | Exact reference to a successful grading receipt; omitted for solve-only results or when no such receipt exists. |
| `teaching_report` | object | No | Versioned immutable report derived from the actual frozen source; omitted for legacy or unsupported reports. |
| `report_markdown` | string | No | Canonical quick answers and deep explanation from the same report object as the PDF. |
| `solution` | string | Yes | Complete solution text. |
| `solve_only` | boolean | Yes | true means solve-only; do not render it as a grading verdict. |
| `verdict` | `agree` / `disagree` / `unverifiable` / `out_of_scope` / `verbatim` | Yes |  |
| `wrong_step` | string | No |  |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid input or term/subject. |
| `404` | Scoped record not found. |
| `409` | Model invocation needs reconciliation; do not blindly resend. |
| `500` | Unclassified execution/storage error. |
| `502` | Solver, grader or verification failed. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/handler.go)


<a id="text-grade-report-reference"></a>
### TextGradeReportReference

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `invocation_id` | string | Yes | Server-issued ID of a successful `grade` receipt, bound to the same owner, agent, text run, and input. |
| `result_digest` | string | Yes | Digest of that grading receipt's result; it cannot be replaced with the solve `finalization.result_digest`. |

This object references an actual assessment; callers cannot supply a grading verdict through it. Printing validates the receipt status, original input, and digest. An invalid supplied reference returns an error instead of falling back to an ungraded report.

<a id="recordmistake"></a>
## POST /api/k12/record-mistake

Record a known mistake.

Writes a known mistake without running solve+verify. An empty error_cause may trigger one cause-summary call; summary failure does not prevent persistence. Record deduplication can return record_created=false. student_answer is the learner answer, not a canonical solution.

Availability: K12 records; optional cause summarizer.

JSON body:

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. minLength=1 |
| `error_cause` | string | No |  |
| `grade` | `` / `一年级上` / `一年级下` / `二年级上` / `二年级下` / `三年级上` / `三年级下` / `四年级上` / `四年级下` / `五年级上` / `五年级下` / `六年级上` / `六年级下` / `初一上` / `初一下` / `初二上` / `初二下` / `初三上` / `初三下` | No | When omitted or empty, use the saved profile term if available; otherwise no term constraint is injected. |
| `knowledge_points` | array<string> / null | No | Knowledge-point names. |
| `problem` | string | Yes | Problem text. minLength=1 |
| `source_session` | string | No | Optional originating session reference. |
| `student_answer` | string | No | Learner answer, not a canonical solution. |
| `subject` | `` / `数学` / `语文` / `英语` / `物理` / `化学` | No | Empty preserves the default route; these direct-calibration endpoints use Chinese subject names. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `error_cause` | string | No |  |
| `record_created` | boolean | Yes | Whether this operation created a mistake record. |
| `record_id` | string | No | Server-issued record identity. |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid input or term/subject. |
| `404` | Scoped record not found. |
| `409` | Model invocation needs reconciliation; do not blindly resend. |
| `500` | Unclassified execution/storage error. |
| `502` | Solver, grader or verification failed. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/handler.go)


<a id="solve"></a>
## POST /api/k12/solve

Solve one problem.

agent/problem are required. Returns the solution, verification evidence and scope projection without grading or creating a mistake. Subject accepts empty or 数学/语文/英语/物理/化学; an omitted term uses the stored profile when available.

When the text execution ledger is wired, owner/agent/solve_run_id freeze the complete input, origin and route. Identical input replays the original result; a changed problem, revision or origin under the same key returns409. Freeze a stable solve_run_id before sending; omitting it creates a new run, so repeating unkeyed requests is not recovery. An absent origin stays empty; a supplied message must be a real user message in the supplied session, with session access checked by the existing authenticated wiring. An unavailable model route does not block local deterministic arithmetic or verified answer adoption; only a new step that actually needs a Provider fails.

Ordinary, default and tutor assistants share the same text finalization contract without fabricated learner or source-message records. Completed results are read using the original input, receipts and result digest; later answer-asset updates or archival do not rewrite that finalization. A new adoption still checks current eligibility. Failed teaching enrichment retains the verified complete answer and reliable steps while preserving failed or unknown receipts. Reading history does not resend model requests.

Multiple explicit blanks are compared in the order of the current original subproblem, without requiring the word “填”. Repeated shared/canonical descriptions do not add answer slots. A claimed first error must be grounded in actual student work, allowing normalized mathematical symbols, whitespace and explanatory paraphrases. Missing process evidence retains the verified reference answer under the existing unverifiable judgment; it does not publish invented steps or establish that the complete work was correct.

Availability: K12 Solver and optional curriculum adapter.

JSON body:

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. minLength=1 |
| `grade` | `` / `一年级上` / `一年级下` / `二年级上` / `二年级下` / `三年级上` / `三年级下` / `四年级上` / `四年级下` / `五年级上` / `五年级下` / `六年级上` / `六年级下` / `初一上` / `初一下` / `初二上` / `初二下` / `初三上` / `初三下` | No | When omitted or empty, use the saved profile term if available; otherwise no term constraint is injected. |
| `knowledge_points` | array<string> / null | No | Knowledge-point names. |
| `problem` | string | Yes | Problem text. minLength=1 |
| `solve_run_id` | string | No | Stable caller run ID scoped by authenticated owner and agent; a new value is generated when omitted. |
| `source_session` | string | No | Actual originating session; stays empty when absent. |
| `source_message_id` | string | No | Existing user message in the supplied session; do not fabricate a reference. |
| `input_revision` | integer | No | Defaults to1 and must be positive; an existing run cannot change revision. |
| `subject` | `` / `数学` / `语文` / `英语` / `物理` / `化学` | No | Empty preserves the default route; these direct-calibration endpoints use Chinese subject names. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `badge` | string | Yes | Verification badge value, such as verified-strong. |
| `answer_source` | object | No | Actual asset, deterministic or model answer source with adoption/execution references; this is not a learner assessment. |
| `finalization` | object | No | Immutable text finalization: source_kind=text_solve, solve_run_id, finalization_id, input_revision, input_digest, facts_digest, result_json/result_digest, invocation_id and optional publication. |
| `curriculum_unmapped` | array<string> / null | No | Knowledge points missing from curriculum mapping; separate from out-of-scope. |
| `evidence_type` | `numeric_exec` / `symbolic_exec` / `heterogeneous_model` / `heuristic` / `verbatim` / `none` | Yes |  |
| `out_of_scope` | boolean | Yes | Whether the term boundary was triggered. |
| `out_of_scope_kp` | string | No | Knowledge point triggering the scope boundary; optional. |
| `teaching_report` | object | No | Versioned immutable report derived from the actual frozen source; omitted for legacy or unsupported reports. |
| `report_markdown` | string | No | Canonical quick answers and deep explanation from the same report object as the PDF. |
| `solution` | string | Yes | Complete solution text. |
| `verdict` | `agree` / `disagree` / `unverifiable` / `out_of_scope` / `verbatim` | Yes |  |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid input or term/subject. |
| `404` | Scoped record not found. |
| `409` | Model invocation needs reconciliation; do not blindly resend. |
| `500` | Unclassified execution/storage error. |
| `502` | Solver, grader or verification failed. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/handler.go)


<a id="gettextsolve"></a>
## GET /api/k12/text-solves/{solve_run_id}

Read the original text-solve ledger in the authenticated owner/agent scope. Read-only: it sends no model request and does not turn an unknown outcome into success. An existing run without a finalization still returns200 with its invocations. Provider steps and local actions remain distinct; ledger row counts are not HTTP request or billing counts.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `solve_run_id` | path | Yes | Nonempty stable run ID. |
| `agent` | query | Yes | Actual Tutor in the authenticated ownership scope. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `run` | object | Yes | Frozen identity, origin, input revision, complete facts/digests, request_json and route_snapshot. |
| `invocations` | array<object> / null | Yes | Actual execution steps, original states and result digests/JSON; execution_kind separates provider and local_deterministic. Do not blindly resend sent/outcome_unknown. |
| `finalized` | boolean | Yes | Whether a committed finalization exists. |
| `publication_eligible` | boolean | Yes | Whether the original finalization has a publication intent; not a substitute for publication-time validation and not evidence of publication. |
| `finalization` | object | No | Original immutable finalization when complete; historical read access does not establish current adoption validity. |
| `adoption_currently_valid` | boolean | No | Present only for an adopted answer; checks the original adoption and current asset validity. |

| Status | Error meaning |
| --- | --- |
| `400` | Missing agent or solve_run_id. |
| `404` | Missing run or invisible Tutor scope. |
| `409` | Identity, version or result conflict. |
| `503` | Text execution records are not wired. |
| `500` | Unclassified storage/read error. |

[Implementation](../../apihttp/text_solve_handler.go)

<a id="resumetextsolvepublication"></a>
## POST /api/k12/text-solves/{solve_run_id}/resume-publication

No JSON body; path/query parameters match the preceding read endpoint. Consumes the original finalization and answer-publication contract without invoking the Solver or a Provider. If a successful solve result was saved but its final transaction was not committed, reconstruct only the local finalization from the complete, digest-matching actual result. Missing recoverable results return404; sent/outcome_unknown returns409 without solving again. Original agree+numeric_exec, supported scope and actual source validation still apply; no GradingJob or learner assessment is manufactured. Recovery cannot automatically revive an archived asset.

Success: `200` `application/json`. Without an original eligible publication intent, published=false and asset=null; this does not mean solving failed.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `published` | boolean | Yes | Whether this operation returns the original published asset. |
| `asset` | object / null | Yes | Original answer asset version and revision, or null without an eligible intent. |
| `model_invocations_added` | integer | Yes | Always0; recovery invokes no model. |

Uses the preceding read endpoint's status mappings.409 includes unknown outcomes and mapped identity/adoption/publication conflicts; unclassified publication-evidence or storage errors return500 in the actual handler. Technical errors are not replaced with a new unverified answer.

[Implementation](../../apihttp/text_solve_handler.go)


<a id="reviewqueue"></a>
## GET /api/k12/review-queue

Read the due review queue.

Read-only; includes mistakes and corrective accumulations. review_kind is verify or verbatim. Parent confirmation and spot-check state are not mastery evidence.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `items` | array<`mistakeDTO`> | Yes | Server-owned list projection. |


| Status | Error meaning |
| --- | --- |
| `400` | agent required. |
| `500` | Queue or review-state read failed. |

[Implementation](../../apihttp/handler.go)


<a id="recordreviewsourcegroup"></a>
## POST /api/k12/review-source-groups

Persist reviewed source equivalence for read-only mistake-sheet and weekly-practice grouping. Strict JSON requires `agent`, a member `canonical_record_id`, at least two distinct records with complete frozen source tuples, and `evidence: {kind: "reviewed_sources", note: "..."}`. The server derives statements, page digests and numbering; replacement answers or client-authored canonical statements are not accepted.

Returns 201 on creation and 200 on replay. Invalid input is 400, missing/cross-agent records 404, changed sources or overlapping active groups 409, and storage failure 500. The response `{group, created, replayed}` retains the first statement, note and timestamp. Existing mistakes, learning states and PDFs remain intact. A stale member disables the whole group for projection. No model call or message delivery occurs.

See the [complete contract](review-source-groups.en.md) and [handler](../../apihttp/review_source_group_handler.go).

<a id="insightreport"></a>
## GET /api/k12/insight-report

Identically named knowledge points are counted separately by subject. The suggestion opening and detailed observation select the same target. A new return from the same diagnostic problem can update the supporting evidence; learning-event and condition rules are documented in the [practice API](practice.en.md).

Read an insight snapshot.

Read-only. Numbers, weak points, drill-down filters and report text share one as_of/source_digest/source_record_ids snapshot. review_completion_rate=-1 means a zero denominator, not 0% completion. message_content/render_manifest project the same canonical text.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `as_of` | integer | Yes | Frozen snapshot time in Unix seconds. |
| `consecutive_fail_kps` | array<string> / null | Yes |  |
| `grade_term` | string | Yes | Learner profile grade-term. |
| `learner` | string | Yes |  |
| `month_new_mistakes` | integer | Yes |  |
| `practice_pending` | integer | Yes |  |
| `review_completion_rate` | number | Yes | -1 means the review-week denominator is zero; otherwise the server-derived proportion. Do not recompute using another snapshot. |
| `review_week_end` | integer | Yes |  |
| `review_week_start` | integer | Yes |  |
| `source_digest` | string | Yes | Digest of the same source snapshot. |
| `source_record_ids` | array<string> / null | Yes |  |
| `suggestion` | string | Yes |  |
| `trend` | `TrendCounts` | Yes |  |
| `unscoped_source_count` | integer | Yes |  |
| `weak_top3` | array<`WeakPoint`> / null | Yes |  |
| `week_pending` | integer | Yes |  |
| `message_content` | `MessageContent` / null | No |  |
| `render_manifest` | `RenderManifest` / null | No |  |


| Status | Error meaning |
| --- | --- |
| `400` | agent required. |
| `500` | Snapshot read/render projection failed. |

[Implementation](../../apihttp/handler.go)


<a id="markmastered"></a>
## POST /api/k12/mark-mastered

Record parent confirmation and schedule a spot check.

The historical path records parent_confirmed_at and schedules a spot check while preserving the mistake status. It does not immediately establish mastery. version is the current record version; a conflict returns 409. The success body is only {"ok":true}.

Availability: K12 module, record store and the dependencies described for this operation.

JSON body:

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. minLength=1 |
| `record_id` | string | Yes | Server-issued record identity. minLength=1 |
| `version` | integer | Yes | Expected current record version, compared by the storage CAS. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `ok` | `True` | Yes |  |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid JSON or input. |
| `404` | Scoped resource not found. |
| `409` | Version, command or state conflict. |
| `500` | Storage or other unclassified execution error. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/handler.go)


<a id="tutoringtips"></a>
## POST /api/k12/tutoring-tips

Build tutoring tips for completed homework.

Accepts only agent and the public dispatch_id. Tips use the owned confirmed homework facts, saved term and textbook; no internal GradingJob identity or client-supplied text is accepted.

Availability: K12 ImageTask facade and owner-scoped tutoring/grounding dependencies.

JSON body (strict decoding):

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. minLength=1 |
| `dispatch_id` | string | Yes | minLength=1 |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `knowledge_points` | array<string> | Yes | Knowledge-point names. |
| `sections` | array<[`TutoringTipsSection`](#tutoringtipssection)> | Yes | Exactly three sections. |


| Status | Error meaning |
| --- | --- |
| `400` | agent/dispatch_id required; unknown JSON fields rejected. |
| `404` | Owned dispatch/profile/catalog not found. |
| `409` | Dispatch not in a tips-capable state. |
| `500` | Unclassified execution failure. |
| `502` | Grounding/model dependency failure. |
| `503` | ImageTask facade not composed. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/handler.go)


<a id="sendtutoringtips"></a>
## POST /api/k12/tutoring-tips/send

Send the frozen tutoring artifact.

Sends the server-owned final_artifact_id. The optional digest must match when provided; arbitrary text is rejected. Uses a frozen snapshot of all active physical direct targets, deduplicated by (platform, instance_id, chat_id). HTTP 200 returns batch facts, not proof of delivery; query receipts until delivered. Query unknown outcomes before retrying.

Availability: K12 durable delivery adapter and at least one active direct binding.

JSON body (strict decoding):

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. minLength=1 |
| `final_artifact_id` | string | Yes | minLength=1 |
| `final_artifact_digest` | string | No |  |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `agent_name` | string | Yes | Tutor owning this resource. |
| `batch_id` | string | Yes | Durable delivery batch identity. |
| `content_digest` | string | Yes | Digest of frozen delivery content. |
| `created_at` | integer | Yes | Creation time in Unix seconds. |
| `dedupe_key` | string | Yes | Server-owned durable deduplication identity. |
| `object_id` | string | Yes |  |
| `object_kind` | string | Yes |  |
| `receipts` | array<`DeliveryReceipt`> / null | Yes | All target/content receipts in batch order. |
| `status` | `DeliveryBatchStatus` | Yes | Current server-owned durable state; use the enclosing resource enum. |
| `updated_at` | integer | Yes | Update time in Unix seconds. |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid/unknown JSON or final artifact mismatch. |
| `404` | Owned artifact not found. |
| `409` | No active direct bindings, invalid delivery state or unsupported reconciliation. Unclassified processing/storage errors also currently map to 409. |
| `501` | Delivery not composed. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/delivery_receipt_handler.go)


<a id="getdeliveryreceipt"></a>
## GET /api/k12/delivery-receipts/{id}

Get delivery receipt.

Reads durable state. sending/external_message_id is not proof of delivered.

Availability: K12 durable delivery adapter; query additionally needs provider reconciliation support.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `id` | path | Yes | string Server-issued resource identity. |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `agent_name` | string | Yes | Tutor owning this resource. |
| `attempt` | integer | Yes | Server-recorded attempt count. |
| `batch_id` | string | No | Durable delivery batch identity. |
| `batch_ordinal` | integer | No |  |
| `binding_id` | string | Yes |  |
| `created_at` | integer | Yes | Creation time in Unix seconds. |
| `dedupe_key` | string | Yes | Server-owned durable deduplication identity. |
| `delivery_id` | string | Yes | Durable delivery receipt identity. |
| `external_message_id` | string | No |  |
| `last_error` | string | No | Most recent error; may be omitted. |
| `object_id` | string | Yes |  |
| `object_kind` | string | Yes |  |
| `part_digest` | string | Yes |  |
| `part_kind` | `PartKind` | Yes |  |
| `part_mime` | string | No |  |
| `part_ordinal` | integer | Yes |  |
| `payload_digest` | string | Yes |  |
| `payload_json` | string | Yes |  |
| `render_manifest_json` | string | Yes |  |
| `status` | `DeliveryReceiptStatus` | Yes | Current server-owned durable state; use the enclosing resource enum. |
| `target` | `DeliveryTarget` | Yes |  |
| `updated_at` | integer | Yes | Update time in Unix seconds. |


| Status | Error meaning |
| --- | --- |
| `400` | agent required or malformed JSON. |
| `404` | Owned delivery resource not found. |
| `409` | Illegal delivery state or query not available. Unclassified processing/storage errors also currently map to 409. |
| `501` | Delivery adapter not composed. |

[Implementation](../../apihttp/delivery_receipt_handler.go)


<a id="retrydeliveryreceipt"></a>
## POST /api/k12/delivery-receipts/{id}/retry

Retry delivery receipt.

Retries only eligible failed receipts. It does not blindly resend outcome_unknown; batches retry only eligible failed children.

Availability: K12 durable delivery adapter; query additionally needs provider reconciliation support.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `id` | path | Yes | string Server-issued resource identity. |

JSON body:

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. minLength=1 |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `agent_name` | string | Yes | Tutor owning this resource. |
| `attempt` | integer | Yes | Server-recorded attempt count. |
| `batch_id` | string | No | Durable delivery batch identity. |
| `batch_ordinal` | integer | No |  |
| `binding_id` | string | Yes |  |
| `created_at` | integer | Yes | Creation time in Unix seconds. |
| `dedupe_key` | string | Yes | Server-owned durable deduplication identity. |
| `delivery_id` | string | Yes | Durable delivery receipt identity. |
| `external_message_id` | string | No |  |
| `last_error` | string | No | Most recent error; may be omitted. |
| `object_id` | string | Yes |  |
| `object_kind` | string | Yes |  |
| `part_digest` | string | Yes |  |
| `part_kind` | `PartKind` | Yes |  |
| `part_mime` | string | No |  |
| `part_ordinal` | integer | Yes |  |
| `payload_digest` | string | Yes |  |
| `payload_json` | string | Yes |  |
| `render_manifest_json` | string | Yes |  |
| `status` | `DeliveryReceiptStatus` | Yes | Current server-owned durable state; use the enclosing resource enum. |
| `target` | `DeliveryTarget` | Yes |  |
| `updated_at` | integer | Yes | Update time in Unix seconds. |


| Status | Error meaning |
| --- | --- |
| `400` | agent required or malformed JSON. |
| `404` | Owned delivery resource not found. |
| `409` | Illegal delivery state or query not available. Unclassified processing/storage errors also currently map to 409. |
| `501` | Delivery adapter not composed. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/delivery_receipt_handler.go)


<a id="querydeliveryreceipt"></a>
## POST /api/k12/delivery-receipts/{id}/query

Query delivery receipt.

Reconciles provider delivery outcomes and persists state without resending. Unsupported provider querying returns 409.

Availability: K12 durable delivery adapter; query additionally needs provider reconciliation support.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `id` | path | Yes | string Server-issued resource identity. |

JSON body:

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. minLength=1 |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `agent_name` | string | Yes | Tutor owning this resource. |
| `attempt` | integer | Yes | Server-recorded attempt count. |
| `batch_id` | string | No | Durable delivery batch identity. |
| `batch_ordinal` | integer | No |  |
| `binding_id` | string | Yes |  |
| `created_at` | integer | Yes | Creation time in Unix seconds. |
| `dedupe_key` | string | Yes | Server-owned durable deduplication identity. |
| `delivery_id` | string | Yes | Durable delivery receipt identity. |
| `external_message_id` | string | No |  |
| `last_error` | string | No | Most recent error; may be omitted. |
| `object_id` | string | Yes |  |
| `object_kind` | string | Yes |  |
| `part_digest` | string | Yes |  |
| `part_kind` | `PartKind` | Yes |  |
| `part_mime` | string | No |  |
| `part_ordinal` | integer | Yes |  |
| `payload_digest` | string | Yes |  |
| `payload_json` | string | Yes |  |
| `render_manifest_json` | string | Yes |  |
| `status` | `DeliveryReceiptStatus` | Yes | Current server-owned durable state; use the enclosing resource enum. |
| `target` | `DeliveryTarget` | Yes |  |
| `updated_at` | integer | Yes | Update time in Unix seconds. |


| Status | Error meaning |
| --- | --- |
| `400` | agent required or malformed JSON. |
| `404` | Owned delivery resource not found. |
| `409` | Illegal delivery state or query not available. Unclassified processing/storage errors also currently map to 409. |
| `501` | Delivery adapter not composed. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/delivery_receipt_handler.go)


<a id="getdeliverybatch"></a>
## GET /api/k12/delivery-batches/{id}

Get delivery batch.

Reads durable state. sending/external_message_id is not proof of delivered.

Availability: K12 durable delivery adapter; query additionally needs provider reconciliation support.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `id` | path | Yes | string Server-issued resource identity. |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `agent_name` | string | Yes | Tutor owning this resource. |
| `batch_id` | string | Yes | Durable delivery batch identity. |
| `content_digest` | string | Yes | Digest of frozen delivery content. |
| `created_at` | integer | Yes | Creation time in Unix seconds. |
| `dedupe_key` | string | Yes | Server-owned durable deduplication identity. |
| `object_id` | string | Yes |  |
| `object_kind` | string | Yes |  |
| `receipts` | array<`DeliveryReceipt`> / null | Yes | All target/content receipts in batch order. |
| `status` | `DeliveryBatchStatus` | Yes | Current server-owned durable state; use the enclosing resource enum. |
| `updated_at` | integer | Yes | Update time in Unix seconds. |


| Status | Error meaning |
| --- | --- |
| `400` | agent required or malformed JSON. |
| `404` | Owned delivery resource not found. |
| `409` | Illegal delivery state or query not available. Unclassified processing/storage errors also currently map to 409. |
| `501` | Delivery adapter not composed. |

[Implementation](../../apihttp/delivery_receipt_handler.go)


<a id="retrydeliverybatch"></a>
## POST /api/k12/delivery-batches/{id}/retry

Retry delivery batch.

Retries only eligible failed receipts. It does not blindly resend outcome_unknown; batches retry only eligible failed children.

Availability: K12 durable delivery adapter; query additionally needs provider reconciliation support.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `id` | path | Yes | string Server-issued resource identity. |

JSON body (strict decoding):

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. minLength=1 |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `agent_name` | string | Yes | Tutor owning this resource. |
| `batch_id` | string | Yes | Durable delivery batch identity. |
| `content_digest` | string | Yes | Digest of frozen delivery content. |
| `created_at` | integer | Yes | Creation time in Unix seconds. |
| `dedupe_key` | string | Yes | Server-owned durable deduplication identity. |
| `object_id` | string | Yes |  |
| `object_kind` | string | Yes |  |
| `receipts` | array<`DeliveryReceipt`> / null | Yes | All target/content receipts in batch order. |
| `status` | `DeliveryBatchStatus` | Yes | Current server-owned durable state; use the enclosing resource enum. |
| `updated_at` | integer | Yes | Update time in Unix seconds. |


| Status | Error meaning |
| --- | --- |
| `400` | agent required or malformed JSON. |
| `404` | Owned delivery resource not found. |
| `409` | Illegal delivery state or query not available. Unclassified processing/storage errors also currently map to 409. |
| `501` | Delivery adapter not composed. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/delivery_receipt_handler.go)


<a id="querydeliverybatch"></a>
## POST /api/k12/delivery-batches/{id}/query

Query delivery batch.

Reconciles provider delivery outcomes and persists state without resending. Unsupported provider querying returns 409.

Availability: K12 durable delivery adapter; query additionally needs provider reconciliation support.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `id` | path | Yes | string Server-issued resource identity. |

JSON body (strict decoding):

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. minLength=1 |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `agent_name` | string | Yes | Tutor owning this resource. |
| `batch_id` | string | Yes | Durable delivery batch identity. |
| `content_digest` | string | Yes | Digest of frozen delivery content. |
| `created_at` | integer | Yes | Creation time in Unix seconds. |
| `dedupe_key` | string | Yes | Server-owned durable deduplication identity. |
| `object_id` | string | Yes |  |
| `object_kind` | string | Yes |  |
| `receipts` | array<`DeliveryReceipt`> / null | Yes | All target/content receipts in batch order. |
| `status` | `DeliveryBatchStatus` | Yes | Current server-owned durable state; use the enclosing resource enum. |
| `updated_at` | integer | Yes | Update time in Unix seconds. |


| Status | Error meaning |
| --- | --- |
| `400` | agent required or malformed JSON. |
| `404` | Owned delivery resource not found. |
| `409` | Illegal delivery state or query not available. Unclassified processing/storage errors also currently map to 409. |
| `501` | Delivery adapter not composed. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/delivery_receipt_handler.go)


<a id="addgrounding"></a>
## POST /api/k12/grounding

Add textbook grounding.

agent/title/content are required. subject is empty for legacy unscoped behavior or one of the six English subject keys. A nonempty subject requires a subject-aware writer; the server does not silently drop it. Title follows the current Title input limit.

Availability: K12 GroundingWriter; SubjectGroundingWriter when subject is nonempty.

JSON body:

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. minLength=1 |
| `subject` | `` / `math` / `chinese` / `english` / `science` / `information_technology` / `art` | No | default= |
| `title` | string | Yes | minLength=1, maxLength=200 |
| `content` | string | Yes | minLength=1 |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `ok` | `True` | Yes |  |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid JSON or input. |
| `404` | Scoped resource not found. |
| `409` | Version, command or state conflict. |
| `500` | Storage or other unclassified execution error. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/handler.go)


<a id="addaccumulation"></a>
## POST /api/k12/accumulation

Create an accumulation entry.

Body contains only content. The current metadata deriver determines subject, entry type and provenance; clients cannot override them. Replaying the same agent/key/content returns the same record with created=false; a different content under the same key conflicts. Metadata-generation failure is 502, with no guessed fallback.

Availability: K12 record store and AccumulationMetadataDeriver.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |
| `Idempotency-Key` | header | Yes | string Reuse the same key only for the same immutable command. |

JSON body (strict decoding):

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `content` | string | Yes | minLength=1 |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `record_id` | string | Yes | Server-issued record identity. |
| `created` | boolean | Yes | Whether a new record was created; replay/deduplication can be false. |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid input or term/subject. |
| `404` | Scoped record not found. |
| `409` | Model invocation needs reconciliation; do not blindly resend. |
| `500` | Unclassified execution/storage error. |
| `502` | Solver, grader or verification failed. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/handler.go)


<a id="listaccumulation"></a>
## GET /api/k12/accumulation

List accumulation entries.

Read-only. created_at uses Unix seconds. source can be omitted. dictation_generation projects durable generation progress.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |
| `subject` | query | No | string Optional subject filter, commonly 语文 or 英语. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `items` | array<`accumDTO`> | Yes | Server-owned list projection. |


| Status | Error meaning |
| --- | --- |
| `400` | agent required. |
| `500` | Accumulation list failed. |

[Implementation](../../apihttp/handler.go)


<a id="getaccumulation"></a>
## GET /api/k12/accumulation/{id}

Read an accumulation entry.



Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `id` | path | Yes | string Server-issued resource identity. |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `content` | string | Yes |  |
| `created_at` | integer | Yes | Creation time in Unix seconds. |
| `dictation_generation` | `accumulationDictationGenerationDTO` / null | No | Poll the same accumulation for durable dictation generation progress. |
| `entry_type` | string | Yes |  |
| `record_id` | string | Yes | Server-issued record identity. |
| `source` | string | No |  |
| `subject` | string | Yes |  |
| `version` | integer | Yes | Record/backup version according to its enclosing resource. |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid JSON or input. |
| `404` | Scoped resource not found. |
| `409` | Version, command or state conflict. |
| `500` | Storage or other unclassified execution error. |

[Implementation](../../apihttp/handler.go)


<a id="sendaccumulation"></a>
## POST /api/k12/accumulation/{id}/send

Send stored accumulation content.

Reads the content from the owned stored record and rejects unknown fields or substitute text. Sends to the complete direct-binding snapshot with frozen content and deduplication; inspect batch/receipt delivery status.

Availability: K12 durable delivery and active direct bindings.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `id` | path | Yes | string Server-issued resource identity. |

JSON body (strict decoding):

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. minLength=1 |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `agent_name` | string | Yes | Tutor owning this resource. |
| `batch_id` | string | Yes | Durable delivery batch identity. |
| `content_digest` | string | Yes | Digest of frozen delivery content. |
| `created_at` | integer | Yes | Creation time in Unix seconds. |
| `dedupe_key` | string | Yes | Server-owned durable deduplication identity. |
| `object_id` | string | Yes |  |
| `object_kind` | string | Yes |  |
| `receipts` | array<`DeliveryReceipt`> / null | Yes | All target/content receipts in batch order. |
| `status` | `DeliveryBatchStatus` | Yes | Current server-owned durable state; use the enclosing resource enum. |
| `updated_at` | integer | Yes | Update time in Unix seconds. |


| Status | Error meaning |
| --- | --- |
| `400` | agent required or invalid/unknown JSON. |
| `404` | Owned accumulation not found. |
| `409` | No active bindings or invalid delivery state. Unclassified processing/storage errors also currently map to 409. |
| `501` | Delivery unavailable. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/handler.go)


<a id="accumdictationtobasket"></a>
## POST /api/k12/accumulation/{id}/dictation-to-basket

Accept accumulation dictation generation.

full_dictation defaults to false (fill in blanks); true explicitly requests full dictation. The server uses dictation:{id} as the command identity. 202 means durable acceptance only. Poll accumulation details: practice_item_id appears when committed, not while queued/generating/validating.

Availability: K12 records and PracticeGeneration coordinator.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `id` | path | Yes | string Server-issued resource identity. |

JSON body (strict decoding):

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. minLength=1 |
| `source_session` | string | No | Optional originating session reference. |
| `full_dictation` | boolean | No | default=False |

Success: `202` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `dictation_generation` | `accumulationDictationGenerationDTO` | Yes | Poll the same accumulation for durable dictation generation progress. |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid request or unsupported accumulation. |
| `404` | Owned accumulation not found. |
| `409` | Generation/state conflict or invocation requires reconciliation. |
| `500` | Unclassified generation/storage error. |
| `502` | Generation dependency failed. |
| `503` | Practice-generation coordinator unavailable. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/handler.go)


<a id="deleteaccumulation"></a>
## DELETE /api/k12/accumulation/{id}

Delete an accumulation entry.

Durable tombstone deletion requires a positive If-Match and Idempotency-Key. The same command can replay; stale versions or key/content conflicts return 409. Do not replace the key when the command outcome is unknown.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `id` | path | Yes | string Server-issued resource identity. |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |
| `If-Match` | header | Yes | string Current positive row version; bare, quoted and weak ETag forms are accepted. |
| `Idempotency-Key` | header | Yes | string Reuse the same key only for the same immutable command. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `accumulation_id` | string | Yes |  |
| `deleted` | boolean | Yes |  |
| `version` | integer | Yes | Record/backup version according to its enclosing resource. |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid JSON or input. |
| `404` | Scoped resource not found. |
| `409` | Version, command or state conflict. |
| `500` | Storage or other unclassified execution error. |

[Implementation](../../apihttp/handler.go)


<a id="backup"></a>
## GET /api/k12/backup

New work feedback calls freeze the complete request and consumed method context before sending. Recovery and safe retries use that snapshot; successful results are read-only and unknown calls are not resent. The optional archive string `current_creative_works[].generations[].request_snapshot_json` contains the request, method prompt and digest. Preserve it verbatim instead of rebuilding it from the current Skill directory. Missing legacy snapshots remain missing. Public work generation DTOs do not expose raw prompts; a `feedback_skill` stamp alone does not prove complete context freezing.

v8 adds `teaching_diagrams` with owner, asset identity, SHA-256 and exact SVG bytes. The archive checksum covers this field; asset and record restoration share one transaction. RestoreAs rewrites target asset references. Legacy archives retain their original checksum contract.

Read a complete learner backup.

v9 adds `review_sources`: reviewed relationships, their committed source snapshots and required page metadata. Original image bytes remain in the single `assets` manifest. Restoration shares the record transaction and does not recreate model calls or delivery tasks. Readers recheck the current question, page digest and attempt identity; an existing grading ledger always takes precedence over archived sources. Invalid relationships cannot become valid through re-export/restoration. Cross-Tutor migration rewrites asset ownership and group identity; rollback restores the original relationship set. Earlier checksum formats remain supported.

`snapshots[].source` and `pages[]` retain the frozen projection's PascalCase field names; clients must transfer them unchanged. `active` records the exported source state, not confidence or an exemption from current identity checks. Relationships that no longer match the original question remain invalid during cross-Tutor migration.

Returns JSON, not a download attachment; save the whole response as .hexbak. Current version is 9. Preserve archive_id/checksum/records/assets/confirmed OCR/problem attempts/source-action/current creative-work archives unchanged. It does not include the complete model configuration or all long-term memory. Missing/corrupt referenced assets fail the backup; do not drop fields and recompute a checksum.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `agent_name` | string | Yes | Tutor owning this resource. |
| `archive_id` | string | No | Immutable archive identity. |
| `assets` | array<`HexbakAsset`> / null | No |  |
| `checksum` | string | Yes | Versioned archive checksum; preserve unchanged. |
| `creative_work_ocr` | array<`CreativeWorkOCRArchiveEvidence`> / null | No |  |
| `current_creative_works` | array<`CreativeWorkArchiveV7`> / null | No |  |
| `exported_at` | integer | Yes | Backup time in Unix seconds. |
| `problem_attempts` | array<`ProblemAttemptSnapshot`> / null | No |  |
| `problem_source` | `ProblemSourceArchiveV6` / null | No |  |
| `profile` | `ChildProfile` / null | No |  |
| `records` | array<`AgentRecord` / null> / null | Yes |  |
| `version` | integer | Yes | Current backup version is 7; restoration supports versioned checksum contracts through 7. Preserve this complete envelope unchanged. |


| Status | Error meaning |
| --- | --- |
| `400` | agent required. |
| `500` | Backup, referenced asset bytes or checksum packing failed. |

[Implementation](../../apihttp/handler.go)


<a id="restore"></a>
## POST /api/k12/restore

Merge a backup into the same Tutor.

Body is the full backup, up to 128 MiB. Validates the versioned checksum and assets, uses imported rows on record-ID collisions, retains current rows absent from the archive and restores the archived profile exactly. snapshot is a best-effort pre-restore backup and can be null; rollback is not guaranteed. Newer unsupported versions return 409; checksum mismatch returns 400.

Availability: K12 module, record store and the dependencies described for this operation.

JSON body:

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent_name` | string | Yes | Tutor owning this resource. |
| `archive_id` | string | No | Immutable archive identity. |
| `assets` | array<`HexbakAsset`> / null | No |  |
| `checksum` | string | Yes | Versioned archive checksum; preserve unchanged. |
| `creative_work_ocr` | array<`CreativeWorkOCRArchiveEvidence`> / null | No |  |
| `current_creative_works` | array<`CreativeWorkArchiveV7`> / null | No |  |
| `exported_at` | integer | Yes | Backup time in Unix seconds. |
| `problem_attempts` | array<`ProblemAttemptSnapshot`> / null | No |  |
| `problem_source` | `ProblemSourceArchiveV6` / null | No |  |
| `profile` | `ChildProfile` / null | No |  |
| `records` | array<`AgentRecord` / null> / null | Yes |  |
| `version` | integer | Yes | Current backup version is 7; restoration supports versioned checksum contracts through 7. Preserve this complete envelope unchanged. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `restored` | integer | Yes | Number of restored records. |
| `snapshot` | `Hexbak` / null | Yes | Pre-restore snapshot; ordinary restore can return null. |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid JSON, checksum, asset manifest or archive fields. |
| `409` | Backup version newer than supported, data/invocation state conflict. |
| `413` | Body exceeds 128 MiB. |
| `500` | Restore/storage failure. |

[Implementation](../../apihttp/handler.go)


<a id="restoreas"></a>
## POST /api/k12/restore-as

Migrate a backup to another Tutor.

Keep original archive identity/checksum unchanged. source_agent must match the original owner; target_agent must differ and identify the rebuilt Tutor. guardian_confirmed=true and a nonblank idempotency_key of at most 200 UTF-8 bytes after trimming are required. Accepts supported backups from v2 through the current v9. Original archive, target pre-restore snapshot, owner-rewrite journal and domain writes commit in one SQLite transaction. The result contains migration_id; identical commands replay durable receipts.

Availability: K12 module, record store and the dependencies described for this operation.

JSON body:

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `archive` | `Hexbak` | Yes | Complete non-null archive; null returns400. |
| `guardian_confirmed` | `True` | Yes |  |
| `idempotency_key` | string | Yes | Nonblank after trimming; at most 200 UTF-8 bytes. |
| `source_agent` | string | Yes |  |
| `target_agent` | string | Yes |  |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `idempotent` | boolean | Yes |  |
| `journal_entries` | integer | Yes |  |
| `migrated_checksum` | string | No |  |
| `migration_id` | string | Yes |  |
| `original_archive_digest` | string | No |  |
| `original_archive_preserved` | boolean | Yes |  |
| `restored` | integer | Yes | Number of restored records. |
| `snapshot` | `Hexbak` / null | No | Pre-restore snapshot; ordinary restore can return null. |
| `snapshot_digest` | string | No |  |
| `source_agent` | string | No |  |
| `status` | string | Yes | Current server-owned durable state; use the enclosing resource enum. |
| `target_agent` | string | Yes |  |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid archive, scope, guardian confirmation or command fields. |
| `404` | Required scoped object not found. |
| `409` | Version/state/idempotency conflict. |
| `413` | Body exceeds 128 MiB. |
| `500` | Atomic migration/storage failed. |

[Implementation](../../apihttp/handler.go)


<a id="rollbackrestoreas"></a>
## POST /api/k12/restore-as/{migration_id}/rollback

Roll back a cross-Tutor migration.

Body requires target_agent and guardian_confirmed=true; migration_id is path-owned (any body value is overwritten). Limit: 1 MiB. Restores the immutable pre-migration snapshot exactly and appends a journal fact. Repeated calls return the same rolled_back receipt.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `migration_id` | path | Yes | string Server-issued resource identity. |

JSON body:

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `guardian_confirmed` | `True` | Yes |  |
| `migration_id` | string | No | Compatibility body field, ignored because the path migration_id always replaces it. |
| `target_agent` | string | Yes |  |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `idempotent` | boolean | Yes |  |
| `journal_entries` | integer | Yes |  |
| `migrated_checksum` | string | No |  |
| `migration_id` | string | Yes |  |
| `original_archive_digest` | string | No |  |
| `original_archive_preserved` | boolean | Yes |  |
| `restored` | integer | Yes | Number of restored records. |
| `snapshot` | `Hexbak` / null | No | Pre-restore snapshot; ordinary restore can return null. |
| `snapshot_digest` | string | No |  |
| `source_agent` | string | No |  |
| `status` | string | Yes | Current server-owned durable state; use the enclosing resource enum. |
| `target_agent` | string | Yes |  |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid/absent guardian confirmation or target. |
| `404` | Migration or target not found. |
| `409` | Migration/state conflict. |
| `413` | Body exceeds 1 MiB. |
| `500` | Rollback/storage failed. |

[Implementation](../../apihttp/handler.go)


<a id="export"></a>
## GET /api/k12/export

Export the current-term learning archive.

Omitted/md returns Markdown JSON; a missing Renderer does the same. Successful PDF/DOCX rendering returns a binary attachment with X-HexClaw-Artifact-ID/Source-Digest/Object-Counts. Render failure still returns 200, but as Markdown JSON from the same canonical snapshot with render_error. Inspect Content-Type/content before saving a PDF. All five object counts share scope/as_of/source_digest.

Availability: K12 records; optional PDF/DOCX Renderer.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |
| `format` | query | No | `md` / `pdf` / `docx`  |

Success: `200` `application/json` or PDF/DOCX binary attachment.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `format` | `markdown` | Yes |  |
| `content` | string | Yes |  |
| `schema_version` | `v1` | Yes |  |
| `scope` | `LearningArchiveScope` | Yes | Frozen Tutor/term scope of this artifact. |
| `as_of` | integer | Yes | Frozen snapshot time in Unix seconds. |
| `source_digest` | string | Yes | Digest of the same source snapshot. |
| `object_counts` | `LearningArchiveObjectCounts` | Yes | Counts for the five object groups in the same artifact. |
| `artifact_id` | string | Yes | Frozen export artifact identity. |
| `render_error` | string | No | Reason for JSON fallback after PDF/DOCX rendering failed. |
| `attachments` | array<`LearningArchiveAttachment`> | No |  |


| Status | Error meaning |
| --- | --- |
| `400` | agent required. |
| `500` | Canonical archive construction failed. |

[Implementation](../../apihttp/handler.go)


<a id="mistakesheet"></a>
## GET /api/k12/mistake-sheet

Build the due mistake sheet.

Read-only Markdown JSON {format:"markdown",content}; not a PDF/Word attachment.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `format` | `markdown` | Yes |  |
| `content` | string | Yes |  |


| Status | Error meaning |
| --- | --- |
| `400` | agent required. |
| `500` | Sheet generation failed. |

[Implementation](../../apihttp/handler.go)


<a id="getprofile"></a>
## GET /api/k12/profile

Read the learner profile and revision.

The current response has child_name/grade_term/textbook_edition/revision only. textbook_edition is the compatibility math projection, not a full six-subject map. Use profile-bundle for complete updates.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `child_name` | string | Yes | Learner display name. |
| `grade_term` | string | Yes | Learner profile grade-term. |
| `revision` | integer | Yes | Current CAS lifecycle revision. |
| `textbook_edition` | string | Yes | Textbook edition; the compatibility math field in profile responses. |


| Status | Error meaning |
| --- | --- |
| `400` | agent required. |
| `404` | Profile read failed/not found. |

[Implementation](../../apihttp/handler.go)


<a id="updateprofile"></a>
## PUT /api/k12/profile

Disabled legacy profile update.

Registered but always returns 405 without reading the body; there is no successful update contract. Use PUT /api/k12/profile-bundle.

Availability: K12 module, record store and the dependencies described for this operation.

Deprecated: every call returns 405 with Allow: GET.

| Status | Error meaning |
| --- | --- |
| `405` | Always returns {"error":"profile updates require /api/k12/profile-bundle"}, with Allow: GET. |

[Implementation](../../apihttp/handler.go)


<a id="listtextbookbindingoptions"></a>
## GET /api/k12/textbook-binding-options

Read textbook manifest candidates.

mode=create reads owner-scoped math candidates without creating an Agent or binding. Existing-scope mode requires agent/subject, validates scope and may reconcile candidate/binding state. catalog is the real manifest catalog or null; fields report indexing failures/retryability. See the canonical textbook contract.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `mode` | query | No | `create` create selects the owner-scoped pre-creation preview; omitted uses the existing Tutor scope. |
| `agent` | query | No | string Required when mode is omitted; unnecessary for mode=create. |
| `subject` | query | No | `math` / `chinese` / `english` / `science` / `information_technology` / `art` Required in existing-scope mode. mode=create fixes the subject to math. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `items` | array<`TextbookBindingOption`> | Yes | Server-owned list projection. |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid owner/agent/subject. |
| `404` | Agent scope unavailable. |
| `500` | Records/principal/candidate read failed. |

[Implementation](../../apihttp/weekly_practice_handler.go)


<a id="getcurriculumcatalog"></a>
## GET /api/k12/curriculum-catalog

Read the authoritative math catalog.

Prefers the active textbook binding. Fallback lookup requires textbook_edition/volume and a valid profile. An unavailable catalog is 502; a missing catalog is 404. It does not invent terms or lessons from filenames.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |
| `subject` | query | Yes | `math`  |
| `textbook_edition` | query | No | string Required only for the fallback catalog source when no active manifest binding exists. |
| `volume` | query | No | string Required only for fallback catalog lookup. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. |
| `grade_term` | string | No | Learner profile grade-term. |
| `page_max` | integer | Yes |  |
| `page_min` | integer | Yes |  |
| `subject` | string | Yes |  |
| `textbook_binding_id` | string | Yes |  |
| `textbook_edition` | string | Yes | Textbook edition; the compatibility math field in profile responses. |
| `textbook_manifest_id` | string | No |  |
| `textbook_version` | string | Yes |  |
| `title` | string | Yes |  |
| `units` | array<`CurriculumCatalogUnit`> / null | Yes |  |
| `volume` | string | Yes |  |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid scope/subject or missing fallback fields. |
| `404` | Owned profile/catalog not found. |
| `500` | Unclassified scope/store failure. |
| `502` | Authoritative catalog unavailable. |

[Implementation](../../apihttp/weekly_practice_handler.go)


<a id="getcurriculumprogress"></a>
## GET /api/k12/curriculum-progress

Read curriculum progress or preview an estimate.

Stored mode returns progress:null or an object; outer revision still identifies lifecycle state when null. estimate is read-only and does not adopt or bind progress. Any manifest/lesson/page input builds an explicit selection; pages must be integers. A missing match can produce null; an estimate is not evidence of actual learning.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `mode` | query | No | `estimate` Omitted reads the stored state; estimate previews without adopting it. |
| `agent` | query | No | string Required for stored-state mode; optional for pre-creation estimates. |
| `subject` | query | No | `math` Stored reads require math; estimate accepts omitted or math. |
| `grade_term` | query | No | string Estimate input; saved profile fills empty values when agent is present. |
| `textbook_edition` | query | No | string Estimate input; saved profile fills empty values when agent is present. |
| `textbook_manifest_id` | query | No | string  |
| `unit_id` | query | No | string  |
| `lesson_id` | query | No | string  |
| `page_from` | query | No | integer  |
| `page_to` | query | No | integer  |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `progress` | `CurriculumProgress` / null | Yes |  |
| `revision` | integer | Yes | Current CAS lifecycle revision. minimum=0 |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid subject, term, page integers or selected catalog. |
| `404` | Agent/profile scope not found. |
| `500` | Unclassified principal/state failure. |
| `502` | Catalog dependency unavailable. |

[Implementation](../../apihttp/weekly_practice_handler.go)


<a id="updateprofilebundle"></a>
## PUT /api/k12/profile-bundle

Atomically update profile, progress and weekly settings.

Strict JSON. Requires a complete six-subject profile, nonempty learner name, an empty or primary-school term, valid timezone and 1..5 arithmetic minutes. Always send all three freshly read expected_*_revision values. Optional agent_config is a complete configuration; provider/model must both be empty or both nonempty. Omitted/null/object curriculum_progress have distinct semantics. A failed transaction leaves no partial profile/binding writes. Same-key commands replay with replayed=true. The canonical API chapters own the ten textbook rules and complete curl example.

Availability: K12 module, record store and the dependencies described for this operation.

JSON body (strict decoding):

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. |
| `agent_config` | object / null | No |  |
| `curriculum_progress` | `ProgressSelection` / null | No | Omitted: automatic resolution preserving parent-confirmed progress. Explicit null: clear this time. Object: explicit shared selection. These states are not interchangeable. |
| `expected_profile_revision` | integer | No | minimum=0 |
| `expected_progress_revision` | integer | No | minimum=0 |
| `expected_settings_revision` | integer | No | minimum=0 |
| `idempotency_key` | string | Yes | minLength=1 |
| `profile` | object | Yes |  |
| `profile.child_name` | string | Yes | Learner display name. minLength=1 |
| `profile.grade_term` | `` / `一年级上` / `一年级下` / `二年级上` / `二年级下` / `三年级上` / `三年级下` / `四年级上` / `四年级下` / `五年级上` / `五年级下` / `六年级上` / `六年级下` | No | Empty is accepted; nonempty terms are restricted to primary school. |
| `profile.subject_textbooks` | `SubjectTextbooksInput` | Yes |  |
| `weekly_practice_settings` | object | Yes |  |
| `weekly_practice_settings.arithmetic_minutes` | integer | Yes | minimum=1, maximum=5 |
| `weekly_practice_settings.arithmetic_warmup_enabled` | boolean | No |  |
| `weekly_practice_settings.textbook_consolidation_enabled` | boolean | No |  |
| `weekly_practice_settings.textbook_consolidation_tier` | `less` / `standard` / `more` | No | default=standard |
| `weekly_practice_settings.timezone` | string | Yes | Valid IANA timezone, for example Asia/Shanghai. minLength=1 |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `agent_config` | `ProfileBundleAgentConfig` / null | No |  |
| `curriculum_progress` | `CurriculumProgress` / null | Yes |  |
| `profile` | `ProfileBundleProfile` | Yes |  |
| `replayed` | boolean | Yes | Whether the durable command was replayed. |
| `weekly_practice_settings` | `WeeklyPracticeSettings` | Yes |  |


| Status | Error meaning |
| --- | --- |
| `400` | Invalid/unknown JSON, incomplete profile/settings or selected progress. |
| `404` | Owned Agent/profile/catalog not found. |
| `409` | Revision or idempotency conflict. |
| `500` | Transaction/publication failure. |
| `502` | Curriculum source unavailable. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/weekly_practice_handler.go)


<a id="coldstart"></a>
## POST /api/k12/cold-start

Preview or confirm a cold-start profile.

confirm defaults to false: infer without writing. Explicit true adopts the suggestion without overwriting an existing valid profile. fallback_grade is empty or a primary-school term; a secondary-school inference is ignored, whereas an explicit secondary fallback returns 400. An omitted textbook remains empty; no edition is assumed.

Availability: K12 optional curriculum constraints; profile writer required only with confirm=true.

JSON body:

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. |
| `child_name` | string | No | Learner display name. |
| `confirm` | boolean | No | default=False |
| `fallback_grade` | `` / `一年级上` / `一年级下` / `二年级上` / `二年级下` / `三年级上` / `三年级下` / `四年级上` / `四年级下` / `五年级上` / `五年级下` / `六年级上` / `六年级下` | No |  |
| `knowledge_points` | array<string> / null | No | Knowledge-point names. |
| `textbook_edition` | string | No | Textbook edition; the compatibility math field in profile responses. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `child_name` | string | Yes | Learner display name. |
| `created` | boolean | Yes | Whether a new record was created; replay/deduplication can be false. |
| `grade_term` | string | Yes | Learner profile grade-term. |
| `inferred` | boolean | Yes |  |
| `textbook_edition` | string | Yes | Textbook edition; the compatibility math field in profile responses. |


| Status | Error meaning |
| --- | --- |
| `400` | agent required or invalid explicit fallback. |
| `500` | Profile persistence/execution failed. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/handler.go)


<a id="tutorturn"></a>
## POST /api/k12/tutor-turn

Generate parent tutoring guidance.

Provide agent to select the learner scope; this handler accepts omission, which supplies no saved learner grade and cannot advance that learner's mistake state. An empty problem produces guidance/emotional support only. stage is currently 3. Emotional support does not block solving. When a problem and Solver are present, it is solved/verified; badge appears only with a solution, and a matching new mistake can advance to explained. Omitted grade is filled from the stored profile. prior_stage is a historical compatibility input, not a required stepped confirmation.

Availability: K12 tutor/model adapters.

JSON body:

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | No | Provide the real Tutor name for saved profile lookup and matching mistake-state updates; omission is accepted by this handler. |
| `grade` | string | No |  |
| `parent_message` | string | No |  |
| `prior_stage` | integer | No | Historical compatibility input; not a required user confirmation step. |
| `problem` | string | No | Problem text. |
| `student_answer` | string | No | Learner answer, not a canonical solution. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `badge` | string | No | Verification badge value, such as verified-strong. |
| `comfort` | boolean | Yes |  |
| `emotion_cue` | string | No |  |
| `escalated` | boolean | Yes |  |
| `prompt_hint` | string | Yes |  |
| `solution` | string | No | Complete solution text. |
| `stage` | integer | Yes |  |


| Status | Error meaning |
| --- | --- |
| `400` | Malformed JSON. |
| `500` | Tutoring or model execution failed. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/handler.go)


<a id="cronmistakesheet"></a>
## GET /api/k12/cron/mistake-sheet

Weekly due-mistake sheet.

Returns an empty body when no mistakes are due; produces content without sending IM messages. Success is always UTF-8 text/plain 200; empty means skip. The outer Cron/Deliverer owns actual delivery.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

Success: `200` UTF-8 `text/plain`.

| Status | Error meaning |
| --- | --- |
| `400` | agent required. |
| `500` | Content generation/read failed. |

[Implementation](../../apihttp/handler.go)


<a id="cronfillbasket"></a>
## POST /api/k12/cron/fill-basket

Legacy no-op fill-basket endpoint.

Retained compatibility endpoint; always returns added=0/skipped=0 without adding items. Current practice-set insertion requires an explicit parent action per problem.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `added` | `0` | Yes |  |
| `skipped` | `0` | Yes |  |


| Status | Error meaning |
| --- | --- |
| `400` | agent required. |

[Implementation](../../apihttp/handler.go)


<a id="crondailyreminder"></a>
## GET /api/k12/cron/daily-reminder

Daily review reminder.

Empty when no review is pending. Available endpoint, but not one of the four current default jobs. Success is always UTF-8 text/plain 200; empty means skip. The outer Cron/Deliverer owns actual delivery.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

Success: `200` UTF-8 `text/plain`.

| Status | Error meaning |
| --- | --- |
| `400` | agent required. |
| `500` | Content generation/read failed. |

[Implementation](../../apihttp/handler.go)


<a id="cronreturnreminder"></a>
## GET /api/k12/cron/return-reminder

Practice-return reminder.

Scans papers finalized yesterday that have no return, at most once per paper. Reading can persist a reminder fact, so do not treat it as side-effect-free polling. Empty when nothing is due. Success is always UTF-8 text/plain 200; empty means skip. The outer Cron/Deliverer owns actual delivery.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

Success: `200` UTF-8 `text/plain`.

| Status | Error meaning |
| --- | --- |
| `400` | agent required. |
| `500` | Content generation/read failed. |

[Implementation](../../apihttp/handler.go)


<a id="cronmonthlyreport"></a>
## GET /api/k12/cron/monthly-report

Monthly insight report.

Empty when no records exist. Retained endpoint, absent from the four current default jobs. Success is always UTF-8 text/plain 200; empty means skip. The outer Cron/Deliverer owns actual delivery.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

Success: `200` UTF-8 `text/plain`.

| Status | Error meaning |
| --- | --- |
| `400` | agent required. |
| `500` | Content generation/read failed. |

[Implementation](../../apihttp/handler.go)


<a id="cronsemestercheck"></a>
## GET /api/k12/cron/semester-check

Term-transition reminder.

Empty when no usable profile/term exists or the last supported term is reached. Produces a confirmation reminder; it does not advance the term. Success is always UTF-8 text/plain 200; empty means skip. The outer Cron/Deliverer owns actual delivery.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

Success: `200` UTF-8 `text/plain`.

| Status | Error meaning |
| --- | --- |
| `400` | agent required. |
| `500` | Content generation/read failed. |

[Implementation](../../apihttp/handler.go)


<a id="cronyeararchive"></a>
## GET /api/k12/cron/year-archive

Year-end archive suggestion.

Empty when no records exist. Suggests archiving without performing it. Retained endpoint, not a current default job. Success is always UTF-8 text/plain 200; empty means skip. The outer Cron/Deliverer owns actual delivery.

Availability: K12 module, record store and the dependencies described for this operation.

| Parameter | Location | Required | Type / constraint |
| --- | --- | --- | --- |
| `agent` | query | Yes | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

Success: `200` UTF-8 `text/plain`.

| Status | Error meaning |
| --- | --- |
| `400` | agent required. |
| `500` | Content generation/read failed. |

[Implementation](../../apihttp/handler.go)


<a id="cronprovision"></a>
## POST /api/k12/cron/provision

Replace the four default automation jobs.

Explicit cutover replaces defaults and reclaims stale jobs identified by the Agent stable source key; it does not delete user-created jobs by display name. Four defaults: Friday 19:00 sheet, daily 20:00 return reminder, March 1 and September 1 at 09:00 term reminders. Durable jobs and active map commit atomically. 200 means registration, not content/delivery success.

Availability: K12 CronRegistrar and authorized Agent owner scope.

JSON body:

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. |
| `base_url` | string | No | Used only when the composed Runtime.BaseURL is empty; required in that case. |
| `chat_id` | string | No |  |
| `deliver` | array<string> / null | No |  |
| `platform` | string | No |  |
| `user_id` | string | No | Optional compatibility claim: if nonempty, it must equal the service-owned principal; it cannot assign quota ownership. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `provisioned` | array<`provisionedJob`> | Yes |  |
| `reclaimed` | array<`ReclaimedCronJob`> | Yes |  |


| Status | Error meaning |
| --- | --- |
| `400` | agent/base_url required or claimed user_id differs from owner. |
| `404` | Agent scope not found. |
| `500` | Atomic provision/reclaim or registrar result failed. |
| `501` | Cron registrar not composed. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/handler.go)


<a id="cronreconciledefaults"></a>
## POST /api/k12/cron/reconcile-defaults

Fill only missing default jobs.

Profile-lifecycle repair fills only missing exact source keys and preserves existing paused/custom timezone/target/script jobs. Exact canonical legacy jobs without a source key are treated as existing without rewriting them. Partial failure can retain earlier successful creations; retry fills the remaining keys. An omitted created field means false.

Availability: K12 CronRegistrar and authorized Agent owner scope.

JSON body:

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. |
| `base_url` | string | No | Used only when the composed Runtime.BaseURL is empty; required in that case. |
| `chat_id` | string | No |  |
| `deliver` | array<string> / null | No |  |
| `platform` | string | No |  |
| `user_id` | string | No | Optional compatibility claim: if nonempty, it must equal the service-owned principal; it cannot assign quota ownership. |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `provisioned` | array<`provisionedJob`> | Yes |  |


| Status | Error meaning |
| --- | --- |
| `400` | agent/base_url required or claimed user_id differs from owner. |
| `404` | Agent scope not found. |
| `500` | A missing default could not be created; previous successful creations remain. |
| `501` | Cron registrar not composed. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/handler.go)


<a id="bindim"></a>
## POST /api/k12/bind-im

Bind a Tutor to an IM direct conversation.

agent/platform/chat_id are required; instance_id is optional. Omitted/empty/1/direct conversation_type denotes a direct conversation; groups/other values return 400. Binding changes future inbound routing. One physical direct conversation cannot serve two learner Tutors simultaneously (409). Success sends no test message and does not prove later homework/delivery completion.

Availability: K12 IMBinder and configured platform instance.

JSON body:

| Field | Type | Required | Constraint |
| --- | --- | --- | --- |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. |
| `chat_id` | string | Yes |  |
| `conversation_type` | `` / `1` / `direct` | No | default=direct |
| `instance_id` | string | No |  |
| `platform` | string | Yes |  |

Success: `200` `application/json`.

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `bound` | `True` | Yes |  |
| `agent` | string | Yes | Tutor scope; does not change authenticated owner. |
| `platform` | string | Yes |  |
| `chat_id` | string | Yes |  |


| Status | Error meaning |
| --- | --- |
| `400` | Missing fields or non-direct conversation. |
| `409` | Binding belongs to a different Tutor. |
| `500` | Binding failed. |
| `501` | IM binder not composed. |
| `413` | Body limit exceeded. |

[Implementation](../../apihttp/handler.go)

## Nested fields

The full versioned backup graph is defined in OpenAPI; preserve exported archives without reconstructing them from a subset. This section expands the nested fields needed for ordinary calls.

### TutoringTipsSection

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `title` | string | Yes | Section title. |
| `content` | string | Yes | Section Markdown text. |
| `source_label` | string | Yes | Service-provided source label. |

A successful tutoring-tips response contains exactly three sections from the same confirmed homework facts.

### accumulationDictationGenerationDTO

| Field | Type | Present | Meaning |
| --- | --- | --- | --- |
| `generation_id` | string | Yes | Durable generation identity. |
| `status` | string | Yes | queued / generating / validating / committed / failed / re_add. |
| `practice_item_id` | string | No | Appears only once the item is committed. |
| `failure_reason` | string | No | Failure explanation when nonempty. |
| `attempt` | integer | No | Attempt count; omitted when zero. |
| `updated_at` | integer | No | Unix seconds; omitted when zero. |

### Delivery status values

- `DeliveryReceiptStatus`: pending / sending / delivered / failed / outcome_unknown.
- `DeliveryBatchStatus`: pending / sending / delivered / failed / partial_failed / outcome_unknown.

### SubjectTextbooksInput

| Field | Type | Required / present | Meaning |
| --- | --- | --- | --- |
| `math` | string | Yes | minLength=1 |
| `chinese` | string | Yes | minLength=1 |
| `english` | string | Yes | minLength=1 |
| `science` | string | Yes | minLength=1 |
| `information_technology` | string | Yes | minLength=1 |
| `art` | string | Yes | minLength=1 |

### AgentConfigInput

| Field | Type | Required / present | Meaning |
| --- | --- | --- | --- |
| `display_name` | string | Yes | Required nonblank after trimming. |
| `description` | string | Yes | Required nonblank after trimming; part of the complete agent_config. |
| `model` | string | No | provider and model must either both be empty or both be nonempty. |
| `provider` | string | No | provider and model must either both be empty or both be nonempty. |
| `skills` | array<string> / null | No | The service trims/deduplicates entries and adds the required K12 skills; omission does not remove mandatory skills. |
| `system_prompt` | string | Yes | Required nonblank after trimming. |

### ProgressSelection

| Field | Type | Required / present | Meaning |
| --- | --- | --- | --- |
| `subject` | `math` | Yes |  |
| `textbook_manifest_id` | string | Yes | minLength=1 |
| `volume` | string | No |  |
| `unit_id` | string | No |  |
| `lesson_id` | string | No |  |
| `page_from` | integer / null | No |  |
| `page_to` | integer / null | No |  |
| `evidence_source` | `parent_confirmed` / `ai_estimated` | Yes |  |

### DeliveryTarget

| Field | Type | Required / present | Meaning |
| --- | --- | --- | --- |
| `chat_id` | string | Yes |  |
| `instance_id` | string | No |  |
| `label` | string | No |  |
| `platform` | string | Yes |  |

### DeliveryReceipt

| Field | Type | Required / present | Meaning |
| --- | --- | --- | --- |
| `agent_name` | string | Yes | Tutor owning this resource. |
| `attempt` | integer | Yes | Server-recorded attempt count. |
| `batch_id` | string | No | Durable delivery batch identity. |
| `batch_ordinal` | integer | No |  |
| `binding_id` | string | Yes |  |
| `created_at` | integer | Yes | Creation time in Unix seconds. |
| `dedupe_key` | string | Yes | Server-owned durable deduplication identity. |
| `delivery_id` | string | Yes | Durable delivery receipt identity. |
| `external_message_id` | string | No |  |
| `last_error` | string | No | Most recent error; may be omitted. |
| `object_id` | string | Yes |  |
| `object_kind` | string | Yes |  |
| `part_digest` | string | Yes |  |
| `part_kind` | `PartKind` | Yes |  |
| `part_mime` | string | No |  |
| `part_ordinal` | integer | Yes |  |
| `payload_digest` | string | Yes |  |
| `payload_json` | string | Yes |  |
| `render_manifest_json` | string | Yes |  |
| `status` | `DeliveryReceiptStatus` | Yes | Current server-owned durable state; use the enclosing resource enum. |
| `target` | `DeliveryTarget` | Yes |  |
| `updated_at` | integer | Yes | Update time in Unix seconds. |

### CatalogUnit

| Field | Type | Required / present | Meaning |
| --- | --- | --- | --- |
| `lessons` | array<`CurriculumCatalogLesson`> / null | Yes |  |
| `page_from` | integer | Yes |  |
| `page_to` | integer | Yes |  |
| `title` | string | Yes |  |
| `unit_id` | string | Yes |  |

### CatalogLesson

| Field | Type | Required / present | Meaning |
| --- | --- | --- | --- |
| `lesson_id` | string | Yes |  |
| `page_from` | integer | Yes |  |
| `page_to` | integer | Yes |  |
| `title` | string | Yes |  |

### ManifestCatalog

| Field | Type | Required / present | Meaning |
| --- | --- | --- | --- |
| `grade_term` | string | No | Learner profile grade-term. |
| `page_max` | integer | Yes |  |
| `page_min` | integer | Yes |  |
| `page_refs` | array<`textbookCatalogPageRef`> / null | Yes |  |
| `subject` | string | Yes |  |
| `textbook_edition` | string | Yes | Textbook edition; the compatibility math field in profile responses. |
| `textbook_version` | string | Yes |  |
| `title` | string | Yes |  |
| `units` | array<`textbookCatalogUnit`> / null | Yes |  |
| `volume` | string | Yes |  |

### CatalogPageReference

| Field | Type | Required / present | Meaning |
| --- | --- | --- | --- |
| `logical_page` | integer | Yes |  |
| `pdf_page` | integer | Yes |  |
| `segment_refs` | array<string> / null | Yes |  |

### EstimateBasis

| Field | Type | Required / present | Meaning |
| --- | --- | --- | --- |
| `as_of_date` | string | Yes |  |
| `evidence_receipt_hash` | string | No |  |
| `reference_term_end` | string | Yes |  |
| `reference_term_start` | string | Yes |  |
| `weight_method` | string | Yes |  |

### ArchiveCounts

| Field | Type | Required / present | Meaning |
| --- | --- | --- | --- |
| `accumulation` | integer | Yes |  |
| `creative_works` | integer | Yes |  |
| `mistakes` | integer | Yes |  |
| `practice_sets` | integer | Yes |  |
| `weekly_review` | integer | Yes |  |

### ArchiveAttachment

| Field | Type | Required / present | Meaning |
| --- | --- | --- | --- |
| `byte_size` | integer | Yes |  |
| `data_base64` | string | Yes |  |
| `media_type` | string | Yes |  |
| `relative_path` | string | Yes |  |
| `sha256` | string | Yes |  |

<a id="print-replies"></a>
## Original-turn PDF replies and recovery

Ordinary chat returns a frozen PDF through its original assistant message. For DingTalk, a `PrintArtifact` enters the original assistant transaction and Outbox through tool metadata `print_artifacts`, then uses the existing K12 DeliveryBatch. It is neither a `MediaOperation` nor a separate send-to-phone operation. These endpoints only consume committed original-turn facts. `message_id` is the original **assistant_message_id**, not ArtifactID or the platform inbound message ID.

| Method / path | Input | Response and side effects |
|---|---|---|
| `GET /api/k12/print-replies/{message_id}` | Required query `agent` | `{source,batch?,state}`; read-only. Source freezes owner/session/request, Agent, physical instance/Chat and original PDF references. Reads an existing batch state without sending |
| `POST /api/k12/print-replies/{message_id}/resume` | Required query `agent`; no body fields | `{batch,model_invocations_added:0}`; resumes the original frozen batch without solving/rendering or resending successful parts. Accepted/unknown parts follow their original receipt query. HTTP200 does not mean delivered |

Both use existing authentication and Agent ownership. Missing agent/message_id returns400; an absent or out-of-scope source returns404; persisted source read failure returns500. Further GET domain/storage errors use the existing K12 error mapping. POST preparation/send/query failure returns502 `{batch,error,model_invocations_added:0}` with actual batch facts. A new message ID is not recovery permission for an unknown outcome.

Each `source.artifacts[]` freezes `kind=print_artifact`, `agent_name/artifact_id/source_digest/byte_digest/byte_size/name/url`. The URL is an authenticated read address. Recovery uses persisted identity and byte digest, never a filename or current template regeneration. Original assistant digest is checked before first batch preparation; an existing batch resumes its frozen plan. Platform acceptance, delivery and handset observation remain separate facts.

When the same turn returns a PDF with generated images, video or speech, the original PDF reply batch freezes the body and every attachment together. Media operations, assets, execution receipts and assistant associations remain persisted without a second media delivery batch. Recovery validates assets and bytes against the original owner; PDFs never become MediaOperations. Each physical send still checks explicit media cancellation, with the existing exception for an explicit resend. GET may return `state=cancelled` for an unsent plan; already sent or unknown receipts retain their actual facts. Cancellation does not imply recall or delivery. Transport disconnects and service shutdown are not business cancellation and do not complete the Outbox event.

For a non-DingTalk conversation without this PDF Outbox, the original inline reply is returned. Trusted attachment bytes are read and verified through Source; the stable download URL is not used as an external byte source. Existing channel attachment capabilities remain unchanged; this does not introduce PDF support to additional channels.

## Copyable examples

Set HEXCLAW_URL and HEXCLAW_TOKEN for the service you are calling. Replace the Agent/resource IDs with values already read from that same service. Example responses show shape, not a frozen model answer.

```bash
export HEXCLAW_URL=http://127.0.0.1:16060
export HEXCLAW_TOKEN="your-service-token"
curl -fsS "$HEXCLAW_URL/api/k12/solve" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" -H "Content-Type: application/json" \
  -d '{"agent":"TutorAgent","subject":"数学","problem":"2 + 3 = ?"}'
```

```json
{
  "solution": "2 + 3 = 5",
  "verdict": "agree",
  "evidence_type": "numeric_exec",
  "badge": "verified-strong",
  "out_of_scope": false,
  "curriculum_unmapped": []
}
```

```bash
curl -fsS "$HEXCLAW_URL/api/k12/accumulation?agent=TutorAgent" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" -H "Content-Type: application/json" \
  -H "Idempotency-Key: collect-poem-1" \
  -d '{"content":"床前明月光，疑是地上霜。"}'
```

```json
{"record_id":"accum-example","created":true}
```

```bash
curl -fsS "$HEXCLAW_URL/api/k12/backup?agent=TutorAgent" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" -o learner.hexbak
curl -fsS "$HEXCLAW_URL/api/k12/restore" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" -H "Content-Type: application/json" \
  --data-binary @learner.hexbak
```

Restore changes records. Run it only for an intended restore, and retain the whole snapshot returned when non-null. See the canonical profile-bundle chapter for its complete example.
