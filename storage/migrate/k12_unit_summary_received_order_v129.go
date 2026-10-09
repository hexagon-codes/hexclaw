package migrate

// 接收序号持久且单调，独立于解析完成顺序与VACUUM后的rowid。
var K12UnitSummaryReceivedOrderV129 = Migration{Version: 129, Description: "K12 unit summary durable received sequence", SQL: `
ALTER TABLE k12_unit_summary_attempts ADD COLUMN received_seq INTEGER NOT NULL DEFAULT 0 CHECK(received_seq>=0);
CREATE TABLE k12_unit_summary_received_orders (
 received_seq INTEGER PRIMARY KEY AUTOINCREMENT,
 attempt_id TEXT NOT NULL UNIQUE,
 agent_name TEXT NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
 FOREIGN KEY(attempt_id) REFERENCES k12_unit_summary_attempts(attempt_id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED
);
INSERT INTO k12_unit_summary_received_orders(attempt_id,agent_name) SELECT attempt_id,agent_name FROM k12_unit_summary_attempts ORDER BY created_at,rowid;
UPDATE k12_unit_summary_attempts SET received_seq=(SELECT received_seq FROM k12_unit_summary_received_orders o WHERE o.attempt_id=k12_unit_summary_attempts.attempt_id);
UPDATE k12_unit_summary_attempts SET request_seq=received_seq WHERE document_id IS NOT NULL AND request_seq IS NOT NULL;
UPDATE k12_unit_summary_documents SET next_request_seq=COALESCE((SELECT MAX(received_seq) FROM k12_unit_summary_attempts a WHERE a.document_id=k12_unit_summary_documents.document_id AND a.request_seq IS NOT NULL),0),accepted_request_seq=COALESCE((SELECT MAX(received_seq) FROM k12_unit_summary_attempts a WHERE a.document_id=k12_unit_summary_documents.document_id AND a.request_seq IS NOT NULL AND a.state IN ('succeeded','reused')),0);
CREATE UNIQUE INDEX idx_k12_unit_summary_received_seq ON k12_unit_summary_attempts(received_seq) WHERE received_seq>0;
`}
