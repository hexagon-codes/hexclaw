package migrate

// K12ProblemAssetFeedbackV111 保存明确答案反馈及受影响作答，Outbox 负责恢复处理。
var K12ProblemAssetFeedbackV111 = Migration{
	Version: 111, Description: "K12 题目资产答案反馈与历史纠正进度",
	SQL: `CREATE TABLE k12_problem_asset_feedback (
    feedback_id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    agent_name TEXT NOT NULL,
    dispatch_id TEXT NOT NULL,
    job_id TEXT NOT NULL,
    problem_id TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    request_json TEXT NOT NULL CHECK(json_valid(request_json)),
    targets_json TEXT NOT NULL CHECK(json_valid(targets_json)),
    status TEXT NOT NULL CHECK(status IN ('pending','completed','unresolved','outcome_unknown')),
    outcomes_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(outcomes_json)),
    last_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX idx_k12_problem_asset_feedback_scope ON k12_problem_asset_feedback(owner_id,dispatch_id,problem_id,created_at);`,
}
