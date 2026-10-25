package k12storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/storage"
)

// CommitConversationPrintReply 与唯一助手消息共用事务，不写入媒体操作。
func (s *Store) CommitConversationPrintReply(ctx context.Context, tx *sql.Tx, msg *storage.MessageRecord, source k12.ConversationPrintReply) (bool, error) {
	if msg.Role != "assistant" || msg.SessionID != source.SessionID || msg.RequestID != source.RequestID || source.Owner == "" || source.AgentName == "" || len(source.Artifacts) == 0 {
		return false, fmt.Errorf("print reply origin mismatch")
	}
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM sessions WHERE id=?`, source.SessionID).Scan(&owner); err != nil {
		return false, err
	}
	if owner != source.Owner {
		return false, fmt.Errorf("print reply owner mismatch")
	}
	for _, ref := range source.Artifacts {
		artifact, err := getPrintArtifactVia(ctx, tx, source.AgentName, ref.ArtifactID)
		if err != nil {
			return false, err
		}
		frozen, err := getPrintArtifactRenderVia(ctx, tx, source.AgentName, ref.ArtifactID)
		if err != nil {
			return false, err
		}
		sum := sha256.Sum256(frozen.Payload)
		if ref.Kind != k12.PrintReplyArtifactKind || ref.AgentName != source.AgentName || artifact.SourceDigest != ref.SourceDigest || frozen.ByteDigest != ref.ByteDigest || frozen.ByteSize != ref.ByteSize || hex.EncodeToString(sum[:]) != ref.ByteDigest {
			return false, fmt.Errorf("print reply frozen asset mismatch")
		}
	}
	source.AssistantMessageID = msg.ID
	encoded, err := json.Marshal(struct {
		Source  k12.ConversationPrintReply
		Content string
	}{source, msg.Content})
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(encoded)
	digest := hex.EncodeToString(sum[:])
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT metadata FROM messages WHERE id=? AND session_id=? AND role='assistant'`, msg.ID, msg.SessionID).Scan(&previous)
	if err == nil {
		var meta map[string]any
		if json.Unmarshal([]byte(previous), &meta) != nil || meta["print_reply_digest"] != digest {
			return false, fmt.Errorf("print reply completion conflict")
		}
		return true, nil
	}
	if err != sql.ErrNoRows {
		return false, err
	}
	var meta map[string]any
	if json.Unmarshal([]byte(msg.Metadata), &meta) != nil {
		return false, fmt.Errorf("invalid print reply metadata")
	}
	meta["print_reply_digest"] = digest
	encoded, err = json.Marshal(meta)
	if err != nil {
		return false, err
	}
	msg.Metadata = string(encoded)
	if source.Platform != "dingtalk" {
		return false, nil
	}
	if source.InstanceID == "" || source.ChatID == "" || source.SourceMessageID == "" {
		return false, fmt.Errorf("print reply channel identity is incomplete")
	}
	payload, err := json.Marshal(source)
	if err != nil {
		return false, err
	}
	_, err = appendOutboxEvent(ctx, tx, OutboxEvent{EventID: "print-reply:" + msg.ID, AgentName: source.AgentName, AggregateID: msg.ID, EventType: k12.EventConversationPrintReply, PayloadVersion: 1, Payload: string(payload)})
	return false, err
}

// ConversationPrintReplyMessage 只读取事件对应原助手消息，不能用当前聊天正文替代来源。
func (s *Store) ConversationPrintReplyMessage(ctx context.Context, source k12.ConversationPrintReply) (*storage.MessageRecord, error) {
	var content, owner, metadata string
	err := s.db.QueryRowContext(ctx, `SELECT m.content,s.user_id,m.metadata FROM messages m JOIN sessions s ON s.id=m.session_id WHERE m.id=? AND m.session_id=? AND m.request_id=? AND m.role='assistant'`, source.AssistantMessageID, source.SessionID, source.RequestID).Scan(&content, &owner, &metadata)
	if err != nil {
		return nil, err
	}
	if owner != source.Owner {
		return nil, fmt.Errorf("print reply owner mismatch")
	}
	var meta map[string]any
	if json.Unmarshal([]byte(metadata), &meta) != nil || meta["print_reply_digest"] == nil {
		return nil, fmt.Errorf("print reply completion is missing")
	}
	encoded, err := json.Marshal(struct {
		Source  k12.ConversationPrintReply
		Content string
	}{source, content})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(encoded)
	if meta["print_reply_digest"] != hex.EncodeToString(sum[:]) {
		return nil, fmt.Errorf("print reply completion digest mismatch")
	}
	return &storage.MessageRecord{ID: source.AssistantMessageID, SessionID: source.SessionID, Role: "assistant", RequestID: source.RequestID, Content: content, Metadata: metadata}, nil
}

func (s *Store) GetConversationPrintReply(ctx context.Context, agent, messageID string) (k12.ConversationPrintReply, error) {
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload_json FROM outbox_events WHERE event_id=? AND agent_name=? AND event_type=?`, "print-reply:"+messageID, agent, k12.EventConversationPrintReply).Scan(&payload)
	if err != nil {
		return k12.ConversationPrintReply{}, err
	}
	var source k12.ConversationPrintReply
	if json.Unmarshal([]byte(payload), &source) != nil || source.AgentName != agent || source.AssistantMessageID != messageID {
		return source, fmt.Errorf("invalid print reply source")
	}
	return source, nil
}

// 独立文字报告只沿原助手消息回原私聊，不赋予学生记录或任意目标投递权限。
func (s *Store) standalonePrintReplySource(ctx context.Context, batch k12.DeliveryBatch, projection *k12.DeliveryProjectionSource) (*storage.MessageRecord, error) {
	if batch.ObjectKind != "im_final_reply" || projection == nil || projection.PrivateEvent == nil {
		return nil, ErrDeliveryBatchConflict
	}
	source, err := s.GetConversationPrintReply(ctx, batch.AgentName, projection.SourceRef)
	if err != nil {
		return nil, err
	}
	proof := projection.PrivateEvent
	if source.BindingID != "" || source.Owner != source.ChatID || len(source.Artifacts) == 0 ||
		source.AgentName != proof.AgentName || source.Platform != proof.Platform || source.InstanceID != proof.InstanceID ||
		source.Owner != proof.SenderID || source.SourceMessageID != proof.MessageID || source.AssistantMessageID != proof.AssistantMessageID {
		return nil, ErrDeliveryBatchConflict
	}
	message, err := s.ConversationPrintReplyMessage(ctx, source)
	if err != nil {
		return nil, err
	}
	for _, ref := range source.Artifacts {
		artifact, err := s.GetPrintArtifact(ctx, source.AgentName, ref.ArtifactID)
		if err != nil || artifact.SourceDigest != ref.SourceDigest {
			return nil, ErrDeliveryBatchConflict
		}
		final, err := s.textSolvePrintArtifactSource(ctx, artifact)
		if err != nil || final.SourceSessionID != source.SessionID {
			return nil, ErrDeliveryBatchConflict
		}
		if err := s.ValidateTextSolveRequest(ctx, final.TextSolveOrigin, source.Owner, source.RequestID); err != nil {
			return nil, err
		}
	}
	return message, nil
}
