package library

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hexagon-codes/toolkit/util/idgen"
)

// 正文与已批准六科模板同源；不包含孩子、教材单元、生成日期或演示题目。
//
//go:embed k12_unit_prompts.json
var k12UnitPromptSource []byte

const k12UnitPromptVersion = 1

// EnsureK12UnitPrompts 在当前服务安装一次六科命令。仅由场景安装/恢复调用，
// 不按孩子复制；编辑及墓碑记录优先，启用状态不由模板升级覆盖。
func (s *PromptStore) EnsureK12UnitPrompts(ctx context.Context) error {
	var defaults []Prompt
	if err := json.Unmarshal(k12UnitPromptSource, &defaults); err != nil {
		return fmt.Errorf("decode K12 prompts: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range defaults {
		sum := sha256.Sum256([]byte(p.BodyMD))
		digest := hex.EncodeToString(sum[:])
		var promptID, oldDigest string
		var version, modified int
		var deleted sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT prompt_id, installed_version, installed_body_digest, user_modified, deleted_at
		 FROM prompt_builtin_installations WHERE builtin_key=?`, p.BuiltinKey).Scan(&promptID, &version, &oldDigest, &modified, &deleted)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			if modified != 0 || deleted.Valid {
				continue
			}
			var enabled int
			var currentBody string
			if err := tx.QueryRowContext(ctx, `SELECT enabled, body_md FROM prompts WHERE id=?`, promptID).Scan(&enabled, &currentBody); err != nil {
				if err != sql.ErrNoRows {
					return err
				}
				// 记录存在而正文已移除时保留删除语义，不自动补回另一份。
				if _, err := tx.ExecContext(ctx, `UPDATE prompt_builtin_installations SET deleted_at=?,user_modified=1 WHERE builtin_key=?`, time.Now().UTC(), p.BuiltinKey); err != nil {
					return err
				}
				continue
			}
			currentSum := sha256.Sum256([]byte(currentBody))
			if hex.EncodeToString(currentSum[:]) != oldDigest {
				if _, err := tx.ExecContext(ctx, `UPDATE prompt_builtin_installations SET user_modified=1 WHERE builtin_key=?`, p.BuiltinKey); err != nil {
					return err
				}
				continue
			}
			if version >= k12UnitPromptVersion {
				continue
			}
			p.ID = promptID
			if err := upsertPromptRow(ctx, tx, p, enabled, time.Now()); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE prompt_builtin_installations SET installed_version=?,installed_body_digest=? WHERE builtin_key=?`, k12UnitPromptVersion, digest, p.BuiltinKey); err != nil {
				return err
			}
			continue
		}
		p.ID = "pr-" + idgen.ShortID()
		if err := upsertPromptRow(ctx, tx, p, 1, time.Now()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO prompt_builtin_installations
		 (builtin_key,prompt_id,installed_version,installed_body_digest,user_modified,deleted_at) VALUES (?,?,?,?,0,NULL)`,
			p.BuiltinKey, p.ID, k12UnitPromptVersion, digest); err != nil {
			return err
		}
	}
	return tx.Commit()
}
