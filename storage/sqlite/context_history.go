package sqlite

import (
	"context"
	"database/sql"

	"github.com/hexagon-codes/hexclaw/storage"
)

type messageQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listContextMessages(ctx context.Context, db messageQueryer, sessionID string, limit, offset int) ([]*storage.MessageRecord, error) {
	return readMessageRows(ctx, db, `SELECT `+messageCols+` FROM (
SELECT `+messageCols+` FROM messages WHERE session_id=? AND context_compaction_id=''
ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?
) ORDER BY created_at ASC,id ASC`, sessionID, limit, offset)
}

func readMessageRows(ctx context.Context, db messageQueryer, query string, args ...any) ([]*storage.MessageRecord, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []*storage.MessageRecord
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *Store) ListContextMessages(ctx context.Context, sessionID string, limit, offset int) ([]*storage.MessageRecord, error) {
	return listContextMessages(ctx, s.db, sessionID, limit, offset)
}

func (s *txStore) ListContextMessages(ctx context.Context, sessionID string, limit, offset int) ([]*storage.MessageRecord, error) {
	return listContextMessages(ctx, s.tx, sessionID, limit, offset)
}

func (s *txStore) MarkContextCompacted(ctx context.Context, sessionID string, messageIDs []string, summaryID string) error {
	for _, id := range messageIDs {
		if _, err := s.tx.ExecContext(ctx, `UPDATE messages SET context_compaction_id=? WHERE id=? AND session_id=?`, summaryID, id, sessionID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) FindAssistantReplies(ctx context.Context, sessionID, requestID string) ([]*storage.MessageRecord, error) {
	return readMessageRows(ctx, s.db, `SELECT `+messageCols+` FROM messages WHERE session_id=? AND request_id=? AND role='assistant' LIMIT 2`, sessionID, requestID)
}
