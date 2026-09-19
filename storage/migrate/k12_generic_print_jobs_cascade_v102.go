package migrate

import (
	"context"
	"database/sql"
	"fmt"
)

// K12GenericPrintJobsCascadeV102 修复含打印历史的 Agent 无法注销的问题。
//
// k12_generic_print_jobs.artifact_id 引用 k12_print_artifacts(artifact_id) 使用
// ON DELETE RESTRICT，而 Agent 注销走 DELETE FROM agents 依赖 SQLite 级联删除：
// 级联删除 artifact 时尚存的 job 行触发 RESTRICT，导致整个删除以
// FOREIGN KEY constraint failed (1811) 回滚（BUG-20260913-007）。
// 产品不存在孤立删除 print artifact 的路径，artifact 随 Agent 同生共灭，
// 因此将该内部依赖改为 ON DELETE CASCADE；Agent 删除时两者同销。
// 已有 V25 表结构保持不变，仅改这一处删除动作（V62 级联修复 precedent）。
var K12GenericPrintJobsCascadeV102 = Migration{
	Version:     102,
	Description: "K12 generic print jobs follow artifact cascade on agent delete",
	AtomicFunc:  migrateK12GenericPrintJobsCascadeV102,
}

func migrateK12GenericPrintJobsCascadeV102(
	ctx context.Context,
	db *sql.DB,
	recordVersion func(context.Context, *sql.Tx) error,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin K12 print jobs cascade migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, K12GenericPrintJobsCascadeV102DDL); err != nil {
		return fmt.Errorf("rebuild K12 generic print jobs cascade table: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("run K12 print jobs cascade foreign_key_check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowID, foreignKeyID any
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			return fmt.Errorf("scan K12 print jobs cascade foreign_key_check: %w", err)
		}
		return fmt.Errorf(
			"K12 print jobs cascade foreign_key_check failed: table=%s parent=%s",
			table,
			parent,
		)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate K12 print jobs cascade foreign_key_check: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close K12 print jobs cascade foreign_key_check: %w", err)
	}
	if err := recordVersion(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit K12 print jobs cascade migration: %w", err)
	}
	return nil
}

const K12GenericPrintJobsCascadeV102DDL = `
CREATE TABLE k12_generic_print_jobs_v102 (
    print_job_id          TEXT    PRIMARY KEY,
    agent_name            TEXT    NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
    idempotency_key       TEXT    NOT NULL CHECK(length(trim(idempotency_key)) BETWEEN 1 AND 512),
    request_digest        TEXT    NOT NULL CHECK(length(request_digest) = 64),
    artifact_id           TEXT    NOT NULL REFERENCES k12_print_artifacts(artifact_id) ON DELETE CASCADE,
    status                TEXT    NOT NULL DEFAULT 'preparing' CHECK(status IN
        ('preparing','dialog_open','submitted','printed','cancelled','failed','outcome_unknown')),
    attempt_count         INTEGER NOT NULL DEFAULT 1 CHECK(attempt_count BETWEEN 1 AND 3),
    native_job_id         TEXT    NOT NULL DEFAULT '',
    native_receipt_id     TEXT    NOT NULL DEFAULT '',
    printer_snapshot_json TEXT    NOT NULL DEFAULT '{}',
    failure_kind          TEXT    NOT NULL DEFAULT '',
    failure_detail        TEXT    NOT NULL DEFAULT '',
    prepared_at           INTEGER NOT NULL CHECK(prepared_at > 0),
    printed_at            INTEGER NOT NULL DEFAULT 0,
    created_at            INTEGER NOT NULL CHECK(created_at > 0),
    updated_at            INTEGER NOT NULL CHECK(updated_at > 0),
    version               INTEGER NOT NULL DEFAULT 0,
    UNIQUE(agent_name, idempotency_key)
);
INSERT INTO k12_generic_print_jobs_v102 (
    print_job_id, agent_name, idempotency_key, request_digest, artifact_id,
    status, attempt_count, native_job_id, native_receipt_id,
    printer_snapshot_json, failure_kind, failure_detail,
    prepared_at, printed_at, created_at, updated_at, version
)
SELECT print_job_id, agent_name, idempotency_key, request_digest, artifact_id,
    status, attempt_count, native_job_id, native_receipt_id,
    printer_snapshot_json, failure_kind, failure_detail,
    prepared_at, printed_at, created_at, updated_at, version
FROM k12_generic_print_jobs;
DROP TABLE k12_generic_print_jobs;
ALTER TABLE k12_generic_print_jobs_v102
    RENAME TO k12_generic_print_jobs;
CREATE INDEX IF NOT EXISTS idx_k12_generic_print_jobs_owner_status
    ON k12_generic_print_jobs(agent_name, status, updated_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_k12_generic_print_jobs_unresolved_artifact
    ON k12_generic_print_jobs(agent_name, artifact_id)
    WHERE status IN ('preparing','dialog_open','submitted','outcome_unknown');
`
