package migrate

// 教学图是服务端生成的冻结资产，与原始作业照片的解码和方向事实分开。
var K12TeachingDiagramV132 = Migration{Version: 132, Description: "K12 immutable teaching diagram assets", SQL: `
CREATE TABLE k12_teaching_diagram_assets (
 owner_scope TEXT NOT NULL,
 agent_name TEXT NOT NULL REFERENCES agents(name) ON DELETE CASCADE,
 asset_id TEXT NOT NULL,
 asset_digest TEXT NOT NULL,
 svg BLOB NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(owner_scope,agent_name,asset_id),
 UNIQUE(agent_name,asset_id)
);
`}
