package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// K12UnclearReferenceAnswerV131 只为未判作答保留独立答案及讲题来源，旧结果逐字保持。
var K12UnclearReferenceAnswerV131 = Migration{
	Version:     131,
	Description: "K12 unclear handwriting independent reference answer",
	AtomicFunc:  migrateK12UnclearReferenceAnswerV131,
}

func migrateK12UnclearReferenceAnswerV131(ctx context.Context, db *sql.DB, recordVersion func(context.Context, *sql.Tx) error) (retErr error) {
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
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='k12_grading_assessment_items'`).Scan(&ddl); err != nil {
		return err
	}
	const guideStatuses = "status IN ('wrong','blank_solved','correct_with_process_issue')"
	const emptyReferences = "solve_invocation_id IS NULL AND grade_invocation_id IS NULL AND answer_source_json='{}')"
	const needle = `OR (status = 'out_of_scope' AND grade_invocation_id IS NULL)`
	if strings.Count(ddl, guideStatuses) != 1 || strings.Count(ddl, emptyReferences) != 1 || strings.Count(ddl, needle) != 1 {
		return fmt.Errorf("V131 unexpected grading assessment schema")
	}
	// 仅新增未判参考解答的成对来源，学生批改和学习副作用仍不可写入。
	ddl = strings.Replace(ddl, guideStatuses, "status IN ('wrong','blank_solved','correct_with_process_issue','answer_unclear')", 1)
	ddl = strings.Replace(ddl, emptyReferences, "solve_invocation_id IS NULL AND grade_invocation_id IS NULL AND parent_guide_invocation_id IS NULL AND answer_source_json='{}')", 1)
	ddl = strings.Replace(ddl, needle, needle+`
        OR (status = 'answer_unclear' AND grade_invocation_id IS NULL
            AND parent_guide_invocation_id IS NOT NULL
            AND (solve_invocation_id IS NOT NULL OR COALESCE(json_extract(answer_source_json,'$.kind'),'')='asset')
            AND projection_created=0 AND projection_record_id='')
`, 1)
	type schemaObject struct{ name, kind, ddl string }
	var objects []schemaObject
	rows, err := tx.QueryContext(ctx, `SELECT name,type,sql FROM sqlite_master WHERE sql IS NOT NULL AND (type='index' AND tbl_name='k12_grading_assessment_items' OR type='trigger') ORDER BY type,name`)
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
	// 关联表触发器会引用原表；重建窗口内暂存，随后逐字恢复。
	for _, object := range objects {
		if object.kind == "trigger" {
			if _, err := tx.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(object.name, `"`, `""`)+`"`); err != nil {
				return err
			}
		}
	}
	ddl = "CREATE TABLE k12_grading_assessment_items_v131 " + ddl[strings.Index(ddl, "("):]
	for _, statement := range []string{
		ddl,
		`INSERT INTO k12_grading_assessment_items_v131 SELECT * FROM k12_grading_assessment_items`,
		`DROP TABLE k12_grading_assessment_items`,
		`ALTER TABLE k12_grading_assessment_items_v131 RENAME TO k12_grading_assessment_items`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	for _, object := range objects {
		if _, err := tx.ExecContext(ctx, object.ddl); err != nil {
			return err
		}
	}
	var violations int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		return err
	}
	if violations != 0 {
		return fmt.Errorf("V131 foreign-key check found %d conflicts", violations)
	}
	if err := recordVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
