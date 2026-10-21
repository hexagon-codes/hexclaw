package engine

import "testing"

const orderedReciprocalProblem = "在括号里填上合适的数，使每组中的两个数互为倒数。\n- 4/11 和（　　）\n- 9 和（　　）\n- （　　）和 5/8\n- 1 和（　　）\n小明说：0的倒数也是0。他的说法对吗？请简要说明理由。"

func TestVerificationOrderedValuesAndJudgment(t *testing.T) {
	computed := "11/4，1/9，8/5，1；小明的说法不对，0没有倒数，因为0与任何数相乘都不等于1"
	for _, tc := range []struct {
		name, candidate   string
		equal, comparable bool
	}{
		{"complete", "**依次填：11/4、1/9、8/5、1。小明的说法不对，0没有倒数。**", true, true},
		{"equivalent fraction", "22/8，2/18，16/10，1；小明的说法错误，因为乘积不为1", true, true},
		{"swapped positions", "1/9，11/4，8/5，1；小明的说法不对", false, true},
		{"wrong value", "11/4，1/9，5/8，1；小明的说法不对", false, true},
		{"wrong judgment", "11/4，1/9，8/5，1；小明的说法正确", false, true},
		{"missing value", "11/4，1/9，8/5；小明的说法不对", false, false},
		{"missing judgment", "11/4，1/9，8/5，1", false, false},
		{"different subject", "11/4，1/9，8/5，1；小红的说法不对", false, false},
		{"extra judgment", "11/4，1/9，8/5，1；小明的说法不对，小红的说法正确", false, false},
		{"incomplete prefix", "解答中出现11/4，1/9，8/5，1；小明的说法不对", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			equal, comparable := verificationAnswersEqual(orderedReciprocalProblem, computed, tc.candidate)
			if equal != tc.equal || comparable != tc.comparable {
				t.Fatalf("equal=%v comparable=%v want %v/%v", equal, comparable, tc.equal, tc.comparable)
			}
		})
	}
}

func TestVerificationOrderedValuesStillRequireExecutionAndProcess(t *testing.T) {
	answer := "11/4，1/9，8/5，1；小明的说法不对"
	input := &verificationInput{Problem: orderedReciprocalProblem, Solution: "按倒数定义逐项检查。", Candidate: answer}
	valid, invalid := true, false
	program := verificationProgram{Scope: "IN_SCOPE", Checks: []verificationStep{{Step: "逐项代回", Valid: &valid, Reason: "各乘积均为1，零不可能乘出1"}}, Note: "已核对"}
	task := "ordered-values-verification"
	missing := verificationProgramJudgment(input, program, nil, task, "program response")
	if verdict, _ := parseVerdict(missing); verdict != verdictUnverifiable {
		t.Fatal("missing execution became verified")
	}
	receipt := standaloneVerifierTestReceipt(task, "COMPUTED: "+answer+"\n")
	program.Checks[0].Valid = &invalid
	judgment := verificationProgramJudgment(input, program, receipt, task, "program response")
	if verdict, _ := parseVerdict(judgment); verdict != verdictDisagree {
		t.Fatal("correct values overrode invalid reasoning")
	}
}

func TestVerificationOrderedBlankValues(t *testing.T) {
	problem := "在括号里填上合适的数：1/3和（　）；7和（　）；（　）和4/5。"
	if equal, comparable := verificationAnswersEqual(problem, "3，1/7，5/4", "依次填：3、1/7、5/4。"); !equal || !comparable {
		t.Fatal("complete ordered blanks rejected")
	}
	if equal, comparable := verificationAnswersEqual(problem, "3，1/7，5/4", "3，5/4，1/7"); equal || !comparable {
		t.Fatal("different blank positions lost")
	}
	if _, comparable := verificationAnswersEqual(problem, "3，1/7，5/4", "3，1/7"); comparable {
		t.Fatal("partial blanks accepted")
	}
}

func TestVerificationNumericalResultJudgment(t *testing.T) {
	problem := "图书角新添故事书和科技书共64本，科技书的本数是故事书的3/5。小华算出故事书有40本，科技书有24本。请根据题中的两个条件，判断他的结果是否正确，并简要说明理由。"
	computed := "故事书40本，科技书24本；小华的结果正确"
	for _, tc := range []struct {
		answer            string
		equal, comparable bool
	}{
		{"**小华的结果正确。**", true, true},
		{"故事书有40本，科技书有24本；小华的结果正确", true, true},
		{"小华的结果错误", false, true},
		{"小明的结果正确", false, false},
		{"正确", false, false},
		{"故事书41本，科技书23本；小华的结果正确", false, false},
		{"小华的结果正确；小明的结果错误", false, false},
	} {
		equal, comparable := verificationAnswersEqual(problem, computed, tc.answer)
		if comparable != tc.comparable || (comparable && equal != tc.equal) {
			t.Fatalf("%q: equal=%v comparable=%v want %v/%v", tc.answer, equal, comparable, tc.equal, tc.comparable)
		}
	}
	if _, _, scoped := resultJudgmentVerificationAnswersEqual("小华算出故事书有40本，科技书有24本。两种书各有多少本？", computed, "小华的结果正确"); scoped {
		t.Fatal("quantity question became judgment-only")
	}
}
