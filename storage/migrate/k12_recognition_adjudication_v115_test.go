package migrate

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

func TestK12RecognitionAdjudicationV115PreservesOldReceiptsAndRollsBack(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := t.Context()
	seedK12RecognitionLayoutPlanV76LegacyFixture(t, db)
	if err := applyMigration(ctx, db, K12RecognitionLayoutPlanV76); err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(ctx, db, K12RecognitionReceiptReuseV101); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO k12_model_physical_invocations(physical_invocation_id,parent_invocation_id,agent_name,job_id,stage,physical_unit,request_digest,route_snapshot_json,request_policy_snapshot_json,status,attempt,result_digest,result_content,external_request_id,failure_kind,created_at,updated_at,recognition_plan_version,plan_digest,candidate_exact_set_digest,reused_from_physical_invocation_id)
	 SELECT 'physical-unknown',parent_invocation_id,agent_name,job_id,stage,'segment_5',request_digest,route_snapshot_json,request_policy_snapshot_json,'outcome_unknown',1,'',NULL,'request-unknown','transport_unknown',created_at,updated_at,'v1','','','' FROM k12_model_physical_invocations WHERE physical_invocation_id='physical-v1-whole'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO k12_model_invocations(invocation_id,agent_name,job_id,stage) VALUES('parent-reuse','agent-v76','job-v76','recognizing')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO k12_model_physical_invocations(physical_invocation_id,parent_invocation_id,agent_name,job_id,stage,physical_unit,request_digest,route_snapshot_json,request_policy_snapshot_json,status,attempt,result_digest,result_content,external_request_id,failure_kind,created_at,updated_at,recognition_plan_version,plan_digest,candidate_exact_set_digest,reused_from_physical_invocation_id)
	 SELECT 'physical-reused','parent-reuse',agent_name,job_id,stage,physical_unit,request_digest,route_snapshot_json,request_policy_snapshot_json,status,attempt,result_digest,result_content,'',failure_kind,created_at,updated_at,recognition_plan_version,plan_digest,candidate_exact_set_digest,'physical-v1-whole' FROM k12_model_physical_invocations WHERE physical_invocation_id='physical-v1-whole'`); err != nil {
		t.Fatal(err)
	}

	snapshot := func(query string) [][]any {
		t.Helper()
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var out [][]any
		for rows.Next() {
			values := make([]any, len(cols))
			args := make([]any, len(cols))
			for i := range values {
				args[i] = &values[i]
			}
			if err := rows.Scan(args...); err != nil {
				t.Fatal(err)
			}
			out = append(out, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	beforeRows := snapshot(`SELECT * FROM k12_model_physical_invocations ORDER BY physical_invocation_id`)
	beforeResults := snapshot(`SELECT * FROM k12_problem_source_recognition_physical_results ORDER BY work_id,ordinal`)
	beforeSchema := snapshot(`SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type,name`)
	rollback := errors.New("deliberate version-record failure")
	if err := migrateK12RecognitionAdjudicationV115(ctx, db, func(context.Context, *sql.Tx) error { return rollback }); !errors.Is(err, rollback) {
		t.Fatalf("migration rollback: %v", err)
	}
	if !reflect.DeepEqual(beforeSchema, snapshot(`SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type,name`)) || !reflect.DeepEqual(beforeRows, snapshot(`SELECT * FROM k12_model_physical_invocations ORDER BY physical_invocation_id`)) {
		t.Fatal("failed migration changed schema or receipts")
	}
	beforeObjects := snapshot(`SELECT type,name,sql FROM sqlite_master WHERE type IN ('index','trigger') AND sql IS NOT NULL ORDER BY type,name`)
	if err := applyMigration(ctx, db, K12RecognitionAdjudicationV115); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeRows, snapshot(`SELECT * FROM k12_model_physical_invocations ORDER BY physical_invocation_id`)) || !reflect.DeepEqual(beforeResults, snapshot(`SELECT * FROM k12_problem_source_recognition_physical_results ORDER BY work_id,ordinal`)) {
		t.Fatal("migration changed old physical receipts")
	}
	if !reflect.DeepEqual(beforeObjects, snapshot(`SELECT type,name,sql FROM sqlite_master WHERE type IN ('index','trigger') AND sql IS NOT NULL AND name NOT LIKE '%adjudication%' ORDER BY type,name`)) {
		t.Fatal("migration changed old indexes or triggers")
	}
	var fk, violations int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		t.Fatal(err)
	}
	if fk != 1 || violations != 0 {
		t.Fatalf("foreign keys=%d violations=%d", fk, violations)
	}
	if _, err := db.ExecContext(ctx, `UPDATE k12_model_physical_invocations SET physical_unit='segment_4' WHERE physical_invocation_id='physical-unknown'`); err == nil {
		t.Fatal("old identity trigger was lost")
	}
	if _, err := db.ExecContext(ctx, `UPDATE k12_model_physical_invocations SET reused_from_physical_invocation_id='changed' WHERE physical_invocation_id='physical-unknown'`); err == nil {
		t.Fatal("old reuse provenance trigger was lost")
	}
}
