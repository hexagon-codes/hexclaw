package migrate

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestIMPhotoIntakesV152AddsReceiptAlongsideExistingK12Sources(t *testing.T) {
	ctx := t.Context()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()
	var old []Migration
	for _, migration := range All {
		if migration.Version < IMPhotoIntakesV152.Version {
			old = append(old, migration)
		}
	}
	if err = Run(ctx, db, old); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO agents(name,display_name) VALUES('tutor','Tutor');
		INSERT INTO k12_im_inbound_receipts(receipt_id,owner_scope,agent_name,binding_id,platform,instance_id,chat_id,provider_message_id,command_digest,command_json,created_at,updated_at)
		VALUES('original','owner','tutor','binding','dingtalk','old-bot','old-chat','old-event','original-digest','{}',1,1);
		INSERT INTO k12_im_inbound_assets(asset_id,receipt_id,asset_name,asset_mime,byte_size,content_digest,asset_bytes,created_at)
		VALUES('original-image','original','file.png','image/png',3,'frozen',X'010203',1)`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = Run(ctx, db, All); err != nil {
			t.Fatal(err)
		}
	}
	var originalSize, newReceipts, versionRows int
	if err = db.QueryRowContext(ctx, `SELECT (SELECT length(a.asset_bytes) FROM k12_im_inbound_pages p JOIN k12_im_inbound_assets a ON a.receipt_id=p.receipt_id WHERE p.source_receipt_id='original'),
		(SELECT count(*) FROM im_photo_intakes),(SELECT count(*) FROM schema_migrations WHERE version=152)`).
		Scan(&originalSize, &newReceipts, &versionRows); err != nil || originalSize != 3 || newReceipts != 0 || versionRows != 1 {
		t.Fatalf("upgrade changed legacy photo or created a new notice: size=%d intakes=%d versions=%d err=%v", originalSize, newReceipts, versionRows, err)
	}
	var foreignKeyErrors int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&foreignKeyErrors); err != nil || foreignKeyErrors != 0 {
		t.Fatalf("foreign keys after upgrade: %d %v", foreignKeyErrors, err)
	}
}
