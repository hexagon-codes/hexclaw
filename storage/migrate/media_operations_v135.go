package migrate

var MediaOperationsV135 = Migration{Version: 135, Description: "Media operations, immutable assets and delivery intents", SQL: `
CREATE TABLE IF NOT EXISTS media_operations (
 id TEXT PRIMARY KEY, owner TEXT NOT NULL, idempotency_key TEXT NOT NULL,
 generation_state TEXT NOT NULL, save_state TEXT NOT NULL, revision INTEGER NOT NULL,
 data TEXT NOT NULL, updated_at DATETIME NOT NULL,
 UNIQUE(owner,idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_media_operations_pending ON media_operations(generation_state,save_state,updated_at);
CREATE INDEX IF NOT EXISTS idx_media_operations_request ON media_operations(owner,json_extract(data,'$.request.origin.kind'),json_extract(data,'$.request.origin.session_id'),json_extract(data,'$.request.origin.request_id'));
CREATE TABLE IF NOT EXISTS media_assets (
 id TEXT PRIMARY KEY, operation_id TEXT NOT NULL REFERENCES media_operations(id),
 owner TEXT NOT NULL, data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_media_assets_owner ON media_assets(owner,operation_id);
CREATE TABLE IF NOT EXISTS media_deliveries (
 id TEXT PRIMARY KEY, owner TEXT NOT NULL, message_id TEXT NOT NULL,
 completion_key TEXT NOT NULL, content_digest TEXT NOT NULL,
 platform TEXT NOT NULL, instance_id TEXT NOT NULL, chat_id TEXT NOT NULL,
 state TEXT NOT NULL, external_message_id TEXT NOT NULL DEFAULT '',
	revision INTEGER NOT NULL DEFAULT 1,
 data TEXT NOT NULL, updated_at DATETIME NOT NULL,
 UNIQUE(owner,completion_key,platform,instance_id,chat_id)
);
`}
