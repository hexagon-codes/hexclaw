package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// adaptReusedReportTeaching 只适配本次教学内容，原采用解法、数值证据和资产保持不变。
// 每个真实调用沿原任务账本恢复；已发送未知结果由该账本阻止重复发送。
func (d Deps) adaptReusedReportTeaching(ctx context.Context, req GradeRequest, solved SolveHomeworkResult) (SolveHomeworkResult, error) {
	if req.SolveOutputVersion != SolveOutputWithTeachingReportV2 || solved.OutOfScope || solved.Solution == "" {
		return solved, nil
	}
	if solved.reportLocalSource {
		request := d.parentTeachingGuideRequest(ctx, req, solved, GradeOutcome{})
		request.ReportRequired = false
		if guide, ok := deterministicParentTeachingGuideForEvidence(request, solved.Evidence); ok {
			solved.Generation = nil
			solved.ReportContent = deterministicReportContent(guide)
			return solved, nil
		}
	}
	if d.ParentTeachingGuide == nil || d.ParentTeachingGuideAudit == nil {
		return solved, fmt.Errorf("report teaching adapter is unavailable")
	}
	request := d.parentTeachingGuideRequest(ctx, req, solved, GradeOutcome{})
	digest := modelInvocationResultDigest(struct {
		Phase   string
		Request ParentTeachingGuideRequest
		Source  *k12.ProblemAnswerSource
	}{"report_current_teaching", request, solved.AnswerSource})
	generated, err := ExecuteGradingPhysicalCall(ctx, GradingPhysicalCallSpec{Operation: k12.GradingItemOperationParentGuide, RequestDigest: digest}, func(callCtx context.Context) (string, error) {
		guide, err := d.generateParentTeachingGuide(callCtx, request)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(guide)
		return string(raw), err
	})
	if err != nil {
		return solved, err
	}
	var guide ParentTeachingGuide
	if err := json.Unmarshal([]byte(generated.Payload), &guide); err != nil {
		return solved, err
	}
	guide, err = finalizeParentTeachingGuide(guide, solved.Solution)
	if err != nil {
		return solved, err
	}
	if guide.ReportContent == nil {
		return solved, ErrTeachingReportContentRequired
	}
	sourceDigest := solved.Evidence.SolverOutputDigest
	if sourceDigest == "" {
		sourceDigest = teachingReportResourceDigest([]byte(solved.Solution))
	}
	auditRequest := ParentTeachingGuideAuditRequest{Request: request, Guide: guide, SourceSolutionDigest: sourceDigest}
	audited, err := ExecuteGradingPhysicalCall(ctx, GradingPhysicalCallSpec{Operation: k12.GradingItemOperationParentGuide, ReplayResultFailure: func(payload string) error {
		var prior ParentTeachingGuideAudit
		if err := json.Unmarshal([]byte(payload), &prior); err != nil {
			return err
		}
		if prior.Verdict != "VALID" || prior.SourceSolutionDigest != sourceDigest {
			return fmt.Errorf("report teaching audit did not accept the candidate")
		}
		return nil
	}, RequestDigest: modelInvocationResultDigest(struct {
		Phase   string
		Request ParentTeachingGuideAuditRequest
	}{"report_current_teaching_audit", auditRequest})}, func(callCtx context.Context) (string, error) {
		audit, err := d.ParentTeachingGuideAudit.AuditParentTeachingGuide(callCtx, auditRequest)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(audit)
		return string(raw), err
	})
	if err != nil {
		return solved, err
	}
	var audit ParentTeachingGuideAudit
	if err := json.Unmarshal([]byte(audited.Payload), &audit); err != nil {
		return solved, err
	}
	if audit.Verdict != "VALID" || audit.SourceSolutionDigest != sourceDigest {
		return solved, fmt.Errorf("report teaching audit did not accept the candidate")
	}
	solved.Generation = &SolveGeneration{OutputVersion: SolveOutputWithTeachingReportV2, ParentGuideCandidate: &guide, GuideAudit: audit.Verdict, SourceSolutionDigest: sourceDigest,
		SkillContextReceipts: guide.SkillContextReceipts, AdaptationGenerationID: generated.InvocationID, AdaptationAuditID: audited.InvocationID}
	return solved, nil
}

// deterministicReportContent 仅投影本机精确计算已有的单一完整答案与步骤，不猜测多空。
func legacyDeterministicReportContent(guide ParentTeachingGuide) *TeachingReportContentDraft {
	blocks := make([]TeachingReportDraftBlock, 0, len(guide.FullSolutionSteps)+1)
	for i, step := range guide.FullSolutionSteps {
		blocks = append(blocks, TeachingReportDraftBlock{ID: fmt.Sprintf("calculation-%d", i+1), ProblemIDs: []string{"current"}, Text: step})
	}
	if guide.CheckingMethod != "" {
		blocks = append(blocks, TeachingReportDraftBlock{ID: "check", ProblemIDs: []string{"current"}, Text: guide.CheckingMethod})
	}
	return &TeachingReportContentDraft{Version: TeachingReportContentContractVersion, Items: []TeachingReportDraftItem{{ProblemID: "current", ShortTitle: "计算结果", Answers: []TeachingReportDraftAnswer{{ID: "answer", Text: guide.Answer}}}},
		Sections: []TeachingReportDraftSection{{ID: "calculation", Title: "计算与检查", ProblemIDs: []string{"current"}, Blocks: blocks}}}
}

// deterministicReportContent 只保留原解法中的具体步骤，不追加泛化提醒。
func deterministicReportContent(guide ParentTeachingGuide) *TeachingReportContentDraft {
	draft := legacyDeterministicReportContent(guide)
	section := &draft.Sections[0]
	section.Title = "计算过程"
	section.Blocks = section.Blocks[:len(guide.FullSolutionSteps)]
	return draft
}

// deterministicReportUnavailableReason 仅呈现已由本地规则确认的矛盾说明，随原结算冻结。
func deterministicReportUnavailableReason(solved SolveHomeworkResult) string {
	if !solved.hasDeterministicProblemIssue() {
		return ""
	}
	hint, ok := deterministicNumericAnswer(solved.Solution)
	if !ok {
		return ""
	}
	var lines []string
	for _, line := range strings.Split(hint, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, strings.Trim(line, "*"))
		}
	}
	return strings.Join(lines, " ")
}
