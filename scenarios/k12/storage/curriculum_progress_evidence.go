package k12storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// CurriculumProgressHomeworkCandidate 保留原作业时间与当前批改关联，不将重评时间当作新学习证据。
type CurriculumProgressHomeworkCandidate struct {
	SubmissionID string
	DispatchID   string
	GradingJobID string
	CreatedAt    int64
}

// ListCurriculumProgressHomeworkCandidates 只读同账户同孩子已完成作业的最近候选。
// 读取上限用于控制查询成本；列表外的作业不因此被判定不存在或未完成。
func (s *Store) ListCurriculumProgressHomeworkCandidates(
	ctx context.Context, ownerID, agentName string,
) ([]CurriculumProgressHomeworkCandidate, error) {
	if strings.TrimSpace(ownerID) == "" || strings.TrimSpace(agentName) == "" {
		return nil, fmt.Errorf("curriculum progress homework scope is incomplete")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT h.submission_id,h.dispatch_id,h.grading_job_id,h.created_at
		FROM k12_homework_submissions h
		JOIN k12_image_task_owner_scopes o
		  ON o.dispatch_id=h.dispatch_id AND o.agent_name=h.agent_name
		JOIN k12_image_task_dispatches d
		  ON d.dispatch_id=h.dispatch_id AND d.agent_name=h.agent_name
		 AND d.target_object_type=? AND d.target_object_id=h.submission_id
		JOIN k12_grading_jobs j
		  ON j.record_id=h.grading_job_id AND j.agent_name=h.agent_name
		 AND j.submission_id=h.submission_id
		WHERE o.owner_scope=? AND h.agent_name=? AND h.status=?
		  AND h.task_intent=? AND j.status=?
		ORDER BY h.created_at DESC,h.submission_id DESC LIMIT 20`,
		k12.ImageTaskTargetHomeworkSubmission, ownerID, agentName,
		k12.HomeworkSubmissionCompleted, k12.ImageTaskIntentCompletedHomework,
		k12.GradingStageCompleted)
	if err != nil {
		return nil, fmt.Errorf("list curriculum progress homework candidates: %w", err)
	}
	defer rows.Close()
	out := make([]CurriculumProgressHomeworkCandidate, 0)
	for rows.Next() {
		var candidate CurriculumProgressHomeworkCandidate
		if err := rows.Scan(&candidate.SubmissionID, &candidate.DispatchID, &candidate.GradingJobID, &candidate.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, candidate)
	}
	return out, rows.Err()
}
