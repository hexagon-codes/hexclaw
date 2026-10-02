package migrate

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestK12DeterministicProblemIssueV123PreservesHistoryAndExactOperations(t *testing.T) {
	var registered bool
	for _, migration := range All {
		if migration.Version == 123 && migration.AtomicFunc != nil {
			registered = true
		}
	}
	if !registered {
		t.Fatal("V123 atomic migration is not registered")
	}
	db := openV123AssessmentDB(t)
	before := snapshotV123History(t, db)
	schema := readV77AssessmentSchema(t, db)
	if err := Run(t.Context(), db, All); err != nil {
		t.Fatal(err)
	}
	if got := snapshotV123History(t, db); !reflect.DeepEqual(before, got) {
		t.Fatalf("upgrade changed historical results, references, receipts, or schema objects\nbefore=%v\nafter=%v", before, got)
	}
	if got := readV77AssessmentSchema(t, db); !reflect.DeepEqual(schema, got) {
		t.Fatalf("upgrade changed assessment columns, indexes, or foreign keys: before=%+v after=%+v", schema, got)
	}
	const issue = `{"Solve":{"problem_issue":"inconsistent_gcd_lcm","Evidence":{"Verdict":"unverifiable","EvidenceType":"numeric_exec"}}}`
	for _, test := range []struct {
		name   string
		result string
		solve  bool
		grade  bool
		guide  bool
		source string
		valid  bool
	}{
		{name: "known contradiction", result: issue, solve: true, source: "{}", valid: true},
		{name: "ordinary solve only", result: "{}", solve: true, source: "{}"},
		{name: "ordinary graded", result: "{}", solve: true, grade: true, source: "{}", valid: true},
		{name: "missing solve", result: issue, source: "{}"},
		{name: "unsupported issue", result: strings.ReplaceAll(issue, "inconsistent_gcd_lcm", "other"), solve: true, source: "{}"},
		{name: "no numeric proof", result: strings.ReplaceAll(issue, "numeric_exec", "none"), solve: true, source: "{}"},
		{name: "agree verdict", result: strings.ReplaceAll(issue, "unverifiable", "agree"), solve: true, source: "{}"},
		{name: "guide invocation", result: issue, solve: true, guide: true, source: "{}"},
		{name: "invented guide", result: strings.TrimSuffix(issue, "}") + `,"ParentGuide":{"Answer":"24"}}`, solve: true, source: "{}"},
		{name: "asset source", result: issue, solve: true, source: `{"kind":"asset"}`},
		{name: "malformed result", result: "{", solve: true, source: "{}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			suffix := strings.ReplaceAll(test.name, " ", "-")
			seedV77AssessmentParents(t, db, suffix)
			var solve, grade, guide any
			if test.solve {
				solve = "solve-" + suffix
			}
			if test.grade {
				grade = "grade-" + suffix
			}
			if test.guide {
				guide = "guide-" + suffix
			}
			_, err := db.ExecContext(t.Context(), `INSERT INTO k12_grading_assessment_items(agent_name,job_id,problem_id,attempt_id,confirmed_version,input_digest,status,result_json,result_digest,solve_invocation_id,grade_invocation_id,parent_guide_invocation_id,answer_source_json,projection_status,created_at,updated_at) VALUES('agent','job',?,?,1,'input','untrusted',?,'result-digest',?,?,?,?,'committed',21,22)`, "problem-"+suffix, "attempt-"+suffix, test.result, solve, grade, guide, test.source)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
		})
	}
	assertV77ForeignKeysClean(t, db)
	var enabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign keys not restored: enabled=%d err=%v", enabled, err)
	}
	if err := Run(t.Context(), db, All); err != nil {
		t.Fatalf("reapplying registered migrations was not idempotent: %v", err)
	}
}

func TestK12DeterministicProblemIssueV123VersionFailureRollsBack(t *testing.T) {
	db := openV123AssessmentDB(t)
	before := snapshotV123History(t, db)
	schema := readV77AssessmentSchema(t, db)
	forced := errors.New("forced version failure")
	err := migrateK12DeterministicProblemIssueV123(t.Context(), db, func(context.Context, *sql.Tx) error { return forced })
	if !errors.Is(err, forced) {
		t.Fatalf("version failure was lost: %v", err)
	}
	if got := snapshotV123History(t, db); !reflect.DeepEqual(before, got) || !reflect.DeepEqual(schema, readV77AssessmentSchema(t, db)) {
		t.Fatal("failed migration changed historical data or schema objects")
	}
	var staging, enabled int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='k12_grading_assessment_items_v123'`).Scan(&staging); err != nil || staging != 0 {
		t.Fatalf("staging table survived rollback: staging=%d err=%v", staging, err)
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign keys not restored after rollback: enabled=%d err=%v", enabled, err)
	}
	assertV77ForeignKeysClean(t, db)
}

func openV123AssessmentDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openV77LegacyAssessmentDB(t)
	for _, migration := range []Migration{K12ProblemAssetsV107, K12AssessmentCorrectionsV108} {
		if err := applyMigration(t.Context(), db, migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE agents(name TEXT PRIMARY KEY); INSERT INTO agents VALUES('agent')`); err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(t.Context(), db, K12TutorContextRefsV109); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO schema_migrations VALUES(122,'previous schema',1)`,
		`INSERT INTO k12_assessment_corrections VALUES('correction','agent','job','problem-legacy',4,1,'','sha256:legacy','request','{"correction":true}',15)`,
		`INSERT INTO k12_tutor_context_refs VALUES('owner','agent','conversation','message','source','job','problem-legacy',4,'sha256:legacy','8','{}','request',16)`,
		`ALTER TABLE k12_grading_item_invocations ADD COLUMN status TEXT NOT NULL DEFAULT 'succeeded'`,
		`ALTER TABLE k12_grading_item_invocations ADD COLUMN result_digest TEXT NOT NULL DEFAULT 'receipt-digest'`,
		`ALTER TABLE k12_grading_item_invocations ADD COLUMN result_content TEXT`,
		`INSERT INTO k12_grading_item_invocations VALUES('old-unknown','outcome_unknown','',NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed previous assessment relations: %v", err)
		}
	}
	return db
}

func snapshotV123History(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	result := make(map[string][][]any)
	for key, query := range map[string]string{
		"assessments": `SELECT * FROM k12_grading_assessment_items ORDER BY problem_id,input_revision`,
		"corrections": `SELECT * FROM k12_assessment_corrections ORDER BY correction_id`,
		"references":  `SELECT * FROM k12_tutor_context_refs ORDER BY message_id`,
		"receipts":    `SELECT * FROM k12_grading_item_invocations ORDER BY item_invocation_id`,
		"objects":     `SELECT type,name,sql FROM sqlite_master WHERE type IN ('index','trigger') AND sql IS NOT NULL ORDER BY type,name`,
	} {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			args := make([]any, len(columns))
			for index := range values {
				args[index] = &values[index]
			}
			if err := rows.Scan(args...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			result[key] = append(result[key], values)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
	return result
}
