package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type historicalAssetCorrectionContextKey struct{}

// ProblemAssetFeedbackConsumer 复用逐题调用账本处理持久反馈，原任务及终稿保持不变。
type ProblemAssetFeedbackConsumer struct {
	Records *k12storage.Store
	mu      sync.RWMutex
	grading *GradingOrchestrator
	ready   chan struct{}
	once    sync.Once
}

func NewProblemAssetFeedbackConsumer(store *k12storage.Store) *ProblemAssetFeedbackConsumer {
	return &ProblemAssetFeedbackConsumer{Records: store, ready: make(chan struct{})}
}

// SetGrading 在启动装配完成时接入同一编排器；启动补投等待接线完成。
func (c *ProblemAssetFeedbackConsumer) SetGrading(grading *GradingOrchestrator) {
	if grading == nil {
		return
	}
	c.mu.Lock()
	c.grading = grading
	c.mu.Unlock()
	c.once.Do(func() { close(c.ready) })
}

func (c *ProblemAssetFeedbackConsumer) Name() string { return "problem-asset-feedback" }

func (c *ProblemAssetFeedbackConsumer) Handle(ctx context.Context, event k12storage.OutboxEvent) error {
	if event.EventType != k12storage.EventProblemAssetFeedback {
		return nil
	}
	var payload struct {
		FeedbackID string `json:"feedback_id"`
		OwnerID    string `json:"owner_id"`
	}
	if event.PayloadVersion != 1 || json.Unmarshal([]byte(event.Payload), &payload) != nil || payload.FeedbackID == "" || payload.OwnerID == "" {
		return fmt.Errorf("invalid answer feedback event")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ready:
	}
	c.mu.RLock()
	grading := c.grading
	c.mu.RUnlock()
	feedback, err := c.Records.GetProblemAssetFeedback(ctx, payload.OwnerID, payload.FeedbackID)
	if err != nil {
		return err
	}
	if feedback.Command.AgentName != event.AgentName || feedback.Command.JobID != event.AggregateID {
		return fmt.Errorf("answer feedback event identity mismatch")
	}
	if feedback.Status != "pending" {
		return nil
	}
	outcomes := make([]k12storage.ProblemAssetFeedbackOutcome, 0, len(feedback.Targets))
	status, message := "completed", ""
	for _, target := range feedback.Targets {
		if err = ctx.Err(); err != nil {
			return err
		}
		outcome := k12storage.ProblemAssetFeedbackOutcome{Target: target, Status: "corrected"}
		outcome.CorrectionID, err = grading.correctHistoricalAssetAssessment(ctx, target)
		if err != nil {
			outcome.Status, outcome.Error = "unresolved", err.Error()
			if errors.Is(err, ErrModelInvocationRequiresReconciliation) || errors.Is(err, ErrGradingPhysicalCallOutcomeUnknown) || invocationOutcomeUnknown(err) {
				outcome.Status = "outcome_unknown"
				status = "outcome_unknown"
			} else if status != "outcome_unknown" {
				status = "unresolved"
			}
			if message == "" {
				message = outcome.Error
			}
		}
		outcomes = append(outcomes, outcome)
	}
	// 业务失败作为可查询的明确结果保存；存储失败仍交 Outbox 恢复。
	return c.Records.CompleteProblemAssetFeedback(ctx, payload.OwnerID, payload.FeedbackID, status, message, outcomes)
}

func (o *GradingOrchestrator) correctHistoricalAssetAssessment(ctx context.Context, target k12storage.ProblemAssetFeedbackTarget) (string, error) {
	lock := o.jobLock(target.JobID)
	lock.Lock()
	defer lock.Unlock()
	view, err := o.deps.Records.GetEffectiveGradingAssessment(ctx, target.AgentName, target.JobID, target.ProblemID)
	if err != nil {
		return "", err
	}
	if view.Original.InputRevision != target.InputRevision || view.Original.ResultDigest != target.ResultDigest || view.Original.CurrentDisposition != k12.GradingAssessmentDispositionCurrent {
		return "", fmt.Errorf("original assessment is no longer the current input")
	}
	if view.Correction != nil && (view.Current.AnswerSource == nil || view.Current.AnswerSource.AssetID != target.AssetID || view.Current.AnswerSource.AssetVersion != target.AssetVersion) {
		if view.Current.Status != k12.GradingAssessmentCorrect && view.Current.Status != k12.GradingAssessmentWrong &&
			view.Current.Status != k12.GradingAssessmentProcessIssue && view.Current.Status != k12.GradingAssessmentBlankSolved {
			return "", fmt.Errorf("existing correction has no reliable assessment")
		}
		if view.Current.Status == k12.GradingAssessmentBlankSolved {
			var item PhotoGradeItem
			if json.Unmarshal([]byte(view.Current.ResultJSON), &item) != nil || !photoEvidenceTrusted(item.Solve.Evidence) {
				return "", fmt.Errorf("existing correction has no verified solve evidence")
			}
		}
		return view.Correction.CorrectionID, nil
	}
	if view.Current.AnswerSource == nil || view.Current.AnswerSource.AssetID != target.AssetID || view.Current.AnswerSource.AssetVersion != target.AssetVersion {
		return "", k12storage.ErrProblemAssetFeedbackConflict
	}
	job, err := o.deps.GetGradingJob(ctx, target.AgentName, target.JobID)
	if err != nil {
		return "", err
	}
	questions, err := o.deps.loadCurrentConfirmedQuestions(ctx, target.AgentName, job.Fields.SubmissionID)
	if err != nil {
		return "", err
	}
	var question *RecognizedQuestion
	for i := range questions {
		if questions[i].ProblemID == target.ProblemID {
			question = &questions[i]
			break
		}
	}
	if question == nil || question.ConfirmedVersion != target.InputRevision || question.InputDigest != view.Original.InputDigest || question.AttemptID != view.Original.AttemptID {
		return "", fmt.Errorf("original problem and answer evidence is unavailable")
	}
	req := PhotoGradeRequest{AgentName: target.AgentName, Subject: question.Subject, SourceSession: job.Record.SourceSession}
	correctionCtx := k12.WithGradingModelSnapshot(ctx, job.Fields.ModelSnapshot)
	correctionCtx = context.WithValue(correctionCtx, assessmentRecoveryContextKey{}, true)
	correctionCtx = context.WithValue(correctionCtx, historicalAssetCorrectionContextKey{}, true)
	if _, err = o.assessDurablePhotoItem(correctionCtx, o.deps, job, req, PhotoModeGrade, *question); err != nil {
		return "", err
	}
	updated, err := o.deps.Records.GetEffectiveGradingAssessment(ctx, target.AgentName, target.JobID, target.ProblemID)
	if err != nil {
		return "", err
	}
	if updated.Correction == nil || updated.Correction.CorrectionID == assessmentCorrectionID(view) {
		return "", fmt.Errorf("answer feedback did not produce a reliable correction")
	}
	return updated.Correction.CorrectionID, nil
}

func assessmentCorrectionID(view k12.EffectiveGradingAssessment) string {
	if view.Correction == nil {
		return ""
	}
	return view.Correction.CorrectionID
}
