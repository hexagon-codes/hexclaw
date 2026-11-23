package engineadapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// RestoreTeachingReportGuide 解码原联合生成及八字段审核，不调用模型或执行工具。
func (a *SolveAdapter) RestoreTeachingReportGuide(solution, sourceDigest string, receipts []usecase.TeachingReportSolveReceipt) (usecase.ParentTeachingGuide, string, string, error) {
	type candidate struct{ solution, digest, guideJSON string }
	var candidates []candidate
	var sources []string
	for _, receipt := range receipts {
		if receipt.Operation != k12.GradingItemOperationSolveGenerate {
			continue
		}
		var generated engine.SubAgentResult
		if json.Unmarshal(receipt.Payload, &generated) != nil || generated.Generation == nil || generated.Generation.OutputVersion != usecase.SolveOutputWithTeachingReportV2 || strings.TrimSpace(generated.Output) == "" {
			continue
		}
		sum := sha256.Sum256([]byte(generated.Output))
		digest := hex.EncodeToString(sum[:])
		candidates = append(candidates, candidate{generated.Output, digest, generated.Generation.ParentGuideJSON})
		sources = append(sources, digest)
	}
	for _, item := range candidates {
		if (sourceDigest != "" && sourceDigest != item.digest) || (sourceDigest == "" && !strings.Contains(solution, normalizeSolveMarkdown(item.solution))) {
			continue
		}
		for _, auditReceipt := range receipts {
			if auditReceipt.Operation != k12.GradingItemOperationSolveVerify {
				continue
			}
			var audit engine.SubAgentResult
			if json.Unmarshal(auditReceipt.Payload, &audit) != nil || engine.TeachingReportGuideAuditVerdictForSources(audit.Output, item.digest, sources) != "VALID" {
				continue
			}
			candidate := solveGenerationFromMetadata(usecase.SolveOutputWithTeachingReportV2, map[string]string{
				"solve_parent_guide_json": item.guideJSON, "solve_parent_guide_source_digest": item.digest, "solve_parent_guide_audit": "VALID",
			})
			if candidate != nil && candidate.GuideAudit == "VALID" && candidate.ParentGuideCandidate != nil && usecase.ValidateSingleProblemReportDraft(candidate.ParentGuideCandidate.ReportContent) == nil {
				return *candidate.ParentGuideCandidate, item.digest, auditReceipt.InvocationID, nil
			}
		}
	}
	return usecase.ParentTeachingGuide{}, "", "", usecase.ErrTeachingReportContentRequired
}
