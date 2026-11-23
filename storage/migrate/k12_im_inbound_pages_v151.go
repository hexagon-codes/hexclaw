package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
)

// K12IMInboundPagesV151 将物理消息身份与各页收据关联，保留原有单图写入及恢复合同。
var K12IMInboundPagesV151 = Migration{
	Version:     151,
	Description: "Keep ordered K12 photo pages under their original provider message",
	AtomicFunc:  migrateK12IMInboundPagesV151,
}

func migrateK12IMInboundPagesV151(ctx context.Context, db *sql.DB, recordVersion func(context.Context, *sql.Tx) error) (retErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, conn.Close()) }()
	// 现代 SQLite 重命名父表时会更新子表外键。只搬迁轻量消息收据，图片 BLOB 留在原表。
	for _, setting := range []string{"foreign_keys", "legacy_alter_table"} {
		var enabled bool
		if err = conn.QueryRowContext(ctx, "PRAGMA "+setting).Scan(&enabled); err != nil {
			return err
		}
		if enabled {
			if _, err = conn.ExecContext(ctx, "PRAGMA "+setting+"=OFF"); err != nil {
				return err
			}
			defer func() {
				if _, restoreErr := conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA "+setting+"=ON"); restoreErr != nil {
					_ = conn.Raw(func(any) error { return driver.ErrBadConn })
					retErr = errors.Join(retErr, restoreErr)
				}
			}()
		}
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			retErr = errors.Join(retErr, rollbackErr)
		}
	}()
	var rootDDL string
	if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='k12_im_inbound_receipts'`).Scan(&rootDDL); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT sql FROM sqlite_master WHERE tbl_name='k12_im_inbound_receipts'
		AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY type,name`)
	if err != nil {
		return err
	}
	var dependents []string
	for rows.Next() {
		var definition string
		if err = rows.Scan(&definition); err != nil {
			return errors.Join(err, rows.Close())
		}
		dependents = append(dependents, definition)
	}
	if err = rows.Err(); err != nil {
		return errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE k12_im_inbound_receipts RENAME TO k12_im_inbound_pages`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, rootDDL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO k12_im_inbound_receipts SELECT * FROM k12_im_inbound_pages ORDER BY rowid;
		ALTER TABLE k12_im_inbound_receipts ADD COLUMN page_count INTEGER NOT NULL DEFAULT 1 CHECK(page_count>0);
		DROP TABLE k12_im_inbound_pages;
		CREATE TABLE k12_im_inbound_pages (
			receipt_id TEXT PRIMARY KEY NOT NULL,
			source_receipt_id TEXT NOT NULL REFERENCES k12_im_inbound_receipts(receipt_id) ON DELETE CASCADE,
			page_index INTEGER NOT NULL CHECK(page_index>=0),
			UNIQUE(source_receipt_id,page_index)
		);
		INSERT INTO k12_im_inbound_pages SELECT receipt_id,receipt_id,0 FROM k12_im_inbound_receipts;
		CREATE TRIGGER k12_im_inbound_single_page AFTER INSERT ON k12_im_inbound_receipts WHEN NEW.page_count=1
		BEGIN INSERT INTO k12_im_inbound_pages(receipt_id,source_receipt_id,page_index)
			VALUES(NEW.receipt_id,NEW.receipt_id,0); END;
		CREATE TRIGGER k12_im_inbound_frozen_page_count BEFORE UPDATE OF page_count ON k12_im_inbound_receipts
		WHEN NEW.page_count!=OLD.page_count BEGIN SELECT RAISE(ABORT,'inbound photo page count is immutable'); END;
		CREATE TRIGGER k12_im_inbound_frozen_page BEFORE UPDATE ON k12_im_inbound_pages
		BEGIN SELECT RAISE(ABORT,'inbound photo page identity is immutable'); END;`); err != nil {
		return err
	}
	for _, definition := range dependents {
		if _, err := tx.ExecContext(ctx, definition); err != nil {
			return err
		}
	}
	var violations int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master s WHERE s.type='table'
		AND s.name IN ('k12_im_inbound_assets','k12_im_inbound_dispatches','k12_im_inbound_routing_snapshots')
		AND NOT EXISTS(SELECT 1 FROM pragma_foreign_key_list(s.name) f WHERE f."table"='k12_im_inbound_pages'
			AND f."from"='receipt_id' AND f."to"='receipt_id' AND f.on_delete='CASCADE')`).Scan(&violations); err != nil {
		return err
	}
	if violations != 0 {
		return fmt.Errorf("V151 could not preserve %d inbound photo page references", violations)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check
		WHERE "table" IN ('k12_im_inbound_receipts','k12_im_inbound_pages') OR parent IN ('k12_im_inbound_receipts','k12_im_inbound_pages')`).Scan(&violations); err != nil {
		return err
	}
	if violations != 0 {
		return fmt.Errorf("V151 inbound photo foreign key violations: %d", violations)
	}
	if err := recordVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
