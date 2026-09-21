package migrate

// KnowledgeReparseV105 将未发布的解析结果与当前可读版本隔离。
var KnowledgeReparseV105 = Migration{
	Version:     105,
	Description: "Isolated knowledge reparse generations",
	SQL: `CREATE TABLE kb_document_reparses (
 job_id TEXT PRIMARY KEY REFERENCES kb_knowledge_jobs(job_id) ON DELETE CASCADE,
 owner_id TEXT NOT NULL,
 corpus_uid TEXT NOT NULL,
 document_id TEXT NOT NULL,
 content_generation INTEGER NOT NULL,
 base_generation INTEGER NOT NULL,
 base_binding_version INTEGER NOT NULL,
 source_digest TEXT NOT NULL,
 parser_version TEXT NOT NULL,
 target_revision_id TEXT NOT NULL DEFAULT '',
 old_document_json TEXT NOT NULL,
 old_chunks_json TEXT NOT NULL,
 prepared_json TEXT NOT NULL DEFAULT '',
 published_at INTEGER,
 created_at INTEGER NOT NULL,
 UNIQUE(corpus_uid,document_id,content_generation),
 FOREIGN KEY(corpus_uid,document_id,content_generation)
 REFERENCES kb_semantic_document_generations(corpus_uid,document_id,content_generation) ON DELETE RESTRICT
);
CREATE TABLE kb_reparse_chunks (
 job_id TEXT NOT NULL REFERENCES kb_document_reparses(job_id) ON DELETE CASCADE,
 id TEXT NOT NULL,
 doc_id TEXT NOT NULL,
 content TEXT NOT NULL,
 chunk_index INTEGER NOT NULL,
 PRIMARY KEY(job_id,id),
 UNIQUE(job_id,chunk_index)
);`,
}
