package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// CompletedSourceReview 保留独立原图复核的完整响应，不把不确定读数发布为事实。
type CompletedSourceReview struct {
	Question RecognizedQuestion `json:"question"`
	Verified bool               `json:"verified"`
	Raw      string             `json:"raw"`
}

// CompletedSourceReviewer 复用现有视觉适配器，仅复核已完成任务的一道原题。
type CompletedSourceReviewer interface {
	ReviewCompletedSource(context.Context, []byte, RecognizedQuestion) (CompletedSourceReview, error)
}

type FinalSourceCorrectionInput struct {
	Agent                    string `json:"agent"`
	IdempotencyKey           string `json:"idempotency_key"`
	ArtifactID               string `json:"artifact_id"`
	ArtifactDigest           string `json:"artifact_digest"`
	InputRevision            int    `json:"input_revision"`
	InputDigest              string `json:"input_digest"`
	RecoveryOf               string `json:"recovery_of,omitempty"`
	RecoveryResponseDigest   string `json:"recovery_response_digest,omitempty"`
	AcceptDuplicateExecution bool   `json:"accept_duplicate_execution,omitempty"`
}

type FinalSourceCorrectionResult struct {
	CorrectionID string                    `json:"correction_id"`
	Status       string                    `json:"status"`
	Replayed     bool                      `json:"replayed"`
	Artifact     *k12.GradingFinalArtifact `json:"artifact,omitempty"`
}

// CorrectCompletedSource 只追加来源、评估和终稿修订，不重新生成学生作答。
func (o *GradingOrchestrator) CorrectCompletedSource(ctx context.Context, owner, dispatchID, problemID string, in FinalSourceCorrectionInput) (FinalSourceCorrectionResult, error) {
	var out FinalSourceCorrectionResult
	if o == nil || o.deps.Records == nil || strings.TrimSpace(in.IdempotencyKey) == "" || owner == "" || in.InputRevision < 1 || in.ArtifactID == "" || in.ArtifactDigest == "" || in.InputDigest == "" {
		return out, fmt.Errorf("%w: correction identity is incomplete", ErrInvalidInput)
	}
	store := o.deps.Records
	actualOwner, err := store.GetImageTaskOwnerScope(ctx, in.Agent, dispatchID)
	if err != nil {
		return out, err
	}
	if actualOwner != owner {
		return out, records.ErrNotFound
	}
	dispatch, err := store.GetImageTaskDispatch(ctx, in.Agent, dispatchID)
	if err != nil {
		return out, err
	}
	homework, err := store.GetHomeworkSubmission(ctx, in.Agent, dispatch.TargetObjectID)
	if err != nil {
		return out, err
	}
	jobID := homework.GradingJobID
	lock := o.jobLock(jobID)
	lock.Lock()
	defer lock.Unlock()
	job, err := o.deps.GetGradingJob(ctx, in.Agent, jobID)
	if err != nil {
		return out, err
	}
	linked, err := gradingFinalImageTaskDispatchID(job)
	if err != nil {
		return out, err
	}
	if linked != dispatchID || job.Record.Status != k12.GradingStageCompleted {
		return out, fmt.Errorf("%w: completed original image task required", ErrInvalidInput)
	}
	id := "source-correction-" + modelInvocationDigest([]byte(in.Agent), []byte(dispatchID), []byte(in.IdempotencyKey))
	out.CorrectionID = id
	command, err := store.GetFinalSourceCorrection(ctx, in.Agent, id)
	if err == nil {
		r := command.Request
		if r.OwnerScope != owner || r.DispatchID != dispatchID || r.ProblemID != problemID || r.OriginalArtifactID != in.ArtifactID || r.OriginalArtifactDigest != in.ArtifactDigest || r.InputRevision != in.InputRevision || r.InputDigest != in.InputDigest || r.RecoveryOf != in.RecoveryOf || r.RecoveryResponseDigest != in.RecoveryResponseDigest || r.AcceptDuplicateExecution != in.AcceptDuplicateExecution {
			return out, records.ErrVersionConflict
		}
		out.Replayed = true
	} else if !errors.Is(err, records.ErrNotFound) {
		return out, err
	}
	if err == nil && command.Status == "completed" {
		a, e := store.GetGradingFinalArtifact(ctx, in.Agent, "final-"+id)
		out.Status = "completed"
		out.Artifact = &a
		return out, e
	}
	if err == nil && (command.Status == "sent" || command.Status == "outcome_unknown" || command.Status == "failed") {
		policyErr := k12.ValidateModelInvocationRequestPolicy(k12.GradingStageRecognizing, command.Request.Model, k12.ApprovedRecognizingRequestPolicy())
		knownPreflight := command.Failure == "original source region unavailable" || (policyErr != nil && command.Failure == policyErr.Error())
		if command.Status == "outcome_unknown" && knownPreflight {
			// 区域与冻结策略校验均发生在外发前；只核实完整空响应的已知本地拒绝。
			var review CompletedSourceReview
			if json.Unmarshal([]byte(command.ResponseJSON), &review) == nil && !review.Verified && review.Raw == "" && review.Question.Question == "" {
				if e := store.ReconcileFinalSourceCorrectionNotSent(ctx, in.Agent, id, command.ResponseJSON, command.ResponseDigest); e != nil {
					return out, e
				}
				command.Status = "failed"
			}
		}
		out.Status = command.Status
		return out, ErrModelInvocationRequiresReconciliation
	}
	original, err := store.GetGradingFinalArtifactByJob(ctx, in.Agent, jobID)
	if err != nil {
		return out, err
	}
	if original.ArtifactID != in.ArtifactID || original.ArtifactDigest != in.ArtifactDigest {
		return out, records.ErrVersionConflict
	}
	questions, err := o.deps.loadCurrentConfirmedQuestions(ctx, in.Agent, job.Fields.SubmissionID)
	if err != nil {
		return out, err
	}
	target := -1
	for i, q := range questions {
		if q.ProblemID == problemID {
			target = i
			break
		}
	}
	if target < 0 {
		return out, records.ErrNotFound
	}
	q := questions[target]
	if q.ConfirmedVersion != in.InputRevision || q.InputDigest != in.InputDigest {
		return out, records.ErrVersionConflict
	}
	if len(dispatch.SourceAssetRefs) != 1 {
		return out, fmt.Errorf("%w: one original page required", ErrInvalidInput)
	}
	page, err := (&PageAssetRepository{Records: store}).OpenReady(ctx, owner, in.Agent, dispatch.SourceAssetRefs[0])
	if err != nil {
		return out, err
	}
	if page.Metadata.ContentDigest != original.OriginalSourceDigest {
		return out, records.ErrVersionConflict
	}
	// 旧结构投影没有保存区域时，只从同题的冻结识别回执取回原始坐标。
	if q.SourceRegion == nil && q.ConfirmedVersion == 1 {
		if receipt, ok := o.readRecognitionReceipt(jobID); ok && receipt.AgentName == in.Agent {
			for _, source := range receipt.Questions {
				// 旧回执使用逻辑 page ID，当前投影可能已升级为 asset URI；原图由上方任务和内容摘要核对。
				if source.ProblemID == q.ProblemID && source.AttemptID == q.AttemptID && source.RawTranscription == q.RawTranscription && source.CanonicalMarkdown == q.CanonicalMarkdown {
					q.SourceRegion = cloneProblemSourceRegion(source.SourceRegion)
					break
				}
			}
		}
	}
	q.SourceWidth, q.SourceHeight = page.Metadata.PixelWidth, page.Metadata.PixelHeight
	if q.SourceRegion == nil || q.SourceWidth <= 0 || q.SourceHeight <= 0 {
		return out, fmt.Errorf("original source region unavailable")
	}
	region := q.SourceRegion
	if region.X < 0 || region.Y < 0 || region.Width <= 0 || region.Height <= 0 || region.X+region.Width > q.SourceWidth || region.Y+region.Height > q.SourceHeight {
		return out, fmt.Errorf("original source region exceeds page bounds")
	}
	if err := k12.ValidateModelInvocationRequestPolicy(k12.GradingStageRecognizing, job.Fields.ModelSnapshot, job.Fields.ModelSnapshot.RecognizingRequestPolicy); err != nil {
		return out, err
	}
	if command.Status == "" {
		request := k12storage.FinalSourceCorrectionRequest{CorrectionID: id, AgentName: in.Agent, JobID: jobID, DispatchID: dispatchID, OwnerScope: owner, SubmissionID: job.Fields.SubmissionID, ProblemID: problemID, InputRevision: in.InputRevision, InputDigest: in.InputDigest, StructureVersion: original.StructureVersion, OriginalArtifactID: original.ArtifactID, OriginalArtifactDigest: original.ArtifactDigest, SourceDigest: original.OriginalSourceDigest, Model: job.Fields.ModelSnapshot}
		request.RecoveryOf = in.RecoveryOf
		request.RecoveryResponseDigest = in.RecoveryResponseDigest
		request.AcceptDuplicateExecution = in.AcceptDuplicateExecution
		command, out.Replayed, err = store.PrepareFinalSourceCorrection(ctx, request)
		if err != nil {
			return out, err
		}
	}
	if command.Status == "prepared" {
		reviewer, ok := o.deps.Recognizer.(CompletedSourceReviewer)
		if !ok {
			return out, fmt.Errorf("source reviewer unavailable")
		}
		claimed, e := store.ClaimFinalSourceCorrection(ctx, in.Agent, id)
		if e != nil {
			return out, e
		}
		if !claimed {
			return out, ErrModelInvocationRequiresReconciliation
		}
		timeout := time.Duration(command.Request.Model.TimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = 120 * time.Second
		}
		callCtx, cancel := context.WithTimeout(k12.WithGradingModelSnapshot(ctx, command.Request.Model), timeout)
		callCtx = k12.WithGradingModelRequestPolicy(callCtx, command.Request.Model.RecognizingRequestPolicy)
		review, callErr := reviewer.ReviewCompletedSource(callCtx, page.Data, q)
		cancel()
		response, marshalErr := json.Marshal(review)
		if marshalErr != nil {
			return out, marshalErr
		}
		status, failure := "succeeded", ""
		if callErr != nil {
			status = "outcome_unknown"
			failure = callErr.Error()
		}
		settleCtx, settleCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err = store.SettleFinalSourceCorrection(settleCtx, in.Agent, id, status, string(response), failure)
		settleCancel()
		if err != nil {
			return out, err
		}
		if callErr != nil {
			out.Status = status
			return out, callErr
		}
		command, err = store.GetFinalSourceCorrection(ctx, in.Agent, id)
		if err != nil {
			return out, err
		}
	}
	var review CompletedSourceReview
	if err = json.Unmarshal([]byte(command.ResponseJSON), &review); err != nil {
		return out, err
	}
	if !review.Verified || review.Question.AnswerState != AnswerStateBlank {
		out.Status = command.Status
		return out, fmt.Errorf("source review did not establish a reliably blank answer")
	}
	// 独立响应存于回执；当前题干保持原值，只撤回误归属的作答。
	q.AnswerState = AnswerStateBlank
	q.StudentAnswer = ""
	q.AnswerRawTranscription = ""
	q.AnswerCanonicalMarkdown = ""
	q.BBox = nil
	q.ObservedAnswerRegion = nil
	q.OCRSignals = nil
	q.ConfirmationRequired = false
	q.ConfirmationReasons = nil
	q.AnswerEvidenceTranscriptions = nil
	q.ConfirmedVersion = in.InputRevision + 1
	q.CanonicalVersion = q.ConfirmedVersion
	q.InputDigest = k12storage.FinalSourceInputDigest(command, problemID, q.ConfirmedVersion)
	q.RecognitionConfidence = review.Question.RecognitionConfidence
	questions[target] = q
	assessments, err := store.ListEffectiveGradingAssessments(ctx, in.Agent, jobID)
	if err != nil {
		return out, err
	}
	byProblem := map[string]k12.GradingAssessmentItem{}
	for _, a := range assessments {
		byProblem[a.ProblemID] = a
	}
	previous, ok := byProblem[problemID]
	if !ok {
		return out, records.ErrNotFound
	}
	item := PhotoGradeItem{Recognized: q, Status: PhotoUnanswered, ResultKind: PhotoItemUnanswered}
	canonical := gradingAssessmentCanonicalResult(item)
	raw, err := json.Marshal(canonical)
	if err != nil {
		return out, err
	}
	now := o.deps.now()
	receipt := k12.GradingAssessmentItem{AgentName: in.Agent, JobID: jobID, ProblemID: problemID, AttemptID: previous.AttemptID, ConfirmedVersion: q.ConfirmedVersion, InputRevision: q.ConfirmedVersion, PublishedRevision: previous.PublishedRevision + 1, CurrentDisposition: k12.GradingAssessmentDispositionCurrent, StructureVersion: previous.StructureVersion, InputDigest: q.InputDigest, Status: k12.GradingAssessmentUnanswered, ResultJSON: string(raw), ResultDigest: modelInvocationResultDigest(canonical), ProjectionStatus: k12.GradingProjectionCommitted, CreatedAt: now, UpdatedAt: now}
	if err = receipt.Validate(); err != nil {
		return out, err
	}
	byProblem[problemID] = receipt
	skips, err := store.ListCurrentProblemSkipReceipts(ctx, in.Agent, jobID)
	if err != nil {
		return out, err
	}
	bySkip := map[string]k12.ProblemSkipReceipt{}
	for _, s := range skips {
		bySkip[s.ProblemID] = s
	}
	entries := []gradingFinalEntry{}
	digests := []string{}
	items := []PhotoGradeItem{}
	for _, question := range questions {
		if question.ProblemKind == ProblemKindCompoundParent {
			continue
		}
		entry := gradingFinalEntry{question: question}
		if a, ok := byProblem[question.ProblemID]; ok {
			entry.assessment = &a
			digests = append(digests, a.ResultDigest)
			pi, e := replayGradingAssessmentItem(question, a)
			if e != nil {
				return out, e
			}
			items = append(items, pi)
		} else if s, ok := bySkip[question.ProblemID]; ok {
			entry.skip = &s
			digests = append(digests, s.ResultDigest)
		} else {
			return out, ErrGradingFinalizationIncomplete
		}
		entries = append(entries, entry)
	}
	if o.deps.PhotoAnnotator == nil {
		return out, fmt.Errorf("photo annotator unavailable")
	}
	rendered, err := o.deps.PhotoAnnotator.Annotate(ctx, page.Data, trustedPhotoMarks(items))
	if err != nil {
		return out, err
	}
	ordered, _ := json.Marshal(digests)
	taskIntent, err := o.persistedImageTaskPhotoIntent(ctx, in.Agent, job.Fields, "")
	if err != nil {
		return out, err
	}
	artifact := original
	artifact.ArtifactID = "final-" + id
	artifact.CanonicalMarkdown = renderCanonicalFinalForTask(entries, taskIntent, original.CoverageStatus)
	artifact.SummaryInvocationID = ""
	artifact.OrderedCurrentDigestsJSON = string(ordered)
	artifact.CreatedAt = now
	artifact.UpdatedAt = now
	run := &gradingRun{result: &PhotoGradeResult{AnnotatedImage: &rendered}}
	if err = o.freezeGradingFinalAnnotatedAsset(ctx, run, job, &artifact); err != nil {
		return out, err
	}
	artifact.ArtifactDigest = k12.ComputeGradingFinalArtifactDigest(artifact)
	source, _ := json.Marshal(q)
	artifact, err = store.CommitFinalBlankSourceCorrection(ctx, id, artifact, source, receipt)
	if err != nil {
		return out, err
	}
	out.Status = "completed"
	out.Artifact = &artifact
	return out, nil
}

func (d Deps) overlayFinalSourceQuestions(ctx context.Context, agent, submission string, questions []RecognizedQuestion) ([]RecognizedQuestion, error) {
	revisions, err := d.Records.ListFinalSourceQuestions(ctx, agent, submission)
	if err != nil {
		return nil, err
	}
	for _, raw := range revisions {
		var q RecognizedQuestion
		if err = json.Unmarshal(raw, &q); err != nil {
			return nil, err
		}
		for i, old := range questions {
			if old.ProblemID == q.ProblemID && old.InputDigest == q.InputDigest {
				questions[i] = q
			}
		}
	}
	return questions, nil
}
