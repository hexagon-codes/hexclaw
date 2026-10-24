package instances

import (
	"context"
	"database/sql"
	"time"
)

// 改名时只归一当前账号生命周期内的名称引用，与账号及路由更新一并提交或回滚。
func preserveInstanceConversationsTx(ctx context.Context, tx *sql.Tx, inst *Instance, nextName string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='sessions')`).Scan(&exists); err != nil || !exists {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,created_at FROM sessions
		WHERE platform=? AND instance_id=? AND NOT EXISTS (
			SELECT 1 FROM platform_instances other WHERE other.id=sessions.instance_id)`, inst.Provider, inst.Name)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		var created time.Time
		if err := rows.Scan(&id, &created); err != nil {
			rows.Close()
			return err
		}
		if !created.Before(inst.CreatedAt) {
			ids = append(ids, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET instance_id=? WHERE id=?`, nextName, id); err != nil {
			return err
		}
	}
	return nil
}

// TokenUsage 是可由稳定账号身份证明的累计实际模型用量，不包含正文估算。
type TokenUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

// TokenUsageByInstance 一次聚合实际成本回执，不按同名账号或发送者补猜旧数据。
func (m *Manager) TokenUsageByInstance(ctx context.Context) (map[string]TokenUsage, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT p.id, SUM(c.prompt_tokens), SUM(c.completion_tokens)
		FROM cost_records c
		JOIN platform_instances p ON p.id = json_extract(
			CASE WHEN json_valid(c.meta) THEN c.meta ELSE '{}' END, '$.platform_instance_id')
			AND p.provider = json_extract(
				CASE WHEN json_valid(c.meta) THEN c.meta ELSE '{}' END, '$.platform')
		GROUP BY p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	usage := make(map[string]TokenUsage)
	for rows.Next() {
		var id string
		var item TokenUsage
		if err := rows.Scan(&id, &item.InputTokens, &item.OutputTokens); err != nil {
			return nil, err
		}
		item.TotalTokens = item.InputTokens + item.OutputTokens
		usage[id] = item
	}
	return usage, rows.Err()
}

// MessageCountsByInstance 统计会话实际消息，兼容现有会话的实例名称引用。
func (m *Manager) MessageCountsByInstance(ctx context.Context) (map[string]int64, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT p.id, p.created_at, s.instance_id, s.created_at, COUNT(m.id)
		FROM platform_instances p
		JOIN sessions s ON s.platform = p.provider AND (
			s.instance_id = p.id OR (s.instance_id = p.name AND NOT EXISTS (
				SELECT 1 FROM platform_instances other WHERE other.id = s.instance_id)))
		LEFT JOIN messages m ON m.session_id = s.id
		GROUP BY p.id, s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int64)
	for rows.Next() {
		var id, reference string
		var accountCreated, sessionCreated time.Time
		var count int64
		if err := rows.Scan(&id, &accountCreated, &reference, &sessionCreated, &count); err != nil {
			return nil, err
		}
		// 名称只兼容当前账号生命周期内的记录，同名重建不能继承旧会话。
		if reference != id && sessionCreated.Before(accountCreated) {
			continue
		}
		counts[id] += count
	}
	return counts, rows.Err()
}
