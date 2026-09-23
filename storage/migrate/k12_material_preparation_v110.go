package migrate

// K12MaterialPreparationV110 将资料准备与学生作答账本分开保存。
var K12MaterialPreparationV110 = Migration{Version: 110, Description: "K12 资料解析快照、独立准备任务与回执", SQL: `
CREATE TABLE k12_material_manifests (
 owner_id TEXT NOT NULL, corpus_uid TEXT NOT NULL, document_id TEXT NOT NULL,
 source_revision INTEGER NOT NULL, source_digest TEXT NOT NULL, parser_version TEXT NOT NULL,
 manifest_json TEXT NOT NULL CHECK(json_valid(manifest_json)), state TEXT NOT NULL,
 created_at INTEGER NOT NULL, PRIMARY KEY(owner_id,document_id,source_revision)
);
CREATE TABLE k12_material_preparations (
 task_id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, document_id TEXT NOT NULL,
 source_revision INTEGER NOT NULL, candidate_id TEXT NOT NULL, candidate_revision INTEGER NOT NULL DEFAULT 1,
 input_digest TEXT NOT NULL, candidate_json TEXT NOT NULL CHECK(json_valid(candidate_json)),
 policy TEXT NOT NULL, state TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL DEFAULT '{}', result_digest TEXT NOT NULL DEFAULT '',
 asset_id TEXT NOT NULL DEFAULT '', asset_version INTEGER NOT NULL DEFAULT 0,
 updated_at INTEGER NOT NULL,
 UNIQUE(owner_id,document_id,source_revision,candidate_id,candidate_revision),
 FOREIGN KEY(owner_id,document_id,source_revision) REFERENCES k12_material_manifests(owner_id,document_id,source_revision)
);
CREATE INDEX idx_k12_material_preparations_pending ON k12_material_preparations(state,updated_at);
CREATE TABLE k12_material_invocations (
 invocation_id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES k12_material_preparations(task_id),
 operation TEXT NOT NULL, request_digest TEXT NOT NULL, execution_kind TEXT NOT NULL,
 status TEXT NOT NULL, result_json TEXT NOT NULL DEFAULT '', result_digest TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 UNIQUE(task_id,operation,request_digest)
);
`}
