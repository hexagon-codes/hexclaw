package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/storage"
)

func (s *Server) supportsChatRegeneration() bool {
	supported, ok := s.engine.(interface{ SupportsChatRegeneration() bool })
	return s.store != nil && ok && supported.SupportsChatRegeneration()
}

// readChatRegenerationInput 让认证、输入检查与快照恢复都使用同 owner 的原输入。
// 执行前和提交时仍由会话与存储重新冻结、核对同一消息。
func (s *Server) readChatRegenerationInput(ctx context.Context, msg *adapter.Message) error {
	if msg.RegenerateFromMessageID == "" {
		return nil
	}
	if !s.supportsChatRegeneration() {
		return errors.New("conversation regeneration is unavailable")
	}
	if strings.TrimSpace(msg.SessionID) == "" || strings.TrimSpace(msg.Metadata["request_id"]) == "" {
		return errors.New("conversation regeneration requires a session and request identity")
	}
	sess, err := s.store.GetSession(ctx, msg.SessionID)
	if err != nil || sess.UserID != msg.UserID {
		return storage.ErrNotFound
	}
	source, err := s.store.GetMessage(ctx, msg.RegenerateFromMessageID)
	if err != nil || source.SessionID != msg.SessionID || source.Role != "user" {
		return storage.ErrNotFound
	}
	var data struct{ Attachments []adapter.Attachment }
	if source.Metadata != "" {
		if err = json.Unmarshal([]byte(source.Metadata), &data); err != nil {
			return err
		}
	}
	msg.Content, msg.Attachments = source.Content, data.Attachments
	return nil
}
