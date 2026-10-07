package usecase

import (
	"context"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type ImageTaskRetryIntent string

const ImageTaskRetryIntentKnownLocalTechnical ImageTaskRetryIntent = "known_local_technical"

type imageTaskKnownLocalTechnicalRetrier interface {
	RetryPhotoGradingJobForKnownLocalTechnical(context.Context, string, string, string, string, int) (GradingJobView, bool, error)
}

// RetryWithIntent 保留普通重试契约；确定本地技术失败只能恢复原父任务及原运行对象。
func (c *ImageTaskCoordinator) RetryWithIntent(
	ctx context.Context, agentName, dispatchID string, expectedVersion int,
	ownerScope string, intent ImageTaskRetryIntent,
) (ImageTaskView, error) {
	if intent == "" {
		return c.Retry(ctx, agentName, dispatchID, expectedVersion)
	}
	if intent != ImageTaskRetryIntentKnownLocalTechnical {
		return ImageTaskView{}, fmt.Errorf("%w: unsupported image task retry intent", ErrInvalidInput)
	}
	if err := c.validate(); err != nil {
		return ImageTaskView{}, err
	}
	dispatch, err := c.Records.GetImageTaskDispatch(ctx, agentName, dispatchID)
	if err != nil {
		return ImageTaskView{}, err
	}
	if dispatch.Version != expectedVersion {
		return ImageTaskView{}, k12storage.ErrImageTaskVersionConflict
	}
	current, err := c.projectTarget(ctx, dispatch)
	if err != nil {
		return ImageTaskView{}, err
	}
	if strings.TrimSpace(ownerScope) == "" || current.Homework == nil || current.Homework.GradingJobID == "" {
		return current, k12storage.ErrImageTaskInvalidState
	}
	runtime, ok := c.Grading.(imageTaskKnownLocalTechnicalRetrier)
	if !ok {
		return current, k12storage.ErrImageTaskInvalidState
	}
	if _, started, err := runtime.RetryPhotoGradingJobForKnownLocalTechnical(
		ctx, agentName, ownerScope, dispatchID, current.Homework.GradingJobID, expectedVersion,
	); err != nil {
		return current, err
	} else if !started {
		return current, k12storage.ErrImageTaskInvalidState
	}
	return c.Get(ctx, agentName, dispatchID)
}

// RetryPhotoGradingJobForKnownLocalTechnical 先恢复原运行对象，在既有调度锁内提交双 CAS 后启动。
// 调度生命周期检查与提交共用锁，关闭中的编排器不得只落库排队而没有运行者。
func (o *GradingOrchestrator) RetryPhotoGradingJobForKnownLocalTechnical(
	ctx context.Context, agentName, ownerScope, dispatchID, jobID string, expectedDispatchVersion int,
) (GradingJobView, bool, error) {
	run, err := o.ensureRun(ctx, jobID)
	if err != nil {
		return GradingJobView{}, false, err
	}
	if run.agentName != agentName {
		return GradingJobView{}, false, k12storage.ErrImageTaskNotFound
	}
	l := o.jobLock(jobID)
	l.Lock()
	defer l.Unlock()
	if run.result != nil {
		return GradingJobView{}, false, k12storage.ErrImageTaskInvalidState
	}
	job, err := o.deps.GetGradingJob(ctx, agentName, jobID)
	if err != nil {
		return GradingJobView{}, false, err
	}
	if job.Record.Status != k12.GradingStageFailedTerminal || job.Fields.FailedStage != k12.GradingStageAssessing ||
		job.Fields.AttemptCount < k12.GradingMaxStageAttempts {
		return job, false, k12storage.ErrImageTaskInvalidState
	}
	now := o.deps.now()
	fields := job.Fields
	fields.ParentAutomaticAttemptID = fmt.Sprintf("%s:%d", dispatchID, now)
	fields.ParentAutomaticDeadlineAt = now + k12.ImageTaskAutomaticBudgetSeconds
	fields.ParentAutomaticRemainingSeconds = k12.ImageTaskAutomaticBudgetSeconds
	if err := (Deps{Records: o.deps.Records, Now: func() int64 { return now }}).setGradingDeadline(ctx, agentName, &fields, k12.GradingStageQueued); err != nil {
		return job, false, err
	}
	started, err := o.startAsyncAfter(jobID, func() error {
		_, record, err := o.deps.Records.RestoreImageTaskKnownLocalTechnical(ctx, k12storage.ImageTaskKnownLocalTechnicalRecovery{
			AgentName: agentName, OwnerScope: ownerScope, DispatchID: dispatchID, JobID: jobID,
			ExpectedDispatchVersion: expectedDispatchVersion, ExpectedJobVersion: job.Record.Version,
			Now: now, QueuedDeadline: fields.Deadline,
		})
		if err != nil {
			return err
		}
		job.Record = record
		job.Fields, err = k12.ParseGradingJobFields(record.Fields)
		return err
	})
	if err != nil {
		return job, false, err
	}
	if !started {
		return job, false, k12storage.ErrImageTaskInvalidState
	}
	return job, true, nil
}
