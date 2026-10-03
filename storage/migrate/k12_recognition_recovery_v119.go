package migrate

// K12RecognitionRecoveryV119 为原未知识别批次保留独立的一次恢复授权。
var K12RecognitionRecoveryV119 = Migration{Version: 119, Description: "K12 识别批次明确恢复授权", SQL: `
CREATE TABLE IF NOT EXISTS k12_recognition_recovery_authorizations (
 authorization_id TEXT PRIMARY KEY,
 agent_name TEXT NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
 dispatch_id TEXT NOT NULL REFERENCES k12_image_task_dispatches(dispatch_id) ON DELETE CASCADE,
 job_id TEXT NOT NULL REFERENCES k12_grading_jobs(record_id) ON DELETE CASCADE,
 source_parent_id TEXT NOT NULL REFERENCES k12_model_invocations(invocation_id) ON DELETE CASCADE,
 source_physical_id TEXT NOT NULL UNIQUE REFERENCES k12_model_physical_invocations(physical_invocation_id) ON DELETE CASCADE,
 new_parent_id TEXT NOT NULL UNIQUE REFERENCES k12_model_invocations(invocation_id) ON DELETE CASCADE,
 new_physical_id TEXT NOT NULL UNIQUE,
 new_request_digest TEXT NOT NULL,
 page_digest TEXT NOT NULL,
 source_plan_digest TEXT NOT NULL,
 source_request_digest TEXT NOT NULL,
 candidate_exact_set_digest TEXT NOT NULL,
 idempotency_key TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 UNIQUE(agent_name,dispatch_id,idempotency_key)
);
CREATE TRIGGER IF NOT EXISTS k12_recognition_recovery_immutable
BEFORE UPDATE ON k12_recognition_recovery_authorizations
BEGIN SELECT RAISE(ABORT,'recognition recovery authorization is immutable'); END;
`}
