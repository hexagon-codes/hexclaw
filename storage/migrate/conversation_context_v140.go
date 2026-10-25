package migrate

// ConversationContextV140 保留完整消息，只从模型窗口排除已摘要内容。
var ConversationContextV140 = Migration{
	Version: 140, Description: "Preserve conversation records outside the compacted model context",
	SQL: `ALTER TABLE messages ADD COLUMN context_compaction_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_messages_active_context ON messages(session_id, created_at DESC, id DESC) WHERE context_compaction_id='';
CREATE INDEX IF NOT EXISTS idx_messages_assistant_request ON messages(session_id, request_id) WHERE role='assistant';`,
}
