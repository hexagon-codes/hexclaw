package usecase

import (
	"context"
	"errors"
	"fmt"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type assessmentCorrectionContextKey struct{}
type assessmentRecoveryContextKey struct{}

func assessmentCorrectionIdentity(view k12.EffectiveGradingAssessment) string {
	predecessor := ""
	if view.Correction != nil {
		predecessor = view.Correction.CorrectionID
	}
	return fmt.Sprintf("%s:%s:%s", k12storage.CorrectionSourceID(view.Original), view.Original.ResultDigest, predecessor)
}

// commitAssetAssessmentCorrection 只修正尚未交付且采用来源失效的同一次作答。
func commitAssetAssessmentCorrection(ctx context.Context, deps Deps, view k12.EffectiveGradingAssessment,
	item k12.GradingAssessmentItem, effects k12storage.GradingAssessmentEffects,
) (k12.GradingAssessmentItem, error) {
	item.InputRevision, item.PublishedRevision = view.Original.InputRevision, view.Original.PublishedRevision
	item.StructureVersion, item.CurrentDisposition = view.Original.StructureVersion, view.Original.CurrentDisposition
	request := k12.GradingAssessmentCorrection{
		CorrectionID:         "asset-correction:" + modelInvocationDigest([]byte(assessmentCorrectionIdentity(view))),
		OriginalResultDigest: view.Original.ResultDigest, OriginalAnswerSource: view.Original.AnswerSource,
		Reason: k12.AssessmentCorrectionAnswer, Assessment: item,
	}
	if view.Correction != nil {
		request.PreviousCorrectionID = view.Correction.CorrectionID
	}
	// 求解已重新验证，但不能把同一作答变成新的掌握／复习证据。
	effects.AssetPublication, effects.SourceCorrection, effects.Review = nil, false, nil
	if id := view.Current.ProjectionRecordID; id != "" {
		record, err := deps.Records.Get(ctx, id)
		if err != nil {
			return item, err
		}
		fields, err := k12.ParseMistakeFields(record.Fields)
		if err != nil {
			return item, err
		}
		status, due := record.Status, record.DueAt
		if item.Status == k12.GradingAssessmentWrong {
			if effects.Mistake != nil {
				fields.ErrorCause, fields.KnowledgePoint = effects.Mistake.Fields.ErrorCause, effects.Mistake.Fields.KnowledgePoint
				fields.CanonicalAnswer = effects.Mistake.Fields.CanonicalAnswer
			}
		} else if status == k12.StatusNew || status == k12.StatusExplained {
			fields.ArchivedReason, fields.ArchivedAt, fields.ArchiveCommandID = k12.MistakeArchivedReasonSourceCorrection, deps.now(), request.CorrectionID
			fields.ArchivedFromStatus, fields.ArchivedFromDueAt, fields.ArchivedFromSpotCheckState = status, due, fields.SpotCheckState
			fields.LastArchive = &k12.MistakeArchiveSnapshot{Reason: fields.ArchivedReason, ArchivedAt: fields.ArchivedAt,
				ArchiveCommandID: request.CorrectionID, FromStatus: status, FromDueAt: due, FromSpotCheckState: fields.SpotCheckState}
			fields.SpotCheckState, status, due = k12.SpotCheckNone, k12.StatusArchived, nil
		}
		effects.Mistake = nil
		effects.Review = &k12storage.GradingReviewEffect{RecordID: id, ExpectedVersion: record.Version, NewStatus: status, Fields: fields, DueAt: due}
	}
	stored, _, err := deps.Records.AppendGradingAssessmentCorrection(ctx, request, effects)
	return stored.Assessment, err
}

// recoverInvalidAssetAssessments 在终稿生成前恢复失效来源，不覆盖成功的聚合调用回执。
func (o *GradingOrchestrator) recoverInvalidAssetAssessments(ctx context.Context, run *gradingRun, job GradingJobView) error {
	items, err := o.deps.Records.ListEffectiveGradingAssessments(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err = o.deps.Records.ValidateGradingAssessmentAnswer(ctx, item); err == nil {
			continue
		}
		if !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
			return err
		}
		found := false
		for _, q := range run.questions {
			if q.ProblemID != item.ProblemID {
				continue
			}
			found = true
			mode := PhotoModeGrade
			if run.result != nil {
				mode = run.result.Mode
			}
			recoveryCtx := context.WithValue(ctx, assessmentRecoveryContextKey{}, true)
			if _, err = o.assessDurablePhotoItem(recoveryCtx, o.deps, job, run.req, mode, q); err != nil {
				return err
			}
			break
		}
		if !found {
			return fmt.Errorf("%w: correction problem is absent from frozen input", ErrGradingAssessmentExactSet)
		}
	}
	return nil
}
