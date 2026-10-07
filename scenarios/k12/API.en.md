# K12 public API

[中文](API.md) · [Core API](../../docs/api.en.md) · [Back to README](../../README.en.md)

Scenario endpoints use `/api/k12/*`; Agent creation still uses `POST /api/v1/agents`. Business requests require the current service Bearer token. Authentication establishes identity; `agent` uses the instance `name`. Scope and record-ownership checks depend on the operation, and this field is not an identity token.

This document describes current source contracts. Releases use their matching documentation. Ordinary JSON errors return `{"error":"..."}` with an HTTP status; exports, assets, and result endpoints may return binary content. Replace sample IDs, revisions, and business values with actual response values.

## Complete endpoint references

| Module | Contents |
| --- | --- |
| [Image tasks, creative work, and materials](docs/api/tasks.en.md) | Image facade, results and recovery, source actions and feedback, works, assets, and material preparation |
| [Practice, weekly plans, and printing](docs/api/practice.en.md) | Mistake practice generation, candidate selection, practice sets, attempts and grading, weekly plans, and native print receipts |
| [Profiles, textbooks, and learning records](docs/api/records.en.md) | View descriptors, single-question processing, profiles and progress, accumulation and insights, export/backup, tutoring, and IM automation |

These references cover the current explicit K12 routes; [OpenAPI](../../api/openapi.yaml) provides the corresponding request and response structures. The sections below describe compatibility and shared profile, progress, and image-completion rules. Use each operation's field, revision, and side-effect contract for native or external clients.

- [Profile bundle updates](#profile-bundle)
- [Textbooks and curriculum progress](#textbook-and-curriculum-progress)
- [Image task completion](#image-task-completion)
- [Scope and compatibility](#other-scenario-apis)

<a id="profile-bundle"></a>
## Profile bundle updates

`PUT /api/k12/profile-bundle` commits the child profile, curriculum progress, weekly practice settings, and optional Agent configuration as one transaction command. It is not a field PATCH. The data update itself needs no model.

Read profiles with `GET /api/k12/profile?agent=...`, which returns `child_name`, `grade_term`, the legacy math projection `textbook_edition`, and `revision`.

The old `PUT /api/k12/profile` always returns `405`, `Allow: GET`, and `{"error":"profile updates require /api/k12/profile-bundle"}`. Do not use it to create or edit profiles.

### Request and revisions

Before updating, read the `revision` from these endpoints:

- `GET /api/k12/profile?agent=...`
- `GET /api/k12/curriculum-progress?agent=...&subject=math`
- `GET /api/k12/weekly-practice/settings?agent=...`

Supply their values as the three `expected_*_revision` fields. Unpersisted states may return `0`; do not guess revisions for existing records. A null progress object still has an outer lifecycle revision.

Omitted revision fields decode as `0`; omission does not bypass revision checks. Correct clients send the actual values just read. Profile `grade_term` may be empty; non-empty values use the current primary-school term enum, rather than other stages accepted by single-question solving.

| Field | Contract |
| --- | --- |
| `agent`, `idempotency_key` | The owned instance name and a non-empty key for this transaction command |
| `expected_profile_revision` | The profile revision just read |
| `expected_progress_revision` | The math progress lifecycle revision just read |
| `expected_settings_revision` | The weekly practice settings revision just read |
| `profile` | Non-empty `child_name`, complete `subject_textbooks`, and an empty or valid primary-school `grade_term`; submit the complete profile values for this update |
| `subject_textbooks` | Exactly six keys: `math/chinese/english/science/information_technology/art`, each containing a non-empty textbook edition string |
| `weekly_practice_settings` | Valid IANA `timezone` and `arithmetic_minutes: 1..5`; two enabled booleans and `textbook_consolidation_tier: less/standard/more`, defaulting to standard when omitted |
| `curriculum_progress` | Omitted: automatic processing; null: clear for this operation; object: follow the [shared progress contract](#textbook-and-curriculum-progress) |
| `agent_config` | Optional; when present, provide complete non-empty display_name/description/system_prompt, both provider and model empty or both non-empty, and skills; the service adds mandatory scenario skills |

Strict decoding rejects unknown fields. This sample explicitly clears progress; its zero revisions are illustrative and must be replaced with the values read from the service:

```json
{
  "agent": "mingming",
  "idempotency_key": "profile-update-1",
  "expected_profile_revision": 0,
  "expected_progress_revision": 0,
  "expected_settings_revision": 0,
  "profile": {
    "child_name": "明明",
    "grade_term": "五年级下",
    "subject_textbooks": {
      "math": "人教版",
      "chinese": "统编版",
      "english": "外研版",
      "science": "教科版",
      "information_technology": "浙教版",
      "art": "人美版"
    }
  },
  "curriculum_progress": null,
  "weekly_practice_settings": {
    "timezone": "Asia/Shanghai",
    "textbook_consolidation_enabled": false,
    "textbook_consolidation_tier": "standard",
    "arithmetic_warmup_enabled": false,
    "arithmetic_minutes": 2
  }
}
```

Save the body as a local `profile-bundle.json`, then use the current service address and token:

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/k12/profile-bundle" \
  -X PUT \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @profile-bundle.json
```

Grade terms and textbook editions are actual domain values, not translated display labels. Keep values returned or selected for the current profile.

### Response and errors

Success is `200` with `{"profile":{...},"curriculum_progress":null or object,"weekly_practice_settings":{...},"replayed":false}`. Supplying optional Agent configuration also returns `agent_config`. Replays are indicated by `replayed`; use a new key when changing command content. The profile's `textbook_edition` is a compatibility projection of the math edition.

| HTTP | Cause |
| --- | --- |
| `400` | Invalid JSON, unknown fields, incomplete six-subject profile, invalid grade/settings, or mismatching explicit curriculum progress |
| `401` | Invalid current service token |
| `404` | Referenced domain record missing or unavailable in the current scope |
| `409` | Revisions changed or command content conflicts with an existing idempotency key; reread state and form a new command |
| `502` | Required textbook catalog unavailable |
| `500` | Unclassified storage or application error |

The error body is `{"error":"..."}`. Validation or transaction failure leaves no partial profile, progress, binding, or settings.

<a id="textbook-and-curriculum-progress"></a>
## Textbooks and curriculum progress

This is the shared contract for creating, editing, and previewing progress.

1. K12 creation uses `POST /api/v1/agents`. Omitting `curriculum_progress` permits a suggestion from one uniquely matching real textbook catalog; explicit `null` leaves progress unset. Creation works without a usable catalog.
2. The Agent, non-null progress, and binding commit in one SQLite transaction. Failure leaves no partial registration. The platform response remains `{"message":"Agent 已注册","name":"..."}`.
3. Creation and `PUT /api/k12/profile-bundle` share non-null selections: `subject: math`, an owned `textbook_manifest_id`, and optional `lesson_id/page_from/page_to`. `evidence_source` distinguishes `parent_confirmed` from `ai_estimated`.
4. Confirmed progress requires a catalog `volume/unit_id`. AI input may leave unit_id empty; the backend computes it while retaining explicit lesson/page values. An explicit non-null AI selection that cannot match a real textbook catalog for the current profile returns `400` for both creation and editing, without saving changes.
5. Estimates never overwrite confirmed progress in the same scope. PUT omission allows automatic processing; explicit null clears progress for this operation. An old empty progress lifecycle does not disable later suggestions.
6. `GET /api/k12/curriculum-progress?mode=estimate` is a read-only preview returning `{"progress":null or object,"revision":...}`. Creation needs no agent; parameters are `grade_term/textbook_edition` and optional `textbook_manifest_id/lesson_id/page_from/page_to`. Existing instances may include agent, allowing persisted profile values to fill omitted grade and edition fields.
7. `GET /api/k12/textbook-binding-options?mode=create` reads the current trusted owner's math textbook candidates before registration, without an agent or binding writes, returning `{"items":[...]}`. Without creation mode, existing Agent-scoped queries and ownership checks remain.
8. Grade matching uses persisted cover evidence, not filenames; ambiguous catalogs are not picked arbitrarily. confirmed_at remains a Unix number and is `0` for AI suggestions. estimate_basis stores the date, reference window, weight method, and optional evidence receipt hash.
9. Reference windows are September 1 through January 31 and March 1 through June 30. Using the Shanghai date, the service chooses the nearest matching cycle and estimates a unit from weekday ratios, catalog lesson counts, and default page spans. These are suggestions, not an actual school calendar or evidence of learning or mastery.
10. Read-only previews do not write. Only new business requests adopt suggestions through CAS. Frozen results are not regenerated because of new suggestions; missing textbooks or progress do not block real questions.

<a id="image-task-completion"></a>
## Image task completion

`POST /api/k12/image-tasks/{id}/retry` accepts the optional fixed `intent: "known_local_technical"` only for the original `failed_terminal/assessing` job with a current-generation definite local technical failure, no unknown calls and no final artifact. Account scope is server-derived; both parent and job versions commit atomically before scheduling. Full frozen model/input/history and count 3 remain; new calls begin at generation 4. Omitting the intent preserves ordinary retry and max3. Repeated versions return `409`; query uncertain outcomes without resubmitting. See the [exact retry contract](docs/api/tasks.en.md#op-post-api-k12-image-tasks-id-retry).

### Common entry points

- `POST /api/k12/image-tasks`
- `GET /api/k12/image-tasks/{dispatch_id}?agent=...`
- `POST /api/k12/image-tasks/{dispatch_id}/confirm`
- `POST /api/k12/image-tasks/{dispatch_id}/retry`
- `POST /api/k12/image-tasks/{dispatch_id}/cancel`
- `GET /api/k12/image-tasks/{dispatch_id}/result?agent=...`

The facade routes images as `completed_homework`, `blank_worksheet`, `writing`, or `artwork`. Internal HomeworkSubmission, GradingJob, and CreativeWorkIntake objects do not expose separate client-addressable image-task routes. `/recognize*`, `/grading-jobs*`, and `/creative-work-ocr-jobs*` are not public contracts and return `404/405`.

### Automatic completion and channels

- Desktop and configured DingTalk use the same domain tasks, decisions, durable results, and automatic progression. Channel differences adapt transport and presentation, not grading standards or confirmation state machines. DingTalk is the currently declared K12-specific channel projection; generic adapters do not prove equivalent K12 support.
- The default flow is image input → automatic recognition/assessment → actual result. Homework grading primarily delivers an annotated original image; readable portions continue processing.
- Content that remains unreadable after automatic recognition is marked “无法识别” (“unreadable”) as part of the terminal result. It is not guessed, answered, marked wrong, or stored as mastery/mistake evidence. Confirmation, OCR correction, reshooting, or skipping is not mandatory; another photo is a voluntary follow-up.
- Provider timeouts, unknown call outcomes, protocol errors, and application failures are technical failures, not unreadable content. Do not replay an unknown outcome blindly; inspect `failure_kind`, `retryable`, and recovery state.
- Poll the same `dispatch_id` and inspect its `target_projection`, progress, and `/result`. Dispatch status `routed` means classified/routed, not that the final annotation or other output was delivered. Intermediate progress cannot replace the final result.
- Manual creative entries retain `creative_entry` and explicit `creative.action=commit` semantics for creating or versioning works. These are not default prerequisites for automatic homework grading. The `confirm` route serves explicit task branches and manual creative actions.

<a id="other-scenario-apis"></a>
## Scope and compatibility

The module references above maintain complete fields, responses, errors, and examples. `POST /api/v1/agents` belongs to the platform API; K12 registration metadata and progress selections follow the shared rules in this document.

- Image input uses the public `image-tasks` facade. Internal recognition, GradingJob, and OCR objects are not client task interfaces.
- `POST /grade`, `POST /solve`, and explicit learning-record operations remain available under the [profiles and learning records](docs/api/records.en.md) contracts; single-question operations have not been removed.
- The registered legacy `PUT /profile` rejects requests with `405` and `Allow: GET`. Updates use `PUT /profile-bundle`.
- Use current module fields for backups, exports, accumulation, and tutoring. The references distinguish backup versions, JSON fallback, and binary outputs.
- IM result delivery and the platform's generic Cron receiver configuration have separate contracts. Channel connectivity does not prove task completion or delivery.

## Implementation references

[Scenario routes](apihttp/handler.go) · [Profile and weekly practice handlers](apihttp/weekly_practice_handler.go) · [Read-only progress suggestions](apihttp/curriculum_progress_handler.go). Use the public contract above rather than inferring request shapes from implementation code.
