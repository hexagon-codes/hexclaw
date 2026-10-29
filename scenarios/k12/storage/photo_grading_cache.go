package k12storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// PhotoGradingReuse 关联新来源命令与已完成的原始结果，不创建第二份作答或模型回执。
type PhotoGradingReuse struct {
	JobID          string
	ArtifactID     string
	ArtifactDigest string
	RequestDigest  string
}

func (s *Store) RegisterPhotoGradingCache(ctx context.Context, agent, job, key string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO k12_photo_grading_cache
        (agent_name,job_id,cache_key,created_at)
        SELECT agent_name,record_id,?,? FROM k12_grading_jobs WHERE agent_name=? AND record_id=?`, key, nowUnix(), agent, job)
	return err
}

// CompletePhotoGradingCache 仅在已有完整终稿后发布索引；缓存写入不改变业务完成状态。
func (s *Store) CompletePhotoGradingCache(ctx context.Context, agent, job string, artifact k12.GradingFinalArtifact) error {
	if artifact.AgentName != agent || artifact.JobID != job || artifact.Validate() != nil || artifact.ArtifactDigest != k12.ComputeGradingFinalArtifactDigest(artifact) {
		return fmt.Errorf("invalid photo grading cache artifact")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE k12_photo_grading_cache SET artifact_id=?,artifact_digest=?
        WHERE agent_name=? AND job_id=? AND artifact_id=''
          AND EXISTS(SELECT 1 FROM k12_grading_jobs WHERE agent_name=? AND record_id=? AND status=?)`,
		artifact.ArtifactID, artifact.ArtifactDigest, agent, job, agent, job, k12.GradingStageCompleted)
	return err
}

func (s *Store) FindPhotoGradingCache(ctx context.Context, agent, key string) ([]PhotoGradingReuse, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.job_id,c.artifact_id,c.artifact_digest
        FROM k12_photo_grading_cache c JOIN k12_grading_jobs j ON j.agent_name=c.agent_name AND j.record_id=c.job_id
        WHERE c.agent_name=? AND c.cache_key=? AND c.artifact_id<>'' AND j.status=? ORDER BY c.created_at DESC LIMIT 5`, agent, key, k12.GradingStageCompleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PhotoGradingReuse
	for rows.Next() {
		var item PhotoGradingReuse
		if err := rows.Scan(&item.JobID, &item.ArtifactID, &item.ArtifactDigest); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetPhotoGradingReuse(ctx context.Context, agent, idempotencyKey string) (PhotoGradingReuse, error) {
	var out PhotoGradingReuse
	err := s.db.QueryRowContext(ctx, `SELECT source_job_id,artifact_id,artifact_digest,request_digest
        FROM k12_photo_grading_reuses WHERE agent_name=? AND idempotency_key=?`, agent, idempotencyKey).
		Scan(&out.JobID, &out.ArtifactID, &out.ArtifactDigest, &out.RequestDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return out, records.ErrNotFound
	}
	return out, err
}

func (s *Store) AdoptPhotoGradingCache(ctx context.Context, agent, idempotencyKey, requestDigest string, source PhotoGradingReuse) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 先取得写锁，再核对当前终稿；来源纠正不能在采用检查与写入之间替换结果。
	if _, err := tx.ExecContext(ctx, `UPDATE k12_photo_grading_cache SET cache_key=cache_key WHERE agent_name=? AND job_id=?`, agent, source.JobID); err != nil {
		return err
	}
	current, err := getGradingFinalArtifactByJobVia(ctx, tx, agent, source.JobID)
	if err != nil {
		return err
	}
	if current.ArtifactID != source.ArtifactID || current.ArtifactDigest != source.ArtifactDigest {
		return records.ErrVersionConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO k12_photo_grading_reuses
        (agent_name,idempotency_key,request_digest,source_job_id,artifact_id,artifact_digest,created_at)
        SELECT agent_name,?,?,job_id,artifact_id,artifact_digest,? FROM k12_photo_grading_cache
        WHERE agent_name=? AND job_id=? AND artifact_id=? AND artifact_digest=?`,
		idempotencyKey, requestDigest, nowUnix(), agent, source.JobID, source.ArtifactID, source.ArtifactDigest)
	if err != nil {
		return err
	}
	var stored PhotoGradingReuse
	err = tx.QueryRowContext(ctx, `SELECT source_job_id,artifact_id,artifact_digest,request_digest
        FROM k12_photo_grading_reuses WHERE agent_name=? AND idempotency_key=?`, agent, idempotencyKey).
		Scan(&stored.JobID, &stored.ArtifactID, &stored.ArtifactDigest, &stored.RequestDigest)
	if err != nil {
		return err
	}
	if stored.JobID != source.JobID || stored.ArtifactID != source.ArtifactID || stored.ArtifactDigest != source.ArtifactDigest || stored.RequestDigest != requestDigest {
		return records.ErrVersionConflict
	}
	return tx.Commit()
}
