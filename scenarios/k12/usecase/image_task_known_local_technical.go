package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assetstore"
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
	if job.Record.Status != k12.GradingStageFailedTerminal ||
		(job.Fields.FailedStage != k12.GradingStageAssessing && job.Fields.FailedStage != k12.GradingStageRecognizing) {
		return job, false, k12storage.ErrImageTaskInvalidState
	}
	proof, err := o.preAssessmentTechnicalRecoveryProof(ctx, run, job, ownerScope)
	if err != nil {
		return job, false, err
	}
	if job.Fields.AttemptCount < k12.GradingMaxStageAttempts && proof == nil {
		return job, false, k12storage.ErrImageTaskInvalidState
	}
	now := o.deps.now()
	fields := job.Fields
	fields.ParentAutomaticAttemptID = fmt.Sprintf("%s:%d", dispatchID, now)
	dispatch, err := o.deps.Records.GetImageTaskDispatch(ctx, agentName, dispatchID)
	if err != nil {
		return GradingJobView{}, false, err
	}
	fields.ParentAutomaticDeadlineAt = now + int64(dispatch.AutomaticBudgetSeconds)
	fields.ParentAutomaticRemainingSeconds = k12.ImageTaskAutomaticBudgetSeconds
	if err := (Deps{Records: o.deps.Records, Now: func() int64 { return now }}).setGradingDeadline(ctx, agentName, &fields, k12.GradingStageQueued); err != nil {
		return job, false, err
	}
	started, err := o.startAsyncAfter(jobID, func() error {
		_, record, err := o.deps.Records.RestoreImageTaskKnownLocalTechnical(ctx, k12storage.ImageTaskKnownLocalTechnicalRecovery{
			AgentName: agentName, OwnerScope: ownerScope, DispatchID: dispatchID, JobID: jobID,
			ExpectedDispatchVersion: expectedDispatchVersion, ExpectedJobVersion: job.Record.Version,
			Now: now, QueuedDeadline: fields.Deadline,
			PreAssessment: proof,
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

func (o *GradingOrchestrator) preAssessmentTechnicalRecoveryProof(
	ctx context.Context, run *gradingRun, job GradingJobView, ownerScope string,
) (*k12storage.ImageTaskPreAssessmentRecoveryProof, error) {
	if job.Fields.FailedStage != k12.GradingStageAssessing || job.Fields.FailureKind != "assess_item_invalid" {
		return nil, nil
	}
	items, err := o.deps.Records.ListGradingItemInvocations(ctx, run.agentName, job.Record.RecordID)
	if err != nil {
		return nil, err
	}
	if len(items) > 0 {
		return nil, nil
	}
	invalid := k12storage.ErrImageTaskInvalidState
	if run.textOnly || run.req.TeachingReport == nil || run.req.TaskIntent != PhotoTaskCompletedHomework ||
		job.Fields.AttemptCount < 1 || !job.Fields.BudgetSnapshot.IsFrozen() ||
		!photoSubmissionMatchesImage(job.Fields.SubmissionID, run.req.Image) {
		return nil, invalid
	}
	if _, err := run.req.TeachingReport.Bundle.Resolve(); err != nil {
		return nil, invalid
	}
	if _, err := o.deps.Records.GetReadyPageAsset(ctx, ownerScope, run.agentName, run.req.SourcePageAssetID); err != nil {
		return nil, invalid
	}
	assetAgent, file, err := assetstore.Parse(run.req.SourcePageAssetID)
	if err != nil || assetAgent != run.agentName {
		return nil, invalid
	}
	image, _, err := assetstore.Read(assetAgent, file)
	if err != nil || !bytes.Equal(image, run.req.Image) {
		return nil, invalid
	}
	snapshot, err := o.deps.Records.GetProblemAttemptSnapshot(ctx, run.agentName, job.Fields.SubmissionID)
	if err != nil {
		return nil, err
	}
	questions, err := RecognizedQuestionsFromProblemAttemptSnapshotForInitialReadMode(snapshot, run.req.InitialReadMode)
	if err != nil || CanonicalRecognizedQuestionsDigest(questions) != CanonicalRecognizedQuestionsDigest(run.questions) {
		return nil, invalid
	}
	byProblem := make(map[string]RecognizedQuestion, len(run.questions))
	for _, question := range run.questions {
		byProblem[question.ProblemID] = question
	}
	if len(byProblem) != len(questions) {
		return nil, invalid
	}
	for _, question := range questions {
		original := byProblem[question.ProblemID]
		if question.PageAssetID != original.PageAssetID || question.RawTranscription != original.RawTranscription ||
			question.AttemptID != original.AttemptID || question.InputDigest != original.InputDigest ||
			question.AnswerRawTranscription != original.AnswerRawTranscription {
			return nil, invalid
		}
	}
	leaves := RecognizedQuestionsForAssessmentForInitialReadMode(run.questions, run.req.InitialReadMode)
	if len(leaves) == 0 || len(leaves) != len(snapshot.Attempts) {
		return nil, invalid
	}
	for _, question := range leaves {
		if question.AnswerState != AnswerStateBlank || strings.TrimSpace(question.AnswerRawTranscription) != "" ||
			strings.TrimSpace(question.AnswerCanonicalMarkdown) != "" || strings.TrimSpace(question.StudentAnswer) != "" ||
			question.ConfirmedVersion < 1 || question.InputDigest == "" || !CanonicalMarkdownValid(question.CanonicalMarkdown) {
			return nil, invalid
		}
	}
	parent, err := o.deps.Records.GetModelInvocationByAttempt(ctx, run.agentName, job.Record.RecordID,
		k12.GradingStageAssessing, job.Fields.AttemptCount)
	if err != nil {
		return nil, err
	}
	requestRaw, err := json.Marshal(struct {
		Request   PhotoGradeRequest    `json:"request"`
		Questions []RecognizedQuestion `json:"questions"`
		Anchored  []RecognizedQuestion `json:"anchored,omitempty"`
	}{run.req, run.questions, run.anchored})
	if err != nil {
		return nil, err
	}
	if parent.Status != k12.ModelInvocationFailed || parent.FailureKind != "assess_item_invalid" ||
		parent.RouteSnapshot != job.Fields.ModelSnapshot || parent.ResultDigest != "" || parent.ResultJSON != "" ||
		parent.RequestDigest != modelInvocationDigest([]byte(k12.GradingStageAssessing), requestRaw) {
		return nil, invalid
	}
	return &k12storage.ImageTaskPreAssessmentRecoveryProof{
		InvocationID: parent.InvocationID, RequestDigest: parent.RequestDigest, Snapshot: snapshot,
	}, nil
}
