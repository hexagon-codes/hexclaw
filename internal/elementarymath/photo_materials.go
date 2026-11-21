package elementarymath

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

// PhotoMaterialSeparator 是现有求解请求的只读来源分隔符，参与持久调用摘要。
const PhotoMaterialSeparator = "\n\nRead-only source material (do not answer as another question):\n"
const PhotoRecoverySeparator = "\n\nVerified source fragment for the declared "
const TextbookEvidenceSeparator = "\n\nVerified textbook evidence (use it only to constrain the solution and grading; it is not the student's answer; do not expose internal source identifiers). Respond in Chinese:\n"
const photoCurrentQuestion = "\n\nCurrent question and answer requirements: "
const photoAnswerRequirements = ". The read-only materials supply the shared conditions and any explicitly printed common main problem. When this item specifies a method, relationship or multiplier for that common problem, complete both its own intermediate answers and the common requested final quantity, including the capacity calculation line; do not stop at a ratio conversion. Do not answer other numbered items. Answer every requested blank, relationship, multiplier, intermediate quantity and final expression. Put ALL required reference answers in the Answer section, with the conditions for alternative valid fillings; a final scalar alone is insufficient when the current question asks for other answers. Preserve the printed equation template's sides, operations and fixed coefficients when filling blanks. A rearranged equation can explain a relationship but cannot replace the filling of a different printed template. These completeness requirements apply to the reference solution; a student may give any one valid alternative rather than enumerate all alternatives."

// WithPhotoAnswerRequirements 附上当前题及完整作答要求，保持原请求的序列化身份。
func WithPhotoAnswerRequirements(problem, current string) string {
	quoted, _ := json.Marshal(current)
	return problem + photoCurrentQuestion + string(quoted) + photoAnswerRequirements
}

var printedPhotoNumber = regexp.MustCompile(`^([0-9]+)([.、])`)

// PhotoComputationQuestion 只剥离已知题号，保留小数、公式外层和所有正文。
func PhotoComputationQuestion(question, number, label string) string {
	view := strings.TrimSpace(question)
	open, close := "", ""
	if strings.HasPrefix(view, `\(`) && strings.HasSuffix(view, `\)`) {
		open, close = `\(`, `\)`
		view = strings.TrimSpace(view[len(open) : len(view)-len(close)])
	}
	if rest, ok := strings.CutPrefix(view, number+"、"); ok {
		return open + strings.TrimSpace(rest) + close
	}
	if label == number+"." {
		if rest, ok := strings.CutPrefix(view, label); ok {
			for _, layout := range []string{"", `\qquad`, `\quad`, `\`} {
				if body, found := strings.CutPrefix(rest, layout); found && strings.TrimLeftFunc(body, unicode.IsSpace) != body {
					return open + strings.TrimLeftFunc(body, unicode.IsSpace) + close
				}
			}
		}
	}
	return question
}

// PhotoVerificationProblem 从既有格式恢复独立字段；执行副本与冻结的请求及回执分开。
func PhotoVerificationProblem(problem string) string {
	source, textbook, grounded := strings.Cut(problem, TextbookEvidenceSeparator)
	body, ok := strings.CutSuffix(source, photoAnswerRequirements)
	if !ok {
		return problem
	}
	before, quoted, ok := strings.Cut(body, photoCurrentQuestion)
	if !ok {
		return problem
	}
	primary, materials := before, ""
	for _, marker := range []string{PhotoMaterialSeparator, PhotoRecoverySeparator} {
		if pos := strings.Index(before, marker); pos >= 0 && pos < len(primary) {
			primary, materials = before[:pos], before[pos:]
		}
	}
	var current string
	if json.Unmarshal([]byte(quoted), &current) != nil || strings.TrimSpace(current) == "" {
		return problem
	}
	if primary != current {
		number := printedPhotoNumber.FindStringSubmatch(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(current), `\(`)))
		if len(number) != 3 || PhotoComputationQuestion(current, number[1], number[0]) != primary {
			return problem
		}
	}
	if grounded {
		materials += TextbookEvidenceSeparator + textbook
	}
	if materials == "" {
		return primary
	}
	scoped, ok := BuildMathSubproblem(strings.TrimPrefix(materials, PhotoMaterialSeparator), primary, "")
	if !ok {
		return problem
	}
	return scoped
}

type photoVerificationCopy struct{ wire, scope string }
type photoVerificationKey struct{}

// WithPhotoVerificationCopy 在适配器转换模型文本之前保留结构化的来源，避免转义丢失。
func WithPhotoVerificationCopy(ctx context.Context, wire, scope string) context.Context {
	return context.WithValue(ctx, photoVerificationKey{}, photoVerificationCopy{wire, scope})
}

func PhotoVerificationFor(ctx context.Context, problem string) string {
	if copy, ok := ctx.Value(photoVerificationKey{}).(photoVerificationCopy); ok && copy.wire == problem {
		return copy.scope
	}
	return PhotoVerificationProblem(problem)
}
