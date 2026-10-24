package migrate

// K12ReviewSourceGroupsV136 独立保存经核验的同题来源关联，不改写错题或答案资产。
// 历史源仅在读侧核对，不添加删除约束；沿用既有数据删除策略并保留失效关联。
var K12ReviewSourceGroupsV136 = Migration{Version: 136, Description: "K12 reviewed source groups and immutable member identities", SQL: `
CREATE TABLE IF NOT EXISTS k12_review_source_groups (
    group_id TEXT PRIMARY KEY,
    agent_name TEXT NOT NULL,
    canonical_record_id TEXT NOT NULL,
    canonical_statement TEXT NOT NULL CHECK(length(trim(canonical_statement))>0),
    canonical_statement_digest TEXT NOT NULL CHECK(length(canonical_statement_digest)=64),
    evidence_kind TEXT NOT NULL CHECK(evidence_kind='reviewed_sources'),
    evidence_note TEXT NOT NULL CHECK(length(trim(evidence_note))>0),
    source_digests_json TEXT NOT NULL CHECK(json_valid(source_digests_json) AND json_type(source_digests_json)='array'),
    created_at INTEGER NOT NULL CHECK(created_at>0),
    UNIQUE(agent_name,group_id),
    FOREIGN KEY(agent_name) REFERENCES agents(name) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS k12_review_source_group_members (
    group_id TEXT NOT NULL,
    agent_name TEXT NOT NULL,
    record_id TEXT NOT NULL,
    question_digest TEXT NOT NULL CHECK(length(question_digest)=64),
    job_id TEXT NOT NULL,
    problem_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL,
    confirmed_version INTEGER NOT NULL CHECK(confirmed_version>=1),
    input_revision INTEGER NOT NULL CHECK(input_revision>=1),
    input_digest TEXT NOT NULL CHECK(length(input_digest)>0),
    result_digest TEXT NOT NULL CHECK(length(result_digest)>0),
    page_digest TEXT NOT NULL CHECK(length(page_digest)=64),
    source_number_path_json TEXT NOT NULL CHECK(json_valid(source_number_path_json) AND json_type(source_number_path_json)='array'),
    parent_source_number_path_json TEXT NOT NULL CHECK(json_valid(parent_source_number_path_json) AND json_type(parent_source_number_path_json)='array'),
    PRIMARY KEY(group_id,record_id),
    FOREIGN KEY(agent_name,group_id) REFERENCES k12_review_source_groups(agent_name,group_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_k12_review_source_group_record
    ON k12_review_source_group_members(agent_name,record_id,group_id);
CREATE TRIGGER IF NOT EXISTS k12_review_source_group_immutable
BEFORE UPDATE ON k12_review_source_groups
BEGIN SELECT RAISE(ABORT,'reviewed source groups are immutable'); END;
CREATE TRIGGER IF NOT EXISTS k12_review_source_group_member_immutable
BEFORE UPDATE ON k12_review_source_group_members
BEGIN SELECT RAISE(ABORT,'reviewed source group members are immutable'); END;
`}
