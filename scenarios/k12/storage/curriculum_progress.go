package k12storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// textbookCoverGradeTerm 旧目录只复用同来源的持久封面页，不从上传名称猜年级。
func (s *Store) textbookCoverGradeTerm(ctx context.Context, ownerID, manifestID string) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.content FROM k12_textbook_catalog_jobs j JOIN k12_textbook_manifests m ON m.manifest_id=j.manifest_id JOIN kb_knowledge_jobs k ON k.job_id=j.ingest_job_id AND k.owner_id=m.owner_id AND k.document_id=m.document_id AND k.document_generation=m.document_generation JOIN kb_ingest_page_checkpoints p ON p.job_id=k.job_id AND p.source_digest=m.source_digest WHERE m.manifest_id=? AND m.owner_id=? AND k.state='succeeded' AND p.page_number < (SELECT MIN(pdf_page) FROM k12_textbook_page_mappings WHERE manifest_id=m.manifest_id AND verification_state='verified') ORDER BY p.page_number`, manifestID, ownerID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var text strings.Builder
	for rows.Next() {
		var page string
		if err := rows.Scan(&page); err != nil {
			return "", err
		}
		text.WriteString(page)
		text.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return k12.ParseTextbookGradeTerm(text.String()), nil
}

// SaveCurriculumProgress 用户新命令采用建议或明确进度；CAS 与绑定同事务完成。
func (s *Store) SaveCurriculumProgress(ctx context.Context, scope TextbookScope, profile k12.ChildProfile, progress *k12.CurriculumProgress, expected int, at int64) (*k12.CurriculumProgress, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := ensureAgentRegistered(ctx, tx, scope.AgentName); err != nil {
		return nil, err
	}
	rev, err := revisionVia(ctx, tx, `SELECT revision FROM k12_curriculum_progress_revisions WHERE agent_name=? AND subject=?`, scope.AgentName, scope.Subject)
	if err != nil {
		return nil, err
	}
	if rev != expected {
		return nil, records.ErrVersionConflict
	}
	var metaJSON string
	if err := tx.QueryRowContext(ctx, `SELECT metadata FROM agents WHERE name=?`, scope.AgentName).Scan(&metaJSON); err != nil {
		return nil, err
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		return nil, err
	}
	currentProfile := k12.ProfileFromMeta(meta)
	if currentProfile.GradeTerm != profile.GradeTerm || currentProfile.SubjectTextbooks.Math != profile.SubjectTextbooks.Math {
		return nil, records.ErrVersionConflict
	}
	current, readErr := scanCurriculumProgress(tx.QueryRowContext(ctx, curriculumProgressSelect+` WHERE agent_name=? AND subject=?`, scope.AgentName, scope.Subject))
	if readErr != nil && !errors.Is(readErr, records.ErrNotFound) {
		return nil, readErr
	}
	if readErr == nil && progress != nil && progress.EvidenceSource == k12.CurriculumSourceAIEstimated && current.EvidenceSource == k12.CurriculumSourceParentConfirmed && current.TextbookEdition == profile.SubjectTextbooks.Math && current.Volume == progress.Volume && (current.GradeTerm == "" || current.GradeTerm == profile.GradeTerm) && current.TextbookManifestID == progress.TextbookManifestID {
		return &current, tx.Commit()
	}
	updated, err := writeCurriculumProgressTx(ctx, tx, scope, profile, progress, rev, at)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}
