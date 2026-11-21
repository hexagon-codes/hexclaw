package usecase

import (
	"context"
	"errors"
	"strings"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/skill"
)

// QuantityAnswerCompletenessChecker 是求解器可选的只读数量完整性能力，不等同于答案正确性验证。
type QuantityAnswerCompletenessChecker interface {
	QuantityAnswerCompleteness(problem, answer string) (required, complete bool)
}

func validateVisualQuantitySolveResult(ctx context.Context, deps Deps, req GradeRequest, solved SolveHomeworkResult) error {
	snapshot, found := k12.GradingModelSnapshotFromContext(ctx)
	if !found || (snapshot.QuantityAnswerPolicy != k12.QuantityAnswerPolicySlotV1 && snapshot.QuantityAnswerPolicy != k12.QuantityAnswerPolicySlotV2) ||
		req.Subject != "数学" || len(skill.InputImages(ctx)) == 0 || solved.OutOfScope {
		return nil
	}
	checker, ok := deps.Solver.(QuantityAnswerCompletenessChecker)
	if !ok {
		return nil
	}
	answer := solved.Solution
	if solved.Generation != nil && solved.Generation.ParentGuideCandidate != nil {
		answer = solved.Generation.ParentGuideCandidate.Answer
	} else if scope, explicit := explicitVerifiedAnswerScope(solved.Solution); explicit {
		answer, _, _ = strings.Cut(scope, "\n\n")
	}
	required, complete := checker.QuantityAnswerCompleteness(req.Problem, answer)
	if required && !complete {
		return errors.Join(egress.ErrProviderResponseProcessed, ErrSolveFailed,
			errors.New("the numerical answer required by the current quantity slot is incomplete"))
	}
	return nil
}
