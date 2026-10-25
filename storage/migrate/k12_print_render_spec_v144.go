package migrate

// 新渲染规格独立关联不可变文件；旧记录无关联行，保持原身份与PDF字节。
var K12PrintRenderSpecV144 = Migration{Version: 144, Description: "K12 immutable PDF render specification and layout result", SQL: `
CREATE TABLE IF NOT EXISTS k12_print_artifact_render_specs (
 artifact_id TEXT PRIMARY KEY REFERENCES k12_print_artifacts(artifact_id) ON DELETE CASCADE,
 render_spec_digest TEXT NOT NULL,
 render_spec_json TEXT NOT NULL,
 layout_check TEXT NOT NULL
);
CREATE TRIGGER IF NOT EXISTS k12_print_render_spec_immutable
BEFORE UPDATE ON k12_print_artifact_render_specs
BEGIN SELECT RAISE(ABORT, 'print render specification is immutable'); END;
`}
