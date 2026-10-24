package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// GetProfileBundleSnapshot 只读同一持久快照，不混用路由内存配置与数据库修订号。
// commandKey 非空时附带该实例的原命令回执；不存在的回执为 nil，不触发重发或写入。
func (s *Store) GetProfileBundleSnapshot(ctx context.Context, agentName, commandKey string) (k12.ProfileBundleSnapshot, error) {
	var out k12.ProfileBundleSnapshot
	var tx *sql.Tx
	var err error
	owned := true
	if bound := s.recordTransaction(ctx); bound != nil {
		if bound.agentName != strings.TrimSpace(agentName) {
			return out, records.ErrNotFound
		}
		tx, owned = bound.tx, false
	} else {
		tx, err = s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return out, err
		}
	}
	if owned {
		defer tx.Rollback()
	}
	var metadata, skills string
	err = tx.QueryRowContext(ctx, `SELECT a.display_name,a.description,a.system_prompt,
	    a.provider,a.model,a.skills,a.metadata,
	    COALESCE((SELECT revision FROM k12_profile_revisions WHERE agent_name=a.name),0)
	    FROM agents a WHERE a.name=?`, strings.TrimSpace(agentName)).Scan(
		&out.AgentConfig.DisplayName, &out.AgentConfig.Description, &out.AgentConfig.SystemPrompt,
		&out.AgentConfig.Provider, &out.AgentConfig.Model, &skills, &metadata, &out.Profile.Revision,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return out, records.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(skills), &out.AgentConfig.Skills); err != nil {
		return out, err
	}
	if out.AgentConfig.Skills == nil {
		out.AgentConfig.Skills = []string{}
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
		return out, err
	}
	profile := k12.ProfileFromMeta(meta)
	out.Profile.ChildName, out.Profile.GradeTerm = profile.ChildName, profile.GradeTerm
	out.Profile.SubjectTextbooks, out.Profile.TextbookEdition = profile.SubjectTextbooks, profile.TextbookEdition
	out.ProgressRevision, err = revisionVia(ctx, tx, `SELECT revision FROM k12_curriculum_progress_revisions WHERE agent_name=? AND subject='math'`, agentName)
	if err != nil {
		return out, err
	}
	progress, err := scanCurriculumProgress(tx.QueryRowContext(ctx, curriculumProgressSelect+` WHERE agent_name=? AND subject='math'`, agentName))
	if err != nil && !errors.Is(err, records.ErrNotFound) {
		return out, err
	}
	if err == nil {
		if progress.Revision != out.ProgressRevision {
			return out, records.ErrVersionConflict
		}
		out.CurriculumProgress = &progress
	}
	out.WeeklyPracticeSettings, err = weeklyPracticeSettingsVia(ctx, tx, agentName)
	if err != nil {
		return out, err
	}
	if commandKey != "" {
		var raw string
		err = tx.QueryRowContext(ctx, `SELECT response_json FROM k12_profile_bundle_commands WHERE agent_name=? AND idempotency_key=?`, agentName, commandKey).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		if err == nil {
			out.Command = &k12.ProfileBundleResult{}
			if err := json.Unmarshal([]byte(raw), out.Command); err != nil {
				return out, err
			}
		}
	}
	if owned {
		return out, tx.Commit()
	}
	return out, nil
}

// GetProfileBundleCommand 在解析当前教材之前核对已提交命令；原请求不因目录更新而失去可重放性。
func (s *Store) GetProfileBundleCommand(ctx context.Context, agentName, commandKey, requestDigest string) (*k12.ProfileBundleResult, error) {
	var digest, raw string
	err := s.db.QueryRowContext(ctx, `SELECT request_digest,response_json FROM k12_profile_bundle_commands WHERE agent_name=? AND idempotency_key=?`, agentName, commandKey).Scan(&digest, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if digest != requestDigest {
		return nil, records.ErrVersionConflict
	}
	var result k12.ProfileBundleResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, err
	}
	return &result, nil
}
