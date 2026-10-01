package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// 首读结算与复读授权仅扩展原账本，旧计划保持空列与原摘要。
var K12RecognitionInitialReadV122 = Migration{Version: 122, Description: "K12 atomic initial read and frozen review batches", AtomicFunc: migrateK12RecognitionInitialReadV122}

const recognitionInitialReadV122DDL = `
ALTER TABLE k12_recognition_layout_plans ADD COLUMN initial_read_settlement_json TEXT CHECK(initial_read_settlement_json IS NULL OR json_valid(initial_read_settlement_json));
ALTER TABLE k12_recognition_layout_batches ADD COLUMN review_authorization_json TEXT CHECK(review_authorization_json IS NULL OR json_valid(review_authorization_json));
ALTER TABLE k12_recognition_layout_batch_settlements ADD COLUMN review_settlement_json TEXT CHECK(review_settlement_json IS NULL OR json_valid(review_settlement_json));
CREATE TRIGGER k12_recognition_layout_initial_read_immutable BEFORE UPDATE OF initial_read_settlement_json ON k12_recognition_layout_plans
WHEN OLD.initial_read_settlement_json IS NOT NULL AND NEW.initial_read_settlement_json IS NOT OLD.initial_read_settlement_json
BEGIN SELECT RAISE(ABORT,'initial read settlement is immutable'); END;
`

const recognitionInitialReadV122SourceGuard = `EXISTS (
 SELECT 1 FROM k12_recognition_layout_plans initial_plan
 WHERE initial_plan.plan_id=NEW.plan_id AND initial_plan.parent_invocation_id=NEW.parent_invocation_id
 AND json_extract(initial_plan.layout_header_json,'$.initial_read_mode')='manifest_with_content_v1'
 AND initial_plan.manifest_physical_invocation_id=child.physical_invocation_id
 AND child.physical_unit='whole_page' AND child.plan_digest=initial_plan.header_digest
 AND initial_plan.manifest_result_digest=child.result_digest
 AND EXISTS (SELECT 1 FROM json_each(initial_plan.initial_read_settlement_json,'$.result.first_reads') first_read
 WHERE json_extract(first_read.value,'$.candidate_id')=NEW.candidate_id
 AND json_extract(first_read.value,'$.classification')='valid'
 AND json_extract(first_read.value,'$.result_kind')=NEW.result_kind
 AND json_extract(first_read.value,'$.result_digest')=NEW.result_digest)
)`

func recognitionInitialReadV122TableDDL(table, ddl string) (string, error) {
	switch table {
	case "k12_model_physical_invocations", "k12_problem_source_recognition_physical_results":
		unit := regexp.MustCompile(`physical_unit GLOB 'layout_repair_\[0-9\]\[0-9\]\[0-9\]\[0-9\]'\s+AND substr\(physical_unit,15,4\) BETWEEN '0001' AND '9999'`)
		if len(unit.FindAllStringIndex(ddl, -1)) != 2 {
			return "", fmt.Errorf("V122 unexpected physical unit schema in %s", table)
		}
		ddl = unit.ReplaceAllString(ddl, `($0) OR (physical_unit GLOB 'layout_review_batch_[0-9][0-9][0-9][0-9]' AND substr(physical_unit,21,4) BETWEEN '0001' AND '9999')`)
	case "k12_recognition_layout_batches":
		unit := regexp.MustCompile(`physical_unit GLOB 'layout_batch_\[0-9\]\[0-9\]\[0-9\]\[0-9\]'\s+AND substr\(physical_unit,14,4\) BETWEEN '0001' AND '9999'\s+AND CAST\(substr\(physical_unit,14,4\) AS INTEGER\)=ordinal`)
		if len(unit.FindAllStringIndex(ddl, -1)) != 1 {
			return "", fmt.Errorf("V122 unexpected batch schema")
		}
		ddl = unit.ReplaceAllString(ddl, `($0) OR (physical_unit GLOB 'layout_review_batch_[0-9][0-9][0-9][0-9]' AND substr(physical_unit,21,4) BETWEEN '0001' AND '9999' AND CAST(substr(physical_unit,21,4) AS INTEGER)=ordinal)`)
	case "k12_recognition_layout_batch_settlements":
		unit := regexp.MustCompile(`source_physical_unit GLOB 'layout_batch_\[0-9\]\[0-9\]\[0-9\]\[0-9\]'\s+AND substr\(source_physical_unit,14,4\) BETWEEN '0001' AND '9999'`)
		if len(unit.FindAllStringIndex(ddl, -1)) != 1 {
			return "", fmt.Errorf("V122 unexpected batch settlement schema")
		}
		ddl = unit.ReplaceAllString(ddl, `($0) OR (source_physical_unit GLOB 'layout_review_batch_[0-9][0-9][0-9][0-9]' AND substr(source_physical_unit,21,4) BETWEEN '0001' AND '9999')`)
	case "k12_recognition_layout_finalizations":
		if !strings.Contains(ddl, "physical_result_count BETWEEN 2 AND 65") {
			return "", fmt.Errorf("V122 unexpected finalization schema")
		}
		ddl = strings.Replace(ddl, "physical_result_count BETWEEN 2 AND 65", "physical_result_count BETWEEN 1 AND 65", 1)
	}
	return ddl, nil
}

func migrateK12RecognitionInitialReadV122(ctx context.Context, db *sql.DB, recordVersion func(context.Context, *sql.Tx) error) (retErr error) {
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
	tables := []string{"k12_problem_source_recognition_physical_results", "k12_model_physical_invocations", "k12_recognition_layout_batches", "k12_recognition_layout_batch_settlements", "k12_recognition_layout_finalizations"}
	type schemaObject struct{ name, kind, ddl string }
	var objects []schemaObject
	rows, err := tx.QueryContext(ctx, `SELECT name,type,sql FROM sqlite_master WHERE sql IS NOT NULL AND (type='index' AND tbl_name IN ('k12_problem_source_recognition_physical_results','k12_model_physical_invocations','k12_recognition_layout_batches','k12_recognition_layout_batch_settlements','k12_recognition_layout_finalizations') OR type='trigger') ORDER BY type,name`)
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

	for _, table := range tables {
		var ddl string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			return err
		}
		var rewriteErr error
		ddl, rewriteErr = recognitionInitialReadV122TableDDL(table, ddl)
		if rewriteErr != nil {
			return rewriteErr
		}
		temporary := table + "_v122"
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
		ddl := object.ddl
		if object.name == "k12_recognition_layout_candidate_result_source_guard" {
			needle := "AND (\n          EXISTS ("
			if !strings.Contains(ddl, needle) {
				return fmt.Errorf("V122 candidate source guard shape changed")
			}
			ddl = strings.Replace(ddl, needle, "AND ("+recognitionInitialReadV122SourceGuard+" OR EXISTS (", 1)
		}
		if object.name == "k12_recognition_layout_finalization_insert_guard" {
			needle := "AND plan.status='running'"
			if !strings.Contains(ddl, needle) {
				return fmt.Errorf("V122 finalization guard shape changed")
			}
			ddl = strings.Replace(ddl, needle, needle+" AND (NEW.physical_result_count>=2 OR json_extract(plan.layout_header_json,'$.initial_read_mode')='manifest_with_content_v1')", 1)
		}
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("restore V122 schema object %s: %w", object.name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, recognitionInitialReadV122DDL); err != nil {
		return err
	}
	var violations int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		return err
	}
	if violations != 0 {
		return fmt.Errorf("V122 recognition adjudication found %d foreign-key conflicts", violations)
	}
	if err := recordVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
