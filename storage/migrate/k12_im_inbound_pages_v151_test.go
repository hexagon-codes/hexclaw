package migrate

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestK12IMInboundPagesV151PreservesPreviousReceiptAndChildren(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "before-151.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := t.Context()
	var previous []Migration
	for _, migration := range All {
		if migration.Version < K12IMInboundPagesV151.Version {
			previous = append(previous, migration)
		}
	}
	if err = Run(ctx, db, previous); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "PRAGMA legacy_alter_table=ON"); err != nil {
		t.Fatal(err)
	}
	var originalAssetRoot, currentAssetRoot int
	if err = db.QueryRowContext(ctx, "SELECT rootpage FROM sqlite_master WHERE name='k12_im_inbound_assets'").Scan(&originalAssetRoot); err != nil {
		t.Fatal(err)
	}
	source, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	const command = `{"schema_version":2,"provider":"example","model":"fixture"}`
	if _, err = db.ExecContext(ctx, `INSERT INTO agents(name,display_name) VALUES('learner','learner');
		INSERT INTO k12_im_inbound_receipts VALUES('receipt','owner','learner','binding','dingtalk','instance','chat','physical','digest',?,1,2)`, command); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO k12_im_inbound_assets VALUES('asset','receipt','image.png','image/png',?,'sha',?,1)`, len(source), source); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO k12_im_inbound_dispatches(dispatch_id,receipt_id,processing_status,image_task_id,version,created_at,updated_at)
		VALUES('dispatch','receipt','image_task_submitted','image-task',3,1,2);
		INSERT INTO k12_im_inbound_routing_snapshots(receipt_id,stage,snapshot_digest,created_at,updated_at)
		VALUES('receipt','intent',?,1,2)`, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}

	// 在最后一步中断，页结构、旧内容和连接设置均应回到升级前。
	interrupted := errors.New("interrupted before migration version commit")
	if err = migrateK12IMInboundPagesV151(ctx, db, func(_ context.Context, _ *sql.Tx) error { return interrupted }); !errors.Is(err, interrupted) {
		t.Fatalf("migration rollback: %v", err)
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('k12_im_inbound_receipts') WHERE name='page_count'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("uncommitted migration changed the receipt table: count=%d err=%v", count, err)
	}
	for range 2 {
		if err = Run(ctx, db, []Migration{K12IMInboundPagesV151}); err != nil {
			t.Fatal(err)
		}
	}
	var foreignKeys, index, total, version int
	var raw []byte
	var frozen, snapshot, providerID string
	if err = db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign_keys=%d err=%v", foreignKeys, err)
	}
	if err = db.QueryRowContext(ctx, "PRAGMA legacy_alter_table").Scan(&count); err != nil || count != 1 {
		t.Fatalf("legacy_alter_table=%d err=%v", count, err)
	}
	if err = db.QueryRowContext(ctx, "SELECT rootpage FROM sqlite_master WHERE name='k12_im_inbound_assets'").Scan(&currentAssetRoot); err != nil || currentAssetRoot != originalAssetRoot {
		t.Fatalf("asset storage root page changed from %d to %d, err=%v", originalAssetRoot, currentAssetRoot, err)
	}
	if err = db.QueryRowContext(ctx, `SELECT r.provider_message_id,p.page_index,r.page_count,r.command_json,a.asset_bytes,d.version,s.snapshot_digest
		FROM k12_im_inbound_receipts r JOIN k12_im_inbound_pages p ON p.source_receipt_id=r.receipt_id
		JOIN k12_im_inbound_assets a ON a.receipt_id=p.receipt_id JOIN k12_im_inbound_dispatches d ON d.receipt_id=p.receipt_id
		JOIN k12_im_inbound_routing_snapshots s ON s.receipt_id=p.receipt_id
		WHERE r.receipt_id='receipt' AND a.asset_id='asset' AND d.dispatch_id='dispatch' AND d.image_task_id='image-task'`).
		Scan(&providerID, &index, &total, &frozen, &raw, &version, &snapshot); err != nil {
		t.Fatal(err)
	}
	if providerID != "physical" || index != 0 || total != 1 || frozen != command || !bytes.Equal(raw, source) || version != 3 || snapshot != strings.Repeat("a", 64) {
		t.Fatal("existing message, source bytes, route snapshot or dispatch version changed")
	}
	if _, err = db.ExecContext(ctx, `UPDATE k12_im_inbound_receipts SET page_count=2 WHERE receipt_id='receipt'`); err == nil {
		t.Fatal("persisted page count could be changed")
	}
	// 旧程序显式列写入，按四列物理身份去重。新版库必须同时建立旧单图所需的同 ID 页。
	const oldInsert = `INSERT INTO k12_im_inbound_receipts(receipt_id,owner_scope,agent_name,binding_id,platform,instance_id,
		chat_id,provider_message_id,command_digest,command_json,created_at,updated_at)
		VALUES(?,'owner','learner','binding','dingtalk','instance','chat','later','digest','{}',3,3)
		ON CONFLICT(platform,instance_id,chat_id,provider_message_id) DO NOTHING`
	for _, id := range []string{"later-single", "replayed-id"} {
		if _, err = db.ExecContext(ctx, oldInsert, id); err != nil {
			t.Fatalf("legacy single insert failed: %v", err)
		}
	}
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM k12_im_inbound_pages WHERE receipt_id='later-single' AND
		source_receipt_id='later-single' AND page_index=0`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("legacy single-page mapping count=%d err=%v", count, err)
	}
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&count); err != nil || count != 0 {
		t.Fatalf("foreign key check count=%d err=%v", count, err)
	}
	if _, err = db.ExecContext(ctx, "DELETE FROM agents WHERE name='learner'"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"k12_im_inbound_receipts", "k12_im_inbound_pages", "k12_im_inbound_assets", "k12_im_inbound_dispatches", "k12_im_inbound_routing_snapshots"} {
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s after cascade: count=%d err=%v", table, count, err)
		}
	}
}
