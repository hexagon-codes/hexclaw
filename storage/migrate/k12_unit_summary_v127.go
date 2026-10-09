package migrate

// K12UnitSummaryV127 保存资料身份、独立尝试与不可变成功快照；PDF仍由既有打印表拥有。
var K12UnitSummaryV127 = Migration{Version: 127, Description: "K12 unit summary durable documents, attempts and revisions", SQL: K12UnitSummaryV127DDL}

const K12UnitSummaryV127DDL = `
CREATE TABLE IF NOT EXISTS k12_unit_summary_documents (
 document_id TEXT PRIMARY KEY,
 owner_id TEXT NOT NULL,
 agent_name TEXT NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
 subject TEXT NOT NULL CHECK(subject IN ('math','chinese','english','science','information_technology','art')),
 scope_key TEXT NOT NULL,
 current_revision_id TEXT REFERENCES k12_unit_summary_revisions(revision_id) ON DELETE SET NULL,
 head_version INTEGER NOT NULL DEFAULT 0 CHECK(head_version>=0),
 next_request_seq INTEGER NOT NULL DEFAULT 0 CHECK(next_request_seq>=0),
 accepted_request_seq INTEGER NOT NULL DEFAULT 0 CHECK(accepted_request_seq>=0 AND accepted_request_seq<=next_request_seq),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 UNIQUE(owner_id,agent_name,scope_key), UNIQUE(document_id,agent_name)
);
CREATE INDEX IF NOT EXISTS idx_k12_unit_documents_owner ON k12_unit_summary_documents(owner_id,agent_name,updated_at,document_id);
CREATE TABLE IF NOT EXISTS k12_unit_summary_attempts (
 attempt_id TEXT PRIMARY KEY,
 owner_id TEXT NOT NULL,
 agent_name TEXT NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
 document_id TEXT REFERENCES k12_unit_summary_documents(document_id) ON DELETE CASCADE,
 session_id TEXT NOT NULL,
 source_message_id TEXT NOT NULL,
 idempotency_key TEXT NOT NULL,
 request_digest TEXT NOT NULL CHECK(length(request_digest)=64),
 request_seq INTEGER,
 state TEXT NOT NULL CHECK(state IN ('resolving','needs_input','queued','generating','reconciling','rendering','publishing','succeeded','reused','superseded','failed')),
 stage TEXT NOT NULL,
 lease_owner TEXT NOT NULL DEFAULT '',
 lease_epoch INTEGER NOT NULL DEFAULT 0,
 lease_expires_at INTEGER NOT NULL DEFAULT 0,
 revision INTEGER NOT NULL DEFAULT 0,
 payload_json TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 UNIQUE(agent_name,idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_k12_unit_attempt_recovery ON k12_unit_summary_attempts(state,lease_expires_at);
CREATE INDEX IF NOT EXISTS idx_k12_unit_attempt_session ON k12_unit_summary_attempts(owner_id,agent_name,session_id,updated_at);
CREATE INDEX IF NOT EXISTS idx_k12_unit_attempt_sequence ON k12_unit_summary_attempts(document_id,request_seq);
CREATE TABLE IF NOT EXISTS k12_unit_summary_model_invocations (
 invocation_id TEXT PRIMARY KEY,
 agent_name TEXT NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
 job_id TEXT NOT NULL REFERENCES k12_unit_summary_attempts(attempt_id) ON DELETE CASCADE,
 stage TEXT NOT NULL,
 request_digest TEXT NOT NULL CHECK(length(request_digest)=64),
 provider TEXT NOT NULL,
 model TEXT NOT NULL,
 route_snapshot_json TEXT NOT NULL,
 request_policy_snapshot_json TEXT NOT NULL DEFAULT '',
 provider_idempotency_key TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL CHECK(status IN ('prepared','sent','succeeded','failed','outcome_unknown','reconciled')),
 attempt INTEGER NOT NULL CHECK(attempt>0),
 result_digest TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL DEFAULT '',
 external_request_id TEXT NOT NULL DEFAULT '',
 failure_kind TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 UNIQUE(job_id,stage,attempt)
);
CREATE TRIGGER IF NOT EXISTS trg_k12_unit_invocation_identity_immutable BEFORE UPDATE OF agent_name,job_id,stage,request_digest,provider,model,route_snapshot_json,request_policy_snapshot_json,attempt ON k12_unit_summary_model_invocations BEGIN SELECT RAISE(ABORT,'k12 unit summary invocation identity is immutable'); END;
CREATE TRIGGER IF NOT EXISTS trg_k12_unit_invocation_result_immutable BEFORE UPDATE OF result_digest,result_json,external_request_id ON k12_unit_summary_model_invocations WHEN OLD.status IN ('succeeded','reconciled') BEGIN SELECT RAISE(ABORT,'k12 unit summary invocation result is immutable'); END;
CREATE TABLE IF NOT EXISTS k12_unit_summary_revisions (
 revision_id TEXT PRIMARY KEY,
 document_id TEXT NOT NULL REFERENCES k12_unit_summary_documents(document_id) ON DELETE CASCADE,
 agent_name TEXT NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
 version INTEGER NOT NULL CHECK(version>0),
 attempt_id TEXT NOT NULL UNIQUE REFERENCES k12_unit_summary_attempts(attempt_id) ON DELETE CASCADE,
 content_digest TEXT NOT NULL CHECK(length(content_digest)=64),
 canonical_markdown TEXT NOT NULL,
 artifact_id TEXT NOT NULL REFERENCES k12_print_artifacts(artifact_id) ON DELETE CASCADE,
 byte_digest TEXT NOT NULL CHECK(length(byte_digest)=64),
 generated_at INTEGER NOT NULL,
 payload_json TEXT NOT NULL,
 UNIQUE(document_id,version), FOREIGN KEY(document_id,agent_name) REFERENCES k12_unit_summary_documents(document_id,agent_name) ON DELETE CASCADE
);
CREATE TRIGGER IF NOT EXISTS trg_k12_unit_summary_revision_immutable BEFORE UPDATE ON k12_unit_summary_revisions BEGIN SELECT RAISE(ABORT,'k12 unit summary revision is immutable'); END;
CREATE TRIGGER IF NOT EXISTS trg_k12_unit_summary_current_scope BEFORE UPDATE OF current_revision_id ON k12_unit_summary_documents WHEN NEW.current_revision_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM k12_unit_summary_revisions r WHERE r.revision_id=NEW.current_revision_id AND r.document_id=NEW.document_id AND r.agent_name=NEW.agent_name) BEGIN SELECT RAISE(ABORT,'k12 unit summary current revision scope differs'); END;
CREATE TABLE IF NOT EXISTS k12_unit_summary_resume_commands (
 attempt_id TEXT NOT NULL REFERENCES k12_unit_summary_attempts(attempt_id) ON DELETE CASCADE,
 command_key TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 applied_revision INTEGER NOT NULL,
 result_json TEXT NOT NULL,
 PRIMARY KEY(attempt_id,command_key)
);
CREATE TABLE IF NOT EXISTS k12_unit_summary_message_projections (
 attempt_id TEXT NOT NULL REFERENCES k12_unit_summary_attempts(attempt_id) ON DELETE CASCADE,
 event_kind TEXT NOT NULL,
 entity_revision INTEGER NOT NULL,
 session_id TEXT NOT NULL,
 message_id TEXT NOT NULL,
 reference_json TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','done')),
 PRIMARY KEY(attempt_id,event_kind,entity_revision)
);
`
