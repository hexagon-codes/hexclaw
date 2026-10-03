package migrate

// K12TutorContextRefsV109 保存来源题目与后续消息的明确关联，不回填历史猜测。
var K12TutorContextRefsV109 = Migration{
	Version:     109,
	Description: "K12 persistent conversation problem references",
	SQL: `
CREATE TABLE k12_tutor_context_refs (
    owner_scope TEXT NOT NULL,
    agent_name TEXT NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
    conversation_key TEXT NOT NULL,
    message_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('source','followup')),
    job_id TEXT NOT NULL,
    problem_id TEXT NOT NULL,
    input_revision INTEGER NOT NULL,
    result_digest TEXT NOT NULL,
    printed_number TEXT NOT NULL DEFAULT '',
    question_json TEXT NOT NULL CHECK(json_valid(question_json)),
    request_digest TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    PRIMARY KEY(owner_scope,agent_name,conversation_key,message_id,job_id,problem_id,input_revision),
    FOREIGN KEY(job_id,problem_id,input_revision)
        REFERENCES k12_grading_assessment_items(job_id,problem_id,input_revision) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_k12_tutor_followup_message
    ON k12_tutor_context_refs(owner_scope,agent_name,conversation_key,message_id) WHERE kind='followup';
CREATE INDEX idx_k12_tutor_context_conversation
    ON k12_tutor_context_refs(owner_scope,agent_name,conversation_key,kind);
`,
}
