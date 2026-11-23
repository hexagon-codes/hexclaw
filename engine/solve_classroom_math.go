package engine

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/big"
	"regexp"
	"strings"
)

// ExactMathTeaching 只描述当前原题的运算和讲法，不携带学生判定、历史内容或模型回执。
type ExactMathTeaching struct {
	Method, Mistake, Prompt, Question, Check string
	Steps                                    []string
}

// VerifiedClassroomMath 由完整题面重新推导，只有所有所求、单位、年级方法和已验证答案一致才返回。
func VerifiedClassroomMath(problem, constraint, answer string) (ExactMathTeaching, bool) {
	if strings.TrimSpace(answer) == "" {
		return ExactMathTeaching{}, false
	}
	if classroomRatioAllowed(constraint) {
		if p, method, ok := ratioShareTeaching(problem); ok {
			left, valid := parseLabeledAnswerQuantities(answer)
			right, _ := parseLabeledAnswerQuantities(p.Answer)
			if !valid || len(left) != 2 || len(right) != 2 {
				return ExactMathTeaching{}, false
			}
			for label, want := range right {
				got, found := left[label]
				if !found || !quantitiesEqual(got, want) {
					return ExactMathTeaching{}, false
				}
			}
			return method, true
		}
		if worked, ratio, ok := solveSimplestRatio(problem); ok {
			got, valid := positiveIntegerRatioAnswer(normalizeAnswer(answer))
			if !valid || got != ratio {
				return ExactMathTeaching{}, false
			}
			m := simplestRatioRequestRe.FindStringSubmatch(elementaryParenthesizedFractionRe.ReplaceAllString(strings.TrimSpace(problem), "$1/$2"))
			if len(m) != 3 {
				return ExactMathTeaching{}, false
			}
			left, right, _ := strings.Cut(ratio, ":")
			return ExactMathTeaching{
				Method:   fmt.Sprintf("把 %s 和 %s 同乘分母的最小公倍数，前后项变成整数后再同时约分。", m[1], m[2]),
				Steps:    classroomWorkedSteps(worked),
				Mistake:  fmt.Sprintf("只把前项 %s 乘大而保持后项 %s 不动，会改变比值。", m[1], m[2]),
				Prompt:   fmt.Sprintf("让孩子把原来的 %s∶%s 和化简后的 %s∶%s 上下对齐，指出每次对两项做了什么。", m[1], m[2], left, right),
				Question: fmt.Sprintf("为什么 %s∶%s 还能表示原比？这两个整数还能同时除以大于 1 的整数吗？", left, right),
				Check:    fmt.Sprintf("交叉相乘核对：%s × %s = %s × %s；%s 与 %s 互质，所以所得比已最简。", m[1], right, m[2], left, left, right),
			}, true
		}
	}
	if trivialArithmeticAllowedByConstraint(problem, constraint) {
		base, forceFraction := unwrapSimplestFractionRequest(problem)
		expr, _, ok := normalizeTrivialArithmetic(base)
		if ok {
			fractions := strings.Contains(base, "/")
			distribution := (constraint == "" || strings.Contains(constraint, "分配律") || strings.Contains(constraint, "运算律") || strings.Contains(constraint, "运算定律")) &&
				!classroomMethodExcluded(constraint, "分配律", "运算律", "运算定律")
			method, want, valid := arithmeticClassroomMethod(expr, strings.TrimSpace(strings.TrimRight(base, "=＝?？")), fractions, distribution)
			got, single := new(big.Rat).SetString(normalizeAnswer(answer))
			if !valid || !single || !bareQuantityRe.MatchString(normalizeAnswer(answer)) || got.Cmp(want) != 0 || forceFraction && normalizeAnswer(answer) != want.RatString() {
				return ExactMathTeaching{}, false
			}
			return method, true
		}
	}
	if linearEquationAllowedByConstraint(problem, constraint) && !classroomMethodExcluded(constraint, "方程", "等式的性质") {
		worked, value, ok := solveLinearEquation(problem)
		if ok {
			lhs, rhs, hasX := strings.Cut(normalizeAnswer(answer), "=")
			rhs = strings.TrimSpace(rhs)
			actual, numeric := new(big.Rat).SetString(rhs)
			want, _ := new(big.Rat).SetString(value)
			if !hasX || !strings.EqualFold(strings.TrimSpace(lhs), "x") || !numeric || !bareQuantityRe.MatchString(rhs) || actual.Cmp(want) != 0 {
				return ExactMathTeaching{}, false
			}
			leftText, rightText, _ := splitLinearEquation(problem)
			left, _ := parseLinearExpression(leftText)
			right, _ := parseLinearExpression(rightText)
			x, _ := new(big.Rat).SetString(value)
			l, r := formatLinearForm(left), formatLinearForm(right)
			return ExactMathTeaching{
				Method:   fmt.Sprintf("把原方程整理为 %s = %s。两边做相同运算，逐步求出 x。", l, r),
				Steps:    classroomWorkedSteps(worked),
				Mistake:  fmt.Sprintf("整理 %s = %s 时，只改动等式的一边就会改变解。", l, r),
				Prompt:   fmt.Sprintf("请孩子先读原式 %s，指出含 x 的项，再解释每一步对等号两边做了什么。", problem),
				Question: fmt.Sprintf("把 x = %s 代回原方程，左边和右边各是多少？", value),
				Check:    fmt.Sprintf("代入原方程 x = %s，左边 %s = %s，右边 %s = %s。两边相等。", value, leftText, formatArithmeticRat(evalLinearForm(left, x)), rightText, formatArithmeticRat(evalLinearForm(right, x))),
			}, true
		}
	}
	solution, ok := solveElementaryWordProblemDetailed(problem)
	if !ok || solution.teaching == nil || !elementaryWordAllowedByConstraint(problem, constraint) {
		return ExactMathTeaching{}, false
	}
	finalAnswer := normalizeAnswer(answer)
	if !bareQuantityRe.MatchString(finalAnswer) {
		label := finalNamedCountRe.FindStringSubmatch(finalAnswer)
		if len(label) != 4 || solution.teachingTarget == "" || label[1] != solution.teachingTarget {
			return ExactMathTeaching{}, false
		}
		finalAnswer = label[2] + label[3]
	}
	got, valid := completeVerificationQuantity(finalAnswer)
	want, known := completeVerificationQuantity(solution.value + solution.unit)
	if !valid || !known || !quantitiesEqual(got, want) {
		return ExactMathTeaching{}, false
	}
	method := *solution.teaching
	method.Steps = classroomWorkedSteps(solution.worked)
	return method, true
}

func classroomRatioAllowed(constraint string) bool {
	return simplestRatioAllowedByConstraint(constraint) && !classroomMethodExcluded(constraint, "比的", "按比", "化简比", "整数比", "比例")
}

// 明确排除的方法不能因为出现在约束文本里而被当作已学。仅检查涉及该方法的同一条约束。
func classroomMethodExcluded(constraint string, methods ...string) bool {
	clauses := strings.FieldsFunc(constraint, func(r rune) bool { return strings.ContainsRune("，,。；;\n", r) })
	for _, clause := range clauses {
		for _, method := range methods {
			if !strings.Contains(clause, method) {
				continue
			}
			for _, exclusion := range []string{"禁止", "不许", "不允", "不得", "不要", "不用", "不使用", "不采用", "不能", "未学", "未教", "不含", "不包括", "排除"} {
				if strings.Contains(clause, exclusion) {
					return true
				}
			}
		}
	}
	return false
}

func classroomWorkedSteps(worked string) []string {
	body, _, _ := strings.Cut(worked, "\n\n答案：")
	return strings.Split(strings.TrimSpace(body), "\n\n")
}

// 按比分配只接受总数、两个不同对象、整份数及相同计量单位均明确的题面。
var ratioShareProblem = regexp.MustCompile(`^([\p{Han}（()）0-9]{1,30}?)有([1-9][0-9]{0,7})(人|本|段)[，,]([\p{Han}]{1,12})[与和]([\p{Han}]{1,12}?)(人数|数量|段数|本数)?的比(?:是|为)([1-9][0-9]{0,5})[:：∶]([1-9][0-9]{0,5})[。．.，,]([\p{Han}]{1,12})[、与和]([\p{Han}]{1,12}?)(?:各有|分别有)多少(人|本|段)[？?。.]?$`)

func ratioShareTeaching(problem string) (solveProgramPlan, ExactMathTeaching, bool) {
	m := ratioShareProblem.FindStringSubmatch(compactElementaryProblem(problem))
	if len(m) != 12 {
		return solveProgramPlan{}, ExactMathTeaching{}, false
	}
	group, totalText, unit, first, second, measure, firstParts, secondParts := m[1], m[2], m[3], m[4], m[5], m[6], m[7], m[8]
	if unit != m[11] || first != m[9] || second != m[10] || first == second || !affirmativeCountLabel(first) || !affirmativeCountLabel(second) ||
		measure != "" && measure != "数量" && measure != unit+"数" {
		return solveProgramPlan{}, ExactMathTeaching{}, false
	}
	// 总数必须属于这两个对象。一般场地的总人数不能用来推断任意两个子群各有多少人。
	pair := strings.TrimSuffix(strings.TrimSuffix(group, "一共"), "共")
	explicitPair := pair == first+"和"+second || pair == first+"与"+second || pair == first+"、"+second
	classMembers := unit == "人" && strings.HasSuffix(pair, "班") && ((first == "男生" && second == "女生") || (first == "女生" && second == "男生"))
	if !explicitPair && !classMembers {
		return solveProgramPlan{}, ExactMathTeaching{}, false
	}
	total, _ := positiveRat(totalText)
	a, _ := positiveRat(firstParts)
	b, _ := positiveRat(secondParts)
	parts := new(big.Rat).Add(a, b)
	per := new(big.Rat).Quo(total, parts)
	x, y := new(big.Rat).Mul(per, a), new(big.Rat).Mul(per, b)
	if !x.IsInt() || !y.IsInt() {
		return solveProgramPlan{}, ExactMathTeaching{}, false
	}
	xs, ys := x.RatString(), y.RatString()
	answer := fmt.Sprintf("%s%s%s，%s%s%s", first, xs, unit, second, ys, unit)
	method := ExactMathTeaching{
		Method: fmt.Sprintf("按 %s∶%s 分配总数 %s%s。先把两部分的份数相加，再用总数除以总份数。", firstParts, secondParts, totalText, unit),
		Steps: []string{fmt.Sprintf("总份数是 %s + %s = %s。每份是 %s ÷ %s = %s（%s）。", firstParts, secondParts, parts.RatString(), totalText, parts.RatString(), formatArithmeticRat(per), unit),
			fmt.Sprintf("%s占 %s 份，共 %s × %s = %s（%s）；%s占 %s 份，共 %s × %s = %s（%s）。", first, firstParts, formatArithmeticRat(per), firstParts, xs, unit, second, secondParts, formatArithmeticRat(per), secondParts, ys, unit)},
		Mistake:  fmt.Sprintf("比的前项 %s 对应%s，后项 %s 对应%s；对调会把两个结果给错对象。", firstParts, first, secondParts, second),
		Prompt:   fmt.Sprintf("让孩子分别画出%s的 %s 份和%s的 %s 份，指出 %s%s 对应的是两部分合计。", first, firstParts, second, secondParts, totalText, unit),
		Question: fmt.Sprintf("怎样同时用两部分的和及 %s∶%s 检查这两个答案？", firstParts, secondParts),
		Check:    fmt.Sprintf("%s + %s = %s（%s）；%s × %s = %s × %s，原总数和原比均成立。", xs, ys, totalText, unit, xs, secondParts, ys, firstParts)}
	code := fmt.Sprintf("total, a, b = F(%s), F(%s), F(%s)\nx, y = total*a/(a+b), total*b/(a+b)\nassert x+y==total and x*b==y*a\nprint('COMPUTED: ' + %q + show(x) + %q + %q + show(y) + %q)\n", totalText, firstParts, secondParts, first, unit+"，", second, unit)
	worked := strings.Join(method.Steps, "\n\n") + "\n\n答案：" + answer
	return solveProgramPlan{Problem: problem, Worked: worked, Answer: answer, Mode: "deterministic_elementary_word", Code: solveProgramPrelude + code}, method, true
}

func arithmeticClassroomMethod(expr, display string, fractions, distribution bool) (ExactMathTeaching, *big.Rat, bool) {
	tree, err := parser.ParseExpr(expr)
	if err != nil {
		return ExactMathTeaching{}, nil, false
	}
	unwrap := func(n ast.Expr) ast.Expr {
		for {
			p, ok := n.(*ast.ParenExpr)
			if !ok {
				return n
			}
			n = p.X
		}
	}
	root, ok := unwrap(tree).(*ast.BinaryExpr)
	get := func(n ast.Expr) *big.Rat {
		remaining := 128
		v, valid := evalArithmeticNode(n, &remaining)
		if !valid {
			return nil
		}
		return v
	}
	if !ok {
		return ExactMathTeaching{}, nil, false
	}
	value, l, r := get(root), get(root.X), get(root.Y)
	if value == nil || l == nil || r == nil {
		return ExactMathTeaching{}, nil, false
	}
	show := formatArithmeticRat
	if fractions {
		show = (*big.Rat).RatString
	}
	ls, rs, vs := show(l), show(r), show(value)
	method := ExactMathTeaching{}
	var steps []string
	var visit func(ast.Expr)
	visit = func(n ast.Expr) {
		b, ok := unwrap(n).(*ast.BinaryExpr)
		if !ok {
			return
		}
		visit(b.X)
		visit(b.Y)
		left, right, result := get(b.X), get(b.Y), get(b)
		op := map[token.Token]string{token.ADD: "+", token.SUB: "−", token.MUL: "×", token.QUO: "÷"}[b.Op]
		if b != root && b.Op == token.QUO && left.IsInt() && right.IsInt() {
			return
		}
		steps = append(steps, fmt.Sprintf("%s %s %s = %s。", show(left), op, show(right), show(result)))
	}
	visit(root)
	switch root.Op {
	case token.MUL:
		method.Method = fmt.Sprintf("把 %s 和 %s 看成两个因数，先约分再把分子、分母分别相乘。", ls, rs)
		method.Mistake = fmt.Sprintf("把 %s 乘 %s 时，约分只能约去分子与分母的公因数。", ls, rs)
		if fractions {
			steps = append(steps[:len(steps)-1], fractionProductStep(l, r))
		} else {
			method.Method = fmt.Sprintf("用乘法求 %s 与 %s 的积，得 %s。", ls, rs, vs)
			method.Mistake = fmt.Sprintf("两项 %s 和 %s 是乘数，把它们相加会改变原式。", ls, rs)
		}
		if r.Sign() != 0 {
			method.Check = fmt.Sprintf("反过来计算 %s ÷ (%s) = %s，能还原另一个因数。", vs, rs, ls)
		} else {
			method.Check = fmt.Sprintf("一个因数为 0，%s × 0 的积确实为 0。", ls)
		}
		for _, pair := range [][2]ast.Expr{{root.X, root.Y}, {root.Y, root.X}} {
			add, yes := unwrap(pair[0]).(*ast.BinaryExpr)
			if !distribution || !yes || add.Op != token.ADD {
				continue
			}
			f, a, b := get(pair[1]), get(add.X), get(add.Y)
			fa, fb := new(big.Rat).Mul(f, a), new(big.Rat).Mul(f, b)
			method.Method = fmt.Sprintf("用乘法分配律，把括号外的 %s 分别乘 %s 和 %s。", f.RatString(), a.RatString(), b.RatString())
			method.Mistake = fmt.Sprintf("括号中有两项，%s 必须乘到两项。", f.RatString())
			steps = []string{fmt.Sprintf("%s × %s + %s × %s = %s + %s = %s。", a.RatString(), f.RatString(), b.RatString(), f.RatString(), fa.RatString(), fb.RatString(), vs)}
			break
		}
	case token.QUO:
		method.Method = fmt.Sprintf("除以 %s，相当于乘它的倒数 %s。", rs, new(big.Rat).Inv(r).RatString())
		method.Mistake = fmt.Sprintf("把除法改写为乘法时，取除数 %s 的倒数，被除数 %s 保持原值。", rs, ls)
		if fractions {
			steps = append(steps[:len(steps)-1], fmt.Sprintf("%s ÷ (%s) = %s × (%s)。", ls, rs, ls, new(big.Rat).Inv(r).RatString()), fractionProductStep(l, new(big.Rat).Inv(r)))
		} else {
			method.Method = fmt.Sprintf("用 %s 除以 %s 求商 %s，再用乘法检查。", ls, rs, vs)
			method.Mistake = fmt.Sprintf("被除数是 %s，除数是 %s；交换位置会改变这一步。", ls, rs)
		}
		method.Check = fmt.Sprintf("商乘除数：%s × %s = %s，等于被除数。", vs, rs, ls)
	case token.ADD, token.SUB:
		if root.Op == token.ADD {
			method.Method = fmt.Sprintf("先把两项分别算到 %s 和 %s，再通分相加。", ls, rs)
			method.Check = fmt.Sprintf("从和中减去一项：%s − (%s) = %s。", vs, rs, ls)
		} else {
			method.Method = fmt.Sprintf("按运算顺序先得到 %s 和 %s，再通分相减。", ls, rs)
			method.Check = fmt.Sprintf("把差与减数相加：%s + %s = %s。", vs, rs, ls)
		}
		method.Mistake = fmt.Sprintf("核对 %s 和 %s 的分母，再合并分子；分母不能直接相加减。", ls, rs)
		if fractions && l.Denom().Cmp(r.Denom()) != 0 {
			common := new(big.Int).Mul(l.Denom(), r.Denom())
			common.Quo(common, new(big.Int).GCD(nil, nil, l.Denom(), r.Denom()))
			ln := new(big.Int).Mul(l.Num(), new(big.Int).Quo(new(big.Int).Set(common), l.Denom()))
			rn := new(big.Int).Mul(r.Num(), new(big.Int).Quo(new(big.Int).Set(common), r.Denom()))
			op := "+"
			if root.Op == token.SUB {
				op = "−"
			}
			steps = append(steps[:len(steps)-1], fmt.Sprintf("以 %s 为公分母：%s/%s %s %s/%s = %s。", common, ln, common, op, rn, common, vs))
		}
		if !fractions {
			method.Method = fmt.Sprintf("对齐 %s 和 %s 的数位，按原式的运算符计算。", ls, rs)
			method.Mistake = fmt.Sprintf("列竖式时，%s 和 %s 的个位要对齐，避免把不同数位直接相加减。", ls, rs)
		}
		a, ao := unwrap(root.X).(*ast.BinaryExpr)
		b, bo := unwrap(root.Y).(*ast.BinaryExpr)
		if distribution && ao && bo && a.Op == token.MUL && b.Op == token.MUL {
			for _, u := range [][2]ast.Expr{{a.X, a.Y}, {a.Y, a.X}} {
				for _, v := range [][2]ast.Expr{{b.X, b.Y}, {b.Y, b.X}} {
					f, g := get(u[0]), get(v[0])
					if f.Cmp(g) != 0 {
						continue
					}
					lc, rc := get(u[1]), get(v[1])
					combined := new(big.Rat).Add(lc, rc)
					op := "+"
					if root.Op == token.SUB {
						combined.Sub(lc, rc)
						op = "−"
					}
					method.Method = fmt.Sprintf("用乘法分配律，提取两个积共同的因数 %s。", f.RatString())
					steps = []string{fmt.Sprintf("%s × (%s %s %s) = %s × %s = %s。", f.RatString(), lc.RatString(), op, rc.RatString(), f.RatString(), combined.RatString(), vs)}
					method.Mistake = fmt.Sprintf("提出 %s 后，括号内应保留 %s %s %s。", f.RatString(), lc.RatString(), op, rc.RatString())
				}
			}
		}
	default:
		return ExactMathTeaching{}, nil, false
	}
	method.Steps = steps
	method.Prompt = fmt.Sprintf("请孩子指着原式 %s，解释这一步为什么成立：%s", display, steps[0])
	method.Question = fmt.Sprintf("算得 %s 后，怎样用原式中的 %s 和 %s 检查？", vs, ls, rs)
	return method, value, true
}

func fractionProductStep(left, right *big.Rat) string {
	first := new(big.Int).GCD(nil, nil, left.Num(), right.Denom())
	second := new(big.Int).GCD(nil, nil, right.Num(), left.Denom())
	n1 := new(big.Int).Quo(left.Num(), first)
	d2 := new(big.Int).Quo(right.Denom(), first)
	n2 := new(big.Int).Quo(right.Num(), second)
	d1 := new(big.Int).Quo(left.Denom(), second)
	result := new(big.Rat).Mul(left, right)
	return fmt.Sprintf("%s × %s = (%s × %s)/(%s × %s) = %s。", left.RatString(), right.RatString(), n1, n2, d1, d2, result.RatString())
}
