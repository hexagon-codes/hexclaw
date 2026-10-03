package migrate

// BackendIdentityV104 将数据实例身份与数据库一起持久化，重启和升级保持不变。
var BackendIdentityV104 = Migration{
	Version:     104,
	Description: "Persistent backend data identity",
	SQL: `CREATE TABLE IF NOT EXISTS backend_metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
INSERT OR IGNORE INTO backend_metadata(key, value)
VALUES ('backend_id', lower(hex(randomblob(16))));`,
}
