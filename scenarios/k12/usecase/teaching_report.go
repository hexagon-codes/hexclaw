package usecase

import (
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

var ErrTeachingReportContentRequired = errors.New("structured teaching report content is required")

// TeachingReportQuestionContent 是教学阶段已核对的展示结构，不包含学生状态或原始作答。
// 旧指南不能可靠拆成该结构时由调用方选择历史回放，组装器不截句或猜答案。
type TeachingReportQuestionContent struct {
	ProblemID            string
	SourceSolutionDigest string
	ShortTitle           string
	AnswerAnalysis       k12.TeachingReportAnswerAnalysis
}

// GradingTeachingReportInput 必须来自调用方按真实身份读回的同次作业快照。
// ObservedSlots 仅接受已有位置证据，不接受教学模型推测的学生作答。
type GradingTeachingReportInput struct {
	AgentName     string
	Source        k12.TeachingReportSource
	Metadata      k12.TeachingReportMetadata
	Questions     []RecognizedQuestion
	Assessments   []k12.GradingAssessmentItem
	Skips         []k12.ProblemSkipReceipt
	Content       []TeachingReportQuestionContent
	ObservedSlots map[string][]k12.TeachingReportAnswerSlot
	SourcePages   []k12.TeachingReportSourcePage
	DeepSections  []k12.TeachingReportDeepSection
}

// BuildGradingTeachingReport 纯组装图片批改报告，不修改回执、不执行模型、存储或投递。
// 文本最终结算由独立来源适配接入，不使用此函数伪造 GradingJob。
func BuildGradingTeachingReport(input GradingTeachingReportInput) (k12.TeachingReport, error) {
	invalid := func(reason string) (k12.TeachingReport, error) {
		return k12.TeachingReport{}, fmt.Errorf("%w: %s", k12.ErrTeachingReportInvalid, reason)
	}
	if input.Source.Kind != k12.TeachingReportSourceGrading || strings.TrimSpace(input.AgentName) == "" {
		return invalid("grading source and agent are required")
	}
	assessments := make(map[string]k12.GradingAssessmentItem, len(input.Assessments))
	skips := make(map[string]k12.ProblemSkipReceipt, len(input.Skips))
	for _, skip := range input.Skips {
		if skip.AgentName != input.AgentName || skip.JobID != input.Source.SourceID || skip.CurrentDisposition != k12.GradingAssessmentDispositionCurrent || skips[skip.ProblemID].ProblemID != "" {
			return invalid("skip does not match the current grading source")
		}
		skips[skip.ProblemID] = skip
	}
	for _, assessment := range input.Assessments {
		if _, duplicate := assessments[assessment.ProblemID]; duplicate {
			return invalid("duplicate assessment problem")
		}
		if assessment.AgentName != input.AgentName || assessment.JobID != input.Source.SourceID ||
			assessment.CurrentDisposition != k12.GradingAssessmentDispositionCurrent {
			return invalid("assessment source does not match the current grading job")
		}
		assessments[assessment.ProblemID] = assessment
	}
	content := make(map[string]TeachingReportQuestionContent, len(input.Content))
	for _, item := range input.Content {
		if _, duplicate := content[item.ProblemID]; duplicate || strings.TrimSpace(item.ProblemID) == "" {
			return invalid("duplicate or empty content problem")
		}
		content[item.ProblemID] = item
	}
	report := k12.TeachingReport{
		SchemaVersion: k12.TeachingReportSchemaVersion, ContentVersion: k12.TeachingReportContentVersion,
		Source: input.Source, Metadata: input.Metadata, SourcePages: input.SourcePages, DeepSections: input.DeepSections,
	}
	seen := make(map[string]bool)
	for _, question := range input.Questions {
		if question.ProblemKind == ProblemKindCompoundParent {
			continue
		}
		if seen[question.ProblemID] || strings.TrimSpace(question.ProblemID) == "" {
			return invalid("duplicate or empty question identity")
		}
		seen[question.ProblemID] = true
		if skip, ok := skips[question.ProblemID]; ok {
			if _, duplicate := assessments[question.ProblemID]; duplicate || skip.InputRevision != question.ConfirmedVersion {
				return invalid("skip and question revision do not match")
			}
			state := reportObservedAnswerState(question.AnswerState)
			raw, canonical := question.AnswerRawTranscription, question.AnswerCanonicalMarkdown
			if raw == "" {
				raw = question.StudentAnswer
			}
			if canonical == "" {
				canonical = question.StudentAnswer
			}
			report.ProblemSources = append(report.ProblemSources, k12.TeachingReportProblemSource{ProblemID: question.ProblemID, ParentProblemID: question.ParentProblemID, AttemptID: question.AttemptID, InputRevision: question.ConfirmedVersion, InputDigest: question.InputDigest, AssessmentDigest: skip.ResultDigest, AnswerState: state, AnswerRaw: raw, AnswerCanonical: canonical})
			report.Items = append(report.Items, k12.TeachingReportItem{ProblemID: question.ProblemID, DisplayLabel: RecognizedQuestionSourceDisplayLabel(question), ShortTitle: question.Question, Assessment: k12.TeachingReportAssessment{Reason: "problem_skipped"},
				AnswerAnalysis: k12.TeachingReportAnswerAnalysis{State: k12.TeachingReportReferenceUnavailable, UnavailableReason: "暂未提供参考解答"}, OriginalAnswer: k12.TeachingReportOriginalAnswer{State: state, RawText: raw, CanonicalText: canonical}})
			continue
		}
		assessment, ok := assessments[question.ProblemID]
		if !ok {
			return k12.TeachingReport{}, fmt.Errorf("%w: problem %s has no current assessment", ErrTeachingReportContentRequired, question.ProblemID)
		}
		if assessment.AttemptID != question.AttemptID ||
			assessment.InputRevision != question.ConfirmedVersion || assessment.ConfirmedVersion != question.ConfirmedVersion ||
			assessment.InputDigest != question.InputDigest {
			return invalid("question and assessment input do not match")
		}
		// 复用原回执重放检查，不把外部传入的讲法当作评分依据。
		item, err := replayGradingAssessmentItem(question, assessment)
		if err != nil {
			return invalid("assessment receipt cannot be replayed: " + err.Error())
		}
		teaching, ok := content[question.ProblemID]
		if !ok || strings.TrimSpace(teaching.ShortTitle) == "" {
			return k12.TeachingReport{}, fmt.Errorf("%w: problem %s", ErrTeachingReportContentRequired, question.ProblemID)
		}
		observed := item.Recognized
		if observed.ConfirmedVersion != question.ConfirmedVersion || observed.ParentProblemID != question.ParentProblemID {
			return invalid("replayed question revision or parent differs from the source")
		}
		raw, canonical := observed.AnswerRawTranscription, observed.AnswerCanonicalMarkdown
		if raw == "" {
			raw = observed.StudentAnswer
		}
		if canonical == "" {
			canonical = observed.StudentAnswer
		}
		state := reportObservedAnswerState(observed.AnswerState)
		solution, evidence := item.Solve.Solution, item.Solve.Evidence
		if strings.TrimSpace(solution) == "" {
			solution = item.Grade.Solution
			evidence = item.Grade.Evidence
		}
		analysis := teaching.AnswerAnalysis
		solutionDigest := firstNonEmpty(evidence.SolverOutputDigest, teaching.SourceSolutionDigest)
		if evidence.SolverOutputDigest != "" && teaching.SourceSolutionDigest != "" && evidence.SolverOutputDigest != teaching.SourceSolutionDigest {
			return invalid("teaching content refers to a different source solution")
		}
		if analysis.State != k12.TeachingReportReferenceUnavailable &&
			(strings.TrimSpace(solution) == "" || strings.TrimSpace(solutionDigest) == "") {
			return k12.TeachingReport{}, fmt.Errorf("%w: problem %s has no source solution identity", ErrTeachingReportContentRequired, question.ProblemID)
		}
		receipt := assessment.SolveInvocationID
		if receipt == "" && assessment.AnswerSource != nil {
			receipt = firstNonEmpty(assessment.AnswerSource.InvocationID, assessment.AnswerSource.AdoptionID)
		}
		analysis.Evidence = k12.TeachingReportReferenceEvidence{
			Verdict: string(evidence.Verdict), EvidenceType: string(evidence.EvidenceType),
			ReceiptRef: receipt, SolutionDigest: solutionDigest,
			VerificationRunID: evidence.VerificationRunID, VerificationDigest: evidence.VerificationInputDigest,
		}
		source := k12.TeachingReportProblemSource{
			ProblemID: question.ProblemID, ParentProblemID: question.ParentProblemID, AttemptID: question.AttemptID,
			InputRevision: question.ConfirmedVersion, InputDigest: question.InputDigest,
			AssessmentDigest: assessment.ResultDigest, AssessmentStatus: assessment.Status,
			SolutionDigest: solutionDigest, AnswerState: state, AnswerRaw: raw,
			AnswerCanonical: canonical, AnswerSource: assessment.AnswerSource,
		}
		report.ProblemSources = append(report.ProblemSources, source)
		report.Items = append(report.Items, k12.TeachingReportItem{
			ProblemID: question.ProblemID, DisplayLabel: RecognizedQuestionSourceDisplayLabel(question), ShortTitle: teaching.ShortTitle,
			Assessment: k12.TeachingReportAssessment{Status: assessment.Status}, AnswerAnalysis: analysis,
			OriginalAnswer: k12.TeachingReportOriginalAnswer{
				State: state, RawText: raw, CanonicalText: canonical, Slots: input.ObservedSlots[question.ProblemID],
			},
		})
	}
	if len(seen) != len(assessments)+len(skips) || len(assessments) != len(content) {
		return invalid("report questions, assessments and content must have the same exact set")
	}
	for problemID := range input.ObservedSlots {
		if !seen[problemID] {
			return invalid("answer slots refer to an unrelated problem")
		}
	}
	return k12.NewTeachingReport(report)
}

func reportObservedAnswerState(state AnswerState) k12.TeachingReportAnswerState {
	switch state {
	case AnswerStateBlank:
		return k12.TeachingReportAnswerBlank
	case AnswerStatePresent:
		return k12.TeachingReportAnswerPresent
	case AnswerStateUnclear:
		return k12.TeachingReportAnswerUnreadable
	default:
		return k12.TeachingReportAnswerUnknown
	}
}
