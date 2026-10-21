package engine

import "testing"

const reciprocalPairingProblem = `将每组中互为倒数的两个数用线连起来。
**第一组**
| 左列 | 右列 |
|---|---|
| 5/12 | 9/4 |
| 7 | 12/5 |
| 4/9 | 1/7 |
**第二组**
| 左列 | 右列 |
|---|---|
| 17/18 | 1/50 |
| 50 | 31/23 |
| 23/31 | 18/17 |`

func TestVerificationCompletePairing(t *testing.T) {
	computed := "第一组：5/12 ↔ 12/5，7 ↔ 1/7，4/9 ↔ 9/4；第二组：17/18 ↔ 18/17，50 ↔ 1/50，23/31 ↔ 31/23"
	for _, tc := range []struct {
		name, answer      string
		equal, comparable bool
	}{
		{"complete", "**" + computed + "。**", true, true},
		{"reordered pairs", "第二组：50 ↔ 1/50，17/18 ↔ 18/17，23/31 ↔ 31/23；第一组：7 ↔ 1/7，5/12 ↔ 12/5，4/9 ↔ 9/4", true, true},
		{"equivalent values", "第一组：10/24 ↔ 24/10，7 ↔ 2/14，8/18 ↔ 18/8；第二组：34/36 ↔ 36/34，50 ↔ 2/100，46/62 ↔ 62/46", true, true},
		{"wrong pairing", "第一组：5/12 ↔ 9/4，7 ↔ 1/7，4/9 ↔ 12/5；第二组：17/18 ↔ 18/17，50 ↔ 1/50，23/31 ↔ 31/23", false, true},
		{"missing group", "第一组：5/12 ↔ 12/5，7 ↔ 1/7，4/9 ↔ 9/4", false, false},
		{"missing pair", "第一组：5/12 ↔ 12/5，7 ↔ 1/7；第二组：17/18 ↔ 18/17，50 ↔ 1/50，23/31 ↔ 31/23", false, false},
		{"duplicate left", "第一组：5/12 ↔ 12/5，5/12 ↔ 1/7，4/9 ↔ 9/4；第二组：17/18 ↔ 18/17，50 ↔ 1/50，23/31 ↔ 31/23", false, false},
		{"unrelated left", "第一组：5/13 ↔ 12/5，7 ↔ 1/7，4/9 ↔ 9/4；第二组：17/18 ↔ 18/17，50 ↔ 1/50，23/31 ↔ 31/23", false, false},
		{"extra conclusion", computed + "，还有1 ↔ 1", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			equal, comparable := verificationAnswersEqual(reciprocalPairingProblem, computed, tc.answer)
			if equal != tc.equal || comparable != tc.comparable {
				t.Fatalf("equal=%v comparable=%v want %v/%v", equal, comparable, tc.equal, tc.comparable)
			}
		})
	}
}
