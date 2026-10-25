package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type textGradeAssessmentSnapshot struct {
	Version            string      `json:"version"`
	FinalizationID     string      `json:"finalization_id"`
	FinalizationDigest string      `json:"finalization_digest"`
	Result             GradeResult `json:"result"`
}

// 评分沿原文字运行记录真实编排与物理调用，不修改先前冻结的求解结果。
func (d Deps) gradeFinalizedTextProblem(ctx context.Context, req GradeRequest, solved SolveHomeworkResult) (GradeResult, error) {
	final := solved.Finalization
	if final == nil || solved.OutOfScope {
		return d.gradeSolvedHomeworkProblem(ctx, req, solved)
	}
	run, err := d.Records.GetTextSolveRun(ctx, final.OwnerID, final.AgentName, final.RunID)
	if err != nil {
		return GradeResult{}, err
	}
	var frozen GradeRequest
	if json.Unmarshal([]byte(run.RequestJSON), &frozen) != nil || frozen.AgentName != req.AgentName || frozen.Problem != req.Problem || frozen.StudentAnswer != req.StudentAnswer || strings.TrimSpace(frozen.StudentAnswer) == "" {
		return GradeResult{}, k12storage.ErrTextSolveConflict
	}
	ctx = WithGradingPhysicalCallExecutor(k12.WithGradingModelSnapshot(ctx, run.RouteSnapshot), &textSolvePhysicalExecutor{records: d.Records, run: run})
	physical, err := ExecuteGradingProgramCall(ctx, GradingPhysicalCallSpec{Operation: k12.GradingItemOperationGrade, RequestDigest: modelInvocationResultDigest([]string{k12.TextGradeAssessmentVersion, final.FinalizationID, final.ResultDigest, run.InputDigest})}, func(callCtx context.Context) (string, error) {
		graded, err := d.gradeSolvedHomeworkProblem(callCtx, frozen, solved)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(textGradeAssessmentSnapshot{Version: k12.TextGradeAssessmentVersion, FinalizationID: final.FinalizationID, FinalizationDigest: final.ResultDigest, Result: graded})
		return string(raw), err
	})
	if err != nil {
		return GradeResult{}, err
	}
	ref := k12.TextGradeReportReference{InvocationID: physical.InvocationID, ResultDigest: modelInvocationDigest([]byte(physical.Payload))}
	snapshot, err := d.textGradeAssessment(ctx, *final, ref)
	if err != nil {
		return GradeResult{}, err
	}
	result := snapshot.Result
	result.GradeReportRef = &ref
	if solved.TeachingReport != nil {
		report, err := projectTextGradeReport(*solved.TeachingReport, snapshot.Result, ref)
		if err != nil {
			return GradeResult{}, err
		}
		result.TeachingReport = &report
	}
	return result, nil
}

func (d Deps) textGradeAssessment(ctx context.Context, final k12.TextSolveFinalization, ref k12.TextGradeReportReference) (textGradeAssessmentSnapshot, error) {
	var snapshot textGradeAssessmentSnapshot
	inv, err := d.Records.ValidateTextGradeReportReference(ctx, final, ref)
	if err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal([]byte(inv.ResultJSON), &snapshot); err != nil {
		return snapshot, err
	}
	if snapshot.Result.Finalization == nil || snapshot.Result.Finalization.FinalizationID != final.FinalizationID || snapshot.Result.Finalization.ResultDigest != final.ResultDigest || snapshot.Result.TeachingReport != nil || snapshot.Result.GradeReportRef != nil {
		return snapshot, k12storage.ErrTextSolveConflict
	}
	return snapshot, nil
}

// 评分状态来自已完成回执，原答和参考解仍由原报告来源提供。
func projectTextGradeReport(base k12.TeachingReport, result GradeResult, ref k12.TextGradeReportReference) (k12.TeachingReport, error) {
	if base.Source.Kind != k12.TeachingReportSourceTextSolve || len(base.Items) != 1 || len(base.ProblemSources) != 1 || base.Items[0].OriginalAnswer.State != k12.TeachingReportAnswerPresent || result.Outcome.Verdict == "" {
		return k12.TeachingReport{}, fmt.Errorf("%w: text grading requires the original submitted answer and actual assessment", k12.ErrTeachingReportInvalid)
	}
	report := base.Clone()
	status := result.Outcome.AssessmentStatus()
	report.ProblemSources[0].AssessmentStatus = status
	report.ProblemSources[0].AssessmentDigest = ref.ResultDigest
	report.Items[0].Assessment = k12.TeachingReportAssessment{Status: status}
	var blocks []k12.TeachingReportExplanationBlock
	source := report.ProblemSources[0]
	origin := k12.TeachingReportContentOrigin{ProblemID: source.ProblemID, InputRevision: source.InputRevision, InputDigest: source.InputDigest, SolutionDigest: source.SolutionDigest}
	for _, entry := range []struct{ id, label, value string }{{"wrong-step", "第一个错步：", result.Outcome.WrongStep}, {"error-cause", "错因：", result.Outcome.ErrorCause}} {
		if strings.TrimSpace(entry.value) != "" {
			blocks = append(blocks, k12.TeachingReportExplanationBlock{ID: source.ProblemID + ":assessment:" + entry.id, Text: entry.label + entry.value, Origins: []k12.TeachingReportContentOrigin{origin}})
		}
	}
	if len(blocks) > 0 {
		report.DeepSections = append(report.DeepSections, k12.TeachingReportDeepSection{ID: source.ProblemID + ":assessment", Title: "批改说明", ProblemIDs: []string{source.ProblemID}, Blocks: blocks})
	}
	return k12.NewTeachingReport(report)
}
