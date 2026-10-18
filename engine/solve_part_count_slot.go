package engine

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math/big"
	"strings"

	"github.com/hexagon-codes/hexclaw/egress"
)

type partCountSlotSolveScopeKey struct{}

var errPartCountSlotAnswerTarget = errors.New("part-count slot answer does not match the current task")

// completePartCountSlotAnswer 复用完整数量语义，不从时间、路程或复合答句截取份数。
func completePartCountSlotAnswer(answer string) (answerQuantity, bool) {
	quantity, ok := completeVerificationQuantity(answer)
	if !ok || quantity.unit != "" && quantity.unit != "份" {
		return answerQuantity{}, false
	}
	value, ok := new(big.Rat).SetString(quantity.value)
	return quantity, ok && value.Sign() >= 0
}

// validatePartCountSlotSolverResult 在持久化前及重放后核对同一完整标准答案。
func validatePartCountSlotSolverResult(result SubAgentResult) error {
	if _, complete := completePartCountSlotAnswer(extractFinalAnswer(result.Output)); !complete {
		return errors.Join(egress.ErrProviderResponseProcessed, errPartCountSlotAnswerTarget)
	}
	return nil
}

// projectPartCountExecutedStep 只把本次求解器实际输出的唯一数值校验等式补入缺方法的解答。
// 等式还须进入原独立 verifier 审计，回执成功本身不证明它符合原题；不处理旧回放或联合指南。
func projectPartCountExecutedStep(task string, result SubAgentResult) SubAgentResult {
	if result.Generation != nil || !result.ExecutionReceipt.completeFor(task) {
		return result
	}
	output := strings.TrimSpace(result.Output)
	answerText := output
	if match := answerMarker.FindStringSubmatch(output); len(match) == 2 && match[0] == output {
		answerText = match[1]
	}
	if !bareQuantityRe.MatchString(normalizeAnswer(answerText)) {
		return result
	}
	answer, onlyAnswer := completePartCountSlotAnswer(answerText)
	if !onlyAnswer {
		return result
	}
	answerValue, _ := new(big.Rat).SetString(answer.value)
	var equation string
	for _, rawLine := range strings.Split(result.ExecutionReceipt.Stdout, "\n") {
		line := strings.TrimSpace(rawLine)
		marked := false
		for _, prefix := range []string{"check:", "验算：", "校验："} {
			if strings.HasPrefix(strings.ToLower(line), prefix) {
				line, marked = strings.TrimSpace(line[len(prefix):]), true
				break
			}
		}
		left, right, found := strings.Cut(strings.ReplaceAll(line, "＝", "="), "=")
		if !found || !singleQuantityEquationLeftRe.MatchString(left) || !singleQuantityEquationLeftRe.MatchString(right) {
			if marked {
				return result
			}
			continue
		}
		if equation != "" || strings.Contains(right, "=") {
			return result
		}
		leftNode, leftValue, leftOK := partCountExecutedExpression(left)
		rightNode, rightValue, rightOK := partCountExecutedExpression(right)
		if !leftOK || !rightOK || leftValue.Cmp(rightValue) != 0 ||
			(!partCountHasAnswerOperand(leftNode, answerValue) && !partCountHasAnswerOperand(rightNode, answerValue)) {
			return result
		}
		equation = line
	}
	if equation != "" {
		result.Output = equation + "\n\n" + result.Output
	}
	return result
}

func partCountExecutedExpression(expression string) (ast.Expr, *big.Rat, bool) {
	normalized, _, ok := normalizeTrivialArithmetic(expression)
	if !ok {
		return nil, nil, false
	}
	node, err := parser.ParseExpr(normalized)
	if err != nil {
		return nil, nil, false
	}
	remaining := 128
	value, ok := evalArithmeticNode(node, &remaining)
	return node, value, ok
}

// 分数视作一个完整数值操作数，不把其分子或等号一侧的独立答案误认作校验关系。
func partCountHasAnswerOperand(node ast.Expr, answer *big.Rat) bool {
	for {
		parenthesized, ok := node.(*ast.ParenExpr)
		if !ok {
			break
		}
		node = parenthesized.X
	}
	if partCountNumericOperand(node) {
		return false
	}
	binary, ok := node.(*ast.BinaryExpr)
	if !ok {
		return false
	}
	for _, operand := range []ast.Expr{binary.X, binary.Y} {
		if partCountNumericOperand(operand) {
			remaining := 128
			if value, valid := evalArithmeticNode(operand, &remaining); valid && value.Cmp(answer) == 0 {
				return true
			}
		} else if partCountHasAnswerOperand(operand, answer) {
			return true
		}
	}
	return false
}

func partCountNumericOperand(node ast.Expr) bool {
	switch node := node.(type) {
	case *ast.ParenExpr:
		return partCountNumericOperand(node.X)
	case *ast.BasicLit:
		return node.Kind == token.INT || node.Kind == token.FLOAT
	case *ast.UnaryExpr:
		return (node.Op == token.ADD || node.Op == token.SUB) && partCountNumericOperand(node.X)
	case *ast.BinaryExpr:
		return node.Op == token.QUO && partCountScalarOperand(node.X) && partCountScalarOperand(node.Y)
	default:
		return false
	}
}

func partCountScalarOperand(node ast.Expr) bool {
	switch node := node.(type) {
	case *ast.ParenExpr:
		return partCountScalarOperand(node.X)
	case *ast.BasicLit:
		return node.Kind == token.INT || node.Kind == token.FLOAT
	case *ast.UnaryExpr:
		return (node.Op == token.ADD || node.Op == token.SUB) && partCountScalarOperand(node.X)
	default:
		return false
	}
}
