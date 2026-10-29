package usecase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// 修改识题、评估或报告语义时升级此合同；模型、技能、档案及模板另按冻结内容隔离。
const photoGradingCacheContract = "photo-grading-result-v1"

// 文件精确匹配保留方向等元数据；重拍、重新压缩或任何笔迹变化均不沿用旧评分。
func photoGradingImageDigest(data []byte) string {
	_, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func photoGradingRequestDigest(req PhotoGradeRequest) string {
	// 原图像素包含全部学生笔迹；来源命令重放不重新读取可变处理配置。
	return digestJSON(struct {
		Image, Agent, Session, Subject, Grade string
		Intent                                PhotoTaskIntent
		Scope                                 *k12.RecognitionImageScopeInputV1
		Practice                              []PracticeGradingReference
		PaperSize                             int
		UncertaintyFinalized                  bool
	}{photoGradingImageDigest(req.Image), req.AgentName, req.SourceSession, req.Subject, req.Grade, req.TaskIntent, req.ImageScope, req.PracticeReferences, req.PracticePaperSize, req.SourceUncertaintyFinalized})
}

func (o *GradingOrchestrator) photoGradingCacheKey(ctx context.Context, in StartPhotoGradingInput, snap k12.GradingModelSnapshot, budget k12.GradingBudgetSnapshot) (string, error) {
	if in.Photo.TaskIntent != PhotoTaskCompletedHomework && in.Photo.TaskIntent != PhotoTaskBlankWorksheet {
		return "", nil
	}
	if photoGradingImageDigest(in.Photo.Image) == "" {
		return "", nil
	}
	owner := o.deps.TextbookOwnerID
	var imageTaskContext any
	if in.SourceKind == "image_task" {
		var err error
		owner, err = o.deps.Records.GetImageTaskOwnerScope(ctx, in.Photo.AgentName, in.SourceKey)
		if err != nil {
			return "", err
		}
		dispatch, err := o.deps.Records.GetImageTaskDispatch(ctx, in.Photo.AgentName, in.SourceKey)
		if err != nil {
			return "", err
		}
		// 附言和语言同样影响冻结交付；新消息不能沿用旧任务的PDF意图或显示语言。
		imageTaskContext = struct{ Learner, MessageIntent, Locale string }{
			dispatch.LearnerID, dispatch.MessageIntent, dispatch.OperationRouteRequest.MessageLocale,
		}
	}
	if owner == "" {
		return "", nil
	}
	profile, err := o.deps.GetProfileBundleSnapshot(ctx, in.Photo.AgentName, "")
	if err != nil {
		return "", err
	}
	var child k12.ChildProfile
	if o.deps.Profiles != nil {
		child, err = o.deps.Profiles.GetProfile(ctx, in.Photo.AgentName)
		if err != nil {
			return "", err
		}
	}
	snap.ParentInstructions = config.ReadAgentInstructions()
	if snap.MathExecutionContract == "" {
		snap.MathExecutionContract = k12.MathExecutionContractProgramFirst
	}
	// 报告复用原日期；当前日期不是重新调用模型的理由。模板语义摘要仍参与身份。
	var report any
	if in.Photo.TeachingReport != nil {
		metadata := in.Photo.TeachingReport.Metadata
		metadata.ReportDate = ""
		report = struct {
			Metadata k12.TeachingReportMetadata
			Bundle   any
		}{metadata, in.Photo.TeachingReport.Bundle.Reference}
	}
	key := digestJSON(struct {
		Contract, Owner, Request      string
		Profile                       k12.ProfileBundleSnapshot
		Child                         k12.ChildProfile
		Model                         k12.GradingModelSnapshot
		Budget                        k12.GradingBudgetSnapshot
		SolveOutput, TeachingContract string
		TeachingSkills                any
		Report                        any
		ImageTaskContext              any
	}{photoGradingCacheContract, owner, photoGradingRequestDigest(in.Photo), profile, child, snap, budget, in.Photo.SolveOutputVersion,
		in.Photo.ParentTeachingContract, in.Photo.ParentTeachingSkillSnapshot, report, imageTaskContext})
	return key, nil
}

func (o *GradingOrchestrator) validCachedPhotoResult(ctx context.Context, agent string, cached k12storage.PhotoGradingReuse) (GradingJobView, bool, error) {
	job, err := o.deps.GetGradingJob(ctx, agent, cached.JobID)
	if err != nil || job.Record.Status != k12.GradingStageCompleted {
		return job, false, err
	}
	artifact, err := o.deps.Records.GetGradingFinalArtifactByJob(ctx, agent, cached.JobID)
	if err != nil {
		return job, false, err
	}
	if artifact.ArtifactID != cached.ArtifactID || artifact.ArtifactDigest != cached.ArtifactDigest ||
		artifact.Validate() != nil || artifact.ArtifactDigest != k12.ComputeGradingFinalArtifactDigest(artifact) ||
		artifact.CoverageStatus != k12.GradingFinalArtifactCoverageComplete {
		return job, false, nil
	}
	if artifact.HasAnnotatedAsset() {
		if _, err := o.deps.Records.OpenGradingFinalAnnotatedAsset(ctx, agent, artifact.ArtifactID); err != nil {
			return job, false, err
		}
	}
	items, err := o.deps.Records.ListEffectiveGradingAssessments(ctx, agent, cached.JobID)
	if err != nil {
		return job, false, err
	}
	if len(items) != artifact.TotalCount {
		return job, false, nil
	}
	owner, err := resolveGradingGroundingTextbookOwner(ctx, o.deps, job)
	if err != nil {
		return job, false, err
	}
	for _, item := range items {
		if failed, failureErr := failedAssessmentVerification(ctx, o.deps, item); failed != nil || failureErr != nil {
			return job, false, failureErr
		}
		switch item.Status {
		case k12.GradingAssessmentCorrect, k12.GradingAssessmentWrong, k12.GradingAssessmentProcessIssue,
			k12.GradingAssessmentBlankSolved, k12.GradingAssessmentAnswerUnclear,
			k12.GradingAssessmentUnanswered, k12.GradingAssessmentOutOfScope, k12.GradingAssessmentUntrusted:
		default:
			return job, false, nil
		}
		var result PhotoGradeItem
		if json.Unmarshal([]byte(item.ResultJSON), &result) != nil || result.Status == PhotoFailed {
			return job, false, nil
		}
		if item.AnswerSource != nil && item.AnswerSource.Kind == k12.ProblemAnswerAsset {
			if err := o.deps.Records.ValidateProblemAssetAdoption(ctx, owner, item.AnswerSource.AdoptionID); err != nil {
				return job, false, err
			}
		}
	}
	// 内容不确定可以是正常终态；技术失败、在途或结果未知不能成为新消息的完成来源。
	parents, err := o.deps.Records.ListModelInvocations(ctx, agent, cached.JobID)
	if err != nil {
		return job, false, err
	}
	for _, call := range parents {
		if call.Status != k12.ModelInvocationSucceeded && !(call.Status == k12.ModelInvocationReconciled && call.ResultJSON != "") {
			return job, false, nil
		}
	}
	physical, err := o.deps.Records.ListModelPhysicalInvocations(ctx, agent, cached.JobID)
	if err != nil {
		return job, false, err
	}
	for _, call := range physical {
		if call.Status != k12.ModelInvocationSucceeded {
			return job, false, nil
		}
	}
	operations, err := o.deps.Records.ListGradingItemInvocations(ctx, agent, cached.JobID)
	if err != nil {
		return job, false, err
	}
	for _, call := range operations {
		if call.Status != k12.ModelInvocationSucceeded {
			return job, false, nil
		}
	}
	return job, true, nil
}

func (o *GradingOrchestrator) replayPhotoGradingReuse(ctx context.Context, in StartPhotoGradingInput) (GradingJobView, bool, error) {
	key := k12.BuildGradingIdempotencyKey(in.SourceKind, in.SourceKey, 0)
	reuse, err := o.deps.Records.GetPhotoGradingReuse(ctx, in.Photo.AgentName, key)
	if errors.Is(err, records.ErrNotFound) {
		return GradingJobView{}, false, nil
	}
	if err != nil {
		return GradingJobView{}, false, err
	}
	if reuse.RequestDigest != photoGradingRequestDigest(in.Photo) {
		return GradingJobView{}, false, fmt.Errorf("%w: reused photo source content changed", ErrInvalidInput)
	}
	job, err := o.deps.GetGradingJob(ctx, in.Photo.AgentName, reuse.JobID)
	return job, err == nil, err
}

func (o *GradingOrchestrator) findPhotoGradingReuse(ctx context.Context, in StartPhotoGradingInput, cacheKey string) (GradingJobView, bool, error) {
	if cacheKey == "" {
		return GradingJobView{}, false, nil
	}
	candidates, err := o.deps.Records.FindPhotoGradingCache(ctx, in.Photo.AgentName, cacheKey)
	if err != nil {
		slog.Warn("K12 photo grading cache lookup failed", "agent_id", in.Photo.AgentName, "error", err)
		return GradingJobView{}, false, nil
	}
	for _, cached := range candidates {
		job, valid, err := o.validCachedPhotoResult(ctx, in.Photo.AgentName, cached)
		if err != nil || !valid {
			continue
		}
		if err := o.deps.Records.AdoptPhotoGradingCache(ctx, in.Photo.AgentName,
			k12.BuildGradingIdempotencyKey(in.SourceKind, in.SourceKey, 0), photoGradingRequestDigest(in.Photo), cached); err != nil {
			return job, false, err
		}
		slog.Info("K12 photo grading result reused", "agent_id", in.Photo.AgentName, "source_kind", in.SourceKind, "source_key", in.SourceKey, "source_job_id", cached.JobID, "artifact_id", cached.ArtifactID)
		return job, true, nil
	}
	return GradingJobView{}, false, nil
}

func (o *GradingOrchestrator) publishPhotoGradingCache(ctx context.Context, run *gradingRun, job GradingJobView) {
	if run.textOnly || run.result == nil {
		return
	}
	for _, item := range run.result.Items {
		if item.Status == PhotoFailed || item.Recognized.ConfirmedVersion > 1 ||
			(item.Recognized.ConfirmationRequired && !run.req.SourceUncertaintyFinalized) {
			return
		}
	}
	artifact, err := o.deps.Records.GetGradingFinalArtifactByJob(ctx, run.agentName, job.Record.RecordID)
	if err == nil {
		err = o.deps.Records.CompletePhotoGradingCache(ctx, run.agentName, job.Record.RecordID, artifact)
	}
	if err != nil {
		slog.Warn("K12 photo grading cache publication failed", "job_id", job.Record.RecordID, "error", err)
	}
}
