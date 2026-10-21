package engine

import "testing"

func TestVerificationDivisionFamilies(t *testing.T) {
	problem := "根据每道乘法算式，分别写出两道除法算式。\n（1）5/6 × 7 = 35/6\n（　）÷（　）=（　）\n（　）÷（　）=（　）\n（2）4/9 × 3/7 = 4/21\n（　）÷（　）=（　）\n（　）÷（　）=（　）"
	computed := "（1）35/6 ÷ 5/6 = 7；35/6 ÷ 7 = 5/6。（2）4/21 ÷ 4/9 = 3/7；4/21 ÷ 3/7 = 4/9。（各小题两式顺序可交换）"
	for _, tc := range []struct {
		name, answer      string
		equal, comparable bool
	}{
		{"complete", computed, true, true},
		{"either row order", "（1）若先除以5/6：第一行35/6 ÷ 5/6 = 7，第二行35/6 ÷ 7 = 5/6；若先除以7：第一行35/6 ÷ 7 = 5/6，第二行35/6 ÷ 5/6 = 7。（2）若先除以4/9：第一行4/21 ÷ 4/9 = 3/7，第二行4/21 ÷ 3/7 = 4/9；若先除以3/7：第一行4/21 ÷ 3/7 = 4/9，第二行4/21 ÷ 4/9 = 3/7。", true, true},
		{"wrong quotient", "（1）35/6 ÷ 5/6 = 8；35/6 ÷ 7 = 5/6。（2）4/21 ÷ 4/9 = 3/7；4/21 ÷ 3/7 = 4/9。", false, true},
		{"duplicate relation", "（1）35/6 ÷ 5/6 = 7；35/6 ÷ 5/6 = 7。（2）4/21 ÷ 4/9 = 3/7；4/21 ÷ 3/7 = 4/9。", false, true},
		{"missing group", "（1）35/6 ÷ 5/6 = 7；35/6 ÷ 7 = 5/6。", false, false},
		{"missing equation", "（1）35/6 ÷ 5/6 = 7。（2）4/21 ÷ 4/9 = 3/7；4/21 ÷ 3/7 = 4/9。", false, false},
		{"extra equation", computed + "1+1=3", false, false},
		{"extra incomplete division", computed + "35/6 ÷", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			equal, comparable := verificationAnswersEqual(problem, computed, tc.answer)
			if equal != tc.equal || comparable != tc.comparable {
				t.Fatalf("equal=%v comparable=%v want %v/%v", equal, comparable, tc.equal, tc.comparable)
			}
		})
	}
}
