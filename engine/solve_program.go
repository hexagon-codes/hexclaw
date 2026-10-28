package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/skill"
)

// SolveSkillOption 仅装配已有受控执行器，未装配的库调用者保留既有精确内核行为。
type SolveSkillOption func(*SolveSkill)

type solveProgramContractKey struct{}
type solveUserProblemKey struct{}
type solveFrozenMathConstraintKey struct{}

// 课程依据只由已路由场景冻结，每轮覆盖，不能继承其他请求的方法范围。
func withSolveFrozenMathConstraint(ctx context.Context, constraint string) context.Context {
	return context.WithValue(ctx, solveFrozenMathConstraintKey{}, strings.TrimSpace(constraint))
}

func solveFrozenMathConstraint(ctx context.Context) string {
	constraint, _ := ctx.Value(solveFrozenMathConstraintKey{}).(string)
	return constraint
}

// 原题仅来自本次纯文字输入，不让子任务或附件继承另一请求的题面。
func withSolveUserProblem(ctx context.Context, msg *adapter.Message) context.Context {
	problem := ""
	if msg != nil && len(msg.Attachments) == 0 && !isSystemDispatch(msg) {
		problem = strings.TrimSpace(msg.Content)
	}
	return context.WithValue(withSolveFrozenMathConstraint(ctx, ""), solveUserProblemKey{}, problem)
}

// 原文必须完整接受既有数学语法；附加方法要求不能由模型参数替用户删去。
func compileFrozenSolveProgram(ctx context.Context, problem string) (solveProgramPlan, bool, bool) {
	constraint := solveFrozenMathConstraint(ctx)
	original, _ := ctx.Value(solveUserProblemKey{}).(string)
	if constraint == "" || original == "" || !strings.HasPrefix(problem, original) {
		return solveProgramPlan{}, false, false
	}
	suffix := strings.TrimLeft(strings.TrimPrefix(problem, original), " \t")
	if suffix != "" && !strings.HasPrefix(suffix, "。") && !strings.HasPrefix(suffix, "\n") {
		return solveProgramPlan{}, false, false
	}
	plan, ok := compileSolveProgram(original, constraint)
	return plan, ok, true
}

// WithSolveProgramExecution 仅供内部场景按已冻结合同选择计算路径。
func WithSolveProgramExecution(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, solveProgramContractKey{}, enabled)
}

func solveProgramEnabled(ctx context.Context) bool {
	enabled, specified := ctx.Value(solveProgramContractKey{}).(bool)
	return !specified || enabled
}

// WithSolveProgramExecutor 注入 ReActEngine 的受控程序执行器；失败不转成模型计算或自动重试。
func WithSolveProgramExecutor(execute SubAgentExecFunc) SolveSkillOption {
	return func(s *SolveSkill) { s.programFunc = execute }
}

// WithSolveTeachingContextSelector 仅投影已消费的冻结教学上下文，不加载技能或决定解题路由。
func WithSolveTeachingContextSelector(selectReceipts func(string, []skill.ContextReceipt) []skill.ContextReceipt) SolveSkillOption {
	return func(s *SolveSkill) { s.teachingContextSelector = selectReceipts }
}

type solveProgramPlan struct {
	Problem, Code, Worked, Answer, Mode string
}

// arithmeticProgram 只编译已由精确内核完整接受的 AST，不执行用户字符串。
func arithmeticProgram(expression string) (string, bool) {
	normalized, _, ok := normalizeTrivialArithmetic(expression)
	if !ok {
		return "", false
	}
	node, err := parser.ParseExpr(normalized)
	if err != nil {
		return "", false
	}
	remaining := 128
	if _, ok := evalArithmeticNode(node, &remaining); !ok {
		return "", false
	}
	var compile func(ast.Expr) string
	compile = func(expr ast.Expr) string {
		switch n := expr.(type) {
		case *ast.BasicLit:
			return fmt.Sprintf("F(%q)", n.Value)
		case *ast.ParenExpr:
			return "(" + compile(n.X) + ")"
		case *ast.UnaryExpr:
			return "(" + n.Op.String() + compile(n.X) + ")"
		case *ast.BinaryExpr:
			return "(" + compile(n.X) + n.Op.String() + compile(n.Y) + ")"
		}
		return ""
	}
	return compile(node), true
}

const solveProgramPrelude = `from fractions import Fraction as F
from math import gcd
def show(value):
    if value.denominator == 1:
        return str(value.numerator)
    d = value.denominator
    for p in (2, 5):
        while d % p == 0:
            d //= p
    if d != 1:
        return str(value)
    from decimal import Decimal, localcontext
    with localcontext() as c:
        c.prec = 1024
        return format(Decimal(value.numerator) / Decimal(value.denominator), 'f').rstrip('0').rstrip('.')
`

func compileSolveProgram(problem, constraint string) (solveProgramPlan, bool) {
	plan := solveProgramPlan{Problem: problem}
	if trivialArithmeticAllowedByConstraint(problem, constraint) {
		if worked, answer, ok := solveTrivialArithmetic(problem); ok {
			expression, forceFraction := unwrapSimplestFractionRequest(problem)
			code, ok := arithmeticProgram(expression)
			if !ok {
				return plan, false
			}
			format := "show(value)"
			if forceFraction {
				format = "str(value)"
			}
			plan.Worked, plan.Answer, plan.Mode = worked, answer, "deterministic_arithmetic"
			plan.Code = solveProgramPrelude + "value = " + code + "\nprint('COMPUTED: ' + " + format + ")\n"
			return plan, true
		}
	}
	if simplestRatioAllowedByConstraint(constraint) {
		if worked, answer, ok := solveSimplestRatio(problem); ok {
			m := simplestRatioRequestRe.FindStringSubmatch(elementaryParenthesizedFractionRe.ReplaceAllString(strings.TrimSpace(problem), "$1/$2"))
			left, leftOK := arithmeticProgram(m[1])
			right, rightOK := arithmeticProgram(m[2])
			if !leftOK || !rightOK {
				return plan, false
			}
			plan.Worked, plan.Answer, plan.Mode = worked, answer, "deterministic_arithmetic"
			plan.Code = solveProgramPrelude + "left, right = " + left + ", " + right + "\nratio = left / right\nprint('COMPUTED: ' + str(ratio.numerator) + ':' + str(ratio.denominator))\n"
			return plan, true
		}
	}
	if linearEquationAllowedByConstraint(problem, constraint) {
		if worked, answer, ok := solveLinearEquation(problem); ok {
			left, right, _ := splitLinearEquation(problem)
			l, _ := parseLinearExpression(left)
			r, _ := parseLinearExpression(right)
			plan.Worked, plan.Answer, plan.Mode = worked, answer, "deterministic_linear_equation"
			plan.Code = solveProgramPrelude + "def left(x): return " + l.program + "\ndef right(x): return " + r.program + "\na = (left(F(1))-left(F(0))) - (right(F(1))-right(F(0)))\nx = (right(F(0))-left(F(0))) / a\nassert left(x) == right(x)\nprint('COMPUTED: ' + show(x))\n"
			return plan, true
		}
	}
	solution, ok := solveElementaryWordProblemDetailed(problem)
	quantity := false
	if !ok {
		solution, ok = solveQuantityPerItemProblem(problem)
		quantity = ok
	}
	allowed := elementaryWordAllowedByConstraint(problem, constraint)
	if quantity {
		allowed = strings.TrimSpace(constraint) == "" || strings.Contains(constraint, "分数乘整数") || strings.Contains(constraint, "分数乘法")
	}
	if !ok || !allowed || solution.problemIssue != "" || solution.program == "" {
		return plan, false
	}
	plan.Worked, plan.Answer, plan.Mode = solution.worked, solution.value+solution.unit, "deterministic_elementary_word"
	plan.Code = solveProgramPrelude + solution.program
	return plan, true
}

// SolveProgramExecutor 复用真实 ToolExecutor 的完整 Hook 链，不绕过权限和当前沙箱策略。
func (e *ReActEngine) SolveProgramExecutor() SubAgentExecFunc {
	return func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
		if !spec.ProgramOnly || !solveGrantFromContext(ctx) || e.toolExecutor == nil {
			return SubAgentResult{}, fmt.Errorf("solve program executor is unavailable")
		}
		if e.toolExecutor.skills == nil {
			return SubAgentResult{}, fmt.Errorf("solve program skill is unavailable")
		}
		if enabled, found := e.toolExecutor.skills.IsEnabled(codeExecToolName); !found || !enabled {
			return SubAgentResult{}, fmt.Errorf("solve program skill is unavailable")
		}
		var plan solveProgramPlan
		if err := json.Unmarshal([]byte(spec.Task), &plan); err != nil {
			return SubAgentResult{}, err
		}
		sink := &codeExecutionReceiptSink{inputDigest: executionInputDigest(spec.Task)}
		ctx = context.WithValue(withSystemDispatch(ctx, solveDispatchSource), codeExecutionReceiptKey{}, sink)
		_, err := e.toolExecutor.Execute(ctx, codeExecToolName, map[string]any{"mode": "snippet", "language": "python", "code": plan.Code, "timeout": 30})
		sink.mu.Lock()
		defer sink.mu.Unlock()
		observed := SubAgentResult{ExecutionReceipt: sink.receipt, ExecutionReceipts: append([]CodeExecutionReceipt(nil), sink.receipts...)}
		if sink.receipt != nil {
			observed.Output = sink.receipt.Stdout
		}
		if err != nil {
			return observed, err
		}
		if sink.receipt == nil {
			return SubAgentResult{}, fmt.Errorf("solve program execution receipt is missing")
		}
		computed, valid := compiledProgramComputed(sink.receipt, spec.Task, plan.Answer)
		if !valid || !(answersEqual(computed, plan.Answer) || computed == plan.Answer) {
			return observed, fmt.Errorf("solve program execution result is invalid")
		}
		return observed, nil
	}
}

func (s *SolveSkill) executeSolveProgram(ctx context.Context, plan solveProgramPlan) (*skill.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	spec := verifierSpec(plan.Problem, plan.Answer, "")
	spec.Task, spec.ProgramOnly = string(raw), true
	// 原拦截器负责持久化／缓存；本地程序只派一次，不走模型瞬时错误重试。
	result, err := executeSubAgentCall(withSolveGrant(ctx), s.programFunc, spec)
	if err != nil {
		return nil, err
	}
	computed, valid := compiledProgramComputed(result.ExecutionReceipt, spec.Task, plan.Answer)
	if !valid || !(answersEqual(computed, plan.Answer) || computed == plan.Answer) {
		return nil, fmt.Errorf("solve program execution result is invalid")
	}
	resolved := &skill.Result{Content: plan.Worked, Metadata: map[string]string{
		"solve_mode": plan.Mode, "solve_verdict": "agree", "solve_evidence": "numeric_exec", "solve_computed": plan.Answer,
		"solve_primary_digest": executionInputDigest(plan.Worked), "solve_verification_input_digest": executionInputDigest(spec.Task),
		"solve_verification_run_id": result.ExecutionReceipt.RunID,
	}}
	s.appendTeachingContextReceipt(ctx, "math", resolved.Metadata)
	return resolved, nil
}

// 本题指导只关联已消费上下文和成功求解；数值校验等级独立保留，不以加载名单补足。
func (s *SolveSkill) appendTeachingContextReceipt(ctx context.Context, subject string, metadata map[string]string) {
	if s.teachingContextSelector == nil || metadata["solve_verdict"] != "agree" {
		return
	}
	receipts := s.teachingContextSelector(subject, ConsumedSkillContextReceipts(ctx))
	if encoded, err := json.Marshal(receipts); err == nil && len(receipts) > 0 {
		metadata[teachingContextReceiptsMetadataKey] = string(encoded)
	}
}

func wordProgram(expressions []string, units []string) string {
	if len(expressions) != len(units) || len(expressions) == 0 {
		return ""
	}
	var code strings.Builder
	for i, expression := range expressions {
		compiled, ok := arithmeticProgram(expression)
		if !ok {
			return ""
		}
		fmt.Fprintf(&code, "v%d = %s\n", i, compiled)
	}
	parts := make([]string, len(expressions))
	for i, unit := range units {
		parts[i] = fmt.Sprintf("show(v%d) + %q", i, unit)
	}
	code.WriteString("print('COMPUTED: ' + " + strings.Join(parts, " + '；' + ") + ")\n")
	return code.String()
}

// 编译器已冻结答案语义；仅这一内部合同允许有序多数量或整数比与精确内核逐字一致。
func compiledProgramComputed(receipt *CodeExecutionReceipt, task, answer string) (string, bool) {
	if computed, ok := receipt.computed(task); ok {
		return computed, true
	}
	if receipt == nil || receipt.InputDigest != executionInputDigest(task) || receipt.RunID == "" ||
		receipt.Status != "success" || receipt.ExitCode != 0 || receipt.Timeout || receipt.RuntimeMissing || receipt.Error != "" ||
		receipt.Truncated || receipt.StdoutTruncated || receipt.StderrTruncated || receipt.StdoutBytes <= 0 || int64(len(receipt.Stdout)) != receipt.StdoutBytes {
		return "", false
	}
	match := executionComputedMarker.FindAllStringSubmatch(receipt.Stdout, -1)
	if len(match) != 1 || strings.TrimSpace(receipt.Stdout) != "COMPUTED: "+strings.TrimSpace(match[0][1]) {
		return "", false
	}
	computed := strings.TrimSpace(match[0][1])
	if computed != answer {
		return "", false
	}
	return computed, true
}
