package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
)

var quantitySlotUnitPrefix = regexp.MustCompile(`(?i)^(` + elementarymath.UnitPattern() + `)(?:[\s。.,，;；!?？]|$)`)
var quantityAnswerUnitSuffix = regexp.MustCompile(`(?i)(` + elementarymath.UnitPattern() + `)\s*$`)
var piLeftProduct = regexp.MustCompile(`([0-9)π])\s*π`)
var piRightProduct = regexp.MustCompile(`π\s*([0-9(])`)
var piParenthesizedProduct = regexp.MustCompile(`([0-9)])\s*\(`)

// QuantityAnswerCompleteness 只检查当前题唯一的已知单位空位是否收到完整数量。
// 返回值不证明数值正确、过程正确或原图可读；没有明确数量目标时不改变原求解语义。
func QuantityAnswerCompleteness(problem, answer string) (required, complete bool) {
	if _, current, _, ok := elementarymath.ParseMathSubproblem(problem); ok {
		problem = current
	}
	problem = normalizeArithmeticAnswerMarkup(adapter.NormalizeMathText(problem))
	slots := verificationBlankSlotRe.FindAllStringIndex(problem, -1)
	if len(slots) != 1 {
		return false, false
	}
	unit := quantitySlotUnitPrefix.FindStringSubmatch(strings.TrimSpace(problem[slots[0][1]:]))
	if len(unit) != 2 || elementarymath.NormalizeUnit(unit[1]) == "" {
		return false, false
	}
	answer = normalizeAnswer(normalizeArithmeticAnswerMarkup(adapter.NormalizeMathText(answer)))
	for _, prefix := range []string{"最终答案：", "最终答案:", "答案：", "答案:", "答：", "答:"} {
		answer = strings.TrimSpace(strings.TrimPrefix(answer, prefix))
	}
	// 只解开完整的肯定命名答句；后续仍检查整个量，不从解释段落抽取数字。
	named := namedQuantityConclusionRe.FindStringSubmatch(strings.NewReplacer("：", "为", ":", "为").Replace(answer))
	if len(named) == 3 && affirmativeCountLabel(named[1]) {
		answer = named[2]
	}
	if _, ok := completeVerificationQuantity(answer); ok {
		return true, true
	}
	return true, completePiQuantity(answer)
}

// π保留为符号，只用标准表达式语法检查完整性，不以近似值替换或推断正确答案。
func completePiQuantity(answer string) bool {
	answer = strings.ReplaceAll(answer, `\pi`, "π")
	if !strings.Contains(answer, "π") {
		return false
	}
	if unit := quantityAnswerUnitSuffix.FindStringIndex(answer); unit != nil {
		answer = strings.TrimSpace(answer[:unit[0]])
	}
	answer = strings.NewReplacer("×", "*", "÷", "/", "＋", "+", "－", "-", "−", "-", "（", "(", "）", ")").Replace(answer)
	answer = piLeftProduct.ReplaceAllString(answer, "$1*π")
	answer = piRightProduct.ReplaceAllString(answer, "π*$1")
	answer = piParenthesizedProduct.ReplaceAllString(answer, "$1*(")
	expression, err := parser.ParseExpr(answer)
	if err != nil {
		return false
	}
	valid, hasPi := true, false
	ast.Inspect(expression, func(node ast.Node) bool {
		if !valid {
			return false
		}
		switch n := node.(type) {
		case nil, *ast.ParenExpr:
		case *ast.Ident:
			valid = n.Name == "π"
			hasPi = hasPi || valid
		case *ast.BasicLit:
			_, valid = numericValue(n.Value)
			valid = valid && (n.Kind == token.INT || n.Kind == token.FLOAT)
		case *ast.UnaryExpr:
			valid = n.Op == token.ADD || n.Op == token.SUB
		case *ast.BinaryExpr:
			valid = n.Op == token.ADD || n.Op == token.SUB || n.Op == token.MUL || n.Op == token.QUO
			if n.Op == token.QUO {
				remaining := 64
				if value, known := evalArithmeticNode(n.Y, &remaining); known && value.Sign() == 0 {
					valid = false
				}
			}
		default:
			valid = false
		}
		return valid
	})
	return valid && hasPi
}
