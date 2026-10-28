package engine

import "testing"

func TestReminderContentDoesNotReplaceDocumentToolsWithScheduler(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"learning document", "请根据以下材料生成数学学习小结PDF，包含概念、数量关系、易错提醒、两道小练习和答案，不发到手机。", false},
		{"reminder prose", "Write a reminder note about checking fraction units in this PDF.", false},
		{"direct reminder", "请提醒我买牛奶", true},
		{"timed reminder", "明天上午八点提醒大家开会", true},
		{"manage reminder", "取消我的提醒", true},
		{"english reminder", "Remind me tomorrow to check the report", true},
		{"scheduled teaching tips", "每天八点把数学易错提醒发给我", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := detectCronIntent(tc.text)
			if got != tc.want {
				t.Fatalf("scheduled intent=%v, want %v", got, tc.want)
			}
		})
	}
}
