package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// K12MaterialRecoveryV117 保留原调用，并为同一冻结请求的明确恢复记录独立尝试。
var K12MaterialRecoveryV117 = Migration{Version: 117, Description: "K12 材料验证的一次明确恢复", AtomicFunc: migrateK12MaterialRecoveryV117}

const materialRecoveryV117DDL = `CREATE TABLE k12_material_recovery_decisions (
 decision_id TEXT PRIMARY KEY,
 owner_id TEXT NOT NULL,
 task_id TEXT NOT NULL REFERENCES k12_material_preparations(task_id),
 original_invocation_id TEXT NOT NULL UNIQUE REFERENCES k12_material_invocations(invocation_id),
 operation TEXT NOT NULL CHECK(operation='solve_verify'),
 request_digest TEXT NOT NULL,
 replacement_attempt INTEGER NOT NULL CHECK(replacement_attempt>0),
 plan_fingerprint TEXT NOT NULL,
 plan_json TEXT NOT NULL CHECK(json_valid(plan_json)),
 idempotency_key TEXT NOT NULL,
 replacement_invocation_id TEXT UNIQUE REFERENCES k12_material_invocations(invocation_id),
 created_at INTEGER NOT NULL,
 UNIQUE(owner_id,idempotency_key),
 UNIQUE(task_id,operation,request_digest,replacement_attempt)
);`

func migrateK12MaterialRecoveryV117(ctx context.Context, db *sql.DB, recordVersion func(context.Context, *sql.Tx) error) (retErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var fk int
	if err = conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		if fk != 0 {
			_, e := conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys=ON`)
			retErr = errors.Join(retErr, e)
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	type object struct{ name, kind, ddl string }
	var objects []object
	rows, err := tx.QueryContext(ctx, `SELECT name,type,sql FROM sqlite_master WHERE sql IS NOT NULL AND (type='index' AND tbl_name='k12_material_invocations' OR type='trigger' AND sql LIKE '%k12_material_invocations%') ORDER BY type,name`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var o object
		if err = rows.Scan(&o.name, &o.kind, &o.ddl); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, o)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, o := range objects {
		if o.kind == "trigger" {
			if _, err = tx.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(o.name, `"`, `""`)+`"`); err != nil {
				return err
			}
		}
	}
	var ddl string
	if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='k12_material_invocations'`).Scan(&ddl); err != nil {
		return err
	}
	unique := regexp.MustCompile(`(?i)UNIQUE\s*\(\s*task_id\s*,\s*operation\s*,\s*request_digest\s*\)`)
	if len(unique.FindAllStringIndex(ddl, -1)) != 1 {
		return errors.New("unexpected material invocation uniqueness")
	}
	ddl = unique.ReplaceAllString(ddl, `attempt INTEGER NOT NULL DEFAULT 0 CHECK(attempt>=0), UNIQUE(task_id,operation,request_digest,attempt)`)
	ddl = "CREATE TABLE k12_material_invocations_v117" + ddl[strings.Index(ddl, "("):]
	if _, err = tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT name FROM pragma_table_info('k12_material_invocations') ORDER BY cid`)
	if err != nil {
		return err
	}
	var cols []string
	for rows.Next() {
		var c string
		if err = rows.Scan(&c); err != nil {
			rows.Close()
			return err
		}
		cols = append(cols, `"`+strings.ReplaceAll(c, `"`, `""`)+`"`)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	columns := strings.Join(cols, ",")
	if _, err = tx.ExecContext(ctx, `INSERT INTO k12_material_invocations_v117 (`+columns+`) SELECT `+columns+` FROM k12_material_invocations`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DROP TABLE k12_material_invocations; ALTER TABLE k12_material_invocations_v117 RENAME TO k12_material_invocations`); err != nil {
		return err
	}
	for _, o := range objects {
		if _, err = tx.ExecContext(ctx, o.ddl); err != nil {
			return fmt.Errorf("restore material schema object %s: %w", o.name, err)
		}
	}
	if _, err = tx.ExecContext(ctx, materialRecoveryV117DDL); err != nil {
		return err
	}
	var violations int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		return err
	}
	if violations != 0 {
		return fmt.Errorf("material recovery migration found %d foreign-key conflicts", violations)
	}
	if err = recordVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
