package engine

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const paperProblem = "环保小组用回收的包装纸做纸花，每一朵花需要用 (3)/(8) 张纸。\n\n做 2 朵花需要用多少张纸？"
const numberedPaperProblem = "环保小组用回收的包装纸做纸花，做一朵花需要用 (3)/(8) 张纸。\n\n（1）做 2 朵花需要用多少张纸？"
const paperScope = "\n\nFor this request, solve or assess only subproblem （1）. Use the shared material as context; do not answer the other subproblems."
const paperTextbook = "\n\nVerified textbook evidence (use it only to constrain the solution and grading; it is not the student's answer; do not expose internal source identifiers). Respond in Chinese:\n# 分数乘法\n（1）做2朵花需要用多少张纸？\n（2）做8朵花需要用多少张纸？\n讨论一下：分数乘整数，怎样计算？\n分数乘整数，用分子乘整数的积作分子，分母不变。能约分的可先约分，再计算。"

// 附加资料留给模型使用，但不能把一道普通题变成需要多种解法的难题。
func TestSolveComplexityUsesCurrentProblem(t *testing.T) {
	for _, tc := range []struct{ name, problem, answer string }{
		{"scope-and-textbook", paperProblem + paperScope + paperTextbook, "3/4张纸"},
		{"textbook", paperProblem + paperTextbook, "3/4张纸"},
		{"frozen-reference", paperProblem + paperScope + "\n\nFrozen practice reference (verified):\n讨论不同解法，（1）参考第一问；（2）参考第二问。", "3/4张纸"},
		{"selected-numbered-first", numberedPaperProblem + "\n\nFor this request, solve or assess only subproblem 1. Use the shared material as context; do not answer the other subproblems." + paperTextbook, "3/4张纸"},
		{"selected-numbered-second", "环保小组用回收的包装纸做纸花，做一朵花需要用 (3)/(8) 张纸。\n\n（2）做 8 朵花需要用多少张纸？\n\nFor this request, solve or assess only subproblem 2. Use the shared material as context; do not answer the other subproblems." + paperTextbook, "3张纸"},
		{"selected-expression-denominator-second", "环保小组用回收的包装纸做纸花，做一朵花需要用 (3)/(8) 张纸。\n\n（2）做8朵花需要用多少张纸？\n\n(3)/(8)×8=(3×8)/(8)=3（印刷约分标记：分子中的8与分母8均约为1）。\n\nFor this request, solve or assess only subproblem 2. Use the shared material as context; do not answer the other subproblems." + paperTextbook, "3张纸"},
		{"selected-expression-denominator-first", numberedPaperProblem + "\n(3+3)/(8)=(3×2)/(8)=3/4" + paperScope + paperTextbook, "3/4张纸"},
		{"selected-bare-numerator-denominator", numberedPaperProblem + "\n3/(8)×2=3/4" + paperScope + paperTextbook, "3/4张纸"},
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
			if exec.solverCalls() != 1 || len(exec.specs) != 2 || !exec.has(verifierAgentName) {
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
}

func TestSolveComplexityPreservesHardQuestionsAndExplicitSettings(t *testing.T) {
	for _, problem := range []string{
		"每朵花用3/8张纸。（1）做2朵花需要多少张纸？（2）做8朵花需要多少张纸？",
		"每朵花用3/8张纸。\n（1）做2朵花需要多少张纸？\n（2）做8朵花需要多少张纸？",
		"每朵花用3/8张纸。\n（1）计算 (3×2)/(8)。\n（2）计算 (3×8)/(8)。",
		"每朵花用3/8张纸。\n（1）做2朵花需要多少张纸？\n(8) 做8朵花需要多少张纸？",
		"每朵花用3/8张纸。\n（2）做2朵花需要多少张纸？",
		"每朵花用3/8张纸。（1）做2朵花需要多少张纸？",
		"（1）讨论一下：分数乘整数，怎样计算？",
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
