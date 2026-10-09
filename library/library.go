// Package library 实现 §11.8 交互层后端：Prompt 库（一库三 type）。
// 用户自管内容，服务端下发，运营增删不发版。
//
// 设计取舍：command 带参走 $ARGUMENTS 纯文本替换（Claude Code 轻做法），
// 严禁演化为命名多槽 + 版本引擎。
//
// 砍薄版（§5）：旧记忆薄版（standing/fact）已并入统一文件记忆（memory.FileMemory），本包不再含记忆。
package library

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/hexagon-codes/toolkit/util/idgen"
)

// ── Prompt 库 ────────────────────────────────────────────────

// Prompt 是一条 Prompt 库条目（type=prompt 普通片段 / type=command 带参命令）。
type Prompt struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"` // prompt | command
	Title       string    `json:"title"`
	Command     string    `json:"command,omitempty"`
	Description string    `json:"description,omitempty"`
	BuiltinKey  string    `json:"builtin_key,omitempty"`
	Scenario    string    `json:"scenario,omitempty"`
	Subject     string    `json:"subject,omitempty"`
	TaskKind    string    `json:"task_kind,omitempty"`
	BodyMD      string    `json:"body_md"`
	ArgsJSON    string    `json:"args_json"`  // command 的轻量参数声明（$ARGUMENTS / 单参）
	ToolScope   string    `json:"tool_scope"` // 召唤时限定的工具范围（逗号分隔）
	Model       string    `json:"model"`      // 召唤时建议模型
	Category    string    `json:"category"`
	Enabled     bool      `json:"enabled"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PromptStore 持久化 Prompt 库。
type PromptStore struct{ db *sql.DB }

const promptColumns = `id, type, title, body_md, args_json, tool_scope, model, category, enabled, updated_at,
 command, description, builtin_key, scenario, subject, task_kind`

// 默认六科按学科顺序呈现，其余条目沿既有最近编辑排序。
const promptOrder = `CASE builtin_key
 WHEN 'k12.unit_summary.math' THEN 0 WHEN 'k12.unit_summary.chinese' THEN 1
 WHEN 'k12.unit_summary.english' THEN 2 WHEN 'k12.unit_summary.science' THEN 3
 WHEN 'k12.unit_summary.information_technology' THEN 4 WHEN 'k12.unit_summary.art' THEN 5
 ELSE 6 END, updated_at DESC, id`

// NewPromptStore 构造 Prompt 库存储。
func NewPromptStore(db *sql.DB) *PromptStore { return &PromptStore{db: db} }

// List 返回全部条目（含禁用），供管理 UI 用。
func (s *PromptStore) List(ctx context.Context) ([]Prompt, error) {
	return s.query(ctx, `SELECT `+promptColumns+` FROM prompts ORDER BY `+promptOrder)
}

// ListEnabled 返回启用条目，供 GET /prompts 服务端下发。
func (s *PromptStore) ListEnabled(ctx context.Context) ([]Prompt, error) {
	return s.query(ctx, `SELECT `+promptColumns+` FROM prompts WHERE enabled = 1 ORDER BY `+promptOrder)
}

func (s *PromptStore) query(ctx context.Context, q string, args ...any) ([]Prompt, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Prompt
	for rows.Next() {
		var p Prompt
		var enabled int
		if err := rows.Scan(&p.ID, &p.Type, &p.Title, &p.BodyMD, &p.ArgsJSON,
			&p.ToolScope, &p.Model, &p.Category, &enabled, &p.UpdatedAt,
			&p.Command, &p.Description, &p.BuiltinKey, &p.Scenario, &p.Subject, &p.TaskKind); err != nil {
			return nil, err
		}
		p.Enabled = enabled == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get 按 id 取单条。
func (s *PromptStore) Get(ctx context.Context, id string) (Prompt, bool, error) {
	list, err := s.query(ctx, `SELECT `+promptColumns+` FROM prompts WHERE id = ?`, id)
	if err != nil {
		return Prompt{}, false, err
	}
	if len(list) == 0 {
		return Prompt{}, false, nil
	}
	return list[0], true, nil
}

// Upsert 创建或更新一条 Prompt（ID 空 → 生成）。type 缺省 "prompt"。返回写入后的 ID。
func (s *PromptStore) Upsert(ctx context.Context, p *Prompt) (string, error) {
	if p.ID == "" {
		p.ID = "pr-" + idgen.ShortID()
	}
	if strings.TrimSpace(p.Type) == "" {
		p.Type = "prompt"
	}
	enabled := 0
	if p.Enabled {
		enabled = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return p.ID, err
	}
	defer tx.Rollback()
	var storedKey string
	err = tx.QueryRowContext(ctx, `SELECT builtin_key FROM prompts WHERE id=?`, p.ID).Scan(&storedKey)
	if err != nil && err != sql.ErrNoRows {
		return p.ID, err
	}
	// 内置身份由持久记录决定；客户端清空或伪造 builtin_key 均不能改变安装保护。
	p.BuiltinKey = storedKey
	if storedKey != "" {
		_, err = tx.ExecContext(ctx, `UPDATE prompt_builtin_installations SET user_modified=1
		 WHERE builtin_key=? AND EXISTS (SELECT 1 FROM prompts WHERE id=? AND
		 (type<>? OR title<>? OR body_md<>? OR args_json<>? OR tool_scope<>? OR model<>? OR category<>?
		 OR command<>? OR description<>? OR scenario<>? OR subject<>? OR task_kind<>?))`,
			storedKey, p.ID, p.Type, p.Title, p.BodyMD, p.ArgsJSON, p.ToolScope, p.Model, p.Category,
			p.Command, p.Description, p.Scenario, p.Subject, p.TaskKind)
		if err != nil {
			return p.ID, err
		}
	}
	if err := upsertPromptRow(ctx, tx, *p, enabled, time.Now()); err != nil {
		return p.ID, err
	}
	return p.ID, tx.Commit()
}

func upsertPromptRow(ctx context.Context, tx *sql.Tx, p Prompt, enabled int, updated time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO prompts (`+promptColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	 ON CONFLICT(id) DO UPDATE SET type=excluded.type, title=excluded.title, body_md=excluded.body_md,
	 args_json=excluded.args_json, tool_scope=excluded.tool_scope, model=excluded.model,
	 category=excluded.category, enabled=excluded.enabled, updated_at=excluded.updated_at,
	 command=excluded.command, description=excluded.description, builtin_key=excluded.builtin_key,
	 scenario=excluded.scenario, subject=excluded.subject, task_kind=excluded.task_kind`,
		p.ID, p.Type, p.Title, p.BodyMD, p.ArgsJSON, p.ToolScope, p.Model, p.Category, enabled, updated,
		p.Command, p.Description, p.BuiltinKey, p.Scenario, p.Subject, p.TaskKind)
	return err
}

// Delete 删除一条 Prompt。
func (s *PromptStore) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 墓碑和删除必须同事务提交；安装恢复不能复活用户删除的默认命令。
	if _, err := tx.ExecContext(ctx, `UPDATE prompt_builtin_installations SET deleted_at=?,user_modified=1 WHERE prompt_id=?`, time.Now().UTC(), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM prompts WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Render 把 command 型 Prompt 的 $ARGUMENTS 占位替换为用户填的参数（纯文本替换，
// 非模板引擎 —— Claude Code 轻做法）。非 command 或无占位时原样返回 body。
func Render(p Prompt, arguments string) string {
	return strings.ReplaceAll(p.BodyMD, "$ARGUMENTS", arguments)
}
