package usecase

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	return finalizeParentTeachingGuide(*generation.ParentGuideCandidate, solved.Solution)
}

func parentGuideGenerationForRequest(req GradeRequest, solved SolveHomeworkResult) *SolveGeneration {
	if req.SolveOutputVersion == SolveOutputWithParentGuideV1 {
		return solved.Generation
	}
	return nil
}
