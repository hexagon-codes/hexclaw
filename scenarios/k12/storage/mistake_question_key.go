package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// mistakeKeyForWrite 只在本次错题写事务中校正命中的旧自动索引。保留记录 ID、学习版本和来源。
func mistakeKeyForWrite(ctx context.Context, tx dbHandle, r *records.AgentRecord) (string, error) {
	if r.Collection != k12.CollectionMistakes {
		return r.DedupeKey, nil
	}
	var fields k12.MistakeFields
	if err := json.Unmarshal([]byte(r.Fields), &fields); err != nil {
		return "", err
	}
	key, legacy := k12.MistakeQuestionKeys(r.SourceSession, fields.Question)
	if legacy == "" || (r.DedupeKey != key && r.DedupeKey != legacy) {
		return r.DedupeKey, nil
	}
	// RETURNING 与写锁在同一句取得，两个连接不能同时按过期的旧键决定归属。
	var id, question string
	err := tx.QueryRowContext(ctx, `UPDATE k12_mistakes SET dedupe_key=dedupe_key
		WHERE agent_name=? AND source_session_id=? AND dedupe_key=? RETURNING record_id,question`,
		r.AgentName, r.SourceSession, legacy).Scan(&id, &question)
	if errors.Is(err, sql.ErrNoRows) {
		return key, nil
	}
	if err != nil {
		return "", err
	}
	destination, original := k12.MistakeQuestionKeys(r.SourceSession, question)
	if original != legacy {
		if key == legacy {
			return "", fmt.Errorf("%w: mistake key belongs to a different source", records.ErrVersionConflict)
		}
		return key, nil
	}
	if destination == legacy {
		return key, nil
	}
	var existingSession, existingQuestion string
	err = tx.QueryRowContext(ctx, `SELECT source_session_id,question FROM k12_mistakes WHERE agent_name=? AND dedupe_key=?`,
		r.AgentName, destination).Scan(&existingSession, &existingQuestion)
	switch {
	case err == nil:
		if existingSession != r.SourceSession || k12.NormalizeQuestion(existingQuestion) != k12.NormalizeQuestion(question) {
			return "", fmt.Errorf("%w: corrected mistake key belongs to a different source", records.ErrVersionConflict)
		}
		// 两条历史记录各自保留。已有正确索引继续负责匹配，旧行释放占用的错误索引。
		destination += dedupeTombstoneSep + id
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	_, err = tx.ExecContext(ctx, `UPDATE k12_mistakes SET dedupe_key=? WHERE agent_name=? AND record_id=? AND dedupe_key=?`, destination, r.AgentName, id, legacy)
	return key, err
}
