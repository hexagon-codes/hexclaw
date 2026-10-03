package k12storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// HasCompletedImageTaskHomework 只认可关联批改当前代次的完整冻结终稿。
// 它不修改旧 dispatch 或调用回执，也不触发批改恢复。
func (s *Store) HasCompletedImageTaskHomework(ctx context.Context, agentName, dispatchID string) (bool, error) {
	return hasCompletedImageTaskHomeworkVia(ctx, s.db, agentName, dispatchID)
}

func hasCompletedImageTaskHomeworkVia(ctx context.Context, q dbQueryer, agentName, dispatchID string) (bool, error) {
	artifact, err := scanGradingFinalArtifact(q.QueryRowContext(ctx, `
		SELECT `+gradingFinalArtifactSelectColumns+`
		FROM k12_image_task_dispatches AS dispatch
		JOIN k12_homework_submissions AS homework
		  ON homework.agent_name=dispatch.agent_name AND homework.submission_id=dispatch.target_object_id
		JOIN k12_grading_jobs AS job
		  ON job.agent_name=homework.agent_name AND job.record_id=homework.grading_job_id
		JOIN k12_grading_final_artifacts AS artifact
		  ON artifact.agent_name=job.agent_name AND artifact.job_id=job.record_id
		 AND artifact.finalization_generation=job.finalization_generation
		LEFT JOIN k12_grading_final_artifact_assets AS asset ON asset.artifact_id=artifact.artifact_id
		WHERE dispatch.agent_name=? AND dispatch.dispatch_id=?
		  AND dispatch.target_object_type=? AND job.status=?`,
		agentName, dispatchID, k12.ImageTaskTargetHomeworkSubmission, k12.GradingStageCompleted))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return artifact.Validate() == nil && artifact.ArtifactDigest == k12.ComputeGradingFinalArtifactDigest(artifact), nil
}
