package engine

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

const paperProblem = "环保小组用回收的包装纸做纸花，每一朵花需要用 (3)/(8) 张纸。\n\n做 2 朵花需要用多少张纸？"
const numberedPaperProblem = "环保小组用回收的包装纸做纸花，做一朵花需要用 (3)/(8) 张纸。\n\n（1）做 2 朵花需要用多少张纸？"
const paperScope = "\n\nFor this request, solve or assess only subproblem （1）. Use the shared material as context; do not answer the other subproblems."
const paperTextbook = "\n\nVerified textbook evidence (use it only to constrain the solution and grading; it is not the student's answer; do not expose internal source identifiers). Respond in Chinese:\n# 分数乘法\n（1）做2朵花需要用多少张纸？\n（2）做8朵花需要用多少张纸？\n讨论一下：分数乘整数，怎样计算？\n分数乘整数，用分子乘整数的积作分子，分母不变。能约分的可先约分，再计算。"

const fractionRuleQuestion = "讨论一下：分数乘整数，怎样计算？"
const fractionRuleAnswer = "分数乘整数，用分子乘整数的积作分子，分母不变；能约分的先约分再计算。"
const fractionRuleSolution = "把分数乘整数看成几个相同的分数相加。\n例如2/9×3=2/9+2/9+2/9=6/9=2/3。\n也可先将整数3与分母9约分，得到(2×1)/3=2/3。\n答案：" + fractionRuleAnswer
const fractionRuleContract = "完整解释分数乘整数的规律，以重复相加说明理由，给出约分示例、易错提醒及家长追问。"
const fractionRuleConstraint = "小学六年级分数乘整数，只用重复相加及约分，不使用代数证明。"

func fractionRuleArgs(problem string) map[string]any {
	return map[string]any{
		"problem": problem, "solve_output_version": jointGuideVersion,
		"parent_teaching_contract": fractionRuleContract,
		"grade":                    "小学六年级", "constraint": fractionRuleConstraint,
	}
}

func fractionRuleGuideJSON(t *testing.T) (string, map[string]any) {
	t.Helper()
	guide := map[string]any{
		"answer":                   fractionRuleAnswer,
		"full_solution_steps":      []string{"把分数乘整数看成几个相同分数相加。", "2/9×3=2/9+2/9+2/9=6/9=2/3。", "先将整数3与分母9约分，得到(2×1)/3=2/3。"},
		"grade_level_method":       "从三个2/9相加说明只把分子乘3，分母表示的每份大小不变。",
		"likely_mistakes":          []string{"不能把分子和分母都乘3，6/27仍等于原来的2/9，而不是三个2/9。"},
		"parent_teaching_sequence": []string{"先让孩子说出2/9×3表示几个2/9相加。", "写出同分母相加过程，再说明分母为什么不变。", "最后比较先约分与算完再约分的结果。"},
		"follow_up_questions":      []string{"2/9×6怎样算？分子2乘6、分母9不变，12/9=4/3。"},
		"checking_method":          "用2/9+2/9+2/9=6/9=2/3核对乘法和约分。",
	}
	raw, err := json.Marshal(map[string]any{"schema": jointGuideVersion, "solution": fractionRuleSolution, "parent_guide": guide})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), guide
}

func fractionRuleJudgment(t *testing.T, audits ...map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(audits)
	if err != nil {
		t.Fatal(err)
	}
	return "VERDICT: UNVERIFIABLE\nPROCESS: VALID\nCOMPUTED: N/A\nSCOPE: IN_SCOPE\nPARENT_GUIDE_AUDITS: " + string(raw)
}

func fractionRuleAudit() map[string]any {
	audit := jointGuideAudit(fractionRuleSolution, "IN_SCOPE")
	for _, field := range audit["fields"].([]map[string]any) {
		field["reason"] = "规律来自同分母重复相加，示例、约分及引导均符合小学分数乘整数范围。"
	}
	return audit
}

const printedPaperProblem = `环保小组用回收的包装纸做纸花，做一朵花需要用 (3)/(8) 张纸。

（1）做2朵花需要用多少张纸？

做2朵花需要2个 (3)/(8) 张，可以直接用乘法计算。

(3)/(8)×2=\underline{\qquad}

图示标签：3 个 (1)/(8) 张；3 个 (1)/(8) 张；(3×2) 个 (1)/(8) 张。

印刷计算过程：
(3)/(8)×2=(3)/(8)+(3)/(8)=(3+3)/(8)=(3×2)/(8)=(6)/(8)=(3)/(4)

印刷约分标记：分子 6 约为 3，分母 8 约为 4。`

// 附加资料留给模型使用，但不能把一道普通题变成需要多种解法的难题。
func TestSolveComplexityUsesCurrentProblem(t *testing.T) {
	for _, tc := range []struct{ name, problem string }{
		{"concept-actual-discussion", fractionRuleQuestion},
		{"concept-polite", "请问分数乘整数如何计算?"},
		{"concept-discussion-colon", "讨论：分数乘整数怎么算？"},
		{"concept-no-prefix", "分数乘整数怎样计算"},
		{"concept-thinking-spaces", "想一想: 分数乘整数，怎样计算？"},
		{"concept-frozen-sources", fractionRuleQuestion + paperTextbook + "\n\nFrozen practice reference (verified):\n其他题讨论两种方法与证明。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, expectedGuide := fractionRuleGuideJSON(t)
			exec := &solveExec{solverOuts: []string{raw, raw}, verifierOut: fractionRuleJudgment(t, fractionRuleAudit())}
			args := fractionRuleArgs(tc.problem)
			result, err := NewSolveSkill(exec.fn, nil).Execute(t.Context(), args)
			if err != nil {
				t.Fatal(err)
			}
			if exec.solverCalls() != 1 || exec.verifierCalls() != 1 || len(exec.specs) != 2 {
				t.Fatalf("calculation-rule question needs one generation and an independent audit: %v", exec.agents())
			}
			if result.Metadata["solve_evidence"] != "model" || result.Metadata["solve_verdict"] != "unverifiable" || result.Metadata["solve_parent_guide_audit"] != "VALID" {
				t.Fatalf("concept explanation lost its audit or acquired numeric proof: %v", result.Metadata)
			}
			var actualGuide map[string]any
			if err := json.Unmarshal([]byte(result.Metadata["solve_parent_guide_json"]), &actualGuide); err != nil {
				t.Fatal(err)
			}
			var expected map[string]any
			expectedJSON, _ := json.Marshal(expectedGuide)
			if err := json.Unmarshal(expectedJSON, &expected); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actualGuide, expected) || result.Metadata["solve_parent_guide_source_digest"] != jointGuideDigest(fractionRuleSolution) || !strings.Contains(result.Content, fractionRuleSolution) {
				t.Fatal("single generation shortened or mismatched the complete solution and seven-field guide")
			}
			if args["problem"] != tc.problem || args["parent_teaching_contract"] != fractionRuleContract {
				t.Fatal("triage changed the frozen original input")
			}
			for _, spec := range exec.specs {
				if !strings.Contains(spec.Task, tc.problem) || !strings.Contains(spec.Task, fractionRuleConstraint) {
					t.Fatalf("%s lost the original problem, sources or curriculum constraint", spec.Agent)
				}
				if spec.Agent == solverAgentName && !strings.Contains(spec.Task, fractionRuleContract) {
					t.Fatal("single generation lost the complete teaching contract")
				}
				if spec.Agent == verifierAgentName {
					if !strings.Contains(spec.Task, jointGuideDigest(fractionRuleSolution)) || !strings.Contains(spec.Task, fractionRuleSolution) {
						t.Fatal("independent audit lost the selected solution and identity")
					}
					for _, field := range jointGuideTestFields {
						if !strings.Contains(spec.Task, field) {
							t.Fatalf("independent audit omitted guide field %s", field)
						}
					}
				}
			}
		})
	}
	for _, tc := range []struct{ name, problem, answer string }{
		{"scope-and-textbook", paperProblem + paperScope + paperTextbook, "3/4张纸"},
		{"textbook", paperProblem + paperTextbook, "3/4张纸"},
		{"frozen-reference", paperProblem + paperScope + "\n\nFrozen practice reference (verified):\n讨论不同解法，（1）参考第一问；（2）参考第二问。", "3/4张纸"},
		{"selected-numbered-first", numberedPaperProblem + "\n\nFor this request, solve or assess only subproblem 1. Use the shared material as context; do not answer the other subproblems." + paperTextbook, "3/4张纸"},
		{"selected-numbered-second", "环保小组用回收的包装纸做纸花，做一朵花需要用 (3)/(8) 张纸。\n\n（2）做 8 朵花需要用多少张纸？\n\nFor this request, solve or assess only subproblem 2. Use the shared material as context; do not answer the other subproblems." + paperTextbook, "3张纸"},
		{"selected-expression-denominator-second", "环保小组用回收的包装纸做纸花，做一朵花需要用 (3)/(8) 张纸。\n\n（2）做8朵花需要用多少张纸？\n\n(3)/(8)×8=(3×8)/(8)=3（印刷约分标记：分子中的8与分母8均约为1）。\n\nFor this request, solve or assess only subproblem 2. Use the shared material as context; do not answer the other subproblems." + paperTextbook, "3张纸"},
		{"selected-expression-denominator-first", numberedPaperProblem + `
印刷提示：做2朵需要2个(3)/(8)张纸。
(3)/(8)×2=\underline{\qquad}
印刷演示：(3)/(8)×2=(3)/(8)+(3)/(8)=(3+3)/(8)=(3×2)/(8)=(6)/(8)=(3)/(4)。` + paperScope + paperTextbook, "3/4张纸"},
		{"selected-inline-printed-example-first", numberedPaperProblem + `做 2 朵花需要 2 个 (3)/(8) 张，可以直接用乘法计算。(3)/(8)×2=\underline{\qquad}。印刷示例：(3)/(8)×2=(3)/(8)+(3)/(8)=(3+3)/(8)=(3×2)/(8)=(6)/(8)=(3)/(4)。` + paperScope + paperTextbook, "3/4张纸"},
		{"selected-bare-numerator-denominator", numberedPaperProblem + "\n3/(8)×2=3/4" + paperScope + paperTextbook, "3/4张纸"},
		{"selected-actual-printed-context-first", printedPaperProblem + "\n\nFor this request, solve or assess only subproblem 1. Use the shared material as context; do not answer the other subproblems." + paperTextbook, "3/4张纸"},
		{"selected-actual-printed-context-second", "环保小组用回收的包装纸做纸花，做一朵花需要用 (3)/(8) 张纸。\n\n（2）做8朵花需要用多少张纸？\n\n印刷计算过程：\n(3)/(8)×8=(3×8)/(8)=3\n\n印刷约分标记：分子中的 8 和分母 8 分别约为 1。\n\nFor this request, solve or assess only subproblem 2. Use the shared material as context; do not answer the other subproblems." + paperTextbook, "3张纸"},
		{"selected-actual-markdown-context-first", `环保小组用回收的包装纸做纸花，做一朵花需要用 $\frac{3}{8}$ 张纸。

（1）做2朵花需要用多少张纸？

**印刷提示：**做2朵花需要2个 $\frac{3}{8}$ 张，可以直接用乘法计算。

$$\frac{3}{8}\times2=\underline{\qquad}$$

**图示标签：**3个 $\frac{1}{8}$ 张；3个 $\frac{1}{8}$ 张；$(3\times2)$ 个 $\frac{1}{8}$ 张。

**印刷计算过程：**
$$\frac{3}{8}\times2=\frac{3}{8}+\frac{3}{8}=\frac{3+3}{8}=\frac{3\times2}{8}=\frac{6}{8}=\frac{3}{4}$$` + paperScope + paperTextbook, "3/4张纸"},
		{"selected-actual-markdown-context-second", `环保小组用回收的包装纸做纸花，做一朵花需要用 $\frac{3}{8}$ 张纸。

（2）做8朵花需要用多少张纸？

**印刷计算过程：**
$$\frac{3}{8}\times8=\frac{3\times8}{8}=3$$

印刷约分标记将分子中的8和分母8分别约为1。` + "\n\nFor this request, solve or assess only subproblem （2）. Use the shared material as context; do not answer the other subproblems." + paperTextbook, "3张纸"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := &solveExec{
				solverOuts:     []string{"每朵用纸乘以朵数。\n答案：" + tc.answer},
				verifierOut:    "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: " + tc.answer,
				verifierStdout: "COMPUTED: " + tc.answer + "\n",
			}
			result, err := NewSolveSkill(exec.fn, nil).Execute(t.Context(), solveArgs(tc.problem))
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "selected-numbered-first" || tc.name == "selected-numbered-second" {
				if len(exec.specs) != 0 || result.Metadata["solve_mode"] != "deterministic_elementary_word" || !strings.Contains(strings.Join(strings.Fields(result.Content), ""), tc.answer) {
					t.Fatalf("complete elementary calculation needs the correct local result without model calls: %v", result.Metadata)
				}
			} else if exec.solverCalls() != 1 || len(exec.specs) != 2 || !exec.has(verifierAgentName) {
				t.Fatalf("ordinary problem needs one solver and one verifier: %v", exec.agents())
			}
			if result.Metadata["solve_evidence"] != "numeric_exec" {
				t.Fatalf("independent verification was lost: %v", result.Metadata)
			}
			for _, spec := range exec.specs {
				if !strings.Contains(spec.Task, tc.problem) {
					t.Fatalf("%s lost the original problem, scope or source", spec.Agent)
				}
			}
		})
	}
	for _, tc := range []struct{ name, problem, solution, answer string }{
		{"single-target-actual-inline-number", `环保小组用回收的包装纸做纸花，做一朵花需要用 $\frac{3}{8}$ 张纸。（1）做 2 朵花需要用多少张纸？`, "3/8×2=6/8=3/4。\n答案：3/4张纸", "3/4张纸"},
		{"single-target-actual-leading-number", "（2）做 8 朵花需要用多少张纸？", "3/8×8=24/8=3。\n答案：3张纸", "3张纸"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guide := map[string]any{
				"answer":                   tc.answer,
				"full_solution_steps":      []string{tc.solution},
				"grade_level_method":       "分数乘整数，把分子乘花朵数量，再约分。",
				"likely_mistakes":          []string{"不能把每朵用纸量与花朵数相加；应把每朵用纸量乘花朵数。"},
				"parent_teaching_sequence": []string{"先确认每朵用纸量与花朵数，再列乘法算式。", tc.solution},
				"follow_up_questions":      []string{"如何用相同加数相加核对乘法？"},
				"checking_method":          "把每朵用纸量按花朵数相加，与乘法结果核对。",
			}
			raw, err := json.Marshal(map[string]any{"schema": jointGuideVersion, "solution": tc.solution, "parent_guide": guide})
			if err != nil {
				t.Fatal(err)
			}
			audit := jointGuideAudit(tc.solution, "IN_SCOPE")
			for _, field := range audit["fields"].([]map[string]any) {
				field["reason"] = "该字段与当前单题的分数乘法步骤一致。"
			}
			audits, err := json.Marshal([]map[string]any{audit})
			if err != nil {
				t.Fatal(err)
			}
			exec := &solveExec{
				solverOuts:     []string{string(raw), string(raw)},
				verifierOut:    "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: " + tc.answer + "\nPARENT_GUIDE_AUDITS: " + string(audits),
				verifierStdout: "COMPUTED: " + tc.answer + "\n",
			}
			problem := tc.problem + paperTextbook
			args := map[string]any{
				"problem": problem, "solve_output_version": jointGuideVersion,
				"parent_teaching_contract": "用分数乘整数解释当前一道题，并保留单位。",
			}
			result, err := NewSolveSkill(exec.fn, nil).Execute(t.Context(), args)
			if exec.solverCalls() != 1 || len(exec.specs) != 2 || !exec.has(verifierAgentName) {
				t.Fatalf("single-target guide needs one solver and one verifier: %v", exec.agents())
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Metadata["solve_evidence"] != "numeric_exec" || result.Metadata["solve_parent_guide_audit"] != "VALID" {
				t.Fatalf("independent verification or guide audit was lost: %v", result.Metadata)
			}
			if args["problem"] != problem {
				t.Fatal("classification changed the original problem")
			}
			for _, spec := range exec.specs {
				if !strings.Contains(spec.Task, problem) {
					t.Fatalf("%s lost the original problem or source", spec.Agent)
				}
			}
			if assessComplexity(problem) != complexityHard {
				t.Fatal("ordinary output without a fixed scope lost its conservative classification")
			}
		})
	}
}

func TestSolveComplexityPreservesHardQuestionsAndExplicitSettings(t *testing.T) {
	for _, tc := range []struct {
		name, problem string
		alter         func(map[string]any)
	}{
		{"concept-other-topic", "讨论一下：分数乘分数，怎样计算？", nil},
		{"concept-additional-condition", "讨论一下：分数乘整数（整数为负数），怎样计算？", nil},
		{"concept-proof", "讨论一下：分数乘整数，怎样计算并证明？", nil},
		{"concept-derivation", "讨论一下：分数乘整数，如何推导计算规则？", nil},
		{"concept-why", "讨论一下：分数乘整数，怎样计算？为什么？", nil},
		{"concept-second-question", "讨论一下：分数乘整数，怎样计算？再算3/8×2。", nil},
		{"concept-legacy", fractionRuleQuestion, func(args map[string]any) {
			delete(args, "solve_output_version")
			delete(args, "parent_teaching_contract")
		}},
		{"concept-grading", fractionRuleQuestion, func(args map[string]any) {
			delete(args, "solve_output_version")
			args["student_answer"] = fractionRuleAnswer
		}},
		{"concept-explicit-diversity", fractionRuleQuestion, func(args map[string]any) { args["method_diversity"] = true }},
		{"concept-explicit-samples", fractionRuleQuestion, func(args map[string]any) { args["self_consistency"] = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := fractionRuleArgs(tc.problem)
			if tc.alter != nil {
				tc.alter(args)
			}
			raw, _ := fractionRuleGuideJSON(t)
			if args["solve_output_version"] == nil {
				raw = fractionRuleSolution
			}
			exec := &solveExec{solverOuts: []string{raw, raw}, verifierOut: fractionRuleJudgment(t, fractionRuleAudit()),
				graderOut: "CORRECT: yes\nFINAL_ANSWER_CORRECT: yes\nWRONG_STEP: N/A\nMISCONCEPTION: N/A\nGUIDANCE: 规律说明正确。"}
			_, err := NewSolveSkill(exec.fn, nil).Execute(t.Context(), args)
			if err != nil {
				t.Fatal(err)
			}
			if exec.solverCalls() != 2 || exec.verifierCalls() != 1 {
				t.Fatalf("true hard question, legacy grading or explicit strategy was downgraded: %v", exec.agents())
			}
			if tc.name == "concept-grading" && exec.graderCalls() != 1 {
				t.Fatal("student-answer grading was bypassed")
			}
		})
	}
	t.Run("concept-invalid-output-contract", func(t *testing.T) {
		for _, alter := range []func(map[string]any){
			func(args map[string]any) { args["parent_teaching_contract"] = "" },
			func(args map[string]any) { args["student_answer"] = fractionRuleAnswer },
		} {
			args := fractionRuleArgs(fractionRuleQuestion)
			alter(args)
			exec := &solveExec{}
			result, err := NewSolveSkill(exec.fn, nil).Execute(t.Context(), args)
			if result != nil || err == nil || len(exec.specs) != 0 {
				t.Fatalf("invalid v1 contract was accepted: result=%+v err=%v calls=%v", result, err, exec.agents())
			}
		}
	})
	t.Run("single-target-multiple-numbers", func(t *testing.T) {
		const solution = "7盒每盒6张和3盒每盒14张都是42张。\n答案：42张"
		raw, _ := jointGuideJSON(t, solution, "用乘法分别计算并比较张数。")
		exec := &solveExec{
			solverOuts:     []string{raw, raw},
			verifierOut:    jointGuideJudgment(t, "AGREE", "VALID", jointGuideAudit(solution, "IN_SCOPE")),
			verifierStdout: "COMPUTED: 42张\n",
		}
		args := jointGuideArgs()
		delete(args, "self_consistency")
		args["problem"] = "（1）每盒6张卡片，7盒有多少张？\n（2）每盒14张卡片，3盒合计是否等于第一问？"
		_, err := NewSolveSkill(exec.fn, nil).Execute(t.Context(), args)
		if err != nil {
			t.Fatal(err)
		}
		if exec.solverCalls() != 2 || len(exec.specs) != 3 || !exec.has(verifierAgentName) {
			t.Fatalf("multiple numbered questions lost method diversity: %v", exec.agents())
		}
	})
	for _, problem := range []string{
		"每朵花用3/8张纸。（1）做2朵花需要多少张纸？（2）做8朵花需要多少张纸？",
		"每朵花用3/8张纸。\n（1）做2朵花需要多少张纸？\n（2）做8朵花需要多少张纸？",
		"每朵花用3/8张纸。\n（1）计算 (3×2)/(8)。\n（2）计算 (3×8)/(8)。",
		"每朵花用3/8张纸。\n（1）做2朵花需要多少张纸？\n(8) 做8朵花需要多少张纸？",
		"每朵花用3/8张纸。\n（2）做2朵花需要多少张纸？",
		"每朵花用3/8张纸。（1）做2朵花需要多少张纸？",
		"（1）讨论一下：分数乘整数，怎样计算？",
		"（1）讨论一下：分数乘整数，怎样计算？\n\n印刷计算过程：3/8×2=3/4。\n印刷约分标记：分子6和分母8同时除以2。",
		"求证：三角形内角和等于180度。",
		"学校准备组织六年级学生参加环保活动。第一小组收集了一批包装纸，每张纸可以制作八朵大小相同的纸花，每朵纸花用掉整张纸的八分之一。第二小组制作的纸花数量是第一小组的两倍，第三小组比第二小组少做十六朵。最后三个小组一共完成了八十朵纸花，过程中没有浪费包装纸，每个小组用纸都由公共仓库统一领用。第一小组一共用了多少张包装纸？",
	} {
		if got := assessComplexity(problem + paperScope + paperTextbook); got != complexityHard {
			t.Errorf("actual hard question was downgraded: %q", problem)
		}
	}
	if got := assessComplexity(numberedPaperProblem + paperTextbook); got != complexityHard {
		t.Fatal("a number without a fixed single-subproblem scope was removed")
	}
	incompleteScope := numberedPaperProblem + "\n\nFor this request, solve or assess only subproblem （1）. Explain the answer."
	if got := complexityProblemStem(incompleteScope); got != incompleteScope {
		t.Fatal("non-system text was removed from the question")
	}
	for _, tc := range []struct {
		name, key string
		value     any
		calls     int
	}{
		{"explicit-diversity", "method_diversity", true, 2},
		{"explicit-samples", "self_consistency", 2, 2},
		{"explicit-single-method", "method_diversity", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := &solveExec{
				solverOuts:     []string{"答案：3/4张纸", "答案：3/4张纸"},
				verifierOut:    "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: 3/4张纸",
				verifierStdout: "COMPUTED: 3/4张纸\n",
			}
			_, err := NewSolveSkill(exec.fn, nil).Execute(t.Context(), map[string]any{
				"problem": numberedPaperProblem + paperScope + paperTextbook, tc.key: tc.value,
			})
			if err != nil {
				t.Fatal(err)
			}
			if exec.solverCalls() != tc.calls || len(exec.specs) != tc.calls+1 || !exec.has(verifierAgentName) {
				t.Fatalf("explicit settings were overridden: %v", exec.agents())
			}
		})
	}
}

// 已保存的实际 stdout 必须一次转成程序验算证据，错误或缺失的单位仍不能视为等值。
func TestSolvePaperQuantityFromSavedExecution(t *testing.T) {
	raw, err := os.ReadFile("testdata/paper_verification.json")
	if err != nil {
		t.Fatal(err)
	}
	var saved []SubAgentResult
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 || saved[0].ExecutionReceipt == nil || saved[1].ExecutionReceipt == nil {
		t.Fatal("missing saved paper verification results")
	}
	for _, tc := range []struct {
		name, candidate, computed string
		index                     int
		verdict                   verifyVerdict
		grounded                  bool
	}{
		{"fraction", "3/4 张纸", "3/4 张纸", 0, verdictAgree, true},
		{"equivalent-fraction", "6/8 张纸", "3/4 张纸", 0, verdictAgree, true},
		{"integer", "3张纸", "3张纸", 1, verdictAgree, true},
		{"wrong-value", "4/3 张纸", "3/4 张纸", 0, verdictDisagree, true},
		{"wrong-unit", "3/4 千克", "3/4 张纸", 0, verdictAgree, false},
		{"missing-unit", "3/4", "3/4 张纸", 0, verdictAgree, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := saved[tc.index]
			exec := &solveExec{verifierOut: result.Output, verifierStdout: result.ExecutionReceipt.Stdout}
			verdict, computed, grounded := NewSolveSkill(exec.fn, nil).verifySolution(t.Context(), paperProblem, "每朵用纸乘以朵数。答案："+tc.candidate, tc.candidate, "")
			if verdict != tc.verdict || computed != tc.computed || grounded != tc.grounded || len(exec.specs) != 1 {
				t.Fatalf("verdict=%v computed=%q grounded=%v calls=%d", verdict, computed, grounded, len(exec.specs))
			}
		})
	}
	for _, answer := range []string{"3/4 张纸", "答案：3/4张纸", "3/8×2=3/4（张纸）"} {
		quantity, ok := parseAnswerQuantity(answer)
		if !ok || quantity.value != "0.75" || quantity.unit != "张纸" {
			t.Errorf("paper unit was lost: %q = %+v,%v", answer, quantity, ok)
		}
	}
	const task = "same frozen paper verification"
	for _, variant := range []string{"valid", "missing-receipt", "missing-run", "failed", "truncated", "wrong-input"} {
		t.Run(variant, func(t *testing.T) {
			receipt := *saved[0].ExecutionReceipt
			receipt.InputDigest = executionInputDigest(task)
			candidate := &receipt
			switch variant {
			case "missing-receipt":
				candidate = nil
			case "missing-run":
				receipt.RunID = ""
			case "failed":
				receipt.Status = "failed"
				receipt.ExitCode = 1
			case "truncated":
				receipt.StdoutTruncated = true
			case "wrong-input":
				receipt.InputDigest = executionInputDigest("another question")
			}
			computed, ok := candidate.computed(task)
			if variant == "valid" {
				if !ok || computed != "3/4 张纸" {
					t.Fatalf("saved valid execution rejected: %q,%v", computed, ok)
				}
			} else if ok {
				t.Fatal("invalid receipt became numeric evidence")
			}
		})
	}
}
