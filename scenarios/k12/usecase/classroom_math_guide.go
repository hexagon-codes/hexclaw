package usecase

import (
	"context"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type classroomMathGuideReader interface {
	VerifiedClassroomGuide(problem, constraint, answer string) (ParentTeachingGuide, bool)
}

// 新任务才按当前原题重算课堂步骤。材料、课程和完整答案有任一项没对上，就沿用原教学链。
func (d Deps) currentClassroomMathGuide(ctx context.Context, req ParentTeachingGuideRequest, solved SolveHomeworkResult) (ParentTeachingGuide, bool) {
	snap, _ := k12.GradingModelSnapshotFromContext(ctx)
	if snap.MathExecutionContract != k12.MathExecutionContractClassroom || req.customExpression || !req.ReportRequired ||
		!isMathSubject(req.Subject) || solved.OutOfScope || solved.ProblemIssue != "" ||
		solved.Evidence.Verdict != VerdictAgree || solved.Evidence.EvidenceType != EvidenceNumericExec {
		return ParentTeachingGuide{}, false
	}
	source := solved.AnswerSource
	if source != nil {
		if source.Kind != k12.ProblemAnswerAsset || source.AdoptionID == "" || source.AssetID == "" || source.AssetVersion < 1 || source.AssetRevision < 1 || source.FactsDigest == "" {
			return ParentTeachingGuide{}, false
		}
	} else if solved.Generation != nil || solved.Evidence.SolverOutputDigest == "" || solved.Evidence.VerificationInputDigest == "" || solved.Evidence.VerificationRunID == "" {
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
