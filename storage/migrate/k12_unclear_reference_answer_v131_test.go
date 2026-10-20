package migrate

import (
	"reflect"
	"strings"
	"testing"
)

func TestUnclearReferenceMigrationPreservesHistoryAndUngradedEffects(t *testing.T) {
	db := openV123AssessmentDB(t)
	if err := Run(t.Context(), db, []Migration{K12DeterministicProblemIssueV123}); err != nil {
		t.Fatal(err)
	}
	before, schema := snapshotV123History(t, db), readV77AssessmentSchema(t, db)
	if err := Run(t.Context(), db, []Migration{K12UnclearReferenceAnswerV131}); err != nil {
		t.Fatal(err)
	}
	if got := snapshotV123History(t, db); !reflect.DeepEqual(before, got) {
		t.Fatal("迁移改变历史结果、引用或触发器")
	}
	if got := readV77AssessmentSchema(t, db); !reflect.DeepEqual(schema, got) {
		t.Fatal("迁移改变原列、索引或外键")
	}
	for _, scenario := range []struct {
		name                               string
		solve, grade, guide, effect, valid bool
	}{
		{name: "legacy", valid: true},
		{name: "independent reference", solve: true, guide: true, valid: true},
		{name: "solve without guide", solve: true},
		{name: "guide without solve", guide: true},
		{name: "student grade forbidden", solve: true, grade: true, guide: true},
		{name: "learning effect forbidden", solve: true, guide: true, effect: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			suffix := "v131-" + strings.ReplaceAll(scenario.name, " ", "-")
			seedV77AssessmentParents(t, db, suffix)
			var solve, grade, guide any
			if scenario.solve {
				solve = "solve-" + suffix
			}
			if scenario.grade {
				grade = "grade-" + suffix
			}
			if scenario.guide {
				guide = "guide-" + suffix
			}
			projection, created := "", 0
			if scenario.effect {
				projection, created = "unexpected-mistake", 1
			}
			_, err := db.ExecContext(t.Context(), `INSERT INTO k12_grading_assessment_items(agent_name,job_id,problem_id,attempt_id,confirmed_version,input_digest,status,result_json,result_digest,solve_invocation_id,grade_invocation_id,parent_guide_invocation_id,answer_source_json,projection_record_id,projection_created,projection_status,created_at,updated_at) VALUES('agent','job',?,?,1,'input','answer_unclear','{}','result-digest',?,?,?,'{}',?,?,'committed',21,22)`, "problem-"+suffix, "attempt-"+suffix, solve, grade, guide, projection, created)
			if (err == nil) != scenario.valid {
				t.Fatalf("valid=%v err=%v", scenario.valid, err)
			}
		})
	}
	assertV77ForeignKeysClean(t, db)
}
