package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/egress"
)

const solveWithParentGuideV1 = "solve_with_parent_guide_v1"

const solveWithTeachingReportV2 = "solve_with_teaching_report_v2"

type solveGenerationFormatKey struct{}

// withSolveGenerationFormat 只为受信版本化求解请求启用已有 Provider 的 JSON 输出能力。
func withSolveGenerationFormat(ctx context.Context, version string) context.Context {
	return context.WithValue(ctx, solveGenerationFormatKey{}, version)
}

func solveGenerationResponseFormat(ctx context.Context) *llm.ResponseFormat {
	version, _ := ctx.Value(solveGenerationFormatKey{}).(string)
	if !solveGrantFromContext(ctx) || !solveTeachingVersionSupported(version) {
		return nil
	}
	return &llm.ResponseFormat{Type: "json_object"}
}

type solveGenerationFormatProvider struct {
	hexagon.Provider
	format *llm.ResponseFormat
}

// 工具运行器会重建补全请求，格式约束在最终 Provider 边界重新应用。
func wrapSolveGenerationFormatProvider(ctx context.Context, provider hexagon.Provider) hexagon.Provider {
	format := solveGenerationResponseFormat(ctx)
	if format == nil {
		return provider
	}
	return preserveContextTokenCounter(&solveGenerationFormatProvider{Provider: provider, format: format}, provider)
}

func (p *solveGenerationFormatProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	req.ResponseFormat = p.format
	return p.Provider.Complete(ctx, req)
}

func (p *solveGenerationFormatProvider) Stream(ctx context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	req.ResponseFormat = p.format
	return p.Provider.Stream(ctx, req)
}

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
	// 仅由同次独立验算的真实 stdout 与过程结论设置，不从模型 JSON 读取。
	verifiedQuantity string
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
		// 兼容模型附带的字符串观察；null等同未提供，不能据此构造可信执行回执。
		ExecutionObservation *string `json:"execution_evidence,omitempty"`
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

// ReparseSolveGenerationResult 只解释已返回的生成响应；可信执行证据仍完整沿用宿主回执。
func ReparseSolveGenerationResult(result SubAgentResult) (SubAgentResult, error) {
	return decodeSolveGeneration(result)
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

func hasReportContentGuideAudit(candidates []guideAuditCandidate) bool {
	for _, candidate := range candidates {
		for _, field := range candidate.RequiredFields {
			if field == "report_content" {
				return true
			}
		}
	}
	return false
}

// ParentGuideAuditVerdict 按实际审核原文判定给定解答来源的七字段合同。
func ParentGuideAuditVerdict(output, sourceDigest string) string {
	audits := parentGuideAuditsForCandidates(output, []guideAuditCandidate{{SourceDigest: sourceDigest}})
	return parentGuideAuditVerdict(audits, sourceDigest)
}

// TeachingReportGuideAuditVerdict 额外要求同次审核双层报告内容，不改变旧七字段审核。
func TeachingReportGuideAuditVerdict(output, sourceDigest string) string {
	return TeachingReportGuideAuditVerdictForSources(output, sourceDigest, []string{sourceDigest})
}

// TeachingReportCoreApproved 只读取单个候选已明确通过的答案与完整步骤。
// 另有字段被否决时，整份指南仍为 INVALID。
func TeachingReportCoreApproved(output, sourceDigest string) bool {
	if sourceDigest == "" || !strings.HasPrefix(strings.TrimSpace(output), "PARENT_GUIDE_AUDITS:") {
		return false
	}
	required := append(append([]string(nil), parentGuideFields[:]...), "report_content")
	audits := strictFinalGuideAudits(output, []guideAuditCandidate{{SourceDigest: sourceDigest, RequiredFields: required}})
	if len(audits) != 1 || audits[0].Scope != "IN_SCOPE" || guideAuditVerdict(audits[0], required) != "INVALID" {
		return false
	}
	return guideAuditAcceptsFields(audits[0], "answer", "full_solution_steps")
}

func guideAuditAcceptsFields(audit parentGuideAudit, first, second string) bool {
	var firstValid, secondValid bool
	for _, field := range audit.Fields {
		switch field.Field {
		case first:
			firstValid = field.Valid != nil && *field.Valid
		case second:
			secondValid = field.Valid != nil && *field.Valid
		}
	}
	return firstValid && secondValid
}

// TeachingReportGuideAuditVerdictForSources 用同题生成回执中的来源集合解释冻结的联合审核。
func TeachingReportGuideAuditVerdictForSources(output, sourceDigest string, sources []string) string {
	if sourceDigest == "" || !slices.Contains(sources, sourceDigest) {
		return "NOT_PROVIDED"
	}
	required := append(append([]string(nil), parentGuideFields[:]...), "report_content")
	candidates := make([]guideAuditCandidate, len(sources))
	for i, source := range sources {
		candidates[i] = guideAuditCandidate{SourceDigest: source, RequiredFields: required}
	}
	return parentGuideAuditVerdictForFields(parentGuideAuditsForCandidates(output, candidates), sourceDigest, required)
}

func withParentGuideVerification(spec SubAgentSpec, candidates []guideAuditCandidate) SubAgentSpec {
	if len(candidates) == 0 {
		return spec
	}
	spec.verification.ParentGuides = candidates
	raw, _ := json.Marshal(candidates)
	contract := parentGuideAuditContract
	if hasReportContentGuideAudit(candidates) {
		contract = TeachingReportGuideAuditInstructions()
	}
	spec.Task += "\n\n" + contract + "\nCandidates:\n" + string(raw) + "\nAfter the four verdict lines, output one additional single line PARENT_GUIDE_AUDITS: <JSON array of audits>."
	if hasReportContentGuideAudit(candidates) {
		spec.Task += "\nFor each candidate with required_fields, audit exactly those fields, including report_content. Check every answer, unit, condition, calculation, explanation and method note in its two-layer content against that candidate's solution. Missing report_content or omitted targets must be invalid; it is not numerical execution evidence."
	}
	return spec
}

// 仅在全部预期来源及审核字段都已收到时，在读取副本补最外层数组结束符。
func parentGuideAuditsForCandidates(output string, candidates []guideAuditCandidate) []parentGuideAudit {
	return readParentGuideAudits(output, candidates, false)
}

func strictFinalGuideAudits(output string, candidates []guideAuditCandidate) []parentGuideAudit {
	return readParentGuideAudits(output, candidates, true)
}

func readParentGuideAudits(output string, candidates []guideAuditCandidate, exact bool) []parentGuideAudit {
	for output != "" {
		var line string
		line, output, _ = strings.Cut(output, "\n")
		raw, found := strings.CutPrefix(strings.TrimSpace(line), "PARENT_GUIDE_AUDITS:")
		if !found {
			continue
		}
		raw = strings.TrimSpace(raw)
		var audits []parentGuideAudit
		if !exact && json.Unmarshal([]byte(raw), &audits) == nil {
			return audits
		}
		if strings.TrimSpace(output) != "" || len(candidates) == 0 {
			return nil
		}
		if !json.Valid([]byte(raw)) && strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "}") {
			raw += "]"
		}
		return strictGuideAuditSet(raw, candidates)
	}
	return nil
}

func strictGuideAuditSet(raw string, candidates []guideAuditCandidate) []parentGuideAudit {
	var audits []parentGuideAudit
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&audits) != nil || decoder.Decode(new(any)) != io.EOF || len(audits) != len(candidates) {
		return nil
	}
	if !uniqueGuideAuditMembers(json.NewDecoder(strings.NewReader(raw))) || !completeGuideAuditSet(audits, candidates) {
		return nil
	}
	return audits
}

// JSON 默认以同名成员的最后一个值为准。修复排版时不能据此覆盖来源或否决结论。
func uniqueGuideAuditMembers(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	if token != json.Delim('{') && token != json.Delim('[') {
		return true
	}
	var keys map[string]bool
	if token == json.Delim('{') {
		keys = make(map[string]bool)
	}
	for decoder.More() {
		if keys != nil {
			keyToken, tokenErr := decoder.Token()
			key, ok := keyToken.(string)
			key = strings.ToLower(key)
			if tokenErr != nil || !ok || keys[key] {
				return false
			}
			keys[key] = true
		}
		if !uniqueGuideAuditMembers(decoder) {
			return false
		}
	}
	_, err = decoder.Token()
	return err == nil
}

func completeGuideAuditSet(audits []parentGuideAudit, candidates []guideAuditCandidate) bool {
	expected := make(map[string][]string, len(candidates))
	for _, candidate := range candidates {
		if _, duplicate := expected[candidate.SourceDigest]; duplicate || candidate.SourceDigest == "" {
			return false
		}
		required := candidate.RequiredFields
		if len(required) == 0 {
			required = parentGuideFields[:]
		}
		expected[candidate.SourceDigest] = required
	}
	for _, audit := range audits {
		required, ok := expected[audit.SourceDigest]
		if !ok || guideAuditVerdict(audit, required) == "NOT_PROVIDED" {
			return false
		}
		delete(expected, audit.SourceDigest)
	}
	return len(expected) == 0
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
	return guideAuditVerdict(*found, required)
}

func guideAuditVerdict(audit parentGuideAudit, required []string) string {
	// 明确超纲始终是否决；采用任何讲法则要求全部声明字段的审核均通过。
	if audit.Scope == "OUT_OF_SCOPE" {
		return "INVALID"
	}
	if audit.Scope != "IN_SCOPE" || len(audit.Fields) != len(required) {
		return "NOT_PROVIDED"
	}
	seen := make(map[string]bool, len(required))
	invalid := false
	for _, field := range audit.Fields {
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

// declaredParentGuideAuditsProvided 只核对已声明的字段合同是否齐备；内容被否决仍保留INVALID。
// 没有required_fields的旧讲法维持原语义，不因新报告字段增加调用。
func declaredParentGuideAuditsProvided(output string, candidates []guideAuditCandidate) bool {
	audits := parentGuideAuditsForCandidates(output, candidates)
	for _, candidate := range candidates {
		if len(candidate.RequiredFields) != 0 &&
			parentGuideAuditVerdictForFields(audits, candidate.SourceDigest, candidate.RequiredFields) == "NOT_PROVIDED" {
			return false
		}
	}
	return true
}

func selectedSolverSolution(groups []answerGroup, verdict verifyVerdict, computed string, grounded bool, problem ...string) solverSolution {
	if len(groups) > 1 && grounded && verdict != verdictUnverifiable && computed != "" {
		if group := findGroup(groups, computed, problem...); group != nil {
			return group.sols[0]
		}
	}
	return groups[0].sols[0]
}

// 单个独立执行量不能证明整份多空答案。只有两份候选各自执行了完整答案，
// 并共享唯一、同名同单位的独立核验量时，才优先采用整份通过审核的原候选。
func auditedReportAlternative(primary solverSolution, candidates []solverSolution, audits []parentGuideAudit) (solverSolution, bool) {
	required := append(append([]string(nil), parentGuideFields[:]...), "report_content")
	auditFor := func(s solverSolution) *parentGuideAudit {
		digest := executionInputDigest(s.output)
		var matching *parentGuideAudit
		for i := range audits {
			if audits[i].SourceDigest == digest {
				if matching != nil {
					return nil
				}
				matching = &audits[i]
			}
		}
		return matching
	}
	current := auditFor(primary)
	if current == nil || current.Scope != "IN_SCOPE" || current.verifiedQuantity == "" || guideAuditVerdict(*current, required) != "INVALID" ||
		!guideAuditAcceptsFields(*current, "answer", "report_content") {
		return solverSolution{}, false
	}
	label := verifiedReportQuantityLabel(primary, current.verifiedQuantity)
	if label == "" {
		return solverSolution{}, false
	}
	var selected solverSolution
	for _, candidate := range candidates {
		audit := auditFor(candidate)
		if audit == nil || guideAuditVerdict(*audit, required) != "VALID" {
			continue
		}
		if audit.verifiedQuantity != current.verifiedQuantity || verifiedReportQuantityLabel(candidate, current.verifiedQuantity) != label {
			return solverSolution{}, false
		}
		if selected.output != "" && selected.output != candidate.output {
			return solverSolution{}, false
		}
		selected = candidate
	}
	return selected, selected.output != ""
}

var reportEmbeddedQuantity = regexp.MustCompile(`(?i)` + answerQuantityNumberPattern + `\s*(?:[（(]\s*)?(?:` + answerQuantityUnitPattern + `)`)

func verifiedReportQuantityLabel(s solverSolution, computed string) string {
	if s.generation == nil || s.generation.OutputVersion != solveWithTeachingReportV2 || !answerMarker.MatchString(s.output) {
		return ""
	}
	own, executed := s.executionReceipt.computedOutput(s.executionTask)
	if !executed || normalizeAnswer(own) != s.answer {
		return ""
	}
	q, ok := completeVerificationQuantity(computed)
	if !ok || q.unit == "" {
		return ""
	}
	var g struct {
		Report struct {
			Version string `json:"version"`
			Items   []struct {
				ID      string `json:"problem_id"`
				Answers []struct {
					Label, Text, Latex string
					Conditions         []string
				} `json:"answers"`
			} `json:"items"`
		} `json:"report_content"`
	}
	if json.Unmarshal([]byte(s.generation.ParentGuideJSON), &g) != nil || g.Report.Version != "teaching-report-content-v1" ||
		len(g.Report.Items) != 1 || g.Report.Items[0].ID != "current" {
		return ""
	}
	var label string
	for _, answer := range g.Report.Items[0].Answers {
		if (strings.TrimSpace(answer.Text) == "") == (strings.TrimSpace(answer.Latex) == "") {
			return ""
		}
		text := adapter.NormalizeMathText(answer.Text + answer.Latex)
		value, numeric := completeVerificationQuantity(text)
		if !numeric {
			if reportEmbeddedQuantity.MatchString(text) {
				return ""
			}
			continue
		}
		if label != "" || len(answer.Conditions) != 0 || strings.TrimSpace(answer.Label) == "" || !quantitiesEqual(value, q) {
			return ""
		}
		label = strings.TrimSpace(answer.Label)
	}
	return label
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
