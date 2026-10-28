package engine

import (
	"strings"
	"testing"
)

const processAnswerProblem = "一盒卡片的1/3有6张。这盒卡片原来有多少张？请写出数量关系，列方程解答，并代回验算。"
const processAnswerCandidate = "数量关系：原来的张数 ÷ 3 ＝ 用去的张数（或用去的张数 × 3 ＝ 原来的张数）；设原来有 x 张，列方程 x ÷ 3 ＝ 6，解得 x ＝ 18；代回验算：18 ÷ 3 ＝ 6（张），符合题意；这盒卡片原来有18张。"

func TestProcessAnswerUsesIndependentNumericExecutionWithoutTruncatingContent(t *testing.T) {
	for _, tc := range []struct {
		name, process, computed string
		verdict                 verifyVerdict
	}{
		{"complete", "VALID", "18张", verdictAgree},
		{"wrong final value", "VALID", "19张", verdictDisagree},
		{"invalid process", "INVALID", "18张", verdictDisagree},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := &solveExec{verifierOut: "VERDICT: AGREE\nPROCESS: " + tc.process + "\nCOMPUTED: " + tc.computed, verifierStdout: "COMPUTED: " + tc.computed + "\n"}
			verdict, got, grounded := NewSolveSkill(exec.fn, nil).verifySolution(t.Context(), processAnswerProblem, "完整推导和验算。\n答案："+processAnswerCandidate, processAnswerCandidate, "")
			if verdict != tc.verdict || got != tc.computed || !grounded || len(exec.specs) != 1 {
				t.Fatalf("actual numeric execution lost: verdict=%v computed=%q grounded=%v calls=%d", verdict, got, grounded, len(exec.specs))
			}
			if !strings.Contains(exec.specs[0].Task, processAnswerCandidate) {
				t.Fatal("full candidate was removed from the independent process audit")
			}
		})
	}
}

func TestProcessAnswerProjectionPreservesOtherTargetsAndRequiredSteps(t *testing.T) {
	for _, tc := range []struct{ name, problem, answer string }{
		{"second question", processAnswerProblem + "还剩多少张？", processAnswerCandidate},
		{"additional target", processAnswerProblem + "并求每张2元时的总价。", processAnswerCandidate},
		{"additional calculation", processAnswerProblem + "计算每张2元时的总价。", processAnswerCandidate},
		{"separate objects", strings.Replace(processAnswerProblem, "这盒卡片原来有多少张", "两盒卡片分别有多少张", 1), processAnswerCandidate},
		{"blank", processAnswerProblem + "关系中填（）。", processAnswerCandidate},
		{"two blanks", processAnswerProblem + "依次填（）、（）。", processAnswerCandidate},
		{"wrong unit", processAnswerProblem, strings.Replace(processAnswerCandidate, "这盒卡片原来有18张", "这盒卡片原来有18个", 1)},
		{"other target", processAnswerProblem, strings.Replace(processAnswerCandidate, "这盒卡片原来有18张", "剩余的卡片有18张", 1)},
		{"negative premise", processAnswerProblem, strings.Replace(processAnswerCandidate, "这盒卡片原来有18张", "这盒卡片原来没有18张", 1)},
		{"conditional conclusion", processAnswerProblem, strings.Replace(processAnswerCandidate, "这盒卡片原来有18张", "如果这盒卡片原来有18张", 1)},
		{"missing relation", processAnswerProblem, strings.Join(strings.Split(processAnswerCandidate, "；")[1:], "；")},
		{"missing equation", processAnswerProblem, strings.Replace(processAnswerCandidate, "设原来有 x 张，列方程 x ÷ 3 ＝ 6，解得 x ＝ 18；", "", 1)},
		{"missing check", processAnswerProblem, strings.Replace(processAnswerCandidate, "代回验算：18 ÷ 3 ＝ 6（张），符合题意；", "", 1)},
		{"conflicting conclusions", processAnswerProblem, strings.Replace(processAnswerCandidate, "这盒卡片原来有18张", "这盒卡片原来有19张；这盒卡片原来有18张", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if equal, comparable := verificationAnswersEqual(tc.problem, "18张", tc.answer); equal && comparable {
				t.Fatal("partial or mismatched compound answer became verified by its last number")
			}
		})
	}
}

func TestProcessAnswerStillRequiresExecutionAndCompleteProcessAudit(t *testing.T) {
	for _, tc := range []struct{ name, process, stdout string }{
		{"missing receipt", "VALID", ""},
		{"missing audit", "NOT_PROVIDED", "COMPUTED: 18张\n"},
		{"missing process field", "", "COMPUTED: 18张\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := "VERDICT: AGREE\nCOMPUTED: 18张"
			if tc.process != "" {
				out += "\nPROCESS: " + tc.process
			}
			exec := &solveExec{verifierOut: out, verifierStdout: tc.stdout}
			_, _, grounded := NewSolveSkill(exec.fn, nil).verifySolution(t.Context(), processAnswerProblem, "完整推导。\n答案："+processAnswerCandidate, processAnswerCandidate, "")
			if grounded {
				t.Fatal("missing execution or audit became numeric evidence")
			}
		})
	}
}

func TestProcessAnswerProjectionUsesQuestionTargetRatherThanFixtureEntity(t *testing.T) {
	problem := "一袋铅笔的1/4有6支。这袋铅笔原来有多少支？请写数量关系、列方程并验算。"
	answer := "数量关系：原来支数 ÷ 4 ＝ 6；列方程：x ÷ 4 ＝ 6，解得 x ＝ 24；验算：24 ÷ 4 ＝ 6；这袋铅笔原来有24支。"
	if equal, comparable := verificationAnswersEqual(problem, "24支", answer); !equal || !comparable {
		t.Fatal("another single-target process answer did not preserve numeric comparison")
	}
}
