package migrate

// K12AssessmentCorrectionsV108 追加纠正关联，不重建或改写原评估表。
var K12AssessmentCorrectionsV108 = Migration{
	Version:     108,
	Description: "K12 原作答的不可变纠正记录及前驱围栏",
	SQL: `
CREATE TABLE k12_assessment_corrections (
    correction_id TEXT PRIMARY KEY,
    agent_name TEXT NOT NULL,
    job_id TEXT NOT NULL,
    problem_id TEXT NOT NULL,
    input_revision INTEGER NOT NULL CHECK(input_revision>=1),
    correction_revision INTEGER NOT NULL CHECK(correction_revision>=1),
    previous_correction_id TEXT NOT NULL DEFAULT '',
    original_result_digest TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    correction_json TEXT NOT NULL CHECK(json_valid(correction_json)),
    created_at INTEGER NOT NULL,
    UNIQUE(job_id,problem_id,input_revision,correction_revision),
    FOREIGN KEY(job_id,problem_id,input_revision)
        REFERENCES k12_grading_assessment_items(job_id,problem_id,input_revision) ON DELETE CASCADE
);
CREATE INDEX idx_k12_assessment_corrections_scope
    ON k12_assessment_corrections(agent_name,job_id,problem_id,input_revision,correction_revision);
CREATE TRIGGER k12_assessment_corrections_immutable
BEFORE UPDATE ON k12_assessment_corrections
BEGIN SELECT RAISE(ABORT,'assessment corrections are immutable'); END;
`,
}
