package usecase

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

const SolveOutputWithParentGuideV1 = "solve_with_parent_guide_v1"

// SolveGeneration 保存实际选中解法的教学候选和独立审计，不作为数值验算证据。
type SolveGeneration struct {
	OutputVersion        string               `json:"output_version"`
	ParentGuideCandidate *ParentTeachingGuide `json:"parent_guide_candidate,omitempty"`
	GuideAudit           string               `json:"guide_audit"`
	SourceSolutionDigest string               `json:"source_solution_digest"`
}

// SolveTeachingContractFreezer 只组装教学合同，不调用模型；新建求解任务将其固化到运行时。
type SolveTeachingContractFreezer interface {
	FreezeSolveTeachingContract(context.Context, string, string) (string, error)
}

type solveTeachingRequestContextKey struct{}
type solveTeachingRequest struct {
	version  string
	contract string
}

func withSolveTeachingRequest(ctx context.Context, req GradeRequest) context.Context {
	if req.SolveOutputVersion == "" {
		return ctx
	}
	contract := req.ParentTeachingContract
	if len(req.KnowledgePoints) != 0 {
		facts, _ := json.Marshal(struct {
			KnowledgePoints []string `json:"knowledge_points"`
		}{normalizeParentGuideList(req.KnowledgePoints)})
		contract += "\nFrozen problem teaching facts: " + string(facts)
	}
	return context.WithValue(ctx, solveTeachingRequestContextKey{}, solveTeachingRequest{
		version: req.SolveOutputVersion, contract: contract,
	})
}

// SolveTeachingRequest 返回冻结版本和教学合同，供求解适配器透传；旧任务返回空值。
func SolveTeachingRequest(ctx context.Context) (version, contract string) {
	request, _ := ctx.Value(solveTeachingRequestContextKey{}).(solveTeachingRequest)
	return request.version, request.contract
}

func auditedSolveParentTeachingGuide(solved SolveHomeworkResult) (ParentTeachingGuide, error) {
	generation := solved.Generation
	if generation == nil || generation.OutputVersion != SolveOutputWithParentGuideV1 ||
		generation.GuideAudit != "VALID" || generation.ParentGuideCandidate == nil {
		return ParentTeachingGuide{}, fmt.Errorf("%w: selected solve parent guide has no valid independent audit", ErrSolveFailed)
	}
	digest, err := hex.DecodeString(generation.SourceSolutionDigest)
	if err != nil || len(digest) != 32 || (solved.Evidence.SolverOutputDigest != "" &&
		solved.Evidence.SolverOutputDigest != generation.SourceSolutionDigest) {
		return ParentTeachingGuide{}, fmt.Errorf("%w: selected solve parent guide source digest mismatch", ErrSolveFailed)
	}
	if err := validateParentTeachingGuide(normalizeParentTeachingGuide(*generation.ParentGuideCandidate)); err != nil {
		return ParentTeachingGuide{}, fmt.Errorf("%w: selected solve parent guide: %v", ErrSolveFailed, err)
	}
	return finalizeSolveParentTeachingGuide(*generation.ParentGuideCandidate, solved)
}

func finalizeSolveParentTeachingGuide(guide ParentTeachingGuide, solved SolveHomeworkResult) (ParentTeachingGuide, error) {
	generation := solved.Generation
	if solved.Evidence.EvidenceType == EvidenceNumericExec && solved.Evidence.Verdict == VerdictDisagree &&
		generation != nil && generation.OutputVersion == SolveOutputWithParentGuideV1 &&
		generation.GuideAudit == "VALID" && generation.ParentGuideCandidate != nil {
		digest, err := hex.DecodeString(generation.SourceSolutionDigest)
		candidate := normalizeParentTeachingGuide(*generation.ParentGuideCandidate)
		scope, explicit := explicitVerifiedAnswerScope(solved.Solution)
		answerParagraph, _, _ := strings.Cut(scope, "\n\n")
		if err == nil && len(digest) == 32 && generation.SourceSolutionDigest == solved.Evidence.SolverOutputDigest &&
			validateParentTeachingGuide(candidate) == nil && explicit &&
			normalizeAnswerAnchorText(answerParagraph) == normalizeAnswerAnchorText(candidate.Answer) &&
			normalizeAnswerAnchorText(guide.Answer) == normalizeAnswerAnchorText(candidate.Answer) {
			// 主解法过程被否决时，同答案的独立审计讲解不能再被未通过核验的步骤覆盖。
			guide = normalizeParentTeachingGuide(guide)
			guide.FullSolutionSteps = candidate.FullSolutionSteps
			if err := validateParentTeachingGuide(guide); err != nil {
				return ParentTeachingGuide{}, fmt.Errorf("%w: parent teaching guide: %v", ErrSolveFailed, err)
			}
			return guide, nil
		}
	}
	return finalizeParentTeachingGuide(guide, solved.Solution)
}

func parentGuideGenerationForRequest(req GradeRequest, solved SolveHomeworkResult) *SolveGeneration {
	if req.SolveOutputVersion == SolveOutputWithParentGuideV1 {
		return solved.Generation
	}
	return nil
}

func executeDurableParentTeachingGuideRepair(
	ctx context.Context, o *GradingOrchestrator, deps Deps, job GradingJobView,
	q RecognizedQuestion, gradeReq GradeRequest, solved SolveHomeworkResult, originalRequest any,
) (ParentTeachingGuide, string, bool, error) {
	var zero ParentTeachingGuide
	generation := solved.Generation
	if gradeReq.SolveOutputVersion != SolveOutputWithParentGuideV1 || generation == nil ||
		generation.OutputVersion != SolveOutputWithParentGuideV1 || generation.GuideAudit != "INVALID" ||
		generation.ParentGuideCandidate == nil || !photoEvidenceTrusted(solved.Evidence) {
		return zero, "", false, nil
	}
	sourceDigest, err := hex.DecodeString(generation.SourceSolutionDigest)
	if err != nil || len(sourceDigest) != 32 || generation.SourceSolutionDigest != solved.Evidence.SolverOutputDigest {
		return zero, "", false, nil
	}
	requestDigest := modelInvocationResultDigest(originalRequest)
	if correctionID, ok := ctx.Value(assessmentCorrectionContextKey{}).(string); ok {
		requestDigest = modelInvocationResultDigest([]string{requestDigest, correctionID})
	}
	if grounding, ok := gradingProviderGroundingFromContext(ctx); ok {
		requestDigest = modelInvocationDigest([]byte("k12-grading-grounded-request-v1"),
			[]byte(requestDigest), []byte(grounding.identityDigest))
	}
	rows, err := deps.Records.ListGradingItemInvocations(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil {
		return zero, "", false, err
	}
	var failed *k12.GradingItemInvocation
	for i := range rows {
		row := &rows[i]
		if row.ProblemID != q.ProblemID || row.Operation != k12.GradingItemOperationParentGuide ||
			row.ExecutionKind != k12.GradingExecutionLocalDeterministic || row.RequestDigest != requestDigest ||
			row.Status != k12.ModelInvocationFailed || row.FailureClass != "local_execution" ||
			row.FailureCode != "deterministic_failed" {
			continue
		}
		if err := validateGradingItemInvocationIdentity(*row, job, q, requestDigest, k12.GradingExecutionLocalDeterministic); err != nil {
			return zero, row.InvocationID, false, err
		}
		if failed == nil || row.OperationAttempt > failed.OperationAttempt {
			failed = row
		}
	}
	// 只有新的一次正常重试可修复明确失败的指南；同轮失败重放保持原拒绝。
	if failed == nil || job.Fields.AttemptCount+1 <= failed.OperationAttempt {
		return zero, "", false, nil
	}
	if deps.ParentTeachingGuide == nil || deps.ParentTeachingGuideAudit == nil {
		return zero, "", true, fmt.Errorf("%w: parent teaching guide repair generator or auditor unavailable", ErrSolveFailed)
	}
	guideRequest := parentTeachingGuideRequest(gradeReq, solved, GradeOutcome{})
	guideRequest.FrozenTeachingContract = gradeReq.ParentTeachingContract
	guideRequest.RejectedCandidate = generation.ParentGuideCandidate
	ctx, _, err = prepareGradingItemGrounding(ctx, deps, job, q, gradeReq)
	if err != nil {
		return zero, "", true, err
	}
	ctx = withParentInstructions(ctx, failed.RouteSnapshot.ParentInstructions)
	executor := newDurableGradingPhysicalCallExecutor(o, job, q)
	generateRequest := struct {
			Phase                string                     `json:"phase"`
			RepairOf             string                     `json:"repair_of"`
			InputDigest          string                     `json:"input_digest"`
			SourceSolutionDigest string                     `json:"source_solution_digest"`
			Request              ParentTeachingGuideRequest `json:"request"`
		}{"guide_generate", failed.InvocationID, q.InputDigest, generation.SourceSolutionDigest, guideRequest}
	generatedCall, err := executor.ExecuteGradingPhysicalCall(ctx, GradingPhysicalCallSpec{
		Operation: k12.GradingItemOperationParentGuide, RequestDigest: modelInvocationResultDigest(generateRequest),
	}, func(callCtx context.Context) (string, error) {
		candidate, callErr := deps.generateParentTeachingGuide(callCtx, guideRequest)
		if callErr != nil {
			return "", callErr
		}
		raw, encodeErr := json.Marshal(candidate)
		return string(raw), encodeErr
	})
	generatedID := generatedCall.InvocationID
	if err != nil {
		return zero, generatedID, true, err
	}
	var generated ParentTeachingGuide
	if err := json.Unmarshal([]byte(generatedCall.Payload), &generated); err != nil {
		return zero, generatedID, true, fmt.Errorf("%w: decode parent guide generation: %v", ErrModelInvocationRequiresReconciliation, err)
	}
	// 审核对象必须是最终会呈现的完整指南，数学方法仍来自原成功解答。
	guide, err := finalizeParentTeachingGuide(generated, solved.Solution)
	if err != nil {
		return zero, generatedID, true, err
	}
	auditRequest := ParentTeachingGuideAuditRequest{
		Request: guideRequest, Guide: guide, SourceSolutionDigest: generation.SourceSolutionDigest,
	}
	generatedDigest := modelInvocationResultDigest(generated)
	verifyRequest := struct {
			Phase                 string                          `json:"phase"`
			RepairOf              string                          `json:"repair_of"`
			InputDigest           string                          `json:"input_digest"`
			GeneratedInvocationID string                          `json:"generated_invocation_id"`
			GeneratedDigest       string                          `json:"generated_digest"`
			Request               ParentTeachingGuideAuditRequest `json:"request"`
		}{"guide_audit", failed.InvocationID, q.InputDigest, generatedID, generatedDigest, auditRequest}
	auditCall, err := executor.ExecuteGradingPhysicalCall(ctx, GradingPhysicalCallSpec{
		Operation: k12.GradingItemOperationParentGuide, RequestDigest: modelInvocationResultDigest(verifyRequest),
	}, func(callCtx context.Context) (string, error) {
		result, callErr := deps.ParentTeachingGuideAudit.AuditParentTeachingGuide(callCtx, auditRequest)
		if callErr != nil {
			return "", callErr
		}
		raw, encodeErr := json.Marshal(result)
		return string(raw), encodeErr
	})
	auditID := auditCall.InvocationID
	if err != nil {
		return zero, auditID, true, err
	}
	var audit ParentTeachingGuideAudit
	if err := json.Unmarshal([]byte(auditCall.Payload), &audit); err != nil {
		return zero, auditID, true, fmt.Errorf("%w: decode parent guide audit: %v", ErrModelInvocationRequiresReconciliation, err)
	}
	final, finalID, err := executeGradingItemOperationWithKind(ctx, o, job, q,
		k12.GradingItemOperationParentGuide, k12.GradingExecutionLocalDeterministic,
		struct {
			Phase                 string                   `json:"phase"`
			RepairOf              string                   `json:"repair_of"`
			InputDigest           string                   `json:"input_digest"`
			SourceSolutionDigest  string                   `json:"source_solution_digest"`
			GeneratedInvocationID string                   `json:"generated_invocation_id"`
			GeneratedDigest       string                   `json:"generated_digest"`
			AuditInvocationID     string                   `json:"audit_invocation_id"`
			Audit                 ParentTeachingGuideAudit `json:"audit"`
			Guide                 ParentTeachingGuide      `json:"guide"`
		}{"guide_finalize", failed.InvocationID, q.InputDigest, generation.SourceSolutionDigest,
			generatedID, generatedDigest, auditID, audit, guide},
		func(context.Context) (ParentTeachingGuide, error) {
			if audit.Verdict != "VALID" || audit.SourceSolutionDigest != generation.SourceSolutionDigest || strings.TrimSpace(audit.Output) == "" {
				return zero, fmt.Errorf("%w: repaired parent guide has no valid independent audit", ErrSolveFailed)
			}
			return finalizeParentTeachingGuide(guide, solved.Solution)
		})
	return final, finalID, true, err
}
