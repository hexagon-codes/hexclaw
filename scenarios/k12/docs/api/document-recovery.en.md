# Conversation document recovery and render revisions

[中文](document-recovery.md) · [K12 API](../../API.en.md)

## Contents

- [1. Source and recovery](#1-source-and-recovery)
- [2. Read and resume preparation](#2-read-and-resume-preparation)
- [3. Re-export an ordinary reply](#3-re-export-an-ordinary-reply)
- [4. Re-export a native report](#4-re-export-a-native-report)

## 1. Source and recovery

Uses the existing `/api/k12` application mount. Ordinary documents require no learner profile and create no learner/mistake/history record. Service authentication is required; remote requests verify the service owner or original sender. Teaching reports retain Agent/source-owner checks.

Original content, attachments, assistant identity, and targets are persisted before rendering. Recovery resumes only document preparation or the original batch, adding zero model invocations. Known zero-send upload failures may resume. Unknown upload/send outcomes only query existing receipts. HTTP success does not establish user observation: inspect actual batch/part receipts.

Resending retains original bytes. Re-export creates an independent artifact while preserving the original result. The required `Idempotency-Key` identifies one explicit export request. Replaying the key reuses its first frozen template and result; a new export request uses a new key.

## 2. Read and resume preparation

`GET /api/k12/final-replies/{message_id}?agent={agent}`

The ID identifies a persisted assistant message. Returns `preparation_id`, `state` (`pending`, `delivered`, `dead`), `failure`, and `model_invocations_added: 0`. Reading neither renders nor sends.

`POST /api/k12/final-replies/{message_id}/resume?agent={agent}`

Accepts no replacement content, target, or model. Returns the actual `batch` and `model_invocations_added: 0`. Completed replay neither renders nor sends again; unknown outcomes remain unknown. Dependency/delivery failures return 502 with `batch` and `error` evidence.

## 3. Re-export an ordinary reply

`POST /api/k12/final-replies/{message_id}/reexport?agent={agent}`

Requires `Idempotency-Key`. Renders and delivers a new revision from original content and attachments, returning the actual `batch`. The same key does not duplicate delivery. Typed teaching PDFs use the artifact route below.

## 4. Re-export a native report

`POST /api/k12/print-artifacts/{id}/reexport?agent={agent}[&send=true]`

Requires `Idempotency-Key`. The ID identifies the original printable PDF artifact. Teaching reports reload their typed report and original images from the original grading finalization or text settlement, then use the currently installed template. Answers, date, subject, and identity retain the original result; models are not invoked again. Text-only printable artifacts also support re-export. Historical Markdown images without a frozen image source return an explicit error instead of an incomplete file.

By default only prepares a file, returning `artifact` (new ID, download URL, digest, size), `replayed`, and `model_invocations_added: 0`. First preparation returns 201; replay returns 200. Download using `GET /api/k12/print-artifacts/{id}/content?agent={agent}`. Explicit `send=true` delivers through existing valid bindings and returns the actual `batch`. Delivery failure returns 502 while preserving the artifact and batch.

Missing required parameters/key returns 400; unavailable scoped resources return 404; source/resource/state conflicts return 409. Existing content, image, digest, and frozen-version checks remain in effect.
