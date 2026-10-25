package migrate

// K12ReviewSourceArchiveV141 保存显式归档恢复选择的来源快照，不恢复执行任务或模型调用。
var K12ReviewSourceArchiveV141 = Migration{Version: 141, Description: "K12 restored review source snapshots", SQL: `
CREATE TABLE IF NOT EXISTS k12_review_source_archive (
    agent_name TEXT NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
    record_id TEXT NOT NULL,
    archive_id TEXT NOT NULL,
    payload TEXT NOT NULL,
    payload_digest TEXT NOT NULL,
    PRIMARY KEY(agent_name,record_id)
);`}
