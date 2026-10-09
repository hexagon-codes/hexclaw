package migrate

// PromptBuiltinMetadataV126 扩展现有Prompt库并记录内置模板安装/删除语义。
var PromptBuiltinMetadataV126 = Migration{
	Version:     126,
	Description: "Prompt command metadata and builtin installation ledger",
	SQL:         PromptBuiltinMetadataV126DDL,
}

const PromptBuiltinMetadataV126DDL = `
ALTER TABLE prompts ADD COLUMN command TEXT NOT NULL DEFAULT '';
ALTER TABLE prompts ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE prompts ADD COLUMN builtin_key TEXT NOT NULL DEFAULT '';
ALTER TABLE prompts ADD COLUMN scenario TEXT NOT NULL DEFAULT '';
ALTER TABLE prompts ADD COLUMN subject TEXT NOT NULL DEFAULT '';
ALTER TABLE prompts ADD COLUMN task_kind TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX prompts_builtin_key_unique ON prompts(builtin_key) WHERE builtin_key<>'';
CREATE TABLE prompt_builtin_installations (
 builtin_key TEXT PRIMARY KEY,
 prompt_id TEXT NOT NULL UNIQUE,
 installed_version INTEGER NOT NULL CHECK(installed_version>0),
 installed_body_digest TEXT NOT NULL,
 user_modified INTEGER NOT NULL DEFAULT 0 CHECK(user_modified IN (0,1)),
 deleted_at DATETIME
);
`
