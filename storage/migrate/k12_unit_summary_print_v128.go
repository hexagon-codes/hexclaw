package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// K12UnitSummaryPrintV128 按最新DDL扩枚举，保留原列、索引、不可变trigger及所有PDF历史。
var K12UnitSummaryPrintV128 = Migration{Version: 128, Description: "K12 unit summary print artifact source kind", AtomicFunc: migrateK12UnitSummaryPrintV128}

func migrateK12UnitSummaryPrintV128(ctx context.Context, db *sql.DB, recordVersion func(context.Context, *sql.Tx) error) (retErr error) {
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
	if err = tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='k12_print_artifacts'").Scan(&ddl); err != nil {
		return err
	}
	if !strings.Contains(ddl, "'unit_summary'") {
		rows, e := tx.QueryContext(ctx, "SELECT sql FROM sqlite_master WHERE tbl_name='k12_print_artifacts' AND type IN ('index','trigger') AND sql IS NOT NULL")
		if e != nil {
			return e
		}
		definitions := []string{}
		for rows.Next() {
			var s string
			if e = rows.Scan(&s); e != nil {
				rows.Close()
				return e
			}
			definitions = append(definitions, s)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		newDDL := strings.Replace(ddl, "k12_print_artifacts", "k12_print_artifacts_v128", 1)
		// 枚举定位沿既有稳定source kind；不复制早期DDL丢失新增列或删除生命周期。
		if !strings.Contains(newDDL, "'tutoring_tips'") {
			return fmt.Errorf("print artifact source-kind schema is unavailable")
		}
		newDDL = strings.Replace(newDDL, "'tutoring_tips'", "'tutoring_tips','unit_summary'", 1)
		if _, err = tx.ExecContext(ctx, newDDL); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO k12_print_artifacts_v128 SELECT * FROM k12_print_artifacts; DROP TABLE k12_print_artifacts; ALTER TABLE k12_print_artifacts_v128 RENAME TO k12_print_artifacts"); err != nil {
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
		return fmt.Errorf("unit summary migration foreign key violations: %d", violations)
	}
	if err = recordVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
