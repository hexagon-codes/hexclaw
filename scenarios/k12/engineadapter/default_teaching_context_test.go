package engineadapter

import (
	"reflect"
	"testing"
)

func TestDefaultTeachingContextUsesTaskAndPreservesCustomSkill(t *testing.T) {
	configured := []string{"custom-family-method", "grade-constraint", "k12-pedagogy", "math-tutor", "chinese-tutor", "english-tutor", "science-tutor", "information-technology-tutor", "art-feedback", "reading-comprehension", "writing-feedback"}
	base := []string{"custom-family-method", "grade-constraint", "k12-pedagogy"}
	for _, tc := range []struct {
		text string
		want []string
	}{
		{"请用数学方法解释这道分数题", append(append([]string{}, base...), "math-tutor")},
		{"请辅导语文阅读理解", append(append([]string{}, base...), "chinese-tutor", "reading-comprehension")},
		{"请讲解信息科技的这个程序", append(append([]string{}, base...), "information-technology-tutor")},
		{"今天孩子很沮丧", base},
		{"请安排数学和英语辅导", base},
		{"你是谁，官网是什么", []string{"custom-family-method"}},
		{"导出这份PDF", []string{"custom-family-method"}},
	} {
		got, handled := SelectDefaultTeachingContext(tc.text, configured)
		if !handled || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("query=%q got=%v handled=%t want=%v", tc.text, got, handled, tc.want)
		}
	}
	if _, handled := SelectDefaultTeachingContext("数学题", []string{"custom-method"}); handled {
		t.Fatal("non-K12 defaults were changed")
	}
}
