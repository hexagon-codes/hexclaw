package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// K12DeterministicProblemIssueV123 保留原评估，仅允许确定性题干矛盾引用求解回执。
var K12DeterministicProblemIssueV123 = Migration{
	Version:     123,
	Description: "K12 deterministic source contradiction solve-only assessment",
	AtomicFunc:  migrateK12DeterministicProblemIssueV123,
}

func migrateK12DeterministicProblemIssueV123(ctx context.Context, db *sql.DB, recordVersion func(context.Context, *sql.Tx) error) (retErr error) {
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
	const needle = `OR (status = 'out_of_scope' AND grade_invocation_id IS NULL)`
	if strings.Count(ddl, needle) != 1 {
		return fmt.Errorf("V123 unexpected grading assessment schema")
	}
	// 仅增加具体矛盾终态的精确操作集，其他状态及历史结果沿用原 CHECK。
	ddl = strings.Replace(ddl, needle, needle+`
        OR (status = 'untrusted' AND solve_invocation_id IS NOT NULL
            AND grade_invocation_id IS NULL AND parent_guide_invocation_id IS NULL
            AND COALESCE(json_extract(answer_source_json,'$.kind'),'')!='asset'
            AND CASE WHEN json_valid(result_json) THEN
                COALESCE(json_extract(result_json,'$.Solve.problem_issue'),'')='inconsistent_gcd_lcm'
                AND COALESCE(json_extract(result_json,'$.Solve.Evidence.Verdict'),'')='unverifiable'
                AND COALESCE(json_extract(result_json,'$.Solve.Evidence.EvidenceType'),'')='numeric_exec'
                AND COALESCE(json_type(result_json,'$.ParentGuide'),'null')='null'
            ELSE 0 END)
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
	ddl = "CREATE TABLE k12_grading_assessment_items_v123 " + ddl[strings.Index(ddl, "("):]
	for _, statement := range []string{
		ddl,
		`INSERT INTO k12_grading_assessment_items_v123 SELECT * FROM k12_grading_assessment_items`,
		`DROP TABLE k12_grading_assessment_items`,
		`ALTER TABLE k12_grading_assessment_items_v123 RENAME TO k12_grading_assessment_items`,
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
		return fmt.Errorf("V123 foreign-key check found %d conflicts", violations)
	}
	if err := recordVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
