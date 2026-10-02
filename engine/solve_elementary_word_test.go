package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSolveElementaryWordProblem(t *testing.T) {
	tests := []struct {
		name, problem, answer string
	}{
		{
			name:    "fraction inverse",
			problem: "一个数的3/8是24，求这个数？",
			answer:  "64",
		},
		{
			name:    "successive fractions",
			problem: "8的1/4的4/5是多少？",
			answer:  "1.6",
		},
		{
			name:    "rectangle perimeter and yield",
			problem: "一个周长是300米的长方形鱼塘，长是宽的2倍。如果每平方米产鱼2.25千克，一共产鱼多少千克？",
			answer:  "11250",
		},
		{
			name:    "open cube fish tank glass and water depth",
			problem: "小明的爸爸用玻璃做了一个棱长是6dm的正方体鱼缸。制作这个鱼缸时，至少需要玻璃多少平方米？小明在鱼缸里注入144L的水，水面高度是多少分米？",
			answer:  "1.8平方米；4分米",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worked, answer, ok := solveElementaryWordProblem(tt.problem)
			if !ok || answer != tt.answer {
				t.Fatalf("solveElementaryWordProblem() = %q,%v; want %q,true", answer, ok, tt.answer)
			}
			if !strings.Contains(worked, tt.answer) || !strings.Contains(worked, "答案") {
				t.Fatalf("worked solution missing answer: %q", worked)
			}
		})
	}
}

func TestSolveElementaryWordProblemSixNumberBalanceLegacy(t *testing.T) {
	for _, problem := range []string{
		"在下列六个数：5、6、12、14、23、29中划去一个数（ ）后，能使其中3个数的和为另外2个数和的2倍。",
		"在下列六个数：5，6，12，14，23，29中划去数（ ）后，能使其中3个数的和为另外2个数和的2倍。",
	} {
		worked, answer, ok := solveElementaryWordProblem(problem)
		if !ok || answer != "29" || !strings.Contains(worked, "40 = 20×2") {
			t.Fatalf("legacy six-number solution = %q, %q, %v; want remove 29 and 40 = 20×2", worked, answer, ok)
		}
		if !elementaryWordAllowedByConstraint(problem, "整数的加法与倍数关系") || elementaryWordAllowedByConstraint(problem, "分数的意义") {
			t.Fatalf("legacy six-number curriculum constraint changed: %q", problem)
		}
	}
	for _, problem := range []string{
		"在下列六个数：5、6、12、14、23、29中划去一个数（ ），后，能使其中3个数的和为另外2个数和的2倍。",
		"在下列六个数：5、6、12、14、23、29中划去一个数（ ）后，能使其中3个数的和为另外2个数和的2倍。且剩下的数均为偶数。",
	} {
		if _, _, ok := solveElementaryWordProblem(problem); ok || elementaryWordAllowedByConstraint(problem, "整数的加法与倍数关系") {
			t.Fatalf("legacy solver or constraint accepted unsupported six-number text: %q", problem)
		}
	}
}

func TestSolveElementaryWordProblemFishTankUsesFiveFacesAndCubicDecimeterVolume(t *testing.T) {
	problem := "小明的爸爸用玻璃做了一个棱长是6dm的正方体鱼缸。制作这个鱼缸时，至少需要玻璃多少平方米？小明在鱼缸里注入144L的水，水面高度是多少分米？"
	worked, answer, ok := solveElementaryWordProblem(problem)
	if !ok {
		t.Fatal("complete fish-tank problem was not solved")
	}
	if answer != "1.8平方米；4分米" {
		t.Fatalf("answer = %q; want both exact quantities", answer)
	}
	for _, want := range []string{"共 5 个面", "6×6×5 = 180", "180÷100 = 1.8", "底面积：6×6 = 36", "144÷(6×6) = 4"} {
		if !strings.Contains(worked, want) {
			t.Fatalf("worked solution %q missing %q", worked, want)
		}
	}
}

func TestSolveElementaryWordProblemFishTankReturnsNumericEvidence(t *testing.T) {
	problem := "小明的爸爸用玻璃做了一个棱长是6dm的正方体鱼缸。制作这个鱼缸时，至少需要玻璃多少平方米？小明在鱼缸里注入144L的水，水面高度是多少分米？"
	calls := 0
	s := NewSolveSkill(func(context.Context, SubAgentSpec) (SubAgentResult, error) {
		calls++
		return SubAgentResult{Output: "不应调用模型"}, nil
	}, NewSubAgentRegistry(""))
	res, err := s.Execute(context.Background(), map[string]any{
		"problem":    problem,
		"grade":      "五年级下",
		"constraint": "长方体和正方体的表面积、体积和容积",
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("确定性题型不应调用模型，calls=%d", calls)
	}
	if res.Metadata["solve_mode"] != "deterministic_elementary_word" || res.Metadata["solve_verdict"] != "agree" || res.Metadata["solve_evidence"] != "numeric_exec" || res.Metadata["solve_computed"] != "1.8平方米；4分米" {
		t.Fatalf("unexpected metadata: %#v", res.Metadata)
	}
}

func TestSolveElementaryWordProblemFishTankOutsideConstraintReturnsImmediately(t *testing.T) {
	problem := "小明的爸爸用玻璃做了一个棱长是6dm的正方体鱼缸。制作这个鱼缸时，至少需要玻璃多少平方米？小明在鱼缸里注入144L的水，水面高度是多少分米？"
	calls := 0
	s := NewSolveSkill(func(context.Context, SubAgentSpec) (SubAgentResult, error) {
		calls++
		return SubAgentResult{Output: "不应调用模型"}, nil
	}, NewSubAgentRegistry(""))
	res, err := s.Execute(context.Background(), map[string]any{
		"problem":    problem,
		"grade":      "五年级上",
		"constraint": "小数乘法、小数除法、多边形的面积",
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("超出约束时不应调用模型，calls=%d", calls)
	}
	if res.Metadata["solve_verdict"] != "out_of_scope" || res.Metadata["solve_out_of_scope_kp"] != "长方体和正方体的表面积、体积和容积" {
		t.Fatalf("unexpected metadata: %#v", res.Metadata)
	}
}

func TestSolveElementaryWordProblemFishTankFailsClosedOnPartialStructure(t *testing.T) {
	for _, problem := range []string{
		"小明的爸爸用玻璃做了一个棱长是6dm的正方体鱼缸。制作这个鱼缸时，至少需要玻璃多少平方米？",
		"小明的爸爸用玻璃做了一个棱长是6m的正方体鱼缸。制作这个鱼缸时，至少需要玻璃多少平方米？小明在鱼缸里注入144L的水，水面高度是多少分米？",
		"小明的爸爸用玻璃做了一个棱长是6dm的正方体鱼缸。制作这个鱼缸时，至少需要玻璃多少平方分米？小明在鱼缸里注入144L的水，水面高度是多少分米？",
		"小明的爸爸用玻璃做了一个棱长是6dm的正方体鱼缸。制作这个鱼缸时，至少需要玻璃多少平方米？小明在鱼缸里注入144mL的水，水面高度是多少分米？",
	} {
		if _, _, ok := solveElementaryWordProblem(problem); ok {
			t.Fatalf("partial or unit-mismatched fish-tank problem must not be guessed: %q", problem)
		}
	}
}

func TestSolveElementaryWordProblemRejectsContradictoryGCDLCMTicket(t *testing.T) {
	problem := "小明有张10至40排的电影票，这张票的排数和座位号的最大公约数是13，最小公倍数是72，小明这张电影票是（）排（）号。"
	if _, _, ok := solveElementaryWordProblem(problem); ok {
		t.Fatal("a contradictory GCD/LCM ticket must not be reported as solved")
	}

	calls := 0
	s := NewSolveSkill(func(context.Context, SubAgentSpec) (SubAgentResult, error) {
		calls++
		return SubAgentResult{Output: "不应调用模型"}, nil
	}, NewSubAgentRegistry(""))
	res, err := s.Execute(context.Background(), map[string]any{
		"problem":    problem,
		"grade":      "五年级下",
		"constraint": "最大公约数、最小公倍数",
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("矛盾题面应由程序拦截，不应调用模型，calls=%d", calls)
	}
	if res.Metadata["solve_mode"] != "deterministic_problem_validation" || res.Metadata["solve_verdict"] != "unverifiable" || res.Metadata["solve_problem_issue"] != "inconsistent_gcd_lcm" || res.Metadata["solve_evidence"] != "numeric_exec" {
		t.Fatalf("unexpected metadata: %#v", res.Metadata)
	}
	if !strings.Contains(res.Content, "题目信息矛盾") || !strings.Contains(res.Content, "13") || !strings.Contains(res.Content, "72") || !strings.Contains(res.Content, "请核对") {
		t.Fatalf("unexpected conflict response: %q", res.Content)
	}
}

func TestSolveElementaryWordProblemGCDLCMValidationFailsClosed(t *testing.T) {
	for _, problem := range []string{
		"小明有张10至40排的电影票，这张票的排数和座位号的最大公约数是12，最小公倍数是72，小明这张电影票是（）排（）号。",
		"这张票的排数和座位号的最大公约数是13，最小公倍数是72。",
	} {
		if solution, ok := solveElementaryWordProblemDetailed(problem); ok && solution.problemIssue != "" {
			t.Fatalf("consistent or partial ticket must not be classified as contradictory: %q", problem)
		}
	}
}

func TestSolveElementaryWordProblemNewTypesRespectConstraint(t *testing.T) {
	problem := "小明的爸爸用玻璃做了一个棱长是6dm的正方体鱼缸。制作这个鱼缸时，至少需要玻璃多少平方米？小明在鱼缸里注入144L的水，水面高度是多少分米？"
	for _, constraint := range []string{"正方体的表面积和体积", "长方体和正方体的容积"} {
		if !elementaryWordAllowedByConstraint(problem, constraint) {
			t.Fatalf("constraint %q should allow fish-tank problem", constraint)
		}
	}
	if elementaryWordAllowedByConstraint(problem, "小数乘法、小数除法") {
		t.Fatal("unrelated constraint must reject fish-tank problem")
	}

	ticket := "小明有张10至40排的电影票，这张票的排数和座位号的最大公约数是13，最小公倍数是72，小明这张电影票是（）排（）号。"
	if !elementaryWordAllowedByConstraint(ticket, "最大公因数、最小公倍数") {
		t.Fatal("五年级下同义课程约束应允许检查最大公约数/最小公倍数")
	}
	if elementaryWordAllowedByConstraint(ticket, "小数乘法、小数除法") {
		t.Fatal("unrelated constraint must reject GCD/LCM ticket problem")
	}
}

func TestSolveElementaryWordProblemRejectsUnknownText(t *testing.T) {
	for _, problem := range []string{
		"小明有一些苹果，送人后还剩多少？",
		"证明三角形内角和是180度",
		"一个数的3/0是24，求这个数？",
		"一个周长是300米的鱼塘，一共产鱼多少千克？",
		"一个数的3/8是24，求3/8是多少？",
		"一个周长是300米的长方形鱼塘，长是宽的2倍。如果每平方米产鱼2.25千克，面积是多少平方米？",
		"一个周长是300米的长方形鱼塘，长是宽的2倍。如果每平方米产鱼2.25千克，宽是多少米？",
	} {
		if _, _, ok := solveElementaryWordProblem(problem); ok {
			t.Fatalf("unsupported problem must not be guessed: %q", problem)
		}
	}
}

func TestSolveElementaryWordProblemUsesGradeAppropriateIntegerSteps(t *testing.T) {
	worked, _, ok := solveElementaryWordProblem("一个数的3/8是24，求这个数？")
	if !ok {
		t.Fatal("inverse fraction problem was not solved")
	}
	if strings.Contains(worked, "÷(3/8)") || !strings.Contains(worked, "24÷3×8") {
		t.Fatalf("inverse solution uses an out-of-grade fraction division: %q", worked)
	}

	worked, _, ok = solveElementaryWordProblem("8的1/4的4/5是多少？")
	if !ok {
		t.Fatal("successive fraction problem was not solved")
	}
	if strings.Contains(worked, "×1/4") || !strings.Contains(worked, "8÷4×1÷5×4") {
		t.Fatalf("successive-fraction solution uses an out-of-grade fraction multiplication: %q", worked)
	}
}

func TestSolveElementaryWordProblemOutsideConstraintReturnsImmediately(t *testing.T) {
	calls := 0
	s := NewSolveSkill(func(context.Context, SubAgentSpec) (SubAgentResult, error) {
		calls++
		return SubAgentResult{Output: "不应调用模型"}, nil
	}, NewSubAgentRegistry(""))
	res, err := s.Execute(context.Background(), map[string]any{
		"problem":    "一个数的3/8是24，求这个数？",
		"grade":      "五年级上",
		"constraint": "小数乘法、小数除法、多边形的面积",
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("已知题型超出约束时不应掉进慢模型链，calls=%d", calls)
	}
	if res.Metadata["solve_verdict"] != "out_of_scope" || res.Metadata["solve_out_of_scope_kp"] != "分数的意义和性质" {
		t.Fatalf("unexpected metadata: %#v", res.Metadata)
	}
}

func TestSolveTotalFractionCountExactAndScoped(t *testing.T) {
	for _, tc := range []struct {
		name, problem, answer, unit string
	}{
		{"books", "图书角有240本书，故事书占总数的3/8。故事书有多少本？", "90", "本"},
		{"different-fraction", "书架有360本书，科技书占总数的5/12。科技书有多少本？", "150", "本"},
		{"people", "班级有36人，男生占总数的4/9。男生有多少人？", "16", "人"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			solve := NewSolveSkill(func(context.Context, SubAgentSpec) (SubAgentResult, error) {
				calls++
				return SubAgentResult{}, errors.New("unexpected model call")
			}, nil)
			result, err := solve.Execute(t.Context(), map[string]any{
				"problem": tc.problem, "grade": "六年级上", "constraint": "分数乘法、分数除法、比",
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 || result.Metadata["solve_mode"] != "deterministic_elementary_word" ||
				result.Metadata["solve_computed"] != tc.answer || result.Metadata["solve_evidence"] != "numeric_exec" ||
				result.Metadata["solve_verdict"] != "agree" || !strings.Contains(result.Content, "答案："+tc.answer+" "+tc.unit) {
				t.Fatalf("quantity result=%+v calls=%d; want %s%s with independent exact calculation", result, calls, tc.answer, tc.unit)
			}
		})
	}

	const problem = "图书角有240本书，故事书占总数的3/8。故事书有多少本？"
	calls := 0
	solve := NewSolveSkill(func(context.Context, SubAgentSpec) (SubAgentResult, error) {
		calls++
		return SubAgentResult{}, errors.New("unexpected model call")
	}, nil)
	result, err := solve.Execute(t.Context(), map[string]any{"problem": problem, "constraint": "只使用分数加减法"})
	if err != nil || calls != 0 || result.Metadata["solve_verdict"] != "out_of_scope" {
		t.Fatalf("unsupported method changed existing scope behavior: result=%+v err=%v calls=%d", result, err, calls)
	}
	if !elementaryWordAllowedByConstraint(problem, "") {
		t.Fatal("empty method constraint lost its existing behavior")
	}
	for _, extra := range []map[string]any{{"self_consistency": 1}, {"method_diversity": false}} {
		fallback := errors.New("explicit strategy model path")
		calls := 0
		solve := NewSolveSkill(func(context.Context, SubAgentSpec) (SubAgentResult, error) {
			calls++
			return SubAgentResult{}, fallback
		}, nil)
		args := map[string]any{"problem": problem, "constraint": "分数乘法"}
		for key, value := range extra {
			args[key] = value
		}
		result, err := solve.Execute(t.Context(), args)
		if result != nil || !errors.Is(err, fallback) || calls == 0 {
			t.Fatalf("explicit strategy was bypassed: result=%+v err=%v calls=%d", result, err, calls)
		}
	}
}

func TestSolveTotalFractionCountRequiresCompleteSource(t *testing.T) {
	for _, problem := range []string{
		"图书角有本书，故事书占总数的3/8。故事书有多少本？",
		"图书角有240本书，故事书占总数的3/。故事书有多少本？",
		"图书角有240本书，故事书占总数的3/0。故事书有多少本？",
		"图书角有240本书，故事书占总数的9/8。故事书有多少本？",
		"图书角有10本书，故事书占总数的1/3。故事书有多少本？",
		"图书角有240本书，故事书占总数的3/8。科技书有多少本？",
		"图书角有240本书，故事书占总数的3/8。故事书有多少人？",
		"图书角有240本书，故事书占总数的3/8。故事书有多少本？再借出10本。",
		"图书角有240本书，故事书占总数的3/8，还有20本漫画。故事书有多少本？",
		"如果图书角有240本书，故事书占总数的3/8。故事书有多少本？",
		"图书角有240本书，不是故事书占总数的3/8。不是故事书有多少本？",
	} {
		if solution, ok := solveElementaryWordProblemDetailed(problem); ok {
			t.Fatalf("incomplete or conflicting source was accepted: %q -> %+v", problem, solution)
		}
	}
}

func TestGradeVerifiedTotalFractionCountPreservesWorkAndUnits(t *testing.T) {
	const problem = "图书角有240本书，故事书占总数的3/8。故事书有多少本？"
	const solution = "故事书占总数的3/8，240÷8×3=90本。\n答案：90本。"
	for _, tc := range []struct {
		name, student, verified string
		correct, finalCorrect   string
		fallback                bool
	}{
		{"direct", "240×3/8=90（本）\n答：90本。", solution, "true", "true", false},
		{"fraction-first", "(3/8)×240=90本\n答：90本", solution, "true", "true", false},
		{"product-first", "(240×3)÷8=90本\n答：90本", solution, "true", "true", false},
		{"share-one-line", "240÷8×3=90本\n答：90本", solution, "true", "true", false},
		{"two-shares", "240÷8=30本\n30×3=90本\n答：90本", solution, "true", "true", false},
		{"wrong-final", "240×3/8=80本\n答：80本", solution, "false", "false", false},
		{"wrong-work-correct-final", "240×3/8=80本\n答：90本", solution, "false", "true", false},
		{"wrong-unit", "答：90人", solution, "false", "false", false},
		{"missing-unit", "答：90", solution, "false", "false", false},
		{"wrong-intermediate-unit", "240×3/8=90（人）\n答：90本", solution, "false", "true", true},
		{"unrelated-equality", "100−10=90本\n答：90本", solution, "false", "true", true},
		{"extra-unrelated-equality", "240×3/8=90本\n100−10=90本\n答：90本", solution, "false", "true", true},
		{"multiple-conclusions", "240×3/8=90本\n答：80本\n答：90本", solution, "false", "true", true},
		{"division-grouping-conflict", "240÷(8×3)=10本\n答：90本", solution, "false", "true", true},
		{"complex-explanation", "先画线段图再分成八份，取三份。\n答：90本", solution, "false", "true", true},
		{"verified-value-conflict", "240×3/8=90本\n答：90本", "答案：80本", "false", "true", true},
		{"verified-unit-conflict", "240×3/8=90本\n答：90本", "答案：90人", "false", "true", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := &solveExec{graderOut: "CORRECT: no\nFINAL_ANSWER_CORRECT: yes\nWRONG_STEP: 需独立判断完整过程\nMISCONCEPTION: 不能仅按尾值判断"}
			result, err := NewSolveSkill(exec.fn, nil).GradeVerified(t.Context(), problem, tc.verified, tc.student)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			if tc.fallback {
				calls = 1
			}
			if exec.graderCalls() != calls || exec.solverCalls() != 0 || exec.verifierCalls() != 0 ||
				result.Metadata["grade_correct"] != tc.correct || result.Metadata["grade_final_answer_correct"] != tc.finalCorrect {
				t.Fatalf("grading lost source, process or unit boundary: result=%+v agents=%v", result, exec.agents())
			}
			if tc.name == "wrong-work-correct-final" && result.Metadata["grade_wrong_step"] == "" {
				t.Fatal("wrong arithmetic step was not retained")
			}
			if tc.fallback {
				grader, ok := exec.specFor(graderAgentName)
				if !ok || !strings.Contains(grader.Task, problem) || !strings.Contains(grader.Task, tc.student) {
					t.Fatal("fallback lost source or complete original work")
				}
			}
		})
	}
}
