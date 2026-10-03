package migrate

// KnowledgeSourceManifestV112 保留每个原文件修订的段落、表格和附件关系。
var KnowledgeSourceManifestV112 = Migration{Version: 112, Description: "知识来源结构解析快照", SQL: `
CREATE TABLE kb_ingest_source_manifests (
 owner_id TEXT NOT NULL, document_id TEXT NOT NULL, content_generation INTEGER NOT NULL,
 source_digest TEXT NOT NULL, manifest_json TEXT NOT NULL CHECK(json_valid(manifest_json)),
 created_at INTEGER NOT NULL,
 PRIMARY KEY(owner_id,document_id,content_generation)
);
`}
