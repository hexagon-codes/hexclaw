package k12

const PrintReplyArtifactKind = "print_artifact"
const EventConversationPrintReply = "k12.conversation.print_reply"

// PrintReplyArtifact 只标识既有冻结 PDF，不能携带任意路径或替换字节。
type PrintReplyArtifact struct {
	Kind         string `json:"kind"`
	AgentName    string `json:"agent_name"`
	ArtifactID   string `json:"artifact_id"`
	SourceDigest string `json:"source_digest"`
	ByteDigest   string `json:"byte_digest"`
	ByteSize     int64  `json:"byte_size"`
	Name         string `json:"name"`
	URL          string `json:"url"`
}

// ConversationPrintReply 在原助手消息事务内冻结原渠道，恢复不再解析当前绑定。
type ConversationPrintReply struct {
	Owner              string               `json:"owner"`
	AgentName          string               `json:"agent_name"`
	SessionID          string               `json:"session_id"`
	RequestID          string               `json:"request_id"`
	SourceMessageID    string               `json:"source_message_id"`
	AssistantMessageID string               `json:"assistant_message_id"`
	Platform           string               `json:"platform"`
	InstanceID         string               `json:"instance_id"`
	ChatID             string               `json:"chat_id"`
	BindingID          string               `json:"binding_id,omitempty"`
	Locale             string               `json:"locale"`
	Artifacts          []PrintReplyArtifact `json:"artifacts"`
}
