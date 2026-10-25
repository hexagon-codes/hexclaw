package engineadapter

import "strings"

var managedTeachingDefaults = map[string]bool{
	"grade-constraint": true, "k12-pedagogy": true, "math-tutor": true,
	"chinese-tutor": true, "english-tutor": true, "science-tutor": true,
	"information-technology-tutor": true, "art-feedback": true, "homework-checker": true,
	"exercise-generator": true, "concept-explainer": true, "writing-feedback": true,
	"reading-comprehension": true, "classical-chinese": true, "english-vocab-coach": true,
	"quiz-generator": true,
}

// SelectDefaultTeachingContext 只处理默认挂有教学基座的Agent，不新增模型调用或改显式挂载。
func SelectDefaultTeachingContext(text string, configured []string) ([]string, bool) {
	managed := false
	for _, name := range configured {
		managed = managed || name == "k12-pedagogy"
	}
	if !managed {
		return nil, false
	}
	has := func(words ...string) bool {
		for _, word := range words {
			if strings.Contains(text, word) {
				return true
			}
		}
		return false
	}
	wanted := map[string]bool{"k12-pedagogy": true, "grade-constraint": true}
	if has("你是谁", "介绍你自己", "官网", "GitHub", "github", "导出", "保存PDF", "打印PDF", "发送到手机") &&
		!has("讲题", "解题", "批改", "辅导", "讲解", "为什么", "怎么教") {
		wanted = map[string]bool{}
	} else if has("作业", "题", "辅导", "孩子", "练习", "知识", "讲解", "批改", "作文", "阅读", "写作", "实验", "程序", "数学", "语文", "英语", "科学", "信息科技", "美术") {
		wanted["k12-pedagogy"], wanted["grade-constraint"] = true, true
		var subjects []string
		for _, subject := range []struct{ label, name string }{{"数学", "math-tutor"}, {"语文", "chinese-tutor"}, {"英语", "english-tutor"}, {"科学", "science-tutor"}, {"信息科技", "information-technology-tutor"}, {"美术", "art-feedback"}} {
			if has(subject.label) {
				subjects = append(subjects, subject.name)
			}
		}
		// 明确多科或无学科证据时使用基座，不把一个关键词猜测写成学生档案事实。
		if len(subjects) == 1 {
			wanted[subjects[0]] = true
		}
		if has("批改", "检查作业") {
			wanted["homework-checker"] = true
		}
		if has("讲概念", "什么是", "为什么") {
			wanted["concept-explainer"] = true
		}
		if has("出变式", "出题", "生成练习", "生成题", "设计练习") {
			wanted["exercise-generator"] = true
		}
		if has("作文", "写作") && !has("英文") && (len(subjects) == 0 || len(subjects) == 1 && subjects[0] == "chinese-tutor") {
			wanted["writing-feedback"] = true
		}
		if len(subjects) == 1 && subjects[0] == "chinese-tutor" {
			wanted["reading-comprehension"] = has("阅读")
			wanted["classical-chinese"] = has("文言")
		}
		if len(subjects) == 1 && subjects[0] == "english-tutor" {
			wanted["english-vocab-coach"] = has("单词", "词汇")
		}
	}
	var selected []string
	for _, name := range configured {
		if !managedTeachingDefaults[name] || wanted[name] {
			selected = append(selected, name)
		}
	}
	return selected, true
}
