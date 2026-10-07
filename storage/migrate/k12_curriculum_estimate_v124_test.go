package migrate

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestAutoProgressMigrationPreservesConfirmationAndAllowsEstimate(t *testing.T) {
	db, err := sql.Open("sqlite", "file:auto-progress-migration?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	legacy := make([]Migration, 0)
	for _, m := range All {
		if m.Version <= 123 {
			legacy = append(legacy, m)
		}
	}
	if err := Run(context.Background(), db, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agents(name) VALUES('confirmed-child'),('estimated-child')`); err != nil {
		t.Fatal(err)
	}
	const insert = `INSERT INTO k12_curriculum_progress(progress_id,agent_name,subject,revision,textbook_binding_id,textbook_edition,textbook_version,title,volume,unit_id,unit_title,page_verification_status,evidence_source,confirmed_at,created_at,updated_at) VALUES(?,?,'math',1,'legacy-binding','人教版','2025','fixture','上册','u1','unit','not_requested',?,?,1,2)`
	if _, err := db.Exec(insert, "p-confirmed", "confirmed-child", "parent_confirmed", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO k12_curriculum_progress_revisions VALUES('confirmed-child','math',1,2)`); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), db, All); err != nil {
		t.Fatal(err)
	}
	var source, binding string
	var confirmed, revision int
	if err := db.QueryRow(`SELECT evidence_source,confirmed_at,revision,textbook_binding_id FROM k12_curriculum_progress WHERE agent_name='confirmed-child'`).Scan(&source, &confirmed, &revision, &binding); err != nil {
		t.Fatal(err)
	}
	if source != "parent_confirmed" || confirmed != 1 || revision != 1 || binding != "legacy-binding" {
		t.Fatalf("legacy confirmation changed: %s %d %d %s", source, confirmed, revision, binding)
	}
	if _, err := db.Exec(insert, "p-estimated", "estimated-child", "ai_estimated", 0); err != nil {
		t.Fatalf("AI estimate must be storable without a false confirmation: %v", err)
	}
	if _, err := db.Exec(`UPDATE k12_curriculum_progress SET confirmed_at=1 WHERE agent_name='estimated-child'`); err == nil {
		t.Fatal("estimated progress must not carry a confirmation timestamp")
	}
	if _, err := db.Exec(`UPDATE k12_curriculum_progress SET confirmed_at=0 WHERE agent_name='confirmed-child'`); err == nil {
		t.Fatal("confirmed progress must retain a real confirmation timestamp")
	}
}
