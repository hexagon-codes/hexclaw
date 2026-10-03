package migrate

// K12FinalSourceCorrectionsV121 保留终稿和已投递身份，完整纠正单独发布。
var K12FinalSourceCorrectionsV121 = Migration{Version: 121, Description: "K12 completed source correction and final revisions", SQL: `
CREATE TABLE k12_final_source_corrections (
 correction_id TEXT PRIMARY KEY,
 agent_name TEXT NOT NULL,
 job_id TEXT NOT NULL,
 problem_id TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 request_json TEXT NOT NULL CHECK(json_valid(request_json)),
 status TEXT NOT NULL CHECK(status IN ('prepared','sent','succeeded','outcome_unknown','failed','completed')),
 response_json TEXT NOT NULL DEFAULT '',
 response_digest TEXT NOT NULL DEFAULT '',
 failure TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 FOREIGN KEY(agent_name,job_id) REFERENCES k12_grading_jobs(agent_name,record_id) ON DELETE CASCADE
);
CREATE INDEX idx_k12_final_source_corrections_target ON k12_final_source_corrections(agent_name,job_id,problem_id);
CREATE TABLE k12_grading_final_revisions (
 artifact_id TEXT PRIMARY KEY,
 agent_name TEXT NOT NULL,
 job_id TEXT NOT NULL,
 previous_artifact_id TEXT NOT NULL,
 correction_id TEXT NOT NULL UNIQUE REFERENCES k12_final_source_corrections(correction_id),
 artifact_json TEXT NOT NULL CHECK(json_valid(artifact_json)),
 asset_owner_scope TEXT NOT NULL,
 source_question_json TEXT NOT NULL CHECK(json_valid(source_question_json)),
 created_at INTEGER NOT NULL,
 FOREIGN KEY(agent_name,job_id) REFERENCES k12_grading_jobs(agent_name,record_id) ON DELETE CASCADE
);
CREATE TABLE k12_grading_final_heads (
 agent_name TEXT NOT NULL,
 job_id TEXT NOT NULL,
 artifact_id TEXT NOT NULL REFERENCES k12_grading_final_revisions(artifact_id),
 PRIMARY KEY(agent_name,job_id),
 FOREIGN KEY(agent_name,job_id) REFERENCES k12_grading_jobs(agent_name,record_id) ON DELETE CASCADE
);
CREATE TRIGGER k12_final_revision_immutable BEFORE UPDATE ON k12_grading_final_revisions
BEGIN SELECT RAISE(ABORT,'final revision is immutable'); END;
`}
