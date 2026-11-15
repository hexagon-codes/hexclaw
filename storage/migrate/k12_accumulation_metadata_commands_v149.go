package migrate

// K12AccumulationMetadataCommandsV149 保留积累创建前的派生调用与结果，不创建占位根对象。
var K12AccumulationMetadataCommandsV149 = Migration{Version: 149, Description: "K12 accumulation metadata durable commands", SQL: `
CREATE TABLE IF NOT EXISTS k12_accumulation_metadata_commands (
 agent_name TEXT NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
 command_key TEXT NOT NULL,
 request_digest TEXT NOT NULL CHECK(length(request_digest)=71),
 grade_term TEXT NOT NULL DEFAULT '',
 policy_version TEXT NOT NULL,
 route_snapshot_json TEXT NOT NULL DEFAULT '',
 request_policy_json TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL CHECK(state IN ('prepared','sent','outcome_unknown','raw_received','metadata_ready','failed')),
 attempt INTEGER NOT NULL CHECK(attempt>0),
 execution_id TEXT NOT NULL DEFAULT '',
 lease_expires_at INTEGER NOT NULL DEFAULT 0,
 raw_response TEXT NOT NULL DEFAULT '',
 raw_digest TEXT NOT NULL DEFAULT '',
 metadata_json TEXT NOT NULL DEFAULT '',
 failure_kind TEXT NOT NULL DEFAULT '',
 attempt_receipts_json TEXT NOT NULL DEFAULT '[]',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(agent_name,command_key)
);
`}
