package migrate

var DocumentArtifactsV145 = Migration{Version: 145, Description: "Durable conversation document artifacts", SQL: `
CREATE TABLE IF NOT EXISTS document_artifacts (
 id TEXT PRIMARY KEY, owner TEXT NOT NULL, session_id TEXT NOT NULL,
 request_id TEXT NOT NULL, input_digest TEXT NOT NULL,
 data TEXT NOT NULL, payload BLOB NOT NULL,
 created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_document_artifacts_source ON document_artifacts(owner,session_id,request_id);
`}
