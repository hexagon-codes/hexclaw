package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var K12RecognitionAdjudicationV115 = Migration{
	Version:     115,
	Description: "K12 独立来源裁决的一次授权与不可变结算",
	AtomicFunc:  migrateK12RecognitionAdjudicationV115,
}

const recognitionAdjudicationV115DDL = `
CREATE TABLE k12_recognition_layout_adjudication_authorizations (
 plan_id TEXT NOT NULL REFERENCES k12_recognition_layout_plans(plan_id) ON DELETE CASCADE,
 candidate_id TEXT NOT NULL,
 authorization_id TEXT NOT NULL UNIQUE,
 authorization_digest TEXT NOT NULL,
 physical_unit TEXT NOT NULL,
 request_json TEXT NOT NULL,
 original_candidate_json TEXT NOT NULL,
 original_candidate_digest TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(plan_id,candidate_id),
 UNIQUE(plan_id,physical_unit),
 FOREIGN KEY(plan_id,candidate_id) REFERENCES k12_recognition_layout_candidates(plan_id,candidate_id) ON DELETE CASCADE
);
CREATE TABLE k12_recognition_layout_adjudication_settlements (
 authorization_id TEXT PRIMARY KEY REFERENCES k12_recognition_layout_adjudication_authorizations(authorization_id) ON DELETE CASCADE,
 source_physical_invocation_id TEXT NOT NULL UNIQUE REFERENCES k12_model_physical_invocations(physical_invocation_id) ON DELETE RESTRICT,
 settlement_json TEXT NOT NULL,
 settlement_digest TEXT NOT NULL,
 result_digest TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL
);
CREATE TRIGGER k12_recognition_layout_adjudication_authorization_immutable
BEFORE UPDATE ON k12_recognition_layout_adjudication_authorizations
BEGIN SELECT RAISE(ABORT,'recognition adjudication authorization is immutable'); END;
CREATE TRIGGER k12_recognition_layout_adjudication_settlement_immutable
BEFORE UPDATE ON k12_recognition_layout_adjudication_settlements
BEGIN SELECT RAISE(ABORT,'recognition adjudication settlement is immutable'); END;
`

func migrateK12RecognitionAdjudicationV115(ctx context.Context, db *sql.DB, recordVersion func(context.Context, *sql.Tx) error) (retErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var foreignKeys int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		if foreignKeys != 0 {
			_, restoreErr := conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys=ON`)
			retErr = errors.Join(retErr, restoreErr)
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// SQLite 的单元 CHECK 随表冻结；从当前 DDL 重建，保留所有历史列及关联对象。
	tables := []string{"k12_problem_source_recognition_physical_results", "k12_model_physical_invocations"}
	type schemaObject struct{ name, kind, ddl string }
	var objects []schemaObject
	rows, err := tx.QueryContext(ctx, `SELECT name,type,sql FROM sqlite_master
	 WHERE sql IS NOT NULL AND (type='index' AND tbl_name IN (?,?) OR type='trigger'
	 AND (sql LIKE '%k12_model_physical_invocations%' OR sql LIKE '%k12_problem_source_recognition_physical_results%')) ORDER BY type,name`, tables[0], tables[1])
	if err != nil {
		return err
	}
	for rows.Next() {
		var object schemaObject
		if err := rows.Scan(&object.name, &object.kind, &object.ddl); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, object := range objects {
		if object.kind == "trigger" {
			if _, err := tx.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(object.name, `"`, `""`)+`"`); err != nil {
				return err
			}
		}
	}
	unit := regexp.MustCompile(`physical_unit GLOB 'layout_repair_\[0-9\]\[0-9\]\[0-9\]\[0-9\]'\s+AND substr\(physical_unit,15,4\) BETWEEN '0001' AND '9999'`)
	for _, table := range tables {
		var ddl string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			return err
		}
		if len(unit.FindAllStringIndex(ddl, -1)) != 2 {
			return fmt.Errorf("V115 unexpected physical unit schema in %s", table)
		}
		ddl = unit.ReplaceAllString(ddl, `($0) OR (physical_unit GLOB 'layout_adjudicate_[0-9][0-9][0-9][0-9]' AND substr(physical_unit,19,4) BETWEEN '0001' AND '9999')`)
		temporary := table + "_v115"
		ddl = "CREATE TABLE " + temporary + ddl[strings.Index(ddl, "("):]
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+temporary+` SELECT * FROM `+table); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE `+table); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+temporary+` RENAME TO `+table); err != nil {
			return err
		}
	}
	for _, object := range objects {
		if _, err := tx.ExecContext(ctx, object.ddl); err != nil {
			return fmt.Errorf("restore V115 schema object %s: %w", object.name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, recognitionAdjudicationV115DDL); err != nil {
		return err
	}
	var violations int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		return err
	}
	if violations != 0 {
		return fmt.Errorf("V115 recognition adjudication found %d foreign-key conflicts", violations)
	}
	if err := recordVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
