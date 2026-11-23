package usecase

import (
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/curriculum"
)

type finalAnswerTeachingProbe struct{ reportResilienceSolver }

func (*finalAnswerTeachingProbe) VerifiedClassroomGuide(problem, constraint, answer string) (ParentTeachingGuide, bool) {
	if problem != "7/9×3/14=" || constraint == "" || answer != "1/6" {
		return ParentTeachingGuide{}, false
	}
	return ParentTeachingGuide{Answer: answer, FullSolutionSteps: []string{"7/9 × 3/14 = (1 × 1)/(3 × 2) = 1/6。"}, GradeLevelMethod: "交叉约分", LikelyMistakes: []string{"只能约去分子与分母的公因数。"}, ParentTeachingSequence: []string{"先找 7 与 14 的公因数。"}, FollowUpQuestions: []string{"3 和 9 怎样约分？"}, CheckingMethod: "1/6 ÷ (3/14) = 7/9。"}, true
}

func TestFinalAnswerOnlyTeachesFromCheckedReferenceWithoutPromotingIt(t *testing.T) {
	d := Deps{Solver: &finalAnswerTeachingProbe{}, Constraint: curriculum.New()}
	ctx := k12.WithGradingModelSnapshot(t.Context(), k12.GradingModelSnapshot{MathExecutionContract: k12.MathExecutionContractClassroom})
	ref := "7/9×3/14=1/6\n答案：1/6"
	solved := SolveHomeworkResult{Solution: ref, Evidence: SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec}}
	req := ParentTeachingGuideRequest{Problem: "7/9×3/14=", Grade: "六年级上", Subject: "数学", ReportRequired: true, WrongStep: "untrusted student line", ErrorCause: "untrusted process"}
	q := RecognizedQuestion{FinalAnswerReceipt: &k12.RecognitionFinalAnswerReceiptV1{}, ImageSourceContext: &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1, SourceComparisonPolicy: k12.RecognitionImageSourceComparisonPolicyV2, ScopeDigest: "original-image", TargetIDs: []string{"target"}}}
	if _, ok := d.currentClassroomMathGuide(ctx, req, solved); ok {
		t.Fatal("ordinary solve accepted a missing execution proof")
	}
	guide, ok := d.finalAnswerClassroomGuide(ctx, req, solved, q, true, ref)
	if !ok || guide.ReportContent == nil || guide.Answer != "1/6" || strings.Contains(strings.Join(append(guide.ParentTeachingSequence, guide.LikelyMistakes...), " "), "untrusted") {
		t.Fatalf("guide=%+v ok=%t", guide, ok)
	}
	if _, ok = d.finalAnswerClassroomGuide(ctx, req, solved, q, false, ref); ok {
		t.Fatal("missing verified final answer accepted")
	}
	if _, ok = d.finalAnswerClassroomGuide(ctx, req, solved, q, true, ref+" changed"); ok {
		t.Fatal("changed solution accepted")
	}
	q.ImageSourceContext.SourceComparisonPolicy = k12.RecognitionImageSourceComparisonPolicyV1
	if _, ok = d.finalAnswerClassroomGuide(ctx, req, solved, q, true, ref); ok {
		t.Fatal("old source received current teaching")
	}
}

func TestActiveEquationGradingPreservesAnExistingProviderOperation(t *testing.T) {
	o, _, run, job := newCurrentReceiptLineageFixture(t, "active-equation-graded")
	o.deps.Constraint = curriculum.New()
	job.Fields.ModelSnapshot.MathExecutionContract = k12.MathExecutionContractClassroom
	q := run.questions[0]
	q.ConfirmationRequired = false
	q.ImageSourceContext = &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1, SourceComparisonPolicy: k12.RecognitionImageSourceComparisonPolicyV2, ScopeDigest: "original-image", TargetIDs: []string{"target"}}
	req := GradeRequest{Subject: "数学", Grade: "六年级上", Problem: "x-1/4x=18", StudentAnswer: "3/4x=18\nx=24"}
	solved := SolveHomeworkResult{Evidence: SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec}}
	ctx := t.Context()
	fresh, err := o.deps.withObservedEquationCurriculum(ctx, job, q, req, solved)
	if err != nil || elementarymath.ObservedEquationCurriculum(fresh) == "" {
		t.Fatalf("new current equation was not eligible: %v", err)
	}
	call := k12.GradingItemInvocation{InvocationID: "original-provider-grade", AgentName: job.Record.AgentName, JobID: job.Record.RecordID, ProblemID: q.ProblemID, AttemptID: q.AttemptID, Operation: k12.GradingItemOperationGrade, OperationAttempt: 1, ExecutionKind: k12.GradingExecutionProvider, RequestDigest: "existing-provider-grade", InputRevision: q.ConfirmedVersion, InputDigest: q.InputDigest, RouteSnapshot: job.Fields.ModelSnapshot}
	if _, _, err = o.deps.Records.PrepareGradingItemInvocation(ctx, call); err != nil {
		t.Fatal(err)
	}
	if _, err = o.deps.Records.MarkGradingItemInvocationSent(ctx, call.AgentName, call.InvocationID); err != nil {
		t.Fatal(err)
	}
	blocked, err := o.deps.withObservedEquationCurriculum(fresh, job, q, req, solved)
	if err != nil || elementarymath.ObservedEquationCurriculum(blocked) != "" {
		t.Fatalf("sent provider invocation bypassed: %v", err)
	}
	saved, err := o.deps.Records.GetGradingItemInvocation(ctx, call.AgentName, call.InvocationID)
	if err != nil || saved.Status != k12.ModelInvocationSent || saved.RequestDigest != call.RequestDigest {
		t.Fatalf("prior call changed: %+v %v", saved, err)
	}
}
