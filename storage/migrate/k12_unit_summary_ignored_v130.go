package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// 非单元语义只终结解析审计；扩展现有状态约束并保留尝试、版本、调用与来源接收序号。
var K12UnitSummaryIgnoredV130 = Migration{Version: 130, Description: "K12 unit summary non-unit ignored terminal", AtomicFunc: migrateK12UnitSummaryIgnoredV130}

func migrateK12UnitSummaryIgnoredV130(ctx context.Context, db *sql.DB, recordVersion func(context.Context, *sql.Tx) error) (retErr error) {
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
			_, e := conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
			if retErr == nil {
				retErr = e
			}
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ddl string
	if err = tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='k12_unit_summary_attempts'").Scan(&ddl); err != nil {
		return err
	}
	if !strings.Contains(ddl, "'ignored'") {
		rows, e := tx.QueryContext(ctx, "SELECT sql FROM sqlite_master WHERE tbl_name='k12_unit_summary_attempts' AND type IN ('index','trigger') AND sql IS NOT NULL")
		if e != nil {
			return e
		}
		definitions := []string{}
		for rows.Next() {
			var definition string
			if e = rows.Scan(&definition); e != nil {
				rows.Close()
				return e
			}
			definitions = append(definitions, definition)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		newDDL := strings.Replace(ddl, "k12_unit_summary_attempts", "k12_unit_summary_attempts_v130", 1)
		if !strings.Contains(newDDL, "'superseded','failed'") {
			return fmt.Errorf("unit summary attempt state schema is unavailable")
		}
		newDDL = strings.Replace(newDDL, "'superseded','failed'", "'superseded','failed','ignored'", 1)
		if _, err = tx.ExecContext(ctx, newDDL); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO k12_unit_summary_attempts_v130 SELECT * FROM k12_unit_summary_attempts; DROP TABLE k12_unit_summary_attempts; ALTER TABLE k12_unit_summary_attempts_v130 RENAME TO k12_unit_summary_attempts"); err != nil {
			return err
		}
		for _, definition := range definitions {
			if _, err = tx.ExecContext(ctx, definition); err != nil {
				return err
			}
		}
	}
	var violations int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil {
		return err
	}
	if violations != 0 {
		return fmt.Errorf("unit summary ignored migration foreign key violations: %d", violations)
	}
	if err = recordVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
