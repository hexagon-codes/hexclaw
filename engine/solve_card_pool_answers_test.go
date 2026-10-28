package engine

import (
	"strings"
	"testing"
)

const reciprocalCardPoolProblem = `数学课上，同桌两人玩“倒数配对”游戏。桌上有六张数字卡片：

2/9、9/2、4、1/4、1、0。

每次选出两张卡片，只有它们上面的数互为倒数，才能配成一组。

这些卡片能配成哪几组？剩下的卡片中，哪张再添一张相同的卡片就能配对？哪张无论添什么数字卡片都不能配对？说明理由。`

const reciprocalCardPoolComputed = "配对：2/9和9/2；4和1/4；剩下：1、0；添相同卡片可配对：1；无论添什么都不能配对：0"
const reciprocalCardPoolCandidateA = "能配成两组：2/9和9/2，4和1/4，因为2/9 × 9/2 = 1，4 × 1/4 = 1；剩下1和0，其中1再添一张1就能配对，因为1 × 1 = 1；0无论添什么数字卡片都不能配对，因为0与任何数相乘都得0，不等于1。"
const reciprocalCardPoolCandidateB = "原有卡片能配成两组：2/9与9/2，4与1/4，因为2/9 × 9/2＝1，4 × 1/4＝1；剩下1和0，其中1再添一张1就能配对，因为1 × 1＝1；0无论添什么数字卡片都不能配对，因为0乘任何数都等于0，不等于1。"
const reciprocalCardPoolVerifierText = "能配成两组：2/9和9/2，4和1/4；剩下1和0，1再添一张1能配对，0无论添什么数字卡片都不能配对。"

func TestVerificationReciprocalCardPoolPreservesCompleteRelations(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		equal        bool
	}{
		{"original candidate A", reciprocalCardPoolCandidateA, true},
		{"original candidate B", reciprocalCardPoolCandidateB, true},
		{"unordered exact fractions", "配对：0.25和4；9/2与4/18；剩下：0和1；添相同卡片可配对：1；无论添什么都不能配对：0", true},
		{"crossed pairs", "配对：2/9和4；9/2和1/4；剩下：1、0；添相同卡片可配对：1；无论添什么都不能配对：0", false},
		{"missing pair", "配对：2/9和9/2；剩下：4、1/4、1、0；添相同卡片可配对：1；无论添什么都不能配对：0", false},
		{"reuse one physical card", "配对：2/9和9/2；4和1/4；1和1；剩下：0；添相同卡片可配对：无；无论添什么都不能配对：0", false},
		{"missing remaining", strings.Replace(reciprocalCardPoolComputed, "；剩下：1、0", "", 1), false},
		{"remaining misses zero", strings.Replace(reciprocalCardPoolComputed, "剩下：1、0", "剩下：1", 1), false},
		{"remaining repeats one", strings.Replace(reciprocalCardPoolComputed, "剩下：1、0", "剩下：1、1", 1), false},
		{"remaining includes used card", strings.Replace(reciprocalCardPoolComputed, "剩下：1、0", "剩下：4、1、0", 1), false},
		{"missing same card", strings.Replace(reciprocalCardPoolComputed, "；添相同卡片可配对：1", "", 1), false},
		{"wrong same card", strings.Replace(reciprocalCardPoolComputed, "添相同卡片可配对：1", "添相同卡片可配对：0", 1), false},
		{"missing zero rule", strings.Replace(reciprocalCardPoolComputed, "；无论添什么都不能配对：0", "", 1), false},
		{"wrong zero rule", strings.Replace(reciprocalCardPoolComputed, "无论添什么都不能配对：0", "无论添什么都不能配对：1", 1), false},
		{"source external value", strings.Replace(reciprocalCardPoolComputed, "2/9和9/2", "2/9和8", 1), false},
		{"duplicate pair", strings.Replace(reciprocalCardPoolComputed, "4和1/4", "2/9和9/2；4和1/4", 1), false},
		{"conflicting final conclusion", reciprocalCardPoolComputed + "；0也能配对", false},
		{"conflicting reason", strings.Replace(reciprocalCardPoolCandidateA, "因为1 × 1 = 1", "因为1 × 1 = 1，0也能配对", 1), false},
		{"incorrect product", strings.Replace(reciprocalCardPoolCandidateA, "2/9 × 9/2 = 1", "2/9 × 9/2 = 2", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			equal, comparable := verificationAnswersEqual(reciprocalCardPoolProblem, reciprocalCardPoolComputed, tc.answer)
			if equal != tc.equal || tc.equal && !comparable {
				t.Fatalf("complete relation comparison: equal=%v comparable=%v want equal=%v", equal, comparable, tc.equal)
			}
		})
	}
}

func TestReciprocalCardPoolRequiresSourceAndIndependentProcess(t *testing.T) {
	for _, tc := range []struct {
		name, process, stdout string
		verdict               verifyVerdict
		grounded              bool
	}{
		{"complete", "VALID", "COMPUTED: " + reciprocalCardPoolComputed + "\n", verdictAgree, true},
		{"invalid process", "INVALID", "COMPUTED: " + reciprocalCardPoolComputed + "\n", verdictDisagree, true},
		{"missing audit", "NOT_PROVIDED", "COMPUTED: " + reciprocalCardPoolComputed + "\n", verdictAgree, false},
		{"missing process field", "", "COMPUTED: " + reciprocalCardPoolComputed + "\n", verdictAgree, false},
		{"missing receipt", "VALID", "", verdictAgree, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := &solveExec{verifierOut: "VERDICT: AGREE\nPROCESS: " + tc.process + "\nCOMPUTED: " + reciprocalCardPoolVerifierText, verifierStdout: tc.stdout}
			verdict, _, grounded := NewSolveSkill(exec.fn, nil).verifySolution(t.Context(), reciprocalCardPoolProblem, "完整配对、补卡及零乘法推导。\n答案："+reciprocalCardPoolCandidateA, reciprocalCardPoolCandidateA, "")
			if verdict != tc.verdict || grounded != tc.grounded {
				t.Fatalf("execution/process boundary: verdict=%v grounded=%v", verdict, grounded)
			}
			if !strings.Contains(exec.specs[0].Task, reciprocalCardPoolCandidateA) {
				t.Fatal("independent audit lost a compound answer target")
			}
		})
	}
	for _, problem := range []string{
		reciprocalCardPoolProblem + "再求所有卡片上的数的和。",
		strings.Replace(reciprocalCardPoolProblem, "六张", "七张", 1),
		strings.Replace(reciprocalCardPoolProblem, "1、0", "1、1", 1),
	} {
		if equal, comparable := verificationAnswersEqual(problem, reciprocalCardPoolComputed, reciprocalCardPoolCandidateA); equal && comparable {
			t.Fatal("incomplete source or additional target became a complete proof")
		}
	}
	otherProblem := strings.Replace(reciprocalCardPoolProblem, "2/9、9/2、4、1/4", "3/8、8/3、6、1/6", 1)
	otherAnswer := "配对：3/8和8/3；6和1/6；剩下：1、0；添相同卡片可配对：1；无论添什么都不能配对：0"
	if equal, comparable := verificationAnswersEqual(otherProblem, otherAnswer, otherAnswer); !equal || !comparable {
		t.Fatal("card identities were not derived from the original source")
	}
}

func TestReciprocalCardPoolDisplayUsesVerifiedCompleteRelation(t *testing.T) {
	first := solverSolution{output: "完整方案甲。\n答案：" + reciprocalCardPoolCandidateA, answer: reciprocalCardPoolCandidateA}
	second := solverSolution{output: "完整方案乙。\n答案：" + reciprocalCardPoolCandidateB, answer: reciprocalCardPoolCandidateB}
	groups := groupByAnswer([]solverSolution{first, second})
	if actual := formatSolve(groups, verdictAgree, reciprocalCardPoolComputed, 2, true, true, reciprocalCardPoolProblem); actual != first.output {
		t.Fatal("equivalent complete targets produced a false disagreement warning")
	}
	invalid := formatSolve(groups, verdictDisagree, reciprocalCardPoolComputed, 2, true, true, reciprocalCardPoolProblem)
	if !strings.HasPrefix(invalid, first.output) || !strings.Contains(invalid, "过程未通过核验") {
		t.Fatal("equivalent candidates suppressed the independent process rejection")
	}
	if selected := selectedSolverSolution(groups, verdictDisagree, reciprocalCardPoolComputed, true, reciprocalCardPoolProblem); selected.output != first.output {
		t.Fatal("process rejection changed the original candidate identity")
	}
	wrong := solverSolution{output: "错误配对。", answer: "配对：2/9和4；9/2和1/4；剩下：1、0；添相同卡片可配对：1；无论添什么都不能配对：0"}
	groups = groupByAnswer([]solverSolution{wrong, second})
	if actual := formatSolve(groups, verdictAgree, reciprocalCardPoolComputed, 2, true, true, reciprocalCardPoolProblem); actual != second.output {
		t.Fatal("formatted output did not select the independently verified candidate")
	}
	if selected := selectedSolverSolution(groups, verdictAgree, reciprocalCardPoolComputed, true, reciprocalCardPoolProblem); selected.output != second.output {
		t.Fatal("source identity differs from the displayed verified candidate")
	}
}
