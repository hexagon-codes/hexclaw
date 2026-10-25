package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func clearTextReportSnapshot(req *GradeRequest) {
	req.TeachingReport = nil
	req.SolveOutputVersion = ""
	req.ParentTeachingContract = ""
	req.ParentTeachingSkillSnapshot = nil
}

func (d Deps) textSolveTeachingReport(ctx context.Context, final k12.TextSolveFinalization, gradeRefs ...k12.TextGradeReportReference) (*k12.TeachingReport, error) {
	if len(gradeRefs) > 1 {
		return nil, fmt.Errorf("exactly one text grading reference is allowed")
	}
	run, err := d.Records.GetTextSolveRun(ctx, final.OwnerID, final.AgentName, final.RunID)
	if err != nil {
		return nil, err
	}
	var req GradeRequest
	if err := json.Unmarshal([]byte(run.RequestJSON), &req); err != nil {
		return nil, err
	}
	if req.TeachingReport == nil {
		return nil, nil
	}
	var solved SolveHomeworkResult
	if err := json.Unmarshal([]byte(final.ResultJSON), &solved); err != nil {
		return nil, err
	}
	content := TeachingReportQuestionContent{ProblemID: final.FinalizationID, ShortTitle: "解题结果"}
	var sections []k12.TeachingReportDeepSection
	if solved.Solution == "" || solved.OutOfScope || solved.hasDeterministicProblemIssue() {
		content.AnswerAnalysis = k12.TeachingReportAnswerAnalysis{State: k12.TeachingReportReferenceUnavailable, UnavailableReason: firstNonEmpty(solved.ReportUnavailableReason, "本题尚无可用的参考解答")}
	} else {
		var guide ParentTeachingGuide
		if solved.ReportContent != nil {
			guide.ReportContent = solved.ReportContent
		} else if solved.Generation != nil {
			guide, err = auditedSolveParentTeachingGuide(solved)
			if err != nil {
				return nil, err
			}
		} else {
			var ok bool
			guide, ok = deterministicParentTeachingGuideForEvidence(d.parentTeachingGuideRequest(ctx, req, solved, GradeOutcome{}), solved.Evidence)
			if !ok {
				return nil, ErrTeachingReportContentRequired
			}
		}
		source := TeachingReportContentSource{Source: k12.TeachingReportProblemSource{ProblemID: final.FinalizationID, InputRevision: final.InputRevision, InputDigest: final.InputDigest, SolutionDigest: final.ResultDigest}, Solution: solved.Solution, Guide: &guide}
		receipt := final.InvocationID
		if solved.Generation != nil && solved.Generation.AdaptationAuditID != "" {
			receipt = solved.Generation.AdaptationAuditID
		}
		items, deep, err := bindSingleProblemTeachingDraft(source, guide, receipt)
		if err != nil {
			return nil, err
		}
		content, sections = items[0], deep
	}
	report, err := d.buildTextSolveTeachingReport(ctx, TextSolveTeachingReportRequest{OwnerID: final.OwnerID, AgentName: final.AgentName, RunID: final.RunID, Metadata: req.TeachingReport.Metadata, Content: content, DeepSections: sections}, false)
	if err != nil {
		return nil, err
	}
	if len(gradeRefs) == 1 {
		graded, err := d.textGradeAssessment(ctx, final, gradeRefs[0])
		if err != nil {
			return nil, err
		}
		report, err = projectTextGradeReport(report, graded.Result, gradeRefs[0])
		if err != nil {
			return nil, err
		}
	}
	return &report, nil
}

// TextSolveTeachingReportContent 按原结算身份与回执读取完整正文，不求解、渲染或发送。
// 历史来源只核对冻结事实，不因答案资产随后更新或归档拒绝读取。
func (d Deps) TextSolveTeachingReportContent(ctx context.Context, owner, agent, runID, finalizationID, resultDigest string) (string, error) {
	return d.textSolveTeachingReportContent(ctx, owner, agent, runID, finalizationID, resultDigest)
}

// TextSolveTeachingReportContentWithGrade 只读指定的真实评分版本，原无评分接口保持原内容。
func (d Deps) TextSolveTeachingReportContentWithGrade(ctx context.Context, owner, agent, runID, finalizationID, resultDigest string, gradeRef k12.TextGradeReportReference) (string, error) {
	return d.textSolveTeachingReportContent(ctx, owner, agent, runID, finalizationID, resultDigest, gradeRef)
}

func (d Deps) textSolveTeachingReportContent(ctx context.Context, owner, agent, runID, finalizationID, resultDigest string, gradeRefs ...k12.TextGradeReportReference) (string, error) {
	if d.Records == nil || owner == "" || agent == "" || runID == "" {
		return "", fmt.Errorf("%w: trusted text solve identity is required", ErrInvalidInput)
	}
	final, err := d.validateTextSolveFinalization(ctx, owner, agent, runID, false)
	if err != nil {
		return "", err
	}
	if final.FinalizationID != finalizationID || final.ResultDigest != resultDigest {
		return "", fmt.Errorf("%w: text report finalization identity mismatch", k12.ErrTeachingReportInvalid)
	}
	report, err := d.textSolveTeachingReport(ctx, final, gradeRefs...)
	if err != nil {
		return "", err
	}
	if report == nil {
		return "", ErrTeachingReportContentRequired
	}
	return TeachingReportMarkdown(*report)
}

// PrepareTextSolveTeachingReportPDF 沿A11真实结算和现有PrintArtifact冻结，不创建图片任务。
func (d Deps) PrepareTextSolveTeachingReportPDF(ctx context.Context, owner, agent, runID, finalizationID, resultDigest, title string) (PrintableArtifactView, bool, error) {
	return d.prepareTextSolveTeachingReportPDF(ctx, owner, agent, runID, finalizationID, resultDigest, title)
}

// PrepareTextSolveTeachingReportPDFWithGrade 冻结指定评分版本，既有PDF不会被后来评分替换。
func (d Deps) PrepareTextSolveTeachingReportPDFWithGrade(ctx context.Context, owner, agent, runID, finalizationID, resultDigest, title string, gradeRef k12.TextGradeReportReference) (PrintableArtifactView, bool, error) {
	return d.prepareTextSolveTeachingReportPDF(ctx, owner, agent, runID, finalizationID, resultDigest, title, gradeRef)
}

func (d Deps) prepareTextSolveTeachingReportPDF(ctx context.Context, owner, agent, runID, finalizationID, resultDigest, title string, gradeRefs ...k12.TextGradeReportReference) (PrintableArtifactView, bool, error) {
	final, err := d.Records.GetTextSolveFinalization(ctx, owner, agent, runID)
	if err != nil {
		return PrintableArtifactView{}, false, err
	}
	if final.FinalizationID != finalizationID || final.ResultDigest != resultDigest {
		return PrintableArtifactView{}, false, fmt.Errorf("text report finalization identity mismatch")
	}
	report, err := d.textSolveTeachingReport(ctx, final, gradeRefs...)
	if err != nil {
		return PrintableArtifactView{}, false, err
	}
	if report == nil {
		return PrintableArtifactView{}, false, ErrTeachingReportContentRequired
	}
	run, err := d.Records.GetTextSolveRun(ctx, owner, agent, runID)
	if err != nil {
		return PrintableArtifactView{}, false, err
	}
	var request GradeRequest
	if err = json.Unmarshal([]byte(run.RequestJSON), &request); err != nil {
		return PrintableArtifactView{}, false, err
	}
	if request.TeachingReport == nil {
		return PrintableArtifactView{}, false, ErrTeachingReportContentRequired
	}
	req, err := prepareTeachingReportPrintRequest(agent, "text_solve:"+final.FinalizationID+":"+final.ResultDigest, title, TeachingReportFrozenSnapshot{Report: *report, Bundle: request.TeachingReport.Bundle}, nil)
	if err != nil {
		return PrintableArtifactView{}, false, err
	}
	if len(gradeRefs) == 1 {
		req.SourceRef += ":grade:" + gradeRefs[0].InvocationID + ":" + gradeRefs[0].ResultDigest
	}
	identity := buildPrintArtifact(req, d.now())
	if existing, err := d.GetPrintableArtifactPDF(ctx, agent, identity.ArtifactID); err == nil {
		return existing, true, nil
	} else if !errors.Is(err, records.ErrNotFound) {
		return PrintableArtifactView{}, false, err
	}
	if _, err := d.validateTextSolveFinalization(ctx, owner, agent, runID, false); err != nil {
		return PrintableArtifactView{}, false, err
	}
	return d.PreparePrintableArtifact(ctx, req)
}
