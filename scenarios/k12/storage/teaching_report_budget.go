package k12storage

import (
	"context"
	"fmt"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// FreezeTeachingReportAutomaticBudget 仅在作业首次创建报告任务前冻结更长窗口。
// 已有关联批改任务、已到期窗口及其他图片场景均不能借此续期。
func (s *Store) FreezeTeachingReportAutomaticBudget(ctx context.Context, agent, dispatchID string, now int64) (int64, error) {
	dispatch, err := s.GetImageTaskDispatch(ctx, agent, dispatchID)
	if err != nil {
		return 0, err
	}
	if dispatch.Status != k12.ImageTaskStatusRouted || dispatch.TargetObjectType != k12.ImageTaskTargetHomeworkSubmission || dispatch.AutomaticDeadlineAt <= now {
		return 0, ErrImageTaskInvalidState
	}
	const budget = 600
	deadline := dispatch.AutomaticStartedAt + budget
	if dispatch.AutomaticBudgetSeconds >= budget {
		return dispatch.AutomaticDeadlineAt, nil
	}
	result, err := s.db.ExecContext(ctx, `UPDATE k12_image_task_dispatches
 SET automatic_budget_seconds=?,automatic_deadline_at=?,automatic_remaining_seconds=?,version=version+1,updated_at=?
 WHERE agent_name=? AND dispatch_id=? AND version=? AND status='routed'
 AND target_object_type='homework_submission' AND automatic_deadline_at>?
 AND EXISTS (SELECT 1 FROM k12_homework_submissions AS homework
 WHERE homework.agent_name=k12_image_task_dispatches.agent_name
 AND homework.dispatch_id=k12_image_task_dispatches.dispatch_id
 AND homework.submission_id=k12_image_task_dispatches.target_object_id AND homework.grading_job_id='')`,
		budget, deadline, max(int64(0), deadline-now), now, agent, dispatchID, dispatch.Version, now)
	if err != nil {
		return 0, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return 0, fmt.Errorf("report automatic budget could not be frozen: %w", ErrImageTaskVersionConflict)
	}
	return deadline, nil
}
