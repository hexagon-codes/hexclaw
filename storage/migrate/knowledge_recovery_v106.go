package migrate

// KnowledgeRecoveryV106 保存未决页的一次替代决定，不改写原调用回执。
var KnowledgeRecoveryV106 = Migration{
	Version:     106,
	Description: "Explicit one-time knowledge OCR recovery",
	SQL: `CREATE TABLE kb_ocr_recovery_decisions (
 original_invocation_id TEXT PRIMARY KEY REFERENCES kb_ingest_page_invocations(invocation_id) ON DELETE CASCADE,
 replacement_job_id TEXT NOT NULL REFERENCES kb_knowledge_jobs(job_id) ON DELETE CASCADE,
 replacement_invocation_id TEXT UNIQUE REFERENCES kb_ingest_page_invocations(invocation_id) ON DELETE CASCADE,
 plan_fingerprint TEXT NOT NULL,
 owner_id TEXT NOT NULL,
 corpus_uid TEXT NOT NULL,
 document_id TEXT NOT NULL,
 content_generation INTEGER NOT NULL,
 source_digest TEXT NOT NULL,
 page_number INTEGER NOT NULL,
 created_at INTEGER NOT NULL,
 consumed_at INTEGER,
 UNIQUE(replacement_job_id,page_number)
);
CREATE INDEX idx_ocr_recovery_job ON kb_ocr_recovery_decisions(replacement_job_id);`,
}
