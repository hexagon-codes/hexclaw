package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// K12TextSolveScopeV143 让文字求解、报告及原会话投递归属实际请求，保留学生记录和原生打印约束。
var K12TextSolveScopeV143 = Migration{Version: 143, Description: "Decouple text solve execution and report artifacts from learner registration", AtomicFunc: migrateK12TextSolveScopeV143}

func migrateK12TextSolveScopeV143(ctx context.Context, db *sql.DB, recordVersion func(context.Context, *sql.Tx) error) (retErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var enabled int
	if err = conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer func() {
		if enabled != 0 {
			_, restoreErr := conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys=ON")
			retErr = errors.Join(retErr, restoreErr)
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	type schemaObject struct{ name, kind, definition string }
	var objects []schemaObject
	rows, err := tx.QueryContext(ctx, `SELECT name,type,sql FROM sqlite_master WHERE sql IS NOT NULL AND
 ((type='index' AND tbl_name IN ('k12_text_solve_runs','k12_print_artifacts','k12_delivery_batches','k12_delivery_receipts')) OR
  (type='trigger' AND (sql LIKE '%k12_text_solve_runs%' OR sql LIKE '%k12_print_artifacts%' OR sql LIKE '%k12_delivery_batches%' OR sql LIKE '%k12_delivery_receipts%'))) ORDER BY type,name`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var object schemaObject
		if err = rows.Scan(&object.name, &object.kind, &object.definition); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, object)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, object := range objects {
		if object.kind == "trigger" {
			if _, err = tx.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(object.name, `"`, `""`)+`"`); err != nil {
				return err
			}
		}
	}
	for _, table := range []string{"k12_text_solve_runs", "k12_print_artifacts", "k12_delivery_batches", "k12_delivery_receipts"} {
		var definition string
		if err = tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&definition); err != nil {
			return err
		}
		pattern := `(?i),\s*FOREIGN KEY\s*\(agent_name\)\s*REFERENCES agents\s*\(name\)\s*ON DELETE CASCADE`
		if table != "k12_text_solve_runs" {
			pattern = `(?i)\s+REFERENCES agents\s*\(name\)\s*ON DELETE CASCADE`
		}
		foreignKey := regexp.MustCompile(pattern)
		if len(foreignKey.FindAllStringIndex(definition, -1)) != 1 {
			return fmt.Errorf("unexpected learner foreign key on %s", table)
		}
		replacement := table + "_v143"
		definition = "CREATE TABLE " + replacement + definition[strings.Index(definition, "("):]
		definition = foreignKey.ReplaceAllString(definition, "")
		if _, err = tx.ExecContext(ctx, definition); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO "+replacement+" SELECT * FROM "+table+"; DROP TABLE "+table+"; ALTER TABLE "+replacement+" RENAME TO "+table); err != nil {
			return err
		}
	}
	for _, object := range objects {
		if _, err = tx.ExecContext(ctx, object.definition); err != nil {
			return fmt.Errorf("restore text solve schema object %s: %w", object.name, err)
		}
	}
	// 已注册实例的删除生命周期保持不变，通用会话不因此伪造一条学生记录。
	if _, err = tx.ExecContext(ctx, `CREATE TRIGGER k12_text_solve_registered_agent_delete AFTER DELETE ON agents BEGIN
 DELETE FROM k12_delivery_receipts WHERE agent_name=OLD.name;
 DELETE FROM k12_delivery_batches WHERE agent_name=OLD.name;
 DELETE FROM k12_print_artifacts WHERE agent_name=OLD.name;
 DELETE FROM k12_text_solve_runs WHERE agent_name=OLD.name;
END`); err != nil {
		return err
	}
	var violations int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil {
		return err
	}
	if violations != 0 {
		return fmt.Errorf("text solve scope migration foreign key violations: %d", violations)
	}
	if err = recordVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
