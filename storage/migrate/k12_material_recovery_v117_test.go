package migrate

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMaterialRecoveryV117PreservesReceiptsAndRollsBack(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "recovery.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var before []Migration
	for _, m := range All {
		if m.Version < 117 {
			before = append(before, m)
		}
	}
	if err = Run(t.Context(), db, before); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO k12_material_manifests VALUES('owner','corpus','doc',1,'source','parser','{}','ready',1);
 INSERT INTO k12_material_preparations(task_id,owner_id,document_id,source_revision,candidate_id,input_digest,candidate_json,policy,state,updated_at) VALUES('task','owner','doc',1,'q','input','{}','policy','outcome_unknown',1);
 INSERT INTO k12_material_invocations VALUES('success','task','solve_generate','generation','provider','succeeded',' original response ','digest',1,2);
 INSERT INTO k12_material_invocations VALUES('unknown','task','solve_verify','verification','provider','outcome_unknown','','empty-digest',3,4);
 CREATE INDEX material_test_index ON k12_material_invocations(operation,status);
 CREATE TRIGGER material_test_identity BEFORE UPDATE OF request_digest ON k12_material_invocations BEGIN SELECT RAISE(ABORT,'request is immutable'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func(query string) [][]any {
		t.Helper()
		rows, e := db.Query(query)
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var out [][]any
		for rows.Next() {
			v := make([]any, len(cols))
			args := make([]any, len(cols))
			for i := range v {
				args[i] = &v[i]
			}
			if e = rows.Scan(args...); e != nil {
				t.Fatal(e)
			}
			out = append(out, v)
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		return out
	}
	const oldCols = `invocation_id,task_id,operation,request_digest,execution_kind,status,result_json,result_digest,created_at,updated_at`
	original := snapshot(`SELECT ` + oldCols + ` FROM k12_material_invocations ORDER BY invocation_id`)
	schema := snapshot(`SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type,name`)
	fail := errors.New("version write failed")
	if err = migrateK12MaterialRecoveryV117(t.Context(), db, func(context.Context, *sql.Tx) error { return fail }); !errors.Is(err, fail) {
		t.Fatalf("rollback error=%v", err)
	}
	if !reflect.DeepEqual(schema, snapshot(`SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type,name`)) || !reflect.DeepEqual(original, snapshot(`SELECT `+oldCols+` FROM k12_material_invocations ORDER BY invocation_id`)) {
		t.Fatal("rollback changed schema or old receipts")
	}
	objects := snapshot(`SELECT type,name,sql FROM sqlite_master WHERE name IN ('material_test_index','material_test_identity') ORDER BY name`)
	if err = Run(t.Context(), db, []Migration{K12MaterialRecoveryV117}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, snapshot(`SELECT `+oldCols+` FROM k12_material_invocations ORDER BY invocation_id`)) || !reflect.DeepEqual(objects, snapshot(`SELECT type,name,sql FROM sqlite_master WHERE name IN ('material_test_index','material_test_identity') ORDER BY name`)) {
		t.Fatal("migration changed old facts or schema objects")
	}
	if _, err = db.Exec(`INSERT INTO k12_material_invocations(invocation_id,task_id,operation,request_digest,execution_kind,status,created_at,updated_at,attempt) VALUES('replacement','task','solve_verify','verification','provider','sent',5,5,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO k12_material_invocations(invocation_id,task_id,operation,request_digest,execution_kind,status,created_at,updated_at) VALUES('duplicate','task','solve_verify','verification','provider','sent',5,5)`); err == nil {
		t.Fatal("original request uniqueness lost")
	}
	if _, err = db.Exec(`UPDATE k12_material_invocations SET request_digest='changed' WHERE invocation_id='unknown'`); err == nil {
		t.Fatal("identity trigger lost")
	}
	var fk, violations, oldAttempt int
	if err = db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT SUM(attempt) FROM k12_material_invocations WHERE invocation_id IN ('success','unknown')`).Scan(&oldAttempt); err != nil {
		t.Fatal(err)
	}
	if fk != 1 || violations != 0 || oldAttempt != 0 {
		t.Fatalf("fk=%d violations=%d old attempts=%d", fk, violations, oldAttempt)
	}
}
