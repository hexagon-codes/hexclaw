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
	key, previous, oldest := k12.MistakeQuestionIndexVersions(r.SourceSession, fields.Question)
	if r.DedupeKey != key && r.DedupeKey != previous && r.DedupeKey != oldest {
		return r.DedupeKey, nil
	}
	if err := reconcileMistakeIndex(ctx, tx, r, fields.Question, key, previous); err != nil {
		return "", err
	}
	if oldest != previous {
		if err := reconcileMistakeIndex(ctx, tx, r, fields.Question, key, oldest); err != nil {
			return "", err
		}
	}
	return key, nil
}

func reconcileMistakeIndex(ctx context.Context, tx dbHandle, r *records.AgentRecord, source, key, legacy string) error {
	// RETURNING 与写锁在同一句取得，两个连接不能同时按过期的旧键决定归属。
	var id, question string
	err := tx.QueryRowContext(ctx, `UPDATE k12_mistakes SET dedupe_key=dedupe_key
		WHERE agent_name=? AND source_session_id=? AND dedupe_key=? RETURNING record_id,question`,
		r.AgentName, r.SourceSession, legacy).Scan(&id, &question)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if k12.NormalizeQuestion(question) != k12.NormalizeQuestion(source) {
		// 旧索引还可能属于另一个词或变量；其记录继续用原 ID，留待它自己的写入确认。
		return nil
	}
	var existingSession, existingQuestion string
	err = tx.QueryRowContext(ctx, `SELECT source_session_id,question FROM k12_mistakes WHERE agent_name=? AND dedupe_key=?`,
		r.AgentName, key).Scan(&existingSession, &existingQuestion)
	switch {
	case err == nil:
		if existingSession != r.SourceSession || k12.NormalizeQuestion(existingQuestion) != k12.NormalizeQuestion(question) {
			return fmt.Errorf("%w: corrected mistake key belongs to a different source", records.ErrVersionConflict)
		}
		// 两条历史记录各自保留。已有正确索引继续负责匹配，旧行释放占用的错误索引。
		key += dedupeTombstoneSep + id
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE k12_mistakes SET dedupe_key=? WHERE agent_name=? AND record_id=? AND dedupe_key=?`, key, r.AgentName, id, legacy)
	return err
}

// 查询已讲过或做过的题时也按原题核对历史索引，读操作不改学习事实或索引。
func (s *Store) findDuplicateMistake(ctx context.Context, probe *records.AgentRecord) (*records.AgentRecord, error) {
	fields, err := k12.ParseMistakeFields(probe.Fields)
	if err != nil {
		return nil, err
	}
	key, previous, oldest := k12.MistakeQuestionIndexVersions(probe.SourceSession, fields.Question)
	candidates, err := s.queryRecords(ctx, mistakeMapper{}, `WHERE agent_name=? AND source_session_id=?
		AND dedupe_key IN (?,?,?) ORDER BY CASE dedupe_key WHEN ? THEN 0 WHEN ? THEN 1 ELSE 2 END`,
		probe.AgentName, probe.SourceSession, key, previous, oldest, key, previous)
	if err != nil {
		return nil, err
	}
	want := k12.NormalizeQuestion(fields.Question)
	for _, row := range candidates {
		stored, err := k12.ParseMistakeFields(row.Fields)
		if err != nil {
			return nil, err
		}
		if k12.NormalizeQuestion(stored.Question) == want {
			return row, nil
		}
	}
	return nil, records.ErrNotFound
}
