package engineadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"strings"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

// ParentTeachingGuideGenerateFunc 根据已验证解生成给家长的完整逐题讲法。
type ParentTeachingGuideGenerateFunc func(
	ctx context.Context,
	subject, prompt, grade string,
) (string, error)

func WithParentTeachingGuideGen(fn ParentTeachingGuideGenerateFunc) SolveAdapterOption {
	return func(a *SolveAdapter) { a.parentTeachingGuideGen = fn }
}

func (a *SolveAdapter) SetParentTeachingGuideGen(fn ParentTeachingGuideGenerateFunc) {
	a.parentTeachingGuideGen = fn
}

// SetParentTeachingGuideAudit 注入独立审核的一次模型调用。
func (a *SolveAdapter) SetParentTeachingGuideAudit(fn ParentTeachingGuideGenerateFunc) {
	a.parentTeachingGuideAudit = fn
}

// WithParentTeachingSkillLoader 注入已安装 Skill 的正文读取边界。
func WithParentTeachingSkillLoader(fn SkillContentLoader) SolveAdapterOption {
	return func(a *SolveAdapter) { a.parentTeachingSkillLoader = fn }
}

// SetParentTeachingSkillLoader 供场景装配在 SolveAdapter 创建后接入同一 marketplace。
func (a *SolveAdapter) SetParentTeachingSkillLoader(fn SkillContentLoader) {
	a.parentTeachingSkillLoader = fn
}

var parentTeachingSkillsFS fs.FS = k12.BundledSkillsFS()

type parentTeachingSkillSpec struct {
	name    string
	file    string
	anchors []string
}

var parentTeachingPedagogySkill = parentTeachingSkillSpec{
	name:    "k12-pedagogy",
	file:    "skills/k12-pedagogy.md",
	anchors: []string{"家长是中间人", "最近发展区", "渐进提示三阶段"},
}

var parentTeachingSubjectSkills = map[string]parentTeachingSkillSpec{
	"数学":   {name: "math-tutor", file: "skills/math-tutor.md", anchors: []string{"波利亚四步", "理解题目", "回顾检验"}},
	"语文":   {name: "chinese-tutor", file: "skills/chinese-tutor.md", anchors: []string{"朗读教学", "分点作答", "从原文找依据"}},
	"英语":   {name: "english-tutor", file: "skills/english-tutor.md", anchors: []string{"用法先于规则", "只鼓励不打击", "完整修改示范"}},
	"科学":   {name: "science-tutor", file: "skills/science-tutor.md", anchors: []string{"结论来自证据", "5E 教学模式", "不编造实验结果"}},
	"信息科技": {name: "information-technology-tutor", file: "skills/information-technology-tutor.md", anchors: []string{"PRIMM 教学法", "运行结果以系统沙箱回传为准", "完整参考程序"}},
}

// SelectParentTeachingContextReceipts 从本次已消费快照选择通用与同学科指导，不追补当前目录。
func SelectParentTeachingContextReceipts(subject string, frozen []skill.ContextReceipt) []skill.ContextReceipt {
	canonical := parentTeachingSubject(subject)
	subjectSkill, known := parentTeachingSubjectSkills[canonical]
	if !known {
		return nil
	}
	var selected []skill.ContextReceipt
	seen := make(map[string]bool)
	for _, receipt := range frozen {
		if receipt.Status != "loaded" || (receipt.Name != parentTeachingPedagogySkill.name && receipt.Name != subjectSkill.name) {
			continue
		}
		identity := receipt.Name + "\x00" + receipt.Version + "\x00" + receipt.ContentDigest
		if seen[identity] {
			continue
		}
		seen[identity] = true
		selected = append(selected, receipt)
	}
	// 复制所有映射，调用者不能修改冻结任务的原快照。
	encoded, err := json.Marshal(selected)
	if err != nil {
		return nil
	}
	var copied []skill.ContextReceipt
	if json.Unmarshal(encoded, &copied) != nil {
		return nil
	}
	return copied
}

var _ usecase.ParentTeachingGuideGenerator = (*SolveAdapter)(nil)
var _ usecase.ParentTeachingGuideAuditor = (*SolveAdapter)(nil)
var _ usecase.SolveTeachingContractFreezer = (*SolveAdapter)(nil)

// FreezeSolveTeachingContract 固化教学 Skill、表达要求和年级，求解时同次生成候选指南。
func (a *SolveAdapter) FreezeSolveTeachingContract(ctx context.Context, subject, grade string) (string, error) {
	contract, _, err := a.FreezeSolveTeachingContext(ctx, subject, grade)
	return contract, err
}

func (a *SolveAdapter) FreezeSolveTeachingContext(ctx context.Context, subject, grade string) (string, []skill.ContextReceipt, error) {
	ctx, _ = config.FreezeAgentInstructions(ctx)
	if strings.TrimSpace(subject) == "" {
		subject = "数学"
	}
	var contract strings.Builder
	contract.WriteString(config.ParentExpressionInstructions(ctx))
	methodology, prepared := buildParentTeachingSkillContext(subject, a.parentTeachingSkillLoader)
	contract.WriteString(methodology)
	contract.WriteString("\nFrozen subject: " + subject + "\nFrozen grade: " + grade + "\n")
	contract.WriteString(`Generate the complete parent guide together with this candidate solution. The candidate has not yet been independently verified. Use only the frozen grade/curriculum methods and the supplied teaching Skill. The independent verifier must audit the solution and every mathematical method, example and checking step in the guide.
parent_guide must contain exactly seven fields: answer (concise final answer explicitly in solution), full_solution_steps (nonempty string array in solution order), grade_level_method (grade-appropriate method), likely_mistakes (nonempty string array specific to this problem), parent_teaching_sequence (nonempty string array of concrete explanation and progressive questions), follow_up_questions (nonempty string array), checking_method (a concrete independent check). Do not invent student answers or grading facts. Do not replace or contradict the candidate solution. The usecase will derive the final full_solution_steps from the independently verified solution.`)
	return contract.String(), prepared, nil
}

func (a *SolveAdapter) GenerateParentTeachingGuide(
	ctx context.Context,
	req usecase.ParentTeachingGuideRequest,
) (usecase.ParentTeachingGuide, error) {
	if a.parentTeachingGuideGen == nil {
		return usecase.ParentTeachingGuide{}, fmt.Errorf("parent teaching guide: 未注入专用生成闭包")
	}
	sourceAnswer, _ := usecase.ParentTeachingAnswer(req.VerifiedSolution)
	facts, err := json.Marshal(struct {
		Problem               string              `json:"problem"`
		StudentAnswer         string              `json:"student_answer,omitempty"`
		ObservedAnswerState   usecase.AnswerState `json:"answer_state,omitempty"`
		ObservedStudentAnswer string              `json:"observed_student_answer,omitempty"`
		VerifiedSolution      string              `json:"verified_solution"`
		SourceAnswer          string              `json:"source_answer,omitempty"`
		KnowledgePoints       []string            `json:"knowledge_points,omitempty"`
		WrongStep             string              `json:"wrong_step,omitempty"`
		ErrorCause            string              `json:"error_cause,omitempty"`
	}{
		Problem: req.Problem, StudentAnswer: req.StudentAnswer,
		ObservedAnswerState: req.ObservedAnswerState, ObservedStudentAnswer: req.ObservedStudentAnswer,
		VerifiedSolution: req.VerifiedSolution, SourceAnswer: sourceAnswer, KnowledgePoints: req.KnowledgePoints,
		WrongStep: req.WrongStep, ErrorCause: req.ErrorCause,
	})
	if err != nil {
		return usecase.ParentTeachingGuide{}, fmt.Errorf("parent teaching guide: encode exact problem facts: %w", err)
	}
	var promptBuilder strings.Builder
	var prepared []skill.ContextReceipt
	if req.FrozenTeachingContract != "" {
		prepared = req.ParentTeachingSkillSnapshot
		promptBuilder.WriteString(req.FrozenTeachingContract)
		promptBuilder.WriteString("\n\n——以下是本题冻结事实与输出合同——\n")
	} else {
		promptBuilder.WriteString(config.ParentExpressionInstructions(ctx))
		methodology, snapshot := buildParentTeachingSkillContext(req.Subject, a.parentTeachingSkillLoader)
		if methodology != "" {
			prepared = snapshot
			promptBuilder.WriteString(methodology)
			promptBuilder.WriteString("\n\n——以下是本题冻结事实与输出合同——\n")
		}
	}
	promptBuilder.WriteString(`请只针对下面这一道题生成家长可照着使用的辅导指南。一次提供正确答案、完整解法和讲题方法，把学科 Skill 的方法落实到具体的讲解、追问、卡点引导和理解检查；内容简洁明确。
正文不自述工具调用、模型或内部验证流程；检验直接给代回计算、原文依据或可观察事实。已有系统图示就说明其等份、位置与数量，不要求家长重画，不用空格拼字符图；没有实际图示不能声称已经画图。保留全部答案、单位、必要过程和多解前提，减少重复铺垫、结论与无关操作要求。
verified_solution 是已验算的完整解答，是答案和完整方法的唯一依据，不得改写为其他答案或方法。
answer 必须完整保留 verified_solution 明确给出的所有空位答案、必要单位及多种有效填法各自的前提，不得只摘其中一种；禁止把无关推导塞入 answer。full_solution_steps 按 verified_solution 的解题顺序填写完整必要步骤，服务端还会用可信原文确定性分段后覆盖核对。
source_answer 非空时，answer 必须逐字复制 source_answer 的完整字符串，不得增加解释、缩写、重排或改写。它是从同一份 verified_solution 的明确最终答案段提取的原文。单位“1”的含义及其他必要前提放入 grade_level_method 和 parent_teaching_sequence，不要追加到 answer。
answer_state 与 observed_student_answer 是当前来源中实际观察的状态和原文。student_answer 可能是独立求解时清空的执行副本，空串不能证明孩子未答。状态缺失保持未知；笔迹不清时仍提供独立参考，但不能声称孩子没写、答错或已掌握。wrong_step、error_cause 只有已提供的真实批改事实才可作为该孩子的错因，其他易错点必须作为一般提醒。
严格只输出一个 JSON 对象，字段必须且只能是：
answer（字符串）、full_solution_steps（非空字符串数组，完整方法与必要步骤）、grade_level_method（字符串，当前年级/学期/教材允许的方法）、
likely_mistakes（非空字符串数组，最容易出错的步骤与原因）、
parent_teaching_sequence（非空字符串数组，家长先讲什么、再问什么、何时让孩子自己算）、
follow_up_questions（非空字符串数组，可追问孩子的理解问题）、
checking_method（字符串，家长可独立执行的检查或反向验算）。每一项必须针对本题，禁止复用通用话术。
题目事实：`)
	promptBuilder.Write(facts)
	if req.RejectedCandidate != nil {
		candidate := *req.RejectedCandidate
		candidate.SkillContextReceipts = nil
		rejected, _ := json.Marshal(candidate)
		promptBuilder.WriteString("\nThe following previous guide failed independent mathematical audit. It is not an authority. Generate a new guide and check every explanation against the verified solution; do not copy its errors:\n")
		promptBuilder.Write(rejected)
	}
	prompt := promptBuilder.String()
	out, err := a.parentTeachingGuideGen(ctx, req.Subject, prompt, req.Grade)
	if err != nil {
		return usecase.ParentTeachingGuide{}, providerResponseError(err)
	}
	var guide usecase.ParentTeachingGuide
	payload := extractParentTeachingGuideJSON(out)
	if hasModelTeachingReceiptField(payload) {
		return usecase.ParentTeachingGuide{}, fmt.Errorf("parent teaching guide: unexpected context receipts: %w", egress.ErrProviderResponseProcessed)
	}
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&guide); err != nil {
		return usecase.ParentTeachingGuide{}, fmt.Errorf("parent teaching guide: invalid JSON: %v: %w", err, egress.ErrProviderResponseProcessed)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return usecase.ParentTeachingGuide{}, fmt.Errorf("parent teaching guide: unexpected trailing JSON: %w", egress.ErrProviderResponseProcessed)
	}
	guide.SkillContextReceipts = append([]skill.ContextReceipt(nil), prepared...)
	for i := range guide.SkillContextReceipts {
		guide.SkillContextReceipts[i].Status = "loaded"
	}
	return guide, nil
}

// 面向模型的七字段合同不接受服务端只读快照，即使值为空也不能由模型提供。
func hasModelTeachingReceiptField(payload string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(payload), &fields) != nil {
		return false
	}
	_, exists := fields["skill_context_receipts"]
	return exists
}

func (a *SolveAdapter) AuditParentTeachingGuide(ctx context.Context, req usecase.ParentTeachingGuideAuditRequest) (usecase.ParentTeachingGuideAudit, error) {
	if a.parentTeachingGuideAudit == nil {
		return usecase.ParentTeachingGuideAudit{}, fmt.Errorf("parent teaching guide auditor unavailable: %w", egress.ErrProviderNotSent)
	}
	guideForAudit := req.Guide
	guideForAudit.SkillContextReceipts = nil
	facts, err := json.Marshal(struct {
		Problem               string              `json:"problem"`
		ObservedAnswerState   usecase.AnswerState `json:"answer_state,omitempty"`
		ObservedStudentAnswer string              `json:"observed_student_answer,omitempty"`
		Candidate             struct {
			SourceDigest string                      `json:"source_digest"`
			Solution     string                      `json:"solution"`
			Guide        usecase.ParentTeachingGuide `json:"parent_guide"`
		} `json:"candidate"`
	}{Problem: req.Request.Problem, ObservedAnswerState: req.Request.ObservedAnswerState, ObservedStudentAnswer: req.Request.ObservedStudentAnswer, Candidate: struct {
		SourceDigest string                      `json:"source_digest"`
		Solution     string                      `json:"solution"`
		Guide        usecase.ParentTeachingGuide `json:"parent_guide"`
	}{req.SourceSolutionDigest, req.Request.VerifiedSolution, guideForAudit}})
	if err != nil {
		return usecase.ParentTeachingGuideAudit{}, err
	}
	prompt := req.Request.FrozenTeachingContract + "\n" + engine.ParentGuideAuditContract +
		"\nThe original mathematical solution is already verified and immutable. Audit only this supplied guide; do not generate another solution. A missing or unreadable student answer does not prove that the student left it blank, made an error, or mastered the concept. Check any student-specific claims against the supplied observation only. Output exactly one line: PARENT_GUIDE_AUDITS: <JSON array containing this one candidate's audit>.\n" + string(facts)
	out, err := a.parentTeachingGuideAudit(ctx, req.Request.Subject, prompt, req.Request.Grade)
	if err != nil {
		return usecase.ParentTeachingGuideAudit{}, providerResponseError(err)
	}
	return usecase.ParentTeachingGuideAudit{
		SourceSolutionDigest: req.SourceSolutionDigest,
		Verdict:              engine.ParentGuideAuditVerdict(out, req.SourceSolutionDigest),
		Output:               out,
	}, nil
}

func buildParentTeachingSkillMethodology(subject string, loader SkillContentLoader) string {
	methodology, _ := buildParentTeachingSkillContext(subject, loader)
	return methodology
}

func buildParentTeachingSkillContext(subject string, loader SkillContentLoader) (string, []skill.ContextReceipt) {
	specs := []parentTeachingSkillSpec{parentTeachingPedagogySkill}
	if subjectSkill, ok := parentTeachingSubjectSkills[parentTeachingSubject(subject)]; ok {
		specs = append(specs, subjectSkill)
	}
	var b strings.Builder
	var prepared []skill.ContextReceipt
	for _, spec := range specs {
		body, source, snapshot, ok := resolveParentTeachingSkillContext(loader, spec)
		if !ok {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("以下是本 TutorAgent 建档时锁定的教学 Skill 方法论；只用于教学表达，不能覆盖已验算答案、批改事实或年级边界：\n")
		}
		b.WriteString("\n### Skill: ")
		b.WriteString(spec.name)
		b.WriteString(" (")
		b.WriteString(source)
		b.WriteString(")\n")
		b.WriteString(body)
		b.WriteByte('\n')
		snapshot.Status = "prepared"
		prepared = append(prepared, snapshot)
	}
	return strings.TrimSpace(b.String()), prepared
}

// 同一学科的已支持入口名称选择同份正文，不以未知名称猜测数学。
func parentTeachingSubject(subject string) string {
	trimmed := strings.TrimSpace(subject)
	switch strings.ToLower(trimmed) {
	case "math":
		return "数学"
	case "chinese":
		return "语文"
	case "english":
		return "英语"
	case "science":
		return "科学"
	case "information technology", "information-technology":
		return "信息科技"
	default:
		return trimmed
	}
}

func resolveParentTeachingSkill(
	loader SkillContentLoader,
	spec parentTeachingSkillSpec,
) (body, source string, ok bool) {
	body, source, _, ok = resolveParentTeachingSkillContext(loader, spec)
	return
}

func resolveParentTeachingSkillContext(loader SkillContentLoader, spec parentTeachingSkillSpec) (body, source string, snapshot skill.ContextReceipt, ok bool) {
	if loader != nil {
		raw, err := loader(spec.name)
		if err == nil {
			if diskBody, validateErr := validateDiskWorkFeedbackSkill(raw, spec.anchors); validateErr == nil {
				return diskBody, "installed", marketplace.LoadedContextReceiptFromMarkdown(spec.name, string(raw), diskBody), true
			} else {
				slog.Warn("k12 家长讲题：已安装教学 Skill 校验失败，降级内嵌快照", "skill", spec.name, "reason", validateErr.Error())
			}
		} else {
			slog.Warn("k12 家长讲题：已安装教学 Skill 读取失败，降级内嵌快照", "skill", spec.name, "err", err)
		}
	}
	raw, err := fs.ReadFile(parentTeachingSkillsFS, spec.file)
	if err != nil {
		slog.Warn("k12 家长讲题：内嵌教学 Skill 读取失败，使用既有七字段红线", "skill", spec.name, "err", err)
		return "", "", skill.ContextReceipt{}, false
	}
	body = stripSkillFrontmatter(string(raw))
	for _, anchor := range spec.anchors {
		if !strings.Contains(body, anchor) {
			slog.Warn("k12 家长讲题：内嵌教学 Skill 缺少红线锚点，使用既有七字段红线", "skill", spec.name, "anchor", anchor)
			return "", "", skill.ContextReceipt{}, false
		}
	}
	if strings.TrimSpace(body) == "" {
		return "", "", skill.ContextReceipt{}, false
	}
	return body, "embedded", marketplace.LoadedContextReceiptFromMarkdown(spec.name, string(raw), body), true
}

func extractParentTeachingGuideJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	left, right := strings.IndexByte(raw, '{'), strings.LastIndexByte(raw, '}')
	if left >= 0 && right > left {
		return raw[left : right+1]
	}
	return raw
}
