package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/hexagon-codes/hexclaw/egress"
)

const solveWithParentGuideV1 = "solve_with_parent_guide_v1"

// SolveGenerationAttachment 保留同一次生成的原响应及讲法，数值证明仍只绑定纯解法。
type SolveGenerationAttachment struct {
	OutputVersion   string `json:"output_version"`
	RawResponse     string `json:"raw_response"`
	ParentGuideJSON string `json:"parent_guide_json"`
}

type guideAuditCandidate struct {
	SourceDigest string          `json:"source_digest"`
	Solution     string          `json:"solution"`
	Guide        json.RawMessage `json:"parent_guide"`
}

type guideFieldAudit struct {
	Field  string `json:"field"`
	Valid  *bool  `json:"valid"`
	Reason string `json:"reason"`
}

type parentGuideAudit struct {
	SourceDigest string            `json:"source_digest"`
	Scope        string            `json:"scope"`
	Fields       []guideFieldAudit `json:"fields"`
}

var parentGuideFields = [...]string{"answer", "full_solution_steps", "grade_level_method", "likely_mistakes", "parent_teaching_sequence", "follow_up_questions", "checking_method"}

func withParentGuideGeneration(spec SubAgentSpec, contract string) SubAgentSpec {
	spec.generationVersion = solveWithParentGuideV1
	spec.Task += `

Return one JSON object only, without Markdown fences, using schema solve_with_parent_guide_v1:
{"schema":"solve_with_parent_guide_v1","solution":"complete worked solution ending with 答案：...","parent_guide":{"answer":"...","full_solution_steps":["..."],"grade_level_method":"...","likely_mistakes":["..."],"parent_teaching_sequence":["..."],"follow_up_questions":["..."],"checking_method":"..."}}.
Keep the full mathematical solution in solution. Every guide field must be concrete, consistent with this solution, and within the supplied curriculum. Treat both as candidates awaiting independent verification; do not claim they are verified. Do not add unrelated examples.
Frozen teaching contract:
` + contract
	return spec
}

// decodeSolveGeneration 位于物理回执保存之前，重放时无需重新请求或重新解释旧正文。
func decodeSolveGeneration(result SubAgentResult) (SubAgentResult, error) {
	if result.Generation != nil && result.Generation.OutputVersion == solveWithParentGuideV1 {
		return result, nil
	}
	raw := result.Output
	text := strings.TrimSpace(raw)
	if strings.HasPrefix(text, "```json\n") && strings.HasSuffix(text, "\n```") {
		text = strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "\n```")
	}
	var envelope struct {
		Schema   string          `json:"schema"`
		Solution string          `json:"solution"`
		Guide    json.RawMessage `json:"parent_guide"`
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return result, fmt.Errorf("solve generation is invalid: %w: %w", err, egress.ErrProviderResponseProcessed)
	}
	if err := decoder.Decode(new(any)); err != io.EOF || envelope.Schema != solveWithParentGuideV1 || strings.TrimSpace(envelope.Solution) == "" {
		return result, fmt.Errorf("solve generation is incomplete: %w", egress.ErrProviderResponseProcessed)
	}
	result.Output = envelope.Solution
	result.Generation = &SolveGenerationAttachment{OutputVersion: envelope.Schema, RawResponse: raw, ParentGuideJSON: string(envelope.Guide)}
	return result, nil
}

func guideCandidates(sols []solverSolution) []guideAuditCandidate {
	var candidates []guideAuditCandidate
	for _, solution := range sols {
		if solution.generation == nil || solution.generation.OutputVersion != solveWithParentGuideV1 || !json.Valid([]byte(solution.generation.ParentGuideJSON)) {
			continue
		}
		candidates = append(candidates, guideAuditCandidate{SourceDigest: executionInputDigest(solution.output), Solution: solution.output, Guide: json.RawMessage(solution.generation.ParentGuideJSON)})
	}
	return candidates
}

const parentGuideAuditContract = `Independently audit every supplied parent_guide against its own solution, the original problem and allowed curriculum. Check all seven fields: answer, full_solution_steps, grade_level_method, likely_mistakes, parent_teaching_sequence, follow_up_questions, checking_method. Audit every mathematical example, follow-up answer, proposed checking method and grade-level explanation. A correct original answer does not excuse an incorrect guide example. Each audit has this exact JSON shape: {"source_digest":"digest supplied with the candidate","scope":"IN_SCOPE or OUT_OF_SCOPE","fields":[{"field":"answer","valid":true,"reason":"concrete reason"}]}, with exactly one entry for each of the seven fields. Set valid=false for missing, contradictory or mathematically wrong content. Guide audits are separate from the original problem's numeric execution evidence.`

func withParentGuideVerification(spec SubAgentSpec, candidates []guideAuditCandidate) SubAgentSpec {
	if len(candidates) == 0 {
		return spec
	}
	spec.verification.ParentGuides = candidates
	raw, _ := json.Marshal(candidates)
	spec.Task += "\n\n" + parentGuideAuditContract + "\nCandidates:\n" + string(raw) + "\nAfter the four verdict lines, output one additional single line PARENT_GUIDE_AUDITS: <JSON array of audits>."
	return spec
}

func parentGuideAuditsFromOutput(output string) []parentGuideAudit {
	for _, line := range strings.Split(output, "\n") {
		if raw, ok := strings.CutPrefix(strings.TrimSpace(line), "PARENT_GUIDE_AUDITS:"); ok {
			var audits []parentGuideAudit
			if json.Unmarshal([]byte(strings.TrimSpace(raw)), &audits) == nil {
				return audits
			}
			return nil
		}
	}
	return nil
}

func parentGuideAuditVerdict(audits []parentGuideAudit, digest string) string {
	var found *parentGuideAudit
	for i := range audits {
		if audits[i].SourceDigest == digest {
			if found != nil {
				return "NOT_PROVIDED"
			}
			found = &audits[i]
		}
	}
	if found == nil {
		return "NOT_PROVIDED"
	}
	if found.Scope == "OUT_OF_SCOPE" {
		return "INVALID"
	}
	if found.Scope != "IN_SCOPE" || len(found.Fields) != len(parentGuideFields) {
		return "NOT_PROVIDED"
	}
	seen := make(map[string]bool, len(parentGuideFields))
	invalid := false
	for _, field := range found.Fields {
		known := false
		for _, name := range parentGuideFields {
			if field.Field == name {
				known = true
				break
			}
		}
		if !known || seen[field.Field] || field.Valid == nil || strings.TrimSpace(field.Reason) == "" {
			return "NOT_PROVIDED"
		}
		seen[field.Field] = true
		invalid = invalid || !*field.Valid
	}
	if invalid {
		return "INVALID"
	}
	return "VALID"
}

func selectedSolverSolution(groups []answerGroup, verdict verifyVerdict, computed string, grounded bool) solverSolution {
	if len(groups) > 1 && grounded && verdict != verdictUnverifiable && computed != "" {
		if group := findGroup(groups, computed); group != nil {
			return group.sols[0]
		}
	}
	return groups[0].sols[0]
}

func addParentGuideMetadata(metadata map[string]string, selected solverSolution, audits []parentGuideAudit) {
	digest := executionInputDigest(selected.output)
	metadata["solve_parent_guide_source_digest"] = digest
	metadata["solve_parent_guide_audit"] = parentGuideAuditVerdict(audits, digest)
	if selected.generation != nil {
		metadata["solve_parent_guide_json"] = selected.generation.ParentGuideJSON
	}
}
