package usecase

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type classroomMathGuideReader interface {
	VerifiedClassroomGuide(problem, constraint, answer string) (ParentTeachingGuide, bool)
}

// 新任务才按当前原题重算课堂步骤。材料、课程和完整答案有任一项没对上，就沿用原教学链。
func (d Deps) currentClassroomMathGuide(ctx context.Context, req ParentTeachingGuideRequest, solved SolveHomeworkResult) (ParentTeachingGuide, bool) {
	source := solved.AnswerSource
	if source != nil {
		if source.Kind != k12.ProblemAnswerAsset || source.AdoptionID == "" || source.AssetID == "" || source.AssetVersion < 1 || source.AssetRevision < 1 || source.FactsDigest == "" {
			return ParentTeachingGuide{}, false
		}
	} else if solved.Generation != nil || solved.Evidence.SolverOutputDigest == "" || solved.Evidence.VerificationInputDigest == "" || solved.Evidence.VerificationRunID == "" {
		return ParentTeachingGuide{}, false
	}
	return d.classroomGuideFromVerifiedProblem(ctx, req, solved)
}

// 末值证据只证明这个学生终答与原算式的精确值不同。讲法仅使用同次已核原解，
// 不把未确认的过程或错因带入，也不将来源升级为通用求解执行证明。
func (d Deps) finalAnswerClassroomGuide(ctx context.Context, req ParentTeachingGuideRequest, solved SolveHomeworkResult, q RecognizedQuestion, confirmed bool, reference string) (ParentTeachingGuide, bool) {
	if !confirmed || reference == "" || solved.Solution != reference || q.FinalAnswerReceipt == nil || !currentSingleObservedAnswer(q) {
		return ParentTeachingGuide{}, false
	}
	req.WrongStep, req.ErrorCause = "", ""
	return d.classroomGuideFromVerifiedProblem(ctx, req, solved)
}

func currentSingleObservedAnswer(q RecognizedQuestion) bool {
	source := q.ImageSourceContext
	return source != nil && source.Version == k12.RecognitionImageScopeVersionV1 && source.SourceComparisonPolicy == k12.RecognitionImageSourceComparisonPolicyV2 &&
		source.ScopeDigest != "" && len(source.TargetIDs) == 1
}

func (d Deps) withObservedEquationCurriculum(ctx context.Context, job GradingJobView, q RecognizedQuestion, req GradeRequest, solved SolveHomeworkResult) (context.Context, error) {
	if elementarymath.ObservedEquationCurriculum(ctx) != "" {
		ctx = elementarymath.WithObservedEquationCurriculum(ctx, "")
	}
	if d.Records == nil || job.Record == nil || job.Fields.ModelSnapshot.MathExecutionContract != k12.MathExecutionContractClassroom || !currentSingleObservedAnswer(q) ||
		q.ConfirmationRequired || q.AnswerState != AnswerStatePresent || req.PracticeReference != nil || !isMathSubject(req.Subject) ||
		!strings.ContainsAny(req.StudentAnswer, "\r\n") || !strings.ContainsAny(req.Problem, "xX") || !strings.ContainsAny(req.Problem, "=＝") ||
		solved.OutOfScope || solved.Evidence.EvidenceType != EvidenceNumericExec || solved.Evidence.Verdict != VerdictAgree {
		return ctx, nil
	}
	calls, err := d.Records.ListGradingItemInvocations(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil {
		return ctx, err
	}
	// 已有模型评分始终走原回执；本地评分重放则由同输入的本地账本返回旧结果。
	if slices.ContainsFunc(calls, func(call k12.GradingItemInvocation) bool {
		return call.Operation == k12.GradingItemOperationGrade && call.ExecutionKind == k12.GradingExecutionProvider && call.ProblemID == q.ProblemID &&
			(call.InputRevision == 0 || call.InputRevision == q.ConfirmedVersion && call.InputDigest == q.InputDigest)
	}) {
		return ctx, nil
	}
	return elementarymath.WithObservedEquationCurriculum(ctx, d.constraintFor(ctx, req.Grade)), nil
}

func (d Deps) classroomGuideFromVerifiedProblem(ctx context.Context, req ParentTeachingGuideRequest, solved SolveHomeworkResult) (ParentTeachingGuide, bool) {
	snap, _ := k12.GradingModelSnapshotFromContext(ctx)
	if snap.MathExecutionContract != k12.MathExecutionContractClassroom || req.customExpression || !req.ReportRequired ||
		!isMathSubject(req.Subject) || solved.OutOfScope || solved.ProblemIssue != "" ||
		solved.Evidence.Verdict != VerdictAgree || solved.Evidence.EvidenceType != EvidenceNumericExec {
		return ParentTeachingGuide{}, false
	}
	reader, ok := d.Solver.(classroomMathGuideReader)
	answers, explicit := verifiedReportAnswerSections(solved.Solution)
	if !ok || !explicit || len(answers) != 1 {
		return ParentTeachingGuide{}, false
	}
	constraint := d.constraintFor(ctx, req.Grade)
	if constraint == "" {
		return ParentTeachingGuide{}, false
	}
	guide, ok := reader.VerifiedClassroomGuide(req.Problem, constraint, strings.TrimSpace(answers[0].Text))
	if !ok {
		return ParentTeachingGuide{}, false
	}
	// 数学验算和原始作答分别归属。只在批改已指明具体错步时把它加入本次讲题。
	if req.WrongStep != "" {
		guide.ParentTeachingSequence = append(guide.ParentTeachingSequence, "请孩子对照自己的这一步重新计算："+req.WrongStep)
	}
	if req.ErrorCause != "" {
		guide.LikelyMistakes = append(guide.LikelyMistakes, req.ErrorCause)
	}
	draft := deterministicReportContent(guide)
	section := &draft.Sections[0]
	section.Blocks = append([]TeachingReportDraftBlock{{ID: "classroom-method", ProblemIDs: []string{"current"}, Text: guide.GradeLevelMethod}}, section.Blocks...)
	section.Blocks = append(section.Blocks,
		TeachingReportDraftBlock{ID: "classroom-watch", ProblemIDs: []string{"current"}, Text: strings.Join(guide.LikelyMistakes, "\n"), Purpose: k12.TeachingReportExplanationPitfall},
		TeachingReportDraftBlock{ID: "classroom-prompt", ProblemIDs: []string{"current"}, Text: strings.Join(guide.ParentTeachingSequence, "\n")},
		TeachingReportDraftBlock{ID: "classroom-question", ProblemIDs: []string{"current"}, Text: strings.Join(guide.FollowUpQuestions, "\n")},
		TeachingReportDraftBlock{ID: "classroom-check", ProblemIDs: []string{"current"}, Text: guide.CheckingMethod})
	guide.ReportContent = draft
	return guide, validateParentTeachingGuide(guide) == nil && ValidateSingleProblemReportDraft(draft) == nil &&
		answerAnchoredInVerifiedSolution(guide.Answer, solved.Solution)
}

// 答案仍锚定原解；本次逐步重算的讲法不能被历史解答中的旧段落覆盖。
func finalizeCurrentClassroomGuide(raw ParentTeachingGuide, solved SolveHomeworkResult, classroom *ParentTeachingGuide) (ParentTeachingGuide, error) {
	if classroom == nil {
		return finalizeSolveParentTeachingGuide(raw, solved)
	}
	if modelInvocationResultDigest(raw) != modelInvocationResultDigest(*classroom) {
		return ParentTeachingGuide{}, fmt.Errorf("%w: classroom teaching differs from the current problem calculation", ErrGradingAssessmentExactSet)
	}
	return raw, nil
}
