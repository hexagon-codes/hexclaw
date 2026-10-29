package migrate

// K12PhotoGradingCacheV147 保存整页结果的输入身份与新来源采用回执，不复制学习事实。
var K12PhotoGradingCacheV147 = Migration{
	Version:     147,
	Description: "K12 exact photo grading result reuse",
	SQL: `
CREATE TABLE k12_photo_grading_cache (
    agent_name TEXT NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
    job_id TEXT NOT NULL REFERENCES k12_grading_jobs(record_id) ON DELETE CASCADE,
    cache_key TEXT NOT NULL,
    artifact_id TEXT NOT NULL DEFAULT '',
    artifact_digest TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    PRIMARY KEY(agent_name,job_id)
);
CREATE INDEX k12_photo_grading_cache_lookup ON k12_photo_grading_cache(agent_name,cache_key,created_at DESC);
CREATE TABLE k12_photo_grading_reuses (
    agent_name TEXT NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    source_job_id TEXT NOT NULL REFERENCES k12_grading_jobs(record_id) ON DELETE CASCADE,
    artifact_id TEXT NOT NULL,
    artifact_digest TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY(agent_name,idempotency_key)
);`,
}
