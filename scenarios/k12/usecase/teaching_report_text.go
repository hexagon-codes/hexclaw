package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// TextSolveTeachingReportRequest 的身份由认证入口传入，内容不授予跨实例读取权限。
// Content和DeepSections来自本次独立核对的教学阶段，不从旧解法中截句推测。
type TextSolveTeachingReportRequest struct {
	OwnerID      string
	AgentName    string
	RunID        string
	Metadata     k12.TeachingReportMetadata
	Content      TeachingReportQuestionContent
	DeepSections []k12.TeachingReportDeepSection
}

// BuildTextSolveTeachingReport 复用A11实际最终化和采用资格检查，不创建批改任务或评分。
// 组装后仍由冻结用例在提交前再次核对来源；此方法不调用模型、发布或发送消息。
func (d Deps) BuildTextSolveTeachingReport(ctx context.Context, req TextSolveTeachingReportRequest) (k12.TeachingReport, error) {
	return d.buildTextSolveTeachingReport(ctx, req, true)
}

func (d Deps) buildTextSolveTeachingReport(ctx context.Context, req TextSolveTeachingReportRequest, checkCurrentAdoption bool) (k12.TeachingReport, error) {
	if d.Records == nil || req.OwnerID == "" || req.AgentName == "" || req.RunID == "" {
		return k12.TeachingReport{}, fmt.Errorf("%w: trusted text solve identity is required", ErrInvalidInput)
	}
	final, err := d.validateTextSolveFinalization(ctx, req.OwnerID, req.AgentName, req.RunID, checkCurrentAdoption)
	if err != nil {
		return k12.TeachingReport{}, err
	}
	run, err := d.Records.GetTextSolveRun(ctx, req.OwnerID, req.AgentName, req.RunID)
	if err != nil {
		return k12.TeachingReport{}, err
	}
	var input GradeRequest
	if err := json.Unmarshal([]byte(run.RequestJSON), &input); err != nil || input.AgentName != req.AgentName || strings.TrimSpace(input.Problem) == "" {
		return k12.TeachingReport{}, fmt.Errorf("%w: frozen text problem is unavailable", k12.ErrTeachingReportInvalid)
	}
	var solved SolveHomeworkResult
	if err := json.Unmarshal([]byte(final.ResultJSON), &solved); err != nil {
		return k12.TeachingReport{}, fmt.Errorf("%w: finalized text result cannot be decoded", k12.ErrTeachingReportInvalid)
	}
	if req.Content.ProblemID != final.FinalizationID || strings.TrimSpace(req.Content.ShortTitle) == "" {
		return k12.TeachingReport{}, fmt.Errorf("%w: teaching content must identify the finalized text problem", ErrTeachingReportContentRequired)
	}
	if req.Content.AnswerAnalysis.State != k12.TeachingReportReferenceUnavailable && strings.TrimSpace(solved.Solution) == "" {
		return k12.TeachingReport{}, fmt.Errorf("%w: finalized text solution is unavailable", ErrTeachingReportContentRequired)
	}
	state := k12.TeachingReportAnswerNotProvided
	if strings.TrimSpace(input.StudentAnswer) != "" {
		state = k12.TeachingReportAnswerPresent
	}
	// 文本条目绑定实际最终结果快照；该摘要表达来源身份，不提升原数值证据等级。
	source := k12.TeachingReportProblemSource{
		ProblemID: final.FinalizationID, QuestionText: input.Problem, InputRevision: final.InputRevision,
		InputDigest: final.InputDigest, SolutionDigest: final.ResultDigest,
		AnswerState: state, AnswerRaw: input.StudentAnswer, AnswerCanonical: input.StudentAnswer,
		AnswerSource: final.AnswerSource,
	}
	analysis := req.Content.AnswerAnalysis
	receipt := ""
	if final.AnswerSource != nil {
		receipt = firstNonEmpty(final.AnswerSource.InvocationID, final.AnswerSource.AdoptionID)
	}
	analysis.Evidence = k12.TeachingReportReferenceEvidence{
		Verdict: string(solved.Evidence.Verdict), EvidenceType: string(solved.Evidence.EvidenceType),
		ReceiptRef: receipt, SolutionDigest: final.ResultDigest,
		VerificationRunID: solved.Evidence.VerificationRunID, VerificationDigest: solved.Evidence.VerificationInputDigest,
	}
	metadata := req.Metadata
	metadata.Subject = firstNonEmpty(input.Subject, run.Facts.Subject)
	if metadata.GradeTerm == "" {
		metadata.GradeTerm = input.Grade
	}
	return k12.NewTeachingReport(k12.TeachingReport{
		SchemaVersion: k12.TeachingReportSchemaVersion, ContentVersion: k12.TeachingReportContentVersion,
		Source: k12.TeachingReportSource{Kind: k12.TeachingReportSourceTextSolve, SourceID: final.FinalizationID,
			Revision: final.InputRevision, Digest: final.ResultDigest},
		Metadata: metadata, ProblemSources: []k12.TeachingReportProblemSource{source}, DeepSections: req.DeepSections,
		Items: []k12.TeachingReportItem{{
			ProblemID: final.FinalizationID, ShortTitle: req.Content.ShortTitle,
			Assessment: k12.TeachingReportAssessment{Reason: "text_solve_without_grading"}, AnswerAnalysis: analysis,
			OriginalAnswer: k12.TeachingReportOriginalAnswer{State: state, RawText: input.StudentAnswer, CanonicalText: input.StudentAnswer},
		}},
	})
}
