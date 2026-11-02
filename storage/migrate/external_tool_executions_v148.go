package migrate

// ExternalToolExecutionsV148 为外部派发和结果文件提供同一持久身份，不复制父任务状态机。
var ExternalToolExecutionsV148 = Migration{
	Version:     148,
	Description: "External tool physical executions and result manifests",
	SQL: `
CREATE TABLE IF NOT EXISTS external_tool_executions (
    execution_id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL DEFAULT '',
    run_id TEXT NOT NULL DEFAULT '',
    tool_call_id TEXT NOT NULL DEFAULT '',
    agent_name TEXT NOT NULL DEFAULT '',
    dispatch_task_ref TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL,
    logical_call_key TEXT NOT NULL,
    attempt INTEGER NOT NULL CHECK(attempt >= 1),
    parent_execution_id TEXT NOT NULL DEFAULT '',
    model_exchange_id TEXT NOT NULL DEFAULT '',
    model_exchange_ordinal INTEGER NOT NULL DEFAULT 0,
    upstream_call_id TEXT NOT NULL DEFAULT '',
    action_index INTEGER NOT NULL DEFAULT 0,
    tool_name TEXT NOT NULL DEFAULT '',
    server_name TEXT NOT NULL DEFAULT '',
    target_id TEXT NOT NULL DEFAULT '',
    input_digest TEXT NOT NULL,
    browser_generation INTEGER NOT NULL DEFAULT 0,
    policy_profile TEXT NOT NULL DEFAULT '',
    policy_revision INTEGER NOT NULL DEFAULT 0,
    phase TEXT NOT NULL CHECK(phase IN ('prepared','dispatched','observed','unknown')),
    outcome TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    result_manifest_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(result_manifest_json)),
    accepted_response INTEGER NOT NULL DEFAULT 0 CHECK(accepted_response IN (0,1)),
    revision INTEGER NOT NULL DEFAULT 1 CHECK(revision >= 1),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(owner_id,kind,logical_call_key,attempt),
    CHECK(accepted_response=0 OR (kind='model_request' AND model_exchange_id<>'' AND phase='observed'))
);
CREATE INDEX IF NOT EXISTS idx_external_tool_executions_source
    ON external_tool_executions(owner_id,session_id,request_id,created_at);
CREATE INDEX IF NOT EXISTS idx_external_tool_executions_parent
    ON external_tool_executions(owner_id,parent_execution_id,created_at);
CREATE INDEX IF NOT EXISTS idx_external_tool_executions_exchange
    ON external_tool_executions(owner_id,model_exchange_id,attempt);
CREATE INDEX IF NOT EXISTS idx_external_tool_executions_phase
    ON external_tool_executions(owner_id,phase,updated_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_external_tool_executions_accepted_response
    ON external_tool_executions(owner_id,model_exchange_id)
    WHERE kind='model_request' AND accepted_response=1;
`,
}
