package migrate

// KnowledgeEmbeddingProgressV116 记录最近成功批次所属的任务尝试，不改写总尝试与旧回执。
var KnowledgeEmbeddingProgressV116 = Migration{
	Version:     116,
	Description: "知识语义任务连续无进展重试预算",
	SQL: `ALTER TABLE kb_knowledge_jobs
		ADD COLUMN last_progress_attempt INTEGER NOT NULL DEFAULT 0;`,
}
