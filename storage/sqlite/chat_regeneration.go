package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/hexagon-codes/hexclaw/storage"
)

func (s *Store) FreezeChatRegeneration(ctx context.Context, owner, sessionID, sourceID string, prefixLimit int) (*storage.ChatRegenerationSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var sourceRow int64
	if err = tx.QueryRowContext(ctx, `SELECT m.rowid FROM messages m JOIN sessions s ON s.id=m.session_id
		WHERE s.id=? AND s.user_id=? AND s.status>=0 AND m.id=? AND m.role='user'`, sessionID, owner, sourceID).Scan(&sourceRow); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = storage.ErrNotFound
		}
		return nil, err
	}
	source, err := scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages WHERE rowid=?`, sourceRow))
	if err != nil {
		return nil, err
	}
	previous, err := scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageCols+` FROM messages
		WHERE session_id=? AND rowid>? AND role IN ('user','assistant') ORDER BY rowid LIMIT 1`, sessionID, sourceRow))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil && previous.Role != "assistant" {
		return nil, storage.ErrChatRegenerationConflict
	}
	if err != nil {
		previous = nil
	}
	// 只使用原用户之前已存在的摘要；晚于该用户的摘要不能进入重生成的上下文。
	prefix, err := readMessageRows(ctx, tx, `SELECT `+messageCols+` FROM (
		SELECT `+messageCols+` FROM messages m WHERE m.session_id=? AND m.rowid<?
		AND NOT EXISTS (SELECT 1 FROM messages c WHERE c.id=m.context_compaction_id AND c.session_id=m.session_id AND c.rowid<?)
		ORDER BY created_at DESC, id DESC LIMIT ?
	) ORDER BY created_at ASC, id ASC`, sessionID, sourceRow, sourceRow, prefixLimit)
	if err != nil {
		return nil, err
	}
	frozen, err := chatRegenerationFingerprint(ctx, tx, sessionID, sourceRow)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &storage.ChatRegenerationSnapshot{
		Source: source, Previous: previous, Prefix: prefix, Invalidate: s.invalidateForkCache,
		Replace: func(ctx context.Context, tx *sql.Tx, reply *storage.MessageRecord) error {
			if reply.SessionID != sessionID || reply.Role != "assistant" {
				return storage.ErrChatRegenerationConflict
			}
			var active int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id=? AND user_id=? AND status>=0`, sessionID, owner).Scan(&active); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					err = storage.ErrChatRegenerationConflict
				}
				return err
			}
			current, err := chatRegenerationFingerprint(ctx, tx, sessionID, sourceRow)
			if err != nil {
				return err
			}
			if current != frozen {
				return storage.ErrChatRegenerationConflict
			}
			// 同一事务先冻结将删除的请求身份。展示历史可替换，已执行身份必须继续阻止重发。
			if _, err = tx.ExecContext(ctx, `INSERT INTO chat_superseded_requests(owner_id,request_id,session_id)
				SELECT ?,request_id,session_id FROM messages WHERE session_id=? AND rowid>?
				AND role='assistant' AND request_id<>'' AND id=request_id||':assistant'
				ON CONFLICT(owner_id,request_id) DO NOTHING`, owner, sessionID, sourceRow); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE messages SET context_compaction_id='' WHERE session_id=? AND rowid<=?
				AND context_compaction_id IN (SELECT id FROM messages WHERE session_id=? AND rowid>?)`, sessionID, sourceRow, sessionID, sourceRow); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE session_id=? AND rowid>?`, sessionID, sourceRow); err != nil {
				return err
			}
			// 新回复插入后，原事务会把它的计数和预览接续到保留的前缀上。
			_, err = tx.ExecContext(ctx, `UPDATE sessions SET
				(message_count,total_prompt_tokens,total_completion_tokens)=(
				 SELECT COUNT(*),COALESCE(SUM(prompt_tokens),0),COALESCE(SUM(completion_tokens),0) FROM messages WHERE session_id=?
				) WHERE id=?`, sessionID, sessionID)
			return err
		},
	}, nil
}

func (s *Store) FindSupersededChatRequest(ctx context.Context, owner, requestID string) (string, error) {
	var sessionID string
	err := s.db.QueryRowContext(ctx, `SELECT session_id FROM chat_superseded_requests
		WHERE owner_id=? AND request_id=?`, owner, requestID).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", storage.ErrNotFound
	}
	return sessionID, err
}

// chatRegenerationFingerprint 校验原用户及待替换尾段的身份、正文和实际执行内容。
// 点赞等仅展示字段不会改变原轮执行输入。
func chatRegenerationFingerprint(ctx context.Context, tx *sql.Tx, sessionID string, sourceRow int64) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT CAST(rowid AS TEXT),id,role,content,metadata,attachments,meta,context_compaction_id
		FROM messages WHERE session_id=? AND rowid>=? ORDER BY rowid`, sessionID, sourceRow)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	for rows.Next() {
		var values [8]sql.NullString
		if err = rows.Scan(&values[0], &values[1], &values[2], &values[3], &values[4], &values[5], &values[6], &values[7]); err != nil {
			return "", err
		}
		if err = encoder.Encode(values); err != nil {
			return "", err
		}
	}
	return string(hash.Sum(nil)), rows.Err()
}
