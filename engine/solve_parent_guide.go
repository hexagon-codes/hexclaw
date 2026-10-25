package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/hexagon-codes/hexclaw/egress"
)

const solveWithParentGuideV1 = "solve_with_parent_guide_v1"

const solveWithTeachingReportV2 = "solve_with_teaching_report_v2"

func solveTeachingVersionSupported(version string) bool {
	return version == solveWithParentGuideV1 || version == solveWithTeachingReportV2
}

// SolveGenerationAttachment 保留同一次生成的原响应及讲法，数值证明仍只绑定纯解法。
type SolveGenerationAttachment struct {
	OutputVersion   string `json:"output_version"`
	RawResponse     string `json:"raw_response"`
	ParentGuideJSON string `json:"parent_guide_json"`
}

type guideAuditCandidate struct {
	SourceDigest   string          `json:"source_digest"`
	Solution       string          `json:"solution"`
	Guide          json.RawMessage `json:"parent_guide"`
	RequiredFields []string        `json:"required_fields,omitempty"`
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
Keep the full mathematical solution in solution. End solution with an explicit final 答案：... and copy that final answer text verbatim into parent_guide.answer, including units or the full conceptual statement; do not paraphrase or abbreviate between the two. Every guide field must be concrete, consistent with this solution, and within the supplied curriculum. Treat both as candidates awaiting independent verification; do not claim they are verified. Do not add unrelated examples.
For a single-quantity numeric answer, the text after 答案： and parent_guide.answer must contain only the final value and its unit; keep explanatory sentences in the preceding worked solution. In likely_mistakes, name only an actually incorrect operation and explain its correction; never label a correct operation as a mistake, including when listing alternatives joined by "or".
For fill-ins with several valid conventions, list the applicable meaning or assumption and the corresponding filled answer for each convention in the final answer, and explicitly say the answer is not unique. Include all requested blanks and expressions, not just a number. For infinitely many values, give the general form and its constraints plus representative applicable cases instead of claiming a finite list is exhaustive. Explain each applicable case fully in solution and full_solution_steps so a parent can teach from it. Do not invent alternatives or missing conditions, and do not require a parent to infer the actual answers from the steps.
Frozen teaching contract:
` + contract
	return spec
}

func withVersionedParentGuideGeneration(spec SubAgentSpec, version, contract string) SubAgentSpec {
	spec = withParentGuideGeneration(spec, contract)
	if version == solveWithTeachingReportV2 {
		spec.generationVersion = version
		spec.Task = strings.ReplaceAll(spec.Task, solveWithParentGuideV1, version)
		spec.Task = strings.Replace(spec.Task, `"checking_method":"..."}}.`, `"checking_method":"...","report_content":{"version":"teaching-report-content-v1","items":[{"problem_id":"current","short_title":"...","answers":[{"id":"a1","text":"..."}],"steps":[]}],"sections":[{"id":"s1","title":"...","problem_ids":["current"],"blocks":[{"id":"b1","problem_ids":["current"],"text":"..."}],"diagram_ids":[],"method_notes":[]}]}}}.`, 1)
		spec.Task += "\nThe parent_guide also requires report_content, with the exact structure in the frozen contract. It belongs to this candidate and must be audited with the original solution. Do not execute another solve or claim verification."
	}
	return spec
}

// decodeSolveGeneration 位于物理回执保存之前，重放时无需重新请求或重新解释旧正文。
func decodeSolveGeneration(result SubAgentResult) (SubAgentResult, error) {
	if result.Generation != nil && solveTeachingVersionSupported(result.Generation.OutputVersion) {
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
	if err := decoder.Decode(new(any)); err != io.EOF || !solveTeachingVersionSupported(envelope.Schema) || strings.TrimSpace(envelope.Solution) == "" {
		return result, fmt.Errorf("solve generation is incomplete: %w", egress.ErrProviderResponseProcessed)
	}
	result.Output = envelope.Solution
	result.Generation = &SolveGenerationAttachment{OutputVersion: envelope.Schema, RawResponse: raw, ParentGuideJSON: string(envelope.Guide)}
	return result, nil
}

func guideCandidates(sols []solverSolution) []guideAuditCandidate {
	var candidates []guideAuditCandidate
	for _, solution := range sols {
		if solution.generation == nil || !solveTeachingVersionSupported(solution.generation.OutputVersion) || !json.Valid([]byte(solution.generation.ParentGuideJSON)) {
			continue
		}
		candidate := guideAuditCandidate{SourceDigest: executionInputDigest(solution.output), Solution: solution.output, Guide: json.RawMessage(solution.generation.ParentGuideJSON)}
		if solution.generation.OutputVersion == solveWithTeachingReportV2 {
			candidate.RequiredFields = append(append([]string(nil), parentGuideFields[:]...), "report_content")
		}
		candidates = append(candidates, candidate)
	}
	return candidates
}

// ParentGuideAuditContract 供联合验算和独立指南审核复用同一七字段合同。
const ParentGuideAuditContract = `Independently audit every supplied parent_guide against its own solution, the original problem and allowed curriculum. Check all seven fields: answer, full_solution_steps, grade_level_method, likely_mistakes, parent_teaching_sequence, follow_up_questions, checking_method. Audit every mathematical example, follow-up answer, proposed checking method and grade-level explanation. A correct original answer does not excuse an incorrect guide example. For every likely_mistakes entry, test each described operation against the solution and its proposed correction: reject an entry that labels a correct operation as wrong, including mixed alternatives, or contradicts its own correction.
Judge substantive correctness, completeness, factual support and curriculum scope. Expression preferences in the frozen teaching instructions, such as sentence count, paragraph grouping, punctuation or wording, guide generation but are not validity gates. In particular, full_solution_steps may preserve the verified solution's grouping and contain several sentences in one item. Require the necessary reasoning in solution order; do not mark a field invalid solely because its expression differs from a style preference. A presentation issue is substantive only when it changes the meaning, omits required content or makes a mathematical relation ambiguous.
Each audit has this exact JSON shape: {"source_digest":"digest supplied with the candidate","scope":"IN_SCOPE or OUT_OF_SCOPE","fields":[{"field":"answer","valid":true,"reason":"concrete reason"}]}, with exactly one entry for each of the seven fields. Set valid=false for missing, contradictory or mathematically wrong content, unsupported factual claims or methods outside the allowed curriculum, and identify the concrete substantive defect in reason. Guide audits are separate from the original problem's numeric execution evidence.`

const parentGuideAuditContract = ParentGuideAuditContract

// TeachingReportGuideAuditInstructions 扩展审核字段，保留旧版七字段合同的原文与行为。
func TeachingReportGuideAuditInstructions() string {
	contract := strings.Replace(ParentGuideAuditContract, "Check all seven fields:", "Check all eight fields, including report_content, and these existing fields:", 1)
	return strings.Replace(contract, "with exactly one entry for each of the seven fields.", "with exactly one entry for each of the eight fields, including report_content.", 1)
}

// ParentGuideAuditVerdict 按实际审核原文判定给定解答来源的七字段合同。
func ParentGuideAuditVerdict(output, sourceDigest string) string {
	return parentGuideAuditVerdict(parentGuideAuditsFromOutput(output), sourceDigest)
}

// TeachingReportGuideAuditVerdict 额外要求同次审核双层报告内容，不改变旧七字段审核。
func TeachingReportGuideAuditVerdict(output, sourceDigest string) string {
	return parentGuideAuditVerdictForFields(parentGuideAuditsFromOutput(output), sourceDigest, append(append([]string(nil), parentGuideFields[:]...), "report_content"))
}

func withParentGuideVerification(spec SubAgentSpec, candidates []guideAuditCandidate) SubAgentSpec {
	if len(candidates) == 0 {
		return spec
	}
	spec.verification.ParentGuides = candidates
	raw, _ := json.Marshal(candidates)
	contract := parentGuideAuditContract
	for _, candidate := range candidates {
		if len(candidate.RequiredFields) != 0 {
			contract = TeachingReportGuideAuditInstructions()
			break
		}
	}
	spec.Task += "\n\n" + contract + "\nCandidates:\n" + string(raw) + "\nAfter the four verdict lines, output one additional single line PARENT_GUIDE_AUDITS: <JSON array of audits>."
	for _, candidate := range candidates {
		if len(candidate.RequiredFields) != 0 {
			spec.Task += "\nFor each candidate with required_fields, audit exactly those fields, including report_content. Check every answer, unit, condition, calculation, explanation and method note in its two-layer content against that candidate's solution. Missing report_content or omitted targets must be invalid; it is not numerical execution evidence."
			break
		}
	}
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

// 单一候选的完整审核对象只缺最外层数组结束符时，可无损恢复语法；
// 不补对象、字段或审核结论，多候选及来源不匹配仍保持未提供。
func parentGuideAuditsForCandidates(output string, candidates []guideAuditCandidate) []parentGuideAudit {
	if audits := parentGuideAuditsFromOutput(output); audits != nil {
		return audits
	}
	if len(candidates) != 1 || candidates[0].SourceDigest == "" {
		return nil
	}
	for _, line := range strings.Split(output, "\n") {
		raw, found := strings.CutPrefix(strings.TrimSpace(line), "PARENT_GUIDE_AUDITS:")
		if !found {
			continue
		}
		raw = strings.TrimSpace(raw)
		if !strings.HasPrefix(raw, "[") || !strings.HasSuffix(raw, "}") {
			return nil
		}
		var audits []parentGuideAudit
		if json.Unmarshal([]byte(raw+"]"), &audits) != nil || len(audits) != 1 {
			return nil
		}
		verdict := parentGuideAuditVerdict(audits, candidates[0].SourceDigest)
		if len(candidates[0].RequiredFields) != 0 {
			verdict = parentGuideAuditVerdictForFields(audits, candidates[0].SourceDigest, candidates[0].RequiredFields)
		}
		if verdict == "VALID" || verdict == "INVALID" {
			return audits
		}
		return nil
	}
	return nil
}

func parentGuideAuditVerdict(audits []parentGuideAudit, digest string) string {
	return parentGuideAuditVerdictForFields(audits, digest, parentGuideFields[:])
}

func parentGuideAuditVerdictForFields(audits []parentGuideAudit, digest string, required []string) string {
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
	if found.Scope != "IN_SCOPE" || len(found.Fields) != len(required) {
		return "NOT_PROVIDED"
	}
	seen := make(map[string]bool, len(required))
	invalid := false
	for _, field := range found.Fields {
		known := false
		for _, name := range required {
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
	if selected.generation != nil && selected.generation.OutputVersion == solveWithTeachingReportV2 {
		metadata["solve_parent_guide_audit"] = parentGuideAuditVerdictForFields(audits, digest, append(append([]string(nil), parentGuideFields[:]...), "report_content"))
	}
	if selected.generation != nil {
		metadata["solve_parent_guide_json"] = selected.generation.ParentGuideJSON
	}
}
