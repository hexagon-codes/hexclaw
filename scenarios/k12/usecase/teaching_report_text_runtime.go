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

func (d Deps) textSolveTeachingReport(ctx context.Context, final k12.TextSolveFinalization) (*k12.TeachingReport, error) {
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
		content.AnswerAnalysis = k12.TeachingReportAnswerAnalysis{State: k12.TeachingReportReferenceUnavailable, UnavailableReason: "本题尚无可用的参考解答"}
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
	return &report, nil
}

// PrepareTextSolveTeachingReportPDF 沿A11真实结算和现有PrintArtifact冻结，不创建图片任务。
func (d Deps) PrepareTextSolveTeachingReportPDF(ctx context.Context, owner, agent, runID, finalizationID, resultDigest, title string) (PrintableArtifactView, bool, error) {
	final, err := d.Records.GetTextSolveFinalization(ctx, owner, agent, runID)
	if err != nil {
		return PrintableArtifactView{}, false, err
	}
	if final.FinalizationID != finalizationID || final.ResultDigest != resultDigest {
		return PrintableArtifactView{}, false, fmt.Errorf("text report finalization identity mismatch")
	}
	report, err := d.textSolveTeachingReport(ctx, final)
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
	identity := buildPrintArtifact(req, d.now())
	if existing, err := d.GetPrintableArtifactPDF(ctx, agent, identity.ArtifactID); err == nil {
		return existing, true, nil
	} else if !errors.Is(err, records.ErrNotFound) {
		return PrintableArtifactView{}, false, err
	}
	if _, err := d.ValidateTextSolveFinalization(ctx, owner, agent, runID); err != nil {
		return PrintableArtifactView{}, false, err
	}
	return d.PreparePrintableArtifact(ctx, req)
}
