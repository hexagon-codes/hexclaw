package usecase

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func clearObservedStudentAnswer(req GradeRequest) string {
	if req.ObservedAnswerState == AnswerStatePresent {
		return strings.TrimSpace(req.ObservedStudentAnswer)
	}
	// 文字批改有明确输入；照片的未知/不清状态不能因解答补全而变成清楚作答。
	if req.ObservedAnswerState == "" {
		return strings.TrimSpace(req.StudentAnswer)
	}
	return ""
}

func gradeSourceProblem(req GradeRequest) string {
	if strings.TrimSpace(req.SourceProblem) != "" {
		return req.SourceProblem
	}
	return req.Problem
}

type practiceDiagnosticContext struct {
	SourceID      string `json:"source_id"`
	SourceVersion int    `json:"source_version"`
	Observation   string `json:"observation"`
	Hypothesis    string `json:"hypothesis"`
	Distinction   string `json:"distinction"`
}

func singlePracticeDiagnostic(source *records.AgentRecord, fields k12.MistakeFields) *practiceDiagnosticContext {
	observed := strings.TrimSpace(fields.ObservedStudentAnswer)
	if observed == "" || strings.TrimSpace(fields.Question) == "" || strings.TrimSpace(fields.CanonicalAnswer) == "" {
		return nil
	}
	normal := strings.NewReplacer(" ", "", "×", "*", "÷", "/", "（", "(", "）", ")").Replace(observed)
	diagnostic := &practiceDiagnosticContext{SourceID: source.RecordID, SourceVersion: source.Version, Observation: observed}
	switch {
	case strings.Contains(fields.Question, "总容量") && strings.Contains(fields.Question, "已用") &&
		(strings.Contains(normal, "2/5/2") || strings.Contains(normal, `\frac{2}{5}/2`)):
		diagnostic.Hypothesis = "may_confuse_ratio_and_quantity"
		diagnostic.Distinction = "Keep the part/whole relation and use different explicit capacity quantities. Ask for the meaning and unit of each quantity, then one-part capacity and total capacity. State the reference answer and checks separately. A new response may support or weaken the hypothesis; one correct response does not establish mastery."
	case strings.Contains(fields.WrongProcess, "单位") || strings.Contains(fields.ErrorCause, "单位"):
		diagnostic.Hypothesis = "may_confuse_or_omit_unit"
		diagnostic.Distinction = "Keep the same mathematical relation with explicit units. Ask for the numerical result and its unit separately, provide a reference answer and separate checks for calculation and units. Do not assume a conceptual error from a missing unit alone."
	default:
		return nil
	}
	return diagnostic
}

func (d *practiceDiagnosticContext) prompt() string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("\nDiagnostic context (a hypothesis, not an established learner trait): source=%s version=%d; observed answer=%q; hypothesis=%s. %s Generate exactly one discriminating question; do not add a second task or claim what the student has learned. Stop after this task until a real new response is available.\n诊断式变式：题目本身须保留实际数量、单位和原数量关系，不能退成裸算式。若区分分率与容量，必须在同一题中让孩子说明分率与容量的含义、求每份容量及总容量；分别给出参考答案和核对依据。", d.SourceID, d.SourceVersion, d.Observation, d.Hypothesis, d.Distinction)
}

var diagnosticFraction = regexp.MustCompile(`[0-9]+/[0-9]+`)

// 只排除与当前明确假设无关的题型；计算正确性仍走原独立验算链。
func (d *practiceDiagnosticContext) acceptsQuestion(question string) bool {
	if d == nil {
		return true
	}
	text := diagramSourceText(question)
	amounts := diagramSourceQuantity.FindAllStringSubmatch(text, -1)
	if len(amounts) == 0 {
		return false
	}
	if d.Hypothesis != "may_confuse_ratio_and_quantity" {
		return true
	}
	capacity := false
	for _, amount := range amounts {
		capacity = capacity || elementarymath.UnitCategory(amount[2]) == "data"
	}
	return capacity && diagnosticFraction.MatchString(text) && strings.Contains(text, "总") &&
		(strings.Contains(text, "已用") || strings.Contains(text, "用了") || strings.Contains(text, "占")) &&
		(strings.Contains(text, "每份") || strings.Contains(text, "一份") || strings.Contains(text, "1份")) &&
		(strings.Contains(text, "表示") || strings.Contains(text, "含义") || strings.Contains(text, "意义") || strings.Contains(text, "分率") || strings.Contains(text, "单位"))
}
