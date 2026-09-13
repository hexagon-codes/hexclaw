package migrate

// KnowledgeUploadDismissalV99 将失败提醒的结束状态与原上传终态分开保存。
var KnowledgeUploadDismissalV99 = Migration{
	Version:     99,
	Description: "Persist knowledge upload reminder dismissal",
	SQL:         `ALTER TABLE kb_upload_operations ADD COLUMN dismissed_at INTEGER;`,
}
