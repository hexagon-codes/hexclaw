package migrate

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func snapshotTextSolveScopeRows(t *testing.T, db *sql.DB, tables []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(tables))
	for _, table := range tables {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var content [][]any
		for rows.Next() {
			values, scan := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				scan[i] = &values[i]
			}
			if err = rows.Scan(scan...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for i, value := range values {
				if raw, ok := value.([]byte); ok {
					values[i] = append([]byte(nil), raw...)
				}
			}
			content = append(content, values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil || len(content) != 1 {
			t.Fatalf("expected one retained %s row: count=%d err=%v", table, len(content), err)
		}
		raw, err := json.Marshal(content)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(raw)
		out[table] = hex.EncodeToString(digest[:])
	}
	return out
}

func TestTextSolveScopeMigrationPreservesExecutionPDFAndDelivery(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "scope.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	var before []Migration
	for _, migration := range All {
		if migration.Version < K12TextSolveScopeV143.Version {
			before = append(before, migration)
		}
	}
	if err = Run(t.Context(), db, before); err != nil {
		t.Fatal(err)
	}
	queries := []string{
		`INSERT INTO agents(name) VALUES('existing-child')`,
		`INSERT INTO k12_text_solve_runs VALUES('owner','existing-child','run','input','facts','{"frozen":"run"}',1)`,
		`INSERT INTO k12_text_solve_invocations VALUES('inv','owner','existing-child','run','solve','local_deterministic',1,'request','succeeded','{"frozen":"invocation"}',2,3)`,
		`INSERT INTO k12_text_solve_finalizations VALUES('final','owner','existing-child','run','result','{"frozen":"finalization"}',4)`,
		`INSERT INTO k12_print_artifacts(artifact_id,agent_name,source_kind,source_ref,title,canonical_markdown,source_digest,created_at) VALUES('pdf','existing-child','text_solve_finalization','text_solve:final','原报告','原正文','` + strings.Repeat("a", 64) + `',5)`,
		`INSERT INTO k12_print_artifact_renders VALUES('pdf','pdf','frozen-contract','application/pdf','` + strings.Repeat("b", 64) + `',8,X'255044462D312E37',6)`,
		`INSERT INTO k12_generic_print_jobs(print_job_id,agent_name,idempotency_key,request_digest,artifact_id,prepared_at,created_at,updated_at) VALUES('print','existing-child','print-click','` + strings.Repeat("c", 64) + `','pdf',7,7,7)`,
		`INSERT INTO k12_delivery_batches VALUES('batch','existing-child','im_final_reply','reply','batch-key','content-digest',8,8)`,
		`INSERT INTO k12_delivery_receipts(delivery_id,agent_name,object_kind,object_id,binding_id,platform,chat_id,status,dedupe_key,payload_digest,payload_json,external_message_id,created_at,updated_at,batch_id,batch_ordinal) VALUES('delivery','existing-child','im_final_reply','reply','binding','dingtalk','original-chat','delivered','delivery-key','payload-digest','{"frozen":"delivery"}','provider-message',9,10,'batch',1)`,
	}
	for _, query := range queries {
		if _, err = db.Exec(query); err != nil {
			t.Fatalf("seed existing data: %s: %v", query, err)
		}
	}
	tables := []string{"k12_text_solve_runs", "k12_text_solve_invocations", "k12_text_solve_finalizations", "k12_print_artifacts", "k12_print_artifact_renders", "k12_generic_print_jobs", "k12_delivery_batches", "k12_delivery_receipts"}
	original := snapshotTextSolveScopeRows(t, db, tables)
	if err = Run(t.Context(), db, []Migration{K12TextSolveScopeV143}); err != nil {
		t.Fatal(err)
	}
	if after := snapshotTextSolveScopeRows(t, db, tables); !reflect.DeepEqual(original, after) {
		t.Fatalf("migration changed original rows or PDF bytes: before=%v after=%v", original, after)
	}
	for _, table := range []string{"k12_text_solve_runs", "k12_text_solve_finalizations", "k12_print_artifacts", "k12_print_artifact_renders"} {
		if _, err = db.Exec("UPDATE " + table + " SET created_at=created_at+1"); err == nil {
			t.Fatalf("immutable source %s became mutable", table)
		}
	}
	for _, query := range []string{`SELECT COUNT(*) FROM pragma_foreign_key_check`, `SELECT COUNT(*) FROM pragma_foreign_key_list('k12_text_solve_runs') WHERE "table"='agents'`, `SELECT COUNT(*) FROM pragma_foreign_key_list('k12_print_artifacts') WHERE "table"='agents'`, `SELECT COUNT(*) FROM pragma_foreign_key_list('k12_delivery_batches') WHERE "table"='agents'`, `SELECT COUNT(*) FROM pragma_foreign_key_list('k12_delivery_receipts') WHERE "table"='agents'`} {
		var count int
		if err = db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("unexpected foreign key result: count=%d err=%v", count, err)
		}
	}
	var enabled int
	if err = db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign keys were not restored: %d %v", enabled, err)
	}
	if _, err = db.Exec(`DELETE FROM agents WHERE name='existing-child'`); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		var count int
		if err = db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("registered learner cleanup lost %s: count=%d err=%v", table, count, err)
		}
	}
}
