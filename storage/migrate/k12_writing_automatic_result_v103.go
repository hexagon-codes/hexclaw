package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// K12WritingAutomaticResultV103 扩展自动写作结果，不修改原始内容或历史作品。
var K12WritingAutomaticResultV103 = Migration{
	Version:     103,
	Description: "K12 writing bounded review and unreadable result",
	AtomicFunc:  migrateK12WritingAutomaticResultV103,
}

func migrateK12WritingAutomaticResultV103(ctx context.Context, db *sql.DB, recordVersion func(context.Context, *sql.Tx) error) (retErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var foreignKeys int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return err
	}
	// 重建被引用的表时暂时禁用本连接级联，提交前仍检查全部外键。
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		if foreignKeys != 0 {
			if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); retErr == nil && err != nil {
				retErr = fmt.Errorf("restore V103 foreign keys: %w", err)
			}
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name='k12_creative_work_intakes'`).Scan(&ddl); err != nil {
		return fmt.Errorf("read V103 intake schema: %w", err)
	}
	// 从现有定义保留所有已迁移列和约束，只替换两项已知枚举。
	for _, replacement := range [][2]string{
		{"CREATE TABLE k12_creative_work_intakes", "CREATE TABLE k12_creative_work_intakes_v103"},
		{"CHECK(status IN ('preparing','awaiting_confirmation','ready','promoted','failed','cancelled'))", "CHECK(status IN ('preparing','awaiting_confirmation','ready','promoted','failed','cancelled','unreadable'))"},
		{"CHECK(confirmation_provenance IN ('','evidence_auto_freeze','parent_confirmed','parent_corrected'))", "CHECK(confirmation_provenance IN ('','evidence_auto_freeze','parent_confirmed','parent_corrected','evidence_bounded_review'))"},
	} {
		if strings.Count(ddl, replacement[0]) != 1 {
			return fmt.Errorf("V103 intake schema does not contain the expected constraint")
		}
		ddl = strings.Replace(ddl, replacement[0], replacement[1], 1)
	}
	rows, err := tx.QueryContext(ctx, `SELECT sql FROM sqlite_schema WHERE tbl_name='k12_creative_work_intakes' AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY type,name`)
	if err != nil {
		return err
	}
	var dependencies []string
	for rows.Next() {
		var definition string
		if err := rows.Scan(&definition); err != nil {
			rows.Close()
			return err
		}
		dependencies = append(dependencies, definition)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	statements := []string{
		ddl,
		`INSERT INTO k12_creative_work_intakes_v103 SELECT * FROM k12_creative_work_intakes`,
		`DROP TABLE k12_creative_work_intakes`,
		`ALTER TABLE k12_creative_work_intakes_v103 RENAME TO k12_creative_work_intakes`,
	}
	for _, statement := range append(statements, dependencies...) {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("rebuild V103 intake constraints: %w", err)
		}
	}
	var violations int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		return err
	}
	if violations != 0 {
		return fmt.Errorf("V103 foreign key check found %d conflicts", violations)
	}
	if err := recordVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
