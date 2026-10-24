# Reviewed question source groups

`POST /api/k12/review-source-groups` records an explicitly reviewed equivalence between existing photo-assessment sources. Mistake sheets and weekly due practice consume it as a read-only projection. Authentication follows the host K12 API.

The request contains `agent`, `canonical_record_id`, at least two distinct `members`, and `evidence: {kind: "reviewed_sources", note: "..."}`. Every member supplies `record_id`, `job_id`, `problem_id`, `attempt_id`, positive `confirmed_version` and `input_revision`, plus the existing `input_digest` and `result_digest`.

The server derives question/page digests, printed numbering and the canonical statement from the selected frozen sources. Clients cannot provide replacement questions, answers or page digests. Inputs must already be published and must match the complete original question. No solving, grading, mastery update or message delivery is performed.

The response is `{group, created, replayed}`. The group preserves its stable ID, canonical statement and digest, complete members and review evidence. A question digest covers the original question text, excluding learning status and scheduling fields.

Identity binds the agent, canonical record and complete members, excluding derived statement text and review notes. Repeating the same request after a statement-normalization update returns the original frozen snapshot. A different group cannot take over members of another still-valid group; it receives 409. A newly reviewed group may be recorded after the previous group becomes stale.

| Status | Meaning |
|---|---|
| 201 | Group first persisted atomically |
| 200 | Exact identity replay; original group, note and timestamp retained |
| 400 | Invalid request, evidence or member set |
| 404 | Record missing or outside the requested agent/mistake collection |
| 409 | Source unpublished/changed, or members overlap another still-valid reviewed group |
| 500 | Storage or other internal failure |

Existing mistakes and frozen PDFs remain intact. Any changed member invalidates the entire group for projection; the original questions remain available. Learning-status or due-time changes do not invalidate identity. Similarity alone never creates a reviewed group. This API does not establish answer verification, Skill installation or A11 text-solve finalization.
